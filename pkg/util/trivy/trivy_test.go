// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build trivy && containerd

// Package trivy holds the scan components
package trivy

import (
	"testing"

	"github.com/containerd/containerd/mount"
	"github.com/stretchr/testify/assert"
)

// TestExtractLayersFromOverlayFSMounts checks if the function correctly extracts layer paths from Mount options.
func TestExtractLayersFromOverlayFSMounts(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mounts []mount.Mount
		want   []string
	}{
		{
			name:   "No mounts",
			mounts: []mount.Mount{},
		},
		{
			name:   "Single upperdir",
			mounts: []mount.Mount{{Options: []string{"someoption=somevalue", "upperdir=/path/to/upper"}}},
			want:   []string{"/path/to/upper"},
		},
		{
			name:   "Single lowerdir",
			mounts: []mount.Mount{{Options: []string{"someoption=somevalue", "lowerdir=/path/to/lower"}}},
			want:   []string{"/path/to/lower"},
		},
		{
			name:   "Multiple lowerdir",
			mounts: []mount.Mount{{Options: []string{"someoption=somevalue", "lowerdir=/path/to/lower1:/path/to/lower2"}}},
			want:   []string{"/path/to/lower1", "/path/to/lower2"},
		},
		{
			name:   "Multiple options",
			mounts: []mount.Mount{{Options: []string{"someoption=somevalue", "upperdir=/path/to/upper", "lowerdir=/path/to/lower1:/path/to/lower2"}}},
			want:   []string{"/path/to/upper", "/path/to/lower1", "/path/to/lower2"},
		},
		{
			name: "Multiple mounts",
			mounts: []mount.Mount{
				{Options: []string{"someoption=somevalue", "upperdir=/path/to/upper1"}},
				{Options: []string{"someoption=somevalue", "lowerdir=/path/to/lower1:/path/to/lower2"}},
			},
			want: []string{"/path/to/upper1", "/path/to/lower1", "/path/to/lower2"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractLayersFromOverlayFSMounts(tt.mounts))
		})
	}
}

func TestLooselyCompareAnalyzers(t *testing.T) {
	entries := []struct {
		name     string
		given    []string
		against  []string
		expected bool
	}{
		{
			name:     "empty lists",
			expected: true,
		},
		{
			name:     "os simple",
			given:    []string{"os"},
			against:  []string{"os"},
			expected: true,
		},
		{
			name:     "os duplicated",
			given:    []string{"os", "os"},
			against:  []string{"os"},
			expected: true,
		},
		{
			name:     "os wrong",
			given:    []string{"languages"},
			against:  []string{"os"},
			expected: false,
		},
		{
			name:     "languages and os",
			given:    []string{"os", "languages"},
			against:  []string{"os", "languages"},
			expected: true,
		},
		{
			name:     "languages and os 2",
			given:    []string{"languages", "os"},
			against:  []string{"os", "languages"},
			expected: true,
		},
	}

	for _, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			assert.Equal(t, entry.expected, looselyCompareAnalyzers(entry.given, entry.against))
		})
	}
}
