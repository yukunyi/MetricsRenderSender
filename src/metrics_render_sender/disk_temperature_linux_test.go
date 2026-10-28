//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxDiskHwmonDirs(t *testing.T) {
	for _, layout := range []string{"hwmon/hwmon3", "hwmon3"} {
		t.Run(layout, func(t *testing.T) {
			devicePath := t.TempDir()
			hwmonDir := filepath.Join(devicePath, layout)
			if err := os.MkdirAll(hwmonDir, 0755); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]string{"name": "drivetemp\n", "temp1_input": "42000\n"} {
				if err := os.WriteFile(filepath.Join(hwmonDir, name), []byte(value), 0644); err != nil {
					t.Fatal(err)
				}
			}
			dirs := linuxDiskHwmonDirs(devicePath)
			if len(dirs) != 1 || dirs[0] != hwmonDir {
				t.Fatalf("expected hwmon directory %q, got %v", hwmonDir, dirs)
			}
			path := selectLinuxDiskTemperaturePath(dirs[0])
			value, ok := readMaxLinuxDiskTemperature([]string{path})
			if !ok || value != 42 {
				t.Fatalf("expected disk temperature 42 C, got %v (available=%v)", value, ok)
			}
		})
	}
}
