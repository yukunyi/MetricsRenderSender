<script setup>
import { computed, ref } from "vue";
import DeferredInput from "./deferred_input.vue";
import DeferredInputNumber from "./deferred_input_number.vue";
import {
  buildOutputTypeOptions,
  createDefaultOutputEntry,
  isAX206Type,
  isHttpPushType,
  isTcpPushType,
  OUTPUT_HTTP_AUTH_OPTIONS,
  OUTPUT_HTTP_BODY_MODE_OPTIONS,
  OUTPUT_FORMAT_OPTIONS,
  OUTPUT_HTTP_METHOD_OPTIONS,
  OUTPUT_TCP_FORMAT_OPTIONS,
} from "../output_configs";

const props = defineProps({
  config: { type: Object, required: true },
  meta: { type: Object, required: true },
  collectors: { type: Array, default: () => [] },
  snapshot: { type: Object, default: null },
  readonlyProfile: { type: Boolean, default: false },
});

const emit = defineEmits(["change"]);

function onField(path, value) {
  emit("change", { path, value });
}

const platform = computed(() => String(props.meta?.platform || "").toLowerCase());

function collectorSupportedOnPlatform(name) {
  const collector = String(name || "").trim().toLowerCase();
  if (collector === "rtss") return platform.value === "windows";
  if (collector === "coolercontrol") return platform.value === "linux";
  if (collector === "librehardwaremonitor") return platform.value === "windows";
  if (collector === "go_native.btrfs_root") {
    return (props.meta.collectors || []).includes("go_native.btrfs_root");
  }
  if (collector === "go_native.zram") {
    return (props.meta.collectors || []).includes("go_native.zram");
  }
  return true;
}

const collectorNames = computed(() => {
  const set = new Set();
  (props.meta.collectors || []).forEach((name) => set.add(String(name)));
  (props.collectors || []).forEach((item) => set.add(String(item.name || "")));
  Object.keys(props.config.collector_config || {}).forEach((name) => set.add(String(name)));
  return [...set]
    .filter((name) => !!name && collectorSupportedOnPlatform(name))
    .sort();
});

const outputTypeOptions = computed(() =>
  buildOutputTypeOptions(props.meta?.output_types, props.config?.outputs),
);
const outputFormatOptions = OUTPUT_FORMAT_OPTIONS;
const outputTCPFormatOptions = OUTPUT_TCP_FORMAT_OPTIONS;
const outputHTTPMethodOptions = OUTPUT_HTTP_METHOD_OPTIONS;
const outputHTTPBodyModeOptions = OUTPUT_HTTP_BODY_MODE_OPTIONS;
const outputHTTPAuthOptions = OUTPUT_HTTP_AUTH_OPTIONS;
const showOutputAdvanced = ref(false);
const outputAdvancedType = ref("");

function collectorEntry(name) {
  if (!props.config.collector_config) return { enabled: false, options: {} };
  return props.config.collector_config[name] || { enabled: false, options: {} };
}

function collectorUrl(name) {
  return collectorEntry(name).options?.url || "";
}

function collectorHasUrl(name) {
  return name === "coolercontrol" || name === "librehardwaremonitor";
}

function collectorHasAuth(name) {
  return name === "coolercontrol" || name === "librehardwaremonitor";
}

function collectorHasAuthUserField(name) {
  return name === "librehardwaremonitor";
}

function collectorFixedAuthUser(name) {
  if (name === "coolercontrol") return "CCAdmin";
  return "";
}

function collectorOption(name, key) {
  return String(collectorEntry(name).options?.[key] || "");
}

function collectorFieldDisabled(name) {
  return props.readonlyProfile || !collectorEntry(name).enabled;
}

function outputEntries() {
  return Array.isArray(props.config.outputs) ? props.config.outputs : [];
}

function updateOutputs(next) {
  onField("outputs", Array.isArray(next) ? next : []);
}

function outputEntryByType(type) {
  return outputEntries().find((item) => String(item?.type || "").trim().toLowerCase() === String(type || "").trim().toLowerCase()) || null;
}

function outputEnabled(type) {
  const entry = outputEntryByType(type);
  if (!entry) return false;
  return entry.enabled !== false;
}

function outputFieldDisabled(type) {
  return props.readonlyProfile || !outputEnabled(type);
}

function setOutputEnabled(type, enabled) {
  const next = outputEntries().map((item) => ({ ...(item || {}) }));
  const normalized = String(type || "").trim().toLowerCase();
  const index = next.findIndex((item) => String(item?.type || "").trim().toLowerCase() === normalized);
  if (enabled) {
    if (index >= 0) {
      next[index] = { ...(next[index] || {}), enabled: true };
    } else {
      next.push(createDefaultOutputEntry(normalized));
    }
  } else if (index >= 0) {
    next[index] = { ...(next[index] || {}), enabled: false };
  } else {
    next.push({ ...createDefaultOutputEntry(normalized), enabled: false });
  }
  updateOutputs(next);
}

function patchOutputByType(type, patch) {
  const next = outputEntries().map((item) => ({ ...(item || {}) }));
  const normalized = String(type || "").trim().toLowerCase();
  const index = next.findIndex((item) => String(item?.type || "").trim().toLowerCase() === normalized);
  if (index >= 0) {
    next[index] = { ...(next[index] || {}), ...(patch || {}) };
  } else {
    next.push({ ...createDefaultOutputEntry(normalized), ...(patch || {}) });
  }
  updateOutputs(next);
}

function outputTypeLabel(type) {
  if (isAX206Type(type)) return "AX206 USB";
  if (isHttpPushType(type)) return "HTTP Push";
  if (isTcpPushType(type)) return "TCP Push";
  return String(type || "");
}

function outputEntryValue(type, key, fallback = "") {
  const entry = outputEntryByType(type);
  if (!entry || !(key in entry)) return fallback;
  return entry[key] ?? fallback;
}

function outputSupportsAdvanced(type) {
  return isHttpPushType(type) || isTcpPushType(type);
}

function outputUsesQuality(type) {
  if (isHttpPushType(type)) return true;
  if (isTcpPushType(type)) return String(outputEntryValue(type, "format", "jpeg")) === "jpeg";
  return false;
}

function tcpPushStatusEntry(type) {
  const stats = props.snapshot?.monitor_runtime?.tcp_push_stats;
  if (!stats || typeof stats !== "object") return null;
  return stats[String(type || "").trim().toLowerCase()] || null;
}

function tcpPushBusyWait(entry) {
  return !!(entry?.busy_wait || entry?.probe_mode);
}

function tcpPushStatusTagType(type) {
  const entry = tcpPushStatusEntry(type);
  if (!entry) return "default";
  if (entry.can_send) return "success";
  if (tcpPushBusyWait(entry)) return "warning";
  if (entry.connected) return "info";
  return "error";
}

function tcpPushStatusLabel(type) {
  const entry = tcpPushStatusEntry(type);
  if (!entry) return "无状态";
  if (entry.can_send) return "可发送";
  if (tcpPushBusyWait(entry)) return "忙等待";
  if (entry.connected) return "已连接";
  return "未连接";
}

function tcpPushStatusSummary(type) {
  const entry = tcpPushStatusEntry(type);
  if (!entry) return "尚未收到运行状态";
  const parts = [];
  if (entry.reason) parts.push(`原因: ${entry.reason}`);
  if (entry.lower_priority_mode) parts.push(`策略: ${entry.lower_priority_mode}`);
  if (entry.active_user) parts.push(`活跃用户: ${entry.active_user}`);
  if (entry.last_stage) parts.push(`阶段: ${entry.last_stage}`);
  if (entry.last_status_code) parts.push(`ACK: ${entry.last_status_code}`);
  if (entry.updated_at) parts.push(`更新: ${entry.updated_at}`);
  return parts.length > 0 ? parts.join(" | ") : "已连接，等待设备反馈";
}

function openOutputAdvanced(type) {
  outputAdvancedType.value = String(type || "");
  showOutputAdvanced.value = true;
}

function closeOutputAdvanced() {
  showOutputAdvanced.value = false;
}

const outputAdvancedTitle = computed(() => outputTypeLabel(outputAdvancedType.value));

function parseKeyValueLines(value, separator = ":") {
  return String(value || "")
    .split("\n")
    .map((line) => String(line || "").trim())
    .filter((line) => !!line)
    .map((line) => {
      const splitAt = line.indexOf(separator);
      if (splitAt < 0) {
        return { key: line.trim(), value: "" };
      }
      return {
        key: line.slice(0, splitAt).trim(),
        value: line.slice(splitAt + separator.length).trim(),
      };
    })
    .filter((item) => item.key);
}

function formatKeyValueLines(items, separator = ": ") {
  if (!Array.isArray(items) || items.length === 0) return "";
  return items
    .map((item) => {
      const key = String(item?.key || "").trim();
      const value = String(item?.value || "").trim();
      if (!key) return "";
      return value ? `${key}${separator}${value}` : key;
    })
    .filter((line) => !!line)
    .join("\n");
}

function parseSuccessCodes(value) {
  const seen = new Set();
  return String(value || "")
    .split(",")
    .map((part) => Number(String(part || "").trim()))
    .filter((code) => Number.isFinite(code))
    .map((code) => Math.round(code))
    .filter((code) => code >= 100 && code <= 599)
    .sort((left, right) => left - right)
    .filter((code) => {
      if (seen.has(code)) return false;
      seen.add(code);
      return true;
    });
}

function formatSuccessCodes(codes) {
  if (!Array.isArray(codes) || codes.length === 0) return "";
  return codes.join(", ");
}

</script>

<template>
  <section class="layout_single basic_tab">
    <div class="basic_inner">
      <n-grid cols="1" :x-gap="8" :y-gap="6">
        <n-grid-item>
          <n-card title="画布配置" size="small">
            <n-form label-placement="left" :label-width="112" size="small">
              <n-grid cols="1 s:2" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="宽度">
                  <DeferredInputNumber
                    :value="config.width"
                    :disabled="readonlyProfile"
                    :show-button="false"
                    @update:value="(v) => onField('width', Number(v || 0))"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="高度">
                  <DeferredInputNumber
                    :value="config.height"
                    :disabled="readonlyProfile"
                    :show-button="false"
                    @update:value="(v) => onField('height', Number(v || 0))"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="内边距">
                  <DeferredInputNumber
                    :value="config.layout_padding"
                    :disabled="readonlyProfile"
                    :show-button="false"
                    @update:value="(v) => onField('layout_padding', Number(v || 0))"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="刷新间隔(ms)">
                  <DeferredInputNumber
                    :value="config.refresh_interval"
                    :disabled="readonlyProfile"
                    :show-button="false"
                    @update:value="(v) => onField('refresh_interval', Number(v || 0))"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="采集告警阈值(ms)">
                  <DeferredInputNumber
                    :value="config.collect_warn_ms"
                    :disabled="readonlyProfile"
                    :show-button="false"
                    @update:value="(v) => onField('collect_warn_ms', Number(v || 0))"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="绘制等待上限(ms)">
                  <DeferredInputNumber
                    :value="config.render_wait_max_ms"
                    :disabled="readonlyProfile"
                    :show-button="false"
                    @update:value="(v) => onField('render_wait_max_ms', Number(v || 0))"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="允许元素样式定制">
                  <n-switch
                    :value="config.allow_custom_style === true"
                    :disabled="readonlyProfile"
                    size="small"
                    @update:value="(v) => onField('allow_custom_style', !!v)"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="输出配置" :span="2">
                  <n-table class="collector_table output_table" size="small" striped>
                    <thead>
                      <tr>
                        <th style="width: 30%">输出类型</th>
                        <th style="width: 12%">启用</th>
                        <th>参数</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="option in outputTypeOptions" :key="option.value">
                        <td>{{ outputTypeLabel(option.value) }}</td>
                        <td>
                          <n-switch
                            :value="outputEnabled(option.value)"
                            :disabled="readonlyProfile"
                            size="small"
                            @update:value="(v) => setOutputEnabled(option.value, !!v)"
                          />
                        </td>
                        <td>
                          <template v-if="isAX206Type(option.value)">
                            <div class="output_basic_grid output_basic_grid_ax206">
                              <div class="output_basic_cell">
                                <n-text depth="3">重连(ms)</n-text>
                                <DeferredInputNumber
                                  :value="Number(outputEntryByType(option.value)?.reconnect_ms || 3000)"
                                  :disabled="outputFieldDisabled(option.value)"
                                  size="small"
                                  :show-button="false"
                                  @update:value="(v) => patchOutputByType(option.value, { reconnect_ms: Number(v || 3000) })"
                                />
                              </div>
                            </div>
                          </template>
                          <template v-else-if="isHttpPushType(option.value)">
                            <div class="output_basic_grid">
                              <div class="output_basic_cell output_basic_cell_url">
                                <n-text depth="3">地址</n-text>
                                <DeferredInput
                                  :value="outputEntryValue(option.value, 'url', '')"
                                  :disabled="outputFieldDisabled(option.value)"
                                  size="small"
                                  placeholder="http://127.0.0.1:18090/push"
                                  @update:value="(v) => patchOutputByType(option.value, { url: String(v || '') })"
                                />
                              </div>
                              <div class="output_basic_cell">
                                <n-text depth="3">格式</n-text>
                                <n-select
                                  :value="outputEntryValue(option.value, 'format', 'jpeg')"
                                  :disabled="outputFieldDisabled(option.value)"
                                  size="small"
                                  :options="outputFormatOptions"
                                  @update:value="(v) => patchOutputByType(option.value, { format: String(v || 'jpeg') })"
                                />
                              </div>
                              <div class="output_basic_cell">
                                <n-text depth="3">质量</n-text>
                                <DeferredInputNumber
                                  :value="Number(outputEntryValue(option.value, 'quality', 80))"
                                  :disabled="outputFieldDisabled(option.value)"
                                  size="small"
                                  :show-button="false"
                                  @update:value="(v) => patchOutputByType(option.value, { quality: Number(v || 80) })"
                                />
                              </div>
                              <div class="output_basic_cell output_basic_cell_action">
                                <n-text depth="3">更多</n-text>
                                <n-button
                                  v-if="outputSupportsAdvanced(option.value)"
                                  size="small"
                                  secondary
                                  :disabled="readonlyProfile"
                                  @click="openOutputAdvanced(option.value)"
                                >
                                  高级
                                </n-button>
                              </div>
                            </div>
                          </template>
                          <template v-else-if="isTcpPushType(option.value)">
                            <div class="output_basic_grid">
                              <div class="output_basic_cell output_basic_cell_url">
                                <n-text depth="3">地址</n-text>
                                <DeferredInput
                                  :value="outputEntryValue(option.value, 'url', '')"
                                  :disabled="outputFieldDisabled(option.value)"
                                  size="small"
                                  placeholder="tcp://127.0.0.1:9100"
                                  @update:value="(v) => patchOutputByType(option.value, { url: String(v || '') })"
                                />
                              </div>
                              <div class="output_basic_cell">
                                <n-text depth="3">格式</n-text>
                                <n-select
                                  :value="outputEntryValue(option.value, 'format', 'jpeg')"
                                  :disabled="outputFieldDisabled(option.value)"
                                  size="small"
                                  :options="outputTCPFormatOptions"
                                  @update:value="(v) => patchOutputByType(option.value, { format: String(v || 'jpeg') })"
                                />
                              </div>
                              <div class="output_basic_cell">
                                <n-text depth="3">质量</n-text>
                                <DeferredInputNumber
                                  :value="Number(outputEntryValue(option.value, 'quality', 80))"
                                  :disabled="outputFieldDisabled(option.value) || !outputUsesQuality(option.value)"
                                  size="small"
                                  :show-button="false"
                                  @update:value="(v) => patchOutputByType(option.value, { quality: Number(v || 80) })"
                                />
                              </div>
                              <div class="output_basic_cell output_basic_cell_action">
                                <n-text depth="3">更多</n-text>
                                <n-button
                                  v-if="outputSupportsAdvanced(option.value)"
                                  size="small"
                                  secondary
                                  :disabled="readonlyProfile"
                                  @click="openOutputAdvanced(option.value)"
                                >
                                  高级
                                </n-button>
                              </div>
                              <div class="output_basic_status">
                                <n-tag size="small" :type="tcpPushStatusTagType(option.value)">
                                  {{ tcpPushStatusLabel(option.value) }}
                                </n-tag>
                                <n-text depth="3">{{ tcpPushStatusSummary(option.value) }}</n-text>
                              </div>
                            </div>
                          </template>
                          <template v-else>-</template>
                        </td>
                      </tr>
                    </tbody>
                  </n-table>
                </n-form-item-gi>
              </n-grid>
            </n-form>
          </n-card>
        </n-grid-item>
      </n-grid>

      <n-card title="采集器开关" size="small" style="margin-top: 8px">
        <n-table class="collector_table" size="small" striped>
          <thead>
            <tr>
              <th style="width: 30%">采集器</th>
              <th style="width: 12%">启用</th>
              <th>URL/参数</th>
            </tr>
          </thead>
                    <tbody>
                      <tr v-for="name in collectorNames" :key="name">
                        <td>{{ name }}</td>
                        <td>
                <n-switch
                  :value="!!collectorEntry(name).enabled"
                  :disabled="readonlyProfile"
                  size="small"
                  @update:value="(v) => onField(['collector_config', name, 'enabled'], !!v)"
                />
              </td>
              <td>
                <template v-if="collectorHasUrl(name)">
                  <n-space vertical size="small" style="width: 100%">
                    <DeferredInput
                      :value="collectorUrl(name)"
                      :disabled="collectorFieldDisabled(name)"
                      size="small"
                      :placeholder="name === 'coolercontrol' ? 'http://127.0.0.1:11987' : 'http://127.0.0.1:8085'"
                      @update:value="(v) => onField(['collector_config', name, 'options', 'url'], String(v || ''))"
                    />
                    <n-space v-if="collectorHasAuth(name)" size="small" :wrap="false">
                      <DeferredInput
                        v-if="collectorHasAuthUserField(name)"
                        :value="collectorOption(name, 'username')"
                        :disabled="collectorFieldDisabled(name)"
                        size="small"
                        placeholder="User"
                        @update:value="(v) => onField(['collector_config', name, 'options', 'username'], String(v || ''))"
                      />
                      <DeferredInput
                        v-else
                        :value="collectorFixedAuthUser(name)"
                        disabled
                        size="small"
                        placeholder="User"
                      />
                      <DeferredInput
                        type="password"
                        show-password-on="click"
                        :value="collectorOption(name, 'password')"
                        :disabled="collectorFieldDisabled(name)"
                        size="small"
                        placeholder="Password"
                        @update:value="(v) => onField(['collector_config', name, 'options', 'password'], String(v || ''))"
                      />
                    </n-space>
                  </n-space>
                </template>
                <template v-else>-</template>
              </td>
            </tr>
          </tbody>
        </n-table>
      </n-card>

      <n-modal
        v-model:show="showOutputAdvanced"
        preset="card"
        :title="`高级配置 · ${outputAdvancedTitle}`"
        class="output_advanced_modal"
        style="width: 920px; max-width: 96vw"
      >
        <template v-if="isHttpPushType(outputAdvancedType)">
          <n-form label-placement="top" size="small" class="output_advanced_form">
            <section class="output_advanced_section">
              <div class="output_advanced_section_title">请求</div>
              <n-grid cols="1 s:2 m:3" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="Method">
                  <n-select
                    :value="outputEntryValue(outputAdvancedType, 'method', 'POST')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :options="outputHTTPMethodOptions"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { method: String(v || 'POST') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Body Mode">
                  <n-select
                    :value="outputEntryValue(outputAdvancedType, 'body_mode', 'binary')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :options="outputHTTPBodyModeOptions"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { body_mode: String(v || 'binary') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Timeout MS">
                  <DeferredInputNumber
                    :value="Number(outputEntryValue(outputAdvancedType, 'timeout_ms', 5000))"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :show-button="false"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { timeout_ms: Number(v || 5000) })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Success Codes">
                  <DeferredInput
                    :value="formatSuccessCodes(outputEntryValue(outputAdvancedType, 'success_codes', []))"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { success_codes: parseSuccessCodes(v) })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Content Type">
                  <DeferredInput
                    :value="outputEntryValue(outputAdvancedType, 'content_type', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { content_type: String(v || '') })"
                  />
                </n-form-item-gi>
              </n-grid>
            </section>
            <section class="output_advanced_section">
              <div class="output_advanced_section_title">认证</div>
              <n-grid cols="1 s:2 m:3" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="Auth Type">
                  <n-select
                    :value="outputEntryValue(outputAdvancedType, 'auth_type', 'none')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :options="outputHTTPAuthOptions"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { auth_type: String(v || 'none') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Auth Username">
                  <DeferredInput
                    :value="outputEntryValue(outputAdvancedType, 'auth_username', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType) || outputEntryValue(outputAdvancedType, 'auth_type', 'none') !== 'basic'"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { auth_username: String(v || '') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Auth Password">
                  <DeferredInput
                    type="password"
                    show-password-on="click"
                    :value="outputEntryValue(outputAdvancedType, 'auth_password', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType) || outputEntryValue(outputAdvancedType, 'auth_type', 'none') !== 'basic'"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { auth_password: String(v || '') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi
                  v-if="outputEntryValue(outputAdvancedType, 'auth_type', 'none') === 'bearer'"
                  label="Bearer Token"
                >
                  <DeferredInput
                    type="password"
                    show-password-on="click"
                    :value="outputEntryValue(outputAdvancedType, 'auth_token', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { auth_token: String(v || '') })"
                  />
                </n-form-item-gi>
              </n-grid>
            </section>
            <section class="output_advanced_section">
              <div class="output_advanced_section_title">附加数据</div>
              <n-grid cols="1 s:2 m:4" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="File Field">
                  <DeferredInput
                    :value="outputEntryValue(outputAdvancedType, 'file_field', 'file')"
                    :disabled="outputFieldDisabled(outputAdvancedType) || outputEntryValue(outputAdvancedType, 'body_mode', 'binary') !== 'multipart'"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { file_field: String(v || 'file') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="File Name">
                  <DeferredInput
                    :value="outputEntryValue(outputAdvancedType, 'file_name', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType) || outputEntryValue(outputAdvancedType, 'body_mode', 'binary') !== 'multipart'"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { file_name: String(v || '') })"
                  />
                </n-form-item-gi>
              </n-grid>
              <n-grid cols="1 s:2" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="Headers">
                  <DeferredInput
                    type="textarea"
                    size="small"
                    :autosize="{ minRows: 2, maxRows: 4 }"
                    :value="formatKeyValueLines(outputEntryValue(outputAdvancedType, 'headers', []), ': ')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { headers: parseKeyValueLines(v, ':') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Form Fields">
                  <DeferredInput
                    type="textarea"
                    size="small"
                    :autosize="{ minRows: 2, maxRows: 4 }"
                    :value="formatKeyValueLines(outputEntryValue(outputAdvancedType, 'form_fields', []), '=')"
                    :disabled="outputFieldDisabled(outputAdvancedType) || outputEntryValue(outputAdvancedType, 'body_mode', 'binary') !== 'multipart'"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { form_fields: parseKeyValueLines(v, '=') })"
                  />
                </n-form-item-gi>
              </n-grid>
            </section>
          </n-form>
        </template>
        <template v-else-if="isTcpPushType(outputAdvancedType)">
          <n-form label-placement="top" size="small" class="output_advanced_form">
            <section class="output_advanced_section">
              <div class="output_advanced_section_title">连接</div>
              <n-grid cols="1 s:2 m:3" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="Timeout MS">
                  <DeferredInputNumber
                    :value="Number(outputEntryValue(outputAdvancedType, 'timeout_ms', 5000))"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :show-button="false"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { timeout_ms: Number(v || 5000) })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Idle Timeout Sec">
                  <DeferredInputNumber
                    :value="Number(outputEntryValue(outputAdvancedType, 'idle_timeout_sec', 120))"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :show-button="false"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { idle_timeout_sec: Number(v || 120) })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Busy Check MS">
                  <DeferredInputNumber
                    :value="Number(outputEntryValue(outputAdvancedType, 'busy_check_ms', 1000))"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    :show-button="false"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { busy_check_ms: Number(v || 1000) })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="Success Codes">
                  <DeferredInput
                    :value="formatSuccessCodes(outputEntryValue(outputAdvancedType, 'success_codes', []))"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { success_codes: parseSuccessCodes(v) })"
                  />
                </n-form-item-gi>
              </n-grid>
            </section>
            <section class="output_advanced_section">
              <div class="output_advanced_section_title">上传</div>
              <n-grid cols="1 s:2 m:3" responsive="screen" :x-gap="8" :y-gap="2">
                <n-form-item-gi label="TCP Key">
                  <DeferredInput
                    type="password"
                    show-password-on="click"
                    :value="outputEntryValue(outputAdvancedType, 'upload_token', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { upload_token: String(v || '') })"
                  />
                </n-form-item-gi>
                <n-form-item-gi label="File Name">
                  <DeferredInput
                    :value="outputEntryValue(outputAdvancedType, 'file_name', '')"
                    :disabled="outputFieldDisabled(outputAdvancedType)"
                    size="small"
                    @update:value="(v) => patchOutputByType(outputAdvancedType, { file_name: String(v || '') })"
                  />
                </n-form-item-gi>
              </n-grid>
            </section>
          </n-form>
        </template>
        <template #footer>
          <n-space justify="end" size="small">
            <n-button size="small" @click="closeOutputAdvanced">关闭</n-button>
          </n-space>
        </template>
      </n-modal>

    </div>
  </section>
</template>
