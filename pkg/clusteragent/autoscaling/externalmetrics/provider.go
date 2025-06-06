// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver

package externalmetrics

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider/defaults"

	datadogclient "github.com/DataDog/datadog-agent/comp/autoscaling/datadogclient/def"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver/common"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver/leaderelection"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/autoscalers"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	autogenExpirationPeriodHours int64 = 3
)

type datadogMetricProvider struct {
	// Default implementation recommended for ListAllExternalMetrics
	defaults.DefaultExternalMetricsProvider

	apiCl            *apiserver.APIClient
	store            DatadogMetricsInternalStore
	autogenNamespace string
}

var (
	metricsMaxAge              int64
	metricsQueryValidityPeriod int64
)

// NewDatadogMetricProvider configures and returns a new datadogMetricProvider
func NewDatadogMetricProvider(ctx context.Context, apiCl *apiserver.APIClient, datadogClient datadogclient.Component) (provider.ExternalMetricsProvider, error) {
	if apiCl == nil {
		return nil, fmt.Errorf("Impossible to create DatadogMetricProvider without valid APIClient")
	}

	le, err := leaderelection.GetLeaderEngine()
	if err != nil {
		return nil, fmt.Errorf("Unable to create DatadogMetricProvider as LeaderElection failed with: %v", err)
	}

	aggregator := pkgconfigsetup.Datadog().GetString("external_metrics.aggregator")
	rollup := pkgconfigsetup.Datadog().GetInt("external_metrics_provider.rollup")
	setQueryConfigValues(aggregator, rollup)

	refreshPeriod := pkgconfigsetup.Datadog().GetInt64("external_metrics_provider.refresh_period")
	metricsMaxAge = int64(math.Max(pkgconfigsetup.Datadog().GetFloat64("external_metrics_provider.max_age"), float64(3*rollup)))
	metricsQueryValidityPeriod = int64(pkgconfigsetup.Datadog().GetFloat64("external_metrics_provider.query_validity_period"))
	splitBatchBackoffOnErrors := pkgconfigsetup.Datadog().GetBool("external_metrics_provider.split_batches_with_backoff")
	autogenNamespace := common.GetResourcesNamespace()
	autogenEnabled := pkgconfigsetup.Datadog().GetBool("external_metrics_provider.enable_datadogmetric_autogen")
	wpaEnabled := pkgconfigsetup.Datadog().GetBool("external_metrics_provider.wpa_controller")
	numWorkers := pkgconfigsetup.Datadog().GetInt("external_metrics_provider.num_workers")

	provider := &datadogMetricProvider{
		apiCl:            apiCl,
		store:            NewDatadogMetricsInternalStore(),
		autogenNamespace: autogenNamespace,
	}

	// Start MetricsRetriever, only leader will do refresh metrics
	metricsRetriever, err := NewMetricsRetriever(refreshPeriod, metricsMaxAge, autoscalers.NewProcessor(datadogClient), le.IsLeader, &provider.store, splitBatchBackoffOnErrors)
	if err != nil {
		return nil, fmt.Errorf("Unable to create DatadogMetricProvider as MetricsRetriever failed with: %v", err)
	}
	go metricsRetriever.Run(ctx.Done())

	var wpaInformer dynamicinformer.DynamicSharedInformerFactory
	if wpaEnabled {
		wpaInformer = apiCl.DynamicInformerFactory
	}

	// Start AutoscalerWatcher, only leader will flag DatadogMetrics as Active/Inactive
	// WPAInformerFactory is nil when WPA is not used. AutoscalerWatcher will check value itself.
	autoscalerWatcher, err := NewAutoscalerWatcher(
		refreshPeriod,
		autogenEnabled,
		autogenExpirationPeriodHours,
		autogenNamespace,
		apiCl.Cl,
		apiCl.InformerFactory,
		wpaInformer,
		le.IsLeader,
		&provider.store,
	)
	if err != nil {
		return nil, fmt.Errorf("Unabled to create DatadogMetricProvider as AutoscalerWatcher failed with: %v", err)
	}

	// We shift controller refresh period from retrieverRefreshPeriod to maximize the probability to have new data from DD
	controller, err := NewDatadogMetricController(apiCl.DynamicCl, apiCl.DynamicInformerFactory, le.IsLeader, &provider.store)
	if err != nil {
		return nil, fmt.Errorf("Unable to create DatadogMetricProvider as DatadogMetric Controller failed with: %v", err)
	}

	// Start informers & controllers (informers can be started multiple times)
	apiCl.DynamicInformerFactory.Start(ctx.Done())
	apiCl.InformerFactory.Start(ctx.Done())

	go autoscalerWatcher.Run(ctx.Done())
	go controller.Run(ctx, numWorkers)

	return provider, nil
}

// GetExternalMetric returns the value of a metric for a given namespace and metric selector
func (p *datadogMetricProvider) GetExternalMetric(_ context.Context, namespace string, metricSelector labels.Selector, info provider.ExternalMetricInfo) (*external_metrics.ExternalMetricValueList, error) {
	startTime := time.Now()
	res, err := p.getExternalMetric(namespace, metricSelector, info, startTime)
	if err != nil {
		convErr := apierr.NewInternalError(err)
		if convErr != nil {
			err = convErr
		}
	}

	setQueryTelemtry("get", startTime, err)
	return res, err
}

func (p *datadogMetricProvider) getExternalMetric(namespace string, metricSelector labels.Selector, info provider.ExternalMetricInfo, time time.Time) (*external_metrics.ExternalMetricValueList, error) {
	log.Debugf("Received external metric query with ns: %s, selector: %s, metricName: %s", namespace, metricSelector.String(), info.Metric)

	// Convert metric name to lower case to allow proper matching (and DD metrics are always lower case)
	info.Metric = strings.ToLower(info.Metric)

	// If the metric name is already prefixed, we can directly look up metrics in store
	datadogMetricID, parsed, hasPrefix := metricNameToDatadogMetricID(info.Metric)
	if !hasPrefix {
		datadogMetricID = p.autogenNamespace + kubernetesNamespaceSep + getAutogenDatadogMetricNameFromSelector(info.Metric, metricSelector)
		parsed = true
	}
	if !parsed {
		return nil, log.Warnf("ExternalMetric does not follow DatadogMetric format: %s", info.Metric)
	}

	datadogMetric := p.store.Get(datadogMetricID)
	log.Tracef("DatadogMetric from store: %v", datadogMetric)

	if datadogMetric == nil {
		return nil, log.Warnf("DatadogMetric not found for metric name: %s, datadogmetricid: %s", info.Metric, datadogMetricID)
	}

	externalMetric, err := datadogMetric.ToExternalMetricFormat(info.Metric, metricsMaxAge, time, metricsQueryValidityPeriod)
	if err != nil {
		return nil, err
	}

	return &external_metrics.ExternalMetricValueList{
		Items: []external_metrics.ExternalMetricValue{*externalMetric},
	}, nil
}
