//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const zramSysfsBlockBase = "/sys/block"

type zramDeviceSnapshot struct {
	diskSizeBytes     uint64
	origDataBytes     uint64
	comprDataBytes    uint64
	memUsedTotalBytes uint64
	memUsedMaxBytes   uint64
	pagesCompacted    uint64
	hugePages         uint64
	hugePagesSince    uint64
	hugePagesSinceOK  bool
}

var (
	zramDetectOnce sync.Once
	zramDetected   zramSource
	zramOK         bool
)

func isZramAvailable() bool {
	_, ok := detectZramSource()
	return ok
}

func detectZramSource() (zramSource, bool) {
	zramDetectOnce.Do(func() {
		source, ok := detectZramSourceOnce()
		if ok {
			snapshot, err := readZramSnapshot(source)
			ok = err == nil && snapshot.OK
		}
		if ok {
			zramDetected = source
			zramOK = true
		}
	})
	return zramDetected, zramOK
}

func detectZramSourceOnce() (zramSource, bool) {
	entries, err := os.ReadDir(zramSysfsBlockBase)
	if err != nil {
		return zramSource{}, false
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if !isZramDeviceName(name) {
			continue
		}
		path := filepath.Join(zramSysfsBlockBase, name)
		if _, err := os.Stat(filepath.Join(path, "disksize")); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, "mm_stat")); err != nil {
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return zramSource{}, false
	}
	sort.Slice(paths, func(i, j int) bool {
		return zramDeviceIndex(filepath.Base(paths[i])) < zramDeviceIndex(filepath.Base(paths[j]))
	})
	return zramSource{sysfsPaths: paths}, true
}

func isZramDeviceName(name string) bool {
	if !strings.HasPrefix(name, "zram") {
		return false
	}
	suffix := strings.TrimPrefix(name, "zram")
	if suffix == "" {
		return false
	}
	_, err := strconv.Atoi(suffix)
	return err == nil
}

func zramDeviceIndex(name string) int {
	suffix := strings.TrimPrefix(strings.TrimSpace(name), "zram")
	value, err := strconv.Atoi(suffix)
	if err != nil {
		return 0
	}
	return value
}

func readZramSnapshot(source zramSource) (zramSnapshot, error) {
	if len(source.sysfsPaths) == 0 {
		return zramSnapshot{}, fmt.Errorf("missing zram sysfs source")
	}
	var (
		deviceCount       int
		diskSizeBytes     uint64
		origDataBytes     uint64
		comprDataBytes    uint64
		memUsedTotalBytes uint64
		memUsedMaxBytes   uint64
		pagesCompacted    uint64
		hugePages         uint64
		hugePagesSince    uint64
		hugePagesSinceOK  bool
	)
	for _, path := range source.sysfsPaths {
		device, err := readZramDeviceSnapshot(path)
		if err != nil {
			return zramSnapshot{}, err
		}
		deviceCount++
		diskSizeBytes += device.diskSizeBytes
		origDataBytes += device.origDataBytes
		comprDataBytes += device.comprDataBytes
		memUsedTotalBytes += device.memUsedTotalBytes
		memUsedMaxBytes += device.memUsedMaxBytes
		pagesCompacted += device.pagesCompacted
		hugePages += device.hugePages
		if device.hugePagesSinceOK {
			hugePagesSince += device.hugePagesSince
			hugePagesSinceOK = true
		}
	}
	if deviceCount == 0 {
		return zramSnapshot{}, fmt.Errorf("zram has no readable devices")
	}
	if diskSizeBytes == 0 {
		return zramSnapshot{}, fmt.Errorf("zram disk size is zero")
	}
	if origDataBytes > diskSizeBytes {
		return zramSnapshot{}, fmt.Errorf("zram original data bytes exceed disk size")
	}

	snapshot := zramSnapshot{
		DeviceCount:      deviceCount,
		DiskSizeGB:       bytesToGiB(diskSizeBytes),
		DataGB:           bytesToGiB(origDataBytes),
		CompressedGB:     bytesToGiB(comprDataBytes),
		MemoryUsedGB:     bytesToGiB(memUsedTotalBytes),
		MemoryPeakGB:     bytesToGiB(memUsedMaxBytes),
		Usage:            percentOf(origDataBytes, diskSizeBytes),
		MemoryUsage:      percentOf(memUsedTotalBytes, diskSizeBytes),
		PagesCompacted:   float64(pagesCompacted),
		HugePages:        float64(hugePages),
		HugePagesSince:   float64(hugePagesSince),
		HugePagesSinceOK: hugePagesSinceOK,
		OK:               true,
	}
	if origDataBytes > 0 && memUsedTotalBytes > 0 {
		snapshot.CompressionRatio = float64(origDataBytes) / float64(memUsedTotalBytes)
		snapshot.CompressionOK = true
	}
	if memUsedTotalBytes > 0 && comprDataBytes > 0 {
		snapshot.AllocatorUsage = percentOf(comprDataBytes, memUsedTotalBytes)
		snapshot.AllocatorUsageOK = true
	}
	return snapshot, nil
}

func readZramDeviceSnapshot(path string) (zramDeviceSnapshot, error) {
	if strings.TrimSpace(path) == "" {
		return zramDeviceSnapshot{}, fmt.Errorf("missing zram sysfs path")
	}
	diskSizeBytes, err := readRequiredZramUint(filepath.Join(path, "disksize"))
	if err != nil {
		return zramDeviceSnapshot{}, err
	}
	stats, err := readZramMMStat(filepath.Join(path, "mm_stat"))
	if err != nil {
		return zramDeviceSnapshot{}, err
	}
	stats.diskSizeBytes = diskSizeBytes
	return stats, nil
}

func readZramMMStat(path string) (zramDeviceSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return zramDeviceSnapshot{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 8 {
		return zramDeviceSnapshot{}, fmt.Errorf("invalid zram mm_stat field count: %s", path)
	}
	values := make([]uint64, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return zramDeviceSnapshot{}, fmt.Errorf("invalid zram mm_stat value %s: %w", path, err)
		}
		values = append(values, value)
	}
	snapshot := zramDeviceSnapshot{
		origDataBytes:     values[0],
		comprDataBytes:    values[1],
		memUsedTotalBytes: values[2],
		memUsedMaxBytes:   values[4],
		pagesCompacted:    values[6],
		hugePages:         values[7],
	}
	if len(values) >= 9 {
		snapshot.hugePagesSince = values[8]
		snapshot.hugePagesSinceOK = true
	}
	return snapshot, nil
}

func readRequiredZramUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0, fmt.Errorf("empty zram sysfs value: %s", path)
	}
	value, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid zram sysfs value %s: %w", path, err)
	}
	return value, nil
}
