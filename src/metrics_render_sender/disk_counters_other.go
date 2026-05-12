//go:build !windows

package main

import gopsutilDisk "github.com/shirou/gopsutil/v3/disk"

func readPlatformDiskCounters() (map[string]diskCounterSample, error) {
	stats, err := gopsutilDisk.IOCounters()
	if err != nil {
		return nil, err
	}
	result := make(map[string]diskCounterSample, len(stats))
	for name, stat := range stats {
		result[name] = diskCounterSample{
			Name:        stat.Name,
			ReadBytes:   stat.ReadBytes,
			WriteBytes:  stat.WriteBytes,
			ReadCount:   stat.ReadCount,
			WriteCount:  stat.WriteCount,
			ReadTimeMS:  float64(stat.ReadTime),
			WriteTimeMS: float64(stat.WriteTime),
			BusyTimeMS:  float64(stat.IoTime),
			QueueDepth:  float64(stat.IopsInProgress),
		}
	}
	return result, nil
}

func readPlatformDiskTemperatures(deviceNames []string) map[string]diskTemperatureSnapshot {
	return readLinuxDiskTemperatures(deviceNames)
}
