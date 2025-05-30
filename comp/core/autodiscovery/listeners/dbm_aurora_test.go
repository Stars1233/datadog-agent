// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2020-present Datadog, Inc.

//go:build ec2

package listeners

import (
	"context"
	"errors"
	"testing"
	"time"

	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/databasemonitoring/aurora"
	"github.com/DataDog/datadog-agent/pkg/databasemonitoring/aws"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBMAuroraListener(t *testing.T) {
	testCases := []struct {
		name                  string
		config                aurora.Config
		numDiscoveryIntervals int
		rdsClientConfigurer   mockRdsClientConfigurer
		expectedServices      []*DBMAuroraService
		expectedDelServices   []*DBMAuroraService
	}{
		{
			name: "GetAuroraClustersFromTags context deadline exceeded produces no services",
			config: aurora.Config{
				DiscoveryInterval: 1,
				QueryTimeout:      1,
				Region:            "us-east-1",
				Tags:              []string{defaultADTag},
				DbmTag:            defaultDbmTag,
			},
			numDiscoveryIntervals: 0,
			rdsClientConfigurer: func(k *aws.MockRdsClient) {
				k.EXPECT().GetAuroraClustersFromTags(contextWithTimeout(1*time.Second), []string{defaultADTag}).DoAndReturn(
					func(ctx context.Context, _ []string) ([]string, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					}).AnyTimes()
			},
			expectedServices:    []*DBMAuroraService{},
			expectedDelServices: []*DBMAuroraService{},
		},
		{
			name: "GetAuroraClusterEndpoints context deadline exceeded produces no services",
			config: aurora.Config{
				DiscoveryInterval: 1,
				QueryTimeout:      1,
				Region:            "us-east-1",
				Tags:              []string{defaultADTag},
				DbmTag:            defaultDbmTag,
			},
			numDiscoveryIntervals: 0,
			rdsClientConfigurer: func(k *aws.MockRdsClient) {
				gomock.InOrder(
					k.EXPECT().GetAuroraClustersFromTags(gomock.Any(), []string{defaultADTag}).Return([]string{"my-cluster-1"}, nil).AnyTimes(),
					k.EXPECT().GetAuroraClusterEndpoints(contextWithTimeout(1*time.Second), []string{"my-cluster-1"}, defaultDbmTag).DoAndReturn(
						func(ctx context.Context, _ []string, _ string) (map[string]*aws.AuroraCluster, error) {
							<-ctx.Done()
							return nil, ctx.Err()
						}).AnyTimes(),
				)

			},
			expectedServices:    []*DBMAuroraService{},
			expectedDelServices: []*DBMAuroraService{},
		},
		{
			name: "GetAuroraClustersFromTags error produces no services",
			config: aurora.Config{
				DiscoveryInterval: 1,
				Region:            "us-east-1",
				Tags:              []string{defaultADTag},
				DbmTag:            defaultDbmTag,
			},
			numDiscoveryIntervals: 0,
			rdsClientConfigurer: func(k *aws.MockRdsClient) {
				k.EXPECT().GetAuroraClustersFromTags(gomock.Any(), []string{defaultADTag}).Return(nil, errors.New("big bad error")).AnyTimes()
			},
			expectedServices:    []*DBMAuroraService{},
			expectedDelServices: []*DBMAuroraService{},
		},
		{
			name: "GetAuroraClusterEndpoints error produces no services",
			config: aurora.Config{
				DiscoveryInterval: 1,
				Region:            "us-east-1",
				Tags:              []string{defaultADTag},
				DbmTag:            defaultDbmTag,
			},
			numDiscoveryIntervals: 0,
			rdsClientConfigurer: func(k *aws.MockRdsClient) {
				gomock.InOrder(
					k.EXPECT().GetAuroraClustersFromTags(gomock.Any(), []string{defaultADTag}).Return([]string{"my-cluster-1"}, nil).AnyTimes(),
					k.EXPECT().GetAuroraClusterEndpoints(gomock.Any(), []string{"my-cluster-1"}, defaultDbmTag).Return(nil, errors.New("big bad error")).AnyTimes(),
				)
			},
			expectedServices:    []*DBMAuroraService{},
			expectedDelServices: []*DBMAuroraService{},
		},
		{
			name: "single endpoint discovered and created",
			config: aurora.Config{
				DiscoveryInterval: 1,
				Region:            "us-east-1",
				Tags:              []string{defaultADTag},
				DbmTag:            defaultDbmTag,
			},
			numDiscoveryIntervals: 1,
			rdsClientConfigurer: func(k *aws.MockRdsClient) {
				k.EXPECT().GetAuroraClustersFromTags(gomock.Any(), []string{defaultADTag}).Return([]string{"my-cluster-1"}, nil).AnyTimes()
				k.EXPECT().GetAuroraClusterEndpoints(gomock.Any(), []string{"my-cluster-1"}, defaultDbmTag).Return(
					map[string]*aws.AuroraCluster{
						"my-cluster-1": {
							Instances: []*aws.Instance{
								{
									Endpoint:   "my-endpoint",
									Port:       5432,
									IamEnabled: true,
									Engine:     "aurora-postgresql",
									DbmEnabled: true,
								},
							},
						},
					}, nil).AnyTimes()
			},
			expectedServices: []*DBMAuroraService{
				{
					adIdentifier: dbmPostgresAuroraADIdentifier,
					entityID:     "f7fee36c58e3da8a",
					checkName:    "postgres",
					clusterID:    "my-cluster-1",
					region:       "us-east-1",
					instance: &aws.Instance{
						ID:         "",
						Endpoint:   "my-endpoint",
						Port:       5432,
						IamEnabled: true,
						Engine:     "aurora-postgresql",
						DbmEnabled: true,
					},
				},
			},
			expectedDelServices: []*DBMAuroraService{},
		},
		{
			name: "multiple endpoints discovered from single cluster and created",
			config: aurora.Config{
				DiscoveryInterval: 1,
				Region:            "us-east-1",
				Tags:              []string{defaultADTag},
				DbmTag:            defaultDbmTag,
			},
			numDiscoveryIntervals: 1,
			rdsClientConfigurer: func(k *aws.MockRdsClient) {
				k.EXPECT().GetAuroraClustersFromTags(gomock.Any(), []string{defaultADTag}).Return([]string{"my-cluster-1"}, nil).AnyTimes()
				k.EXPECT().GetAuroraClusterEndpoints(gomock.Any(), []string{"my-cluster-1"}, defaultDbmTag).Return(
					map[string]*aws.AuroraCluster{
						"my-cluster-1": {
							Instances: []*aws.Instance{
								{
									Endpoint:   "my-endpoint",
									Port:       5432,
									IamEnabled: true,
									Engine:     "aurora-postgresql",
								},
								{
									Endpoint:   "foo-endpoint",
									Port:       5432,
									IamEnabled: true,
									Engine:     "aurora-postgresql",
								},
								{
									Endpoint:   "bar-endpoint",
									Port:       5444,
									IamEnabled: false,
									Engine:     "aurora-postgresql",
								},
							},
						},
					}, nil).AnyTimes()
			},
			expectedServices: []*DBMAuroraService{
				{
					adIdentifier: dbmPostgresAuroraADIdentifier,
					entityID:     "f7fee36c58e3da8a",
					checkName:    "postgres",
					clusterID:    "my-cluster-1",
					region:       "us-east-1",
					instance: &aws.Instance{
						ID:         "",
						Endpoint:   "my-endpoint",
						Port:       5432,
						IamEnabled: true,
						Engine:     "aurora-postgresql",
					},
				},
				{
					adIdentifier: dbmPostgresAuroraADIdentifier,
					entityID:     "509dbfd2cc1ae2be",
					checkName:    "postgres",
					clusterID:    "my-cluster-1",
					region:       "us-east-1",
					instance: &aws.Instance{
						ID:         "",
						Endpoint:   "foo-endpoint",
						Port:       5432,
						IamEnabled: true,
						Engine:     "aurora-postgresql",
					},
				},
				{
					adIdentifier: dbmPostgresAuroraADIdentifier,
					entityID:     "cc92e57c9b7b7531",
					checkName:    "postgres",
					clusterID:    "my-cluster-1",
					region:       "us-east-1",
					instance: &aws.Instance{
						ID:         "",
						Endpoint:   "bar-endpoint",
						Port:       5444,
						IamEnabled: false,
						Engine:     "aurora-postgresql",
					},
				},
			},
			expectedDelServices: []*DBMAuroraService{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			newSvc := make(chan Service, 10)
			delSvc := make(chan Service, 10)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockConfig := configmock.New(t)
			mockConfig.SetWithoutSource("autodiscover_aurora_clusters", map[string]interface{}{
				"discovery_interval": tc.config.DiscoveryInterval,
				"query_timeout":      tc.config.QueryTimeout,
				"region":             tc.config.Region,
				"tags":               tc.config.Tags,
				"dbm_tag":            tc.config.DbmTag,
			})
			mockAWSClient := aws.NewMockRdsClient(ctrl)
			tc.rdsClientConfigurer(mockAWSClient)
			ticks := make(chan time.Time, 1)
			l := newDBMAuroraListener(tc.config, mockAWSClient, ticks)
			l.Listen(newSvc, delSvc)
			// execute loop
			for i := 0; i < tc.numDiscoveryIntervals; i++ {
				ticks <- time.Now()
			}

			// shutdown service, and get output from channels
			l.Stop()
			close(newSvc)
			close(delSvc)

			// assert that the expected new services were created
			createdServices := make([]*DBMAuroraService, 0)
			for newService := range newSvc {
				dbmAuroraService, ok := newService.(*DBMAuroraService)
				if !ok {
					require.Fail(t, "received service of incorrect type on service chan")
				}
				createdServices = append(createdServices, dbmAuroraService)
			}
			assert.Equal(t, len(tc.expectedServices), len(createdServices))
			assert.ElementsMatch(t, tc.expectedServices, createdServices)

			// assert that the expected deleted services were created
			deletedServices := make([]*DBMAuroraService, 0)
			for delService := range delSvc {
				dbmAuroraService, ok := delService.(*DBMAuroraService)
				if !ok {
					require.Fail(t, "received service of incorrect type on service chan")
				}
				deletedServices = append(deletedServices, dbmAuroraService)
			}
			assert.Equal(t, len(tc.expectedDelServices), len(deletedServices))
			assert.ElementsMatch(t, tc.expectedDelServices, deletedServices)
		})
	}
}

func TestGetExtraAuroraConfig(t *testing.T) {
	testCases := []struct {
		service       *DBMAuroraService
		expectedExtra map[string]string
	}{
		{
			service: &DBMAuroraService{
				adIdentifier: dbmPostgresAuroraADIdentifier,
				entityID:     "f7fee36c58e3da8a",
				checkName:    "postgres",
				clusterID:    "my-cluster-1",
				region:       "us-east-1",
				instance: &aws.Instance{
					ID:         "",
					Endpoint:   "my-endpoint",
					Port:       5432,
					IamEnabled: true,
					Engine:     "aurora-postgresql",
					DbName:     "app",
					DbmEnabled: true,
				},
			},
			expectedExtra: map[string]string{
				"dbname":                         "app",
				"region":                         "us-east-1",
				"managed_authentication_enabled": "true",
				"dbclusteridentifier":            "my-cluster-1",
				"dbm":                            "true",
			},
		},
	}

	for _, tc := range testCases {
		for key, value := range tc.expectedExtra {
			v, err := tc.service.GetExtraConfig(key)
			assert.NoError(t, err)
			assert.Equal(t, value, v)
		}
	}
}
