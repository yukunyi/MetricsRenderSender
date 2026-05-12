package main

import (
	"math"
	"sort"
	"strings"
)

func normalizeThresholdGroups(groups []ThresholdGroupConfig) []ThresholdGroupConfig {
	normalized := make([]ThresholdGroupConfig, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		entry := ThresholdGroupConfig{
			Name:     name,
			Monitors: normalizeThresholdGroupMonitors(group.Monitors),
			Ranges:   normalizeThresholdRanges(group.Ranges),
		}
		if len(entry.Ranges) == 0 {
			continue
		}
		normalized = append(normalized, entry)
		seen[name] = struct{}{}
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	return normalized
}

func normalizeThresholdGroupMonitors(monitors []string) []string {
	if len(monitors) == 0 {
		return nil
	}
	out := make([]string, 0, len(monitors))
	seen := make(map[string]struct{}, len(monitors))
	for _, raw := range monitors {
		name := normalizeMonitorAlias(raw)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func normalizeThresholdRanges(ranges []ThresholdRangeConfig) []ThresholdRangeConfig {
	if len(ranges) == 0 {
		return nil
	}
	out := make([]ThresholdRangeConfig, 0, len(ranges))
	for _, raw := range ranges {
		color := strings.TrimSpace(raw.Color)
		if color == "" {
			continue
		}
		entry := ThresholdRangeConfig{Color: color}
		if raw.Min != nil && !math.IsNaN(*raw.Min) && !math.IsInf(*raw.Min, 0) {
			minValue := *raw.Min
			entry.Min = &minValue
		}
		if raw.Max != nil && !math.IsNaN(*raw.Max) && !math.IsInf(*raw.Max, 0) {
			maxValue := *raw.Max
			entry.Max = &maxValue
		}
		if entry.Min != nil && entry.Max != nil && *entry.Max < *entry.Min {
			continue
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftMin := math.Inf(-1)
		rightMin := math.Inf(-1)
		if out[i].Min != nil {
			leftMin = *out[i].Min
		}
		if out[j].Min != nil {
			rightMin = *out[j].Min
		}
		if leftMin == rightMin {
			leftMax := math.Inf(1)
			rightMax := math.Inf(1)
			if out[i].Max != nil {
				leftMax = *out[i].Max
			}
			if out[j].Max != nil {
				rightMax = *out[j].Max
			}
			return leftMax < rightMax
		}
		return leftMin < rightMin
	})
	return out
}

func findThresholdGroupByName(config *MonitorConfig, name string) *ThresholdGroupConfig {
	if config == nil {
		return nil
	}
	target := strings.TrimSpace(name)
	if target == "" {
		return nil
	}
	for idx := range config.ThresholdGroups {
		if config.ThresholdGroups[idx].Name == target {
			return &config.ThresholdGroups[idx]
		}
	}
	return nil
}

func findThresholdGroupForMonitor(config *MonitorConfig, monitorName string) *ThresholdGroupConfig {
	if config == nil {
		return nil
	}
	normalizedMonitor := normalizeMonitorNameInput(monitorName)
	if normalizedMonitor == "" {
		return nil
	}
	for idx := range config.ThresholdGroups {
		group := &config.ThresholdGroups[idx]
		for _, candidate := range group.Monitors {
			if candidate == normalizedMonitor {
				return group
			}
		}
	}
	return nil
}

func resolveThresholdRangeColor(group *ThresholdGroupConfig, value float64) string {
	if group == nil {
		return ""
	}
	return resolveThresholdRangesColor(group.Ranges, value)
}

func resolveThresholdRangesColor(ranges []ThresholdRangeConfig, value float64) string {
	if len(ranges) == 0 {
		return ""
	}
	firstColor := strings.TrimSpace(ranges[0].Color)
	lastColor := strings.TrimSpace(ranges[len(ranges)-1].Color)
	for _, thresholdRange := range ranges {
		if thresholdRange.Min != nil && value < *thresholdRange.Min {
			continue
		}
		if thresholdRange.Max != nil && value > *thresholdRange.Max {
			continue
		}
		return strings.TrimSpace(thresholdRange.Color)
	}
	if first := ranges[0]; first.Min != nil && value < *first.Min {
		return firstColor
	}
	if last := ranges[len(ranges)-1]; last.Max != nil && value > *last.Max {
		return lastColor
	}
	return ""
}
