// The chat page: sidebar, live process view, composer, voice and auto mode.

import { api, el, toast, fmtDuration, fmtClock } from './api.js';
import { renderMarkdown } from './markdown.js';
import { Recorder, speak, stopSpeaking, isSpeaking, plainSpeech } from './voice.js';

const $ = (id) => document.getElementById(id);

const dom = {
  sidebar: $('sidebar'),
  chatList: $('chatList'),
  thread: $('thread'),
  threadInner: $('threadInner'),
  title: $('chatTitle'),
  composer: $('composer'),
  input: $('input'),
  sendBtn: $('sendBtn'),
  micBtn: $('micBtn'),
  recTime: $('recTime'),
  stopBtn: $('stopBtn'),
  autoToggle: $('autoToggle'),
  autoScreen: $('autoScreen'),
  autoStatus: $('autoStatus'),
  autoTimer: $('autoTimer'),
  autoTranscript: $('autoTranscript'),
  autoMic: $('autoMic'),
  autoLive: $('autoLive'),
  autoAnswer: $('autoAnswer'),
  autoQuestion: $('autoQuestion'),
  autoActions: $('autoActions'),
  chatSettings: $('chatSettings'),
  chatPanel: $('chatPanel'),
  panelTitle: $('panelTitle'),
  panelWorkspace: $('panelWorkspace'),
  autoReplay: $('autoReplay'),
  autoDetails: $('autoDetails'),
};

const state = {
  chats: [],
  chatId: null,
  chat: null,
  rev: 0,
  busy: false,
  auto: localStorage.getItem('socrates.auto') === '1',
  es: null,
  stepEls: new Map(),
  stepData: new Map(),
  // One live stream per terminal session on screen.
  termStreams: new Map(),
  turnEls: new Map(),
  expanded: new Set(),
  touched: new Set(),
  pendingQuestion: null,
  optimisticUser: null,
  lastAnswer: '',
  autoPhase: 'idle',
  recorder: new Recorder(),
  recTimer: null,
  prefs: { speak_in_auto_mode: true, speak_in_chat_mode: false, tts_rate: 1, tts_language: '' },
};

const ICONS = {
  spark: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M18 6l-2.5 2.5M8.5 15.5 6 18"/></svg>',
  check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m5 13 4 4L19 7"/></svg>',
  cross: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M6 6l12 12M18 6 6 18"/></svg>',
  chev: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="13" height="13"><path d="m9 6 6 6-6 6"/></svg>',
  dot: '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="12" cy="12" r="3.5"/></svg>',
  trash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4 7h16M9 7V5h6v2M7 7l1 12h8l1-12"/></svg>',
  mic: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0 0 14 0M12 18v3"/></svg>',
  stop: '<svg viewBox="0 0 24 24" fill="currentColor"><rect x="7" y="7" width="10" height="10" rx="2.5"/></svg>',
  send: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7"/></svg>',
};

/* ------------------------------------------------------------- bootstrap */

init().catch((err) => toast(err.message, 'error'));

async function init() {
  setAuto(state.auto, true);
  bindUI();
  try {
    const prefs = await api('/api/preferences');
    if (prefs) state.prefs = prefs;
  } catch { /* defaults are fine */ }
  await refreshChats();
  const fromHash = location.hash.replace(/^#/, '');
  if (fromHash && state.chats.some((c) => c.id === fromHash)) await openChat(fromHash);
  else if (state.chats.length) await openChat(state.chats[0].id);
  else showEmptyState();
}

function bindUI() {
  dom.composer.addEventListener('submit', (event) => {
    event.preventDefault();
    submitText(dom.input.value);
  });
  dom.input.addEventListener('input', () => {
    autosize();
    dom.sendBtn.disabled = !dom.input.value.trim() || state.busy;
  });
  dom.input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      submitText(dom.input.value);
    }
  });
  $('newChat').addEventListener('click', () => startNewChat());
  $('menuBtn').addEventListener('click', () => document.body.classList.toggle('nav-open'));
  $('logout').addEventListener('click', async () => {
    await api('/api/logout', { method: 'POST' });
    location.href = '/login';
  });
  dom.stopBtn.addEventListener('click', stopRun);
  dom.chatSettings.addEventListener('click', toggleChatPanel);
  $('panelCancel').addEventListener('click', () => { dom.chatPanel.hidden = true; });
  $('panelSave').addEventListener('click', saveChatSettings);
  document.addEventListener('click', (event) => {
    if (dom.chatPanel.hidden) return;
    if (event.target.closest('#chatPanel') || event.target.closest('#chatSettings')) return;
    dom.chatPanel.hidden = true;
  });
  dom.micBtn.addEventListener('click', () => toggleRecording('chat'));
  dom.autoMic.addEventListener('click', () => toggleRecording('auto'));
  dom.autoToggle.checked = state.auto;
  dom.autoToggle.addEventListener('change', () => setAuto(dom.autoToggle.checked));
  dom.autoReplay.addEventListener('click', () => {
    if (isSpeaking()) stopSpeaking();
    else if (state.lastAnswer) speak(state.lastAnswer, { rate: state.prefs.tts_rate, lang: state.prefs.tts_language });
  });
  dom.autoDetails.addEventListener('click', () => setAuto(false));
  window.addEventListener('beforeunload', () => disconnect());
  window.addEventListener('hashchange', () => {
    const id = location.hash.replace(/^#/, '');
    if (id && id !== state.chatId) openChat(id).catch((err) => toast(err.message, 'error'));
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') document.body.classList.remove('nav-open');
  });
}

function autosize() {
  dom.input.style.height = 'auto';
  dom.input.style.height = Math.min(dom.input.scrollHeight, 200) + 'px';
}

/* ----------------------------------------------------------------- chats */

async function refreshChats() {
  const data = await api('/api/chats');
  state.chats = data.chats || [];
  renderChatList();
}

function renderChatList() {
  dom.chatList.innerHTML = '';
  if (!state.chats.length) {
    dom.chatList.append(el('div', { class: 'group-label', text: 'No chats yet' }));
    return;
  }
  dom.chatList.append(el('div', { class: 'group-label', text: 'Chats' }));
  for (const chat of state.chats) {
    const item = el('div', {
      class: 'chat-item' + (chat.id === state.chatId ? ' active' : ''),
      onclick: (event) => {
        if (event.target.closest('.del')) return;
        openChat(chat.id);
        document.body.classList.remove('nav-open');
      },
    },
      chat.id === state.chatId && state.busy ? el('span', { class: 'dot' }) : null,
      el('span', { class: 'label', text: chat.title || 'New chat' }),
      el('button', {
        class: 'icon-btn del',
        title: 'Delete chat',
        html: ICONS.trash,
        onclick: (event) => {
          event.stopPropagation();
          deleteChat(chat);
        },
      }),
    );
    dom.chatList.append(item);
  }
}

async function deleteChat(chat) {
  if (!confirm('Delete "' + (chat.title || 'New chat') + '"? This cannot be undone.')) return;
  await api('/api/chats/' + chat.id, { method: 'DELETE' });
  if (state.chatId === chat.id) {
    disconnect();
    state.chatId = null;
    state.chat = null;
    showEmptyState();
  }
  await refreshChats();
  if (!state.chatId && state.chats.length) openChat(state.chats[0].id);
}

function startNewChat() {
  disconnect();
  state.chatId = null;
  state.chat = null;
  state.rev = 0;
  state.pendingQuestion = null;
  state.lastAnswer = '';
  location.hash = '';
  dom.title.textContent = 'New chat';
  dom.chatSettings.hidden = true;
  dom.chatPanel.hidden = true;
  setBusy(false);
  showEmptyState();
  renderChatList();
  resetAutoScreen('Tap the microphone and speak');
  dom.input.focus();
}

async function openChat(id) {
  if (state.chatId === id && state.es) return;
  disconnect();
  state.chatId = id;
  state.rev = 0;
  state.pendingQuestion = null;
  state.lastAnswer = '';
  location.hash = id;
  const data = await api('/api/chats/' + id);
  state.chat = data.chat;
  state.effectiveWorkspace = data.effective_workspace || '';
  state.rev = data.rev || 0;
  dom.title.textContent = data.chat.title || 'New chat';
  dom.chatSettings.hidden = false;
  renderSnapshot(data);
  renderChatList();
  setBusy(!!data.busy);
  connect();
  resetAutoScreen(state.busy ? 'Working…' : 'Tap the microphone and speak');
  const lastAssistant = [...(data.messages || [])].reverse().find((m) => m.role === 'assistant');
  if (lastAssistant) state.lastAnswer = lastAssistant.content;
  const pending = (data.questions || []).find((q) => q.status === 'pending');
  if (pending) state.pendingQuestion = pending;
  if (state.auto) {
    if (lastAssistant) showAutoAnswer(lastAssistant.content, false);
    if (pending) showAutoQuestion(pending, false);
  }
}

/* ------------------------------------------------------------- rendering */

function showEmptyState() {
  dom.threadInner.innerHTML = '';
  closeAllTerminalStreams();
  state.stepEls.clear();
  state.stepData.clear();
  state.turnEls.clear();
  const wrap = el('div', { class: 'empty' },
    el('h2', { text: 'What should we work on?' }),
    el('p', { text: 'Socrates plans the work and hands it to Claude Code, Codex or OpenCode.' }),
  );
  const suggestions = el('div', { class: 'suggestions' });
  for (const text of [
    'Research how this repository is structured',
    'Fix the failing tests and explain what was wrong',
    'Add a README with setup instructions',
  ]) {
    suggestions.append(el('button', { type: 'button', text, onclick: () => submitText(text) }));
  }
  wrap.append(suggestions);
  dom.threadInner.append(wrap);
}

function renderSnapshot(data) {
  dom.threadInner.innerHTML = '';
  closeAllTerminalStreams();
  state.stepEls.clear();
  state.stepData.clear();
  state.turnEls.clear();
  state.optimisticUser = null;

  for (const message of data.messages || []) addMessage(message, false);
  for (const step of data.steps || []) upsertStep(step, false);
  scrollToEnd(true);
}

function ensureTurn(runId) {
  if (!runId) runId = 'orphan';
  let turn = state.turnEls.get(runId);
  if (turn) return turn;
  turn = el('div', { class: 'turn', 'data-run': runId }, el('div', { class: 'process' }));
  state.turnEls.set(runId, turn);
  dom.threadInner.append(turn);
  return turn;
}

function addMessage(message, animate = true) {
  const empty = dom.threadInner.querySelector('.empty');
  if (empty) empty.remove();

  if (message.role === 'user') {
    if (state.optimisticUser) {
      // adopt the bubble we rendered before the server confirmed
      state.optimisticUser.dataset.msg = message.id;
      state.optimisticUser = null;
      ensureTurn(message.run_id);
      return;
    }
    if (dom.threadInner.querySelector('[data-msg="' + message.id + '"]')) return;
    const node = el('div', { class: 'msg user', 'data-msg': message.id, text: message.content });
    if (!animate) node.style.animation = 'none';
    dom.threadInner.append(node);
    ensureTurn(message.run_id);
    scrollToEnd();
    return;
  }

  if (dom.threadInner.querySelector('[data-msg="' + message.id + '"]')) return;
  const node = el('div', { class: 'msg assistant', 'data-msg': message.id },
    el('div', { class: 'md', html: renderMarkdown(message.content) }));
  if (!animate) node.style.animation = 'none';
  ensureTurn(message.run_id).append(node);
  scrollToEnd();
}

function insertBySeq(container, node, seq) {
  node.dataset.seq = seq;
  const sibling = [...container.children].find((child) => Number(child.dataset.seq) > Number(seq));
  container.insertBefore(node, sibling || null);
}

function upsertStep(step, animate = true) {
  state.stepData.set(step.id, step);
  const turn = ensureTurn(step.run_id);
  let container = turn.querySelector(':scope > .process');
  if (step.parent_id) {
    const parentNode = state.stepEls.get(step.parent_id);
    const children = parentNode && parentNode.querySelector(':scope > .children');
    if (children) container = children;
  }

  const existing = state.stepEls.get(step.id);
  // A terminal step redraws constantly. Rebuilding its node would throw away
  // the scroll position and the caret of whoever is typing into it, so the
  // existing one is updated in place.
  if (existing && step.kind === 'terminal') {
    updateTerminal(existing, step);
    return;
  }
  const node = buildStep(step);
  if (!animate) node.style.animation = 'none';
  if (existing) {
    node.dataset.seq = existing.dataset.seq || step.seq;
    existing.replaceWith(node);
  } else {
    insertBySeq(container, node, step.seq);
  }
  state.stepEls.set(step.id, node);
  scrollToEnd();
}

function detailOf(step) {
  if (!step.detail) return {};
  if (typeof step.detail === 'object') return step.detail;
  try { return JSON.parse(step.detail); } catch { return {}; }
}

function statusIcon(status) {
  if (status === 'running' || status === 'pending') return el('span', { class: 'step-icon' }, el('span', { class: 'spinner' }));
  if (status === 'failed') return el('span', { class: 'step-icon cross', html: ICONS.cross });
  if (status === 'interrupted' || status === 'cancelled') return el('span', { class: 'step-icon', html: ICONS.cross });
  return el('span', { class: 'step-icon tick', html: ICONS.check });
}

function toggleStep(node, id) {
  node.classList.toggle('open');
  state.touched.add(id);
  if (node.classList.contains('open')) state.expanded.add(id);
  else state.expanded.delete(id);
}

function buildStep(step) {
  const detail = detailOf(step);
  switch (step.kind) {
    case 'terminal': return buildTerminal(step, detail);
    case 'shell': return buildShell(step, detail);
    case 'question': return buildQuestion(step, detail);
    case 'error': return el('div', { class: 'step error-step', 'data-step': step.id },
      el('div', { text: step.title || 'Error' }),
      step.body ? el('div', { style: 'margin-top:4px;opacity:.85', text: step.body }) : null);
    case 'thinking': return buildCollapsible(step, 'Reasoning', step.body, ICONS.spark);
    case 'text': {
      const node = el('div', { class: 'step text-step', 'data-step': step.id },
        el('div', { class: 'body md', html: renderMarkdown(step.body) }));
      return node;
    }
    default: return buildSubLine(step, detail);
  }
}

function buildCollapsible(step, label, body, iconHtml) {
  const node = el('div', { class: 'step collapsible', 'data-step': step.id });
  const head = el('div', { class: 'head' },
    el('span', { class: 'step-icon', html: iconHtml || ICONS.dot }),
    el('span', { class: 'name', text: label }),
    el('span', { class: 'chev', html: ICONS.chev }),
  );
  head.addEventListener('click', () => toggleStep(node, step.id));
  node.append(head, el('div', { class: 'body', text: body || '' }));
  if (state.expanded.has(step.id)) node.classList.add('open');
  return node;
}

function subTag(step) {
  switch (step.kind) {
    case 'sub_thinking': return 'thinking';
    case 'sub_text': return 'message';
    case 'sub_tool': return step.title || 'tool';
    case 'sub_error': return 'error';
    case 'sub_log': return 'log';
    default: return step.title || 'status';
  }
}

function buildSubLine(step, detail) {
  const node = el('div', { class: 'step collapsible', 'data-step': step.id });
  const value = (step.body || step.title || '').split('\n')[0] || '';
  const extraParts = [];
  if (detail.input) extraParts.push(typeof detail.input === 'string' ? detail.input : JSON.stringify(detail.input, null, 2));
  if (step.body && step.body.includes('\n')) extraParts.push(step.body);
  if (detail.result) extraParts.push(String(detail.result));
  if (detail.command && detail.command !== value) extraParts.push(detail.command);
  const extra = extraParts.join('\n\n');

  const classes = ['sub-line'];
  if (step.kind === 'sub_log') classes.push('log');
  if (step.kind === 'sub_error') classes.push('err');

  const head = el('div', { class: classes.join(' ') },
    step.status === 'running' ? el('span', { class: 'spinner' }) : null,
    el('span', { class: 'tag', text: subTag(step) }),
    el('span', { class: 'val', text: value }),
  );
  node.append(head);
  if (extra.trim()) {
    head.style.cursor = 'pointer';
    head.addEventListener('click', () => toggleStep(node, step.id));
    node.append(el('div', { class: 'body code', text: extra }));
    if (state.expanded.has(step.id)) node.classList.add('open');
  }
  return node;
}

// A terminal step shows the live screen of a session, and lets the user take
// the keyboard: Socrates and the person type into the same program.
function buildTerminal(step, detail) {
  const node = el('div', { class: 'step term', 'data-step': step.id });
  const dot = el('span', { class: 'term-dot' });
  const name = el('span', { class: 'nm', text: step.title || 'terminal' });
  const cmd = el('span', { class: 'cmd', text: detail.command || '' });
  const meta = el('span', { class: 'meta', style: 'font-size:11px;color:#8d9099' });
  const head = el('div', { class: 'term-head' }, dot, name, cmd, meta);

  const screen = el('pre', { class: 'term-screen', text: step.body || '' });

  const input = el('input', {
    type: 'text',
    placeholder: 'Type here to take over…',
    spellcheck: 'false',
    autocomplete: 'off',
  });
  const send = (text, keys) => {
    const session = detailOf(state.stepData.get(step.id) || step).session;
    if (!session) return;
    api('/api/terminals/' + encodeURIComponent(session) + '/input', {
      method: 'POST',
      body: { text: text || '', keys: keys || [] },
    }).catch((err) => toast(err.message, 'error'));
  };
  const form = el('form', {
    class: 'term-foot',
    onsubmit: (event) => {
      event.preventDefault();
      send(input.value, ['enter']);
      input.value = '';
    },
  }, input);
  for (const key of ['enter', 'escape', 'up', 'down', 'ctrl+c']) {
    form.append(el('button', {
      class: 'term-key', type: 'button', title: 'Press ' + key, text: key,
      onclick: () => send('', [key]),
    }));
  }

  node.append(head, screen, form,
    el('div', { class: 'term-note', text: 'Socrates is driving this session. Anything you type goes to the same program.' }));
  updateTerminal(node, step);
  watchTerminal(node, step);
  return node;
}

function updateTerminal(node, step) {
  state.stepData.set(step.id, step);
  const detail = detailOf(step);
  const screen = node.querySelector(':scope > .term-screen');
  const wasAtBottom = screen.scrollHeight - screen.scrollTop - screen.clientHeight < 24;
  if (screen.textContent !== (step.body || '')) {
    screen.textContent = step.body || '';
    if (wasAtBottom) screen.scrollTop = screen.scrollHeight;
  }
  const running = detail.running !== false && step.status === 'running';
  const dot = node.querySelector('.term-dot');
  dot.className = 'term-dot' + (running ? '' : (step.status === 'failed' ? ' failed' : ' stopped'));
  const meta = node.querySelector('.term-head .meta');
  meta.textContent = running ? 'running'
    : (detail.exit_code ? 'exited ' + detail.exit_code : 'exited');
  node.querySelector('.term-foot').hidden = !running;
}

// watchTerminal subscribes to the session's own stream, which is far quicker
// than the once a second screen that arrives with the chat events.
function watchTerminal(node, step) {
  const session = detailOf(step).session;
  if (!session || state.termStreams.has(session)) return;
  const source = new EventSource('/api/terminals/' + encodeURIComponent(session) + '/events');
  state.termStreams.set(session, source);
  source.onmessage = (event) => {
    let payload = null;
    try { payload = JSON.parse(event.data); } catch { return; }
    const terminal = payload && payload.terminal;
    if (!terminal) return;
    const current = state.stepData.get(step.id) || step;
    updateTerminal(node, Object.assign({}, current, {
      body: terminal.screen,
      status: terminal.running ? 'running' : (terminal.exit_code ? 'failed' : 'done'),
      detail: Object.assign({}, detailOf(current), {
        running: terminal.running,
        exit_code: terminal.exit_code,
      }),
    }));
    if (!terminal.running) closeTerminalStream(session);
  };
  source.onerror = () => closeTerminalStream(session);
}

function closeAllTerminalStreams() {
  for (const source of state.termStreams.values()) source.close();
  state.termStreams.clear();
}

function closeTerminalStream(session) {
  const source = state.termStreams.get(session);
  if (!source) return;
  source.close();
  state.termStreams.delete(session);
}

// A shell command is a one shot: the command line and what it printed.
function buildShell(step, detail) {
  const node = el('div', { class: 'step collapsible', 'data-step': step.id });
  const head = el('div', { class: 'sub-line' },
    step.status === 'running' ? el('span', { class: 'spinner' }) : null,
    el('span', { class: 'tag', text: 'shell' }),
    el('span', { class: 'val', text: detail.command || step.title || '' }),
    detail.exit_code ? el('span', { class: 'meta', text: 'exit ' + detail.exit_code }) : null,
  );
  head.style.cursor = 'pointer';
  head.addEventListener('click', () => toggleStep(node, step.id));
  node.append(head, el('div', { class: 'body code', text: step.body || '(no output)' }));
  if (state.expanded.has(step.id)) node.classList.add('open');
  return node;
}

function buildQuestion(step, detail) {
  const node = el('div', { class: 'step question', 'data-step': step.id });
  const answered = step.status !== 'pending';
  if (answered) node.classList.add('answered');
  node.append(el('div', { class: 'q-label', text: detail.kind === 'permission' ? 'Permission needed' : 'Your input' }));
  node.append(el('div', { class: 'q-text', text: step.body || '' }));

  if (answered) {
    const label = detail.answer || (step.status === 'cancelled' ? 'cancelled' : '—');
    node.append(el('div', { class: 'answered-note' }, 'You answered: ', el('b', { text: label })));
    return node;
  }

  const options = el('div', { class: 'options' });
  for (const option of detail.options || []) {
    options.append(el('button', { class: 'opt', type: 'button', onclick: () => answerQuestion(detail.question_id, option.value || option.label) },
      el('div', { class: 'l', text: option.label }),
      option.description ? el('div', { class: 'd', text: option.description }) : null,
    ));
  }
  node.append(options);
  if (detail.free_text !== false) {
    const input = el('input', { class: 'input', type: 'text', placeholder: 'Type your answer…' });
    const form = el('form', {
      class: 'free',
      onsubmit: (event) => {
        event.preventDefault();
        const value = input.value.trim();
        if (value) answerQuestion(detail.question_id, value);
      },
    }, input, el('button', { class: 'btn sm primary', type: 'submit', text: 'Send' }));
    node.append(form);
  }
  return node;
}

async function answerQuestion(questionId, value) {
  if (!questionId) {
    toast('This question can no longer be answered.', 'error');
    return;
  }
  try {
    await api('/api/questions/' + questionId + '/answer', { method: 'POST', body: { value } });
    state.pendingQuestion = null;
    hideAutoQuestion();
    updateMicState();
    setAutoStatus('Working…');
  } catch (err) {
    toast(err.message, 'error');
  }
}

function toggleChatPanel() {
  if (!state.chat) return;
  const opening = dom.chatPanel.hidden;
  dom.chatPanel.hidden = !opening;
  if (!opening) return;
  dom.panelTitle.value = state.chat.title || '';
  dom.panelWorkspace.value = state.chat.workspace || '';
  dom.panelWorkspace.placeholder = state.effectiveWorkspace || '';
  dom.panelTitle.focus();
}

async function saveChatSettings() {
  if (!state.chat) return;
  try {
    const data = await api('/api/chats/' + state.chat.id, {
      method: 'PATCH',
      body: { title: dom.panelTitle.value.trim(), workspace: dom.panelWorkspace.value.trim() },
    });
    state.chat = data.chat;
    dom.title.textContent = data.chat.title || 'New chat';
    dom.chatPanel.hidden = true;
    await refreshChats();
    toast('Chat updated');
  } catch (err) {
    toast(err.message, 'error');
  }
}

function scrollToEnd(force = false) {
  const gap = dom.thread.scrollHeight - dom.thread.scrollTop - dom.thread.clientHeight;
  if (force || gap < 160) {
    requestAnimationFrame(() => { dom.thread.scrollTop = dom.thread.scrollHeight; });
  }
}

/* -------------------------------------------------------------- streaming */

function connect() {
  disconnect();
  if (!state.chatId) return;
  const source = new EventSource('/api/chats/' + state.chatId + '/events?rev=' + state.rev);
  source.onmessage = (event) => {
    let payload;
    try { payload = JSON.parse(event.data); } catch { return; }
    handleEvent(payload);
  };
  source.onerror = () => {
    source.close();
    if (state.es === source) {
      state.es = null;
      setTimeout(() => { if (state.chatId && !state.es) connect(); }, 1500);
    }
  };
  state.es = source;
}

function disconnect() {
  if (state.es) {
    state.es.close();
    state.es = null;
  }
}

function handleEvent(event) {
  switch (event.type) {
    case 'step':
      state.rev = Math.max(state.rev, event.step.rev || 0);
      upsertStep(event.step);
      updateAutoLive(event.step);
      break;
    case 'step_removed': {
      const node = state.stepEls.get(event.step_id);
      if (node) node.remove();
      state.stepEls.delete(event.step_id);
      state.stepData.delete(event.step_id);
      break;
    }
    case 'message':
      addMessage(event.message);
      if (event.message.role === 'assistant') {
        state.lastAnswer = event.message.content;
        if (state.auto) {
          showAutoAnswer(event.message.content, state.prefs.speak_in_auto_mode !== false);
        } else if (state.prefs.speak_in_chat_mode) {
          speak(event.message.content, { rate: state.prefs.tts_rate, lang: state.prefs.tts_language }).catch(() => {});
        }
      }
      break;
    case 'run':
      setBusy(event.run.status === 'running' || event.run.status === 'waiting_input');
      if (event.run.status === 'failed' && event.run.error) toast(event.run.error, 'error');
      if (!state.busy) setAutoStatus(state.lastAnswer ? '' : 'Tap the microphone and speak');
      break;
    case 'chat':
      state.chat = event.chat;
      dom.chatSettings.hidden = false;
      dom.title.textContent = event.chat.title || 'New chat';
      refreshChats();
      break;
    case 'question':
      if (event.question.status === 'pending') {
        state.pendingQuestion = event.question;
        updateMicState();
        if (state.auto) showAutoQuestion(event.question, state.prefs.speak_in_auto_mode !== false);
      } else if (state.pendingQuestion && state.pendingQuestion.id === event.question.id) {
        state.pendingQuestion = null;
        hideAutoQuestion();
        updateMicState();
      }
      break;
    case 'ready':
      state.rev = Math.max(state.rev, event.rev || 0);
      // Anything that arrived while the stream was down is replayed here.
      for (const message of event.messages || []) addMessage(message, false);
      setBusy(!!event.busy);
      break;
    case 'resync':
      connect();
      break;
    default:
      break;
  }
}

function setBusy(busy) {
  state.busy = busy;
  dom.sendBtn.disabled = busy || !dom.input.value.trim();
  dom.stopBtn.hidden = !busy;
  updateMicState();
  const active = state.chats.find((c) => c.id === state.chatId);
  if (active) renderChatList();
}

// The microphone stays usable while a question is waiting: the answer can be
// spoken instead of clicked.
function updateMicState() {
  const blocked = state.busy && !state.pendingQuestion;
  dom.autoMic.classList.toggle('busy', blocked);
  dom.autoMic.disabled = blocked;
  dom.micBtn.disabled = blocked;
}

async function stopRun() {
  if (!state.chatId) return;
  try {
    await api('/api/chats/' + state.chatId + '/stop', { method: 'POST' });
    setAutoStatus('Stopped');
  } catch (err) {
    toast(err.message, 'error');
  }
}

/* --------------------------------------------------------------- sending */

async function submitText(raw) {
  const text = (raw || '').trim();
  if (!text || state.busy) return;
  dom.input.value = '';
  autosize();
  dom.sendBtn.disabled = true;

  try {
    if (!state.chatId) {
      const created = await api('/api/chats', { method: 'POST', body: {} });
      state.chatId = created.chat.id;
      state.chat = created.chat;
      state.effectiveWorkspace = '';
      dom.chatSettings.hidden = false;
      state.rev = 0;
      location.hash = state.chatId;
      await refreshChats();
      connect();
    }
    const empty = dom.threadInner.querySelector('.empty');
    if (empty) empty.remove();
    const bubble = el('div', { class: 'msg user', text });
    dom.threadInner.append(bubble);
    state.optimisticUser = bubble;
    scrollToEnd(true);

    await api('/api/chats/' + state.chatId + '/messages', { method: 'POST', body: { text, auto: state.auto } });
    setBusy(true);
    setAutoStatus('Working…');
  } catch (err) {
    toast(err.message, 'error');
    if (state.optimisticUser) {
      state.optimisticUser.remove();
      state.optimisticUser = null;
    }
    setBusy(false);
  }
}

/* ----------------------------------------------------------------- voice */

async function toggleRecording(origin) {
  if (state.recorder.recording) {
    await finishRecording(origin);
    return;
  }
  if (state.busy && !state.pendingQuestion) {
    toast('Wait until the current run is finished.');
    return;
  }
  stopSpeaking();
  try {
    await state.recorder.start();
  } catch (err) {
    toast(err.message, 'error');
    return;
  }
  if (origin === 'auto') {
    dom.autoScreen.classList.remove('answering');
    dom.autoAnswer.hidden = true;
    dom.autoActions.hidden = true;
    dom.autoMic.classList.add('recording');
    dom.autoMic.innerHTML = ICONS.stop;
    dom.autoTimer.hidden = false;
    dom.autoTranscript.hidden = true;
    setAutoStatus('Listening…');
  } else {
    dom.micBtn.classList.add('rec');
    dom.micBtn.innerHTML = ICONS.stop;
    dom.recTime.hidden = false;
  }
  state.recTimer = setInterval(() => {
    const label = fmtClock(state.recorder.seconds);
    dom.recTime.textContent = label;
    dom.autoTimer.textContent = label;
  }, 200);
}

async function finishRecording(origin) {
  clearInterval(state.recTimer);
  state.recTimer = null;
  const result = await state.recorder.stop();
  resetRecordingUI();
  if (!result) {
    setAutoStatus('I did not hear anything');
    return;
  }
  if (result.seconds < 0.4) {
    toast('That was too short.');
    setAutoStatus('Tap the microphone and speak');
    return;
  }

  if (origin === 'auto') setAutoStatus('Transcribing…');
  else toast('Transcribing…');

  let text = '';
  try {
    const data = await api('/api/voice/transcribe', { method: 'POST', body: { audio: result.base64, format: result.format } });
    text = (data && data.text) || '';
  } catch (err) {
    toast(err.message, 'error');
    setAutoStatus('Transcription failed');
    return;
  }
  if (!text) {
    setAutoStatus('I did not catch that');
    return;
  }

  if (state.pendingQuestion) {
    const value = matchOption(text, state.pendingQuestion.options || []);
    await answerQuestion(state.pendingQuestion.id, value);
    return;
  }

  if (origin === 'auto') {
    dom.autoTranscript.hidden = false;
    dom.autoTranscript.textContent = '“' + text + '”';
    dom.autoAnswer.hidden = true;
    dom.autoActions.hidden = true;
    dom.autoTimer.hidden = true;
    await submitText(text);
  } else {
    dom.input.value = dom.input.value ? dom.input.value + ' ' + text : text;
    autosize();
    dom.sendBtn.disabled = !dom.input.value.trim() || state.busy;
    dom.input.focus();
  }
}

function resetRecordingUI() {
  dom.micBtn.classList.remove('rec');
  dom.micBtn.innerHTML = ICONS.mic;
  dom.recTime.hidden = true;
  dom.autoMic.classList.remove('recording');
  dom.autoMic.innerHTML = ICONS.mic;
  dom.autoTimer.hidden = true;
}

// matchOption maps a spoken answer onto one of the offered options.
function matchOption(spoken, options) {
  const text = spoken.toLowerCase().trim();
  if (!options.length) return spoken;
  const numeric = text.match(/^(?:option\s*)?(\d)/);
  if (numeric) {
    const index = Number(numeric[1]) - 1;
    if (options[index]) return options[index].value || options[index].label;
  }
  for (const option of options) {
    const label = (option.label || '').toLowerCase();
    if (label && (text.includes(label) || label.includes(text))) return option.value || option.label;
  }
  return spoken;
}

/* ------------------------------------------------------------- auto mode */

function setAuto(on, silent = false) {
  state.auto = on;
  localStorage.setItem('socrates.auto', on ? '1' : '0');
  document.body.classList.toggle('auto', on);
  dom.autoToggle.checked = on;
  if (!on) {
    stopSpeaking();
    return;
  }
  if (!silent && state.lastAnswer) showAutoAnswer(state.lastAnswer, false);
}

function setAutoStatus(text) {
  dom.autoStatus.textContent = text;
}

function resetAutoScreen(status) {
  dom.autoScreen.classList.remove('answering');
  setAutoStatus(status || '');
  dom.autoAnswer.hidden = true;
  dom.autoQuestion.hidden = true;
  dom.autoActions.hidden = true;
  dom.autoTranscript.hidden = true;
  dom.autoLive.textContent = '';
}

function updateAutoLive(step) {
  if (!state.auto) return;
  const detail = detailOf(step);
  let label = '';
  switch (step.kind) {
    case 'terminal':
      label = (step.title || 'A program') + (step.status === 'running' ? ' is working…' : ' finished');
      break;
    case 'shell': label = 'Running ' + (detail.command || 'a command') + '…'; break;
    case 'thinking': label = 'Thinking…'; break;
    case 'text': label = 'Writing the answer…'; break;
    case 'sub_tool': label = (step.title || 'tool') + ': ' + (step.body || '').split('\n')[0]; break;
    case 'sub_thinking': label = 'Reasoning…'; break;
    case 'error': label = step.title || 'Something went wrong'; break;
    default: return;
  }
  dom.autoLive.textContent = label;
  if (state.busy) setAutoStatus('Working…');
}

// plainAnswer strips markdown so the big auto mode text stays readable.
function plainAnswer(text) {
  return String(text || '')
    .replace(/```[\s\S]*?```/g, (block) => block.replace(/```\w*\n?/g, '').trim())
    .replace(/`([^`]+)`/g, '$1')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/(^|\s)\*([^*\n]+)\*/g, '$1$2')
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/^\s*[-*+]\s+/gm, '\u2022 ')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

function showAutoAnswer(text, doSpeak) {
  dom.autoQuestion.hidden = true;
  dom.autoTranscript.hidden = true;
  dom.autoLive.textContent = '';
  setAutoStatus('');
  dom.autoAnswer.hidden = false;
  dom.autoAnswer.textContent = plainAnswer(text);
  dom.autoActions.hidden = false;
  dom.autoScreen.classList.add('answering');
  fitAnswer();
  if (doSpeak) speak(text, { rate: state.prefs.tts_rate, lang: state.prefs.tts_language }).catch(() => {});
}

function fitAnswer() {
  const node = dom.autoAnswer;
  const length = node.textContent.length;
  const size = length < 90 ? 46 : length < 220 ? 38 : length < 480 ? 30 : 24;
  node.style.fontSize = 'clamp(20px, ' + (size / 14) + 'vw, ' + size + 'px)';
}

function showAutoQuestion(question, doSpeak) {
  state.pendingQuestion = question;
  dom.autoScreen.classList.add('answering');
  dom.autoAnswer.hidden = true;
  dom.autoActions.hidden = true;
  dom.autoLive.textContent = '';
  setAutoStatus('Your decision');
  dom.autoQuestion.hidden = false;
  dom.autoQuestion.innerHTML = '';
  dom.autoQuestion.append(el('div', { class: 'q', text: question.question }));
  const options = el('div', { class: 'opts' });
  (question.options || []).forEach((option, index) => {
    options.append(el('button', {
      type: 'button',
      onclick: () => answerQuestion(question.id, option.value || option.label),
    }, el('span', { class: 'num', text: (index + 1) + '.' }), option.label));
  });
  dom.autoQuestion.append(options);
  if (doSpeak) {
    const spoken = [plainSpeech(question.question)];
    (question.options || []).forEach((option, index) => {
      spoken.push('Option ' + (index + 1) + ': ' + option.label + '.');
    });
    speak(spoken.join(' '), { rate: state.prefs.tts_rate, lang: state.prefs.tts_language }).catch(() => {});
  }
}

function hideAutoQuestion() {
  dom.autoQuestion.hidden = true;
  dom.autoQuestion.innerHTML = '';
}
