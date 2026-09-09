// When a session was last used, as a person would say it.
//
// The sidebar is read the way a phone's call list is read: the sessions from
// today are the ones being worked on, and everything below them is history. So
// the list is split by day rather than by a timestamp nobody wants to read,
// and the split is done here - away from the drawing - because the only hard
// part is the calendar, and a calendar is worth being able to test on its own.
//
// The boundaries are the browser's own local day, week and month: the person
// holding the phone is the one who decides what "yesterday" means, not the
// server's clock and not UTC.

/**
 * DAY_GROUPS is every group there is, in the order the list shows them.
 *
 * The keys are what bucketOf answers with, the labels are what the header
 * reads. A group with nothing in it is not drawn at all, so this is an order
 * rather than a layout.
 */
export const DAY_GROUPS = [
  { key: 'today', label: 'Today' },
  { key: 'yesterday', label: 'Yesterday' },
  { key: 'week', label: 'This week' },
  { key: 'month', label: 'This month' },
  { key: 'older', label: 'Older' },
];

// The first moment of the local day a millisecond belongs to.
function dayStart(ms) {
  const at = new Date(ms);
  at.setHours(0, 0, 0, 0);
  return at;
}

// Days are moved with setDate rather than by subtracting hours: a day is 23 or
// 25 hours long twice a year, and "yesterday" has to stay yesterday on both of
// those days.
function daysBefore(at, days) {
  const out = new Date(at);
  out.setDate(out.getDate() - days);
  return out;
}

/**
 * bucketOf says which group a moment belongs to, from the browser's local
 * calendar.
 *
 * The week starts on Monday, which is what the week means everywhere this is
 * used. A moment in the future - a clock that is a few seconds ahead of the
 * server's, which is every phone - is today rather than nowhere, and a
 * timestamp that is not a number at all is history rather than an exception.
 */
export function bucketOf(ts, now = Date.now()) {
  const when = Number(ts);
  if (!Number.isFinite(when) || when <= 0) return 'older';
  const today = dayStart(now);
  const day = dayStart(when);
  if (day.getTime() >= today.getTime()) return 'today';
  if (day.getTime() === daysBefore(today, 1).getTime()) return 'yesterday';
  // Monday is the first day of the week, and getDay() calls Sunday zero.
  const week = daysBefore(today, (today.getDay() + 6) % 7);
  if (when >= week.getTime()) return 'week';
  const month = new Date(today.getFullYear(), today.getMonth(), 1);
  if (when >= month.getTime()) return 'month';
  return 'older';
}

/**
 * momentOf is the last time a session's status changed, as the list
 * understands it - which is the moment the list orders and groups by.
 *
 * `active_at` is written by the server when the session did something: it
 * started working, finished, began needing an answer, or its program came up
 * or went down. It is deliberately not `updated_at`, which every attach,
 * rename and resize bumps: a row that jumped to the top of the list for
 * being opened and read was the complaint that made this column exist.
 * `updated_at` is the fallback for a row from a server that did not know the
 * column yet, and `created_at` for a row that has never been written to.
 */
export function momentOf(session) {
  if (!session) return 0;
  return Number(session.active_at) || Number(session.updated_at) || Number(session.created_at) || 0;
}

/**
 * movesRow says whether a committed change of activity from `prev` to `next`
 * is a status change in the sense the list orders by: the agent started
 * working, finished, or began needing an answer.
 *
 * It is the same rule the server applies when it writes `active_at`, and it
 * is applied here too so a row moves the moment the frame arrives rather
 * than at the next fetch of the list. A change into idle from anything but
 * busy is not on the list on purpose: it is what every running session
 * reports when the detector comes up after a restart.
 */
export function movesRow(prev, next) {
  if (!next || prev === next) return false;
  return next === 'busy' || prev === 'busy' || next === 'waiting';
}
