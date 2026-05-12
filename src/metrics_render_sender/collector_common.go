package main

import (
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FanInfo represents fan information
type FanInfo struct {
	Name  string
	Speed int
	Index int
}

// CPUInfo represents detailed CPU information
type CPUInfo struct {
	Model        string
	Cores        int
	Threads      int
	Architecture string
	MaxFreq      float64
	MinFreq      float64
	Vendor       string
}

// DiskInfo represents detailed disk information
type DiskInfo struct {
	Name             string
	Model            string
	Size             int64   // Total size in GB
	Used             int64   // Used size in GB
	Available        int64   // Available size in GB
	Usage            float64 // Usage percentage
	Temperature      float64 // Disk temperature in Celsius
	TempAvailable    bool
	ReadSpeed        float64 // MiB/s
	WriteSpeed       float64 // MiB/s
	ReadIOPS         float64
	WriteIOPS        float64
	ReadLatencyMS    float64
	WriteLatencyMS   float64
	BusyPercent      float64
	QueueDepth       float64
	DynamicAvailable bool
}

var (
	cachedCPUInfo    *CPUInfo
	cachedDiskInfo   []*DiskInfo
	cacheInitMutex   sync.Once
	diskInfoMutex    sync.RWMutex
	lastDiskUpdate   time.Time
	lastDiskScanAt   time.Time
	diskUpdatePeriod = 1 * time.Second
	diskScanPeriod   = 30 * time.Second

	// 无锁读取用原子存储
	diskInfoStore atomic.Value // []*DiskInfo

	// 渲染门控
	renderAccessMutex   sync.Mutex
	lastRenderAccess    time.Time
	renderAccessTimeout = 5 * time.Second

	diskSamplerOnce sync.Once
)

func noteRenderAccess() {
	renderAccessMutex.Lock()
	lastRenderAccess = time.Now()
	renderAccessMutex.Unlock()
}

func isRenderActive() bool {
	renderAccessMutex.Lock()
	defer renderAccessMutex.Unlock()
	return time.Since(lastRenderAccess) <= renderAccessTimeout
}

func initializeCache() {
	cacheInitMutex.Do(func() {
		cachedCPUInfo = detectCPUInfo()
		go func() {
			updateDiskInfo()
			startDiskSampler()
			printSystemInfo()
		}()
	})
}

// updateDiskInfo updates disk information if enough time has passed
func updateDiskInfo() {
	now := time.Now()
	diskInfoMutex.Lock()
	if now.Sub(lastDiskUpdate) < diskUpdatePeriod {
		diskInfoMutex.Unlock()
		return
	}
	existing := cloneDiskInfoList(cachedDiskInfo)
	needScan := len(existing) == 0 || now.Sub(lastDiskScanAt) >= diskScanPeriod
	diskInfoMutex.Unlock()

	newDisks := existing
	if needScan {
		newDisks = detectDiskInfoStatic()
	}
	populateDiskDynamicMetrics(newDisks)
	if len(newDisks) > 1 {
		sort.Slice(newDisks, func(i, j int) bool { return newDisks[i].Name < newDisks[j].Name })
	}

	// 写入缓存与时间戳
	diskInfoMutex.Lock()
	cachedDiskInfo = newDisks
	lastDiskUpdate = now
	if needScan {
		lastDiskScanAt = now
	}
	diskInfoMutex.Unlock()
	diskInfoStore.Store(newDisks)
}

// getCachedDiskInfo returns current disk information without lock (atomic)
func getCachedDiskInfo() []*DiskInfo {
	initializeCache()
	if v := diskInfoStore.Load(); v != nil {
		if disks, ok := v.([]*DiskInfo); ok {
			return disks
		}
	}
	diskInfoMutex.RLock()
	defer diskInfoMutex.RUnlock()
	return cachedDiskInfo
}

func startDiskSampler() {
	diskSamplerOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(diskUpdatePeriod)
			defer ticker.Stop()
			for range ticker.C {
				if !isRenderActive() {
					continue
				}
				updateDiskInfo()
			}
		}()
	})
}

func printSystemInfo() {
	logInfo("=== System Information ===")
	if cachedCPUInfo != nil {
		logInfo("CPU: %s", cachedCPUInfo.Model)
		logInfo("CPU Vendor: %s", cachedCPUInfo.Vendor)
		logInfo("CPU Architecture: %s", cachedCPUInfo.Architecture)
		logInfo("CPU Cores: %d, Threads: %d", cachedCPUInfo.Cores, cachedCPUInfo.Threads)
		logInfo("CPU Frequency: %.0f MHz - %.0f MHz", cachedCPUInfo.MinFreq, cachedCPUInfo.MaxFreq)
	}
	disks := getCachedDiskInfo()
	if len(disks) > 0 {
		logInfo("Disks: %d detected", len(disks))
		for i, disk := range disks {
			if i < 3 {
				logInfo("Disk %d: %s (%s) - %.0f GB used=%d%% read=%.1f MiB/s write=%.1f MiB/s busy=%.0f%%", i+1, disk.Name, disk.Model, float64(disk.Size), int64(disk.Usage+0.5), disk.ReadSpeed, disk.WriteSpeed, disk.BusyPercent)
			}
		}
	}
	logInfo("OS: %s %s", runtime.GOOS, runtime.GOARCH)
	logInfo("========================")
}

func cloneDiskInfoList(input []*DiskInfo) []*DiskInfo {
	if len(input) == 0 {
		return []*DiskInfo{}
	}
	out := make([]*DiskInfo, 0, len(input))
	for _, disk := range input {
		if disk == nil {
			continue
		}
		copied := *disk
		out = append(out, &copied)
	}
	return out
}
