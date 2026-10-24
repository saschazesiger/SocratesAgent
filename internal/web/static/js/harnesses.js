// Which programs this machine can run a session on, where a session is
// allowed to work, and the sheet that binds a new one to all three - drawn
// by settingsForm, which the panel of a prepared session (prestart.js) draws
// its settings with too.
//
// A session is bound to a harness, a working directory and a model before it
// exists, and only the model can be changed afterwards. So the sheet is the
// only thing standing between the person and a terminal - which is why the
// catalogue is kept in local storage and the sheet renders from that copy when
// there is no connection. Everything it produces is checked again by the
// server, which is the real boundary: a stale copy degrades to "the server
// refuses that directory", never to a session in the wrong place.

import { api, el, toast } from './api.js';
import * as accounts from './accounts.js';
import { combobox, dropdown } from './combobox.js';
import { dictated } from './keyboard.js';
import { agentMark } from './logos.js';
import { hideOverlay, html, icon, iconEl, morph, raw, showOverlay, watchScrolled } from './ui.js';

const CACHE_KEY = 'socrates.harnesses';

// The two programs that are not agents. Nothing is asked of a shell or of an
// editor and nothing is reported back by them, so they have no model to run
// on and no status to be in: they are a window onto the machine, and a window
// is either open or it is not. Everywhere those two facts are needed - the
// second row of the sheet, the marks a session row wears - this is the list.
export const TOOLS = ['shell', 'vscode'];

export function isTool(id) { return TOOLS.includes(id); }

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
// untilReset is how long a limit window has until it renews, the way a
// person says it: "2h 36m", "3d 4h", "12m". Empty when the window says
// nothing about it, or its reset is already behind it.
export function untilReset(window, now = Date.now()) {
  const at = window && window.resets_at ? new Date(window.resets_at).getTime() : NaN;
  if (!Number.isFinite(at) || at <= now) return '';
  const minutes = Math.ceil((at - now) / 60000);
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const rest = minutes % 60;
  if (days > 0) return days + 'd' + (hours ? ' ' + hours + 'h' : '');
  if (hours > 0) return hours + 'h' + (rest ? ' ' + rest + 'm' : '');
  return rest + 'm';
}

// resetDetail is a window's renewal for a tooltip: how long, and when.
export function resetDetail(window) {
  const left = untilReset(window);
  if (!left) return '';
  return 'resets in ' + left + ' (' + new Date(window.resets_at).toLocaleString() + ')';
}

export function workspace() { return snapshot().workspace || EMPTY_WORKSPACE; }
export function harness(id) { return list().find((h) => h.id === id) || null; }

/**
 * isChat says whether a session of this program is a chat - driven through
 * its protocol and drawn by the page (REBUILD.md §3) - rather than a
 * terminal. Every coding agent is one; the Shell and VS Code are the
 * terminals.
 */
export function isChat(id) { return !!id && !isTool(id); }

/**
 * ideAvailable says whether a folder offers a VS Code: whether the dashboard
 * has it switched on. Whether it is installed is not asked - the probe that
 * says so can time out on a slow start, and a mark that comes and goes with
 * it is worse than an editor that says why it could not start.
 */
export function ideAvailable() {
  const found = harness('vscode');
  return !found || found.enabled !== false;
}

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
  if (!found) return !isTool(id);
  return (found.models || []).length > 0 || !!found.default_model;
}

// harnessCard is one program as the form offers it: a radio card with the
// program's own mark and name. A program that is not installed stays on the
// form rather than disappearing from it - knowing it exists and is not here
// is the answer to "why can I not pick Codex" - and says so in words.
function harnessCard(entry, name) {
  const usable = entry.enabled && entry.installed;
  return el('label', { class: usable ? 'ds-radio so-settings__program' : 'ds-radio ds-radio--disabled so-settings__program', 'data-value': entry.id },
    el('input', { type: 'radio', name, value: entry.id, disabled: !usable }),
    el('span', { class: 'so-settings__program-body' },
      agentMark(entry.id, 24),
      el('strong', { text: entry.label }),
      usable ? null : el('small', { class: 'ds-radio__note' }, iconEl('alert', 16), 'Not installed')));
}

/**
 * limitMeters is an account's limit windows ({label, used_percent,
 * resets_at}) as flat DS progress bars, the five hours and the week, each
 * with how much of it is used and how long until it renews. The figure is
 * always written out: the bar alone would say it by length only.
 */
export function limitMeters(all) {
  const windows = (all || []).filter((w) => w.label === '5h' || w.label === '7d');
  if (!windows.length) return '';
  return html`<div class="so-limits">${windows.map((w) => {
    const used = Math.max(0, Math.min(100, Math.round(Number(w.used_percent) || 0)));
    const left = untilReset(w);
    const title = w.label + ': ' + used + '% used' + (left ? ' · ' + resetDetail(w) : '');
    return html`<div class="so-limits__row" title="${title}"><span>${w.label}</span><div class="ds-progress ds-progress--flat" style="--ds-progress: ${used / 100}"><div class="ds-progress__track" role="progressbar" aria-label="${w.label + ' used'}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${used}"><div class="ds-progress__bar"></div></div></div><span class="so-limits__value">${used + '%'}</span><span class="so-limits__reset">${left ? left + ' left' : ''}</span></div>`;
  })}</div>`;
}

// accountFace is what the closed account field shows: the name, and quieter
// beside it who is signed in there.
function accountFace(name, who) {
  return html`<span class="so-drop__value">${name}</span>${who ? html`<span class="so-drop__meta">${who}</span>` : ''}`;
}

// The value a directory option carries. A preset is a mode and a path, and one
// string holds both so the dropdown has one value per entry and nothing has to
// be kept in step beside it.
const dirValue = (mode, path) => (mode === PRESET ? PRESET + ':' + path : mode);

// dirName is what a preset is called in the list: the name the dashboard gave
// it, or the last segment of its path. The path itself is the sub-text, so the
// list reads as places rather than as a column of prefixes.
function dirName(preset) {
  if (preset.label) return preset.label;
  const parts = String(preset.path || '').split('/').filter(Boolean);
  return parts.length ? parts[parts.length - 1] : preset.path;
}

// dirItems is the dropdown's view of where a session may work: a fresh
// directory, every place the dashboard named, and - when the dashboard allows
// one at all - a path typed by hand.
function dirItems() {
  const space = workspace();
  const items = [{ value: DYNAMIC, label: 'Dynamic', hint: 'a fresh directory for every session' }];
  for (const preset of space.presets || []) {
    items.push({ value: dirValue(PRESET, preset.path), label: dirName(preset), hint: preset.path });
  }
  if (space.allow_custom) items.push({ value: CUSTOM, label: 'Custom path…' });
  return items;
}

// dirLabel is how a chosen directory reads in the closed field.
function dirLabel(value) {
  const found = dirItems().find((item) => item.value === value);
  return found ? found.label : '';
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

// The programs a session can be prepared for: the agents, whose first message
// a prepared chat holds. A shell has nothing to send one to and VS Code is
// opened from a folder. The server's createPrestart (internal/server/
// prestart.go) is the real boundary.
export function canPrestart(id) { return isChat(id); }

// The account dropdown's value for "choose when it starts"; an account id is
// a-<hex>.
const AUTO_ACCOUNT = '@auto';

/**
 * settingsForm builds the controls a session is bound by: the program, the
 * working directory, the model, the effort, Ultracode and the account. A chat
 * (isChat) is asked for its folder, model, effort and account - agent,
 * folder, model, account - whether it may switch accounts by itself at a
 * limit, and Claude Code for Ultracode too; a Shell for its folder and
 * nothing else. The new-session sheet and the panel of a prepared session
 * both draw theirs with it, so the two can never offer different choices or
 * say different things about them - a prepared session has to start exactly
 * as the sheet would have started it.
 *
 * Every element id is `prefix` and the field's name, which is how the sheet
 * keeps the ids it has always had (nsHarness, nsDir, nsModelField, ...) and
 * the panel gets ids of its own (psHarness, ...).
 *
 *   include(entry)            which programs are offered at all
 *   deferredAccount(harness)  whether "Automatic" may leave the account to
 *                             the moment the chat starts - true for a
 *                             prepared chat, whose start can be hours away
 *   onChange(kind)            called after a change the person made; kind is
 *                             harness, dir, model, effort, ultracode, account
 *                             or auto_switch. Nothing calls it for set().
 *
 * It hands back the node to put on the page and the calls that read and
 * write it.
 */
export function settingsForm(options = {}) {
  const {
    prefix = 'ns',
    include = (entry) => entry.id !== 'vscode',
    deferredAccount = () => false,
    onChange = () => {},
  } = options;
  const id = (name) => prefix + name;

  /* ------------------------------------------------------------- markup */

  // The program is one choice in two groups of radio cards: the agents, and
  // under them the shell, which is not an agent - nothing is asked of it and
  // nothing is reported back - so the choice reads as two kinds of thing. VS
  // Code is not here at all: a folder has one editor, opened from its mark in
  // the sidebar. One radio name across both groups makes them one choice for
  // the keyboard as well.
  const harnessName = id('HarnessChoice');
  const harnessRow = el('div', { class: 'ds-radios so-settings__programs', id: id('Harness'), role: 'group', 'aria-label': 'Agent' });
  const toolsRow = el('div', { class: 'ds-radios so-settings__programs', id: id('Tools'), role: 'group', 'aria-label': 'Tool' });
  const harnessField = el('fieldset', { class: 'ds-field', id: id('HarnessField') },
    el('legend', { class: 'ds-field__label', text: 'Program' }),
    el('div', { class: 'so-settings__groups' }, harnessRow, toolsRow));

  const dirRow = el('div', { id: id('Dir') });
  const dirPath = el('input', {
    class: 'ds-input', type: 'text', id: id('DirPath'), spellcheck: 'false', 'aria-label': 'Directory path',
    autocorrect: 'off', autocapitalize: 'none', placeholder: '/srv/projects/socrates',
    'aria-describedby': id('DirHint'),
  });
  const dirHint = el('small', { class: 'ds-field__hint so-break', id: id('DirHint') });
  const dirField = el('div', { class: 'ds-field', id: id('DirField') },
    el('label', { class: 'ds-field__label', for: id('DirInput'), text: 'Working directory' }), dirRow, dirPath, dirHint);

  const modelHost = el('div', { id: id('Model') });
  const modelField = el('div', { class: 'ds-field', id: id('ModelField') },
    el('label', { class: 'ds-field__label', for: id('ModelInput'), text: 'Model' }), modelHost);

  // The levels are a handful of words, so they are radio chips: every one in
  // view and one tap away, which is what a phone in a car needs.
  const effortName = id('EffortChoice');
  const effortRow = el('div', { class: 'ds-chips', id: id('Effort') });
  const effortField = el('fieldset', { class: 'ds-field', id: id('EffortField'), hidden: true },
    el('legend', { class: 'ds-field__label', text: 'Effort' }), effortRow);

  // Claude Code only. Off every time the form is drawn, and fixed for the
  // session's life once it is started.
  const ultracodeInput = el('input', { type: 'checkbox', id: id('Ultracode'), 'aria-describedby': id('UltracodeHint') });
  const ultracodeField = el('div', { class: 'ds-field', id: id('UltracodeField'), hidden: true },
    el('label', { class: 'ds-check' }, ultracodeInput, el('span', { text: 'Ultracode' })),
    el('small', {
      class: 'ds-field__hint', id: id('UltracodeHint'),
      text: 'Dynamic workflows on every task: Claude Code orchestrates several agents for each request, '
        + 'which uses much more of the account’s limits.',
    }));

  // A chat runs on an account (REBUILD.md §4), the last thing it is asked
  // for: a dropdown of every account of the agent with how full it is and
  // when it renews, the one with the most left marked. A prepared chat may
  // leave it to its start.
  const accountRow = el('div', { id: id('Account') });
  const accountHint = el('small', { class: 'ds-field__hint', id: id('AccountHint') });
  const accountSignIn = el('button', {
    class: 'ds-btn ds-btn--text ds-btn--sm', type: 'button', id: id('AccountSignIn'), hidden: true, text: 'Sign in',
  });
  const accountField = el('div', { class: 'ds-field', id: id('AccountField'), hidden: true },
    el('label', { class: 'ds-field__label', for: id('AccountInput'), text: 'Account' }), accountRow,
    el('div', { class: 'so-settings__note' }, accountHint, accountSignIn));

  // Off unless asked for, on every opening: a chat that moves to another
  // account by itself spends that account's limits too.
  const autoSwitchInput = el('input', { type: 'checkbox', id: id('AutoSwitch'), 'aria-describedby': id('AutoSwitchHint') });
  const autoSwitchField = el('div', { class: 'ds-field', id: id('AutoSwitchField'), hidden: true },
    el('label', { class: 'ds-check' }, autoSwitchInput, el('span', { text: 'Switch accounts at a limit' })),
    el('small', {
      class: 'ds-field__hint', id: id('AutoSwitchHint'),
      text: 'When the account reaches a limit, the chat goes on with the account that has the most left.',
    }));

  // agent, folder, model and effort (and Ultracode, Claude Code's), then the
  // account (REBUILD.md §7).
  const node = el('div', { class: 'so-settings' }, harnessField,
    dirField, modelField, effortField, ultracodeField, accountField, autoSwitchField);

  /* -------------------------------------------------------------- state */

  // VS Code is not something a session is started as: a folder has one
  // editor, opened from the folder's own mark in the sidebar.
  const entries = list().filter((entry) => entry.id !== 'vscode' && include(entry));
  const space = workspace();
  const pick = {
    harness: '', model: '', effort: '', mode: DYNAMIC, workdir: '', ultracode: false,
    account: '', accountChosen: false, autoSwitch: false,
  };

  // Ultracode starts off on every opening: it costs a great deal more of the
  // account, so it is something asked for each time rather than remembered.
  ultracodeInput.checked = false;
  ultracodeInput.addEventListener('change', () => {
    pick.ultracode = ultracodeInput.checked;
    onChange('ultracode');
  });

  for (const entry of entries) {
    // The mark and the name are what a person picks by. The build a CLI is and
    // where it was found are not part of that choice, and are not here.
    // Which of the two groups it lands in is the one other thing the choice
    // is: an agent to give work to, or a window onto the machine.
    (isTool(entry.id) ? toolsRow : harnessRow).append(harnessCard(entry, harnessName));
  }
  // A group with nothing in it would be an empty box on the form.
  harnessRow.hidden = !harnessRow.childElementCount;
  toolsRow.hidden = !toolsRow.childElementCount;
  harnessField.addEventListener('change', (event) => {
    if (event.target.name !== harnessName) return;
    selectHarness(event.target.value);
    onChange('harness');
  });

  effortRow.addEventListener('change', (event) => {
    if (event.target.name !== effortName) return;
    pick.effort = event.target.value;
    onChange('effort');
  });

  // Where a session works is a dropdown rather than a row of cells: a machine
  // with a dozen preset directories on it would otherwise be a dozen cells
  // three characters wide. It is the same control the model is picked with,
  // strict because the list is the whole of what the server allows - Dynamic,
  // every preset, and a path typed by hand where the dashboard permits one.
  const dirPicker = combobox({
    value: DYNAMIC,
    strict: true,
    items: dirItems,
    display: dirLabel,
    emptyText: 'No directory matches',
    onChange: (value) => {
      if (value === DYNAMIC || value === CUSTOM) {
        selectDir(value, value === CUSTOM ? dirPath.value.trim() : '');
      } else {
        selectDir(PRESET, value.slice(PRESET.length + 1));
      }
      onChange('dir');
    },
  });
  dirPicker.input.id = id('DirInput');
  dirPicker.input.setAttribute('aria-describedby', id('DirHint'));
  dirRow.append(dictated(dirPicker.node, dirPicker.input));
  // The free-form path is a field like any other: it can be spoken, which with
  // the keyboard put away is the only way it can be filled. The row is what is
  // hidden and shown from here on, because hiding the field alone would leave
  // its microphone standing on the form with nothing beside it.
  const dirPathRow = dictated(dirPath, dirPath);
  dirPathRow.hidden = true;
  dirPath.addEventListener('input', () => {
    if (pick.mode !== CUSTOM) return;
    pick.workdir = dirPath.value.trim();
    renderDir();
    onChange('dir');
  });

  const picker = combobox({
    value: '',
    items: () => modelItems(pick.harness),
    placeholder: 'Pick a model, or type its id',
    onChange: (value) => {
      pick.model = value.trim();
      // A model brings its own starting effort - the one set for it in the
      // dashboard, or the CLI's.
      const model = modelOf(pick.harness, pick.model);
      if (model) pick.effort = model.default_effort || '';
      renderEffort();
      onChange('model');
    },
  });
  picker.input.id = id('ModelInput');
  modelHost.append(dictated(picker.node, picker.input));

  const accountPicker = dropdown({
    value: '',
    id: id('AccountInput'),
    label: 'Account',
    items: () => [
      ...(deferredAccount(pick.harness) ? [{ value: AUTO_ACCOUNT, auto: true }] : []),
      ...accounts.forAgent(pick.harness).map((a) => ({ value: a.id, account: a })),
    ],
    renderItem: (item) => (item.auto ? autoAccountEntry() : accounts.entry(item.account, {
      recommended: item.value === accounts.recommended(pick.harness) && accounts.forAgent(pick.harness).length > 1,
    })),
    renderValue: (item) => (item.auto ? accountFace('Automatic', '') : accountFace(item.account.label, accounts.who(item.account))),
    onChange: (value) => {
      pick.account = value;
      pick.accountChosen = true;
      renderAccountHint();
      onChange('account');
    },
  });
  accountPicker.button.setAttribute('aria-describedby', id('AccountHint'));
  accountRow.append(accountPicker.node);
  // The button under the field signs the chosen account in, or adds the
  // agent's first.
  accountSignIn.addEventListener('click', async () => {
    if (!accounts.forAgent(pick.harness).length) {
      const made = await accounts.addAccountDialog(accountSignIn, pick.harness);
      if (made) {
        pick.account = made.id;
        pick.accountChosen = true;
      }
    } else {
      const chosen = accounts.byId(pick.account);
      if (!chosen) return;
      await accounts.signIn(chosen, accountSignIn);
    }
    renderAccount();
    onChange('account');
  });
  autoSwitchInput.checked = false;
  autoSwitchInput.addEventListener('change', () => {
    pick.autoSwitch = autoSwitchInput.checked;
    onChange('auto_switch');
  });

  /* ------------------------------------------------------------ drawing */

  function selectDir(mode, path, focus = true) {
    pick.mode = mode;
    pick.workdir = mode === DYNAMIC ? '' : path;
    // Only "Custom path…" has anything left to ask, so it is the only choice
    // that puts a second field on the form.
    dirPathRow.hidden = mode !== CUSTOM;
    if (mode === CUSTOM && focus) dirPath.focus();
    renderDir();
  }

  function renderDir() {
    // `false`: the field is being told what was chosen, not told to choose.
    dirPicker.setValue(dirValue(pick.mode, pick.workdir), false);
    if (pick.mode === DYNAMIC) dirHint.textContent = dynamicPreview(pick.harness);
    else if (pick.mode === PRESET) dirHint.textContent = pick.workdir;
    // The path is in the field right above: the hint says what it has to be.
    else dirHint.textContent = 'An absolute path on this machine.';
  }

  // renderEffort follows the model: whether there is an effort to pick at all
  // is the model's own answer.
  function renderEffort() {
    const efforts = effortsFor(pick.harness, pick.model);
    effortField.hidden = efforts.length === 0 || modelField.hidden;
    if (!efforts.length) {
      pick.effort = '';
      morph(effortRow, '');
      return;
    }
    if (pick.effort && !efforts.includes(pick.effort)) pick.effort = '';
    morph(effortRow, html`${['', ...efforts].map((value) => html`<label class="ds-chip" data-value="${value}" data-ds-key="${'effort-' + value}"><input class="ds-chip__input" type="radio" name="${effortName}" value="${value}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${effortLabel(value)}</span></span></label>`)}`);
    // The checked state is the property, which a patch of the markup does not
    // carry: the choice is said here, after it.
    for (const input of effortRow.querySelectorAll('input')) input.checked = input.value === pick.effort;
  }

  // renderAccount offers a chat its agent's accounts. Until somebody picks
  // one, the chat starts on the one with the most usage left; a prepared chat
  // leaves that to its start. With no account at all the chat would start on
  // a Default account the server makes for it, not signed in yet: the field
  // says so, and adds one.
  function renderAccount() {
    const list = isChat(pick.harness) ? accounts.forAgent(pick.harness) : [];
    accountField.hidden = !isChat(pick.harness);
    accountRow.hidden = !list.length;
    // Switching needs somewhere to switch to.
    autoSwitchField.hidden = list.length < 2;
    if (autoSwitchField.hidden) {
      pick.autoSwitch = false;
      autoSwitchInput.checked = false;
    }
    if (!list.length) {
      pick.account = '';
      pick.accountChosen = false;
      accountHint.textContent = 'No ' + label(pick.harness) + ' account yet. Add one, or the chat starts on a new '
        + 'Default account that has to be signed in first.';
      accountSignIn.hidden = !isChat(pick.harness);
      accountSignIn.textContent = 'Add an account';
      return;
    }
    const known = pick.account === AUTO_ACCOUNT ? deferredAccount(pick.harness) : list.some((a) => a.id === pick.account);
    if (!pick.accountChosen || !known) {
      pick.accountChosen = false;
      const best = accounts.best(pick.harness);
      pick.account = deferredAccount(pick.harness) ? AUTO_ACCOUNT : (best ? best.id : '');
    }
    accountPicker.setValue(pick.account, false);
    renderAccountHint();
  }

  function renderAccountHint() {
    const chosen = accounts.byId(pick.account);
    let words = '';
    accountSignIn.hidden = true;
    if (pick.account === AUTO_ACCOUNT) {
      words = 'Chosen when the chat starts: the account with the most usage left at that moment.';
    } else if (chosen && !accounts.usable(chosen)) {
      // A subscription signs in from here; a key that was refused is
      // replaced on the dashboard's Accounts card.
      const key = chosen.kind === 'api_key';
      words = key ? 'The key of ' + chosen.label + ' was not taken; replace it on the dashboard.'
        : chosen.label + ' is not signed in yet.';
      accountSignIn.hidden = key;
      accountSignIn.textContent = 'Sign in';
    } else if (chosen && accounts.renewal(chosen)) {
      words = chosen.label + ' is at its limit and ' + accounts.renewal(chosen) + '.';
    } else if (!pick.accountChosen && chosen && chosen.id === accounts.recommended(pick.harness)
      && accounts.forAgent(pick.harness).length > 1) {
      words = 'Chosen automatically: the most usage left before it renews.';
    }
    accountHint.textContent = words;
  }

  function selectHarness(harnessId) {
    pick.harness = harnessId;
    // Another agent has other accounts.
    pick.accountChosen = false;
    // Ultracode is Claude Code's option.
    ultracodeField.hidden = harnessId !== 'claude';
    if (ultracodeField.hidden && pick.ultracode) {
      // Ultracode is Claude Code's; another program does not carry it along.
      pick.ultracode = false;
      ultracodeInput.checked = false;
    }
    // Both groups are one choice, so the card is checked by its value across
    // both: a script's choice (selectDefault, set) shows like a tap would.
    for (const input of harnessField.querySelectorAll('input[name="' + harnessName + '"]')) {
      input.checked = input.value === harnessId;
    }
    // Shell has nothing to run on, so the step is not there at all.
    modelField.hidden = !needsModel(harnessId);
    pick.model = modelField.hidden ? '' : defaultModelFor(harnessId);
    picker.setValue(pick.model, false);
    const model = modelOf(harnessId, pick.model);
    // The dashboard's default effort goes with the model a chat starts on;
    // any other model brings its own.
    const entry = harness(harnessId);
    pick.effort = entry && entry.default_effort && pick.model === entry.default_model ? entry.default_effort
      : (model && model.default_effort ? model.default_effort : '');
    renderEffort();
    renderDir();
    renderAccount();
  }

  /* ---------------------------------------------------------------- api */

  return {
    node,

    /** harness is the program chosen right now. */
    harness: () => pick.harness,

    /** value is what a session is created or prepared with. */
    value() {
      return {
        harness: pick.harness,
        model: modelField.hidden ? '' : pick.model,
        effort: pick.effort,
        ultracode: !ultracodeField.hidden && pick.ultracode,
        workdir_mode: pick.mode,
        workdir: pick.workdir,
        account: accountField.hidden || pick.account === AUTO_ACCOUNT ? '' : pick.account,
        auto_switch: !autoSwitchField.hidden && pick.autoSwitch,
      };
    },

    /** missing is what still has to be chosen, as a sentence; '' when nothing. */
    missing() {
      if (!entries.length) return 'There is no program to start yet.';
      if (!pick.harness || (!modelField.hidden && !pick.model)) return 'Pick a program and a model first.';
      if (pick.mode === CUSTOM && !pick.workdir) return 'Type the directory this session should work in.';
      // A chat started now needs an account it can run on; a prepared one
      // can be signed in to before it starts.
      const chosen = accountField.hidden ? null : accounts.byId(pick.account);
      if (chosen && !accounts.usable(chosen) && !deferredAccount(pick.harness)) {
        return 'Sign in to ' + chosen.label + ' first, or pick another account.';
      }
      return '';
    },

    /**
     * notes is what stands between the person and a session: why a program
     * cannot be used, what is still missing, and whether this catalogue is
     * even current. What a program is for is the dashboard's sentence, not
     * the form's - by the time this is open, the choice is being made.
     */
    notes() {
      const parts = [];
      if (!entries.length) {
        // A first ever visit that is already offline has nothing to show.
        // Saying so is the only honest answer; there is no id to guess at.
        parts.push('Socrates has not been able to ask this machine which programs are installed yet. '
          + 'Open this page once with a connection and the form will work offline after that.');
      }
      const entry = harness(pick.harness);
      if (entry) {
        if (entry.error) parts.push(entry.error);
        if (entry.installed && !entry.static && !defaultModelFor(entry.id) && !pick.model && !modelField.hidden) {
          parts.push('Pick a model - ' + entry.label + ' does not name a default.');
        }
      }
      if (stale()) parts.push('Showing the programs from your last visit.');
      return parts;
    },

    /**
     * selectDefault starts the form where a new session starts: on Dynamic,
     * and on the dashboard's default program or the first usable one - the
     * common case is two taps.
     */
    selectDefault() {
      const first = entries.find((h) => h.id === space.default_harness && h.enabled && h.installed)
        || entries.find((h) => h.enabled && h.installed);
      selectDir(DYNAMIC, '', false);
      if (first) selectHarness(first.id);
    },

    /**
     * set draws stored settings - a prepared session's row - without calling
     * onChange: it is the form being told, not the person choosing. Only the
     * keys that are present are applied.
     */
    set(values = {}) {
      if (values.harness !== undefined && values.harness !== pick.harness) selectHarness(values.harness);
      if (values.model !== undefined && !modelField.hidden) {
        pick.model = values.model || '';
        picker.setValue(pick.model, false);
      }
      if (values.effort !== undefined) pick.effort = values.effort || '';
      renderEffort();
      if (values.ultracode !== undefined) {
        pick.ultracode = !!values.ultracode && !ultracodeField.hidden;
        ultracodeInput.checked = pick.ultracode;
      }
      if (values.account !== undefined) {
        pick.accountChosen = !!values.account;
        pick.account = values.account || '';
      }
      if (values.auto_switch !== undefined) {
        pick.autoSwitch = !!values.auto_switch;
        autoSwitchInput.checked = pick.autoSwitch;
      }
      renderAccount();
      if (values.workdir_mode !== undefined) {
        const mode = values.workdir_mode || DYNAMIC;
        if (mode === CUSTOM) dirPath.value = values.workdir || '';
        selectDir(mode, values.workdir || '', false);
      }
    },

    /** refresh draws again what depends on the catalogue or on the caller. */
    refresh() {
      if (!pick.harness) return;
      renderAccount();
    },

    /** refreshAccounts reads the accounts and their usage again, then draws them. */
    async refreshAccounts() {
      await accounts.load().catch(() => { /* the copy this browser kept */ });
      if (node.isConnected && pick.harness) renderAccount();
    },
  };
}

// autoAccountEntry is the "Automatic" entry of a prepared chat's accounts,
// drawn the way an account is so the list reads as one list.
function autoAccountEntry() {
  return html`<span class="ds-combo__name">Automatic</span><span class="ds-combo__meta">the account with the most usage left when it starts</span>`;
}

/**
 * openNewSessionSheet asks the questions a session is bound by. It resolves
 * with {harness, model, effort, ultracode, workdir_mode, workdir, account,
 * auto_switch, prestart, chat} or null if the person backed out. `chat` says the agent is a chat: agent,
 * folder, model and account are all it asks; a Shell is a folder and nothing
 * else.
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
  const host = document.getElementById('nsForm');
  const prestartField = document.getElementById('nsPrestartField');
  const prestartInput = document.getElementById('nsPrestart');
  const hint = document.getElementById('nsHint');
  const hintText = document.getElementById('nsHintText');
  const start = document.getElementById('nsStart');
  if (!watched) {
    // The head's hairline while the body is scrolled (DS Side sheet).
    watched = true;
    watchScrolled(sheet.querySelector('.ds-sheet__body'), sheet.querySelector('.ds-sheet__panel'),
      { gap: 'var(--ds-space-3)' });
  }

  // PreStart is off on every opening: what the sheet is for, most of the
  // time, is a chat now.
  let prestartOn = false;
  const prepared = (harnessId) => prestartOn && canPrestart(harnessId);
  const form = settingsForm({
    prefix: 'ns',
    deferredAccount: prepared,
    onChange: () => render(),
  });
  // The form is built again on every opening, from the catalogue as it is
  // now; what the last opening drew goes with it.
  host.replaceChildren(form.node);
  if (prestartInput) prestartInput.checked = false;

  // render is the part of the sheet around the form: whether PreStart is on
  // offer, what the button says it does, and what stands in the way.
  function render() {
    const harnessId = form.harness();
    const later = prepared(harnessId);
    if (prestartField) prestartField.hidden = !canPrestart(harnessId);
    start.textContent = later ? 'Prepare' : 'Start session';
    const snap = snapshot();
    const parts = [];
    // What the machine can lack is a usable tmux, and only a terminal runs in
    // one: a chat - started now or prepared - runs in an agent host of its
    // own, so the reason is the Shell's alone.
    const blocked = !isChat(harnessId) && snap.sessions_available === false;
    if (blocked && snap.sessions_error) parts.push(snap.sessions_error);
    parts.push(...form.notes());
    const words = parts.join(' ');
    if (hintText) hintText.textContent = words;
    hint.hidden = !words;
    start.disabled = !!form.missing() || blocked;
  }

  form.selectDefault();
  render();
  form.refreshAccounts().then(() => { if (sheet.open) render(); });

  // The sheet is one element that is opened again and again, so every handler
  // this call adds is taken off with it. Without that, the second opening
  // would resolve the first opening's promise as well. Closing - the close
  // button, Cancel, Escape, a tap on the backdrop - is DS.overlay's, through
  // their data-close; whichever way it closes, a sheet that closed without
  // Start is a no.
  return new Promise((resolve) => {
    const off = new AbortController();
    const on = { signal: off.signal };
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      off.abort();
      resolve(value);
      if (sheet.open) hideOverlay(sheet);
    };
    if (prestartInput) {
      prestartInput.addEventListener('change', () => {
        prestartOn = prestartInput.checked;
        // The account list gains or loses "Automatic" with it.
        form.refresh();
        render();
      }, on);
    }
    start.addEventListener('click', () => {
      const missing = form.missing();
      if (missing) {
        toast(missing);
        return;
      }
      finish({ ...form.value(), prestart: prepared(form.harness()), chat: isChat(form.harness()) });
    }, on);
    sheet.addEventListener('close', () => finish(null), on);
    showOverlay(sheet);
  });
}

// watched says the sheet's scroll hairline is wired; it is one element for
// the page's life.
let watched = false;
