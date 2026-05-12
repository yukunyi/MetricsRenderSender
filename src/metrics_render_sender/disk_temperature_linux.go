//go:build linux

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type linuxDiskTemperatureState struct {
	paths     []string
	lastProbe time.Time
}

var linuxDiskTemperatureCache = struct {
	mu     sync.Mutex
	states map[string]linuxDiskTemperatureState
}{
	states: make(map[string]linuxDiskTemperatureState),
}

func readLinuxDiskTemperatures(deviceNames []string) map[string]diskTemperatureSnapshot {
	result := make(map[string]diskTemperatureSnapshot, len(deviceNames))
	if len(deviceNames) == 0 {
		return result
	}

	now := time.Now()
	linuxDiskTemperatureCache.mu.Lock()
	defer linuxDiskTemperatureCache.mu.Unlock()

	for _, deviceName := range deviceNames {
		name := normalizeDiskBaseName(deviceName, "")
		if name == "" {
			continue
		}
		state := linuxDiskTemperatureCache.states[name]
		if len(state.paths) == 0 && (state.lastProbe.IsZero() || now.Sub(state.lastProbe) >= diskScanPeriod) {
			state.paths = discoverLinuxDiskTemperaturePaths(name)
			state.lastProbe = now
			linuxDiskTemperatureCache.states[name] = state
		}
		if len(state.paths) == 0 {
			continue
		}

		value, ok := readMaxLinuxDiskTemperature(state.paths)
		if !ok {
			state.paths = nil
			state.lastProbe = now
			linuxDiskTemperatureCache.states[name] = state
			continue
		}
		result[name] = diskTemperatureSnapshot{
			Temperature: value,
			OK:          true,
		}
	}
	return result
}

func discoverLinuxDiskTemperaturePaths(baseName string) []string {
	baseName = normalizeDiskBaseName(baseName, "")
	if baseName == "" {
		return nil
	}
	devicePath := filepath.Join("/sys/block", baseName, "device")
	hwmonDirs := linuxDiskHwmonDirs(devicePath)
	if len(hwmonDirs) == 0 {
		return nil
	}

	paths := make([]string, 0, len(hwmonDirs))
	for _, dir := range hwmonDirs {
		path := selectLinuxDiskTemperaturePath(dir)
		if path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	return paths
}

func selectLinuxDiskTemperaturePath(hwmonDir string) string {
	hwmonName := strings.ToLower(strings.TrimSpace(readSysfsTrimmed(filepath.Join(hwmonDir, "name"))))
	if hwmonName == "drivetemp" {
		path := filepath.Join(hwmonDir, "temp1_input")
		if _, ok := readLinuxTemperatureInput(path); ok {
			return path
		}
		return ""
	}

	entries, err := os.ReadDir(hwmonDir)
	if err != nil {
		return ""
	}
	inputs := make([]string, 0, 2)
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "temp") || !strings.HasSuffix(name, "_input") {
			continue
		}
		inputs = append(inputs, name)
	}
	sort.Strings(inputs)

	for _, input := range inputs {
		labelPath := filepath.Join(hwmonDir, strings.TrimSuffix(input, "_input")+"_label")
		label := strings.ToLower(strings.TrimSpace(readSysfsTrimmed(labelPath)))
		if label != "composite" {
			continue
		}
		path := filepath.Join(hwmonDir, input)
		if _, ok := readLinuxTemperatureInput(path); ok {
			return path
		}
	}
	return ""
}

func linuxDiskHwmonDirs(devicePath string) []string {
	entries, err := os.ReadDir(devicePath)
	if err != nil {
		return nil
	}
	dirs := make([]string, 0, 2)
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := entry.Name()
		path := filepath.Join(devicePath, name)
		if strings.HasPrefix(name, "hwmon") && linuxPathIsDir(path) {
			dirs = append(dirs, path)
			continue
		}
		if name != "hwmon" || !linuxPathIsDir(path) {
			continue
		}
		children, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		for _, child := range children {
			if child == nil {
				continue
			}
			childName := child.Name()
			childPath := filepath.Join(path, childName)
			if strings.HasPrefix(childName, "hwmon") && linuxPathIsDir(childPath) {
				dirs = append(dirs, childPath)
			}
		}
	}
	return dirs
}

func linuxPathIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func readMaxLinuxDiskTemperature(paths []string) (float64, bool) {
	maxTemp := 0.0
	ok := false
	for _, path := range paths {
		value, valueOK := readLinuxTemperatureInput(path)
		if !valueOK {
			continue
		}
		if !ok || value > maxTemp {
			maxTemp = value
			ok = true
		}
	}
	return maxTemp, ok
}

func readLinuxTemperatureInput(path string) (float64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	var buf [32]byte
	n, _ := file.Read(buf[:])
	if n <= 0 {
		return 0, false
	}
	raw, ok := parseLinuxTemperatureMilliCelsius(buf[:n])
	if !ok {
		return 0, false
	}
	value := float64(raw) / 1000.0
	if value < DiskTempMin || value > DiskTempMax {
		return 0, false
	}
	return value, true
}

func parseLinuxTemperatureMilliCelsius(data []byte) (int64, bool) {
	i := 0
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\n' || data[i] == '\r') {
		i++
	}
	if i >= len(data) {
		return 0, false
	}
	sign := int64(1)
	if data[i] == '-' {
		sign = -1
		i++
	} else if data[i] == '+' {
		i++
	}
	value := int64(0)
	digits := 0
	for i < len(data) {
		ch := data[i]
		if ch < '0' || ch > '9' {
			break
		}
		value = value*10 + int64(ch-'0')
		digits++
		i++
	}
	if digits == 0 {
		return 0, false
	}
	return value * sign, true
}
