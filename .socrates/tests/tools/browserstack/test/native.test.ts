import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { describeElement, iosPrompt, matchNative, middle, nativeOverlay, parseNative } from "../src/native";
import { answerButton } from "../src/prompt";

const fixture = (name: string) => readFileSync(new URL(`fixtures/${name}`, import.meta.url), "utf8");

test("Safari's microphone question on an iPhone is an alert with Cancel and Allow", () => {
  const screen = parseNative(fixture("ios-safari-microphone-alert.xml"), "ios");
  assert.equal(screen.alert, true);
  const prompt = iosPrompt(screen);
  assert.equal(prompt?.kind, "alert");
  assert.deepEqual(prompt?.buttons, ["Cancel", "Allow"]);
  assert.match(prompt!.texts[0]!, /Would Like to Access the Microphone/);
  assert.equal(answerButton(prompt!, "allow"), "Allow");
  assert.equal(answerButton(prompt!, "block"), "Cancel");
  // The page's own controls and Safari's bars are not listed; the alert is.
  const overlay = nativeOverlay(screen, "ios").map(describeElement);
  assert.ok(overlay.includes('Button "Allow"'));
  assert.ok(!overlay.some((line) => /Speak|Address|Page Menu|Tabs/.test(line)), overlay.join("\n"));
});

test("Safari's date picker: the calendar's month, its days and the time wheels, without a label of their own", () => {
  const screen = parseNative(fixture("ios-safari-time-wheels.xml"), "ios");
  const overlay = nativeOverlay(screen, "ios");
  assert.ok(overlay.some((element) => element.type === "DatePicker"));
  assert.deepEqual(
    overlay.filter((element) => element.type === "PickerWheel").map((element) => element.value ?? element.label),
    ["14 o’clock", "18 minutes"],
  );
  const header = overlay.find((element) => element.id === "DatePicker.Show");
  assert.equal(header?.value, "October 2026");
  assert.ok(overlay.some((element) => element.id === "PopoverDismissRegion"));
  // A day's number inside its button adds nothing: only the button is listed.
  assert.ok(!overlay.some((element) => element.type === "StaticText" && element.label === "30"));
  assert.equal(matchNative(overlay, "Next Month").pick?.id, "DatePicker.NextMonth");
});

test("the edit menu of a field is no paste callout; Paste alone is", () => {
  const item = (label: string, x: number) => `<XCUIElementTypeMenuItem type="XCUIElementTypeMenuItem" name="${label}" label="${label}" enabled="true" visible="true" x="${x}" y="400" width="60" height="40"/>`;
  const wrap = (items: string) => `<AppiumAUT><XCUIElementTypeApplication type="XCUIElementTypeApplication" name="Safari" label="Safari" x="0" y="0" width="393" height="852">${items}</XCUIElementTypeApplication></AppiumAUT>`;
  assert.equal(iosPrompt(parseNative(wrap(item("Cut", 10) + item("Copy", 80) + item("Paste", 150)), "ios")), null);
  assert.deepEqual(iosPrompt(parseNative(wrap(item("Paste", 150)), "ios"))?.buttons, ["Paste"]);
});

test("Chrome's date and time dialog on a Pixel: its spinners and buttons, the page left out", () => {
  const overlay = nativeOverlay(parseNative(fixture("android-chrome-date-dialog.xml"), "android"), "android");
  const lines = overlay.map(describeElement);
  assert.ok(lines.includes('TextView "Set date and time" id alertTitle'), lines.join("\n"));
  assert.ok(lines.includes('Button "Set" id button1'));
  assert.ok(lines.includes('EditText "Oct" id numberpicker_input'));
  assert.ok(overlay.some((element) => element.type === "NumberPicker"));
  const set = matchNative(overlay, "set");
  assert.equal(set.pick?.label, "Set");
  assert.deepEqual(middle(set.pick!.rect), { x: 936, y: 1887 });
});

test("Android's file chooser after Attach files", () => {
  const overlay = nativeOverlay(parseNative(fixture("android-chrome-file-chooser.xml"), "android"), "android");
  assert.deepEqual([...new Set(overlay.map((element) => element.label))], ["Camera", "Video", "Media picker"]);
  assert.equal(matchNative(overlay, "media picker").pick?.label, "Media picker");
});
