// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

// Package dentry holds dentry related files
package dentry

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"unsafe"

	"github.com/DataDog/datadog-go/v5/statsd"
	lib "github.com/cilium/ebpf"
	"go.uber.org/atomic"
	"golang.org/x/sys/unix"

	manager "github.com/DataDog/ebpf-manager"

	"github.com/DataDog/datadog-agent/pkg/security/ebpf"
	"github.com/DataDog/datadog-agent/pkg/security/metrics"
	"github.com/DataDog/datadog-agent/pkg/security/probe/config"
	"github.com/DataDog/datadog-agent/pkg/security/probe/erpc"
	"github.com/DataDog/datadog-agent/pkg/security/probe/managerhelper"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model"
	"github.com/DataDog/datadog-agent/pkg/security/utils"
	"github.com/DataDog/datadog-agent/pkg/security/utils/cache"
)

type counterEntry struct {
	resolutionType string
	resolution     string
}

func (ce *counterEntry) Tags() []string {
	return []string{ce.resolutionType, ce.resolution}
}

// Resolver resolves inode/mountID to full paths
type Resolver struct {
	config                *config.Config
	statsdClient          statsd.ClientInterface
	pathnames             *lib.Map
	erpcStats             [2]*lib.Map
	bufferSelector        *lib.Map
	activeERPCStatsBuffer uint32
	cache                 *cache.TwoLayersLRU[uint32, model.PathKey, PathEntry]
	erpc                  *erpc.ERPC
	erpcSegment           []byte
	erpcSegmentSize       int
	useBPFProgWriteUser   bool
	erpcRequest           *erpc.Request
	erpcStatsZero         []eRPCStats
	numCPU                int
	challenge             uint32

	// buffers
	filenameParts    []string
	keys             []model.PathKey
	cacheNameEntries []string

	hitsCounters map[counterEntry]*atomic.Int64
	missCounters map[counterEntry]*atomic.Int64
}

// ErrEntryNotFound is thrown when a path key was not found in the cache
var ErrEntryNotFound = errors.New("entry not found")

// PathEntry is the path structure saved in cache
type PathEntry struct {
	Parent model.PathKey
	Name   string
}

// eRPCStats is used to collect kernel space metrics about the eRPC resolution
type eRPCStats struct {
	Count uint64
}

// eRPCRet is the type used to parse the eRPC return value
type eRPCRet uint32

func (ret eRPCRet) String() string {
	switch ret {
	case eRPCok:
		return "ok"
	case eRPCCacheMiss:
		return "cache_miss"
	case eRPCBufferSize:
		return "buffer_size"
	case eRPCWritePageFault:
		return "write_page_fault"
	case eRPCTailCallError:
		return "tail_call_error"
	case eRPCReadPageFault:
		return "read_page_fault"
	default:
		return "unknown"
	}
}

const (
	eRPCok eRPCRet = iota
	eRPCCacheMiss
	eRPCBufferSize
	eRPCWritePageFault
	eRPCTailCallError
	eRPCReadPageFault
	eRPCUnknownError
)

func allERPCRet() []eRPCRet {
	return []eRPCRet{eRPCok, eRPCCacheMiss, eRPCBufferSize, eRPCWritePageFault, eRPCTailCallError, eRPCReadPageFault, eRPCUnknownError}
}

// SendStats sends the dentry resolver metrics
func (dr *Resolver) SendStats() error {
	for counterEntry, counter := range dr.hitsCounters {
		val := counter.Swap(0)
		if val > 0 {
			_ = dr.statsdClient.Count(metrics.MetricDentryResolverHits, val, counterEntry.Tags(), 1.0)
		}
	}

	for counterEntry, counter := range dr.missCounters {
		val := counter.Swap(0)
		if val > 0 {
			_ = dr.statsdClient.Count(metrics.MetricDentryResolverMiss, val, counterEntry.Tags(), 1.0)
		}
	}

	_ = dr.statsdClient.Gauge(metrics.MetricDentryCacheSize, float64(dr.cache.Len()), []string{}, 1)

	return dr.sendERPCStats()
}

func (dr *Resolver) sendERPCStats() error {
	buffer := dr.erpcStats[1-dr.activeERPCStatsBuffer]
	iterator := buffer.Iterate()
	stats := make([]eRPCStats, dr.numCPU)
	counters := map[eRPCRet]int64{}
	var ret eRPCRet

	for iterator.Next(&ret, &stats) {
		if ret == eRPCok {
			continue
		}
		for _, count := range stats {
			if _, ok := counters[ret]; !ok {
				counters[ret] = 0
			}
			counters[ret] += int64(count.Count)
		}
	}
	for r, count := range counters {
		if count > 0 {
			_ = dr.statsdClient.Count(metrics.MetricDentryERPC, count, []string{fmt.Sprintf("ret:%s", r)}, 1.0)
		}
	}
	for _, r := range allERPCRet() {
		_ = buffer.Put(r, dr.erpcStatsZero)
	}

	dr.activeERPCStatsBuffer = 1 - dr.activeERPCStatsBuffer
	return dr.bufferSelector.Put(ebpf.BufferSelectorERPCMonitorKey, dr.activeERPCStatsBuffer)
}

// DelCacheEntries removes all the entries belonging to a mountID
func (dr *Resolver) DelCacheEntries(mountID uint32) {
	dr.cache.RemoveKey1(mountID)
}

func (dr *Resolver) lookupInodeFromCache(pathKey model.PathKey) (PathEntry, error) {
	entry, exists := dr.cache.Get(pathKey.MountID, pathKey)
	if !exists {
		return PathEntry{}, ErrEntryNotFound
	}
	return entry, nil
}

// We need to cache inode by inode instead of caching the whole path in order to be
// able to invalidate the whole path if one of its element got rename or removed.
func (dr *Resolver) cacheInode(key model.PathKey, path PathEntry) {
	dr.cache.Add(key.MountID, key, path)
}

// ResolveNameFromCache returns the name
func (dr *Resolver) ResolveNameFromCache(pathKey model.PathKey) (string, error) {
	entry := counterEntry{
		resolutionType: metrics.CacheTag,
		resolution:     metrics.SegmentResolutionTag,
	}

	path, err := dr.lookupInodeFromCache(pathKey)
	if err != nil {
		dr.missCounters[entry].Inc()
		return "", err
	}

	dr.hitsCounters[entry].Inc()
	return path.Name, nil
}

func (dr *Resolver) lookupInodeFromMap(pathKey model.PathKey) (model.PathLeaf, error) {
	var pathLeaf model.PathLeaf
	if err := dr.pathnames.Lookup(pathKey, &pathLeaf); err != nil {
		return pathLeaf, fmt.Errorf("unable to get filename for mountID `%d` and inode `%d`: %w", pathKey.MountID, pathKey.Inode, err)
	}
	return pathLeaf, nil
}

func newPathEntry(parent model.PathKey, name string) PathEntry {
	return PathEntry{
		Parent: parent,
		Name:   name,
	}
}

// ResolveNameFromMap resolves the name of the provided inode
func (dr *Resolver) ResolveNameFromMap(pathKey model.PathKey) (string, error) {
	entry := counterEntry{
		resolutionType: metrics.KernelMapsTag,
		resolution:     metrics.SegmentResolutionTag,
	}

	pathLeaf, err := dr.lookupInodeFromMap(pathKey)
	if err != nil {
		dr.missCounters[entry].Inc()
		return "", fmt.Errorf("unable to get filename for mountID `%d` and inode `%d`: %w", pathKey.MountID, pathKey.Inode, err)
	}

	dr.hitsCounters[entry].Inc()

	name := pathLeaf.GetName()

	if !model.IsFakeInode(pathKey.Inode) {
		cacheEntry := newPathEntry(pathLeaf.Parent, name)
		dr.cacheInode(pathKey, cacheEntry)
	}

	return name, nil
}

// ResolveName resolves an inode/mount ID pair to a file basename
func (dr *Resolver) ResolveName(pathKey model.PathKey) string {
	name, err := dr.ResolveNameFromCache(pathKey)
	if err != nil && dr.config.MapDentryResolutionEnabled {
		name, err = dr.ResolveNameFromMap(pathKey)
	}

	if err != nil {
		name = ""
	}
	return name
}

// ResolveFromCache resolves path from the cache
func (dr *Resolver) ResolveFromCache(pathKey model.PathKey) (string, error) {
	var path PathEntry
	var err error
	depth := int64(0)
	filenameParts := make([]string, 0, 128)

	entry := counterEntry{
		resolutionType: metrics.CacheTag,
		resolution:     metrics.PathResolutionTag,
	}

	// Fetch path recursively
	for i := 0; i <= model.MaxPathDepth; i++ {
		path, err = dr.lookupInodeFromCache(pathKey)
		if err != nil {
			dr.missCounters[entry].Inc()
			break
		}
		depth++

		// Don't append dentry name if this is the root dentry (i.d. name == '/')
		if len(path.Name) != 0 && path.Name[0] != '\x00' && path.Name[0] != '/' {
			filenameParts = append(filenameParts, path.Name)
		}

		if path.Parent.Inode == 0 {
			break
		}

		// Prepare next key
		pathKey = path.Parent
	}

	if depth > 0 {
		dr.hitsCounters[entry].Add(depth)
	}

	return computeFilenameFromParts(filenameParts), err
}

func computeFilenameFromParts(parts []string) string {
	if len(parts) == 0 {
		return "/"
	}

	// pre-allocation
	prealloc := 0
	for _, part := range parts {
		prealloc += len(part) + 1
	}

	var builder strings.Builder
	builder.Grow(prealloc)

	// reverse iteration
	for i := 0; i < len(parts); i++ {
		j := len(parts) - 1 - i

		builder.WriteRune('/')
		builder.WriteString(parts[j])
	}
	return builder.String()
}

// ResolveFromMap resolves the path of the provided inode / mount id / path id
func (dr *Resolver) ResolveFromMap(pathKey model.PathKey, cache bool) (string, error) {
	var resolutionErr error

	keyBuffer, err := pathKey.MarshalBinary()
	if err != nil {
		return "", err
	}

	depth := int64(0)

	dr.prepareBuffersWithCapacity(128)

	// Fetch path recursively
	for i := 0; i <= model.MaxPathDepth; i++ {
		var pathLeaf model.PathLeaf
		pathKey.Write(keyBuffer)
		if err := dr.pathnames.Lookup(keyBuffer, &pathLeaf); err != nil {
			dr.filenameParts = dr.filenameParts[:0]
			resolutionErr = &ErrDentryPathKeyNotFound{PathKey: pathKey}
			break
		}
		depth++

		if pathLeaf.Name[0] == '\x00' {
			if depth >= model.MaxPathDepth {
				resolutionErr = errTruncatedParents
			} else {
				resolutionErr = errKernelMapResolution
			}
			break
		}

		// Don't append dentry name if this is the root dentry (i.d. name == '/')
		var name string
		if pathLeaf.Name[0] == '/' {
			name = "/"
		} else {
			name = model.NullTerminatedString(pathLeaf.Name[:])
			dr.filenameParts = append(dr.filenameParts, name)
		}

		// do not cache fake path keys in the case of rename events
		if !model.IsFakeInode(pathKey.Inode) && cache {
			dr.keys = append(dr.keys, pathKey)
			dr.cacheNameEntries = append(dr.cacheNameEntries, name)
		}

		if pathLeaf.Parent.Inode == 0 {
			break
		}

		// Prepare next key
		pathKey = pathLeaf.Parent
	}

	filename := computeFilenameFromParts(dr.filenameParts)

	entry := counterEntry{
		resolutionType: metrics.KernelMapsTag,
		resolution:     metrics.PathResolutionTag,
	}

	if resolutionErr == nil && len(dr.keys) > 0 {
		resolutionErr = dr.cacheEntries(dr.keys, dr.cacheNameEntries)

		if depth > 0 {
			dr.hitsCounters[entry].Add(depth)
		}
	}

	if resolutionErr != nil {
		dr.missCounters[entry].Inc()
	}

	return filename, resolutionErr
}

// preventSegmentMajorPageFault prepares the userspace memory area where the dentry resolver response is written. Used in kernel versions where BPF_F_MMAPABLE array maps are not yet available.
func (dr *Resolver) preventSegmentMajorPageFault() {
	// if we don't access the segment, the eBPF program can't write to it ... (major page fault)
	dr.erpcSegment[0] = 0
	dr.erpcSegment[os.Getpagesize()] = 0
	dr.erpcSegment[2*os.Getpagesize()] = 0
	dr.erpcSegment[3*os.Getpagesize()] = 0
	dr.erpcSegment[4*os.Getpagesize()] = 0
	dr.erpcSegment[5*os.Getpagesize()] = 0
	dr.erpcSegment[6*os.Getpagesize()] = 0
}

func (dr *Resolver) requestResolve(op uint8, pathKey model.PathKey) (uint32, error) {
	challenge := dr.challenge
	dr.challenge++

	// create eRPC request
	dr.erpcRequest.OP = op
	binary.NativeEndian.PutUint64(dr.erpcRequest.Data[0:8], pathKey.Inode)
	binary.NativeEndian.PutUint32(dr.erpcRequest.Data[8:12], pathKey.MountID)
	binary.NativeEndian.PutUint32(dr.erpcRequest.Data[12:16], pathKey.PathID)
	// 16-28 populated at start
	binary.NativeEndian.PutUint32(dr.erpcRequest.Data[28:32], challenge)

	// if we don't try to access the segment, the eBPF program can't write to it ... (major page fault)
	if dr.useBPFProgWriteUser {
		dr.preventSegmentMajorPageFault()
	}

	return challenge, dr.erpc.Request(dr.erpcRequest)
}

func (dr *Resolver) cacheEntries(keys []model.PathKey, names []string) error {
	if len(keys) != len(names) {
		return errors.New("out of bound")
	}

	for i, k := range keys {
		cacheEntry := PathEntry{
			Name: names[i],
		}
		if len(keys) > i+1 {
			cacheEntry.Parent = keys[i+1]
		}

		dr.cacheInode(k, cacheEntry)
	}

	return nil
}

func (dr *Resolver) computeSegmentCount() int {
	count := 0
	i := 0
	for i < dr.erpcSegmentSize {
		i += 16 // skip the path key

		if i < dr.erpcSegmentSize {
			if dr.erpcSegment[i] == '/' {
				break
			}

			// skip the segment
			i += bytes.IndexByte(dr.erpcSegment[i:], 0)
		}

		i++ // skip the null terminator
		count++
	}
	return count
}

// ResolveFromERPC resolves the path of the provided inode / mount id / path id
func (dr *Resolver) ResolveFromERPC(pathKey model.PathKey, cache bool) (string, error) {
	var resolutionErr error
	depth := int64(0)

	entry := counterEntry{
		resolutionType: metrics.ERPCTag,
		resolution:     metrics.PathResolutionTag,
	}

	// create eRPC request and send using the ioctl syscall
	challenge, err := dr.requestResolve(erpc.ResolvePathOp, pathKey)
	if err != nil {
		dr.missCounters[entry].Inc()
		return "", fmt.Errorf("unable to resolve the path of mountID `%d` and inode `%d` with eRPC: %w", pathKey.MountID, pathKey.Inode, err)
	}

	segmentCount := dr.computeSegmentCount()
	dr.prepareBuffersWithCapacity(segmentCount)

	i := 0
	// make sure that we keep room for at least one pathKey + character + \0 => (sizeof(pathID) + 1 = 17)
	for i < dr.erpcSegmentSize-17 {
		depth++

		// parse the path_key_t structure
		pathKey.Inode = binary.NativeEndian.Uint64(dr.erpcSegment[i : i+8])
		pathKey.MountID = binary.NativeEndian.Uint32(dr.erpcSegment[i+8 : i+12])

		// check challenge
		if challenge != binary.NativeEndian.Uint32(dr.erpcSegment[i+12:i+16]) {
			if depth >= model.MaxPathDepth {
				resolutionErr = errTruncatedParentsERPC
				break
			}
			dr.missCounters[entry].Inc()
			return "", errERPCRequestNotProcessed
		}

		// skip PathID
		i += 16

		if dr.erpcSegment[i] == 0 {
			if depth >= model.MaxPathDepth {
				resolutionErr = errTruncatedParentsERPC
			} else {
				resolutionErr = errERPCResolution
			}
			break
		}

		if dr.erpcSegment[i] == '/' {
			break
		}

		segment := model.NullTerminatedString(dr.erpcSegment[i:])
		dr.filenameParts = append(dr.filenameParts, segment)
		i += len(segment) + 1

		if !model.IsFakeInode(pathKey.Inode) && cache {
			dr.keys = append(dr.keys, pathKey)
			dr.cacheNameEntries = append(dr.cacheNameEntries, segment)
		}
	}

	if resolutionErr == nil && len(dr.keys) > 0 {
		resolutionErr = dr.cacheEntries(dr.keys, dr.cacheNameEntries)

		if depth > 0 {
			dr.hitsCounters[entry].Add(depth)
		}
	}

	if resolutionErr != nil {
		dr.missCounters[entry].Inc()
	}

	return computeFilenameFromParts(dr.filenameParts), resolutionErr
}

// Resolve the pathname of a dentry, starting at the pathnameKey in the pathnames table
func (dr *Resolver) Resolve(pathKey model.PathKey, cache bool) (string, error) {
	var path string
	var err = ErrEntryNotFound

	if cache {
		path, err = dr.ResolveFromCache(pathKey)
	}
	if err != nil && dr.config.ERPCDentryResolutionEnabled {
		path, err = dr.ResolveFromERPC(pathKey, cache)
	}
	if err != nil && err != errTruncatedParentsERPC && dr.config.MapDentryResolutionEnabled {
		path, err = dr.ResolveFromMap(pathKey, cache)
	}
	return path, err
}

// ResolveParentFromCache resolves the parent
func (dr *Resolver) ResolveParentFromCache(pathKey model.PathKey) (model.PathKey, error) {
	entry := counterEntry{
		resolutionType: metrics.CacheTag,
		resolution:     metrics.ParentResolutionTag,
	}

	path, err := dr.lookupInodeFromCache(pathKey)
	if err != nil {
		dr.missCounters[entry].Inc()
		return model.PathKey{}, ErrEntryNotFound
	}

	dr.hitsCounters[entry].Inc()
	return path.Parent, nil
}

// ResolveParentFromMap resolves the parent
func (dr *Resolver) ResolveParentFromMap(pathKey model.PathKey) (model.PathKey, error) {
	entry := counterEntry{
		resolutionType: metrics.KernelMapsTag,
		resolution:     metrics.ParentResolutionTag,
	}

	path, err := dr.lookupInodeFromMap(pathKey)
	if err != nil {
		dr.missCounters[entry].Inc()
		return model.PathKey{}, err
	}

	dr.hitsCounters[entry].Inc()
	return path.Parent, nil
}

// GetParent returns the parent mount_id/inode
func (dr *Resolver) GetParent(pathKey model.PathKey) (model.PathKey, error) {
	pathKey, err := dr.ResolveParentFromCache(pathKey)
	if err != nil && dr.config.MapDentryResolutionEnabled {
		pathKey, err = dr.ResolveParentFromMap(pathKey)
	}

	if pathKey.Inode == 0 {
		return model.PathKey{}, ErrEntryNotFound
	}

	return pathKey, err
}

func (dr *Resolver) prepareBuffersWithCapacity(capacity int) {
	if cap(dr.filenameParts) < capacity {
		dr.filenameParts = make([]string, 0, capacity)
	} else {
		dr.filenameParts = dr.filenameParts[:0]
	}

	if cap(dr.keys) < capacity {
		dr.keys = make([]model.PathKey, 0, capacity)
	} else {
		dr.keys = dr.keys[:0]
	}

	if cap(dr.cacheNameEntries) < capacity {
		dr.cacheNameEntries = make([]string, 0, capacity)
	} else {
		dr.cacheNameEntries = dr.cacheNameEntries[:0]
	}
}

// Start the dentry resolver
func (dr *Resolver) Start(manager *manager.Manager) error {
	pathnames, err := managerhelper.Map(manager, "pathnames")
	if err != nil {
		return err
	}
	dr.pathnames = pathnames

	erpcStatsFB, err := managerhelper.Map(manager, "dr_erpc_stats_fb")
	if err != nil {
		return err
	}
	dr.erpcStats[0] = erpcStatsFB

	erpcStatsBB, err := managerhelper.Map(manager, "dr_erpc_stats_bb")
	if err != nil {
		return err
	}
	dr.erpcStats[1] = erpcStatsBB

	bufferSelector, err := managerhelper.Map(manager, "buffer_selector")
	if err != nil {
		return err
	}
	dr.bufferSelector = bufferSelector

	erpcBuffer, err := managerhelper.Map(manager, "dr_erpc_buffer")
	if err != nil {
		return err
	}

	// Memory map a BPF_F_MMAPABLE array map that ebpf writes to so that userspace can read it
	if erpcBuffer.Flags()&unix.BPF_F_MMAPABLE != 0 {
		dr.erpcSegment, err = unix.Mmap(erpcBuffer.FD(), 0, 8*4096, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return fmt.Errorf("failed to mmap dr_erpc_buffer map: %w", err)
		}
	}

	// BPF_F_MMAPABLE array maps were introduced in kernel version 5.5, so we need a fallback for older versions.
	// Allocate memory area in userspace that ebpf programs will write to. Will receive warning because the kernel writing to userspace memory can cause instability.
	if dr.erpcSegment == nil {
		// We need at least 7 memory pages for the eRPC segment method to work.
		// For each segment of a path, we write 16 bytes to store (inode, mount_id, path_id), and then at least 2 bytes to
		// store the smallest possible path (segment of size 1 + trailing 0). 18 * 1500 = 27 000.
		// Then, 27k + 256 / page_size < 7.
		dr.erpcSegment = make([]byte, 7*4096)
		dr.useBPFProgWriteUser = true

		binary.NativeEndian.PutUint64(dr.erpcRequest.Data[16:24], uint64(uintptr(unsafe.Pointer(&dr.erpcSegment[0]))))
	}

	dr.erpcSegmentSize = len(dr.erpcSegment)
	binary.NativeEndian.PutUint32(dr.erpcRequest.Data[24:28], uint32(dr.erpcSegmentSize))

	return nil
}

// ToJSON return a json version of the cache
func (dr *Resolver) ToJSON() ([]byte, error) {
	dump := struct {
		Entries []json.RawMessage
	}{}

	dr.cache.Walk(func(_ uint32, pathKey model.PathKey, value PathEntry) {
		entry := struct {
			PathKey   model.PathKey
			PathEntry PathEntry
		}{
			PathKey:   pathKey,
			PathEntry: value,
		}

		data, err := json.Marshal(entry)
		if err == nil {
			dump.Entries = append(dump.Entries, data)
		}
	})

	return json.Marshal(dump)
}

// Close cleans up the eRPC segment
func (dr *Resolver) Close() error {
	if !dr.useBPFProgWriteUser {
		if err := unix.Munmap(dr.erpcSegment); err != nil {
			return fmt.Errorf("couldn't cleanup eRPC memory segment: %w", err)
		}
	}
	return nil
}

// NewResolver returns a new dentry resolver
func NewResolver(config *config.Config, statsdClient statsd.ClientInterface, e *erpc.ERPC) (*Resolver, error) {
	hitsCounters := make(map[counterEntry]*atomic.Int64)
	missCounters := make(map[counterEntry]*atomic.Int64)
	for _, resolution := range metrics.AllResolutionsTags {
		for _, resolutionType := range metrics.AllTypesTags {
			// procfs resolution doesn't exist in the dentry resolver
			if resolutionType == metrics.ProcFSTag {
				continue
			}
			entry := counterEntry{
				resolutionType: resolutionType,
				resolution:     resolution,
			}
			hitsCounters[entry] = atomic.NewInt64(0)
			missCounters[entry] = atomic.NewInt64(0)
		}
	}

	numCPU, err := utils.NumCPU()
	if err != nil {
		return nil, fmt.Errorf("couldn't fetch the host CPU count: %w", err)
	}

	cache, err := cache.NewTwoLayersLRU[uint32, model.PathKey, PathEntry](config.DentryCacheSize)
	if err != nil {
		return nil, err
	}

	return &Resolver{
		config:        config,
		statsdClient:  statsdClient,
		cache:         cache,
		erpc:          e,
		erpcRequest:   erpc.NewERPCRequest(0),
		erpcStatsZero: make([]eRPCStats, numCPU),
		hitsCounters:  hitsCounters,
		missCounters:  missCounters,
		numCPU:        numCPU,
		challenge:     rand.Uint32(),
	}, nil
}
