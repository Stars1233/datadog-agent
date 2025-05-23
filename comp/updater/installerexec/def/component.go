// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2025-present Datadog, Inc.

// Package installerexec provides a component to execute installer commands
package installerexec

import (
	installertypes "github.com/DataDog/datadog-agent/pkg/fleet/installer/types"
)

// team: fleet

// Component is the component type.
type Component interface {
	installertypes.Installer
}
