package main

import (
	"github.com/shirou/gopsutil/v3/load"
	"os"
	"strconv"
	"strings"
	"time"
)

type GoNativeSystemCollector struct {
	*BaseCollector
}

func NewGoNativeSystemCollector() *GoNativeSystemCollector {
	return &GoNativeSystemCollector{BaseCollector: NewBaseCollector("go_native.system")}
}

func (c *GoNativeSystemCollector) ensureStaticItems() {
	if c.getItem("go_native.system.load_avg") != nil {
		return
	}
	c.setItem("go_native.system.load_avg", NewCollectItem("go_native.system.load_avg", "System load average", "", 0, 0, 1))
	c.setItem("go_native.system.current_time", NewCollectItem("go_native.system.current_time", "Current time", "", 0, 0, 0))
	c.setItem("go_native.system.hostname", NewCollectItem("go_native.system.hostname", "Host name", "", 0, 0, 0))
	c.setItem("go_native.system.resolution", NewCollectItem("go_native.system.resolution", "Display resolution", "", 0, 0, 0))
	c.setItem("go_native.system.refresh_rate", NewCollectItem("go_native.system.refresh_rate", "Display refresh rate", "", 0, 0, 0))
	c.setItem("go_native.system.display", NewCollectItem("go_native.system.display", "Display mode", "", 0, 0, 0))
	c.setItem("go_native.system.collect.max_ms", NewCollectItem("go_native.system.collect.max_ms", "Collect max duration", "ms", 0, 0, 0))
	c.setItem("go_native.system.collect.avg_ms", NewCollectItem("go_native.system.collect.avg_ms", "Collect avg duration", "ms", 0, 0, 0))
	c.setItem("go_native.system.render.max_ms", NewCollectItem("go_native.system.render.max_ms", "Render max duration", "ms", 0, 0, 0))
	c.setItem("go_native.system.render.avg_ms", NewCollectItem("go_native.system.render.avg_ms", "Render avg duration", "ms", 0, 0, 0))
	c.setItem("go_native.system.output.max_ms", NewCollectItem("go_native.system.output.max_ms", "Output max duration", "ms", 0, 0, 0))
	c.setItem("go_native.system.output.avg_ms", NewCollectItem("go_native.system.output.avg_ms", "Output avg duration", "ms", 0, 0, 0))
	c.setItem("go_native.cpu.min_freq", NewCollectItem("go_native.cpu.min_freq", "CPU min frequency", "MHz", 0, 0, 0))
	c.setItem("go_native.disk.total_read", NewCollectItem("go_native.disk.total_read", "Disk total read speed", "MiB/s", 0, 0, 2))
	c.setItem("go_native.disk.total_write", NewCollectItem("go_native.disk.total_write", "Disk total write speed", "MiB/s", 0, 0, 2))
	c.setItem("go_native.disk.max_busy", NewCollectItem("go_native.disk.max_busy", "Disk max busy", "%", 0, 100, 0))
	c.setItem("go_native.disk.max_latency", NewCollectItem("go_native.disk.max_latency", "Disk max latency", "ms", 0, 0, 2))
	c.setItem("go_native.disk.max_temp", NewCollectItem("go_native.disk.max_temp", "Disk max temperature", "°C", 0, DiskTempMax, 1))
	ensureOutputMetricItems(c, outputTypeMemImg, "Output memimg")
	ensureOutputMetricItems(c, outputTypeAX206USB, "AX206 refresh")
	ensureOutputMetricItems(c, outputTypeHTTPPush, "HTTP push")
	ensureOutputMetricItems(c, outputTypeTCPPush, "TCP push")
}

func (c *GoNativeSystemCollector) ApplyConfig(cfg *MonitorConfig) {
	c.ensureStaticItems()
	if cfg == nil {
		return
	}
	for _, outputCfg := range cfg.Outputs {
		typeName := strings.ToLower(strings.TrimSpace(outputCfg.Type))
		if typeName == "" {
			continue
		}
		ensureOutputMetricItems(c, typeName, "Output "+typeName)
	}
}

func (c *GoNativeSystemCollector) GetAllItems() map[string]*CollectItem {
	c.ensureStaticItems()
	if item := c.getItem("go_native.system.current_time"); item != nil {
		item.SetValue(time.Now().Format("2006-01-02 15:04:05"))
		item.SetAvailable(true)
	}
	if item := c.getItem("go_native.system.hostname"); item != nil {
		if hostName, err := os.Hostname(); err == nil && strings.TrimSpace(hostName) != "" {
			item.SetValue(hostName)
			item.SetAvailable(true)
		} else {
			item.SetAvailable(false)
		}
	}
	updateSystemDisplayItems(c)
	return c.ItemsSnapshot()
}

func (c *GoNativeSystemCollector) UpdateItems() error {
	if !c.IsEnabled() {
		return nil
	}

	var err error
	if item := c.getItem("go_native.system.load_avg"); item != nil {
		loadInfo, loadErr := load.Avg()
		if loadErr != nil {
			item.SetAvailable(false)
			err = loadErr
		} else {
			item.SetValue(loadInfo.Load1)
			item.SetAvailable(true)
		}
	}
	if item := c.getItem("go_native.system.current_time"); item != nil {
		item.SetValue(time.Now().Format("2006-01-02 15:04:05"))
		item.SetAvailable(true)
	}
	updateSystemDisplayItems(c)

	if manager := CurrentCollectorManager(); manager != nil {
		updateAggregateMonitorItems(c, manager.GetAll())
		stats := manager.Stats()
		setSystemMetricItem(c.getItem("go_native.system.collect.max_ms"), stats.CollectMaxMS)
		setSystemMetricItem(c.getItem("go_native.system.collect.avg_ms"), stats.CollectAvgMS)
		setSystemMetricItem(c.getItem("go_native.system.render.max_ms"), stats.RenderMaxMS)
		setSystemMetricItem(c.getItem("go_native.system.render.avg_ms"), stats.RenderAvgMS)
		setSystemMetricItem(c.getItem("go_native.system.output.max_ms"), stats.OutputMaxMS)
		setSystemMetricItem(c.getItem("go_native.system.output.avg_ms"), stats.OutputAvgMS)

		for typeName := range stats.OutputStats {
			setOutputTypeMetric(c, typeName, stats.OutputStats)
		}
		setAX206DeviceOutputMetrics(c, outputTypeAX206USB, GetAX206DeviceFrameRuntimeStats())
		for typeName, pushStats := range GetHTTPPushRuntimeStats() {
			setOutputMetricValues(c, typeName, pushStats.Calls, pushStats.LastMS, pushStats.MaxMS, pushStats.AvgMS)
		}
		for typeName, pushStats := range GetTCPPushRuntimeStats() {
			setOutputMetricValues(c, typeName, pushStats.Calls, pushStats.LastMS, pushStats.MaxMS, pushStats.AvgMS)
		}
	}
	return err
}

func updateAggregateMonitorItems(c *GoNativeSystemCollector, items map[string]*CollectItem) {
	if c == nil {
		return
	}
	setFloatMonitorItem(c.getItem("go_native.cpu.min_freq"), aggregateCPUMinFreq(items))
	diskRuntime := aggregateDiskRuntimeMetricSet(items)
	setFloatMonitorItem(c.getItem("go_native.disk.total_read"), diskRuntime.totalRead)
	setFloatMonitorItem(c.getItem("go_native.disk.total_write"), diskRuntime.totalWrite)
	setFloatMonitorItem(c.getItem("go_native.disk.max_busy"), diskRuntime.maxBusy)
	setFloatMonitorItem(c.getItem("go_native.disk.max_latency"), diskRuntime.maxLatency)
	setFloatMonitorItem(c.getItem("go_native.disk.max_temp"), diskRuntime.maxTemp)
}

type floatAggregateResult struct {
	value float64
	ok    bool
}

func setFloatMonitorItem(item *CollectItem, result floatAggregateResult) {
	if item == nil {
		return
	}
	if result.ok {
		item.SetValue(result.value)
		item.SetAvailable(true)
		return
	}
	item.SetAvailable(false)
}

func aggregateCPUMinFreq(items map[string]*CollectItem) floatAggregateResult {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	minValue := 0.0
	ok := false
	for _, name := range names {
		index, isCoreClock := parseLibreCPUCoreClockIndex(name)
		if !isCoreClock || index <= 0 {
			continue
		}
		value, valueOK := collectItemFloatValue(items[name])
		if !valueOK {
			continue
		}
		if !ok || value < minValue {
			minValue = value
			ok = true
		}
	}
	if ok {
		return floatAggregateResult{value: minValue, ok: true}
	}
	value, valueOK := collectItemFloatValue(items["go_native.cpu.freq"])
	return floatAggregateResult{value: value, ok: valueOK}
}

type diskRuntimeAggregateResult struct {
	totalRead  floatAggregateResult
	totalWrite floatAggregateResult
	maxBusy    floatAggregateResult
	maxLatency floatAggregateResult
	maxTemp    floatAggregateResult
}

func aggregateDiskRuntimeMetrics(items map[string]*CollectItem) (floatAggregateResult, floatAggregateResult, floatAggregateResult) {
	result := aggregateDiskRuntimeMetricSet(items)
	return result.totalRead, result.totalWrite, result.maxBusy
}

func aggregateDiskRuntimeMetricSet(items map[string]*CollectItem) diskRuntimeAggregateResult {
	totalRead := 0.0
	totalWrite := 0.0
	maxBusy := 0.0
	maxLatency := 0.0
	maxTemp := 0.0
	readOK := false
	writeOK := false
	busyOK := false
	latencyOK := false
	tempOK := false
	for name, item := range items {
		if !strings.HasPrefix(strings.TrimSpace(name), "go_native.disk.") {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".read"):
			value, ok := collectItemFloatValue(item)
			if !ok {
				continue
			}
			totalRead += value
			readOK = true
		case strings.HasSuffix(name, ".write"):
			value, ok := collectItemFloatValue(item)
			if !ok {
				continue
			}
			totalWrite += value
			writeOK = true
		case strings.HasSuffix(name, ".busy"):
			value, ok := collectItemFloatValue(item)
			if !ok {
				continue
			}
			if !busyOK || value > maxBusy {
				maxBusy = value
				busyOK = true
			}
		case strings.HasSuffix(name, ".read_latency") || strings.HasSuffix(name, ".write_latency"):
			value, ok := collectItemFloatValue(item)
			if !ok {
				continue
			}
			if !latencyOK || value > maxLatency {
				maxLatency = value
				latencyOK = true
			}
		case strings.HasSuffix(name, ".temp"):
			value, ok := collectItemFloatValue(item)
			if !ok {
				continue
			}
			if !tempOK || value > maxTemp {
				maxTemp = value
				tempOK = true
			}
		}
	}
	return diskRuntimeAggregateResult{
		totalRead:  floatAggregateResult{value: totalRead, ok: readOK},
		totalWrite: floatAggregateResult{value: totalWrite, ok: writeOK},
		maxBusy:    floatAggregateResult{value: maxBusy, ok: busyOK},
		maxLatency: floatAggregateResult{value: maxLatency, ok: latencyOK},
		maxTemp:    floatAggregateResult{value: maxTemp, ok: tempOK},
	}
}

func collectItemFloatValue(item *CollectItem) (float64, bool) {
	if item == nil || !item.IsAvailable() {
		return 0, false
	}
	value := item.GetValue()
	if value == nil {
		return 0, false
	}
	switch typed := value.Value.(type) {
	case float64, float32, int, int64, uint64:
		return getFloat64Value(typed), true
	default:
		return 0, false
	}
}

func parseLibreCPUCoreClockIndex(name string) (int, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(normalized, "libre_") || !strings.Contains(normalized, "cpu") || !strings.Contains(normalized, "_clock_") {
		return 0, false
	}
	parts := strings.Split(normalized, "_")
	if len(parts) < 2 {
		return 0, false
	}
	index, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, false
	}
	return index, true
}

func setSystemMetricItem(item *CollectItem, value int64) {
	if item == nil {
		return
	}
	item.SetValue(value)
	item.SetAvailable(true)
}

func setOutputTypeMetric(c *GoNativeSystemCollector, typeName string, stats map[string]OutputHandlerRuntimeStats) {
	if c == nil {
		return
	}
	lastKey, maxKey, avgKey := outputMetricKeys(typeName)
	lastItem := c.getItem(lastKey)
	maxItem := c.getItem(maxKey)
	avgItem := c.getItem(avgKey)
	if lastItem == nil && maxItem == nil && avgItem == nil {
		return
	}
	entry, ok := stats[typeName]
	if !ok {
		if lastItem != nil {
			lastItem.SetAvailable(false)
		}
		if maxItem != nil {
			maxItem.SetAvailable(false)
		}
		if avgItem != nil {
			avgItem.SetAvailable(false)
		}
		return
	}
	setOutputMetricValues(c, typeName, entry.Calls, entry.LastMS, entry.MaxMS, entry.AvgMS)
}

func setAX206DeviceOutputMetrics(c *GoNativeSystemCollector, typeName string, stats AX206DeviceFrameRuntimeStats) {
	if c == nil {
		return
	}
	setOutputMetricValues(c, typeName, stats.Calls, stats.LastMS, stats.MaxMS, stats.AvgMS)
}

func sanitizeOutputMetricType(typeName string) string {
	normalized := strings.ToLower(strings.TrimSpace(typeName))
	if normalized == "" {
		return "unknown"
	}
	builder := strings.Builder{}
	builder.Grow(len(normalized))
	lastUnderscore := false
	for _, ch := range normalized {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			builder.WriteRune(ch)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "unknown"
	}
	return result
}

func outputMetricKeys(typeName string) (string, string, string) {
	metricType := sanitizeOutputMetricType(typeName)
	prefix := "go_native.system.output." + metricType
	return prefix + ".last_ms", prefix + ".max_ms", prefix + ".avg_ms"
}

func ensureOutputMetricItems(c *GoNativeSystemCollector, typeName string, labelPrefix string) {
	if c == nil {
		return
	}
	lastKey, maxKey, avgKey := outputMetricKeys(typeName)
	typeLabel := strings.ToLower(strings.TrimSpace(typeName))
	if strings.TrimSpace(labelPrefix) == "" {
		labelPrefix = "Output " + typeLabel
	}
	if c.getItem(lastKey) == nil {
		c.setItem(lastKey, NewCollectItem(lastKey, labelPrefix+" last duration", "ms", 0, 0, 0))
	}
	if c.getItem(maxKey) == nil {
		c.setItem(maxKey, NewCollectItem(maxKey, labelPrefix+" max duration", "ms", 0, 0, 0))
	}
	if c.getItem(avgKey) == nil {
		c.setItem(avgKey, NewCollectItem(avgKey, labelPrefix+" avg duration", "ms", 0, 0, 0))
	}
}

func setOutputMetricValues(c *GoNativeSystemCollector, typeName string, calls, lastMS, maxMS, avgMS int64) {
	if c == nil {
		return
	}
	lastKey, maxKey, avgKey := outputMetricKeys(typeName)
	lastItem := c.getItem(lastKey)
	maxItem := c.getItem(maxKey)
	avgItem := c.getItem(avgKey)
	if calls <= 0 {
		if lastItem != nil {
			lastItem.SetAvailable(false)
		}
		if maxItem != nil {
			maxItem.SetAvailable(false)
		}
		if avgItem != nil {
			avgItem.SetAvailable(false)
		}
		return
	}
	if lastItem != nil {
		lastItem.SetValue(lastMS)
		lastItem.SetAvailable(true)
	}
	if maxItem != nil {
		maxItem.SetValue(maxMS)
		maxItem.SetAvailable(true)
	}
	if avgItem != nil {
		avgItem.SetValue(avgMS)
		avgItem.SetAvailable(true)
	}
}

func updateSystemDisplayItems(c *GoNativeSystemCollector) {
	if c == nil {
		return
	}
	resolution, refreshRate, ok := getDisplayInfoSnapshot(2 * time.Minute)
	resolutionItem := c.getItem("go_native.system.resolution")
	refreshItem := c.getItem("go_native.system.refresh_rate")
	displayItem := c.getItem("go_native.system.display")
	if resolutionItem != nil {
		if ok && strings.TrimSpace(resolution) != "" {
			resolutionItem.SetValue(resolution)
			resolutionItem.SetAvailable(true)
		} else {
			resolutionItem.SetAvailable(false)
		}
	}
	if refreshItem != nil {
		if ok && strings.TrimSpace(refreshRate) != "" {
			refreshItem.SetValue(refreshRate)
			refreshItem.SetAvailable(true)
		} else {
			refreshItem.SetAvailable(false)
		}
	}
	if displayItem != nil {
		if ok {
			displayItem.SetValue(composeDisplayModeValue(resolution, refreshRate))
			displayItem.SetAvailable(true)
		} else {
			displayItem.SetAvailable(false)
		}
	}
}

func composeDisplayModeValue(resolution, refreshRate string) string {
	resolution = strings.TrimSpace(resolution)
	refreshRate = strings.TrimSpace(refreshRate)
	if resolution == "" {
		resolution = "-"
	}
	if refreshRate == "" {
		refreshRate = "-"
	}
	return resolution + "@" + refreshRate
}
