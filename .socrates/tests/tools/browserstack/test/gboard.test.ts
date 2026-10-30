import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { TO_LETTERS, TO_SYMBOLS, find, isLetter, keyboardKeys, labelsFor, letterKey } from "../src/gboard";

const fixture = (name: string) => readFileSync(new URL(`fixtures/${name}`, import.meta.url), "utf8");

test("Gboard's letter keys, Shift, Space, Enter and the switch to symbols on a Pixel 9", () => {
  const keys = keyboardKeys(fixture("android-gboard-letters.xml"));
  assert.deepEqual(keys.get("l")?.rect, { x: 914, y: 1778, width: 161, height: 166 });
  for (const label of ["q", "m", "Shift", "Space", "Enter", "Delete", "Symbol keyboard"]) assert.ok(keys.has(label), label);
  assert.equal(find(keys, TO_SYMBOLS)?.label, "Symbol keyboard");
  assert.deepEqual(letterKey(keys, "S")?.shifted, false);
  assert.equal(letterKey(keys, "1"), null);
});

test("the symbol layout names its keys", () => {
  const keys = keyboardKeys(fixture("android-gboard-symbols.xml"));
  assert.ok(keys.has("1") && keys.has("0"));
  assert.equal(find(keys, labelsFor("-"))?.label, "Dash");
  assert.equal(find(keys, labelsFor("/"))?.label, "slash");
  assert.equal(find(keys, labelsFor("_"))?.label, "underline");
  assert.equal(find(keys, labelsFor("&"))?.label, "&");
  assert.equal(find(keys, TO_LETTERS)?.label, "Letter keyboard");
});

test("what counts as a letter", () => {
  assert.ok(isLetter("a") && isLetter("Q") && isLetter("ä"));
  assert.ok(!isLetter("1") && !isLetter("-") && !isLetter(" "));
  assert.deepEqual(labelsFor(" "), ["Space", " "]);
  assert.deepEqual(labelsFor("x"), ["x"]);
});
