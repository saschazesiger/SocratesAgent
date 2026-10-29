// The cell viewer of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", the cell viewer): one attempt of a cell, over any view, the whole
// window on every screen.
//
// Its head names the test and the cell's values, and walks the list the
// cell was opened from (previous and next, "3 of 12"). Under it the verdict
// as it counts and who gave it, the change against the run before, the
// attempts, and the actions: Override verdict, Retry, Fix in a chat, Open
// transcript. Then the steps:
//
//   wide (900px and more)  the steps on the left, the chosen step's
//                          screenshot large on the right with a film strip
//                          of every screenshot of the cell; Compare with the
//                          last pass puts the same step of the cell's latest
//                          passing attempt beside it. Under them the actions
//                          log and the facts.
//   narrow                 a switch Steps · Actions · Details; the steps as a
//                          list, each with its screenshots at full width in
//                          a strip that swipes.
//
// A running cell is live: it is read again every few seconds while it is
// shown, its steps fill as they are judged and its latest screenshot is the
// one shown (until the person picks another).
//
// mount(ctx) hands back { open(attemptId, runId, list), close(), refresh() };
// `list` ([{ attempt, run }]) is the cells it was opened from, for previous
// and next (without one, the cells of the run in their order).

import { api, errorMessage, toast } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import * as accounts from './accounts.js';
import * as harnesses from './harnesses.js';
import * as V from './testsview.js';

const $ = (id) => document.getElementById(id);

// The split view of steps and screenshots needs this much window.
const WIDE = window.matchMedia('(min-width: 900px)');
// A phone takes the cell's other actions as an action sheet, not a menu.
const PHONE = window.matchMedia('(max-width: 600px)');
// A running cell is read again this often while it is shown.
const LIVE_EVERY = 3000;

// What each verdict means, for the override dialog.
const MEANS = {
  pass: 'It does what every step expects.',
  issue: 'It works, with a flaw worth fixing.',
  fail: 'A step does not do what it should.',
  blocked: 'It could not be tested: a device, an account or the environment was in the way.',
  na: 'It does not apply to this cell.',
};

// How a cell stood in the run before, after "Regressed:" or "Fixed:".
const WAS = {
  pass: 'it passed', issue: 'it had an issue', fail: 'it failed', blocked: 'it was blocked', na: 'it did not apply',
};

const PARTS = [
  { id: 'steps', label: 'Steps' },
  { id: 'actions', label: 'Actions' },
  { id: 'details', label: 'Details' },
];

// Keys typed into a field are the field's.
const typing = (el) => !!(el && el.closest && el.closest('input, textarea, select, [contenteditable="true"], [role="radiogroup"], [role="tablist"]'));

export function mount(ctx) {
  const { state } = ctx;
  const dialog = $('cellDialog');
  const body = $('cellBody');
  const nav = $('cellNav');
  const overrideDialog = $('overrideDialog');
  const keysDialog = $('cellKeys');
  const moreMenu = $('cellMoreMenu');
  const moreSheet = $('cellMoreSheet');
  const rovings = new WeakSet(); // the step lists whose arrow keys are wired

  let shown = false; // the dialog is open
  let want = null; // { id, run }: the attempt the address asks for
  let view = null; // its AttemptView, once read
  let error = '';
  let list = []; // [{ attempt, run }]: previous and next
  let asked = 0;
  let liveTimer = 0;
  let part = 'steps'; // the narrow layout's part
  let zoom = false; // the large screenshot at its own size
  let compare = false; // the last pass beside it (kept from cell to cell)
  let sel = { key: '', step: 0, shot: -1, manual: false };
  let pass = null; // { for, state: 'loading' | 'none' | 'ready', run, attempt, label, view }
  const details = new Map(); // `${run}/${attempt}` -> AttemptView
  const runCells = new Map(); // run -> { rows } | { promise }
  const histories = new Map(); // test -> Promise of GET …/tests/{test}
  let bugs = null; // { repo, list } the repository's bugs, for the titles

  harnesses.load().catch(() => {}).then(() => { if (shown && view) draw(); });
  accounts.load().catch(() => {}).then(() => { if (shown && view) draw(); });

  /* ------------------------------------------------------------- reading */

  const keyOf = (run, attempt) => run + '/' + attempt;

  async function detailOf(run, attempt, { fresh = false } = {}) {
    const key = keyOf(run, attempt);
    if (!fresh && details.has(key)) return details.get(key);
    const v = await api(V.paths.attempt(run, attempt));
    details.set(key, v);
    return v;
  }

  // cellsOf is a run's rows (CellRow: prev, change, since, live): the
  // frame's for the live run, else read once and again after a change.
  async function cellsOf(run) {
    if (state.live && state.live.id === run) return state.live.cells || [];
    const have = runCells.get(run);
    if (have && have.rows) return have.rows;
    if (have && have.promise) return have.promise;
    const promise = api(V.paths.cells(run)).then((d) => {
      runCells.set(run, { rows: d.cells || [] });
      return d.cells || [];
    }).catch(() => {
      runCells.delete(run);
      return [];
    });
    runCells.set(run, { promise });
    return promise;
  }

  function rowsNow(run) {
    if (state.live && state.live.id === run) return state.live.cells || [];
    const have = runCells.get(run);
    return (have && have.rows) || [];
  }

  function rowOf(v) {
    return v ? rowsNow(v.run).find((r) => r.id === v.id) || null : null;
  }

  function historyOf(test) {
    const key = state.repo + ' ' + test;
    if (!histories.has(key)) {
      histories.set(key, api(V.paths.test(state.repo, test)).catch((err) => {
        histories.delete(key);
        throw err;
      }));
    }
    return histories.get(key);
  }

  async function loadBugs() {
    if (bugs && bugs.repo === state.repo) return;
    const repo = state.repo;
    bugs = { repo, list: [] };
    try {
      bugs = { repo, list: (await api(V.paths.repoBugs(repo), { attempts: 1 })).bugs || [] };
    } catch { /* the ids stand alone */ }
    if (shown && view) draw();
  }

  async function load({ quiet = false } = {}) {
    if (!want) return;
    const me = ++asked;
    const { id, run } = want;
    const cached = details.get(keyOf(run, id));
    if (!quiet) {
      if (cached) { view = cached; error = ''; draw(); } else if (!view || view.id !== id) { view = null; drawLoading(); }
    }
    try {
      const [v] = await Promise.all([detailOf(run, id, { fresh: true }), cellsOf(run)]);
      if (me !== asked) return;
      view = v;
      error = '';
    } catch (err) {
      if (me !== asked) return;
      if (!cached && !quiet) {
        error = err && err.status === 404 ? 'This cell is gone: its run was deleted, or it is older than the runs kept.' : errorMessage(err);
        view = null;
        drawError();
        return;
      }
    }
    if (!view) return;
    if (view.bugs && view.bugs.length) loadBugs();
    draw();
    findPass(view);
    scheduleLive();
    prefetch();
  }

  // The cells before and after the shown one are read ahead, so walking the list is at once.
  function prefetch() {
    const i = indexInList();
    for (const j of [i - 1, i + 1]) {
      const n = list[j];
      if (n && !details.has(keyOf(n.run, n.attempt))) detailOf(n.run, n.attempt).catch(() => {});
    }
  }

  function scheduleLive() {
    clearTimeout(liveTimer);
    if (!shown || !view || document.hidden) return;
    const reviewing = view.review && (view.review.state === 'pending' || view.review.state === 'running');
    if (!V.OPEN_ATTEMPT.has(view.state) && !reviewing) return;
    liveTimer = setTimeout(() => load({ quiet: true }), LIVE_EVERY);
  }

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && shown) load({ quiet: true });
  });

  /* --------------------------------------------------- the last pass */

  // findPass looks for the cell's latest passing attempt before this one:
  // an earlier attempt of the same run, else the test's history of runs.
  async function findPass(v) {
    const key = keyOf(v.run, v.id);
    if (pass && pass.for === key) return;
    pass = { for: key, state: 'loading' };
    let target = null;
    const same = (v.attempts || []).filter((a) => a.id !== v.id && a.attempt < v.attempt && a.final === 'pass')
      .sort((a, b) => b.attempt - a.attempt)[0];
    if (same) target = { run: v.run, attempt: same.id, label: 'Attempt ' + same.attempt + ' of this run' };
    if (!target) {
      try {
        const h = await historyOf(v.test);
        const runs = h.runs || [];
        const here = runs.find((r) => r.id === v.run);
        const known = ctx.runOf(v.run);
        const number = here ? here.number : known ? known.n : Infinity;
        const row = (h.grid || {})[v.cell_id] || {};
        for (const r of runs) {
          if (!(r.number < number)) continue;
          const g = row[r.id];
          if (g && g.verdict === 'pass' && g.attempt) {
            target = { run: r.id, attempt: g.attempt, label: 'Run ' + r.number };
            break;
          }
        }
      } catch { /* no history: nothing to compare */ }
    }
    if (!pass || pass.for !== key) return;
    if (!target) {
      pass = { for: key, state: 'none' };
      if (compare) zoom = false;
      draw();
      return;
    }
    try {
      const pv = await detailOf(target.run, target.attempt);
      if (!pass || pass.for !== key) return;
      pass = { for: key, state: 'ready', ...target, view: pv };
    } catch {
      if (pass && pass.for === key) pass = { for: key, state: 'none' };
    }
    if (view && keyOf(view.run, view.id) === key) draw();
  }

  /* ------------------------------------------------------------ shots */

  const actionOf = (v, n) => (v.actions || []).find((x) => x.n === n);

  // The screenshots of a step: those of its actions, then what it attached.
  function stepShots(v, step) {
    const out = [];
    for (const n of step.actions || []) {
      const act = actionOf(v, n);
      if (act && act.screenshot && !out.some((s) => s.name === act.screenshot)) out.push({ name: act.screenshot, act });
    }
    for (const name of step.attach || []) if (!out.some((s) => s.name === name)) out.push({ name, act: null });
    return out;
  }

  // allShots is every screenshot of the attempt in step order (step 0: no
  // step took it), each with its place in its step.
  function allShots(v) {
    const out = [];
    const seen = new Set();
    for (const step of v.steps || []) {
      const mine = stepShots(v, step).filter((s) => !seen.has(s.name));
      mine.forEach((s, i) => {
        seen.add(s.name);
        out.push({ ...s, step: step.n, i, of: mine.length });
      });
    }
    const rest = (v.actions || []).filter((a) => a.screenshot && !seen.has(a.screenshot));
    rest.forEach((a, i) => {
      seen.add(a.screenshot);
      out.push({ name: a.screenshot, act: a, step: 0, i, of: rest.length });
    });
    return out;
  }

  // stepOf is a step of the shown attempt by its number.
  const stepOf = (v, n) => (v.steps || []).find((x) => x.n === n) || null;

  // shotWords is a screenshot in words: its step's action from the catalog
  // ("Step 2 of 4: Open the first result, screenshot 1 of 2"); the tools'
  // own caption only for one outside the steps (the actions log has them all).
  function shotWords(s, v = view) {
    const n = s.of > 1 ? ', screenshot ' + (s.i + 1) + ' of ' + s.of : '';
    if (!s.step) return 'Outside the steps' + n + ': ' + (s.act ? s.act.summary || s.act.action : 'attached by the agent');
    return stepTitle(v, s.step) + n;
  }

  // stepTitle is "Step 2 of 4: Open the first result".
  function stepTitle(v, n) {
    const steps = (v && v.steps) || [];
    const step = steps.find((x) => x.n === n);
    return 'Step ' + n + (steps.length ? ' of ' + steps.length : '') + (step && step.do ? ': ' + step.do : '');
  }

  const fileOf = (v, name) => V.paths.file(v.run, v.id, name);

  // stepVerdict is a step's verdict as it counts (the reviewer's over the agent's).
  function stepVerdict(step) {
    return step.review && step.review.verdict ? step.review.verdict : step.verdict || '';
  }

  // liveStep is the step a running cell is at: the first not judged yet.
  function liveStep(v) {
    if (!V.OPEN_ATTEMPT.has(v.state) || v.state !== 'running') return 0;
    const s = (v.steps || []).find((x) => !x.verdict);
    return s ? s.n : 0;
  }

  // pickDefault chooses what a newly shown attempt shows first: the latest
  // screenshot of a running cell, else the first step that did not pass,
  // else the first step.
  function pickDefault(v, shots) {
    const steps = v.steps || [];
    if (V.OPEN_ATTEMPT.has(v.state)) {
      const last = shots.length - 1;
      return { step: last >= 0 ? shots[last].step : liveStep(v) || (steps[0] || {}).n || 0, shot: last };
    }
    const bad = steps.find((s) => { const x = stepVerdict(s); return x && !V.passed(x); }) || steps[0];
    const n = bad ? bad.n : 0;
    return { step: n, shot: shots.findIndex((s) => s.step === n) };
  }

  function syncSelection(v, shots) {
    const key = keyOf(v.run, v.id);
    if (sel.key !== key) {
      sel = { key, manual: false, ...pickDefault(v, shots) };
      zoom = false;
      return;
    }
    if (!sel.manual && V.OPEN_ATTEMPT.has(v.state) && shots.length) {
      sel.shot = shots.length - 1;
      sel.step = shots[sel.shot].step;
    }
    if (sel.shot >= shots.length) sel.shot = shots.length - 1;
  }

  function selectStep(n) {
    if (!view) return;
    const shots = allShots(view);
    sel.step = n;
    sel.shot = shots.findIndex((s) => s.step === n);
    sel.manual = true;
    zoom = false;
    draw();
  }

  function selectShot(i) {
    if (!view) return;
    const shots = allShots(view);
    if (!shots[i]) return;
    sel.shot = i;
    sel.step = shots[i].step;
    sel.manual = true;
    draw();
    const thumb = body.querySelector('[data-thumb="' + i + '"]');
    if (thumb) thumb.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    const stepRow = body.querySelector('[data-step-row="' + sel.step + '"]');
    if (stepRow) stepRow.scrollIntoView({ block: 'nearest' });
  }

  function stepShot(by) {
    if (!view) return;
    const shots = allShots(view);
    if (!shots.length) return;
    const from = sel.shot < 0 ? (by > 0 ? -1 : shots.length) : sel.shot;
    const next = from + by;
    if (next < 0 || next >= shots.length) return;
    selectShot(next);
  }

  /* -------------------------------------------------------------- list */

  function indexInList() {
    if (!want) return -1;
    return list.findIndex((x) => x.attempt === want.id && (!x.run || x.run === want.run));
  }

  function fallbackList() {
    if (!want || indexInList() >= 0) return;
    const rows = rowsNow(want.run);
    const own = rows.map((r) => ({ attempt: r.id, run: want.run }));
    if (own.some((x) => x.attempt === want.id)) list = own;
  }

  function walk(by) {
    const i = indexInList();
    const next = list[i + by];
    if (i < 0 || !next) return;
    ctx.go({ cell: next.attempt, run: next.run || want.run });
  }

  /* ---------------------------------------------------------- drawing */

  function setHead(title, sub) {
    const t = $('cellTitle');
    if (t.textContent !== title) t.textContent = title;
    const s = $('cellSub');
    if (s.textContent !== sub) s.textContent = sub;
  }

  function drawNav() {
    fallbackList();
    const i = indexInList();
    const has = i >= 0 && list.length > 1;
    const prev = has && i > 0;
    const next = has && i < list.length - 1;
    const where = has ? (i + 1) + ' of ' + list.length : '';
    morph(nav, html`${has ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-walk="-1" aria-label="Previous cell" data-tip="Previous cell ([ or k)" aria-keyshortcuts="[ k"${prev ? '' : raw(' aria-disabled="true"')}>${raw(icon('chevronLeft', 20))}</button>
      <span class="so-cell__count" aria-label="${'Cell ' + where}">${where}</span>
      <button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-walk="1" aria-label="Next cell" data-tip="Next cell (] or j)" aria-keyshortcuts="] j"${next ? '' : raw(' aria-disabled="true"')}>${raw(icon('chevronRight', 20))}</button>` : ''}
      ${WIDE.matches ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-keys aria-haspopup="dialog" aria-expanded="false" aria-label="Keys" data-tip="Keys (?)" aria-keyshortcuts="?">${raw(icon('keyboard', 20))}</button>` : ''}`);
  }

  function drawLoading() {
    setHead('Reading the cell', '');
    drawNav();
    morph(body, html`<div class="ds-loading" role="status" data-ds-key="loading"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the cell</div>`);
  }

  function drawError() {
    setHead('Cell', '');
    drawNav();
    morph(body, html`<div class="ds-alert ds-alert--error" role="alert" data-ds-key="error">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${error}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-reload>Try again</button></div>`);
  }

  function draw() {
    const v = view;
    if (!shown || !v) return;
    const shots = allShots(v);
    syncSelection(v, shots);
    const r = rowOf(v);
    setHead(v.title || v.test, [V.dimsWords(v.dims) || v.cell, (v.attempts || []).length > 1 ? 'attempt ' + v.attempt : ''].filter(Boolean).join(' · '));
    drawNav();
    const wide = WIDE.matches;
    morph(body, html`<div class="so-cell__page" data-layout="${wide ? 'wide' : 'narrow'}">
      ${band(v, r)}
      ${wide ? html`<div class="so-cell__split" data-ds-key="split">
          <section class="so-cell__steps" aria-labelledby="cellStepsTitle">
            ${notes(v)}
            <h3 class="ds-card__subtitle" id="cellStepsTitle">${stepsTitle(v)}</h3>
            ${stepList(v, shots)}
          </section>
          ${viewer(v, shots)}
        </div>
        ${actionsSection(v)}
        ${detailsSection(v, r)}`
        : html`${partSwitch(v)}
        <div class="so-cell__part" id="cellPart" role="tabpanel" aria-labelledby="${'cellPart-' + part}" data-ds-key="${'part-' + part}">
          ${part === 'steps' ? html`${compareSwitch()}${stepBlocks(v, shots)}` : part === 'actions' ? actionsSection(v) : detailsSection(v, r)}
        </div>`}
    </div>`);
    if (!wide) wirePartSwitch();
    // The steps are one stop of Tab; the arrow keys walk them and show each one's screenshots.
    const list = body.querySelector('.so-cell-steps');
    if (list && !rovings.has(list)) {
      rovings.add(list);
      DS.dom.roving(list, { items: '[data-step]', orientation: 'vertical', wrap: false });
    }
  }

  function stepsTitle(v) {
    const steps = v.steps || [];
    const judged = steps.filter((s) => s.verdict).length;
    return V.OPEN_ATTEMPT.has(v.state) && steps.length ? 'Steps · ' + judged + ' of ' + steps.length + ' judged' : 'Steps';
  }

  /* ---------------------------------------------------- the verdict band */

  // provenance is who gave the verdict that counts: "The agent said Fail ·
  // the reviewer made it Issue · you made it Pass".
  function provenance(v) {
    const parts = [];
    if (v.verdict) parts.push('the agent said ' + V.verdictWord(v.verdict));
    else if (V.OPEN_ATTEMPT.has(v.state)) parts.push(v.state === 'running' ? 'the agent is at work' : 'the agent has not started');
    else parts.push('the agent gave no verdict');
    const review = v.review || null;
    if (v.reviewed && v.reviewed !== v.verdict) parts.push('the reviewer made it ' + V.verdictWord(v.reviewed));
    else if (review && review.state === 'done') parts.push('the reviewer kept it');
    else if (review && (review.state === 'pending' || review.state === 'running')) parts.push('a reviewer looks at it');
    if (v.override) parts.push('you made it ' + V.verdictWord(v.override.verdict));
    const text = parts.join(' · ');
    return text.charAt(0).toUpperCase() + text.slice(1);
  }

  function changeLine(v, r) {
    if (!r) return '';
    const bits = [];
    const prev = r.prev;
    const prevLink = prev && prev.attempt
      ? html`<button class="ds-link-btn" type="button" data-open-prev="${prev.attempt}" data-prev-run="${prev.run}">${'Run ' + (prev.number || '?')}</button>`
      : prev ? 'Run ' + (prev.number || '?') : '';
    if (r.change === 'regressed' || r.change === 'fixed') {
      bits.push(html`<span>${(WAS[prev && prev.verdict] || 'it was different').replace(/^i/, 'I')}${prev ? html` in ${prevLink}` : ''}</span>`);
    } else if (r.change === 'new') {
      bits.push(html`<span>No earlier run had this cell</span>`);
    } else if (prev && prev.verdict && r.final) {
      bits.push(html`<span>${'The same as in '}${prevLink}${' (' + V.verdictWord(prev.verdict) + ')'}</span>`);
    }
    if (r.since && r.final && !V.passed(r.final) && r.since.run !== v.run) bits.push(html`<span>${V.sinceWords(r.final, r.since.number).replace(/^./, (c) => c.toUpperCase())}</span>`);
    if (!bits.length) return '';
    return html`<p class="ds-meta so-cell__change"><span class="ds-parts"><span class="ds-parts__list">${bits}</span></span></p>`;
  }

  // changeChip is how the cell moved against the run before, as a chip.
  function changeChip(r) {
    if (!r || !V.CHANGE[r.change]) return '';
    const tone = r.change === 'regressed' ? 'error' : r.change === 'fixed' ? 'ok' : '';
    return V.chip({ word: V.CHANGE[r.change].word, glyph: V.CHANGE[r.change].glyph, tone });
  }

  function attemptSwitch(v) {
    const all = v.attempts || [];
    if (all.length < 2) return '';
    return html`<div class="ds-segmented so-cell__attempts" role="radiogroup" aria-label="Attempts of this cell" data-ds-key="attempts">${all.map((a) => {
      const on = a.id === v.id;
      const label = 'Attempt ' + a.attempt + ' · ' + (a.final ? V.verdictWord(a.final) : V.attemptWord(a.state));
      return html`<button class="ds-segmented__option" type="button" role="radio" aria-checked="${on ? 'true' : 'false'}"${on ? '' : raw(' tabindex="-1"')} data-attempt="${a.id}" data-label="${label}">${label}</button>`;
    })}</div>`;
  }

  function liveLine(v, r) {
    if (v.state !== 'running') return '';
    const live = r && r.live;
    const at = live ? live.step : liveStep(v);
    const last = (v.actions || [])[(v.actions || []).length - 1];
    const when = live && live.at ? live.at : last && last.at ? Date.parse(last.at) : 0;
    return html`<p class="ds-meta so-cell__live" role="status">${at ? stepTitle(v, at) : 'Every step judged'}${when ? ' · last action ' + V.ago(when) : ''}</p>`;
  }

  // problem: the shown attempt did not pass (or ended without a verdict),
  // so fixing it is an action of its own.
  const problem = (v) => (v.final ? !V.passed(v.final) : !V.OPEN_ATTEMPT.has(v.state));

  // moreItems is what the More menu (the action sheet on a phone) holds.
  function moreItems(v) {
    const open = V.OPEN_ATTEMPT.has(v.state);
    const item = (glyph, label, what) => html`<button class="ds-menu__item" role="menuitem" type="button" data-cell-act="${what}">${raw(icon(glyph, 20))}<span class="ds-menu__label">${label}</span></button>`;
    return [
      v.chat ? item('external', 'Open the agent’s transcript', 'transcript') : '',
      open ? '' : item('play', 'Run this cell in a new run', 'rerun'),
    ].filter(Boolean);
  }

  // The actions follow the verdict: on a problem, check the verdict
  // (Override) or try it again (Retry), then fix it in a chat; a pass has
  // nothing to fix; a cell still at work has nothing to judge yet. What is
  // left goes in More, with its words (a menu, an action sheet on a phone).
  function actions(v) {
    const open = V.OPEN_ATTEMPT.has(v.state);
    const more = moreItems(v);
    return html`<div class="so-cell__actions" role="group" aria-label="Actions of this cell">
      ${open ? '' : html`<button class="ds-btn" type="button" data-cell-act="override" aria-haspopup="dialog" aria-expanded="false">${raw(icon('pencil', 16))}${v.override ? 'Change your verdict' : 'Override verdict'}</button>
        <button class="ds-btn" type="button" data-cell-act="retry" data-tip="Run this cell once more in this run">${raw(icon('sync', 16))}Retry</button>`}
      ${!open && problem(v) ? html`<button class="ds-btn" type="button" data-cell-act="chat" aria-haspopup="dialog">${raw(icon('chat', 16))}Fix in a chat</button>` : ''}
      ${more.length ? html`<button class="ds-btn ds-btn--text" type="button" data-cell-more aria-haspopup="${PHONE.matches ? 'dialog' : 'menu'}" aria-expanded="false" aria-controls="${PHONE.matches ? 'cellMoreSheet' : 'cellMoreMenu'}">More${raw(icon('chevronDown', 16))}</button>` : ''}
    </div>`;
  }

  // bugChips are the bugs the analyst filed the cell under, each with its
  // severity and status, opening the bug: "B-5 · Major · open".
  function bugChips(v) {
    const ids = v.bugs || [];
    if (!ids.length) return '';
    const known = bugs && bugs.repo === state.repo ? bugs.list : [];
    return html`<div class="ds-chips so-cell__bugs" role="group" aria-label="Bugs of this cell">${ids.map((id) => {
      const bug = known.find((b) => b.id === id);
      const words = bug ? id + ' · ' + V.severityWord(bug.severity) + ' · ' + V.bugStateWords(bug).toLowerCase() : id;
      return html`<button class="ds-chip" type="button" data-go-bug="${id}" data-ds-key="${'bug-' + id}"${bug ? raw(html` data-tip="${bug.title}"`) : ''} aria-label="${'Bug ' + words + (bug ? ': ' + bug.title : '') + '. Open it'}"><span class="ds-chip__body">${raw(icon('flag', 18, 'ds-chip__icon'))}<span class="ds-chip__label">${words}</span></span></button>`;
    })}</div>`;
  }

  function band(v, r) {
    return html`<section class="so-cell__band" aria-label="Verdict" data-ds-key="band">
      <div class="so-cell__verdict">
        <div class="so-cell__says">
          ${v.final ? V.verdictChip(v.final) : V.cellChip({ state: v.state, wait: v.wait })}
          ${changeChip(r)}
          <p class="so-cell__who">${provenance(v)}</p>
        </div>
        ${changeLine(v, r)}
        ${liveLine(v, r)}
        ${bugChips(v)}
        ${attemptSwitch(v)}
      </div>
      ${actions(v)}
      ${WIDE.matches ? '' : notes(v)}
    </section>`;
  }

  function notes(v) {
    const review = v.review || null;
    const parts = [
      v.override ? html`<div class="ds-alert" data-ds-key="override">${raw(icon('pencil', 20))}<div class="ds-alert__text"><p class="ds-alert__title">${'You made it ' + V.verdictWord(v.override.verdict) + (v.override.at ? ' · ' + V.ago(v.override.at) : '')}</p>${v.override.note ? html`<p class="so-break">${v.override.note}</p>` : ''}</div></div>` : '',
      review && review.note ? html`<div class="ds-alert" data-ds-key="review">${raw(icon('eye', 20))}<div class="ds-alert__text"><p class="ds-alert__title">${v.reviewed && v.reviewed !== v.verdict ? 'The reviewer made it ' + V.verdictWord(v.reviewed) : 'The reviewer'}</p><p class="so-break">${review.note}</p></div></div>` : '',
      v.state === 'waiting' && v.wait ? html`<div class="ds-alert" data-ds-key="wait">${raw(icon('clock', 20))}<span class="ds-alert__text so-break">${v.wait}</span></div>` : '',
      v.end_reason ? html`<div class="ds-alert ds-alert--warning" data-ds-key="end">${raw(icon('alert', 20))}<div class="ds-alert__text"><p class="ds-alert__title">${'Why it ended ' + (v.state === 'ended' ? 'early' : V.attemptWord(v.state).toLowerCase())}</p><p class="so-break">${v.end_reason}</p></div></div>` : '',
      v.summary ? html`<p class="so-break so-cell__summary" data-ds-key="summary">${v.summary}</p>` : '',
    ].filter(Boolean);
    return parts.length ? html`<div class="so-cell__notes">${parts}</div>` : '';
  }

  /* ---------------------------------------------------------- the steps */

  function stepAria(step) {
    const verdict = stepVerdict(step);
    return 'Step ' + step.n + ': ' + step.do + ' ' + (verdict ? V.verdictWord(verdict) : 'not judged yet');
  }

  function stepChip(v, step) {
    const verdict = stepVerdict(step);
    if (verdict) return V.verdictChip(verdict);
    if (liveStep(v) === step.n) return V.chip({ word: 'At work', spin: true });
    return html`<span class="ds-status">${raw(icon('minimize', 16))}<span>${V.OPEN_ATTEMPT.has(v.state) ? 'Not yet' : 'Not judged'}</span></span>`;
  }

  function reviewNote(step) {
    const r = step.review;
    if (!r || (!r.note && !r.verdict)) return '';
    const changed = r.verdict && step.verdict && r.verdict !== step.verdict;
    return html`<span class="so-cell-step__note">${raw(icon('eye', 16))}<span><strong>${changed ? 'The reviewer made it ' + V.verdictWord(r.verdict) + ' (the agent said ' + V.verdictWord(step.verdict) + ')' : 'The reviewer'}</strong>${r.note ? ': ' + r.note : ''}</span></span>`;
  }

  // The wide layout's steps: rows that choose the step whose screenshots show.
  function stepList(v, shots) {
    const steps = v.steps || [];
    if (!steps.length) return html`<p class="ds-muted">This test has no steps.</p>`;
    return html`<ol class="ds-list ds-list--interactive so-cell-steps" aria-label="Steps: the arrow keys move between them" data-ds-key="steplist">${steps.map((step) => {
      const mine = shots.filter((s) => s.step === step.n).length;
      const current = sel.step === step.n;
      return html`<li class="ds-list__item so-cell-step" data-ds-key="${'s' + step.n}" data-step-row="${step.n}">
        <span class="ds-list__text">
          <span class="ds-list__title"><button class="ds-list__action" type="button" data-step="${step.n}"${current ? raw(' aria-current="step"') : ''} aria-label="${stepAria(step) + (mine ? ', ' + V.plural(mine, 'screenshot') : '')}">${step.n + '. ' + step.do}</button></span>
          <span class="ds-list__meta so-break">${'Expect: ' + step.expect}</span>
          ${step.observed ? html`<span class="so-cell-step__seen so-break">${step.observed}</span>` : ''}
          ${reviewNote(step)}
          ${mine || step.at ? html`<span class="ds-list__meta">${mine ? html`<span class="so-tests-nowrap">${raw(icon('image', 16))}${V.plural(mine, 'screenshot')}</span>` : ''}${mine && step.at ? ' · ' : ''}${step.at ? V.clock(step.at) : ''}</span>` : ''}
        </span>
        ${stepChip(v, step)}
      </li>`;
    })}</ol>`;
  }

  function compareButton() {
    const ready = pass && pass.state === 'ready';
    const why = !pass || pass.state === 'loading' ? 'Looking for the last pass' : 'No earlier attempt of this cell passed';
    return html`<button class="ds-btn ds-btn--quiet ds-btn--sm" type="button" data-compare aria-pressed="${compare && ready ? 'true' : 'false'}" aria-keyshortcuts="c"${ready ? raw(html` data-tip="${'Show ' + pass.label + '’s screenshot beside this one (c)'}"`) : raw(html` aria-disabled="true" data-tip="${why}"`)}>${raw(icon('columns', 16))}Compare with the last pass</button>`;
  }

  // compareShot is the last pass's screenshot of the same place: the same
  // step, the same screenshot of it where it has as many, else its first.
  function compareShot(s) {
    if (!(pass && pass.state === 'ready' && pass.view && s)) return null;
    const theirs = allShots(pass.view).filter((x) => x.step === s.step);
    return theirs[s.i] || theirs[0] || null;
  }

  function figure(v, s, { label = '', big = true } = {}) {
    const src = fileOf(v, s.name);
    return html`<figure class="so-cell-shot" data-ds-key="${'fig-' + v.id + '-' + s.name + (label ? '-pass' : '')}">
      ${label ? html`<figcaption class="so-cell-shot__label"><span class="ds-tag ds-tag--neutral">${label}</span></figcaption>` : ''}
      <button class="so-cell-shot__frame ds-pressable" type="button" data-zoom aria-pressed="${zoom ? 'true' : 'false'}" aria-label="${(zoom ? 'Fit to the window: ' : 'Show at its own size: ') + shotWords(s)}" aria-keyshortcuts="z">
        <img class="so-cell-shot__img" src="${src}" alt="${shotWords(s)}" decoding="async" data-ds-key="${'img-' + v.id + '-' + s.name}"${big ? '' : raw(' loading="lazy"')}>
      </button>
    </figure>`;
  }

  // The wide layout's screenshot panel: the chosen one large, a bar over it,
  // and the film strip of every screenshot of the cell under it.
  function viewer(v, shots) {
    const s = shots[sel.shot];
    const step = (v.steps || []).find((x) => x.n === sel.step);
    const two = !!(compare && s && pass && pass.state === 'ready');
    const theirs = two ? compareShot(s) : null;
    // The step's action names the screenshot; the tools' caption is in the actions log.
    const title = s ? (s.step ? stepTitle(v, s.step) : 'Outside the steps')
      : step ? stepTitle(v, step.n) : 'Screenshots';
    const meta = s ? [s.of > 1 ? 'Screenshot ' + (s.i + 1) + ' of ' + s.of : '', !s.step && s.act ? s.act.summary || s.act.action : '',
      s.act && s.act.at ? V.clock(Date.parse(s.act.at)) : '', s.act ? s.act.url : 'Attached by the agent'].filter(Boolean).join(' · ') : '';
    return html`<section class="so-cell-viewer" aria-labelledby="cellShotTitle" data-ds-key="viewer">
      <div class="so-cell-viewer__bar">
        <div class="so-cell-viewer__caption">
          <h3 class="ds-card__subtitle" id="cellShotTitle">${title}</h3>
          ${meta ? html`<p class="ds-meta so-break">${meta}</p>` : ''}
        </div>
        <div class="so-cell-viewer__tools">
          ${compareButton()}
          ${s ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-zoom aria-pressed="${zoom ? 'true' : 'false'}" aria-label="Show at its own size" data-tip="${zoom ? 'Fit to the window (z)' : 'Show at its own size (z)'}" aria-keyshortcuts="z">${raw(icon(zoom ? 'minimize' : 'search', 18))}</button>
          <a class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" href="${fileOf(v, s.name)}" target="_blank" rel="noopener" aria-label="Open the screenshot in a new tab" data-tip="Open in a new tab (o)" aria-keyshortcuts="o">${raw(icon('external', 18))}</a>` : ''}
          <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-shot-step="-1" aria-label="Previous screenshot" data-tip="Previous screenshot (←)" aria-keyshortcuts="ArrowLeft"${sel.shot > 0 ? '' : raw(' aria-disabled="true"')}>${raw(icon('chevronLeft', 18))}</button>
          <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-shot-step="1" aria-label="Next screenshot" data-tip="Next screenshot (→)" aria-keyshortcuts="ArrowRight"${sel.shot >= 0 && sel.shot < shots.length - 1 ? '' : raw(' aria-disabled="true"')}>${raw(icon('chevronRight', 18))}</button>
        </div>
      </div>
      <div class="${'so-cell-viewer__stage' + (two ? ' so-cell-viewer__stage--two' : '')}" data-zoomed="${zoom ? 'true' : 'false'}" data-ds-key="stage">
        ${s ? html`${figure(v, s, { label: two ? 'This attempt' : '' })}${two
          ? theirs ? figure(pass.view, theirs, { label: 'Last pass · ' + pass.label }) : html`<div class="ds-empty so-cell-shot" data-ds-key="nopass"><p class="ds-empty__title">${'No screenshot of this step in ' + pass.label}</p></div>` : ''}`
          : html`<div class="ds-empty" data-ds-key="noshot">
            <span class="ds-empty__disc" aria-hidden="true">${raw(icon(V.OPEN_ATTEMPT.has(v.state) ? 'clock' : 'image', 24))}</span>
            <p class="ds-empty__title">${V.OPEN_ATTEMPT.has(v.state) && !(step && step.verdict) ? 'No screenshot yet' : 'No screenshot of this step'}</p>
            <p class="ds-empty__text">${shots.length ? 'The strip below has the cell’s ' + V.plural(shots.length, 'screenshot') + '.' : V.OPEN_ATTEMPT.has(v.state) ? 'They show up here as the agent takes them.' : 'The agent took none in this cell.'}</p>
          </div>`}
      </div>
      ${shots.length ? strip(v, shots) : ''}
    </section>`;
  }

  function strip(v, shots) {
    return html`<ol class="so-cell-strip" aria-label="${'Every screenshot of the cell, ' + shots.length}" data-ds-key="strip">${shots.map((s, i) => {
      const first = i === 0 || shots[i - 1].step !== s.step;
      return html`<li class="so-cell-strip__item" data-ds-key="${'t-' + s.name}">
        <span class="${'so-cell-strip__step' + (first ? '' : ' so-cell-strip__step--same')}" aria-hidden="true">${s.step ? 'Step ' + s.step : 'Other'}</span>
        <button class="so-cell-strip__thumb ds-pressable" type="button" data-thumb="${i}" aria-label="${shotWords(s)}"${i === sel.shot ? raw(' aria-current="true"') : ''}>
          <img src="${fileOf(v, s.name)}" alt="" loading="lazy" decoding="async">
        </button>
      </li>`;
    })}</ol>`;
  }

  /* ------------------------------------------------ the narrow layout */

  function partSwitch(v) {
    const actions = (v.actions || []).length;
    const errors = (v.actions || []).filter((a) => a.error).length;
    return html`<div class="ds-tabs so-cell__tabs" role="tablist" aria-label="Parts of the cell" id="cellParts" data-ds-key="parts">${PARTS.map((p) => {
      const on = p.id === part;
      const n = p.id === 'actions' ? actions : 0;
      return html`<button class="ds-tabs__tab" role="tab" type="button" id="${'cellPart-' + p.id}" aria-selected="${on ? 'true' : 'false'}" aria-controls="cellPart"${on ? '' : raw(' tabindex="-1"')} data-part="${p.id}" data-label="${p.label}">${p.label}${n ? html`<span class="${errors ? 'ds-badge ds-badge--error' : 'ds-badge ds-badge--neutral'}">${n}</span>` : ''}</button>`;
    })}</div>`;
  }

  let partIndicator = null;
  function wirePartSwitch() {
    const tabs = $('cellParts');
    if (!tabs) return;
    if (!tabs.dataset.wired) {
      tabs.dataset.wired = '1';
      partIndicator = DS.indicator.createIndicator(tabs, { active: '[aria-selected="true"]', className: 'ds-tabs__indicator', reveal: true });
      DS.dom.roving(tabs);
    } else if (partIndicator) partIndicator.update();
  }

  function compareSwitch() {
    if (!pass || pass.state !== 'ready') return '';
    return html`<div class="so-cell__compare" data-ds-key="compare">${compareButton()}</div>`;
  }

  // The narrow layout's steps: each with its screenshots at full width, in
  // a strip that swipes; compared, each beside the last pass's.
  function stepBlocks(v, shots) {
    const steps = v.steps || [];
    const outside = shots.filter((s) => s.step === 0);
    if (!steps.length && !outside.length) return html`<p class="ds-muted">This test has no steps.</p>`;
    return html`<ol class="so-cell-blocks" aria-label="Steps" data-ds-key="blocks">${steps.map((step) => {
      const mine = shots.filter((s) => s.step === step.n);
      return html`<li class="so-cell-block" data-ds-key="${'b' + step.n}" data-step-row="${step.n}">
        <div class="so-cell-block__head">
          <h3 class="so-cell-block__title">${step.n + '. ' + step.do}</h3>
          ${stepChip(v, step)}
        </div>
        <p class="ds-meta so-break">${'Expect: ' + step.expect}</p>
        ${step.observed ? html`<p class="so-break">${step.observed}</p>` : ''}
        ${reviewNote(step)}
        ${comparing() && mine.length ? pairs(v, mine) : swipe(v, mine, 'Screenshots of step ' + step.n)}
      </li>`;
    })}${outside.length ? html`<li class="so-cell-block" data-ds-key="b0">
        <div class="so-cell-block__head"><h3 class="so-cell-block__title">Outside the steps</h3></div>
        ${swipe(v, outside, 'Screenshots outside the steps')}
      </li>` : ''}</ol>`;
  }

  function swipe(v, mine, label) {
    if (!mine.length) return '';
    return html`<div class="so-cell-swipe" role="region" aria-label="${label}" tabindex="0" data-ds-key="${'sw-' + label}">${mine.map((s) => html`<figure class="so-cell-swipe__item" data-ds-key="${'f-' + v.id + '-' + s.name}">
      <a class="so-cell-swipe__link ds-pressable" href="${fileOf(v, s.name)}" target="_blank" rel="noopener" aria-label="${'Open full size: ' + shotWords(s)}">
        <img src="${fileOf(v, s.name)}" alt="${shotWords(s)}" loading="lazy" decoding="async">
      </a>
      <figcaption class="ds-meta so-break">${[s.of > 1 ? (s.i + 1) + ' of ' + s.of : '', !s.step && s.act ? s.act.summary || s.act.action : '', s.act && s.act.at ? V.clock(Date.parse(s.act.at)) : '', s.act ? '' : 'attached by the agent'].filter(Boolean).join(' · ')}</figcaption>
    </figure>`)}</div>`;
  }

  const comparing = () => !!(compare && pass && pass.state === 'ready');

  // pairs is a step's screenshots beside the last pass's of the same place,
  // half the width each, labelled.
  function pairs(v, mine) {
    const half = (label, src, alt, key) => html`<figure class="so-cell-pair__item" data-ds-key="${key}">
      <figcaption class="so-cell-shot__label"><span class="ds-tag ds-tag--neutral">${label}</span></figcaption>
      <a class="so-cell-swipe__link ds-pressable" href="${src}" target="_blank" rel="noopener" aria-label="${'Open full size: ' + alt}">
        <img src="${src}" alt="${alt}" loading="lazy" decoding="async">
      </a>
    </figure>`;
    return html`<div class="so-cell-pairs" data-ds-key="pairs">${mine.map((s) => {
      const theirs = compareShot(s);
      return html`<div class="so-cell-pair" data-ds-key="${'pair-' + s.name}">
        ${half('This attempt', fileOf(v, s.name), shotWords(s), 'mine-' + v.id + '-' + s.name)}
        ${theirs ? half('Last pass · ' + pass.label, fileOf(pass.view, theirs.name), 'The last pass, ' + shotWords(theirs), 'pass-' + pass.attempt + '-' + theirs.name)
          : html`<p class="ds-meta so-cell-pair__none" data-ds-key="nopass">${'No screenshot of this step in ' + pass.label + '.'}</p>`}
      </div>`;
    })}</div>`;
  }

  /* ------------------------------------------------------- the actions */

  function stepOfAction(v, n) {
    const step = (v.steps || []).find((s) => (s.actions || []).includes(n));
    return step ? step.n : 0;
  }

  function actionsSection(v) {
    const acts = v.actions || [];
    const shots = allShots(v);
    const errors = acts.filter((a) => a.error).length;
    const wide = WIDE.matches;
    const timed = acts.some((a) => a.ms);
    const head = html`<div class="ds-card__row">
        <h3 class="ds-card__title" id="cellActsTitle">Actions</h3>
        <span class="ds-meta">${acts.length ? V.plural(acts.length, 'action') + (errors ? ' · ' + V.plural(errors, 'error') : '') : ''}</span>
      </div>`;
    if (!acts.length) {
      return html`<section class="ds-card so-cell__section" aria-labelledby="cellActsTitle" data-ds-key="acts">${head}
        <p class="ds-muted">${V.OPEN_ATTEMPT.has(v.state) ? 'The tools’ actions show up here as they happen.' : 'The tools wrote no action for this cell.'}</p></section>`;
    }
    const shotLink = (a) => {
      const i = shots.findIndex((s) => s.name === a.screenshot);
      if (!a.screenshot) return '';
      return wide && i >= 0
        ? html`<button class="ds-link-btn" type="button" data-thumb="${i}" aria-label="${'Show the screenshot of action ' + a.n}">${raw(icon('image', 16))}<span class="ds-sr">Screenshot</span></button>`
        : html`<a href="${fileOf(v, a.screenshot)}" target="_blank" rel="noopener" aria-label="${'Open the screenshot of action ' + a.n}">${raw(icon('image', 16))}</a>`;
    };
    const what = (a) => html`<span class="so-break">${a.summary || a.action}</span>${a.url ? html`<small class="so-tests__sub ds-meta so-break">${a.url}</small>` : ''}${a.error ? html`<span class="so-tests__sub"><span class="ds-status ds-status--error">${raw(icon('alertCircle', 16))}<span>Error</span></span><span class="so-break">${a.error}</span></span>` : ''}`;
    const when = (a) => (a.at ? V.clock(Date.parse(a.at)) : '');
    if (wide) {
      return html`<section class="ds-card so-cell__section" aria-labelledby="cellActsTitle" data-ds-key="acts">${head}
        <div class="ds-table-wrap"><table class="ds-table ds-table--dense so-cell-acts">
          <caption class="ds-sr">Every action the tools took in this cell, in order</caption>
          <thead><tr><th scope="col" class="ds-num">#</th><th scope="col">Step</th><th scope="col">Time</th><th scope="col">Action</th><th scope="col">What happened</th>${timed ? html`<th scope="col" class="ds-num">Took</th>` : ''}<th scope="col"><span class="ds-sr">Screenshot</span></th></tr></thead>
          <tbody>${acts.map((a) => {
            const step = stepOfAction(v, a.n);
            return html`<tr data-ds-key="${'a' + a.n}">
              <td class="ds-num">${String(a.n)}</td>
              <td>${step ? String(step) : html`<span class="ds-muted">–</span>`}</td>
              <td><span class="so-tests-nowrap">${when(a)}</span></td>
              <td><span class="so-tests-nowrap">${a.tool ? a.tool + ' · ' : ''}${a.action}</span>${a.interaction ? html`<small class="so-tests__sub ds-meta">interaction</small>` : ''}</td>
              <td>${what(a)}</td>
              ${timed ? html`<td class="ds-num">${a.ms ? V.span(a.ms) || a.ms + ' ms' : ''}</td>` : ''}
              <td>${shotLink(a)}</td>
            </tr>`;
          })}</tbody>
        </table></div>
      </section>`;
    }
    return html`<section class="ds-card so-cell__section" aria-labelledby="cellActsTitle" data-ds-key="acts">${head}
      <ol class="ds-list so-cell-actlist" aria-label="Actions">${acts.map((a) => {
        const step = stepOfAction(v, a.n);
        return html`<li class="ds-list__item" data-ds-key="${'a' + a.n}">
          <span class="ds-list__text">
            <span class="ds-list__title">${a.n + '. ' + (a.tool ? a.tool + ' · ' : '') + a.action}</span>
            <span class="ds-list__meta">${[step ? 'step ' + step : 'outside the steps', when(a), a.ms ? V.span(a.ms) || a.ms + ' ms' : '', a.interaction ? 'interaction' : ''].filter(Boolean).join(' · ')}</span>
            <span>${what(a)}</span>
          </span>
          ${a.screenshot ? html`<span class="ds-list__actions">${shotLink(a)}</span>` : ''}
        </li>`;
      })}</ol>
    </section>`;
  }

  /* ------------------------------------------------------- the details */

  function detailsSection(v, r) {
    const role = v.role || {};
    const model = role.model ? (harnesses.modelOf(role.harness, role.model) || {}).label || role.model : '';
    const took = v.started_at ? (v.ended_at || (V.OPEN_ATTEMPT.has(v.state) ? Date.now() : 0)) - v.started_at : 0;
    const facts = [
      ['Test', html`<a href="${V.paths.page({ repo: state.repo, view: 'tests', test: v.test })}" data-go-test="${v.test}">${v.title || v.test}</a>${v.title ? html` <span class="ds-meta">${v.test}</span>` : ''}`],
      ...(v.dims || []).map((d) => [V.titleCase(d.name), d.label ? d.label + ' (' + d.id + ')' : d.id]),
      ['Priority', [v.priority, ...(v.tags || []).map((t) => '#' + t)].filter(Boolean).join(' · ')],
      ['Run', (() => { const run = ctx.runOf(v.run); return run ? V.runName(run) : v.run; })()],
      ['Attempt', 'Attempt ' + v.attempt + ' · ' + V.attemptWord(v.state) + (v.retried ? ' · retried' : '')],
      ['Agent', [harnesses.label(role.harness), model, role.effort ? harnesses.effortLabel(role.effort) : ''].filter(Boolean).join(' · ')],
      ['Account', role.account ? accounts.labelOf(role.account) || role.account : ''],
      ['Lease', (v.leases || []).map((l) => (l.label || 'account ' + l.member) + ' (' + l.tool + ')').join(', ')],
      ['Queued', V.whenLong(v.queued_at)],
      ['Started', V.whenLong(v.started_at)],
      ['Ended', V.whenLong(v.ended_at)],
      ['Took', V.span(took)],
      ['Waited', V.span(v.waited_ms)],
    ].filter(([, value]) => value);
    const links = Object.entries(v.links || {});
    const files = v.files || [];
    const known = bugs && bugs.repo === state.repo ? bugs.list : [];
    return html`<div class="so-cell__details" data-ds-key="details">
      <section class="ds-card so-cell__section" aria-labelledby="cellFactsTitle">
        <h3 class="ds-card__title" id="cellFactsTitle">Details</h3>
        <dl class="ds-facts ds-facts--rows">${facts.map(([k, val]) => html`<div><dt>${k}</dt><dd class="so-break">${val}</dd></div>`)}</dl>
      </section>
      <div class="so-cell__aside">
        ${(v.bugs || []).length ? html`<section class="ds-card so-cell__section" aria-labelledby="cellBugsTitle">
          <h3 class="ds-card__title" id="cellBugsTitle">Bugs</h3>
          <ul class="ds-list ds-list--interactive" aria-label="Bugs of this cell">${v.bugs.map((id) => {
            const bug = known.find((b) => b.id === id);
            return html`<li class="ds-list__item" data-ds-key="${'bug-' + id}">
              <span class="ds-list__text">
                <span class="ds-list__title"><a class="ds-list__action" href="${V.paths.page({ repo: state.repo, view: 'bugs', bug: id })}" data-go-bug="${id}">${bug ? id + ' · ' + bug.title : id}</a></span>
                ${bug ? html`<span class="ds-list__meta">${[V.bugStateWords(bug), bug.runs ? 'seen in ' + V.plural(bug.runs.length, 'run') : ''].filter(Boolean).join(' · ')}</span>` : ''}
              </span>
              ${bug ? V.severityChip(bug.severity) : ''}
            </li>`;
          })}</ul>
        </section>` : ''}
        <section class="ds-card so-cell__section" aria-labelledby="cellLinksTitle">
          <h3 class="ds-card__title" id="cellLinksTitle">Links and logs</h3>
          ${links.length || files.length || v.chat ? html`<ul class="ds-plain-list so-cell__links">
            ${v.chat ? html`<li><a href="${'/#open=' + encodeURIComponent(v.chat)}" target="_blank" rel="noopener">The agent’s transcript${raw(icon('external', 16))}</a></li>` : ''}
            ${links.map(([label, url]) => html`<li><a href="${url}" target="_blank" rel="noopener noreferrer" class="so-break">${label}${raw(icon('external', 16))}</a></li>`)}
            ${files.map((name) => html`<li><a href="${fileOf(v, name)}" target="_blank" rel="noopener" class="so-break">${raw(icon('fileText', 16))}${name.replace(/^logs\//, '')}</a></li>`)}
          </ul>` : html`<p class="ds-muted">${V.OPEN_ATTEMPT.has(v.state) ? 'Links and logs show up here once the agent writes them.' : 'The cell left no link and no log.'}</p>`}
        </section>
      </div>
    </div>`;
  }

  /* -------------------------------------------------------- actions */

  async function retry(button) {
    const v = view;
    if (!v) return;
    try {
      const made = await M.busy(button, api(V.paths.action(v.run, 'retry'), { method: 'POST', body: { cell: v.cell_id } }), { done: false });
      toast('Another attempt of this cell is queued.', 'success');
      runCells.delete(v.run);
      ctx.refreshSoon();
      if (made && made.id) ctx.go({ cell: made.id, run: v.run });
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
  }

  function openOverride(opener) {
    const v = view;
    if (!v) return;
    const current = v.override ? v.override.verdict : '';
    morph($('overrideBody'), html`<p class="ds-dialog__text" id="overrideText">${'The verdict that counts is yours: in the counts, the history and the bugs. ' + (v.override ? 'Without yours it is ' : 'Now it is ') + V.verdictWord(v.reviewed || v.verdict || v.final || '') + '.'}</p>
      <div class="ds-radios" role="radiogroup" aria-label="Verdict">${V.VERDICTS.map((verdict) => html`<label class="ds-radio" data-ds-key="${verdict}"><input type="radio" name="overrideVerdict" value="${verdict}"${verdict === current ? raw(' checked') : ''}>
        <span><strong class="so-tests-nowrap">${raw(icon(V.VERDICT[verdict].glyph, 16))}${V.verdictWord(verdict)}</strong><small>${MEANS[verdict]}</small></span></label>`)}</div>
      <div class="ds-field">
        <label class="ds-field__label" for="overrideNote">Note <span class="ds-field__optional">(optional)</span></label>
        <textarea class="ds-input" id="overrideNote" maxlength="2000" aria-describedby="overrideNoteHint"></textarea>
        <small class="ds-field__hint" id="overrideNoteHint">Why: the analyst and the next person read it.</small>
      </div>
      ${alsoBugs(v)}`);
    $('overrideNote').value = v.override ? v.override.note || '' : '';
    $('overrideRemove').hidden = !v.override;
    syncOverrideSave();
    DS.overlay.open(overrideDialog, { opener });
  }

  // alsoBugs offers, with a verdict that passes, to mark the cell's open
  // bugs Not a bug in the same step (PATCH …/bugs/{id}); hidden otherwise.
  function alsoBugs(v) {
    const known = bugs && bugs.repo === state.repo ? bugs.list : [];
    const open = (v.bugs || []).map((id) => known.find((b) => b.id === id)).filter((b) => b && (b.status === 'open' || b.status === 'known'));
    if (!open.length) return '';
    return html`<fieldset class="ds-field" id="overrideBugs" hidden>
      <legend class="ds-field__label">Its bugs</legend>
      <div class="ds-check-list">${open.map((b) => {
        const others = (b.cells || []).filter((c) => c !== v.cell_id).length;
        return html`<label class="ds-check" data-ds-key="${b.id}"><input type="checkbox" data-also-bug="${b.id}"><span>${'Also mark ' + b.id + ' Not a bug'} <span class="ds-meta">${'“' + b.title + '”' + (others ? ' · it covers ' + V.plural(others, 'other cell') + ' too' : '')}</span></span></label>`;
      })}</div>
      <small class="ds-field__hint">The analyst then stops reporting it, unless the evidence changes. Your note goes with it.</small>
    </fieldset>`;
  }

  function syncOverrideSave() {
    const picked = overrideDialog.querySelector('input[name="overrideVerdict"]:checked');
    const also = $('overrideBugs');
    if (also) also.hidden = !(picked && V.passed(picked.value));
    const save = $('overrideSave');
    save.setAttribute('aria-disabled', picked ? 'false' : 'true');
    if (picked) save.removeAttribute('data-tip');
    else save.setAttribute('data-tip', 'Choose a verdict');
  }

  async function sendOverride(verdict, note, button, alsoNotABug = []) {
    const v = view;
    if (!v) return;
    overrideDialog.setAttribute('data-ds-busy', '');
    $('overrideCancel').setAttribute('aria-disabled', 'true');
    try {
      await M.busy(button, api(V.paths.override(v.run, v.id), { method: 'POST', body: { verdict, note } }), { done: false });
    } catch (err) {
      toast(errorMessage(err), 'error');
      return;
    } finally {
      overrideDialog.removeAttribute('data-ds-busy');
      $('overrideCancel').removeAttribute('aria-disabled');
    }
    // The bugs the person said are none go with it; one that fails says so.
    const marked = [];
    for (const id of alsoNotABug) {
      const bug = ((bugs && bugs.list) || []).find((b) => b.id === id);
      const change = { status: 'not_a_bug' };
      if (note && bug && !bug.note) change.note = note;
      try {
        await api(V.paths.bug(state.repo, id), { method: 'PATCH', body: change });
        marked.push(id);
      } catch (err) {
        toast(id + ': ' + errorMessage(err), 'error');
      }
    }
    if (marked.length) bugs = null;
    DS.overlay.close(overrideDialog, { reason: 'confirm' });
    toast(verdict ? 'You made it ' + V.verdictWord(verdict) + (marked.length ? '; ' + marked.join(' and ') + (marked.length === 1 ? ' is' : ' are') + ' marked Not a bug.' : '.')
      : 'Your verdict is removed: the reviewer’s or the agent’s counts again.', 'success');
    details.delete(keyOf(v.run, v.id));
    runCells.delete(v.run);
    histories.clear();
    pass = null;
    ctx.refreshSoon();
    load({ quiet: true });
  }

  overrideDialog.addEventListener('change', (event) => {
    if (event.target.name === 'overrideVerdict') syncOverrideSave();
  });
  $('overrideSave').addEventListener('click', () => {
    const picked = overrideDialog.querySelector('input[name="overrideVerdict"]:checked');
    if (!picked) return;
    const also = V.passed(picked.value) ? [...overrideDialog.querySelectorAll('[data-also-bug]:checked')].map((n) => n.dataset.alsoBug) : [];
    sendOverride(picked.value, $('overrideNote').value.trim(), $('overrideSave'), also);
  });
  $('overrideRemove').addEventListener('click', () => sendOverride('', '', $('overrideRemove')));

  /* ------------------------------------------------------ more actions */

  function cellAction(act) {
    if (act.getAttribute('aria-disabled') === 'true' || !view) return;
    const what = act.dataset.cellAct;
    if (what === 'retry') retry(act);
    else if (what === 'override') openOverride(act);
    else if (what === 'chat') ctx.openChat({ kind: 'fix-cell', subject: { run: view.run, attempt: view.id, view } }, act);
    else if (what === 'transcript' && view.chat) window.open('/#open=' + encodeURIComponent(view.chat), '_blank', 'noopener');
    else if (what === 'rerun') ctx.startRun({ cells: [view.cell_id] }, nav.querySelector('[data-cell-more]') || body.querySelector('[data-cell-more]'));
  }

  // openMore opens the cell's other actions: a menu at a desk, an action
  // sheet on a phone (DS: the same items).
  function openMore(trigger, event) {
    if (!view || (event && event.detail > 1)) return;
    if (PHONE.matches) {
      morph($('cellMoreList'), html`${moreItems(view)}`);
      DS.overlay.open(moreSheet, { opener: trigger });
      return;
    }
    if (trigger.getAttribute('aria-expanded') === 'true') { DS.overlay.close(moreMenu); return; }
    morph(moreMenu, html`${moreItems(view)}`);
    DS.overlay.open(moreMenu, { opener: trigger });
  }

  for (const box of [moreMenu, moreSheet]) {
    box.addEventListener('click', (event) => {
      const act = event.target.closest('[data-cell-act]');
      if (!act) return;
      if (box === moreSheet) DS.overlay.close(moreSheet);
      cellAction(act);
    });
  }
  body.addEventListener('keydown', (event) => {
    const more = event.target.closest('[data-cell-more]');
    if (!more || PHONE.matches || (event.key !== 'ArrowDown' && event.key !== 'ArrowUp')) return;
    event.preventDefault();
    morph(moreMenu, html`${moreItems(view)}`);
    DS.overlay.open(moreMenu, { opener: more });
  });

  /* ------------------------------------------------------------ events */

  dialog.addEventListener('ds-close', (event) => {
    if (event.target !== dialog) return;
    shown = false;
    clearTimeout(liveTimer);
    ctx.closeCell();
  });

  nav.addEventListener('click', (event) => {
    const step = event.target.closest('[data-walk]');
    if (step) { walk(Number(step.dataset.walk)); return; }
    const keys = event.target.closest('[data-keys]');
    if (keys) DS.overlay.open(keysDialog, { opener: keys });
  });

  body.addEventListener('click', (event) => {
    const t = event.target;
    if (t.closest('[data-reload]')) { load(); return; }
    const stepBtn = t.closest('[data-step]');
    if (stepBtn) { selectStep(Number(stepBtn.dataset.step)); return; }
    const thumb = t.closest('[data-thumb]');
    if (thumb) { selectShot(Number(thumb.dataset.thumb)); return; }
    const move = t.closest('[data-shot-step]');
    if (move) { stepShot(Number(move.dataset.shotStep)); return; }
    if (t.closest('[data-zoom]')) { zoom = !zoom; draw(); return; }
    if (t.closest('[data-compare]')) {
      if (pass && pass.state === 'ready') { compare = !compare; zoom = false; draw(); }
      return;
    }
    const attempt = t.closest('[data-attempt]');
    if (attempt) {
      if (attempt.dataset.attempt !== (view && view.id)) ctx.go({ cell: attempt.dataset.attempt, run: view.run });
      return;
    }
    const prev = t.closest('[data-open-prev]');
    if (prev) { ctx.go({ cell: prev.dataset.openPrev, run: prev.dataset.prevRun }, { push: true }); return; }
    const tab = t.closest('[data-part]');
    if (tab) {
      if (tab.dataset.part !== part) {
        part = tab.dataset.part;
        draw();
        M.enter($('cellPart'), 'swap');
      }
      return;
    }
    const more = t.closest('[data-cell-more]');
    if (more) { openMore(more, event); return; }
    const act = t.closest('[data-cell-act]');
    if (act) { cellAction(act); return; }
    const test = t.closest('[data-go-test]');
    if (test) {
      event.preventDefault();
      ctx.go({ view: 'tests', test: test.dataset.goTest, run: '', bug: '', cell: '' }, { push: true });
      return;
    }
    const bug = t.closest('[data-go-bug]');
    if (bug) {
      event.preventDefault();
      ctx.go({ view: 'bugs', bug: bug.dataset.goBug, run: '', test: '', cell: '' }, { push: true });
    }
  });

  // The segmented switch of attempts: arrow keys move between them.
  body.addEventListener('keydown', (event) => {
    const option = event.target.closest('.so-cell__attempts [data-attempt]');
    if (!option || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
    const all = [...option.parentElement.querySelectorAll('[data-attempt]')];
    const i = all.indexOf(option) + (event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1);
    if (!all[i]) return;
    event.preventDefault();
    event.stopPropagation();
    all[i].focus();
    ctx.go({ cell: all[i].dataset.attempt, run: view.run });
  });

  dialog.addEventListener('keydown', (event) => {
    if (!shown || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
    if (DS.overlay.topmost() !== dialog) return;
    if (typing(event.target)) return;
    const k = event.key;
    if (k === '[' || k === 'k') { event.preventDefault(); walk(-1); } else if (k === ']' || k === 'j') { event.preventDefault(); walk(1); } else if (k === 'ArrowLeft' || k === 'ArrowRight') {
      if (event.target.closest('.so-cell-swipe, .ds-tabs')) return;
      event.preventDefault();
      stepShot(k === 'ArrowLeft' ? -1 : 1);
    } else if (k === 'z' && WIDE.matches) { event.preventDefault(); zoom = !zoom; draw(); } else if (k === 'c') {
      if (pass && pass.state === 'ready') { event.preventDefault(); compare = !compare; zoom = false; draw(); }
    } else if (k === 'o' && view) {
      const shots = allShots(view);
      const s = shots[sel.shot];
      if (s) { event.preventDefault(); window.open(fileOf(view, s.name), '_blank', 'noopener'); }
    } else if (k === '?') {
      event.preventDefault();
      DS.overlay.open(keysDialog, { opener: nav.querySelector('[data-keys]') || document.activeElement });
    }
  });

  // Escape while a tooltip shows: DS gives that key to the tooltip (WCAG
  // 1.4.13) and the viewer would need a second one. Here the first closes
  // the viewer too: the tooltip goes with it. (Seen before the tooltip's own
  // listener, in the capture phase of the window.)
  window.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || event.isComposing || !shown || DS.overlay.topmost() !== dialog || typing(event.target)) return;
    const tip = [...document.querySelectorAll('.ds-tooltip')].some((el) => !el.hidden && el.checkVisibility && el.checkVisibility());
    if (!tip) return;
    setTimeout(() => { if (shown && DS.overlay.topmost() === dialog) DS.overlay.close(dialog); }, 0);
  }, true);

  WIDE.addEventListener('change', () => { if (shown && view) draw(); });
  PHONE.addEventListener('change', () => { if (shown && view) draw(); });

  /* ------------------------------------------------------------ module */

  async function show() {
    if (shown) return;
    shown = true;
    await DS.overlay.open(dialog, { opener: document.activeElement, returnTo: () => document.querySelector('[data-cell="' + (want && want.id) + '"]') || $('main') });
  }

  return {
    /** open shows an attempt; a second call for the one shown only takes the list. */
    open(attemptId, runId, frameList) {
      if (frameList && frameList.length) list = frameList.slice();
      const same = shown && want && want.id === attemptId && want.run === runId;
      want = { id: attemptId, run: runId };
      if (same) { drawNav(); return; }
      if (!shown) { part = 'steps'; sel = { key: '', step: 0, shot: -1, manual: false }; }
      show();
      load();
    },
    close() {
      clearTimeout(liveTimer);
      if (!shown) return;
      shown = false;
      want = null;
      DS.overlay.close(dialog);
    },
    /** refresh reads the shown cell again: its run changed. */
    refresh() {
      if (!shown || !want) return;
      runCells.delete(want.run);
      histories.clear();
      bugs = null;
      load({ quiet: true });
    },
  };
}
