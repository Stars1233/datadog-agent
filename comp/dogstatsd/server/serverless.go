// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package server implements a component to run the dogstatsd server
package server

import (
	"context"

	"github.com/DataDog/datadog-agent/comp/core/hostname/hostnameimpl"
	logComponentImpl "github.com/DataDog/datadog-agent/comp/core/log/impl"
	telemetry "github.com/DataDog/datadog-agent/comp/core/telemetry/noopsimpl"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/pidmap/pidmapimpl"
	replay "github.com/DataDog/datadog-agent/comp/dogstatsd/replay/impl-noop"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/serverDebug/serverdebugimpl"
	"github.com/DataDog/datadog-agent/pkg/aggregator"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

// team: agent-metric-pipelines

// ServerlessDogstatsd is the interface for the serverless dogstatsd server.
type ServerlessDogstatsd interface {
	Component
	Stop()
}

//nolint:revive // TODO(AML) Fix revive linter
func NewServerlessServer(demux aggregator.Demultiplexer) (ServerlessDogstatsd, error) {
	wmeta := option.None[workloadmeta.Component]()
	s := newServerCompat(pkgconfigsetup.Datadog(), logComponentImpl.NewTemporaryLoggerWithoutInit(), hostnameimpl.NewHostnameService(), replay.NewNoopTrafficCapture(), serverdebugimpl.NewServerlessServerDebug(), true, demux, wmeta, pidmapimpl.NewServerlessPidMap(), telemetry.GetCompatComponent())

	err := s.start(context.TODO())
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *server) Stop() {
	_ = s.stop(context.TODO())
}
