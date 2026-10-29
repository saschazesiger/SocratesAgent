// The Tests page (docs/design/TEST-MODE.md, "The Tests page"): the test
// catalogs of the repositories, their runs and every cell of a run down to
// its screenshots.
//
// Everything is the server's (internal/server/tests_api.go): the page reads
// the repositories, the shown repository's runs and the shown run's cells,
// and reads them again when the event stream says a run changed. Where it
// is - repository, run, tab, the open cell or test - is in the address
// (#repo=…&run=…&tab=…&cell=…), so a cell can be linked to and Back closes
// it. This module draws the frame, the results, the runs and the bugs;
// testscell.js the cell and the test, testsforms.js the catalog, the
// settings and the Run sheet.

import { api, el, errorMessage, toast, confirmDialog, promptDialog, onWake } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import { mountEvents } from './events.js';
import { markdown } from './md.js';
import * as V from './testsview.js';
import { mountCell } from './testscell.js';
import { mountForms } from './testsforms.js';

const $ = (id) => document.getElementById(id);

// A burst of frames (every cell that changes sends one) is read once, this
// long after the last of them; the search runs this long after the last key.
const SETTLE = 600;
const TYPE_PAUSE = 300;

const TABS = ['results', 'runs', 'bugs', 'catalog', 'settings'];
const PHONE = window.matchMedia('(max-width: 600px)');

const state = {
  loaded: false,
  repos: [],
  global: { max_sessions: 8 },
  repo: '',
  run: '',
  tab: 'results',
  runs: [],
  detail: null, // the shown run (RunView with its areas and pools)
  cells: [], // its cells (CellRow, the latest attempt of each)
  history: {}, // test id -> its last runs, newest first
  bugs: null, // { bugs, conclusions } of the shown run
  catalog: null, // the repository's catalog as it is now
  catalogError: '',
  settings: null, // { settings, defaults }
  active: null, // { detail, cells } of the repository's active run when it is not the shown one
  filters: { area: '', priority: '', tag: '', verdict: '', dims: {}, reviewed: false, problems: false },
  query: '',
  hits: null,
  expanded: new Set(), // bug ids whose description is open
};

/* ------------------------------------------------------------- the route */

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  const tab = p.get('tab') || 'results';
  return {
    repo: p.get('repo') || '', run: p.get('run') || '', tab: TABS.includes(tab) ? tab : 'results',
    cell: p.get('cell') || '', test: p.get('test') || '',
  };
}

function hashOf(r) {
  const p = new URLSearchParams();
  if (r.repo) p.set('repo', r.repo);
  if (r.run) p.set('run', r.run);
  if (r.tab && r.tab !== 'results') p.set('tab', r.tab);
  if (r.cell) p.set('cell', r.cell);
  else if (r.test) p.set('test', r.test);
  return '#' + p.toString();
}

// go moves to another place on the page: `push` for a step the person took
// that Back should undo (a tab, a cell opened), else the address is only
// corrected.
function go(change, { push = false } = {}) {
  const next = { ...readHash(), ...change };
  const target = hashOf(next);
  if (target !== location.hash) {
    // A step that opened a cell or a test is marked, so that closing it is
    // Back rather than one more step (testscell.js).
    const sheet = !!(next.cell || next.test);
    if (push) history.pushState({ sheet }, '', target);
    else history.replaceState({ sheet: sheet && !!(history.state && history.state.sheet) }, '', target);
  }
  route();
}

let routing = Promise.resolve();
let drawnRoute = '';
function route() {
  routing = routing.then(applyRoute).catch((err) => console.error(err));
  return routing;
}

async function applyRoute() {
  if (!state.repos.length) return;
  const r = readHash();
  const repo = state.repos.some((x) => x.key === r.repo) ? r.repo : state.repos[0].key;
  if (repo !== state.repo) {
    state.repo = repo;
    state.run = '';
    Object.assign(state, { runs: [], history: {}, catalog: null, catalogError: '', settings: null, hits: null, query: '', active: null });
    state.filters = { area: '', priority: '', tag: '', verdict: '', dims: {}, reviewed: false, problems: false };
    forms.repoChanged();
    await loadRepo();
  }
  const run = state.runs.some((x) => x.id === r.run) ? r.run : (state.runs[0] ? state.runs[0].id : '');
  if (run !== state.run) {
    state.run = run;
    Object.assign(state, { detail: null, cells: [], bugs: null });
    state.expanded.clear();
    await loadRun();
  }
  if (r.repo !== repo || r.run !== run) history.replaceState(null, '', hashOf({ ...r, repo, run }));
  setTab(r.tab);
  // Opening or closing a cell changes nothing under it: the page is drawn
  // again only for another repository, run or tab.
  const key = [repo, run, r.tab].join('\n');
  if (key !== drawnRoute) {
    drawnRoute = key;
    render();
  }
  if (r.cell) cell.openCell(r.cell);
  else if (r.test) cell.openTest(r.test);
  else cell.close();
}

/* -------------------------------------------------------------- loading */

async function loadRepos() {
  const data = await api(V.paths.repos());
  state.repos = data.repos || [];
  state.global = data.global || state.global;
  state.loaded = true;
}

async function loadRepo() {
  if (!state.repo) return;
  const [runs, hist] = await Promise.all([
    api(V.paths.runs(state.repo)).catch(() => ({ runs: [] })),
    api(V.paths.history(state.repo)).catch(() => ({ history: {} })),
  ]);
  state.runs = runs.runs || [];
  state.history = hist.history || {};
}

async function loadRun() {
  if (!state.run) return;
  const id = state.run;
  try {
    const [detail, cells] = await Promise.all([api(V.paths.run(id)), api(V.paths.cells(id))]);
    if (id !== state.run) return;
    state.detail = detail;
    state.cells = cells.cells || [];
  } catch (err) {
    if (id === state.run) notice(errorMessage(err));
  }
  if (state.tab === 'bugs') await ensureBugs();
}

async function ensureBugs(force = false) {
  if (!state.run || (state.bugs && !force)) return state.bugs;
  const id = state.run;
  try {
    const data = await api(V.paths.bugs(id));
    if (id === state.run) state.bugs = { bugs: data.bugs || [], conclusions: data.conclusions || '' };
  } catch {
    if (id === state.run && !state.bugs) state.bugs = { bugs: [], conclusions: '' };
  }
  return state.bugs;
}

async function ensureCatalog(force = false) {
  if (!state.repo || (state.catalog && !force)) return state.catalog;
  const repo = state.repo;
  try {
    const data = await api(V.paths.catalog(repo));
    if (repo === state.repo) { state.catalog = data; state.catalogError = ''; }
  } catch (err) {
    if (repo === state.repo) state.catalogError = errorMessage(err);
  }
  return state.catalog;
}

async function ensureSettings(force = false) {
  if (!state.repo || (state.settings && !force)) return state.settings;
  const repo = state.repo;
  const data = await api(V.paths.settings(repo));
  if (repo === state.repo) state.settings = data;
  return state.settings;
}

// The repository's active run, when it is not the one shown: the Runs tab
// lists its running and waiting cells and its pools.
async function loadActive() {
  const active = repoOf(state.repo) && repoOf(state.repo).active;
  if (!active || active.id === state.run) { state.active = null; return; }
  try {
    const [detail, cells] = await Promise.all([api(V.paths.run(active.id)), api(V.paths.cells(active.id))]);
    state.active = { detail, cells: cells.cells || [] };
  } catch { state.active = null; }
}

function repoOf(key) { return state.repos.find((r) => r.key === key) || null; }

// refresh reads again what a change of a run touches.
let refreshTimer = 0;
let refreshFrames = [];
function refreshSoon(frame) {
  if (frame) refreshFrames.push(frame);
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refresh, SETTLE);
}

async function refresh() {
  const frames = refreshFrames;
  refreshFrames = [];
  const all = !frames.length;
  const mine = all || frames.some((f) => f.repo === state.repo);
  try {
    await loadRepos();
  } catch { return; /* the next frame tries again */ }
  if (!state.repos.length) { render(); return; }
  if (!repoOf(state.repo)) { state.repo = ''; await route(); return; }
  if (mine) await loadRepo();
  const shown = all || frames.some((f) => f.run === state.run);
  if (state.run && !state.runs.some((r) => r.id === state.run)) {
    // The shown run was deleted: the latest instead.
    go({ run: '', cell: '', test: '' });
    return;
  }
  if (!state.run && state.runs.length) { await route(); return; }
  if (shown) {
    await loadRun();
    if (state.bugs) await ensureBugs(true);
  }
  if (state.tab === 'runs') await loadActive();
  render();
  if (shown) cell.refresh();
}

/* ------------------------------------------------------------ the frame */

function notice(text) {
  if (!text) { morph($('notice'), ''); return; }
  morph($('notice'), html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${text}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-retry>Try again</button></div>`);
}

function render() {
  $('loading').hidden = state.loaded;
  const none = state.loaded && !state.repos.length;
  $('noRepos').hidden = !none;
  $('body').hidden = !state.loaded || none;
  $('addRepo').hidden = !state.loaded || none;
  $('runBtn').hidden = !state.repos.length;
  if (!state.loaded || none) return;
  renderHead();
  renderActive();
  renderPanel();
}

function renderHead() {
  const repo = repoOf(state.repo);
  $('repoField').hidden = state.repos.length < 2;
  fill($('repoSel'), state.repos.map((r) => [r.key, r.title]), state.repo);
  $('runField').hidden = !state.runs.length;
  fill($('runSel'), state.runs.map((r) => [r.id, V.runLabel(r) + ' · ' + V.runWord(r.state)]), state.run);
  const lead = repo ? (state.repos.length < 2 ? repo.title + ' · ' : '') + repo.path : '';
  if ($('lead').textContent !== lead) $('lead').textContent = lead;
  const d = state.detail;
  morph($('headStatus'), d ? html`${V.runChip(d.state)}${d.commit ? html`<span class="ds-meta">${raw(icon('fileCode', 16))}${d.commit.slice(0, 10)}${d.dirty ? ' with changes' : ''}</span>` : ''}` : '');
}

function fill(select, options, value) {
  const same = select.options.length === options.length
    && options.every(([v, label], i) => select.options[i].value === v && select.options[i].textContent === label);
  if (!same) select.replaceChildren(...options.map(([v, label]) => el('option', { value: v, text: label })));
  if (select.value !== value) select.value = value;
  select.title = select.selectedOptions[0] ? select.selectedOptions[0].textContent : '';
}

function renderActive() {
  const box = $('active');
  const repo = repoOf(state.repo);
  const run = repo && repo.active;
  box.hidden = !run;
  if (!run) { morph(box, ''); return; }
  const c = run.counts || {};
  const states = c.states || {};
  const share = c.cells ? Math.min(1, (c.done || 0) / c.cells) : 0;
  const running = (states.running || 0) + (states.starting || 0);
  const detail = [running ? running + ' running' : '', states.waiting ? states.waiting + ' waiting' : '',
    states.queued ? states.queued + ' queued' : '', c.reviews ? c.reviews + ' in review' : ''].filter(Boolean).join(' · ');
  morph(box, html`<div class="ds-card__row">
      <h2 class="ds-card__title" id="activeTitle">${run.id === state.run ? 'This run' : V.runLabel(run)}</h2>
      ${V.runChip(run.state)}
    </div>
    ${run.error ? html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text">${run.error}</span></div>` : ''}
    <div class="ds-progress" style="${'--ds-progress: ' + share.toFixed(4)}">
      <div class="ds-progress__track" role="progressbar" aria-label="Cells done" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(share * 100)}"><div class="ds-progress__bar"></div></div>
      <p class="ds-progress__label">${(c.done || 0) + ' of ' + (c.cells || 0) + ' cells'}</p>
      <p class="ds-progress__detail">${detail || V.countsWords(c)}</p>
    </div>
    <div class="ds-card__links">
      ${run.id === state.run ? '' : html`<button class="ds-btn ds-btn--text ds-card__link" type="button" data-open-run="${run.id}">Show this run</button>`}
      ${run.state === 'paused' ? html`<button class="ds-btn ds-btn--text ds-card__link" type="button" data-run-act="resume" data-run="${run.id}">Resume</button>` : ''}
      <button class="ds-btn ds-btn--text ds-card__link" type="button" data-run-act="stop" data-run="${run.id}">${raw(icon('stopCircle', 16))}Stop</button>
    </div>`);
}

/* ------------------------------------------------------------- the tabs */

const tabs = $('tabs');
const indicator = DS.indicator.createIndicator(tabs, { active: '[aria-selected="true"]', className: 'ds-tabs__indicator', reveal: true });
DS.dom.roving(tabs);

function setTab(tab) {
  const changed = tab !== state.tab;
  state.tab = tab;
  for (const button of tabs.querySelectorAll('[role="tab"]')) {
    const on = button.dataset.tab === tab;
    button.setAttribute('aria-selected', String(on));
    button.tabIndex = on ? 0 : -1;
    $(button.getAttribute('aria-controls')).hidden = !on;
  }
  indicator.update();
  if (changed) {
    M.enter($('p-' + tab), 'swap');
    if (tab === 'bugs') ensureBugs().then(render);
    if (tab === 'catalog') ensureCatalog().then(render);
    if (tab === 'settings') ensureSettings().then(render).catch((err) => notice(errorMessage(err)));
    if (tab === 'runs') loadActive().then(render);
  }
}

tabs.addEventListener('click', (event) => {
  const tab = event.target.closest('[role="tab"]');
  if (!tab || tab.dataset.tab === state.tab) return;
  go({ tab: tab.dataset.tab, cell: '', test: '' }, { push: true });
});

function renderPanel() {
  const counts = state.detail ? state.detail.bugs : 0;
  morph($('t-bugs'), html`Bugs${counts ? html`<span class="ds-badge">${counts}</span>` : ''}`);
  switch (state.tab) {
    case 'results': renderResults(); break;
    case 'runs': renderRuns(); break;
    case 'bugs': renderBugs(); break;
    case 'catalog': forms.renderCatalog($('p-catalog')); break;
    case 'settings': forms.renderSettings($('p-settings')); break;
    default: break;
  }
}

/* ---------------------------------------------------------- the results */

// isProblem: a cell that did not pass, or one that ended without a verdict.
function isProblem(row) {
  if (row.final) return row.final !== 'pass' && row.final !== 'na';
  return row.state === 'aborted' || row.state === 'failed';
}

function matches(row, { verdict = true } = {}) {
  const f = state.filters;
  if (f.area && row.area !== f.area) return false;
  if (f.priority && row.priority !== f.priority) return false;
  if (f.tag && !(row.tags || []).includes(f.tag)) return false;
  if (verdict && f.verdict && row.final !== f.verdict) return false;
  for (const [name, id] of Object.entries(f.dims)) {
    if (id && !(row.dims || []).some((d) => d.name === name && d.id === id)) return false;
  }
  if (f.reviewed && !row.reviewed && row.review !== 'done') return false;
  if (f.problems && !isProblem(row)) return false;
  return true;
}

let resultsBuilt = false;
function buildResults() {
  const panel = $('p-results');
  morph(panel, html`<div class="ds-tiles" role="group" aria-label="Show cells by verdict" id="rTiles" style="--ds-tile-min-width: 128px">
      ${['fail', 'issue', 'blocked', 'pass'].map((v) => html`<button class="ds-tile" type="button" aria-pressed="false" data-verdict="${v}">
        <span class="ds-tile__label">${raw(icon(V.VERDICT[v].glyph, 16))}<span>${V.VERDICT[v].word}</span></span>
        <span class="ds-tile__value" data-value="0">0</span>
        <span class="ds-tile__note"></span>
      </button>`)}
    </div>
    <div class="ds-toolbar">
      <div class="ds-filters" id="rFilters">
        <div class="ds-field ds-field--grow">
          <label class="ds-sr" for="rSearch">Search every run</label>
          <div class="ds-search">${raw(icon('search', 20))}<input class="ds-input" id="rSearch" type="search" placeholder="Search every run" autocomplete="off" spellcheck="false" aria-keyshortcuts="/"><kbd class="ds-kbd ds-search__key" aria-hidden="true">/</kbd></div>
        </div>
        <div class="ds-field"><label class="ds-field__label" for="fArea">Area</label><select class="ds-input ds-input--filled" id="fArea" data-filter="area"></select></div>
        <div class="ds-field"><label class="ds-field__label" for="fPriority">Priority</label><select class="ds-input ds-input--filled" id="fPriority" data-filter="priority"></select></div>
        <div class="ds-field"><label class="ds-field__label" for="fTag">Tag</label><select class="ds-input ds-input--filled" id="fTag" data-filter="tag"></select></div>
        <div class="ds-field"><label class="ds-field__label" for="fVerdict">Verdict</label><select class="ds-input ds-input--filled" id="fVerdict" data-filter="verdict"></select></div>
        <span class="so-tests__dims" id="fDims"></span>
      </div>
      <fieldset class="ds-chips" id="rChips">
        <legend class="ds-sr">Show only</legend>
        <label class="ds-chip"><input class="ds-chip__input" type="checkbox" data-flag="problems"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>Problems only</span></span></label>
        <label class="ds-chip"><input class="ds-chip__input" type="checkbox" data-flag="reviewed"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>Reviewed only</span></span></label>
        <button class="ds-btn ds-btn--text ds-btn--sm" type="button" id="rClear" hidden>Clear filters</button>
      </fieldset>
    </div>
    <div id="rHits"></div>
    <div class="so-tests__stack" id="rGrid"></div>`);
  resultsBuilt = true;

  panel.addEventListener('click', (event) => {
    const tile = event.target.closest('[data-verdict]');
    if (tile) {
      state.filters.verdict = state.filters.verdict === tile.dataset.verdict ? '' : tile.dataset.verdict;
      renderResults();
      return;
    }
    const cellBtn = event.target.closest('[data-cell]');
    if (cellBtn) { openCell(cellBtn.dataset.cell, cellBtn.dataset.run); return; }
    const testBtn = event.target.closest('[data-test]');
    if (testBtn) { go({ test: testBtn.dataset.test, cell: '' }, { push: true }); return; }
    if (event.target.closest('#rClear')) {
      state.filters = { area: '', priority: '', tag: '', verdict: '', dims: {}, reviewed: false, problems: false };
      renderResults();
      $('rSearch').focus();
    }
  });
  panel.addEventListener('change', (event) => {
    const t = event.target;
    if (t.dataset.filter) state.filters[t.dataset.filter] = t.value;
    else if (t.dataset.dim) state.filters.dims[t.dataset.dim] = t.value;
    else if (t.dataset.flag) state.filters[t.dataset.flag] = t.checked;
    else return;
    renderResults();
  });
  let typing = 0;
  $('rSearch').addEventListener('input', () => {
    clearTimeout(typing);
    typing = setTimeout(search, TYPE_PAUSE);
  });
}

function openCell(attempt, run) {
  go({ run: run || state.run, cell: attempt, test: '', tab: 'results' }, { push: true });
}

async function search() {
  const q = $('rSearch').value.trim();
  state.query = q;
  if (!q) { state.hits = null; renderHits(); return; }
  try {
    const data = await api(V.paths.search(state.repo, q));
    if (q !== state.query) return;
    state.hits = data.hits || [];
  } catch (err) {
    state.hits = [];
    toast(errorMessage(err), 'error');
  }
  renderHits();
}

function renderHits() {
  const box = $('rHits');
  if (!state.hits) { morph(box, ''); return; }
  if (!state.hits.length) {
    morph(box, html`<p class="ds-muted">Nothing in any run matches “${state.query}”.</p>`);
    return;
  }
  morph(box, html`<section class="ds-card" aria-labelledby="hitsTitle">
    <h2 class="ds-card__title" id="hitsTitle">${state.hits.length === 1 ? '1 cell' : state.hits.length + ' cells'} match “${state.query}”</h2>
    <ul class="ds-list ds-list--interactive" aria-label="Search results">
      ${state.hits.map((hit) => html`<li class="ds-list__item" data-ds-key="${hit.run + '/' + hit.id}">
        <span class="ds-list__text">
          <span class="ds-list__title"><button class="ds-list__action" type="button" data-cell="${hit.id}" data-run="${hit.run}">${hit.title}</button></span>
          <span class="ds-list__meta">${[dimsWords(hit.dims), V.when(hit.created_at), hit.snippet].filter(Boolean).join(' · ')}</span>
        </span>
        ${V.cellChip(hit)}
      </li>`)}
    </ul>
  </section>`);
}

function dimsWords(dims) { return (dims || []).map((d) => d.label || d.id).join(', '); }

function fillFilter(id, field, values, allLabel) {
  const select = $(id);
  fill(select, [['', allLabel], ...values], state.filters[field] || '');
  select.closest('.ds-field').hidden = values.length < 2 && !state.filters[field];
}

function renderResults() {
  if (!resultsBuilt) buildResults();
  const grid = $('rGrid');
  const tilesBox = $('rTiles');
  const cells = state.cells;
  const toolbar = $('rFilters').closest('.ds-toolbar');
  tilesBox.hidden = !state.run;
  toolbar.hidden = !state.run && !state.runs.length;
  if (!state.run) {
    morph(grid, html`<div class="ds-card"><div class="ds-empty">
      <span class="ds-empty__disc" aria-hidden="true">${raw(icon('play', 24))}</span>
      <p class="ds-empty__title">No run yet</p>
      <p class="ds-empty__text">Run the catalog, or a part of it, and every cell shows up here with its verdict.</p>
      <button class="ds-btn ds-btn--tonal" type="button" data-run-sheet>Run tests</button>
    </div></div>`);
    return;
  }

  // The filters offer what this run has.
  const areas = state.detail ? state.detail.areas || [] : [];
  const seen = (fn) => [...new Set(cells.flatMap(fn))].filter(Boolean).sort();
  fillFilter('fArea', 'area', (areas.length ? areas.map((a) => [a.id, a.title || a.id]) : seen((r) => [r.area]).map((a) => [a, a])), 'All areas');
  fillFilter('fPriority', 'priority', seen((r) => [r.priority]).map((p) => [p, p]), 'All priorities');
  fillFilter('fTag', 'tag', seen((r) => r.tags || []).map((t) => [t, t]), 'All tags');
  fillFilter('fVerdict', 'verdict', V.VERDICTS.map((v) => [v, V.VERDICT[v].word]), 'All verdicts');
  const dims = new Map();
  for (const row of cells) for (const d of row.dims || []) {
    if (!dims.has(d.name)) dims.set(d.name, new Map());
    dims.get(d.name).set(d.id, d.label || d.id);
  }
  const dimBox = $('fDims');
  morph(dimBox, html`${[...dims.keys()].map((name) => html`<div class="ds-field" data-ds-key="${name}"><label class="ds-field__label" for="${'fDim-' + name}">${name.charAt(0).toUpperCase() + name.slice(1)}</label><select class="ds-input ds-input--filled" id="${'fDim-' + name}" data-dim="${name}"></select></div>`)}`);
  for (const [name, values] of dims) {
    const select = $('fDim-' + name);
    fill(select, [['', 'All'], ...values], state.filters.dims[name] || '');
  }
  for (const input of $('rChips').querySelectorAll('[data-flag]')) input.checked = !!state.filters[input.dataset.flag];
  const f = state.filters;
  $('rClear').hidden = !(f.area || f.priority || f.tag || f.verdict || f.reviewed || f.problems || Object.values(f.dims).some(Boolean));

  // The tiles count what the other filters leave.
  const base = cells.filter((row) => matches(row, { verdict: false }));
  for (const tile of tilesBox.querySelectorAll('[data-verdict]')) {
    const v = tile.dataset.verdict;
    const n = base.filter((row) => row.final === v).length;
    tile.setAttribute('aria-pressed', String(f.verdict === v));
    const value = tile.querySelector('.ds-tile__value');
    value.dataset.value = String(n);
    M.countUp(value, n);
    let note = '';
    if (v === 'pass') {
      const na = base.filter((row) => row.final === 'na').length;
      const open = base.filter((row) => V.OPEN_ATTEMPT.has(row.state)).length;
      note = [na ? na + ' n/a' : '', open ? open + ' to go' : ''].filter(Boolean).join(' · ');
    } else {
      const reviewed = base.filter((row) => row.final === v && row.reviewed).length;
      note = reviewed ? reviewed + ' by the reviewer' : '';
    }
    const noteNode = tile.querySelector('.ds-tile__note');
    if (noteNode.textContent !== note) noteNode.textContent = note;
  }

  // Why a run that ended early ended (an active run says it in its card).
  const d = state.detail;
  const error = d && d.error && !V.ACTIVE_RUN.has(d.state)
    ? html`<div class="ds-alert ds-alert--error" data-ds-key="run-error">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${d.error}</span></div>` : '';
  const rows = cells.filter((row) => matches(row));
  if (!rows.length) {
    const failed = d && (d.state === 'failed' || d.state === 'stopped');
    morph(grid, html`${error}<div class="ds-card" data-ds-key="empty"><div class="ds-empty">
      <span class="ds-empty__disc" aria-hidden="true">${raw(icon('filter', 24))}</span>
      <p class="ds-empty__title">${cells.length ? 'No cell matches these filters' : 'No cells'}</p>
      <p class="ds-empty__text">${cells.length ? 'Clear a filter to see more.' : failed ? 'The run ended before any cell was queued.' : 'The run is still being prepared.'}</p>
    </div></div>`);
    return;
  }
  const order = areas.length ? areas : [...new Set(rows.map((r) => r.area))].map((id) => ({ id, title: id }));
  morph(grid, html`${error}${order.map((area) => areaTable(area, rows.filter((r) => r.area === area.id)))}`);
}

// areaTable is one area's tests as rows and the values of its columns
// dimension as columns; a test with no cell in a column leaves it empty.
function areaTable(area, rows) {
  if (!rows.length) return '';
  const phone = PHONE.matches;
  const dim = (area.dims || []).find((d) => d.name === area.columns);
  const used = new Set(rows.map((r) => dimId(r, area.columns)));
  const columns = dim
    ? [...(dim.values || []), ...(dim.extra || [])].filter((v) => used.has(v.id))
    : [{ id: '', label: 'Result' }];
  for (const id of used) if (dim && !columns.some((c) => c.id === id)) columns.push({ id, label: id });
  const tests = [];
  const byTest = new Map();
  for (const row of rows) {
    if (!byTest.has(row.test)) { byTest.set(row.test, []); tests.push(row); }
    byTest.get(row.test).push(row);
  }
  const table = phone
    ? 'ds-table ds-table--dense ds-table--stack so-tests__table'
    : 'ds-table ds-table--dense ds-table--interactive ds-table--pin-first so-tests__table';
  return html`<section class="ds-card" data-ds-key="${'area:' + area.id}" aria-labelledby="${'area-' + area.id}">
    <h2 class="ds-card__title" id="${'area-' + area.id}">${area.title || area.id}</h2>
    <div class="ds-table-wrap">
      <table class="${table}">
        <thead><tr>
          <th scope="col">Test</th>
          ${columns.map((c) => html`<th scope="col">${c.label || c.id}</th>`)}
          <th scope="col">History</th>
        </tr></thead>
        <tbody>${tests.map((first) => testRow(first, byTest.get(first.test), columns, area, phone))}</tbody>
      </table>
    </div>
  </section>`;
}

function dimId(row, name) {
  const d = (row.dims || []).find((x) => x.name === name);
  return d ? d.id : '';
}

function testRow(first, cells, columns, area, phone) {
  const lead = phone
    ? html`<button class="ds-link-btn" type="button" data-test="${first.test}">${first.title}</button>`
    : html`<button class="ds-link-btn ds-table__row-link" type="button" data-test="${first.test}">${first.title}</button>`;
  return html`<tr data-ds-key="${first.test}">
    ${phone ? html`<td class="so-tests__test" data-label="Test">${lead}${testTags(first)}</td>`
      : html`<th scope="row" class="so-tests__test">${lead}${testTags(first)}</th>`}
    ${columns.map((c) => {
      const here = cells.filter((r) => !area.columns || dimId(r, area.columns) === c.id);
      return html`<td data-label="${c.label || c.id}"><span class="so-tests__cells">${here.map((r) => cellButton(r, area))}</span></td>`;
    })}
    <td data-label="History">${historyMarks(first.test)}</td>
  </tr>`;
}

function testTags(row) {
  return html`<small class="so-tests__sub"><span class="ds-tag ds-tag--neutral">${row.priority || 'P?'}</span>${(row.tags || []).map((t) => html`<span class="ds-tag">${t}</span>`)}</small>`;
}

function cellButton(row, area) {
  const others = (row.dims || []).filter((d) => d.name !== area.columns);
  const word = row.final ? V.verdictWord(row.final) : V.attemptWord(row.state);
  const tip = row.state === 'waiting' && row.wait ? row.wait
    : row.reviewed && row.verdict && row.reviewed !== row.verdict ? 'Reviewed: the agent said ' + V.verdictWord(row.verdict)
      : '';
  return html`<button class="ds-pressable so-tests__cell" type="button" data-cell="${row.id}" data-ds-key="${row.id}"
      aria-label="${row.title + ', ' + dimsWords(row.dims) + ': ' + word + (tip ? '. ' + tip : '')}"${tip ? raw(html` data-tip="${tip}"`) : ''}>
    ${V.cellChip({ ...row, wait: '' })}${others.map((d) => html`<span class="ds-tag ds-tag--neutral">${d.id}</span>`)}${row.reviewed ? html`<span class="so-tests__mark">${raw(icon('eye', 16))}</span>` : ''}${row.attempt > 1 ? html`<span class="ds-meta">${'#' + row.attempt}</span>` : ''}
  </button>`;
}

function historyMarks(test) {
  const points = (state.history[test] || []).slice().reverse();
  if (!points.length) return html`<span class="ds-meta">None</span>`;
  return html`<span class="so-tests__history" role="list" aria-label="Last runs, oldest first">${points.map((p) => html`<span role="listitem">${
    V.mark(p.verdict, (V.VERDICT[p.verdict] ? V.verdictWord(p.verdict) : V.attemptWord(p.verdict)) + ' · ' + V.when(p.created_at) + (p.run === state.run ? ' (this run)' : ''))}</span>`)}</span>`;
}

PHONE.addEventListener('change', () => { if (state.tab === 'results') renderResults(); });

/* -------------------------------------------------------------- the runs */

function renderRuns() {
  const panel = $('p-runs');
  if (!state.runs.length) {
    morph(panel, html`<div class="ds-card"><div class="ds-empty">
      <span class="ds-empty__disc" aria-hidden="true">${raw(icon('history', 24))}</span>
      <p class="ds-empty__title">No run yet</p>
      <p class="ds-empty__text">Every run of this repository is listed here, the newest first.</p>
      <button class="ds-btn ds-btn--tonal" type="button" data-run-sheet>Run tests</button>
    </div></div>`);
    return;
  }
  const phone = PHONE.matches;
  const repo = repoOf(state.repo);
  const active = repo && repo.active;
  const activeView = active && (active.id === state.run ? { detail: state.detail, cells: state.cells } : state.active);
  morph(panel, html`<section class="ds-card" aria-labelledby="runsTitle">
      <h2 class="ds-card__title" id="runsTitle">Runs</h2>
      <div class="ds-table-wrap">
        <table class="${phone ? 'ds-table ds-table--dense ds-table--stack' : 'ds-table ds-table--dense ds-table--interactive'}">
          <thead><tr>
            <th scope="col">Run</th><th scope="col">State</th><th scope="col" class="ds-col--secondary">Selection</th>
            <th scope="col">Results</th><th scope="col" class="ds-col--secondary">Duration</th><th scope="col"><span class="ds-sr">Actions</span></th>
          </tr></thead>
          <tbody>${state.runs.map((run) => runRow(run, phone))}</tbody>
        </table>
      </div>
    </section>
    ${activeView && activeView.detail ? activeSection(activeView) : ''}`);
}

function runRow(run, phone) {
  const shown = run.id === state.run;
  const end = run.ended_at || (V.ACTIVE_RUN.has(run.state) ? Date.now() : 0);
  const label = V.runLabel(run);
  return html`<tr data-ds-key="${run.id}"${shown ? raw(' class="ds-table__row--selected"') : ''}>
    <td class="ds-col--text" data-label="Run"><button class="${phone ? 'ds-link-btn' : 'ds-link-btn ds-table__row-link'}" type="button" data-open-run="${run.id}"${shown ? raw(' aria-current="true"') : ''}>${label}</button>${run.pinned ? html` <span class="ds-tag ds-tag--neutral">${raw(icon('lock', 16))}Pinned</span>` : ''}${shown ? html` <span class="ds-sr">(shown)</span>` : ''}</td>
    <td data-label="State">${V.runChip(run.state)}</td>
    <td class="ds-col--secondary" data-label="Selection">${V.selectionWords(run.selection)}</td>
    <td data-label="Results">${V.countsWords(run.counts)}${run.bugs ? html`<small class="so-tests__sub">${run.bugs === 1 ? '1 bug' : run.bugs + ' bugs'}</small>` : ''}</td>
    <td class="ds-col--secondary" data-label="Duration">${V.span(end - (run.started_at || run.created_at))}</td>
    <td class="ds-table__actions"><span class="ds-hover-actions">
      <button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-run-act="pin" data-run="${run.id}" aria-pressed="${run.pinned ? 'true' : 'false'}" aria-label="${(run.pinned ? 'Unpin ' : 'Pin ') + label}" data-tip="${run.pinned ? 'Unpin: it may be deleted with the old runs' : 'Pin: keep it whatever the retention says'}">${raw(icon('lock', 20))}</button>
      ${run.state === 'paused' ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-run-act="resume" data-run="${run.id}" aria-label="${'Resume ' + label}" data-tip="Resume">${raw(icon('play', 20))}</button>` : ''}
      ${V.ACTIVE_RUN.has(run.state) ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-run-act="stop" data-run="${run.id}" aria-label="${'Stop ' + label}" data-tip="Stop">${raw(icon('stopCircle', 20))}</button>` : ''}
      <button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-run-act="delete" data-run="${run.id}" aria-label="${'Delete ' + label}" data-tip="Delete">${raw(icon('trash', 20))}</button>
    </span></td>
  </tr>`;
}

// activeSection is what the active run is doing right now: its cells that
// run or wait, with their reasons, and who holds which member of a pool.
function activeSection(view) {
  const going = view.cells.filter((r) => r.state === 'running' || r.state === 'starting' || r.state === 'waiting');
  const pools = view.detail.pools || [];
  return html`<section class="ds-card" aria-labelledby="nowTitle">
    <h2 class="ds-card__title" id="nowTitle">Now</h2>
    ${going.length ? html`<ul class="ds-list ds-list--interactive" aria-label="Running and waiting cells">${going.map((r) => html`<li class="ds-list__item" data-ds-key="${r.id}">
      <span class="ds-list__text">
        <span class="ds-list__title"><button class="ds-list__action" type="button" data-cell="${r.id}" data-run="${view.detail.id}">${r.title}</button></span>
        <span class="ds-list__meta">${[dimsWords(r.dims), r.state === 'waiting' ? r.wait : (r.started_at ? 'since ' + V.clock(r.started_at) : '')].filter(Boolean).join(' · ')}</span>
      </span>
      ${V.cellChip({ ...r, wait: '' })}
    </li>`)}</ul>` : html`<p class="ds-muted">No cell is running or waiting.</p>`}
    ${pools.map((pool) => html`<h3 class="ds-card__subtitle">${pool.title || pool.tool}</h3>
      <ul class="ds-kv-list">${pool.members.map((m) => html`<li><span>${m.label || 'Member ' + m.n}</span><strong>${
        m.holders && m.holders.length ? m.holders.join(', ')
          : m.busy ? html`${raw(icon('ban', 16, 'ds-kv-list__icon ds-kv-list__icon--warning'))}<span class="ds-sr">Busy:</span> in use outside Socrates`
            : html`<span class="ds-muted">Free</span>`}</strong></li>`)}</ul>`)}
  </section>`;
}

async function runAction(what, id, opener) {
  const run = state.runs.find((r) => r.id === id) || (repoOf(state.repo) || {}).active || { id };
  const label = V.runLabel(run) || id;
  try {
    if (what === 'delete') {
      const yes = await confirmDialog({
        title: 'Delete the run of ' + label + '?',
        body: 'Its cells, screenshots, sessions and bugs go with it. This cannot be undone.',
        confirmLabel: 'Delete run', danger: true, opener,
      });
      if (!yes) return;
      await api(V.paths.run(id), { method: 'DELETE' });
      toast('The run is deleted.', 'success');
    } else if (what === 'stop') {
      const yes = await confirmDialog({
        title: 'Stop the run?',
        body: 'Queued cells are dropped and running ones are stopped; what they found so far is kept.',
        confirmLabel: 'Stop run', danger: true, opener,
      });
      if (!yes) return;
      await api(V.paths.action(id, 'stop'), { method: 'POST', body: {} });
    } else if (what === 'resume') {
      await api(V.paths.action(id, 'resume'), { method: 'POST', body: {} });
    } else if (what === 'pin') {
      await api(V.paths.action(id, 'pin'), { method: 'POST', body: { pinned: !run.pinned } });
    } else if (what === 'analyse') {
      await api(V.paths.action(id, 'analyse'), { method: 'POST', body: {} });
      toast('The analyst reads the run again.', 'info');
    }
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
  refreshSoon();
}

/* -------------------------------------------------------------- the bugs */

function renderBugs() {
  const panel = $('p-bugs');
  if (!state.run) {
    morph(panel, html`<div class="ds-card"><div class="ds-empty">
      <span class="ds-empty__disc" aria-hidden="true">${raw(icon('flag', 24))}</span>
      <p class="ds-empty__title">No run yet</p>
      <p class="ds-empty__text">Once a run is reviewed, its analyst lists one bug per root cause here.</p>
    </div></div>`);
    return;
  }
  if (!state.bugs) {
    morph(panel, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the bugs</div>`);
    return;
  }
  const d = state.detail || {};
  const analysis = d.analysis;
  const analysing = analysis && analysis.state === 'running';
  const { bugs, conclusions } = state.bugs;
  const phone = PHONE.matches;
  const sorted = bugs.slice().sort((a, b) => sevRank(a.severity) - sevRank(b.severity));
  morph(panel, html`<section class="ds-card" aria-labelledby="bugsTitle">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="bugsTitle">Bugs</h2>
        ${analysing ? V.runChip('finishing') : html`<button class="ds-btn ds-btn--sm" type="button" data-run-act="analyse" data-run="${state.run}"${V.ACTIVE_RUN.has(d.state) && d.state !== 'finishing' ? raw(' aria-disabled="true" data-tip="The run is not done yet"') : ''}>${raw(icon('sparkles', 16))}Analyse again</button>`}
      </div>
      ${analysis && analysis.error ? html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text">${analysis.error}</span></div>` : ''}
      ${sorted.length ? html`<div class="ds-table-wrap"><table class="${phone ? 'ds-table ds-table--dense ds-table--stack' : 'ds-table ds-table--dense'}">
        <thead><tr>${phone ? '' : html`<th scope="col"><span class="ds-sr">Details</span></th>`}<th scope="col">Severity</th><th scope="col">Bug</th><th scope="col">Cells</th></tr></thead>
        <tbody>${sorted.map((bug) => bugRows(bug, phone))}</tbody>
      </table></div>` : html`<p class="ds-muted">${analysis ? 'The analyst found no bug.' : d.settings && d.settings.analysis === false ? 'This run is not analysed.' : 'No analysis yet: it runs once the cells are reviewed.'}</p>`}
    </section>
    ${conclusions ? html`<section class="ds-card" aria-labelledby="conclTitle">
      <h2 class="ds-card__title" id="conclTitle">Conclusions</h2>
      <div class="ds-prose so-break">${markdown(conclusions)}</div>
    </section>` : ''}`);
}

function sevRank(s) { const i = Object.keys(V.SEVERITY).indexOf(s); return i < 0 ? 9 : i; }

function bugRows(bug, phone) {
  const open = state.expanded.has(bug.id);
  const key = 'bug-' + bug.id;
  const cells = (bug.cells || []).map((ref) => {
    const row = state.cells.find((r) => r.cell_id === ref || r.id === ref || r.cell === ref);
    return row ? html`<button class="ds-link-btn" type="button" data-cell="${row.id}" data-run="${state.run}">${row.test.split('/').pop() + ' · ' + (dimsWords(row.dims) || row.cell)}</button>` : html`<span>${ref}</span>`;
  });
  const cellsHtml = html`<span class="so-tests__links">${cells}</span>`;
  if (phone) {
    return html`<tr data-ds-key="${key}">
      <td data-label="Severity">${V.severityChip(bug.severity)}</td>
      <td data-label="Bug"><strong>${bug.id}</strong> ${bug.title}<div class="ds-prose so-break">${markdown(bug.description || '')}</div></td>
      <td data-label="Cells">${cellsHtml}</td>
    </tr>`;
  }
  return html`<tr data-ds-key="${key}">
      <td><button class="ds-btn ds-btn--quiet ds-btn--icon ds-table__toggle" type="button" aria-expanded="${open ? 'true' : 'false'}" aria-controls="${key + '-detail'}" aria-label="${'Description of ' + bug.id}" data-bug="${bug.id}">${raw(icon('chevronDown', 20, 'ds-chevron'))}</button></td>
      <td>${V.severityChip(bug.severity)}</td>
      <td class="ds-col--text"><strong>${bug.id}</strong> ${bug.title}</td>
      <td>${cellsHtml}</td>
    </tr>
    <tr class="ds-table__detail" id="${key + '-detail'}" data-ds-key="${key + '-detail'}"${open ? '' : raw(' inert')}>
      <td colspan="4"><div class="ds-table__expand"><div><div class="ds-prose so-break">${markdown(bug.description || 'No description.')}</div></div></div></td>
    </tr>`;
}

/* ------------------------------------------------------------ the parts */

const cell = mountCell({
  state,
  go,
  ensureBugs,
  ensureCatalog,
  refreshSoon,
  closeRoute: () => {
    const r = readHash();
    if (!r.cell && !r.test) return;
    if (history.state && history.state.sheet) history.back();
    else go({ cell: '', test: '' });
  },
});

const forms = mountForms({
  state,
  ensureCatalog,
  ensureSettings,
  // A run was started: it is the one shown.
  started: async (run) => {
    try {
      await loadRepos();
      await loadRepo();
    } catch { /* the frames bring it */ }
    go({ run: run && run.id ? run.id : '', tab: 'results', cell: '', test: '' }, { push: true });
  },
});

/* ---------------------------------------------------------- the actions */

document.addEventListener('click', (event) => {
  const runAct = event.target.closest('[data-run-act]');
  if (runAct) { runAction(runAct.dataset.runAct, runAct.dataset.run, runAct); return; }
  const openRun = event.target.closest('[data-open-run]');
  if (openRun) { go({ run: openRun.dataset.openRun, tab: 'results', cell: '', test: '' }, { push: true }); return; }
  const sheet = event.target.closest('[data-run-sheet]');
  if (sheet) { forms.openRunSheet(sheet); return; }
  const bug = event.target.closest('[data-bug]');
  if (bug) {
    if (state.expanded.has(bug.dataset.bug)) state.expanded.delete(bug.dataset.bug);
    else state.expanded.add(bug.dataset.bug);
    renderBugs();
    return;
  }
  // A cell named outside the results (a bug's, the active run's list).
  const cellBtn = event.target.closest('[data-cell]');
  if (cellBtn && !cellBtn.closest('#p-results') && !cellBtn.closest('#cellSheet')) openCell(cellBtn.dataset.cell, cellBtn.dataset.run);
});

$('runBtn').addEventListener('click', () => forms.openRunSheet($('runBtn')));
$('repoSel').addEventListener('change', () => go({ repo: $('repoSel').value, run: '', cell: '', test: '' }, { push: true }));
$('runSel').addEventListener('change', () => go({ run: $('runSel').value, cell: '', test: '' }, { push: true }));
$('notice').addEventListener('click', (event) => {
  if (!event.target.closest('[data-retry]')) return;
  notice('');
  start();
});

async function addRepo(opener) {
  const path = await promptDialog({
    title: 'Add a repository',
    body: 'Its catalog is read from .socrates/tests/TESTS.md in the repository’s root.',
    label: 'Path of the repository', confirmLabel: 'Add', opener,
  });
  if (!path) return;
  try {
    const repo = await api(V.paths.repos(), { method: 'POST', body: { path } });
    await loadRepos();
    go({ repo: repo.key, run: '', cell: '', test: '', tab: 'results' }, { push: true });
    toast('The repository is added.', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}
$('addRepo').addEventListener('click', () => addRepo($('addRepo')));
$('addRepoEmpty').addEventListener('click', () => addRepo($('addRepoEmpty')));

// "/" searches, as DS's search key says.
document.addEventListener('keydown', (event) => {
  if (event.key !== '/' || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
  if (event.target.closest('input, textarea, select, [contenteditable]') || DS.overlay.topmost()) return;
  const field = $('rSearch');
  if (!field || state.tab !== 'results') return;
  event.preventDefault();
  field.focus();
});

window.addEventListener('popstate', () => route());
window.addEventListener('hashchange', () => route());

/* ------------------------------------------------------------ the start */

async function start() {
  try {
    await loadRepos();
  } catch (err) {
    state.loaded = true;
    $('loading').hidden = true;
    notice(errorMessage(err));
    return;
  }
  render();
  await route();
}

const events = mountEvents({
  hello: () => { if (state.loaded) refreshSoon(); },
  tests: (frame) => refreshSoon(frame),
  status: () => {},
  fast: () => false,
});
events.start();
onWake(() => { if (state.loaded) refreshSoon(); });
start();
