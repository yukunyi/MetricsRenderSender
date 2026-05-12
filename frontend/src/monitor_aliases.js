const MONITOR_ALIASES = {
  disk_default_read_speed: "go_native.disk.total_read",
  disk_default_write_speed: "go_native.disk.total_write",
  disk_default_temp: "go_native.disk.max_temp",
};

const MONITOR_ALIAS_LABELS = {
  disk_default_read_speed: "Disk total read speed",
  disk_default_write_speed: "Disk total write speed",
  disk_default_temp: "Disk max temperature",
};

export function normalizeMonitorName(raw) {
  const name = String(raw || "").trim();
  if (!name || name === "-") return "";
  return MONITOR_ALIASES[name] || name;
}

export function monitorAliasLabel(raw, labels = null) {
  const name = String(raw || "").trim();
  if (!name || name === "-") return "";
  const source = labels && typeof labels === "object" ? labels : MONITOR_ALIAS_LABELS;
  return String(source[name] || MONITOR_ALIAS_LABELS[name] || "").trim();
}
