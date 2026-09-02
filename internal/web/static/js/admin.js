// The admin dashboard: everything about Socrates is configurable here.
//
// The page is one settings document with a form drawn over it. Every control
// writes straight into that document and Save sends the whole thing back, so
// an unsaved change is undone by leaving the page and a save is one request
// that either succeeded or did not.
//
// The harness cards are not hand written. §C.9-C.12 of the specification is a
// catalogue of options - a key, a control, the flag or variable it becomes -
// and OPTIONS below is that catalogue in one place, so a new option is one
// entry rather than a field, a reader, a writer and a paragraph of HTML.

import {
  api, el, toast, isOffline, errorMessage, setClass, onWake, infoTip,
  confirmDialog, LiveStream,
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

// The two OpenRouter model ids are searchable fields rather than plain text
// ones, so neither is in FIELDS. There is no model catalogue endpoint any more
// - it went with the chat API - so the combobox is what it is with no items: a
// text field that takes any id, with the ids already in use offered back.
const MODEL_PICKERS = [
  ['orTranscribe', 'openrouter.transcribe_model'],
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
  ['ttsRate', 'voice.tts_rate', 'number'],
  ['speakAuto', 'voice.speak_in_auto_mode', 'bool'],
  ['speakChat', 'voice.speak_in_chat_mode', 'bool'],
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

/* ------------------------------------------------------- the option catalogue */

// A group is a disclosure inside a harness card. The order here is the order
// they appear in; a group with no options for a harness is not drawn.
const GROUP_ORDER = [
  'Model & effort',
  'Model & agent',
  'Permissions & sandbox',
  'Permissions',
  'Providers',
  'Isolation',
  'Remote control',
  'Session & prompt',
  'Session',
  'Extensions',
  'Tools & features',
  'Theme & terminal',
  'Diagnostics',
  'Advanced (raw)',
];

// The Codex themes the specification names, and OpenCode's verified built-in
// list. Neither CLI validates a theme name, so a typo is a session that starts
// and looks wrong - which is why both are closed lists with a way out.
const CODEX_THEMES = ['light-gray', 'gruvbox-light', 'ocean-light', 'light-dark', 'gruvbox-dark'];
const OPENCODE_THEMES = ['github', 'solarized', 'tokyonight', 'everforest', 'ayu', 'catppuccin',
  'gruvbox', 'kanagawa', 'nord', 'one-dark', 'rosepine', 'flexoki', 'vercel', 'system'];

const CLAUDE_EFFORTS = ['', 'low', 'medium', 'high', 'xhigh', 'max'];
const CODEX_EFFORTS = ['', 'minimal', 'low', 'medium', 'high', 'xhigh'];

// The raw groups every harness has, because every harness has the flag and the
// variable this app did not think of.
const RAW = [
  { key: 'extra_args', label: 'Extra arguments', type: 'list', group: 'Advanced (raw)',
    hint: 'Appended to the command line verbatim, separated by spaces.' },
  { key: 'extra_env', label: 'Extra environment', type: 'lines', group: 'Advanced (raw)',
    hint: 'One KEY=VALUE per line. A name that is not a valid variable name is dropped on save.' },
];

const OPTIONS = {
  shell: [
    { key: 'login', label: 'Login shell', type: 'switch', group: 'Session',
      hint: 'Starts the shell with -l, so the profile that sets up PATH and the prompt is read.' },
    ...RAW,
  ],

  claude: [
    { key: 'default_model', label: 'Default model', type: 'model', group: 'Model & effort',
      hint: '--model. A session started without a model of its own uses this one.' },
    { key: 'default_effort', label: 'Default effort', type: 'select', values: CLAUDE_EFFORTS,
      group: 'Model & effort', hint: '--effort. Empty leaves Claude Code on its own default.' },
    { key: 'autocompact', label: 'Autocompact', type: 'text', group: 'Model & effort',
      placeholder: 'auto, or 200k',
      hint: '--autocompact. Either the word auto or a context size between 100k and 1M.' },
    { key: 'max_thinking_tokens', label: 'Max thinking tokens', type: 'int', min: 0,
      group: 'Model & effort', hint: 'env MAX_THINKING_TOKENS. 0 leaves it unset.' },

    { key: 'permission_mode', label: 'Permission mode', type: 'select',
      values: ['unset', 'manual', 'acceptEdits', 'auto', 'plan', 'dontAsk', 'bypassPermissions'],
      group: 'Permissions & sandbox', hint: '--permission-mode. "unset" leaves the flag off.' },
    { key: 'skip_permissions', label: 'Dangerous skip', type: 'select',
      values: [
        { value: 'off', label: 'Off — the flag is never passed' },
        { value: 'allow', label: 'Allow — the session may skip permission prompts' },
        { value: 'force', label: 'Force — skip every permission prompt' },
      ],
      group: 'Permissions & sandbox', confirm: (value) => value !== 'off',
      confirmTitle: 'Let Claude Code skip permission prompts?',
      confirmBody: 'Every edit, every command and every network call happens without being asked '
        + 'about. Only do this in a directory you are willing to lose.',
      hint: 'Off passes nothing; allow passes --allow-dangerously-skip-permissions; force passes '
        + '--dangerously-skip-permissions.' },
    { key: 'allowed_tools', label: 'Allowed tools', type: 'list', group: 'Permissions & sandbox',
      hint: '--allowedTools, comma separated.' },
    { key: 'disallowed_tools', label: 'Disallowed tools', type: 'list', group: 'Permissions & sandbox',
      hint: '--disallowedTools, comma separated.' },
    { key: 'tools', label: 'Tools', type: 'text', group: 'Permissions & sandbox',
      hint: '--tools. Empty, the word default, or a list.' },
    { key: 'setting_sources', label: 'Setting sources', type: 'multi',
      values: ['user', 'project', 'local'], group: 'Permissions & sandbox',
      hint: '--setting-sources. Which layers of the user’s own settings Claude Code reads.' },
    { key: 'cleanup_period_days', label: 'Keep transcripts for', type: 'int', min: 1, max: 3650,
      group: 'Permissions & sandbox', hint: 'Days, written as cleanupPeriodDays into the generated settings file.' },
    { key: 'restricted', label: 'Restricted', type: 'switch', group: 'Permissions & sandbox', hint: '--restricted.' },
    { key: 'safe_mode', label: 'Safe mode', type: 'switch', group: 'Permissions & sandbox', hint: '--safe-mode.' },
    { key: 'bare', label: 'Bare', type: 'switch', group: 'Permissions & sandbox',
      hint: '--bare. It forces API-key authentication, so a subscription login will not be used.' },

    { key: 'add_dirs', label: 'Extra directories', type: 'lines', group: 'Permissions & sandbox',
      hint: '--add-dir, one absolute path per line. They are also written as permissions.additionalDirectories.' },

    { key: 'remote_control', label: 'Remote control', type: 'switch', group: 'Remote control',
      hint: '--remote-control.' },
    { key: 'remote_control_name', label: 'Remote control name', type: 'text', group: 'Remote control',
      hint: 'The name passed after --remote-control. Empty uses the session title.' },
    { key: 'remote_control_prefix', label: 'Session name prefix', type: 'text', group: 'Remote control',
      hint: 'env CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX.' },

    { key: 'resume_mode', label: 'On resume', type: 'select',
      values: [
        { value: 'continue', label: 'Continue the conversation' },
        { value: 'fork', label: 'Fork it, leaving the original as it was' },
      ],
      group: 'Session & prompt', hint: '--resume, with --fork-session for the second.' },
    { key: 'agent', label: 'Agent', type: 'text', group: 'Session & prompt', hint: '--agent.' },
    { key: 'append_system_prompt', label: 'Appended system prompt', type: 'textarea',
      group: 'Session & prompt', hint: '--append-system-prompt.' },
    { key: 'system_prompt_snapshot', label: 'System prompt snapshot', type: 'select',
      values: ['on', 'off'], group: 'Session & prompt',
      hint: '--system-prompt-snapshot. It only decides anything when a prompt is appended above.' },
    { key: 'exclude_dynamic_prompt_sections', label: 'Exclude dynamic prompt sections',
      type: 'switch', group: 'Session & prompt', hint: '--exclude-dynamic-system-prompt-sections.' },
    { key: 'disable_slash_commands', label: 'Disable slash commands', type: 'switch',
      group: 'Session & prompt', hint: '--disable-slash-commands.' },

    { key: 'mcp_config', label: 'MCP configuration files', type: 'lines', group: 'Extensions',
      hint: '--mcp-config, one path per line.' },
    { key: 'strict_mcp_config', label: 'Strict MCP configuration', type: 'switch',
      group: 'Extensions', hint: '--strict-mcp-config.' },
    { key: 'plugin_dirs', label: 'Plugin directories', type: 'lines', group: 'Extensions',
      hint: '--plugin-dir, one path per line.' },

    { key: 'pin_light_theme', label: 'Pin the light theme', type: 'switch', group: 'Theme & terminal',
      hint: 'Writes "theme":"light" into the global Claude Code preferences, so the pane matches the page.' },
    { key: 'disable_terminal_title', label: 'Leave the terminal title alone', type: 'switch',
      group: 'Theme & terminal', hint: 'env CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1.' },
    { key: 'disable_mouse', label: 'Disable the mouse', type: 'switch', group: 'Theme & terminal',
      hint: 'env CLAUDE_CODE_DISABLE_MOUSE=1.' },
    { key: 'no_flicker', label: 'No flicker', type: 'switch', group: 'Theme & terminal',
      hint: 'env CLAUDE_CODE_NO_FLICKER=1.' },
    { key: 'force_sync_output', label: 'Force synchronous output', type: 'switch',
      group: 'Theme & terminal', hint: 'env CLAUDE_CODE_FORCE_SYNC_OUTPUT=1.' },

    { key: 'verbose', label: 'Verbose', type: 'switch', group: 'Diagnostics', hint: '--verbose.' },
    { key: 'debug_filter', label: 'Debug filter', type: 'text', group: 'Diagnostics', hint: '-d <filter>.' },
    { key: 'debug_file', label: 'Write a debug log', type: 'switch', group: 'Diagnostics',
      hint: '--debug-file, into the session’s own directory.' },

    { key: 'advisor', label: 'Advisor model', type: 'text', group: 'Advanced (raw)',
      hint: '--advisor. Undocumented and absent from --help, so it lives here: it is offered '
        + 'because it is in the shipped binary, and a wrong value is a session that will not start.' },
    { key: 'settings_overrides', label: 'Settings overrides', type: 'json', group: 'Advanced (raw)',
      hint: 'JSON, deep-merged into the generated settings file. Invalid JSON is refused when you save.' },
    ...RAW,
  ],

  codex: [
    { key: 'default_model', label: 'Default model', type: 'model', group: 'Model & effort', hint: '-m.' },
    { key: 'default_effort', label: 'Default effort', type: 'select', values: CODEX_EFFORTS,
      group: 'Model & effort',
      hint: '-c model_reasoning_effort. Codex does not check this value, so the list is closed here.' },
    { key: 'model_reasoning_summary', label: 'Reasoning summary', type: 'select',
      values: ['', 'auto', 'concise', 'detailed', 'none'], group: 'Model & effort',
      hint: '-c model_reasoning_summary.' },
    { key: 'model_verbosity', label: 'Verbosity', type: 'select', values: ['', 'low', 'medium', 'high'],
      group: 'Model & effort', hint: '-c model_verbosity.' },
    { key: 'personality', label: 'Personality', type: 'select', values: ['', 'none', 'friendly', 'pragmatic'],
      group: 'Model & effort', hint: '-c personality.' },
    { key: 'review_model', label: 'Review model', type: 'text', group: 'Model & effort',
      hint: '-c review_model.' },

    { key: 'sandbox', label: 'Sandbox', type: 'select',
      values: ['read-only', 'workspace-write', 'danger-full-access'],
      group: 'Permissions & sandbox', hint: '-s.' },
    { key: 'approval', label: 'Approval policy', type: 'select',
      values: ['on-request', 'on-failure', 'never'], group: 'Permissions & sandbox',
      hint: '-a. on-failure goes through -c approval_policy, because the flag itself rejects it.' },
    { key: 'network_access', label: 'Network access', type: 'switch', group: 'Permissions & sandbox',
      hint: '-c sandbox_workspace_write.network_access=true.' },
    { key: 'writable_roots', label: 'Writable roots', type: 'lines', group: 'Permissions & sandbox',
      hint: '-c sandbox_workspace_write.writable_roots, one path per line.' },
    { key: 'add_dirs', label: 'Extra directories', type: 'lines', group: 'Permissions & sandbox',
      hint: '--add-dir, one path per line.' },
    { key: 'approve_for_me', label: 'Approve for me', type: 'switch', group: 'Permissions & sandbox',
      hint: '--approve-for-me.' },
    { key: 'bypass', label: 'Bypass approvals and sandbox', type: 'switch',
      group: 'Permissions & sandbox', confirm: (value) => value === true,
      confirmTitle: 'Turn the Codex sandbox off?',
      confirmBody: 'Codex will run every command with your own rights and no approval. Only do '
        + 'this in a directory you are willing to lose.',
      hint: '--dangerously-bypass-approvals-and-sandbox.' },
    { key: 'trust_workdir', label: 'Trust the working directory', type: 'switch',
      group: 'Permissions & sandbox', warnWhenOff:
        'With this off Codex opens on its trust picker and the session blocks until somebody '
        + 'answers it in the pane.',
      hint: '-c projects."<cwd>".trust_level="trusted". Leave it on unless you know why not.' },

    { key: 'remote_addr', label: 'Remote address', type: 'text', group: 'Remote control',
      placeholder: 'ws://…', hint: '--remote.' },
    { key: 'remote_auth_token_env', label: 'Remote token variable', type: 'text',
      group: 'Remote control',
      hint: '--remote-auth-token-env. This is the name of an environment variable, not the token.' },

    { key: 'web_search', label: 'Web search', type: 'switch', group: 'Tools & features', hint: '--search.' },
    { key: 'features_enable', label: 'Features on', type: 'list', group: 'Tools & features',
      hint: '--enable, comma separated. Run `codex features list` to see what there is; Codex '
        + 'cannot validate these names.' },
    { key: 'features_disable', label: 'Features off', type: 'list', group: 'Tools & features',
      hint: '--disable, comma separated.' },
    { key: 'hide_agent_reasoning', label: 'Hide reasoning', type: 'switch', group: 'Tools & features',
      hint: '-c hide_agent_reasoning.' },
    { key: 'show_raw_agent_reasoning', label: 'Show raw reasoning', type: 'switch',
      group: 'Tools & features', hint: '-c show_raw_agent_reasoning.' },

    { key: 'tui_theme', label: 'Theme', type: 'freeselect', values: CODEX_THEMES,
      group: 'Theme & terminal', hint: '-c tui.theme. Any name Codex knows is accepted; it does not check it.' },
    { key: 'no_alt_screen', label: 'No alternate screen', type: 'switch', group: 'Theme & terminal',
      hint: '--no-alt-screen. It keeps the scrollback, which is what a web terminal wants.' },
    { key: 'disable_keyboard_enhancement', label: 'Disable keyboard enhancement', type: 'switch',
      group: 'Theme & terminal',
      hint: 'env CODEX_TUI_DISABLE_KEYBOARD_ENHANCEMENT=1, for when keys misbehave through tmux.' },

    { key: 'config_overrides', label: 'Configuration overrides', type: 'lines', group: 'Advanced (raw)',
      hint: 'One key=value per line, each passed as its own -c. Codex is launched with '
        + '--strict-config, so a wrong key here is a session that refuses to start.' },
    ...RAW,
  ],

  opencode: [
    { key: 'default_model', label: 'Default model', type: 'model', group: 'Model & agent',
      hint: '-m provider/model. Everything after the first slash is the model id.' },
    { key: 'small_model', label: 'Small model', type: 'text', group: 'Model & agent',
      hint: 'config small_model, for the cheap work OpenCode does on the side.' },
    { key: 'default_agent', label: 'Default agent', type: 'text', group: 'Model & agent',
      hint: '--agent. An unknown name silently falls back to build.' },

    { key: 'auto', label: 'Approve everything', type: 'switch', group: 'Permissions',
      confirm: (value) => value === true,
      confirmTitle: 'Approve everything OpenCode does?',
      confirmBody: 'Every action not explicitly denied below runs without being asked about.',
      hint: '--auto. Auto-approves everything not explicitly denied.' },
    { key: 'permission_json', label: 'Permissions', type: 'json', group: 'Permissions',
      placeholder: '{"*":"ask","bash":{"git *":"allow"}}',
      hint: 'env OPENCODE_PERMISSION. Actions are ask, allow or deny. Invalid JSON is refused when you save.' },

    { key: 'enabled_providers', label: 'Providers allowed', type: 'list', group: 'Providers',
      hint: 'config enabled_providers, comma separated. An allowlist: empty means all of them.' },
    { key: 'disabled_providers', label: 'Providers blocked', type: 'list', group: 'Providers',
      hint: 'config disabled_providers, comma separated.' },

    { key: 'pure', label: 'Pure', type: 'switch', group: 'Isolation', hint: '--pure.' },
    { key: 'disable_project_config', label: 'Ignore project configuration', type: 'switch',
      group: 'Isolation', hint: 'env OPENCODE_DISABLE_PROJECT_CONFIG=1.' },
    { key: 'disable_models_fetch', label: 'Do not fetch the model list', type: 'switch',
      group: 'Isolation',
      hint: 'env OPENCODE_DISABLE_MODELS_FETCH=1. This is what lets OpenCode start with no network at all.' },

    { key: 'resume_mode', label: 'On resume', type: 'select',
      values: [
        { value: 'continue', label: 'Continue the conversation' },
        { value: 'fork', label: 'Fork it, leaving the original as it was' },
      ],
      group: 'Session', hint: '--session, with --fork for the second.' },
    { key: 'share', label: 'Sharing', type: 'select', values: ['manual', 'auto', 'disabled'],
      group: 'Session', hint: 'config share. Disabled is the default and the only one that publishes nothing.' },

    { key: 'tui_theme', label: 'Theme', type: 'freeselect', values: OPENCODE_THEMES,
      group: 'Theme & terminal',
      hint: 'tui.json theme. The light or dark palette of it is chosen by the pane, not by the name.' },
    { key: 'mini', label: 'Mini', type: 'switch', group: 'Theme & terminal',
      hint: '--mini. Line-oriented instead of an alternate screen, which is easier on a phone.' },
    { key: 'no_replay', label: 'No replay', type: 'switch', group: 'Theme & terminal',
      requires: 'mini', hint: '--no-replay. Mini only.' },
    { key: 'replay_limit', label: 'Replay limit', type: 'int', min: 0, group: 'Theme & terminal',
      requires: 'mini', hint: '--replay-limit. Mini only; 0 leaves it unset.' },
    { key: 'disable_mouse', label: 'Disable the mouse', type: 'switch', group: 'Theme & terminal',
      hint: 'env OPENCODE_DISABLE_MOUSE=1.' },
    { key: 'mouse', label: 'Mouse in the TUI', type: 'switch', group: 'Theme & terminal',
      hint: 'tui.json mouse.' },
    { key: 'attention', label: 'Attention sounds', type: 'switch', group: 'Theme & terminal',
      hint: 'tui.json attention.enabled. A server has no business making desktop notification sounds.' },

    { key: 'log_level', label: 'Log level', type: 'select', values: ['', 'DEBUG', 'INFO', 'WARN', 'ERROR'],
      group: 'Diagnostics', hint: '--log-level. Empty leaves OpenCode on its own.' },

    { key: 'config_content', label: 'Configuration', type: 'json', group: 'Advanced (raw)',
      hint: 'JSON, deep-merged into OPENCODE_CONFIG_CONTENT.' },
    { key: 'tui_config', label: 'TUI configuration', type: 'json', group: 'Advanced (raw)',
      hint: 'JSON, deep-merged into the generated tui.json.' },
    ...RAW,
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
  loadHarnesses();
  refreshTmux();
  if (data.local_url) localURL = data.local_url;
  refreshTunnel();
  refreshVoice();
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
  for (const id of ['shell', 'claude', 'codex', 'opencode']) {
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

/* ------------------------------------------------------- the tmux engine */

const TMUX_LABEL = { ok: 'tmux is ready', old: 'tmux is too old', missing: 'tmux is missing' };

let tmuxReport = null;
let tmuxStream = null;

function tmuxState(report) {
  if (report.ok) return 'ok';
  return report.installed ? 'old' : 'missing';
}

function renderTmux(report) {
  tmuxReport = report;
  const host = $('tmuxStatus');
  let dot = host.querySelector(':scope > .state-dot');
  if (!dot) {
    dot = el('span', { class: 'state-dot' });
    host.prepend(dot);
  }
  const state = report.installing ? 'installing' : tmuxState(report);
  for (const name of ['ok', 'old', 'missing', 'installing']) setClass(dot, name, state === name);
  while (dot.nextSibling) dot.nextSibling.remove();

  const parts = [el('span', {
    class: 'state-label',
    text: report.installing ? 'Installing tmux…' : TMUX_LABEL[state],
  })];
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

  const install = $('tmuxInstall');
  install.hidden = report.ok || !report.can_install;
  install.disabled = !!report.installing;
  install.textContent = report.installing ? 'Installing…' : 'Install tmux with ' + report.manager;

  const manual = $('tmuxManualField');
  manual.hidden = report.ok || !report.command;
  if (!manual.hidden) $('tmuxManual').value = report.command;

  const toggle = $('tmuxLogToggle');
  toggle.hidden = !(report.log || []).length && !report.installing;
  renderTmuxLog(report.log || []);
}

function renderTmuxLog(lines) {
  const log = $('tmuxLog');
  if (lines.length) log.dataset.lines = String(lines.length);
  if (log.hidden) return;
  log.textContent = lines.join('\n') || 'no output yet';
  log.scrollTop = log.scrollHeight;
}

async function refreshTmux() {
  try {
    const report = await api('/api/tmux');
    renderTmux(report);
    if (report.installing) watchInstall();
  } catch (err) {
    if (!isOffline(err)) toast(errorMessage(err), 'error');
  }
}

// watchInstall subscribes to the progress stream. It is the same LiveStream
// the session page uses, so the backoff, the watchdog and the connection bar
// are the ones the rest of the app already has.
function watchInstall() {
  if (tmuxStream) return;
  const lines = [...(tmuxReport && tmuxReport.log ? tmuxReport.log : [])];
  $('tmuxLog').hidden = false;
  $('tmuxLogToggle').hidden = false;
  $('tmuxLogToggle').textContent = 'Hide output';
  tmuxStream = new LiveStream({
    url: () => '/api/tmux/events',
    reportsGlobal: false,
    onMessage: (event) => {
      if (!event) return;
      if (event.type === 'line') {
        lines.push(event.line);
        renderTmuxLog(lines);
        return;
      }
      if (event.type !== 'done') return;
      tmuxStream.stop();
      tmuxStream = null;
      toast(event.ok ? 'tmux installed' : 'The install failed — the output says why.',
        event.ok ? '' : 'error');
      refreshTmux();
    },
  });
  tmuxStream.start();
}

async function installTmux() {
  const button = $('tmuxInstall');
  busyButton(button, true, 'Installing…');
  try {
    await api('/api/tmux/install', { method: 'POST', body: {} });
    watchInstall();
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    busyButton(button, false, 'Installing…');
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

function harnessLabel(id) {
  const found = harnesses.list().find((h) => h.id === id);
  if (found) return found.label;
  return { shell: 'Shell', claude: 'Claude Code', codex: 'Codex', opencode: 'OpenCode' }[id] || id;
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
  if (status && status !== 'loading' && status !== 'refreshing') {
    host.append(el('div', { class: 'hint', text: status }));
  } else if (harnesses.stale()) {
    host.append(el('div', { class: 'hint',
      text: 'Showing the programs from your last visit — this machine could not be asked just now.' }));
  }
  for (const id of ['shell', 'claude', 'codex', 'opencode']) {
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
    if (harness.version) facts.push(harness.version);
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
    harness.id === 'shell' ? null : modelList(harness),
    field('Binary path', binary,
      'Leave it empty to look the program up on PATH, which is what a normal installation wants.'),
  );

  const built = new Map();
  for (const group of GROUP_ORDER) {
    const options = (OPTIONS[harness.id] || []).filter((option) => option.group === group);
    if (!options.length) continue;
    const body = el('div', { class: 'group-body' });
    for (const option of options) {
      const made = optionField(harness, option);
      built.set(option.key, made);
      body.append(made.node);
    }
    card.append(el('details', { class: 'group', 'data-group': group },
      el('summary', { text: group }), body));
  }
  // A control that only means something while another one is on follows it:
  // one listener per pair, on the card that owns both.
  for (const made of built.values()) {
    const master = made.requires && built.get(made.requires);
    if (master) master.control.addEventListener('change', made.reflect);
  }
  card.append(el('div', { class: 'row' }, refresh));
  card.append(el('div', { class: 'hint start-only', text: START_ONLY_NOTE }));
  return card;
}

// optionField turns one catalogue entry into the control the specification
// names for it, with the flag or variable it maps to in the hint - that is
// technical, and technical detail is what the hint is for.
function optionField(harness, option) {
  const id = 'opt-' + harness.id + '-' + option.key;
  const path = optionPath(harness.id, option.key);
  const value = optionValue(harness.id, option.key);
  const write = (next) => setPath(settings, path, next);

  let control = null;
  let wrapper = null;
  switch (option.type) {
    case 'switch': {
      const box = el('input', { type: 'checkbox', id });
      box.checked = !!value;
      box.addEventListener('change', () => guard(option, box.checked, box, () => write(box.checked)));
      wrapper = el('label', { class: 'switch' }, box, el('span', { class: 'track' }),
        el('span', { text: option.label }));
      control = box;
      break;
    }
    case 'select':
    case 'freeselect': {
      control = el('select', { class: 'select', id });
      const entries = option.values.map((v) => (typeof v === 'string' ? { value: v, label: v || '—' } : v));
      // A free select keeps a value the CLI knows and this list does not, so
      // that saving the page never quietly changes somebody's theme.
      if (option.type === 'freeselect' && value && !entries.some((e) => e.value === value)) {
        entries.push({ value, label: value + ' (typed in)' });
      }
      for (const entry of entries) control.append(el('option', { value: entry.value, text: entry.label }));
      control.value = value ?? '';
      // The dialog below has to be able to put a refused choice back, and a
      // select does not remember what it was before the change event.
      control.dataset.previous = control.value;
      control.addEventListener('change', () => guard(option, control.value, control, () => {
        write(control.value);
        control.dataset.previous = control.value;
      }));
      break;
    }
    case 'multi': {
      wrapper = el('div', { class: 'multi', id });
      const chosen = new Set(value || []);
      for (const name of option.values) {
        const box = el('input', { type: 'checkbox', id: id + '-' + name });
        box.checked = chosen.has(name);
        box.addEventListener('change', () => {
          if (box.checked) chosen.add(name); else chosen.delete(name);
          write(option.values.filter((v) => chosen.has(v)));
        });
        wrapper.append(el('label', { class: 'switch' }, box, el('span', { class: 'track' }),
          el('span', { text: name })));
      }
      // A multi is a set of boxes with no single control behind it, so the
      // group itself stands in for one.
      control = wrapper;
      break;
    }
    case 'int': {
      control = el('input', { class: 'input', type: 'number', id, value: value ?? 0 });
      if (option.min !== undefined) control.setAttribute('min', option.min);
      if (option.max !== undefined) control.setAttribute('max', option.max);
      control.addEventListener('input', () => write(Math.round(Number(control.value) || 0)));
      break;
    }
    case 'list': {
      control = el('input', {
        class: 'input mono', type: 'text', id, spellcheck: 'false',
        placeholder: option.placeholder || '',
        value: (value || []).join(', '),
      });
      control.addEventListener('input', () => write(splitCommas(control.value)));
      break;
    }
    case 'lines': {
      control = el('textarea', { class: 'textarea mono', id, spellcheck: 'false',
        placeholder: option.placeholder || '', value: (value || []).join('\n') });
      control.addEventListener('input', () => write(splitLines(control.value)));
      break;
    }
    case 'textarea':
    case 'json': {
      control = el('textarea', {
        class: 'textarea' + (option.type === 'json' ? ' mono' : ''), id, spellcheck: 'false',
        placeholder: option.placeholder || '', value: value || '',
      });
      control.addEventListener('input', () => write(control.value));
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

  const node = wrapper || control;
  const hint = el('div', { class: 'hint', text: option.hint || '' });
  const warning = el('div', { class: 'hint bad', hidden: true });
  const wrap = el('div', { class: 'field option', 'data-key': option.key, 'data-startonly': '1' });
  // A switch carries its own label; everything else gets one above it.
  if (option.type !== 'switch') wrap.append(el('label', { for: id, text: option.label }));
  wrap.append(node, hint, warning);

  // reflect is everything about this control that depends on another value:
  // the red line under a switch that should not be off, and the greying out of
  // a control whose master switch is off.
  const reflect = () => {
    if (option.warnWhenOff) {
      const off = !control.checked;
      warning.hidden = !off;
      warning.textContent = off ? option.warnWhenOff : '';
    }
    if (option.requires) {
      const on = !!optionValue(harness.id, option.requires);
      setClass(wrap, 'disabled', !on);
      control.disabled = !on;
    }
  };
  reflect();
  control.addEventListener('change', reflect);
  return { node: wrap, control, reflect, requires: option.requires || '' };
}

// guard is the confirm-twice dialog the dangerous switches carry. The control
// is put back if the answer is no, because a switch that stays on after a
// refusal is a switch that lied.
function guard(option, value, control, apply) {
  if (!option.confirm || !option.confirm(value)) {
    apply();
    return;
  }
  const previous = control.type === 'checkbox' ? !control.checked : (control.dataset.previous || '');
  confirmDialog({
    title: option.confirmTitle,
    body: option.confirmBody,
    confirmLabel: 'I understand',
    danger: true,
  }).then((yes) => {
    if (yes) {
      apply();
      return;
    }
    if (control.type === 'checkbox') control.checked = previous;
    else control.value = previous;
    control.dispatchEvent(new Event('change'));
  });
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
  $('windowSize').addEventListener('change', renderWindowSizeHint);
  $('tmuxInstall').addEventListener('click', installTmux);
  $('tmuxLogToggle').addEventListener('click', () => {
    const log = $('tmuxLog');
    log.hidden = !log.hidden;
    $('tmuxLogToggle').textContent = log.hidden ? 'Show output' : 'Hide output';
    if (!log.hidden) refreshTmux();
  });
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
      // nothing at all.
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

const splitCommas = (value) => value.split(',').map((part) => part.trim()).filter(Boolean);
const splitLines = (value) => value.split('\n').map((part) => part.trim()).filter(Boolean);

/* ------------------------------------------------------------- the voice */

const VOICE_LABEL = {
  ready: 'Voice ready',
  installing: 'Installing the voice…',
  missing: 'Voice not installed',
  failed: 'Voice failed',
};

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
      host.append(el('div', { class: 'check' },
        el('span', { class: 'st ' + (check.ok ? 'ok' : 'bad') }),
        el('span', { class: 'nm' }, mark ? agentMark(mark, 14) : null, el('span', { text: check.name })),
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
