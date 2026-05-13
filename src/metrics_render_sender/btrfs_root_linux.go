//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const btrfsRootSysfsBase = "/sys/fs/btrfs"

type btrfsRootSource struct {
	sysfsPath       string
	deviceSizeFiles []string
	discardableFile string
}

type btrfsRootMountInfo struct {
	source string
}

type btrfsBlockGroupSnapshot struct {
	totalBytes   uint64
	bytesUsed    uint64
	diskTotal    uint64
	diskUsed     uint64
	bytesPinned  uint64
	reclaimBytes uint64
}

var (
	btrfsRootDetectOnce sync.Once
	btrfsRootDetected   btrfsRootSource
	btrfsRootOK         bool
)

func isBtrfsRootAvailable() bool {
	_, ok := detectBtrfsRootSource()
	return ok
}

func detectBtrfsRootSource() (btrfsRootSource, bool) {
	btrfsRootDetectOnce.Do(func() {
		source, ok := detectBtrfsRootSourceOnce()
		if ok {
			snapshot, err := readBtrfsRootSnapshot(source)
			ok = err == nil && snapshot.OK
		}
		if ok {
			btrfsRootDetected = source
			btrfsRootOK = true
		}
	})
	return btrfsRootDetected, btrfsRootOK
}

func detectBtrfsRootSourceOnce() (btrfsRootSource, bool) {
	rootMount, ok := readRootBtrfsMountInfo()
	if !ok {
		return btrfsRootSource{}, false
	}
	rootSysfsDevice, ok := rootMountSysfsDevice(rootMount.source)
	if !ok {
		return btrfsRootSource{}, false
	}

	entries, err := os.ReadDir(btrfsRootSysfsBase)
	if err != nil {
		return btrfsRootSource{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sysfsPath := filepath.Join(btrfsRootSysfsBase, entry.Name())
		devicesDir := filepath.Join(sysfsPath, "devices")
		deviceEntries, err := os.ReadDir(devicesDir)
		if err != nil || len(deviceEntries) == 0 {
			continue
		}
		matched := false
		deviceSizeFiles := make([]string, 0, len(deviceEntries))
		for _, deviceEntry := range deviceEntries {
			devicePath := filepath.Join(devicesDir, deviceEntry.Name())
			realDevicePath, err := filepath.EvalSymlinks(devicePath)
			if err != nil {
				continue
			}
			if realDevicePath == rootSysfsDevice {
				matched = true
			}
			sizeFile := filepath.Join(realDevicePath, "size")
			if _, err := os.Stat(sizeFile); err == nil {
				deviceSizeFiles = append(deviceSizeFiles, sizeFile)
			}
		}
		if !matched || len(deviceSizeFiles) == 0 {
			continue
		}
		return btrfsRootSource{
			sysfsPath:       sysfsPath,
			deviceSizeFiles: deviceSizeFiles,
			discardableFile: filepath.Join(sysfsPath, "discard", "discardable_bytes"),
		}, true
	}
	return btrfsRootSource{}, false
}

func readRootBtrfsMountInfo() (btrfsRootMountInfo, bool) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return btrfsRootMountInfo{}, false
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		leftFields := strings.Fields(left)
		if len(leftFields) < 5 || decodeMountInfoField(leftFields[4]) != "/" {
			continue
		}
		rightFields := strings.Fields(right)
		if len(rightFields) < 2 || rightFields[0] != "btrfs" {
			return btrfsRootMountInfo{}, false
		}
		source := decodeMountInfoField(rightFields[1])
		if strings.TrimSpace(source) == "" {
			return btrfsRootMountInfo{}, false
		}
		return btrfsRootMountInfo{source: source}, true
	}
	return btrfsRootMountInfo{}, false
}

func decodeMountInfoField(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}

func rootMountSysfsDevice(source string) (string, bool) {
	if !strings.HasPrefix(source, "/") {
		return "", false
	}
	var stat unix.Stat_t
	if err := unix.Stat(source, &stat); err != nil {
		return "", false
	}
	if (stat.Mode & unix.S_IFMT) != unix.S_IFBLK {
		return "", false
	}
	sysfsPath := fmt.Sprintf("/sys/dev/block/%d:%d", unix.Major(stat.Rdev), unix.Minor(stat.Rdev))
	realPath, err := filepath.EvalSymlinks(sysfsPath)
	if err != nil {
		return "", false
	}
	return realPath, true
}

func readBtrfsRootSnapshot(source btrfsRootSource) (btrfsRootSnapshot, error) {
	if strings.TrimSpace(source.sysfsPath) == "" || len(source.deviceSizeFiles) == 0 {
		return btrfsRootSnapshot{}, fmt.Errorf("missing btrfs root sysfs source")
	}
	deviceSizeBytes, err := readBtrfsRootDeviceSize(source.deviceSizeFiles)
	if err != nil {
		return btrfsRootSnapshot{}, err
	}
	data, err := readBtrfsBlockGroup(filepath.Join(source.sysfsPath, "allocation", "data"))
	if err != nil {
		return btrfsRootSnapshot{}, err
	}
	metadata, err := readBtrfsBlockGroup(filepath.Join(source.sysfsPath, "allocation", "metadata"))
	if err != nil {
		return btrfsRootSnapshot{}, err
	}
	system, err := readBtrfsBlockGroup(filepath.Join(source.sysfsPath, "allocation", "system"))
	if err != nil {
		return btrfsRootSnapshot{}, err
	}

	allocatedBytes := data.diskTotal + metadata.diskTotal + system.diskTotal
	allocatedUsedBytes := data.diskUsed + metadata.diskUsed + system.diskUsed
	if allocatedBytes > deviceSizeBytes {
		return btrfsRootSnapshot{}, fmt.Errorf("btrfs root allocated bytes exceed device size")
	}
	if allocatedUsedBytes > allocatedBytes {
		return btrfsRootSnapshot{}, fmt.Errorf("btrfs root allocated used bytes exceed allocated bytes")
	}

	discardableBytes, discardableOK, err := readOptionalBtrfsUint(source.discardableFile)
	if err != nil {
		return btrfsRootSnapshot{}, err
	}

	snapshot := btrfsRootSnapshot{
		DeviceSizeGB:       bytesToGiB(deviceSizeBytes),
		AllocatedGB:        bytesToGiB(allocatedBytes),
		AllocatedUsedGB:    bytesToGiB(allocatedUsedBytes),
		UnallocatedGB:      bytesToGiB(deviceSizeBytes - allocatedBytes),
		AllocationUsage:    percentOf(allocatedBytes, deviceSizeBytes),
		BalanceReclaimable: percentOf(allocatedBytes-allocatedUsedBytes, deviceSizeBytes),
		DataUsage:          percentOf(data.bytesUsed, data.totalBytes),
		MetadataUsage:      percentOf(metadata.bytesUsed, metadata.totalBytes),
		SystemUsage:        percentOf(system.bytesUsed, system.totalBytes),
		PinnedGB:           bytesToGiB(data.bytesPinned + metadata.bytesPinned + system.bytesPinned),
		ReclaimGB:          bytesToGiB(data.reclaimBytes + metadata.reclaimBytes + system.reclaimBytes),
		DiscardableOK:      discardableOK,
		OK:                 true,
	}
	if discardableOK {
		snapshot.DiscardableGB = bytesToGiB(discardableBytes)
	}
	return snapshot, nil
}

func readBtrfsRootDeviceSize(sizeFiles []string) (uint64, error) {
	total := uint64(0)
	for _, sizeFile := range sizeFiles {
		sectors, err := readRequiredBtrfsUint(sizeFile)
		if err != nil {
			return 0, err
		}
		total += sectors * 512
	}
	if total == 0 {
		return 0, fmt.Errorf("btrfs root device size is zero")
	}
	return total, nil
}

func readBtrfsBlockGroup(path string) (btrfsBlockGroupSnapshot, error) {
	totalBytes, err := readRequiredBtrfsUint(filepath.Join(path, "total_bytes"))
	if err != nil {
		return btrfsBlockGroupSnapshot{}, err
	}
	bytesUsed, err := readRequiredBtrfsUint(filepath.Join(path, "bytes_used"))
	if err != nil {
		return btrfsBlockGroupSnapshot{}, err
	}
	diskTotal, err := readRequiredBtrfsUint(filepath.Join(path, "disk_total"))
	if err != nil {
		return btrfsBlockGroupSnapshot{}, err
	}
	diskUsed, err := readRequiredBtrfsUint(filepath.Join(path, "disk_used"))
	if err != nil {
		return btrfsBlockGroupSnapshot{}, err
	}
	bytesPinned, err := readRequiredBtrfsUint(filepath.Join(path, "bytes_pinned"))
	if err != nil {
		return btrfsBlockGroupSnapshot{}, err
	}
	reclaimBytes, err := readRequiredBtrfsUint(filepath.Join(path, "reclaim_bytes"))
	if err != nil {
		return btrfsBlockGroupSnapshot{}, err
	}
	if totalBytes == 0 || diskTotal == 0 {
		return btrfsBlockGroupSnapshot{}, fmt.Errorf("btrfs block group has zero total: %s", path)
	}
	return btrfsBlockGroupSnapshot{
		totalBytes:   totalBytes,
		bytesUsed:    bytesUsed,
		diskTotal:    diskTotal,
		diskUsed:     diskUsed,
		bytesPinned:  bytesPinned,
		reclaimBytes: reclaimBytes,
	}, nil
}

func readRequiredBtrfsUint(path string) (uint64, error) {
	value, ok, err := readOptionalBtrfsUint(path)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("missing btrfs sysfs value: %s", path)
	}
	return value, nil
}

func readOptionalBtrfsUint(path string) (uint64, bool, error) {
	if strings.TrimSpace(path) == "" {
		return 0, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0, false, fmt.Errorf("empty btrfs sysfs value: %s", path)
	}
	value, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid btrfs sysfs value %s: %w", path, err)
	}
	return value, true, nil
}

func bytesToGiB(bytes uint64) float64 {
	return float64(bytes) / 1024 / 1024 / 1024
}

func percentOf(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
}
