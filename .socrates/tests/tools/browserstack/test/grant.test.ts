import assert from "node:assert/strict";
import { test } from "node:test";
import { deviceById, grantCapabilities, parseGrants } from "../src/devices";

test("--grant gives desktop Chrome, Edge and Firefox their permissions in advance", () => {
  const chrome = deviceById("win-chrome");
  assert.deepEqual(parseGrants("mic, notifications", chrome), ["microphone", "notifications"]);
  const caps = grantCapabilities(chrome, ["microphone", "notifications"]) as { "goog:chromeOptions": { prefs: Record<string, number>; args: string[] } };
  assert.equal(caps["goog:chromeOptions"].prefs["profile.default_content_setting_values.media_stream_mic"], 1);
  assert.equal(caps["goog:chromeOptions"].prefs["profile.default_content_setting_values.notifications"], 1);
  assert.ok(caps["goog:chromeOptions"].args.includes("--use-fake-device-for-media-stream"));
  assert.ok("ms:edgeOptions" in grantCapabilities(deviceById("win-edge"), ["clipboard"]));
  const firefox = grantCapabilities(deviceById("win-firefox"), ["notifications"]) as { "moz:firefoxOptions": { prefs: Record<string, unknown> } };
  assert.equal(firefox["moz:firefoxOptions"].prefs["permissions.default.desktop-notification"], 1);
  assert.deepEqual(grantCapabilities(chrome, []), {});
});

test("--grant is refused where it cannot work", () => {
  assert.deepEqual(parseGrants(undefined, deviceById("iphone")), []);
  assert.throws(() => parseGrants("microphone", deviceById("mac-safari")), /Safari/);
  assert.throws(() => parseGrants("microphone", deviceById("pixel")), /qa prompt/);
  assert.throws(() => parseGrants("camera,teleport", deviceById("win-chrome")), /--grant takes/);
});
