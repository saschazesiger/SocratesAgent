// The admin dashboard: everything about Socrates is configurable here.

import { api, el, toast } from './api.js';
import { speak } from './voice.js';
import { combobox } from './combobox.js';
import * as models from './models.js';

const $ = (id) => document.getElementById(id);

let settings = null;
let defaults = null;

// The three OpenRouter models are picked with a searchable dropdown rather
// than a text field, so they are not in FIELDS.
const MODEL_PICKERS = [
  ['orChat', 'openrouter.chat_model', () => models.all()],
  ['orTranscribe', 'openrouter.transcribe_model', () => models.audio()],
  ['orTitle', 'openrouter.title_model', () => models.all()],
];

const FIELDS = [
  ['orKey', 'openrouter.api_key'],
  ['orBase', 'openrouter.base_url'],
  ['systemPrompt', 'agent.system_prompt'],
  ['maxIterations', 'agent.max_iterations', 'number'],
  ['temperature', 'agent.temperature', 'number'],
  ['workspaceRoot', 'agent.workspace_root'],
  ['sttProvider', 'voice.stt_provider'],
  ['sttBase', 'voice.stt_base_url'],
  ['sttKey', 'voice.stt_api_key'],
  ['sttModel', 'voice.stt_model'],
  ['sttPrompt', 'voice.stt_prompt'],
  ['ttsProvider', 'voice.tts_provider'],
  ['ttsBase', 'voice.tts_base_url'],
  ['ttsKey', 'voice.tts_api_key'],
  ['ttsModel', 'voice.tts_model'],
  ['ttsVoice', 'voice.tts_voice'],
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

load().catch((err) => toast(err.message, 'error'));

async function load() {
  const data = await api('/api/settings');
  settings = data.settings;
  defaults = data.defaults;
  $('versionLabel').textContent = 'Socrates ' + (data.version || '');
  fillForm();
  buildModelPickers();
  renderAgents();
  bind();
  loadModels();
  if (data.local_url) localURL = data.local_url;
  refreshTunnel();
  if (new URLSearchParams(location.search).get('welcome')) {
    showNotice('Welcome. Add your OpenRouter key, check the tools below, then head back to the chat.', 'ok');
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

// buildModelPickers replaces the three model text fields with dropdowns. They
// are built before the catalogue arrives, so the dashboard is usable straight
// away and simply gains its list a moment later.
function buildModelPickers() {
  for (const [id, path, items] of MODEL_PICKERS) {
    const host = $(id);
    if (!host) continue;
    host.innerHTML = '';
    const picker = combobox({
      value: getPath(settings, path) || '',
      items,
      placeholder: 'anthropic/claude-sonnet-4.5',
      onChange: (value) => { setPath(settings, path, value); },
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
  for (const picker of agentModelPickers) {
    picker.refresh();
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
  $('addAgent').addEventListener('click', addAgent);
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
  $('testVoice').addEventListener('click', () => {
    speak('This is how Socrates will read answers to you in auto mode.').catch((err) => toast(err.message, 'error'));
  });
  $('resetPrompt').addEventListener('click', () => {
    $('systemPrompt').value = defaults.agent.system_prompt;
    toast('Default prompt restored. Do not forget to save.');
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
  next.tools = settings.tools;
  try {
    const data = await api('/api/settings', { method: 'PUT', body: { settings: next } });
    settings = data.settings;
    fillForm();
    syncModelPickers();
    renderAgents();
    refreshTunnel();
    hint('Saved');
    toast('Settings saved');
  } catch (err) {
    toast(err.message, 'error');
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

/* ----------------------------------------------------------------- tools */

function slug(text) {
  return String(text || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
}

// agentModelPickers holds the per tool dropdowns so they can be refreshed when
// the catalogue finishes loading.
let agentModelPickers = [];

function addAgent() {
  settings.tools.push({
    id: 'tool-' + (settings.tools.length + 1),
    name: 'New tool',
    enabled: false,
    description: 'Describe when Socrates should reach for this program.',
    command: '',
    args: [],
    model: '',
    model_flag: '',
    skip_permissions: false,
    skip_permission_args: [],
    ask_permission_args: [],
    driving: 'How do you hand it a task, how do you know it has finished, which keys answer its questions?',
    ready_pattern: '',
    idle_seconds: 5,
    timeout_seconds: 1800,
  });
  renderAgents();
}

function renderAgents() {
  const host = $('agents');
  host.innerHTML = '';
  agentModelPickers = [];
  settings.tools.forEach((tool, index) => host.append(agentCard(tool, index)));
}

function field(label, control, hintText) {
  return el('div', { class: 'field' },
    el('label', { text: label }),
    control,
    hintText ? el('div', { class: 'hint', text: hintText }) : null);
}

function text(value, onInput, attrs = {}) {
  return el('input', Object.assign({
    class: 'input mono', type: 'text', value: value ?? '',
    oninput: (event) => onInput(event.target.value),
  }, attrs));
}

function number(value, onInput, attrs = {}) {
  return el('input', Object.assign({
    class: 'input', type: 'number', value: value ?? 0,
    oninput: (event) => onInput(Number(event.target.value) || 0),
  }, attrs));
}

function toggle(checked, onChange, label) {
  return el('label', { class: 'switch' },
    el('input', { type: 'checkbox', checked, onchange: (event) => onChange(event.target.checked) }),
    el('span', { class: 'track' }),
    el('span', { text: label }));
}

// modelField is the same searchable dropdown as the OpenRouter models, except
// that a tool's model name belongs to that program - Claude Code wants
// "sonnet", Codex wants "gpt-5-codex" - so anything typed is kept as is.
function modelField(tool) {
  const picker = combobox({
    value: tool.model || '',
    items: () => models.all(),
    placeholder: 'leave empty for the program default',
    onChange: (value) => { tool.model = value; },
  });
  agentModelPickers.push({ refresh: () => picker.setValue(tool.model || '', false) });
  return picker.node;
}

function agentCard(tool, index) {
  const card = el('div', { class: 'agent' });
  const titleNode = el('span', { class: 'nm', text: tool.name || tool.id });
  const modeNode = el('span', {
    class: 'kind',
    text: tool.skip_permissions ? 'skips permissions' : 'asks permission',
  });

  const head = el('div', { class: 'agent-head' },
    el('label', { class: 'switch', onclick: (event) => event.stopPropagation() },
      el('input', {
        type: 'checkbox', checked: tool.enabled,
        onchange: (event) => { tool.enabled = event.target.checked; },
      }),
      el('span', { class: 'track' })),
    titleNode,
    modeNode,
    el('span', { class: 'spacer' }),
    el('button', {
      class: 'btn sm danger', type: 'button', text: 'Remove',
      onclick: (event) => {
        event.stopPropagation();
        settings.tools.splice(index, 1);
        renderAgents();
      },
    }),
  );
  head.addEventListener('click', (event) => {
    if (event.target.closest('button') || event.target.closest('.switch')) return;
    card.classList.toggle('open');
  });

  const skipArgsField = field('Arguments that skip permissions',
    text((tool.skip_permission_args || []).join(' '),
      (value) => { tool.skip_permission_args = splitArgs(value); },
      { placeholder: '--dangerously-skip-permissions' }),
    'Added when the switch above is on.');

  const askArgsField = field('Arguments that keep permissions on',
    text((tool.ask_permission_args || []).join(' '),
      (value) => { tool.ask_permission_args = splitArgs(value); },
      { placeholder: '--ask-for-approval on-request' }),
    'Added when the switch above is off. Socrates then answers the prompts on screen itself.');

  const body = el('div', { class: 'agent-body' },
    el('div', { class: 'grid-2' },
      field('Display name', text(tool.name, (value) => {
        tool.name = value;
        titleNode.textContent = value || tool.id;
      }, { class: 'input' })),
      field('Identifier', text(tool.id, (value) => { tool.id = slug(value); },
        { placeholder: 'codex' }), 'How Socrates refers to this tool.'),
    ),
    field('When should Socrates use it?',
      el('textarea', {
        class: 'textarea', rows: '3', value: tool.description,
        oninput: (event) => { tool.description = event.target.value; },
      }),
      'Goes straight into the system prompt, so write it the way you would explain it to a colleague.'),
    el('div', { class: 'grid-2' },
      field('Command', text(tool.command, (value) => { tool.command = value; },
        { placeholder: 'claude' }), 'Binary name or absolute path.'),
      field('Arguments', text((tool.args || []).join(' '),
        (value) => { tool.args = splitArgs(value); },
        { placeholder: '--no-alt-screen' }), 'Always passed, before everything else.'),
      field('Model', modelField(tool),
        'The program\u2019s own model name. Pick one from the list or type anything, for example "sonnet".'),
      field('Model argument', text(tool.model_flag, (value) => { tool.model_flag = value; },
        { placeholder: '--model' }), 'The flag the model name follows. Leave empty to never pass one.'),
    ),
    el('div', { class: 'row', style: 'margin: 4px 0 18px' },
      toggle(!!tool.skip_permissions, (checked) => {
        tool.skip_permissions = checked;
        modeNode.textContent = checked ? 'skips permissions' : 'asks permission';
        skipArgsField.hidden = !checked;
        askArgsField.hidden = checked;
      }, 'Skip permission prompts (run unattended)')),
    el('div', { class: 'hint', style: 'margin: -14px 0 16px' },
      'On, the program is started in its own unattended mode and never stops to ask. ' +
      'Off, it asks before it acts and Socrates answers the prompts on screen the way you would.'),
    skipArgsField,
    askArgsField,
    field('How to drive it',
      el('textarea', {
        class: 'textarea', rows: '4', value: tool.driving || '',
        oninput: (event) => { tool.driving = event.target.value; },
      }),
      'How to hand it a task, how to tell that it has finished, which keys answer its questions. ' +
      'This is what makes a new program usable without changing any code.'),
    el('div', { class: 'grid-2' },
      field('Ready when the screen matches', text(tool.ready_pattern, (value) => { tool.ready_pattern = value; },
        { placeholder: 'leave empty to wait for it to go quiet' }),
        'Regular expression. Only needed if waiting for silence starts typing too early.'),
      field('Counts as finished after (seconds)',
        number(tool.idle_seconds, (value) => { tool.idle_seconds = value; }, { min: '1', max: '300' }),
        'How long the program must print nothing before its turn is treated as over.'),
      field('Timeout (seconds)',
        number(tool.timeout_seconds, (value) => { tool.timeout_seconds = value; }, { min: '30' })),
      field('Window size', el('div', { class: 'row', style: 'gap:8px' },
        number(tool.cols || 0, (value) => { tool.cols = value; }, { min: '0', max: '400', placeholder: 'cols' }),
        number(tool.rows || 0, (value) => { tool.rows = value; }, { min: '0', max: '200', placeholder: 'rows' })),
        'Columns and rows the program is given. Zero for the default.'),
    ),
  );

  skipArgsField.hidden = !tool.skip_permissions;
  askArgsField.hidden = !!tool.skip_permissions;

  card.append(head, body);
  return card;
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
  host.innerHTML = '';
  const parts = [
    el('span', { class: 'state-dot ' + status.state }),
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
    toast(err.message, 'error');
  }
}

async function startTunnel() {
  const button = $('tunnelStart');
  button.disabled = true;
  try {
    const data = await api('/api/tunnel/start', { method: 'POST', body: { tunnel: tunnelForm() } });
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel starting');
    setTimeout(refreshTunnel, 1200);
  } catch (err) {
    toast(err.message, 'error');
  } finally {
    button.disabled = false;
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
    toast(err.message, 'error');
  } finally {
    button.disabled = false;
    button.textContent = 'Download cloudflared';
    refreshTunnel();
  }
}

async function stopTunnel() {
  try {
    const data = await api('/api/tunnel/stop', { method: 'POST', body: {} });
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel stopped');
  } catch (err) {
    toast(err.message, 'error');
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
    toast(err.message, 'error');
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
  try {
    await api('/api/settings/password', { method: 'POST', body: { current, next } });
    $('pwCurrent').value = '';
    $('pwNext').value = '';
    toast('Password changed');
  } catch (err) {
    toast(err.message, 'error');
  }
}
