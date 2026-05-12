//go:build !linux

package main

func readLinuxDiskTemperatures(deviceNames []string) map[string]diskTemperatureSnapshot {
	return map[string]diskTemperatureSnapshot{}
}
