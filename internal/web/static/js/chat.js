// The chat page: sidebar, live process view, composer, voice and auto mode.

import {
  api, el, toast, confirmDialog, fmtDuration, fmtClock, isOffline, errorMessage,
  LiveStream, Outbox, clientKey, onWake, HttpError, RetryLater,
} from './api.js';
import { renderMarkdown } from './markdown.js';
import { mountTerminalDock } from './terminals.js';
import { Recorder, speak, stopSpeaking, isSpeaking, plainSpeech } from './voice.js';

const $ = (id) => document.getElementById(id);

// The terminal sessions of this chat live beside the conversation, not inside
// it. The transcript keeps one line per session; the dock keeps the screen.
const dock = mountTerminalDock();

const dom = {
  sidebar: $('sidebar'),
  chatList: $('chatList'),
  thread: $('thread'),
  threadInner: $('threadInner'),
  title: $('chatTitle'),
  composer: $('composer'),
  composerNote: $('composerNote'),
  askPanel: $('askPanel'),
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
  autoBusy: $('autoBusy'),
  autoOffline: $('autoOffline'),
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
  serverBusy: false,
  auto: readFlag('socrates.auto'),
  // The chat event stream, and whether it is actually delivering. Everything
  // on screen is only as true as this flag.
  stream: null,
  live: false,
  lastSync: 0,
  // Queues that survive a reload: nothing the person did is lost because the
  // connection went away between tapping and landing.
  outbox: null,
  answers: null,
  reopenTimer: null,
  loadFailed: false,
  stepEls: new Map(),
  stepData: new Map(),
  chatEls: new Map(),
  turnEls: new Map(),
  expanded: new Set(),
  touched: new Set(),
  pendingQuestion: null,
  // The answer that has just been given and is on its way out. It keeps the
  // question panel on screen for a moment longer, saying so, instead of the
  // composer snapping back as if nothing had happened.
  askSending: null,
  askTimer: null,
  askShown: null,
  // Who asked what, learned from the question rows. The step a question leaves
  // behind does not carry it, so the record in the transcript would otherwise
  // forget whose question it was the moment it was answered.
  questionSources: new Map(),
  spokeOffline: false,
  lastAnswer: '',
  autoPhase: 'idle',
  recorder: new Recorder(),
  recTimer: null,
  // The always on "something is happening" row: what it says, when it started
  // and the ticker that keeps its clock moving.
  workLabel: '',
  workSince: 0,
  workTimer: null,
  prefs: { speak_in_auto_mode: true, speak_in_chat_mode: false, tts_rate: 1, language: 'en' },
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

/* -------------------------------------------------------- local storage */

// Local storage throws in a few real situations - private windows, a browser
// set to block site data - and none of them are a reason for the chat to stop
// working. Every access goes through these.

function readFlag(key) {
  try { return localStorage.getItem(key) === '1'; } catch { return false; }
}

function writeValue(key, value) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch { /* nothing to do about it */ }
}

function readValue(key) {
  try { return localStorage.getItem(key) || ''; } catch { return ''; }
}

// The composer is a draft until it is sent. Losing signal mid sentence, or the
// browser dropping the tab to save memory, must not cost what was typed.
function draftKey() {
  return 'socrates.draft.' + (state.chatId || 'new');
}

// The last known title of a chat. Reopening with no signal cannot ask the
// server what this conversation is called, and calling it "New chat" would be
// simply wrong.
function titleKey(id) {
  return 'socrates.title.' + id;
}

function saveDraft() {
  writeValue(draftKey(), dom.input.value);
}

function restoreDraft() {
  dom.input.value = readValue(draftKey());
  autosize();
  updateSendButton();
}

/* ------------------------------------------------------------- bootstrap */

// BOOT is the request budget for everything the page needs before it can show
// itself. Two quick attempts, then give up and let the retry loops take over -
// a blank screen for fifteen seconds is worse than an honest one after two.
const BOOT = { attempts: 2, timeout: 8000 };

// speechOptions is how every spoken line is configured: the rate and the
// language the admin chose.
function speechOptions() {
  return { rate: state.prefs.tts_rate, lang: state.prefs.language };
}

// The one sentence Socrates says on its own behalf, in both languages it can
// be set to. It is spoken when the connection drops, which is exactly when
// nothing can be fetched to translate it.
const OFFLINE_NOTICE = {
  en: 'The connection dropped. I will keep trying.',
  de: 'Die Verbindung ist weg. Ich versuche es weiter.',
};

async function init() {
  setAuto(state.auto, true);
  buildQueues();
  bindUI();
  // Preferences are a nicety and the defaults are sensible, so the page is
  // never held up waiting for them. Everything on the boot path is deliberately
  // impatient: on a bad connection what matters is that the app appears and
  // says what is wrong, not that it eventually loads everything.
  api('/api/preferences', BOOT)
    .then((prefs) => { if (prefs) state.prefs = prefs; })
    .catch(() => { /* defaults are fine */ });
  await refreshChats(BOOT);
  // The address bar is the more reliable of the two: reopening the app with no
  // signal cannot fetch the chat list, and falling back to a blank new chat
  // there would lose both the place and the draft that belongs to it.
  const fromHash = location.hash.replace(/^#/, '');
  if (fromHash) await openChat(fromHash);
  else if (state.chats.length) await openChat(state.chats[0].id);
  else {
    showEmptyState();
    restoreDraft();
  }
}

// boot keeps trying. Opening the app in a tunnel or a dead spot used to leave
// an empty page that never healed; now it says so and comes back on its own as
// soon as there is a connection again.
function boot(attempt = 1) {
  init().catch((err) => {
    if (!isOffline(err)) {
      toast(errorMessage(err), 'error');
      return;
    }
    showLoadError('Socrates could not be reached.', () => boot(1));
    const delay = Math.min(15000, 1500 * attempt);
    clearTimeout(state.reopenTimer);
    state.reopenTimer = setTimeout(() => boot(attempt + 1), delay);
  });
}

// buildQueues wires the two things a person can hand to Socrates - a message
// and an answer to a question - onto durable queues. Both carry a key that the
// server recognises, so a retry after a dropped connection is a no-op rather
// than a second message.
function buildQueues() {
  state.outbox = new Outbox('messages', sendQueuedMessage);
  state.answers = new Outbox('answers', sendQueuedAnswer);
  state.outbox.onChange(renderPending);
  state.answers.onChange(() => {
    refreshQuestionSteps();
    updateWorkRow();
    updateLiveUI();
  });
}

// sendQueuedMessage is the whole "say something" transaction: create the chat
// if there is not one yet, then post the message. Both halves are keyed, so
// the pair can be repeated from the start without duplicating anything.
async function sendQueuedMessage(payload, item) {
  if (!payload.chatId) {
    const created = await api('/api/chats', { method: 'POST', body: { client_id: payload.chatKey } });
    payload.chatId = created.chat.id;
    state.outbox.persist();
    adoptCreatedChat(created.chat, item);
  }
  try {
    await api('/api/chats/' + payload.chatId + '/messages', {
      method: 'POST',
      body: { text: payload.text, auto: payload.auto, client_id: payload.key },
    });
  } catch (err) {
    // The chat is busy with the previous turn. That passes on its own, so the
    // message waits rather than being thrown back at the user.
    if (err instanceof HttpError && err.status === 409) {
      throw new RetryLater('Socrates is still finishing the previous message.');
    }
    throw err;
  }
}

async function sendQueuedAnswer(payload) {
  try {
    await api('/api/questions/' + payload.questionId + '/answer', {
      method: 'POST',
      body: { value: payload.value },
    });
  } catch (err) {
    // Already answered, or the question is gone with its run: either way there
    // is nothing left to deliver and the queue should let go of it.
    if (err instanceof HttpError && (err.status === 404 || err.status === 409)) return;
    throw err;
  }
}

// adoptCreatedChat moves the page onto the chat the queue just created, but
// only if the person is still looking at the blank one they typed into.
function adoptCreatedChat(chat, item) {
  const queued = state.outbox.items.filter((entry) => entry.id !== item.id && !entry.payload.chatId);
  for (const entry of queued) entry.payload.chatId = chat.id;
  state.outbox.persist();
  if (state.chatId) return;
  state.chatId = chat.id;
  if (dock) dock.setChat(chat.id);
  state.chat = chat;
  state.effectiveWorkspace = '';
  state.rev = 0;
  dom.chatSettings.hidden = false;
  location.hash = chat.id;
  connect();
  refreshChats();
}

function bindUI() {
  dom.composer.addEventListener('submit', (event) => {
    event.preventDefault();
    submitText(dom.input.value);
  });
  dom.input.addEventListener('input', () => {
    autosize();
    updateSendButton();
    saveDraft();
  });
  // The button under the composer is where a person looks for both actions, so
  // it sends while idle and stops while a run is going.
  dom.sendBtn.addEventListener('click', (event) => {
    if (!state.busy) return;
    event.preventDefault();
    stopRun();
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
    // Signing out locally is the part that matters; if the server could not be
    // told, the page still leaves rather than hanging on a spinner.
    try { await api('/api/logout', { method: 'POST', attempts: 2 }); } catch { /* leave anyway */ }
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
    else if (state.lastAnswer) speak(state.lastAnswer, speechOptions());
  });
  dom.autoDetails.addEventListener('click', () => setAuto(false));
  window.addEventListener('beforeunload', () => {
    saveDraft();
    disconnect();
  });
  // pagehide is the one that actually fires on iOS when the browser is put
  // away, which is exactly when a draft would otherwise be lost.
  window.addEventListener('pagehide', saveDraft);
  window.addEventListener('hashchange', () => {
    const id = location.hash.replace(/^#/, '');
    if (id && id !== state.chatId) openChat(id).catch((err) => toast(errorMessage(err), 'error'));
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') document.body.classList.remove('nav-open');
  });
  // Coming back from a locked phone: the sidebar and the chat are both refreshed
  // rather than trusted, because the stream may have been asleep for hours.
  onWake(() => {
    if (document.visibilityState === 'hidden') return;
    refreshChats();
    if (state.loadFailed && state.chatId) openChat(state.chatId, { force: true }).catch(() => {});
  });
  // The clock in the working row and the "how old is this" line have to keep
  // moving even when nothing arrives, because that is the whole point of them.
  setInterval(updateLiveUI, 1000);
}

function autosize() {
  dom.input.style.height = 'auto';
  dom.input.style.height = Math.min(dom.input.scrollHeight, 200) + 'px';
}

/* ----------------------------------------------------------------- chats */

// refreshChats never empties the sidebar because of a bad moment: a list that
// could not be fetched leaves the previous one alone.
async function refreshChats(options = {}) {
  try {
    const data = await api('/api/chats', options);
    state.chats = data.chats || [];
    renderChatList();
  } catch (err) {
    if (!isOffline(err)) throw err;
  }
}

// The sidebar is patched rather than rebuilt. Its rows carry the running dot,
// and a dot that is thrown away and made again every time a run event arrives
// restarts its animation - which is what made it look like it was flickering
// instead of pulsing.
function renderChatList() {
  if (!state.chats.length) {
    dom.chatList.innerHTML = '';
    state.chatEls.clear();
    dom.chatList.append(el('div', { class: 'group-label', text: 'No chats yet' }));
    return;
  }

  let label = dom.chatList.querySelector(':scope > .group-label');
  if (!label || label.textContent !== 'Chats') {
    dom.chatList.innerHTML = '';
    state.chatEls.clear();
    label = el('div', { class: 'group-label', text: 'Chats' });
    dom.chatList.append(label);
  }

  const live = new Set(state.chats.map((chat) => chat.id));
  for (const [id, node] of state.chatEls) {
    if (live.has(id)) continue;
    node.remove();
    state.chatEls.delete(id);
  }

  let previous = label;
  for (const chat of state.chats) {
    let item = state.chatEls.get(chat.id);
    if (!item) {
      item = buildChatItem(chat);
      state.chatEls.set(chat.id, item);
    }
    item.update(chat);
    if (previous.nextElementSibling !== item) previous.after(item);
    previous = item;
  }
}

function buildChatItem(chat) {
  const item = el('div', {
    class: 'chat-item',
    onclick: (event) => {
      if (event.target.closest('.del')) return;
      openChat(item.dataset.chat);
      document.body.classList.remove('nav-open');
    },
  });
  const dot = el('span', { class: 'dot', hidden: true, title: 'Working' });
  const label = el('span', { class: 'label' });
  const remove = el('button', {
    class: 'icon-btn del',
    title: 'Delete chat',
    html: ICONS.trash,
    onclick: (event) => {
      event.stopPropagation();
      const current = state.chats.find((c) => c.id === item.dataset.chat);
      if (current) deleteChat(current);
    },
  });
  item.append(dot, label, remove);
  item.update = (next) => {
    item.dataset.chat = next.id;
    const active = next.id === state.chatId;
    item.classList.toggle('active', active);
    const title = next.title || 'New chat';
    if (label.textContent !== title) label.textContent = title;
    dot.hidden = !(active && state.busy);
  };
  return item;
}

// markChatBusy only touches the running dots, so switching between busy and
// idle never rebuilds the list.
function markChatBusy() {
  for (const [id, item] of state.chatEls) {
    const dot = item.querySelector(':scope > .dot');
    if (dot) dot.hidden = !(id === state.chatId && state.busy);
  }
}

async function deleteChat(chat) {
  const ok = await confirmDialog({
    title: 'Delete this chat?',
    body: '"' + (chat.title || 'New chat') + '" and everything in it goes away. This cannot be undone.',
    confirmLabel: 'Delete chat',
    danger: true,
  });
  if (!ok) return;
  try {
    await api('/api/chats/' + chat.id, { method: 'DELETE', attempts: 3 });
  } catch (err) {
    toast(isOffline(err) ? 'No connection — the chat is still there.' : errorMessage(err), 'error');
    return;
  }
  writeValue(titleKey(chat.id), '');
  writeValue('socrates.draft.' + chat.id, '');
  if (state.chatId === chat.id) {
    disconnect();
    state.chatId = null;
    if (dock) dock.setChat(null);
    state.chat = null;
    showEmptyState();
  }
  await refreshChats();
  if (!state.chatId && state.chats.length) openChat(state.chats[0].id);
}

function startNewChat() {
  saveDraft();
  disconnect();
  state.chatId = null;
  if (dock) dock.setChat(null);
  state.chat = null;
  state.rev = 0;
  state.pendingQuestion = null;
  clearAskSending();
  updateAskPanel();
  state.questionSources.clear();
  state.lastAnswer = '';
  state.loadFailed = false;
  clearTimeout(state.reopenTimer);
  location.hash = '';
  dom.title.textContent = 'New chat';
  dom.chatSettings.hidden = true;
  dom.chatPanel.hidden = true;
  setBusy(false);
  showEmptyState();
  renderChatList();
  resetAutoScreen('Tap the microphone and speak');
  restoreDraft();
  dom.input.focus();
}

async function openChat(id, options = {}) {
  if (!options.force && state.chatId === id && state.stream && !state.loadFailed) return;
  saveDraft();
  disconnect();
  clearTimeout(state.reopenTimer);
  state.chatId = id;
  if (dock) dock.setChat(id);
  state.rev = 0;
  state.pendingQuestion = null;
  clearAskSending();
  updateAskPanel();
  state.questionSources.clear();
  state.lastAnswer = '';
  state.workSince = 0;
  state.workLabel = '';
  state.loadFailed = false;
  location.hash = id;

  let data;
  try {
    data = await api('/api/chats/' + id, { attempts: 2, timeout: 12000 });
  } catch (err) {
    // A chat that could not be loaded must not look like an empty one. It says
    // what happened, offers a retry, and takes one by itself when the network
    // comes back.
    if (!isOffline(err)) throw err;
    state.loadFailed = true;
    dom.title.textContent = readValue(titleKey(id)) || 'Chat';
    showLoadError('This chat could not be loaded.', () => openChat(id, { force: true }).catch(() => {}));
    // Whatever was half typed belongs to this chat, and it is still here.
    restoreDraft();
    renderPending();
    updateLiveUI();
    state.reopenTimer = setTimeout(() => openChat(id, { force: true }).catch(() => {}), 4000);
    return;
  }

  state.chat = data.chat;
  state.effectiveWorkspace = data.effective_workspace || '';
  state.rev = data.rev || 0;
  dom.title.textContent = data.chat.title || 'New chat';
  writeValue(titleKey(id), data.chat.title || '');
  dom.chatSettings.hidden = false;
  renderSnapshot(data);
  renderChatList();
  setBusy(!!data.busy);
  connect();
  restoreDraft();
  resetAutoScreen(state.busy ? 'Working…' : 'Tap the microphone and speak');
  const lastAssistant = [...(data.messages || [])].reverse().find((m) => m.role === 'assistant');
  if (lastAssistant) state.lastAnswer = lastAssistant.content;
  // Assigned either way: a chat with nothing pending must not inherit the
  // question that belonged to the one before it.
  for (const question of data.questions || []) rememberAsker(question);
  const pending = (data.questions || []).find((q) => q.status === 'pending') || null;
  state.pendingQuestion = pending;
  state.askSending = null;
  updateAskPanel();
  refreshQuestionSteps();
  if (state.auto) {
    if (lastAssistant) showAutoAnswer(lastAssistant.content, false);
    if (pending) showAutoQuestion(pending, false);
  }
}

// showLoadError replaces the thread with a plain explanation and a way out,
// instead of the blank screen a failed load used to leave behind.
function showLoadError(message, retry) {
  dom.threadInner.innerHTML = '';
  state.stepEls.clear();
  state.stepData.clear();
  state.turnEls.clear();
  const card = el('div', { class: 'empty load-error' },
    el('h2', { text: message }),
    el('p', { text: 'Socrates keeps trying on its own. Nothing you sent has been lost.' }),
    el('button', { class: 'btn primary', type: 'button', text: 'Try again now', onclick: () => retry() }),
  );
  dom.threadInner.append(card);
  renderPending();
}

/* ------------------------------------------------------------- rendering */

function showEmptyState() {
  dom.threadInner.innerHTML = '';
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
  renderPending();
  updateWorkRow();
}

function renderSnapshot(data) {
  dom.threadInner.innerHTML = '';
  state.stepEls.clear();
  state.stepData.clear();
  state.turnEls.clear();

  for (const message of data.messages || []) addMessage(message, false);
  for (const step of data.steps || []) upsertStep(step, false);
  // Whatever is still queued belongs at the end of the transcript it was typed
  // into, snapshot or not.
  renderPending();
  const last = (data.steps || [])[(data.steps || []).length - 1];
  state.workLabel = last ? workLabelFor(last) : '';
  updateWorkRow();
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
    // The bubble may already be on screen as a queued send. Adopting it keeps
    // the text in place instead of removing one node and appending an
    // identical one, and clears the "sending" line in the same breath.
    const pending = message.client_id ? findPending(message.client_id) : null;
    if (pending) {
      pending.dataset.msg = message.id;
      delete pending.dataset.pending;
      pending.classList.remove('pending', 'stuck');
      const line = pending.querySelector(':scope > .msg-state');
      if (line) line.remove();
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

/* --------------------------------------------------------- working row */

// While a run is going, one row is always on screen saying so. Steps come and
// go - the model thinks before it writes anything, a program starts before it
// prints anything - and in those gaps there used to be nothing at all to show
// that Socrates was still busy.
const workRow = buildWorkRow();

function buildWorkRow() {
  const node = el('div', { class: 'working' },
    el('span', { class: 'working-dots' }, el('i'), el('i'), el('i')),
    el('span', { class: 'working-label' }),
    el('span', { class: 'working-time' }),
  );
  // It always sorts after every step, so a step that arrives later is still
  // inserted above it.
  node.dataset.seq = String(Number.MAX_SAFE_INTEGER);
  return node;
}

// workRowHome is the process list of the newest turn, so the row lines up with
// the steps above it, or the thread itself before the first turn exists.
function workRowHome() {
  const siblings = [...dom.threadInner.children].filter((child) => child !== workRow);
  const last = siblings[siblings.length - 1];
  if (last && last.classList.contains('turn')) {
    const process = last.querySelector(':scope > .process');
    if (process) return process;
  }
  return dom.threadInner;
}

// queuedHere counts the messages waiting to be delivered for the open chat.
// They keep the working row on screen even before the server knows about them.
function queuedHere() {
  if (!state.outbox) return 0;
  return state.outbox.items.filter((item) => sameChat(item.payload.chatId) && item.state !== 'failed').length;
}

// queuedAnswer is the answer a person has already given that has not reached
// the server yet. The card has to show it, or the buttons look untouched and
// the same decision gets made twice.
function queuedAnswer(questionId) {
  if (!state.answers || !questionId) return null;
  const item = state.answers.items.find((entry) => entry.payload.questionId === questionId);
  return item ? item : null;
}

function queuedAnswersHere() {
  if (!state.answers) return 0;
  return state.answers.items.filter((item) => sameChat(item.payload.chatId) && item.state !== 'failed').length;
}

// refreshQuestionSteps redraws the question cards after the answer queue moved.
function refreshQuestionSteps() {
  for (const [id, node] of state.stepEls) {
    const step = state.stepData.get(id);
    if (step && step.kind === 'question' && typeof node.update === 'function') node.update(step);
  }
  updateAskPanel();
}

function updateWorkRow() {
  if (!state.busy && !queuedHere() && !queuedAnswersHere()) {
    if (state.workTimer) {
      clearInterval(state.workTimer);
      state.workTimer = null;
    }
    state.workSince = 0;
    state.workLabel = '';
    workRow.remove();
    return;
  }
  const home = workRowHome();
  // Steps are inserted above it by sequence number, so inside a process list it
  // stays last on its own. Anything appended to the thread itself - the message
  // the person just sent, a new turn - lands after it and it has to move.
  if (workRow.parentNode !== home || home.lastElementChild !== workRow) {
    home.append(workRow);
    scrollToEnd();
  }
  if (!state.workSince) state.workSince = Date.now();
  if (!state.workTimer) state.workTimer = setInterval(tickWorkRow, 1000);
  tickWorkRow();
}

// tickWorkRow says what is going on in one line. The order matters: a lost
// connection outranks everything, because while it is down nothing else the
// row could say is known to still be true.
function tickWorkRow() {
  if (!workRow.isConnected) return;
  const queued = queuedHere();
  const waiting = !!state.pendingQuestion;
  const lost = !!state.chatId && !state.live;
  let label;
  const heldAnswers = queuedAnswersHere();
  if (lost && (queued || heldAnswers)) label = 'Saved — it will send itself when there is signal';
  else if (lost) label = 'Reconnecting — this is the last update that got through';
  else if (heldAnswers && !queued) label = 'Sending your answer…';
  else if (queued) label = queued > 1 ? 'Sending ' + queued + ' messages…' : 'Sending…';
  else if (waiting) label = 'Waiting for your answer…';
  else label = state.workLabel || 'Working…';
  workRow.classList.toggle('waiting', waiting && !lost);
  workRow.classList.toggle('lost', lost);
  const labelNode = workRow.querySelector('.working-label');
  if (labelNode.textContent !== label) labelNode.textContent = label;
  const timeNode = workRow.querySelector('.working-time');
  if (lost) {
    const since = state.lastSync || state.workSince || Date.now();
    const away = Math.floor((Date.now() - since) / 1000);
    timeNode.textContent = away >= 2 ? fmtClock(away) + ' ago' : '';
    return;
  }
  const seconds = Math.floor((Date.now() - state.workSince) / 1000);
  timeNode.textContent = seconds >= 2 ? fmtClock(seconds) : '';
}

// workLabelFor turns the newest step into one line of plain language, so the
// row says what is happening rather than only that something is.
function workLabelFor(step) {
  const detail = detailOf(step);
  switch (step.kind) {
    case 'terminal':
      return isRunning(step.status)
        ? (step.title || 'The program') + ' is working…'
        : (step.title || 'The program') + ' finished';
    case 'shell': return 'Running ' + (detail.command || step.title || 'a command') + '…';
    case 'thinking': return 'Thinking…';
    case 'text': return 'Writing the answer…';
    case 'question': return step.status === 'pending' ? 'Waiting for your answer…' : '';
    case 'error': return '';
    case 'sub_thinking': return 'Reasoning…';
    case 'sub_tool': return (step.title || 'tool') + ': ' + ((step.body || '').split('\n')[0] || '');
    default: return '';
  }
}

function noteWork(step) {
  const label = workLabelFor(step);
  if (label) state.workLabel = label;
  updateWorkRow();
}

function insertBySeq(container, node, seq) {
  node.dataset.seq = seq;
  const sibling = [...container.children].find((child) => Number(child.dataset.seq) > Number(seq));
  container.insertBefore(node, sibling || null);
}

// A step is written once and then patched. Streamed text arrives many times a
// second, and rebuilding the row each time restarted every animation inside it
// - which is why the spinners looked like they were flickering rather than
// turning, and why finished rows kept fading back in.
function upsertStep(step, animate = true) {
  state.stepData.set(step.id, step);
  const existing = state.stepEls.get(step.id);
  if (existing && existing.dataset.kind === step.kind && typeof existing.update === 'function') {
    existing.update(step);
    scrollToEnd();
    return;
  }

  const node = buildStep(step);
  node.dataset.kind = step.kind;
  if (existing) {
    // The kind changed under us, which should not happen - rebuild, but do not
    // replay the entrance animation for a row that is already on screen.
    node.style.animation = 'none';
    node.dataset.seq = existing.dataset.seq || step.seq;
    existing.replaceWith(node);
    state.stepEls.set(step.id, node);
    scrollToEnd();
    return;
  }

  if (!animate) node.style.animation = 'none';
  const turn = ensureTurn(step.run_id);
  let container = turn.querySelector(':scope > .process');
  if (step.parent_id) {
    const parentNode = state.stepEls.get(step.parent_id);
    const children = parentNode && parentNode.querySelector(':scope > .children');
    if (children) container = children;
  }
  insertBySeq(container, node, step.seq);
  state.stepEls.set(step.id, node);
  scrollToEnd();
}

function detailOf(step) {
  if (!step.detail) return {};
  if (typeof step.detail === 'object') return step.detail;
  try { return JSON.parse(step.detail); } catch { return {}; }
}

function isRunning(status) {
  return status === 'running' || status === 'pending';
}

// statusIcon holds one spinner and one glyph and shows whichever the status
// calls for. The spinner element is never replaced, so it keeps turning from
// the moment the step starts until the moment it stops.
function statusIcon(restIcon = ICONS.check) {
  const slot = el('span', { class: 'step-icon' },
    el('span', { class: 'spinner', hidden: true }),
    el('span', { class: 'glyph' }),
  );
  const spinner = slot.firstElementChild;
  const glyph = slot.lastElementChild;
  let shown = null;
  slot.set = (status) => {
    const running = isRunning(status);
    spinner.hidden = !running;
    glyph.hidden = running;
    const failed = status === 'failed';
    const stopped = failed || status === 'interrupted' || status === 'cancelled';
    slot.classList.toggle('tick', !running && !stopped && restIcon === ICONS.check);
    slot.classList.toggle('cross', failed);
    const icon = stopped ? ICONS.cross : restIcon;
    if (!running && shown !== icon) {
      shown = icon;
      glyph.innerHTML = icon;
    }
  };
  return slot;
}

function toggleStep(node, id) {
  node.classList.toggle('open');
  state.touched.add(id);
  if (node.classList.contains('open')) state.expanded.add(id);
  else state.expanded.delete(id);
}

// Every builder returns a node with an update(step) on it. buildStep is only
// ever called once per step; everything after that goes through update, so no
// running animation is interrupted by a redraw.
function buildStep(step) {
  const detail = detailOf(step);
  switch (step.kind) {
    case 'terminal': return buildTerminal(step, detail);
    case 'shell': return buildShell(step);
    case 'question': return buildQuestion(step);
    case 'error': return buildError(step);
    case 'thinking': return buildCollapsible(step, 'Reasoning', ICONS.spark);
    case 'text': return buildText(step);
    default: return buildSubLine(step);
  }
}

function buildError(step) {
  const node = el('div', { class: 'step error-step', 'data-step': step.id });
  const title = el('div');
  const body = el('div', { style: 'margin-top:4px;opacity:.85' });
  node.append(title, body);
  node.update = (next) => {
    title.textContent = next.title || 'Error';
    body.textContent = next.body || '';
    body.hidden = !next.body;
  };
  node.update(step);
  return node;
}

function buildText(step) {
  const node = el('div', { class: 'step text-step', 'data-step': step.id });
  const body = el('div', { class: 'body md' });
  node.append(body);
  let shown = null;
  node.update = (next) => {
    if (shown === next.body) return;
    shown = next.body;
    body.innerHTML = renderMarkdown(next.body);
  };
  node.update(step);
  return node;
}

function buildCollapsible(step, label, iconHtml) {
  const node = el('div', { class: 'step collapsible', 'data-step': step.id });
  const icon = statusIcon(iconHtml || ICONS.dot);
  const head = el('div', { class: 'head' },
    icon,
    el('span', { class: 'name', text: label }),
    el('span', { class: 'chev', html: ICONS.chev }),
  );
  head.addEventListener('click', () => toggleStep(node, step.id));
  const body = el('div', { class: 'body' });
  node.append(head, body);
  node.update = (next) => {
    icon.set(next.status);
    const text = next.body || '';
    if (body.textContent !== text) body.textContent = text;
  };
  node.update(step);
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

function subExtra(step, detail) {
  const value = (step.body || step.title || '').split('\n')[0] || '';
  const parts = [];
  if (detail.input) parts.push(typeof detail.input === 'string' ? detail.input : JSON.stringify(detail.input, null, 2));
  if (step.body && step.body.includes('\n')) parts.push(step.body);
  if (detail.result) parts.push(String(detail.result));
  if (detail.command && detail.command !== value) parts.push(detail.command);
  return parts.join('\n\n');
}

function buildSubLine(step) {
  const node = el('div', { class: 'step collapsible', 'data-step': step.id });
  const classes = ['sub-line'];
  if (step.kind === 'sub_log') classes.push('log');
  if (step.kind === 'sub_error') classes.push('err');

  const spinner = el('span', { class: 'spinner', hidden: true });
  const tag = el('span', { class: 'tag' });
  const val = el('span', { class: 'val' });
  const head = el('div', { class: classes.join(' ') }, spinner, tag, val);
  const body = el('div', { class: 'body code', hidden: true });
  node.append(head, body);
  head.addEventListener('click', () => {
    if (body.hidden) return;
    toggleStep(node, step.id);
  });

  node.update = (next) => {
    const detail = detailOf(next);
    spinner.hidden = !isRunning(next.status);
    tag.textContent = subTag(next);
    val.textContent = (next.body || next.title || '').split('\n')[0] || '';
    const extra = subExtra(next, detail);
    body.hidden = !extra.trim();
    head.style.cursor = body.hidden ? '' : 'pointer';
    if (!body.hidden && body.textContent !== extra) body.textContent = extra;
    if (body.hidden) node.classList.remove('open');
  };
  node.update(step);
  if (state.expanded.has(step.id)) node.classList.add('open');
  return node;
}

// A terminal keeps one line in the transcript, and no more. The screen itself
// is in the dock beside the conversation: a program painting a full screen
// several times a second is something you look at, not something you scroll
// past on the way to the next message.
function buildTerminal(step, detail) {
  const node = el('div', { class: 'step term-line', 'data-step': step.id });
  const dot = el('span', { class: 'term-dot' });
  const what = el('span', { class: 'what' });
  const open = el('button', {
    class: 'open-term', type: 'button', text: 'Open terminal',
    onclick: () => {
      if (!dock) return;
      const current = state.stepData.get(step.id) || step;
      const now = detailOf(current);
      dock.open(now.session, current, now);
    },
  });
  node.append(dot, what, open);
  node.update = (next) => {
    state.stepData.set(next.id, next);
    const now = detailOf(next);
    const running = now.running !== false && next.status === 'running';
    // Assigning the same class list is a no op, so the pulse is never
    // restarted by an update that changed nothing about it.
    const cls = 'term-dot' + (running ? ' live' : (next.status === 'failed' ? ' failed' : ' stopped'));
    if (dot.className !== cls) dot.className = cls;
    const name = next.title || now.skill || now.tool || 'a program';
    const text = running
      ? 'Opened ' + name + ' in a terminal'
      : 'Ran ' + name + ' in a terminal — ' + (now.exit_code ? 'exited ' + now.exit_code : 'finished');
    if (what.textContent !== text) what.textContent = text;
    open.hidden = !now.session;
    // The dock learns about every session from the step that opened it, so a
    // reloaded page finds its terminals again without a second endpoint.
    if (dock) dock.noteStep(next, now);
  };
  node.update(Object.assign({}, step, { detail: detail || step.detail }));
  return node;
}

// A shell command is a one shot: the command line and what it printed.
function buildShell(step) {
  const node = el('div', { class: 'step collapsible', 'data-step': step.id });
  const spinner = el('span', { class: 'spinner', hidden: true });
  const val = el('span', { class: 'val' });
  const meta = el('span', { class: 'meta', hidden: true });
  const head = el('div', { class: 'sub-line' },
    spinner,
    el('span', { class: 'tag', text: 'shell' }),
    val,
    meta,
  );
  head.style.cursor = 'pointer';
  head.addEventListener('click', () => toggleStep(node, step.id));
  const body = el('div', { class: 'body code' });
  node.append(head, body);

  node.update = (next) => {
    const detail = detailOf(next);
    spinner.hidden = !isRunning(next.status);
    val.textContent = detail.command || next.title || '';
    meta.hidden = !detail.exit_code;
    if (detail.exit_code) meta.textContent = 'exit ' + detail.exit_code;
    const text = next.body || '(no output)';
    if (body.textContent !== text) body.textContent = text;
  };
  node.update(step);
  if (state.expanded.has(step.id)) node.classList.add('open');
  return node;
}

// The row a question leaves in the transcript. While the question is still
// open it is not drawn at all: the panel over the composer is the one place it
// is asked, and a second copy of the same buttons is one too many. What stays
// here afterwards is the record - what was asked, and what was answered - so
// reading the conversation back still makes sense.
function buildQuestion(step) {
  const node = el('div', { class: 'step question', 'data-step': step.id });
  let shown = null;
  node.update = (next) => {
    const detail = detailOf(next);
    const queued = queuedAnswer(detail.question_id);
    // While the panel over the composer is showing this very question, the
    // transcript stays out of it: one question, one place it is put.
    const onPanel = next.status === 'pending' && !!state.pendingQuestion &&
      state.pendingQuestion.id === detail.question_id;
    const signature = next.status + '\u0000' + (next.body || '') + '\u0000' + JSON.stringify(detail) +
      '\u0000' + (queued ? queued.state + queued.payload.value : '') + '\u0000' + onPanel;
    if (shown === signature) return;
    shown = signature;
    node.innerHTML = '';
    fillQuestion(node, next, detail, queued, onPanel);
  };
  node.update(step);
  return node;
}

function fillQuestion(node, step, detail, queued, onPanel) {
  // Still open: it lives over the composer, not here.
  const open = onPanel || (step.status === 'pending' && !queued);
  node.hidden = open;
  if (open) return;

  node.classList.add('answered');
  const asker = ((detail.source || '') || state.questionSources.get(detail.question_id) || '').trim();
  const lead = detail.kind === 'permission'
    ? (asker ? asker + ' asked for permission:' : 'Permission asked:')
    : (asker ? asker + ' asked:' : 'Asked:');
  node.append(el('div', { class: 'q-record' },
    el('span', { class: 'q-lead', text: lead }),
    el('span', { class: 'q-text', text: step.body || '' }),
  ));

  // A cancelled question was never answered - the run was stopped underneath
  // it. Writing "Answered: cancelled" would put words in the person's mouth.
  if (step.status === 'cancelled' && !queued) {
    node.append(el('div', { class: 'answered-note' },
      el('b', { text: 'Not answered' }), ' \u2014 the run was stopped.'));
    return;
  }

  const answer = queued ? queued.payload.value : (detail.answer || '\u2014');
  const note = el('div', { class: 'answered-note' }, 'Answered: ', el('b', { text: answer }));

  // The answer was given but has not reached the server. Saying so here is
  // both honest and what stops the same decision being made twice.
  if (queued) {
    if (queued.state === 'failed') {
      note.append(el('span', { class: 'msg-state' },
        el('span', { text: queued.error || 'Could not be sent.' }),
        el('button', { class: 'link', type: 'button', text: 'Try again', onclick: () => state.answers.retry(queued.id) }),
      ));
    } else {
      note.append(el('span', { class: 'msg-state' },
        el('span', { text: 'Waiting for the connection \u2014 it will be sent automatically.' })));
    }
  }
  node.append(note);
}

/* ------------------------------------------------------- question panel */

// A question blocks the run, so it also blocks the composer: there is nothing
// useful to type until it is answered. The panel takes the composer's place at
// the bottom of the screen, where the answer is one thumb away.

// How long the panel stays up saying the answer is on its way. Long enough to
// read, short enough that the composer does not feel gone.
const ASK_SENDING_MS = 700;

// askerName is who is really asking. The orchestrator asks in its own name;
// a question relayed from a harness says whose it is, so "allow this?" is not
// mistaken for Socrates' own idea.
function askerName(question) {
  return ((question && question.source) || '').trim();
}

// rememberAsker keeps the attribution around for the transcript.
function rememberAsker(question) {
  if (!question || !question.id) return;
  const source = askerName(question);
  if (source) state.questionSources.set(question.id, source);
}

function askHeading(question) {
  const who = askerName(question) || 'Socrates';
  return who + ' asks:';
}

// questionFreeText reads the flag off the step the question belongs to: the
// question row itself does not carry it. Anything unknown allows typing, which
// is the harmless direction to be wrong in.
function questionFreeText(question) {
  const step = question && question.step_id ? state.stepData.get(question.step_id) : null;
  if (!step) return true;
  return detailOf(step).free_text !== false;
}

function clearAskSending() {
  clearTimeout(state.askTimer);
  state.askTimer = null;
  state.askSending = null;
}

// clearPendingQuestion takes a question off the screen once it can no longer be
// answered here: it was answered on another device, the run was stopped, or the
// run ended while this browser was away. The transcript keeps the record; the
// composer comes back.
function clearPendingQuestion() {
  if (!state.pendingQuestion && !state.askSending) return;
  state.pendingQuestion = null;
  clearAskSending();
  hideAutoQuestion();
  updateMicState();
  updateAskPanel();
  // The transcript held the row back while the panel had it. Now it is the
  // only place the question is still visible.
  refreshQuestionSteps();
  updateWorkRow();
}

// updateAskPanel is the single place that decides whether the person sees the
// composer or a question. It redraws only when what it would draw changed, so
// a half typed free text answer survives every event that arrives meanwhile.
function updateAskPanel() {
  if (!dom.askPanel) return;
  const question = state.pendingQuestion || (state.askSending && state.askSending.question) || null;
  // A leftover "sending" belongs to the question it was given for and to no
  // other. A second question arriving right behind the first must not inherit
  // the answer to the one before it.
  if (state.askSending && (!question || state.askSending.questionId !== question.id)) clearAskSending();
  const sending = state.askSending;
  const active = !!question;
  dom.askPanel.hidden = !active;
  dom.composer.hidden = active;
  dom.composerNote.hidden = active;
  if (!active) {
    dom.askPanel.innerHTML = '';
    state.askShown = null;
    return;
  }
  // The answer may already be given and only waiting for a connection. Coming
  // back to it - a reconnect, a replayed snapshot - must not offer the buttons
  // again: the decision has been made, it just has not landed.
  const queued = queuedAnswer(question.id);
  const signature = JSON.stringify([
    question.id, question.question, question.kind, question.source || '', question.options || [],
    questionFreeText(question), sending ? sending.value : null,
    queued ? queued.state + '\u0000' + queued.payload.value : null,
  ]);
  if (state.askShown === signature) return;
  state.askShown = signature;
  dom.askPanel.innerHTML = '';
  fillAskPanel(question, sending, queued);
}

function fillAskPanel(question, sending, queued) {
  const node = dom.askPanel;
  node.append(el('div', { class: 'ask-head' },
    el('span', { class: 'ask-who', text: askHeading(question) }),
    question.kind === 'permission' ? el('span', { class: 'ask-kind', text: 'Permission needed' }) : null,
  ));
  node.append(el('div', { class: 'ask-q', text: question.question || '' }));

  if (sending || queued) {
    const value = sending ? sending.value : queued.payload.value;
    const stuck = !!queued && queued.state === 'failed';
    node.append(el('div', { class: 'ask-sending' },
      stuck ? null : el('span', { class: 'spinner' }),
      el('span', {}, sending ? 'Sending your answer: ' : 'Your answer: ', el('b', { text: value })),
    ));
    if (stuck) {
      node.append(el('div', { class: 'msg-state ask-note' },
        el('span', { text: queued.error || 'Could not be sent.' }),
        el('button', { class: 'link', type: 'button', text: 'Try again', onclick: () => state.answers.retry(queued.id) }),
      ));
    } else if (queued && !sending) {
      node.append(el('div', { class: 'msg-state ask-note',
        text: 'Waiting for the connection \u2014 it will be sent automatically.' }));
    }
    return;
  }

  // Every option in full, label and description both. "Option 2" is useless on
  // a phone screen a minute later, and it is exactly the moment where picking
  // the wrong one costs the most.
  const options = (question.options || []).filter((option) => option && (option.label || option.value));
  if (options.length) {
    const list = el('div', { class: 'ask-opts' });
    for (const option of options) {
      const value = option.value || option.label;
      list.append(el('button', { class: 'ask-opt', type: 'button', onclick: () => answerQuestion(question.id, value) },
        el('span', { class: 'l', text: option.label || option.value }),
        option.description ? el('span', { class: 'd', text: option.description }) : null,
      ));
    }
    node.append(list);
  }

  if (questionFreeText(question)) {
    const input = el('input', { class: 'input', type: 'text', id: 'askFree', placeholder: 'Or type your answer\u2026', autocomplete: 'off' });
    node.append(el('form', {
      class: 'ask-free',
      onsubmit: (event) => {
        event.preventDefault();
        const value = input.value.trim();
        if (value) answerQuestion(question.id, value);
      },
    }, input, el('button', { class: 'btn sm primary', type: 'submit', text: 'Send' })));
  }
}

// answerQuestion queues the answer rather than posting it and hoping. In auto
// mode the person is driving; an answer that quietly failed to send would
// leave the run stuck with no way to know why.
function answerQuestion(questionId, value) {
  if (!questionId) {
    toast('This question can no longer be answered.', 'error');
    return;
  }
  const asked = state.pendingQuestion && state.pendingQuestion.id === questionId ? state.pendingQuestion : null;
  // Durable first: from here on the answer survives a reload, a dead zone and
  // a closed tab, whatever the panel does next.
  state.answers.add({ questionId, value, chatId: state.chatId });
  state.pendingQuestion = null;
  if (asked) {
    clearTimeout(state.askTimer);
    state.askSending = { questionId, value, question: asked };
    state.askTimer = setTimeout(() => {
      if (state.askSending && state.askSending.questionId === questionId) clearAskSending();
      updateAskPanel();
    }, ASK_SENDING_MS);
  }
  updateAskPanel();
  // The transcript was holding this row back while the panel had the question.
  // It takes over now, queue state and all.
  refreshQuestionSteps();
  hideAutoQuestion();
  updateMicState();
  updateWorkRow();
  setAutoStatus('Working\u2026');
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
    writeValue(titleKey(data.chat.id), data.chat.title || '');
    dom.chatPanel.hidden = true;
    await refreshChats();
    toast('Chat updated');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

function scrollToEnd(force = false) {
  const gap = dom.thread.scrollHeight - dom.thread.scrollTop - dom.thread.clientHeight;
  if (force || gap < 160) {
    requestAnimationFrame(() => { dom.thread.scrollTop = dom.thread.scrollHeight; });
  }
}

/* -------------------------------------------------------------- streaming */

// connect opens the chat's event stream. The stream reconnects itself, asks
// for everything that happened while it was away by revision number, and says
// out loud when it is not delivering - so the thread on screen is either
// current or visibly marked as not.
function connect() {
  disconnect();
  if (!state.chatId) return;
  const chatId = state.chatId;
  state.stream = new LiveStream({
    // Read fresh on every attempt: the revision moves while the stream is up,
    // and a reconnect must ask for what this client is actually missing.
    url: () => '/api/chats/' + chatId + '/events?rev=' + state.rev,
    onMessage: (payload) => {
      if (state.chatId !== chatId) return;
      state.lastSync = Date.now();
      handleEvent(payload);
    },
    onStatus: (status) => {
      if (state.chatId !== chatId) return;
      setLive(status === 'live');
    },
    // A stream that keeps failing looks the same whether the network is gone
    // or the session expired. One ordinary request settles it: a 401 sends the
    // person to the login page instead of leaving them watching a frozen chat
    // that will never come back.
    onFail: (attempt) => {
      if (attempt !== 3) return;
      api('/api/chats', { attempts: 1 }).then((data) => {
        if (!data) return;
        state.chats = data.chats || [];
        renderChatList();
      }).catch(() => { /* the stream keeps retrying either way */ });
    },
  });
  state.stream.start();
}

function disconnect() {
  if (state.stream) {
    state.stream.stop();
    state.stream = null;
  }
  state.live = false;
  document.body.classList.remove('stale');
}

// setLive is the single switch between "what you see is happening now" and
// "what you see is the last thing that got through".
function setLive(live) {
  if (state.live === live) return;
  state.live = live;
  if (live) {
    state.lastSync = Date.now();
    state.spokeOffline = false;
  } else if (state.auto && state.busy && state.prefs.speak_in_auto_mode !== false && !state.spokeOffline) {
    // In hands free mode the person is looking at the road. A single spoken
    // notice is the only way they learn the answer they are waiting for has
    // stopped coming.
    state.spokeOffline = true;
    speak(OFFLINE_NOTICE[state.prefs.language] || OFFLINE_NOTICE.en, speechOptions()).catch(() => {});
  }
  document.body.classList.toggle('stale', !live);
  updateLiveUI();
  updateWorkRow();
  updateAutoBusy();
}

// updateLiveUI keeps every "this may be out of date" marker honest, once a
// second, whether or not anything arrived.
function updateLiveUI() {
  const stale = !!state.chatId && !state.live;
  document.body.classList.toggle('stale', stale);
  if (dock) dock.tick();
  if (state.busy || stale) tickWorkRow();
  updateAutoOffline(stale);
}

function handleEvent(event) {
  switch (event.type) {
    case 'step':
      state.rev = Math.max(state.rev, event.step.rev || 0);
      upsertStep(event.step);
      noteWork(event.step);
      updateAutoLive(event.step);
      // A question step that stopped being pending settles the panel too. Stop
      // and an answer given on another device both arrive as this event, and
      // without it the composer stays hidden behind a question that nobody is
      // waiting for any more.
      if (event.step.kind === 'question' && event.step.status !== 'pending' &&
          state.pendingQuestion && detailOf(event.step).question_id === state.pendingQuestion.id) {
        clearPendingQuestion();
      }
      break;
    case 'step_removed': {
      const node = state.stepEls.get(event.step_id);
      if (node) node.remove();
      state.stepEls.delete(event.step_id);
      state.stepData.delete(event.step_id);
      break;
    }
    case 'message':
      state.rev = Math.max(state.rev, event.message.rev || 0);
      addMessage(event.message);
      updateWorkRow();
      if (event.message.role === 'assistant') {
        state.lastAnswer = event.message.content;
        if (state.auto) {
          showAutoAnswer(event.message.content, state.prefs.speak_in_auto_mode !== false);
        } else if (state.prefs.speak_in_chat_mode) {
          speak(event.message.content, speechOptions()).catch(() => {});
        }
      }
      break;
    case 'run': {
      const alive = event.run.status === 'running' || event.run.status === 'waiting_input';
      setBusy(alive);
      if (event.run.status === 'failed' && event.run.error) toast(event.run.error, 'error');
      // The run is over - stopped, finished or failed. Nothing can be waiting
      // for an answer any more, so the composer comes back either way.
      if (!alive) clearPendingQuestion();
      if (!state.busy) setAutoStatus(state.lastAnswer ? '' : 'Tap the microphone and speak');
      break;
    }
    case 'chat':
      state.chat = event.chat;
      dom.chatSettings.hidden = false;
      dom.title.textContent = event.chat.title || 'New chat';
      // Titles are written after the first message, so this is usually where a
      // chat gets the name an offline reload will show.
      writeValue(titleKey(event.chat.id), event.chat.title || '');
      refreshChats();
      break;
    case 'question':
      rememberAsker(event.question);
      if (event.question.status === 'pending') {
        state.pendingQuestion = event.question;
        updateMicState();
        if (state.auto) showAutoQuestion(event.question, state.prefs.speak_in_auto_mode !== false);
      } else if ((state.pendingQuestion && state.pendingQuestion.id === event.question.id) ||
                 (state.askSending && state.askSending.questionId === event.question.id)) {
        // Answered, or cancelled with the run: either way it is settled.
        clearPendingQuestion();
      }
      updateAskPanel();
      refreshQuestionSteps();
      updateWorkRow();
      break;
    case 'ready':
      // Anything that arrived while the stream was down is replayed here: the
      // steps and messages came first, and step_ids says which rows still
      // exist, so anything deleted during the outage goes away instead of
      // sitting there looking current.
      for (const message of event.messages || []) addMessage(message, false);
      if (event.step_ids) reconcileSteps(event.step_ids);
      state.rev = Math.max(state.rev, event.rev || 0);
      setBusy(!!event.busy);
      // A run that ended while this client was away takes its question with it,
      // step and all. Nothing then arrives to close the panel, so the snapshot
      // itself has to say so.
      if (!event.busy) clearPendingQuestion();
      setLive(true);
      break;
    case 'resync':
      // The server could not keep up with this client and gave up on the
      // buffer. Reconnecting replays from the last revision, so nothing is
      // lost - it just costs one round trip.
      if (state.stream) state.stream.reconnect(0);
      break;
    default:
      break;
  }
}

// reconcileSteps drops the rows the server no longer has. A deletion cannot
// carry a revision, so a client that was away would otherwise keep showing a
// step that has since been replaced by the finished answer.
function reconcileSteps(ids) {
  const live = new Set(ids);
  for (const [id, node] of state.stepEls) {
    if (live.has(id)) continue;
    node.remove();
    state.stepEls.delete(id);
    state.stepData.delete(id);
  }
}

// setBusy records what the server says. Whether the composer is actually
// blocked is a wider question - a message sitting in the queue because there is
// no signal is work in progress too - so the two are kept apart.
function setBusy(busy) {
  state.serverBusy = busy;
  refreshBusy();
}

function refreshBusy() {
  const busy = state.serverBusy || queuedHere() > 0;
  const changed = state.busy !== busy;
  state.busy = busy;
  updateSendButton();
  dom.stopBtn.hidden = !busy;
  document.body.classList.toggle('busy', busy);
  updateMicState();
  markChatBusy();
  if (changed && busy) state.workLabel = '';
  updateWorkRow();
  updateAutoBusy();
}

// updateSendButton keeps the composer button honest: an arrow that sends, or a
// square that stops whatever is running.
function updateSendButton() {
  const stopping = state.busy;
  dom.sendBtn.classList.toggle('stop', stopping);
  dom.sendBtn.disabled = stopping ? false : !dom.input.value.trim();
  dom.sendBtn.title = stopping ? 'Stop generating' : 'Send';
  dom.sendBtn.setAttribute('aria-label', dom.sendBtn.title);
  const icon = stopping ? ICONS.stop : ICONS.send;
  if (dom.sendBtn.dataset.icon !== (stopping ? 'stop' : 'send')) {
    dom.sendBtn.dataset.icon = stopping ? 'stop' : 'send';
    dom.sendBtn.innerHTML = icon;
  }
}

// The microphone stays usable while a question is waiting: the answer can be
// spoken instead of clicked.
function updateMicState() {
  const blocked = state.busy && !state.pendingQuestion;
  dom.autoMic.classList.toggle('busy', blocked);
  dom.autoMic.disabled = blocked;
  dom.micBtn.disabled = blocked;
  updateAutoBusy();
}

async function stopRun() {
  if (!state.chatId) return;
  // Anything queued but not yet sent is part of what "stop" means: it would
  // otherwise start a new run the moment the connection came back.
  const queued = state.outbox.items.filter((item) => sameChat(item.payload.chatId));
  for (const item of queued) state.outbox.drop(item.id);
  try {
    await api('/api/chats/' + state.chatId + '/stop', { method: 'POST', attempts: 3 });
    setAutoStatus('Stopped');
  } catch (err) {
    toast(isOffline(err) ? 'No connection — could not stop the run yet.' : errorMessage(err), 'error');
  }
}

/* --------------------------------------------------------------- sending */

// submitText hands the message to the queue and returns. Nothing here waits
// for the network: the bubble appears immediately, the queue keeps the text
// until the server has it, and a dropped connection only delays it.
function submitText(raw) {
  const text = (raw || '').trim();
  if (!text || state.busy) return;
  dom.input.value = '';
  saveDraft();
  autosize();
  // Busy from the moment the person presses send, not from the moment the
  // server confirms: creating the chat and posting the message are two round
  // trips, and there used to be nothing on screen for either of them.
  state.workLabel = '';
  state.workSince = 0;
  const empty = dom.threadInner.querySelector('.empty');
  if (empty) empty.remove();
  // Adding to the queue is what makes the page busy: the bubble, the working
  // row and the locked composer all follow from there, with no round trip in
  // between.
  state.outbox.add({
    chatId: state.chatId,
    chatKey: state.chatId ? '' : clientKey(),
    key: clientKey(),
    text,
    auto: state.auto,
  });
  setAutoStatus('Working…');
  scrollToEnd(true);
}

// renderPending draws the messages that are queued but not yet acknowledged.
// They look like ordinary bubbles with a line underneath saying where they
// stand, so it is never a mystery whether something was actually sent.
function renderPending() {
  if (!state.outbox) return;
  const mine = state.outbox.items.filter((item) => sameChat(item.payload.chatId));
  const wanted = new Map(mine.map((item) => [item.payload.key, item]));

  for (const node of [...dom.threadInner.querySelectorAll('[data-pending]')]) {
    if (!wanted.has(node.dataset.pending)) node.remove();
  }
  for (const [key, item] of wanted) {
    let node = findPending(key);
    if (!node) {
      node = el('div', { class: 'msg user pending', 'data-pending': key, text: item.payload.text },
        el('div', { class: 'msg-state' }));
      dom.threadInner.append(node);
    }
    const failed = item.state === 'failed';
    node.classList.toggle('stuck', failed);
    const line = node.querySelector(':scope > .msg-state');
    line.innerHTML = '';
    if (failed) {
      line.append(
        el('span', { text: item.error || 'Could not be sent.' }),
        el('button', { class: 'link', type: 'button', text: 'Try again', onclick: () => state.outbox.retry(item.id) }),
        el('button', {
          class: 'link',
          type: 'button',
          text: 'Discard',
          onclick: () => {
            dom.input.value = dom.input.value || item.payload.text;
            autosize();
            saveDraft();
            updateSendButton();
            state.outbox.drop(item.id);
          },
        }),
      );
    } else {
      line.append(el('span', {
        text: state.live && item.attempts <= 1 ? 'Sending…' : 'Waiting for the connection — it will be sent automatically.',
      }));
    }
  }
  // A message waiting in the queue is work in progress, so the composer has to
  // agree: this also runs after a chat is opened, when the queue itself has not
  // changed but the chat it belongs to has.
  refreshBusy();
}

function findPending(key) {
  for (const node of dom.threadInner.querySelectorAll('[data-pending]')) {
    if (node.dataset.pending === key) return node;
  }
  return null;
}

// sameChat treats "the chat that does not exist yet" as the open one, so a
// first message typed into a blank page is shown where it was typed.
function sameChat(chatId) {
  if (!chatId) return !state.chatId;
  return chatId === state.chatId;
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
    updateAutoBusy();
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
    // Transcription only reads the audio back as words, so repeating it costs
    // a moment and loses nothing - which is exactly what a car needs.
    const data = await api('/api/voice/transcribe', {
      method: 'POST',
      attempts: 3,
      timeout: 60000,
      body: { audio: result.base64, format: result.format },
    });
    text = (data && data.text) || '';
  } catch (err) {
    const offline = isOffline(err);
    toast(offline ? 'No connection — that recording could not be transcribed.' : errorMessage(err), 'error');
    setAutoStatus(offline ? 'No connection. Try again when you have signal.' : 'Transcription failed');
    return;
  }
  if (!text) {
    setAutoStatus('I did not catch that');
    return;
  }

  if (state.pendingQuestion) {
    const value = matchOption(text, state.pendingQuestion.options || []);
    answerQuestion(state.pendingQuestion.id, value);
    return;
  }

  if (origin === 'auto') {
    dom.autoTranscript.hidden = false;
    dom.autoTranscript.textContent = '“' + text + '”';
    dom.autoAnswer.hidden = true;
    dom.autoActions.hidden = true;
    dom.autoTimer.hidden = true;
    submitText(text);
  } else {
    dom.input.value = dom.input.value ? dom.input.value + ' ' + text : text;
    autosize();
    updateSendButton();
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
  updateAutoBusy();
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
  writeValue('socrates.auto', on ? '1' : '0');
  document.body.classList.toggle('auto', on);
  dom.autoToggle.checked = on;
  updateAskPanel();
  if (!on) {
    stopSpeaking();
    return;
  }
  // Switching into hands free mode with a question already waiting has to show
  // it: the composer panel is gone with the rest of the chat surface, and the
  // run would otherwise look stuck.
  if (state.pendingQuestion) {
    showAutoQuestion(state.pendingQuestion, false);
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
  updateAutoBusy();
  updateAutoOffline(!!state.chatId && !state.live);
}

// updateAutoBusy shows the same three dots on the hands free screen, so it too
// is never silent about a run that is still going.
function updateAutoBusy() {
  if (!dom.autoBusy) return;
  dom.autoBusy.hidden = !(state.busy && !state.pendingQuestion && !state.recorder.recording);
}

// updateAutoOffline is the big screen version of the status bar. The car case
// is the whole reason this app has a hands free mode, and it is also the case
// where a frozen answer is least likely to be noticed.
function updateAutoOffline(stale) {
  if (!dom.autoOffline) return;
  dom.autoOffline.hidden = !stale;
  if (!stale) return;
  const queued = queuedHere() + queuedAnswersHere();
  const away = state.lastSync ? Math.round((Date.now() - state.lastSync) / 1000) : 0;
  const text = queued
    ? 'No connection. What you said is saved and will be sent as soon as there is signal.'
    : 'No connection. Reconnecting…' + (away > 3 ? ' Last update ' + fmtClock(away) + ' ago.' : '');
  if (dom.autoOffline.textContent !== text) dom.autoOffline.textContent = text;
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
  updateAutoBusy();
  fitAnswer();
  if (doSpeak) speak(text, speechOptions()).catch(() => {});
}

function fitAnswer() {
  const node = dom.autoAnswer;
  const length = node.textContent.length;
  const size = length < 90 ? 46 : length < 220 ? 38 : length < 480 ? 30 : 24;
  node.style.fontSize = 'clamp(20px, ' + (size / 14) + 'vw, ' + size + 'px)';
}

function showAutoQuestion(question, doSpeak) {
  state.pendingQuestion = question;
  updateAskPanel();
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
    speak(spoken.join(' '), speechOptions()).catch(() => {});
  }
}

function hideAutoQuestion() {
  dom.autoQuestion.hidden = true;
  dom.autoQuestion.innerHTML = '';
}

// Last, so that every value this module builds at load time - the working row
// among them - exists before the first line of it runs.
boot();
