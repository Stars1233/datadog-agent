// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build orchestrator

package processors

import (
	"fmt"
	"runtime/debug"

	"github.com/pkg/errors"

	"github.com/DataDog/datadog-agent/pkg/orchestrator"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// NewMarshallingError creates an error that wraps the cause of a marshalling
// error.
func NewMarshallingError(cause error) error {
	return errors.WithMessage(cause, "unable to marshal resource to JSON")
}

// RecoverOnPanic is used to recover panics triggered by processors.
func RecoverOnPanic() {
	if r := recover(); r != nil {
		stack := debug.Stack()
		log.Errorc(fmt.Sprintf("unable to process resources (panic!): %s", stack), orchestrator.ExtraLogContext)
	}
}
