// The device ids of the area are the driver's devices: the device dimension of web/AREA.md and devices.ts
// name the same ids with the same labels, in the same order, the extra ones as extra.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { parse } from "yaml";
import { DEVICES } from "../src/devices";

interface Dimension {
  values: Record<string, string>;
  extra?: Record<string, string>;
  default?: unknown;
}

/** .socrates/tests/web/AREA.md, from the tool's folder .socrates/tests/tools/browserstack/. */
const AREA = new URL("../../../web/AREA.md", import.meta.url);

function frontMatter(): { dimensions: Record<string, Dimension>; tools: string[] } {
  const text = readFileSync(AREA, "utf8");
  const match = /^---\n([\s\S]*?)\n---\n/.exec(text);
  assert.ok(match, "AREA.md starts with front matter");
  return parse(match[1]!) as { dimensions: Record<string, Dimension>; tools: string[] };
}

test("the area uses this tool", () => {
  assert.ok(frontMatter().tools.includes("browserstack"));
});

test("every device of AREA.md is in devices.ts, with its label, in order", () => {
  const { device } = frontMatter().dimensions;
  assert.ok(device);
  const matrix = DEVICES.filter((entry) => !entry.extra);
  const extra = DEVICES.filter((entry) => entry.extra);
  assert.deepEqual(Object.entries(device.values), matrix.map((entry) => [entry.id, entry.label]));
  assert.deepEqual(Object.entries(device.extra ?? {}), extra.map((entry) => [entry.id, entry.label]));
});

test("every device of devices.ts is in AREA.md", () => {
  const { device } = frontMatter().dimensions;
  const ids = new Set([...Object.keys(device?.values ?? {}), ...Object.keys(device?.extra ?? {})]);
  for (const entry of DEVICES) assert.ok(ids.has(entry.id), `${entry.id} is missing from AREA.md`);
});

test("the car screen stand-in starts in landscape", () => {
  const tesla = DEVICES.find((entry) => entry.id === "tesla");
  assert.equal(tesla?.kind, "android");
  assert.equal(tesla?.options.deviceOrientation, "landscape");
});
