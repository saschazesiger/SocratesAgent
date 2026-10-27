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
//
// The page wears the Dashboard-Style app frame: the drawer is its table of
// contents, each card is one section, and every state the machine reports is
// a status chip with its word, the technical detail behind the "i".

import {
  api, el, toast, isOffline, errorMessage, onWake, infoTip, confirmDialog, alertDialog, promptDialog, openDialog,
} from './api.js';
import { DS, M, html, raw, icon, iconEl, morph, flipMorph, ms, faces } from './ui.js';
import { agentMark } from './logos.js';
import { speak, speechKind, setVoiceKey } from './voice.js';
import { combobox } from './combobox.js';
import { dictated } from './keyboard.js';
import * as harnesses from './harnesses.js';
import * as accounts from './accounts.js';

const $ = (id) => document.getElementById(id);

// facedButton is a button built in script with the busy face (and the done
// face, when `doneLabel` is given) of the ones in admin.html.
function facedButton(label, busyLabel, doneLabel, attrs = {}) {
  const button = el('button', { class: 'ds-btn ds-btn--sm', type: 'button', ...attrs });
  morph(button, faces(label, busyLabel, doneLabel));
  return button;
}

let settings = null;
let defaults = null;

// The OpenRouter models are ids typed by hand: there is no model catalogue
// endpoint any more - it went with the chat API. They are plain text fields
// with a microphone, built here (not in FIELDS) because they are built
// before the settings arrive; a combobox with nothing to list only cost the
// id 48px for a "Show all" that showed nothing, and at 360 the Status and
// Title models were cut to the same "google/gemini-2.5-fla…".
const MODEL_PICKERS = [
  ['orTranscribe', 'openrouter.transcribe_model'],
  ['orStatus', 'openrouter.status_model'],
  ['orTitle', 'openrouter.title_model'],
  ['orHome', 'openrouter.home_model'],
];

// FIELDS is every control that is one element and one path. Everything with a
// shape of its own - the presets, the harness cards, the model short lists -
// is built and read below.
const FIELDS = [
  ['orKey', 'openrouter.api_key'],
  ['orOutcomes', 'openrouter.outcomes', 'bool'],
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

const HARNESS_ORDER = ['shell', 'claude', 'codex', 'opencode', 'vscode'];

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

/* ---------------------------------------------------------- status chips */

// A state is a DS status chip: always an icon and a word. The kind is the
// chip's look, data-state the machine's own word for it, so a test or a
// script reads what was reported rather than a colour.
const CHIP = {
  ok: ['ds-status ds-status--ok', 'check'],
  warning: ['ds-status ds-status--warning', 'alert'],
  error: ['ds-status ds-status--error', 'alertCircle'],
  running: ['ds-status ds-status--running', null],
  waiting: ['ds-status', 'clock'],
  off: ['ds-status', 'ban'],
  plain: ['ds-status', 'check'],
};

function chip(kind, label, state = kind) {
  const [cls, glyph] = CHIP[kind];
  const mark = glyph ? raw(icon(glyph, 16)) : html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>`;
  return html`<span class="${cls}" data-state="${state}">${mark}<span>${label}</span></span>`;
}

function chipEl(kind, label, state = kind) {
  const [cls, glyph] = CHIP[kind];
  return el('span', { class: cls, 'data-state': state },
    glyph ? iconEl(glyph, 16) : el('span', { class: 'ds-spinner ds-status__spinner', 'aria-hidden': 'true' }),
    el('span', { text: label }));
}

// parts is a run of short facts beside a chip ("last run 2 h ago · next run
// in 22 h"); DS wraps it between parts and never leaves a "·" at a line end.
function parts(list) {
  const shown = list.filter((part) => part !== null && part !== undefined && part !== '');
  if (!shown.length) return '';
  return html`<span class="ds-parts"><span class="ds-parts__list">${shown.map((part) => html`<span>${part}</span>`)}</span></span>`;
}

// setTip keeps the "i" of a status line: a new button only when its lines
// changed, so a poll that says the same thing leaves an open tooltip alone.
function setTip(slot, lines, label) {
  if (!slot) return;
  const shown = lines.filter((line) => line !== null && line !== undefined && line !== '').map(String);
  const now = slot.firstElementChild;
  if (now && now.getAttribute('data-tip') === shown.join('\n') && now.getAttribute('aria-label') === label) return;
  slot.replaceChildren(...(shown.length ? [infoTip(shown, { label })] : []));
}

// renderState draws one status line: the chip, its facts, and its "i". The
// line is patched in place, so a chip that changes glides to its new colour.
function renderState(host, { kind, label, state, facts = [], tip = [], tipLabel = 'Details' }) {
  morph(host.querySelector('.so-state__line'), html`${chip(kind, label, state)}${parts(facts)}`);
  setTip(host.querySelector('.so-state__tip'), tip, tipLabel);
}

// showAlert fills or empties a DS alert whose text is in `textId`.
function showAlert(boxId, textId, text) {
  $(textId).textContent = text || '';
  $(boxId).hidden = !text;
}

/* ------------------------------------------------------------------- boot */

// The admin page is loaded once. If the connection was down at that moment it
// would otherwise stay blank for good, so it retries and comes back on its own.
wireFrame();
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
  mcpList = (getPath(settings, 'mcp.servers') || []).map((server) => ({ ...server }));
  renderMCP();
  bind();
  loadAccounts();
  // The groups are headed with the agents' names, which the catalogue has.
  loadHarnesses().then(() => { renderAccounts(); settleHash(); });
  refreshTmux();
  refreshUpdates();
  watchUpdatesCard();
  refreshBackup(true);
  if (data.local_url) localURL = data.local_url;
  refreshTunnel();
  if (new URLSearchParams(location.search).get('welcome')) {
    showNotice('Welcome. Sign the agents in under Accounts below, add your OpenRouter key if you want to '
      + 'dictate, then head back to the sessions. The terminal engine and the setup check are at the bottom.', 'ok');
  }
  const tunnelError = sessionStorage.getItem('socrates.tunnel_error');
  if (tunnelError) {
    sessionStorage.removeItem('socrates.tunnel_error');
    showNotice('The tunnel could not be started: ' + tunnelError, 'error');
  }
  settleHash();
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
  // The microphones on this page ask whether there is a key to transcribe
  // with before they record (voice.js); this page knows.
  setVoiceKey(!!String(getPath(settings, 'openrouter.api_key') || '').trim(), { told: true });
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
  next.mcp = { servers: mcpList.map((server) => ({ ...server })) };
  return next;
}

function renderWindowSizeHint() {
  const node = $('windowSizeHint');
  if (node) node.textContent = WINDOW_SIZE_HINTS[$('windowSize').value] || '';
}

/* -------------------------------------------------------------- the frame */

// The drawer is the page's table of contents. An item scrolls the view to its
// card and becomes the current one at once; while that scroll runs the spy is
// muted, or the pill would walk through every card on the way. On a phone the
// drawer is modal and closes once a card is picked.
let spyMuted = false;
let spyIdle = 0;
let hashSettled = false;

function wireFrame() {
  const app = $('app');
  const drawer = $('adminNav');
  const menu = $('menuBtn');
  const view = $('main');
  // The top bar's hairline while the view is scrolled (DS App frame).
  DS.dom.watchScrolled(view);
  fitDrawer();
  DS.indicator.createIndicator($('adminNavList'), { active: '[aria-current="page"]', className: 'ds-drawer__indicator' });
  menu.addEventListener('click', () => {
    if (app.hasAttribute('data-drawer-open')) DS.overlay.close(drawer);
    else DS.overlay.open(drawer, { opener: menu });
  });
  for (const link of sectionLinks()) {
    link.addEventListener('click', (event) => {
      const target = $(link.dataset.section);
      if (!target) return;
      event.preventDefault();
      hashSettled = true;
      if (app.hasAttribute('data-drawer-open')) DS.overlay.close(drawer);
      setCurrent(link);
      muteSpy(view);
      target.scrollIntoView({ behavior: reducedMotion() ? 'auto' : 'smooth', block: 'start' });
      history.replaceState(null, '', '#' + target.id);
    });
  }
  let queued = false;
  view.addEventListener('scroll', () => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      if (!spyMuted) spy(view);
    });
  }, { passive: true });
  // A person who scrolls has decided where they are; a late render does not
  // take them back to the address's card any more.
  view.addEventListener('wheel', () => { hashSettled = true; }, { passive: true, once: true });
  view.addEventListener('touchstart', () => { hashSettled = true; }, { passive: true, once: true });
  // The page's own links to a card (the voice card names Status & naming).
  view.addEventListener('click', (event) => {
    const link = event.target.closest('a[href^="#"]');
    if (!link) return;
    const item = sectionLinks().find((one) => one.getAttribute('href') === link.getAttribute('href'));
    if (item) { event.preventDefault(); item.click(); }
  });
  // The first card is current until something else is.
  const first = sectionLinks()[0];
  if (first) setCurrent(first);
}

function sectionLinks() {
  return [...document.querySelectorAll('#adminNavList a[data-section]')];
}

// setCurrent marks the card in view in the drawer, and keeps its item in
// sight when the list is taller than the window.
function setCurrent(link) {
  for (const one of sectionLinks()) {
    if (one === link) one.setAttribute('aria-current', 'page');
    else one.removeAttribute('aria-current');
  }
  const list = $('adminNavList');
  if (list.scrollHeight > list.clientHeight) {
    const box = link.getBoundingClientRect();
    const room = list.getBoundingClientRect();
    if (box.top < room.top || box.bottom > room.bottom) link.scrollIntoView({ block: 'nearest' });
  }
}

// fitDrawer says when the drawer's list is taller than the window, so its
// scrollbar stays visible (.ds-drawer__list--overflow).
function fitDrawer() {
  const list = $('adminNavList');
  const fit = () => list.classList.toggle('ds-drawer__list--overflow', list.scrollHeight > list.clientHeight + 1);
  fit();
  if (window.ResizeObserver) new ResizeObserver(fit).observe(list);
  else window.addEventListener('resize', fit);
}

function reducedMotion() {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

// muteSpy holds the scroll spy while a programmatic scroll runs: until
// `scrollend` where the browser has it, otherwise until no scroll came for a
// moment. A link to where the view already is never scrolls, so a ceiling
// lets go of it as well.
function muteSpy(view) {
  spyMuted = true;
  clearTimeout(spyIdle);
  const release = () => {
    spyMuted = false;
    clearTimeout(spyIdle);
    view.removeEventListener('scroll', restart);
    view.removeEventListener('scrollend', release);
  };
  const restart = () => {
    clearTimeout(spyIdle);
    spyIdle = setTimeout(release, ms('--ds-dur-fast') * 2);
  };
  view.addEventListener('scroll', restart, { passive: true });
  if ('onscrollend' in view) view.addEventListener('scrollend', release, { once: true });
  spyIdle = setTimeout(release, ms('--ds-dur-long') * 3);
}

// spy makes the card at the top of the view the current one: the last card
// whose top has passed the upper part of the view, or the last card of all
// once the view is at its end.
function spy(view) {
  const links = sectionLinks();
  if (!links.length) return;
  const box = view.getBoundingClientRect();
  let current = links[0];
  if (view.scrollTop + view.clientHeight >= view.scrollHeight - 2) {
    current = links[links.length - 1];
  } else {
    const line = box.top + Math.min(box.height / 3, 160);
    for (const link of links) {
      const target = $(link.dataset.section);
      if (target && target.getBoundingClientRect().top <= line) current = link;
    }
  }
  if (current.getAttribute('aria-current') !== 'page') setCurrent(current);
}

// settleHash brings the card the address names into view (the session page
// links to /admin#voiceCard). It runs again once the program cards are in,
// because they push everything below them down - unless the person has
// moved meanwhile.
function settleHash() {
  if (hashSettled) return;
  const id = location.hash.slice(1);
  const target = id && $(id);
  if (!target) return;
  target.scrollIntoView({ block: 'start' });
  const link = sectionLinks().find((one) => one.dataset.section === id);
  if (link) setCurrent(link);
}

/* ---------------------------------------------------------- model pickers */

const pickers = {};

// buildModelPickers fills the empty slots the page leaves for them. They are
// built before anything arrives, so the dashboard is usable straight away.
function buildModelPickers() {
  for (const [id, path] of MODEL_PICKERS) {
    const host = $(id);
    if (!host) continue;
    const input = el('input', {
      class: 'ds-input', type: 'text', id: id + 'Input', value: getPath(settings, path) || '',
      placeholder: path === 'openrouter.transcribe_model' ? 'google/gemini-2.5-flash' : 'anthropic/claude-haiku-5.5', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off',
      enterkeyhint: 'done',
    });
    // Read on every change, as the combobox was: dictation fills the field
    // and says so with an input event.
    input.addEventListener('input', () => setPath(settings, path, input.value.trim()));
    pickers[id] = input;
    host.replaceChildren();
    host.append(dictated(input, input));
  }
}

// syncModelPickers pushes values back into the fields after a save.
function syncModelPickers() {
  for (const [id, path] of MODEL_PICKERS) {
    if (pickers[id]) pickers[id].value = getPath(settings, path) || '';
  }
}

/* ------------------------------------------------------------- workspace */

// The default program is one choice of five, each with its mark: radio
// chips, which keep the choice in the form and the tab order.
function renderDefaultHarness() {
  const host = $('defaultHarness');
  if (!host) return;
  const current = getPath(settings, 'workspace.default_harness');
  host.replaceChildren(...HARNESS_ORDER.map((id) => {
    const input = el('input', {
      class: 'ds-chip__input', type: 'radio', name: 'defaultHarness', value: id, checked: current === id,
    });
    input.addEventListener('change', () => {
      if (input.checked) setPath(settings, 'workspace.default_harness', id);
    });
    return el('label', { class: 'ds-chip', 'data-value': id }, input,
      el('span', { class: 'ds-chip__body' }, iconEl('check', 18, 'ds-chip__check'),
        agentMark(id, 18), el('span', { text: harnessLabel(id) })));
  }));
}

// The preset editor is a repeating row, read back as a list when the page is
// saved rather than written into the document on every keystroke: a half-typed
// path is not a preset yet.
function presetRow(preset = { label: '', path: '' }) {
  const label = el('input', {
    class: 'ds-input', type: 'text', placeholder: 'Name', value: preset.label || '', 'aria-label': 'Name',
  });
  const path = el('input', {
    class: 'ds-input', type: 'text', spellcheck: 'false', placeholder: '/srv/projects',
    value: preset.path || '', 'aria-label': 'Path',
  });
  const row = el('div', { class: 'so-preset' }, label, path,
    el('button', {
      class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button',
      'aria-label': 'Remove this directory', 'data-tip': 'Remove this directory',
      onclick: () => M.flip(row.parentElement, () => row.remove()),
    }, iconEl('trash', 18)));
  return row;
}

function renderPresets() {
  const host = $('presetDirs');
  if (!host) return;
  host.replaceChildren(...(getPath(settings, 'workspace.presets') || []).map((preset) => presetRow(preset)));
}

function readPresets() {
  const host = $('presetDirs');
  if (!host) return [];
  return [...host.querySelectorAll('.so-preset')].map((row) => {
    const [label, path] = row.querySelectorAll('input');
    return { label: label.value.trim(), path: path.value.trim() };
  }).filter((preset) => preset.path !== '');
}

/* ----------------------------------------------------------- MCP servers */

// The MCP servers every chat may have are part of the settings document: the
// card edits a copy (mcpList) that Save writes with the rest, as the presets
// are. A server is more fields than a row has room for, so it is edited in a
// dialog; the row says what it is and whether it is on in every chat.
let mcpList = [];

const MCP_TYPES = [['stdio', 'Local command'], ['http', 'HTTP'], ['sse', 'SSE']];
const MCP_NAME = /^[A-Za-z0-9_-]+$/;

function mcpSummary(server) {
  if (server.type === 'stdio') return [server.command, ...(server.args || [])].join(' ');
  return (server.type === 'sse' ? 'SSE · ' : 'HTTP · ') + server.url;
}

// renderMCP draws the rows from mcpList, keyed by name, so a row that stays
// is patched in place and the others come and go in their places. What a row
// does is bound once, on the list (bindMCP).
function renderMCP() {
  const host = $('mcpServers');
  if (!host) return;
  const rows = mcpList.map((server) => html`<div class="so-mcp" data-ds-key="${server.name}" data-name="${server.name}">
    <div class="so-mcp__main"><p class="so-mcp__name">${server.name}</p><p class="ds-meta so-mcp__what">${mcpSummary(server)}</p></div>
    <label class="ds-switch"><input type="checkbox" role="switch" data-action="default"${server.default ? html` checked` : ''} aria-label="${server.name + ' in every chat'}"><span class="ds-switch__track" aria-hidden="true"></span><span class="ds-switch__label">In every chat</span><span class="ds-switch__state" aria-hidden="true"><span class="ds-switch__on">On</span><span class="ds-switch__off">Off</span></span></label>
    <div class="so-mcp__actions">
      <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="edit" aria-label="${'Edit ' + server.name}" data-tip="Edit">${raw(icon('pencil', 18))}</button>
      <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="delete" aria-label="${'Remove ' + server.name}" data-tip="Remove">${raw(icon('trash', 18))}</button>
    </div></div>`);
  flipMorph(host, mcpList.length ? html`${rows}` : html`<p class="ds-meta" data-ds-key="none">No MCP server yet.</p>`);
  // morph keeps an input's checked as the page left it; the list says.
  for (const input of host.querySelectorAll('input[data-action="default"]')) {
    const server = mcpOf(input);
    if (server) input.checked = !!server.default;
  }
}

function mcpOf(node) {
  const row = node.closest('.so-mcp');
  return row ? mcpList.find((one) => one.name === row.dataset.name) : null;
}

function bindMCP() {
  const host = $('mcpServers');
  host.addEventListener('change', (event) => {
    const server = event.target.matches('input[data-action="default"]') ? mcpOf(event.target) : null;
    if (server) server.default = event.target.checked;
  });
  host.addEventListener('click', (event) => {
    const button = event.target.closest('button[data-action]');
    const server = button && mcpOf(button);
    if (!server) return;
    if (button.dataset.action === 'edit') editMCP(server, button);
    if (button.dataset.action === 'delete') {
      mcpList = mcpList.filter((one) => one !== server);
      renderMCP();
    }
  });
  $('mcpAdd').addEventListener('click', (event) => editMCP(null, event.currentTarget));
  $('mcpImport').addEventListener('click', (event) => importMCP(event.currentTarget));
}

// The environment and the headers are one entry per line: KEY=value and
// Name: value, as they are written in a shell and in a request.
function linesOf(map, sep) {
  return Object.entries(map || {}).map(([key, value]) => key + sep + value).join('\n');
}

function mapOf(text, sep) {
  const out = {};
  for (const line of String(text || '').split('\n')) {
    const at = line.indexOf(sep);
    if (at <= 0) continue;
    out[line.slice(0, at).trim()] = line.slice(at + sep.length).trim();
  }
  return out;
}

// quoteArgs is splitArgs undone: an argument with a space keeps its quotes.
function quoteArgs(args) {
  return (args || []).map((a) => (/\s/.test(a) || a === '' ? (a.includes('"') ? "'" + a + "'" : '"' + a + '"') : a)).join(' ');
}

// mcpProblem is what keeps a server from being saved, in words; the server
// checks the same on Save.
function mcpProblem(server, previous) {
  if (!server.name) return ['name', 'The server needs a name.'];
  if (!MCP_NAME.test(server.name)) return ['name', 'A name may only have letters, digits, - and _.'];
  if (server.name === 'socrates') return ['name', 'The name socrates is Socrates’ own.'];
  if (mcpList.some((one) => one !== previous && one.name === server.name)) return ['name', 'There is a server named ' + server.name + ' already.'];
  if (server.type === 'stdio' && !server.command) return ['command', 'A local server needs its command.'];
  if (server.type !== 'stdio' && !server.url) return ['url', 'A remote server needs its URL.'];
  return null;
}

// editMCP adds a server (previous null) or changes one, in a dialog.
async function editMCP(previous, opener) {
  const server = previous || { name: '', type: 'stdio', default: true };
  const input = (id, label, value, { hint = '', ...attrs } = {}) => {
    const node = el(attrs.rows ? 'textarea' : 'input', {
      class: 'ds-input', id, value: value || '', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', ...attrs,
      type: attrs.rows ? null : 'text',
    });
    return [node, field(label, node, hint, id)];
  };
  const [name, nameField] = input('mcpEditName', 'Name', server.name, { autofocus: true, placeholder: 'github', hint: 'Letters, digits, - and _. The agent sees it in the names of the tools.' });
  const type = el('select', { class: 'ds-input', id: 'mcpEditType' },
    MCP_TYPES.map(([id, label]) => el('option', { value: id, text: label })));
  type.value = server.type || 'stdio';
  const typeField = field('Type', type, '', 'mcpEditType');
  const [command, commandField] = input('mcpEditCommand', 'Command', server.command, { placeholder: 'npx' });
  const [args, argsField] = input('mcpEditArgs', 'Arguments', quoteArgs(server.args), { placeholder: '-y @modelcontextprotocol/server-github' });
  const [env, envField] = input('mcpEditEnv', 'Environment', linesOf(server.env, '='), { rows: 3, placeholder: 'GITHUB_TOKEN=${GITHUB_TOKEN}', hint: 'One KEY=value per line.' });
  const [url, urlField] = input('mcpEditURL', 'URL', server.url, { placeholder: 'https://example.com/mcp', inputmode: 'url' });
  const [headers, headersField] = input('mcpEditHeaders', 'Headers', linesOf(server.headers, ': '), { rows: 3, placeholder: 'Authorization: Bearer ${API_TOKEN}', hint: 'One Name: value per line.' });
  const on = el('input', { type: 'checkbox', role: 'switch', id: 'mcpEditDefault', checked: server.default !== false });
  const onField = el('div', { class: 'ds-field' }, el('label', { class: 'ds-switch' }, on,
    el('span', { class: 'ds-switch__track', 'aria-hidden': 'true' }),
    el('span', { class: 'ds-switch__label', text: 'In every chat' }),
    el('span', { class: 'ds-switch__state', 'aria-hidden': 'true' },
      el('span', { class: 'ds-switch__on', text: 'On' }), el('span', { class: 'ds-switch__off', text: 'Off' }))),
  el('small', { class: 'ds-field__hint', text: 'Off, a chat has it only once it is switched on there.' }));
  const problem = el('div', { class: 'so-admin-notice' });
  problem.hidden = true;
  const local = [commandField, argsField, envField];
  const remote = [urlField, headersField];
  const showType = () => {
    for (const node of local) node.hidden = type.value !== 'stdio';
    for (const node of remote) node.hidden = type.value === 'stdio';
  };
  type.addEventListener('change', showType);
  showType();
  const form = el('div', { class: 'ds-form' }, problem, nameField, typeField, ...local, ...remote, onField);
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: previous ? 'Keep the changes' : 'Add the server' });
  const { dialog, shown } = openDialog({ title: previous ? 'MCP server ' + server.name : 'A new MCP server', field: form, actions: [cancel, accept], opener });
  accept.addEventListener('click', () => {
    const next = { name: name.value.trim(), type: type.value, default: on.checked };
    if (next.type === 'stdio') {
      Object.assign(next, { command: command.value.trim(), args: splitArgs(args.value), env: mapOf(env.value, '=') });
    } else {
      Object.assign(next, { url: url.value.trim(), headers: mapOf(headers.value, ':') });
    }
    const bad = mcpProblem(next, previous);
    for (const node of [name, command, url]) node.removeAttribute('aria-invalid');
    if (bad) {
      const [which, text] = bad;
      const node = { name, command, url }[which];
      node.setAttribute('aria-invalid', 'true');
      morph(problem, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${text}</span></div>`);
      problem.hidden = false;
      node.focus();
      return;
    }
    dialog.answer(next);
    DS.overlay.close(dialog);
  });
  const answer = await shown;
  if (!answer) return;
  if (previous) mcpList[mcpList.indexOf(previous)] = answer;
  else mcpList.push(answer);
  mcpList.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  renderMCP();
}

// importMCP takes servers as a .mcp.json or a program's docs write them -
// {"mcpServers": {name: {...}}}, or the inner object alone - and adds them,
// replacing one of the same name; Save keeps them.
async function importMCP(opener) {
  const [text, textField] = (() => {
    const node = el('textarea', { class: 'ds-input', id: 'mcpImportText', rows: 8, spellcheck: 'false', autocomplete: 'off', autofocus: true,
      placeholder: '{"mcpServers": {"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"]}}}' });
    return [node, field('JSON', node, 'As a .mcp.json has it. Each server comes in on in every chat.', 'mcpImportText')];
  })();
  const problem = el('div', { class: 'so-admin-notice' });
  problem.hidden = true;
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Add the servers' });
  const { dialog, shown } = openDialog({ title: 'Paste MCP servers', field: el('div', { class: 'ds-form' }, problem, textField), actions: [cancel, accept], opener });
  accept.addEventListener('click', () => {
    let servers;
    try {
      servers = mcpFromJSON(text.value);
    } catch (err) {
      text.setAttribute('aria-invalid', 'true');
      morph(problem, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${err.message}</span></div>`);
      problem.hidden = false;
      text.focus();
      return;
    }
    dialog.answer(servers);
    DS.overlay.close(dialog);
  });
  const servers = await shown;
  if (!servers) return;
  const names = new Set(servers.map((s) => s.name));
  mcpList = [...mcpList.filter((one) => !names.has(one.name)), ...servers]
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  renderMCP();
  toast(servers.length === 1 ? 'One server added. Save to keep it.' : servers.length + ' servers added. Save to keep them.');
}

function mcpFromJSON(text) {
  let data;
  try {
    data = JSON.parse(text);
  } catch (err) {
    throw new Error('That is not JSON: ' + err.message);
  }
  if (data && typeof data === 'object' && data.mcpServers) data = data.mcpServers;
  if (!data || typeof data !== 'object' || Array.isArray(data) || !Object.keys(data).length) {
    throw new Error('There is no server in it: write {"mcpServers": {"name": {...}}}.');
  }
  const out = [];
  for (const [name, entry] of Object.entries(data)) {
    if (!entry || typeof entry !== 'object') throw new Error('The server ' + name + ' is not an object.');
    const command = String(entry.command || '').trim();
    const url = String(entry.url || '').trim();
    let type = String(entry.type || '').trim().toLowerCase();
    if (!type) type = !command && url ? 'http' : 'stdio';
    if (type === 'streamable-http' || type === 'streamablehttp') type = 'http';
    if (!['stdio', 'http', 'sse'].includes(type)) throw new Error('The server ' + name + ' has the type ' + type + '; stdio, http and sse are known.');
    const server = type === 'stdio'
      ? { name, type, command, args: (entry.args || []).map(String), env: entry.env || {}, default: true }
      : { name, type, url, headers: entry.headers || {}, default: true };
    const bad = mcpProblem(server, mcpList.find((one) => one.name === name));
    if (bad) throw new Error(name + ': ' + bad[1]);
    out.push(server);
  }
  return out;
}

/* ------------------------------------------------------------ reordering */

// gripButton is a row's drag handle (DS .ds-grip): DS.motion.sortable on
// the list moves the row by pointer, finger or arrow keys, says where it
// went, and tells the list's onChange once it is let go.
function gripButton(label) {
  return el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm ds-grip so-grip', type: 'button',
    'aria-label': label + ' (arrow keys)', 'data-tip': 'Drag to reorder',
  }, iconEl('grip', 18));
}

/* -------------------------------------------------------------- accounts */

// Accounts are not part of the settings document: each change is its own
// request and is live at once, because an account is a sign-in and a folder
// on the machine (accounts.js has the list and the sign-in flows).

async function loadAccounts() {
  const host = $('accountList');
  if (!host) return;
  try {
    await accounts.load();
    renderAccounts();
  } catch (err) {
    host.replaceChildren(alertEl('error', errorMessage(err)));
  }
}

// renderAccounts is one group per agent, each with its accounts in order and
// a way to add one.
function renderAccounts() {
  const host = $('accountList');
  if (!host) return;
  host.replaceChildren(...accounts.agents().map((agentId) => {
    const list = accounts.forAgent(agentId);
    const rows = el('div', { class: 'so-rows', 'data-agent': agentId },
      list.length ? list.map(accountRow)
        : el('p', { class: 'ds-meta', text: 'No ' + harnesses.label(agentId) + ' account yet.' }));
    M.sortable(rows, { items: '.so-account', onChange: () => saveAccountOrder() });
    const add = el('button', {
      class: 'ds-btn ds-btn--text ds-add-row', type: 'button', 'data-agent': agentId,
      onclick: async (event) => {
        await accounts.addAccountDialog(event.currentTarget, agentId);
        renderAccounts();
      },
    }, iconEl('plus', 16), (/^[aeiou]/i.test(harnesses.label(agentId)) ? 'Add an ' : 'Add a ') + harnesses.label(agentId) + ' account');
    return el('section', { class: 'so-account-group', 'data-agent': agentId, 'aria-label': harnesses.label(agentId) },
      el('h3', { class: 'so-account-group__title' }, agentMark(agentId, 20), el('span', { text: harnesses.label(agentId) })),
      rows, add);
  }));
}

// The order of the lists is the order the sheet and the chats offer the
// accounts in, saved the moment a row is let go; it is one order across
// every agent.
async function saveAccountOrder() {
  const order = [...document.querySelectorAll('#accountList .so-account')].map((row) => row.dataset.id);
  try {
    await api('/api/accounts/order', { method: 'PUT', body: { order } });
    await accounts.load();
  } catch (err) {
    toast(errorMessage(err), 'error');
    await loadAccounts();
  }
}

function accountRow(account) {
  const signedIn = account.state === 'signed_in';
  const key = account.kind === 'api_key';
  const name = el('p', { class: 'so-account__name' }, accounts.iconFor(account, 16), el('span', { text: account.label }),
    el('span', { class: 'ds-meta', text: accounts.KIND_WORDS[account.kind] || account.kind }));
  const state = el('div', { class: 'so-account__state' });
  morph(state, html`${accounts.stateChip(account)}${account.recommended ? html`<span class="ds-meta" data-tag="recommended">most left</span>` : ''}${accounts.renewal(account) ? html`<span class="ds-meta">${accounts.renewal(account)}</span>` : ''}`);
  const meters = el('div', { class: 'so-account__meters' });
  morph(meters, accounts.meters(account));
  const actions = [];
  if (!signedIn) {
    actions.push(el('button', {
      class: 'ds-btn ds-btn--sm', type: 'button', 'data-action': 'signin',
      text: key ? 'Enter the key' : (account.state_detail ? 'Sign in again' : 'Sign in'),
      onclick: (event) => signInAccount(account, event.currentTarget),
    }));
  } else {
    actions.push(el('button', {
      class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button', 'data-action': 'signin',
      'aria-label': (key ? 'Replace the key of ' : 'Sign in again to ') + account.label,
      'data-tip': key ? 'Replace the key' : 'Sign in again',
      onclick: (event) => signInAccount(account, event.currentTarget),
    }, iconEl(key ? 'key' : 'sync', 18)));
  }
  actions.push(infoTip(accounts.facts(account), { label: 'About ' + account.label }));
  actions.push(el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button', 'data-action': 'rename',
    'aria-label': 'Rename ' + account.label, 'data-tip': 'Rename',
    onclick: (event) => renameAccount(account, event.currentTarget),
  }, iconEl('pencil', 18)));
  actions.push(el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button', 'data-action': 'delete',
    'aria-label': 'Delete ' + account.label, 'data-tip': 'Delete',
    onclick: (event) => deleteAccount(account, event.currentTarget),
  }, iconEl('trash', 18)));
  const grip = gripButton('Move ' + account.label);
  return el('div', { class: 'so-account', 'data-id': account.id, 'data-account-state': account.state },
    grip, el('div', { class: 'so-account__main' }, name, state, meters),
    el('div', { class: 'so-account__actions' }, actions));
}

// signInAccount runs a subscription's sign-in, or takes a new key for an
// API-key account.
async function signInAccount(account, opener) {
  if (account.kind === 'api_key') {
    const keyValue = await promptSecret('A new key for ' + account.label, opener);
    if (!keyValue) return;
    try {
      await api('/api/accounts/' + encodeURIComponent(account.id), { method: 'PATCH', body: { key: keyValue }, timeout: 45000 });
      toast('The key of ' + account.label + ' was replaced.', 'success');
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
    await loadAccounts();
    return;
  }
  await accounts.signIn(account, opener);
  await loadAccounts();
}

// promptSecret asks for a key in a field that does not show it.
async function promptSecret(title, opener) {
  const input = el('input', { class: 'ds-input', type: 'password', id: 'accountKeyAgain', autocomplete: 'off', spellcheck: 'false', autofocus: true, enterkeyhint: 'done' });
  const field = el('div', { class: 'ds-field' }, el('label', { class: 'ds-field__label', for: 'accountKeyAgain', text: 'API key' }), input);
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Save the key' });
  const { dialog, shown } = openDialog({ title, body: 'It is kept encrypted on this machine and is never shown again.', field, actions: [cancel, accept], opener });
  const done = () => {
    dialog.answer(input.value.trim());
    DS.overlay.close(dialog);
  };
  accept.addEventListener('click', done);
  input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.isComposing) {
      event.preventDefault();
      done();
    }
  });
  const answer = await shown;
  return answer || '';
}

async function renameAccount(account, opener) {
  const next = await promptDialog({ title: 'Rename ' + account.label, label: 'Name', value: account.label, confirmLabel: 'Rename', opener });
  if (!next || next === account.label) return;
  try {
    await api('/api/accounts/' + encodeURIComponent(account.id), { method: 'PATCH', body: { label: next } });
    toast('Renamed to ' + next + '.', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
  await loadAccounts();
}

// Deleting takes the account's sign-in with it, so it asks first, opening on
// Cancel, and the button says what it does. The server refuses an account a
// chat runs on, and the agent's own sign-in moved in from HOME.
async function deleteAccount(account, opener) {
  const yes = await confirmDialog({
    title: 'Delete ' + account.label + '?',
    body: 'Its sign-in goes with it. Chats that run on it have to move to another account first.',
    confirmLabel: 'Delete ' + account.label,
    danger: true,
    opener,
  });
  if (!yes) return;
  try {
    await api('/api/accounts/' + encodeURIComponent(account.id), { method: 'DELETE' });
    toast(account.label + ' was deleted.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
  await loadAccounts();
}

/* ------------------------------------------------------- the tmux engine */

const TMUX_CHIP = {
  ok: ['ok', 'Ready'],
  old: ['warning', 'Too old'],
  missing: ['error', 'Missing'],
};

function tmuxState(report) {
  if (report.ok) return 'ok';
  return report.installed ? 'old' : 'missing';
}

function renderTmux(report) {
  const state = tmuxState(report);
  const [kind, label] = TMUX_CHIP[state];
  // The version found and the one needed are what this card is for, so they
  // are in the line; the path is state, not documentation, so it goes behind
  // the "i" where the rest of this page puts a build number.
  renderState($('tmuxStatus'), {
    kind, label, state,
    facts: [report.version ? 'tmux ' + report.version : '', 'needs ' + (report.min || '3.3') + ' or newer'],
    tip: report.path ? [report.path] : [],
    tipLabel: 'Where tmux is',
  });
  showAlert('tmuxReasonBox', 'tmuxReason', report.ok ? '' : (report.reason || ''));
}

async function refreshTmux() {
  try {
    renderTmux(await api('/api/tmux'));
  } catch (err) {
    if (!isOffline(err)) toast(errorMessage(err), 'error');
  }
}

/* --------------------------------------------------------------- updates */

// What the updater did last, read from GET /api/updates. The run itself is
// socrates-update.service, beside Socrates rather than inside it: the button
// only asks systemd to start it, and the card polls until it is over - every
// three seconds while it runs, once a minute otherwise, and only while the
// card can be seen. onWake catches up with whatever happened in between.

const UPDATES_CHIP = {
  ok: ['ok', 'Up to date'],
  failed: ['error', 'Failed'],
  stalled: ['error', 'Did not finish'],
  running: ['running', 'Updating'],
  never: ['off', 'Never updated'],
};

// What one component's run came to, as a chip.
const COMPONENT_CHIP = {
  updated: ['ok', 'Updated'],
  installed: ['ok', 'Installed'],
  current: ['plain', 'Current'],
  failed: ['error', 'Failed'],
  held: ['warning', 'Held'],
  skipped: ['warning', 'Skipped'],
};

let updatesTimer = null;
let updatesRunning = false;
let updatesSeenAt = 0;
let updatesOnScreen = true;
// updatesLast is the last answer drawn, and updatesAwaiting a run the button
// has asked systemd for that systemd may not be showing yet: --no-block
// returns before the unit is "activating", and until it is the card would
// otherwise say the run is over before it began.
let updatesLast = null;
let updatesAwaiting = null;

// updatesWhen is a time from the report or from systemd, said relative to
// now in either direction. systemd's own text, when the server could not read
// it, is shown as it came.
function updatesWhen(iso) {
  const at = Date.parse(iso || '');
  if (Number.isNaN(at)) return iso || '';
  const diff = Math.round((at - Date.now()) / 1000);
  const s = Math.abs(diff);
  if (s < 60) return diff > 0 ? 'in a moment' : 'just now';
  let text;
  if (s < 3600) text = Math.round(s / 60) + '\u00a0min';
  else if (s < 86400) text = Math.round(s / 3600) + '\u00a0h';
  else if (s < 7 * 86400) text = Math.round(s / 86400) + (Math.round(s / 86400) === 1 ? ' day' : ' days');
  else return new Date(at).toLocaleString();
  return diff > 0 ? 'in ' + text : text + ' ago';
}

// updatesExact is a time for the "i": the local date and time when it can be
// read, and the text as it came when it cannot, never "Invalid Date".
function updatesExact(iso) {
  const at = Date.parse(iso || '');
  return Number.isNaN(at) ? (iso || '') : new Date(at).toLocaleString();
}

// updatesState is the card's verdict. A report that says "running" while the
// service is not is a run that died - a reboot in the middle of it - unless
// there is no service to ask, which is somebody running the script by hand.
function updatesState(data) {
  const status = data.status;
  if (data.running) return 'running';
  if (!status) return 'never';
  if (status.state === 'running') return data.timer && data.timer.installed ? 'stalled' : 'running';
  return status.state === 'done' && status.ok ? 'ok' : 'failed';
}

function updateRow(component) {
  const updated = component.status === 'updated' && component.previous;
  const version = updated
    ? component.previous + ' → ' + component.version
    : (component.version || '—');
  const [kind, label] = COMPONENT_CHIP[component.status] || ['waiting', component.status || 'Not run'];
  const more = component.error || component.detail || '';
  return html`<tr data-ds-key="${component.name}">
    <td class="ds-col--text"><span class="so-cell-name">${component.name}</span>${more ? html`<span class="ds-meta so-cell-more so-break">${more}</span>` : ''}</td>
    <td class="so-break">${version}</td>
    <td>${chip(kind, label, component.status || '')}</td>
  </tr>`;
}

function renderUpdates(data) {
  // The run the button started counts as running until systemd says so
  // itself, the report shows a new run, or long enough has passed that
  // systemd evidently did not start it.
  const status = data.status;
  if (updatesAwaiting && (data.running || Date.now() > updatesAwaiting.until
    || (status && status.started_at && status.started_at !== updatesAwaiting.before))) {
    updatesAwaiting = null;
  }
  updatesLast = data;
  const state = updatesAwaiting ? 'running' : updatesState(data);
  const [kind, label] = UPDATES_CHIP[state];

  const timer = data.timer || {};
  const facts = [];
  if (status && (status.finished_at || status.started_at)) {
    facts.push('last run ' + updatesWhen(status.finished_at || status.started_at)
      + (status.trigger ? ' (' + status.trigger + ')' : ''));
  }
  if (timer.installed && timer.enabled && timer.next) facts.push('next run ' + updatesWhen(timer.next));
  // The exact times and what the updater may touch are state, not the
  // verdict, so they go behind the "i".
  const tip = [];
  if (status && status.started_at) tip.push('Started ' + updatesExact(status.started_at));
  if (status && status.finished_at) tip.push('Finished ' + updatesExact(status.finished_at));
  if (timer.next) tip.push('Next run ' + updatesExact(timer.next));
  if (data.auto) tip.push('Updates automatically: ' + data.auto);
  renderState($('updatesStatus'), { kind, label, state, facts, tip, tipLabel: 'Update details' });

  const running = state === 'running';
  $('updatesProgress').hidden = !running;

  const components = status && Array.isArray(status.components) ? status.components.filter(Boolean) : [];
  morph($('updatesList'), html`${components.map(updateRow)}`);
  $('updatesTable').hidden = !components.length;

  let hintText = '';
  if (!timer.installed) {
    hintText = 'The update timer is not installed on this machine, so nothing updates by itself. scripts/install.sh sets it up.';
  } else if (!timer.enabled) {
    hintText = 'The update timer is switched off, so nothing updates by itself until it is enabled again.';
  } else if (state === 'stalled') {
    hintText = 'The run stopped before it wrote its result. The log shows how far it got.';
  } else if (status && status.state === 'failed') {
    hintText = 'The run itself was aborted. The log says where.';
  }
  $('updatesHint').textContent = hintText;

  const log = $('updatesLog');
  if (!log.hidden) {
    log.textContent = data.log_tail || 'no output yet';
    log.scrollTop = log.scrollHeight;
  }

  // A run that has just ended is said once, and an update of Socrates itself
  // means this page is the old one.
  if (updatesRunning && !running && status) {
    const self = components.find((c) => c.name === 'socrates' && c.status === 'updated');
    if (self) toast('Socrates was updated. Reload the page to use the new version.');
    else toast(status.ok ? 'Update finished' : 'Update finished with errors', status.ok ? 'success' : 'error');
  }
  updatesRunning = running;
  const button = $('updatesRun');
  button.disabled = running;
  M.setLabel(button, running ? 'Updating…' : 'Update now');

  clearTimeout(updatesTimer);
  updatesTimer = setTimeout(pollUpdates, running ? 3000 : 60000);
}

// pollUpdates is the timer's tick. The slow poll does nothing while the card
// is off screen or the page is hidden; a run in progress is followed as long
// as the page is open, because its end is what the person is waiting for.
function pollUpdates() {
  const hidden = document.visibilityState === 'hidden';
  if (hidden || (!updatesRunning && !updatesOnScreen)) {
    clearTimeout(updatesTimer);
    updatesTimer = setTimeout(pollUpdates, updatesRunning ? 3000 : 60000);
    return;
  }
  refreshUpdates();
}

async function refreshUpdates() {
  // Set when asking rather than when answered, so the observer's first
  // callback, which comes while the first request is still out, does not
  // send a second one.
  updatesSeenAt = Date.now();
  try {
    const data = await api('/api/updates');
    renderUpdates(data);
  } catch (err) {
    clearTimeout(updatesTimer);
    // A run that updates Socrates restarts it, so a poll that cannot reach
    // the server is expected now and then, and the connection bar already
    // says so. The poll goes on either way.
    updatesTimer = setTimeout(pollUpdates, updatesRunning ? 3000 : 10000);
    if (!isOffline(err)) $('updatesHint').textContent = errorMessage(err);
  }
}

function watchUpdatesCard() {
  if (!('IntersectionObserver' in window)) return;
  new IntersectionObserver((entries) => {
    updatesOnScreen = entries.some((entry) => entry.isIntersecting);
    // Scrolled back to after the minute was up: the card catches up now
    // rather than at the next tick.
    if (updatesOnScreen && settings && Date.now() - updatesSeenAt > 60000) refreshUpdates();
  }).observe($('updatesCard'));
}

async function runUpdates() {
  const button = $('updatesRun');
  try {
    await M.busy(button, api('/api/updates/run', { method: 'POST', body: {} }));
    const last = updatesLast && updatesLast.status;
    updatesAwaiting = { before: (last && last.started_at) || '', until: Date.now() + 30000 };
    toast('Update started');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    // renderUpdates decides whether this button belongs enabled at all; the
    // short wait gives systemd the moment it takes to say "activating".
    setTimeout(refreshUpdates, 800);
  }
}

// toggleLog shows or hides a log under its button, and fetches it fresh.
function toggleLog(buttonId, logId, refresh) {
  const log = $(logId);
  const button = $(buttonId);
  log.hidden = !log.hidden;
  button.textContent = log.hidden ? 'Show log' : 'Hide log';
  button.setAttribute('aria-expanded', String(!log.hidden));
  if (!log.hidden) refresh();
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

// alertEl is a DS alert built in script: '' is the neutral one. `details`,
// the technical side (a program's own error), goes behind its "i".
function alertEl(kind, text, details = []) {
  const glyph = kind === 'error' ? 'alertCircle' : kind === 'warning' ? 'alert' : 'info';
  return el('div', { class: 'ds-alert' + (kind ? ' ds-alert--' + kind : ''), role: kind === 'error' ? 'alert' : 'status' },
    iconEl(glyph, 20), el('span', { class: 'ds-alert__text', text }),
    details.length ? infoTip(details, { label: 'Details' }) : null);
}

// programTrouble is what a program's error means here, in words; its own
// message ("codex would not list its models: exit status 2") is the detail.
function programTrouble(harness) {
  if (!harness.installed) return harness.label + ' is not installed on this machine.';
  return harness.label + ' could not list its models. The sheet takes a model’s id typed in, or starts it on its own default.';
}

function renderHarnesses(status) {
  const host = $('harnessCards');
  if (!host) return;
  const found = harnesses.list();
  const refreshing = status === 'refreshing';
  const refresh = facedButton(refreshing ? 'Asking…' : 'Ask this machine again', 'Asking', null, {
    id: 'harnessRefresh', disabled: refreshing,
    onclick: () => { loadHarnesses(true).catch(() => {}); },
  });
  const intro = el('section', { class: 'ds-card', 'aria-labelledby': 'programsTitle' },
    el('div', { class: 'ds-card__row' }, el('h2', { class: 'ds-card__title', id: 'programsTitle', text: 'Programs' }),
      found.length ? refresh : null));
  if (!found.length) {
    intro.append(status === 'loading'
      ? el('div', { class: 'ds-loading', role: 'status' },
        el('span', { class: 'ds-loader ds-loader--sm', 'aria-hidden': 'true' }),
        'Asking this machine which programs are installed')
      : alertEl('error', status || 'Socrates could not ask this machine which programs are installed.'));
    host.replaceChildren(intro);
    return;
  }
  intro.append(el('p', { class: 'ds-muted',
    text: 'Which programs a session can be, where each one lives, and what it starts on. '
      + 'How they are started — permissions, sandbox, remote control, theme — is not a setting: '
      + 'every session gets the same opinionated command line.' }));
  if (status && status !== 'loading' && !refreshing) {
    intro.append(alertEl('error', status));
  } else if (harnesses.stale()) {
    intro.append(alertEl('',
      'Showing the programs from your last visit — this machine could not be asked just now.'));
  }
  const cards = HARNESS_ORDER.map((id) => found.find((h) => h.id === id)).filter(Boolean).map(harnessCard);
  host.replaceChildren(intro, ...cards);
  renderDefaultHarness();
}

function harnessCard(harness) {
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

  const binaryId = 'opt-' + harness.id + '-binary';
  const binary = el('input', {
    class: 'ds-input', type: 'text', spellcheck: 'false', id: binaryId,
    placeholder: harness.path || 'found on PATH',
    value: optionValue(harness.id, 'binary') || '',
  });
  binary.addEventListener('input', () => setPath(settings, optionPath(harness.id, 'binary'), binary.value.trim()));

  const mark = agentMark(harness.id, 24);
  if (!harness.installed) mark.classList.add('so-agent--quiet');
  const titleId = 'harness-' + harness.id + '-title';
  const options = (OPTIONS[harness.id] || []).map((option) => optionField(harness, option));

  return el('section', {
    class: 'ds-card so-program', id: 'harness-' + harness.id, 'data-installed': String(!!harness.installed),
    'aria-labelledby': titleId,
  },
    el('div', { class: 'ds-card__row' },
      el('h2', { class: 'ds-card__title', id: titleId },
        el('span', { class: 'so-program__name' }, mark, el('span', { text: harness.label }))),
      el('span', { class: 'so-program__state' },
        harness.installed ? null : chipEl('warning', 'Not installed', 'missing'),
        facts.length ? infoTip(facts, { label: harness.label + ' details' }) : null)),
    harness.error ? alertEl(harness.installed ? 'warning' : 'error', programTrouble(harness), [harness.error]) : null,
    harness.notes ? el('p', { class: 'ds-muted', text: harness.notes }) : null,
    el('label', { class: 'ds-check' }, toggle,
      el('span', { text: 'Offer ' + harness.label + ' in the new-session sheet' })),
    // Two fields at most, so they sit in the card rather than behind a
    // disclosure: a setting nobody can find is a setting nobody has.
    el('div', { class: 'ds-form' },
      options.length ? el('div', { class: 'ds-form-grid ds-form-grid--2' }, options) : null,
      ['shell', 'vscode'].includes(harness.id) ? null : modelList(harness),
      field('Binary path', binary,
        'Leave it empty to look the program up on PATH, which is what a normal installation wants.', binaryId)),
    el('p', { class: 'ds-meta', text: START_ONLY_NOTE }),
  );
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
  switch (option.type) {
    case 'select': {
      control = el('select', { class: 'ds-input', id });
      for (const v of option.values) control.append(el('option', { value: v, text: v || '—' }));
      control.value = value ?? '';
      control.addEventListener('change', () => write(control.value));
      break;
    }
    case 'model': {
      // A program that could not list its models leaves an id to type in
      // (combobox.js: no list, so no arrow either).
      const picker = combobox({
        value: value || '',
        placeholder: (harness.models || []).length ? 'Pick from the list, or type a model id' : 'Type a model id',
        items: () => (harness.models || []).map((m) => ({
          value: m.id, label: m.label || m.id, hint: m.hint || '', group: m.group || '',
        })),
        onChange: (next) => write(next.trim()),
      });
      picker.input.id = id;
      control = dictated(picker.node, picker.input);
      break;
    }
    default: {
      control = el('input', {
        class: 'ds-input', type: 'text', id, spellcheck: 'false',
        placeholder: option.placeholder || '', value: value ?? '',
      });
      control.addEventListener('input', () => write(control.value));
    }
  }
  return field(option.label, control, option.hint || '', id);
}

// modelList is the person's own short list for one harness: the models the
// new-session sheet offers, in this order, each with the effort it starts on.
// An empty list means the sheet offers everything the program reported. A
// row moves by its grip, like an account.
function modelList(harness) {
  const path = optionPath(harness.id, 'models');
  const picks = () => (optionValue(harness.id, 'models') || [])
    .map((p) => ({ id: p.id, effort: p.effort || '', instructions: p.instructions || '' }));
  const known = (id) => (harness.models || []).find((m) => m.id === id) || null;

  const rows = el('div', { class: 'so-rows' });
  M.sortable(rows, {
    items: '.so-model',
    onChange: (order) => {
      const list = picks();
      setPath(settings, path, order.map((row) => list.find((p) => p.id === row.dataset.id)).filter(Boolean));
    },
  });
  function render() {
    const list = picks();
    if (!list.length) {
      rows.replaceChildren(el('p', { class: 'ds-meta', text: (harness.models || []).length
        ? 'No short list — the new-session sheet offers every model ' + harness.label + ' reports.'
        : 'No short list — the new-session sheet takes a model’s id typed in.' }));
      return;
    }
    rows.replaceChildren(...list.map((pick) => {
      const model = known(pick.id);
      const efforts = harnesses.effortsFor(harness.id, pick.id);
      const effort = el('select', { class: 'ds-input', 'aria-label': 'Default effort for ' + pick.id });
      for (const value of ['', ...efforts]) {
        effort.append(el('option', { value, text: harnesses.effortLabel(value) }));
      }
      effort.value = efforts.includes(pick.effort) ? pick.effort : '';
      effort.hidden = !efforts.length;
      effort.addEventListener('change', () => {
        setPath(settings, path, picks().map((p) => (p.id === pick.id ? { ...p, effort: effort.value } : p)));
      });
      const name = model ? (model.label || pick.id) : pick.id;
      const instructions = el('button', {
        class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button',
        'aria-label': 'Instructions for ' + pick.id, 'data-tip': 'Instructions',
        onclick: async (event) => {
          const text = await modelInstructions(harness, name, pick.instructions, event.currentTarget);
          if (text === null) return;
          setPath(settings, path, picks().map((p) => (p.id === pick.id ? { ...p, instructions: text } : p)));
          render();
        },
      }, iconEl('fileText', 18));
      const remove = el('button', {
        class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button',
        'aria-label': 'Remove ' + pick.id, 'data-tip': 'Remove',
        onclick: () => { setPath(settings, path, picks().filter((p) => p.id !== pick.id)); render(); },
      }, iconEl('trash', 18));
      // Whether it has instructions is said in words, beside what it is.
      const about = (model ? (model.hint || pick.id) : pick.id + ' · typed in, not on the list ' + harness.label + ' reports')
        + (pick.instructions ? ' · with instructions' : '');
      return el('div', { class: 'so-model', 'data-id': pick.id },
        gripButton('Move ' + pick.id),
        el('div', { class: 'so-model__name' },
          el('span', { class: 'so-ellipsis', text: name }),
          el('span', { class: 'ds-meta so-ellipsis', text: about })),
        effort, el('span', { class: 'so-model__actions' }, instructions, remove));
    }));
  }

  const picker = combobox({
    value: '',
    placeholder: harness.static ? 'sonnet' : ((harness.models || []).length ? 'Pick from the list, or type a model id' : 'Type a model id'),
    items: () => (harness.models || []).map((m) => ({
      value: m.id, label: m.label || m.id, hint: m.hint || '', group: m.group || '',
    })),
    onChange: () => {},
  });
  const input = picker.input;
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
    if (event.key === 'Enter' && !picker.isOpen()) {
      event.preventDefault();
      add();
    }
  });
  render();
  return el('div', { class: 'ds-field' },
    el('label', { class: 'ds-field__label', for: input.id, text: 'Models offered in the sheet' }),
    rows,
    el('div', { class: 'ds-field__row' }, dictated(picker.node, input),
      el('button', { class: 'ds-btn', type: 'button', text: 'Add', id: 'add-model-' + harness.id, onclick: add })),
    el('small', { class: 'ds-field__hint', text: (harness.models || []).length
      ? 'Pick from what ' + harness.label + ' reports or type an id, and set the effort a new session starts on. '
        + 'Leave the list empty to offer all of them.'
      : 'Type a model’s id, and set the effort a new session starts on. Leave the list empty to type one in the sheet.' }),
  );
}

// modelInstructions asks for the person's instructions for one favourite:
// what a chat of the harness is told while it runs on that model, and only
// then. It resolves with the trimmed text ("" for none), or null when the
// person cancelled; Save keeps it.
async function modelInstructions(harness, name, value, opener) {
  const id = 'modelInstructionsText';
  const text = el('textarea', {
    class: 'ds-input', id, rows: 10, autofocus: true,
    placeholder: 'For example: answer briefly, and ask before a change that touches more than one file.',
  });
  text.value = value || '';
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Keep the instructions' });
  const { dialog, shown } = openDialog({
    title: 'Instructions for ' + name,
    body: 'A ' + harness.label + ' chat on this model is told these with its own instructions, and only while it runs on '
      + 'this model: a chat switched to it gets them with its next message.',
    field: el('div', { class: 'ds-form' }, field('Instructions', dictated(text, text), 'Empty means none. '
      + 'Once saved, they count from a chat’s next start.', id)),
    actions: [cancel, accept],
    opener,
  });
  // Instructions run to paragraphs: DS's wide dialog gives them the room.
  dialog.classList.add('ds-dialog--wide');
  accept.addEventListener('click', () => {
    dialog.answer({ text: text.value.trim() });
    DS.overlay.close(dialog);
  });
  const answer = await shown;
  return answer ? answer.text : null;
}

/* ---------------------------------------------------------------- saving */

function bind() {
  $('saveTop').addEventListener('click', save);
  $('saveBottom').addEventListener('click', save);
  $('runChecks').addEventListener('click', runChecks);
  $('changePw').addEventListener('click', changePassword);
  $('presetAdd').addEventListener('click', () => {
    const host = $('presetDirs');
    const row = presetRow();
    M.flip(host, () => host.append(row));
    row.querySelector('input').focus();
  });
  bindMCP();
  $('windowSize').addEventListener('change', renderWindowSizeHint);
  $('tunnelStart').addEventListener('click', startTunnel);
  $('tunnelStop').addEventListener('click', stopTunnel);
  $('tunnelMode').addEventListener('change', renderTunnelMode);
  $('tunnelInstall').addEventListener('click', installCloudflared);
  $('tunnelLogToggle').addEventListener('click', () => toggleLog('tunnelLogToggle', 'tunnelLog', refreshTunnel));
  $('updatesRun').addEventListener('click', runUpdates);
  $('updatesLogToggle').addEventListener('click', () => toggleLog('updatesLogToggle', 'updatesLog', refreshUpdates));
  $('checkVoiceKey').addEventListener('click', checkVoiceKey);
  $('backupTest').addEventListener('click', testBackup);
  $('backupRun').addEventListener('click', runBackup);
  $('backupSave').addEventListener('click', saveBackup);
  $('backupOff').addEventListener('click', turnBackupOff);
  $('backupDetail').addEventListener('click', openBackupTip);
  $('backupLegacy').addEventListener('click', (event) => {
    const button = event.target.closest('[data-action="delete-legacy"]');
    if (button) deleteLegacy(button);
  });
  $('testVoice').addEventListener('click', testVoice);
  for (const summary of document.querySelectorAll('.so-disclosure > summary')) {
    if (!summary.querySelector('.ds-chevron')) summary.append(iconEl('chevronDown', 20, 'ds-chevron'));
  }
  document.addEventListener('keydown', (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 's') {
      event.preventDefault();
      save();
    }
  });
}

let saving = false;

async function save() {
  if (saving) return;
  saving = true;
  const next = collect();
  // Settings are written whole, so the same body sent twice leaves exactly
  // the same result - which makes this safe to repeat over a bad link.
  const work = api('/api/settings', { method: 'PUT', attempts: 4, body: { settings: next } });
  try {
    // Both Save buttons are one action, and they say so together.
    const [data] = await Promise.all([M.busy($('saveTop'), work), M.busy($('saveBottom'), work)]);
    settings = data.settings;
    fillForm();
    syncModelPickers();
    renderPresets();
    mcpList = (getPath(settings, 'mcp.servers') || []).map((server) => ({ ...server }));
    renderMCP();
    // The cards are drawn from the settings they just wrote, so they have to
    // follow whatever the server normalised them into.
    renderHarnesses('');
    refreshTunnel();
    for (const warning of data.warnings || []) toast(warning);
    toast('Settings saved', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    saving = false;
  }
}

// showNotice is the page's one standing message, as a DS alert.
function showNotice(text, kind) {
  const notice = $('notice');
  const [cls, glyph] = kind === 'error' ? ['ds-alert ds-alert--error', 'alertCircle']
    : kind === 'ok' ? ['ds-alert ds-alert--success', 'checkCircle'] : ['ds-alert', 'info'];
  morph(notice, html`<div class="${cls}" role="${kind === 'error' ? 'alert' : 'status'}">${raw(icon(glyph, 20))}<span class="ds-alert__text">${text}</span></div>`);
  notice.hidden = false;
}

function field(label, control, hintText, id) {
  return el('div', { class: 'ds-field' },
    el('label', { class: 'ds-field__label', for: id || null, text: label }),
    control,
    hintText ? el('small', { class: 'ds-field__hint', text: hintText }) : null);
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
  renderState(host, {
    kind: ok ? 'ok' : 'error', label: ok ? 'Voice ready' : 'Voice not working', state: ok ? 'ready' : 'failed',
    facts: [detail],
  });
}

async function testVoice() {
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
  try {
    await M.busy($('testVoice'), () => speak(sample));
    showVoiceStatus(true, 'The sample was rendered by Google and played here.');
  } catch (err) {
    // Every press answers, whether or not this reason has been seen before:
    // a button that stays silent the second time reads as a button that did
    // nothing at all.
    showVoiceStatus(false, errorMessage(err));
    toast(errorMessage(err), speechKind(err));
  }
}

// checkVoiceKey asks Google for the voice list of the stored language, which
// is the cheapest thing that proves a key: it needs the same API enabled and
// the same key restriction as speaking does, and it renders nothing.
async function checkVoiceKey() {
  try {
    const data = await M.busy($('checkVoiceKey'), api('/api/voice/check', { method: 'POST' }));
    showVoiceStatus(!!data.ok, data.detail || '');
  } catch (err) {
    showVoiceStatus(false, errorMessage(err));
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
  $('tunnelToken').closest('.ds-field').hidden = !named;
  $('tunnelHostname').closest('.ds-field').hidden = !named;
  const local = localURL || 'the local address shown above';
  // The choices are short, so that a phone shows them whole; what each one
  // means is said here.
  $('tunnelModeHint').textContent = named
    ? 'Your own hostname, with a token from your Cloudflare account. In the Cloudflare dashboard, point its public hostname at ' + local + '.'
    : 'A random trycloudflare.com address, free and instant, but it changes every restart and anyone with the link can reach the login page.';
}

const TUNNEL_CHIP = {
  running: ['ok', 'Up'],
  starting: ['running', 'Connecting'],
  installing: ['running', 'Downloading cloudflared'],
  failed: ['error', 'Failed'],
  stopped: ['off', 'Off'],
};

function renderTunnelStatus(status) {
  const [kind, label] = TUNNEL_CHIP[status.state] || ['waiting', status.state || 'Unknown'];
  const facts = [];
  if (status.url) facts.push(html`<a href="${status.url}" target="_blank" rel="noopener" class="so-break">${status.url}</a>`);
  facts.push('local: ' + (status.local_url || 'http://127.0.0.1:8080'));
  if (!status.installed) {
    facts.push(status.can_install ? 'cloudflared is downloaded on start' : 'cloudflared is not installed');
  }
  if (status.restarts) facts.push(status.restarts + ' restarts');
  // Which build and where it lives are state, behind the "i".
  const tip = [];
  if (status.installed && status.version) tip.push(status.version);
  if (status.installed && status.path) tip.push(status.path);
  renderState($('tunnelStatus'), { kind, label, state: status.state || '', facts, tip, tipLabel: 'cloudflared details' });
  showAlert('tunnelErrorBox', 'tunnelError', status.error || '');

  const log = $('tunnelLog');
  if (!log.hidden) {
    log.textContent = (status.logs || []).join('\n') || 'no output yet';
    log.scrollTop = log.scrollHeight;
  }
  M.setLabel($('tunnelStart'), status.state === 'stopped' ? 'Start tunnel' : 'Restart tunnel');
  $('tunnelStop').disabled = status.state === 'stopped';

  const installButton = $('tunnelInstall');
  installButton.hidden = status.installed || !status.can_install;
  if (status.installed) {
    $('tunnelInstallHint').textContent = status.managed
      ? 'Managed by Socrates.'
      : 'Found on your PATH.';
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
  // Back from a sign-in page, or from another device.
  loadAccounts();
  refreshTunnel();
  refreshTmux();
  refreshUpdates();
  refreshBackup();
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
  try {
    const data = await M.busy($('tunnelStart'), api('/api/tunnel/start', { method: 'POST', body: { tunnel: tunnelForm() } }));
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel starting');
    setTimeout(refreshTunnel, 1200);
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function installCloudflared() {
  if ($('tunnelLog').hidden) toggleLog('tunnelLogToggle', 'tunnelLog', refreshTunnel);
  else refreshTunnel();
  try {
    const data = await M.busy($('tunnelInstall'), api('/api/tunnel/install', { method: 'POST', body: {} }));
    renderTunnelStatus(data.status || {});
    toast('cloudflared installed', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    refreshTunnel();
  }
}

async function stopTunnel() {
  try {
    const data = await M.busy($('tunnelStop'), api('/api/tunnel/stop', { method: 'POST', body: {} }));
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel stopped');
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    // renderTunnelStatus decides whether this button belongs enabled at all.
    refreshTunnel();
  }
}

/* ---------------------------------------------------------------- backup */

// The backup of the data directory. The card reads GET /api/backup; its form
// is separate from the settings document, because it is proved against the
// bucket before it is saved and lives in backup.json, the one file the
// backup leaves out.

let backupTimer = null;
let backupWasRunning = false;
let snapshotsLoaded = false;

function ago(ms) {
  if (!ms) return 'never';
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return Math.round(s / 60) + '\u00a0min ago';
  if (s < 86400) return Math.round(s / 3600) + '\u00a0h ago';
  return new Date(ms).toLocaleString();
}

function inTime(ms) {
  const s = Math.max(0, Math.round((ms - Date.now()) / 1000));
  if (s < 60) return 'in under a minute';
  if (s < 3600) return 'in ' + Math.round(s / 60) + '\u00a0min';
  return 'at ' + new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function bytes(n) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + units[i];
}

// backupAlert is one alert of the card. What it is about - paths, the
// technical rest - goes behind its "i", which the card's click handler opens.
function backupAlert(kind, text, tip = [], tipLabel = 'Details') {
  const [cls, glyph, role] = kind === 'error' ? ['ds-alert ds-alert--error', 'alertCircle', 'alert']
    : kind === 'warning' ? ['ds-alert ds-alert--warning', 'alert', 'alert'] : ['ds-alert', 'info', 'status'];
  const info = tip.length
    ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" aria-label="${tipLabel}" data-tip="${tip.join('\n')}">${raw(icon('info', 18))}</button>`
    : '';
  return html`<div class="${cls}" role="${role}">${raw(icon(glyph, 20))}<span class="ds-alert__text">${text}</span>${info}</div>`;
}

function openBackupTip(event) {
  const button = event.target.closest('.so-info');
  if (!button) return;
  alertDialog({ title: button.getAttribute('aria-label'), lines: button.getAttribute('data-tip').split('\n'),
    confirmLabel: 'Close', opener: button });
}

// firstSentence is a server error without its technical rest, which goes
// behind the "i".
function firstSentence(text) {
  const at = text.indexOf(': ');
  const head = at > 0 && at < 90 ? text.slice(0, at) : text;
  const sentence = head.charAt(0).toUpperCase() + head.slice(1);
  return /[.!?…]$/.test(sentence) ? sentence : sentence + '.';
}

function renderBackup(status, fillForm) {
  const cfg = status.config || null;
  const last = status.last_success;
  let kind = 'off';
  let label = 'Not configured';
  const facts = [];
  if (status.running) {
    kind = 'running';
    label = status.phase ? status.phase.charAt(0).toUpperCase() + status.phase.slice(1) : 'Backing up';
    if (status.files) facts.push(status.files + ' file' + (status.files === 1 ? '' : 's') + ' read', bytes(status.uploaded) + ' sent');
  } else if (status.broken) {
    kind = 'error';
    label = 'Backups off';
  } else if (cfg && status.last_error && (!last || status.last_error_at > last.at)) {
    kind = 'error';
    label = 'Last backup failed';
    facts.push(ago(status.last_error_at));
  } else if (cfg && (last || status.checked_at)) {
    kind = status.overdue ? 'error' : 'ok';
    label = status.overdue ? 'Backup overdue' : 'Backed up';
    facts.push(ago(Math.max(last ? last.at : 0, status.checked_at || 0)));
    if (last) facts.push(bytes(last.size), last.files + ' file' + (last.files === 1 ? '' : 's'));
  } else if (cfg) {
    kind = 'waiting';
    label = 'No backup yet';
  }
  if (cfg && !status.running && status.next_run) facts.push('next ' + inTime(status.next_run));
  const tip = [];
  if (cfg) {
    tip.push((cfg.endpoint || 'AWS S3') + ' · ' + cfg.bucket + '/' + (cfg.prefix || '').replace(/\/$/, ''));
    tip.push('This machine: ' + cfg.host_id);
  }
  if (last) tip.push('Last snapshot ' + last.id + ', ' + bytes(last.uploaded) + ' sent in ' + last.chunks + ' chunks');
  if (status.last_error) tip.push(status.last_error);
  renderState($('backupStatus'), { kind, label, state: status.running ? 'running' : kind, facts, tip, tipLabel: 'Backup details' });

  const alerts = [];
  const kept = status.kept || [];
  const keptTip = kept.map((k) => k.path + ': could not be copied since ' + new Date(k.since).toLocaleString());
  const gcTip = [status.gc_error || ''];
  const gcLabel = 'Why the bucket is not cleaned up';
  if (status.broken) alerts.push(backupAlert('error', status.warning, [status.broken], 'Why backup.json cannot be read'));
  else if (status.warning) {
    // What the warning is about decides what its "i" holds: the databases
    // kept as an earlier copy, or why the clean-up of the bucket stopped.
    const [tip, label] = status.warning_about === 'kept' ? [keptTip, 'Databases kept as an earlier copy']
      : status.warning_about === 'gc' ? [gcTip, gcLabel] : [[], 'Details'];
    alerts.push(backupAlert('warning', status.warning, tip, label));
  }
  if (kind === 'error' && status.last_error && !status.broken) {
    alerts.push(backupAlert('error', firstSentence(status.last_error), [status.last_error], 'What went wrong'));
  }
  if (kept.length && status.warning_about !== 'kept') {
    alerts.push(backupAlert('warning', (kept.length === 1 ? 'A database' : kept.length + ' databases')
      + ' could not be copied in the last run; the backup holds an earlier copy.', keptTip, 'Databases kept as an earlier copy'));
  }
  // A run that wrote its snapshot succeeded, so a clean-up that stopped would
  // otherwise leave the card at "Backed up" while the bucket grows.
  if (status.gc_error && status.warning_about !== 'gc') {
    alerts.push(backupAlert('warning', 'The bucket could not be cleaned up after the last backup: it keeps growing until it can be.',
      gcTip, gcLabel));
  }
  if (status.skipped && status.skipped.length) {
    alerts.push(backupAlert('warning', (status.skipped.length === 1 ? 'A database' : status.skipped.length + ' databases')
      + ' could not be copied and ' + (status.skipped.length === 1 ? 'is' : 'are') + ' not in the backup.',
    status.skipped, 'Databases not in the backup'));
  }
  if (status.withheld && status.withheld.length) {
    alerts.push(backupAlert('warning', 'Some files that may hold a secret could not be read as they should be, and are left out of the backup.',
      status.withheld, 'Files left out'));
  }
  if (status.other_hosts && status.other_hosts.length) {
    alerts.push(backupAlert('warning', 'Another machine backed up into this bucket in the last day. Each machine keeps snapshots of its own.',
      status.other_hosts, 'Other machines'));
  }
  morph($('backupDetail'), html`${alerts}`);

  $('backupRun').disabled = !cfg || status.running;
  $('backupTest').disabled = false;
  $('backupOff').hidden = !cfg && !status.broken;
  if (fillForm) {
    $('backupEndpoint').value = cfg ? cfg.endpoint || '' : '';
    $('backupRegion').value = cfg ? cfg.region || '' : '';
    $('backupBucket').value = cfg ? cfg.bucket || '' : '';
    $('backupPrefix').value = cfg ? (cfg.prefix || '').replace(/\/$/, '') : '';
    $('backupKey').value = cfg ? cfg.access_key_id || '' : '';
    $('backupSecret').value = '';
    $('backupPassphrase').value = '';
    $('backupSecret').placeholder = cfg && cfg.has_secret ? 'Unchanged' : '';
    $('backupPassphrase').placeholder = cfg && cfg.has_passphrase ? 'Unchanged' : '';
    $('backupInterval').value = cfg ? cfg.interval_minutes : 10;
    $('backupPathStyle').checked = cfg ? !!cfg.path_style : true;
  }

  // The list reads the bucket, so it is asked for when the card is first
  // drawn with a backup configured and after a run, not on every poll.
  if (cfg && ((backupWasRunning && !status.running) || !snapshotsLoaded)) loadSnapshots();
  backupWasRunning = !!status.running;
  clearTimeout(backupTimer);
  backupTimer = setTimeout(refreshBackup, status.running ? 2000 : 30000);
}

async function refreshBackup(fillForm = false) {
  try {
    renderBackup(await api('/api/backup'), fillForm);
  } catch (err) {
    if (!isOffline(err)) morph($('backupDetail'), backupAlert('error', errorMessage(err)));
    clearTimeout(backupTimer);
    backupTimer = setTimeout(refreshBackup, 10000);
  }
}

// snapshotRow is one snapshot: when, on which machine, how big. The date is
// the short one and may wrap, and "(this one)" has a line of its own: in full
// ("10/24/2026, 4:51:07 PM", kept from wrapping) the Size column ran past the
// card at 360px. A machine's name (an EC2 host is one long word) breaks
// anywhere for the same reason.
function snapshotRow(s) {
  const at = new Date(s.at);
  return html`<tr data-ds-key="${s.id}"><td title="${at.toLocaleString()}">${at.toLocaleString([], { dateStyle: 'short', timeStyle: 'short' })}</td><td class="so-break">${s.hostname || s.host}${s.own ? html`<span class="so-admin-own">(this one)</span>` : ''}</td>
    <td class="ds-num">${s.files ? bytes(s.size) : '–'}</td><td class="ds-num ds-col--secondary">${s.files || '–'}</td></tr>`;
}

// loadSnapshots lists what the bucket holds. It reads the bucket, so it is
// asked for when the page opens and after a run, not on every poll.
async function loadSnapshots() {
  snapshotsLoaded = true;
  try {
    const data = await api('/api/backup/snapshots', { attempts: 1, timeout: 120000 });
    const list = data.snapshots || [];
    $('backupSnapshots').hidden = !list.length;
    flipMorph($('backupSnapshotList'), html`${list.slice(0, 20).map(snapshotRow)}`, { items: 'tr', live: true });
    $('backupSnapshotsHint').textContent = !list.length ? 'The bucket holds no snapshot yet.'
      : list.length > 20 ? 'The newest 20 of ' + list.length + ' snapshots.' : list.length + (list.length === 1 ? ' snapshot.' : ' snapshots.');
  } catch (err) {
    if (err && err.status === 409) { $('backupSnapshotsHint').textContent = ''; return; }
    $('backupSnapshotsHint').textContent = 'The snapshots could not be listed: ' + firstSentence(errorMessage(err));
  }
  loadLegacy();
}

// legacyWhere is where in the bucket the mirror wrote, in words.
function legacyWhere(legacy) {
  return legacy.prefix ? 'under ' + legacy.prefix : 'at the top of the bucket';
}

// loadLegacy says what the bucket mirror of the build before left beside the
// backup (REBUILD.md §8.4): kept as it was until this machine has a snapshot
// in the backup, and offered for deletion from then on. It reads the bucket,
// so it goes with the snapshot list.
async function loadLegacy() {
  const host = $('backupLegacy');
  let legacy = null;
  try {
    legacy = (await api('/api/backup/legacy', { attempts: 1, timeout: 120000 })).legacy || null;
  } catch {
    // A bucket that cannot be listed says so in the snapshot list's line.
    legacy = null;
  }
  if (!legacy || !legacy.objects) {
    morph(host, '');
    return;
  }
  const what = legacy.objects + (legacy.objects === 1 ? ' object' : ' objects') + ' (' + bytes(legacy.bytes) + ')';
  const text = legacy.snapshot
    ? 'The bucket still holds the ' + what + ' the mirror of the build before wrote ' + legacyWhere(legacy)
      + '. This machine has a snapshot in the backup now, so they are not needed any more.'
    : 'The bucket still holds the ' + what + ' the mirror of the build before wrote ' + legacyWhere(legacy)
      + '. They stay until this machine has its first snapshot in the backup.';
  // The button goes under the sentence, as a limit card's does: beside it,
  // it left the sentence a column of 120px at 360.
  morph(host, html`<div class="ds-alert" role="status">${raw(icon('info', 20))}<span class="ds-alert__text so-admin-stack"><span>${text}</span>${
    legacy.snapshot ? html`<button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="delete-legacy" data-objects="${legacy.objects}" data-where="${legacyWhere(legacy)}">${faces('Delete them', 'Deleting')}</button>` : ''
  }</span></div>`);
}

// deleteLegacy deletes the mirror's objects, after asking: they are the old
// build's copy of this machine, and nothing brings them back.
async function deleteLegacy(button) {
  const objects = Number(button.dataset.objects) || 0;
  const yes = await confirmDialog({
    title: 'Delete the old mirror objects?',
    body: 'The ' + objects + (objects === 1 ? ' object' : ' objects') + ' the bucket mirror of the build before wrote '
      + button.dataset.where + ' are deleted: its meta.json, lease.json and logins.json, and its kv, sessions, '
      + 'drafts and costs objects. Nothing of the backup is touched, and its snapshots stay.',
    confirmLabel: 'Delete ' + objects + (objects === 1 ? ' object' : ' objects'),
    danger: true,
    opener: button,
  });
  if (!yes) return;
  try {
    const data = await M.busy(button, api('/api/backup/legacy', { method: 'DELETE', attempts: 1, timeout: 600000 }));
    toast('Deleted ' + data.deleted + (data.deleted === 1 ? ' old object.' : ' old objects.'), 'success');
  } catch (err) {
    toast(firstSentence(errorMessage(err)), 'error');
  }
  loadLegacy();
}

function backupBody() {
  return {
    endpoint: $('backupEndpoint').value.trim(),
    region: $('backupRegion').value.trim(),
    bucket: $('backupBucket').value.trim(),
    prefix: $('backupPrefix').value.trim(),
    access_key_id: $('backupKey').value.trim(),
    secret_access_key: $('backupSecret').value.trim(),
    passphrase: $('backupPassphrase').value,
    interval_minutes: Number($('backupInterval').value) || 0,
    path_style: $('backupPathStyle').checked,
  };
}

async function testBackup() {
  try {
    const data = await M.busy($('backupTest'), api('/api/backup/test', { method: 'POST', body: backupBody() }));
    const result = data.result || {};
    if (!result.ok) toast(firstSentence(result.error || 'The bucket did not answer.'), 'error');
    else toast(result.found ? 'The bucket answers and the passphrase opens its backup (' + result.latency_ms + ' ms)'
      : 'The bucket answers; there is no backup there yet (' + result.latency_ms + ' ms)', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function saveBackup() {
  const body = backupBody();
  if (body.interval_minutes && body.interval_minutes < 5) {
    toast('A backup runs at most every 5 minutes.', 'error');
    return;
  }
  try {
    const data = await M.busy($('backupSave'), api('/api/backup/config', { method: 'PUT', body }));
    toast('Backup settings saved', 'success');
    if (data.status) renderBackup(data.status, true);
    loadSnapshots();
  } catch (err) {
    toast(firstSentence(errorMessage(err)), 'error');
  }
}

// turnBackupOff removes backup.json. The bucket keeps what it holds, and
// saving the settings again turns backups back on.
async function turnBackupOff() {
  const yes = await confirmDialog({
    title: 'Turn off backups?',
    body: 'Nothing more is backed up from this machine. The snapshots in the bucket stay there, and saving these settings again turns backups back on.',
    confirmLabel: 'Turn off backups',
    danger: true,
    opener: $('backupOff'),
  });
  if (!yes) return;
  try {
    const data = await api('/api/backup/config', { method: 'DELETE' });
    toast('Backups are off.');
    $('backupSnapshots').hidden = true;
    $('backupSnapshotsHint').textContent = '';
    morph($('backupLegacy'), '');
    snapshotsLoaded = false;
    if (data.status) renderBackup(data.status, true);
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function runBackup() {
  try {
    const data = await M.busy($('backupRun'), api('/api/backup/run', { method: 'POST', body: {} }));
    if (data.status) renderBackup(data.status);
  } catch (err) {
    toast(errorMessage(err), 'error');
    refreshBackup();
  }
}

/* ------------------------------------------------------------ side tasks */

// The marks beside a diagnostics row: a check that names a program is drawn
// with that program's mark, so the cards above and this list read as one
// thing.
const CHECK_MARKS = {
  'Claude Code': 'claude', 'Claude Code state': 'claude',
  Codex: 'codex', 'Codex state': 'codex',
  OpenCode: 'opencode', 'OpenCode state': 'opencode',
  Shell: 'shell',
};

function checkRow(check) {
  const mark = CHECK_MARKS[check.name] || '';
  // §E.10 rule 3: a version, a path, a socket or an error message is
  // technical state, so it is behind the row's "i" and never in the line
  // itself. What is visible is the verdict, in words.
  const tip = el('td', { class: 'so-check__tip' });
  // A detail is split into the bubble's lines at its " · " or at a newline,
  // which is how a row with a list in it - the updates - says one item per
  // line.
  if (check.detail) tip.append(infoTip(String(check.detail).split(/\n| · /), { label: check.name + ' details' }));
  return el('tr', { class: 'so-check', 'data-ok': String(!!check.ok) },
    el('th', { scope: 'row', class: 'ds-col--text' },
      el('span', { class: 'so-cell-name' }, mark ? agentMark(mark, 16) : null,
        el('span', { class: 'so-check__label', text: check.name })),
      el('span', { class: 'ds-meta so-cell-more so-check__summary', text: check.summary || (check.ok ? 'ok' : 'not ready') })),
    el('td', {}, check.ok ? chipEl('ok', 'OK') : chipEl('error', 'Not ready', 'failed')),
    tip);
}

async function runChecks() {
  try {
    const data = await M.busy($('runChecks'), api('/api/diagnostics', { method: 'POST', body: {} }));
    const checks = data.checks || [];
    $('checks').replaceChildren(...checks.map(checkRow));
    $('checksTable').hidden = !checks.length;
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function changePassword() {
  const current = $('pwCurrent').value;
  const next = $('pwNext').value;
  if (!current || !next) {
    toast('Fill in both password fields.', 'error');
    return;
  }
  try {
    await M.busy($('changePw'), api('/api/settings/password', { method: 'POST', body: { current, next } }));
    $('pwCurrent').value = '';
    $('pwNext').value = '';
    toast('Password changed', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}
