// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

//go:build kubelet

package kubernetes

import (
	"context"
	"fmt"

	"github.com/DataDog/datadog-agent/pkg/config/env"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/hostinfo"
)

var (
	// CloudProviderName contains the inventory name for Kubernetes (through the API server)
	CloudProviderName = "kubernetes"
)

// GetHostAliases returns the host aliases from the Kubernetes node annotations
func GetHostAliases(ctx context.Context) ([]string, error) {
	if !env.IsFeaturePresent(env.Kubernetes) {
		return []string{}, nil
	}

	aliases := []string{}

	hostAliases := pkgconfigsetup.Datadog().GetStringSlice("kubernetes_node_annotations_as_host_aliases")

	annotations, err := hostinfo.GetNodeAnnotations(ctx, hostAliases...)
	if err != nil {
		return nil, fmt.Errorf("failed to get node annotations: %w", err)
	}

	for _, annotation := range hostAliases {
		if value, found := annotations[annotation]; found {
			aliases = append(aliases, value)
		}
	}

	return aliases, nil
}
