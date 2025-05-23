// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf

package postgres

import (
	"errors"

	"github.com/DataDog/sketches-go/ddsketch"

	"github.com/DataDog/datadog-agent/pkg/network/protocols"
	"github.com/DataDog/datadog-agent/pkg/network/types"
	"github.com/DataDog/datadog-agent/pkg/process/util"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// Key is an identifier for a group of Postgres transactions
type Key struct {
	Operation  Operation
	Parameters string
	types.ConnectionKey
}

// NewKey creates a new postgres key
func NewKey(saddr, daddr util.Address, sport, dport uint16, operation Operation, parameters string) Key {
	return Key{
		ConnectionKey: types.NewConnectionKey(saddr, daddr, sport, dport),
		Operation:     operation,
		Parameters:    parameters,
	}
}

// RequestStat represents a group of Postgres transactions that has a shared key.
type RequestStat struct {
	// this field order is intentional to help the GC pointer tracking
	Latencies          *ddsketch.DDSketch
	FirstLatencySample float64
	Count              int
	StaticTags         uint64
}

// CombineWith merges the data in 2 RequestStats objects
// newStats is kept as it is, while the method receiver gets mutated
func (r *RequestStat) CombineWith(newStats *RequestStat) {
	r.Count += newStats.Count
	r.StaticTags |= newStats.StaticTags
	// If the receiver has no latency sample, use the newStats sample
	if r.FirstLatencySample == 0 {
		r.FirstLatencySample = newStats.FirstLatencySample
	}
	// If newStats has no ddsketch latency, we have nothing to merge
	if newStats.Latencies == nil {
		return
	}
	// If the receiver has no ddsketch latency, use the newStats latency
	if r.Latencies == nil {
		r.Latencies = newStats.Latencies.Copy()
	} else if newStats.Latencies != nil {
		// Merge the ddsketch latencies
		if err := r.Latencies.MergeWith(newStats.Latencies); err != nil {
			log.Debugf("could not add request latency to ddsketch: %v", err)
		}
	}
}

func (r *RequestStat) initSketch() error {
	latencies := protocols.SketchesPool.Get()
	if latencies == nil {
		return errors.New("error recording postgres transaction latency: could not create new ddsketch")
	}
	r.Latencies = latencies
	return nil
}

// Close cleans up the RequestStat
func (r *RequestStat) Close() {
	if r.Latencies != nil {
		r.Latencies.Clear()
		protocols.SketchesPool.Put(r.Latencies)
	}
}
