// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package otel contains files for otel integration tests
package otel

import (
	datadogconfig "github.com/open-telemetry/opentelemetry-collector-contrib/pkg/datadog/config"

	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/hostname/hostnameinterface"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	"github.com/DataDog/datadog-agent/comp/otelcol/logsagentpipeline"
	"github.com/DataDog/datadog-agent/comp/otelcol/logsagentpipeline/logsagentpipelineimpl"
	"github.com/DataDog/datadog-agent/comp/otelcol/otlp/components/exporter/logsagentexporter"
	"github.com/DataDog/datadog-agent/comp/otelcol/otlp/components/metricsclient"
	"github.com/DataDog/datadog-agent/comp/otelcol/otlp/components/statsprocessor"
	"github.com/DataDog/datadog-agent/pkg/config/model"
	"github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	"github.com/DataDog/datadog-agent/pkg/trace/api"
	strategy_none "github.com/DataDog/datadog-agent/pkg/util/compression/impl-noop"
)

const (
	_ = metricsclient.ExporterSourceTag
)

func _(
	_ datadogconfig.Config,
	_ *statsprocessor.TraceAgent,
	_ config.Component,
	_ hostnameinterface.Component,
	_ log.Component,
	_ logsagentpipeline.Component,
	_ logsagentpipelineimpl.Agent,
	_ logsagentexporter.Config,
	_ model.Config,
	_ setup.ConfigurationProviders,
	_ trace.Trace,
	_ *api.OTLPReceiver,
	_ *strategy_none.NoopStrategy,
) {
	main()
}

func main() {}
