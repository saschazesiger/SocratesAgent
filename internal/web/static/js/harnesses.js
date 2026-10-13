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
import { combobox, dropdown } from './combobox.js';
import { dictated } from './keyboard.js';
import { agentMark } from './logos.js';

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
        shell_execution: data.shell_execution || { host_available: false, default: 'container' },
        profiles: data.profiles || [],
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
// profiles is every sign-in profile, the default one first. A catalogue from
// before profiles existed has none, and the sheet then offers no choice.
export function profiles() { return snapshot().profiles || []; }

// refreshProfiles asks again which profiles there are and who is signed in to
// them. The catalogue is loaded once per page, but a profile added in the
// dashboard or signed in to a minute ago is exactly what the next sheet has
// to show. A failure keeps what was there.
export async function refreshProfiles() {
  try {
    const data = await api('/api/profiles', { attempts: 1, timeout: 8000 });
    if (!cached) return;
    cached.profiles = data.profiles || [];
    cached.recommended = data.recommended || {};
    if (!fromStorage) writeCache(cached);
  } catch { /* the copy we have is the answer */ }
}

// recommendedProfile is the profile a new session of a program starts in when
// nobody chooses: the account with the most usage left for the time until it
// renews. undefined when there is nothing to choose by - no limits are known
// for the program, or there is one profile.
export function recommendedProfile(harnessId) {
  const recommended = snapshot().recommended || {};
  return Object.prototype.hasOwnProperty.call(recommended, harnessId) ? recommended[harnessId] : undefined;
}

// limitWindows is the two windows a profile's limits are judged by.
function limitWindows(profile, harnessId) {
  const windows = ((profile && profile.limits) || {})[harnessId] || [];
  return windows.filter((w) => w.label === '5h' || w.label === '7d');
}

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

// profileName is what a session's profile is called, for the session's facts.
export function profileName(id) {
  const found = profiles().find((p) => p.id === (id || ''));
  return found ? found.name : (id ? id : 'Default');
}

export function workspace() { return snapshot().workspace || EMPTY_WORKSPACE; }
export function harness(id) { return list().find((h) => h.id === id) || null; }

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

// The programs a profile means something to. VS Code is not on the sheet, and
// it has no sign-in of its own.
const PROFILE_HARNESSES = ['claude', 'codex', 'opencode', 'shell'];

const PROFILE_KEY = 'socrates.profile';

// The profile the last session was started in is where the next one starts:
// somebody working for a client all afternoon should not have to say so on
// every sheet. It is a per-browser convenience and nothing more.
function lastProfile() {
  try { return localStorage.getItem(PROFILE_KEY) || ''; } catch { return ''; }
}

function rememberProfile(id) {
  try { localStorage.setItem(PROFILE_KEY, id); } catch { /* private window */ }
}

// accountLabel is the line under a profile's name: who this program is
// signed in as there.
function accountLabel(profile, harnessId) {
  const login = (profile.accounts || {})[harnessId];
  if (!login) return '';
  if (login.account) return login.account;
  return login.signed_in ? 'signed in' : 'not signed in';
}

// signInHint says how to sign a program in to a profile it is not signed in
// to yet. Claude Code asks by itself; the other two are signed in from a shell.
function signInHint(profile, harnessId) {
  const login = (profile.accounts || {})[harnessId];
  if (!login || login.signed_in) return '';
  if (harnessId === 'claude') return 'Claude Code is not signed in to ' + profile.name + ' yet; the session opens on its sign-in.';
  if (harnessId === 'codex') return 'Codex is not signed in to ' + profile.name + ' yet. Start a container Shell in this profile and run codex login --device-auth.';
  if (harnessId === 'opencode') return 'OpenCode has no provider in ' + profile.name + ' yet. Start a container Shell in this profile and run opencode auth login.';
  return '';
}

// profileEntry is a profile as the dropdown shows it: its name, the account
// this program is signed in as there, and a bar for each limit window - how
// full each account is and how long until it renews, which is what choosing
// between them comes down to.
function profileEntry(profile, harnessId, recommended) {
  const meters = limitWindows(profile, harnessId).map((w) => {
    const used = Math.max(0, Math.min(100, Math.round(Number(w.used_percent) || 0)));
    const left = untilReset(w);
    const title = w.label + ': ' + used + '% used' + (left ? ' · ' + resetDetail(w) : '');
    const level = used >= 90 ? ' full' : used >= 70 ? ' high' : '';
    return el('span', { class: 'profile-meter' + level, title },
      el('span', { class: 'profile-meter-label', text: w.label }),
      el('span', { class: 'profile-meter-bar' }, el('span', { style: 'width:' + used + '%' })),
      el('span', { class: 'profile-meter-value', text: used + '%' }),
      left ? el('span', { class: 'profile-meter-reset', text: left + ' left' }) : null);
  });
  return el('span', { class: 'profile-entry' },
    el('span', { class: 'profile-entry-head' },
      el('span', { class: 'profile-entry-name', text: profile.name }),
      recommended ? el('span', { class: 'profile-entry-tag', text: 'most left' }) : null),
    el('span', { class: 'profile-entry-account', text: accountLabel(profile, harnessId) }),
    meters.length ? el('span', { class: 'profile-usage' }, meters) : null);
}

function pressed(row, value) {
  for (const button of row.querySelectorAll('.seg')) {
    const on = button.dataset.value === value;
    button.classList.toggle('on', on);
    button.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
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


// The programs a session can be prepared for: the three a first prompt can be
// handed to. A shell has nothing to send one to and VS Code is opened from a
// folder. The server's prestartHarness (internal/server/prestart.go) is the
// same list, and the real boundary.
export const PRESTART_HARNESSES = ['claude', 'codex', 'opencode'];

export function canPrestart(id) { return PRESTART_HARNESSES.includes(id); }

// The profile dropdown's value for "choose when it starts". A profile id is
// generated by the server and never starts with an @.
const AUTO_PROFILE = '@auto';

/**
 * settingsForm builds the controls a session is bound by: the program, how a
 * shell runs, the sign-in profile, the working directory, the model, the
 * effort and Ultracode. The new-session sheet and the panel of a prepared
 * session both draw theirs with it, so the two can never offer different
 * choices or say different things about them - a prepared session has to
 * start exactly as the sheet would have started it.
 *
 * Every element id is `prefix` and the field's name, which is how the sheet
 * keeps the ids it has always had (nsHarness, nsDir, nsModelField, ...) and
 * the panel gets ids of its own (psHarness, ...).
 *
 *   include(entry)            which programs are offered at all
 *   deferredProfile(harness)  whether "Automatic" may leave the profile to
 *                             the moment the session starts - true for a
 *                             prepared session, whose start can be hours away
 *   onChange(kind)            called after a change the person made; kind is
 *                             harness, shell, profile, dir, model, effort or
 *                             ultracode. Nothing calls it for set().
 *
 * It hands back the node to put on the page and the calls that read and
 * write it.
 */
export function settingsForm(options = {}) {
  const {
    prefix = 'ns',
    include = (entry) => entry.id !== 'vscode',
    deferredProfile = () => false,
    onChange = () => {},
  } = options;
  const id = (name) => prefix + name;

  /* ------------------------------------------------------------- markup */

  const harnessRow = el('div', { class: 'seg-row agents', id: id('Harness'), role: 'group', 'aria-label': 'Agent' });
  // The shell is not an agent: nothing is asked of it and nothing is reported
  // back. It sits on a row of its own under the agents, so the choice reads
  // as two kinds of thing. VS Code is not here at all: a folder has one
  // editor, opened from its mark in the sidebar.
  const toolsRow = el('div', { class: 'seg-row agents tools', id: id('Tools'), role: 'group', 'aria-label': 'Tool' });

  const shellExecutionRow = el('div', {
    class: 'seg-row', id: id('ShellExecution'), role: 'group', 'aria-label': 'Shell execution',
  }, segButton('Host', 'host', 'redirect to host'), segButton('Container', 'container', 'run inside container'));
  const shellExecutionHint = el('div', { class: 'hint', id: id('ShellExecutionHint') });
  const shellExecutionField = el('div', { class: 'field', id: id('ShellExecutionField'), hidden: true },
    el('label', { text: 'Shell execution' }), shellExecutionRow, shellExecutionHint);

  // Which sign-ins the session runs on. Only there when the dashboard has
  // more profiles than the default one.
  const profileRow = el('div', { id: id('Profile') });
  const profileHint = el('div', { class: 'hint', id: id('ProfileHint') });
  const profileField = el('div', { class: 'field', id: id('ProfileField'), hidden: true },
    el('label', { text: 'Profile' }), profileRow, profileHint);

  const dirRow = el('div', { id: id('Dir') });
  const dirPath = el('input', {
    class: 'input mono', type: 'text', id: id('DirPath'), spellcheck: 'false',
    autocorrect: 'off', autocapitalize: 'none', placeholder: '/srv/projects/socrates',
  });
  const dirHint = el('div', { class: 'hint mono', id: id('DirHint') });
  const dirField = el('div', { class: 'field', id: id('DirField') },
    el('label', { text: 'Working directory' }), dirRow, dirPath, dirHint);

  const modelHost = el('div', { id: id('Model') });
  const modelField = el('div', { class: 'field', id: id('ModelField') }, el('label', { text: 'Model' }), modelHost);

  const effortRow = el('div', { class: 'seg-row', id: id('Effort'), role: 'group', 'aria-label': 'Reasoning effort' });
  const effortField = el('div', { class: 'field', id: id('EffortField'), hidden: true },
    el('label', { text: 'Effort' }), effortRow);

  // Claude Code only. Off every time the sheet opens, and fixed for the
  // session's life once it is started.
  const ultracodeRow = el('div', { class: 'seg-row', id: id('Ultracode'), role: 'group', 'aria-label': 'Ultracode' },
    segButton('Off', 'off'), segButton('On', 'on'));
  const ultracodeField = el('div', { class: 'field', id: id('UltracodeField'), hidden: true },
    el('label', { text: 'Ultracode' }), ultracodeRow,
    el('div', {
      class: 'hint',
      text: 'Dynamic workflows on every task: Claude Code orchestrates several agents for each request, '
        + 'which uses much more of the account’s limits.',
    }));

  const node = el('div', { class: 'settings-form' }, harnessRow, toolsRow, shellExecutionField, profileField,
    dirField, modelField, effortField, ultracodeField);

  /* -------------------------------------------------------------- state */

  // VS Code is not something a session is started as: a folder has one
  // editor, opened from the folder's own mark in the sidebar.
  const entries = list().filter((entry) => entry.id !== 'vscode' && include(entry));
  const space = workspace();
  const shellCapability = snapshot().shell_execution || { host_available: false, default: 'container' };
  const pick = {
    harness: '', model: '', effort: '', mode: DYNAMIC, workdir: '',
    shellExecution: shellCapability.default === 'host' && shellCapability.host_available ? 'host' : 'container',
    profile: lastProfile(), profileChosen: false, profileAuto: false, ultracode: false,
  };

  const hostButton = shellExecutionRow.querySelector('[data-value="host"]');
  hostButton.disabled = !shellCapability.host_available;
  shellExecutionHint.textContent = shellCapability.host_available
    ? 'Host uses the host login shell and credentials. Container uses the shell and tools installed inside Socrates.'
    : 'Host execution needs a container started with host namespace access.';
  for (const button of shellExecutionRow.querySelectorAll('.seg')) {
    button.addEventListener('click', () => {
      pick.shellExecution = button.dataset.value;
      pressed(shellExecutionRow, pick.shellExecution);
      renderProfile();
      onChange('shell');
    });
  }
  pressed(shellExecutionRow, pick.shellExecution);

  // Ultracode starts off on every opening: it costs a great deal more of the
  // account, so it is something asked for each time rather than remembered.
  for (const button of ultracodeRow.querySelectorAll('.seg')) {
    button.addEventListener('click', () => {
      pick.ultracode = button.dataset.value === 'on';
      pressed(ultracodeRow, pick.ultracode ? 'on' : 'off');
      onChange('ultracode');
    });
  }
  pressed(ultracodeRow, 'off');

  for (const entry of entries) {
    const usable = entry.enabled && entry.installed;
    const button = segButton(entry.label, entry.id, usable ? '' : 'not installed', agentMark(entry.id, 22));
    button.disabled = !usable;
    button.addEventListener('click', () => {
      selectHarness(entry.id);
      onChange('harness');
    });
    // The mark and the name are what a person picks by. The build a CLI is and
    // where it was found are not part of that choice, and are not here.
    // Which of the two rows it lands in is the one other thing the choice is:
    // an agent to give work to, or a window onto the machine.
    (isTool(entry.id) ? toolsRow : harnessRow).append(button);
  }

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
  dirRow.append(dirPicker.node);
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
    placeholder: 'sonnet',
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
  modelHost.append(picker.node);

  // The profile is a dropdown rather than a row of cells: with more than
  // three profiles the cells were too narrow for a name, let alone the account
  // and its usage. Every entry in the list still shows how full its account
  // is, so the choice is made with the numbers in view. A prepared session
  // has one more entry on top: leave it to the moment it starts, when the
  // numbers that decide it are the ones that matter.
  const profilePicker = dropdown({
    value: pick.profile,
    label: 'Sign-in profile',
    items: () => [
      ...(deferredProfile(pick.harness) ? [{ value: AUTO_PROFILE, auto: true }] : []),
      ...profiles().map((p) => ({ value: p.id, profile: p })),
    ],
    renderItem: (item) => (item.auto ? autoEntry() : profileEntry(item.profile, pick.harness,
      recommendedProfile(pick.harness) === item.value && profiles().length > 1)),
    onChange: (value) => {
      if (value === AUTO_PROFILE) {
        pick.profileChosen = false;
        pick.profileAuto = true;
      } else {
        pick.profile = value;
        pick.profileChosen = true;
        pick.profileAuto = false;
        rememberProfile(value);
      }
      renderProfileHint();
      onChange('profile');
    },
  });
  profileRow.append(profilePicker.node);

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
        onChange('effort');
      });
      effortRow.append(button);
    }
    pressed(effortRow, pick.effort);
  }

  // deferred says whether the profile is left to the start right now.
  function deferred() {
    return !profileField.hidden && !pick.profileChosen && pick.profileAuto && deferredProfile(pick.harness);
  }

  // renderProfile offers the sign-in profiles to the programs that sign in -
  // and to a container shell, which is where Codex and OpenCode are signed in
  // to one. Each choice says which account it means for this program, which
  // is the whole point of choosing.
  function renderProfile() {
    const all = profiles();
    const onHost = pick.harness === 'shell' && pick.shellExecution === 'host';
    const wanted = all.length > 1 && PROFILE_HARNESSES.includes(pick.harness) && !onHost;
    profileField.hidden = !wanted;
    if (!wanted) return;
    // Until somebody picks a profile, the program decides: the account with
    // the most usage left before it renews, where that is known, and the
    // last one chosen otherwise. A session that starts later decides later.
    if (!pick.profileChosen) {
      if (deferredProfile(pick.harness)) {
        pick.profileAuto = true;
      } else {
        const recommended = recommendedProfile(pick.harness);
        pick.profileAuto = recommended !== undefined && all.some((p) => p.id === recommended);
        pick.profile = pick.profileAuto ? recommended : lastProfile();
      }
    }
    if (!all.some((p) => p.id === pick.profile)) pick.profile = '';
    // `false`: the field is being told what was chosen, and the entries are
    // drawn again for the program now on the form.
    profilePicker.setValue(deferred() ? AUTO_PROFILE : pick.profile, false);
    renderProfileHint();
  }

  function renderProfileHint() {
    const chosen = deferred() ? null : profiles().find((p) => p.id === pick.profile);
    const signIn = chosen ? signInHint(chosen, pick.harness) : '';
    let words = '';
    if (deferred()) words = 'Chosen when the session starts: the profile with the most usage left at that moment.';
    else if (pick.profileAuto) words = 'Chosen automatically: the most usage left before it renews.';
    profileHint.textContent = signIn || words;
  }

  function selectHarness(harnessId) {
    pick.harness = harnessId;
    shellExecutionField.hidden = harnessId !== 'shell';
    ultracodeField.hidden = harnessId !== 'claude';
    if (ultracodeField.hidden && pick.ultracode) {
      // Ultracode is Claude Code's; another program does not carry it along.
      pick.ultracode = false;
      pressed(ultracodeRow, 'off');
    }
    renderProfile();
    // Both rows are one choice, so both are told what was picked: the row that
    // does not hold it is the row that has to let go of it.
    pressed(harnessRow, harnessId);
    pressed(toolsRow, harnessId);
    // Shell has nothing to run on, so the step is not there at all.
    modelField.hidden = !needsModel(harnessId);
    pick.model = modelField.hidden ? '' : defaultModelFor(harnessId);
    picker.setValue(pick.model, false);
    const model = modelOf(harnessId, pick.model);
    pick.effort = model && model.default_effort ? model.default_effort : '';
    renderEffort();
    renderDir();
  }

  /* ---------------------------------------------------------------- api */

  return {
    node,

    /** harness is the program chosen right now. */
    harness: () => pick.harness,

    /**
     * value is what a session is created or prepared with. profile_auto is
     * only ever true where the profile is left to the start.
     */
    value() {
      const later = deferred();
      return {
        harness: pick.harness,
        model: modelField.hidden ? '' : pick.model,
        effort: pick.effort,
        shell_execution: pick.harness === 'shell' ? pick.shellExecution : undefined,
        profile: profileField.hidden || later ? '' : pick.profile,
        profile_auto: later,
        ultracode: !ultracodeField.hidden && pick.ultracode,
        workdir_mode: pick.mode,
        workdir: pick.workdir,
      };
    },

    /** missing is what still has to be chosen, as a sentence; '' when nothing. */
    missing() {
      if (!entries.length) return 'There is no program to start yet.';
      if (!pick.harness || (!modelField.hidden && !pick.model)) return 'Pick a program and a model first.';
      if (pick.mode === CUSTOM && !pick.workdir) return 'Type the directory this session should work in.';
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
        pressed(ultracodeRow, pick.ultracode ? 'on' : 'off');
      }
      if (values.profile_auto) {
        pick.profileChosen = false;
        pick.profileAuto = true;
      } else if (values.profile !== undefined) {
        pick.profileChosen = true;
        pick.profileAuto = false;
        pick.profile = values.profile || '';
      }
      renderProfile();
      if (values.workdir_mode !== undefined) {
        const mode = values.workdir_mode || DYNAMIC;
        if (mode === CUSTOM) dirPath.value = values.workdir || '';
        selectDir(mode, values.workdir || '', false);
      }
    },

    /** refresh draws again what depends on the catalogue or on the caller. */
    refresh() {
      if (!pick.harness) return;
      renderProfile();
    },

    /** refreshProfiles asks again who is signed in where, then draws it. */
    async refreshProfiles() {
      await refreshProfiles();
      if (node.isConnected && pick.harness) renderProfile();
    },
  };
}

// autoEntry is the "Automatic" entry of a prepared session's profile list,
// drawn the way a profile is so the list reads as one list.
function autoEntry() {
  return el('span', { class: 'profile-entry' },
    el('span', { class: 'profile-entry-head' }, el('span', { class: 'profile-entry-name', text: 'Automatic' })),
    el('span', { class: 'profile-entry-account', text: 'the most usage left when it starts' }));
}

/**
 * openNewSessionSheet asks the questions a session is bound by. It resolves
 * with {harness, model, effort, shell_execution, profile, profile_auto,
 * ultracode, workdir_mode, workdir, prestart} or null if the person backed
 * out.
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
  const prestartRow = document.getElementById('nsPrestart');
  const hint = document.getElementById('nsHint');
  const start = document.getElementById('nsStart');
  const cancel = document.getElementById('nsCancel');

  // preStart is off on every opening: what the sheet is for, most of the
  // time, is a terminal now.
  let prestartOn = false;
  const prepared = (harnessId) => prestartOn && canPrestart(harnessId);
  const form = settingsForm({
    prefix: 'ns',
    deferredProfile: prepared,
    onChange: () => render(),
  });
  // The form is built again on every opening, from the catalogue as it is
  // now; what the last opening drew goes with it.
  host.replaceChildren(form.node);

  for (const button of prestartRow ? prestartRow.querySelectorAll('.seg') : []) {
    button.onclick = () => {
      prestartOn = button.dataset.value === 'on';
      pressed(prestartRow, prestartOn ? 'on' : 'off');
      // The profile list gains or loses "Automatic" with it.
      form.refresh();
      render();
    };
  }
  if (prestartRow) pressed(prestartRow, 'off');

  // render is the part of the sheet around the form: whether preStart is on
  // offer, what the button says it does, and what stands in the way.
  function render() {
    const harnessId = form.harness();
    const later = prepared(harnessId);
    if (prestartField) prestartField.hidden = !canPrestart(harnessId);
    start.textContent = later ? 'Prepare' : 'Start session';
    const snap = snapshot();
    const parts = [];
    // A prepared session runs nothing, so a machine that cannot run sessions
    // right now is no reason to refuse one.
    const blocked = !later && snap.sessions_available === false;
    if (blocked && snap.sessions_error) parts.push(snap.sessions_error);
    parts.push(...form.notes());
    hint.textContent = parts.join(' ');
    start.disabled = !!form.missing() || blocked;
  }

  form.selectDefault();
  render();
  form.refreshProfiles().then(() => { if (sheet.open) render(); });

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
      const missing = form.missing();
      if (missing) {
        toast(missing);
        return;
      }
      finish({ ...form.value(), prestart: prepared(form.harness()) });
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
    //
    // An open list can hang past the sheet's edge, and an option taken there
    // leaves a click outside the rectangle too. So the press has to have
    // started on the backdrop as well: a press on an option never did.
    const outside = (event) => {
      if (event.target !== sheet) return false;
      const box = sheet.getBoundingClientRect();
      return event.clientX < box.left || event.clientX > box.right
        || event.clientY < box.top || event.clientY > box.bottom;
    };
    let pressedOutside = false;
    sheet.addEventListener('pointerdown', (event) => { pressedOutside = outside(event); }, on);
    sheet.addEventListener('click', (event) => {
      // A click with no pointer behind it - a keyboard activation, or one
      // dispatched by a script - has no coordinates to judge, and is never a
      // tap on the backdrop.
      if (!event.detail) return;
      const wasOutside = pressedOutside;
      pressedOutside = false;
      if (wasOutside && outside(event)) finish(null);
    }, on);
    sheet.addEventListener('close', () => finish(null), on);
    sheet.showModal();
  });
}
