// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package types contains all the types needed by the OpenTelemetry Extension component without the underlying implementation and dependencies.
package types

// BuildInfoResponse is the response struct for BuildInfo
type BuildInfoResponse struct {
	AgentVersion     string `json:"version"`
	AgentCommand     string `json:"command"`
	AgentDesc        string `json:"description"`
	ExtensionVersion string `json:"extension_version"`
	BYOC             bool   `json:"byoc"`
}

// ConfigResponse is the response struct for Config
type ConfigResponse struct {
	CustomerConfig        string `json:"provided_configuration"`
	EnvConfig             string `json:"environment_variable_configuration"`
	RuntimeOverrideConfig string `json:"runtime_override_configuration"`
	RuntimeConfig         string `json:"full_configuration"`
}

// OTelFlareSource is the response struct for flare debug sources
type OTelFlareSource struct {
	URLs []string `json:"url"`
}

// DebugSourceResponse is the response struct for a map of OTelFlareSource
type DebugSourceResponse struct {
	Sources map[string]OTelFlareSource `json:"sources,omitempty"`
}

// Response is the response struct for API queries
type Response struct {
	BuildInfoResponse
	ConfigResponse
	DebugSourceResponse
	Environment map[string]string `json:"environment,omitempty"`
}
