package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
)

var (
	nvmePartitionPattern   = regexp.MustCompile(`^(nvme\d+n\d+)p\d+$`)
	mmcblkPartitionPattern = regexp.MustCompile(`^(mmcblk\d+)p\d+$`)
	legacyDiskPartPattern  = regexp.MustCompile(`^((?:sd|hd|vd|xvd)[a-z]+)\d+$`)
)

func detectCPUInfo() *CPUInfo {
	info := &CPUInfo{
		Model:        "Unknown CPU",
		Cores:        runtime.NumCPU(),
		Threads:      runtime.NumCPU(),
		Architecture: runtime.GOARCH,
		Vendor:       "unknown",
	}

	if threads, err := cpu.Counts(true); err == nil && threads > 0 {
		info.Threads = threads
	}
	if cores, err := cpu.Counts(false); err == nil && cores > 0 {
		info.Cores = cores
	} else if info.Threads > 0 {
		info.Cores = info.Threads
	}

	cpuInfos, err := cpu.Info()
	if err == nil && len(cpuInfos) > 0 {
		var (
			maxFreq float64
			minFreq float64
		)
		for _, item := range cpuInfos {
			if strings.TrimSpace(item.ModelName) != "" && info.Model == "Unknown CPU" {
				info.Model = strings.TrimSpace(item.ModelName)
			}
			if strings.TrimSpace(item.VendorID) != "" && info.Vendor == "unknown" {
				info.Vendor = strings.TrimSpace(item.VendorID)
			}
			if item.Mhz > 0 {
				if maxFreq == 0 || item.Mhz > maxFreq {
					maxFreq = item.Mhz
				}
				if minFreq == 0 || item.Mhz < minFreq {
					minFreq = item.Mhz
				}
			}
		}
		info.MaxFreq = maxFreq
		info.MinFreq = minFreq
	}

	return info
}

func detectDiskInfo() []*DiskInfo {
	disks := detectDiskInfoStatic()
	populateDiskDynamicMetrics(disks)
	return disks
}

func detectDiskInfoStatic() []*DiskInfo {
	if runtime.GOOS == "windows" {
		return detectDiskInfoByWindows()
	}
	if runtime.GOOS == "linux" {
		disks, err := detectDiskInfoBySysfs()
		if err == nil {
			return disks
		}
		logWarnModule("disk", "sysfs disk detection failed, fallback to gopsutil: %v", err)
	}
	return detectDiskInfoByGopsutil()
}

type diskUsageAccumulator struct {
	info       *DiskInfo
	totalBytes uint64
	usedBytes  uint64
}

func detectDiskInfoBySysfs() ([]*DiskInfo, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	usageByDisk := collectDiskUsageByBaseName()
	disks := make([]*DiskInfo, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		baseName := normalizeDiskBaseName(entry.Name(), "")
		if !isLinuxPhysicalDiskName(baseName) {
			continue
		}

		info := buildDiskInfoFromSysfs(baseName, usageByDisk[baseName])
		if info == nil {
			continue
		}
		disks = append(disks, info)
	}

	sort.Slice(disks, func(i, j int) bool {
		return disks[i].Name < disks[j].Name
	})
	return disks, nil
}

func detectDiskInfoByGopsutil() []*DiskInfo {
	partitions, err := disk.Partitions(false)
	if err != nil {
		logWarnModule("disk", "disk partition detection failed: %v", err)
		return []*DiskInfo{}
	}

	items := make(map[string]*diskUsageAccumulator)
	for _, part := range partitions {
		baseName := normalizeDiskBaseName("", part.Device)
		if baseName == "" {
			continue
		}
		if runtime.GOOS == "linux" && isLinuxPseudoDiskName(baseName) {
			continue
		}

		key := strings.ToLower(baseName)
		acc, exists := items[key]
		if !exists {
			acc = &diskUsageAccumulator{
				info: &DiskInfo{
					Name:  baseName,
					Model: inferDiskModel(baseName),
				},
			}
			items[key] = acc
		}

		usageTarget := normalizeUsageMountpoint(part.Mountpoint)
		if usageTarget == "" {
			continue
		}

		usage, err := disk.Usage(usageTarget)
		if err != nil {
			continue
		}
		acc.totalBytes += usage.Total
		acc.usedBytes += usage.Used
	}

	disks := make([]*DiskInfo, 0, len(items))
	for _, acc := range items {
		if acc == nil || acc.info == nil {
			continue
		}
		if acc.totalBytes > 0 {
			acc.info.Size = int64(acc.totalBytes / (1024 * 1024 * 1024))
			acc.info.Used = int64(acc.usedBytes / (1024 * 1024 * 1024))
			acc.info.Available = int64((acc.totalBytes - acc.usedBytes) / (1024 * 1024 * 1024))
			acc.info.Usage = float64(acc.usedBytes) * 100 / float64(acc.totalBytes)
		}
		disks = append(disks, acc.info)
	}
	sort.Slice(disks, func(i, j int) bool {
		return disks[i].Name < disks[j].Name
	})
	return disks
}

func normalizeDiskBaseName(kname, fallback string) string {
	name := strings.TrimSpace(kname)
	if name == "" {
		name = strings.TrimSpace(fallback)
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return ""
	}
	return trimDiskPartitionSuffix(name)
}

func inferDiskModel(baseName string) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return strings.ToUpper(baseName)
	}
	return ""
}

func isLinuxPseudoDiskName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	return strings.HasPrefix(name, "loop") ||
		strings.HasPrefix(name, "zram") ||
		strings.HasPrefix(name, "ram") ||
		strings.HasPrefix(name, "fd")
}

func normalizeUsageMountpoint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "[") {
		return ""
	}
	return value
}

func trimDiskPartitionSuffix(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if match := nvmePartitionPattern.FindStringSubmatch(name); len(match) == 2 {
		return match[1]
	}
	if match := mmcblkPartitionPattern.FindStringSubmatch(name); len(match) == 2 {
		return match[1]
	}
	if match := legacyDiskPartPattern.FindStringSubmatch(strings.ToLower(name)); len(match) == 2 {
		return match[1]
	}
	return name
}

func isLinuxPhysicalDiskName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || isLinuxPseudoDiskName(name) {
		return false
	}
	infoPath := filepath.Join("/sys/block", name, "device")
	if _, err := os.Stat(infoPath); err != nil {
		return false
	}
	return true
}

func buildDiskInfoFromSysfs(baseName string, usage *diskUsageAccumulator) *DiskInfo {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		return nil
	}

	sizeBytes := readSysfsUint64(filepath.Join("/sys/block", baseName, "size")) * 512
	info := &DiskInfo{
		Name:  baseName,
		Model: readDiskModelFromSysfs(baseName),
		Size:  int64(sizeBytes / (1024 * 1024 * 1024)),
	}
	if usage != nil && usage.totalBytes > 0 {
		info.Size = int64(usage.totalBytes / (1024 * 1024 * 1024))
		info.Used = int64(usage.usedBytes / (1024 * 1024 * 1024))
		info.Available = int64((usage.totalBytes - usage.usedBytes) / (1024 * 1024 * 1024))
		info.Usage = float64(usage.usedBytes) * 100 / float64(usage.totalBytes)
	}
	return info
}

func populateDiskDynamicMetrics(disks []*DiskInfo) {
	if len(disks) == 0 {
		return
	}
	names := make([]string, 0, len(disks))
	for _, disk := range disks {
		if disk == nil || strings.TrimSpace(disk.Name) == "" {
			continue
		}
		names = append(names, disk.Name)
	}
	snapshots := getDiskMetricsSnapshots(names)
	tempSnapshots := getDiskTemperatureSnapshots(names)
	for _, disk := range disks {
		if disk == nil {
			continue
		}
		if tempSnapshot, ok := tempSnapshots[disk.Name]; ok && tempSnapshot.OK {
			disk.Temperature = tempSnapshot.Temperature
			disk.TempAvailable = true
		} else {
			disk.TempAvailable = false
		}
		snapshot, ok := snapshots[disk.Name]
		if !ok || !snapshot.OK {
			disk.DynamicAvailable = false
			continue
		}
		disk.ReadSpeed = snapshot.Read
		disk.WriteSpeed = snapshot.Write
		disk.ReadIOPS = snapshot.ReadIOPS
		disk.WriteIOPS = snapshot.WriteIOPS
		disk.ReadLatencyMS = snapshot.ReadLatencyMS
		disk.WriteLatencyMS = snapshot.WriteLatencyMS
		disk.BusyPercent = snapshot.BusyPercent
		disk.QueueDepth = snapshot.QueueDepth
		disk.DynamicAvailable = true
	}
}

func collectDiskUsageByBaseName() map[string]*diskUsageAccumulator {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return map[string]*diskUsageAccumulator{}
	}

	result := make(map[string]*diskUsageAccumulator)
	for _, part := range partitions {
		baseName := normalizeDiskBaseName("", part.Device)
		if !isLinuxPhysicalDiskName(baseName) {
			continue
		}
		mountpoint := normalizeUsageMountpoint(part.Mountpoint)
		if mountpoint == "" {
			continue
		}
		usage, err := disk.Usage(mountpoint)
		if err != nil {
			continue
		}
		acc := result[baseName]
		if acc == nil {
			acc = &diskUsageAccumulator{}
			result[baseName] = acc
		}
		acc.totalBytes += usage.Total
		acc.usedBytes += usage.Used
	}
	return result
}

func readDiskModelFromSysfs(baseName string) string {
	model := readSysfsTrimmed(filepath.Join("/sys/block", baseName, "device", "model"))
	if serial := readSysfsTrimmed(filepath.Join("/sys/block", baseName, "device", "serial")); serial != "" {
		if model != "" {
			return model
		}
		return serial
	}
	return model
}

func readSysfsTrimmed(filename string) string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readSysfsUint64(filename string) uint64 {
	value := readSysfsTrimmed(filename)
	if value == "" {
		return 0
	}
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return number
}
