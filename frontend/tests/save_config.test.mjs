import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("../src/app.vue", import.meta.url), "utf8");
function extractFunction(name) {
  const start = source.indexOf(`function ${name}(`);
  let index = source.indexOf("{", start) + 1;
  let depth = 1;
  for (; depth; index++) {
    if (source[index] === "{") depth++;
    if (source[index] === "}") depth--;
  }
  return source.slice(start, index);
}

for (const editDuringSave of [false, true]) {
  test(`save preserves submitted baseline; later edits: ${editDuringSave}`, async () => {
    const state = {
      config: { items: [], width: 100 }, dirty: true, saving: false,
      loadedProfile: "test", editingProfile: "test",
    };
    const committedConfig = { value: null };
    const committedConfigJson = { value: "" };
    const undoStack = { value: [{ config: { items: [], width: 50 } }] };
    let finish;
    let persisted;
    const context = {
      state, committedConfig, committedConfigJson,
      readonlyProfile: { value: false },
      deepClone: (value) => JSON.parse(JSON.stringify(value)),
      serializeConfig: JSON.stringify,
      undoStack,
      clearUndoStack: () => { undoStack.value = []; },
      setError: (error) => assert.equal(error, ""),
      syncPreview: async () => {},
      isMonitorRequiredType: () => false,
      api: async (_path, options) => {
        persisted = JSON.parse(options.body).config;
        return new Promise((resolve) => { finish = () => resolve({}); });
      },
    };
    const run = new Function(...Object.keys(context), `return (async ${extractFunction("saveConfig")})();`);
    const saving = run(...Object.values(context));
    if (editDuringSave) {
      const pushUndoSnapshot = new Function(...Object.keys(context), `return (${extractFunction("pushUndoSnapshot")});`)(...Object.values(context));
      pushUndoSnapshot("change-width");
      state.config.width = 200;
    }
    finish();
    await saving;
    assert.deepEqual(committedConfig.value, persisted);
    assert.equal(state.dirty, editDuringSave);
    assert.equal(state.config.width, editDuringSave ? 200 : 100);
    assert.equal(state.saving, false);
    assert.equal(undoStack.value.length, editDuringSave ? 2 : 0);
    if (editDuringSave) assert.equal(undoStack.value.at(-1).config.width, 100);
  });
}


test("failed save preserves committed baseline and undo history", async () => {
  const state = {
    config: { items: [], width: 200 }, dirty: true, saving: false,
    loadedProfile: "test", editingProfile: "test",
  };
  const baseline = { items: [], width: 100 };
  const committedConfig = { value: baseline };
  const committedConfigJson = { value: JSON.stringify(baseline) };
  const undoStack = { value: [{ config: baseline }] };
  const errors = [];
  const context = {
    state, committedConfig, committedConfigJson,
    readonlyProfile: { value: false },
    deepClone: (value) => JSON.parse(JSON.stringify(value)),
    serializeConfig: JSON.stringify,
    clearUndoStack: () => { undoStack.value = []; },
    setError: (error) => errors.push(error),
    syncPreview: async () => assert.fail("failed save must not sync preview"),
    isMonitorRequiredType: () => false,
    api: async () => { throw new Error("write failed"); },
  };
  await new Function(...Object.keys(context), `return (async ${extractFunction("saveConfig")})();`)(...Object.values(context));
  assert.strictEqual(committedConfig.value, baseline);
  assert.equal(committedConfigJson.value, JSON.stringify(baseline));
  assert.equal(undoStack.value.length, 1);
  assert.equal(state.dirty, true);
  assert.equal(state.saving, false);
  assert.deepEqual(errors, ["write failed"]);
});
