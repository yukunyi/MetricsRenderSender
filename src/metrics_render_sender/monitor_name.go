package main

import "strings"

var monitorAliasMap = map[string]string{
	"disk_default_read_speed":  "go_native.disk.total_read",
	"disk_default_write_speed": "go_native.disk.total_write",
	"disk_default_temp":        "go_native.disk.max_temp",
}

var monitorAliasLabelMap = map[string]string{
	"disk_default_read_speed":  "Disk total read speed",
	"disk_default_write_speed": "Disk total write speed",
	"disk_default_temp":        "Disk max temperature",
}

func normalizeMonitorNameInput(name string) string {
	return strings.TrimSpace(name)
}

func normalizeMonitorAliasInput(name string) string {
	return normalizeMonitorNameInput(name)
}

func normalizeMonitorAlias(name string) string {
	trimmed := normalizeMonitorAliasInput(name)
	if trimmed == "" {
		return ""
	}
	if target, ok := monitorAliasMap[trimmed]; ok {
		return target
	}
	return trimmed
}

func isMonitorAliasName(name string) bool {
	_, ok := monitorAliasMap[normalizeMonitorAliasInput(name)]
	return ok
}

func monitorAliasNames() []string {
	names := make([]string, 0, len(monitorAliasMap))
	for name := range monitorAliasMap {
		names = append(names, name)
	}
	return names
}

func monitorAliasLabels() map[string]string {
	labels := make(map[string]string, len(monitorAliasLabelMap))
	for name, label := range monitorAliasLabelMap {
		labels[name] = label
	}
	return labels
}

func resolveMonitorAliasWithItems(name string, items map[string]*CollectItem) string {
	normalized := normalizeMonitorAliasInput(name)
	if normalized == "" {
		return ""
	}
	target, ok := monitorAliasMap[normalized]
	if !ok {
		return normalized
	}
	if items == nil {
		return target
	}
	if _, exists := items[target]; exists {
		return target
	}
	return target
}

func buildMonitorAliasResolution(items map[string]*CollectItem) map[string]string {
	resolution := make(map[string]string, len(monitorAliasMap))
	for name := range monitorAliasMap {
		resolution[name] = resolveMonitorAliasWithItems(name, items)
	}
	return resolution
}
