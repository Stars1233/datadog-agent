// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build !windows

// Package wincrashdetect implements the windows crash detection on windows.  It does nothing on linux
package wincrashdetect

import (
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

const (
	// CheckName is the name of the check
	CheckName = "wincrashdetect"
)

// Factory creates a new check factory
func Factory() option.Option[func() check.Check] {
	return option.None[func() check.Check]()
}
