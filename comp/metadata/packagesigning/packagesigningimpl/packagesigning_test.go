// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

package packagesigningimpl

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/hostname/hostnameimpl"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	pkgUtils "github.com/DataDog/datadog-agent/comp/metadata/packagesigning/utils"
	"github.com/DataDog/datadog-agent/pkg/serializer"
	serializermock "github.com/DataDog/datadog-agent/pkg/serializer/mocks"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
)

func TestGetAPTPayload(t *testing.T) {
	setupAPTSigningMock(t)

	expectedMetadata := &signingMetadata{
		SigningKeys: []signingKey{
			{Fingerprint: "F1068E14E09422B3", ExpirationDate: "2022-06-28", KeyType: "signed-by", Repositories: []pkgUtils.Repository{{Name: "https://apt.datadoghq.com/ stable 7"}}},
			{Fingerprint: "FD4BF915", ExpirationDate: "9999-12-31", KeyType: "trusted"},
		},
	}

	ih := getTestPackageSigning(t)

	p := ih.getPayload().(*Payload)
	assert.Equal(t, expectedMetadata, p.Metadata)
}

func TestGetYUMPayload(t *testing.T) {
	setupYUMSigningMock(t)

	expectedMetadata := &signingMetadata{
		SigningKeys: []signingKey{
			{Fingerprint: "AL1C1AK3YS", ExpirationDate: "9999-12-31", KeyType: "repo", Repositories: []pkgUtils.Repository{{Name: "https://yum.datadoghq.com/stable/7/x86_64/"}}},
			{Fingerprint: "733142A241337", ExpirationDate: "2030-03-02", KeyType: "rpm"},
		},
	}

	ih := getTestPackageSigning(t)

	p := ih.getPayload().(*Payload)
	assert.Equal(t, expectedMetadata, p.Metadata)
}

func setupAPTSigningMock(t *testing.T) {
	t.Cleanup(func() {
		getPkgManager = pkgUtils.GetPackageManager
		getAPTKeys = getAPTSignatureKeys
		getYUMKeys = getYUMSignatureKeys
	})

	getPkgManager = getPackageAPTMock
	getAPTKeys = getAPTKeysMock
}
func setupYUMSigningMock(t *testing.T) {
	setupAPTSigningMock(t)

	getPkgManager = getPackageYUMMock
	getYUMKeys = getYUMKeysMock
}
func getPackageAPTMock() string { return "apt" }
func getPackageYUMMock() string { return "yum" }
func getAPTKeysMock(_ *http.Client, _ log.Component) []signingKey {
	return []signingKey{
		{Fingerprint: "F1068E14E09422B3", ExpirationDate: "2022-06-28", KeyType: "signed-by", Repositories: []pkgUtils.Repository{{Name: "https://apt.datadoghq.com/ stable 7"}}},
		{Fingerprint: "FD4BF915", ExpirationDate: "9999-12-31", KeyType: "trusted"},
	}
}
func getYUMKeysMock(_ string, _ *http.Client, _ log.Component) []signingKey {
	return []signingKey{
		{Fingerprint: "AL1C1AK3YS", ExpirationDate: "9999-12-31", KeyType: "repo", Repositories: []pkgUtils.Repository{{Name: "https://yum.datadoghq.com/stable/7/x86_64/"}}},
		{Fingerprint: "733142A241337", ExpirationDate: "2030-03-02", KeyType: "rpm"},
	}
}

func getTestPackageSigning(t *testing.T) *pkgSigning {
	p := newPackageSigningProvider(
		fxutil.Test[dependencies](
			t,
			fx.Provide(func() log.Component { return logmock.New(t) }),
			config.MockModule(),
			fx.Provide(func() serializer.MetricSerializer { return serializermock.NewMetricSerializer(t) }),
			hostnameimpl.MockModule(),
		),
	)
	return p.Comp.(*pkgSigning)
}

func TestCheckInstallationMethod(t *testing.T) {
	testCases := []struct {
		name   string
		input  string
		output bool
	}{
		{
			name:   "Allowed method",
			input:  "saltstack",
			output: true,
		},
		{
			name:   "Forbidden method exact match",
			input:  "helm",
			output: false,
		},
		{
			name:   "Forbidden method partial match",
			input:  "https://github.com/honeyscience/kube-cluster-config/",
			output: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {

			isAllowed := isAllowedInstallationTool(testCase.input)
			if isAllowed != testCase.output {
				t.Errorf("Wrong value for installation method %s, expected %t get %t", testCase.input, testCase.output, isAllowed)
			}
		})

	}
}
