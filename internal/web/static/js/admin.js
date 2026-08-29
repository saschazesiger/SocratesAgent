// The admin dashboard: everything about Socrates is configurable here.

import { api, el, toast } from './api.js';
import { speak } from './voice.js';

const $ = (id) => document.getElementById(id);

let settings = null;
let defaults = null;
let kinds = ['claude', 'codex', 'opencode', 'custom'];

const FIELDS = [
  ['orKey', 'openrouter.api_key'],
  ['orBase', 'openrouter.base_url'],
  ['orChat', 'openrouter.chat_model'],
  ['orTranscribe', 'openrouter.transcribe_model'],
  ['orTitle', 'openrouter.title_model'],
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
];

const APPROVALS = [
  ['auto', 'Run unattended (auto approve)'],
  ['ask', 'Ask me in the web interface'],
];
const SANDBOXES = [
  ['workspace-write', 'workspace-write'],
  ['read-only', 'read-only'],
  ['danger-full-access', 'danger-full-access'],
];

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
  if (Array.isArray(data.kinds) && data.kinds.length) kinds = data.kinds;
  $('versionLabel').textContent = 'Socrates ' + (data.version || '');
  fillForm();
  renderAgents();
  bind();
  if (new URLSearchParams(location.search).get('welcome')) {
    showNotice('Welcome. Add your OpenRouter key, check the agents below, then head back to the chat.', 'ok');
  }
}

function fillForm() {
  for (const [id, path, type] of FIELDS) {
    const node = $(id);
    if (!node) continue;
    const value = getPath(settings, path);
    if (type === 'bool') node.checked = !!value;
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
    else setPath(next, path, node.value);
  }
  return next;
}

function bind() {
  $('saveTop').addEventListener('click', save);
  $('saveBottom').addEventListener('click', save);
  $('addAgent').addEventListener('click', addAgent);
  $('loadModels').addEventListener('click', loadModels);
  $('runChecks').addEventListener('click', runChecks);
  $('changePw').addEventListener('click', changePassword);
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
  next.backends = settings.backends;
  try {
    const data = await api('/api/settings', { method: 'PUT', body: { settings: next } });
    settings = data.settings;
    fillForm();
    renderAgents();
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

/* ---------------------------------------------------------------- agents */

function slug(text) {
  return String(text || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
}

function addAgent() {
  settings.backends.push({
    id: 'agent-' + (settings.backends.length + 1),
    kind: 'custom',
    name: 'New agent',
    enabled: false,
    description: 'Describe when Socrates should use this agent.',
    command: '',
    extra_args: [],
    model: '',
    approval: 'auto',
    sandbox: 'workspace-write',
    timeout_seconds: 1800,
  });
  renderAgents();
}

function renderAgents() {
  const host = $('agents');
  host.innerHTML = '';
  settings.backends.forEach((backend, index) => host.append(agentCard(backend, index)));
}

function field(label, control, hintText) {
  return el('div', { class: 'field' },
    el('label', { text: label }),
    control,
    hintText ? el('div', { class: 'hint', text: hintText }) : null);
}

function select(value, options, onChange) {
  const node = el('select', { class: 'select', onchange: (event) => onChange(event.target.value) });
  for (const [optionValue, optionLabel] of options) {
    node.append(el('option', { value: optionValue, selected: optionValue === value }, optionLabel));
  }
  node.value = value;
  return node;
}

function text(value, onInput, attrs = {}) {
  return el('input', Object.assign({
    class: 'input mono', type: 'text', value: value ?? '',
    oninput: (event) => onInput(event.target.value),
  }, attrs));
}

function agentCard(backend, index) {
  const card = el('div', { class: 'agent' });
  const titleNode = el('span', { class: 'nm', text: backend.name || backend.id });
  const kindNode = el('span', { class: 'kind', text: backend.kind });

  const head = el('div', { class: 'agent-head' },
    el('label', { class: 'switch', onclick: (event) => event.stopPropagation() },
      el('input', {
        type: 'checkbox', checked: backend.enabled,
        onchange: (event) => { backend.enabled = event.target.checked; },
      }),
      el('span', { class: 'track' })),
    titleNode,
    kindNode,
    el('span', { class: 'spacer' }),
    el('button', {
      class: 'btn sm danger', type: 'button', text: 'Remove',
      onclick: (event) => {
        event.stopPropagation();
        settings.backends.splice(index, 1);
        renderAgents();
      },
    }),
  );
  head.addEventListener('click', (event) => {
    if (event.target.closest('button') || event.target.closest('.switch')) return;
    card.classList.toggle('open');
  });

  const body = el('div', { class: 'agent-body' });
  body.append(
    el('div', { class: 'grid-2' },
      field('Display name', text(backend.name, (value) => {
        backend.name = value;
        titleNode.textContent = value || backend.id;
      }, { class: 'input' })),
      field('Identifier', text(backend.id, (value) => { backend.id = slug(value); },
        { placeholder: 'codex' }), 'Used by the orchestrator to select this agent.'),
    ),
    field('When should Socrates use it?',
      el('textarea', {
        class: 'textarea', rows: '3', value: backend.description,
        oninput: (event) => { backend.description = event.target.value; },
      }),
      'This text goes straight into the tool description the model reads.'),
    el('div', { class: 'grid-2' },
      field('Type', select(backend.kind, kinds.map((k) => [k, k]), (value) => {
        backend.kind = value;
        kindNode.textContent = value;
        renderAgents();
      }), 'Decides how the output stream is parsed.'),
      field('Command', text(backend.command, (value) => { backend.command = value; },
        { placeholder: 'claude' }), 'Binary name or absolute path.'),
      field('Model override', text(backend.model, (value) => { backend.model = value; },
        { placeholder: 'leave empty for the agent default' })),
      field('Extra arguments', text((backend.extra_args || []).join(' '),
        (value) => { backend.extra_args = splitArgs(value); },
        { placeholder: '--add-dir /srv' }),
        backend.kind === 'custom' ? 'Use {{prompt}} where the task text should go, otherwise it is piped to stdin.' : 'Appended to the generated command line.'),
      field('Permissions', select(backend.approval || 'auto', APPROVALS, (value) => { backend.approval = value; }),
        backend.kind === 'claude'
          ? 'Ask mode routes every tool call through the web interface.'
          : 'Ask mode falls back to a restrictive sandbox for this agent.'),
      backend.kind === 'codex'
        ? field('Sandbox', select(backend.sandbox || 'workspace-write', SANDBOXES, (value) => { backend.sandbox = value; }))
        : field('Timeout (seconds)', el('input', {
            class: 'input', type: 'number', min: '30', value: backend.timeout_seconds,
            oninput: (event) => { backend.timeout_seconds = Number(event.target.value) || 1800; },
          })),
    ),
  );
  if (backend.kind === 'codex') {
    body.append(field('Timeout (seconds)', el('input', {
      class: 'input', type: 'number', min: '30', value: backend.timeout_seconds,
      oninput: (event) => { backend.timeout_seconds = Number(event.target.value) || 1800; },
    })));
  }

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

/* ------------------------------------------------------------ side tasks */

async function loadModels() {
  const button = $('loadModels');
  button.disabled = true;
  button.textContent = 'Loading…';
  try {
    const data = await api('/api/models');
    const list = $('modelList');
    list.innerHTML = '';
    for (const model of data.models || []) {
      list.append(el('option', { value: model.id }, model.name || model.id));
    }
    toast((data.models || []).length + ' models loaded');
  } catch (err) {
    toast(err.message, 'error');
  } finally {
    button.disabled = false;
    button.textContent = 'Load model list';
  }
}

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
