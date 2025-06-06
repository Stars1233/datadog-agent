// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package npm

import (
	"testing"

	"github.com/DataDog/test-infra-definitions/components/os"
	"github.com/DataDog/test-infra-definitions/scenarios/aws/ec2"

	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/e2e"
	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/provisioners"
	awshost "github.com/DataDog/datadog-agent/test/new-e2e/pkg/provisioners/aws/host"
)

type ec2VMWKitSuite struct {
	e2e.BaseSuite[hostHttpbinEnv]
}

// TestEC2VMWKitSuite will validate running the agent on a single EC2 VM
func TestEC2VMWKitSuite(t *testing.T) {
	t.Parallel()

	s := &ec2VMWKitSuite{}

	e2eParams := []e2e.SuiteOption{e2e.WithProvisioner(provisioners.NewTypedPulumiProvisioner("hostHttpbin", hostDockerHttpbinEnvProvisioner(awshost.WithEC2InstanceOptions(ec2.WithOS(os.WindowsDefault))), nil))}

	e2e.Run(t, s, e2eParams...)
}

// SetupSuite
func (v *ec2VMWKitSuite) SetupSuite() {
	v.BaseSuite.SetupSuite()
	// SetupSuite needs to defer CleanupOnSetupFailure() if what comes after BaseSuite.SetupSuite() can fail.
	defer v.CleanupOnSetupFailure()

	v.Env().RemoteHost.MustExecute("Invoke-WebRequest -UseBasicParsing http://s3.amazonaws.com/dd-agent-mstesting/windows/pvt/nplanel/httpd-2.4.59-240404-win64-VS17.zip -OutFile httpd.zip")
	v.Env().RemoteHost.MustExecute("Expand-Archive httpd.zip")
}

// BeforeTest will be called before each test
func (v *ec2VMWKitSuite) BeforeTest(suiteName, testName string) {
	v.BaseSuite.BeforeTest(suiteName, testName)

	// default is to reset the current state of the fakeintake aggregators
	if !v.BaseSuite.IsDevMode() {
		v.Env().FakeIntake.Client().FlushServerAndResetAggregators()
	}
}

// AfterTest will be called after each test
func (v *ec2VMWKitSuite) AfterTest(suiteName, testName string) {
	test1HostFakeIntakeNPMDumpInfo(v.T(), v.Env().FakeIntake)

	v.BaseSuite.AfterTest(suiteName, testName)
}

// TestFakeIntakeNPM_HostRequests Validate the agent can communicate with the (fake) backend and send connections every 30 seconds
// 2 tests generate the request on the host and on docker
//   - looking for 1 host to send CollectorConnections payload to the fakeintake
//   - looking for 3 payloads and check if the last 2 have a span of 30s +/- 500ms
//
// The test start by 00 to validate the agent/system-probe is up and running
func (v *ec2VMWKitSuite) Test00FakeIntakeNPM_HostRequests() {
	testURL := "http://" + v.Env().HTTPBinHost.Address + "/"

	v.Env().RemoteHost.MustExecute("$result = Invoke-WebRequest -UseBasicParsing -Uri " + testURL)

	test1HostFakeIntakeNPM(&v.BaseSuite, v.Env().FakeIntake)
}

// TestFakeIntakeNPM600cnxBucket_HostRequests Validate the agent can communicate with the (fake) backend and send connections
// every 30 seconds with a maximum of 600 connections per payloads, if more another payload will follow.
//   - looking for 1 host to send CollectorConnections payload to the fakeintake
//   - looking for n payloads and check if the last 2 have a maximum span of 200ms
func (v *ec2VMWKitSuite) TestFakeIntakeNPM600cnxBucket_HostRequests() {
	testURL := "http://" + v.Env().HTTPBinHost.Address + "/"

	// generate connections
	v.Env().RemoteHost.MustExecute("C:\\Users\\Administrator\\httpd\\Apache24\\bin\\ab.exe -n 600 -c 600 " + testURL)

	test1HostFakeIntakeNPM600cnxBucket(&v.BaseSuite, v.Env().FakeIntake)
}

// TestFakeIntakeNPM_TCP_UDP_DNS_HostRequests validate we received tcp, udp, and DNS connections
// with some basic checks, like IPs/Ports present, DNS query has been captured, ...
func (v *ec2VMWKitSuite) TestFakeIntakeNPM_TCP_UDP_DNS_HostRequests() {
	testURL := "http://" + v.Env().HTTPBinHost.Address + "/"

	// generate connections
	v.Env().RemoteHost.MustExecute("$result = Invoke-WebRequest -UseBasicParsing -Uri " + testURL)
	v.Env().RemoteHost.MustExecute("Resolve-DnsName -Name www.google.ch -Server 8.8.8.8")

	test1HostFakeIntakeNPMTCPUDPDNS(&v.BaseSuite, v.Env().FakeIntake)
}
