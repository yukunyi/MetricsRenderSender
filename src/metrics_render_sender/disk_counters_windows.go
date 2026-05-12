//go:build windows

package main

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ioctlDiskPerformance           = 0x70020
	ioctlStorageQueryProperty      = 0x2d1400
	storageDeviceTemperatureID     = 52
	storagePropertyStandardQuery   = 0
	storageTemperatureNotReported  = 0x8000
	windowsDiskHandleRefreshPeriod = int64(time.Minute)
)

type windowsDiskPerformance struct {
	BytesRead           int64
	BytesWritten        int64
	ReadTime            int64
	WriteTime           int64
	IdleTime            int64
	ReadCount           uint32
	WriteCount          uint32
	QueueDepth          uint32
	SplitCount          uint32
	QueryTime           int64
	StorageDeviceNumber uint32
	StorageManagerName  [8]uint16
	alignmentPadding    uint32
}

type windowsStoragePropertyQuery struct {
	PropertyID           uint32
	QueryType            uint32
	AdditionalParameters [1]byte
	alignmentPadding     [3]byte
}

type windowsStorageTemperatureInfo struct {
	Index                    uint16
	Temperature              int16
	OverThreshold            int16
	UnderThreshold           int16
	OverThresholdChangeable  byte
	UnderThresholdChangeable byte
	EventGenerated           byte
	Reserved0                byte
	Reserved1                uint32
}

type windowsStorageTemperatureDescriptor struct {
	Version             uint32
	Size                uint32
	CriticalTemperature int16
	WarningTemperature  int16
	InfoCount           uint16
	Reserved0           [2]byte
	Reserved1           [2]uint32
	TemperatureInfo     [8]windowsStorageTemperatureInfo
}

type windowsDiskHandleState struct {
	mu                        sync.Mutex
	handles                   map[string]windows.Handle
	temperatureBlockedUntilNS map[string]int64
	lastRefreshNS             int64
}

var windowsDiskHandles = windowsDiskHandleState{
	handles:                   make(map[string]windows.Handle),
	temperatureBlockedUntilNS: make(map[string]int64),
}

func readPlatformDiskCounters() (map[string]diskCounterSample, error) {
	windowsDiskHandles.mu.Lock()
	defer windowsDiskHandles.mu.Unlock()

	nowNS := unixNowNS()
	if nowNS-windowsDiskHandles.lastRefreshNS >= windowsDiskHandleRefreshPeriod || len(windowsDiskHandles.handles) == 0 {
		refreshWindowsDiskHandles()
		windowsDiskHandles.lastRefreshNS = nowNS
	}

	result := make(map[string]diskCounterSample, len(windowsDiskHandles.handles))
	for name, handle := range windowsDiskHandles.handles {
		sample, err := readWindowsDiskPerformance(name, handle)
		if err != nil {
			_ = windows.CloseHandle(handle)
			delete(windowsDiskHandles.handles, name)
			reopened, reopenErr := openWindowsDiskHandle(name)
			if reopenErr != nil {
				continue
			}
			windowsDiskHandles.handles[name] = reopened
			sample, err = readWindowsDiskPerformance(name, reopened)
			if err != nil {
				_ = windows.CloseHandle(reopened)
				delete(windowsDiskHandles.handles, name)
				continue
			}
			result[name] = sample
			continue
		}
		result[name] = sample
	}
	return result, nil
}

func readPlatformDiskTemperatures(deviceNames []string) map[string]diskTemperatureSnapshot {
	result := make(map[string]diskTemperatureSnapshot, len(deviceNames))
	if len(deviceNames) == 0 {
		return result
	}

	windowsDiskHandles.mu.Lock()
	defer windowsDiskHandles.mu.Unlock()

	nowNS := unixNowNS()
	if nowNS-windowsDiskHandles.lastRefreshNS >= windowsDiskHandleRefreshPeriod || len(windowsDiskHandles.handles) == 0 {
		refreshWindowsDiskHandles()
		windowsDiskHandles.lastRefreshNS = nowNS
	}

	for _, deviceName := range deviceNames {
		candidates := normalizeDiskCounterCandidates(deviceName)
		if len(candidates) == 0 {
			continue
		}
		for _, candidate := range candidates {
			handle, exists := windowsDiskHandles.handles[candidate]
			if !exists {
				continue
			}
			if blockedUntil := windowsDiskHandles.temperatureBlockedUntilNS[candidate]; blockedUntil > nowNS {
				break
			}
			value, ok := readWindowsDiskTemperature(handle)
			if !ok {
				windowsDiskHandles.temperatureBlockedUntilNS[candidate] = nowNS + windowsDiskHandleRefreshPeriod
				break
			}
			result[deviceName] = diskTemperatureSnapshot{
				Temperature: value,
				OK:          true,
			}
			break
		}
	}
	return result
}

func refreshWindowsDiskHandles() {
	live := enumerateWindowsFixedDrives()
	for name, handle := range windowsDiskHandles.handles {
		if _, ok := live[name]; ok {
			continue
		}
		_ = windows.CloseHandle(handle)
		delete(windowsDiskHandles.handles, name)
		delete(windowsDiskHandles.temperatureBlockedUntilNS, name)
	}
	for name := range live {
		if _, ok := windowsDiskHandles.handles[name]; ok {
			continue
		}
		handle, err := openWindowsDiskHandle(name)
		if err != nil {
			continue
		}
		windowsDiskHandles.handles[name] = handle
	}
}

func enumerateWindowsFixedDrives() map[string]struct{} {
	result := make(map[string]struct{})
	buf := make([]uint16, 254)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil || n == 0 {
		return result
	}
	for _, value := range buf[:n] {
		if value < 'A' || value > 'Z' {
			continue
		}
		path := string(rune(value)) + ":"
		typePath, err := windows.UTF16PtrFromString(path)
		if err != nil {
			continue
		}
		if windows.GetDriveType(typePath) != windows.DRIVE_FIXED {
			continue
		}
		result[path] = struct{}{}
	}
	return result
}

func openWindowsDiskHandle(name string) (windows.Handle, error) {
	devicePath, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\%s`, name))
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		devicePath,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
}

func readWindowsDiskPerformance(name string, handle windows.Handle) (diskCounterSample, error) {
	var perf windowsDiskPerformance
	var returned uint32
	err := windows.DeviceIoControl(
		handle,
		ioctlDiskPerformance,
		nil,
		0,
		(*byte)(unsafe.Pointer(&perf)),
		uint32(unsafe.Sizeof(perf)),
		&returned,
		nil,
	)
	if err != nil {
		return diskCounterSample{}, err
	}

	busyRaw := perf.QueryTime - perf.IdleTime
	if busyRaw < 0 {
		busyRaw = perf.ReadTime + perf.WriteTime
	}
	return diskCounterSample{
		Name:        name,
		ReadBytes:   uint64(perf.BytesRead),
		WriteBytes:  uint64(perf.BytesWritten),
		ReadCount:   uint64(perf.ReadCount),
		WriteCount:  uint64(perf.WriteCount),
		ReadTimeMS:  windowsDiskTimeToMS(perf.ReadTime),
		WriteTimeMS: windowsDiskTimeToMS(perf.WriteTime),
		BusyTimeMS:  windowsDiskTimeToMS(busyRaw),
		QueueDepth:  float64(perf.QueueDepth),
	}, nil
}

func readWindowsDiskTemperature(handle windows.Handle) (float64, bool) {
	query := windowsStoragePropertyQuery{
		PropertyID: storageDeviceTemperatureID,
		QueryType:  storagePropertyStandardQuery,
	}
	var descriptor windowsStorageTemperatureDescriptor
	var returned uint32
	err := windows.DeviceIoControl(
		handle,
		ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&query)),
		uint32(unsafe.Sizeof(query)),
		(*byte)(unsafe.Pointer(&descriptor)),
		uint32(unsafe.Sizeof(descriptor)),
		&returned,
		nil,
	)
	if err != nil || returned == 0 || descriptor.InfoCount == 0 {
		return 0, false
	}

	count := int(descriptor.InfoCount)
	if count > len(descriptor.TemperatureInfo) {
		count = len(descriptor.TemperatureInfo)
	}
	maxTemp := 0.0
	ok := false
	for i := 0; i < count; i++ {
		raw := descriptor.TemperatureInfo[i].Temperature
		if uint16(raw) == storageTemperatureNotReported {
			continue
		}
		value := float64(raw)
		if value < DiskTempMin || value > DiskTempMax {
			continue
		}
		if !ok || value > maxTemp {
			maxTemp = value
			ok = true
		}
	}
	return maxTemp, ok
}

func windowsDiskTimeToMS(value int64) float64 {
	if value <= 0 {
		return 0
	}
	return float64(value) / 10000.0
}

func unixNowNS() int64 {
	return time.Now().UnixNano()
}
