// Which agents this machine has, and the sheet that binds a new chat to one
// of them.
//
// A chat is bound to an agent, a model and an effort before its first word,
// and that binding cannot be changed afterwards except for the model. So the
// sheet is the only thing standing between the person and a queued message -
// which is why the catalogue is kept in local storage and the sheet renders
// from that copy when there is no connection. The ids it produces are checked
// by the server when the chat is finally created; a stale copy degrades to
// "the agent reports a bad model as a run error", never to a lost message.

import { api, el, toast, infoTip } from './api.js';
import { combobox } from './combobox.js';
import { agentMark } from './logos.js';

const CACHE_KEY = 'socrates.agents';

// How a level reads on a button. A level nobody here has heard of is shown
// as the agent spells it: the agent named it, so the agent takes it.
const EFFORT_LABELS = {
  '': 'Default', minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High',
  xhigh: 'X-High', max: 'Max', ultra: 'Ultra',
};

export function effortLabel(value) {
  return EFFORT_LABELS[value] || value;
}

// effortsFor is the levels to offer: the chosen model's own list, or - before
// a model is chosen, or for one the agent has not reported - every level any
// of the agent's models offers. The levels differ between agents (Claude Code
// goes up to max, Codex to xhigh, OpenCode names them per model), so there is
// no fixed row.
export function effortsFor(agentId, modelId) {
  const model = modelOf(agentId, modelId);
  if (model && model.efforts && model.efforts.length) return model.efforts;
  const found = agent(agentId);
  if (!found) return [];
  const all = [];
  for (const m of found.models || []) for (const e of m.efforts || []) if (!all.includes(e)) all.push(e);
  return all;
}

let cached = null;      // the agents array, whatever its provenance
let fromStorage = false; // whether what we have is last visit's copy
let loading = null;

function readCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed.agents) ? parsed : null;
  } catch { return null; }
}

function writeCache(agents, at) {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify({ at, agents }));
  } catch { /* private window, or site data blocked */ }
}

/**
 * load fetches the catalogue, once. A failure falls back to the copy from the
 * last visit rather than leaving the picker empty, because a picker that
 * cannot be drawn is a message that cannot be typed.
 *
 * force asks every CLI again through /api/agents/refresh - the dashboard's
 * Refresh button.
 */
export async function load(force = false) {
  if (!force && cached && !fromStorage) return cached;
  if (!force && loading) return loading;
  const attempt = (async () => {
    try {
      const data = force
        ? await api('/api/agents/refresh', { method: 'POST', attempts: 1, timeout: 60000 })
        : await api('/api/agents', { attempts: 2, timeout: 12000 });
      cached = data.agents || [];
      fromStorage = false;
      writeCache(cached, data.refreshed_at || Date.now());
      return cached;
    } catch (err) {
      const stored = readCache();
      if (!stored) throw err;
      cached = stored.agents;
      fromStorage = true;
      return cached;
    } finally {
      loading = null;
    }
  })();
  loading = attempt;
  return attempt;
}

// list is what load() last produced, including the stored copy. It never
// fetches, so it is safe to call while drawing.
export function list() {
  if (cached) return cached;
  const stored = readCache();
  if (!stored) return [];
  cached = stored.agents;
  fromStorage = true;
  return cached;
}

export function agent(id) {
  return list().find((a) => a.id === id) || null;
}

// stale says the list on screen is last visit's rather than this machine's
// answer right now. The sheet says so out loud.
export function stale() {
  return fromStorage;
}

// offered is what the sheet shows for one agent: the short list from the
// dashboard when there is one, otherwise everything the agent reported. The
// dashboard itself always works from the full list.
export function offered(agentId) {
  const found = agent(agentId);
  if (!found) return [];
  if (found.picks && found.picks.length) return found.picks;
  return found.models || [];
}

// modelItems is the combobox's view of one agent's models: the id is the
// value, because that is what the server is given.
export function modelItems(agentId) {
  return offered(agentId).map((model) => ({
    value: model.id,
    label: model.label || model.id,
    hint: model.hint || '',
    group: model.group || '',
  }));
}

// modelOf finds one model entry, so the effort control can ask what it offers.
export function modelOf(agentId, modelId) {
  return offered(agentId).find((m) => m.id === modelId) || null;
}

// defaultModelFor is what the picker starts on: the first of the dashboard's
// short list, else what the agent says is its default, else the entry that
// flags itself as one.
export function defaultModelFor(agentId) {
  const found = agent(agentId);
  if (!found) return '';
  if (found.picks && found.picks.length) return found.picks[0].id;
  if (found.default_model) return found.default_model;
  const flagged = (found.models || []).find((m) => m.default);
  return flagged ? flagged.id : '';
}

// segButton is the one control shape this sheet uses for a short, closed set
// of choices: a row of buttons, one of them pressed.
function segButton(label, value, sub, mark) {
  return el('button', {
    class: 'seg' + (mark ? ' with-mark' : ''),
    type: 'button',
    'data-value': value,
    'aria-pressed': 'false',
  }, mark || null, el('span', { class: 'seg-text' },
    el('span', { class: 'seg-label', text: label }),
    sub ? el('span', { class: 'seg-sub', text: sub }) : null));
}

// agentFacts is what an agent reported about itself, for the small mark
// beside its name: the build it is, and where it was found. It is detail, so
// it is hover only - the name and the logo are what a person picks by.
export function agentFacts(entry) {
  const facts = [];
  if (!entry) return facts;
  if (entry.installed && entry.version) facts.push(entry.version);
  if (entry.installed && entry.path) facts.push(entry.path);
  return facts;
}

function pressed(row, value) {
  for (const button of row.querySelectorAll('.seg')) {
    const on = button.dataset.value === value;
    button.classList.toggle('on', on);
    button.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
}

/**
 * openNewChatSheet asks who should answer. It resolves with
 * {agent, model, effort} or null if the person backed out.
 *
 * The catalogue is loaded first, but a failure is not fatal: the stored copy
 * is used and the hint says where it came from.
 */
export async function openNewChatSheet() {
  try {
    await load();
  } catch { /* the hint below says what happened */ }

  const sheet = document.getElementById('newChatSheet');
  const agentRow = document.getElementById('ncAgent');
  const modelHost = document.getElementById('ncModel');
  const effortField = document.getElementById('ncEffortField');
  const effortRow = document.getElementById('ncEffort');
  const hint = document.getElementById('ncHint');
  const start = document.getElementById('ncStart');
  const cancel = document.getElementById('ncCancel');
  if (!sheet) return null;

  const agents = list();
  const pick = { agent: '', model: '', effort: '' };

  agentRow.innerHTML = '';
  if (!agents.length) {
    // A first ever visit that is already offline has nothing to show. Saying
    // so is the only honest answer; there is no id to guess at.
    hint.textContent = 'Socrates has not been able to ask this machine which agents are installed yet. '
      + 'Open this page once with a connection and the picker will work offline after that.';
    start.disabled = true;
  }

  for (const entry of agents) {
    const usable = entry.enabled && entry.installed;
    const button = segButton(entry.label, entry.id, usable ? '' : 'not installed', agentMark(entry.id, 20));
    button.disabled = !usable;
    button.addEventListener('click', () => selectAgent(entry.id));
    // The build and the path sit behind an "i" on the corner of the button
    // rather than in the hint: they are the answer to a question almost
    // nobody asks, and the sheet is read by someone about to type.
    const facts = agentFacts(entry);
    const cell = el('span', { class: 'seg-cell' }, button,
      facts.length ? infoTip(facts, { label: entry.label + ' details', bubbleClass: 'mono' }) : null);
    agentRow.append(cell);
  }

  modelHost.innerHTML = '';
  const picker = combobox({
    value: '',
    items: () => modelItems(pick.agent),
    placeholder: 'sonnet',
    onChange: (value) => {
      pick.model = value.trim();
      // A model brings its own starting effort - the one set for it in the
      // dashboard, or the agent's - the same as when the agent is switched.
      const model = modelOf(pick.agent, pick.model);
      if (model) pick.effort = model.default_effort || '';
      renderEffort();
      renderHint();
    },
  });
  modelHost.append(picker.node);

  // renderEffort follows the model: whether there is an effort to pick at all
  // is the model's own answer, and has_effort only decides whether the control
  // can be shown before one is chosen.
  function renderEffort() {
    const efforts = effortsFor(pick.agent, pick.model);
    effortField.hidden = efforts.length === 0;
    if (!efforts.length) {
      pick.effort = '';
      effortRow.innerHTML = '';
      return;
    }
    if (pick.effort && !efforts.includes(pick.effort)) pick.effort = '';
    // The row is the model's own list, so it is rebuilt for every model.
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
  // ends up: what the agent said about itself, why it cannot be used, and
  // whether this list is even current.
  function renderHint() {
    const entry = agent(pick.agent);
    const parts = [];
    if (entry) {
      if (entry.error) parts.push(entry.error);
      if (entry.notes) parts.push(entry.notes);
      // Codex and OpenCode have no default of their own: the catalogue reports
      // what this installation has connected, and one of them has to be named.
      if (entry.installed && !entry.static && !defaultModelFor(entry.id) && !pick.model) {
        parts.push('Pick a model - ' + entry.label + ' does not name a default.');
      }
    }
    if (stale()) parts.push('Showing the agents from your last visit.');
    hint.textContent = parts.join(' ');
    start.disabled = !pick.agent || !pick.model;
  }

  function selectAgent(id) {
    pick.agent = id;
    pressed(agentRow, id);
    pick.model = defaultModelFor(id);
    picker.setValue(pick.model, false);
    const model = modelOf(id, pick.model);
    pick.effort = model && model.default_effort ? model.default_effort : '';
    renderEffort();
    renderHint();
  }

  // The first usable agent is preselected, so the common case is two taps.
  const first = agents.find((a) => a.enabled && a.installed);
  if (first) selectAgent(first.id);
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
      if (!pick.agent || !pick.model) {
        toast('Pick an agent and a model first.');
        return;
      }
      finish({ agent: pick.agent, model: pick.model, effort: pick.effort });
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
    // retargeted to the dialog - which used to read as a tap on the backdrop
    // and threw away the model the person had just picked. Where the pointer
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
