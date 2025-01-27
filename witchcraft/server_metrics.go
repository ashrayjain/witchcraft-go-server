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

package witchcraft

import (
	"context"

	"github.com/palantir/pkg/metrics"
	werror "github.com/palantir/witchcraft-go-error"
	"github.com/palantir/witchcraft-go-logging/wlog/metriclog/metric1log"
	"github.com/palantir/witchcraft-go-logging/wlog/wapp"
	"github.com/palantir/witchcraft-go-server/config"
)

func defaultMetricTypeValuesBlacklist() map[string]map[string]struct{} {
	return map[string]map[string]struct{}{
		"histogram": {
			"min":    {},
			"mean":   {},
			"stddev": {},
			"p50":    {},
		},
		"meter": {
			"1m":   {},
			"5m":   {},
			"15m":  {},
			"mean": {},
		},
		"timer": {
			"1m":       {},
			"5m":       {},
			"15m":      {},
			"meanRate": {},
			"min":      {},
			"mean":     {},
			"stddev":   {},
			"p50":      {},
		},
	}
}

func (s *Server) initMetrics(ctx context.Context, installCfg config.Install) (rRegistry metrics.RootRegistry, rDeferFn func(), rErr error) {
	metricsRegistry := metrics.DefaultMetricsRegistry
	metricsEmitFreq := defaultMetricEmitFrequency
	if freq := installCfg.MetricsEmitFrequency; freq > 0 {
		metricsEmitFreq = freq
	}

	// start routine that capture Go runtime metrics
	if !s.disableGoRuntimeMetrics {
		if ok := metrics.CaptureRuntimeMemStatsWithContext(ctx, metricsRegistry, metricsEmitFreq); !ok {
			return nil, nil, werror.Error("metricsRegistry does not support capturing runtime memory statistics")
		}
	}

	metricTypeValuesBlacklist := s.metricTypeValuesBlacklist
	if metricTypeValuesBlacklist == nil {
		metricTypeValuesBlacklist = defaultMetricTypeValuesBlacklist()
	}

	emitFn := func(metricID string, tags metrics.Tags, metricVal metrics.MetricVal) {
		if _, blackListed := s.metricsBlacklist[metricID]; blackListed {
			// skip emitting metric if it is blacklisted
			return
		}

		valuesToUse := metricVal.Values()
		metricType := metricVal.Type()
		if metricTypeValueBlacklist, ok := metricTypeValuesBlacklist[metricType]; ok {
			// remove blacklisted keys
			for blacklistedKey := range metricTypeValueBlacklist {
				delete(valuesToUse, blacklistedKey)
			}
		}
		if len(valuesToUse) == 0 {
			// do not record metric if it does not have any values
			return
		}
		s.metricLogger.Metric(metricID, metricType, metric1log.Values(valuesToUse), metric1log.Tags(tags.ToMap()))
	}

	// start goroutine that logs metrics at the given frequency
	go wapp.RunWithRecoveryLogging(ctx, func(ctx context.Context) {
		metrics.RunEmittingRegistry(ctx, metricsRegistry, metricsEmitFreq, emitFn)
	})

	return metricsRegistry, func() {
		// emit all metrics a final time on termination
		metricsRegistry.Each(emitFn)
	}, nil
}
