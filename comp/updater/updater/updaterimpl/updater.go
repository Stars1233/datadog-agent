// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package updaterimpl implements the updater component.
package updaterimpl

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/hostname"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	"github.com/DataDog/datadog-agent/comp/remote-config/rcservice"
	updatercomp "github.com/DataDog/datadog-agent/comp/updater/updater"
	"github.com/DataDog/datadog-agent/pkg/fleet/daemon"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

var (
	errRemoteConfigRequired = errors.New("remote config is required to create the updater")
)

// Module is the fx module for the updater.
func Module() fxutil.Module {
	return fxutil.Component(
		fx.Provide(newUpdaterComponent),
	)
}

// dependencies contains the dependencies to build the updater.
type dependencies struct {
	fx.In

	Hostname     hostname.Component
	Log          log.Component
	Config       config.Component
	RemoteConfig option.Option[rcservice.Component]
}

func newUpdaterComponent(lc fx.Lifecycle, dependencies dependencies) (updatercomp.Component, error) {
	remoteConfig, ok := dependencies.RemoteConfig.Get()
	if !ok {
		return nil, errRemoteConfigRequired
	}
	hostname, err := dependencies.Hostname.Get(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not get hostname: %w", err)
	}
	daemon, err := daemon.NewDaemon(hostname, remoteConfig, dependencies.Config)
	if err != nil {
		return nil, fmt.Errorf("could not create updater: %w", err)
	}
	lc.Append(fx.Hook{OnStart: daemon.Start, OnStop: daemon.Stop})
	return daemon, nil
}
