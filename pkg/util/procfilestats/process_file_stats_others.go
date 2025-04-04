// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build !linux

package procfilestats

import "errors"

// ErrNotImplemented is the "not implemented" error given by `gopsutil` when an
// OS doesn't support an API. Unfortunately it's in an internal package so
// we can't import it so we'll copy it here.
var ErrNotImplemented = errors.New("not implemented yet")

// GetProcessFileStats returns the number of file handles the Agent process has open
func GetProcessFileStats() (*ProcessFileStats, error) {
	return nil, ErrNotImplemented
}
