// Copyright (c) 2018 Palantir Technologies. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/palantir/pkg/httpserver"
	"github.com/palantir/pkg/metrics"
	"github.com/palantir/witchcraft-go-logging/conjure/witchcraft/api/logging"
	"github.com/palantir/witchcraft-go-server/config"
	"github.com/palantir/witchcraft-go-server/witchcraft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitMetrics verifies that metrics are printed periodically by a Witchcraft server and that the emitted values
// respect the default blacklist. We verify both custom metrics set in the InitFunc (with tags) and server.response
// metrics from the metrics middleware.
func TestEmitMetrics(t *testing.T) {
	logOutputBuffer := &bytes.Buffer{}
	port, err := httpserver.AvailablePort()
	require.NoError(t, err)

	// ensure that registry used in this test is unique/does not have any past metrics registered on it
	metrics.DefaultMetricsRegistry = metrics.NewRootMetricsRegistry()
	server, serverErr, cleanup := createAndRunCustomTestServer(t, port, port, func(ctx context.Context, info witchcraft.InitInfo) (deferFn func(), rErr error) {
		ctx = metrics.AddTags(ctx, metrics.MustNewTag("key", "val"))
		metrics.FromContext(ctx).Counter("my-counter").Inc(13)
		return nil, info.Router.Post("/error", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(500)
		}))
	}, logOutputBuffer, func(t *testing.T, initFn witchcraft.InitFunc, installCfg config.Install, logOutputBuffer io.Writer) *witchcraft.Server {
		installCfg.MetricsEmitFrequency = 100 * time.Millisecond
		return createTestServer(t, initFn, installCfg, logOutputBuffer)
	})
	defer func() {
		require.NoError(t, server.Close())
	}()
	defer cleanup()

	// Make POST that will 404 to trigger request size and error rate metrics
	_, err = testServerClient().Post(fmt.Sprintf("https://localhost:%d/%s/%s", port, basePath, "error"), "application/json", strings.NewReader("{}"))
	require.NoError(t, err)

	// Allow the metric emitter to do its thing.
	time.Sleep(150 * time.Millisecond)

	parts := strings.Split(logOutputBuffer.String(), "\n")
	var metricLogs []logging.MetricLogV1
	for _, curr := range parts {
		if strings.Contains(curr, `"metric.1"`) {
			var currLog logging.MetricLogV1
			require.NoError(t, json.Unmarshal([]byte(curr), &currLog))
			metricLogs = append(metricLogs, currLog)
		}
	}

	var seenMyCounter, seenResponseTimer, seenResponseSize, seenRequestSize, seenResponseError bool
	for _, metricLog := range metricLogs {
		switch metricLog.MetricName {
		case "my-counter":
			seenMyCounter = true
			assert.Equal(t, "counter", metricLog.MetricType, "my-counter metric had incorrect type")
			assert.Equal(t, map[string]interface{}{"count": json.Number("13")}, metricLog.Values)
			assert.Equal(t, map[string]string{"key": "val"}, metricLog.Tags)
		case "server.response":
			seenResponseTimer = true
			assert.Equal(t, "timer", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotNil(t, metricLog.Values["count"])
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["p95"])
			assert.NotNil(t, metricLog.Values["p99"])

			// keys are part of the default blacklist and should thus be nil
			assert.Nil(t, metricLog.Values["1m"])
			assert.Nil(t, metricLog.Values["5m"])
			assert.Nil(t, metricLog.Values["15m"])
			assert.Nil(t, metricLog.Values["meanRate"])
			assert.Nil(t, metricLog.Values["min"])
			assert.Nil(t, metricLog.Values["mean"])
			assert.Nil(t, metricLog.Values["stddev"])
			assert.Nil(t, metricLog.Values["p50"])
		case "server.request.size":
			seenRequestSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["p95"])
			assert.NotNil(t, metricLog.Values["p99"])
			assert.NotNil(t, metricLog.Values["count"])

			// keys are part of the default blacklist and should thus be nil
			assert.Nil(t, metricLog.Values["min"])
			assert.Nil(t, metricLog.Values["mean"])
			assert.Nil(t, metricLog.Values["stddev"])
			assert.Nil(t, metricLog.Values["p50"])
		case "server.response.size":
			seenResponseSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["p95"])
			assert.NotNil(t, metricLog.Values["p99"])
			assert.NotNil(t, metricLog.Values["count"])

			// keys are part of the default blacklist and should thus be nil
			assert.Nil(t, metricLog.Values["min"])
			assert.Nil(t, metricLog.Values["mean"])
			assert.Nil(t, metricLog.Values["stddev"])
			assert.Nil(t, metricLog.Values["p50"])
		case "server.response.error":
			seenResponseError = true
			assert.Equal(t, "meter", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotNil(t, metricLog.Values["count"])

			// keys are part of the default blacklist and should thus be nil
			assert.Nil(t, metricLog.Values["1m"])
			assert.Nil(t, metricLog.Values["5m"])
			assert.Nil(t, metricLog.Values["15m"])
			assert.Nil(t, metricLog.Values["mean"])
		default:
			assert.Fail(t, "unexpected metric encountered: %s", metricLog.MetricName)
		}
	}
	assert.True(t, seenMyCounter, "my-counter metric was not emitted")
	assert.True(t, seenResponseTimer, "server.response metric was not emitted")
	assert.True(t, seenRequestSize, "server.request.size metric was not emitted")
	assert.True(t, seenResponseSize, "server.response.size metric was not emitted")
	assert.True(t, seenResponseError, "server.response.error metric was not emitted")

	select {
	case err := <-serverErr:
		require.NoError(t, err)
	default:
	}
}

// TestEmitMetricsEmptyBlacklist verifies that metrics are printed periodically by a Witchcraft server and that, if the
// blacklist is empty, all values are emitted. We verify both custom metrics set in the InitFunc (with tags) and
// server.response metrics from the metrics middleware.
func TestEmitMetricsEmptyBlacklist(t *testing.T) {
	logOutputBuffer := &bytes.Buffer{}
	port, err := httpserver.AvailablePort()
	require.NoError(t, err)

	// ensure that registry used in this test is unique/does not have any past metrics registered on it
	metrics.DefaultMetricsRegistry = metrics.NewRootMetricsRegistry()
	server, serverErr, cleanup := createAndRunCustomTestServer(t, port, port, func(ctx context.Context, info witchcraft.InitInfo) (deferFn func(), rErr error) {
		ctx = metrics.AddTags(ctx, metrics.MustNewTag("key", "val"))
		metrics.FromContext(ctx).Counter("my-counter").Inc(13)
		return nil, info.Router.Post("/error", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(500)
		}))
	}, logOutputBuffer, func(t *testing.T, initFn witchcraft.InitFunc, installCfg config.Install, logOutputBuffer io.Writer) *witchcraft.Server {
		installCfg.MetricsEmitFrequency = 100 * time.Millisecond
		return createTestServer(t, initFn, installCfg, logOutputBuffer).WithMetricTypeValuesBlacklist(map[string]map[string]struct{}{})
	})
	defer func() {
		require.NoError(t, server.Close())
	}()
	defer cleanup()

	// Make POST that will 404 to trigger request size and error rate metrics
	_, err = testServerClient().Post(fmt.Sprintf("https://localhost:%d/%s/%s", port, basePath, "error"), "application/json", strings.NewReader("{}"))
	require.NoError(t, err)

	// Allow the metric emitter to do its thing.
	time.Sleep(150 * time.Millisecond)

	parts := strings.Split(logOutputBuffer.String(), "\n")
	var metricLogs []logging.MetricLogV1
	for _, curr := range parts {
		if strings.Contains(curr, `"metric.1"`) {
			var currLog logging.MetricLogV1
			require.NoError(t, json.Unmarshal([]byte(curr), &currLog))
			metricLogs = append(metricLogs, currLog)
		}
	}

	var seenMyCounter, seenResponseTimer, seenResponseSize, seenRequestSize, seenResponseError bool
	for _, metricLog := range metricLogs {
		switch metricLog.MetricName {
		case "my-counter":
			seenMyCounter = true
			assert.Equal(t, "counter", metricLog.MetricType, "my-counter metric had incorrect type")
			assert.Equal(t, map[string]interface{}{"count": json.Number("13")}, metricLog.Values)
			assert.Equal(t, map[string]string{"key": "val"}, metricLog.Tags)
		case "server.response":
			seenResponseTimer = true
			assert.Equal(t, "timer", metricLog.MetricType, "server.response metric had incorrect type")

			// blacklist is set to empty, so all keys should be non-nil
			assert.NotNil(t, metricLog.Values["count"])
			assert.NotNil(t, metricLog.Values["1m"])
			assert.NotNil(t, metricLog.Values["5m"])
			assert.NotNil(t, metricLog.Values["15m"])
			assert.NotNil(t, metricLog.Values["meanRate"])
			assert.NotNil(t, metricLog.Values["min"])
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["mean"])
			assert.NotNil(t, metricLog.Values["stddev"])
			assert.NotNil(t, metricLog.Values["p50"])
			assert.NotNil(t, metricLog.Values["p95"])
			assert.NotNil(t, metricLog.Values["p99"])
		case "server.request.size":
			seenRequestSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")

			// blacklist is set to empty, so all keys should be non-nil
			assert.NotNil(t, metricLog.Values["min"])
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["mean"])
			assert.NotNil(t, metricLog.Values["stddev"])
			assert.NotNil(t, metricLog.Values["p50"])
			assert.NotNil(t, metricLog.Values["p95"])
			assert.NotNil(t, metricLog.Values["p99"])
			assert.NotNil(t, metricLog.Values["count"])
		case "server.response.size":
			seenResponseSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")

			// blacklist is set to empty, so all keys should be non-nil
			assert.NotNil(t, metricLog.Values["min"])
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["mean"])
			assert.NotNil(t, metricLog.Values["stddev"])
			assert.NotNil(t, metricLog.Values["p50"])
			assert.NotNil(t, metricLog.Values["p95"])
			assert.NotNil(t, metricLog.Values["p99"])
			assert.NotNil(t, metricLog.Values["count"])
		case "server.response.error":
			seenResponseError = true
			assert.Equal(t, "meter", metricLog.MetricType, "server.response metric had incorrect type")

			// blacklist is set to empty, so all keys should be non-nil
			assert.NotNil(t, metricLog.Values["count"])
			assert.NotNil(t, metricLog.Values["1m"])
			assert.NotNil(t, metricLog.Values["5m"])
			assert.NotNil(t, metricLog.Values["15m"])
			assert.NotNil(t, metricLog.Values["mean"])
		default:
			assert.Fail(t, "unexpected metric encountered: %s", metricLog.MetricName)
		}
	}
	assert.True(t, seenMyCounter, "my-counter metric was not emitted")
	assert.True(t, seenResponseTimer, "server.response metric was not emitted")
	assert.True(t, seenRequestSize, "server.request.size metric was not emitted")
	assert.True(t, seenResponseSize, "server.response.size metric was not emitted")
	assert.True(t, seenResponseError, "server.response.error metric was not emitted")

	select {
	case err := <-serverErr:
		require.NoError(t, err)
	default:
	}
}

// TestMetricTypeValueBlacklist tests that if a metric type value is blacklisted, all metric of that type does not
// contain any of the blacklisted keys.
func TestMetricTypeValueBlacklist(t *testing.T) {
	logOutputBuffer := &bytes.Buffer{}
	port, err := httpserver.AvailablePort()
	require.NoError(t, err)

	// ensure that registry used in this test is unique/does not have any past metrics registered on it
	metrics.DefaultMetricsRegistry = metrics.NewRootMetricsRegistry()
	server, serverErr, cleanup := createAndRunCustomTestServer(t, port, port, func(ctx context.Context, info witchcraft.InitInfo) (deferFn func(), rErr error) {
		ctx = metrics.AddTags(ctx, metrics.MustNewTag("key", "val"))
		metrics.FromContext(ctx).Counter("my-counter").Inc(13)
		return nil, info.Router.Post("/error", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(500)
		}))
	}, logOutputBuffer, func(t *testing.T, initFn witchcraft.InitFunc, installCfg config.Install, logOutputBuffer io.Writer) *witchcraft.Server {
		installCfg.MetricsEmitFrequency = 100 * time.Millisecond
		return createTestServer(t, initFn, installCfg, logOutputBuffer).WithMetricTypeValuesBlacklist(map[string]map[string]struct{}{
			"histogram": {"count": {}},
		})
	})
	defer func() {
		require.NoError(t, server.Close())
	}()
	defer cleanup()

	// Make POST that will 404 to trigger request size and error rate metrics
	_, err = testServerClient().Post(fmt.Sprintf("https://localhost:%d/%s/%s", port, basePath, "error"), "application/json", strings.NewReader("{}"))
	require.NoError(t, err)

	// Allow the metric emitter to do its thing.
	time.Sleep(150 * time.Millisecond)

	parts := strings.Split(logOutputBuffer.String(), "\n")
	var metricLogs []logging.MetricLogV1
	for _, curr := range parts {
		if strings.Contains(curr, `"metric.1"`) {
			var currLog logging.MetricLogV1
			require.NoError(t, json.Unmarshal([]byte(curr), &currLog))
			metricLogs = append(metricLogs, currLog)
		}
	}

	var seenMyCounter, seenResponseTimer, seenResponseSize, seenRequestSize, seenResponseError bool
	for _, metricLog := range metricLogs {
		switch metricLog.MetricName {
		case "my-counter":
			seenMyCounter = true
			assert.Equal(t, "counter", metricLog.MetricType, "my-counter metric had incorrect type")
			assert.Equal(t, map[string]interface{}{"count": json.Number("13")}, metricLog.Values)
			assert.Equal(t, map[string]string{"key": "val"}, metricLog.Tags)
		case "server.response":
			seenResponseTimer = true
			assert.Equal(t, "timer", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotNil(t, metricLog.Values["count"])
			assert.NotNil(t, metricLog.Values["mean"])
			assert.NotNil(t, metricLog.Values["max"])
			assert.NotNil(t, metricLog.Values["min"])
		case "server.request.size":
			seenRequestSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")
			// there should be no value for "count" because it is blacklisted for the histogram type
			assert.Nil(t, metricLog.Values["count"])
		case "server.response.size":
			seenResponseSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")
			// there should be no value for "count" because it is blacklisted for the histogram type
			assert.Nil(t, metricLog.Values["count"])
		case "server.response.error":
			seenResponseError = true
			assert.Equal(t, "meter", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotNil(t, metricLog.Values["count"])
		default:
			assert.Fail(t, "unexpected metric encountered: %s", metricLog.MetricName)
		}
	}
	assert.True(t, seenMyCounter, "my-counter metric was not emitted")
	assert.True(t, seenResponseTimer, "server.response metric was not emitted")
	assert.True(t, seenRequestSize, "server.request.size metric was not emitted")
	assert.True(t, seenResponseSize, "server.response.size metric was not emitted")
	assert.True(t, seenResponseError, "server.response.error metric was not emitted")

	select {
	case err := <-serverErr:
		require.NoError(t, err)
	default:
	}
}

// TestMetricsBlacklist verifies that blacklisted metrics are not emitted.
func TestMetricsBlacklist(t *testing.T) {
	logOutputBuffer := &bytes.Buffer{}
	port, err := httpserver.AvailablePort()
	require.NoError(t, err)
	server, serverErr, cleanup := createAndRunCustomTestServer(t, port, port, func(ctx context.Context, info witchcraft.InitInfo) (deferFn func(), rErr error) {
		ctx = metrics.AddTags(ctx, metrics.MustNewTag("key", "val"))
		metrics.FromContext(ctx).Counter("my-counter").Inc(13)
		return nil, info.Router.Post("/error", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(500)
		}))
	}, logOutputBuffer, func(t *testing.T, initFn witchcraft.InitFunc, installCfg config.Install, logOutputBuffer io.Writer) *witchcraft.Server {
		installCfg.MetricsEmitFrequency = 100 * time.Millisecond
		server := createTestServer(t, initFn, installCfg, logOutputBuffer)
		server.WithMetricsBlacklist(map[string]struct{}{
			"my-counter":          {},
			"server.request.size": {},
		})
		return server
	})
	defer func() {
		require.NoError(t, server.Close())
	}()
	defer cleanup()

	// Make POST that will 404 to trigger request size and error rate metrics
	_, err = testServerClient().Post(fmt.Sprintf("https://localhost:%d/%s/%s", port, basePath, "error"), "application/json", strings.NewReader("{}"))
	require.NoError(t, err)

	// Allow the metric emitter to do its thing.
	time.Sleep(150 * time.Millisecond)

	parts := strings.Split(logOutputBuffer.String(), "\n")
	var metricLogs []logging.MetricLogV1
	for _, curr := range parts {
		if strings.Contains(curr, `"metric.1"`) {
			var currLog logging.MetricLogV1
			require.NoError(t, json.Unmarshal([]byte(curr), &currLog))
			metricLogs = append(metricLogs, currLog)
		}
	}

	var seenMyCounter, seenResponseTimer, seenResponseSize, seenRequestSize, seenResponseError bool
	for _, metricLog := range metricLogs {
		switch metricLog.MetricName {
		case "my-counter":
			assert.Fail(t, "my-counter metric should not be emitted")
		case "server.response":
			seenResponseTimer = true
			assert.Equal(t, "timer", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotZero(t, metricLog.Values["count"])
		case "server.request.size":
			assert.Fail(t, "server.request.size metric should not be emitted")
		case "server.response.size":
			seenResponseSize = true
			assert.Equal(t, "histogram", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotZero(t, metricLog.Values["count"])
		case "server.response.error":
			seenResponseError = true
			assert.Equal(t, "meter", metricLog.MetricType, "server.response metric had incorrect type")
			assert.NotZero(t, metricLog.Values["count"])
		default:
			assert.Fail(t, "unexpected metric encountered: %s", metricLog.MetricName)
		}
	}
	assert.False(t, seenMyCounter, "my-counter metric was emitted")
	assert.False(t, seenRequestSize, "server.request.size metric was emitted")

	assert.True(t, seenResponseTimer, "server.response metric was not emitted")
	assert.True(t, seenResponseSize, "server.response.size metric was not emitted")
	assert.True(t, seenResponseError, "server.response.error metric was not emitted")

	select {
	case err := <-serverErr:
		require.NoError(t, err)
	default:
	}
}
