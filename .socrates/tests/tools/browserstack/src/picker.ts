/**
 * `qa pick`: the value a date or time field should get, and how the native pickers name their parts. The
 * field's own format is the HTML one (`2026-10-30`, `14:25`, `2026-10-30T14:25`); a value may also be relative
 * to the device's clock (`+5m`, `+2h`, `+1d`), as a person reads the time off the phone they hold.
 */

export type PickKind = "date" | "time" | "datetime-local";
export const PICK_KINDS: readonly string[] = ["date", "time", "datetime-local"];

/** A field's value split into its parts (month 1-12). */
export interface Moment {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
}

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

/** The HTML value of a moment for a field of `kind`. */
export function formatValue(moment: Moment, kind: PickKind): string {
  const date = `${pad(moment.year, 4)}-${pad(moment.month)}-${pad(moment.day)}`;
  const time = `${pad(moment.hour)}:${pad(moment.minute)}`;
  return kind === "date" ? date : kind === "time" ? time : `${date}T${time}`;
}

/** Parses an HTML value of a date, time or datetime-local field (seconds are dropped). */
export function parseValue(value: string, kind: PickKind): Moment | null {
  const date = /^(\d{4})-(\d{2})-(\d{2})/.exec(value);
  const time = /(?:^|T)(\d{2}):(\d{2})/.exec(value);
  if (kind !== "time" && !date) return null;
  if (kind !== "date" && !time) return null;
  return {
    year: date ? Number(date[1]) : 1970,
    month: date ? Number(date[2]) : 1,
    day: date ? Number(date[3]) : 1,
    hour: time ? Number(time[1]) : 0,
    minute: time ? Number(time[2]) : 0,
  };
}

/**
 * The moment `qa pick` asks for: an HTML value of the field's kind, or `+<n>m|h|d` from `now` (the device's local
 * time, as its parts), rounded up to the next whole minute. Throws a message for anything else.
 */
export function wantedMoment(spec: string, kind: PickKind, now: Moment & { second: number }): Moment {
  const relative = /^\+(\d+)\s*(m|min|h|d)$/i.exec(spec.trim());
  if (relative) {
    const minutes = Number(relative[1]) * ({ m: 1, min: 1, h: 60, d: 1440 } as Record<string, number>)[relative[2]!.toLowerCase()]!;
    const base = Date.UTC(now.year, now.month - 1, now.day, now.hour, now.minute) + (now.second > 0 ? 60_000 : 0);
    const at = new Date(base + minutes * 60_000);
    return { year: at.getUTCFullYear(), month: at.getUTCMonth() + 1, day: at.getUTCDate(), hour: at.getUTCHours(), minute: at.getUTCMinutes() };
  }
  const moment = parseValue(spec.trim(), kind);
  const example = formatValue({ year: 2026, month: 10, day: 30, hour: 14, minute: 25 }, kind);
  if (!moment || !valid(moment)) throw new Error(`a ${kind} field takes ${example} (or +5m, +2h, +1d from the device's clock), not ${spec}`);
  return moment;
}

function valid(m: Moment): boolean {
  const date = new Date(Date.UTC(m.year, m.month - 1, m.day));
  return date.getUTCMonth() === m.month - 1 && date.getUTCDate() === m.day && m.hour >= 0 && m.hour < 24 && m.minute >= 0 && m.minute < 60;
}

export const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

/** Months from the shown one to the wanted one (negative: back). */
export function monthSteps(shown: { year: number; month: number }, wanted: { year: number; month: number }): number {
  return (wanted.year - shown.year) * 12 + (wanted.month - shown.month);
}

/** Reads "October 2026" (iOS's month header, the calendar's value). */
export function parseMonthYear(text: string): { year: number; month: number } | null {
  const m = new RegExp(`(${MONTHS.join("|")})\\s+(\\d{4})`, "i").exec(text);
  if (!m) return null;
  return { month: MONTHS.findIndex((name) => name.toLowerCase() === m[1]!.toLowerCase()) + 1, year: Number(m[2]) };
}

/** Whether a calendar day's label names the day ("Friday, 30 October", "Today, Friday, 30. October", "October 30"). */
export function dayLabelMatches(label: string, day: number, month: number): boolean {
  const name = MONTHS[month - 1]!;
  return new RegExp(`(^|[\\s,])(${day}\\.?\\s+${name}|${name}\\s+${day})(\\b|$)`, "i").test(label);
}

/** The hour as a 12-hour wheel shows it, and its half of the day. */
export function hour12(hour: number): { hour: number; half: "AM" | "PM" } {
  return { hour: hour % 12 === 0 ? 12 : hour % 12, half: hour < 12 ? "AM" : "PM" };
}
