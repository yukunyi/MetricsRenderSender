//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCPUTemperatureWithPartialSensorFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sensors map[string]map[string]string
		want    float64
	}{
		{
			name: "healthy sensors",
			sensors: map[string]map[string]string{
				"hwmon0": {"name": "k10temp", "temp1_label": "Tctl", "temp1_input": "51000", "temp2_label": "Tccd1", "temp2_input": "47000"},
				"hwmon1": {"name": "amdgpu", "temp1_label": "junction", "temp1_input": "70000"},
			},
			want: 51,
		},
		{
			name: "unrelated sensor read failure",
			sensors: map[string]map[string]string{
				"hwmon0": {"name": "k10temp", "temp1_label": "Tctl", "temp1_input": "51000"},
				"hwmon1": {"name": "ath12k_hwmon", "temp1_input": "invalid"},
			},
			want: 51,
		},
		{
			name: "one CPU sensor fails",
			sensors: map[string]map[string]string{
				"hwmon0": {"name": "coretemp", "temp1_label": "Package id 0", "temp1_input": "55000", "temp2_label": "Core 0", "temp2_input": "invalid"},
			},
			want: 55,
		},
		{
			name: "no valid CPU temperature",
			sensors: map[string]map[string]string{
				"hwmon0": {"name": "k10temp", "temp1_label": "Tctl", "temp1_input": "invalid", "temp2_label": "Tccd1", "temp2_input": "0", "temp3_label": "Tccd2", "temp3_input": "150000"},
				"hwmon1": {"name": "amdgpu", "temp1_label": "junction", "temp1_input": "70000"},
			},
		},
		{name: "no sensors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sysRoot := t.TempDir()
			t.Setenv("HOST_SYS", sysRoot)
			for sensor, files := range tc.sensors {
				dir := filepath.Join(sysRoot, "class", "hwmon", sensor)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				for name, value := range files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if got := getRealCPUTemperature(); got != tc.want {
				t.Fatalf("CPU temperature = %v, want %v", got, tc.want)
			}
		})
	}
}
