// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package stats contains the stats for a check
package stats

import (
	"maps"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"

	haagent "github.com/DataDog/datadog-agent/comp/haagent/def"
	checkid "github.com/DataDog/datadog-agent/pkg/collector/check/id"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/config/utils"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	runCheckFailureTag = "fail"
	runCheckSuccessTag = "ok"
)

// EventPlatformNameTranslations contains human readable translations for event platform event types
var EventPlatformNameTranslations = map[string]string{
	"dbm-samples":                "Database Monitoring Query Samples",
	"dbm-metrics":                "Database Monitoring Query Metrics",
	"dbm-activity":               "Database Monitoring Activity Samples",
	"dbm-metadata":               "Database Monitoring Metadata Samples",
	"network-devices-metadata":   "Network Devices Metadata",
	"network-devices-netflow":    "Network Devices NetFlow",
	"network-devices-snmp-traps": "SNMP Traps",
	"network-path":               "Network Path",
}

var (
	tlmRuns = telemetry.NewCounter("checks", "runs",
		[]string{"check_name", "state"}, "Check runs")
	tlmWarnings = telemetry.NewCounter("checks", "warnings",
		[]string{"check_name"}, "Check warnings")
	tlmMetricsSamples = telemetry.NewCounter("checks", "metrics_samples",
		[]string{"check_name"}, "Metrics count")
	tlmEvents = telemetry.NewCounter("checks", "events",
		[]string{"check_name"}, "Events count")
	tlmServices = telemetry.NewCounter("checks", "services_checks",
		[]string{"check_name"}, "Service checks count")
	tlmHistogramBuckets = telemetry.NewCounter("checks", "histogram_buckets",
		[]string{"check_name"}, "Histogram buckets count")
	tlmExecutionTime = telemetry.NewGauge("checks", "execution_time",
		[]string{"check_name", "check_loader"}, "Check execution time")
	tlmCheckDelay = telemetry.NewGauge("checks",
		"delay",
		[]string{"check_name"},
		"Check start time delay relative to the previous check run")
	tlmHaAgentIntegrationRuns = telemetry.NewCounterWithOpts(
		"ha_agent",
		"integration_runs",
		[]string{"integration", "config_id"},
		"Tracks number of HA integrations runs.",
		telemetry.Options{DefaultMetric: true},
	)
)

// SenderStats contains statistics showing the count of various types of telemetry sent by a check sender
type SenderStats struct {
	MetricSamples    int64
	Events           int64
	ServiceChecks    int64
	HistogramBuckets int64
	// EventPlatformEvents tracks the number of events submitted for each eventType
	EventPlatformEvents map[string]int64
	// LongRunningCheck is a field that is only set for long running checks
	// converted to a normal check
	LongRunningCheck bool
}

// NewSenderStats creates a new SenderStats
func NewSenderStats() SenderStats {
	return SenderStats{
		EventPlatformEvents: make(map[string]int64),
	}
}

// Copy creates a copy of the current SenderStats
func (s SenderStats) Copy() (result SenderStats) {
	result = s
	result.EventPlatformEvents = make(map[string]int64, len(s.EventPlatformEvents))
	maps.Copy(result.EventPlatformEvents, s.EventPlatformEvents)
	return result
}

// Stats holds basic runtime statistics about check instances
type Stats struct {
	CheckName         string
	CheckVersion      string
	CheckConfigSource string
	CheckLoader       string
	CheckID           checkid.ID
	Interval          time.Duration
	// LongRunning is true if the check is a long running check
	// converted to a normal check
	LongRunning              bool
	Cancelling               bool
	TotalRuns                uint64
	TotalErrors              uint64
	TotalWarnings            uint64
	MetricSamples            int64
	Events                   int64
	ServiceChecks            int64
	HistogramBuckets         int64
	TotalMetricSamples       uint64
	TotalEvents              uint64
	TotalServiceChecks       uint64
	TotalHistogramBuckets    uint64
	EventPlatformEvents      map[string]int64
	TotalEventPlatformEvents map[string]int64
	ExecutionTimes           [32]int64 // circular buffer of recent run durations, most recent at [(TotalRuns+31) % 32]
	AverageExecutionTime     int64     // average run duration
	LastExecutionTime        int64     // most recent run duration, provided for convenience
	LastSuccessDate          int64     // most recent successful execution date, unix timestamp in seconds
	LastError                string    // error that occurred in the last run, if any
	LastDelay                int64     // most recent check start time delay relative to the previous check run, in seconds
	LastWarnings             []string  // warnings that occurred in the last run, if any
	UpdateTimestamp          int64     // latest update to this instance, unix timestamp in seconds
	m                        sync.Mutex
	Telemetry                bool // do we want telemetry on this Check
	HASupported              bool
}

//nolint:revive
type StatsCheck interface {
	// String provides a printable version of the check name
	String() string
	// ID provides a unique identifier for every check instance
	ID() checkid.ID
	// Version returns the version of the check if available
	Version() string
	//Interval returns the interval time for the check
	Interval() time.Duration
	// ConfigSource returns the configuration source of the check
	ConfigSource() string
	// Loader returns the name of the check loader
	Loader() string
	// IsHASupported returns if the check is HA enabled
	IsHASupported() bool
}

// NewStats returns a new check stats instance
func NewStats(c StatsCheck) *Stats {
	stats := Stats{
		CheckID:                  c.ID(),
		CheckName:                c.String(),
		CheckLoader:              c.Loader(),
		CheckVersion:             c.Version(),
		CheckConfigSource:        c.ConfigSource(),
		Interval:                 c.Interval(),
		Telemetry:                utils.IsCheckTelemetryEnabled(c.String(), pkgconfigsetup.Datadog()),
		EventPlatformEvents:      make(map[string]int64),
		TotalEventPlatformEvents: make(map[string]int64),
		HASupported:              c.IsHASupported(),
	}

	// We are interested in a check's run state values even when they are 0 so we
	// initialize them here explicitly
	if stats.Telemetry && utils.IsTelemetryEnabled(pkgconfigsetup.Datadog()) {
		tlmRuns.InitializeToZero(stats.CheckName, runCheckFailureTag)
		tlmRuns.InitializeToZero(stats.CheckName, runCheckSuccessTag)
	}

	return &stats
}

// Add tracks a new execution time
func (cs *Stats) Add(t time.Duration, err error, warnings []error, metricStats SenderStats, haagent haagent.Component) {
	cs.m.Lock()
	defer cs.m.Unlock()

	cs.LastDelay = calculateCheckDelay(time.Now(), cs, t)
	if cs.Telemetry {
		tlmCheckDelay.Set(float64(cs.LastDelay), cs.CheckName)
	}

	// store execution times in Milliseconds
	tms := t.Nanoseconds() / 1e6
	cs.LongRunning = metricStats.LongRunningCheck
	cs.LastExecutionTime = tms
	cs.ExecutionTimes[cs.TotalRuns%uint64(len(cs.ExecutionTimes))] = tms
	cs.TotalRuns++
	if cs.Telemetry {
		tlmExecutionTime.Set(float64(tms), cs.CheckName, cs.CheckLoader)
	}
	var totalExecutionTime int64
	ringSize := min(cs.TotalRuns, uint64(len(cs.ExecutionTimes)))
	for i := uint64(0); i < ringSize; i++ {
		totalExecutionTime += cs.ExecutionTimes[i]
	}
	cs.AverageExecutionTime = totalExecutionTime / int64(ringSize)
	if err != nil {
		cs.TotalErrors++
		if cs.Telemetry {
			tlmRuns.Inc(cs.CheckName, runCheckFailureTag)
		}
		cs.LastError = err.Error()
	} else {
		if cs.Telemetry {
			tlmRuns.Inc(cs.CheckName, runCheckSuccessTag)
		}
		cs.LastError = ""
		cs.LastSuccessDate = time.Now().Unix()
	}
	cs.LastWarnings = []string{}
	if len(warnings) != 0 {
		if cs.Telemetry {
			tlmWarnings.Add(float64(len(warnings)), cs.CheckName)
		}
		for _, w := range warnings {
			cs.TotalWarnings++
			cs.LastWarnings = append(cs.LastWarnings, w.Error())
		}
	}
	cs.UpdateTimestamp = time.Now().Unix()

	if metricStats.MetricSamples > 0 {
		cs.MetricSamples = metricStats.MetricSamples
		cs.TotalMetricSamples += uint64(metricStats.MetricSamples)
		if cs.Telemetry {
			tlmMetricsSamples.Add(float64(metricStats.MetricSamples), cs.CheckName)
		}
	}
	if metricStats.Events > 0 {
		cs.Events = metricStats.Events
		cs.TotalEvents += uint64(metricStats.Events)
		if cs.Telemetry {
			tlmEvents.Add(float64(metricStats.Events), cs.CheckName)
		}
	}
	if metricStats.ServiceChecks > 0 {
		cs.ServiceChecks = metricStats.ServiceChecks
		cs.TotalServiceChecks += uint64(metricStats.ServiceChecks)
		if cs.Telemetry {
			tlmServices.Add(float64(metricStats.ServiceChecks), cs.CheckName)
		}
	}
	if metricStats.HistogramBuckets > 0 {
		cs.HistogramBuckets = metricStats.HistogramBuckets
		cs.TotalHistogramBuckets += uint64(metricStats.HistogramBuckets)
		if cs.Telemetry {
			tlmHistogramBuckets.Add(float64(metricStats.HistogramBuckets), cs.CheckName)
		}
	}
	for k, v := range metricStats.EventPlatformEvents {
		// translate event types into more descriptive names
		if humanName, ok := EventPlatformNameTranslations[k]; ok {
			k = humanName
		}
		cs.TotalEventPlatformEvents[k] = cs.TotalEventPlatformEvents[k] + v
		cs.EventPlatformEvents[k] = v
	}
	if haagent != nil && haagent.Enabled() && cs.HASupported {
		tlmHaAgentIntegrationRuns.Inc(cs.CheckName, pkgconfigsetup.Datadog().GetString("config_id"))
	}
}

// SetStateCancelling sets the check stats to be in a cancelling state
func (cs *Stats) SetStateCancelling() {
	cs.m.Lock()
	defer cs.m.Unlock()
	cs.Cancelling = true
}

type aggStats struct {
	EventPlatformEvents       map[string]interface{}
	EventPlatformEventsErrors map[string]interface{}
	Other                     map[string]interface{} `mapstructure:",remain"`
}

func translateEventTypes(original map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	if original == nil {
		return result
	}
	for k, v := range original {
		if translated, ok := EventPlatformNameTranslations[k]; ok {
			result[translated] = v
			log.Debugf("successfully translated event platform event type from '%s' to '%s'", original, translated)
		} else {
			result[k] = v
		}
	}
	return result
}

// TranslateEventPlatformEventTypes translates the event platform event types in aggregator stats to human readable names
func TranslateEventPlatformEventTypes(aggregatorStats interface{}) (interface{}, error) {
	var aggStats aggStats
	err := mapstructure.Decode(aggregatorStats, &aggStats)
	if err != nil {
		return nil, err
	}
	result := make(map[string]interface{})
	result["EventPlatformEvents"] = translateEventTypes(aggStats.EventPlatformEvents)
	result["EventPlatformEventsErrors"] = translateEventTypes(aggStats.EventPlatformEventsErrors)
	maps.Copy(result, aggStats.Other)
	return result, nil
}
