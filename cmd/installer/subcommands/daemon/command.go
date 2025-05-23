// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package daemon provides the installer daemon commands.
package daemon

import (
	"github.com/DataDog/datadog-agent/cmd/installer/command"

	"github.com/spf13/cobra"
)

// Commands returns the run command
func Commands(global *command.GlobalParams) []*cobra.Command {
	ctlCmd := &cobra.Command{
		Use:     "daemon [command]",
		Short:   "Interact with the installer daemon",
		GroupID: "daemon",
	}
	ctlCmd.AddCommand(apiCommands(global)...)
	return []*cobra.Command{runCommand(global), ctlCmd}
}
