import { normalizeStyleKeys } from "./style_keys";
import { isRangeType, normalizeItemTypes } from "./item_types";
import { normalizeOutputs } from "./output_configs";
import { normalizeMonitorName } from "./monitor_aliases";

function defaultCreateItemId() {
  return `itm_${Date.now()}`;
}

export function buildStyleKeySet(styleKeys) {
  const set = new Set();
  normalizeStyleKeys(styleKeys).forEach((meta) => {
    if (meta.key) set.add(meta.key);
  });
  return set;
}

export function normalizeStyleMap(raw, styleKeySet) {
  const source = raw && typeof raw === "object" ? raw : {};
  const result = {};
  Object.entries(source).forEach(([key, value]) => {
    const name = String(key || "").trim();
    if (!name || !styleKeySet.has(name)) return;
    result[name] = value;
  });
  return result;
}

export function normalizeRenderAttrs(raw, styleKeySet) {
  const source = raw && typeof raw === "object" ? raw : {};
  const result = {};
  Object.entries(source).forEach(([key, value]) => {
    const name = String(key || "").trim();
    if (!name || styleKeySet.has(name)) return;
    result[name] = value;
  });
  return result;
}

export function normalizeFullTableRows(raw) {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((entry) => {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) return null;
      const monitor = String(entry.monitor || "").trim();
      const label = String(entry.label || "").trim();
      return { monitor, label };
    })
    .filter(Boolean);
}

export function normalizePositiveInt(raw, fallback = 1) {
  const value = Number(raw);
  if (!Number.isFinite(value)) return fallback;
  const rounded = Math.round(value);
  return rounded >= 1 ? rounded : fallback;
}

function normalizeFiniteNumber(raw) {
  const value = Number(raw);
  return Number.isFinite(value) ? value : null;
}

function normalizeItemRangeFields(item) {
  if (!item || typeof item !== "object") return item;
  if (!isRangeType(item.type)) {
    delete item.min_value;
    delete item.max_value;
    return item;
  }

  const minValue = normalizeFiniteNumber(item.min_value);
  const maxValue = normalizeFiniteNumber(item.max_value);
  if (minValue !== null) {
    item.min_value = minValue;
  } else {
    delete item.min_value;
  }
  if (maxValue !== null) {
    item.max_value = maxValue;
  } else {
    delete item.max_value;
  }
  return item;
}

export function normalizeThresholdRanges(raw) {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((entry) => {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) return null;
      const color = String(entry.color || "").trim();
      if (!color) return null;
      const min = normalizeFiniteNumber(entry.min);
      const max = normalizeFiniteNumber(entry.max);
      if (min !== null && max !== null && max < min) return null;
      const result = { color };
      if (min !== null) result.min = min;
      if (max !== null) result.max = max;
      return result;
    })
    .filter(Boolean);
}

export function normalizeThresholdGroups(raw) {
  if (!Array.isArray(raw)) return [];
  const used = new Set();
  return raw
    .map((entry) => {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) return null;
      const name = String(entry.name || "").trim();
      if (!name || used.has(name)) return null;
      const monitors = Array.isArray(entry.monitors)
        ? [...new Set(entry.monitors.map((item) => normalizeMonitorName(item)).filter(Boolean))]
        : [];
      const ranges = normalizeThresholdRanges(entry.ranges);
      if (ranges.length === 0) return null;
      used.add(name);
      return { name, monitors, ranges };
    })
    .filter(Boolean);
}

export function normalizeItemRenderAttrs(itemType, raw, styleKeySet) {
  const normalizedType = String(itemType || "").trim();
  const result = normalizeRenderAttrs(raw, styleKeySet);
  if (normalizedType === "full_table") {
    const rows = normalizeFullTableRows(result.rows);
    const colCount = normalizePositiveInt(result.col_count, 1);
    const rowCount = normalizePositiveInt(result.row_count, 1);
    result.rows = rows;
    if (colCount > 1) {
      result.col_count = colCount;
    } else {
      delete result.col_count;
    }
    if (rowCount > 1) {
      result.row_count = rowCount;
    } else {
      delete result.row_count;
    }
    delete result.columns;
    return result;
  }
  delete result.rows;
  delete result.columns;
  delete result.col_count;
  delete result.row_count;
  return result;
}

export function normalizeTypeDefaults(raw, styleKeySet, itemTypesRaw = []) {
  const source = raw && typeof raw === "object" ? raw : {};
  const result = {};
  normalizeItemTypes(itemTypesRaw).forEach((type) => {
    const input = source[type] && typeof source[type] === "object" ? source[type] : {};
    result[type] = {
      render_attrs_map: normalizeItemRenderAttrs(type, input.render_attrs_map, styleKeySet),
      style: normalizeStyleMap(input.style, styleKeySet),
    };
  });
  Object.keys(source).forEach((type) => {
    if (result[type]) return;
    const input = source[type] && typeof source[type] === "object" ? source[type] : {};
    result[type] = {
      render_attrs_map: normalizeItemRenderAttrs(type, input.render_attrs_map, styleKeySet),
      style: normalizeStyleMap(input.style, styleKeySet),
    };
  });
  return result;
}

export function ensureCollectorEntry(config, collectorName, defaultCollectorEnabled) {
  if (!config.collector_config || typeof config.collector_config !== "object") {
    config.collector_config = {};
  }
  if (!config.collector_config[collectorName]) {
    config.collector_config[collectorName] = {
      enabled: !!defaultCollectorEnabled[collectorName],
      options: {},
    };
  }
  if (!config.collector_config[collectorName].options) {
    config.collector_config[collectorName].options = {};
  }
}

export function normalizeConfigModel(
  cfg,
  {
    styleKeysRaw = [],
    itemTypesRaw = [],
    createItemId = defaultCreateItemId,
    defaultCollectorEnabled = {},
  } = {},
) {
  const styleKeySet = buildStyleKeySet(styleKeysRaw);
  const config = JSON.parse(JSON.stringify(cfg || {}));
  config.name = String(config.name || "web");
  config.width = Math.max(10, Number(config.width || 480));
  config.height = Math.max(10, Number(config.height || 320));
  config.layout_padding = Math.max(0, Number(config.layout_padding || 0));
  config.refresh_interval = Math.max(100, Number(config.refresh_interval || 1000));
  config.collect_warn_ms = Math.max(10, Number(config.collect_warn_ms || 100));
  config.render_wait_max_ms = Math.max(0, Number(config.render_wait_max_ms || 300));
  config.history_size = Math.max(10, Number(config.history_size || 180));
  config.default_history_points = Math.max(10, Number(config.default_history_points || 150));
  config.default_font = String(config.default_font || "");
  config.style_base = normalizeStyleMap(config.style_base, styleKeySet);
  config.allow_custom_style = config.allow_custom_style === true;
  config.font_families = Array.isArray(config.font_families) ? config.font_families : [];
  config.outputs = normalizeOutputs(config.outputs, config.output_types);
  config.output_types = [...new Set(config.outputs.filter((item) => item?.enabled !== false).map((item) => item.type))];
  delete config.pause_collect_on_lock;
  config.type_defaults = normalizeTypeDefaults(config.type_defaults, styleKeySet, itemTypesRaw);
  delete config.default_thresholds;
  config.threshold_groups = normalizeThresholdGroups(config.threshold_groups);
  config.items = Array.isArray(config.items) ? config.items : [];
  const itemIdSet = new Set();
  config.items = config.items.map((item) => {
    const next = { ...(item || {}) };
    next.id = String(next.id || "").trim() || createItemId();
    if (itemIdSet.has(next.id)) {
      next.id = createItemId();
    }
    itemIdSet.add(next.id);
    next.custom_style = config.allow_custom_style ? next.custom_style === true : false;
    next.monitor = normalizeMonitorName(next.monitor);
    next.style = normalizeStyleMap(next.style, styleKeySet);
    next.render_attrs_map = normalizeItemRenderAttrs(next.type, next.render_attrs_map, styleKeySet);
    normalizeItemRangeFields(next);
    return next;
  });
  config.custom_monitors = Array.isArray(config.custom_monitors) ? config.custom_monitors : [];
  config.custom_monitors = config.custom_monitors.map((item) => {
    const next = { ...(item || {}) };
    next.source = normalizeMonitorName(next.source);
    next.sources = Array.isArray(next.sources)
      ? next.sources.map((source) => normalizeMonitorName(source)).filter(Boolean)
      : [];
    return next;
  });
  config.collector_config = config.collector_config || {};
  Object.keys(defaultCollectorEnabled).forEach((name) =>
    ensureCollectorEntry(config, name, defaultCollectorEnabled),
  );
  return config;
}
