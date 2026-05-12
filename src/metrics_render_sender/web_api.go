package main

import (
	"fmt"
	"metrics_render_sender/rtsssource"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	webTickerInterval = 250 * time.Millisecond
)

type WebMonitorSnapshotItem struct {
	Available bool   `json:"available"`
	Label     string `json:"label,omitempty"`
	Text      string `json:"text"`
	Unit      string `json:"unit,omitempty"`
}

type WebSnapshotResponse struct {
	Mode           string                            `json:"mode"`
	UpdatedAt      string                            `json:"updated_at"`
	Monitors       []string                          `json:"monitors"`
	Values         map[string]WebMonitorSnapshotItem `json:"values"`
	MonitorRuntime *CollectorManagerStats            `json:"monitor_runtime,omitempty"`
}

type webFrameRuntimeStats struct {
	width    int
	height   int
	pngBytes int
}

type WebAPI struct {
	mu            sync.RWMutex
	applyMu       sync.Mutex
	renderMu      sync.Mutex
	fontCache     *FontCache
	config        *MonitorConfig
	required      []string
	registry      *CollectorManager
	renderManager *RenderManager
	outputManager *OutputManager
	outputConfigs []OutputConfig
	outputTypes   []string
	outputHasMem  bool
	layoutCache   webSnapshotLayoutCache
	snapshotCache webSnapshotCache
	valueCache    map[*CollectItem]webSnapshotValueCache
	previewOutput *MemImgOutputHandler
	lastProbeCC   string
	lastProbeLHM  string
	lastProbeRTSS bool

	activityMu   sync.RWMutex
	lastActivity time.Time
	modeFull     bool
	updatedAt    time.Time
	realtimeConn int32
	lastEpoch    int64
	frameStats   webFrameRuntimeStats

	outputChan chan webOutputFrame
	outputWg   sync.WaitGroup

	stopOnce sync.Once
	stopCh   chan struct{}
	stopped  chan struct{}
}

type webOutputFrame struct {
	result     *RenderResult
	enqueuedAt time.Time
	modeFull   bool
}

type webSnapshotCache struct {
	valid     bool
	registry  *CollectorManager
	modeFull  bool
	updatedAt string
	required  []string
	response  WebSnapshotResponse
}

type webSnapshotLayoutCache struct {
	valid           bool
	registry        *CollectorManager
	snapshotVersion uint64
	modeFull        bool
	required        []string
	layout          webSnapshotLayout
}

type webSnapshotLayout struct {
	names   []string
	entries []webSnapshotEntry
}

type webSnapshotEntry struct {
	name      string
	monitor   *CollectItem
	baseLabel string
	labelKind webSnapshotLabelKind
	index     int
	suffix    string
}

type webSnapshotLabelKind uint8

const (
	webSnapshotLabelStatic webSnapshotLabelKind = iota
	webSnapshotLabelNetIndexed
	webSnapshotLabelDiskIndexed
)

type webSnapshotValueCache struct {
	version   uint64
	available bool
	text      string
	unit      string
}

func NewWebAPI(cfg *MonitorConfig) (*WebAPI, error) {
	fontCache, err := loadFontCache()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize web runtime fonts: %w", err)
	}

	runtime := &WebAPI{
		fontCache:     fontCache,
		previewOutput: NewMemImgOutputHandler(),
		outputChan:    make(chan webOutputFrame, 1),
		valueCache:    make(map[*CollectItem]webSnapshotValueCache),
		stopCh:        make(chan struct{}),
		stopped:       make(chan struct{}),
	}

	if err := runtime.applyConfigInternal(cfg, false); err != nil {
		return nil, err
	}

	runtime.outputWg.Add(1)
	go runtime.outputLoop()
	go runtime.loop()
	return runtime, nil
}

func (r *WebAPI) Touch() {
	r.activityMu.Lock()
	r.lastActivity = time.Now()
	r.activityMu.Unlock()
}

func (r *WebAPI) ApplyConfig(cfg *MonitorConfig) error {
	if err := r.applyConfigInternal(cfg, false); err != nil {
		return err
	}
	_, _ = r.renderOnce(true)
	return nil
}

func (r *WebAPI) ApplyPreviewConfig(cfg *MonitorConfig) error {
	// Preview edit should not rebuild USB outputs on every minor config change.
	if err := r.applyConfigInternal(cfg, true); err != nil {
		return err
	}
	_, err := r.renderOnce(true)
	return err
}

func (r *WebAPI) SetRealtimeConnectionCount(count int) {
	if count < 0 {
		count = 0
	}
	atomic.StoreInt32(&r.realtimeConn, int32(count))
}

func (r *WebAPI) outputLoop() {
	defer r.outputWg.Done()
	for frame := range r.outputChan {
		if frame.result == nil {
			continue
		}
		outputStart := time.Now()
		outputFrame := frame.result.OutputFrame()
		if outputFrame == nil {
			continue
		}
		// Always refresh preview buffer for WebSocket clients, independent of output types.
		if err := r.previewOutput.OutputFrame(outputFrame); err != nil {
			logDebugModule("web", "preview output failed: %v", err)
		} else if outputFrame.Image != nil {
			bounds := outputFrame.Image.Bounds()
			r.setLatestFrameStats(bounds.Dx(), bounds.Dy(), GetMemImgPNGSize())
		}

		_, _, _, _, outputManager, _ := r.getRuntimeRefs()
		if outputManager == nil {
			continue
		}
		if err := outputManager.OutputFrame(outputFrame); err != nil {
			logDebugModule("web", "runtime output failed: %v", err)
			continue
		}
		queueDelay := outputStart.Sub(frame.enqueuedAt)
		outputDuration := time.Since(outputStart)
		logDebugModule("web", "output=%v queue=%v", outputDuration, queueDelay)
	}
}

func (r *WebAPI) renderOnce(forceFull bool) (bool, error) {
	r.renderMu.Lock()
	defer r.renderMu.Unlock()

	modeFull := forceFull || r.isFullMode()
	r.setMode(modeFull)
	noteRenderAccess()

	cfg, _, registry, renderManager, _, _ := r.getRuntimeRefs()
	if cfg == nil || registry == nil || renderManager == nil {
		return false, nil
	}

	waitMax := cfg.GetRenderWaitMaxDuration()
	currentEpoch := registry.CurrentEpoch()
	if currentEpoch > r.lastEpoch {
		waitComplete, waitDuration := registry.WaitForEpoch(currentEpoch, waitMax)
		logDebugModule("web", "epoch=%d wait=%v complete=%v", currentEpoch, waitDuration, waitComplete)
		r.lastEpoch = currentEpoch
	} else if !forceFull {
		return false, nil
	}

	renderStartedAt := time.Now()
	result, err := renderManager.Render(cfg)
	if err != nil {
		return false, err
	}
	recordRenderDuration(time.Since(renderStartedAt))

	replaced, ok := enqueueLatestWebFrame(r.outputChan, webOutputFrame{
		result:     result,
		enqueuedAt: time.Now(),
		modeFull:   modeFull,
	})
	if !ok {
		logDebugModule("web", "output queue busy, skip frame")
	} else if replaced {
		logDebugModule("web", "output queue replaced stale frame")
	}

	r.setUpdatedAt(time.Now())
	return true, nil
}

func (r *WebAPI) applyConfigInternal(cfg *MonitorConfig, forceMemImg bool) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()

	// Prevent output manager swap/close from racing with ongoing render output.
	r.renderMu.Lock()
	defer r.renderMu.Unlock()

	configCopy := cloneMonitorConfig(cfg)
	normalizeMonitorConfig(configCopy)

	SetGlobalCollectorConfig(configCopy)
	initializeCache()

	required := getRequiredMonitors(configCopy)
	registry := GetCollectorManagerWithConfig(required, configCopy.GetNetworkInterface())
	registry.SetPreviewMode(forceMemImg)
	var historyStore *renderHistoryStore
	r.mu.RLock()
	if r.renderManager != nil {
		historyStore = r.renderManager.history
	}
	r.mu.RUnlock()
	renderManager := NewRenderManagerWithHistory(r.fontCache, registry, historyStore)

	outputSummary := resolveOutputConfigSummaryFromList(configCopy.Outputs, false)
	outputConfigs := outputSummary.Configs

	var outputManager *OutputManager
	r.mu.RLock()
	existingOutputManager := r.outputManager
	existingOutputConfigs := append([]OutputConfig(nil), r.outputConfigs...)
	r.mu.RUnlock()
	if existingOutputManager != nil && outputConfigsEqual(existingOutputConfigs, outputConfigs) {
		outputManager = existingOutputManager
	} else {
		outputManager, outputConfigs = buildOutputManager(configCopy, false)
		outputSummary = describeOutputConfigs(outputConfigs)
	}

	r.mu.Lock()
	oldOutputManager := r.outputManager
	r.config = configCopy
	r.required = required
	r.registry = registry
	r.renderManager = renderManager
	r.outputManager = outputManager
	r.outputConfigs = append([]OutputConfig(nil), outputConfigs...)
	r.outputTypes = append([]string(nil), outputSummary.Types...)
	r.outputHasMem = outputSummary.HasMemImg
	r.layoutCache = webSnapshotLayoutCache{}
	r.snapshotCache = webSnapshotCache{}
	r.valueCache = make(map[*CollectItem]webSnapshotValueCache)
	r.lastEpoch = 0
	r.mu.Unlock()

	if oldOutputManager != nil && oldOutputManager != outputManager {
		oldOutputManager.Close()
	}
	r.maybeProbeDataSources(configCopy)
	return nil
}

func enqueueLatestWebFrame(ch chan webOutputFrame, frame webOutputFrame) (bool, bool) {
	select {
	case ch <- frame:
		return false, true
	default:
	}

	replaced := false
	select {
	case <-ch:
		replaced = true
	default:
	}

	select {
	case ch <- frame:
		return replaced, true
	default:
		return replaced, false
	}
}

func (r *WebAPI) maybeProbeDataSources(cfg *MonitorConfig) {
	ccURL := cfg.GetCoolerControlURL()
	lhmURL := cfg.GetLibreHardwareMonitorURL()
	ccEnabled := cfg.IsCollectorEnabled(collectorCoolerControl, false)
	lhmEnabled := cfg.IsCollectorEnabled(collectorLibreHardwareMonitor, false)
	rtssEnabled := cfg.IsCollectorEnabled(collectorRTSS, false)

	shouldProbeCC := false
	shouldProbeLHM := false
	shouldProbeRTSS := false

	r.mu.Lock()
	if !ccEnabled {
		r.lastProbeCC = ""
	} else if ccURL != "" && ccURL != r.lastProbeCC {
		r.lastProbeCC = ccURL
		shouldProbeCC = true
	}
	if !lhmEnabled {
		r.lastProbeLHM = ""
	} else if lhmURL != "" && lhmURL != r.lastProbeLHM {
		r.lastProbeLHM = lhmURL
		shouldProbeLHM = true
	}
	if !rtssEnabled {
		r.lastProbeRTSS = false
	} else if !r.lastProbeRTSS {
		r.lastProbeRTSS = true
		shouldProbeRTSS = true
	}
	r.mu.Unlock()

	if shouldProbeCC {
		go func(url string, cfg *MonitorConfig) {
			options, err := listConfiguredCoolerControlOptions(cfg)
			if err != nil {
				logWarnModule("web", "coolercontrol probe failed (url=%s): %v", url, err)
				return
			}
			logInfoModule(
				"web",
				"coolercontrol probe success (url=%s, monitors=%d)",
				url,
				len(options),
			)
		}(ccURL, cfg)
	}

	if shouldProbeLHM {
		go func(url string, cfg *MonitorConfig) {
			items, err := listConfiguredLibreHardwareMonitorOptions(cfg)
			if err != nil {
				logWarnModule("web", "librehardwaremonitor probe failed (url=%s): %v", url, err)
				return
			}
			logInfoModule("web", "librehardwaremonitor probe success (url=%s, monitors=%d)", url, len(items))
		}(lhmURL, cfg)
	}

	if shouldProbeRTSS {
		go func() {
			client := rtsssource.GetRTSSClient()
			connected := client.RefreshMetrics(250 * time.Millisecond)
			options := client.ListMonitorOptions()
			if !connected {
				logWarnModule("web", "rtss probe failed: shared memory unavailable or no active app")
				return
			}
			logInfoModule("web", "rtss probe success (monitors=%d)", len(options))
		}()
	}
}

func (r *WebAPI) loop() {
	defer close(r.stopped)
	ticker := time.NewTicker(webTickerInterval)
	defer ticker.Stop()

	lastModeFull := false

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
		}

		modeFull := r.isFullMode()
		if modeFull != lastModeFull {
			if modeFull {
				logInfoModule("web", "Monitor update mode switched to full scan")
			} else {
				logInfoModule("web", "Monitor update mode switched to required-only")
			}
			lastModeFull = modeFull
		}
		cfg, _, _, _, _, _ := r.getRuntimeRefs()
		if cfg == nil {
			continue
		}

		_, err := r.renderOnce(false)
		if err != nil {
			logDebugModule("web", "render runtime image failed: %v", err)
			continue
		}
	}
}

func (r *WebAPI) Snapshot() WebSnapshotResponse {
	modeFull := r.isFullMode()
	r.setMode(modeFull)

	_, required, registry, _, _, _ := r.getRuntimeRefs()
	if registry == nil {
		return WebSnapshotResponse{
			Mode:      "required",
			UpdatedAt: time.Now().Format(time.RFC3339Nano),
			Values:    map[string]WebMonitorSnapshotItem{},
		}
	}

	mode := "required"
	if modeFull {
		mode = "full"
	}

	updatedAt := r.getUpdatedAt()
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	updatedAtText := updatedAt.Format(time.RFC3339Nano)

	if snapshot, ok := r.loadCachedSnapshot(registry, modeFull, updatedAtText, required); ok {
		return snapshot
	}

	monitorStats := registry.Stats()
	layout := r.snapshotLayout(registry, modeFull, required)
	values := make(map[string]WebMonitorSnapshotItem, len(layout.entries))
	for _, entry := range layout.entries {
		if entry.monitor == nil {
			continue
		}
		item := r.snapshotValueItem(entry.monitor, entry.baseLabel)
		values[entry.name] = item
	}
	applyDynamicWebSnapshotLabels(values, layout.entries)
	applyWebFrameRuntimeValues(values, r.latestFrameStats())

	snapshot := WebSnapshotResponse{
		Mode:           mode,
		UpdatedAt:      updatedAtText,
		Monitors:       append([]string(nil), layout.names...),
		Values:         values,
		MonitorRuntime: &monitorStats,
	}
	r.storeCachedSnapshot(registry, modeFull, updatedAtText, required, snapshot)
	return snapshot
}

func (r *WebAPI) setLatestFrameStats(width, height, pngBytes int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	if pngBytes < 0 {
		pngBytes = 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frameStats.width == width && r.frameStats.height == height && r.frameStats.pngBytes == pngBytes {
		return
	}
	r.frameStats = webFrameRuntimeStats{
		width:    width,
		height:   height,
		pngBytes: pngBytes,
	}
	r.snapshotCache = webSnapshotCache{}
}

func (r *WebAPI) latestFrameStats() webFrameRuntimeStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.frameStats
}

func applyWebFrameRuntimeValues(values map[string]WebMonitorSnapshotItem, stats webFrameRuntimeStats) {
	if values == nil {
		return
	}

	values["go_native.system.frame_size"] = WebMonitorSnapshotItem{
		Available: stats.width > 0 && stats.height > 0,
		Label:     "Frame Size",
		Text:      formatWebFrameDimensions(stats.width, stats.height),
	}
	values["go_native.system.frame_bytes"] = WebMonitorSnapshotItem{
		Available: stats.pngBytes > 0,
		Label:     "Frame Bytes",
		Text:      formatWebByteSize(stats.pngBytes),
	}
}

func formatWebFrameDimensions(width, height int) string {
	if width <= 0 || height <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dx%d", width, height)
}

func formatWebByteSize(size int) string {
	if size <= 0 {
		return "0 B"
	}
	value := float64(size)
	units := []string{"B", "KiB", "MiB", "GiB"}
	unitIdx := 0
	for value >= 1024 && unitIdx < len(units)-1 {
		value /= 1024
		unitIdx++
	}
	if unitIdx == 0 {
		return fmt.Sprintf("%d %s", size, units[unitIdx])
	}
	return fmt.Sprintf("%.1f %s", value, units[unitIdx])
}

func (r *WebAPI) MonitorStats() *CollectorManagerStats {
	_, _, registry, _, _, _ := r.getRuntimeRefs()
	if registry == nil {
		return nil
	}
	stats := registry.Stats()
	return &stats
}

func (r *WebAPI) AllMonitorNames() []string {
	_, _, registry, _, _, _ := r.getRuntimeRefs()
	if registry == nil {
		return []string{}
	}
	return registry.AllNames()
}

func (r *WebAPI) CollectorNames() []string {
	_, _, registry, _, _, _ := r.getRuntimeRefs()
	if registry == nil {
		return []string{}
	}
	return registry.CollectorNames()
}

func (r *WebAPI) CollectorStates() map[string]bool {
	_, _, registry, _, _, _ := r.getRuntimeRefs()
	if registry == nil {
		return map[string]bool{}
	}
	return registry.CollectorStates()
}

func (r *WebAPI) SetCollectorEnabled(name string, enabled bool) bool {
	_, _, registry, _, _, _ := r.getRuntimeRefs()
	if registry == nil {
		return false
	}
	return registry.SetCollectorEnabled(name, enabled)
}

func (r *WebAPI) CurrentConfig() *MonitorConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.config == nil {
		return nil
	}
	return cloneMonitorConfig(r.config)
}

func (r *WebAPI) Close() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		<-r.stopped
	})

	r.mu.Lock()
	oldOutputManager := r.outputManager
	outputChan := r.outputChan
	r.outputChan = nil
	r.outputManager = nil
	r.outputConfigs = nil
	r.outputTypes = nil
	r.layoutCache = webSnapshotLayoutCache{}
	r.snapshotCache = webSnapshotCache{}
	r.valueCache = nil
	r.registry = nil
	r.renderManager = nil
	r.config = nil
	r.required = nil
	r.outputHasMem = false
	r.mu.Unlock()

	if outputChan != nil {
		close(outputChan)
	}
	r.outputWg.Wait()
	if oldOutputManager != nil {
		oldOutputManager.Close()
	}
}

func (r *WebAPI) getRuntimeRefs() (*MonitorConfig, []string, *CollectorManager, *RenderManager, *OutputManager, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	requiredCopy := append([]string(nil), r.required...)
	return r.config, requiredCopy, r.registry, r.renderManager, r.outputManager, r.outputHasMem
}

func (r *WebAPI) isFullMode() bool {
	_ = atomic.LoadInt32(&r.realtimeConn)
	return true
}

func (r *WebAPI) setMode(modeFull bool) {
	r.activityMu.Lock()
	r.modeFull = modeFull
	r.activityMu.Unlock()
}

func (r *WebAPI) setUpdatedAt(updatedAt time.Time) {
	r.activityMu.Lock()
	r.updatedAt = updatedAt
	r.activityMu.Unlock()
}

func (r *WebAPI) getUpdatedAt() time.Time {
	r.activityMu.RLock()
	defer r.activityMu.RUnlock()
	return r.updatedAt
}

func (r *WebAPI) loadCachedSnapshot(
	registry *CollectorManager,
	modeFull bool,
	updatedAt string,
	required []string,
) (WebSnapshotResponse, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cache := r.snapshotCache
	if !cache.valid {
		return WebSnapshotResponse{}, false
	}
	if cache.registry != registry || cache.modeFull != modeFull || cache.updatedAt != updatedAt {
		return WebSnapshotResponse{}, false
	}
	if !modeFull && !equalStringSlices(cache.required, required) {
		return WebSnapshotResponse{}, false
	}
	return cache.response, true
}

func (r *WebAPI) storeCachedSnapshot(
	registry *CollectorManager,
	modeFull bool,
	updatedAt string,
	required []string,
	response WebSnapshotResponse,
) {
	r.mu.Lock()
	r.snapshotCache = webSnapshotCache{
		valid:     true,
		registry:  registry,
		modeFull:  modeFull,
		updatedAt: updatedAt,
		required:  append([]string(nil), required...),
		response:  response,
	}
	r.mu.Unlock()
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func (r *WebAPI) snapshotLayout(
	registry *CollectorManager,
	modeFull bool,
	required []string,
) webSnapshotLayout {
	snapshotVersion := registry.SnapshotVersion()

	r.mu.RLock()
	cache := r.layoutCache
	r.mu.RUnlock()
	if cache.valid &&
		cache.registry == registry &&
		cache.snapshotVersion == snapshotVersion &&
		cache.modeFull == modeFull &&
		(modeFull || equalStringSlices(cache.required, required)) {
		return cache.layout
	}

	names := make([]string, 0)
	if modeFull {
		names = registry.AllNames()
	} else {
		names = append(names, required...)
		sort.Strings(names)
	}

	entries := make([]webSnapshotEntry, 0, len(names))
	for _, name := range names {
		monitor := registry.Get(name)
		label := buildStaticWebSnapshotLabel(name, monitor)
		entry := webSnapshotEntry{
			name:      name,
			monitor:   monitor,
			baseLabel: label,
			labelKind: webSnapshotLabelStatic,
		}
		index, suffix, ok := parseGoNativeIndexedWebMonitor(name, "go_native.net.")
		if ok {
			entry.labelKind = webSnapshotLabelNetIndexed
			entry.index = index
			entry.suffix = suffix
		} else if index, suffix, ok = parseGoNativeIndexedWebMonitor(name, "go_native.disk."); ok {
			entry.labelKind = webSnapshotLabelDiskIndexed
			entry.index = index
			entry.suffix = suffix
		}
		entries = append(entries, entry)
	}

	layout := webSnapshotLayout{
		names:   names,
		entries: entries,
	}

	r.mu.Lock()
	r.layoutCache = webSnapshotLayoutCache{
		valid:           true,
		registry:        registry,
		snapshotVersion: snapshotVersion,
		modeFull:        modeFull,
		required:        append([]string(nil), required...),
		layout:          layout,
	}
	r.mu.Unlock()
	return layout
}

func buildStaticWebSnapshotLabel(name string, monitor *CollectItem) string {
	if label, ok := explicitWebSnapshotLabels[name]; ok {
		return label
	}
	if monitor != nil {
		if label := strings.TrimSpace(monitor.GetLabel()); label != "" {
			return label
		}
	}
	return humanizeMonitorKey(name)
}

func (r *WebAPI) snapshotValueItem(monitor *CollectItem, baseLabel string) WebMonitorSnapshotItem {
	version, available, value := monitor.SnapshotState()

	r.mu.RLock()
	cache, ok := r.valueCache[monitor]
	r.mu.RUnlock()
	if ok && cache.version == version {
		return WebMonitorSnapshotItem{
			Available: cache.available,
			Label:     baseLabel,
			Text:      cache.text,
			Unit:      cache.unit,
		}
	}

	item := WebMonitorSnapshotItem{
		Available: available,
		Label:     baseLabel,
		Text:      "-",
	}
	if available && value != nil {
		item.Text = FormatCollectValue(value, true, "")
		item.Unit = value.Unit
	}

	r.mu.Lock()
	if r.valueCache != nil {
		r.valueCache[monitor] = webSnapshotValueCache{
			version:   version,
			available: item.Available,
			text:      item.Text,
			unit:      item.Unit,
		}
	}
	r.mu.Unlock()
	return item
}

func applyDynamicWebSnapshotLabels(values map[string]WebMonitorSnapshotItem, entries []webSnapshotEntry) {
	if len(values) == 0 || len(entries) == 0 {
		return
	}
	for _, entry := range entries {
		item, ok := values[entry.name]
		if !ok {
			continue
		}
		switch entry.labelKind {
		case webSnapshotLabelNetIndexed:
			iface := resolveGoNativeIndexedMonitorAnchor(values, "go_native.net.", entry.index, "interface")
			if iface == "" {
				iface = "net" + itoa(entry.index)
			}
			switch entry.suffix {
			case "upload":
				item.Label = "Net " + iface + " upload speed"
			case "download":
				item.Label = "Net " + iface + " download speed"
			case "ip":
				item.Label = "Net " + iface + " ip"
			case "interface":
				item.Label = "Net " + iface + " interface"
			}
			values[entry.name] = item
		case webSnapshotLabelDiskIndexed:
			diskName := resolveGoNativeIndexedMonitorAnchor(values, "go_native.disk.", entry.index, "name")
			if diskName == "" {
				diskName = "disk" + itoa(entry.index)
			}
			switch entry.suffix {
			case "name":
				item.Label = "Disk " + diskName + " name"
			case "size":
				item.Label = "Disk " + diskName + " size"
			case "used":
				item.Label = "Disk " + diskName + " used"
			case "available":
				item.Label = "Disk " + diskName + " available"
			case "usage":
				item.Label = "Disk " + diskName + " usage"
			case "temp":
				item.Label = "Disk " + diskName + " temperature"
			case "busy":
				item.Label = "Disk " + diskName + " busy"
			case "read":
				item.Label = "Disk " + diskName + " read speed"
			case "write":
				item.Label = "Disk " + diskName + " write speed"
			case "read_iops":
				item.Label = "Disk " + diskName + " read IOPS"
			case "write_iops":
				item.Label = "Disk " + diskName + " write IOPS"
			case "read_latency":
				item.Label = "Disk " + diskName + " read latency"
			case "write_latency":
				item.Label = "Disk " + diskName + " write latency"
			}
			values[entry.name] = item
		}
	}
}

var explicitWebSnapshotLabels = map[string]string{
	"go_native.cpu.usage":                      "CPU usage",
	"go_native.cpu.min_freq":                   "CPU min frequency",
	"go_native.cpu.user":                       "CPU user",
	"go_native.cpu.system":                     "CPU system",
	"go_native.cpu.idle":                       "CPU idle",
	"go_native.cpu.iowait":                     "CPU iowait",
	"go_native.cpu.irq":                        "CPU irq",
	"go_native.cpu.softirq":                    "CPU softirq",
	"go_native.cpu.temp":                       "CPU temperature",
	"go_native.cpu.freq":                       "CPU frequency",
	"go_native.cpu.max_freq":                   "CPU max frequency",
	"go_native.cpu.model":                      "CPU model",
	"go_native.cpu.cores":                      "CPU cores",
	"go_native.disk.total_read":                "Disk total read speed",
	"go_native.disk.total_write":               "Disk total write speed",
	"go_native.disk.max_busy":                  "Disk max busy",
	"go_native.disk.max_latency":               "Disk max latency",
	"go_native.disk.max_temp":                  "Disk max temperature",
	"go_native.btrfs_root.device_size":         "Btrfs root device size",
	"go_native.btrfs_root.allocated":           "Btrfs root allocated",
	"go_native.btrfs_root.allocated_used":      "Btrfs root allocated used",
	"go_native.btrfs_root.unallocated":         "Btrfs root unallocated",
	"go_native.btrfs_root.alloc_usage":         "Btrfs root allocation usage",
	"go_native.btrfs_root.data_usage":          "Btrfs root data usage",
	"go_native.btrfs_root.metadata_usage":      "Btrfs root metadata usage",
	"go_native.btrfs_root.system_usage":        "Btrfs root system usage",
	"go_native.btrfs_root.pinned":              "Btrfs root pinned",
	"go_native.btrfs_root.reclaim":             "Btrfs root reclaim",
	"go_native.btrfs_root.discardable":         "Btrfs root discardable",
	"go_native.memory.usage":                   "Memory usage",
	"go_native.memory.used":                    "Memory used",
	"go_native.memory.total":                   "Memory total",
	"go_native.memory.usage_text":              "Memory usage detail",
	"go_native.memory.usage_progress":          "Memory usage progress",
	"go_native.memory.swap_usage":              "Swap usage",
	"go_native.system.load_avg":                "System load average",
	"go_native.system.current_time":            "Current time",
	"go_native.system.hostname":                "Host name",
	"go_native.system.resolution":              "Display resolution",
	"go_native.system.refresh_rate":            "Display refresh rate",
	"go_native.system.display":                 "Display mode",
	"go_native.system.collect.max_ms":          "Collect max ms",
	"go_native.system.collect.avg_ms":          "Collect avg ms",
	"go_native.system.render.max_ms":           "Render max ms",
	"go_native.system.render.avg_ms":           "Render avg ms",
	"go_native.system.output.max_ms":           "Output max ms",
	"go_native.system.output.avg_ms":           "Output avg ms",
	"go_native.system.output.memimg.last_ms":   "Output memimg last ms",
	"go_native.system.output.memimg.max_ms":    "Output memimg max ms",
	"go_native.system.output.memimg.avg_ms":    "Output memimg avg ms",
	"go_native.system.output.ax206usb.last_ms": "AX206 refresh duration",
	"go_native.system.output.ax206usb.max_ms":  "Output ax206usb max ms",
	"go_native.system.output.ax206usb.avg_ms":  "Output ax206usb avg ms",
	"go_native.system.output.httppush.last_ms": "HTTP push last ms",
	"go_native.system.output.httppush.max_ms":  "HTTP push max ms",
	"go_native.system.output.httppush.avg_ms":  "HTTP push avg ms",
	"go_native.system.output.tcppush.last_ms":  "TCP push last ms",
	"go_native.system.output.tcppush.max_ms":   "TCP push max ms",
	"go_native.system.output.tcppush.avg_ms":   "TCP push avg ms",
	"rtss_connected":                           "RTSS connected",
	"rtss_fps":                                 "RTSS FPS",
	"rtss_frametime_ms":                        "RTSS frame time",
	"rtss_fps_avg":                             "RTSS FPS avg",
	"rtss_fps_1p_low":                          "RTSS FPS 1% low",
	"rtss_fps_01p_low":                         "RTSS FPS 0.1% low",
	"rtss_frametime_min_ms":                    "RTSS frame time min",
	"rtss_frametime_avg_ms":                    "RTSS frame time avg",
	"rtss_frametime_max_ms":                    "RTSS frame time max",
	"rtss_frametime_p99_ms":                    "RTSS frame time p99",
	"rtss_frametime_p999_ms":                   "RTSS frame time p99.9",
	"rtss_max_fps":                             "RTSS max FPS",
	"rtss_active_apps":                         "RTSS active apps",
	"rtss_foreground_pid":                      "RTSS foreground PID",
}

func parseGoNativeIndexedWebMonitor(name, basePrefix string) (int, string, bool) {
	if !strings.HasPrefix(name, basePrefix) {
		return 0, "", false
	}
	rest := strings.TrimPrefix(name, basePrefix)
	parts := strings.Split(rest, ".")
	if len(parts) != 2 {
		return 0, "", false
	}
	indexText := parts[0]
	index := atoi(indexText)
	if index <= 0 {
		return 0, "", false
	}
	suffix := parts[1]
	if suffix == "" {
		return 0, "", false
	}
	return index, suffix, true
}

func resolveGoNativeIndexedMonitorAnchor(values map[string]WebMonitorSnapshotItem, basePrefix string, index int, anchor string) string {
	key := basePrefix + itoa(index) + "." + anchor
	item, ok := values[key]
	if !ok {
		return ""
	}
	text := strings.TrimSpace(item.Text)
	if text == "" || text == "-" {
		return ""
	}
	return text
}

func humanizeMonitorKey(key string) string {
	if strings.TrimSpace(key) == "" {
		return key
	}
	parts := strings.Split(key, "_")
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, " ")
}

func atoi(text string) int {
	value := 0
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0
		}
		value = value*10 + int(ch-'0')
	}
	return value
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	v := value
	for v > 0 {
		i--
		buf[i] = byte('0' + (v % 10))
		v /= 10
	}
	return string(buf[i:])
}
