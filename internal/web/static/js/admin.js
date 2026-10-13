// The admin dashboard: everything about Socrates is configurable here.
//
// The page is one settings document with a form drawn over it. Every control
// writes straight into that document and Save sends the whole thing back, so
// an unsaved change is undone by leaving the page and a save is one request
// that either succeeded or did not.
//
// The harness cards are not hand written: OPTIONS below is the catalogue of
// what a program's card offers, so a new setting is one entry rather than a
// field, a reader, a writer and a paragraph of HTML.
//
// That catalogue is deliberately four lines long. Until 2026-09-02 it held
// some seventy options - permission modes, sandbox policies, remote control,
// raw flag and environment textareas - and the owner reversed that: the
// dashboard offers the basics, and every flag a session is started with is
// fixed policy inside the app. What that policy is, and where each flag in it
// was verified, is in docs/design/HARNESS-POLICY.md.

import {
  api, el, toast, isOffline, errorMessage, setClass, onWake, infoTip, LiveStream,
} from './api.js';
import { agentMark } from './logos.js';
import { speak, speechKind } from './voice.js';
import { combobox } from './combobox.js';
import * as harnesses from './harnesses.js';

const $ = (id) => document.getElementById(id);

// busyButton is the smallest honest answer to a tap that has to wait for the
// network: the control stops taking taps and says it is working.
function busyButton(button, on, label) {
  if (!button) return;
  button.disabled = on;
  setClass(button, 'is-busy', on);
  if (label !== undefined) {
    if (on) {
      if (button.dataset.idleLabel === undefined) button.dataset.idleLabel = button.textContent;
      button.textContent = label;
    } else if (button.dataset.idleLabel !== undefined) {
      button.textContent = button.dataset.idleLabel;
    }
  }
}

let settings = null;
let defaults = null;

// The transcription model is a searchable field rather than a plain text one,
// so it is not in FIELDS. There is no model catalogue endpoint any more - it
// went with the chat API - so the combobox is what it is with no items: a text
// field that takes any id, with the id already in use offered back.
const MODEL_PICKERS = [
  ['orTranscribe', 'openrouter.transcribe_model'],
  ['orStatus', 'openrouter.status_model'],
  ['orTitle', 'openrouter.title_model'],
];

// FIELDS is every control that is one element and one path. Everything with a
// shape of its own - the presets, the harness cards, the model short lists -
// is built and read below.
const FIELDS = [
  ['orKey', 'openrouter.api_key'],
  ['workspaceRoot', 'workspace.root'],
  ['allowCustomDir', 'workspace.allow_custom', 'bool'],
  ['windowSize', 'terminal.window_size'],
  ['historyLimit', 'terminal.history_limit', 'int'],
  ['scrollback', 'terminal.scrollback', 'int'],
  ['fontSize', 'terminal.font_size', 'int'],
  ['mouseOn', 'terminal.mouse', 'bool'],
  ['extendedKeys', 'terminal.extended_keys', 'bool'],
  ['webgl', 'terminal.webgl', 'bool'],
  ['voiceLanguage', 'voice.language'],
  ['sttPrompt', 'voice.stt_prompt'],
  ['googleKey', 'voice.google_api_key'],
  ['googleVoiceEn', 'voice.google_voice_en'],
  ['googleVoiceDe', 'voice.google_voice_de'],
  ['ttsRate', 'voice.tts_rate', 'number'],
  ['tunnelMode', 'tunnel.mode'],
  ['tunnelToken', 'tunnel.token'],
  ['tunnelHostname', 'tunnel.hostname'],
  ['tunnelCommand', 'tunnel.command'],
  ['tunnelArgs', 'tunnel.extra_args', 'args'],
];

const WINDOW_SIZE_HINTS = {
  manual: 'Socrates sizes the window to the viewer that last connected or resized; typing never changes it.',
  latest: 'The window follows whichever viewer last typed or attached.',
  largest: 'The window is as large as the biggest viewer, and smaller viewers pan over it.',
};

let tunnelTimer = null;
let installHints = {};
let localURL = '';

function getPath(object, path) {
  return path.split('.').reduce((acc, key) => (acc == null ? acc : acc[key]), object);
}
function setPath(object, path, value) {
  const keys = path.split('.');
  const last = keys.pop();
  const target = keys.reduce((acc, key) => (acc[key] = acc[key] || {}), object);
  target[last] = value;
}

/* ------------------------------------------------------- the option catalogue */

// The effort levels each program's flag accepts. Neither list is the other:
// --effort rejects "minimal", and Codex does not validate its value at all, so
// a typo there would reach the model as configuration and be ignored.
const CLAUDE_EFFORTS = ['', 'low', 'medium', 'high', 'xhigh', 'max'];
const CODEX_EFFORTS = ['', 'minimal', 'low', 'medium', 'high', 'xhigh'];

// What a program's card offers, besides the binary path and the model short
// list that every card has. A session started without a choice of its own gets
// these; everything else about the launch is policy, not a setting.
const OPTIONS = {
  shell: [],
  vscode: [],
  claude: [
    { key: 'default_model', label: 'Default model', type: 'model',
      hint: 'A session started without a model of its own uses this one.' },
    { key: 'default_effort', label: 'Default effort', type: 'select', values: CLAUDE_EFFORTS,
      hint: 'Empty leaves Claude Code on its own default.' },
  ],
  codex: [
    { key: 'default_model', label: 'Default model', type: 'model',
      hint: 'A session started without a model of its own uses this one.' },
    { key: 'default_effort', label: 'Default effort', type: 'select', values: CODEX_EFFORTS,
      hint: 'Empty leaves Codex on its own default.' },
  ],
  opencode: [
    { key: 'default_model', label: 'Default model', type: 'model',
      hint: 'provider/model. A session started without a model of its own uses this one.' },
  ],
};

// Every option in the catalogue is start-only: a session that is running keeps
// the command line it was launched with.
const START_ONLY_NOTE = 'These apply to sessions started from now on.';

/* ------------------------------------------------------------------- boot */

// The admin page is loaded once. If the connection was down at that moment it
// would otherwise stay blank for good, so it retries and comes back on its own.
boot();

function boot(attempt = 1) {
  load().catch((err) => {
    if (!isOffline(err)) {
      toast(errorMessage(err), 'error');
      return;
    }
    setTimeout(() => boot(attempt + 1), Math.min(15000, 1500 * attempt));
  });
}

async function load() {
  const data = await api('/api/settings');
  settings = data.settings;
  defaults = data.defaults;
  $('versionLabel').textContent = 'Socrates ' + (data.version || '');
  fillForm();
  buildModelPickers();
  renderDefaultHarness();
  renderPresets();
  bind();
  loadProfiles();
  loadHarnesses();
  refreshTmux();
  if (data.local_url) localURL = data.local_url;
  refreshTunnel();
  if (new URLSearchParams(location.search).get('welcome')) {
    showNotice('Welcome. Check the terminal engine below, add your OpenRouter key if you want to '
      + 'dictate, then head back to the sessions.', 'ok');
  }
  const tunnelError = sessionStorage.getItem('socrates.tunnel_error');
  if (tunnelError) {
    sessionStorage.removeItem('socrates.tunnel_error');
    showNotice('The tunnel could not be started: ' + tunnelError, 'error');
  }
}

function fillForm() {
  for (const [id, path, type] of FIELDS) {
    const node = $(id);
    if (!node) continue;
    const value = getPath(settings, path);
    if (type === 'bool') node.checked = !!value;
    else if (type === 'args') node.value = (value || []).join(' ');
    else node.value = value ?? '';
  }
  renderWindowSizeHint();
}

function collect() {
  const next = JSON.parse(JSON.stringify(settings));
  for (const [id, path, type] of FIELDS) {
    const node = $(id);
    if (!node) continue;
    if (type === 'bool') setPath(next, path, node.checked);
    else if (type === 'int') setPath(next, path, Math.round(Number(node.value) || 0));
    else if (type === 'number') setPath(next, path, Number(node.value) || 0);
    else if (type === 'args') setPath(next, path, splitArgs(node.value));
    else setPath(next, path, node.value);
  }
  next.workspace.presets = readPresets();
  return next;
}

function renderWindowSizeHint() {
  const node = $('windowSizeHint');
  if (node) node.textContent = WINDOW_SIZE_HINTS[$('windowSize').value] || '';
}

/* ---------------------------------------------------------- model pickers */

const pickers = {};

// buildModelPickers fills the empty slots the page leaves for them. They are
// built before anything arrives, so the dashboard is usable straight away.
function buildModelPickers() {
  for (const [id, path] of MODEL_PICKERS) {
    const host = $(id);
    if (!host) continue;
    host.innerHTML = '';
    const picker = combobox({
      value: getPath(settings, path) || '',
      items: () => [],
      placeholder: 'google/gemini-2.5-flash',
      onChange: (value) => setPath(settings, path, value),
    });
    pickers[id] = picker;
    host.append(picker.node);
  }
}

// syncModelPickers pushes values back into the dropdowns after a save.
function syncModelPickers() {
  for (const [id, path] of MODEL_PICKERS) {
    if (pickers[id]) pickers[id].setValue(getPath(settings, path) || '', false);
  }
}

/* ------------------------------------------------------------- workspace */

function renderDefaultHarness() {
  const host = $('defaultHarness');
  if (!host) return;
  host.innerHTML = '';
  for (const id of ['shell', 'claude', 'codex', 'opencode', 'vscode']) {
    const button = el('button', {
      class: 'seg with-mark', type: 'button', 'data-value': id,
      onclick: () => {
        setPath(settings, 'workspace.default_harness', id);
        renderDefaultHarness();
      },
    }, agentMark(id, 18), el('span', { class: 'seg-text' },
      el('span', { text: harnessLabel(id) })));
    setClass(button, 'on', getPath(settings, 'workspace.default_harness') === id);
    button.setAttribute('aria-pressed', String(getPath(settings, 'workspace.default_harness') === id));
    host.append(button);
  }
}

// The preset editor is a repeating row, read back as a list when the page is
// saved rather than written into the document on every keystroke: a half-typed
// path is not a preset yet.
function presetRow(preset = { label: '', path: '' }) {
  const label = el('input', { class: 'input', type: 'text', placeholder: 'Name', value: preset.label || '' });
  const path = el('input', {
    class: 'input mono', type: 'text', spellcheck: 'false', placeholder: '/srv/projects',
    value: preset.path || '',
  });
  const row = el('div', { class: 'preset-row' }, label, path,
    el('button', {
      class: 'btn sm', type: 'button', text: 'Remove', 'aria-label': 'Remove this directory',
      onclick: () => row.remove(),
    }));
  return row;
}

function renderPresets() {
  const host = $('presetDirs');
  if (!host) return;
  host.innerHTML = '';
  for (const preset of getPath(settings, 'workspace.presets') || []) host.append(presetRow(preset));
}

function readPresets() {
  const host = $('presetDirs');
  if (!host) return [];
  return [...host.querySelectorAll('.preset-row')].map((row) => {
    const [label, path] = row.querySelectorAll('input');
    return { label: label.value.trim(), path: path.value.trim() };
  }).filter((preset) => preset.path !== '');
}

/* -------------------------------------------------------------- profiles */

// Profiles are not part of the settings document: each change is its own
// request and is live at once, because a profile is a directory on the
// machine and the document's Save would otherwise be what decides when a
// directory full of sign-ins is deleted.

const LOGIN_NAMES = [['claude', 'Claude'], ['codex', 'Codex'], ['opencode', 'OpenCode']];

async function loadProfiles() {
  const host = $('profileList');
  if (!host) return;
  try {
    const data = await api('/api/profiles');
    renderProfiles(data.profiles || []);
  } catch (err) {
    host.textContent = errorMessage(err);
  }
}

function loginSummary(profile) {
  const accounts = profile.accounts || {};
  return LOGIN_NAMES.map(([id, name]) => {
    const login = accounts[id] || {};
    const who = login.account || (login.signed_in ? 'signed in' : 'not signed in');
    const limits = ((profile.limits || {})[id] || [])
      .filter((w) => w.label === '5h' || w.label === '7d')
      .map((w) => {
        const left = harnesses.untilReset(w);
        return w.label + ' ' + Math.round(w.used_percent) + '%' + (left ? ' · ' + left + ' left' : '');
      }).join(', ');
    return name + ': ' + who + (limits ? ' (' + limits + ')' : '');
  }).join(' · ') + (profile.sessions ? ' · ' + profile.sessions + ' session' + (profile.sessions === 1 ? '' : 's') : '');
}

function renderProfiles(list) {
  const host = $('profileList');
  host.innerHTML = '';
  for (const profile of list) {
    const row = profileRow(profile);
    row.dataset.id = profile.id;
    row.prepend(profileGrip(profile));
    host.append(row);
  }
}

// The order of the list is the order the new-session sheet offers the
// profiles in. A row is moved by its grip - dragged with a mouse or a finger,
// or with the arrow keys once the grip has the focus - and the order is saved
// the moment it is let go.

function profileOrder(host) {
  return [...host.querySelectorAll('.profile-row')].map((row) => row.dataset.id);
}

async function saveProfileOrder(host, before) {
  const order = profileOrder(host);
  if (order.join('\n') === before.join('\n')) return;
  try {
    await api('/api/profiles/order', { method: 'PUT', body: { order } });
  } catch (err) {
    toast(errorMessage(err), 'error');
    await loadProfiles();
  }
}

function profileGrip(profile) {
  const grip = el('button', {
    class: 'profile-grip', type: 'button', title: 'Drag to reorder',
    'aria-label': 'Move ' + profile.name + ' (arrow keys)',
    html: '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="9" cy="6" r="1.6"/><circle cx="15" cy="6" r="1.6"/><circle cx="9" cy="12" r="1.6"/><circle cx="15" cy="12" r="1.6"/><circle cx="9" cy="18" r="1.6"/><circle cx="15" cy="18" r="1.6"/></svg>',
  });
  grip.addEventListener('keydown', (event) => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
    event.preventDefault();
    const row = grip.closest('.profile-row');
    const host = row.parentElement;
    const before = profileOrder(host);
    if (event.key === 'ArrowUp' && row.previousElementSibling) row.previousElementSibling.before(row);
    else if (event.key === 'ArrowDown' && row.nextElementSibling) row.nextElementSibling.after(row);
    grip.focus();
    saveProfileOrder(host, before);
  });
  grip.addEventListener('pointerdown', (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const row = grip.closest('.profile-row');
    const host = row.parentElement;
    const before = profileOrder(host);
    row.classList.add('dragging');
    // The row follows the pointer through the list: it goes before the first
    // other row whose middle is below the pointer, or to the end. Moving the
    // row takes it out of the document for a moment, which ends any pointer
    // capture, so the pointer is followed on the window instead.
    const move = (e) => {
      if (e.pointerId !== event.pointerId) return;
      const target = [...host.querySelectorAll('.profile-row')].find((other) => {
        if (other === row) return false;
        const box = other.getBoundingClientRect();
        return e.clientY < box.top + box.height / 2;
      });
      if (target) { if (row.nextElementSibling !== target) target.before(row); }
      else if (host.lastElementChild !== row) host.append(row);
    };
    const end = (e) => {
      if (e.pointerId !== event.pointerId) return;
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', end);
      window.removeEventListener('pointercancel', end);
      row.classList.remove('dragging');
      saveProfileOrder(host, before);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', end);
    window.addEventListener('pointercancel', end);
  });
  return grip;
}

function profileRow(profile) {
  const logins = el('div', { class: 'profile-logins', text: loginSummary(profile) });
  if (profile.default) {
    return el('div', { class: 'profile-row' },
      el('span', { class: 'profile-default', text: profile.name }),
      el('span', { class: 'hint', text: 'the CLIs\' own sign-ins' }),
      el('span'),
      logins);
  }
  const name = el('input', {
    class: 'input', type: 'text', maxlength: '40', value: profile.name,
    'aria-label': 'Name of ' + profile.name,
  });
  const rename = el('button', { class: 'btn sm', type: 'button', text: 'Rename' });
  const remove = el('button', { class: 'btn sm', type: 'button', text: 'Delete' });
  rename.addEventListener('click', async () => {
    const next = name.value.trim();
    if (!next || next === profile.name) return;
    busyButton(rename, true, 'Saving…');
    try {
      await api('/api/profiles/' + encodeURIComponent(profile.id), { method: 'PATCH', body: { name: next } });
      toast('Renamed to ' + next + '.');
      await loadProfiles();
    } catch (err) {
      toast(errorMessage(err), 'error');
    } finally {
      busyButton(rename, false);
    }
  });
  // Deleting takes the profile's sign-ins and conversations with it, so the
  // first tap only asks and the second one, within a few seconds, deletes.
  let armed = null;
  remove.addEventListener('click', async () => {
    if (!armed) {
      remove.textContent = 'Really delete?';
      armed = setTimeout(() => { armed = null; remove.textContent = 'Delete'; }, 4000);
      return;
    }
    clearTimeout(armed);
    armed = null;
    busyButton(remove, true, 'Deleting…');
    try {
      await api('/api/profiles/' + encodeURIComponent(profile.id), { method: 'DELETE' });
      toast(profile.name + ' and its sign-ins were deleted.');
      await loadProfiles();
    } catch (err) {
      toast(errorMessage(err), 'error');
      busyButton(remove, false);
      remove.textContent = 'Delete';
    }
  });
  return el('div', { class: 'profile-row' }, name, rename, remove, logins);
}

async function addProfile() {
  const input = $('profileName');
  const name = input.value.trim();
  if (!name) {
    toast('Give the profile a name first.');
    input.focus();
    return;
  }
  const button = $('profileAdd');
  busyButton(button, true, 'Adding…');
  try {
    await api('/api/profiles', { method: 'POST', body: { name } });
    input.value = '';
    toast(name + ' was added. Start a session in it to sign in.');
    await loadProfiles();
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    busyButton(button, false);
  }
}

/* ------------------------------------------------------- the tmux engine */

const TMUX_LABEL = { ok: 'tmux is ready', old: 'tmux is too old', missing: 'tmux is missing' };

function tmuxState(report) {
  if (report.ok) return 'ok';
  return report.installed ? 'old' : 'missing';
}

function renderTmux(report) {
  const host = $('tmuxStatus');
  let dot = host.querySelector(':scope > .state-dot');
  if (!dot) {
    dot = el('span', { class: 'state-dot' });
    host.prepend(dot);
  }
  const state = tmuxState(report);
  for (const name of ['ok', 'old', 'missing']) setClass(dot, name, state === name);
  while (dot.nextSibling) dot.nextSibling.remove();

  const parts = [el('span', { class: 'state-label', text: TMUX_LABEL[state] })];
  if (report.version) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', { class: 'muted', text: 'tmux ' + report.version }));
  }
  parts.push(el('span', { class: 'sep', text: '·' }));
  parts.push(el('span', { class: 'muted', text: 'needs ' + (report.min || '3.3') + ' or newer' }));
  // The path is state, not documentation, so it goes behind the "i" where the
  // rest of this page puts a build number.
  if (report.path) {
    parts.push(infoTip([report.path], { label: 'Where tmux is' }));
  }
  host.append(...parts);

  $('tmuxReason').textContent = report.ok ? '' : (report.reason || '');
}

async function refreshTmux() {
  try {
    renderTmux(await api('/api/tmux'));
  } catch (err) {
    if (!isOffline(err)) toast(errorMessage(err), 'error');
  }
}

/* ------------------------------------------------------------- harnesses */

async function loadHarnesses(force = false) {
  renderHarnesses(force ? 'refreshing' : 'loading');
  try {
    await harnesses.load(force);
    renderHarnesses('');
  } catch (err) {
    renderHarnesses(errorMessage(err));
  }
}

// cleanVersion is the client half of the same rule the server applies: what a
// program printed for --version is its text, not ours, and a CLI that puts a
// colour query in it must not put escape glyphs on this page.
function cleanVersion(text) {
  // eslint-disable-next-line no-control-regex
  const stripped = String(text || '').replace(/\u001b[\[\]][^\u0007\u001b]*(?:\u0007|\u001b\\)?/g, '')
    .replace(/[\u0000-\u001f\u007f]/g, '').trim();
  return stripped.length > 120 ? stripped.slice(0, 120).trim() + '…' : stripped;
}

function harnessLabel(id) {
  const found = harnesses.list().find((h) => h.id === id);
  if (found) return found.label;
  return { vscode: 'VS Code', shell: 'Shell', claude: 'Claude Code', codex: 'Codex', opencode: 'OpenCode' }[id] || id;
}

function optionPath(id, key) { return 'harnesses.' + id + '.' + key; }

function optionValue(id, key) {
  const value = getPath(settings, optionPath(id, key));
  if (value !== undefined && value !== null) return value;
  return getPath(defaults, optionPath(id, key));
}

function renderHarnesses(status) {
  const host = $('harnessCards');
  if (!host) return;
  host.innerHTML = '';
  const found = harnesses.list();
  if (!found.length) {
    host.append(el('section', { class: 'card' },
      el('h2', { text: 'Programs' }),
      el('p', { class: 'card-sub', text: status === 'loading'
        ? 'Asking this machine which programs are installed…'
        : (status || 'Socrates could not ask this machine which programs are installed.') })));
    return;
  }
  host.append(el('p', { class: 'card-sub programs-lead',
    text: 'Which programs a session can be, where each one lives, and what it starts on. '
      + 'How they are started — permissions, sandbox, remote control, theme — is not a setting: '
      + 'every session gets the same opinionated command line.' }));
  if (status && status !== 'loading' && status !== 'refreshing') {
    host.append(el('div', { class: 'hint', text: status }));
  } else if (harnesses.stale()) {
    host.append(el('div', { class: 'hint',
      text: 'Showing the programs from your last visit — this machine could not be asked just now.' }));
  }
  for (const id of ['shell', 'claude', 'codex', 'opencode', 'vscode']) {
    const harness = found.find((h) => h.id === id);
    if (harness) host.append(harnessCard(harness, status === 'refreshing'));
  }
  renderDefaultHarness();
}

function harnessCard(harness, refreshing) {
  const toggle = el('input', { type: 'checkbox', id: 'opt-' + harness.id + '-enabled' });
  toggle.checked = !!optionValue(harness.id, 'enabled');
  toggle.addEventListener('change', () => setPath(settings, optionPath(harness.id, 'enabled'), toggle.checked));

  // What this machine reported, in the order somebody reads it: is it here,
  // which build, and how much it can be run on. It is state, so it goes behind
  // the "i" rather than into the body of the card.
  const facts = [];
  if (!harness.installed) facts.push('not installed');
  else {
    if (harness.version) facts.push(cleanVersion(harness.version));
    if (harness.path) facts.push(harness.path);
  }
  const count = (harness.models || []).length;
  if (count) facts.push(count + ' model' + (count === 1 ? '' : 's') + (harness.static ? ' · curated' : ''));

  const binary = el('input', {
    class: 'input mono', type: 'text', spellcheck: 'false', id: 'opt-' + harness.id + '-binary',
    placeholder: harness.path || 'found on PATH',
    value: optionValue(harness.id, 'binary') || '',
  });
  binary.addEventListener('input', () => setPath(settings, optionPath(harness.id, 'binary'), binary.value.trim()));

  const refresh = el('button', {
    class: 'btn sm', type: 'button', text: refreshing ? 'Asking…' : 'Ask this machine again',
    disabled: refreshing,
    onclick: () => { loadHarnesses(true).catch(() => {}); },
  });

  const card = el('section', { class: 'card harness-card' + (harness.installed ? '' : ' missing'),
    id: 'harness-' + harness.id },
    el('div', { class: 'harness-head' },
      el('label', { class: 'switch' }, toggle, el('span', { class: 'track' }),
        el('span', { class: 'harness-name' }, agentMark(harness.id, 22), el('span', { text: harness.label }))),
      facts.length
        ? infoTip(facts, { label: harness.label + ' details' })
        : null,
    ),
    harness.error ? el('div', { class: 'agent-note bad', text: harness.error }) : null,
    harness.notes ? el('p', { class: 'card-sub', text: harness.notes }) : null,
    ['shell', 'vscode'].includes(harness.id) ? null : modelList(harness),
    field('Binary path', binary,
      'Leave it empty to look the program up on PATH, which is what a normal installation wants.'),
  );

  // Two fields at most, so they sit in the card rather than behind a
  // disclosure: a setting nobody can find is a setting nobody has.
  for (const option of OPTIONS[harness.id] || []) card.append(optionField(harness, option));
  card.append(el('div', { class: 'row' }, refresh));
  card.append(el('div', { class: 'hint start-only', text: START_ONLY_NOTE }));
  return card;
}

// optionField turns one catalogue entry into the control it names, with what
// the value means in the hint - that is technical, and technical detail is
// what the hint is for.
function optionField(harness, option) {
  const id = 'opt-' + harness.id + '-' + option.key;
  const path = optionPath(harness.id, option.key);
  const value = optionValue(harness.id, option.key);
  const write = (next) => setPath(settings, path, next);

  let control = null;
  let wrapper = null;
  switch (option.type) {
    case 'select': {
      control = el('select', { class: 'select', id });
      for (const v of option.values) control.append(el('option', { value: v, text: v || '—' }));
      control.value = value ?? '';
      control.addEventListener('change', () => write(control.value));
      break;
    }
    case 'model': {
      const picker = combobox({
        value: value || '',
        placeholder: 'pick from the list, or type a model id',
        items: () => (harness.models || []).map((m) => ({
          value: m.id, label: m.label || m.id, hint: m.hint || '', group: m.group || '',
        })),
        onChange: (next) => write(next.trim()),
      });
      const input = picker.node.querySelector('input');
      input.id = id;
      wrapper = picker.node;
      control = input;
      break;
    }
    default: {
      control = el('input', {
        class: 'input mono', type: 'text', id, spellcheck: 'false',
        placeholder: option.placeholder || '', value: value ?? '',
      });
      control.addEventListener('input', () => write(control.value));
    }
  }

  const wrap = el('div', { class: 'field option', 'data-key': option.key, 'data-startonly': '1' });
  wrap.append(el('label', { for: id, text: option.label }), wrapper || control,
    el('div', { class: 'hint', text: option.hint || '' }));
  return wrap;
}

// modelList is the person's own short list for one harness: the models the
// new-session sheet offers, in this order, each with the effort it starts on.
// An empty list means the sheet offers everything the program reported.
function modelList(harness) {
  const path = optionPath(harness.id, 'models');
  const picks = () => (optionValue(harness.id, 'models') || []).map((p) => ({ id: p.id, effort: p.effort || '' }));
  const known = (id) => (harness.models || []).find((m) => m.id === id) || null;

  const rows = el('div', { class: 'model-list' });
  function render() {
    rows.innerHTML = '';
    const list = picks();
    if (!list.length) {
      rows.append(el('div', { class: 'hint', text: 'No short list — the new-session sheet offers every model '
        + harness.label + ' reports.' }));
      return;
    }
    list.forEach((pick, index) => {
      const model = known(pick.id);
      const efforts = harnesses.effortsFor(harness.id, pick.id);
      const effort = el('select', { class: 'select sm', 'aria-label': 'Default effort for ' + pick.id });
      for (const value of ['', ...efforts]) {
        effort.append(el('option', { value, text: harnesses.effortLabel(value) }));
      }
      effort.value = efforts.includes(pick.effort) ? pick.effort : '';
      effort.hidden = !efforts.length;
      effort.addEventListener('change', () => {
        const next = picks();
        next[index].effort = effort.value;
        setPath(settings, path, next);
      });
      const remove = el('button', {
        class: 'btn sm', type: 'button', text: 'Remove', 'aria-label': 'Remove ' + pick.id,
        onclick: () => { setPath(settings, path, picks().filter((_, i) => i !== index)); render(); },
      });
      rows.append(el('div', { class: 'model-row' },
        el('div', { class: 'model-name' },
          el('div', { text: model ? (model.label || pick.id) : pick.id }),
          el('div', { class: 'hint mono', text: model ? (model.hint || pick.id)
            : pick.id + ' · typed in, not on the list ' + harness.label + ' reports' }),
        ),
        effort, remove,
      ));
    });
  }

  const picker = combobox({
    value: '',
    placeholder: harness.static ? 'sonnet' : 'pick from the list, or type a model id',
    items: () => (harness.models || []).map((m) => ({
      value: m.id, label: m.label || m.id, hint: m.hint || '', group: m.group || '',
    })),
    onChange: () => {},
  });
  const input = picker.node.querySelector('input');
  input.id = 'models-' + harness.id;
  const add = () => {
    const id = input.value.trim();
    if (!id) return;
    const list = picks();
    if (list.some((p) => p.id === id)) {
      toast(id + ' is on the list already.');
      return;
    }
    const model = known(id);
    list.push({ id, effort: model && model.default_effort ? model.default_effort : '' });
    setPath(settings, path, list);
    picker.setValue('', false);
    render();
  };
  input.addEventListener('keydown', (event) => {
    // Enter with the list closed adds what is typed; with the list open the
    // combobox takes the highlighted entry first, and a second Enter adds it.
    if (event.key === 'Enter' && picker.node.querySelector('.combo-list').hidden) {
      event.preventDefault();
      add();
    }
  });
  render();
  return el('div', { class: 'field model-picks' },
    el('label', { text: 'Models offered in the sheet' }),
    rows,
    el('div', { class: 'model-add' }, picker.node,
      el('button', { class: 'btn sm', type: 'button', text: 'Add', id: 'add-model-' + harness.id, onclick: add })),
    el('div', { class: 'hint', text: 'Pick from what ' + harness.label + ' reports or type an id, and set the '
      + 'effort a new session starts on. Leave the list empty to offer all of them.' }),
  );
}

/* ---------------------------------------------------------------- saving */

function bind() {
  $('saveTop').addEventListener('click', save);
  $('saveBottom').addEventListener('click', save);
  $('runChecks').addEventListener('click', runChecks);
  $('changePw').addEventListener('click', changePassword);
  $('presetAdd').addEventListener('click', () => $('presetDirs').append(presetRow()));
  $('profileAdd').addEventListener('click', addProfile);
  $('profileName').addEventListener('keydown', (event) => {
    if (event.key === 'Enter') { event.preventDefault(); addProfile(); }
  });
  $('windowSize').addEventListener('change', renderWindowSizeHint);
  $('tunnelStart').addEventListener('click', startTunnel);
  $('tunnelStop').addEventListener('click', stopTunnel);
  $('tunnelMode').addEventListener('change', renderTunnelMode);
  $('tunnelInstall').addEventListener('click', installCloudflared);
  $('tunnelLogToggle').addEventListener('click', () => {
    const log = $('tunnelLog');
    log.hidden = !log.hidden;
    $('tunnelLogToggle').textContent = log.hidden ? 'Show log' : 'Hide log';
    if (!log.hidden) refreshTunnel();
  });
  $('checkVoiceKey').addEventListener('click', checkVoiceKey);
  $('testVoice').addEventListener('click', async () => {
    // The server renders with what it has stored, so anything typed into this
    // card and not saved yet would be read by the old settings. Saying so is
    // better than sounding wrong, or than testing a key nobody saved.
    const saved = getPath(settings, 'voice.language') || 'en';
    const unsaved = $('voiceLanguage').value !== saved ||
      $('googleKey').value !== (getPath(settings, 'voice.google_api_key') || '') ||
      $('googleVoiceEn').value !== (getPath(settings, 'voice.google_voice_en') || '') ||
      $('googleVoiceDe').value !== (getPath(settings, 'voice.google_voice_de') || '') ||
      Number($('ttsRate').value) !== Number(getPath(settings, 'voice.tts_rate'));
    if (unsaved) {
      toast('Save first — the test uses the voice settings the server has stored.');
      return;
    }
    const sample = saved === 'de'
      ? 'So klingt Socrates, wenn dir eine Antwort im Freisprechmodus vorgelesen wird.'
      : 'This is how Socrates will read answers to you in audio mode.';
    const button = $('testVoice');
    busyButton(button, true, 'Reading…');
    try {
      await speak(sample);
      showVoiceStatus(true, 'The sample was rendered by Google and played here.');
    } catch (err) {
      // Every press answers, whether or not this reason has been seen before:
      // a button that stays silent the second time reads as a button that did
      // nothing at all.
      showVoiceStatus(false, errorMessage(err));
      toast(errorMessage(err), speechKind(err));
    } finally {
      busyButton(button, false, 'Reading…');
    }
  });
  document.addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 's') {
      event.preventDefault();
      save();
    }
  });
}

async function save() {
  const next = collect();
  const buttons = [$('saveTop'), $('saveBottom')];
  for (const button of buttons) busyButton(button, true, 'Saving…');
  try {
    // Settings are written whole, so the same body sent twice leaves exactly
    // the same result - which makes this safe to repeat over a bad link.
    const data = await api('/api/settings', { method: 'PUT', attempts: 4, body: { settings: next } });
    settings = data.settings;
    fillForm();
    syncModelPickers();
    renderPresets();
    // The cards are drawn from the settings they just wrote, so they have to
    // follow whatever the server normalised them into.
    renderHarnesses('');
    refreshTunnel();
    hint('Saved');
    for (const warning of data.warnings || []) toast(warning);
    toast('Settings saved');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    for (const button of buttons) busyButton(button, false, 'Saving…');
  }
}

function hint(text) {
  for (const id of ['savedHint', 'savedHint2']) {
    const node = $(id);
    if (!node) continue;
    node.textContent = text;
    setTimeout(() => { node.textContent = ''; }, 2500);
  }
}

function showNotice(text, kind) {
  const notice = $('notice');
  notice.className = 'notice ' + kind;
  notice.textContent = text;
}

function field(label, control, hintText) {
  return el('div', { class: 'field' },
    el('label', { text: label }),
    control,
    hintText ? el('div', { class: 'hint', text: hintText }) : null);
}

// splitArgs understands simple quoting so paths with spaces survive.
function splitArgs(value) {
  const out = [];
  const pattern = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let match;
  while ((match = pattern.exec(value)) !== null) {
    out.push(match[1] ?? match[2] ?? match[3]);
  }
  return out;
}


/* ------------------------------------------------------------- the voice */

// The voice card has no state to poll: there is nothing installing and nothing
// running here, only a key that either works at Google or does not. The two
// buttons are what ask, and this line is where the answer lands - so it says
// nothing at all until one of them has been pressed.
function showVoiceStatus(ok, detail) {
  const host = $('voiceStatus');
  host.hidden = false;
  let dot = host.querySelector(':scope > .state-dot');
  if (!dot) {
    dot = el('span', { class: 'state-dot' });
    host.prepend(dot);
  }
  setClass(dot, 'ready', !!ok);
  setClass(dot, 'failed', !ok);
  while (dot.nextSibling) dot.nextSibling.remove();
  host.append(
    el('span', { class: 'state-label', text: ok ? 'Voice ready' : 'Voice not working' }),
    el('span', { class: 'sep', text: '·' }),
    el('span', { class: 'detail', text: detail }),
  );
}

// checkVoiceKey asks Google for the voice list of the stored language, which
// is the cheapest thing that proves a key: it needs the same API enabled and
// the same key restriction as speaking does, and it renders nothing.
async function checkVoiceKey() {
  const button = $('checkVoiceKey');
  busyButton(button, true, 'Checking…');
  try {
    const data = await api('/api/voice/check', { method: 'POST' });
    showVoiceStatus(!!data.ok, data.detail || '');
  } catch (err) {
    showVoiceStatus(false, errorMessage(err));
  } finally {
    busyButton(button, false, 'Checking…');
  }
}

/* ------------------------------------------------------------ the tunnel */

function tunnelForm() {
  return {
    enabled: true,
    mode: $('tunnelMode').value,
    token: $('tunnelToken').value.trim(),
    hostname: $('tunnelHostname').value.trim(),
    command: $('tunnelCommand').value.trim() || 'cloudflared',
    extra_args: splitArgs($('tunnelArgs').value),
  };
}

function renderTunnelMode() {
  const named = $('tunnelMode').value === 'token';
  $('tunnelToken').closest('.field').style.display = named ? '' : 'none';
  $('tunnelHostname').closest('.field').style.display = named ? '' : 'none';
  const local = localURL || 'the local address shown above';
  $('tunnelModeHint').textContent = named
    ? 'Runs your own named tunnel. In the Cloudflare dashboard, point its public hostname at ' + local + '.'
    : 'Free and instant, but the address changes every restart and anyone with the link can reach the login page.';
}

const STATE_LABEL = {
  running: 'Tunnel is up',
  starting: 'Connecting…',
  installing: 'Downloading cloudflared…',
  failed: 'Tunnel failed',
  stopped: 'Tunnel is off',
};

function renderTunnelStatus(status) {
  const host = $('tunnelStatus');
  let dot = host.querySelector(':scope > .state-dot');
  if (!dot) {
    dot = el('span', { class: 'state-dot' });
    host.prepend(dot);
  }
  for (const name of Object.keys(STATE_LABEL)) setClass(dot, name, status.state === name);
  while (dot.nextSibling) dot.nextSibling.remove();

  const parts = [
    el('span', { class: 'state-label', text: STATE_LABEL[status.state] || status.state }),
  ];
  if (status.url) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('a', { href: status.url, target: '_blank', rel: 'noopener', text: status.url }));
  }
  parts.push(el('span', { class: 'sep', text: '·' }));
  parts.push(el('span', { class: 'muted', text: 'local: ' + (status.local_url || 'http://127.0.0.1:8080') }));
  if (!status.installed) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', {
      class: 'muted',
      text: status.can_install ? 'cloudflared is downloaded on start' : 'cloudflared is not installed',
    }));
  } else if (status.version) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', { class: 'muted', text: status.version }));
  }
  if (status.error) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', { style: 'color:var(--red)', text: status.error }));
  }
  if (status.restarts) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', { class: 'muted', text: status.restarts + ' restarts' }));
  }
  host.append(...parts);

  const log = $('tunnelLog');
  if (!log.hidden) {
    log.textContent = (status.logs || []).join('\n') || 'no output yet';
    log.scrollTop = log.scrollHeight;
  }
  $('tunnelStart').textContent = status.state === 'stopped' ? 'Start tunnel' : 'Restart tunnel';
  $('tunnelStop').disabled = status.state === 'stopped';

  const installButton = $('tunnelInstall');
  installButton.hidden = status.installed || !status.can_install;
  if (status.installed) {
    $('tunnelInstallHint').textContent = status.managed
      ? 'Managed by Socrates: ' + status.path
      : 'Found in your PATH: ' + (status.path || 'cloudflared');
  } else if (status.can_install) {
    $('tunnelInstallHint').textContent = 'Not installed yet. Socrates downloads it automatically when you start the tunnel.';
  } else {
    const platform = navigator.platform.toLowerCase();
    const key = platform.includes('mac') ? 'macos' : platform.includes('win') ? 'windows' : 'linux';
    $('tunnelInstallHint').textContent = 'No build for this platform. Install it yourself: ' + (installHints[key] || installHints.docs);
  }

  const live = status.state === 'running' || status.state === 'starting' ||
    status.state === 'installing' || status.state === 'failed';
  clearTimeout(tunnelTimer);
  if (live || !log.hidden) tunnelTimer = setTimeout(refreshTunnel, 3000);
}

onWake(() => {
  if (document.visibilityState === 'hidden' || !settings) return;
  refreshTunnel();
  refreshTmux();
});

async function refreshTunnel() {
  try {
    const data = await api('/api/tunnel');
    installHints = data.install || installHints;
    if (data.status && data.status.local_url) localURL = data.status.local_url;
    if (data.tunnel) {
      settings.tunnel = data.tunnel;
      $('tunnelMode').value = data.tunnel.mode || 'quick';
      $('tunnelCommand').value = data.tunnel.command || 'cloudflared';
      if (document.activeElement !== $('tunnelHostname')) $('tunnelHostname').value = data.tunnel.hostname || '';
      if (document.activeElement !== $('tunnelToken')) $('tunnelToken').value = data.tunnel.token || '';
      if (document.activeElement !== $('tunnelArgs')) $('tunnelArgs').value = (data.tunnel.extra_args || []).join(' ');
    }
    renderTunnelMode();
    renderTunnelStatus(data.status || {});
  } catch (err) {
    clearTimeout(tunnelTimer);
    // A poll that could not reach the server is not worth a message - the
    // connection bar already says so - but it must not stop polling either,
    // or the page would sit on a status that is no longer true.
    if (isOffline(err)) {
      tunnelTimer = setTimeout(refreshTunnel, 5000);
      return;
    }
    toast(errorMessage(err), 'error');
  }
}

async function startTunnel() {
  const button = $('tunnelStart');
  busyButton(button, true, 'Starting…');
  try {
    const data = await api('/api/tunnel/start', { method: 'POST', body: { tunnel: tunnelForm() } });
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel starting');
    setTimeout(refreshTunnel, 1200);
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    busyButton(button, false, 'Starting…');
  }
}

async function installCloudflared() {
  const button = $('tunnelInstall');
  button.disabled = true;
  button.textContent = 'Downloading…';
  $('tunnelLog').hidden = false;
  $('tunnelLogToggle').textContent = 'Hide log';
  refreshTunnel();
  try {
    const data = await api('/api/tunnel/install', { method: 'POST', body: {} });
    renderTunnelStatus(data.status || {});
    toast('cloudflared installed');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    button.disabled = false;
    button.textContent = 'Download cloudflared';
    refreshTunnel();
  }
}

async function stopTunnel() {
  const button = $('tunnelStop');
  busyButton(button, true, 'Stopping…');
  try {
    const data = await api('/api/tunnel/stop', { method: 'POST', body: {} });
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel stopped');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    busyButton(button, false, 'Stopping…');
    // renderTunnelStatus decides whether this button belongs enabled at all.
    refreshTunnel();
  }
}

/* ------------------------------------------------------------ side tasks */

// The marks beside a diagnostics row: a check that names a program is drawn
// with that program's mark, so the four cards above and this list read as one
// thing.
const CHECK_MARKS = {
  'Claude Code': 'claude', 'Claude Code state': 'claude',
  Codex: 'codex', 'Codex state': 'codex',
  OpenCode: 'opencode', 'OpenCode state': 'opencode',
  Shell: 'shell',
};

async function runChecks() {
  const button = $('runChecks');
  const host = $('checks');
  button.disabled = true;
  button.textContent = 'Checking…';
  host.innerHTML = '';
  try {
    const data = await api('/api/diagnostics', { method: 'POST', body: {} });
    for (const check of data.checks || []) {
      const mark = CHECK_MARKS[check.name] || '';
      // §E.10 rule 3: a version, a path, a socket or an error message is
      // technical state, so it is behind the row's "i" and never in the line
      // itself. What is visible is the verdict, in words.
      host.append(el('div', { class: 'check' },
        el('span', { class: 'st ' + (check.ok ? 'ok' : 'bad') }),
        el('span', { class: 'nm' }, mark ? agentMark(mark, 14) : null, el('span', { text: check.name })),
        el('span', { class: 'dt', text: check.summary || (check.ok ? 'ok' : 'not ready') }),
        check.detail ? infoTip(String(check.detail).split(' · '), { label: check.name + ' details' }) : null,
      ));
    }
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    button.disabled = false;
    button.textContent = 'Run checks';
  }
}

async function changePassword() {
  const current = $('pwCurrent').value;
  const next = $('pwNext').value;
  if (!current || !next) {
    toast('Fill in both password fields.', 'error');
    return;
  }
  const button = $('changePw');
  busyButton(button, true, 'Changing…');
  try {
    await api('/api/settings/password', { method: 'POST', body: { current, next } });
    $('pwCurrent').value = '';
    $('pwNext').value = '';
    toast('Password changed');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    busyButton(button, false, 'Changing…');
  }
}
