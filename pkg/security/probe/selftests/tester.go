// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package selftests holds selftests related files
package selftests

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/atomic"

	"github.com/DataDog/datadog-agent/pkg/security/config"
	"github.com/DataDog/datadog-agent/pkg/security/probe"
	"github.com/DataDog/datadog-agent/pkg/security/proto/api"
	"github.com/DataDog/datadog-agent/pkg/security/secl/compiler/eval"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model"
	"github.com/DataDog/datadog-agent/pkg/security/secl/rules"
	"github.com/DataDog/datadog-agent/pkg/security/serializers"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	logRateLimit = time.Minute
)

// SelfTest represent one self test
type SelfTest interface {
	GetRuleDefinition() *rules.RuleDefinition
	GenerateEvent(_ context.Context) error
	HandleEvent(selfTestEvent)
	IsSuccess() bool
}

// SelfTester represents all the state needed to conduct rule injection test at startup
type SelfTester struct {
	sync.Mutex

	config          *config.RuntimeSecurityConfig
	waitingForEvent *atomic.Bool
	eventChan       chan selfTestEvent
	probe           *probe.Probe
	success         []eval.RuleID
	fails           []eval.RuleID
	lastTimestamp   time.Time
	selfTests       []SelfTest
	tmpDir          string
	isClosed        bool
	done            chan bool
	selfTestRunning chan time.Duration
	errorTimestamp  map[eval.RuleID]time.Time
}

var _ rules.PolicyProvider = (*SelfTester)(nil)

// RunSelfTest runs the self test and return the result
func (t *SelfTester) RunSelfTest(ctx context.Context, timeout time.Duration) error {
	t.Lock()
	defer t.Unlock()

	if err := t.beginSelfTests(timeout); err != nil {
		return err
	}

	for _, selfTest := range t.selfTests {
		// allow 10 seconds for the self test event to be generated
		ctx, cancelFnc := context.WithTimeout(ctx, 10*time.Second)
		if err := selfTest.GenerateEvent(ctx); err != nil {
			if time.Since(t.errorTimestamp[selfTest.GetRuleDefinition().ID]) > logRateLimit {
				log.Errorf("self test failed (%s): %v", selfTest.GetRuleDefinition().ID, err)

				t.errorTimestamp[selfTest.GetRuleDefinition().ID] = time.Now()
			}
		}
		cancelFnc()
	}

	return nil
}

// Start implements the policy provider interface
func (t *SelfTester) Start() {}

// GetStatus returns the result of the last performed self tests
func (t *SelfTester) GetStatus() *api.SelfTestsStatus {
	t.Lock()
	defer t.Unlock()

	return &api.SelfTestsStatus{
		LastTimestamp: t.lastTimestamp.Format(time.RFC822),
		Success:       t.success,
		Fails:         t.fails,
	}
}

// CreateTargetDir creates temporary directory
func CreateTargetDir() (string, error) {
	// Create temp directory to put target file in
	tmpDir, err := os.MkdirTemp("", "datadog_agent_cws_self_test")
	if err != nil {
		return "", err
	}
	return tmpDir, nil
}

// WaitForResult wait for self test results
func (t *SelfTester) WaitForResult(cb func(success []eval.RuleID, fails []eval.RuleID)) {
	for timeout := range t.selfTestRunning {
		timer := time.After(timeout)

		var (
			success []string
			fails   []string
			events  = make(map[eval.RuleID]*serializers.EventSerializer)
		)

	LOOP:
		for {
			select {
			case <-t.done:
				return
			case event := <-t.eventChan:
				t.Lock()
				for _, selfTest := range t.selfTests {
					if !selfTest.IsSuccess() {
						selfTest.HandleEvent(event)

						if selfTest.IsSuccess() {
							id := selfTest.GetRuleDefinition().ID
							events[id] = event.Event
						}
					}
				}
				t.Unlock()

				// all test passed
				if len(events) == len(t.selfTests) {
					break LOOP
				}
			case <-timer:
				break LOOP
			}
		}

		t.Lock()
		for _, selfTest := range t.selfTests {
			id := selfTest.GetRuleDefinition().ID

			if _, ok := events[id]; ok {
				success = append(success, id)
			} else {
				fails = append(fails, id)
			}
		}
		t.success, t.fails, t.lastTimestamp = success, fails, time.Now()
		t.Unlock()

		cb(success, fails)

		t.endSelfTests()
	}
}

// Close removes temp directories and files used by the self tester
func (t *SelfTester) Close() error {
	t.Lock()
	defer t.Unlock()

	t.isClosed = true
	close(t.selfTestRunning)
	close(t.done)

	if t.tmpDir != "" {
		err := os.RemoveAll(t.tmpDir)
		t.tmpDir = ""
		return err
	}
	return nil
}

// LoadPolicies implements the PolicyProvider interface
func (t *SelfTester) LoadPolicies(_ []rules.MacroFilter, _ []rules.RuleFilter) ([]*rules.Policy, *multierror.Error) {
	t.Lock()
	defer t.Unlock()

	policyDef := &rules.PolicyDef{
		Version: policyVersion,
		Rules:   make([]*rules.RuleDefinition, len(t.selfTests)),
	}

	for i, selfTest := range t.selfTests {
		policyDef.Rules[i] = selfTest.GetRuleDefinition()
	}

	pInfo := &rules.PolicyInfo{
		Name:       policyName,
		Source:     policySource,
		Type:       rules.SelftestPolicy,
		IsInternal: true,
	}

	policy, err := rules.LoadPolicyFromDefinition(pInfo, policyDef, nil, nil)
	if err != nil {
		return nil, multierror.Append(nil, err)
	}

	return []*rules.Policy{policy}, nil
}

func (t *SelfTester) beginSelfTests(timeout time.Duration) error {
	// t.Lock is held here
	if t.isClosed {
		return nil
	}

	select {
	case t.selfTestRunning <- timeout:
	default:
		return fmt.Errorf("channel is already full, self test is already running")
	}
	t.waitingForEvent.Store(true)

	return nil
}

func (t *SelfTester) endSelfTests() {
	t.waitingForEvent.Store(false)
}

type selfTestEvent struct {
	RuleID   eval.RuleID
	Filepath string
	Event    *serializers.EventSerializer
}

// IsExpectedEvent sends an event to the tester
func (t *SelfTester) IsExpectedEvent(rule *rules.Rule, event eval.Event, _ *probe.Probe) bool {
	if t.waitingForEvent.Load() && rule.Policy.Source == policySource {
		ev, ok := event.(*model.Event)
		if !ok {
			return true
		}

		s := serializers.NewEventSerializer(ev, rule)
		if s == nil {
			return false
		}

		selfTestEvent := selfTestEvent{
			RuleID: rule.ID,
			Event:  s,
		}

		select {
		case t.eventChan <- selfTestEvent:
		default:
			log.Debug("self test channel is full, discarding event.")
		}

		return true
	}
	return false
}
