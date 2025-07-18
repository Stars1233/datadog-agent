// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build !windows && kubeapiserver

package start

import (
	"context"
	"os"

	"k8s.io/client-go/dynamic"

	ipc "github.com/DataDog/datadog-agent/comp/core/ipc/def"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	"github.com/DataDog/datadog-agent/comp/logs/agent/config"
	logscompression "github.com/DataDog/datadog-agent/comp/serializer/logscompression/def"
	"github.com/DataDog/datadog-agent/pkg/aggregator/sender"
	"github.com/DataDog/datadog-agent/pkg/compliance"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/logs/client"
	logshttp "github.com/DataDog/datadog-agent/pkg/logs/client/http"
	"github.com/DataDog/datadog-agent/pkg/util/hostname"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/startstop"
)

const (
	intakeTrackType = "compliance"
)

func runCompliance(ctx context.Context, senderManager sender.SenderManager, wmeta workloadmeta.Component, apiCl *apiserver.APIClient, compression logscompression.Component, ipc ipc.Component, isLeader func() bool) error {
	stopper := startstop.NewSerialStopper()
	if err := startCompliance(senderManager, wmeta, stopper, apiCl, isLeader, compression, ipc); err != nil {
		return err
	}

	<-ctx.Done()

	stopper.Stop()
	return nil
}

func newLogContext(logsConfig *config.LogsConfigKeys, endpointPrefix string) (*config.Endpoints, *client.DestinationsContext, error) {
	endpoints, err := config.BuildHTTPEndpointsWithConfig(pkgconfigsetup.Datadog(), logsConfig, endpointPrefix, intakeTrackType, config.AgentJSONIntakeProtocol, config.DefaultIntakeOrigin)
	if err != nil {
		endpoints, err = config.BuildHTTPEndpoints(pkgconfigsetup.Datadog(), intakeTrackType, config.AgentJSONIntakeProtocol, config.DefaultIntakeOrigin)
		if err == nil {
			httpConnectivity := logshttp.CheckConnectivity(endpoints.Main, pkgconfigsetup.Datadog())
			endpoints, err = config.BuildEndpoints(pkgconfigsetup.Datadog(), httpConnectivity, intakeTrackType, config.AgentJSONIntakeProtocol, config.DefaultIntakeOrigin)
		}
	}

	if err != nil {
		return nil, nil, log.Errorf("Invalid endpoints: %v", err)
	}

	for _, status := range endpoints.GetStatus() {
		log.Info(status)
	}

	destinationsCtx := client.NewDestinationsContext()
	destinationsCtx.Start()

	return endpoints, destinationsCtx, nil
}

func newLogContextCompliance() (*config.Endpoints, *client.DestinationsContext, error) {
	logsConfigComplianceKeys := config.NewLogsConfigKeys("compliance_config.endpoints.", pkgconfigsetup.Datadog())
	return newLogContext(logsConfigComplianceKeys, "cspm-intake.")
}

func startCompliance(senderManager sender.SenderManager, wmeta workloadmeta.Component, stopper startstop.Stopper, apiCl *apiserver.APIClient, isLeader func() bool, compression logscompression.Component, ipc ipc.Component) error {
	endpoints, ctx, err := newLogContextCompliance()
	if err != nil {
		log.Error(err)
	}
	stopper.Add(ctx)

	configDir := pkgconfigsetup.Datadog().GetString("compliance_config.dir")
	checkInterval := pkgconfigsetup.Datadog().GetDuration("compliance_config.check_interval")

	hname, err := hostname.Get(context.TODO())
	if err != nil {
		return err
	}

	reporter := compliance.NewLogReporter(hname, "compliance-agent", "compliance", endpoints, ctx, compression)
	statsdClient, err := simpleTelemetrySenderFromSenderManager(senderManager)
	if err != nil {
		return err
	}

	agent := compliance.NewAgent(statsdClient, wmeta, ipc, compliance.AgentOptions{
		ConfigDir:     configDir,
		Reporter:      reporter,
		CheckInterval: checkInterval,
		RuleFilter: func(rule *compliance.Rule) bool {
			return rule.HasScope(compliance.KubernetesClusterScope)
		},
		ResolverOptions: compliance.ResolverOptions{
			Hostname:           hname,
			HostRoot:           os.Getenv("HOST_ROOT"),
			DockerProvider:     compliance.DefaultDockerProvider,
			LinuxAuditProvider: compliance.DefaultLinuxAuditProvider,
			KubernetesProvider: wrapKubernetesClient(apiCl, isLeader),
		},
	})
	err = agent.Start()
	if err != nil {
		return log.Errorf("Error starting compliance agent, exiting: %v", err)
	}
	stopper.Add(agent)

	log.Infof("Running compliance checks every %s", checkInterval.String())
	return nil
}

func wrapKubernetesClient(apiCl *apiserver.APIClient, isLeader func() bool) compliance.KubernetesProvider {
	return func(_ context.Context) (dynamic.Interface, compliance.KubernetesGroupsAndResourcesProvider, error) {
		if isLeader() {
			discoveryCl := apiCl.Cl.Discovery()
			return apiCl.DynamicCl, discoveryCl.ServerGroupsAndResources, nil
		}
		return nil, nil, compliance.ErrIncompatibleEnvironment
	}
}
