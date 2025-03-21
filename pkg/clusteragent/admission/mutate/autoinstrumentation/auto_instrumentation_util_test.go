// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver

package autoinstrumentation

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	corev1 "k8s.io/api/core/v1"

	"github.com/DataDog/datadog-agent/comp/core/config"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	workloadmetafxmock "github.com/DataDog/datadog-agent/comp/core/workloadmeta/fx-mock"
	workloadmetamock "github.com/DataDog/datadog-agent/comp/core/workloadmeta/mock"
	"github.com/DataDog/datadog-agent/pkg/clusteragent/admission/mutate/common"
	"github.com/DataDog/datadog-agent/pkg/languagedetection/languagemodels"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
)

func TestGetOwnerNameAndKind(t *testing.T) {
	tests := []struct {
		name         string
		pod          *corev1.Pod
		expectedName string
		expectedKind string
		wantFound    bool
	}{
		{
			name:         "Pod with no parent",
			pod:          common.FakePod("orphan-pod"),
			expectedName: "",
			expectedKind: "",
			wantFound:    false,
		},
		{
			name: "Pod with replicaset parent, and no deployment grandparent",
			pod: common.FakePodSpec{
				NS:         "default",
				ParentKind: "replicaset",
				ParentName: "dummy-rs",
			}.Create(),
			expectedName: "dummy-rs",
			expectedKind: "ReplicaSet",
			wantFound:    true,
		},
		{
			name: "Pod with replicaset parent, and deployment grandparent",
			pod: common.FakePodSpec{
				NS:         "default",
				ParentKind: "replicaset",
				ParentName: "dummy-rs-12344",
			}.Create(),
			expectedName: "dummy-rs",
			expectedKind: "Deployment",
			wantFound:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, kind, found := getOwnerNameAndKind(tt.pod)
			require.Equal(t, found, tt.wantFound)
			require.Equal(t, name, tt.expectedName)
			require.Equal(t, kind, tt.expectedKind)
		})
	}
}

func assertEqualLibInjection(actualLibs []libInfo, expectedLibs []libInfo) bool {

	actualLibsAsSet := make(map[libInfo]struct{})
	expectedLibsAsSet := make(map[libInfo]struct{})

	for _, li := range actualLibs {
		actualLibsAsSet[li] = struct{}{}
	}

	for _, li := range expectedLibs {
		expectedLibsAsSet[li] = struct{}{}
	}

	return reflect.DeepEqual(actualLibsAsSet, expectedLibsAsSet)
}

func TestGetLibListFromDeploymentAnnotations(t *testing.T) {

	mockStore := fxutil.Test[workloadmetamock.Mock](t, fx.Options(
		fx.Provide(func() log.Component { return logmock.New(t) }),
		config.MockModule(),
		workloadmetafxmock.MockModule(workloadmeta.NewParams()),
	))

	//java, js, python, dotnet, ruby

	mockStore.Set(&workloadmeta.KubernetesDeployment{
		EntityID: workloadmeta.EntityID{
			Kind: workloadmeta.KindKubernetesDeployment,
			ID:   "default/dummy",
		},
		InjectableLanguages: languagemodels.ContainersLanguages{
			*languagemodels.NewContainer("container-1"): {"java": {}, "js": {}},
			*languagemodels.NewContainer("container-2"): {"python": {}},
		},
	})

	mockStore.Set(&workloadmeta.KubernetesDeployment{
		EntityID: workloadmeta.EntityID{
			Kind: workloadmeta.KindKubernetesDeployment,
			ID:   "custom/dummy",
		},
		InjectableLanguages: languagemodels.ContainersLanguages{
			*languagemodels.NewContainer("container-1"): {"ruby": {}, "python": {}},
			*languagemodels.NewContainer("container-2"): {"java": {}},
		},
	})

	tests := []struct {
		name            string
		deploymentName  string
		namespace       string
		registry        string
		expectedLibList []libInfo
	}{
		{
			name:            "Deployment with no annotations",
			deploymentName:  "deployment-no-annotations",
			namespace:       "default",
			registry:        "",
			expectedLibList: []libInfo{},
		},
		{
			name:           "Deployment with some annotations in default namespace",
			deploymentName: "dummy",
			namespace:      "default",
			registry:       "registry",
			expectedLibList: []libInfo{
				java.defaultLibInfo("registry", "container-1"),
				js.defaultLibInfo("registry", "container-1"),
				python.defaultLibInfo("registry", "container-2"),
			},
		},
		{
			name:           "Deployment with some annotations in custom namespace",
			deploymentName: "dummy",
			namespace:      "custom",
			registry:       "registry",
			expectedLibList: []libInfo{
				ruby.defaultLibInfo("registry", "container-1"),
				python.defaultLibInfo("registry", "container-1"),
				java.defaultLibInfo("registry", "container-2"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			libList := getLibListFromDeploymentAnnotations(mockStore, tt.deploymentName, tt.namespace, tt.registry)
			if !assertEqualLibInjection(libList, tt.expectedLibList) {
				t.Fatalf("Expected %s, got %s", tt.expectedLibList, libList)
			}
		})
	}
}
