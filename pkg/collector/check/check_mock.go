// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package check

import (
	"time"

	checkid "github.com/DataDog/datadog-agent/pkg/collector/check/id"
)

// MockInfo is a mock for test using check.Info interface
type MockInfo struct {
	Name         string
	LoaderName   string
	CheckID      checkid.ID
	Source       string
	InitConf     string
	InstanceConf string
}

// String returns the name of the check
func (m MockInfo) String() string { return m.Name }

// Interval returns 0 always
func (m MockInfo) Interval() time.Duration { return 0 }

// ID returns the ID of the check
func (m MockInfo) ID() checkid.ID { return m.CheckID }

// Version returns an empty string
func (m MockInfo) Version() string { return "" }

// ConfigSource returns the source of the check
func (m MockInfo) ConfigSource() string { return m.Source }

// Loader returns the name of the check loader
func (m MockInfo) Loader() string { return m.LoaderName }

// InitConfig returns the init_config of the check
func (m MockInfo) InitConfig() string { return m.InitConf }

// InstanceConfig returns the instance config of the check
func (m MockInfo) InstanceConfig() string { return m.InstanceConf }
