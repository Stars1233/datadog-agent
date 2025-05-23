// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux && functionaltests

// Package tests holds tests related files
package tests

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/DataDog/datadog-agent/pkg/security/ebpf/kernel"
	"github.com/DataDog/datadog-agent/pkg/security/events"
	"github.com/DataDog/datadog-agent/pkg/security/probe"
	cgroupModel "github.com/DataDog/datadog-agent/pkg/security/resolvers/cgroup/model"
	"github.com/DataDog/datadog-agent/pkg/security/secl/containerutils"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model"
	"github.com/DataDog/datadog-agent/pkg/security/secl/rules"
	"github.com/DataDog/datadog-agent/pkg/security/security_profile/profile"
	"github.com/DataDog/datadog-agent/pkg/security/utils"
	"github.com/DataDog/datadog-agent/pkg/util/ktime"
)

func TestSecurityProfile(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "open", "syscalls", "dns", "bind"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)
	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                  true,
		activityDumpRateLimiter:             200,
		activityDumpTracedCgroupsCount:      3,
		activityDumpDuration:                testActivityDumpDuration,
		activityDumpLocalStorageDirectory:   outputDir,
		activityDumpLocalStorageCompression: false,
		activityDumpLocalStorageFormats:     expectedFormats,
		activityDumpTracedEventTypes:        testActivityDumpTracedEventTypes,
		enableSecurityProfile:               true,
		securityProfileDir:                  outputDir,
		securityProfileWatchDir:             true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("security-profile-metadata", func(t *testing.T) {
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}

		validateActivityDumpOutputs(t, test, expectedFormats, dump.OutputFiles, nil,
			func(sp *profile.Profile) bool {
				if sp.Metadata.Name != dump.Name {
					t.Errorf("Profile name %s != %s\n", sp.Metadata.Name, dump.Name)
				}
				if (sp.Metadata.ContainerID != dump.ContainerID) &&
					(sp.Metadata.CGroupContext.CGroupID != dump.CGroupID) {
					t.Errorf("Profile containerID %s != %s\n", sp.Metadata.ContainerID, dump.ContainerID)
				}

				ctx := sp.GetVersionContextIndex(0)
				if ctx == nil {
					t.Errorf("No profile context found!")
				} else {
					if !slices.Contains(ctx.Tags, "container_id:"+string(dump.ContainerID)) {
						t.Errorf("Profile did not contains container_id tag: %v\n", ctx.Tags)
					}
					if !slices.Contains(ctx.Tags, "image_tag:latest") {
						t.Errorf("Profile did not contains image_tag:latest %v\n", ctx.Tags)
					}
					found := false
					for _, tag := range ctx.Tags {
						if strings.HasPrefix(tag, "image_name:fake_ubuntu_") {
							found = true
							break
						}
					}
					if found == false {
						t.Errorf("Profile did not contains image_name tag: %v\n", ctx.Tags)
					}
				}
				return true
			})
	})

	t.Run("security-profile-process", func(t *testing.T) {
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}

		validateActivityDumpOutputs(t, test, expectedFormats, dump.OutputFiles, nil,
			func(sp *profile.Profile) bool {
				nodes := WalkActivityTree(sp.ActivityTree, func(node *ProcessNodeAndParent) bool {
					return node.Node.Process.FileEvent.PathnameStr == syscallTester
				})
				if nodes == nil {
					t.Fatal("Node not found in security profile")
				}
				if len(nodes) != 1 {
					t.Fatalf("Found %d nodes, expected only one.", len(nodes))
				}
				return true
			})
	})

	t.Run("security-profile-dns", func(t *testing.T) {
		checkKernelCompatibility(t, "RHEL, SLES and Oracle kernels", func(kv *kernel.Version) bool {
			// TODO: Oracle because we are missing offsets. See dns_test.go
			return kv.IsRH7Kernel() || kv.IsOracleUEKKernel() || kv.IsSLESKernel()
		})

		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}

		validateActivityDumpOutputs(t, test, expectedFormats, dump.OutputFiles, nil,
			func(sp *profile.Profile) bool {
				nodes := WalkActivityTree(sp.ActivityTree, func(node *ProcessNodeAndParent) bool {
					return node.Node.Process.Argv0 == "nslookup"
				})
				if nodes == nil {
					t.Fatal("Node not found in security profile")
				}
				if len(nodes) != 1 {
					t.Fatalf("Found %d nodes, expected only one.", len(nodes))
				}
				for name := range nodes[0].DNSNames {
					if name == "one.one.one.one" {
						return true
					}
				}
				t.Error("DNS req not found in security profile")
				return false
			})
	})
}

func TestAnomalyDetection(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "open", "syscalls", "dns", "bind"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)
	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          3,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              []string{"exec", "dns"},
		anomalyDetectionMinimumStablePeriodExec: time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  time.Second,
		anomalyDetectionWarmupPeriod:            time.Second,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("anomaly-detection-process", func(t *testing.T) {
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstance.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("anomaly-detection-process-negative", func(t *testing.T) {
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

		test.GetCustomEventSent(t, func() error {
			// don't do anything
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection.")
			return false
		}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-dns", func(t *testing.T) {
		checkKernelCompatibility(t, "RHEL, SLES and Oracle kernels", func(kv *kernel.Version) bool {
			// TODO: Oracle because we are missing offsets. See dns_test.go
			return kv.IsRH7Kernel() || kv.IsOracleUEKKernel() || kv.IsSLESKernel()
		})
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstance.Command("nslookup", []string{"google.com"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("anomaly-detection-dns-negative", func(t *testing.T) {
		checkKernelCompatibility(t, "RHEL, SLES and Oracle kernels", func(kv *kernel.Version) bool {
			// TODO: Oracle because we are missing offsets. See dns_test.go
			return kv.IsRH7Kernel() || kv.IsOracleUEKKernel() || kv.IsSLESKernel()
		})
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

		test.GetCustomEventSent(t, func() error {
			// don't do anything
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection.")
			return false
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
	})
}

func TestAnomalyDetectionWarmup(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "dns"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)
	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          3,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              []string{"exec", "dns"},
		anomalyDetectionMinimumStablePeriodExec: 0,
		anomalyDetectionMinimumStablePeriodDNS:  0,
		anomalyDetectionWarmupPeriod:            3 * time.Second,
		tagger:                                  NewFakeMonoTagger(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	err = test.StopAllActivityDumps()
	if err != nil {
		t.Fatal(err)
	}

	mainDockerInstance, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer mainDockerInstance.stop()

	cmd := mainDockerInstance.Command("nslookup", []string{"google.fr"}, []string{})
	cmd.CombinedOutput()
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	testDockerInstance1, _, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer testDockerInstance1.stop()

	t.Run("anomaly-detection-warmup-1", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance1.Command("nslookup", []string{"one.one.one.one"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection during warm up.")
			return false
		}, time.Second*5, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-warmed-up-autolearned-1", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance1.Command("nslookup", []string{"one.one.one.one"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection during warm up.")
			return false
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-warmed-up-not-autolearned-1", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance1.Command("nslookup", []string{"foo.baz"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Error(err)
		}
	})

	testDockerInstance2, _, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer testDockerInstance2.stop()

	t.Run("anomaly-detection-warmup-2", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance2.Command("nslookup", []string{"foo.baz"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection during warm up.")
			return false
		}, time.Second*5, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	// already sleep for timeout for warmup period + 2sec spare (5s)

	t.Run("anomaly-detection-warmed-up-autolearned-2", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance2.Command("nslookup", []string{"one.one.one.one"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection during warm up.")
			return false
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-warmed-up-autolearned-bis-2", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance2.Command("nslookup", []string{"foo.baz"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection during warm up.")
			return false
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-warmed-up-autolearned-bis-1", func(t *testing.T) {
		test.GetCustomEventSent(t, func() error {
			cmd := testDockerInstance1.Command("nslookup", []string{"foo.baz"}, []string{})
			cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error("Should not had receive any anomaly detection during warm up.")
			return false
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
	})
}

func TestSecurityProfileReinsertionPeriod(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "open", "syscalls", "dns", "bind"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          3,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              []string{"exec", "dns"},
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  10 * time.Second,
		anomalyDetectionWarmupPeriod:            10 * time.Second,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("anomaly-detection-reinsertion-process", func(t *testing.T) {
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstance.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-reinsertion-dns", func(t *testing.T) {
		checkKernelCompatibility(t, "RHEL, SLES and Oracle kernels", func(kv *kernel.Version) bool {
			// TODO: Oracle because we are missing offsets. See dns_test.go
			return kv.IsRH7Kernel() || kv.IsOracleUEKKernel() || kv.IsSLESKernel()
		})
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstance.Command("nslookup", []string{"google.fr"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("anomaly-detection-stable-period-process", func(t *testing.T) {
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second)  // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)
		time.Sleep(time.Second * 10) // waiting for the stable period

		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstance.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("anomaly-detection-stable-period-dns", func(t *testing.T) {
		checkKernelCompatibility(t, "RHEL, SLES and Oracle kernels", func(kv *kernel.Version) bool {
			// TODO: Oracle because we are missing offsets. See dns_test.go
			return kv.IsRH7Kernel() || kv.IsOracleUEKKernel() || kv.IsSLESKernel()
		})
		dockerInstance, dump, err := test.StartADockerGetDump()
		if err != nil {
			t.Fatal(err)
		}
		defer dockerInstance.stop()

		cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
		_, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

		err = test.StopActivityDump(dump.Name)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second)  // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)
		time.Sleep(time.Second * 10) // waiting for the stable period

		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstance.Command("nslookup", []string{"google.fr"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

}

func TestSecurityProfileAutoSuppression(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile", "protobuf"}
	var testActivityDumpTracedEventTypes = []string{"exec", "open", "syscalls", "dns", "bind"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)
	reinsertPeriod := time.Second
	rulesDef := []*rules.RuleDefinition{
		{
			ID:         "test_autosuppression_exec",
			Expression: `exec.file.name == "getconf"`,
			Tags:       map[string]string{"allow_autosuppression": "true"},
		},
		{
			ID:         "test_autosuppression_exec_2",
			Expression: `exec.file.name == "getent"`,
			Tags:       map[string]string{"allow_autosuppression": "true"},
		},
		{
			ID:         "test_autosuppression_dns",
			Expression: `dns.question.type == A && dns.question.name == "one.one.one.one"`,
			Tags:       map[string]string{"allow_autosuppression": "true"},
		},
		{
			ID:         "test_autosuppression_dns_2",
			Expression: `dns.question.type == A && dns.question.name == "foo.baz"`,
			Tags:       map[string]string{"allow_autosuppression": "true"},
		},
	}
	test, err := newTestModule(t, nil, rulesDef, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          3,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAutoSuppression:                   true,
		autoSuppressionEventTypes:               []string{"exec", "dns"},
		anomalyDetectionMinimumStablePeriodExec: reinsertPeriod,
		anomalyDetectionMinimumStablePeriodDNS:  reinsertPeriod,
		anomalyDetectionWarmupPeriod:            reinsertPeriod,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstance, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstance.stop()

	cmd := dockerInstance.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	t.Run("auto-suppression-process-signal", func(t *testing.T) {
		// check that we generate an event during profile learning phase
		err = test.GetEventSent(t, func() error {
			cmd := dockerInstance.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(rule *rules.Rule, event *model.Event) bool {
			return assertTriggeredRule(t, rule, "test_autosuppression_exec") &&
				assert.Equal(t, "getconf", event.ProcessContext.FileEvent.BasenameStr, "wrong exec file")
		}, time.Second*3, "test_autosuppression_exec")
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("auto-suppression-dns-signal", func(t *testing.T) {
		// check that we generate an event during profile learning phase
		err = test.GetEventSent(t, func() error {
			cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(rule *rules.Rule, event *model.Event) bool {
			return assertTriggeredRule(t, rule, "test_autosuppression_dns") &&
				assert.Equal(t, "nslookup", event.ProcessContext.Argv0, "wrong exec file")
		}, time.Second*3, "test_autosuppression_dns")
		if err != nil {
			t.Fatal(err)
		}
	})

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	t.Run("auto-suppression-process-suppression", func(t *testing.T) {
		// check we autosuppress signals
		err = test.GetEventSent(t, func() error {
			cmd := dockerInstance.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, event *model.Event) bool {
			if event.ProcessContext.ContainerID == containerutils.ContainerID(dump.ContainerID) {
				t.Error("Got a signal that should have been suppressed")
			}
			return false
		}, time.Second*3, "test_autosuppression_exec")
		if err != nil {
			if otherErr, ok := err.(ErrTimeout); !ok {
				t.Fatal(otherErr)
			}
		}
	})

	t.Run("auto-suppression-dns-suppression", func(t *testing.T) {
		// check we autosuppress signals
		err = test.GetEventSent(t, func() error {
			cmd := dockerInstance.Command("nslookup", []string{"one.one.one.one"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, event *model.Event) bool {
			if event.ProcessContext.ContainerID == containerutils.ContainerID(dump.ContainerID) {
				t.Error("Got a signal that should have been suppressed")
			}
			return false
		}, time.Second*3, "test_autosuppression_dns")
		if err != nil {
			if otherErr, ok := err.(ErrTimeout); !ok {
				t.Fatal(otherErr)
			}
		}
	})

	// let the profile became stable
	time.Sleep(reinsertPeriod)

	t.Run("auto-suppression-process-no-suppression", func(t *testing.T) {
		// check we don't autosuppress signals
		err = test.GetEventSent(t, func() error {
			cmd := dockerInstance.Command("getent", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(rule *rules.Rule, event *model.Event) bool {
			return assertTriggeredRule(t, rule, "test_autosuppression_exec_2") &&
				assert.Equal(t, "getent", event.ProcessContext.FileEvent.BasenameStr, "wrong exec file")
		}, time.Second*3, "test_autosuppression_exec_2")
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("auto-suppression-dns-no-suppression", func(t *testing.T) {
		// check we don't autosuppress signals
		err = test.GetEventSent(t, func() error {
			cmd := dockerInstance.Command("nslookup", []string{"foo.baz"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(rule *rules.Rule, event *model.Event) bool {
			return assertTriggeredRule(t, rule, "test_autosuppression_dns_2") &&
				assert.Equal(t, "nslookup", event.ProcessContext.Argv0, "wrong exec file")
		}, time.Second*3, "test_autosuppression_dns_2")
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSecurityProfileDifferentiateArgs(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)
	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          3,
		activityDumpCgroupDifferentiateArgs:     true,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              []string{"exec"},
		anomalyDetectionMinimumStablePeriodExec: time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  time.Second,
		anomalyDetectionWarmupPeriod:            time.Second,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	dockerInstance, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstance.stop()

	time.Sleep(time.Second * 1) // to ensure we did not get ratelimited
	cmd := dockerInstance.Command("/bin/date", []string{"-u"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	cmd = dockerInstance.Command("/bin/date", []string{"-R"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}

	// test profiling part
	validateActivityDumpOutputs(t, test, expectedFormats, dump.OutputFiles, nil, func(sp *profile.Profile) bool {
		nodes := WalkActivityTree(sp.ActivityTree, func(node *ProcessNodeAndParent) bool {
			if node.Node.Process.FileEvent.PathnameStr == "/bin/date" || node.Node.Process.Argv0 == "/bin/date" {
				if len(node.Node.Process.Argv) == 1 && slices.Contains([]string{"-u", "-R"}, node.Node.Process.Argv[0]) {
					return true
				}
			}
			return false
		})
		if len(nodes) != 2 {
			t.Fatalf("found %d nodes, expected two.", len(nodes))
		}
		processNodesFound := uint32(0)
		for _, node := range nodes {
			if len(node.Process.Argv) == 1 && node.Process.Argv[0] == "-u" {
				processNodesFound |= 1
			} else if len(node.Process.Argv) == 1 && node.Process.Argv[0] == "-R" {
				processNodesFound |= 2
			}
		}
		if processNodesFound != (1 | 2) {
			t.Fatalf("could not find processes with expected arguments: %d", processNodesFound)
		}
		return true
	})

	// test matching part
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)
	err = test.GetCustomEventSent(t, func() error {
		cmd := dockerInstance.Command("/bin/date", []string{"--help"}, []string{})
		_, err = cmd.CombinedOutput()
		return err
	}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
		return true
	}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSecurityProfileLifeCycleExecs(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "dns"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	fakeManualTagger := NewFakeManualTagger()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          10,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              testActivityDumpTracedEventTypes,
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  10 * time.Second,
		anomalyDetectionWarmupPeriod:            1 * time.Second,
		tagger:                                  fakeManualTagger,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstanceV1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV1.stop()

	cmd := dockerInstanceV1.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	// HERE: V1 is learning

	t.Run("life-cycle-v1-learning-new-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	selector := fakeManualTagger.GetContainerSelector(dockerInstanceV1.containerID)
	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.StableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is stable

	t.Run("life-cycle-v1-stable-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("getent", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "+",
	})
	dockerInstanceV2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV2.stop()

	// HERE: V1 is stable and V2 is learning

	t.Run("life-cycle-v2-learning-new-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("iconv", []string{"-l"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("life-cycle-v2-learning-v1-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("life-cycle-v1-stable-v2-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("iconv", []string{"-l"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.UnstableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is unstable and V2 is learning

	t.Run("life-cycle-v1-unstable-new-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("scanelf", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been discarded"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})
}

func TestSecurityProfileLifeCycleDNS(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "dns"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	fakeManualTagger := NewFakeManualTagger()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          10,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              testActivityDumpTracedEventTypes,
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  10 * time.Second,
		anomalyDetectionWarmupPeriod:            1 * time.Second,
		tagger:                                  fakeManualTagger,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstanceV1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV1.stop()

	cmd := dockerInstanceV1.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	// HERE: V1 is learning

	t.Run("life-cycle-v1-learning-new-dns", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.fr"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	time.Sleep(time.Second * 10) // waiting for the stable period

	// HERE: V1 is stable

	t.Run("life-cycle-v1-stable-dns-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.com"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	selector := fakeManualTagger.GetContainerSelector(dockerInstanceV1.containerID)
	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "+",
	})
	dockerInstanceV2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV2.stop()

	// HERE: V1 is stable and V2 is learning

	t.Run("life-cycle-v2-learning-new-dns-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("nslookup", []string{"google.es"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*3, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	// most of the time DNS events triggers twice, let the second be handled before continuing
	time.Sleep(time.Second)

	t.Run("life-cycle-v2-learning-v1-dns", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("nslookup", []string{"google.fr"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("life-cycle-v1-stable-v2-dns", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.es"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.UnstableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is unstable and V2 is learning

	t.Run("life-cycle-v1-unstable-new-dns", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.co.uk"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been discarded"))
			return false
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
	})
}

func TestSecurityProfileLifeCycleSyscall(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "syscalls"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	fakeManualResolver := NewFakeManualTagger()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                         true,
		activityDumpRateLimiter:                    200,
		activityDumpTracedCgroupsCount:             10,
		activityDumpDuration:                       testActivityDumpDuration,
		activityDumpLocalStorageDirectory:          outputDir,
		activityDumpLocalStorageCompression:        false,
		activityDumpLocalStorageFormats:            expectedFormats,
		activityDumpTracedEventTypes:               testActivityDumpTracedEventTypes,
		enableSecurityProfile:                      true,
		securityProfileDir:                         outputDir,
		securityProfileWatchDir:                    true,
		enableAnomalyDetection:                     true,
		anomalyDetectionEventTypes:                 testActivityDumpTracedEventTypes,
		anomalyDetectionMinimumStablePeriodExec:    10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:     10 * time.Second,
		anomalyDetectionDefaultMinimumStablePeriod: 10 * time.Second,
		anomalyDetectionWarmupPeriod:               1 * time.Second,
		tagger:                                     fakeManualResolver,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstanceV1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV1.stop()

	cmd := dockerInstanceV1.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile be loaded (5sec debounce + 1sec spare)

	// HERE: V1 is learning

	// Some syscall will be missing from the initial dump because they had no way to come back to user space
	// (i.e. no new syscall to flush the dirty entry + no new exec + no new exit)
	t.Run("life-cycle-v1-learning", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("sleep", []string{"1"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, event *events.CustomEvent) bool {
			// We shouldn't see anything: the profile is still learning
			data, _ := event.MarshalJSON()
			t.Error(fmt.Errorf("syscall anomaly detected when it should have been ignored: %s", string(data)))
			// we answer false on purpose: we might have 2 or more syscall anomaly events
			return false
		}, time.Second*2, model.SyscallsEventType, events.AnomalyDetectionRuleID)
	})

	time.Sleep(time.Second * 10) // waiting for the stable period

	// HERE: V1 is stable

	t.Run("life-cycle-v1-stable-no-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("sleep", []string{"1"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, event *events.CustomEvent) bool {
			// this time we shouldn't see anything new.
			data, _ := event.MarshalJSON()
			t.Error(fmt.Errorf("syscall anomaly detected when it should have been ignored: %s", string(data)))
			return false
		}, time.Second*2, model.SyscallsEventType, events.AnomalyDetectionRuleID)
	})

	t.Run("life-cycle-v1-stable-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			// this will generate new syscalls, and should therefore generate an anomaly
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.com"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(r *rules.Rule, _ *events.CustomEvent) bool {
			assert.Equal(t, events.AnomalyDetectionRuleID, r.Rule.ID, "wrong custom event rule ID")
			return true
		}, time.Second*3, model.SyscallsEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	selector := fakeManualResolver.GetContainerSelector(dockerInstanceV1.containerID)
	fakeManualResolver.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "+",
	})
	dockerInstanceV2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV2.stop()

	// HERE: V1 is stable and V2 is learning

	t.Run("life-cycle-v1-stable-v2-learning-anomaly", func(t *testing.T) {
		var gotSyscallsEvent bool
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("date", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(r *rules.Rule, _ *events.CustomEvent) bool {
			// we should see an anomaly that will be inserted in the profile
			assert.Equal(t, events.AnomalyDetectionRuleID, r.Rule.ID, "wrong custom event rule ID")
			gotSyscallsEvent = true
			// there may be multiple syscalls events
			return false
		}, time.Second*3, model.SyscallsEventType, events.AnomalyDetectionRuleID)
		if !gotSyscallsEvent {
			t.Fatal(err)
		}
	})

	t.Run("life-cycle-v1-stable-v2-learning-no-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("date", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, event *events.CustomEvent) bool {
			// this time we shouldn't see anything new.
			data, _ := event.MarshalJSON()
			t.Error(fmt.Errorf("syscall anomaly detected when it should have been ignored: %s", string(data)))
			return false
		}, time.Second*2, model.SyscallsEventType, events.AnomalyDetectionRuleID)
	})

	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.UnstableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is unstable and V2 is learning

	t.Run("life-cycle-v1-unstable-v2-learning", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.com"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, event *events.CustomEvent) bool {
			// We shouldn't see anything: the profile is unstable
			data, _ := event.MarshalJSON()
			t.Error(fmt.Errorf("syscall anomaly detected when it should have been ignored: %s", string(data)))
			// we answer false on purpose: we might have 2 or more syscall anomaly events
			return false
		}, time.Second*2, model.SyscallsEventType, events.AnomalyDetectionRuleID)
	})
}

func TestSecurityProfileLifeCycleEvictionProcess(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "dns"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	fakeManualTagger := NewFakeManualTagger()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          10,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              testActivityDumpTracedEventTypes,
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  10 * time.Second,
		anomalyDetectionWarmupPeriod:            1 * time.Second,
		tagger:                                  fakeManualTagger,
		securityProfileMaxImageTags:             2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstanceV1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV1.stop()

	cmd := dockerInstanceV1.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	// HERE: V1 is learning

	t.Run("life-cycle-eviction-process-v1-learning-new-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	selector := fakeManualTagger.GetContainerSelector(dockerInstanceV1.containerID)
	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.StableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is stable

	t.Run("life-cycle-eviction-process-v1-stable-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("getent", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "v2",
	})
	dockerInstanceV2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV2.stop()

	// HERE: V1 is stable and V2 is learning

	t.Run("life-cycle-eviction-process-v2-learning-new-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("iconv", []string{"-l"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "v3",
	})
	dockerInstanceV3, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV3.stop()

	// HERE: V1 is deleted, V2 is learning and V3 is learning

	t.Run("life-cycle-eviction-process-check-v1-evicted", func(t *testing.T) {
		versions, err := test.GetProfileVersions(selector.Image)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 2, len(versions))
		assert.True(t, slices.Contains(versions, selector.Tag+"v2"))
		assert.True(t, slices.Contains(versions, selector.Tag+"v3"))
		assert.False(t, slices.Contains(versions, selector.Tag))
	})

	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag+"v3", model.StableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is deleted, V2 is learning and V3 is stable

	t.Run("life-cycle-eviction-process-v1-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV3.Command("getconf", []string{"-a"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSecurityProfileLifeCycleEvictionDNS(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "dns"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	fakeManualTagger := NewFakeManualTagger()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          10,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              testActivityDumpTracedEventTypes,
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  10 * time.Second,
		anomalyDetectionWarmupPeriod:            1 * time.Second,
		tagger:                                  fakeManualTagger,
		securityProfileMaxImageTags:             2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstanceV1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV1.stop()

	cmd := dockerInstanceV1.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	// HERE: V1 is learning

	t.Run("life-cycle-eviction-dns-v1-learning-new-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.fr"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
	})

	selector := fakeManualTagger.GetContainerSelector(dockerInstanceV1.containerID)
	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.StableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is stable

	t.Run("life-cycle-eviction-dns-v1-stable-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("nslookup", []string{"google.com"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "v2",
	})
	dockerInstanceV2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV2.stop()

	// HERE: V1 is stable and V2 is learning

	t.Run("life-cycle-eviction-dns-v2-learning-new-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("nslookup", []string{"google.es"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "v3",
	})
	dockerInstanceV3, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV3.stop()

	// HERE: V1 is deleted, V2 is learning and V3 is learning

	t.Run("life-cycle-eviction-dns-check-v1-evicted", func(t *testing.T) {
		versions, err := test.GetProfileVersions(selector.Image)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 2, len(versions))
		assert.True(t, slices.Contains(versions, selector.Tag+"v2"))
		assert.True(t, slices.Contains(versions, selector.Tag+"v3"))
		assert.False(t, slices.Contains(versions, selector.Tag))
	})

	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag+"v3", model.StableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is deleted, V2 is learning and V3 is stable

	t.Run("life-cycle-eviction-dns-v1-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV3.Command("nslookup", []string{"google.fr"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.DNSEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSecurityProfileLifeCycleEvictionProcessUnstable(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec", "dns"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	fakeManualTagger := NewFakeManualTagger()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          10,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              testActivityDumpTracedEventTypes,
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionMinimumStablePeriodDNS:  10 * time.Second,
		anomalyDetectionWarmupPeriod:            1 * time.Second,
		tagger:                                  fakeManualTagger,
		securityProfileMaxImageTags:             2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	syscallTester, err := loadSyscallTester(t, test, "syscall_tester")
	if err != nil {
		t.Fatal(err)
	}

	dockerInstanceV1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV1.stop()

	cmd := dockerInstanceV1.Command(syscallTester, []string{"sleep", "1"}, []string{})
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // a quick sleep to let events to be added to the dump

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile to be loaded (5sec debounce + 1sec spare)

	// HERE: V1 is learning

	t.Run("life-cycle-eviction-process-unstable-v1-learning-new-process", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("getconf", []string{"-a"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been reinserted"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	selector := fakeManualTagger.GetContainerSelector(dockerInstanceV1.containerID)
	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag, model.UnstableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is unstable

	t.Run("life-cycle-eviction-process-unstable-v1-unstable", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV1.Command("getent", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been discarded"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "v2",
	})
	dockerInstanceV2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV2.stop()

	// HERE: V1 is unstable and V2 is learning

	t.Run("life-cycle-eviction-process-unstable-v2-learning", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV2.Command("iconv", []string{"-l"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been discarded"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	fakeManualTagger.SpecifyNextSelector(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   selector.Tag + "v3",
	})
	dockerInstanceV3, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstanceV3.stop()

	// HERE: V1 is deleted, V2 is learning and V3 is learning

	t.Run("life-cycle-eviction-process-unstable-v3-learning", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV3.Command("getconf", []string{"-a"}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			t.Error(errors.New("catch a custom event that should had been discarded"))
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
	})

	if err := test.SetProfileVersionState(&cgroupModel.WorkloadSelector{
		Image: selector.Image,
		Tag:   "*",
	}, selector.Tag+"v3", model.StableEventType); err != nil {
		t.Fatal(err)
	}

	// HERE: V1 is deleted, V2 is learning and V3 is stable

	t.Run("life-cycle-eviction-process-unstable-v3-process-anomaly", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			cmd := dockerInstanceV3.Command("getent", []string{}, []string{})
			_, _ = cmd.CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSecurityProfilePersistence(t *testing.T) {
	SkipIfNotAvailable(t)

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	var expectedFormats = []string{"profile"}
	var testActivityDumpTracedEventTypes = []string{"exec"}

	outputDir := t.TempDir()
	os.MkdirAll(outputDir, 0755)
	defer os.RemoveAll(outputDir)

	rulesDef := []*rules.RuleDefinition{
		{
			ID:         "test_autosuppression_exec",
			Expression: `exec.file.name == "getconf"`,
			Tags:       map[string]string{"allow_autosuppression": "true"},
		},
	}

	fakeManualTagger := NewFakeManualTagger()

	test, err := newTestModule(t, nil, rulesDef, withStaticOpts(testOpts{
		enableActivityDump:                      true,
		activityDumpRateLimiter:                 200,
		activityDumpTracedCgroupsCount:          3,
		activityDumpDuration:                    testActivityDumpDuration,
		activityDumpLocalStorageDirectory:       outputDir,
		activityDumpLocalStorageCompression:     false,
		activityDumpLocalStorageFormats:         expectedFormats,
		activityDumpTracedEventTypes:            testActivityDumpTracedEventTypes,
		enableSecurityProfile:                   true,
		securityProfileDir:                      outputDir,
		securityProfileWatchDir:                 true,
		enableAutoSuppression:                   true,
		autoSuppressionEventTypes:               []string{"exec"},
		enableAnomalyDetection:                  true,
		anomalyDetectionEventTypes:              []string{"exec"},
		anomalyDetectionMinimumStablePeriodExec: 10 * time.Second,
		anomalyDetectionWarmupPeriod:            1 * time.Second,
		tagger:                                  fakeManualTagger,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	dockerInstance1, dump, err := test.StartADockerGetDump()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstance1.stop()

	err = test.StopActivityDump(dump.Name)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // a quick sleep to let the profile be loaded (5sec debounce + 1sec spare)

	// add auto-suppression test event during reinsertion period
	_, err = dockerInstance1.Command("getconf", []string{"-a"}, []string{}).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}

	// add anomaly test event during reinsertion period
	_, err = dockerInstance1.Command("/bin/echo", []string{"aaa"}, []string{}).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second) // wait for the stable period
	_, err = dockerInstance1.Command("/bin/echo", []string{"aaa"}, []string{}).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second) // quick sleep to let the exec event state become stable

	// stop the container so that the profile gets persisted
	dockerInstance1.stop()

	// make sure the next instance has the same image name as the previous one
	fakeManualTagger.SpecifyNextSelector(fakeManualTagger.GetContainerSelector(dockerInstance1.containerID))
	dockerInstance2, err := test.StartADocker()
	if err != nil {
		t.Fatal(err)
	}
	defer dockerInstance2.stop()
	time.Sleep(10 * time.Second) // sleep to let the profile be loaded (directory provider debouncers)

	// check the profile is still applied, and events can be auto suppressed
	t.Run("persistence-autosuppression-check", func(t *testing.T) {
		err = test.GetEventSent(t, func() error {
			_, err := dockerInstance2.Command("getconf", []string{"-a"}, []string{}).CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *model.Event) bool {
			t.Error("Got an event that should have been suppressed")
			return false
		}, time.Second*3, "test_autosuppression_exec")
		if err != nil {
			if otherErr, ok := err.(ErrTimeout); !ok {
				t.Fatal(otherErr)
			}
		}
	})

	// check the profile is still applied, and anomaly events can be generated
	t.Run("persistence-anomaly-check", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			dockerInstance2.Command("getent", []string{}, []string{}).CombinedOutput()
			return nil
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return true
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			t.Fatal(err)
		}
	})

	// check the profile is still applied, and anomalies aren't generated for known events
	t.Run("persistence-no-anomaly-check", func(t *testing.T) {
		err = test.GetCustomEventSent(t, func() error {
			_, err := dockerInstance2.Command("/bin/echo", []string{"aaa"}, []string{}).CombinedOutput()
			return err
		}, func(_ *rules.Rule, _ *events.CustomEvent) bool {
			return false
		}, time.Second*2, model.ExecEventType, events.AnomalyDetectionRuleID)
		if err != nil {
			if otherErr, ok := err.(ErrTimeout); !ok {
				t.Fatal(otherErr)
			}
		}
	})
}

func generateSyscallTestProfile(timeResolver *ktime.Resolver, add ...model.Syscall) *profile.Profile {
	syscallProfile := profile.New(
		profile.WithWorkloadSelector(cgroupModel.WorkloadSelector{Image: "fake_ubuntu", Tag: "latest"}),
	)

	baseSyscalls := []uint32{
		5,   // SysFstat
		10,  // SysMprotect
		11,  // SysMunmap
		12,  // SysBrk
		13,  // SysRtSigaction
		14,  // SysRtSigprocmask
		15,  // SysRtSigreturn
		17,  // SysPread64
		24,  // SysSchedYield
		28,  // SysMadvise
		35,  // SysNanosleep
		39,  // SysGetpid
		56,  // SysClone
		63,  // SysUname
		72,  // SysFcntl
		79,  // SysGetcwd
		80,  // SysChdir
		97,  // SysGetrlimit
		102, // SysGetuid
		105, // SysSetuid
		106, // SysSetgid
		116, // SysSetgroups
		125, // SysCapget
		126, // SysCapset
		131, // SysSigaltstack
		137, // SysStatfs
		138, // SysFstatfs
		157, // SysPrctl
		158, // SysArchPrctl
		186, // SysGettid
		202, // SysFutex
		204, // SysSchedGetaffinity
		217, // SysGetdents64
		218, // SysSetTidAddress
		233, // SysEpollCtl
		234, // SysTgkill
		250, // SysKeyctl
		257, // SysOpenat
		262, // SysNewfstatat
		267, // SysReadlinkat
		273, // SysSetRobustList
		281, // SysEpollPwait
		290, // SysEventfd2
		291, // SysEpollCreate1
		293, // SysPipe2
		302, // SysPrlimit64
		317, // SysSeccomp
		321, // SysBpf
		334, // SysRseq
		435, // SysClone3
		439, // SysFaccessat2
	}

	syscalls := slices.Clone(baseSyscalls)
	for _, toAdd := range add {
		if !slices.Contains(syscalls, uint32(toAdd)) {
			syscalls = append(syscalls, uint32(toAdd))
		}
	}

	nowNano := uint64(timeResolver.ComputeMonotonicTimestamp(time.Now()))
	syscallProfile.AddVersionContext("latest", &profile.VersionContext{
		EventTypeState: make(map[model.EventType]*profile.EventTypeState),
		FirstSeenNano:  nowNano,
		LastSeenNano:   nowNano,
		Syscalls:       syscalls,
		Tags:           []string{"image_name:fake_ubuntu", "image_tag:latest"},
	})

	return syscallProfile
}

func checkExpectedSyscalls(t *testing.T, got []model.Syscall, expectedSyscalls []model.Syscall, eventReason model.SyscallDriftEventReason, testOutput map[model.SyscallDriftEventReason]bool) bool {
	for _, s := range expectedSyscalls {
		if !slices.Contains(got, s) {
			t.Logf("A %s syscall drift event was received with the wrong list of syscalls. Expected %v, got %v", eventReason, expectedSyscalls, got)
			return false
		}
	}
	if len(got) != len(expectedSyscalls) {
		t.Logf("A %s syscall drift event was received with additional syscalls. Expected %v, got %v", eventReason, expectedSyscalls, got)
		return false
	}
	testOutput[eventReason] = true

	// If all 3 reasons are OK, exit early
	return testOutput[model.ExecveReason] && testOutput[model.ExitReason] && testOutput[model.SyscallMonitorPeriodReason]
}

func TestSecurityProfileSyscallDrift(t *testing.T) {
	SkipIfNotAvailable(t)

	// this test is only available on amd64
	if utils.RuntimeArch() != "x64" {
		t.Skip("Skip test when not running on amd64")
	}

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	outputDir := t.TempDir()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		activityDumpSyscallMonitorPeriod:           3 * time.Second,
		anomalyDetectionDefaultMinimumStablePeriod: 1 * time.Second,
		anomalyDetectionEventTypes:                 []string{"exec", "syscalls"},
		anomalyDetectionWarmupPeriod:               1 * time.Second,
		enableSecurityProfile:                      true,
		enableAnomalyDetection:                     true,
		securityProfileDir:                         outputDir,
		tagger:                                     NewFakeMonoTagger(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	goSyscallTester, err := loadSyscallTester(t, test, "syscall_go_tester")
	if err != nil {
		t.Fatal(err)
	}

	var dockerInstance *dockerCmdWrapper
	dockerInstance, err = test.StartADocker()
	if err != nil {
		t.Fatalf("failed to start a Docker instance: %v", err)
	}

	testOutput := map[model.SyscallDriftEventReason]bool{
		model.ExecveReason:               false,
		model.ExitReason:                 false,
		model.SyscallMonitorPeriodReason: false,
	}

	t.Run("activity-dump-syscall-drift", func(t *testing.T) {
		if err = test.GetProbeEvent(func() error {
			manager := test.probe.PlatformProbe.(*probe.EBPFProbe).GetProfileManager()
			manager.AddProfile(generateSyscallTestProfile(test.probe.PlatformProbe.(*probe.EBPFProbe).Resolvers.TimeResolver))

			time.Sleep(1 * time.Second) // ensure the profile has time to be pushed kernel space

			// run the syscall drift test command
			cmd := dockerInstance.Command(goSyscallTester, []string{"-syscall-drift-test"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(event *model.Event) bool {
			if event.GetType() == "syscalls" {
				var expectedSyscalls []model.Syscall
				switch event.Syscalls.EventReason {
				case model.ExecveReason:
					// Context: this syscall drift event should be sent when `runc.XXXX` execs into the syscall tester.
					// Only basic syscalls performed to prepare the execution of the syscall tester, and that are
					// not in the profile should be here. This includes the Execve syscall itself
					expectedSyscalls = []model.Syscall{
						model.SysRead,
						model.SysWrite,
						model.SysClose,
						model.SysMmap,
						model.SysExecve,
					}
				case model.SyscallMonitorPeriodReason:
					// Context: this event should be sent by the openat syscall made by the syscall tester while creating the
					// temporary file. The openat syscall itself shouldn't be in the list, since it is already in the profile.
					// Thus, only basic syscalls performed during the start of the execution of the syscall tester, and that are
					// not in the profile should be here.
					expectedSyscalls = []model.Syscall{
						model.SysRead,
						model.SysClose,
						model.SysMmap,
					}
				case model.ExitReason:
					// Context: this event should be sent when the syscall tester exits and the last dirty syscall cache entry
					// is flushed to user space. This event should include only the file management syscalls that
					// are performed by the syscall tester after the sleep, and that aren't in the profile.
					expectedSyscalls = []model.Syscall{
						model.SysWrite,
						model.SysClose,
						model.SysExitGroup,
						model.SysUnlinkat,
					}
				default:
					t.Errorf("unknown syscall drift event reason: %v", event.Syscalls.EventReason)
					return false
				}

				return checkExpectedSyscalls(t, event.Syscalls.Syscalls, expectedSyscalls, event.Syscalls.EventReason, testOutput)
			}
			return false
		}, 20*time.Second); err != nil {
			t.Error(err)
		}

		// Make sure all 3 syscall drift events were received
		for key, value := range testOutput {
			if !value {
				t.Errorf("missing syscall drift event reason: %v", key)
			}
		}

		dockerInstance.stop()
	})
}

func TestSecurityProfileSyscallDriftExecExitInProfile(t *testing.T) {
	SkipIfNotAvailable(t)

	// this test is only available on amd64
	if utils.RuntimeArch() != "x64" {
		t.Skip("Skip test when not running on amd64")
	}

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	outputDir := t.TempDir()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		activityDumpSyscallMonitorPeriod:           3 * time.Second,
		anomalyDetectionDefaultMinimumStablePeriod: 1 * time.Second,
		anomalyDetectionEventTypes:                 []string{"exec", "syscalls"},
		anomalyDetectionWarmupPeriod:               1 * time.Second,
		enableSecurityProfile:                      true,
		enableAnomalyDetection:                     true,
		securityProfileDir:                         outputDir,
		tagger:                                     NewFakeMonoTagger(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	goSyscallTester, err := loadSyscallTester(t, test, "syscall_go_tester")
	if err != nil {
		t.Fatal(err)
	}

	var dockerInstance *dockerCmdWrapper
	dockerInstance, err = test.StartADocker()
	if err != nil {
		t.Fatalf("failed to start a Docker instance: %v", err)
	}

	testOutput := map[model.SyscallDriftEventReason]bool{
		model.ExecveReason:               false,
		model.ExitReason:                 false,
		model.SyscallMonitorPeriodReason: false,
	}

	t.Run("activity-dump-syscall-drift", func(t *testing.T) {
		if err = test.GetProbeEvent(func() error {
			manager := test.probe.PlatformProbe.(*probe.EBPFProbe).GetProfileManager()
			manager.AddProfile(generateSyscallTestProfile(test.probe.PlatformProbe.(*probe.EBPFProbe).Resolvers.TimeResolver, model.SysExecve, model.SysExit, model.SysExitGroup))

			time.Sleep(1 * time.Second) // ensure the profile has time to be pushed kernel space

			// run the syscall drift test command
			cmd := dockerInstance.Command(goSyscallTester, []string{"-syscall-drift-test"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(event *model.Event) bool {
			if event.GetType() == "syscalls" {
				var expectedSyscalls []model.Syscall
				switch event.Syscalls.EventReason {
				case model.ExecveReason:
					// Context: this syscall drift event should be sent when `runc.XXXX` execs into the syscall tester.
					// Only basic syscalls performed to prepare the execution of the syscall tester, and that are
					// not in the profile should be here. This includes the Execve syscall itself
					expectedSyscalls = []model.Syscall{
						model.SysRead,
						model.SysWrite,
						model.SysClose,
						model.SysMmap,
					}
				case model.SyscallMonitorPeriodReason:
					// Context: this event should be sent by the openat syscall made by the syscall tester while creating the
					// temporary file. The openat syscall itself shouldn't be in the list, since it is already in the profile.
					// Thus, only basic syscalls performed during the start of the execution of the syscall tester, and that are
					// not in the profile should be here.
					expectedSyscalls = []model.Syscall{
						model.SysRead,
						model.SysClose,
						model.SysMmap,
					}
				case model.ExitReason:
					// Context: this event should be sent when the syscall tester exits and the last dirty syscall cache entry
					// is flushed to user space. This event should include only the file management syscalls that
					// are performed by the syscall tester after the sleep, and that aren't in the profile.
					expectedSyscalls = []model.Syscall{
						model.SysWrite,
						model.SysClose,
						model.SysUnlinkat,
					}
				default:
					t.Errorf("unknown syscall drift event reason: %v", event.Syscalls.EventReason)
					return false
				}

				return checkExpectedSyscalls(t, event.Syscalls.Syscalls, expectedSyscalls, event.Syscalls.EventReason, testOutput)
			}
			return false
		}, 20*time.Second); err != nil {
			t.Error(err)
		}

		// Make sure all 3 syscall drift events were received
		for key, value := range testOutput {
			if !value {
				t.Errorf("missing syscall drift event reason: %v", key)
			}
		}

		dockerInstance.stop()
	})
}

func TestSecurityProfileSyscallDriftNoNewSyscall(t *testing.T) {
	SkipIfNotAvailable(t)

	// this test is only available on amd64
	if utils.RuntimeArch() != "x64" {
		t.Skip("Skip test when not running on amd64")
	}

	// skip test that are about to be run on docker (to avoid trying spawning docker in docker)
	if testEnvironment == DockerEnvironment {
		t.Skip("Skip test spawning docker containers on docker")
	}
	if _, err := whichNonFatal("docker"); err != nil {
		t.Skip("Skip test where docker is unavailable")
	}
	if !IsDedicatedNodeForAD() {
		t.Skip("Skip test when not run in dedicated env")
	}

	outputDir := t.TempDir()

	test, err := newTestModule(t, nil, []*rules.RuleDefinition{}, withStaticOpts(testOpts{
		activityDumpSyscallMonitorPeriod:           3 * time.Second,
		anomalyDetectionDefaultMinimumStablePeriod: 1 * time.Second,
		anomalyDetectionEventTypes:                 []string{"exec", "syscalls"},
		anomalyDetectionWarmupPeriod:               1 * time.Second,
		enableSecurityProfile:                      true,
		enableAnomalyDetection:                     true,
		securityProfileDir:                         outputDir,
		tagger:                                     NewFakeMonoTagger(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()

	goSyscallTester, err := loadSyscallTester(t, test, "syscall_go_tester")
	if err != nil {
		t.Fatal(err)
	}

	var dockerInstance *dockerCmdWrapper
	dockerInstance, err = test.StartADocker()
	if err != nil {
		t.Fatalf("failed to start a Docker instance: %v", err)
	}

	t.Run("activity-dump-syscall-drift", func(t *testing.T) {
		if err = test.GetProbeEvent(func() error {
			manager := test.probe.PlatformProbe.(*probe.EBPFProbe).GetProfileManager()
			manager.AddProfile(generateSyscallTestProfile(
				test.probe.PlatformProbe.(*probe.EBPFProbe).Resolvers.TimeResolver,
				model.SysExecve,
				model.SysExit,
				model.SysExitGroup,
				model.SysRead,
				model.SysWrite,
				model.SysClose,
				model.SysMmap,
				model.SysUnlinkat,
			))

			time.Sleep(1 * time.Second) // ensure the profile has time to be pushed kernel space

			// run the syscall drift test command
			cmd := dockerInstance.Command(goSyscallTester, []string{"-syscall-drift-test"}, []string{})
			_, err = cmd.CombinedOutput()
			return err
		}, func(event *model.Event) bool {
			if event.GetType() == "syscalls" {
				t.Errorf("shouldn't get an event, got: syscalls:%v reason:%v", event.Syscalls.Syscalls, event.Syscalls.EventReason)
				return true
			}
			return false
		}, 20*time.Second); err != nil && errors.Is(err, ErrTimeout{}) {
			t.Error(err)
		}

		dockerInstance.stop()
	})
}
