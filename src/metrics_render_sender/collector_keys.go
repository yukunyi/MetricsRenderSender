package main

import (
	"runtime"
	"strings"
)

const (
	collectorGoNativeCPU       = "go_native.cpu"
	collectorGoNativeMemory    = "go_native.memory"
	collectorGoNativeSystem    = "go_native.system"
	collectorGoNativeDisk      = "go_native.disk"
	collectorGoNativeNetwork   = "go_native.network"
	collectorGoNativeBtrfsRoot = "go_native.btrfs_root"
	collectorCustomAll         = "custom.all"

	collectorCoolerControl        = "coolercontrol"
	collectorLibreHardwareMonitor = "librehardwaremonitor"
	collectorRTSS                 = "rtss"
)

func isCollectorSupportedOnCurrentPlatform(name string) bool {
	switch name {
	case collectorCoolerControl:
		return runtime.GOOS == "linux"
	case collectorLibreHardwareMonitor:
		return runtime.GOOS == "windows"
	case collectorRTSS:
		return runtime.GOOS == "windows"
	case collectorGoNativeBtrfsRoot:
		return runtime.GOOS == "linux" && isBtrfsRootAvailable()
	default:
		return true
	}
}

func isMonitorSupportedOnCurrentPlatform(name string) bool {
	normalized := strings.TrimSpace(name)
	if strings.HasPrefix(normalized, collectorGoNativeBtrfsRoot+".") {
		return isCollectorSupportedOnCurrentPlatform(collectorGoNativeBtrfsRoot)
	}
	return true
}
