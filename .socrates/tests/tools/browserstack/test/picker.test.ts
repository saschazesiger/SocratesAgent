import assert from "node:assert/strict";
import { test } from "node:test";
import { dayLabelMatches, formatValue, hour12, monthSteps, parseMonthYear, parseValue, wantedMoment } from "../src/picker";

const now = { year: 2026, month: 12, day: 31, hour: 23, minute: 57, second: 12 };

test("a value is the field's HTML format, or relative to the device's clock rounded up to the minute", () => {
  assert.equal(formatValue(wantedMoment("2026-10-30T14:25", "datetime-local", now), "datetime-local"), "2026-10-30T14:25");
  assert.equal(formatValue(wantedMoment("14:25", "time", now), "time"), "14:25");
  assert.equal(formatValue(wantedMoment("2026-10-30", "date", now), "date"), "2026-10-30");
  // 23:57:12 rounds up to 23:58, five minutes on is past midnight and the new year.
  assert.equal(formatValue(wantedMoment("+5m", "datetime-local", now), "datetime-local"), "2027-01-01T00:03");
  assert.equal(formatValue(wantedMoment("+2h", "time", { ...now, second: 0 }), "time"), "01:57");
  assert.equal(formatValue(wantedMoment("+1d", "date", now), "date"), "2027-01-01");
  for (const bad of ["tomorrow", "2026-02-30T10:00", "25:00", "+5x"]) assert.throws(() => wantedMoment(bad, bad.includes("T") ? "datetime-local" : "time", now), /takes/, bad);
  assert.deepEqual(parseValue("2026-10-30T14:25:30", "datetime-local"), { year: 2026, month: 10, day: 30, hour: 14, minute: 25 });
});

test("the calendar's month and day labels, in the formats iPhone and iPad showed", () => {
  assert.deepEqual(parseMonthYear("October 2026"), { year: 2026, month: 10 });
  assert.equal(monthSteps({ year: 2026, month: 11 }, { year: 2027, month: 1 }), 2);
  assert.equal(monthSteps({ year: 2026, month: 10 }, { year: 2026, month: 9 }), -1);
  assert.ok(dayLabelMatches("Today, Friday, 30 October", 30, 10));
  assert.ok(dayLabelMatches("Today, Friday, October 30", 30, 10));
  assert.ok(dayLabelMatches("Friday, 30. October", 30, 10));
  assert.ok(!dayLabelMatches("Friday, 3 October", 30, 10));
  assert.ok(!dayLabelMatches("Friday, 30. October", 3, 10));
  assert.ok(!dayLabelMatches("Friday, 30 November", 30, 10));
});

test("12-hour wheels", () => {
  assert.deepEqual(hour12(0), { hour: 12, half: "AM" });
  assert.deepEqual(hour12(12), { hour: 12, half: "PM" });
  assert.deepEqual(hour12(15), { hour: 3, half: "PM" });
});
