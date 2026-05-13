package main

import "fmt"

type zramSource struct {
	sysfsPaths []string
}

type zramSnapshot struct {
	DeviceCount      int
	DiskSizeGB       float64
	DataGB           float64
	CompressedGB     float64
	MemoryUsedGB     float64
	MemoryPeakGB     float64
	Usage            float64
	MemoryUsage      float64
	CompressionRatio float64
	CompressionOK    bool
	AllocatorUsage   float64
	AllocatorUsageOK bool
	PagesCompacted   float64
	HugePages        float64
	HugePagesSince   float64
	HugePagesSinceOK bool
	OK               bool
}

type GoNativeZramCollector struct {
	*BaseCollector
	source zramSource
}

func NewGoNativeZramCollector() *GoNativeZramCollector {
	source, ok := detectZramSource()
	if !ok {
		return nil
	}
	collector := &GoNativeZramCollector{
		BaseCollector: NewBaseCollector(collectorGoNativeZram),
		source:        source,
	}
	collector.ensureItems()
	return collector
}

func (c *GoNativeZramCollector) ensureItems() {
	c.setItem("go_native.zram.count", NewCollectItem("go_native.zram.count", "Zram device count", "", 0, 0, 0))
	c.setItem("go_native.zram.size", NewCollectItem("go_native.zram.size", "Zram size", "GB", 0, 0, 1))
	c.setItem("go_native.zram.data", NewCollectItem("go_native.zram.data", "Zram data", "GB", 0, 0, 2))
	c.setItem("go_native.zram.compressed", NewCollectItem("go_native.zram.compressed", "Zram compressed data", "GB", 0, 0, 2))
	c.setItem("go_native.zram.memory_used", NewCollectItem("go_native.zram.memory_used", "Zram memory used", "GB", 0, 0, 2))
	c.setItem("go_native.zram.memory_peak", NewCollectItem("go_native.zram.memory_peak", "Zram memory peak", "GB", 0, 0, 2))
	c.setItem("go_native.zram.usage", NewCollectItem("go_native.zram.usage", "Zram usage", "%", 0, 100, 1))
	c.setItem("go_native.zram.memory_usage", NewCollectItem("go_native.zram.memory_usage", "Zram memory usage", "%", 0, 0, 1))
	c.setItem("go_native.zram.compression_ratio", NewCollectItem("go_native.zram.compression_ratio", "Zram compression ratio", "", 0, 0, 2))
	c.setItem("go_native.zram.allocator_usage", NewCollectItem("go_native.zram.allocator_usage", "Zram allocator usage", "%", 0, 100, 1))
	c.setItem("go_native.zram.pages_compacted", NewCollectItem("go_native.zram.pages_compacted", "Zram pages compacted", "", 0, 0, 0))
	c.setItem("go_native.zram.huge_pages", NewCollectItem("go_native.zram.huge_pages", "Zram huge pages", "", 0, 0, 0))
	c.setItem("go_native.zram.huge_pages_since", NewCollectItem("go_native.zram.huge_pages_since", "Zram huge pages since setup", "", 0, 0, 0))
}

func (c *GoNativeZramCollector) GetAllItems() map[string]*CollectItem {
	c.ensureItems()
	return c.ItemsSnapshot()
}

func (c *GoNativeZramCollector) UpdateItems() error {
	if !c.IsEnabled() {
		return nil
	}
	snapshot, err := readZramSnapshot(c.source)
	if err != nil || !snapshot.OK {
		c.setAllUnavailable()
		if err != nil {
			return err
		}
		return fmt.Errorf("zram snapshot unavailable")
	}
	c.setValue("go_native.zram.count", float64(snapshot.DeviceCount))
	c.setValue("go_native.zram.size", snapshot.DiskSizeGB)
	c.setValue("go_native.zram.data", snapshot.DataGB)
	c.setValue("go_native.zram.compressed", snapshot.CompressedGB)
	c.setValue("go_native.zram.memory_used", snapshot.MemoryUsedGB)
	c.setValue("go_native.zram.memory_peak", snapshot.MemoryPeakGB)
	c.setValue("go_native.zram.usage", snapshot.Usage)
	c.setValue("go_native.zram.memory_usage", snapshot.MemoryUsage)
	c.setOptionalValue("go_native.zram.compression_ratio", snapshot.CompressionRatio, snapshot.CompressionOK)
	c.setOptionalValue("go_native.zram.allocator_usage", snapshot.AllocatorUsage, snapshot.AllocatorUsageOK)
	c.setValue("go_native.zram.pages_compacted", snapshot.PagesCompacted)
	c.setValue("go_native.zram.huge_pages", snapshot.HugePages)
	c.setOptionalValue("go_native.zram.huge_pages_since", snapshot.HugePagesSince, snapshot.HugePagesSinceOK)
	return nil
}

func (c *GoNativeZramCollector) setValue(name string, value float64) {
	item := c.getItem(name)
	if item == nil {
		return
	}
	item.SetValue(value)
	item.SetAvailable(true)
}

func (c *GoNativeZramCollector) setOptionalValue(name string, value float64, ok bool) {
	item := c.getItem(name)
	if item == nil {
		return
	}
	if !ok {
		item.SetAvailable(false)
		return
	}
	item.SetValue(value)
	item.SetAvailable(true)
}

func (c *GoNativeZramCollector) setAllUnavailable() {
	for _, item := range c.ItemsSnapshot() {
		if item != nil {
			item.SetAvailable(false)
		}
	}
}
