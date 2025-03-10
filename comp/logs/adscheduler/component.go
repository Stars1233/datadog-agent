// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package adscheduler is glue code to connect autodiscovery to the logs agent. It receives and filters events and converts them into log sources.
package adscheduler

// team: agent-log-pipelines

// Component is the component type.
type Component interface{}
