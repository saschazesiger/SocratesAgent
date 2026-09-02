// Which programs this machine can run a session on, where a session is
// allowed to work, and the sheet that binds a new one to all three.
//
// A session is bound to a harness, a working directory and a model before it
// exists, and only the model can be changed afterwards. So the sheet is the
// only thing standing between the person and a terminal - which is why the
// catalogue is kept in local storage and the sheet renders from that copy when
// there is no connection. Everything it produces is checked again by the
// server, which is the real boundary: a stale copy degrades to "the server
// refuses that directory", never to a session in the wrong place.

import { api, el, toast, infoTip } from './api.js';
import { combobox } from './combobox.js';
import { agentMark } from './logos.js';

const CACHE_KEY = 'socrates.harnesses';

// The three ways of choosing a working directory, as internal/harnesses names
// them.
export const DYNAMIC = 'dynamic';
export const PRESET = 'preset';
export const CUSTOM = 'custom';

// How a level reads on a button. A level nobody here has heard of is shown as
// the CLI spells it: the CLI named it, so the CLI takes it.
const EFFORT_LABELS = {
  '': 'Default', minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High',
  xhigh: 'X-High', max: 'Max', ultra: 'Ultra',
};

export function effortLabel(value) {
  return EFFORT_LABELS[value] || value;
}

// effortsFor is the levels to offer: the chosen model's own list, or - before
// a model is chosen, or for one the CLI has not reported - every level any of
// its models offers. The levels differ between harnesses (Claude Code goes up
// to max, Codex to xhigh, OpenCode names them per model), so there is no fixed
// row.
export function effortsFor(id, modelId) {
  const model = modelOf(id, modelId);
  if (model && model.efforts && model.efforts.length) return model.efforts;
  const found = harness(id);
  if (!found) return [];
  const all = [];
  for (const m of found.models || []) for (const e of m.efforts || []) if (!all.includes(e)) all.push(e);
  return all;
}

let cached = null;       // the catalogue, whatever its provenance
let fromStorage = false; // whether what we have is last visit's copy
let loading = null;

const EMPTY_WORKSPACE = { root: '', default_harness: '', allow_custom: false, presets: [], modes: [DYNAMIC] };

function readCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed.harnesses) ? parsed : null;
  } catch { return null; }
}

function writeCache(data) {
  try { localStorage.setItem(CACHE_KEY, JSON.stringify(data)); } catch { /* private window */ }
}

/**
 * load fetches the catalogue, once. A failure falls back to the copy from the
 * last visit rather than leaving the sheet empty, because a sheet that cannot
 * be drawn is a session that cannot be started.
 *
 * force asks every CLI again through /api/harnesses/refresh - the dashboard's
 * Refresh button.
 */
export async function load(force = false) {
  if (!force && cached && !fromStorage) return cached;
  if (!force && loading) return loading;
  const attempt = (async () => {
    try {
      const data = force
        ? await api('/api/harnesses/refresh', { method: 'POST', attempts: 1, timeout: 60000 })
        : await api('/api/harnesses', { attempts: 2, timeout: 12000 });
      cached = {
        harnesses: data.harnesses || [],
        workspace: data.workspace || EMPTY_WORKSPACE,
        sessions_available: data.sessions_available !== false,
        sessions_error: data.sessions_error || '',
        refreshed_at: data.refreshed_at || Date.now(),
      };
      fromStorage = false;
      writeCache(cached);
      return cached;
    } catch (err) {
      const stored = readCache();
      if (!stored) throw err;
      cached = stored;
      fromStorage = true;
      return cached;
    } finally {
      loading = null;
    }
  })();
  loading = attempt;
  return attempt;
}

// snapshot is what load() last produced, including the stored copy. It never
// fetches, so it is safe to call while drawing.
export function snapshot() {
  if (cached) return cached;
  const stored = readCache();
  if (!stored) return { harnesses: [], workspace: EMPTY_WORKSPACE, sessions_available: true, sessions_error: '' };
  cached = stored;
  fromStorage = true;
  return cached;
}

export function list() { return snapshot().harnesses; }
export function workspace() { return snapshot().workspace || EMPTY_WORKSPACE; }
export function harness(id) { return list().find((h) => h.id === id) || null; }

/** label is a harness's own name, for anywhere one is written out. */
export function label(id) {
  const found = harness(id);
  if (found && found.label) return found.label;
  return id ? id.charAt(0).toUpperCase() + id.slice(1) : '';
}

// stale says the catalogue on screen is last visit's rather than this
// machine's answer right now. The sheet says so out loud.
export function stale() { return fromStorage; }

// offered is what the sheet shows for one harness: the short list from the
// dashboard when there is one, otherwise everything the CLI reported.
export function offered(id) {
  const found = harness(id);
  if (!found) return [];
  if (found.picks && found.picks.length) return found.picks;
  return found.models || [];
}

// modelItems is the combobox's view of one harness's models: the id is the
// value, because that is what the server is given.
export function modelItems(id) {
  return offered(id).map((model) => ({
    value: model.id,
    label: model.label || model.id,
    hint: model.hint || '',
    group: model.group || '',
  }));
}

export function modelOf(id, modelId) {
  return offered(id).find((m) => m.id === modelId) || null;
}

// defaultModelFor is what the picker starts on: the first of the dashboard's
// short list, else what the CLI says is its default, else the entry that flags
// itself as one.
export function defaultModelFor(id) {
  const found = harness(id);
  if (!found) return '';
  if (found.picks && found.picks.length) return found.picks[0].id;
  if (found.default_model) return found.default_model;
  const flagged = (found.models || []).find((m) => m.default);
  return flagged ? flagged.id : '';
}

// needsModel is false for Shell, which runs a login shell and has nothing to
// choose. §E.3: the whole step is hidden, not disabled.
export function needsModel(id) {
  const found = harness(id);
  if (!found) return id !== 'shell';
  return (found.models || []).length > 0 || !!found.default_model;
}

// harnessFacts is what a CLI reported about itself, for the "i" beside its
// name: the build it is, and where it was found. It is detail, so it is hover
// only - the name and the mark are what a person picks by.
export function harnessFacts(entry) {
  const facts = [];
  if (!entry) return facts;
  if (entry.installed && entry.version) facts.push(entry.version);
  if (entry.installed && entry.path) facts.push(entry.path);
  return facts;
}

// segButton is the one control shape this sheet uses for a short, closed set
// of choices: a row of buttons, one of them pressed.
function segButton(text, value, sub, mark) {
  return el('button', {
    class: 'seg' + (mark ? ' with-mark' : ''),
    type: 'button',
    'data-value': value,
    'aria-pressed': 'false',
  }, mark || null, el('span', { class: 'seg-text' },
    el('span', { class: 'seg-label', text }),
    sub ? el('span', { class: 'seg-sub', text: sub }) : null));
}

function pressed(row, value) {
  for (const button of row.querySelectorAll('.seg')) {
    const on = button.dataset.value === value;
    button.classList.toggle('on', on);
    button.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
}

// dynamicPreview is the directory a dynamic session will get, spelled the way
// internal/harnesses/workdir.go spells it. It is shown greyed under the row so
// that "Dynamic" is a place and not a promise.
function dynamicPreview(id) {
  const root = workspace().root || '<workspace>';
  const now = new Date();
  const two = (n) => String(n).padStart(2, '0');
  const stamp = now.getFullYear() + two(now.getMonth() + 1) + two(now.getDate())
    + '-' + two(now.getHours()) + two(now.getMinutes()) + two(now.getSeconds());
  return root.replace(/\/$/, '') + '/' + (id || 'session') + '-' + stamp + '-…';
}

/**
 * openNewSessionSheet asks the three questions a session is bound by. It
 * resolves with {harness, model, effort, workdir_mode, workdir} or null if the
 * person backed out.
 *
 * The catalogue is loaded first, but a failure is not fatal: the stored copy
 * is used and the hint says where it came from.
 */
export async function openNewSessionSheet() {
  try {
    await load();
  } catch { /* the hint below says what happened */ }

  const sheet = document.getElementById('newSessionSheet');
  if (!sheet) return null;
  const harnessRow = document.getElementById('nsHarness');
  const dirRow = document.getElementById('nsDir');
  const dirPath = document.getElementById('nsDirPath');
  const dirHint = document.getElementById('nsDirHint');
  const modelField = document.getElementById('nsModelField');
  const modelHost = document.getElementById('nsModel');
  const effortField = document.getElementById('nsEffortField');
  const effortRow = document.getElementById('nsEffort');
  const hint = document.getElementById('nsHint');
  const start = document.getElementById('nsStart');
  const cancel = document.getElementById('nsCancel');

  const entries = list();
  const space = workspace();
  const pick = { harness: '', model: '', effort: '', mode: DYNAMIC, workdir: '' };

  harnessRow.innerHTML = '';
  if (!entries.length) {
    // A first ever visit that is already offline has nothing to show. Saying
    // so is the only honest answer; there is no id to guess at.
    hint.textContent = 'Socrates has not been able to ask this machine which programs are installed yet. '
      + 'Open this page once with a connection and the sheet will work offline after that.';
    start.disabled = true;
  }

  for (const entry of entries) {
    const usable = entry.enabled && entry.installed;
    const button = segButton(entry.label, entry.id, usable ? '' : 'not installed', agentMark(entry.id, 22));
    button.disabled = !usable;
    button.addEventListener('click', () => selectHarness(entry.id));
    // The build and the path sit behind an "i" on the corner of the button
    // rather than in the hint: they are the answer to a question almost
    // nobody asks, and the sheet is read by someone about to start work.
    const facts = harnessFacts(entry);
    harnessRow.append(el('span', { class: 'seg-cell' }, button,
      facts.length ? infoTip(facts, { label: entry.label + ' details', bubbleClass: 'mono' }) : null));
  }

  // The directory row: Dynamic, one cell per preset the dashboard named, and
  // Custom… when the dashboard allows a free-form path at all.
  dirRow.innerHTML = '';
  const dynamic = segButton('Dynamic', DYNAMIC, 'a fresh directory');
  dynamic.addEventListener('click', () => selectDir(DYNAMIC, ''));
  dirRow.append(dynamic);
  for (const preset of space.presets || []) {
    const button = segButton(preset.label || preset.path, PRESET + ':' + preset.path);
    button.addEventListener('click', () => selectDir(PRESET, preset.path));
    dirRow.append(button);
  }
  if (space.allow_custom) {
    const custom = segButton('Custom…', CUSTOM);
    custom.addEventListener('click', () => selectDir(CUSTOM, dirPath.value.trim()));
    dirRow.append(custom);
  }
  dirPath.addEventListener('input', () => {
    if (pick.mode !== CUSTOM) return;
    pick.workdir = dirPath.value.trim();
    renderDir();
    renderHint();
  });

  modelHost.innerHTML = '';
  const picker = combobox({
    value: '',
    items: () => modelItems(pick.harness),
    placeholder: 'sonnet',
    onChange: (value) => {
      pick.model = value.trim();
      // A model brings its own starting effort - the one set for it in the
      // dashboard, or the CLI's.
      const model = modelOf(pick.harness, pick.model);
      if (model) pick.effort = model.default_effort || '';
      renderEffort();
      renderHint();
    },
  });
  modelHost.append(picker.node);

  function selectDir(mode, path) {
    pick.mode = mode;
    pick.workdir = mode === DYNAMIC ? '' : path;
    dirPath.hidden = mode !== CUSTOM;
    if (mode === CUSTOM) dirPath.focus();
    renderDir();
    renderHint();
  }

  function renderDir() {
    pressed(dirRow, pick.mode === PRESET ? PRESET + ':' + pick.workdir : pick.mode);
    if (pick.mode === DYNAMIC) dirHint.textContent = dynamicPreview(pick.harness);
    else if (pick.mode === PRESET) dirHint.textContent = pick.workdir;
    else dirHint.textContent = pick.workdir || 'An absolute path on this machine.';
  }

  // renderEffort follows the model: whether there is an effort to pick at all
  // is the model's own answer.
  function renderEffort() {
    const efforts = effortsFor(pick.harness, pick.model);
    effortField.hidden = efforts.length === 0 || modelField.hidden;
    if (!efforts.length) {
      pick.effort = '';
      effortRow.innerHTML = '';
      return;
    }
    if (pick.effort && !efforts.includes(pick.effort)) pick.effort = '';
    effortRow.innerHTML = '';
    for (const value of ['', ...efforts]) {
      const button = segButton(effortLabel(value), value);
      button.addEventListener('click', () => {
        pick.effort = value;
        pressed(effortRow, pick.effort);
      });
      effortRow.append(button);
    }
    pressed(effortRow, pick.effort);
  }

  // renderHint is where everything the person needs to know but did not ask
  // ends up: what the CLI said about itself, why it cannot be used, and
  // whether this catalogue is even current.
  function renderHint() {
    const entry = harness(pick.harness);
    const parts = [];
    const snap = snapshot();
    if (snap.sessions_available === false && snap.sessions_error) parts.push(snap.sessions_error);
    if (entry) {
      if (entry.error) parts.push(entry.error);
      if (entry.notes) parts.push(entry.notes);
      if (entry.installed && !entry.static && !defaultModelFor(entry.id) && !pick.model && !modelField.hidden) {
        parts.push('Pick a model - ' + entry.label + ' does not name a default.');
      }
    }
    if (stale()) parts.push('Showing the programs from your last visit.');
    hint.textContent = parts.join(' ');
    const wantsModel = !modelField.hidden;
    const wantsPath = pick.mode === CUSTOM;
    start.disabled = !pick.harness
      || (wantsModel && !pick.model)
      || (wantsPath && !pick.workdir)
      || snap.sessions_available === false;
  }

  function selectHarness(id) {
    pick.harness = id;
    pressed(harnessRow, id);
    // Shell has nothing to run on, so the step is not there at all.
    modelField.hidden = !needsModel(id);
    pick.model = modelField.hidden ? '' : defaultModelFor(id);
    picker.setValue(pick.model, false);
    const model = modelOf(id, pick.model);
    pick.effort = model && model.default_effort ? model.default_effort : '';
    renderEffort();
    renderDir();
    renderHint();
  }

  // The dashboard's default, or the first usable one: the common case is two
  // taps.
  const first = entries.find((h) => h.id === space.default_harness && h.enabled && h.installed)
    || entries.find((h) => h.enabled && h.installed);
  selectDir(DYNAMIC, '');
  if (first) selectHarness(first.id);
  else renderHint();

  // The sheet is one element that is opened again and again, so every handler
  // this call adds is taken off with it. Without that, the second opening
  // would resolve the first opening's promise as well.
  return new Promise((resolve) => {
    const off = new AbortController();
    const on = { signal: off.signal };
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      off.abort();
      resolve(value);
      if (sheet.open) sheet.close();
    };
    start.addEventListener('click', () => {
      if (!pick.harness || (!modelField.hidden && !pick.model)) {
        toast('Pick a program and a model first.');
        return;
      }
      if (pick.mode === CUSTOM && !pick.workdir) {
        toast('Type the directory this session should work in.');
        return;
      }
      finish({
        harness: pick.harness,
        model: pick.model,
        effort: pick.effort,
        workdir_mode: pick.mode,
        workdir: pick.workdir,
      });
    }, on);
    cancel.addEventListener('click', () => finish(null), on);
    // Escape and a tap on the backdrop both mean no.
    sheet.addEventListener('cancel', (event) => {
      event.preventDefault();
      finish(null);
    }, on);
    // A tap beside the sheet means no. Deciding that from the event's target
    // alone is wrong, and expensively so: combobox.js takes an option on
    // mousedown and hides its list in the same breath, so by the time the
    // click arrives the option under the pointer is gone and the click is
    // retargeted to the dialog - which would read as a tap on the backdrop
    // and throw away the model the person had just picked. Where the pointer
    // was is the honest answer, so the click is measured against the sheet's
    // own rectangle instead.
    sheet.addEventListener('click', (event) => {
      if (event.target !== sheet) return;
      // A click with no pointer behind it - a keyboard activation, or one
      // dispatched by a script - has no coordinates to judge, and is never a
      // tap on the backdrop.
      if (!event.detail) return;
      const box = sheet.getBoundingClientRect();
      const outside = event.clientX < box.left || event.clientX > box.right
        || event.clientY < box.top || event.clientY > box.bottom;
      if (outside) finish(null);
    }, on);
    sheet.addEventListener('close', () => finish(null), on);
    sheet.showModal();
  });
}
