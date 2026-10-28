package main

import (
	"testing"
	"time"
)

func TestDiskCollectorSamplesEnabledMetricsOncePerTick(t *testing.T) {
	collector := NewGoNativeDiskCollector(nil)
	disks := []*DiskInfo{{Name: "diskA", Size: 100}, {Name: "diskB", Size: 200}}
	collector.snapshotProvider = func() []*DiskInfo { return disks }
	collector.ensureSlotsForCount(len(disks))
	counterReads, temperatureReads := 0, 0
	collector.readCounters = func(names []string) map[string]diskRateSnapshot {
		counterReads++
		if len(names) != len(disks) {
			t.Fatalf("expected one batch containing both disks, got %v", names)
		}
		result := make(map[string]diskRateSnapshot)
		for _, name := range names {
			result[name] = diskRateSnapshot{
				ReadBytes: uint64(counterReads) * 200 * 1024 * 1024,
				at:        time.Unix(10+int64(counterReads)*2, 0),
			}
		}
		return result
	}
	collector.readTemperatures = func(names []string) map[string]diskTemperatureSnapshot {
		temperatureReads++
		if len(names) != len(disks) {
			t.Fatalf("expected one temperature batch containing both disks, got %v", names)
		}
		return map[string]diskTemperatureSnapshot{
			"diskA": {Temperature: 42, OK: true},
			"diskB": {Temperature: 48, OK: true},
		}
	}
	for _, disk := range disks {
		state := collector.diskState(disk.Name)
		state.hasLast = true
		state.last = diskRateSnapshot{at: time.Unix(10, 0)}
	}
	for tick := 1; tick <= 2; tick++ {
		if err := collector.UpdateItems(); err != nil {
			t.Fatal(err)
		}
		if counterReads != tick || temperatureReads != tick {
			t.Fatalf("tick %d: expected one read per source, got counters=%d temperatures=%d", tick, counterReads, temperatureReads)
		}
		for index, disk := range disks {
			slot := collector.slots[index+1]
			if got := slot.readItem.GetValue().Value; got != 100.0 {
				t.Fatalf("%s: expected sampled rate 100 MiB/s, got %v", disk.Name, got)
			}
			if !slot.tempItem.IsAvailable() || slot.tempItem.GetValue().Value != float64(42+index*6) {
				t.Fatalf("%s: expected sampled temperature, got %v", disk.Name, slot.tempItem.GetValue())
			}
		}
	}

	for _, item := range collector.ItemsSnapshot() {
		item.SetEnabled(false)
	}
	collector.slots[1].nameItem.SetEnabled(true)
	if err := collector.UpdateItems(); err != nil {
		t.Fatal(err)
	}
	if counterReads != 2 || temperatureReads != 2 {
		t.Fatalf("disabled dynamic metrics still sampled: counters=%d temperatures=%d", counterReads, temperatureReads)
	}

	collector.slots[1].tempItem.SetEnabled(true)
	if err := collector.UpdateItems(); err != nil {
		t.Fatal(err)
	}
	if counterReads != 2 || temperatureReads != 3 {
		t.Fatalf("temperature-only tick: counters=%d temperatures=%d", counterReads, temperatureReads)
	}
	collector.slots[1].tempItem.SetEnabled(false)
	collector.slots[1].readItem.SetEnabled(true)
	if err := collector.UpdateItems(); err != nil {
		t.Fatal(err)
	}
	if counterReads != 3 || temperatureReads != 3 {
		t.Fatalf("rate-only tick: counters=%d temperatures=%d", counterReads, temperatureReads)
	}
}

func TestDiskDiscoveryDoesNotMutateStaticSnapshot(t *testing.T) {
	collector := NewGoNativeDiskCollector(nil)
	disk := &DiskInfo{Name: "metrics-render-test-missing-disk", Temperature: 42, TempAvailable: true}
	collector.snapshotProvider = func() []*DiskInfo { return []*DiskInfo{disk} }
	items := collector.GetAllItems()
	if len(items) != 13 {
		t.Fatalf("expected all 13 disk metrics, got %d", len(items))
	}
	if disk.Temperature != 42 || !disk.TempAvailable {
		t.Fatal("discovery mutated the shared static disk snapshot")
	}
}
