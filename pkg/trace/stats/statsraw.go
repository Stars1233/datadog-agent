// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package stats

import (
	"math"
	"math/rand"

	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	"github.com/DataDog/datadog-agent/pkg/trace/log"

	"github.com/golang/protobuf/proto"

	"github.com/DataDog/sketches-go/ddsketch"
)

const (
	// relativeAccuracy is the value accuracy we have on the percentiles. For example, we can
	// say that p99 is 100ms +- 1ms
	relativeAccuracy = 0.01
	// maxNumBins is the maximum number of bins of the ddSketch we use to store percentiles.
	// It can affect relative accuracy, but in practice, 2048 bins is enough to have 1% relative accuracy from
	// 80 micro second to 1 year: http://www.vldb.org/pvldb/vol12/p2195-masson.pdf
	maxNumBins = 2048
)

// Most "algorithm" stuff here is tested with stats_test.go as what is important
// is that the final data, the one with send after a call to Export(), is correct.

type groupedStats struct {
	// using float64 here to avoid the accumulation of rounding issues.
	hits            float64
	topLevelHits    float64
	errors          float64
	duration        float64
	okDistribution  *ddsketch.DDSketch
	errDistribution *ddsketch.DDSketch
	peerTags        []string
}

// round a float to an int, uniformly choosing
// between the lower and upper approximations.
func round(f float64) uint64 {
	i := uint64(f)
	if rand.Float64() < f-float64(i) {
		i++
	}
	return i
}

func (s *groupedStats) export(a Aggregation) (*pb.ClientGroupedStats, error) {
	msg := s.okDistribution.ToProto()
	okSummary, err := proto.Marshal(msg)
	if err != nil {
		return &pb.ClientGroupedStats{}, err
	}
	msg = s.errDistribution.ToProto()
	errSummary, err := proto.Marshal(msg)
	if err != nil {
		return &pb.ClientGroupedStats{}, err
	}
	return &pb.ClientGroupedStats{
		Service:        a.Service,
		Name:           a.Name,
		Resource:       a.Resource,
		HTTPStatusCode: a.StatusCode,
		Type:           a.Type,
		Hits:           round(s.hits),
		Errors:         round(s.errors),
		Duration:       round(s.duration),
		TopLevelHits:   round(s.topLevelHits),
		OkSummary:      okSummary,
		ErrorSummary:   errSummary,
		Synthetics:     a.Synthetics,
		SpanKind:       a.SpanKind,
		PeerTags:       s.peerTags,
		IsTraceRoot:    a.IsTraceRoot,
		GRPCStatusCode: a.GRPCStatusCode,
	}, nil
}

func newGroupedStats() *groupedStats {
	okSketch, err := ddsketch.LogCollapsingLowestDenseDDSketch(relativeAccuracy, maxNumBins)
	if err != nil {
		log.Errorf("Error when creating ddsketch: %v", err)
	}
	errSketch, err := ddsketch.LogCollapsingLowestDenseDDSketch(relativeAccuracy, maxNumBins)
	if err != nil {
		log.Errorf("Error when creating ddsketch: %v", err)
	}
	return &groupedStats{
		okDistribution:  okSketch,
		errDistribution: errSketch,
	}
}

// RawBucket is used to compute span data and aggregate it
// within a time-framed bucket. This should not be used outside
// the agent, use ClientStatsBucket for this.
type RawBucket struct {
	// This should really have no public fields. At all.

	start    uint64 // timestamp of start in our format
	duration uint64 // duration of a bucket in nanoseconds

	// this should really remain private as it's subject to refactoring
	data map[Aggregation]*groupedStats

	containerTagsByID map[string][]string // a map from container ID to container tags
	processTagsByHash map[uint64]string   // a map from process hash to process tags
}

// NewRawBucket opens a new calculation bucket for time ts and initializes it properly
func NewRawBucket(ts, d uint64) *RawBucket {
	// The only non-initialized value is the Duration which should be set by whoever closes that bucket
	return &RawBucket{
		start:             ts,
		duration:          d,
		data:              make(map[Aggregation]*groupedStats),
		containerTagsByID: make(map[string][]string),
		processTagsByHash: make(map[uint64]string),
	}
}

// Export transforms a RawBucket into a ClientStatsBucket, typically used
// before communicating data to the API, as RawBucket is the internal
// type while ClientStatsBucket is the public, shared one.
func (sb *RawBucket) Export() map[PayloadAggregationKey]*pb.ClientStatsBucket {
	m := make(map[PayloadAggregationKey]*pb.ClientStatsBucket)
	for k, v := range sb.data {
		b, err := v.export(k)
		if err != nil {
			log.Errorf("Dropping stats bucket due to encoding error: %v.", err)
			continue
		}
		key := PayloadAggregationKey{
			Hostname:        k.Hostname,
			Version:         k.Version,
			Env:             k.Env,
			ContainerID:     k.ContainerID,
			GitCommitSha:    k.GitCommitSha,
			ImageTag:        k.ImageTag,
			Lang:            k.Lang,
			ProcessTagsHash: k.ProcessTagsHash,
		}
		s, ok := m[key]
		if !ok {
			s = &pb.ClientStatsBucket{
				Start:    sb.start,
				Duration: sb.duration,
			}
		}
		s.Stats = append(s.Stats, b)
		m[key] = s
	}
	return m
}

// HandleSpan adds the span to this bucket stats, aggregated with the finest grain matching given aggregators
func (sb *RawBucket) HandleSpan(s *StatSpan, weight float64, origin string, aggKey PayloadAggregationKey) {
	if aggKey.Env == "" {
		panic("env should never be empty")
	}
	aggr := NewAggregationFromSpan(s, origin, aggKey)
	sb.add(s, weight, aggr)
}

func (sb *RawBucket) add(s *StatSpan, weight float64, aggr Aggregation) {
	var gs *groupedStats
	var ok bool

	if gs, ok = sb.data[aggr]; !ok {
		gs = newGroupedStats()
		gs.peerTags = s.matchingPeerTags
		sb.data[aggr] = gs
	}
	if s.isTopLevel {
		gs.topLevelHits += weight
	}
	gs.hits += weight
	if s.error != 0 {
		gs.errors += weight
	}
	gs.duration += float64(s.duration) * weight
	// alter resolution of duration distro
	trundur := nsTimestampToFloat(s.duration)
	if s.error != 0 {
		if err := gs.errDistribution.Add(trundur); err != nil {
			log.Debugf("Error adding error distribution stats: %v", err)
		}
	} else {
		if err := gs.okDistribution.Add(trundur); err != nil {
			log.Debugf("Error adding distribution stats: %v", err)
		}
	}
}

// nsTimestampToFloat converts a nanosec timestamp into a float nanosecond timestamp truncated to a fixed precision
func nsTimestampToFloat(ns int64) float64 {
	b := math.Float64bits(float64(ns))
	// IEEE-754
	// the mask include 1 bit sign 11 bits exponent (0xfff)
	// then we filter the mantissa to 10bits (0xff8) (9 bits as it has implicit value of 1)
	// 10 bits precision (any value will be +/- 1/1024)
	// https://en.wikipedia.org/wiki/Double-precision_floating-point_format
	b &= 0xfffff80000000000
	return math.Float64frombits(b)
}
