// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build !windows

package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/config/model"
)

func initMockConf(t *testing.T) (model.Config, string) {
	testDir := t.TempDir()

	f, err := os.CreateTemp(testDir, "fake-datadog-yaml-")
	require.NoError(t, err, fmt.Errorf("%v", err))
	t.Cleanup(func() {
		f.Close()
	})

	mockConfig := configmock.New(t)
	mockConfig.SetConfigFile(f.Name())
	mockConfig.SetWithoutSource("auth_token", "")

	return mockConfig, filepath.Join(testDir, "auth_token")
}

func TestCreateOrFetchAuthTokenValidGen(t *testing.T) {
	config, expectTokenPath := initMockConf(t)
	token, err := FetchOrCreateAuthToken(context.Background(), config)
	require.NoError(t, err, fmt.Sprintf("%v", err))
	assert.True(t, len(token) > authTokenMinimalLen, fmt.Sprintf("%d", len(token)))
	_, err = os.Stat(expectTokenPath)
	require.NoError(t, err)
}

func TestFetchAuthToken(t *testing.T) {
	config, expectTokenPath := initMockConf(t)

	token, err := FetchAuthToken(config)
	require.NotNil(t, err)
	require.Equal(t, "", token)
	_, err = os.Stat(expectTokenPath)
	require.True(t, os.IsNotExist(err))

	newToken, err := FetchOrCreateAuthToken(context.Background(), config)
	require.NoError(t, err, fmt.Sprintf("%v", err))
	require.True(t, len(newToken) > authTokenMinimalLen, fmt.Sprintf("%d", len(newToken)))
	_, err = os.Stat(expectTokenPath)
	require.NoError(t, err)

	token, err = FetchAuthToken(config)
	require.NoError(t, err, fmt.Sprintf("%v", err))
	require.Equal(t, newToken, token)
}
