package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func detectNamedDiskCount() int {
	count := 0
	for _, disk := range detectDiskInfo() {
		if disk == nil {
			continue
		}
		if strings.TrimSpace(disk.Name) == "" {
			continue
		}
		count++
	}
	return count
}

type goNativeDiskSlot struct {
	nameItem         *CollectItem
	sizeItem         *CollectItem
	usedItem         *CollectItem
	availableItem    *CollectItem
	usageItem        *CollectItem
	tempItem         *CollectItem
	busyItem         *CollectItem
	readItem         *CollectItem
	writeItem        *CollectItem
	readIOPSItem     *CollectItem
	writeIOPSItem    *CollectItem
	readLatencyItem  *CollectItem
	writeLatencyItem *CollectItem
}

type GoNativeDiskCollector struct {
	*BaseCollector
	requiredProvider func() []string
	slots            map[int]*goNativeDiskSlot
	runtimeMu        runtimeDiskMetricsStore
}

type runtimeDiskMetricsStore struct {
	states map[string]*runtimeDiskMetricsState
}

type runtimeDiskMetricsState struct {
	last       diskRateSnapshot
	hasLast    bool
	lastGood   *diskComputedMetrics
	validUntil time.Time
}

type diskComputedMetrics struct {
	read           float64
	write          float64
	readIOPS       float64
	writeIOPS      float64
	readLatencyMS  float64
	writeLatencyMS float64
	busyPercent    float64
	queueDepth     float64
}

func smoothDiskMetric(previous float64, current float64, alpha float64) float64 {
	if alpha <= 0 {
		return current
	}
	if alpha >= 1 {
		return previous
	}
	return previous*alpha + current*(1-alpha)
}

func smoothDiskMetrics(previous *diskComputedMetrics, current *diskComputedMetrics) *diskComputedMetrics {
	if current == nil {
		return nil
	}
	if previous == nil {
		return current
	}
	return &diskComputedMetrics{
		read:           smoothDiskMetric(previous.read, current.read, 0.55),
		write:          smoothDiskMetric(previous.write, current.write, 0.55),
		readIOPS:       smoothDiskMetric(previous.readIOPS, current.readIOPS, 0.55),
		writeIOPS:      smoothDiskMetric(previous.writeIOPS, current.writeIOPS, 0.55),
		readLatencyMS:  smoothDiskMetric(previous.readLatencyMS, current.readLatencyMS, 0.4),
		writeLatencyMS: smoothDiskMetric(previous.writeLatencyMS, current.writeLatencyMS, 0.4),
		busyPercent:    smoothDiskMetric(previous.busyPercent, current.busyPercent, 0.3),
		queueDepth:     current.queueDepth,
	}
}

func NewGoNativeDiskCollector(requiredProvider func() []string) *GoNativeDiskCollector {
	return &GoNativeDiskCollector{
		BaseCollector:    NewBaseCollector("go_native.disk"),
		requiredProvider: requiredProvider,
		slots:            make(map[int]*goNativeDiskSlot),
		runtimeMu: runtimeDiskMetricsStore{
			states: make(map[string]*runtimeDiskMetricsState),
		},
	}
}

func (c *GoNativeDiskCollector) requiredMaxIndex() int {
	required := []string{}
	if c.requiredProvider != nil {
		required = c.requiredProvider()
	}
	maxIndex := 0
	for _, key := range required {
		trimmed := strings.TrimSpace(key)
		if !strings.HasPrefix(trimmed, "go_native.disk.") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "go_native.disk.")
		parts := strings.Split(rest, ".")
		if len(parts) != 2 {
			continue
		}
		idx, err := strconv.Atoi(parts[0])
		if err != nil || idx <= 0 {
			continue
		}
		switch parts[1] {
		case "name", "size", "used", "available", "usage", "temp", "busy", "read", "write", "read_iops", "write_iops", "read_latency", "write_latency":
			if idx > maxIndex {
				maxIndex = idx
			}
		}
	}
	return maxIndex
}

func (c *GoNativeDiskCollector) ensureSlots() {
	c.ensureSlotsForCount(detectNamedDiskCount())
}

func (c *GoNativeDiskCollector) ensureSlotsForCount(detected int) {
	requiredMax := c.requiredMaxIndex()
	slotCount := max(detected, requiredMax)
	if slotCount > 16 {
		slotCount = 16
	}
	for index := 1; index <= slotCount; index++ {
		if _, exists := c.slots[index]; exists {
			continue
		}
		slot := &goNativeDiskSlot{
			nameItem:         NewCollectItem(fmt.Sprintf("go_native.disk.%d.name", index), fmt.Sprintf("Disk %d name", index), "", 0, 0, 0),
			sizeItem:         NewCollectItem(fmt.Sprintf("go_native.disk.%d.size", index), fmt.Sprintf("Disk %d size", index), "GB", 0, 0, 0),
			usedItem:         NewCollectItem(fmt.Sprintf("go_native.disk.%d.used", index), fmt.Sprintf("Disk %d used", index), "GB", 0, 0, 0),
			availableItem:    NewCollectItem(fmt.Sprintf("go_native.disk.%d.available", index), fmt.Sprintf("Disk %d available", index), "GB", 0, 0, 0),
			usageItem:        NewCollectItem(fmt.Sprintf("go_native.disk.%d.usage", index), fmt.Sprintf("Disk %d usage", index), "%", 0, 100, 0),
			tempItem:         NewCollectItem(fmt.Sprintf("go_native.disk.%d.temp", index), fmt.Sprintf("Disk %d temperature", index), "°C", 0, DiskTempMax, 1),
			busyItem:         NewCollectItem(fmt.Sprintf("go_native.disk.%d.busy", index), fmt.Sprintf("Disk %d busy", index), "%", 0, 100, 0),
			readItem:         NewCollectItem(fmt.Sprintf("go_native.disk.%d.read", index), fmt.Sprintf("Disk %d read speed", index), "MiB/s", 0, 0, 2),
			writeItem:        NewCollectItem(fmt.Sprintf("go_native.disk.%d.write", index), fmt.Sprintf("Disk %d write speed", index), "MiB/s", 0, 0, 2),
			readIOPSItem:     NewCollectItem(fmt.Sprintf("go_native.disk.%d.read_iops", index), fmt.Sprintf("Disk %d read IOPS", index), "IOPS", 0, 0, 0),
			writeIOPSItem:    NewCollectItem(fmt.Sprintf("go_native.disk.%d.write_iops", index), fmt.Sprintf("Disk %d write IOPS", index), "IOPS", 0, 0, 0),
			readLatencyItem:  NewCollectItem(fmt.Sprintf("go_native.disk.%d.read_latency", index), fmt.Sprintf("Disk %d read latency", index), "ms", 0, 0, 2),
			writeLatencyItem: NewCollectItem(fmt.Sprintf("go_native.disk.%d.write_latency", index), fmt.Sprintf("Disk %d write latency", index), "ms", 0, 0, 2),
		}
		c.slots[index] = slot
		c.setItem(slot.nameItem.GetName(), slot.nameItem)
		c.setItem(slot.sizeItem.GetName(), slot.sizeItem)
		c.setItem(slot.usedItem.GetName(), slot.usedItem)
		c.setItem(slot.availableItem.GetName(), slot.availableItem)
		c.setItem(slot.usageItem.GetName(), slot.usageItem)
		c.setItem(slot.tempItem.GetName(), slot.tempItem)
		c.setItem(slot.busyItem.GetName(), slot.busyItem)
		c.setItem(slot.readItem.GetName(), slot.readItem)
		c.setItem(slot.writeItem.GetName(), slot.writeItem)
		c.setItem(slot.readIOPSItem.GetName(), slot.readIOPSItem)
		c.setItem(slot.writeIOPSItem.GetName(), slot.writeIOPSItem)
		c.setItem(slot.readLatencyItem.GetName(), slot.readLatencyItem)
		c.setItem(slot.writeLatencyItem.GetName(), slot.writeLatencyItem)
	}
}

func (c *GoNativeDiskCollector) snapshotDisks() []*DiskInfo {
	initializeCache()
	updateDiskInfo()
	return getCachedDiskInfo()
}

func updateDiskStaticItems(slot *goNativeDiskSlot, disk *DiskInfo) {
	if slot == nil {
		return
	}
	if disk == nil || strings.TrimSpace(disk.Name) == "" {
		slot.nameItem.SetAvailable(false)
		slot.sizeItem.SetAvailable(false)
		slot.usedItem.SetAvailable(false)
		slot.availableItem.SetAvailable(false)
		slot.usageItem.SetAvailable(false)
		return
	}
	name := strings.TrimSpace(disk.Name)
	model := strings.TrimSpace(disk.Model)
	if model != "" {
		name = fmt.Sprintf("%s (%s)", name, model)
	}
	slot.nameItem.SetValue(name)
	slot.nameItem.SetAvailable(true)
	slot.sizeItem.SetValue(disk.Size)
	slot.sizeItem.SetAvailable(true)
	slot.usedItem.SetValue(disk.Used)
	slot.usedItem.SetAvailable(true)
	slot.availableItem.SetValue(disk.Available)
	slot.availableItem.SetAvailable(true)
	slot.usageItem.SetValue(disk.Usage)
	slot.usageItem.SetAvailable(true)
}

func updateDiskRateItems(slot *goNativeDiskSlot, disk *DiskInfo) {
	if slot == nil {
		return
	}
	if disk != nil && disk.DynamicAvailable {
		slot.readItem.SetValue(disk.ReadSpeed)
		slot.readItem.SetAvailable(true)
		slot.writeItem.SetValue(disk.WriteSpeed)
		slot.writeItem.SetAvailable(true)
		slot.readIOPSItem.SetValue(disk.ReadIOPS)
		slot.readIOPSItem.SetAvailable(true)
		slot.writeIOPSItem.SetValue(disk.WriteIOPS)
		slot.writeIOPSItem.SetAvailable(true)
		slot.readLatencyItem.SetValue(disk.ReadLatencyMS)
		slot.readLatencyItem.SetAvailable(true)
		slot.writeLatencyItem.SetValue(disk.WriteLatencyMS)
		slot.writeLatencyItem.SetAvailable(true)
		slot.busyItem.SetValue(disk.BusyPercent)
		slot.busyItem.SetAvailable(true)
		return
	}
	slot.readItem.SetAvailable(false)
	slot.writeItem.SetAvailable(false)
	slot.readIOPSItem.SetAvailable(false)
	slot.writeIOPSItem.SetAvailable(false)
	slot.readLatencyItem.SetAvailable(false)
	slot.writeLatencyItem.SetAvailable(false)
	slot.busyItem.SetAvailable(false)
}

func updateDiskTemperatureItem(slot *goNativeDiskSlot, disk *DiskInfo) {
	if slot == nil || slot.tempItem == nil {
		return
	}
	if disk == nil || !disk.TempAvailable {
		slot.tempItem.SetAvailable(false)
		return
	}
	slot.tempItem.SetValue(disk.Temperature)
	slot.tempItem.SetAvailable(true)
}

func updateDiskTemperatureSnapshotItem(slot *goNativeDiskSlot, disk *DiskInfo, snapshots map[string]diskTemperatureSnapshot) {
	if slot == nil || slot.tempItem == nil {
		return
	}
	if disk == nil {
		slot.tempItem.SetAvailable(false)
		return
	}
	name := strings.TrimSpace(disk.Name)
	if name == "" {
		slot.tempItem.SetAvailable(false)
		return
	}
	snapshot, ok := snapshots[name]
	if !ok || !snapshot.OK {
		slot.tempItem.SetAvailable(false)
		return
	}
	slot.tempItem.SetValue(snapshot.Temperature)
	slot.tempItem.SetAvailable(true)
}

func diskTemperatureItemsEnabled(slots map[int]*goNativeDiskSlot) bool {
	for _, slot := range slots {
		if slot != nil && slot.tempItem != nil && slot.tempItem.IsEnabled() {
			return true
		}
	}
	return false
}

func setDiskDynamicMetrics(slot *goNativeDiskSlot, metrics *diskComputedMetrics) {
	if slot == nil || metrics == nil {
		updateDiskRateItems(slot, nil)
		return
	}
	slot.readItem.SetValue(metrics.read)
	slot.readItem.SetAvailable(true)
	slot.writeItem.SetValue(metrics.write)
	slot.writeItem.SetAvailable(true)
	slot.readIOPSItem.SetValue(metrics.readIOPS)
	slot.readIOPSItem.SetAvailable(true)
	slot.writeIOPSItem.SetValue(metrics.writeIOPS)
	slot.writeIOPSItem.SetAvailable(true)
	slot.readLatencyItem.SetValue(metrics.readLatencyMS)
	slot.readLatencyItem.SetAvailable(true)
	slot.writeLatencyItem.SetValue(metrics.writeLatencyMS)
	slot.writeLatencyItem.SetAvailable(true)
	slot.busyItem.SetValue(metrics.busyPercent)
	slot.busyItem.SetAvailable(true)
}

func computeDiskMetrics(current diskRateSnapshot, previous diskRateSnapshot) (*diskComputedMetrics, bool) {
	if current.at.IsZero() || previous.at.IsZero() {
		return nil, false
	}
	seconds := current.at.Sub(previous.at).Seconds()
	elapsedMS := current.at.Sub(previous.at).Seconds() * 1000
	if seconds <= 0 || elapsedMS <= 0 ||
		current.ReadBytes < previous.ReadBytes ||
		current.WriteBytes < previous.WriteBytes ||
		current.ReadCount < previous.ReadCount ||
		current.WriteCount < previous.WriteCount ||
		current.ReadTimeMS < previous.ReadTimeMS ||
		current.WriteTimeMS < previous.WriteTimeMS ||
		current.BusyTimeMS < previous.BusyTimeMS {
		return nil, false
	}

	readBytesDelta := current.ReadBytes - previous.ReadBytes
	writeBytesDelta := current.WriteBytes - previous.WriteBytes
	readCountDelta := current.ReadCount - previous.ReadCount
	writeCountDelta := current.WriteCount - previous.WriteCount
	readTimeDelta := current.ReadTimeMS - previous.ReadTimeMS
	writeTimeDelta := current.WriteTimeMS - previous.WriteTimeMS
	busyTimeDelta := current.BusyTimeMS - previous.BusyTimeMS

	metrics := &diskComputedMetrics{
		read:       float64(readBytesDelta) / seconds / 1024 / 1024,
		write:      float64(writeBytesDelta) / seconds / 1024 / 1024,
		readIOPS:   float64(readCountDelta) / seconds,
		writeIOPS:  float64(writeCountDelta) / seconds,
		queueDepth: current.QueueDepth,
	}
	if readCountDelta > 0 {
		metrics.readLatencyMS = readTimeDelta / float64(readCountDelta)
	}
	if writeCountDelta > 0 {
		metrics.writeLatencyMS = writeTimeDelta / float64(writeCountDelta)
	}
	if busyTimeDelta > 0 {
		metrics.busyPercent = clampPercentage(busyTimeDelta / elapsedMS * 100.0)
	}
	return metrics, true
}

func (c *GoNativeDiskCollector) GetAllItems() map[string]*CollectItem {
	disks := c.snapshotDisks()
	c.ensureSlotsForCount(len(disks))
	for index, slot := range c.slots {
		var disk *DiskInfo
		if index > 0 && index <= len(disks) {
			disk = disks[index-1]
		}
		updateDiskStaticItems(slot, disk)
		updateDiskRateItems(slot, disk)
		updateDiskTemperatureItem(slot, disk)
	}
	return c.ItemsSnapshot()
}

func (c *GoNativeDiskCollector) diskState(name string) *runtimeDiskMetricsState {
	key := strings.TrimSpace(name)
	if key == "" {
		return nil
	}
	state := c.runtimeMu.states[key]
	if state == nil {
		state = &runtimeDiskMetricsState{}
		c.runtimeMu.states[key] = state
	}
	return state
}

func (c *GoNativeDiskCollector) UpdateItems() error {
	if !c.IsEnabled() {
		return nil
	}
	disks := c.snapshotDisks()
	names := make([]string, 0, len(disks))
	for _, disk := range disks {
		if disk == nil || strings.TrimSpace(disk.Name) == "" {
			continue
		}
		names = append(names, disk.Name)
	}
	samples := getDiskCounterSamples(names)
	temperatureSnapshots := map[string]diskTemperatureSnapshot{}
	if diskTemperatureItemsEnabled(c.slots) {
		temperatureSnapshots = getDiskTemperatureSnapshots(names)
	}
	for index, slot := range c.slots {
		if slot == nil {
			continue
		}
		var disk *DiskInfo
		if index > 0 && index <= len(disks) {
			disk = disks[index-1]
		}
		if slot.nameItem.IsEnabled() || slot.sizeItem.IsEnabled() ||
			slot.usedItem.IsEnabled() || slot.availableItem.IsEnabled() || slot.usageItem.IsEnabled() {
			updateDiskStaticItems(slot, disk)
		}
		if slot.readItem.IsEnabled() || slot.writeItem.IsEnabled() ||
			slot.readIOPSItem.IsEnabled() || slot.writeIOPSItem.IsEnabled() ||
			slot.readLatencyItem.IsEnabled() || slot.writeLatencyItem.IsEnabled() ||
			slot.busyItem.IsEnabled() {
			if disk == nil {
				updateDiskRateItems(slot, nil)
			} else {
				stateName := strings.TrimSpace(disk.Name)
				state := c.diskState(stateName)
				if state == nil {
					updateDiskRateItems(slot, nil)
				} else if sample, ok := samples[stateName]; ok {
					if state.hasLast {
						if metrics, metricsOK := computeDiskMetrics(sample, state.last); metricsOK {
							state.lastGood = smoothDiskMetrics(state.lastGood, metrics)
							state.validUntil = time.Now().Add(15 * time.Second)
							setDiskDynamicMetrics(slot, state.lastGood)
						} else if state.lastGood != nil && time.Now().Before(state.validUntil) {
							setDiskDynamicMetrics(slot, state.lastGood)
						} else {
							updateDiskRateItems(slot, nil)
						}
					} else {
						zero := &diskComputedMetrics{}
						state.lastGood = zero
						state.validUntil = time.Now().Add(15 * time.Second)
						setDiskDynamicMetrics(slot, zero)
					}
					state.last = sample
					state.hasLast = true
				} else if state.lastGood != nil && time.Now().Before(state.validUntil) {
					setDiskDynamicMetrics(slot, state.lastGood)
				} else {
					updateDiskRateItems(slot, nil)
				}
			}
		}
		if slot.tempItem != nil && slot.tempItem.IsEnabled() {
			updateDiskTemperatureSnapshotItem(slot, disk, temperatureSnapshots)
		}
	}
	return nil
}
