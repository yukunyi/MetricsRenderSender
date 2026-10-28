package main

import (
	"github.com/shirou/gopsutil/v3/cpu"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type GoNativeCPUCollector struct {
	*BaseCollector

	usageLastTotal float64
	usageLastIdle  float64
	usageLastUser  float64
	usageLastSys   float64
	usageLastIO    float64
	usageLastIRQ   float64
	usageLastSIRQ  float64
	usageLastValue cpuUsageBreakdown
	usageReady     bool

	tempMu       sync.RWMutex
	tempValue    float64
	tempOK       bool
	tempAt       time.Time
	tempUpdating int32

	freqMu       sync.RWMutex
	freqValue    float64
	freqMaxValue float64
	freqOK       bool
	freqAt       time.Time
	freqUpdating int32
}

func NewGoNativeCPUCollector() *GoNativeCPUCollector {
	collector := &GoNativeCPUCollector{BaseCollector: NewBaseCollector("go_native.cpu")}
	if nativeCPUTemperatureSupported() {
		collector.triggerTempRefresh()
	}
	collector.triggerFreqRefresh()
	return collector
}

func (c *GoNativeCPUCollector) GetAllItems() map[string]*CollectItem {
	if c.getItem("go_native.cpu.usage") == nil {
		c.setItem("go_native.cpu.usage", NewCollectItem("go_native.cpu.usage", "CPU usage", "%", 0, 100, 0))
		c.setItem("go_native.cpu.user", NewCollectItem("go_native.cpu.user", "CPU user", "%", 0, 100, 0))
		c.setItem("go_native.cpu.system", NewCollectItem("go_native.cpu.system", "CPU system", "%", 0, 100, 0))
		c.setItem("go_native.cpu.idle", NewCollectItem("go_native.cpu.idle", "CPU idle", "%", 0, 100, 0))
		c.setItem("go_native.cpu.iowait", NewCollectItem("go_native.cpu.iowait", "CPU iowait", "%", 0, 100, 0))
		c.setItem("go_native.cpu.irq", NewCollectItem("go_native.cpu.irq", "CPU irq", "%", 0, 100, 0))
		c.setItem("go_native.cpu.softirq", NewCollectItem("go_native.cpu.softirq", "CPU softirq", "%", 0, 100, 0))
		if nativeCPUTemperatureSupported() {
			c.setItem("go_native.cpu.temp", NewCollectItem("go_native.cpu.temp", "CPU temperature", "°C", 0, 120, 0))
		}
		c.setItem("go_native.cpu.freq", NewCollectItem("go_native.cpu.freq", "CPU frequency", "MHz", 0, 0, 0))
		c.setItem("go_native.cpu.max_freq", NewCollectItem("go_native.cpu.max_freq", "CPU max frequency", "MHz", 0, 0, 0))
		c.setItem("go_native.cpu.model", NewCollectItem("go_native.cpu.model", "CPU model", "", 0, 0, 0))
		c.setItem("go_native.cpu.cores", NewCollectItem("go_native.cpu.cores", "CPU cores", "", 0, 0, 0))
	}

	initializeCache()
	if cachedCPUInfo != nil {
		if item := c.getItem("go_native.cpu.model"); item != nil {
			item.SetValue(cachedCPUInfo.Model)
			item.SetAvailable(true)
		}
		if item := c.getItem("go_native.cpu.cores"); item != nil {
			item.SetValue(cachedCPUInfo.Cores)
			item.SetAvailable(true)
		}
	} else {
		if item := c.getItem("go_native.cpu.model"); item != nil {
			item.SetAvailable(false)
		}
		if item := c.getItem("go_native.cpu.cores"); item != nil {
			item.SetAvailable(false)
		}
	}
	return c.ItemsSnapshot()
}

func (c *GoNativeCPUCollector) UpdateItems() error {
	if !c.IsEnabled() {
		return nil
	}

	now := time.Now()
	c.maybeRefreshTemp(now)
	c.maybeRefreshFreq(now)

	usageValue, usageOK, usageErr := c.sampleCPUUsage()
	if usage := c.getItem("go_native.cpu.usage"); usage != nil {
		if usageOK {
			usage.SetValue(usageValue.Usage)
			usage.SetAvailable(true)
		} else {
			usage.SetAvailable(false)
		}
	}
	c.updateCPUUsageBreakdownItem("go_native.cpu.user", usageValue.User, usageOK)
	c.updateCPUUsageBreakdownItem("go_native.cpu.system", usageValue.System, usageOK)
	c.updateCPUUsageBreakdownItem("go_native.cpu.idle", usageValue.Idle, usageOK)
	c.updateCPUUsageBreakdownItem("go_native.cpu.iowait", usageValue.Iowait, usageOK)
	c.updateCPUUsageBreakdownItem("go_native.cpu.irq", usageValue.Irq, usageOK)
	c.updateCPUUsageBreakdownItem("go_native.cpu.softirq", usageValue.Softirq, usageOK)

	if temp := c.getItem("go_native.cpu.temp"); temp != nil {
		if value, ok := c.getCachedTemp(); ok {
			temp.SetValue(value)
			temp.SetAvailable(true)
		} else {
			temp.SetAvailable(false)
		}
	}

	if freq := c.getItem("go_native.cpu.freq"); freq != nil {
		if value, ok := c.getCachedFreq(); ok {
			freq.SetValue(value)
			freq.SetAvailable(true)
		} else {
			freq.SetAvailable(false)
		}
	}
	if maxFreq := c.getItem("go_native.cpu.max_freq"); maxFreq != nil {
		if value, ok := c.getCachedMaxFreq(); ok {
			maxFreq.SetValue(value)
			maxFreq.SetAvailable(true)
		} else {
			maxFreq.SetAvailable(false)
		}
	}

	return usageErr
}

type cpuUsageBreakdown struct {
	Usage   float64
	User    float64
	System  float64
	Idle    float64
	Iowait  float64
	Irq     float64
	Softirq float64
}

func (c *GoNativeCPUCollector) updateCPUUsageBreakdownItem(name string, value float64, ok bool) {
	item := c.getItem(name)
	if item == nil {
		return
	}
	if ok {
		item.SetValue(value)
		item.SetAvailable(true)
		return
	}
	item.SetAvailable(false)
}

func (c *GoNativeCPUCollector) sampleCPUUsage() (cpuUsageBreakdown, bool, error) {
	stats, err := cpu.Times(false)
	if err != nil || len(stats) == 0 {
		return cpuUsageBreakdown{}, false, err
	}
	sample := stats[0]
	total := cpuTotalTime(sample)
	idle := sample.Idle + sample.Iowait

	if !c.usageReady {
		c.usageLastTotal = total
		c.usageLastIdle = idle
		c.usageLastUser = sample.User
		c.usageLastSys = sample.System
		c.usageLastIO = sample.Iowait
		c.usageLastIRQ = sample.Irq
		c.usageLastSIRQ = sample.Softirq
		c.usageReady = true

		percent, percentErr := cpu.Percent(0, false)
		if percentErr == nil && len(percent) > 0 {
			c.usageLastValue = cpuUsageBreakdown{Usage: clampPercentage(percent[0])}
			return c.usageLastValue, true, nil
		}
		return cpuUsageBreakdown{}, false, percentErr
	}

	totalDelta := total - c.usageLastTotal
	c.usageLastTotal = total
	breakdown := computeCPUUsageBreakdown(
		sample,
		totalDelta,
		c.usageLastIdle,
		c.usageLastUser,
		c.usageLastSys,
		c.usageLastIO,
		c.usageLastIRQ,
		c.usageLastSIRQ,
	)
	c.usageLastIdle = idle
	c.usageLastUser = sample.User
	c.usageLastSys = sample.System
	c.usageLastIO = sample.Iowait
	c.usageLastIRQ = sample.Irq
	c.usageLastSIRQ = sample.Softirq
	if totalDelta <= 0 {
		if c.usageLastValue.Usage >= 0 {
			return c.usageLastValue, true, nil
		}
		return cpuUsageBreakdown{}, false, nil
	}
	c.usageLastValue = breakdown
	return breakdown, true, nil
}

func cpuTotalTime(sample cpu.TimesStat) float64 {
	total := sample.User + sample.System + sample.Idle + sample.Nice + sample.Iowait + sample.Irq + sample.Softirq + sample.Steal + sample.Guest + sample.GuestNice
	if runtime.GOOS == "linux" {
		// Linux already includes guest time in the user and nice counters.
		total -= sample.Guest + sample.GuestNice
	}
	return total
}

func clampPercentage(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func computeCPUUsageBreakdown(
	sample cpu.TimesStat,
	totalDelta float64,
	lastIdle float64,
	lastUser float64,
	lastSystem float64,
	lastIowait float64,
	lastIRQ float64,
	lastSoftirq float64,
) cpuUsageBreakdown {
	if totalDelta <= 0 {
		return cpuUsageBreakdown{}
	}
	idleNow := sample.Idle + sample.Iowait
	idleDelta := idleNow - lastIdle
	usage := ((totalDelta - idleDelta) / totalDelta) * 100.0
	return cpuUsageBreakdown{
		Usage:   clampPercentage(usage),
		User:    clampPercentage((sample.User - lastUser) / totalDelta * 100.0),
		System:  clampPercentage((sample.System - lastSystem) / totalDelta * 100.0),
		Idle:    clampPercentage((sample.Idle - (lastIdle - lastIowait)) / totalDelta * 100.0),
		Iowait:  clampPercentage((sample.Iowait - lastIowait) / totalDelta * 100.0),
		Irq:     clampPercentage((sample.Irq - lastIRQ) / totalDelta * 100.0),
		Softirq: clampPercentage((sample.Softirq - lastSoftirq) / totalDelta * 100.0),
	}
}

func nativeCPUTemperatureSupported() bool {
	return runtime.GOOS != "windows"
}

func (c *GoNativeCPUCollector) maybeRefreshTemp(now time.Time) {
	if !nativeCPUTemperatureSupported() {
		return
	}
	c.tempMu.RLock()
	stale := c.tempAt.IsZero() || now.Sub(c.tempAt) >= 2*time.Second
	c.tempMu.RUnlock()
	if stale {
		c.triggerTempRefresh()
	}
}

func (c *GoNativeCPUCollector) triggerTempRefresh() {
	if !nativeCPUTemperatureSupported() {
		return
	}
	if !atomic.CompareAndSwapInt32(&c.tempUpdating, 0, 1) {
		return
	}
	go func() {
		defer atomic.StoreInt32(&c.tempUpdating, 0)
		value := getRealCPUTemperature()
		now := time.Now()
		c.tempMu.Lock()
		c.tempAt = now
		if value > 0 {
			c.tempValue = value
			c.tempOK = true
		} else {
			c.tempOK = false
		}
		c.tempMu.Unlock()
	}()
}

func (c *GoNativeCPUCollector) getCachedTemp() (float64, bool) {
	c.tempMu.RLock()
	defer c.tempMu.RUnlock()
	if !c.tempOK {
		return 0, false
	}
	return c.tempValue, true
}

func (c *GoNativeCPUCollector) maybeRefreshFreq(now time.Time) {
	c.freqMu.RLock()
	stale := c.freqAt.IsZero() || now.Sub(c.freqAt) >= 3*time.Second
	c.freqMu.RUnlock()
	if stale {
		c.triggerFreqRefresh()
	}
}

func (c *GoNativeCPUCollector) triggerFreqRefresh() {
	if !atomic.CompareAndSwapInt32(&c.freqUpdating, 0, 1) {
		return
	}
	go func() {
		defer atomic.StoreInt32(&c.freqUpdating, 0)
		current, maxFreq := getRealCPUFrequency()
		now := time.Now()
		c.freqMu.Lock()
		c.freqAt = now
		if current > 0 {
			c.freqValue = current
			c.freqOK = true
		} else {
			c.freqOK = false
		}
		if maxFreq > 0 {
			c.freqMaxValue = maxFreq
		}
		c.freqMu.Unlock()
	}()
}

func (c *GoNativeCPUCollector) getCachedFreq() (float64, bool) {
	c.freqMu.RLock()
	defer c.freqMu.RUnlock()
	if !c.freqOK {
		return 0, false
	}
	return c.freqValue, true
}

func (c *GoNativeCPUCollector) getCachedMaxFreq() (float64, bool) {
	c.freqMu.RLock()
	defer c.freqMu.RUnlock()
	if c.freqMaxValue <= 0 {
		return 0, false
	}
	return c.freqMaxValue, true
}
