// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build test

// Package stub is a stub package for testing purposes
package stub

import (
	"time"

	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/integration"
	diagnose "github.com/DataDog/datadog-agent/comp/core/diagnose/def"
	"github.com/DataDog/datadog-agent/pkg/aggregator/sender"
	checkid "github.com/DataDog/datadog-agent/pkg/collector/check/id"
	"github.com/DataDog/datadog-agent/pkg/collector/check/stats"
)

// StubCheck stubs a check, should only be used in tests
//
//nolint:revive
type StubCheck struct{}

// String provides a printable version of the check name
func (c *StubCheck) String() string { return "StubCheck" }

// Version returns the empty string
func (c *StubCheck) Version() string { return "" }

// ConfigSource returns the empty string
func (c *StubCheck) ConfigSource() string { return "" }

// Loader returns a stubbed loader name
func (*StubCheck) Loader() string { return "stub" }

// Stop is a noop
func (c *StubCheck) Stop() {}

// Cancel is a noop
func (c *StubCheck) Cancel() {}

// Configure is a noop
func (c *StubCheck) Configure(sender.SenderManager, uint64, integration.Data, integration.Data, string) error {
	return nil
}

// Interval returns a duration of one second
func (c *StubCheck) Interval() time.Duration { return 1 * time.Second }

// Run is a noop
func (c *StubCheck) Run() error { return nil }

// ID returns the check name
func (c *StubCheck) ID() checkid.ID { return checkid.ID(c.String()) }

// GetWarnings returns an empty slice
func (c *StubCheck) GetWarnings() []error { return []error{} }

// GetSenderStats returns an empty map
func (c *StubCheck) GetSenderStats() (stats.SenderStats, error) { return stats.NewSenderStats(), nil }

// IsTelemetryEnabled returns false
func (c *StubCheck) IsTelemetryEnabled() bool { return false }

// InitConfig returns the init_config configuration of the check
func (c *StubCheck) InitConfig() string { return "" }

// InstanceConfig returns the instance configuration of the check
func (c *StubCheck) InstanceConfig() string { return "" }

// GetDiagnoses returns the diagnoses of the check
func (c *StubCheck) GetDiagnoses() ([]diagnose.Diagnosis, error) { return nil, nil }

// IsHASupported returns false
func (c *StubCheck) IsHASupported() bool { return false }
