// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package runner

import (
	"strconv"
	"testing"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
	"github.com/benbjohnson/clock"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"go.uber.org/fx"

	model "github.com/DataDog/agent-payload/v5/process"
	mockStatsd "github.com/DataDog/datadog-go/v5/statsd/mocks"

	"github.com/DataDog/datadog-agent/comp/core/config"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	"github.com/DataDog/datadog-agent/comp/process/forwarders"
	"github.com/DataDog/datadog-agent/comp/process/forwarders/forwardersimpl"
	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	pkgconfigmodel "github.com/DataDog/datadog-agent/pkg/config/model"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/process/util/api/headers"
	"github.com/DataDog/datadog-agent/pkg/util/flavor"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/version"
)

func TestNewCollectorQueueSize(t *testing.T) {
	tests := []struct {
		name              string
		override          bool
		queueSize         int
		expectedQueueSize int
	}{
		{
			name:              "default queue size",
			override:          false,
			queueSize:         42,
			expectedQueueSize: pkgconfigsetup.DefaultProcessQueueSize,
		},
		{
			name:              "valid queue size override",
			override:          true,
			queueSize:         42,
			expectedQueueSize: 42,
		},
		{
			name:              "invalid negative queue size override",
			override:          true,
			queueSize:         -10,
			expectedQueueSize: pkgconfigsetup.DefaultProcessQueueSize,
		},
		{
			name:              "invalid 0 queue size override",
			override:          true,
			queueSize:         0,
			expectedQueueSize: pkgconfigsetup.DefaultProcessQueueSize,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockConfig := configmock.New(t)
			if tc.override {
				mockConfig.SetWithoutSource("process_config.queue_size", tc.queueSize)
			}
			deps := newSubmitterDepsWithConfig(t, mockConfig)
			c, err := NewSubmitter(mockConfig, deps.Log, deps.Forwarders, deps.Statsd, testHostName)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedQueueSize, c.processResults.MaxSize())
		})
	}
}

func TestNewCollectorRTQueueSize(t *testing.T) {
	tests := []struct {
		name              string
		override          bool
		queueSize         int
		expectedQueueSize int
	}{
		{
			name:              "default queue size",
			override:          false,
			queueSize:         2,
			expectedQueueSize: pkgconfigsetup.DefaultProcessRTQueueSize,
		},
		{
			name:              "valid queue size override",
			override:          true,
			queueSize:         2,
			expectedQueueSize: 2,
		},
		{
			name:              "invalid negative size override",
			override:          true,
			queueSize:         -2,
			expectedQueueSize: pkgconfigsetup.DefaultProcessRTQueueSize,
		},
		{
			name:              "invalid 0 queue size override",
			override:          true,
			queueSize:         0,
			expectedQueueSize: pkgconfigsetup.DefaultProcessRTQueueSize,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockConfig := configmock.New(t)
			if tc.override {
				mockConfig.SetWithoutSource("process_config.rt_queue_size", tc.queueSize)
			}
			deps := newSubmitterDepsWithConfig(t, mockConfig)
			c, err := NewSubmitter(mockConfig, deps.Log, deps.Forwarders, deps.Statsd, testHostName)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedQueueSize, c.rtProcessResults.MaxSize())
		})
	}
}

func TestNewCollectorProcessQueueBytes(t *testing.T) {
	tests := []struct {
		name              string
		override          bool
		queueBytes        int
		expectedQueueSize int
	}{
		{
			name:              "default queue size",
			override:          false,
			queueBytes:        42000,
			expectedQueueSize: pkgconfigsetup.DefaultProcessQueueBytes,
		},
		{
			name:              "valid queue size override",
			override:          true,
			queueBytes:        42000,
			expectedQueueSize: 42000,
		},
		{
			name:              "invalid negative queue size override",
			override:          true,
			queueBytes:        -2,
			expectedQueueSize: pkgconfigsetup.DefaultProcessQueueBytes,
		},
		{
			name:              "invalid 0 queue size override",
			override:          true,
			queueBytes:        0,
			expectedQueueSize: pkgconfigsetup.DefaultProcessQueueBytes,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockConfig := configmock.New(t)
			if tc.override {
				mockConfig.SetWithoutSource("process_config.process_queue_bytes", tc.queueBytes)
			}
			deps := newSubmitterDepsWithConfig(t, mockConfig)
			s, err := NewSubmitter(mockConfig, deps.Log, deps.Forwarders, deps.Statsd, testHostName)
			assert.NoError(t, err)
			assert.Equal(t, int64(tc.expectedQueueSize), s.processResults.MaxWeight())
			assert.Equal(t, int64(tc.expectedQueueSize), s.rtProcessResults.MaxWeight())
			assert.Equal(t, tc.expectedQueueSize, s.forwarderRetryMaxQueueBytes)
		})
	}
}

func TestCollectorMessagesToCheckResult(t *testing.T) {
	originalFlavor := flavor.GetFlavor()
	defer flavor.SetFlavor(originalFlavor)
	flavor.SetFlavor(flavor.ProcessAgent)

	deps := newSubmitterDeps(t)
	submitter, err := NewSubmitter(deps.Config, deps.Log, deps.Forwarders, deps.Statsd, testHostName)
	assert.NoError(t, err)

	now := time.Now()
	agentVersion, _ := version.Agent()

	requestID := submitter.getRequestID(now, 0)

	tests := []struct {
		name          string
		message       model.MessageBody
		expectHeaders map[string]string
	}{
		{
			name: "process",
			message: &model.CollectorProc{
				Containers: []*model.Container{
					{}, {}, {},
				},
			},
			expectHeaders: map[string]string{
				headers.TimestampHeader:      strconv.Itoa(int(now.Unix())),
				headers.HostHeader:           testHostName,
				headers.ProcessVersionHeader: agentVersion.GetNumber(),
				headers.ContainerCountHeader: "3",
				headers.ContentTypeHeader:    headers.ProtobufContentType,
				headers.RequestIDHeader:      requestID,
				headers.AgentStartTime:       strconv.Itoa(int(submitter.agentStartTime)),
				headers.PayloadSource:        "process_agent",
			},
		},
		{
			name: "rt_process",
			message: &model.CollectorRealTime{
				ContainerStats: []*model.ContainerStat{
					{}, {}, {},
				},
			},
			expectHeaders: map[string]string{
				headers.TimestampHeader:      strconv.Itoa(int(now.Unix())),
				headers.HostHeader:           testHostName,
				headers.ProcessVersionHeader: agentVersion.GetNumber(),
				headers.ContainerCountHeader: "3",
				headers.ContentTypeHeader:    headers.ProtobufContentType,
				headers.AgentStartTime:       strconv.Itoa(int(submitter.agentStartTime)),
				headers.PayloadSource:        "process_agent",
			},
		},
		{
			name: "container",
			message: &model.CollectorContainer{
				Containers: []*model.Container{
					{}, {},
				},
			},
			expectHeaders: map[string]string{
				headers.TimestampHeader:      strconv.Itoa(int(now.Unix())),
				headers.HostHeader:           testHostName,
				headers.ProcessVersionHeader: agentVersion.GetNumber(),
				headers.ContainerCountHeader: "2",
				headers.ContentTypeHeader:    headers.ProtobufContentType,
				headers.AgentStartTime:       strconv.Itoa(int(submitter.agentStartTime)),
				headers.PayloadSource:        "process_agent",
			},
		},
		{
			name: "rt_container",
			message: &model.CollectorContainerRealTime{
				Stats: []*model.ContainerStat{
					{}, {}, {}, {}, {},
				},
			},
			expectHeaders: map[string]string{
				headers.TimestampHeader:      strconv.Itoa(int(now.Unix())),
				headers.HostHeader:           testHostName,
				headers.ProcessVersionHeader: agentVersion.GetNumber(),
				headers.ContainerCountHeader: "5",
				headers.ContentTypeHeader:    headers.ProtobufContentType,
				headers.AgentStartTime:       strconv.Itoa(int(submitter.agentStartTime)),
				headers.PayloadSource:        "process_agent",
			},
		},
		{
			name:    "process_discovery",
			message: &model.CollectorProcDiscovery{},
			expectHeaders: map[string]string{
				headers.TimestampHeader:      strconv.Itoa(int(now.Unix())),
				headers.HostHeader:           testHostName,
				headers.ProcessVersionHeader: agentVersion.GetNumber(),
				headers.ContainerCountHeader: "0",
				headers.ContentTypeHeader:    headers.ProtobufContentType,
				headers.AgentStartTime:       strconv.Itoa(int(submitter.agentStartTime)),
				headers.PayloadSource:        "process_agent",
			},
		},
		{
			name:    "process_events",
			message: &model.CollectorProcEvent{},
			expectHeaders: map[string]string{
				headers.TimestampHeader:        strconv.Itoa(int(now.Unix())),
				headers.HostHeader:             testHostName,
				headers.ProcessVersionHeader:   agentVersion.GetNumber(),
				headers.ContainerCountHeader:   "0",
				headers.ContentTypeHeader:      headers.ProtobufContentType,
				headers.EVPOriginHeader:        "process-agent",
				headers.EVPOriginVersionHeader: version.AgentVersion,
				headers.AgentStartTime:         strconv.Itoa(int(submitter.agentStartTime)),
				headers.PayloadSource:          "process_agent",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := []model.MessageBody{
				test.message,
			}
			result := submitter.messagesToCheckResult(now, test.name, messages)
			assert.Equal(t, test.name, result.name)
			assert.Len(t, result.payloads, 1)
			payload := result.payloads[0]
			assert.Len(t, payload.headers, len(test.expectHeaders))
			for k, v := range test.expectHeaders {
				assert.Equal(t, v, payload.headers.Get(k))
			}
		})
	}
}

func Test_getRequestID(t *testing.T) {
	deps := newSubmitterDeps(t)
	s, err := NewSubmitter(deps.Config, deps.Log, deps.Forwarders, deps.Statsd, testHostName)
	assert.NoError(t, err)

	fixedDate1 := time.Date(2022, 9, 1, 0, 0, 1, 0, time.Local)
	id1 := s.getRequestID(fixedDate1, 1)
	id2 := s.getRequestID(fixedDate1, 1)
	// The calculation should be deterministic, so making sure the parameters generates the same id.
	assert.Equal(t, id1, id2)
	fixedDate2 := time.Date(2022, 9, 1, 0, 0, 2, 0, time.Local)
	id3 := s.getRequestID(fixedDate2, 1)

	// The request id is based on time, so if the difference it only the time, then the new ID should be greater.
	id1Num, _ := strconv.ParseUint(id1, 10, 64)
	id3Num, _ := strconv.ParseUint(id3, 10, 64)
	assert.Greater(t, id3Num, id1Num)

	// Increasing the chunk index should increase the id.
	id4 := s.getRequestID(fixedDate2, 3)
	id4Num, _ := strconv.ParseUint(id4, 10, 64)
	assert.Equal(t, id3Num+2, id4Num)

	// Changing the host -> changing the hash.
	s.hostname = "host2"
	s.requestIDCachedHash = nil
	id5 := s.getRequestID(fixedDate1, 1)
	assert.NotEqual(t, id1, id5)
}

func TestSubmitterHeartbeatProcess(t *testing.T) {
	originalFlavor := flavor.GetFlavor()
	defer flavor.SetFlavor(originalFlavor)
	flavor.SetFlavor(flavor.ProcessAgent)

	ctrl := gomock.NewController(t)
	statsdClient := mockStatsd.NewMockClientInterface(ctrl)
	statsdClient.EXPECT().Gauge("datadog.process.agent", float64(1), gomock.Any(), float64(1)).MinTimes(1)

	deps := newSubmitterDeps(t)
	s, err := NewSubmitter(deps.Config, deps.Log, deps.Forwarders, statsdClient, testHostName)
	assert.NoError(t, err)
	mockedClock := clock.NewMock()
	s.clock = mockedClock
	s.Start()
	mockedClock.Add(15 * time.Second)
	s.Stop()
}

func TestSubmitterHeartbeatCore(t *testing.T) {
	originalFlavor := flavor.GetFlavor()
	defer flavor.SetFlavor(originalFlavor)
	flavor.SetFlavor(flavor.DefaultAgent)

	ctrl := gomock.NewController(t)
	statsdClient := mockStatsd.NewMockClientInterface(ctrl)
	statsdClient.EXPECT().Gauge("datadog.process.agent", float64(1), gomock.Any(), float64(1)).Times(0)

	deps := newSubmitterDeps(t)
	s, err := NewSubmitter(deps.Config, deps.Log, deps.Forwarders, statsdClient, testHostName)
	assert.NoError(t, err)
	mockedClock := clock.NewMock()
	s.clock = mockedClock
	s.Start()
	mockedClock.Add(15 * time.Second)
	s.Stop()
}

type submitterDeps struct {
	fx.In
	Config     config.Component
	Log        log.Component
	Forwarders forwarders.Component
	Statsd     statsd.ClientInterface
}

func newSubmitterDeps(t *testing.T) submitterDeps {
	return fxutil.Test[submitterDeps](t, getForwardersMockModules(t, nil))
}

func newSubmitterDepsWithConfig(t *testing.T, config pkgconfigmodel.Config) submitterDeps {
	overrides := config.AllSettings()
	return fxutil.Test[submitterDeps](t, getForwardersMockModules(t, overrides))
}

func getForwardersMockModules(t *testing.T, configOverrides map[string]interface{}) fx.Option {
	return fx.Options(
		config.MockModule(),
		fx.Replace(config.MockParams{Overrides: configOverrides}),
		forwardersimpl.MockModule(),
		fx.Provide(func() log.Component {
			return logmock.New(t)
		}),
		fx.Provide(func() statsd.ClientInterface {
			return &statsd.NoOpClient{}
		}),
	)
}
