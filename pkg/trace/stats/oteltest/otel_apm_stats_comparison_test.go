// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package oteltest

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/DataDog/opentelemetry-mapping-go/pkg/otlp/attributes"

	"github.com/DataDog/datadog-go/v5/statsd"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/metric/noop"
	semconv "go.opentelemetry.io/otel/semconv/v1.6.1"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/DataDog/datadog-agent/comp/otelcol/otlp/components/statsprocessor"
	"github.com/DataDog/datadog-agent/pkg/obfuscate"
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	traceconfig "github.com/DataDog/datadog-agent/pkg/trace/config"
	"github.com/DataDog/datadog-agent/pkg/trace/stats"
	"github.com/DataDog/datadog-agent/pkg/trace/timing"
)

// Comparison test to ensure APM stats generated from 2 different OTel ingestion paths are consistent.
func TestOTelAPMStatsMatch(t *testing.T) {
	t.Run("ReceiveResourceSpansV1", func(t *testing.T) {
		t.Run("OperationNameV1", func(t *testing.T) {
			t.Parallel()
			testOTelAPMStatsMatch(false, false, t)
		})

		t.Run("OperationNameV2", func(t *testing.T) {
			t.Parallel()
			testOTelAPMStatsMatch(false, true, t)
		})
	})

	t.Run("ReceiveResourceSpansV2", func(t *testing.T) {
		t.Run("OperationNameV1", func(t *testing.T) {
			t.Parallel()
			testOTelAPMStatsMatch(true, false, t)
		})

		t.Run("OperationNameV2", func(t *testing.T) {
			t.Parallel()
			testOTelAPMStatsMatch(true, true, t)
		})
	})
}

func testOTelAPMStatsMatch(enableReceiveResourceSpansV2 bool, enableOperationNameLogicV2 bool, t *testing.T) {
	ctx := context.Background()
	set := componenttest.NewNopTelemetrySettings()
	set.MeterProvider = noop.NewMeterProvider()
	attributesTranslator, err := attributes.NewTranslator(set)
	require.NoError(t, err)
	tcfg := getTraceAgentCfg(attributesTranslator)
	peerTagKeys := tcfg.ConfiguredPeerTags()
	if !enableReceiveResourceSpansV2 {
		tcfg.Features["disable_receive_resource_spans_v2"] = struct{}{}
	}
	if !enableOperationNameLogicV2 {
		tcfg.Features["disable_operation_and_resource_name_logic_v2"] = struct{}{}
	}

	metricsClient := &statsd.NoOpClient{}
	timingReporter := timing.New(metricsClient)
	// Set up 2 output channels for APM stats, and start 2 fake trace agent to conduct a comparison test
	out1 := make(chan *pb.StatsPayload, 100)
	fakeAgent1 := statsprocessor.NewAgentWithConfig(ctx, tcfg, out1, metricsClient, timingReporter)
	fakeAgent1.Start()
	defer fakeAgent1.Stop()
	out2 := make(chan *pb.StatsPayload, 100)
	fakeAgent2 := statsprocessor.NewAgentWithConfig(ctx, tcfg, out2, metricsClient, timingReporter)
	fakeAgent2.Start()
	defer fakeAgent2.Stop()

	traces := getTestTraces()

	// fakeAgent1 has OTLP traces go through the old pipeline: ReceiveResourceSpan -> TraceWriter -> ... ->  Concentrator.Run
	fakeAgent1.Ingest(ctx, traces)

	obfuscator := newTestObfuscator(tcfg)
	// fakeAgent2 calls the new API in Concentrator that directly calculates APM stats for OTLP traces
	inputs := stats.OTLPTracesToConcentratorInputsWithObfuscation(traces, tcfg, []string{string(semconv.ContainerIDKey), string(semconv.K8SContainerNameKey)}, peerTagKeys, obfuscator)
	for _, input := range inputs {
		fakeAgent2.Concentrator.Add(input)
	}

	// Verify APM stats generated from 2 paths are consistent
	var payload1 *pb.StatsPayload
	var payload2 *pb.StatsPayload
	for payload1 == nil || payload2 == nil {
		select {
		case sp1 := <-out1:
			if len(sp1.Stats) > 0 {
				payload1 = sp1
				for _, csb := range sp1.Stats {
					require.Len(t, csb.Stats, 1)
					require.Len(t, csb.Stats[0].Stats, 4) // stats on 4 spans
					sort.Slice(csb.Stats[0].Stats, func(i, j int) bool {
						return csb.Stats[0].Stats[i].Name < csb.Stats[0].Stats[j].Name
					})
				}
			}
		case sp2 := <-out2:
			if len(sp2.Stats) > 0 {
				payload2 = sp2
				for _, csb := range sp2.Stats {
					require.Len(t, csb.Stats, 1)
					require.Len(t, csb.Stats[0].Stats, 4) // stats on 4 spans
					sort.Slice(csb.Stats[0].Stats, func(i, j int) bool {
						return csb.Stats[0].Stats[i].Name < csb.Stats[0].Stats[j].Name
					})
				}
			}
		}
	}

	if diff := cmp.Diff(
		payload1,
		payload2,
		protocmp.Transform(),
		// OTLPTracesToConcentratorInputs adds container tags to ClientStatsPayload, other fields should match.
		protocmp.IgnoreFields(&pb.ClientStatsPayload{}, "tags")); diff != "" {
		t.Errorf("Diff between APM stats received:\n%v", diff)
	}
	require.ElementsMatch(t, payload2.Stats[0].Tags, []string{"kube_container_name:k8s_container", "container_id:test_cid"})
}

func getTraceAgentCfg(attributesTranslator *attributes.Translator) *traceconfig.AgentConfig {
	acfg := traceconfig.New()
	acfg.OTLPReceiver.AttributesTranslator = attributesTranslator
	acfg.ComputeStatsBySpanKind = true
	acfg.PeerTagsAggregation = true
	acfg.Features["enable_otlp_compute_top_level_by_span_kind"] = struct{}{}
	return acfg
}

var (
	traceID = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID1 = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	spanID2 = [8]byte{2, 2, 3, 4, 5, 6, 7, 8}
	spanID3 = [8]byte{3, 2, 3, 4, 5, 6, 7, 8}
	spanID4 = [8]byte{4, 2, 3, 4, 5, 6, 7, 8}
)

func getTestTraces() ptrace.Traces {
	traces := ptrace.NewTraces()
	rspan := traces.ResourceSpans().AppendEmpty()
	rattrs := rspan.Resource().Attributes()
	rattrs.PutStr(string(semconv.ContainerIDKey), "test_cid")
	rattrs.PutStr(string(semconv.ServiceNameKey), "test_SerVIce!@#$%")
	rattrs.PutStr(string(semconv.DeploymentEnvironmentKey), "teSt_eNv^&*()")
	rattrs.PutStr(string(semconv.K8SContainerNameKey), "k8s_container")

	sspan := rspan.ScopeSpans().AppendEmpty()

	root := sspan.Spans().AppendEmpty()
	root.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	root.SetKind(ptrace.SpanKindClient)
	root.SetName("root")
	root.SetTraceID(traceID)
	root.SetSpanID(spanID1)
	rootattrs := root.Attributes()
	rootattrs.PutStr("resource.name", "test_resource")
	rootattrs.PutStr("operation.name", "test_opeR@aT^&*ion")
	rootattrs.PutInt(string(semconv.HTTPStatusCodeKey), 404)
	rootattrs.PutStr(string(semconv.PeerServiceKey), "test_peer_svc")
	rootattrs.PutStr(string(semconv.DBSystemKey), "redis")
	rootattrs.PutStr(string(semconv.DBStatementKey), "SET key value")
	root.Status().SetCode(ptrace.StatusCodeError)

	child1 := sspan.Spans().AppendEmpty()
	child1.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	child1.SetKind(ptrace.SpanKindServer) // OTel spans with SpanKindServer are top-level
	child1.SetName("child1")
	child1.SetTraceID(traceID)
	child1.SetSpanID(spanID2)
	child1.SetParentSpanID(spanID1)
	child1attrs := child1.Attributes()
	child1attrs.PutInt(string(semconv.HTTPStatusCodeKey), 200)
	child1attrs.PutStr(string(semconv.HTTPMethodKey), "GET")
	child1attrs.PutStr(string(semconv.HTTPRouteKey), "/home")
	child1.Status().SetCode(ptrace.StatusCodeError)
	child1.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now()))

	child2 := sspan.Spans().AppendEmpty()
	child2.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	child2.SetKind(ptrace.SpanKindProducer) // OTel spans with SpanKindProducer get APM stats
	child2.SetName("child2")
	child2.SetTraceID(traceID)
	child2.SetSpanID(spanID3)
	child2.SetParentSpanID(spanID1)
	child2attrs := child2.Attributes()
	child2attrs.PutStr(string(semconv.RPCMethodKey), "test_method")
	child2attrs.PutStr(string(semconv.RPCServiceKey), "test_rpc_svc")
	child2.Status().SetCode(ptrace.StatusCodeError)
	child2.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now()))

	child3 := sspan.Spans().AppendEmpty()
	child3.SetStartTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	child3.SetKind(ptrace.SpanKindInternal)
	child3.SetName("child3")
	child3.SetTraceID(traceID)
	child3.SetSpanID(spanID4)
	child3.SetParentSpanID(spanID1)
	child3.Attributes().PutInt("_dd.measured", 1) // _dd.measured forces the span to get APM stats despite having span kind internal
	child3.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now()))

	root.SetEndTimestamp(pcommon.NewTimestampFromTime(time.Now()))

	return traces
}

// newTestObfuscator creates a new obfuscator for testing
func newTestObfuscator(conf *traceconfig.AgentConfig) *obfuscate.Obfuscator {
	oconf := conf.Obfuscation.Export(conf)
	oconf.Redis.Enabled = true
	o := obfuscate.NewObfuscator(oconf)
	return o
}
