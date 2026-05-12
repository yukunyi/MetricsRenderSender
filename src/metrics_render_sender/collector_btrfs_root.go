package main

import "fmt"

type btrfsRootSnapshot struct {
	DeviceSizeGB    float64
	AllocatedGB     float64
	AllocatedUsedGB float64
	UnallocatedGB   float64
	AllocationUsage float64
	DataUsage       float64
	MetadataUsage   float64
	SystemUsage     float64
	PinnedGB        float64
	ReclaimGB       float64
	DiscardableGB   float64
	DiscardableOK   bool
	OK              bool
}

type GoNativeBtrfsRootCollector struct {
	*BaseCollector
	source btrfsRootSource
}

func NewGoNativeBtrfsRootCollector() *GoNativeBtrfsRootCollector {
	source, ok := detectBtrfsRootSource()
	if !ok {
		return nil
	}
	collector := &GoNativeBtrfsRootCollector{
		BaseCollector: NewBaseCollector(collectorGoNativeBtrfsRoot),
		source:        source,
	}
	collector.ensureItems()
	return collector
}

func (c *GoNativeBtrfsRootCollector) ensureItems() {
	c.setItem("go_native.btrfs_root.device_size", NewCollectItem("go_native.btrfs_root.device_size", "Btrfs root device size", "GB", 0, 0, 1))
	c.setItem("go_native.btrfs_root.allocated", NewCollectItem("go_native.btrfs_root.allocated", "Btrfs root allocated", "GB", 0, 0, 1))
	c.setItem("go_native.btrfs_root.allocated_used", NewCollectItem("go_native.btrfs_root.allocated_used", "Btrfs root allocated used", "GB", 0, 0, 1))
	c.setItem("go_native.btrfs_root.unallocated", NewCollectItem("go_native.btrfs_root.unallocated", "Btrfs root unallocated", "GB", 0, 0, 1))
	c.setItem("go_native.btrfs_root.alloc_usage", NewCollectItem("go_native.btrfs_root.alloc_usage", "Btrfs root allocation usage", "%", 0, 100, 1))
	c.setItem("go_native.btrfs_root.data_usage", NewCollectItem("go_native.btrfs_root.data_usage", "Btrfs root data usage", "%", 0, 100, 1))
	c.setItem("go_native.btrfs_root.metadata_usage", NewCollectItem("go_native.btrfs_root.metadata_usage", "Btrfs root metadata usage", "%", 0, 100, 1))
	c.setItem("go_native.btrfs_root.system_usage", NewCollectItem("go_native.btrfs_root.system_usage", "Btrfs root system usage", "%", 0, 100, 1))
	c.setItem("go_native.btrfs_root.pinned", NewCollectItem("go_native.btrfs_root.pinned", "Btrfs root pinned", "GB", 0, 0, 2))
	c.setItem("go_native.btrfs_root.reclaim", NewCollectItem("go_native.btrfs_root.reclaim", "Btrfs root reclaim", "GB", 0, 0, 2))
	c.setItem("go_native.btrfs_root.discardable", NewCollectItem("go_native.btrfs_root.discardable", "Btrfs root discardable", "GB", 0, 0, 2))
}

func (c *GoNativeBtrfsRootCollector) GetAllItems() map[string]*CollectItem {
	c.ensureItems()
	return c.ItemsSnapshot()
}

func (c *GoNativeBtrfsRootCollector) UpdateItems() error {
	if !c.IsEnabled() {
		return nil
	}
	snapshot, err := readBtrfsRootSnapshot(c.source)
	if err != nil || !snapshot.OK {
		c.setAllUnavailable()
		if err != nil {
			return err
		}
		return fmt.Errorf("btrfs root snapshot unavailable")
	}
	c.setValue("go_native.btrfs_root.device_size", snapshot.DeviceSizeGB)
	c.setValue("go_native.btrfs_root.allocated", snapshot.AllocatedGB)
	c.setValue("go_native.btrfs_root.allocated_used", snapshot.AllocatedUsedGB)
	c.setValue("go_native.btrfs_root.unallocated", snapshot.UnallocatedGB)
	c.setValue("go_native.btrfs_root.alloc_usage", snapshot.AllocationUsage)
	c.setValue("go_native.btrfs_root.data_usage", snapshot.DataUsage)
	c.setValue("go_native.btrfs_root.metadata_usage", snapshot.MetadataUsage)
	c.setValue("go_native.btrfs_root.system_usage", snapshot.SystemUsage)
	c.setValue("go_native.btrfs_root.pinned", snapshot.PinnedGB)
	c.setValue("go_native.btrfs_root.reclaim", snapshot.ReclaimGB)
	c.setOptionalValue("go_native.btrfs_root.discardable", snapshot.DiscardableGB, snapshot.DiscardableOK)
	return nil
}

func (c *GoNativeBtrfsRootCollector) setValue(name string, value float64) {
	item := c.getItem(name)
	if item == nil {
		return
	}
	item.SetValue(value)
	item.SetAvailable(true)
}

func (c *GoNativeBtrfsRootCollector) setOptionalValue(name string, value float64, ok bool) {
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

func (c *GoNativeBtrfsRootCollector) setAllUnavailable() {
	for _, item := range c.ItemsSnapshot() {
		if item != nil {
			item.SetAvailable(false)
		}
	}
}
