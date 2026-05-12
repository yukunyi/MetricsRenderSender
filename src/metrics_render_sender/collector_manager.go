package main

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type CollectValue struct {
	Value     interface{}
	Unit      string
	Min       float64
	Max       float64
	Precision int
}

type BaseCollectItem struct {
	name        string
	label       string
	value       *CollectValue
	available   bool
	enabled     bool
	rateWindow  time.Duration
	rateSamples []rateSample
	version     uint64
	mutex       sync.RWMutex
}

type rateSample struct {
	at    time.Time
	value float64
}

const defaultThroughputRateWindow = 3 * time.Second

func NewBaseCollectItem(name, label string, min, max float64, unit string, precision int) *BaseCollectItem {
	return &BaseCollectItem{
		name:       name,
		label:      label,
		available:  true,
		enabled:    true,
		rateWindow: builtinRateWindow(name, unit),
		value: &CollectValue{
			Value:     0.0,
			Unit:      unit,
			Min:       min,
			Max:       max,
			Precision: precision,
		},
	}
}

func (b *BaseCollectItem) GetName() string  { return b.name }
func (b *BaseCollectItem) GetLabel() string { return b.label }

func (b *BaseCollectItem) GetValue() *CollectValue {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	if b.value == nil {
		return nil
	}
	copied := *b.value
	return &copied
}

func (b *BaseCollectItem) SnapshotState() (uint64, bool, *CollectValue) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	var copied *CollectValue
	if b.value != nil {
		valueCopy := *b.value
		copied = &valueCopy
	}
	return b.version, b.available, copied
}

func (b *BaseCollectItem) Version() uint64 {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	return b.version
}

func (b *BaseCollectItem) IsAvailable() bool {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	return b.available
}

func (b *BaseCollectItem) IsEnabled() bool {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	return b.enabled
}

func (b *BaseCollectItem) SetEnabled(enabled bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.enabled = enabled
	b.version++
}

func (b *BaseCollectItem) SetValue(value interface{}) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if b.value == nil {
		return
	}
	if b.rateWindow > 0 {
		if numeric, ok := toRateFloat64(value); ok {
			now := time.Now()
			b.rateSamples = append(b.rateSamples, rateSample{at: now, value: numeric})
			cutoff := now.Add(-b.rateWindow)
			total := 0.0
			validCount := 0
			writeIndex := 0
			for _, sample := range b.rateSamples {
				if sample.at.Before(cutoff) {
					continue
				}
				b.rateSamples[writeIndex] = sample
				writeIndex++
				total += sample.value
				validCount++
			}
			b.rateSamples = b.rateSamples[:writeIndex]
			if validCount > 0 {
				value = total / float64(validCount)
			}
		} else {
			b.rateSamples = b.rateSamples[:0]
		}
	}
	b.value.Value = value
	b.version++
}

func (b *BaseCollectItem) SetUnit(unit string) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if b.value == nil {
		return
	}
	b.value.Unit = unit
	b.rateWindow = builtinRateWindow(b.name, unit)
	if b.rateWindow <= 0 {
		b.rateSamples = nil
	}
	b.version++
}

func (b *BaseCollectItem) SetAvailable(available bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.available = available
	b.version++
}

func FormatCollectValue(value *CollectValue, showUnit bool, unitOverride string) string {
	numberText, unitText := FormatCollectValueParts(value, unitOverride)
	if !showUnit || unitText == "" {
		return numberText
	}
	return numberText + unitText
}

func FormatCollectValueParts(value *CollectValue, unitOverride string) (string, string) {
	if value == nil {
		return "N/A", ""
	}
	unit := value.Unit
	autoScale := true
	if unitOverride != "" {
		unit = unitOverride
		autoScale = false
	}
	switch v := value.Value.(type) {
	case string:
		return v, ""
	case float64, float32, int, int64, uint64:
		val := getFloat64Value(v)
		precision := value.Precision
		if autoScale {
			val, unit, precision = autoScaleUnitValue(val, unit, precision)
		}
		format := "%." + itoa(max(0, precision)) + "f"
		return fmt.Sprintf(format, val), unit
	default:
		return fmt.Sprintf("%v", value.Value), ""
	}
}

func autoScaleUnitValue(value float64, unit string, precision int) (float64, string, int) {
	trimmedUnit := strings.ToLower(strings.TrimSpace(unit))
	if trimmedUnit == "" {
		return value, unit, precision
	}
	family, index, scaleFactor, ok := getAutoScaleFamily(trimmedUnit)
	if !ok || index < 0 || index >= len(family) {
		return value, unit, precision
	}
	scaled := value
	absValue := math.Abs(value)
	if absValue > 0 {
		for absValue >= scaleFactor && index < len(family)-1 {
			scaled /= scaleFactor
			index++
			absValue = math.Abs(scaled)
		}
		for absValue > 0 && absValue < 1 && index > 0 {
			scaled *= scaleFactor
			index--
			absValue = math.Abs(scaled)
		}
	}
	scaledUnit := family[index]
	if strings.HasPrefix(unit, " ") {
		scaledUnit = " " + scaledUnit
	}
	return scaled, scaledUnit, autoScalePrecision(scaled, precision, scaledUnit != strings.TrimSpace(unit))
}

func getAutoScaleFamily(unit string) ([]string, int, float64, bool) {
	switch unit {
	case "b", "kb", "mb", "gb", "tb":
		return []string{"B", "KB", "MB", "GB", "TB"}, unitIndex(unit, []string{"b", "kb", "mb", "gb", "tb"}), 1024, true
	case "b/s", "kb/s", "mb/s", "gb/s", "tb/s":
		return []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}, unitIndex(unit, []string{"b/s", "kb/s", "mb/s", "gb/s", "tb/s"}), 1024, true
	case "kib", "mib", "gib", "tib":
		return []string{"B", "KiB", "MiB", "GiB", "TiB"}, unitIndex(unit, []string{"b", "kib", "mib", "gib", "tib"}), 1024, true
	case "kib/s", "mib/s", "gib/s", "tib/s":
		return []string{"B/s", "KiB/s", "MiB/s", "GiB/s", "TiB/s"}, unitIndex(unit, []string{"b/s", "kib/s", "mib/s", "gib/s", "tib/s"}), 1024, true
	case "hz", "khz", "mhz", "ghz", "thz":
		return []string{"Hz", "KHz", "MHz", "GHz", "THz"}, unitIndex(unit, []string{"hz", "khz", "mhz", "ghz", "thz"}), 1000, true
	default:
		return nil, -1, 0, false
	}
}

func unitIndex(unit string, family []string) int {
	for idx, value := range family {
		if unit == value {
			return idx
		}
	}
	return -1
}

func autoScalePrecision(value float64, defaultPrecision int, scaled bool) int {
	precision := max(0, defaultPrecision)
	if !scaled {
		return precision
	}
	absValue := math.Abs(value)
	switch {
	case absValue >= 100:
		return 0
	case absValue >= 10:
		return max(1, min(precision, 1))
	case absValue >= 1:
		return max(1, min(precision, 2))
	default:
		return max(2, precision)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func getFloat64Value(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	default:
		return 0.0
	}
}

func builtinRateWindow(name, unit string) time.Duration {
	trimmedName := strings.ToLower(strings.TrimSpace(name))
	trimmedUnit := strings.ToLower(strings.TrimSpace(unit))
	if !strings.HasPrefix(trimmedName, "go_native.") {
		return 0
	}
	if strings.Contains(trimmedUnit, "/s") {
		return defaultThroughputRateWindow
	}
	return 0
}

func toRateFloat64(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}

func defaultMonitorWorkerCount() int {
	workers := runtime.NumCPU()
	if workers < 2 {
		return 2
	}
	if workers > 8 {
		return 8
	}
	return workers
}

func defaultMonitorQueueSize(workers int) int {
	queue := workers * 8
	if queue < 16 {
		return 16
	}
	if queue > 256 {
		return 256
	}
	return queue
}

type CollectorManagerStats struct {
	WorkerCount int  `json:"worker_count"`
	QueueSize   int  `json:"queue_size"`
	QueueLen    int  `json:"queue_len"`
	Paused      bool `json:"paused"`

	AutoTune          bool    `json:"auto_tune"`
	AutoTuneIntervalS int     `json:"auto_tune_interval_sec"`
	AutoTuneSlowRate  float64 `json:"auto_tune_slow_rate"`
	AutoTuneStableRun int     `json:"auto_tune_stable_runs"`
	AutoTuneMaxScale  int     `json:"auto_tune_max_scale"`
	IntervalScale     int     `json:"interval_scale"`

	ScheduledTotal int64 `json:"scheduled_total"`
	DroppedTotal   int64 `json:"dropped_total"`
	SlowTotal      int64 `json:"slow_total"`
	CompletedTotal int64 `json:"completed_total"`

	LastWindowScheduled int64 `json:"last_window_scheduled"`
	LastWindowDropped   int64 `json:"last_window_dropped"`
	LastWindowSlow      int64 `json:"last_window_slow"`
	LastWindowCompleted int64 `json:"last_window_completed"`

	CollectorCount        int `json:"collector_count"`
	EnabledCollectorCount int `json:"enabled_collector_count"`
	ItemCount             int `json:"item_count"`
	EnabledItemCount      int `json:"enabled_item_count"`

	CurrentEpoch          int64 `json:"current_epoch"`
	LastRenderEpoch       int64 `json:"last_render_epoch"`
	LastRenderWaitMS      int64 `json:"last_render_wait_ms"`
	LastRenderComplete    bool  `json:"last_render_complete"`
	CollectorTimeoutTotal int64 `json:"collector_timeout_total"`

	LastCollectMaxMS int64 `json:"last_collect_max_ms"`
	LastCollectAvgMS int64 `json:"last_collect_avg_ms"`
	CollectMaxMS     int64 `json:"collect_max_ms"`
	CollectAvgMS     int64 `json:"collect_avg_ms"`

	RenderLastMS int64 `json:"render_last_ms"`
	RenderMaxMS  int64 `json:"render_max_ms"`
	RenderAvgMS  int64 `json:"render_avg_ms"`

	OutputLastMS int64                                `json:"output_last_ms"`
	OutputMaxMS  int64                                `json:"output_max_ms"`
	OutputAvgMS  int64                                `json:"output_avg_ms"`
	OutputStats  map[string]OutputHandlerRuntimeStats `json:"output_stats,omitempty"`
	TCPPushStats map[string]TCPPushAvailabilityStats  `json:"tcp_push_stats,omitempty"`
}

type CollectorManager struct {
	collectors       map[string]Collector
	collectorOrder   []string
	collectorEnabled map[string]bool
	items            map[string]*CollectItem
	itemToCollector  map[string]string
	allItemsSnapshot map[string]*CollectItem
	allNamesSnapshot []string
	snapshotDirty    bool
	snapshotVersion  uint64

	requiredSet      map[string]struct{}
	requiredResolved map[string]struct{}
	requiredSig      string
	modeFull         bool
	previewMode      bool
	paused           bool

	missingRetryAttempts int
	missingRetryNextAt   time.Time

	lastWindowScheduled int64
	lastWindowDropped   int64
	lastWindowSlow      int64
	lastWindowCompleted int64
	totalScheduled      int64
	totalDropped        int64
	totalSlow           int64
	totalCompleted      int64
	lastCollectMax      time.Duration
	lastCollectAvg      time.Duration
	totalCollectNS      int64
	totalCollectCount   int64
	collectMax          time.Duration

	closed int32
	mutex  sync.RWMutex

	tickDuration    time.Duration
	collectWarn     time.Duration
	renderWaitMax   time.Duration
	currentEpoch    int64
	lastRenderEpoch int64
	lastRenderWait  time.Duration
	lastRenderFull  bool
	timeoutTotal    int64

	epochCond   *sync.Cond
	epochStates map[int64]*collectorEpochState
	workerChans map[string]chan int64
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	started     bool
}

type collectorEpochState struct {
	doneCh     chan struct{}
	expected   int
	completed  int
	success    int
	dropped    int
	slow       int
	durationNS int64
	maxNS      int64
	collectors map[string]struct{}
}

func NewCollectorManager() *CollectorManager {
	manager := &CollectorManager{
		collectors:       make(map[string]Collector),
		collectorEnabled: make(map[string]bool),
		items:            make(map[string]*CollectItem),
		itemToCollector:  make(map[string]string),
		snapshotDirty:    true,
		requiredSet:      make(map[string]struct{}),
		requiredResolved: make(map[string]struct{}),
		epochStates:      make(map[int64]*collectorEpochState),
		workerChans:      make(map[string]chan int64),
		stopCh:           make(chan struct{}),
		tickDuration:     time.Second,
		collectWarn:      100 * time.Millisecond,
		renderWaitMax:    300 * time.Millisecond,
	}
	manager.epochCond = sync.NewCond(&manager.mutex)
	return manager
}

func (m *CollectorManager) RegisterCollector(c Collector) {
	if c == nil {
		return
	}
	name := strings.TrimSpace(c.Name())
	if name == "" {
		return
	}
	m.mutex.Lock()
	if _, exists := m.collectors[name]; !exists {
		m.collectorOrder = append(m.collectorOrder, name)
	}
	m.collectors[name] = c
	if _, exists := m.collectorEnabled[name]; !exists {
		m.collectorEnabled[name] = true
	}
	m.mutex.Unlock()
}

type namedCollector struct {
	name      string
	collector Collector
}

type collectorDiscoveryResult struct {
	discovered int
	newItems   int
	totalItems int
}

func (m *CollectorManager) snapshotCollectors() []namedCollector {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	result := make([]namedCollector, 0, len(m.collectorOrder))
	for _, name := range m.collectorOrder {
		collector := m.collectors[name]
		if collector == nil {
			continue
		}
		result = append(result, namedCollector{name: name, collector: collector})
	}
	return result
}

func (m *CollectorManager) discoverFromCollectors(collectors []namedCollector, trigger string) collectorDiscoveryResult {
	if len(collectors) == 0 {
		return collectorDiscoveryResult{}
	}
	discoveredItems := make(map[string]*CollectItem)
	discoveredOwners := make(map[string]string)
	for _, entry := range collectors {
		startedAt, warnThreshold, slowTimer := m.startDiscoverSlowWatch(entry.name, trigger)
		items := entry.collector.GetAllItems()
		duration := time.Since(startedAt)
		m.finishDiscoverSlowWatch(entry.name, trigger, duration, warnThreshold, slowTimer)
		for key, item := range items {
			trimmed := strings.TrimSpace(key)
			if trimmed == "" || item == nil {
				continue
			}
			discoveredItems[trimmed] = item
			discoveredOwners[trimmed] = entry.name
		}
	}
	if len(discoveredItems) == 0 {
		m.mutex.RLock()
		totalItems := len(m.items)
		m.mutex.RUnlock()
		return collectorDiscoveryResult{totalItems: totalItems}
	}
	m.mutex.Lock()
	newItems := 0
	for key, item := range discoveredItems {
		if _, exists := m.items[key]; !exists {
			newItems++
		}
		m.items[key] = item
		m.itemToCollector[key] = discoveredOwners[key]
	}
	m.invalidateItemSnapshotsLocked()
	m.rebuildRequiredResolvedLocked()
	totalItems := len(m.items)
	m.mutex.Unlock()
	return collectorDiscoveryResult{
		discovered: len(discoveredItems),
		newItems:   newItems,
		totalItems: totalItems,
	}
}

func (m *CollectorManager) startDiscoverSlowWatch(name, trigger string) (time.Time, time.Duration, *time.Timer) {
	m.mutex.RLock()
	warnThreshold := m.collectWarn
	m.mutex.RUnlock()
	startedAt := time.Now()
	if warnThreshold <= 0 {
		return startedAt, 0, nil
	}
	slowTimer := time.AfterFunc(warnThreshold, func() {
		duration := time.Since(startedAt)
		if duration < warnThreshold {
			duration = warnThreshold
		}
		logWarnModule(
			"collect",
			"action=discover,result=slow,collector=%s,trigger=%s,duration_ms=%d,threshold_ms=%d",
			name,
			strings.TrimSpace(trigger),
			duration.Milliseconds(),
			warnThreshold.Milliseconds(),
		)
	})
	return startedAt, warnThreshold, slowTimer
}

func (m *CollectorManager) finishDiscoverSlowWatch(name, trigger string, duration, warnThreshold time.Duration, slowTimer *time.Timer) {
	if warnThreshold <= 0 || duration <= warnThreshold {
		if slowTimer != nil {
			slowTimer.Stop()
		}
		return
	}
	if slowTimer != nil && !slowTimer.Stop() {
		return
	}
	logWarnModule(
		"collect",
		"action=discover,result=slow,collector=%s,trigger=%s,duration_ms=%d,threshold_ms=%d",
		name,
		strings.TrimSpace(trigger),
		duration.Milliseconds(),
		warnThreshold.Milliseconds(),
	)
}

func (m *CollectorManager) invalidateItemSnapshotsLocked() {
	m.allItemsSnapshot = nil
	m.allNamesSnapshot = nil
	m.snapshotDirty = true
	m.snapshotVersion++
}

func (m *CollectorManager) rebuildItemSnapshotsLocked() {
	if !m.snapshotDirty {
		return
	}
	items := make(map[string]*CollectItem, len(m.items))
	for name, item := range m.items {
		items[name] = item
	}
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	m.allItemsSnapshot = items
	m.allNamesSnapshot = names
	m.snapshotDirty = false
}

func (m *CollectorManager) discoverAll(trigger string) {
	collectors := m.snapshotCollectors()
	result := m.discoverFromCollectors(collectors, trigger)
	logInfoModule(
		"collect",
		"action=discover,trigger=%s,collectors=%d,discovered=%d,new=%d,total=%d",
		strings.TrimSpace(trigger),
		len(collectors),
		result.discovered,
		result.newItems,
		result.totalItems,
	)
}

func normalizeCollectorSnapshotItems(items map[string]*CollectItem) map[string]*CollectItem {
	if len(items) == 0 {
		return map[string]*CollectItem{}
	}
	normalized := make(map[string]*CollectItem, len(items))
	for key, item := range items {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" || item == nil {
			continue
		}
		normalized[trimmed] = item
	}
	return normalized
}

func (m *CollectorManager) syncCollectorItemsFromSnapshots(collectors []namedCollector) {
	if len(collectors) == 0 {
		return
	}
	snapshots := make(map[string]map[string]*CollectItem, len(collectors))
	for _, entry := range collectors {
		snapshotter, ok := entry.collector.(CollectorItemSnapshotProvider)
		if !ok {
			continue
		}
		snapshots[entry.name] = normalizeCollectorSnapshotItems(snapshotter.ItemsSnapshot())
	}
	if len(snapshots) == 0 {
		return
	}

	m.mutex.Lock()
	changed := false
	for collectorName, items := range snapshots {
		for key, owner := range m.itemToCollector {
			if owner != collectorName {
				continue
			}
			if _, exists := items[key]; exists {
				continue
			}
			delete(m.itemToCollector, key)
			delete(m.items, key)
			changed = true
		}
		for key, item := range items {
			if m.items[key] != item || m.itemToCollector[key] != collectorName {
				changed = true
			}
			m.items[key] = item
			m.itemToCollector[key] = collectorName
		}
	}
	if changed {
		m.invalidateItemSnapshotsLocked()
		m.rebuildRequiredResolvedLocked()
	}
	m.mutex.Unlock()
}

func signatureOfNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if !isMonitorSupportedOnCurrentPlatform(trimmed) {
			continue
		}
		set[trimmed] = struct{}{}
	}
	uniq := make([]string, 0, len(set))
	for key := range set {
		uniq = append(uniq, key)
	}
	sort.Strings(uniq)
	return strings.Join(uniq, "\n")
}

func (m *CollectorManager) setRequiredItems(names []string) {
	sig := signatureOfNames(names)
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if sig == m.requiredSig {
		return
	}
	m.requiredSig = sig
	m.requiredSet = make(map[string]struct{})
	if sig != "" {
		for _, name := range strings.Split(sig, "\n") {
			if name != "" {
				m.requiredSet[normalizeMonitorNameInput(name)] = struct{}{}
			}
		}
	}
	m.rebuildRequiredResolvedLocked()
	m.missingRetryAttempts = 0
	m.missingRetryNextAt = time.Time{}
}

func (m *CollectorManager) requiredItemsSnapshot() []string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	result := make([]string, 0, len(m.requiredSet))
	for name := range m.requiredSet {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func (m *CollectorManager) rebuildRequiredResolvedLocked() {
	m.requiredResolved = make(map[string]struct{}, len(m.requiredSet))
	for name := range m.requiredSet {
		trimmed := normalizeMonitorNameInput(name)
		if trimmed == "" {
			continue
		}
		m.requiredResolved[trimmed] = struct{}{}
	}
}

func (m *CollectorManager) missingRequiredCountLocked() int {
	missing := 0
	for name := range m.requiredSet {
		if !m.isRequiredItemSatisfiedLocked(name) {
			missing++
		}
	}
	return missing
}

func (m *CollectorManager) isRequiredItemSatisfiedLocked(name string) bool {
	trimmed := normalizeMonitorNameInput(name)
	if trimmed == "" {
		return true
	}
	_, exists := m.items[trimmed]
	return exists
}

func (m *CollectorManager) setItemEnabledStatesLocked() {
	for name, item := range m.items {
		if item == nil {
			continue
		}
		collectorName := m.itemToCollector[name]
		collectorEnabled := m.collectorEnabled[collectorName]
		enabled := collectorEnabled
		if !m.modeFull {
			_, required := m.requiredResolved[name]
			enabled = collectorEnabled && required
		}
		item.SetEnabled(enabled)
	}
}

func (m *CollectorManager) hasEnabledItemsLocked(collectorName string) bool {
	for itemName, owner := range m.itemToCollector {
		if owner != collectorName {
			continue
		}
		item := m.items[itemName]
		if item != nil && item.IsEnabled() {
			return true
		}
	}
	return false
}

func (m *CollectorManager) maybeRetryDiscover(now time.Time) {
	m.mutex.Lock()
	if len(m.requiredSet) == 0 {
		m.mutex.Unlock()
		return
	}
	missing := false
	for key := range m.requiredSet {
		if !m.isRequiredItemSatisfiedLocked(key) {
			missing = true
			break
		}
	}
	if !missing {
		m.missingRetryAttempts = 0
		m.missingRetryNextAt = time.Time{}
		m.mutex.Unlock()
		return
	}
	if m.missingRetryAttempts >= 10 {
		m.mutex.Unlock()
		return
	}
	if !m.missingRetryNextAt.IsZero() && now.Before(m.missingRetryNextAt) {
		m.mutex.Unlock()
		return
	}
	attempt := m.missingRetryAttempts + 1
	missingBefore := m.missingRequiredCountLocked()
	collectors := make([]namedCollector, 0, len(m.collectorOrder))
	for _, name := range m.collectorOrder {
		if collector := m.collectors[name]; collector != nil {
			collectors = append(collectors, namedCollector{name: name, collector: collector})
		}
	}
	m.mutex.Unlock()

	result := m.discoverFromCollectors(collectors, "auto")

	m.mutex.Lock()
	defer m.mutex.Unlock()
	missingAfter := m.missingRequiredCountLocked()
	resolved := missingAfter == 0
	logInfoModule(
		"collect",
		"action=discover,trigger=auto,reason=missing_required,attempt=%d,collectors=%d,discovered=%d,new=%d,total=%d,missing_before=%d,missing_after=%d",
		attempt,
		len(collectors),
		result.discovered,
		result.newItems,
		result.totalItems,
		missingBefore,
		missingAfter,
	)
	if resolved {
		m.missingRetryAttempts = 0
		m.missingRetryNextAt = time.Time{}
		return
	}
	m.missingRetryAttempts++
	m.missingRetryNextAt = now.Add(time.Minute)
}

func alignedNextTick(now time.Time, tick time.Duration) time.Time {
	if tick <= 0 {
		tick = time.Second
	}
	base := now.Truncate(tick)
	if !base.After(now) {
		base = base.Add(tick)
	}
	return base
}

func (m *CollectorManager) configureRuntimeFromConfig(cfg *MonitorConfig) {
	tick := time.Second
	collectWarn := 100 * time.Millisecond
	renderWait := 300 * time.Millisecond
	if cfg != nil {
		tick = cfg.GetCollectTickDuration()
		collectWarn = cfg.GetCollectWarnDuration()
		renderWait = cfg.GetRenderWaitMaxDuration()
	}
	if tick <= 0 {
		tick = time.Second
	}
	if collectWarn < 0 {
		collectWarn = 0
	}
	if renderWait < 0 {
		renderWait = 0
	}

	m.mutex.Lock()
	m.tickDuration = tick
	m.collectWarn = collectWarn
	m.renderWaitMax = renderWait
	m.mutex.Unlock()
}

func (m *CollectorManager) startCollectorWorkers() {
	collectors := m.snapshotCollectors()
	for _, entry := range collectors {
		entry := entry
		ch := make(chan int64, 1)

		m.mutex.Lock()
		m.workerChans[entry.name] = ch
		m.mutex.Unlock()

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			for {
				select {
				case <-m.stopCh:
					return
				case epochID := <-ch:
					m.runCollectorEpoch(entry.name, entry.collector, epochID)
				}
			}
		}()
	}
}

func (m *CollectorManager) runCollectorEpoch(name string, collector Collector, epochID int64) {
	if collector == nil || epochID <= 0 {
		return
	}

	startedAt := time.Now()
	err := func() (retErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				retErr = fmt.Errorf("panic: %v", recovered)
			}
		}()
		return collector.UpdateItems()
	}()
	duration := time.Since(startedAt)

	m.mutex.RLock()
	warnThreshold := m.collectWarn
	m.mutex.RUnlock()

	slow := duration > warnThreshold && warnThreshold > 0
	if slow {
		atomic.AddInt64(&m.timeoutTotal, 1)
		logWarnModule(
			"collect",
			"action=update,result=slow,collector=%s,epoch=%d,duration_ms=%d,threshold_ms=%d",
			name,
			epochID,
			duration.Milliseconds(),
			warnThreshold.Milliseconds(),
		)
	}

	m.mutex.Lock()
	state := m.epochStates[epochID]
	if state == nil {
		m.mutex.Unlock()
		return
	}
	if _, expected := state.collectors[name]; !expected {
		m.mutex.Unlock()
		return
	}
	delete(state.collectors, name)
	state.completed++
	state.durationNS += duration.Nanoseconds()
	if duration.Nanoseconds() > state.maxNS {
		state.maxNS = duration.Nanoseconds()
	}
	m.totalCollectNS += duration.Nanoseconds()
	m.totalCollectCount++
	if duration > m.collectMax {
		m.collectMax = duration
	}
	if slow {
		state.slow++
		m.totalSlow++
	}
	if err != nil {
		state.dropped++
		m.totalDropped++
		logDebugModule("collect", "action=update,result=failed,collector=%s,epoch=%d,error=%q", name, epochID, err)
	} else {
		state.success++
		m.totalCompleted++
	}
	if state.completed >= state.expected {
		m.lastWindowScheduled = int64(state.expected)
		m.lastWindowDropped = int64(state.dropped)
		m.lastWindowSlow = int64(state.slow)
		m.lastWindowCompleted = int64(state.success)
		if state.completed > 0 {
			m.lastCollectMax = time.Duration(state.maxNS)
			m.lastCollectAvg = time.Duration(state.durationNS / int64(state.completed))
		}
		select {
		case <-state.doneCh:
		default:
			close(state.doneCh)
		}
	}
	m.mutex.Unlock()
}

func (m *CollectorManager) snapshotActiveCollectorsLocked() []namedCollector {
	m.setItemEnabledStatesLocked()
	result := make([]namedCollector, 0, len(m.collectorOrder))
	for _, collectorName := range m.collectorOrder {
		collector := m.collectors[collectorName]
		if collector == nil {
			continue
		}
		if !m.collectorEnabled[collectorName] {
			continue
		}
		if !m.hasEnabledItemsLocked(collectorName) {
			continue
		}
		result = append(result, namedCollector{name: collectorName, collector: collector})
	}
	return result
}

func trySendLatestEpoch(ch chan int64, epochID int64) {
	if ch == nil {
		return
	}
	select {
	case ch <- epochID:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- epochID:
	default:
	}
}

func (m *CollectorManager) dispatchEpoch(epochID int64, active []namedCollector) {
	for _, entry := range active {
		m.mutex.RLock()
		ch := m.workerChans[entry.name]
		m.mutex.RUnlock()
		trySendLatestEpoch(ch, epochID)
	}
}

func (m *CollectorManager) pruneEpochStatesLocked(currentEpoch int64) {
	keepFrom := currentEpoch - 8
	for epochID := range m.epochStates {
		if epochID < keepFrom {
			delete(m.epochStates, epochID)
		}
	}
}

func (m *CollectorManager) runEpochScheduler() {
	defer m.wg.Done()

	m.mutex.RLock()
	tick := m.tickDuration
	m.mutex.RUnlock()
	nextTick := alignedNextTick(time.Now(), tick)
	timer := time.NewTimer(time.Until(nextTick))
	defer timer.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-timer.C:
		}

		now := time.Now()
		m.mutex.RLock()
		tick = m.tickDuration
		m.mutex.RUnlock()
		if tick <= 0 {
			tick = time.Second
		}

		epochID := atomic.AddInt64(&m.currentEpoch, 1)
		active := func() []namedCollector {
			m.mutex.Lock()
			defer m.mutex.Unlock()
			activeCollectors := []namedCollector{}
			if !m.paused {
				activeCollectors = m.snapshotActiveCollectorsLocked()
			}
			state := &collectorEpochState{
				doneCh:     make(chan struct{}),
				expected:   len(activeCollectors),
				collectors: make(map[string]struct{}, len(activeCollectors)),
			}
			for _, entry := range activeCollectors {
				state.collectors[entry.name] = struct{}{}
			}
			if state.expected == 0 {
				close(state.doneCh)
			}
			m.epochStates[epochID] = state
			m.totalScheduled += int64(state.expected)
			m.pruneEpochStatesLocked(epochID)
			m.epochCond.Broadcast()
			return activeCollectors
		}()

		m.dispatchEpoch(epochID, active)
		m.maybeRetryDiscover(now)

		nextTick = nextTick.Add(tick)
		now = time.Now()
		for !nextTick.After(now) {
			nextTick = nextTick.Add(tick)
		}
		timer.Reset(time.Until(nextTick))
	}
}

func (m *CollectorManager) startAsyncIfNeeded() {
	if atomic.LoadInt32(&m.closed) == 1 {
		return
	}
	m.mutex.Lock()
	if m.started {
		m.mutex.Unlock()
		return
	}
	m.started = true
	m.mutex.Unlock()
	m.startCollectorWorkers()
	m.wg.Add(1)
	go m.runEpochScheduler()
}

func (m *CollectorManager) WaitForNextEpoch(lastEpoch int64, maxWait time.Duration) (int64, bool, time.Duration) {
	m.mutex.Lock()
	for atomic.LoadInt32(&m.closed) == 0 && m.currentEpoch <= lastEpoch {
		m.epochCond.Wait()
	}
	if atomic.LoadInt32(&m.closed) == 1 {
		m.mutex.Unlock()
		return lastEpoch, false, 0
	}
	epochID := m.currentEpoch
	state := m.epochStates[epochID]
	waitDefault := m.renderWaitMax
	m.mutex.Unlock()

	if maxWait <= 0 {
		maxWait = waitDefault
	}
	if maxWait < 0 {
		maxWait = 0
	}
	if state == nil {
		return epochID, false, 0
	}

	waitStart := time.Now()
	completed := false
	if maxWait == 0 {
		select {
		case <-state.doneCh:
			completed = true
		default:
		}
	} else {
		timer := time.NewTimer(maxWait)
		select {
		case <-state.doneCh:
			completed = true
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	waited := time.Since(waitStart)

	m.mutex.Lock()
	m.lastRenderEpoch = epochID
	m.lastRenderWait = waited
	m.lastRenderFull = completed
	m.mutex.Unlock()

	return epochID, completed, waited
}

func (m *CollectorManager) CurrentEpoch() int64 {
	return atomic.LoadInt64(&m.currentEpoch)
}

func (m *CollectorManager) WaitForEpoch(epochID int64, maxWait time.Duration) (bool, time.Duration) {
	if epochID <= 0 {
		return false, 0
	}
	m.mutex.RLock()
	state := m.epochStates[epochID]
	waitDefault := m.renderWaitMax
	m.mutex.RUnlock()
	if state == nil {
		return false, 0
	}
	if maxWait <= 0 {
		maxWait = waitDefault
	}
	if maxWait < 0 {
		maxWait = 0
	}

	waitStart := time.Now()
	completed := false
	if maxWait == 0 {
		select {
		case <-state.doneCh:
			completed = true
		default:
		}
	} else {
		timer := time.NewTimer(maxWait)
		select {
		case <-state.doneCh:
			completed = true
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	waited := time.Since(waitStart)

	m.mutex.Lock()
	m.lastRenderEpoch = epochID
	m.lastRenderWait = waited
	m.lastRenderFull = completed
	m.mutex.Unlock()

	return completed, waited
}

func (m *CollectorManager) Get(name string) *CollectItem {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	normalized := normalizeMonitorNameInput(name)
	if normalized == "" {
		return nil
	}
	return m.items[normalized]
}

func (m *CollectorManager) GetAll() map[string]*CollectItem {
	m.mutex.RLock()
	if !m.snapshotDirty {
		items := m.allItemsSnapshot
		m.mutex.RUnlock()
		return items
	}
	m.mutex.RUnlock()

	m.mutex.Lock()
	m.rebuildItemSnapshotsLocked()
	items := m.allItemsSnapshot
	m.mutex.Unlock()
	return items
}

func (m *CollectorManager) AllNames() []string {
	m.mutex.RLock()
	if !m.snapshotDirty {
		names := append([]string(nil), m.allNamesSnapshot...)
		m.mutex.RUnlock()
		return names
	}
	m.mutex.RUnlock()

	m.mutex.Lock()
	m.rebuildItemSnapshotsLocked()
	names := append([]string(nil), m.allNamesSnapshot...)
	m.mutex.Unlock()
	return names
}

func (m *CollectorManager) SnapshotVersion() uint64 {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.snapshotVersion
}

func (m *CollectorManager) Update(requiredItems []string) error {
	m.mutex.Lock()
	m.modeFull = false
	m.mutex.Unlock()
	m.setRequiredItems(requiredItems)
	return nil
}

func (m *CollectorManager) UpdateAll() error {
	m.mutex.Lock()
	m.modeFull = true
	m.mutex.Unlock()
	return nil
}

func (m *CollectorManager) SetCollectorEnabled(name string, enabled bool) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if _, exists := m.collectors[trimmed]; !exists {
		return false
	}
	m.collectorEnabled[trimmed] = enabled
	return true
}

func (m *CollectorManager) SetPreviewMode(enabled bool) {
	m.mutex.Lock()
	m.previewMode = enabled
	m.mutex.Unlock()
}

func (m *CollectorManager) SetPaused(paused bool) {
	m.mutex.Lock()
	changed := m.paused != paused
	m.paused = paused
	m.mutex.Unlock()
	if !changed {
		return
	}
	if paused {
		logInfoModule("collect", "action=manager,state=paused")
		return
	}
	logInfoModule("collect", "action=manager,state=resumed")
}

func (m *CollectorManager) IsPaused() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.paused
}

func (m *CollectorManager) PreviewMode() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.previewMode
}

func (m *CollectorManager) ResolveOutputSummary(outputs []OutputConfig) OutputConfigSummary {
	return resolveOutputConfigSummaryFromList(outputs, m.PreviewMode())
}

func (m *CollectorManager) CollectorStates() map[string]bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	states := make(map[string]bool, len(m.collectorEnabled))
	for name, enabled := range m.collectorEnabled {
		states[name] = enabled
	}
	return states
}

func (m *CollectorManager) CollectorNames() []string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	names := append([]string(nil), m.collectorOrder...)
	sort.Strings(names)
	return names
}

func (m *CollectorManager) Stats() CollectorManagerStats {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	enabledCollectorCount := 0
	for _, enabled := range m.collectorEnabled {
		if enabled {
			enabledCollectorCount++
		}
	}
	enabledItemCount := 0
	for _, item := range m.items {
		if item != nil && item.IsEnabled() {
			enabledItemCount++
		}
	}
	queueLen := 0
	for _, ch := range m.workerChans {
		queueLen += len(ch)
	}
	queueSize := len(m.workerChans)
	collectAvg := time.Duration(0)
	if m.totalCollectCount > 0 && m.totalCollectNS > 0 {
		collectAvg = time.Duration(m.totalCollectNS / m.totalCollectCount)
	}
	renderStats := renderRuntimeSnapshot()
	outputStats := GetOutputRuntimeStats()
	return CollectorManagerStats{
		WorkerCount: len(m.workerChans),
		QueueSize:   queueSize,
		QueueLen:    queueLen,
		Paused:      m.paused,

		AutoTune:          false,
		AutoTuneIntervalS: 0,
		AutoTuneSlowRate:  0,
		AutoTuneStableRun: 0,
		AutoTuneMaxScale:  0,
		IntervalScale:     1,

		ScheduledTotal: m.totalScheduled,
		DroppedTotal:   m.totalDropped,
		SlowTotal:      m.totalSlow,
		CompletedTotal: m.totalCompleted,

		LastWindowScheduled: m.lastWindowScheduled,
		LastWindowDropped:   m.lastWindowDropped,
		LastWindowSlow:      m.lastWindowSlow,
		LastWindowCompleted: m.lastWindowCompleted,

		CollectorCount:        len(m.collectors),
		EnabledCollectorCount: enabledCollectorCount,
		ItemCount:             len(m.items),
		EnabledItemCount:      enabledItemCount,

		CurrentEpoch:          atomic.LoadInt64(&m.currentEpoch),
		LastRenderEpoch:       m.lastRenderEpoch,
		LastRenderWaitMS:      m.lastRenderWait.Milliseconds(),
		LastRenderComplete:    m.lastRenderFull,
		CollectorTimeoutTotal: atomic.LoadInt64(&m.timeoutTotal),

		LastCollectMaxMS: m.lastCollectMax.Milliseconds(),
		LastCollectAvgMS: m.lastCollectAvg.Milliseconds(),
		CollectMaxMS:     m.collectMax.Milliseconds(),
		CollectAvgMS:     collectAvg.Milliseconds(),

		RenderLastMS: renderStats.LastMS,
		RenderMaxMS:  renderStats.MaxMS,
		RenderAvgMS:  renderStats.AvgMS,

		OutputLastMS: outputStats.LastMS,
		OutputMaxMS:  outputStats.MaxMS,
		OutputAvgMS:  outputStats.AvgMS,
		OutputStats:  outputStats.Handlers,
		TCPPushStats: GetTCPPushAvailabilityStats(),
	}
}

func (m *CollectorManager) Close() {
	if atomic.SwapInt32(&m.closed, 1) == 1 {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.mutex.Lock()
	m.epochCond.Broadcast()
	m.mutex.Unlock()
	m.wg.Wait()
}

type CollectItemConfig struct {
	Name     string
	Required bool
}

type CollectorManagerConfig struct {
	Items []CollectItemConfig
}

func getCollectorManagerConfig() *CollectorManagerConfig {
	names := []string{
		"go_native.cpu.usage",
		"go_native.cpu.min_freq",
		"go_native.cpu.user",
		"go_native.cpu.system",
		"go_native.cpu.idle",
		"go_native.cpu.iowait",
		"go_native.cpu.irq",
		"go_native.cpu.softirq",
		"go_native.cpu.freq",
		"go_native.cpu.max_freq",
		"go_native.cpu.model",
		"go_native.cpu.cores",
		"go_native.disk.total_read",
		"go_native.disk.total_write",
		"go_native.disk.max_busy",
		"go_native.disk.max_latency",
		"go_native.disk.max_temp",
		"go_native.memory.usage",
		"go_native.memory.used",
		"go_native.memory.total",
		"go_native.memory.usage_text",
		"go_native.memory.usage_progress",
		"go_native.memory.swap_usage",
		"go_native.system.load_avg",
		"go_native.system.current_time",
		"go_native.system.hostname",
		"go_native.system.resolution",
		"go_native.system.refresh_rate",
		"go_native.system.display",
		"go_native.system.collect.max_ms",
		"go_native.system.collect.avg_ms",
		"go_native.system.render.max_ms",
		"go_native.system.render.avg_ms",
		"go_native.system.output.max_ms",
		"go_native.system.output.avg_ms",
		"go_native.system.output.memimg.last_ms",
		"go_native.system.output.memimg.max_ms",
		"go_native.system.output.memimg.avg_ms",
		"go_native.system.output.httppush.last_ms",
		"go_native.system.output.httppush.max_ms",
		"go_native.system.output.httppush.avg_ms",
		"go_native.system.output.tcppush.last_ms",
		"go_native.system.output.tcppush.max_ms",
		"go_native.system.output.tcppush.avg_ms",
		"go_native.system.output.ax206usb.last_ms",
		"go_native.system.output.ax206usb.max_ms",
		"go_native.system.output.ax206usb.avg_ms",
	}
	if runtime.GOOS != "windows" {
		names = append(names, "go_native.cpu.temp")
	}
	if isBtrfsRootAvailable() {
		names = append(names,
			"go_native.btrfs_root.device_size",
			"go_native.btrfs_root.allocated",
			"go_native.btrfs_root.allocated_used",
			"go_native.btrfs_root.unallocated",
			"go_native.btrfs_root.alloc_usage",
			"go_native.btrfs_root.data_usage",
			"go_native.btrfs_root.metadata_usage",
			"go_native.btrfs_root.system_usage",
			"go_native.btrfs_root.pinned",
			"go_native.btrfs_root.reclaim",
			"go_native.btrfs_root.discardable",
		)
	}
	items := make([]CollectItemConfig, 0, len(names))
	for _, name := range names {
		items = append(items, CollectItemConfig{Name: name, Required: true})
	}
	return &CollectorManagerConfig{Items: items}
}

func initializeCollectors(manager *CollectorManager, cfg *MonitorConfig) {
	if manager == nil {
		return
	}
	if cfg == nil {
		cfg = &MonitorConfig{}
	}
	registerCollectorWithConfig(manager, cfg, NewGoNativeCPUCollector(), true)
	registerCollectorWithConfig(manager, cfg, NewGoNativeMemoryCollector(), true)
	registerCollectorWithConfig(manager, cfg, NewGoNativeSystemCollector(), true)
	registerCollectorWithConfig(manager, cfg, NewGoNativeDiskCollector(manager.requiredItemsSnapshot), true)
	registerCollectorWithConfig(manager, cfg, NewGoNativeNetworkCollector(manager.requiredItemsSnapshot), true)
	if btrfsRoot := NewGoNativeBtrfsRootCollector(); btrfsRoot != nil {
		registerCollectorWithConfig(manager, cfg, btrfsRoot, true)
	}
	if cc := NewCoolerControlCollector(cfg); cc != nil {
		registerCollectorWithConfig(manager, cfg, cc, true)
	}
	if lhm := NewLibreHardwareMonitorCollector(cfg); lhm != nil {
		registerCollectorWithConfig(manager, cfg, lhm, true)
	}
	if rtss := NewRTSSCollector(cfg); rtss != nil {
		registerCollectorWithConfig(manager, cfg, rtss, true)
	}
	registerCollectorWithConfig(manager, cfg, NewCustomCollector(cfg, manager.Get), true)
}

func registerCollectorWithConfig(manager *CollectorManager, cfg *MonitorConfig, collector Collector, defaultEnabled bool) {
	if manager == nil || collector == nil {
		return
	}
	manager.RegisterCollector(collector)
	manager.mutex.Lock()
	manager.collectorEnabled[collector.Name()] = defaultEnabled
	manager.mutex.Unlock()
}

func defaultCollectorEnabled(name string) bool {
	switch strings.TrimSpace(name) {
	case collectorCoolerControl, collectorLibreHardwareMonitor, collectorRTSS:
		return false
	default:
		return true
	}
}

func (m *CollectorManager) ApplyConfig(cfg *MonitorConfig, requiredItems []string) {
	m.applyConfig(cfg, requiredItems, false, "")
}

func (m *CollectorManager) applyConfig(cfg *MonitorConfig, requiredItems []string, discover bool, trigger string) {
	if m == nil {
		return
	}
	if cfg == nil {
		cfg = &MonitorConfig{}
	}
	m.configureRuntimeFromConfig(cfg)
	m.setRequiredItems(requiredItems)
	collectors := m.snapshotCollectors()

	m.mutex.Lock()
	for _, entry := range collectors {
		defaultEnabled := defaultCollectorEnabled(entry.name)
		m.collectorEnabled[entry.name] = cfg.IsCollectorEnabled(entry.name, defaultEnabled)
	}
	m.mutex.Unlock()

	for _, entry := range collectors {
		if applier, ok := entry.collector.(CollectorConfigApplier); ok {
			applier.ApplyConfig(cfg)
		}
	}
	m.syncCollectorItemsFromSnapshots(collectors)
	if discover {
		m.discoverAll(trigger)
	}
}

func newCollectorManagerFromConfig(cfg *MonitorConfig, requiredItems []string) *CollectorManager {
	manager := NewCollectorManager()
	manager.modeFull = true
	initializeCollectors(manager, cfg)
	manager.applyConfig(cfg, requiredItems, true, "init")
	manager.startAsyncIfNeeded()
	return manager
}

var (
	globalCollectorManager *CollectorManager
	globalCollectorConfig  *MonitorConfig
	globalCollectorMu      sync.RWMutex
)

func GetCollectorManager() *CollectorManager {
	globalCollectorMu.Lock()
	defer globalCollectorMu.Unlock()
	if globalCollectorManager == nil {
		globalCollectorManager = newCollectorManagerFromConfig(globalCollectorConfig, nil)
	}
	return globalCollectorManager
}

func GetCollectorManagerWithConfig(requiredItems []string, networkInterface string) *CollectorManager {
	_ = networkInterface
	globalCollectorMu.Lock()
	defer globalCollectorMu.Unlock()
	if globalCollectorManager == nil {
		globalCollectorManager = newCollectorManagerFromConfig(globalCollectorConfig, requiredItems)
		return globalCollectorManager
	}
	globalCollectorManager.ApplyConfig(globalCollectorConfig, requiredItems)
	return globalCollectorManager
}

func CurrentCollectorManager() *CollectorManager {
	globalCollectorMu.RLock()
	defer globalCollectorMu.RUnlock()
	return globalCollectorManager
}

func SetGlobalCollectorConfig(config *MonitorConfig) {
	globalCollectorMu.Lock()
	defer globalCollectorMu.Unlock()
	globalCollectorConfig = config
	if globalCollectorManager != nil {
		globalCollectorManager.ApplyConfig(config, globalCollectorManager.requiredItemsSnapshot())
	}
}

func GetGlobalCollectorConfig() *MonitorConfig {
	globalCollectorMu.RLock()
	defer globalCollectorMu.RUnlock()
	return globalCollectorConfig
}

func ResetGlobalCollectorManager() {
	globalCollectorMu.Lock()
	oldManager := globalCollectorManager
	globalCollectorManager = nil
	globalCollectorMu.Unlock()
	if oldManager != nil {
		oldManager.Close()
	}
}
