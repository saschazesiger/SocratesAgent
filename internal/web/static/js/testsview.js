// What every part of the Tests page says the same way (docs/design/TEST-MODE.md,
// "The Tests page"): a verdict, a cell's or a run's state, a run's name
// ("Run 12 · nightly"), a time, a duration, a cost, a change against the run
// before, a result bar, and the addresses of the API. A state is always a
// glyph and a word (a DS status chip); never a colour alone.
//
// Every view module (tests*.js) draws with these, so a verdict looks the same
// in the overview, a run's table, a test's history and the cell viewer.

import { html, raw, icon } from './ui.js';
import * as harnesses from './harnesses.js';

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

/** rank orders verdicts worst first (unknown last). */
export function rank(verdict) {
  const i = VERDICTS.indexOf(verdict);
  return i < 0 ? VERDICTS.length : i;
}

/** sevRank orders severities worst first. */
export function sevRank(severity) {
  const i = Object.keys(SEVERITY).indexOf(severity);
  return i < 0 ? 9 : i;
}

/** chip is a DS status chip of one entry of the tables above. */
export function chip(entry, { tip = '', extra = '' } = {}) {
  if (!entry) return '';
  const tone = entry.tone ? ' ds-status--' + entry.tone : '';
  if (entry.spin) {
    return html`<span class="${'ds-status ds-status--running' + extra}"${tip ? raw(html` data-tip="${tip}" tabindex="0"`) : ''}><span class="ds-spinner ds-status__spinner" aria-hidden="true"></span><span>${entry.word}</span></span>`;
  }
  return html`<span class="${'ds-status' + tone + extra}"${tip ? raw(html` data-tip="${tip}" tabindex="0"`) : ''}>${raw(icon(entry.glyph, 16))}<span>${entry.word}</span></span>`;
}

export function verdictChip(verdict, options) { return chip(VERDICT[verdict] || { word: verdict, glyph: 'helpCircle' }, options); }
export function runChip(state, options) { return chip(RUN[state] || { word: state, glyph: 'helpCircle' }, options); }
export function severityChip(severity) { return chip(SEVERITY[severity] || { word: severity || 'Unrated', glyph: 'info' }); }

/**
 * cellChip is how a cell stands: its verdict once it has one (the one that
 * counts: an override, else the reviewer's, else the agent's), else its
 * state; a waiting cell carries its reason in its tooltip.
 */
export function cellChip(row, { tip = true } = {}) {
  if (row.final) return verdictChip(row.final);
  const entry = ATTEMPT[row.state] || { word: row.state, glyph: 'helpCircle' };
  return chip(entry, { tip: tip && row.state === 'waiting' ? row.wait || 'Waiting' : '' });
}

export function attemptWord(state) { return (ATTEMPT[state] || { word: state }).word; }
export function verdictWord(verdict) { return (VERDICT[verdict] || ATTEMPT[verdict] || { word: verdict }).word; }
export function runWord(state) { return (RUN[state] || { word: state }).word; }
export function severityWord(severity) { return (SEVERITY[severity] || { word: severity || 'Unrated' }).word; }

/** mark is a verdict as a small glyph with its word for the reader and the tip. */
export function mark(verdict, tip) {
  const entry = VERDICT[verdict] || ATTEMPT[verdict] || { word: verdict, glyph: 'helpCircle' };
  const label = tip || entry.word;
  return html`<span class="${'so-mark so-mark--' + (VERDICT[verdict] ? verdict : 'open')}" role="img" aria-label="${label}" data-tip="${label}">${raw(icon(entry.glyph || 'clock', 16))}</span>`;
}

/* ------------------------------------------------------------- problems */

/** passed: a verdict that is not a problem. */
export function passed(verdict) { return verdict === 'pass' || verdict === 'na'; }

/** isProblem: a cell that did not pass, or one that ended without a verdict. */
export function isProblem(row) {
  if (row.final) return !passed(row.final);
  return row.state === 'aborted' || row.state === 'failed' || row.state === 'stopped';
}

/** problemCount is how many cells of a run did not pass (what "the problems of run N" runs). */
export function problemCount(counts) {
  const c = counts || {};
  const v = c.verdicts || {};
  const open = Object.entries(c.states || {}).filter(([s]) => OPEN_ATTEMPT.has(s)).reduce((n, [, k]) => n + k, 0);
  return Math.max(0, (c.cells || 0) - (v.pass || 0) - (v.na || 0) - open);
}

/** passRate is the share of a run's judged cells that passed, 0 to 1 (null without any). */
export function passRate(counts) {
  const v = (counts && counts.verdicts) || {};
  const judged = (v.pass || 0) + (v.issue || 0) + (v.fail || 0) + (v.blocked || 0);
  return judged ? (v.pass || 0) / judged : null;
}

/* ------------------------------------------------------------- changes */

// CHANGE is how a cell moved against the run before (CellRow.change).
export const CHANGE = {
  regressed: { word: 'Regressed', glyph: 'arrowDown' },
  fixed: { word: 'Fixed', glyph: 'arrowUp' },
  new: { word: 'New', glyph: 'plus' },
};

/**
 * changeMark is a cell's change as a glyph with its words for the reader:
 * "Regressed: Pass in Run 11". Nothing for an unchanged cell.
 */
export function changeMark(row, { label = false } = {}) {
  const c = CHANGE[row.change];
  if (!c || (row.change === 'new' && !label)) return '';
  const prev = row.prev;
  const words = c.word + (prev && prev.verdict ? ': ' + verdictWord(prev.verdict) + ' in Run ' + (prev.number || '?') : '');
  return html`<span class="${'so-tests-change so-tests-change--' + row.change}" role="img" aria-label="${words}" data-tip="${words}">${raw(icon(c.glyph, 16))}${label ? html`<span>${c.word}</span>` : ''}</span>`;
}

/* ---------------------------------------------------------------- times */

const dateTime = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
const dateLong = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
const clockTime = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

export function when(ms) { return ms ? dateTime.format(new Date(ms)) : ''; }
export function whenLong(ms) { return ms ? dateLong.format(new Date(ms)) : ''; }
export function clock(ms) { return ms ? clockTime.format(new Date(ms)) : ''; }

/** ago is a time in words relative to now: "5 minutes ago", "yesterday"; past a week the date. */
export function ago(ms, now = Date.now()) {
  if (!ms) return '';
  const s = Math.round((ms - now) / 1000);
  const a = Math.abs(s);
  if (a < 45) return 'just now';
  if (a < 3600) return relative.format(Math.round(s / 60), 'minute');
  if (a < 86400) return relative.format(Math.round(s / 3600), 'hour');
  if (a < 7 * 86400) return relative.format(Math.round(s / 86400), 'day');
  return when(ms);
}

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

/** runSpan is how long a run took, or has taken so far. */
export function runSpan(run, now = Date.now()) {
  if (!run) return '';
  if (run.duration_ms > 0 && !ACTIVE_RUN.has(run.state)) return span(run.duration_ms);
  const end = run.ended_at || (ACTIVE_RUN.has(run.state) ? now : 0);
  return end ? span(end - (run.started_at || run.created_at)) : '';
}

const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 });
const usdWhole = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 });

/** money is a cost: "$3.20", "$48" from ten dollars on; '' when unknown. */
export function money(v) {
  if (typeof v !== 'number' || !Number.isFinite(v)) return '';
  if (v > 0 && v < 0.005) return '<$0.01';
  return v >= 10 ? usdWhole.format(v) : usd.format(v);
}

export function plural(n, one, many) { return n + ' ' + (n === 1 ? one : many || one + 's'); }

/* ----------------------------------------------------------------- runs */

/**
 * numberRuns gives every run its number `n`: the server's (Run.Number;
 * a run of before the numbers is counted by its place among the
 * repository's runs, oldest 1).
 */
export function numberRuns(runs) {
  const byAge = runs.slice().sort((a, b) => a.created_at - b.created_at);
  byAge.forEach((run, i) => { run.n = Number.isFinite(run.number) && run.number > 0 ? run.number : i + 1; });
  return runs;
}

/** runName is how a run is named: "Run 12", with its name "Run 12 · nightly". */
export function runName(run, { name = true } = {}) {
  if (!run) return '';
  const n = 'Run ' + (run.n || run.number || '?');
  return name && run.name ? n + ' · ' + run.name : n;
}

/** runLabel is kept for the modules that name a run by date. */
export function runLabel(run) { return runName(run); }

/** startedBy is the chat that started a run (Run.StartedBy {chat, title}), in words ('' for a person). */
export function startedBy(run) {
  const s = run && run.started_by;
  return s ? s.title || 'a chat' : '';
}

/** startedByHref is the address of the chat that started a run ('' for a person). */
export function startedByHref(run) {
  const s = run && run.started_by;
  return s && s.chat ? '/#open=' + encodeURIComponent(s.chat) : '';
}

/** countsWords is a run's counts in a line: "41 pass · 7 issue · 3 fail". */
export function countsWords(counts) {
  const c = counts || {};
  const parts = [];
  for (const v of VERDICTS) if (c.verdicts && c.verdicts[v]) parts.push(c.verdicts[v] + ' ' + verdictWord(v).toLowerCase());
  const states = c.states || {};
  const open = (states.queued || 0) + (states.waiting || 0) + (states.starting || 0) + (states.running || 0);
  if (open) parts.push(open + ' to go');
  return parts.length ? parts.join(' · ') : (c.cells ? plural(c.cells, 'cell') : 'No cells');
}

/** selectionWords is a run's selection in a few words (Selection.Describe). */
export function selectionWords(sel, runs) {
  const s = sel || {};
  const parts = [];
  const add = (label, list) => { if (list && list.length) parts.push(label + ' ' + list.join(', ')); };
  add(s.areas && s.areas.length === 1 ? 'area' : 'areas', s.areas);
  if (s.tests && s.tests.length) parts.push(s.tests.length > 2 ? plural(s.tests.length, 'test') : s.tests.map((t) => t.split('/').pop()).join(', '));
  if (s.cells && s.cells.length) parts.push(plural(s.cells.length, 'chosen cell'));
  add(s.tags && s.tags.length === 1 ? 'tag' : 'tags', s.tags);
  if (s.priorities && s.priorities.length) parts.push(s.priorities.join(', '));
  for (const name of Object.keys(s.dims || {}).sort()) add(name, s.dims[name]);
  if (s.problems_of) {
    const from = (runs || []).find((r) => r.id === s.problems_of);
    parts.push('the problems of ' + (from ? runName(from, { name: false }) : s.problems_of === 'latest' ? 'the latest run' : 'an earlier run'));
  }
  return parts.length ? parts.join(' · ') : 'Everything';
}

/** dimsWords is a cell's dimension values: "Phone A, es". */
export function dimsWords(dims, skip = '') {
  return (dims || []).filter((d) => d.name !== skip).map((d) => d.label || d.id).join(', ');
}

/** titleCase is a dimension's name as a label: "device" → "Device". */
export function titleCase(name) { return name ? name.charAt(0).toUpperCase() + name.slice(1) : ''; }

/**
 * resultBar is a run's counts as one stacked bar (pass · issue · fail ·
 * blocked · still to go), each part as wide as its share. It is decoration
 * beside the counts in words, so it is hidden from the reader.
 */
export function resultBar(counts, { size = '' } = {}) {
  const c = counts || {};
  const v = c.verdicts || {};
  const total = c.cells || 0;
  if (!total) return html`<span class="${'so-tests-bar' + (size ? ' so-tests-bar--' + size : '')}" aria-hidden="true"></span>`;
  const rest = Math.max(0, total - ['pass', 'issue', 'fail', 'blocked'].reduce((n, k) => n + (v[k] || 0), 0));
  const parts = [['pass', v.pass], ['issue', v.issue], ['fail', v.fail], ['blocked', v.blocked], ['rest', rest]].filter(([, n]) => n > 0);
  return html`<span class="${'so-tests-bar' + (size ? ' so-tests-bar--' + size : '')}" aria-hidden="true">${parts.map(([k, n]) =>
    html`<span class="${'so-tests-bar__part so-tests-bar__part--' + k}" style="${'flex-grow: ' + n}"></span>`)}</span>`;
}

/** resultParts is a run's counts as DS parts, each a glyph and its word: "✓ 41 pass · ⚠ 7 issue". */
export function resultParts(counts) {
  const c = counts || {};
  const v = c.verdicts || {};
  const parts = [];
  for (const k of ['fail', 'issue', 'blocked', 'pass']) {
    if (v[k]) parts.push(html`<span class="so-tests-count">${raw(icon(VERDICT[k].glyph, 16))}${v[k] + ' ' + VERDICT[k].word.toLowerCase()}</span>`);
  }
  const states = c.states || {};
  const open = (states.queued || 0) + (states.waiting || 0) + (states.starting || 0) + (states.running || 0);
  if (open) parts.push(html`<span class="so-tests-count">${raw(icon('clock', 16))}${open + ' to go'}</span>`);
  if (!parts.length) parts.push(html`<span>${c.cells ? plural(c.cells, 'cell') : 'No cells'}</span>`);
  return html`<span class="ds-parts"><span class="ds-parts__list">${parts.map((p) => html`<span>${p}</span>`)}</span></span>`;
}

/**
 * roleWords is a role of a run in a few words: "Claude Code · Sonnet 5.5 ·
 * Medium" (the program's catalogue, harnesses.js, names the model once it
 * is loaded).
 */
export function roleWords(role) {
  if (!role || !role.harness) return '';
  const model = role.model ? (harnesses.modelOf(role.harness, role.model) || {}).label || role.model : '';
  return [harnesses.label(role.harness), model, role.effort ? harnesses.effortLabel(role.effort) : ''].filter(Boolean).join(' · ');
}

/**
 * holderWords names the cells that hold an account of a pool by their test
 * and values ("The checkout · Phone A"), from the run's cells; an id the
 * cells do not have stays as it is.
 */
export function holderWords(holders, cells) {
  const byId = new Map((cells || []).map((r) => [r.cell_id, r]));
  return (holders || []).map((id) => {
    const r = byId.get(id);
    return r ? r.title + ' · ' + (dimsWords(r.dims) || r.cell) : id;
  }).join(', ');
}

/** stripHeading drops a leading "# Conclusions" (or any first-level heading) the card titles already. */
export function stripHeading(md) {
  return String(md || '').replace(/^\s*#\s+[^\n]*\n+/, '');
}

/* -------------------------------------------------------------- the API */

const enc = encodeURIComponent;
const testPath = (id) => String(id).split('/').map(enc).join('/');

export const paths = {
  repos: () => '/api/tests/repos',
  repo: (repo) => '/api/tests/repos/' + enc(repo),
  catalog: (repo) => '/api/tests/repos/' + enc(repo) + '/catalog',
  settings: (repo) => '/api/tests/repos/' + enc(repo) + '/settings',
  check: (repo, tool) => '/api/tests/repos/' + enc(repo) + '/tools/' + enc(tool) + '/check',
  runs: (repo) => '/api/tests/repos/' + enc(repo) + '/runs',
  history: (repo, limit = 10) => '/api/tests/repos/' + enc(repo) + '/history?limit=' + limit,
  search: (repo, q) => '/api/tests/repos/' + enc(repo) + '/search?q=' + enc(q),
  // The reworked page's (TEST-MODE.md "Revision: the reworked Tests page").
  overview: (repo) => '/api/tests/repos/' + enc(repo) + '/overview',
  estimate: (repo) => '/api/tests/repos/' + enc(repo) + '/estimate',
  test: (repo, test) => '/api/tests/repos/' + enc(repo) + '/tests/' + testPath(test),
  repoBugs: (repo) => '/api/tests/repos/' + enc(repo) + '/bugs',
  bug: (repo, id) => '/api/tests/repos/' + enc(repo) + '/bugs/' + enc(id),
  bugIssue: (repo, id) => '/api/tests/repos/' + enc(repo) + '/bugs/' + enc(id) + '/issue',
  github: (repo) => '/api/tests/repos/' + enc(repo) + '/github',
  global: () => '/api/tests/global',
  run: (run) => '/api/tests/runs/' + enc(run),
  cells: (run) => '/api/tests/runs/' + enc(run) + '/cells',
  bugs: (run) => '/api/tests/runs/' + enc(run) + '/bugs',
  report: (run, download = false) => '/api/tests/runs/' + enc(run) + '/report.md' + (download ? '?download=1' : ''),
  action: (run, what) => '/api/tests/runs/' + enc(run) + '/' + what,
  attempt: (run, attempt) => '/api/tests/runs/' + enc(run) + '/attempts/' + enc(attempt),
  override: (run, attempt) => '/api/tests/runs/' + enc(run) + '/attempts/' + enc(attempt) + '/override',
  file: (run, attempt, name) => '/api/tests/runs/' + enc(run) + '/attempts/' + enc(attempt) + '/files/'
    + String(name).split('/').map(enc).join('/'),
  /** page is the address of a place on the Tests page (for links and breadcrumbs). */
  page: (route) => '#' + new URLSearchParams(Object.entries(route).filter(([, v]) => v)).toString(),
};
