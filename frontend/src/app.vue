<script setup>
import { darkTheme } from "naive-ui";
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import TopBar from "./components/top_bar.vue";
import BasicTab from "./components/basic_tab.vue";
import DeferredInput from "./components/deferred_input.vue";
import ElementsTab from "./components/elements_tab.vue";
import OtherTab from "./components/other_tab.vue";
import TypeDefaultsTab from "./components/type_defaults_tab.vue";
import RuntimeTab from "./components/runtime_tab.vue";
import { isMonitorRequiredType } from "./item_types";
import { monitorAliasLabel, normalizeMonitorName } from "./monitor_aliases";
import {
  buildStyleKeySet,
  normalizeConfigModel,
  normalizeFullTableRows,
  normalizeItemRenderAttrs,
  normalizePositiveInt,
  normalizeStyleMap,
} from "./config_normalizer";
import { applyAutoThresholdGroupsToConfig } from "./threshold_group_auto";

const state = reactive({
  loading: true,
  error: "",
  meta: null,
  config: null,
  profiles: [],
  collectors: [],
  snapshot: null,
  activeTab: "elements",
  editingProfile: "",
  loadedProfile: "",
  selectedIndex: -1,
  dirty: false,
  saving: false,
  profileLoading: false,
  previewUrl: "",
  previewSync: true,
  zoomAuto: true,
  zoom: 100,
});

const profileSwitch = reactive({
  pendingProfile: "",
});

const runtime = reactive({
  ws: null,
  ready: false,
  reconnectTimer: null,
  reqSeq: 1,
  pending: {},
});

const profileDialog = reactive({
  show: false,
  mode: "create",
  value: "",
  submitting: false,
  error: "",
});
const importInputRef = ref(null);

const DEFAULT_COLLECTOR_ENABLED = {
  "go_native.cpu": true,
  "go_native.memory": true,
  "go_native.system": true,
  "go_native.disk": true,
  "go_native.network": true,
  "go_native.btrfs_root": true,
  "go_native.zram": true,
  "custom.all": true,
  coolercontrol: false,
  librehardwaremonitor: false,
  rtss: false,
};

const PROFILE_NAME_RE = /^[A-Za-z0-9._-]+$/;

const theme_overrides = {
  common: {
    primaryColor: "#3b82f6",
    primaryColorHover: "#60a5fa",
    primaryColorPressed: "#2563eb",
    primaryColorSuppl: "#3b82f6",
    bodyColor: "#0b0f17",
    cardColor: "#111827",
    popoverColor: "#111827",
    modalColor: "#111827",
    tableColor: "#0f172a",
    actionColor: "#1f2937",
    borderColor: "#334155",
    dividerColor: "#334155",
    textColorBase: "#e5e7eb",
    textColor1: "#f8fafc",
    textColor2: "#e2e8f0",
    textColor3: "#94a3b8",
    placeholderColor: "#94a3b8",
    inputColor: "#0f172a",
    inputColorDisabled: "#1f2937",
    closeIconColor: "#94a3b8",
    closeColorHover: "#334155",
  },
  Card: {
    color: "#111827",
    textColor: "#f1f5f9",
    titleTextColor: "#f8fafc",
    borderColor: "#334155",
  },
  Input: {
    color: "#0f172a",
    colorFocus: "#0f172a",
    colorDisabled: "#1f2937",
    textColor: "#f8fafc",
    border: "#334155",
    borderHover: "#60a5fa",
    borderFocus: "#3b82f6",
    placeholderColor: "#94a3b8",
  },
  Button: {
    textColorPrimary: "#f8fafc",
    textColorHoverPrimary: "#f8fafc",
    textColorPressedPrimary: "#e2e8f0",
    textColorFocusPrimary: "#f8fafc",
    borderPrimary: "#2563eb",
    borderHoverPrimary: "#3b82f6",
    borderPressedPrimary: "#1d4ed8",
    colorPrimary: "#2563eb",
    colorHoverPrimary: "#3b82f6",
    colorPressedPrimary: "#1d4ed8",
  },
  Select: {
    peers: {
      InternalSelection: {
        color: "#0f172a",
        textColor: "#f8fafc",
        border: "#334155",
        borderHover: "#60a5fa",
        borderFocus: "#3b82f6",
      },
    },
  },
  DataTable: {
    thColor: "#111827",
    tdColor: "#0f172a",
    borderColor: "#334155",
    thTextColor: "#cbd5e1",
    tdTextColor: "#f8fafc",
    borderRadius: 6,
  },
  Table: {
    tdColor: "#0f172a",
    thColor: "#111827",
    borderColor: "#334155",
  },
};

const readonlyProfile = computed(() => {
  const current = state.profiles.find((item) => item.name === state.editingProfile);
  return !!current?.readonly;
});

const activeProfile = computed(() => String(state.meta?.active_profile || ""));
const monitorCatalog = ref([]);
const monitorCatalogSet = new Set();
const monitorLabelMap = reactive({});

const visibleTabs = computed(() => {
  if (readonlyProfile.value) {
    return [
      { key: "elements", label: "屏幕元素" },
      { key: "collection", label: "采集运行态" },
    ];
  }
  return [
    { key: "basic", label: "基础配置" },
    { key: "other", label: "其他" },
    { key: "elements", label: "屏幕元素" },
    { key: "type-defaults", label: "样式管理" },
    { key: "collection", label: "采集运行态" },
  ];
});

const monitorOptions = computed(() =>
  monitorCatalog.value.map((name) => {
    const label = String(monitorLabelMap[name] || aliasLabel(name) || "").trim();
    if (!label || label === name) return { label: name, value: name };
    return { label: `${label} (${name})`, value: name };
  }),
);

const currentItem = computed(() => {
  if (!state.config?.items?.length) return null;
  if (state.selectedIndex < 0 || state.selectedIndex >= state.config.items.length) return null;
  return state.config.items[state.selectedIndex];
});

const undoStack = ref([]);
const committedConfig = ref(null);
const committedConfigJson = ref("");
const canUndo = computed(() => undoStack.value.length > 0);

let itemIdSeed = 1;

function aliasLabel(name) {
  return monitorAliasLabel(name, state.meta?.monitor_alias_labels || null);
}

function createItemId() {
  const stamp = Date.now();
  const seq = itemIdSeed++;
  return `itm_${stamp}_${seq}`;
}

function deepClone(obj) {
  return JSON.parse(JSON.stringify(obj));
}

function serializeConfig(config) {
  if (!config || typeof config !== "object") return "";
  try {
    return JSON.stringify(config);
  } catch (_) {
    return "";
  }
}

function clearUndoStack() {
  undoStack.value = [];
}

function markCommittedFromCurrent() {
  if (!state.config) {
    committedConfig.value = null;
    committedConfigJson.value = "";
    state.dirty = false;
    clearUndoStack();
    return;
  }
  committedConfig.value = deepClone(state.config);
  committedConfigJson.value = serializeConfig(committedConfig.value);
  state.dirty = false;
  clearUndoStack();
}

function normalizeSelection() {
  if (!state.config?.items?.length) {
    state.selectedIndex = -1;
    return;
  }
  if (state.selectedIndex < 0 || state.selectedIndex >= state.config.items.length) {
    state.selectedIndex = 0;
  }
}

function pushUndoSnapshot(operation = "") {
  if (!state.config || state.saving) return;
  const encoded = serializeConfig(state.config);
  if (!encoded) return;
  const top = undoStack.value.length > 0 ? undoStack.value[undoStack.value.length - 1].encoded : "";
  if (top === encoded) return;
  undoStack.value.push({
    operation,
    encoded,
    config: deepClone(state.config),
  });
  const maxDepth = 80;
  if (undoStack.value.length > maxDepth) {
    undoStack.value.splice(0, undoStack.value.length - maxDepth);
  }
}

function mergeMonitorNames(names) {
  if (!Array.isArray(names) || names.length === 0) return;
  let changed = false;
  names.forEach((raw) => {
    const name = normalizeMonitorName(raw);
    if (!name || monitorCatalogSet.has(name)) return;
    monitorCatalogSet.add(name);
    const label = aliasLabel(name);
    if (label && !monitorLabelMap[name]) {
      monitorLabelMap[name] = label;
    }
    changed = true;
  });
  if (!changed) return;
  monitorCatalog.value = [...monitorCatalogSet].sort();
}

function mergeSnapshotMonitors(snapshot) {
  if (!snapshot || typeof snapshot !== "object") return;
  mergeMonitorNames(snapshot.monitors || []);
  mergeMonitorNames(Object.keys(snapshot.values || {}));
  Object.entries(snapshot.values || {}).forEach(([name, item]) => {
    const monitor = normalizeMonitorName(name);
    if (!monitor) return;
    const label = String(item?.label || aliasLabel(monitor) || "").trim();
    if (!label) return;
    monitorLabelMap[monitor] = label;
  });
}

function mergeConfigMonitors(config) {
  if (!config || typeof config !== "object") return;
  mergeMonitorNames((config.items || []).map((item) => item?.monitor));
  mergeMonitorNames((config.custom_monitors || []).map((item) => item?.name));
}

function normalizeConfig(cfg, styleKeysRaw = [], itemTypesRaw = []) {
  const collectorEnabled = { ...DEFAULT_COLLECTOR_ENABLED };
  if (!state.meta?.collectors?.includes("go_native.btrfs_root")) {
    delete collectorEnabled["go_native.btrfs_root"];
  }
  if (!state.meta?.collectors?.includes("go_native.zram")) {
    delete collectorEnabled["go_native.zram"];
  }
  return normalizeConfigModel(cfg, {
    styleKeysRaw,
    itemTypesRaw,
    createItemId,
    defaultCollectorEnabled: collectorEnabled,
  });
}

function createDefaultItem(type = "simple_value", monitor = "") {
  const selectedMonitor = String(monitor || "").trim();
  const defaultMonitor = selectedMonitor || String(monitorOptions.value[0]?.value || "");
  const isSimpleLine = type === "simple_line";
  const isFullGauge = type === "full_gauge";
  const isFullTable = type === "full_table";
  return {
    id: createItemId(),
    type,
    edit_ui_name: "",
    custom_style: false,
    monitor: isMonitorRequiredType(type) ? defaultMonitor : "",
    x: 10,
    y: 10,
    width: isSimpleLine ? 160 : isFullGauge ? 150 : isFullTable ? 220 : 140,
    height: isSimpleLine ? 12 : isFullGauge ? 120 : isFullTable ? 136 : 36,
    unit: isFullTable ? "" : "auto",
    style: {},
    render_attrs_map: isFullTable
      ? { col_count: 1, row_count: 1, rows: [{ monitor: "", label: "" }] }
      : {},
  };
}

function setDirty() {
  state.dirty = true;
  schedulePreviewSync();
}

function undoLastChange() {
  if (readonlyProfile.value || state.saving || undoStack.value.length <= 0) return;
  const last = undoStack.value.pop();
  if (!last?.config) return;
  state.config = normalizeConfig(last.config, state.meta?.style_keys || [], state.meta?.item_types || []);
  mergeConfigMonitors(state.config);
  normalizeSelection();
  const currentJson = serializeConfig(state.config);
  state.dirty = currentJson !== committedConfigJson.value;
  schedulePreviewSync();
  setError("");
}

function restoreUnsavedChanges() {
  if (readonlyProfile.value || state.saving || !state.dirty) return;
  if (!committedConfig.value) return;
  state.config = normalizeConfig(
    deepClone(committedConfig.value),
    state.meta?.style_keys || [],
    state.meta?.item_types || [],
  );
  mergeConfigMonitors(state.config);
  normalizeSelection();
  state.dirty = false;
  clearUndoStack();
  syncPreview(true).catch(() => {});
  setError("");
}

function setError(err) {
  state.error = err ? String(err) : "";
}

function triggerImportConfig() {
  if (readonlyProfile.value) {
    setError("内置只读配置不能直接导入，请先新建可编辑配置");
    return;
  }
  const input = importInputRef.value;
  if (!input) return;
  input.value = "";
  input.click();
}

async function onImportFileChange(event) {
  if (readonlyProfile.value) return;
  const file = event?.target?.files?.[0];
  if (!file) return;
  try {
    const text = await file.text();
    const parsed = JSON.parse(text);
    const imported = parsed && typeof parsed === "object" && parsed.config ? parsed.config : parsed;
    if (!imported || typeof imported !== "object") {
      throw new Error("配置文件格式不正确");
    }
    pushUndoSnapshot("import-config");
    state.config = normalizeConfig(imported, state.meta?.style_keys || [], state.meta?.item_types || []);
    mergeConfigMonitors(state.config);
    state.selectedIndex = state.config.items.length > 0 ? 0 : -1;
    setDirty();
    setError("");
  } catch (err) {
    setError(`导入失败: ${err.message}`);
  }
}

function exportConfig() {
  if (!state.config) return;
  const payload = {
    config: deepClone(state.config),
  };
  const json = JSON.stringify(payload, null, 2);
  const blob = new Blob([json], { type: "application/json;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  const profileName = String(state.editingProfile || "config").trim() || "config";
  link.href = url;
  link.download = `${profileName}.json`;
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  URL.revokeObjectURL(url);
}

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const payload = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(payload?.error || `HTTP ${res.status}`);
  }
  return payload;
}

function wsURL() {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${window.location.host}/api/ws`;
}

function clearPending(reason = "ws closed") {
  Object.keys(runtime.pending).forEach((id) => {
    const task = runtime.pending[id];
    if (task?.timer) window.clearTimeout(task.timer);
    if (task?.reject) task.reject(new Error(reason));
  });
  runtime.pending = {};
}

function connectWS() {
  if (runtime.ws && (runtime.ws.readyState === WebSocket.OPEN || runtime.ws.readyState === WebSocket.CONNECTING)) {
    return;
  }
  if (runtime.reconnectTimer) {
    window.clearTimeout(runtime.reconnectTimer);
    runtime.reconnectTimer = null;
  }

  const ws = new WebSocket(wsURL());
  runtime.ws = ws;

  ws.onopen = () => {
    runtime.ready = true;
    requestRuntime().catch(() => {});
  };

  ws.onmessage = (event) => {
    try {
      const message = JSON.parse(event.data);
      if (message.type === "runtime") {
        if (message.snapshot && state.activeTab === "collection") {
          state.snapshot = message.snapshot;
        } else if (message.snapshot && !state.snapshot) {
          state.snapshot = message.snapshot;
        }
        if (message.preview_png) {
          state.previewUrl = `data:image/png;base64,${message.preview_png}`;
        }
        return;
      }
      if (message.type === "response") {
        const pending = runtime.pending[message.id];
        if (!pending) return;
        if (pending.timer) window.clearTimeout(pending.timer);
        delete runtime.pending[message.id];
        if (message.ok) pending.resolve(message.result);
        else pending.reject(new Error(message.error || "request failed"));
      }
    } catch (_) {
      // ignore malformed ws messages
    }
  };

  ws.onclose = () => {
    runtime.ready = false;
    clearPending("ws closed");
    runtime.reconnectTimer = window.setTimeout(connectWS, 1000);
  };

  ws.onerror = () => {
    runtime.ready = false;
  };
}

function runtimeCall(type, payload = {}, timeout = 6000) {
  return new Promise((resolve, reject) => {
    if (!runtime.ready || !runtime.ws || runtime.ws.readyState !== WebSocket.OPEN) {
      reject(new Error("websocket not connected"));
      return;
    }
    const id = `${Date.now()}_${runtime.reqSeq++}`;
    const timer = window.setTimeout(() => {
      delete runtime.pending[id];
      reject(new Error(`runtime call timeout: ${type}`));
    }, timeout);
    runtime.pending[id] = { resolve, reject, timer };
    runtime.ws.send(JSON.stringify({ type, id, ...payload }));
  });
}

async function requestRuntime() {
  if (!runtime.ready) return;
  await runtimeCall("request_runtime", {}, 4000);
}

async function refreshMonitorCatalog() {
  try {
    if (runtime.ready) {
      await requestRuntime();
      mergeSnapshotMonitors(state.snapshot);
    }
  } catch (_) {
    // ignore websocket request failures and fallback to HTTP snapshot
  }

  if (monitorCatalog.value.length > 0) return;

  try {
    const snapshot = await api("/api/snapshot");
    if (snapshot && typeof snapshot === "object") {
      state.snapshot = snapshot;
      mergeSnapshotMonitors(snapshot);
    }
  } catch (_) {
    // ignore
  }

  if (monitorCatalog.value.length > 0) return;

  try {
    const meta = await api("/api/meta");
    mergeMonitorNames(meta?.monitors || []);
  } catch (_) {
    // ignore
  }
}

let previewSyncTimer = null;

function schedulePreviewSync() {
  if (!state.previewSync || !state.config) return;
  if (previewSyncTimer) {
    window.clearTimeout(previewSyncTimer);
    previewSyncTimer = null;
  }
  previewSyncTimer = window.setTimeout(() => {
    previewSyncTimer = null;
    syncPreview(true).catch(() => {});
  }, 220);
}

function setPreviewSync(value) {
  state.previewSync = !!value;
  if (state.previewSync) {
    syncPreview(true).catch(() => {});
  }
}

async function syncPreview(silent = false) {
  if (!state.config) return;
  try {
    await runtimeCall("preview_config", { config: deepClone(state.config) }, 6000);
    if (!silent) setError("");
  } catch (err) {
    if (!silent) setError(`预览同步失败: ${err.message}`);
  }
}

async function loadInitial() {
  state.loading = true;
  try {
    const [metaRes, profilesRes, collectorsRes, snapshotRes] = await Promise.all([
      api("/api/meta"),
      api("/api/profiles").catch(() => ({ active: "", items: [] })),
      api("/api/collectors").catch(() => ({ items: [] })),
      api("/api/snapshot").catch(() => null),
    ]);

    state.meta = metaRes;
    state.profiles = profilesRes.items || [];
    state.collectors = collectorsRes.items || [];
    if (snapshotRes && typeof snapshotRes === "object") {
      state.snapshot = snapshotRes;
    }
    mergeMonitorNames(metaRes?.monitors || []);
    mergeSnapshotMonitors(snapshotRes);
    const initialProfile = profilesRes.active || metaRes.active_profile || state.profiles[0]?.name || "default";
    await loadProfileIntoEditor(initialProfile, { force: true, syncPreviewAfter: false });
    if (monitorCatalog.value.length === 0) await refreshMonitorCatalog();
    setError("");
  } catch (err) {
    setError(err.message);
  } finally {
    state.loading = false;
  }
}

function patchByPath(target, path, value) {
  if (!target) return;
  const parts = Array.isArray(path) ? path : [path];
  let cur = target;
  for (let i = 0; i < parts.length - 1; i += 1) {
    const key = parts[i];
    if (!cur[key] || typeof cur[key] !== "object") cur[key] = {};
    cur = cur[key];
  }
  const leaf = parts[parts.length - 1];
  if (value === undefined) {
    delete cur[leaf];
  } else {
    cur[leaf] = value;
  }
  setDirty();
}

function onBasicChange({ path, value }) {
  if (!state.config || readonlyProfile.value) return;
  pushUndoSnapshot("basic-change");
  patchByPath(state.config, path, value);
  if ((Array.isArray(path) ? path.join(".") : String(path)) === "outputs") {
    state.config.output_types = (Array.isArray(state.config.outputs) ? state.config.outputs : [])
      .filter((item) => item?.enabled !== false)
      .map((item) => String(item?.type || "").trim().toLowerCase())
      .filter((item, index, arr) => item && arr.indexOf(item) === index);
  }
  if ((Array.isArray(path) ? path.join(".") : String(path)) === "allow_custom_style" && !value) {
    state.config.items = (state.config.items || []).map((item) => ({
      ...item,
      custom_style: false,
    }));
  }
}

function onItemFieldChange({ field, value }) {
  if (!state.config || readonlyProfile.value || !currentItem.value) return;
  pushUndoSnapshot(`item-field:${field}`);
  const styleKeySet = buildStyleKeySet(state.meta?.style_keys || []);
  if (field === "custom_style" && !state.config.allow_custom_style) {
    currentItem.value.custom_style = false;
    setDirty();
    return;
  }
  currentItem.value[field] = value;
  if (field === "type") {
    const nextType = String(value || "").trim();
    if (nextType === "full_table") {
      const currentRows = normalizeFullTableRows(currentItem.value.render_attrs_map?.rows);
      const colCount = normalizePositiveInt(currentItem.value.render_attrs_map?.col_count, 1);
      const rowCount = normalizePositiveInt(currentItem.value.render_attrs_map?.row_count, 1);
      const fallbackMonitor = normalizeMonitorName(currentItem.value.monitor);
      currentItem.value.render_attrs_map = {
        ...(currentItem.value.render_attrs_map || {}),
        col_count: colCount,
        row_count: rowCount,
        rows: currentRows.length > 0
          ? currentRows
          : [{ monitor: fallbackMonitor || "", label: "" }],
      };
      currentItem.value.monitor = "";
      currentItem.value.unit = "";
    } else {
      currentItem.value.render_attrs_map = normalizeItemRenderAttrs(
        nextType,
        currentItem.value.render_attrs_map,
        styleKeySet,
      );
      if (isMonitorRequiredType(nextType) && !String(currentItem.value.monitor || "").trim()) {
        currentItem.value.monitor = String(monitorOptions.value[0]?.value || "");
      }
      if (!String(currentItem.value.unit || "").trim()) {
        currentItem.value.unit = "auto";
      }
    }
  }
  if (field === "type" && String(value || "").trim() === "full_table") {
    currentItem.value.render_attrs_map = normalizeItemRenderAttrs(
      "full_table",
      currentItem.value.render_attrs_map,
      styleKeySet,
    );
  }
  if (field === "monitor") mergeMonitorNames([value]);
  setDirty();
}

function onItemPatch({ index, patch }) {
  if (!state.config || readonlyProfile.value || !state.config.items[index]) return;
  pushUndoSnapshot("item-patch");
  const item = state.config.items[index];
  if (!patch || typeof patch !== "object") return;
  const nextPatch = { ...patch };
  const styleKeySet = buildStyleKeySet(state.meta?.style_keys || []);
  if ("style" in nextPatch) {
    nextPatch.style = normalizeStyleMap(nextPatch.style, styleKeySet);
  }
  if ("render_attrs_map" in nextPatch) {
    nextPatch.render_attrs_map = normalizeItemRenderAttrs(
      String(nextPatch.type || item.type || ""),
      nextPatch.render_attrs_map,
      styleKeySet,
    );
  }
  if (!(state.config.allow_custom_style && item.custom_style === true)) {
    delete nextPatch.style;
  }
  Object.assign(item, nextPatch);
  setDirty();
}

function addItem(payload = "simple_value") {
  if (!state.config || readonlyProfile.value) return;
  pushUndoSnapshot("add-item");
  const inputType =
    typeof payload === "string" ? payload : String(payload?.type || "simple_value");
  const inputMonitor =
    typeof payload === "string" ? "" : String(payload?.monitor || "");
  state.config.items.push(createDefaultItem(String(inputType || "simple_value"), inputMonitor));
  mergeConfigMonitors(state.config);
  state.selectedIndex = state.config.items.length - 1;
  setDirty();
}

function cloneItem() {
  if (!state.config || readonlyProfile.value || !currentItem.value) return;
  pushUndoSnapshot("clone-item");
  const item = deepClone(currentItem.value);
  item.id = createItemId();
  item.x = Number(item.x || 0) + 8;
  item.y = Number(item.y || 0) + 8;
  state.config.items.push(item);
  state.selectedIndex = state.config.items.length - 1;
  setDirty();
}

function removeItem() {
  if (!state.config || readonlyProfile.value || state.selectedIndex < 0) return;
  pushUndoSnapshot("remove-item");
  state.config.items.splice(state.selectedIndex, 1);
  if (state.selectedIndex >= state.config.items.length) {
    state.selectedIndex = state.config.items.length - 1;
  }
  setDirty();
}

function moveItem(step) {
  if (!state.config || readonlyProfile.value || state.selectedIndex < 0) return;
  pushUndoSnapshot("move-item");
  const from = state.selectedIndex;
  const to = from + step;
  if (to < 0 || to >= state.config.items.length) return;
  const tmp = state.config.items[to];
  state.config.items[to] = state.config.items[from];
  state.config.items[from] = tmp;
  state.selectedIndex = to;
  setDirty();
}

function addCustom() {
  if (!state.config || readonlyProfile.value) return;
  pushUndoSnapshot("add-custom");
  state.config.custom_monitors.push({
    name: "",
    label: "",
    type: "file",
    unit: "",
    path: "",
    source: "",
    sources: [],
    aggregate: "max",
  });
  setDirty();
}

function removeCustom(index) {
  if (!state.config || readonlyProfile.value) return;
  pushUndoSnapshot("remove-custom");
  state.config.custom_monitors.splice(index, 1);
  setDirty();
}

function changeCustom({ index, field, value }) {
  if (!state.config || readonlyProfile.value || !state.config.custom_monitors[index]) return;
  pushUndoSnapshot(`change-custom:${field}`);
  state.config.custom_monitors[index][field] = value;
  if (field === "name") mergeMonitorNames([value]);
  setDirty();
}

async function switchProfile() {
  if (!state.editingProfile) return;
  if (state.dirty) {
    setError("当前配置有未保存改动，请先保存");
    return;
  }
  await activateProfile(state.editingProfile);
}

async function saveConfig() {
  if (!state.config || readonlyProfile.value) return;
  if (state.profileLoading) {
    setError("配置切换中，请等待加载完成后再保存");
    return;
  }
  const targetProfile = String(state.loadedProfile || "").trim();
  if (!targetProfile) {
    setError("当前未加载有效配置，请重新选择配置后再保存");
    return;
  }
  if (targetProfile !== String(state.editingProfile || "").trim()) {
    setError("当前选择的配置尚未加载完成，请重新切换并确认内容后再保存");
    return;
  }
  const missingMonitorIndex = state.config.items.findIndex(
    (item) =>
      isMonitorRequiredType(String(item.type || "")) &&
      !String(item.monitor || "").trim(),
  );
  if (missingMonitorIndex >= 0) {
    setError(`第 ${missingMonitorIndex + 1} 个元素缺少监控项，请先选择监控项后再保存`);
    return;
  }
  state.saving = true;
  try {
    const payload = { config: deepClone(state.config) };
    const res = await api(`/api/profiles/${encodeURIComponent(targetProfile)}`, {
      method: "PUT",
      body: JSON.stringify(payload),
    });
    if (Array.isArray(res?.items)) state.profiles = res.items;
    if (res?.active) state.meta = { ...(state.meta || {}), active_profile: res.active };
    markCommittedFromCurrent();
    await syncPreview();
    setError("");
  } catch (err) {
    setError(err.message);
  } finally {
    state.saving = false;
  }
}

async function refreshProfiles() {
  try {
    const res = await api("/api/profiles");
    state.profiles = res.items || [];
    state.meta = { ...(state.meta || {}), active_profile: res.active || state.meta?.active_profile };
    if (!state.editingProfile) state.editingProfile = res.active || state.profiles[0]?.name || "default";
  } catch (err) {
    setError(err.message);
  }
}

async function createProfile() {
  if (!state.config || profileDialog.submitting) return;
  const name = String(profileDialog.value || "").trim();
  if (!name) {
    profileDialog.error = "请输入配置名称";
    return;
  }
  if (!PROFILE_NAME_RE.test(name)) {
    profileDialog.error = "名称仅允许 a-zA-Z0-9._-";
    return;
  }
  profileDialog.error = "";
  profileDialog.submitting = true;
  try {
    const nextConfig = applyAutoThresholdGroupsToConfig(deepClone(state.config), {
      snapshot: state.snapshot,
      monitorOptions: monitorOptions.value,
    });
    const res = await api("/api/profiles", {
      method: "POST",
      body: JSON.stringify({
        name,
        config: nextConfig,
        switch: false,
      }),
    });
    state.profiles = res.items || state.profiles;
    state.config = normalizeConfig(nextConfig, state.meta?.style_keys || [], state.meta?.item_types || []);
    state.editingProfile = name;
    state.loadedProfile = name;
    mergeConfigMonitors(state.config);
    state.selectedIndex = state.config.items.length > 0 ? 0 : -1;
    markCommittedFromCurrent();
    profileDialog.show = false;
    setError("");
  } catch (err) {
    profileDialog.error = err.message;
  } finally {
    profileDialog.submitting = false;
  }
}

async function renameProfile() {
  if (readonlyProfile.value || profileDialog.submitting) return;
  const oldName = state.editingProfile;
  if (!oldName) return;
  const newName = String(profileDialog.value || "").trim();
  if (!newName) {
    profileDialog.error = "请输入新的配置名称";
    return;
  }
  if (!PROFILE_NAME_RE.test(newName)) {
    profileDialog.error = "名称仅允许 a-zA-Z0-9._-";
    return;
  }
  if (!newName || newName === oldName) return;
  profileDialog.error = "";
  profileDialog.submitting = true;
  try {
    const res = await api("/api/profiles/rename", {
      method: "POST",
      body: JSON.stringify({ old_name: oldName, new_name: newName }),
    });
    state.profiles = res.items || state.profiles;
    if (res.active) state.meta = { ...(state.meta || {}), active_profile: res.active };
    state.editingProfile = newName;
    if (state.loadedProfile === oldName) {
      state.loadedProfile = newName;
    }
    if (res.config) {
      state.config = normalizeConfig(res.config, state.meta?.style_keys || [], state.meta?.item_types || []);
      mergeConfigMonitors(state.config);
      await refreshMonitorCatalog();
    }
    markCommittedFromCurrent();
    profileDialog.show = false;
    setError("");
  } catch (err) {
    profileDialog.error = err.message;
  } finally {
    profileDialog.submitting = false;
  }
}

async function deleteProfile() {
  if (readonlyProfile.value) return;
  const name = state.editingProfile;
  if (!name) return;
  try {
    const res = await api(`/api/profiles/${encodeURIComponent(name)}`, { method: "DELETE" });
    state.profiles = res.items || [];
    state.meta = { ...(state.meta || {}), active_profile: res.active || "" };
    state.editingProfile = state.meta.active_profile || state.profiles[0]?.name || "default";
    await loadProfileIntoEditor(state.editingProfile, { force: true, syncPreviewAfter: false });
    await refreshMonitorCatalog();
    await syncPreview();
    setError("");
  } catch (err) {
    setError(err.message);
  }
}

function openCreateProfile() {
  if (!state.config) return;
  profileDialog.mode = "create";
  profileDialog.value = "new_profile";
  profileDialog.error = "";
  profileDialog.submitting = false;
  profileDialog.show = true;
}

function openRenameProfile() {
  if (!state.editingProfile || readonlyProfile.value) return;
  profileDialog.mode = "rename";
  profileDialog.value = state.editingProfile;
  profileDialog.error = "";
  profileDialog.submitting = false;
  profileDialog.show = true;
}

async function submitProfileDialog() {
  if (profileDialog.mode === "rename") {
    await renameProfile();
    return;
  }
  await createProfile();
}

function closeProfileDialog() {
  if (profileDialog.submitting) return;
  profileDialog.show = false;
  profileDialog.error = "";
}

async function setEditingProfile(name) {
  const nextProfile = String(name || "").trim();
  if (!nextProfile || nextProfile === state.editingProfile) return;
  if (state.dirty) {
    setError("当前配置有未保存改动，请先保存");
    return;
  }
  await loadProfileIntoEditor(nextProfile, { syncPreviewAfter: true });
}

function beginProfileSwitch(nextProfile) {
  state.profileLoading = true;
  profileSwitch.pendingProfile = nextProfile;
}

function finishProfileSwitch() {
  state.profileLoading = false;
  profileSwitch.pendingProfile = "";
}

async function loadProfileIntoEditor(profileName, options = {}) {
  const nextProfile = String(profileName || "").trim();
  if (!nextProfile) return false;
  const previousEditing = state.editingProfile;
  const previousLoaded = state.loadedProfile;
  beginProfileSwitch(nextProfile);
  try {
    const loaded = await api(`/api/profiles/${encodeURIComponent(nextProfile)}`);
    state.editingProfile = nextProfile;
    state.loadedProfile = nextProfile;
    state.config = normalizeConfig(loaded.config, state.meta?.style_keys || [], state.meta?.item_types || []);
    mergeConfigMonitors(state.config);
    if (state.config.items.length <= 0) {
      state.selectedIndex = -1;
    } else if (state.selectedIndex < 0 || state.selectedIndex >= state.config.items.length || options.force) {
      state.selectedIndex = 0;
    }
    markCommittedFromCurrent()
    await refreshMonitorCatalog();
    if (options.syncPreviewAfter) {
      await syncPreview(true);
    }
    setError("");
    return true;
  } catch (err) {
    state.editingProfile = previousEditing;
    state.loadedProfile = previousLoaded;
    setError(err.message);
    return false;
  } finally {
    finishProfileSwitch();
  }
}

async function activateProfile(profileName) {
  const nextProfile = String(profileName || "").trim();
  if (!nextProfile) return false;
  const previousEditing = state.editingProfile;
  const previousLoaded = state.loadedProfile;
  beginProfileSwitch(nextProfile);
  try {
    const res = await api("/api/profiles/switch", {
      method: "POST",
      body: JSON.stringify({ name: nextProfile }),
    });
    state.meta = { ...(state.meta || {}), active_profile: res.active || nextProfile };
    state.profiles = Array.isArray(res.items) ? res.items : state.profiles;
    state.editingProfile = nextProfile;
    state.loadedProfile = nextProfile;
    state.config = normalizeConfig(res.config, state.meta?.style_keys || [], state.meta?.item_types || []);
    mergeConfigMonitors(state.config);
    state.selectedIndex = state.config.items.length > 0 ? 0 : -1;
    markCommittedFromCurrent();
    await refreshMonitorCatalog();
    await syncPreview();
    setError("");
    return true;
  } catch (err) {
    state.editingProfile = previousEditing;
    state.loadedProfile = previousLoaded;
    setError(err.message);
    return false;
  } finally {
    finishProfileSwitch();
  }
}

function switchTab(tab) {
  state.activeTab = tab;
  if (tab === "collection") requestRuntime().catch(() => {});
  if (tab === "elements" || tab === "basic" || tab === "other" || tab === "type-defaults") {
    refreshMonitorCatalog().catch(() => {});
  }
}

watch(readonlyProfile, (readonly) => {
  if (readonly && (state.activeTab === "basic" || state.activeTab === "other" || state.activeTab === "type-defaults")) {
    state.activeTab = "elements";
  }
});

watch(
  () => state.activeTab,
  (tab) => {
    if (tab === "collection") requestRuntime().catch(() => {});
  },
);

onMounted(async () => {
  await loadInitial();
  connectWS();
  await requestRuntime().catch(() => {});
  if (monitorCatalog.value.length === 0) {
    await refreshMonitorCatalog().catch(() => {});
  }
});

onBeforeUnmount(() => {
  if (previewSyncTimer) {
    window.clearTimeout(previewSyncTimer);
    previewSyncTimer = null;
  }
  if (runtime.reconnectTimer) window.clearTimeout(runtime.reconnectTimer);
  clearPending("shutdown");
  if (runtime.ws) {
    runtime.ws.close();
    runtime.ws = null;
  }
});
</script>

<template>
  <n-config-provider :theme="darkTheme" :theme-overrides="theme_overrides">
    <main class="app_root">
      <TopBar
        :profiles="state.profiles"
        :active-profile="activeProfile"
        :editing-profile="state.editingProfile"
        :loaded-profile="state.loadedProfile"
        :readonly-profile="readonlyProfile"
        :dirty="state.dirty"
        :saving="state.saving"
        :profile-loading="state.profileLoading"
        :can-undo="canUndo"
        @update:editing-profile="setEditingProfile"
        @switch-profile="switchProfile"
        @save="saveConfig"
        @undo="undoLastChange"
        @restore="restoreUnsavedChanges"
        @create-profile="openCreateProfile"
        @rename-profile="openRenameProfile"
        @delete-profile="deleteProfile"
        @import-config="triggerImportConfig"
        @export-config="exportConfig"
      />
      <input
        ref="importInputRef"
        type="file"
        accept=".json,application/json"
        style="display: none"
        @change="onImportFileChange"
      />

      <n-alert v-if="state.error" class="global_alert" type="error" :show-icon="false">
        {{ state.error }}
      </n-alert>

      <section class="app_body">
        <div v-if="state.loading" class="loading_wrap">
          <n-spin size="small" />
          <n-text depth="3">加载中...</n-text>
        </div>

        <div v-if="state.profileLoading" class="profile_loading_overlay">
          <n-spin size="small" />
          <n-text depth="3">
            正在切换配置{{ profileSwitch.pendingProfile ? `：${profileSwitch.pendingProfile}` : "" }}
          </n-text>
        </div>

        <template v-else-if="state.config && state.meta">
          <nav class="tab_bar">
            <n-space size="small">
              <n-button
                v-for="tab in visibleTabs"
                :key="tab.key"
                size="small"
                :type="state.activeTab === tab.key ? 'primary' : 'default'"
                :secondary="state.activeTab !== tab.key"
                @click="switchTab(tab.key)"
              >
                {{ tab.label }}
              </n-button>
            </n-space>
          </nav>

          <section class="tab_content">
            <BasicTab
              v-if="state.activeTab === 'basic' && !readonlyProfile"
              :config="state.config"
              :meta="state.meta"
              :collectors="state.collectors"
              :snapshot="state.snapshot"
              :readonly-profile="readonlyProfile"
              @change="onBasicChange"
            />

            <OtherTab
              v-else-if="state.activeTab === 'other' && !readonlyProfile"
              :config="state.config"
              :meta="state.meta"
              :snapshot="state.snapshot"
              :monitor-options="monitorOptions"
              :readonly-profile="readonlyProfile"
              @change="onBasicChange"
              @add-custom="addCustom"
              @remove-custom="removeCustom"
              @change-custom="changeCustom"
              @refresh-monitors="refreshMonitorCatalog"
            />

            <ElementsTab
              v-else-if="state.activeTab === 'elements'"
              :config="state.config"
              :meta="state.meta"
              :selected-index="state.selectedIndex"
              :readonly-profile="readonlyProfile"
              :monitor-options="monitorOptions"
              :preview-url="state.previewUrl"
              :preview-sync="state.previewSync"
              :zoom-auto="state.zoomAuto"
              :zoom="state.zoom"
              @select-item="(i) => (state.selectedIndex = i)"
              @add-item="addItem"
              @refresh-monitors="refreshMonitorCatalog"
              @change-preview-sync="setPreviewSync"
              @clone-item="cloneItem"
              @remove-item="removeItem"
              @move-item-up="moveItem(-1)"
              @move-item-down="moveItem(1)"
              @patch-item="onItemPatch"
              @change-item-field="onItemFieldChange"
              @change-zoom-auto="(v) => (state.zoomAuto = !!v)"
              @change-zoom="(v) => (state.zoom = Number(v || 100))"
            />

            <TypeDefaultsTab
              v-else-if="state.activeTab === 'type-defaults' && !readonlyProfile"
              :config="state.config"
              :meta="state.meta"
              :readonly-profile="readonlyProfile"
              @change="onBasicChange"
            />

            <RuntimeTab v-else :snapshot="state.snapshot" />
          </section>
        </template>
      </section>

      <n-modal
        v-model:show="profileDialog.show"
        preset="card"
        :title="profileDialog.mode === 'rename' ? '重命名配置' : '新建配置'"
        style="width: 420px"
        :closable="!profileDialog.submitting"
        :mask-closable="!profileDialog.submitting"
      >
        <n-form label-placement="top" size="small">
          <n-form-item label="配置名称" :validation-status="profileDialog.error ? 'error' : undefined">
            <DeferredInput
              v-model:value="profileDialog.value"
              placeholder="仅支持 a-zA-Z0-9._-"
              :disabled="profileDialog.submitting"
              @keyup.enter="submitProfileDialog"
            />
          </n-form-item>
          <n-text v-if="profileDialog.error" type="error" style="font-size: 12px">
            {{ profileDialog.error }}
          </n-text>
        </n-form>
        <template #footer>
          <n-space justify="end" size="small">
            <n-button size="small" :disabled="profileDialog.submitting" @click="closeProfileDialog">
              取消
            </n-button>
            <n-button
              size="small"
              type="primary"
              :loading="profileDialog.submitting"
              @click="submitProfileDialog"
            >
              确认
            </n-button>
          </n-space>
        </template>
      </n-modal>
    </main>
  </n-config-provider>
</template>
