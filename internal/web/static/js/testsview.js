// What every part of the Tests page says the same way: a verdict, a cell's
// or a run's state, a time, a duration, the address of a cell's evidence
// (docs/design/TEST-MODE.md, "The Tests page"). A state is always a glyph
// and a word (a DS status chip); never a colour alone.

import { html, raw, icon } from './ui.js';

// The verdicts, worst first, as Socrates ranks them.
export const VERDICTS = ['fail', 'blocked', 'issue', 'pass', 'na'];

// Each verdict's word, glyph and status tone ('' is the neutral chip).
export const VERDICT = {
  fail: { word: 'Fail', glyph: 'alertCircle', tone: 'error' },
  blocked: { word: 'Blocked', glyph: 'ban', tone: '' },
  issue: { word: 'Issue', glyph: 'alert', tone: 'warning' },
  pass: { word: 'Pass', glyph: 'checkCircle', tone: 'ok' },
  na: { word: 'N/A', glyph: 'minimize', tone: '' },
};

// An attempt's states that are not over yet, and the ones that are.
const ATTEMPT = {
  queued: { word: 'Queued', glyph: 'clock' },
  waiting: { word: 'Waiting', glyph: 'clock' },
  starting: { word: 'Starting', spin: true },
  running: { word: 'Running', spin: true },
  ended: { word: 'Ended', glyph: 'check' },
  aborted: { word: 'Aborted', glyph: 'alertCircle', tone: 'error' },
  stopped: { word: 'Stopped', glyph: 'stopCircle' },
  failed: { word: 'Failed to start', glyph: 'alertCircle', tone: 'error' },
};

// A run's states.
const RUN = {
  preparing: { word: 'Preparing', spin: true },
  waiting: { word: 'Waiting for another run', glyph: 'clock' },
  running: { word: 'Running', spin: true },
  finishing: { word: 'Reviewing', spin: true },
  done: { word: 'Done', glyph: 'checkCircle', tone: 'ok' },
  stopped: { word: 'Stopped', glyph: 'stopCircle' },
  failed: { word: 'Failed', glyph: 'alertCircle', tone: 'error' },
  paused: { word: 'Paused', glyph: 'pause', tone: 'warning' },
};

// A bug's severity, worst first.
export const SEVERITY = {
  critical: { word: 'Critical', glyph: 'alertCircle', tone: 'error' },
  major: { word: 'Major', glyph: 'alert', tone: 'warning' },
  minor: { word: 'Minor', glyph: 'info', tone: '' },
  cosmetic: { word: 'Cosmetic', glyph: 'sparkles', tone: '' },
};

export const ACTIVE_RUN = new Set(['preparing', 'waiting', 'running', 'finishing', 'paused']);
export const OPEN_ATTEMPT = new Set(['queued', 'waiting', 'starting', 'running']);

export function rank(verdict) {
  const i = VERDICTS.indexOf(verdict);
  return i < 0 ? VERDICTS.length : i;
}

/** chip is a DS status chip of one entry of the tables above. */
export function chip(entry, { tip = '', extra = '' } = {}) {
  if (!entry) return '';
  const tone = entry.tone ? ' ds-status--' + entry.tone : '';
  if (entry.spin) {
    return html`<span class="ds-status ds-status--running${extra}"${tip ? raw(html` data-tip="${tip}" tabindex="0"`) : ''}><span class="ds-spinner ds-status__spinner" aria-hidden="true"></span><span>${entry.word}</span></span>`;
  }
  return html`<span class="${'ds-status' + tone + extra}"${tip ? raw(html` data-tip="${tip}" tabindex="0"`) : ''}>${raw(icon(entry.glyph, 16))}<span>${entry.word}</span></span>`;
}

export function verdictChip(verdict, options) { return chip(VERDICT[verdict] || { word: verdict, glyph: 'helpCircle' }, options); }
export function runChip(state, options) { return chip(RUN[state] || { word: state, glyph: 'helpCircle' }, options); }
export function severityChip(severity) { return chip(SEVERITY[severity] || { word: severity || 'Unrated', glyph: 'info' }); }

/**
 * cellChip is how a cell stands: its verdict once it has one (the reviewed
 * one where a reviewer changed it), else its state; a waiting cell carries
 * its reason in its tooltip.
 */
export function cellChip(row) {
  if (row.final) return verdictChip(row.final);
  const entry = ATTEMPT[row.state] || { word: row.state, glyph: 'helpCircle' };
  return chip(entry, { tip: row.state === 'waiting' ? row.wait || 'Waiting' : '' });
}

export function attemptWord(state) { return (ATTEMPT[state] || { word: state }).word; }
export function verdictWord(verdict) { return (VERDICT[verdict] || { word: verdict }).word; }
export function runWord(state) { return (RUN[state] || { word: state }).word; }

/** mark is a verdict as a small glyph with its word for the reader and the tip. */
export function mark(verdict, tip) {
  const entry = VERDICT[verdict] || ATTEMPT[verdict] || { word: verdict, glyph: 'helpCircle' };
  const label = tip || entry.word;
  return html`<span class="${'so-mark so-mark--' + (VERDICT[verdict] ? verdict : 'open')}" role="img" aria-label="${label}" data-tip="${label}">${raw(icon(entry.glyph || 'clock', 16))}</span>`;
}

/* ---------------------------------------------------------------- times */

const dateTime = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
const dateLong = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
const clockTime = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });

export function when(ms) { return ms ? dateTime.format(new Date(ms)) : ''; }
export function whenLong(ms) { return ms ? dateLong.format(new Date(ms)) : ''; }
export function clock(ms) { return ms ? clockTime.format(new Date(ms)) : ''; }

/** span is a duration in words: "45 s", "12 min", "1 h 5 min". */
export function span(ms) {
  if (!(ms > 0)) return '';
  const s = Math.round(ms / 1000);
  if (s < 60) return s + ' s';
  const m = Math.round(s / 60);
  if (m < 60) return m + ' min';
  const h = Math.floor(m / 60);
  return h + ' h' + (m % 60 ? ' ' + (m % 60) + ' min' : '');
}

/** runLabel is how a run is named in a list: its name, else its date. */
export function runLabel(run) {
  if (!run) return '';
  return run.name ? run.name + ' · ' + when(run.created_at) : when(run.created_at);
}

/** countsWords is a run's counts in a line: "41 pass · 7 issue · 3 fail". */
export function countsWords(counts) {
  const c = counts || {};
  const parts = [];
  for (const v of VERDICTS) if (c.verdicts && c.verdicts[v]) parts.push(c.verdicts[v] + ' ' + verdictWord(v).toLowerCase());
  const states = c.states || {};
  const open = (states.queued || 0) + (states.waiting || 0) + (states.starting || 0) + (states.running || 0);
  if (open) parts.push(open + ' to go');
  return parts.length ? parts.join(' · ') : (c.cells ? c.cells + ' cells' : 'No cells');
}

/** selectionWords is a run's selection in a few words (Selection.Describe). */
export function selectionWords(sel) {
  const s = sel || {};
  const parts = [];
  const add = (label, list) => { if (list && list.length) parts.push(label + ' ' + list.join(', ')); };
  add('areas', s.areas);
  add('tests', s.tests);
  add('tags', s.tags);
  add('priorities', s.priorities);
  for (const name of Object.keys(s.dims || {}).sort()) add(name, s.dims[name]);
  if (s.problems_of) parts.push('problems of ' + s.problems_of);
  return parts.length ? parts.join('; ') : 'everything';
}

/* -------------------------------------------------------------- the API */

const enc = encodeURIComponent;

export const paths = {
  repos: () => '/api/tests/repos',
  repo: (repo) => '/api/tests/repos/' + enc(repo),
  catalog: (repo) => '/api/tests/repos/' + enc(repo) + '/catalog',
  settings: (repo) => '/api/tests/repos/' + enc(repo) + '/settings',
  check: (repo, tool) => '/api/tests/repos/' + enc(repo) + '/tools/' + enc(tool) + '/check',
  runs: (repo) => '/api/tests/repos/' + enc(repo) + '/runs',
  history: (repo) => '/api/tests/repos/' + enc(repo) + '/history?limit=10',
  search: (repo, q) => '/api/tests/repos/' + enc(repo) + '/search?q=' + enc(q),
  global: () => '/api/tests/global',
  run: (run) => '/api/tests/runs/' + enc(run),
  cells: (run) => '/api/tests/runs/' + enc(run) + '/cells',
  bugs: (run) => '/api/tests/runs/' + enc(run) + '/bugs',
  action: (run, what) => '/api/tests/runs/' + enc(run) + '/' + what,
  attempt: (run, attempt) => '/api/tests/runs/' + enc(run) + '/attempts/' + enc(attempt),
  file: (run, attempt, name) => '/api/tests/runs/' + enc(run) + '/attempts/' + enc(attempt) + '/files/'
    + String(name).split('/').map(enc).join('/'),
};
