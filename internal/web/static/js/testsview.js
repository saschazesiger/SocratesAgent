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
import * as accounts from './accounts.js';

// The verdicts in the one order every part of the page shows them: the
// tiles, the result bars and their counts, the filters, the legends, the
// override dialog and a run's head all read Fail · Issue · Blocked · Pass ·
// N/A, left to right (top to bottom in the trend's columns).
export const VERDICTS = ['fail', 'issue', 'blocked', 'pass', 'na'];

// The verdicts worst first, as Socrates ranks them (a test's worst cell).
const WORST = ['fail', 'blocked', 'issue', 'pass', 'na'];

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
  const i = WORST.indexOf(verdict);
  return i < 0 ? WORST.length : i;
}

// How a cell or a test stands while a problem lasts, before "since Run N".
const STANDS = { fail: 'failing', issue: 'an issue', blocked: 'blocked' };

/**
 * sinceWords is a problem's streak in the words of its verdict: "failing
 * since Run 1", "an issue since Run 1", "blocked since Run 3" ('' without
 * a run).
 */
export function sinceWords(verdict, number) {
  if (!number) return '';
  return (STANDS[verdict] || 'a problem') + ' since Run ' + number;
}

/**
 * testOrder is the order of a catalog's tests everywhere on the page, the
 * one a run takes them in (catalog.Cells): P0 first, then by id.
 */
export function testOrder(a, b) {
  const p = (t) => ({ P0: 0, P1: 1, P2: 2 })[t.priority] ?? 1;
  // A catalog's test has its id in `id`; a run's row has it in `test` (its `id` is the attempt's).
  const x = String(a.test || a.id);
  const y = String(b.test || b.id);
  return p(a) - p(b) || (x < y ? -1 : x > y ? 1 : 0);
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

/**
 * legend says what the marks beside a verdict mean, for the kinds a table
 * shows (regressed, fixed, reviewed, overridden, attempt, flaky); empty
 * without any.
 */
const LEGEND = {
  regressed: { glyph: 'arrowDown', cls: 'so-tests-change so-tests-change--regressed', word: 'broke since the run before' },
  fixed: { glyph: 'arrowUp', cls: 'so-tests-change so-tests-change--fixed', word: 'fixed since the run before' },
  reviewed: { glyph: 'eye', cls: 'so-tests__mark', word: 'reviewed' },
  overridden: { glyph: 'pencil', cls: 'so-tests__mark', word: 'your verdict' },
  attempt: { text: '#2', word: 'attempt 2 or later' },
  flaky: { glyph: 'activity', cls: 'so-tests__mark', word: 'flaky' },
};
export function legend(kinds) {
  const shown = Object.keys(LEGEND).filter((k) => kinds.includes(k));
  if (!shown.length) return '';
  return html`<p class="ds-meta so-tests-legend"><span class="ds-parts"><span class="ds-parts__list">${shown.map((k) => {
    const l = LEGEND[k];
    return html`<span class="so-tests-nowrap">${l.text ? html`<span aria-hidden="true">${l.text}</span>` : html`<span class="${l.cls}">${raw(icon(l.glyph, 16))}</span>`}${l.word}</span>`;
  })}</span></span></p>`;
}

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

/**
 * runName is how a run is named: "Run 12", with its name in quotes, "Run 12
 * “nightly”", so that a name never reads like a state ("Run 6 · live").
 */
export function runName(run, { name = true } = {}) {
  if (!run) return '';
  const n = 'Run ' + (run.n || run.number || '?');
  return name && run.name ? n + ' “' + run.name + '”' : n;
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
 * resultBar is a run's counts as one stacked bar (fail · issue · blocked ·
 * pass · still to go, as VERDICTS), each part as wide as its share. It is decoration
 * beside the counts in words, so it is hidden from the reader.
 */
export function resultBar(counts, { size = '' } = {}) {
  const c = counts || {};
  const v = c.verdicts || {};
  const total = c.cells || 0;
  if (!total) return html`<span class="${'so-tests-bar' + (size ? ' so-tests-bar--' + size : '')}" aria-hidden="true"></span>`;
  const shown = VERDICTS.filter((k) => k !== 'na');
  const rest = Math.max(0, total - shown.reduce((n, k) => n + (v[k] || 0), 0));
  const parts = [...shown.map((k) => [k, v[k]]), ['rest', rest]].filter(([, n]) => n > 0);
  return html`<span class="${'so-tests-bar' + (size ? ' so-tests-bar--' + size : '')}" aria-hidden="true">${parts.map(([k, n]) =>
    html`<span class="${'so-tests-bar__part so-tests-bar__part--' + k}" style="${'flex-grow: ' + n}"></span>`)}</span>`;
}

/** resultParts is a run's counts as DS parts, each a glyph and its word: "✓ 41 pass · ⚠ 7 issue". */
export function resultParts(counts) {
  const c = counts || {};
  const v = c.verdicts || {};
  const parts = [];
  for (const k of VERDICTS.filter((x) => x !== 'na')) {
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
/**
 * modelName is a model's name as the person knows it ("Sonnet"), found among
 * the program's offered models first and then among all it has (a role may
 * run on a model that is not one of Admin's favourites); else its id.
 */
export function modelName(harness, model) {
  if (!model) return '';
  const known = harnesses.modelOf(harness, model);
  if (known) return known.label || known.id;
  const all = (harnesses.harness(harness) || {}).models || [];
  const exact = all.find((m) => m.id === model);
  if (exact) return exact.label || exact.id;
  if (/^claude-/i.test(model)) {
    for (const word of String(model).toLowerCase().split(/[-_.]/).slice(1)) {
      const alias = all.find((m) => m.id === word);
      if (alias) return alias.label || alias.id;
    }
  }
  return model;
}

export function roleWords(role) {
  if (!role || !role.harness) return '';
  const model = modelName(role.harness, role.model);
  return [harnesses.label(role.harness), model, role.effort ? harnesses.effortLabel(role.effort) : ''].filter(Boolean).join(' · ');
}

// waitWords is the waiting cells in a few words: "3 waiting for a free
// Fake devices account".
function waitWords(cells) {
  const reasons = new Map();
  for (const row of cells || []) {
    if (row.state !== 'waiting') continue;
    const why = String(row.wait || '').replace(/\s*\(\d+ ahead\)\s*$/, '').replace(/^waiting:?\s*/i, '');
    reasons.set(why, (reasons.get(why) || 0) + 1);
  }
  return [...reasons].sort((a, b) => b[1] - a[1]).map(([why, n]) => n + ' waiting' + (why ? (/^(for|on|until)\b/.test(why) ? ' ' : ': ') + why : ''));
}

/**
 * activeWords is how far an active run is besides its count: "3 running ·
 * 2 waiting for … · about 4 min left · $1.20". `cells` (its rows, when
 * read) name why cells wait.
 */
export function activeWords(d, cells) {
  const c = d.counts || {};
  const states = c.states || {};
  const running = (states.running || 0) + (states.starting || 0);
  return [
    running ? running + ' running' : '',
    ...(cells ? waitWords(cells) : states.waiting ? [states.waiting + ' waiting'] : []),
    states.queued ? states.queued + ' queued' : '',
    c.reviews ? c.reviews + ' in review' : '',
    d.eta_ms > 0 ? (d.eta_ms < 60000 ? 'less than a minute left' : 'about ' + span(d.eta_ms) + ' left') : '',
    money(d.cost),
  ].filter(Boolean).join(' · ') || countsWords(c);
}

/**
 * runProgress is an active run's progress: a DS progress bar with "12 of
 * 20 cells done" and activeWords under it; `bare` leaves the words out
 * (the phone's one-line banner says the count itself).
 */
export function runProgress(d, { cells = null, label = '', bare = false } = {}) {
  const c = d.counts || {};
  const share = c.cells ? Math.min(1, (c.done || 0) / c.cells) : 0;
  const pct = Math.round(share * 100);
  return html`<div class="ds-progress" style="${'--ds-progress: ' + share.toFixed(4)}">
    <div class="ds-progress__track" role="progressbar" aria-label="${(label || runName(d, { name: false })) + ': cells done'}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}"${bare ? raw(html` aria-valuetext="${(c.done || 0) + ' of ' + plural(c.cells || 0, 'cell') + ' done'}"`) : ''}><div class="ds-progress__bar"></div></div>
    ${bare ? '' : html`<p class="ds-progress__label">${(c.done || 0) + ' of ' + plural(c.cells || 0, 'cell') + ' done'}</p>
    <p class="ds-progress__detail">${activeWords(d, cells)}</p>`}
  </div>`;
}

/**
 * roleShort is a role in a word or two for a line of several: its model
 * ("Sonnet 5.5"), else its program.
 */
export function roleShort(role) {
  if (!role || !role.harness) return 'the default';
  const model = modelName(role.harness, role.model);
  return model || harnesses.label(role.harness);
}

/**
 * rolesWords is the agents of a run in one line: "Cells: Sonnet 5.5 ·
 * Review: Opus · Analysis: Opus" ("No review" when it is off).
 */
export function rolesWords(s) {
  const set = s || {};
  return [
    'Cells: ' + roleShort(set.cell),
    set.review === false ? 'No review' : 'Review: ' + roleShort(set.reviewer),
    set.analysis === false ? 'No analysis' : 'Analysis: ' + roleShort(set.analyst),
  ].join(' · ');
}

const ROLE_WORDS = { cell: 'the cell agent', reviewer: 'the reviewer', analyst: 'the analyst' };

/**
 * roleProblems is what stands in the way of the roles of a run's settings
 * (the repository's, or the Run dialog's): a program this server cannot
 * run, accounts chosen that are gone (both block a run: its sessions would
 * wait for ever), or no account the role may use that Socrates knows to be
 * signed in (a warning: the server still tries the others, as a chat does,
 * since the program may be signed in on its own). Roles with the same
 * problem on the same program are said together. Each:
 * { block, text } (text without the link to Admin).
 */
export function roleProblems(settings) {
  const s = settings || {};
  const found = new Map();
  const add = (role, key, block, tail) => {
    if (!found.has(key)) found.set(key, { block, roles: [], tail });
    found.get(key).roles.push(ROLE_WORDS[role]);
  };
  for (const role of ['cell', 'reviewer', 'analyst']) {
    if (role === 'reviewer' && s.review === false) continue;
    if (role === 'analyst' && s.analysis === false) continue;
    const r = s[role] || {};
    if (!r.harness) continue;
    const name = harnesses.label(r.harness);
    const h = harnesses.harness(r.harness);
    if (h && !(h.enabled && h.installed)) {
      add(role, 'off:' + r.harness, true, ' on ' + name + ', which is not installed or switched on on this server.');
      continue;
    }
    if (!accounts.agents().includes(r.harness)) continue;
    const allowed = r.accounts || [];
    const list = accounts.forAgent(r.harness).filter((a) => !allowed.length || allowed.includes(a.id));
    if (allowed.length && !list.length) {
      add(role, 'gone:' + role, true, ' on ' + name + ' accounts that are no longer there: tick others, or none for every account.');
    } else if (list.length && !list.some(accounts.usable)) {
      add(role, 'out:' + r.harness + ':' + list.map((a) => a.id).join(','), false, ' on ' + name + ', and no account {they} may use is signed in: {their} sessions try '
        + (list.length === 1 ? list[0].label : 'them') + ' anyway, which works only if ' + name + ' is signed in on its own.');
    }
  }
  return [...found.values()].map((p) => {
    const who = p.roles.length === 1 ? p.roles[0] : p.roles.slice(0, -1).join(', ') + ' and ' + p.roles[p.roles.length - 1];
    const verb = p.roles.length === 1 ? ' runs' : ' run';
    const one = p.roles.length === 1;
    const tail = p.tail.replace('{they}', one ? 'it' : 'they').replace('{their}', one ? 'its' : 'their');
    return { block: p.block, text: who.charAt(0).toUpperCase() + who.slice(1) + verb + tail };
  });
}

/** roleAlerts is roleProblems as DS alerts, each with the way to Admin's accounts. */
export function roleAlerts(problems) {
  return html`${problems.map((p) => html`<div class="${'ds-alert ' + (p.block ? 'ds-alert--error' : 'ds-alert--warning')}"${p.block ? raw(' role="alert"') : ''} data-ds-key="${p.text}">${raw(icon(p.block ? 'alertCircle' : 'alert', 20))}<span class="ds-alert__text so-break">${p.text}</span><a class="ds-btn ds-btn--text ds-btn--sm" href="/admin#accountsCard">Accounts in Admin</a></div>`)}`;
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

/**
 * poolList is a run's pools as a list: a row per account of a pool, the
 * cells that hold it below its name (a list, so long names wrap on a phone
 * rather than squeezing beside a label).
 */
export function poolList(pools, cells) {
  return html`${(pools || []).map((pool) => html`<ul class="ds-list" aria-label="${pool.title || pool.tool}" data-ds-key="${'pool-' + pool.tool}">${(pool.members || []).map((m) => html`<li class="ds-list__item" data-ds-key="${'m' + m.n}">
    <span class="ds-list__text">
      <span class="ds-list__title">${(pool.title || pool.tool) + ' · ' + (m.label || 'account ' + m.n)}</span>
      <span class="ds-list__meta so-break">${m.holders && m.holders.length ? holderWords(m.holders, cells) : m.busy ? 'In use outside Socrates' : 'Free'}</span>
    </span>
    ${m.holders && m.holders.length ? chip({ word: 'In use', glyph: 'play' })
      : m.busy ? chip({ word: 'Busy', glyph: 'ban', tone: 'warning' }) : chip({ word: 'Free', glyph: 'check' })}
  </li>`)}</ul>`)}`;
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

/* --- bugs and chat --- */

// (Imported here, with the part that needs them; imports are hoisted.)
import { DS } from './ui.js';
import { api, confirmDialog } from './api.js';

// A tracked bug's status (TrackedBug.status), as the Bugs view and the bug
// view say it: a glyph and a word, and what it means for the person who
// sets it (the triage's explanation of each).
export const BUG_STATUS = {
  open: { word: 'Open', glyph: 'flag', tone: '', hint: 'Needs a fix. Counted on the Bugs tab and the overview.' },
  known: { word: 'Known', glyph: 'eye', tone: '', hint: 'It stays listed and is reported again when seen, but no longer counts as open.' },
  not_a_bug: { word: 'Not a bug', glyph: 'ban', tone: '', hint: 'The test or its expectation is wrong. The analyst does not report it again unless the evidence changes.' },
  fixed: { word: 'Fixed', glyph: 'checkCircle', tone: 'ok', hint: 'Fixed in the code. A later run that reports it opens it again.' },
};
export const BUG_STATUSES = ['open', 'known', 'not_a_bug', 'fixed'];

/** bugStatusChip is a bug's status; an open bug that came back after it was fixed says so. */
export function bugStatusChip(bug) {
  if (bug.status === 'open' && bug.regressed) {
    return chip({ word: 'Open · came back', glyph: 'sync', tone: 'error' }, { tip: 'Open again: it was fixed, then a later run reported it' });
  }
  return chip(BUG_STATUS[bug.status] || { word: bug.status || 'Open', glyph: 'flag' });
}

export function bugStatusWord(status) { return (BUG_STATUS[status] || { word: status }).word; }

/** bugStateWords is a bug's status in words, "open · came back" for one that returned. */
export function bugStateWords(bug) {
  const word = bugStatusWord(bug.status || 'open');
  return (bug.status === 'open' || !bug.status) && bug.regressed ? word + ' · came back' : word;
}

/** seenWords is where a bug was seen: "Run 2 → Run 5", "Run 3" for one run. */
export function seenWords(bug) {
  const first = bug.first_number;
  const last = bug.last_number;
  if (!first && !last) return '';
  if (!first || first === last) return 'Run ' + (last || first);
  return 'Run ' + first + ' → Run ' + last;
}

/** issueNumber is the number of a GitHub issue's address ("123"), '' for another address. */
export function issueNumber(url) {
  const m = /\/issues\/(\d+)\b/.exec(String(url || ''));
  return m ? m[1] : '';
}

/**
 * problemSteps is what an attempt (GET …/attempts/{attempt}) found wrong:
 * every step whose verdict is not a pass, each with what it did, what was
 * expected, what was observed and the reviewer's word on it. A cell whose
 * steps all passed but which did not (a blocked cell) has none.
 */
export function problemSteps(view) {
  return ((view && view.steps) || []).filter((s) => s.verdict && !passed(s.verdict)).map((s) => ({
    n: s.n, do: s.do, expect: s.expect, verdict: s.verdict, observed: s.observed || '',
    review: s.review && s.review.note ? s.review.note : '',
  }));
}

/** cellName is a cell as text: "The checkout takes an order · iPhone 15, German". */
export function cellName(row) {
  const dims = dimsWords(row.dims) || row.cell || '';
  return (row.title || row.test || row.cell_id) + (dims ? ' · ' + dims : '');
}

// The attempts read for a bug's cells or a chat's message, by run and
// attempt: an ended attempt does not change (an override aside, which the
// bug view and the chat do not show).
const attempts = new Map();

/** attemptView reads an attempt in full (GET …/attempts/{attempt}), once. */
export function attemptView(run, attempt) {
  const key = run + '/' + attempt;
  if (!attempts.has(key)) {
    const read = api(paths.attempt(run, attempt), { attempts: 2 }).catch((err) => {
      attempts.delete(key);
      throw err;
    });
    attempts.set(key, read);
  }
  return attempts.get(key);
}

/**
 * guardClose keeps a dialog's edits from going by accident: its Close,
 * Cancel and backdrop carry data-guarded-close instead of DS's data-close,
 * and they and Escape close it at once while nothing is edited, else after
 * a question. A busy dialog (data-ds-busy) does not close.
 */
export function guardClose(dialog, { dirty, question }) {
  async function request() {
    if (dialog.hasAttribute('data-ds-busy') || !dialog.open) return;
    if (dirty()) {
      const yes = await confirmDialog({ ...question(), danger: true, opener: document.activeElement });
      if (!yes) return;
    }
    DS.overlay.close(dialog);
  }
  dialog.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || event.isComposing || event.defaultPrevented || DS.overlay.topmost() !== dialog) return;
    // DS closes it on its own when there is nothing to lose.
    if (!dirty() || dialog.hasAttribute('data-ds-busy')) return;
    event.preventDefault();
    request();
  });
  dialog.addEventListener('click', (event) => {
    if (event.target.closest('[data-guarded-close]')) request();
  });
  return request;
}
