// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

package utils

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/version"
)

func Test_CopyStrings(t *testing.T) {
	tags := []string{"aa", "bb"}
	newTags := CopyStrings(tags)
	assert.Equal(t, tags, newTags)
	assert.NotEqual(t, fmt.Sprintf("%p", tags), fmt.Sprintf("%p", newTags))
	assert.NotEqual(t, fmt.Sprintf("%p", &tags[0]), fmt.Sprintf("%p", &newTags[0]))
}

func Test_BoolToFloat64(t *testing.T) {
	assert.Equal(t, BoolToFloat64(true), 1.0)
	assert.Equal(t, BoolToFloat64(false), 0.0)
}

func Test_getAgentTags(t *testing.T) {
	mockConfig := configmock.New(t)
	mockConfig.SetWithoutSource("hostname", "my-host")

	assert.Equal(t, []string{
		"agent_host:my-host",
		"agent_version:" + version.AgentVersion,
	}, GetCommonAgentTags())
}
