// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build unix

// Package main is the entrypoint for system-probe process
package main

import (
	"os"

	"github.com/DataDog/datadog-agent/cmd/internal/runcmd"
	"github.com/DataDog/datadog-agent/cmd/system-probe/command"
	"github.com/DataDog/datadog-agent/cmd/system-probe/subcommands"
	"github.com/DataDog/datadog-agent/pkg/util/flavor"
)

func main() {
	flavor.SetFlavor(flavor.SystemProbe)
	rootCmd := command.MakeCommand(subcommands.SysprobeSubcommands())
	command.SetDefaultCommandIfNonePresent(rootCmd)
	os.Exit(runcmd.Run(rootCmd))
}
