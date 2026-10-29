// The Tests page (docs/design/TEST-MODE.md, "The Tests page"): the frame.
//
// It owns where the person is, what is loaded for the repository shown, and
// the parts every view shares: the head with the repository's name and the
// Run action, the banner of the active run, the tabs, and the overlays. Each
// view is a module of its own that draws into its panel:
//
//   testsoverview.js  Overview          testsruns.js   Runs and a run
//   teststests.js     Tests and a test  testsbugs.js   Bugs and a bug
//   testsforms.js     Settings, and the Run dialog
//   testscell.js      the cell viewer (over any view)
//   testschat.js      the chat dialog (Fix in a chat, Write a test ...)
//
// The module contract. Every view module exports `mount(ctx)` and hands back
// `{ render(panel), refresh?(), close?(), repoChanged?() }`: render draws the
// view from what it has (and may start loading, then draw again through
// ctx.redraw); refresh reads its own data again after the server said
// something changed; close is called when the person leaves the view;
// repoChanged when another repository is shown. `ctx` is below (the object
// handed to every module).
//
// Where the person is lives in the address, so every place can be linked to
// and Back undoes every step: #repo=<key>&view=<view> and, inside a view,
// run= (a run, also the run of an open cell), test=, bug=, and cell= (the
// cell viewer over any view). Old links (#repo=…&run=…, &tab=…, &cell=…)
// still land where they meant.

import { api, errorMessage, toast, confirmDialog, promptDialog, onWake } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import { mountEvents } from './events.js';
import * as V from './testsview.js';
import * as overviewView from './testsoverview.js';
import * as runsView from './testsruns.js';
import * as testsView from './teststests.js';
import * as bugsView from './testsbugs.js';
import * as forms from './testsforms.js';
import * as cellViewer from './testscell.js';
import * as chatDialog from './testschat.js';

const $ = (id) => document.getElementById(id);

// A burst of frames (every cell that changes sends one) is read once, this
// long after the last of them. While a run is active and the page is seen,
// its cells are read again this often (the live cards, the banner).
const SETTLE = 600;
const LIVE_EVERY = 3000;
const PHONE = window.matchMedia('(max-width: 600px)');

const VIEWS = ['overview', 'runs', 'tests', 'bugs', 'settings'];
const LEGACY_TAB = { results: 'runs', runs: 'runs', bugs: 'bugs', catalog: 'tests', settings: 'settings' };

const state = {
  loaded: false,
  repos: [],
  global: { max_sessions: 8 },
  repo: '',
  route: { view: '', run: '', test: '', bug: '', cell: '' },
  runs: [], // the repository's runs, newest first, each with its number `n`
  catalog: null, // the catalog as it is in the repository now
  catalogError: '',
  settings: null, // { settings, defaults }
  live: null, // { id, detail, cells } of the repository's active run
  openBugs: null, // the Bugs tab's badge: the repository's open bugs
  cellList: [], // [{ attempt, run }] the open cell was opened from (its previous and next)
};

/* ------------------------------------------------------------- the route */

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  const r = {
    repo: p.get('repo') || '', run: p.get('run') || '', test: p.get('test') || '',
    bug: p.get('bug') || '', cell: p.get('cell') || '', view: p.get('view') || '',
  };
  if (!VIEWS.includes(r.view)) r.view = LEGACY_TAB[p.get('tab')] || '';
  if (!r.view) r.view = r.test ? 'tests' : r.bug ? 'bugs' : r.run ? 'runs' : 'overview';
  return r;
}

function hashOf(r) {
  const p = new URLSearchParams();
  for (const key of ['repo', 'view', 'run', 'test', 'bug', 'cell']) if (r[key]) p.set(key, r[key]);
  return '#' + p.toString();
}

/**
 * go moves to another place on the page: `push` for a step the person took
 * that Back should undo (a view, a run, a cell opened); else the address is
 * only corrected. A step that opened the cell viewer is marked, so that
 * closing it is Back rather than one more step.
 */
function go(change, { push = false } = {}) {
  const cur = readHash();
  const next = { ...cur, ...change };
  const target = hashOf(next);
  if (target !== location.hash) {
    const overlay = !!next.cell && (cur.cell ? !!(history.state && history.state.overlay) : push);
    if (push) history.pushState({ overlay }, '', target);
    else history.replaceState({ overlay }, '', target);
  }
  return route();
}

let routing = Promise.resolve();
function route() {
  routing = routing.then(applyRoute).catch((err) => console.error(err));
  return routing;
}

async function applyRoute() {
  if (!state.repos.length) { render(); return; }
  const r = readHash();
  const repo = repoOf(r.repo) ? r.repo : state.repos[0].key;
  if (repo !== state.repo) await switchRepo(repo);
  const normal = hashOf({ ...r, repo });
  if (normal !== location.hash) history.replaceState(history.state, '', normal);
  const was = state.route;
  state.route = { view: r.view, run: r.run, test: r.test, bug: r.bug, cell: r.cell };
  // A snackbar is about the place it was raised in: it goes with it (the
  // cell viewer opening or closing over the view is no new place).
  const place = (x) => [x.view, x.cell ? '' : x.run, x.test, x.bug].join('|');
  if (was.view && place(was) !== place(r)) DS.overlay.closeToasts(null);
  if (was.view && was.view !== r.view && views[was.view].close) views[was.view].close();
  setTab(r.view, !!was.view && was.view !== r.view);
  render();
  if (r.cell) cell.open(r.cell, r.run, state.cellList);
  else cell.close();
}

/* -------------------------------------------------------------- loading */

function repoOf(key) { return state.repos.find((r) => r.key === key) || null; }
function runOf(id) { return state.runs.find((r) => r.id === id) || null; }

async function loadRepos() {
  const data = await api(V.paths.repos());
  state.repos = data.repos || [];
  state.global = data.global || state.global;
  state.loaded = true;
  const repo = repoOf(state.repo);
  if (repo) state.openBugs = repo.open_bugs || 0;
}

async function switchRepo(key) {
  state.repo = key;
  const known = repoOf(key);
  Object.assign(state, { runs: [], catalog: null, catalogError: '', settings: null, live: null, openBugs: known ? known.open_bugs || 0 : null, cellList: [] });
  for (const view of Object.values(views)) if (view.repoChanged) view.repoChanged();
  await Promise.all([loadRuns(), loadLive()]);
  // The Run menu and the empty overview read the catalog: it comes in behind.
  ensureCatalog().then(() => renderHead());
}

async function loadRuns() {
  if (!state.repo) return;
  const repo = state.repo;
  try {
    const data = await api(V.paths.runs(repo));
    if (repo !== state.repo) return;
    state.runs = V.numberRuns(data.runs || []);
  } catch (err) {
    if (repo === state.repo) notice(errorMessage(err));
  }
}

/**
 * loadLive reads the repository's active run: its detail (the pools, the
 * estimate of the time left, the cost) and its cells; a running cell's row
 * carries its step, activity and latest screenshot (`live`).
 */
async function loadLive() {
  const repo = repoOf(state.repo);
  const active = repo && repo.active;
  if (!active) { state.live = null; return; }
  try {
    const [detail, cells] = await Promise.all([api(V.paths.run(active.id)), api(V.paths.cells(active.id))]);
    const rows = cells.cells || [];
    if (state.repo === repo.key) state.live = { id: active.id, detail, cells: rows };
  } catch {
    // The next frame or tick reads it again.
  }
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
  try {
    await loadRepos();
  } catch { return; /* the next frame tries again */ }
  if (!state.repos.length) { render(); return; }
  if (!repoOf(state.repo)) { state.repo = ''; await route(); return; }
  const mine = all || frames.some((f) => f.repo === state.repo);
  if (mine) {
    await Promise.all([loadRuns(), loadLive()]);
    const view = views[state.route.view];
    if (view && view.refresh) await view.refresh();
  }
  render();
  cell.refresh();
  scheduleLive();
}

// While a run is active and the page is seen, its cells are read every few
// seconds besides the frames: a running cell's screenshot and step change
// without a frame.
let liveTimer = 0;
function scheduleLive() {
  clearTimeout(liveTimer);
  if (!state.live || document.hidden) return;
  liveTimer = setTimeout(async () => {
    await loadLive();
    renderActive();
    renderView();
    scheduleLive();
  }, LIVE_EVERY);
}
document.addEventListener('visibilitychange', () => {
  if (document.hidden) clearTimeout(liveTimer);
  else if (state.loaded) refreshSoon();
});

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
  if (!state.loaded || none) return;
  renderHead();
  renderActive();
  renderTabs();
  renderView();
}

function renderHead() {
  const repo = repoOf(state.repo);
  if (!repo) return;
  // The catalog's own title (TESTS.md) once it is read; the server's name for it before.
  const name = (state.catalog && state.catalog.title) || repo.title;
  const title = $('repoTitle');
  if (title.textContent !== name) title.textContent = name;
  document.title = name + ' · Tests · Socrates';
  const last = repo.last || state.runs[0];
  const commit = last && last.commit ? last.commit.slice(0, 10) + (last.dirty ? ' with changes' : '') : '';
  // On a phone the path and the commit are in the repositories' menu (the title's), not over the tabs.
  morph($('lead'), html`<span class="so-break">${repo.path}</span>${commit ? html` · <span class="so-tests-nowrap" data-tip="The commit the latest run ran on">${raw(icon('fileCode', 16))}${commit}</span>` : ''}`);
  const cat = state.catalog;
  const errors = cat && cat.errors ? cat.errors.length : 0;
  morph($('headStatus'), errors
    ? html`<button class="ds-link-btn" type="button" data-go-view="tests">${V.chip({ word: errors === 1 ? 'The catalog has an error' : 'The catalog has ' + errors + ' errors', glyph: 'alertCircle', tone: 'error' })}</button>`
    : '');
}

const ACTIVE_TITLE = {
  preparing: 'is being prepared', running: 'is running', finishing: 'is being reviewed',
  paused: 'is paused', waiting: 'waits for another run',
};

// shownElsewhere: the view shows the active run already, with its own
// progress and actions (its run view, or the overview's Live card), so the
// banner would say it twice.
function shownElsewhere(run) {
  const r = state.route;
  if (r.view === 'runs' && r.run === run.id) return true;
  return r.view === 'overview' && !!state.live && state.live.id === run.id;
}

function renderActive() {
  const box = $('active');
  const repo = repoOf(state.repo);
  const run = repo && repo.active;
  box.hidden = !run || shownElsewhere(run);
  if (box.hidden) { morph(box, ''); return; }
  const live = state.live && state.live.id === run.id ? state.live : null;
  const d = live ? live.detail : run;
  const known = runOf(run.id);
  const named = { ...d, n: known ? known.n : d.number };
  const name = V.runName(named, { name: false });
  const c = d.counts || run.counts || {};
  const phone = PHONE.matches;
  // On a phone the banner is one line over its bar: the run, its count, Watch and Stop.
  const title = phone ? name + ' · ' + (c.done || 0) + ' of ' + (c.cells || 0) + ' done' : name + ' ' + (ACTIVE_TITLE[run.state] || 'is active');
  morph(box, html`<div class="so-tests-active__row">
      <h2 class="ds-card__title" id="activeTitle">${title}</h2>
      ${phone ? '' : V.runChip(run.state)}
      <span class="so-tests-active__acts">
        <button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-watch="${run.id}" aria-label="${'Watch ' + name}">${raw(icon('eye', 16))}Watch</button>
        ${run.state === 'paused' ? html`<button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-run-act="resume" data-run="${run.id}">${raw(icon('play', 16))}Resume</button>` : ''}
        <button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-run-act="stop" data-run="${run.id}" aria-label="${'Stop ' + name}">${raw(icon('stopCircle', 16))}Stop</button>
      </span>
    </div>
    ${run.error && !phone ? html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text so-break">${run.error}</span></div>` : ''}
    ${V.runProgress({ ...named, counts: c }, { cells: live ? live.cells : null, bare: phone })}`);
}

/* ------------------------------------------------------------- the tabs */

const tabs = $('tabs');
const indicator = DS.indicator.createIndicator(tabs, { active: '[aria-selected="true"]', className: 'ds-tabs__indicator', reveal: true });
DS.dom.roving(tabs);

function setTab(view, changed) {
  for (const button of tabs.querySelectorAll('[role="tab"]')) {
    const on = button.dataset.view === view;
    button.setAttribute('aria-selected', String(on));
    button.tabIndex = on ? 0 : -1;
    $(button.getAttribute('aria-controls')).hidden = !on;
  }
  indicator.update();
  if (changed) M.enter($('p-' + view), 'swap');
}

function renderTabs() {
  const n = state.openBugs;
  morph($('t-bugs'), html`Bugs${n ? html`<span class="ds-badge">${n}</span>` : ''}`);
  $('t-bugs').setAttribute('aria-label', n ? 'Bugs, ' + V.plural(n, 'open bug') : 'Bugs');
}

function renderView() {
  const view = views[state.route.view];
  if (view && state.repo) view.render($('p-' + state.route.view));
}

/** redraw draws a view again if it is the one shown (a module calls it once its data came in). */
function redraw(name) {
  if (state.route.view === name) renderView();
}

tabs.addEventListener('click', (event) => {
  const tab = event.target.closest('[role="tab"]');
  if (!tab) return;
  const r = state.route;
  if (tab.dataset.view === r.view && !r.run && !r.test && !r.bug) return;
  go({ view: tab.dataset.view, run: '', test: '', bug: '', cell: '' }, { push: true });
});

/* ------------------------------------------------------------ the menus */

function toggleMenu(trigger, menu, fill) {
  if (trigger.getAttribute('aria-expanded') === 'true') { DS.overlay.close(menu); return; }
  Promise.resolve(fill()).then(() => DS.overlay.open(menu, { opener: trigger }));
}

function wireMenu(trigger, menu, fill) {
  trigger.addEventListener('click', (event) => {
    if (event.detail > 1) return;
    toggleMenu(trigger, menu, fill);
  });
  trigger.addEventListener('keydown', (event) => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    Promise.resolve(fill()).then(() => DS.overlay.open(menu, { opener: trigger }));
  });
}

const item = (glyph, label, attrs, meta = '') => html`<button class="ds-menu__item" role="menuitem" type="button" ${raw(attrs)}>${raw(icon(glyph, 20))}<span class="ds-menu__label">${label}</span>${meta ? html`<span class="ds-menu__meta">${meta}</span>` : ''}</button>`;

function fillRepoMenu() {
  const repo = repoOf(state.repo);
  const last = repo && (repo.last || state.runs[0]);
  const commit = last && last.commit ? 'Latest run on ' + last.commit.slice(0, 10) + (last.dirty ? ' with changes' : '') : '';
  morph($('repoMenu'), html`${repo ? html`<p class="ds-menu__heading so-break so-tests-menu__where">${repo.path}${commit ? html`<br>${commit}` : ''}</p>
    <div class="ds-menu__divider" role="separator"></div>` : ''}<div role="group" aria-labelledby="repoMenuHead">
      <p class="ds-menu__heading" id="repoMenuHead">Repositories</p>
      ${state.repos.map((r) => html`<button class="ds-menu__item" role="menuitemradio" type="button" aria-checked="${r.key === state.repo ? 'true' : 'false'}" data-repo="${r.key}">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">${r.title}</span>${r.active ? html`<span class="ds-menu__meta">running</span>` : ''}</button>`)}
    </div>
    <div class="ds-menu__divider" role="separator"></div>
    ${item('plus', 'Add repository', 'data-repo-act="add"')}
    ${item('trash', 'Remove from the list', 'data-repo-act="remove"')}`);
}

// The quick ways to run ("Start now"): everything, the problems of the
// latest run, one priority; each says how many cells it runs, and starts
// after a question that says them again (testsforms.js startDirect).
async function fillRunMenu() {
  await ensureCatalog();
  const cat = state.catalog;
  const latest = state.runs.find((r) => !V.ACTIVE_RUN.has(r.state));
  const problems = latest ? V.problemCount(latest.counts) : 0;
  const tests = cat ? (cat.areas || []).flatMap((a) => a.tests || []) : [];
  const priorities = [...new Set(tests.map((t) => t.priority).filter(Boolean))].sort();
  const cellsOf = (p) => tests.filter((t) => t.priority === p).reduce((n, t) => n + (t.cells || []).length, 0);
  morph($('runMenu'), html`<div role="group" aria-labelledby="runMenuHead">
      <p class="ds-menu__heading" id="runMenuHead">Start now</p>
      ${item('play', 'Everything', 'data-quick="all"', cat ? V.plural(cat.cells || 0, 'cell') : '')}
      ${problems ? item('sync', 'The problems of ' + V.runName(latest, { name: false }), html`data-quick="problems" data-run="${latest.id}"`, V.plural(problems, 'cell')) : ''}
      ${priorities.length > 1 ? priorities.map((p) => item('flag', 'Only ' + p, html`data-quick="priority" data-priority="${p}"`, V.plural(cellsOf(p), 'cell'))) : ''}
    </div>
    <div class="ds-menu__divider" role="separator"></div>
    ${item('list', 'Choose what to run…', 'data-quick="choose"')}`);
}

wireMenu($('repoMenuBtn'), $('repoMenu'), fillRepoMenu);
wireMenu($('runMoreBtn'), $('runMenu'), fillRunMenu);

$('repoMenu').addEventListener('click', (event) => {
  const pick = event.target.closest('[data-repo]');
  if (pick) {
    if (pick.dataset.repo !== state.repo) go({ repo: pick.dataset.repo, view: 'overview', run: '', test: '', bug: '', cell: '' }, { push: true });
    return;
  }
  const act = event.target.closest('[data-repo-act]');
  if (!act) return;
  if (act.dataset.repoAct === 'add') addRepo($('repoMenuBtn'));
  else removeRepo($('repoMenuBtn'));
});

$('runMenu').addEventListener('click', (event) => {
  const quick = event.target.closest('[data-quick]');
  if (!quick) return;
  const opener = $('runMoreBtn');
  const kind = quick.dataset.quick;
  if (kind === 'choose') form.openRunDialog({ choose: true }, opener);
  else if (kind === 'all') form.startDirect({}, opener);
  else if (kind === 'problems') form.startDirect({ problems_of: quick.dataset.run }, opener);
  else if (kind === 'priority') form.startDirect({ priorities: [quick.dataset.priority] }, opener);
});

/* ---------------------------------------------------------- the actions */

/**
 * runAction stops, resumes, pins, analyses or deletes a run, asking first
 * where it cannot be undone.
 */
async function runAction(what, id, opener) {
  const run = runOf(id) || (repoOf(state.repo) || {}).active || { id };
  const label = V.runName(run);
  try {
    if (what === 'delete') {
      const yes = await confirmDialog({
        title: 'Delete ' + label + '?',
        body: 'Its cells, screenshots, sessions and bugs go with it. This cannot be undone.',
        confirmLabel: 'Delete run', danger: true, opener,
      });
      if (!yes) return false;
      await api(V.paths.run(id), { method: 'DELETE' });
      toast(label + ' is deleted.', 'success');
      if (state.route.run === id) go({ run: '', cell: '' });
    } else if (what === 'stop') {
      const yes = await confirmDialog({
        title: 'Stop ' + label + '?',
        body: 'Queued cells are dropped and running ones are stopped; what they found so far is kept.',
        confirmLabel: 'Stop run', danger: true, opener,
      });
      if (!yes) return false;
      await api(V.paths.action(id, 'stop'), { method: 'POST', body: {} });
    } else if (what === 'resume') {
      await api(V.paths.action(id, 'resume'), { method: 'POST', body: {} });
    } else if (what === 'pin') {
      await api(V.paths.action(id, 'pin'), { method: 'POST', body: { pinned: !run.pinned } });
      toast(run.pinned ? label + ' may go with the old runs again.' : label + ' is kept whatever the retention says.', 'success');
    } else if (what === 'analyse') {
      await api(V.paths.action(id, 'analyse'), { method: 'POST', body: {} });
      toast('The analyst reads ' + label + ' again.', 'info');
    }
  } catch (err) {
    toast(errorMessage(err), 'error');
    return false;
  }
  refreshSoon();
  return true;
}

document.addEventListener('click', (event) => {
  const act = event.target.closest('[data-run-act]');
  if (act) { runAction(act.dataset.runAct, act.dataset.run, act); return; }
  const watch = event.target.closest('[data-watch]');
  if (watch) { go({ view: 'runs', run: watch.dataset.watch, test: '', bug: '', cell: '' }, { push: true }); return; }
  const view = event.target.closest('[data-go-view]');
  if (view) go({ view: view.dataset.goView, run: '', test: '', bug: '', cell: '' }, { push: true });
});

$('runBtn').addEventListener('click', () => form.openRunDialog(undefined, $('runBtn')));
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
    go({ repo: repo.key, view: 'overview', run: '', test: '', bug: '', cell: '' }, { push: true });
    toast('The repository is added.', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function removeRepo(opener) {
  const repo = repoOf(state.repo);
  if (!repo) return;
  const yes = await confirmDialog({
    title: 'Take ' + repo.title + ' off the list?',
    body: 'Its runs stay where they are and come back when it is added again. Nothing in the repository changes.',
    confirmLabel: 'Remove from the list', opener,
  });
  if (!yes) return;
  try {
    await api(V.paths.repo(repo.key), { method: 'DELETE' });
    await loadRepos();
    state.repo = '';
    go({ repo: '', view: 'overview', run: '', test: '', bug: '', cell: '' }, { push: true });
    toast(repo.title + ' is off the list.', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}
$('addRepoEmpty').addEventListener('click', () => addRepo($('addRepoEmpty')));

// "/" searches the view's own list (a run's cells), as DS's search key says.
document.addEventListener('keydown', (event) => {
  if (event.key !== '/' || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return;
  if (event.target.closest('input, textarea, select, [contenteditable]') || DS.overlay.topmost()) return;
  const panel = $('p-' + state.route.view);
  const field = panel && panel.querySelector('[data-search]');
  if (!field || !field.offsetParent) return;
  event.preventDefault();
  field.focus();
});

PHONE.addEventListener('change', () => { if (state.loaded) renderActive(); });

window.addEventListener('popstate', () => route());
window.addEventListener('hashchange', () => route());

/* --------------------------------------------------------- the modules */

/**
 * ctx is what every module gets. `state` is the page's (read it, do not
 * replace its parts but through the functions here); `go(change, { push })`
 * moves to another place; `refreshSoon()` reads the repository again soon;
 * `redraw(view)` draws a view again if it is shown.
 */
const ctx = {
  state,
  go,
  refreshSoon,
  redraw,
  api,
  paths: V.paths,
  repoOf,
  runOf,
  ensureCatalog,
  ensureSettings,
  runAction,
  /** openCell opens the cell viewer on an attempt; `list` ([{ attempt, run }]) is what previous and next walk. */
  openCell(attempt, run, list) {
    state.cellList = list || [];
    return go({ cell: attempt, run: run || state.route.run }, { push: true });
  },
  /** closeCell closes the cell viewer: Back when opening it was a step. */
  closeCell() {
    const r = readHash();
    if (!r.cell) return;
    if (history.state && history.state.overlay) history.back();
    else go({ cell: '' });
  },
  /** openRunDialog opens the Run dialog; preset: { tests } | { cells } | { problems_of } | { priorities } | { choose }. */
  openRunDialog: (preset, opener) => form.openRunDialog(preset, opener),
  /** startRun starts a preset at once, after a question that says how many cells. */
  startRun: (preset, opener) => form.startDirect(preset, opener),
  /** openChat opens the chat dialog: { kind: 'fix-bug' | 'fix-cell' | 'write-test' | 'change-test', subject }. */
  openChat: (spec, opener) => chat.open(spec, opener),
  /** started: a run was started from the page; it is the one shown. */
  async started(run) {
    try {
      await loadRepos();
      await Promise.all([loadRuns(), loadLive()]);
    } catch { /* the frames bring it */ }
    scheduleLive();
    go({ view: 'runs', run: run && run.id ? run.id : '', test: '', bug: '', cell: '' }, { push: true });
  },
};

const form = forms.mount(ctx);
const views = {
  overview: overviewView.mount(ctx),
  runs: runsView.mount(ctx),
  tests: testsView.mount(ctx),
  bugs: bugsView.mount(ctx),
  settings: form,
};
const cell = cellViewer.mount(ctx);
const chat = chatDialog.mount(ctx);

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
  await route();
  scheduleLive();
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
