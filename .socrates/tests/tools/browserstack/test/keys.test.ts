import assert from "node:assert/strict";
import { test } from "node:test";
import { KEYS, keyActions, parseKey } from "../src/keys";

const CTRL = "";
const SHIFT = "";
const META = "";

test("plain keys press alone", () => {
  assert.deepEqual(parseKey("Enter"), { name: "enter", modifiers: [], key: KEYS.enter });
  assert.deepEqual(keyActions(parseKey("tab")), [
    { type: "keyDown", value: KEYS.tab },
    { type: "keyUp", value: KEYS.tab },
  ]);
});

test("combinations hold their modifier around the key", () => {
  assert.deepEqual(parseKey("Ctrl+C"), { name: "ctrl+c", modifiers: [CTRL], key: "c" });
  assert.deepEqual(parseKey("ctrl+enter"), { name: "ctrl+enter", modifiers: [CTRL], key: KEYS.enter });
  assert.deepEqual(parseKey("meta+enter").modifiers, [META]);
  assert.deepEqual(parseKey("shift+enter").modifiers, [SHIFT]);
  assert.deepEqual(keyActions(parseKey("shift+tab")), [
    { type: "keyDown", value: SHIFT },
    { type: "keyDown", value: KEYS.tab },
    { type: "keyUp", value: KEYS.tab },
    { type: "keyUp", value: SHIFT },
  ]);
});

test("anything else is refused", () => {
  for (const spec of ["f5", "ctrl+1", "ctrl+tab", "shift+a", "meta+c", "ctrl+shift+c", "ctrl+", "+c", "alt+enter"]) {
    assert.throws(() => parseKey(spec), /unknown key/, spec);
  }
});
