// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver

package custommetrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider/defaults"

	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	externalMetricsMaxBackoff  = 32 * time.Second
	externalMetricsBaseBackoff = 1 * time.Second
)

type externalMetric struct {
	info  provider.ExternalMetricInfo
	value external_metrics.ExternalMetricValue
}

type datadogProvider struct {
	// Default implementation recommended for ListAllExternalMetrics
	defaults.DefaultExternalMetricsProvider

	client dynamic.Interface
	mapper apimeta.RESTMapper

	externalMetrics []externalMetric
	store           Store
	isServing       bool
	timestamp       int64
	maxAge          int64
}

// NewDatadogProvider creates a Custom Metrics and External Metrics Provider.
func NewDatadogProvider(ctx context.Context, client dynamic.Interface, mapper apimeta.RESTMapper, store Store) provider.ExternalMetricsProvider {
	maxAge := pkgconfigsetup.Datadog().GetInt64("external_metrics_provider.local_copy_refresh_rate")
	d := &datadogProvider{
		client: client,
		mapper: mapper,
		store:  store,
		maxAge: maxAge,
	}
	go d.externalMetricsSetter(ctx)
	return d
}

func (p *datadogProvider) externalMetricsSetter(ctx context.Context) {
	log.Infof("Starting async loop to collect External Metrics")
	ctxCancel, cancel := context.WithCancel(ctx)
	defer cancel()

	currentBackoff := externalMetricsBaseBackoff
	for {
		var externalMetricsList []externalMetric
		// TODO as we implement a more resilient logic to access a potentially deleted CM, we should pass in ctxCancel in case of permafail.
		rawMetrics, err := p.store.ListAllExternalMetricValues()
		if err != nil {
			if k8serrors.IsNotFound(err) {
				log.Errorf("ConfigMap for external metrics not found: %s", err.Error())
			} else {
				log.Errorf("Could not list the external metrics in the store: %s", err.Error())
			}
			p.isServing = false
		} else {
			for _, metric := range rawMetrics.External {
				// Only metrics that exist in Datadog and available are eligible to be evaluated in the Autoscaler Controller process.
				if !metric.Valid {
					continue
				}
				var extMetric externalMetric
				extMetric.info = provider.ExternalMetricInfo{
					Metric: metric.MetricName,
				}
				// Avoid overflowing when trying to get a 10^3 precision
				q, err := resource.ParseQuantity(fmt.Sprintf("%v", metric.Value))
				if err != nil {
					log.Errorf("Could not parse the metric value: %v into the exponential format", metric.Value)
					continue
				}
				extMetric.value = external_metrics.ExternalMetricValue{
					MetricName:   metric.MetricName,
					MetricLabels: metric.Labels,
					Value:        q,
				}
				externalMetricsList = append(externalMetricsList, extMetric)
			}
			p.externalMetrics = externalMetricsList
			p.timestamp = metav1.Now().Unix()
			p.isServing = true
		}
		select {
		case <-ctxCancel.Done():
			log.Infof("Received instruction to terminate collection of External Metrics, stopping async loop")
			return
		default:
			if p.isServing {
				currentBackoff = externalMetricsBaseBackoff
			} else {
				currentBackoff = min(currentBackoff*2, externalMetricsMaxBackoff)
				log.Infof("Retrying externalMetricsSetter with backoff %.0f seconds", currentBackoff.Seconds())
			}
			time.Sleep(currentBackoff)
			continue
		}
	}
}

// GetExternalMetric is called by the Autoscaler Controller to get the value of the external metric it is currently evaluating.
func (p *datadogProvider) GetExternalMetric(_ context.Context, _ string, metricSelector labels.Selector, info provider.ExternalMetricInfo) (*external_metrics.ExternalMetricValueList, error) {
	if !p.isServing || time.Now().Unix()-p.timestamp > 2*p.maxAge {
		return nil, fmt.Errorf("external metrics invalid")
	}

	matchingMetrics := []external_metrics.ExternalMetricValue{}
	for _, metric := range p.externalMetrics {
		metricFromDatadog := external_metrics.ExternalMetricValue{
			MetricName:   metric.info.Metric,
			MetricLabels: metric.value.MetricLabels,
			Value:        metric.value.Value,
		}
		// Datadog metrics are not case sensitive but the Autoscaler Controller lower cases the metric name as it queries the metrics provider.
		// Lowering the metric name retrieved by the Autoscaler Informer here, allows for users to use metrics with capital letters.
		// Datadog tags are lower cased, but metrics labels are not case sensitive.
		// If tags with capital letters are used (as the label selector in the Autoscaler), no metrics will be retrieved from Datadog.
		if info.Metric == strings.ToLower(metric.info.Metric) &&
			metricSelector.Matches(labels.Set(metric.value.MetricLabels)) {
			metricValue := metricFromDatadog
			metricValue.Timestamp = metav1.Now()
			matchingMetrics = append(matchingMetrics, metricValue)
		}
	}
	log.Debugf("External metrics returned: %#v", matchingMetrics)
	return &external_metrics.ExternalMetricValueList{
		Items: matchingMetrics,
	}, nil
}
