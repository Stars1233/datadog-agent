// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022-present Datadog, Inc.

//go:build !linux || serverless

package api

import (
	"context"
	"net"
	"net/http"

	"github.com/DataDog/datadog-agent/comp/core/tagger/origindetection"
	"github.com/DataDog/datadog-agent/pkg/trace/api/internal/header"
)

// connContext is unimplemented for non-linux builds.
func connContext(ctx context.Context, _ net.Conn) context.Context {
	return ctx
}

// IDProvider implementations are able to look up a container ID given a ctx and http header.
type IDProvider interface {
	GetContainerID(context.Context, http.Header) string
}

type idProvider struct{}

// NewIDProvider initializes an IDProvider instance, in non-linux environments the procRoot arg is unused.
func NewIDProvider(_ string, _ func(originInfo origindetection.OriginInfo) (string, error)) IDProvider {
	return &idProvider{}
}

// GetContainerID returns the container ID from the http header.
func (*idProvider) GetContainerID(_ context.Context, h http.Header) string {
	return h.Get(header.ContainerID)
}
