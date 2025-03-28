// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

package probes

const (
	// SecurityAgentUID is the UID used for all the runtime security module probes
	SecurityAgentUID = "security"
)

const (
	// DentryResolverKernKey is the key to the kernel dentry resolver tail call program
	DentryResolverKernKey uint32 = iota
	// ActivityDumpFilterKey is the key to the kernel activity dump filter tail call program
	ActivityDumpFilterKey
	// DentryResolverKernInputs is the key to the kernel dentry segment resolver tail call program without full syscall context
	DentryResolverKernInputs
	// DentryResolverERPCKey is the key to the eRPC dentry resolver tail call program
	DentryResolverERPCKey
	// DentryResolverParentERPCKey is the key to the eRPC dentry parent resolver tail call program
	DentryResolverParentERPCKey
	// DentryResolverSegmentERPCKey is the key to the eRPC dentry segment resolver tail call program
	DentryResolverSegmentERPCKey
)

const (
	// DentryResolverSetAttrCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of an setattr event
	DentryResolverSetAttrCallbackKprobeKey = iota + 1
	// DentryResolverMkdirCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of an mkdir event
	DentryResolverMkdirCallbackKprobeKey
	// DentryResolverMountStageOneCallbackKprobeKey is the key to the callback program to execute after resolving the root dentry of a new mount
	DentryResolverMountStageOneCallbackKprobeKey
	// DentryResolverMountStageTwoCallbackKprobeKey is the key to the callback program to execute after resolving the mountpoint dentry a new mount
	DentryResolverMountStageTwoCallbackKprobeKey
	// DentryResolverSecurityInodeRmdirCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of an rmdir or unlink event
	DentryResolverSecurityInodeRmdirCallbackKprobeKey
	// DentryResolverSetXAttrCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of an setxattr event
	DentryResolverSetXAttrCallbackKprobeKey
	// DentryResolverUnlinkCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of an unlink event
	DentryResolverUnlinkCallbackKprobeKey
	// DentryResolverLinkSrcCallbackKprobeKey is the key to the callback program to execute after resolving the source dentry of a link event
	DentryResolverLinkSrcCallbackKprobeKey
	// DentryResolverLinkDstCallbackKprobeKey is the key to the callback program to execute after resolving the destination dentry of a link event
	DentryResolverLinkDstCallbackKprobeKey
	// DentryResolverRenameCallbackKprobeKey is the key to the callback program to execute after resolving the destination dentry of a rename event
	DentryResolverRenameCallbackKprobeKey
	// DentryResolverSELinuxCallbackKprobeKey is the key to the callback program to execute after resolving the destination dentry of a selinux event
	DentryResolverSELinuxCallbackKprobeKey
	// DentryResolverChdirCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of an chdir event
	DentryResolverChdirCallbackKprobeKey
	// DentryResolverCGroupWriteCallbackKprobeKey is the key to the callback program to execute after resolving the dentry of a newly created cgroup
	DentryResolverCGroupWriteCallbackKprobeKey
)

const (
	// DentryResolverMkdirCallbackTracepointKey is the key to the callback program to execute after resolving the dentry of an mkdir event
	DentryResolverMkdirCallbackTracepointKey uint32 = iota + 1
	// DentryResolverMountStageOneCallbackTracepointKey is the key to the callback program to execute after resolving the root dentry of a new mount
	DentryResolverMountStageOneCallbackTracepointKey
	// DentryResolverMountStageTwoCallbackTracepointKey is the key to the callback program to execute after resolving the mountpoint dentry a new mount
	DentryResolverMountStageTwoCallbackTracepointKey
	// DentryResolverLinkDstCallbackTracepointKey is the key to the callback program to execute after resolving the destination dentry of a link event
	DentryResolverLinkDstCallbackTracepointKey
	// DentryResolverRenameCallbackTracepointKey is the key to the callback program to execute after resolving the destination dentry of a rename event
	DentryResolverRenameCallbackTracepointKey
	// DentryResolverChdirCallbackTracepointKey is the key to the callback program to execute after resolving the dentry of an chdir event
	DentryResolverChdirCallbackTracepointKey
	// DentryResolverCGroupWriteCallbackTracepointKey is the key to the callback program to execute after resolving the dentry of a newly created cgroup
	DentryResolverCGroupWriteCallbackTracepointKey
)

const (
	// RawPacketFilterMaxTailCall defines the maximum of tail calls
	RawPacketFilterMaxTailCall = 5
)

const (
	// TCDNSRequestKey is the key to the DNS request program
	TCDNSRequestKey uint32 = iota + 1
	// TCDNSRequestParserKey is the key to the DNS request parser program
	TCDNSRequestParserKey
	// TCIMDSRequestParserKey is the key to the IMDS request program
	TCIMDSRequestParserKey
	// TCDNSResponseKey is the key to the DNS response program
	TCDNSResponseKey
)

const (
	// TCRawPacketFilterKey  is the key to the raw packet filter program
	// reserve 5 tail calls for the filtering
	TCRawPacketFilterKey uint32 = iota
	// TCRawPacketParserSenderKey is the key to the raw packet sender program
	TCRawPacketParserSenderKey = TCRawPacketFilterKey + RawPacketFilterMaxTailCall // reserved key for filter tail calls
)

const (
	// ExecGetEnvsOffsetKey is the key to the program that computes the environment variables offset
	ExecGetEnvsOffsetKey uint32 = iota
	// ExecParseArgsEnvsSplitKey is the key to the program that splits the parsing of arguments and environment variables between tailcalls
	ExecParseArgsEnvsSplitKey
	// ExecParseArgsEnvsKey is the key to the program that parses arguments and then environment variables
	ExecParseArgsEnvsKey
)

const (
	// FlushNetworkStatsExitKey is the key to the program that flushes network stats before resuming the normal exit event processing
	FlushNetworkStatsExitKey uint32 = iota
	// FlushNetworkStatsExecKey is the key to the program that flushes network stats before resuming the normal exec event processing
	FlushNetworkStatsExecKey
)
