// The admin dashboard: everything about Socrates is configurable here.

import { api, el, toast, confirmDialog, isOffline, errorMessage, setClass, onWake } from './api.js';
import { speak } from './voice.js';
import { combobox } from './combobox.js';
import * as models from './models.js';

const $ = (id) => document.getElementById(id);

let settings = null;
let defaults = null;
let presets = [];

// The three OpenRouter models are picked with a searchable dropdown rather
// than a text field, so they are not in FIELDS.
const MODEL_PICKERS = [
  ['orChat', 'openrouter.chat_model', () => models.all()],
  ['orTranscribe', 'openrouter.transcribe_model', () => models.audio()],
  ['orTitle', 'openrouter.title_model', () => models.all()],
  ['netSearchModel', 'internet.search_model', () => models.all()],
];

const FIELDS = [
  ['orKey', 'openrouter.api_key'],
  ['orBase', 'openrouter.base_url'],
  ['systemPrompt', 'agent.system_prompt'],
  ['maxIterations', 'agent.max_iterations', 'number'],
  ['temperature', 'agent.temperature', 'number'],
  ['workspaceRoot', 'agent.workspace_root'],
  ['voiceLanguage', 'voice.language'],
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
  ['netEnabled', 'internet.enabled', 'bool'],
  ['netProvider', 'internet.search_provider'],
  ['netTavilyKey', 'internet.tavily_api_key'],
  ['netJinaKey', 'internet.jina_api_key'],
  ['netMaxResults', 'internet.max_results', 'number'],
  ['netFetchEngine', 'internet.fetch_engine'],
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
  presets = data.presets || [];
  $('versionLabel').textContent = 'Socrates ' + (data.version || '');
  fillForm();
  buildModelPickers();
  renderSkills();
  renderInternet();
  bind();
  loadModels();
  if (data.local_url) localURL = data.local_url;
  refreshTunnel();
  if (new URLSearchParams(location.search).get('welcome')) {
    showNotice('Welcome. Add your OpenRouter key, check the skills below, then head back to the chat.', 'ok');
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
  for (const picker of skillModelPickers) {
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
  $('addSkill').addEventListener('click', addSkill);
  $('addPreset').addEventListener('click', addFromPreset);
  $('runChecks').addEventListener('click', runChecks);
  $('changePw').addEventListener('click', changePassword);
  $('tunnelStart').addEventListener('click', startTunnel);
  $('tunnelStop').addEventListener('click', stopTunnel);
  $('tunnelMode').addEventListener('change', renderTunnelMode);
  $('netEnabled').addEventListener('change', renderInternet);
  $('netProvider').addEventListener('change', renderInternet);
  $('tunnelInstall').addEventListener('click', installCloudflared);
  $('tunnelLogToggle').addEventListener('click', () => {
    const log = $('tunnelLog');
    log.hidden = !log.hidden;
    $('tunnelLogToggle').textContent = log.hidden ? 'Show log' : 'Hide log';
    if (!log.hidden) refreshTunnel();
  });
  $('testVoice').addEventListener('click', () => {
    // The sample is read in the language the form currently shows, not the one
    // that was last saved, so the button answers the question you are actually
    // asking: does this setting sound right?
    //
    // Only the browser voice follows it before a save - a configured endpoint
    // renders with the language the server has stored.
    const language = $('voiceLanguage').value;
    const sample = language === 'de'
      ? 'So klingt Socrates, wenn dir eine Antwort im Freisprechmodus vorgelesen wird.'
      : 'This is how Socrates will read answers to you in auto mode.';
    speak(sample, { lang: language }).catch((err) => toast(err.message, 'error'));
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
  next.skills = settings.skills;
  try {
    // Settings are written whole, so the same body sent twice leaves exactly
    // the same result - which makes this safe to repeat over a bad link.
    const data = await api('/api/settings', { method: 'PUT', attempts: 4, body: { settings: next } });
    settings = data.settings;
    fillForm();
    syncModelPickers();
    renderSkills();
    renderInternet();
    refreshTunnel();
    hint('Saved');
    toast('Settings saved');
  } catch (err) {
    toast(errorMessage(err), 'error');
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

/* -------------------------------------------------------------- internet */

// Every provider needs a different field, so the card only shows the one that
// belongs to the choice in the dropdown. Nothing is hidden that is in use.
const PROVIDER_HINTS = {
  openrouter: 'No second account needed — the search runs on your OpenRouter key and OpenRouter bills it per search, on top of the tokens.',
  tavily: 'A search API built for agents. Returns a short answer alongside the results.',
  jina: 'Works without a key at a low rate limit; a key raises it.',
};

function renderInternet() {
  const on = $('netEnabled').checked;
  const provider = $('netProvider').value || 'openrouter';
  $('netBody').hidden = !on;
  $('netProviderHint').textContent = PROVIDER_HINTS[provider] || '';
  $('netTavilyField').hidden = provider !== 'tavily';
  $('netJinaField').hidden = provider !== 'jina';
  $('netSearchModelField').hidden = provider !== 'openrouter';
}

/* ---------------------------------------------------------------- skills */

function slug(text) {
  return String(text || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
}

// skillModelPickers holds the per skill dropdowns so they can be refreshed
// when the model catalogue finishes loading.
let skillModelPickers = [];

// The manual: the sections that tell Socrates how to work this program. They
// are ordinary prose, which is what makes a new program usable without any
// code change.
const MANUAL = [
  ['startup', 'Starting it',
    'The screens right after launch and the exact keys that get past them — trust dialogs, update notices, login — and what "ready" looks like.'],
  ['giving_tasks', 'Giving it a task',
    'Where to type, how to submit, how to write more than one line, slash commands worth knowing.'],
  ['reading_state', 'Reading its state',
    'How to tell working from idle from waiting for an answer. Name the phrases or patterns to look for.'],
  ['answering', 'Answering its questions',
    'Every dialog it can show and the exact keys that answer it.'],
  ['exiting', 'Interrupting and quitting',
    'How to stop what it is doing, and how to leave cleanly.'],
  ['notes', 'Notes',
    'Pitfalls, version quirks, anything that did not fit above.'],
];

function blankSkill() {
  return {
    id: 'skill-' + (settings.skills.length + 1),
    name: 'New skill',
    enabled: false,
    preset: '',
    description: 'Describe when Socrates should reach for this program.',
    command: '',
    args: [],
    env: [],
    model: '',
    model_flag: '',
    skip_permissions: false,
    skip_permission_args: [],
    ask_permission_args: [],
    startup: '',
    giving_tasks: '',
    reading_state: '',
    answering: '',
    exiting: '',
    notes: '',
    interactive_only: true,
    headless_forms: '',
    headless_usage: '',
    ready_pattern: '',
    busy_pattern: '',
    idle_seconds: 5,
    timeout_seconds: 1800,
  };
}

function addSkill() {
  settings.skills.push(blankSkill());
  renderSkills();
}

// missingPresets are the shipped skills this installation does not have. An
// installation that was set up before a preset existed never receives it
// silently, so this is how it is offered instead.
function missingPresets() {
  const have = new Set(settings.skills.map((skill) => skill.preset || skill.id));
  return presets.filter((preset) => !have.has(preset.id));
}

function addFromPreset() {
  const id = $('presetPick').value;
  const preset = presets.find((p) => p.id === id);
  if (!preset) {
    toast('Every preset is already in the list.');
    return;
  }
  settings.skills.push(JSON.parse(JSON.stringify(preset)));
  renderSkills();
  toast(preset.name + ' added. Do not forget to save.');
}

function renderPresetPicker() {
  const pick = $('presetPick');
  const button = $('addPreset');
  if (!pick || !button) return;
  const missing = missingPresets();
  pick.innerHTML = '';
  for (const preset of missing) {
    pick.append(el('option', { value: preset.id, text: preset.name }));
  }
  pick.hidden = missing.length === 0;
  button.hidden = missing.length === 0;
}

function renderSkills() {
  const host = $('skills');
  host.innerHTML = '';
  skillModelPickers = [];
  settings.skills.forEach((skill, index) => host.append(skillCard(skill, index)));
  renderPresetPicker();
}

function field(label, control, hintText) {
  return el('div', { class: 'field' },
    el('label', { text: label }),
    control,
    hintText ? el('div', { class: 'hint', text: hintText }) : null);
}

function text(value, onInput, attrs = {}) {
  return el('input', Object.assign({
    class: 'input mono', type: 'text', value: value || '',
    oninput: (event) => onInput(event.target.value),
  }, attrs));
}

function area(value, onInput, rows = '4') {
  return el('textarea', {
    class: 'textarea', rows, value: value || '',
    oninput: (event) => onInput(event.target.value),
  });
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
// that a skill's model name belongs to that program - Claude Code wants
// "sonnet", Codex wants "gpt-5-codex" - so anything typed is kept as is.
function modelField(skill) {
  const picker = combobox({
    value: skill.model || '',
    items: () => models.all(),
    placeholder: 'leave empty for the program default',
    onChange: (value) => { skill.model = value; },
  });
  skillModelPickers.push({ refresh: () => picker.setValue(skill.model || '', false) });
  return picker.node;
}

function skillCard(skill, index) {
  const card = el('div', { class: 'skill' });
  const titleNode = el('span', { class: 'nm', text: skill.name || skill.id });
  const modeNode = el('span', {
    class: 'kind',
    text: skill.skip_permissions ? 'skips permissions' : 'asks permission',
  });

  const head = el('div', { class: 'skill-head' },
    el('label', { class: 'switch', onclick: (event) => event.stopPropagation() },
      el('input', {
        type: 'checkbox', checked: skill.enabled,
        onchange: (event) => { skill.enabled = event.target.checked; },
      }),
      el('span', { class: 'track' })),
    titleNode,
    modeNode,
    skill.preset ? el('span', { class: 'kind', text: 'preset · ' + skill.preset }) : null,
    el('span', { class: 'spacer' }),
    skill.preset ? el('button', {
      class: 'btn sm', type: 'button', text: 'Reset to preset',
      onclick: async (event) => {
        event.stopPropagation();
        const preset = presets.find((p) => p.id === skill.preset);
        if (!preset) {
          toast('This installation does not ship a preset called "' + skill.preset + '".', 'error');
          return;
        }
        const ok = await confirmDialog({
          title: 'Reset to the shipped preset?',
          body: 'Everything you changed about "' + (skill.name || skill.id) +
            '" goes back to what Socrates ships, except whether it is enabled. ' +
            'Nothing is written until you save.',
          confirmLabel: 'Reset',
        });
        if (!ok) return;
        const copy = JSON.parse(JSON.stringify(preset));
        copy.enabled = skill.enabled;
        settings.skills[index] = copy;
        renderSkills();
      },
    }) : null,
    el('button', {
      class: 'btn sm danger', type: 'button', text: 'Remove',
      onclick: async (event) => {
        event.stopPropagation();
        const ok = await confirmDialog({
          title: 'Remove this skill?',
          body: '"' + (skill.name || skill.id) + '" disappears from the list. ' +
            'Nothing is written until you save.',
          confirmLabel: 'Remove skill',
          danger: true,
        });
        if (!ok) return;
        settings.skills.splice(index, 1);
        renderSkills();
      },
    }),
  );
  head.addEventListener('click', (event) => {
    if (event.target.closest('button') || event.target.closest('.switch')) return;
    card.classList.toggle('open');
  });

  const skipArgsField = field('Arguments that skip permissions',
    text((skill.skip_permission_args || []).join(' '),
      (value) => { skill.skip_permission_args = splitArgs(value); },
      { placeholder: '--dangerously-skip-permissions' }),
    'Added when the switch above is on.');

  const askArgsField = field('Arguments that keep permissions on',
    text((skill.ask_permission_args || []).join(' '),
      (value) => { skill.ask_permission_args = splitArgs(value); },
      { placeholder: '--ask-for-approval on-request' }),
    'Added when the switch above is off. Socrates then answers the prompts on screen itself.');

  const headlessUsageField = field('Headless usage',
    area(skill.headless_usage, (value) => { skill.headless_usage = value; }, '3'),
    'How to use it without a terminal. Only this text reaches Socrates, and only while the switch above is off.');

  const manualFields = MANUAL.map(([key, label, hintText]) =>
    field(label, area(skill[key], (value) => { skill[key] = value; }), hintText));

  const body = el('div', { class: 'skill-body' },
    el('div', { class: 'grid-2' },
      field('Display name', text(skill.name, (value) => {
        skill.name = value;
        titleNode.textContent = value || skill.id;
      }, { class: 'input' })),
      field('Identifier', text(skill.id, (value) => { skill.id = slug(value); },
        { placeholder: 'codex' }), 'How Socrates refers to this skill.'),
    ),
    field('When should Socrates use it?',
      area(skill.description, (value) => { skill.description = value; }, '3'),
      'Goes straight into the system prompt, so write it the way you would explain it to a colleague.'),
    el('div', { class: 'grid-2' },
      field('Command', text(skill.command, (value) => { skill.command = value; },
        { placeholder: 'claude' }), 'Binary name or absolute path.'),
      field('Arguments', text((skill.args || []).join(' '),
        (value) => { skill.args = splitArgs(value); },
        { placeholder: '--no-alt-screen' }), 'Always passed, before everything else.'),
      field('Environment', text((skill.env || []).join(' '),
        (value) => { skill.env = splitArgs(value); },
        { placeholder: 'IS_SANDBOX=1' }),
        'KEY=VALUE pairs put in front of the command, the way you would in a shell. ' +
        'Claude Code needs IS_SANDBOX=1 to skip permissions as root.'),
      field('Model', modelField(skill),
        'The program’s own model name. Pick one from the list or type anything, for example "sonnet".'),
      field('Model argument', text(skill.model_flag, (value) => { skill.model_flag = value; },
        { placeholder: '--model' }), 'The flag the model name follows. Leave empty to never pass one.'),
    ),
    el('div', { class: 'row', style: 'margin: 4px 0 18px' },
      toggle(!!skill.skip_permissions, (checked) => {
        skill.skip_permissions = checked;
        modeNode.textContent = checked ? 'skips permissions' : 'asks permission';
        skipArgsField.hidden = !checked;
        askArgsField.hidden = checked;
      }, 'Skip permission prompts (run unattended)')),
    el('div', { class: 'hint', style: 'margin: -14px 0 16px' },
      'On, the program is started in its own unattended mode and never stops to ask. ' +
      'Off, it asks before it acts and Socrates answers the prompts on screen the way you would.'),
    skipArgsField,
    askArgsField,
    el('h3', { class: 'skill-section', text: 'The manual' }),
    el('div', { class: 'hint', style: 'margin: -8px 0 14px' },
      'What Socrates reads before it touches this program. Write it as you would brief a colleague ' +
      'who has never seen it; leave a section empty and it is simply not mentioned.'),
    ...manualFields,
    el('h3', { class: 'skill-section', text: 'How it may be used' }),
    el('div', { class: 'row', style: 'margin: 4px 0 18px' },
      toggle(skill.interactive_only !== false, (checked) => {
        skill.interactive_only = checked;
        headlessUsageField.hidden = checked;
      }, 'Interactive only')),
    el('div', { class: 'hint', style: 'margin: -14px 0 16px' },
      'On, Socrates may only drive this program in a terminal session, where you can watch it and ' +
      'take the keyboard. Off, it may also run it as a plain shell command.'),
    field('Headless forms',
      area(skill.headless_forms, (value) => { skill.headless_forms = value; }, '3'),
      'The program’s non-interactive invocations, named so Socrates knows exactly what to avoid.'),
    headlessUsageField,
    el('h3', { class: 'skill-section', text: 'Timing' }),
    el('div', { class: 'grid-2' },
      field('Ready when the screen matches', text(skill.ready_pattern, (value) => { skill.ready_pattern = value; },
        { placeholder: 'leave empty to wait for it to go quiet' }),
        'Regular expression. Only needed if waiting for silence starts typing too early.'),
      field('Busy pattern', text(skill.busy_pattern, (value) => { skill.busy_pattern = value; },
        { placeholder: 'for example: esc to interrupt' }),
        'Regular expression that means the program is still working. While it matches, Socrates keeps waiting instead of answering.'),
      field('Counts as finished after (seconds)',
        number(skill.idle_seconds, (value) => { skill.idle_seconds = value; }, { min: '1', max: '300' }),
        'How long the program must print nothing before its turn is treated as over.'),
      field('Timeout (seconds)',
        number(skill.timeout_seconds, (value) => { skill.timeout_seconds = value; }, { min: '30' })),
      field('Window size', el('div', { class: 'row', style: 'gap:8px' },
        number(skill.cols || 0, (value) => { skill.cols = value; }, { min: '0', max: '400', placeholder: 'cols' }),
        number(skill.rows || 0, (value) => { skill.rows = value; }, { min: '0', max: '200', placeholder: 'rows' })),
        'Columns and rows the program is given. Zero for the default.'),
    ),
  );

  skipArgsField.hidden = !skill.skip_permissions;
  askArgsField.hidden = !!skill.skip_permissions;
  headlessUsageField.hidden = skill.interactive_only !== false;

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
  button.disabled = true;
  try {
    const data = await api('/api/tunnel/start', { method: 'POST', body: { tunnel: tunnelForm() } });
    settings.tunnel = data.tunnel;
    renderTunnelStatus(data.status || {});
    toast('Tunnel starting');
    setTimeout(refreshTunnel, 1200);
  } catch (err) {
    toast(errorMessage(err), 'error');
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
    toast(errorMessage(err), 'error');
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
    toast(errorMessage(err), 'error');
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
  try {
    await api('/api/settings/password', { method: 'POST', body: { current, next } });
    $('pwCurrent').value = '';
    $('pwNext').value = '';
    toast('Password changed');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}
