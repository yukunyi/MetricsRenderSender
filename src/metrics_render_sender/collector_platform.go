package main

import (
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	gopsutilNet "github.com/shirou/gopsutil/v3/net"
)

type netRateSnapshot struct {
	counter gopsutilNet.IOCountersStat
	at      time.Time
}

type networkSpeedSnapshot struct {
	Upload   float64
	Download float64
	OK       bool
}

type diskRateSnapshot struct {
	Name        string
	ReadBytes   uint64
	WriteBytes  uint64
	ReadCount   uint64
	WriteCount  uint64
	ReadTimeMS  float64
	WriteTimeMS float64
	BusyTimeMS  float64
	QueueDepth  float64
	at          time.Time
}

type diskCounterSample = diskRateSnapshot

type diskMetricsSnapshot struct {
	Read           float64
	Write          float64
	ReadIOPS       float64
	WriteIOPS      float64
	ReadLatencyMS  float64
	WriteLatencyMS float64
	BusyPercent    float64
	QueueDepth     float64
	OK             bool
}

type diskTemperatureSnapshot struct {
	Temperature float64
	OK          bool
}

var (
	networkRateMu    sync.Mutex
	networkRateCache = make(map[string]netRateSnapshot)
)

func getRealCPUTemperature() float64 {
	if temp := getTemperatureByKeywords([]string{"cpu", "package", "core", "tctl", "ccd"}); temp > 0 {
		return temp
	}
	return 0
}

func getRealCPUFrequency() (float64, float64) {
	if current, maxFreq, ok := getCPUFrequencyByGopsutil(); ok {
		return current, maxFreq
	}
	return 0, 0
}

func getCPUFrequencyByGopsutil() (float64, float64, bool) {
	infos, err := cpu.Info()
	if err != nil || len(infos) == 0 {
		return 0, 0, false
	}

	var (
		total float64
		maxV  float64
		count int
	)
	for _, item := range infos {
		if item.Mhz <= 0 {
			continue
		}
		total += item.Mhz
		count++
		if item.Mhz > maxV {
			maxV = item.Mhz
		}
	}
	if count == 0 || maxV <= 0 {
		return 0, 0, false
	}
	return total / float64(count), maxV, true
}

func getNetworkInfo() NetworkInfoData {
	info := NetworkInfoData{}
	ifaceName, ip := getPrimaryIPv4Interface()
	info.IP = ip
	if ifaceName != "" {
		upload, download, ok := getNetworkSpeedByInterface(ifaceName)
		if ok {
			info.UploadSpeed = upload
			info.DownloadSpeed = download
			return info
		}
	}

	return info
}

func getPrimaryIPv4Interface() (string, string) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}

	sort.Slice(interfaces, func(i, j int) bool {
		return interfaces[i].Index < interfaces[j].Index
	})

	for _, iface := range interfaces {
		name := strings.TrimSpace(iface.Name)
		if name == "" {
			continue
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet == nil || ipnet.IP == nil || ipnet.IP.IsLoopback() {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return name, ip4.String()
			}
		}
	}
	return "", ""
}

func getNetworkSpeedByInterface(interfaceName string) (float64, float64, bool) {
	snapshots := getNetworkSpeedSnapshots([]string{interfaceName})
	snapshot, exists := snapshots[interfaceName]
	if !exists || !snapshot.OK {
		return 0, 0, false
	}
	return snapshot.Upload, snapshot.Download, true
}

func getNetworkSpeedSnapshots(interfaceNames []string) map[string]networkSpeedSnapshot {
	result := make(map[string]networkSpeedSnapshot, len(interfaceNames))
	if len(interfaceNames) == 0 {
		return result
	}

	stats, err := gopsutilNet.IOCounters(true)
	if err != nil {
		return result
	}

	currentByName := make(map[string]gopsutilNet.IOCountersStat, len(stats))
	for _, item := range stats {
		currentByName[item.Name] = item
	}

	now := time.Now()
	networkRateMu.Lock()
	defer networkRateMu.Unlock()

	for _, interfaceName := range interfaceNames {
		current, ok := currentByName[interfaceName]
		if !ok {
			result[interfaceName] = networkSpeedSnapshot{}
			continue
		}
		previous, hasPrevious := networkRateCache[interfaceName]
		networkRateCache[interfaceName] = netRateSnapshot{counter: current, at: now}

		if !hasPrevious || previous.at.IsZero() {
			result[interfaceName] = networkSpeedSnapshot{}
			continue
		}

		seconds := now.Sub(previous.at).Seconds()
		if seconds <= 0 {
			result[interfaceName] = networkSpeedSnapshot{}
			continue
		}
		if current.BytesSent < previous.counter.BytesSent || current.BytesRecv < previous.counter.BytesRecv {
			result[interfaceName] = networkSpeedSnapshot{}
			continue
		}

		upload := float64(current.BytesSent-previous.counter.BytesSent) / seconds / 1024 / 1024
		download := float64(current.BytesRecv-previous.counter.BytesRecv) / seconds / 1024 / 1024
		result[interfaceName] = networkSpeedSnapshot{
			Upload:   upload,
			Download: download,
			OK:       true,
		}
	}
	return result
}

func findNetworkCounter(stats []gopsutilNet.IOCountersStat, interfaceName string) (gopsutilNet.IOCountersStat, bool) {
	for _, item := range stats {
		if item.Name == interfaceName {
			return item, true
		}
	}
	return gopsutilNet.IOCountersStat{}, false
}

var diskPartitionSuffixPattern = regexp.MustCompile(`(p?\d+)$`)

func normalizeDiskCounterCandidates(deviceName string) []string {
	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" {
		return nil
	}
	seen := make(map[string]struct{}, 4)
	result := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	add(deviceName)
	base := filepath.Base(deviceName)
	add(base)
	if base != "" {
		add(diskPartitionSuffixPattern.ReplaceAllString(base, ""))
	}
	return result
}

func getDiskMetricsSnapshots(deviceNames []string) map[string]diskMetricsSnapshot {
	result := make(map[string]diskMetricsSnapshot, len(deviceNames))
	samples := getDiskCounterSamples(deviceNames)
	now := time.Now()
	for name, current := range samples {
		if current.at.IsZero() {
			current.at = now
		}
		result[name] = diskMetricsSnapshot{
			Read:        0,
			Write:       0,
			ReadIOPS:    0,
			WriteIOPS:   0,
			BusyPercent: 0,
			QueueDepth:  current.QueueDepth,
			OK:          true,
		}
	}
	return result
}

func getDiskTemperatureSnapshots(deviceNames []string) map[string]diskTemperatureSnapshot {
	return readPlatformDiskTemperatures(deviceNames)
}

func getDiskCounterSamples(deviceNames []string) map[string]diskRateSnapshot {
	result := make(map[string]diskRateSnapshot, len(deviceNames))
	if len(deviceNames) == 0 {
		return result
	}

	stats, err := readPlatformDiskCounters()
	if err != nil {
		return result
	}
	now := time.Now()
	for _, deviceName := range deviceNames {
		candidates := normalizeDiskCounterCandidates(deviceName)
		if len(candidates) == 0 {
			continue
		}
		for _, candidate := range candidates {
			stat, exists := stats[candidate]
			if !exists {
				continue
			}
			result[deviceName] = diskRateSnapshot{
				Name:        stat.Name,
				ReadBytes:   stat.ReadBytes,
				WriteBytes:  stat.WriteBytes,
				ReadCount:   stat.ReadCount,
				WriteCount:  stat.WriteCount,
				ReadTimeMS:  stat.ReadTimeMS,
				WriteTimeMS: stat.WriteTimeMS,
				BusyTimeMS:  stat.BusyTimeMS,
				QueueDepth:  stat.QueueDepth,
				at:          now,
			}
			break
		}
	}
	return result
}

func getTemperatureByKeywords(keywords []string) float64 {
	temps, err := host.SensorsTemperatures()
	if err != nil {
		return 0
	}
	maxTemp := 0.0
	for _, stat := range temps {
		key := strings.ToLower(strings.TrimSpace(stat.SensorKey))
		if key == "" {
			continue
		}
		if stat.Temperature <= 0 || stat.Temperature > 130 {
			continue
		}
		for _, keyword := range keywords {
			if strings.Contains(key, keyword) {
				if stat.Temperature > maxTemp {
					maxTemp = stat.Temperature
				}
				break
			}
		}
	}
	return maxTemp
}
