// The admin dashboard: everything about Socrates is configurable here.

import { api, el, toast, isOffline, errorMessage, setClass, onWake } from './api.js';
import { speak, speechKind } from './voice.js';
import { combobox } from './combobox.js';
import * as models from './models.js';

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

// Everything that names a model is picked from the OpenRouter catalogue with a
// searchable dropdown rather than typed into a text field, so none of them are
// in FIELDS.
const MODEL_PICKERS = [
  ['orTranscribe', 'openrouter.transcribe_model', () => models.audio()],
  ['orTitle', 'openrouter.title_model', () => models.chat()],
];

const FIELDS = [
  ['orKey', 'openrouter.api_key'],
  ['workspaceRoot', 'agent.workspace_root'],
  ['voiceLanguage', 'voice.language'],
  ['sttPrompt', 'voice.stt_prompt'],
  ['ttsRate', 'voice.tts_rate', 'number'],
  ['speakAuto', 'voice.speak_in_auto_mode', 'bool'],
  ['speakChat', 'voice.speak_in_chat_mode', 'bool'],
  ['tunnelMode', 'tunnel.mode'],
  ['tunnelToken', 'tunnel.token'],
  ['tunnelHostname', 'tunnel.hostname'],
  ['tunnelCommand', 'tunnel.command'],
  ['tunnelArgs', 'tunnel.extra_args', 'args'],
];

let tunnelTimer = null;
let voiceTimer = null;
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
  bind();
  loadModels();
  if (data.local_url) localURL = data.local_url;
  refreshTunnel();
  refreshVoice();
  if (new URLSearchParams(location.search).get('welcome')) {
    showNotice('Welcome. Add your OpenRouter key, check the agents below, then head back to the chat.', 'ok');
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
}

function collect() {
  const next = JSON.parse(JSON.stringify(settings));
  for (const [id, path, type] of FIELDS) {
    const node = $(id);
    if (!node) continue;
    if (type === 'bool') setPath(next, path, node.checked);
    else if (type === 'number') setPath(next, path, Number(node.value) || 0);
    else if (type === 'args') setPath(next, path, splitArgs(node.value));
    else setPath(next, path, node.value);
  }
  return next;
}

/* ---------------------------------------------------------- model pickers */

const pickers = {};

// buildModelPickers fills the empty slots the page leaves for them. They are
// built before the catalogue arrives, so the dashboard is usable straight away
// and simply gains its list a moment later.
function buildModelPickers() {
  for (const [id, path, items] of MODEL_PICKERS) {
    const host = $(id);
    if (!host) continue;
    host.innerHTML = '';
    const picker = combobox({
      value: getPath(settings, path) || '',
      items,
      placeholder: 'anthropic/claude-sonnet-4.5',
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

async function loadModels() {
  const hint = $('modelsHint');
  try {
    await models.load();
    if (hint) hint.textContent = models.count() + ' models loaded from OpenRouter. Type to search, or enter any id by hand.';
  } catch (err) {
    if (hint) {
      hint.textContent = 'Could not load the model list (' + err.message +
        '). The fields still accept a model id typed by hand.';
    }
  }
}

function bind() {
  $('saveTop').addEventListener('click', save);
  $('saveBottom').addEventListener('click', save);
  $('runChecks').addEventListener('click', runChecks);
  $('changePw').addEventListener('click', changePassword);
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
  $('testVoice').addEventListener('click', async () => {
    // The server picks the voice from the language it has stored, so a
    // language switched here and not saved yet would be read by the old voice.
    // Saying so is better than sounding wrong.
    const saved = getPath(settings, 'voice.language') || 'en';
    if ($('voiceLanguage').value !== saved) {
      toast('Save first — the voice follows the language the server has stored.');
      return;
    }
    const sample = saved === 'de'
      ? 'So klingt Socrates, wenn dir eine Antwort im Freisprechmodus vorgelesen wird.'
      : 'This is how Socrates will read answers to you in audio mode.';
    const button = $('testVoice');
    busyButton(button, true, 'Reading…');
    try {
      await speak(sample);
    } catch (err) {
      // Every press answers, whether or not this reason has been seen before:
      // a button that stays silent the second time reads as a button that did
      // nothing at all. A voice that is still downloading itself is not one of
      // the failures, so it is not shown as one.
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
    refreshTunnel();
    hint('Saved');
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

const VOICE_LABEL = {
  ready: 'Voice ready',
  installing: 'Installing the voice…',
  missing: 'Voice not installed',
  failed: 'Voice failed',
};

// The same line as the tunnel below, down to the dot, because it is the same
// kind of thing: something that downloads itself once and then simply works.
// Everything after the dot is redrawn on every poll and the dot itself stays
// put - one that is taken out of the page and put back every two seconds never
// gets far enough into its animation to look like it is pulsing.
function renderVoiceStatus(voice) {
  const host = $('voiceStatus');
  let dot = host.querySelector(':scope > .state-dot');
  if (!dot) {
    dot = el('span', { class: 'state-dot' });
    host.prepend(dot);
  }
  const state = voice.state || 'missing';
  for (const name of Object.keys(VOICE_LABEL)) setClass(dot, name, state === name);
  while (dot.nextSibling) dot.nextSibling.remove();

  const parts = [el('span', { class: 'state-label', text: VOICE_LABEL[state] || state })];
  // The detail is where the download says how far it has got, so it is the
  // part that has to change between two polls for the card to look alive.
  if (voice.detail) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', { class: 'detail', text: voice.detail }));
  }
  if (voice.error) {
    parts.push(el('span', { class: 'sep', text: '·' }));
    parts.push(el('span', { style: 'color:var(--red)', text: voice.error }));
  }
  host.append(...parts);

  clearTimeout(voiceTimer);
  // A voice that is ready stays ready, and one whose install failed will not
  // repair itself while the page is open. Anything in between is still moving.
  if (state !== 'ready' && state !== 'failed') voiceTimer = setTimeout(refreshVoice, 2000);
}

async function refreshVoice() {
  try {
    const data = await api('/api/voice/status');
    renderVoiceStatus((data && data.voice) || {});
  } catch (err) {
    clearTimeout(voiceTimer);
    // Like the tunnel: a poll that never arrived is the connection bar's
    // business rather than a toast, but it must not stop the polling either.
    if (isOffline(err)) {
      voiceTimer = setTimeout(refreshVoice, 5000);
      return;
    }
    toast(errorMessage(err), 'error');
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

// The status line is polled every three seconds. Everything after the dot is
// redrawn, but the dot itself stays put: one that is taken out of the page and
// put back that often never gets far enough into its animation to look like it
// is pulsing - it just blinks.
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
  refreshVoice();
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

async function runChecks() {
  const button = $('runChecks');
  const host = $('checks');
  button.disabled = true;
  button.textContent = 'Checking…';
  host.innerHTML = '';
  try {
    const data = await api('/api/diagnostics', { method: 'POST', body: {} });
    for (const check of data.checks || []) {
      host.append(el('div', { class: 'check' },
        el('span', { class: 'st ' + (check.ok ? 'ok' : 'bad') }),
        el('span', { class: 'nm', text: check.name }),
        el('span', { class: 'dt', text: check.detail }),
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
