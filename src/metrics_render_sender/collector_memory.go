package main

import (
	"fmt"
	"runtime"

	"github.com/shirou/gopsutil/v3/mem"
	"time"
)

type GoNativeMemoryCollector struct {
	*BaseCollector
}

func NewGoNativeMemoryCollector() *GoNativeMemoryCollector {
	return &GoNativeMemoryCollector{BaseCollector: NewBaseCollector("go_native.memory")}
}

func (c *GoNativeMemoryCollector) GetAllItems() map[string]*CollectItem {
	if c.getItem("go_native.memory.usage") == nil {
		c.setItem("go_native.memory.usage", NewCollectItem("go_native.memory.usage", "Memory usage", "%", 0, 100, 0))
		c.setItem("go_native.memory.used", NewCollectItem("go_native.memory.used", "Memory used", "GB", 0, 0, 1))
		c.setItem("go_native.memory.total", NewCollectItem("go_native.memory.total", "Memory total", "GB", 0, 0, 1))
		c.setItem("go_native.memory.usage_text", NewCollectItem("go_native.memory.usage_text", "Memory usage detail", "", 0, 0, 0))
		c.setItem("go_native.memory.swap_usage", NewCollectItem("go_native.memory.swap_usage", "Swap usage", "%", 0, 100, 0))
	}

	if info, err := mem.VirtualMemory(); err == nil && info != nil {
		totalGB := float64(info.Total) / (1024 * 1024 * 1024)
		if item := c.getItem("go_native.memory.total"); item != nil {
			item.SetValue(totalGB)
			item.SetAvailable(true)
		}
	}
	return c.ItemsSnapshot()
}

func (c *GoNativeMemoryCollector) UpdateItems() error {
	if !c.IsEnabled() {
		return nil
	}

	err := fetchMemorySnapshot(250 * time.Millisecond)
	virtualInfo, virtualOK := getVirtualMemorySnapshot()
	swapInfo, swapOK := getSwapMemorySnapshot()
	usedBytes, usedPercent, memoryOK := memoryUsageValues(virtualInfo, virtualOK)

	if item := c.getItem("go_native.memory.usage"); item != nil {
		if memoryOK {
			item.SetValue(usedPercent)
			item.SetAvailable(true)
		} else {
			item.SetAvailable(false)
		}
	}

	if item := c.getItem("go_native.memory.used"); item != nil {
		if memoryOK {
			item.SetValue(float64(usedBytes) / (1024 * 1024 * 1024))
			item.SetAvailable(true)
		} else {
			item.SetAvailable(false)
		}
	}

	if item := c.getItem("go_native.memory.usage_text"); item != nil {
		if memoryOK {
			usedGB := float64(usedBytes) / (1024 * 1024 * 1024)
			totalGB := float64(virtualInfo.Total) / (1024 * 1024 * 1024)
			item.SetValue(fmt.Sprintf("%.1f/%.1f GB (%.0f%%)", usedGB, totalGB, usedPercent))
			item.SetAvailable(true)
		} else {
			item.SetAvailable(false)
		}
	}

	if item := c.getItem("go_native.memory.swap_usage"); item != nil {
		if swapOK && swapInfo != nil {
			item.SetValue(swapInfo.UsedPercent)
			item.SetAvailable(true)
		} else {
			item.SetAvailable(false)
		}
	}
	return err
}

func memoryUsageValues(info *mem.VirtualMemoryStat, ok bool) (uint64, float64, bool) {
	if !ok || info == nil || info.Total == 0 {
		return 0, 0, false
	}
	if runtime.GOOS == "linux" {
		if info.Available > info.Total {
			return 0, 0, false
		}
		used := info.Total - info.Available
		return used, float64(used) * 100 / float64(info.Total), true
	}
	return info.Used, info.UsedPercent, true
}
