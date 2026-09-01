// The chat page: sidebar, live process view, composer, voice and audio mode.

import {
  api, el, toast, confirmDialog, fmtClock, fmtTokens, isOffline, errorMessage,
  setClass, LiveStream, Outbox, clientKey, onWake, HttpError, RetryLater,
  isBusyConflict, CONNECTION_GRACE,
} from './api.js';
import { renderMarkdown } from './markdown.js';
import {
  Recorder, describeMicError, speak, stopSpeaking, isSpeaking,
  onSpeechError, fetchSpeech, playSpeech,
} from './voice.js';
import * as agents from './agents.js';
import { combobox } from './combobox.js';

const $ = (id) => document.getElementById(id);

const dom = {
  navScrim: $('navScrim'),
  chatList: $('chatList'),
  chatScope: $('chatScope'),
  viewSlider: $('viewSlider'),
  thread: $('thread'),
  threadInner: $('threadInner'),
  title: $('chatTitle'),
  agentBadge: $('chatAgent'),
  archivedTag: $('chatArchived'),
  composer: $('composer'),
  composerLegacy: $('composerLegacy'),
  input: $('input'),
  sendBtn: $('sendBtn'),
  micBtn: $('micBtn'),
  recTime: $('recTime'),
  autoScreen: $('autoScreen'),
  autoStatus: $('autoStatus'),
  autoTimer: $('autoTimer'),
  autoTranscript: $('autoTranscript'),
  autoMic: $('autoMic'),
  autoLive: $('autoLive'),
  autoBusy: $('autoBusy'),
  autoOffline: $('autoOffline'),
  autoAnswer: $('autoAnswer'),
  autoActions: $('autoActions'),
  chatSettings: $('chatSettings'),
  chatPanel: $('chatPanel'),
  panelTitle: $('panelTitle'),
  panelWorkspace: $('panelWorkspace'),
  panelWorkspaceNow: $('panelWorkspaceNow'),
  panelBinding: $('panelBinding'),
  panelModel: $('panelModel'),
  panelEffort: $('panelEffort'),
  panelBindingHint: $('panelBindingHint'),
  autoReplay: $('autoReplay'),
  autoDetails: $('autoDetails'),
};

const state = {
  chats: [],
  // Which chats the sidebar shows: the active ones, or every one of them.
  // The choice is remembered, because it is a way of working rather than a
  // one off question.
  chatScope: readValue('socrates.chatScope') === 'all' ? 'all' : 'active',
  chatId: null,
  chat: null,
  // Which of the two the pane is showing: the conversation, or the hands free
  // screen. Remembered per chat, because it is how that chat is used rather
  // than a passing choice - except hands free, which follows the person.
  // What was remembered is read in init, once the page is there to show it.
  view: 'chat',
  // What this chat is bound to, as the header shows it: the agent's name, the
  // model's name, whether the binding still works on this machine, and whether
  // this is a transcript from before Socrates talked to agents directly.
  agentLabel: '',
  modelLabel: '',
  agentOK: true,
  legacy: false,
  // What the new chat sheet decided, for a chat that does not exist yet. It
  // travels in the payload of every queued message that has no chat id, so a
  // chat typed into while offline is created with its agent when the message
  // is finally delivered.
  pendingBinding: null,
  rev: 0,
  busy: false,
  serverBusy: false,
  // The chat event stream, and whether it is actually delivering. Everything
  // on screen is only as true as this flag.
  stream: null,
  live: false,
  // When the stream stopped delivering, and whether the page has already acted
  // on it. Switching chats closes one stream and opens another, so "not live"
  // only becomes "stale" once it has lasted longer than a reconnect does.
  liveLostAt: 0,
  staleShown: false,
  staleTimer: null,
  lastSync: 0,
  // Queues that survive a reload: nothing the person did is lost because the
  // connection went away between tapping and landing.
  outbox: null,
  reopenTimer: null,
  loadFailed: false,
  stepEls: new Map(),
  stepData: new Map(),
  chatEls: new Map(),
  groupEls: new Map(),
  turnEls: new Map(),
  expanded: new Set(),
  spokeOffline: false,
  lastAnswer: '',
  recorder: new Recorder(),
  recTimer: null,
  // The always on "something is happening" row: what it says, when it started
  // and the ticker that keeps its clock moving.
  workLabel: '',
  workSince: 0,
  workTimer: null,
  // Where this chat actually works: what it was pointed at, and the folder it
  // falls back to. Both are needed to keep the settings dialog truthful while
  // the directory changes under it.
  effectiveWorkspace: '',
  defaultWorkspace: '',
  prefs: { speak_in_auto_mode: true, speak_in_chat_mode: false, language: 'en' },
};

const ICONS = {
  spark: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M18 6l-2.5 2.5M8.5 15.5 6 18"/></svg>',
  check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m5 13 4 4L19 7"/></svg>',
  cross: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M6 6l12 12M18 6 6 18"/></svg>',
  chev: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="13" height="13"><path d="m9 6 6 6-6 6"/></svg>',
  dot: '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="12" cy="12" r="3.5"/></svg>',
  info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 7.6v.1"/></svg>',
  trash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4 7h16M9 7V5h6v2M7 7l1 12h8l1-12"/></svg>',
  archive: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4"/></svg>',
  restore: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M12 17v-6M9 14l3-3 3 3"/></svg>',
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

// The one sentence Socrates says on its own behalf, in both languages it can
// be set to. It is spoken when the connection drops.
const OFFLINE_NOTICE = {
  en: 'The connection dropped. I will keep trying.',
  de: 'Die Verbindung ist weg. Ich versuche es weiter.',
};

// The voice that reads it lives on the server, and this is the one sentence
// that is due exactly when the server cannot be reached - so the audio is
// fetched long before it is needed and kept in memory. Losing it would cost
// the one thing that tells someone who is driving that the answer they are
// waiting for has stopped coming.
let offlineClip = { language: '', blob: null, fetching: false };

// primeOfflineNotice is called wherever the connection has just proved itself:
// on the way in, on the way back from a locked phone, and every time the
// stream reports itself ready. It is a nicety, so a fetch that fails is simply
// dropped and tried again at the next of those moments.
function primeOfflineNotice() {
  const language = state.prefs.language || 'en';
  if (offlineClip.fetching || (offlineClip.blob && offlineClip.language === language)) return;
  offlineClip.fetching = true;
  fetchSpeech(OFFLINE_NOTICE[language] || OFFLINE_NOTICE.en)
    .then((blob) => { offlineClip = { language, blob, fetching: false }; })
    .catch(() => { offlineClip.fetching = false; });
}

async function init() {
  setView(storedView(null), { silent: true });
  buildQueues();
  bindUI();
  // An answer that is not read out loud sounds exactly like an answer that was
  // never given, so the reason is shown. It is said once per reason, so a
  // voice that is still installing itself is reported the first time it costs
  // you an answer and not on every answer after it.
  onSpeechError((reason, kind) => toast(reason, kind));
  // Preferences are a nicety and the defaults are sensible, so the page is
  // never held up waiting for them. Everything on the boot path is deliberately
  // impatient: on a bad connection what matters is that the app appears and
  // says what is wrong, not that it eventually loads everything.
  api('/api/preferences', BOOT)
    .then((prefs) => { if (prefs) state.prefs = prefs; })
    .catch(() => { /* defaults are fine */ })
    .finally(primeOfflineNotice);
  // The agent catalogue is what the new chat sheet and the header badge are
  // drawn from. It is fetched here so it is warm before either is needed, and
  // it falls back to the copy from the last visit on its own.
  agents.load()
    .then(() => paintAgentBadge())
    .catch(() => { /* the sheet says so when it is opened */ });
  await refreshChats(BOOT);
  // The address bar is the more reliable of the two: reopening the app with no
  // signal cannot fetch the chat list, and falling back to a blank new chat
  // there would lose both the place and the draft that belongs to it.
  const fromHash = location.hash.replace(/^#/, '');
  // A chat that was started and typed into but never delivered has no id to
  // open, and opening another chat over it would put the bubble the person is
  // waiting on to land on no screen at all. It wins the boot.
  if (fromHash) await openChat(fromHash);
  else if (firstChatId() && !state.pendingBinding) await openChat(firstChatId());
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

// buildQueues wires the one thing a person hands to Socrates - a message -
// onto a durable queue. It carries a key the server recognises, so a retry
// after a dropped connection is a no-op rather than a second message.
function buildQueues() {
  state.outbox = new Outbox('messages', sendQueuedMessage);
  state.outbox.onChange(renderPending);
  // A chat that was started but never delivered survives a reload in the
  // queue. Its binding comes back with it, so the header still says who is
  // going to answer and a second message lands in the same chat.
  const unbound = state.outbox.items.find((item) => !item.payload.chatId && item.payload.agent);
  if (unbound) {
    state.pendingBinding = {
      agent: unbound.payload.agent,
      model: unbound.payload.model || '',
      effort: unbound.payload.effort || '',
    };
  }
}

// sendQueuedMessage is the whole "say something" transaction: create the chat
// if there is not one yet, then post the message. Both halves are keyed, so
// the pair can be repeated from the start without duplicating anything.
async function sendQueuedMessage(payload, item) {
  if (!payload.chatId) {
    // The binding travels with the message rather than being read from the
    // page: by the time this runs the page may have been reloaded, and a
    // create with no agent is refused permanently - which would lose a message
    // the person did nothing to earn.
    const created = await api('/api/chats', {
      method: 'POST',
      body: {
        client_id: payload.chatKey,
        agent: payload.agent || '',
        model: payload.model || '',
        effort: payload.effort || '',
      },
    });
    payload.chatId = created.chat.id;
    state.outbox.persist();
    adoptCreatedChat(created.chat, item);
  }
  try {
    await api('/api/chats/' + encodeURIComponent(payload.chatId) + '/messages', {
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

// adoptCreatedChat moves the page onto the chat the queue just created, but
// only if the person is still looking at the blank one they typed into.
function adoptCreatedChat(chat, item) {
  const queued = state.outbox.items.filter((entry) => entry.id !== item.id && !entry.payload.chatId);
  for (const entry of queued) entry.payload.chatId = chat.id;
  state.outbox.persist();
  // The chat exists, so the binding the sheet produced has done its job and
  // the sheet may be opened again.
  state.pendingBinding = null;
  if (state.chatId) return;
  state.chatId = chat.id;
  state.chat = chat;
  updateArchivedMark();
  state.effectiveWorkspace = '';
  state.rev = 0;
  state.legacy = false;
  state.agentOK = true;
  paintAgentBadge();
  dom.chatSettings.hidden = false;
  location.hash = chat.id;
  connect();
  refreshChats();
}

// boot retries init on a bad connection, so this guards against binding every
// handler a second time - which is what made one tap on the menu button toggle
// the drawer twice.
let bound = false;

function bindUI() {
  if (bound) return;
  bound = true;
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
  $('newChat').addEventListener('click', () => { startNewChat().catch(() => {}); });
  dom.chatScope.addEventListener('click', (event) => {
    const button = event.target.closest('.seg');
    if (button) setChatScope(button.dataset.scope);
  });
  dom.viewSlider.addEventListener('click', (event) => {
    const stop = event.target.closest('.stop');
    if (stop && stop.getAttribute('aria-disabled') !== 'true') setView(stop.dataset.view);
  });
  // One knob on one slider is one radio group, and a radio group is driven
  // with the arrow keys.
  dom.viewSlider.addEventListener('keydown', (event) => {
    const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1
      : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0;
    if (!step) return;
    event.preventDefault();
    const stops = [...dom.viewSlider.querySelectorAll('.stop')]
      .filter((stop) => stop.getAttribute('aria-disabled') !== 'true');
    if (!stops.length) return;
    const at = stops.findIndex((stop) => stop.dataset.view === state.view);
    const stop = stops[(Math.max(at, 0) + step + stops.length) % stops.length];
    setView(stop.dataset.view);
    stop.focus();
  });
  // The knob is placed from the layout the browser made, so it is placed again
  // whenever that layout can have changed: the bar narrowing past the width
  // where the stops give up their words, or a font arriving late.
  if (window.ResizeObserver) new ResizeObserver(placeKnob).observe(dom.viewSlider);
  window.addEventListener('resize', placeKnob);
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(placeKnob).catch(() => {});
  // The remembered choice has to be on the buttons before the first list
  // arrives, or the sidebar would say "Active" while showing everything.
  setChatScope(state.chatScope);
  $('menuBtn').addEventListener('click', () => setNav(!navOpen()));
  // Tapping beside the drawer closes it. The scrim is what "beside" means: it
  // covers everything the drawer is over, so this one handler is every empty
  // spot and the conversation behind it.
  dom.navScrim.addEventListener('click', closeNav);
  // The rest of "beside", for the few things that sit above the scrim - the
  // connection bar - and for a wide window, where there is no scrim at all.
  // The menu button is left out because it is the one thing that toggles.
  document.addEventListener('click', (event) => {
    if (!navOpen()) return;
    if (event.target.closest('#sidebar') || event.target.closest('#menuBtn')) return;
    closeNav();
  });
  $('logout').addEventListener('click', async () => {
    // Signing out locally is the part that matters; if the server could not be
    // told, the page still leaves rather than hanging on a spinner.
    try { await api('/api/logout', { method: 'POST', attempts: 2 }); } catch { /* leave anyway */ }
    location.href = '/login';
  });
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
  dom.autoReplay.addEventListener('click', () => {
    if (isSpeaking()) stopSpeaking();
    else if (state.lastAnswer) speak(state.lastAnswer).catch(() => { /* reported once */ });
  });
  dom.autoDetails.addEventListener('click', () => setView('chat'));
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
    if (event.key === 'Escape') closeNav();
  });
  // Coming back from a locked phone: the sidebar and the chat are both refreshed
  // rather than trusted, because the stream may have been asleep for hours.
  onWake(() => {
    if (document.visibilityState === 'hidden') return;
    refreshChats();
    primeOfflineNotice();
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

// chatsPath asks for exactly what the sidebar is currently showing. The scope
// is always named, so the request says what it wants rather than relying on
// what the server happens to default to.
function chatsPath() {
  return '/api/chats?scope=' + state.chatScope;
}

// refreshChats never empties the sidebar because of a bad moment: a list that
// could not be fetched leaves the previous one alone.
async function refreshChats(options = {}) {
  try {
    const data = await api(chatsPath(), options);
    state.chats = data.chats || [];
    renderChatList();
  } catch (err) {
    if (!isOffline(err)) throw err;
  }
}

// setChatScope switches between the active chats and all of them. The sidebar
// is repainted from what is already here first, so the switch answers at once
// and the fetch only fills in what this scope has not seen yet.
function setChatScope(scope) {
  const next = scope === 'all' ? 'all' : 'active';
  for (const button of dom.chatScope.querySelectorAll('.seg')) {
    const on = button.dataset.scope === next;
    setClass(button, 'on', on);
    button.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
  if (state.chatScope === next) return;
  state.chatScope = next;
  writeValue('socrates.chatScope', next);
  if (next === 'active') {
    state.chats = state.chats.filter((chat) => !chat.archived);
    renderChatList();
  }
  refreshChats().catch(() => { /* the sidebar keeps what it has */ });
}

/* ------------------------------------------------------------ chat view */

// Two ways to use a chat: reading and typing, or hands free. They are one
// slider in the top bar because they are one decision - which of them has the
// pane - and a pair of switches for it could disagree.
const VIEWS = ['chat', 'auto'];

// The view is remembered per chat, so a chat that is used hands free is still
// hands free the next time it is opened.
function viewKey(id) { return 'socrates.view.' + id; }

// storedView is where a chat starts. Hands free is the one choice that follows
// the person rather than the chat - it says how the app is being used right
// now, in a car say - so it carries over to whatever is opened next, while a
// chat with nothing remembered is a conversation.
function storedView(id) {
  const saved = id ? readValue(viewKey(id)) : '';
  if (VIEWS.includes(saved)) return saved;
  return readFlag('socrates.auto') ? 'auto' : 'chat';
}

// setView is the only thing that decides what the pane shows. Each of the two
// is a whole screen rather than a panel, so switching is a matter of handing
// the pane over: nothing about the chat or its stream changes underneath.
function setView(view, options = {}) {
  const next = VIEWS.includes(view) ? view : 'chat';
  const before = state.view;
  state.view = next;
  renderViewSlider();
  if (!options.silent && state.chatId) writeValue(viewKey(state.chatId), next);
  if (!options.silent) writeValue('socrates.auto', next === 'auto' ? '1' : '0');
  setClass(document.body, 'auto', next === 'auto');
  if (before === 'auto' && next !== 'auto') stopSpeaking();
  if (next === 'auto' && before !== 'auto' && !options.silent && state.lastAnswer) {
    showAutoAnswer(state.lastAnswer, false);
  }
}

// renderViewSlider says which stop has the pane.
function renderViewSlider() {
  for (const stop of dom.viewSlider.querySelectorAll('.stop')) {
    const on = stop.dataset.view === state.view;
    stop.setAttribute('aria-checked', on ? 'true' : 'false');
    // One tab stop for the group, the way a set of radio buttons behaves.
    stop.tabIndex = on ? 0 : -1;
  }
  placeKnob();
}

// The stops are as wide as their words, and the words come and go with the
// width of the bar, so the knob is measured from the layout the browser
// actually made rather than assumed to be a quarter of the slider.
function placeKnob() {
  const active = dom.viewSlider.querySelector('.stop[aria-checked="true"]');
  if (!active || !active.offsetWidth) return;
  dom.viewSlider.style.setProperty('--knob-x', active.offsetLeft + 'px');
  dom.viewSlider.style.setProperty('--knob-w', active.offsetWidth + 'px');
  // The first placement is the one that must not slide: a knob gliding in from
  // the left edge on load reads as the page changing its mind.
  if (!dom.viewSlider.classList.contains('placed')) {
    requestAnimationFrame(() => setClass(dom.viewSlider, 'placed', true));
  }
}

// restoreView puts a chat back into the view it was last used in.
function restoreView(id) {
  setView(storedView(id), { silent: true });
}

// busyButton is the smallest honest answer to a tap that has to wait for the
// network: the control stops taking taps and says it is working.
function busyButton(button, on) {
  if (!button) return;
  button.disabled = on;
  setClass(button, 'is-busy', on);
  const spin = button.querySelector(':scope > .spinner');
  if (on && !spin) button.prepend(el('span', { class: 'spinner' }));
  if (!on && spin) spin.remove();
}

// The sidebar is patched rather than rebuilt. Its rows carry the running dot,
// and a dot that is thrown away and made again every time a run event arrives
// restarts its animation - which is what made it look like it was flickering
// instead of pulsing.
function renderChatList() {
  if (!state.chats.length) {
    dom.chatList.innerHTML = '';
    state.chatEls.clear();
    state.groupEls.clear();
    const empty = state.chatScope === 'all' ? 'No chats yet' : 'No active chats';
    dom.chatList.append(el('div', { class: 'group-label', text: empty }));
    return;
  }

  const groups = chatGroups();
  const liveChats = new Set();
  for (const group of groups) for (const chat of group.chats) liveChats.add(chat.id);
  for (const [id, node] of state.chatEls) {
    if (liveChats.has(id)) continue;
    node.remove();
    state.chatEls.delete(id);
  }
  const liveGroups = new Set(groups.map((group) => group.label));
  for (const [label, node] of state.groupEls) {
    if (liveGroups.has(label)) continue;
    node.remove();
    state.groupEls.delete(label);
  }
  // A label left over from the empty state belongs to no group and would
  // otherwise sit above the list saying there is nothing here.
  const kept = new Set(state.groupEls.values());
  for (const node of dom.chatList.querySelectorAll(':scope > .group-label')) {
    if (!kept.has(node)) node.remove();
  }

  // Nodes are moved only when they are actually out of place: moving one
  // restarts its CSS animation, which is what made the running dot flicker.
  let previous = null;
  const place = (node) => {
    const inPlace = previous ? previous.nextElementSibling === node : dom.chatList.firstElementChild === node;
    if (!inPlace) {
      if (previous) previous.after(node);
      else dom.chatList.prepend(node);
    }
    previous = node;
  };
  for (const group of groups) {
    let label = state.groupEls.get(group.label);
    if (!label) {
      label = el('div', { class: 'group-label', text: group.label });
      state.groupEls.set(group.label, label);
    }
    place(label);
    for (const chat of group.chats) {
      let item = state.chatEls.get(chat.id);
      if (!item) {
        item = buildChatItem(chat);
        state.chatEls.set(chat.id, item);
      }
      item.update(chat);
      place(item);
    }
  }
}

// chatGroups splits the sidebar into the headings it should have. Showing
// everything means the archive comes after the active chats under its own
// label, so "all" never hides which of the two a row belongs to.
function chatGroups() {
  const active = state.chats.filter((chat) => !chat.archived);
  const archived = state.chats.filter((chat) => chat.archived);
  if (!archived.length) return [{ label: 'Chats', chats: active }];
  if (!active.length) return [{ label: 'Archived', chats: archived }];
  return [{ label: 'Chats', chats: active }, { label: 'Archived', chats: archived }];
}

// patchChatListItem writes a changed chat into the list this page already has,
// so the sidebar follows a rename immediately instead of a round trip later.
function patchChatListItem(chat) {
  const known = state.chats.find((c) => c.id === chat.id);
  if (!known) return;
  known.title = chat.title;
  known.workspace = chat.workspace;
  known.archived = !!chat.archived;
  renderChatList();
}

// updateArchivedMark keeps the marker beside the chat title honest. The
// sidebar can be filtered to hide this chat entirely, so this is the one place
// that always says whether what is on screen has been put away.
function updateArchivedMark() {
  dom.archivedTag.hidden = !(state.chat && state.chat.archived);
}

function buildChatItem(chat) {
  const item = el('div', {
    class: 'chat-item',
    onclick: (event) => {
      if (event.target.closest('.act')) return;
      openChat(item.dataset.chat);
    },
  });
  // Both actions work on the chat as it is right now, not on the one this row
  // was built from: a row outlives several refreshes of the list.
  const current = () => state.chats.find((c) => c.id === item.dataset.chat);
  const dot = el('span', { class: 'dot', hidden: true, title: 'Working' });
  const label = el('span', { class: 'label' });
  const archive = el('button', {
    class: 'icon-btn act',
    onclick: (event) => {
      event.stopPropagation();
      const chatNow = current();
      if (chatNow) setArchived(chatNow, !chatNow.archived);
    },
  });
  const remove = el('button', {
    class: 'icon-btn act',
    title: 'Delete chat',
    'aria-label': 'Delete chat',
    html: ICONS.trash,
    onclick: (event) => {
      event.stopPropagation();
      const chatNow = current();
      if (chatNow) deleteChat(chatNow);
    },
  });
  item.append(dot, label, archive, remove);
  item.update = (next) => {
    item.dataset.chat = next.id;
    const active = next.id === state.chatId;
    setClass(item, 'active', active);
    setClass(item, 'archived', !!next.archived);
    const title = next.title || 'New chat';
    if (label.textContent !== title) label.textContent = title;
    const mode = next.archived ? 'restore' : 'archive';
    if (archive.dataset.mode !== mode) {
      archive.dataset.mode = mode;
      archive.innerHTML = next.archived ? ICONS.restore : ICONS.archive;
      const action = next.archived ? 'Restore chat' : 'Archive chat';
      archive.title = action;
      archive.setAttribute('aria-label', action);
    }
    dot.hidden = !(active && state.busy);
  };
  return item;
}

// setArchived puts a chat away or brings it back. Archiving is the honest
// middle ground between keeping a chat and deleting it: the conversation
// stays, everything it had running does not.
async function setArchived(chat, archived) {
  if (archived) {
    const ok = await confirmDialog({
      title: 'Archive this chat?',
      body: '"' + (chat.title || 'New chat') + '" keeps its conversation, but the run in progress '
        + 'is stopped and its agent session is closed. Sending a message makes it active again.',
      confirmLabel: 'Archive chat',
    });
    if (!ok) return;
  }
  // The list moves first and the server is told afterwards. Archiving is a
  // decision, not a question, and waiting for the round trip left a tap that
  // looked like it had done nothing.
  const known = state.chats.find((c) => c.id === chat.id);
  const before = known ? !!known.archived : null;
  if (known) known.archived = archived;
  const open = state.chat && state.chat.id === chat.id;
  if (open) {
    state.chat.archived = archived;
    updateArchivedMark();
  }
  renderChatList();
  api('/api/chats/' + encodeURIComponent(chat.id) + (archived ? '/archive' : '/unarchive'), {
    method: 'POST', attempts: 3,
  }).then(() => {
    refreshChats().catch(() => { /* the sidebar keeps what it has */ });
    toast(archived ? 'Chat archived' : 'Chat restored');
  }).catch((err) => {
    // It did not happen, so the sidebar has to stop saying that it did.
    if (known && before !== null) known.archived = before;
    if (open) {
      state.chat.archived = before;
      updateArchivedMark();
    }
    renderChatList();
    toast(isOffline(err) ? 'No connection — nothing was changed.' : errorMessage(err), 'error');
  });
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
  // The row goes at once. A delete that was already confirmed has nothing left
  // to wait for, and a chat that sits there for another round trip reads as a
  // tap that missed.
  const index = state.chats.findIndex((c) => c.id === chat.id);
  const removed = index >= 0 ? state.chats.splice(index, 1)[0] : null;
  renderChatList();
  writeValue(titleKey(chat.id), '');
  writeValue('socrates.draft.' + chat.id, '');
  writeValue(viewKey(chat.id), '');
  if (state.chatId === chat.id) {
    disconnect();
    state.chatId = null;
    state.chat = null;
    setView(state.view, { silent: true });
    updateArchivedMark();
    dom.title.textContent = 'New chat';
    dom.chatSettings.hidden = true;
    dom.chatPanel.hidden = true;
    showEmptyState();
    const next = firstChatId();
    if (next) openChat(next).catch(() => {});
  }
  api('/api/chats/' + encodeURIComponent(chat.id), { method: 'DELETE', attempts: 3 })
    .then(() => refreshChats().catch(() => {}))
    .catch((err) => {
      // Still there after all: it goes back where it was rather than leaving
      // the sidebar quietly wrong.
      if (removed && index >= 0) state.chats.splice(index, 0, removed);
      renderChatList();
      toast(isOffline(err) ? 'No connection — the chat is still there.' : errorMessage(err), 'error');
    });
}

// The drawer has one switch, and everything that opens or closes it goes
// through here rather than reaching for the class: on a wide window the class
// means nothing, and on a narrow one it has to take the scrim with it.
function navOpen() {
  return document.body.classList.contains('nav-open');
}

function setNav(open) {
  setClass(document.body, 'nav-open', open);
}

function closeNav() {
  setNav(false);
}

// firstChatId is what "the chat to fall back to" means: the most recent one
// that is still in use, and only an archived one if there is nothing else.
function firstChatId() {
  const chat = state.chats.find((c) => !c.archived) || state.chats[0];
  return chat ? chat.id : '';
}

// startNewChat asks who should answer, and then clears the page for the first
// message. The chat itself is not created here: the outbox creates it inside
// the same transaction that delivers the first message, which is what makes
// "type it offline, it arrives later" work. The binding travels with it.
async function startNewChat() {
  closeNav();
  // The outbox assigns a newly created chat's id to every queued item that has
  // none, which is only safe while there can be at most one unbound chat in
  // flight. A second one would have its messages adopted into the first.
  if (state.outbox && state.outbox.items.some((item) => !item.payload.chatId)) {
    toast('Finish sending the chat you started first.');
    return;
  }
  const binding = await agents.openNewChatSheet();
  if (!binding) return;
  state.pendingBinding = binding;
  saveDraft();
  disconnect();
  state.chatId = null;
  state.chat = null;
  state.legacy = false;
  state.agentOK = true;
  state.agentLabel = '';
  state.modelLabel = '';
  setView(state.view, { silent: true });
  updateArchivedMark();
  state.rev = 0;
  state.lastAnswer = '';
  state.loadFailed = false;
  clearTimeout(state.reopenTimer);
  location.hash = '';
  dom.title.textContent = 'New chat';
  dom.chatSettings.hidden = true;
  dom.chatPanel.hidden = true;
  setBusy(false);
  paintAgentBadge();
  updateComposerMode();
  showEmptyState();
  renderChatList();
  resetAutoScreen('Tap the microphone and speak');
  restoreDraft();
  dom.input.focus();
}

// paintAgentBadge is the one line in the header that says what this chat is
// bound to. A chat with no agent - a transcript from before the rewrite - says
// so instead, and one whose agent has since been removed is marked.
function paintAgentBadge() {
  const badge = dom.agentBadge;
  if (!badge) return;
  if (state.legacy) {
    badge.hidden = false;
    badge.className = 'agent-badge warn';
    badge.replaceChildren(el('span', { class: 'b-model', text: 'No agent' }));
    badge.title = 'This chat was made before Socrates talked to agents directly.';
    return;
  }
  const binding = state.chat && state.chat.agent
    ? { agent: state.chat.agent, model: state.chat.model, effort: state.chat.effort }
    : state.pendingBinding;
  if (!binding || !binding.agent) {
    badge.hidden = true;
    badge.replaceChildren();
    return;
  }
  // The labels the server sent are the truthful ones; before there is a chat
  // to ask about, the cached catalogue says the same thing.
  const known = agents.agent(binding.agent);
  const agentLabel = state.agentLabel || (known ? known.label : binding.agent);
  const modelEntry = agents.modelOf(binding.agent, binding.model);
  const modelLabel = state.modelLabel || (modelEntry ? modelEntry.label : binding.model);
  // The three parts are separate elements so a narrow top bar can drop the
  // agent's name - which is fixed for the life of the chat and is in the
  // settings popover - rather than eat the chat's own name. The whole binding
  // stays in the title attribute either way.
  const parts = [];
  const push = (cls, text) => {
    if (!text) return;
    // The separator is a node of its own so that hiding a part takes its
    // separator with it, rather than leaving a dot hanging off the front.
    if (parts.length) parts.push(el('span', { class: 'b-sep', text: ' · ' }));
    parts.push(el('span', { class: cls, text }));
  };
  push('b-agent', agentLabel);
  push('b-model', modelLabel);
  push('b-effort', binding.effort);
  badge.hidden = false;
  badge.className = 'agent-badge' + (state.agentOK ? '' : ' warn');
  badge.replaceChildren(...parts);
  const full = [agentLabel, modelLabel, binding.effort].filter(Boolean).join(' · ');
  badge.title = state.agentOK
    ? full
    : full + ' — ' + agentLabel + ' is not available on this machine any more.';
}

// updateComposerMode swaps the composer for one sentence on a chat that can
// never be answered again. Nothing is queued from there, so the outbox never
// sees a message that can only ever fail.
//
// Hands free goes with it. Audio mode is a microphone and nothing else, so on
// a chat that cannot be answered it is a button that would record, spend a
// transcription and then be told no - which is a worse way of saying no than
// not offering it.
function updateComposerMode() {
  setClass(document.body, 'legacy-chat', state.legacy);
  if (dom.composerLegacy) dom.composerLegacy.hidden = !state.legacy;
  const auto = dom.viewSlider.querySelector('.stop[data-view="auto"]');
  if (auto) auto.setAttribute('aria-disabled', state.legacy ? 'true' : 'false');
  if (state.legacy && state.view === 'auto') setView('chat', { silent: true });
  updateMicState();
}

async function openChat(id, options = {}) {
  // Before the early return: tapping the chat that is already open is still
  // someone saying they are done with the list.
  closeNav();
  if (!options.force && state.chatId === id && state.stream && !state.loadFailed) return;
  saveDraft();
  disconnect();
  clearTimeout(state.reopenTimer);
  state.chatId = id;
  // The view belongs to the chat, and it is restored before anything is
  // fetched: a chat that was used hands free opens hands free with no signal.
  restoreView(id);
  state.rev = 0;
  state.lastAnswer = '';
  state.workSince = 0;
  state.workLabel = '';
  state.loadFailed = false;
  // Until this chat has answered for itself, nothing about the last one is
  // still true of it.
  state.agentLabel = '';
  state.modelLabel = '';
  state.agentOK = true;
  state.legacy = false;
  paintAgentBadge();
  updateComposerMode();
  location.hash = id;

  let data;
  try {
    data = await api('/api/chats/' + encodeURIComponent(id), { attempts: 2, timeout: 12000 });
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
  updateArchivedMark();
  state.defaultWorkspace = data.default_workspace || '';
  state.effectiveWorkspace = data.effective_workspace || '';
  state.agentLabel = data.agent_label || '';
  state.modelLabel = data.model_label || '';
  state.agentOK = data.agent_ok !== false;
  state.legacy = !!data.legacy;
  state.pendingBinding = null;
  paintAgentBadge();
  updateComposerMode();
  state.rev = data.rev || 0;
  dom.title.textContent = data.chat.title || 'New chat';
  writeValue(titleKey(id), data.chat.title || '');
  dom.chatSettings.hidden = false;
  fillChatPanel();
  renderSnapshot(data);
  renderChatList();
  setBusy(!!data.busy);
  connect();
  restoreDraft();
  resetAutoScreen(state.busy ? 'Working…' : 'Tap the microphone and speak');
  const lastAssistant = [...(data.messages || [])].reverse().find((m) => m.role === 'assistant');
  if (lastAssistant) state.lastAnswer = lastAssistant.content;
  if (state.view === 'auto' && lastAssistant) showAutoAnswer(lastAssistant.content, false);
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
    el('p', { text: 'This chat goes straight to the agent it is bound to.' }),
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

function updateWorkRow() {
  if (!state.busy && !queuedHere()) {
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
  const lost = isStale();
  let label;
  if (lost && queued) label = 'Saved — it will send itself when there is signal';
  else if (lost) label = 'Reconnecting — this is the last update that got through';
  else if (queued) label = queued > 1 ? 'Sending ' + queued + ' messages…' : 'Sending…';
  else label = state.workLabel || 'Working…';
  setClass(workRow, 'lost', lost);
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
    case 'tool': return (detail.name || step.title || 'Working') + '…';
    case 'subagent': return 'A subagent is working…';
    case 'reasoning': return 'Thinking…';
    case 'draft': return 'Writing the answer…';
    case 'usage': return '';
    case 'notice': return '';
    case 'error': return step.title || '';
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
  const container = turn.querySelector(':scope > .process');
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
  return status === 'running';
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
    const stopped = failed || status === 'interrupted';
    setClass(slot, 'tick', !running && !stopped && restIcon === ICONS.check);
    setClass(slot, 'cross', failed);
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
  if (node.classList.contains('open')) state.expanded.add(id);
  else state.expanded.delete(id);
}

// Every builder returns a node with an update(step) on it. buildStep is only
// ever called once per step; everything after that goes through update, so no
// running animation is interrupted by a redraw.
function buildStep(step) {
  switch (step.kind) {
    case 'tool': return buildTool(step, 'tool');
    case 'subagent': return buildTool(step, 'subagent');
    case 'reasoning': return buildCollapsible(step, 'Reasoning', ICONS.spark);
    case 'usage': return buildUsage(step);
    case 'notice': return buildNotice(step);
    case 'error': return buildError(step);
    case 'draft': return buildText(step);
    // The seven above are a closed set (§4.4) and this page ships in the same
    // binary that writes them, so nothing should ever land here. If something
    // does, it is shown as a note rather than as the answer: a kind we do not
    // understand must not be able to look like what the agent said.
    default: return buildNotice(step);
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

// A tool call is a card: what was run, on the head line, and what it printed
// underneath. It is written once and patched from there, so the spinner turns
// from the moment the call starts until the moment it ends and the output
// grows without the row being rebuilt around it.
//
// A subagent is the same card with a different tag and no exit code: it is one
// agent handing work to another, not a command with a status to report.
function buildTool(step, flavour) {
  const node = el('div', { class: 'step collapsible tool-step', 'data-step': step.id });
  const icon = statusIcon();
  const tag = el('span', { class: 'tag', text: flavour === 'subagent' ? 'agent' : 'tool' });
  const name = el('span', { class: 'name' });
  const val = el('span', { class: 'val' });
  const meta = el('span', { class: 'meta', hidden: true });
  const head = el('div', { class: 'head' },
    icon, tag, name, val, meta,
    el('span', { class: 'chev', html: ICONS.chev }),
  );
  head.addEventListener('click', () => toggleStep(node, step.id));
  // The arguments the tool was called with, when the adapter had them as JSON.
  // They are above the output because they explain it.
  const input = el('pre', { class: 'tool-input', hidden: true });
  const body = el('div', { class: 'body code' });
  node.append(head, input, body);

  node.update = (next) => {
    const detail = detailOf(next);
    icon.set(next.status);
    const label = detail.name || next.title || (flavour === 'subagent' ? 'subagent' : 'tool');
    if (name.textContent !== label) name.textContent = label;
    const line = detail.input || (detail.name ? '' : next.title || '');
    if (val.textContent !== line) val.textContent = line;
    // Only a failure is worth a number. A command that exited 0 says so by
    // being ticked.
    const exit = Number(detail.exit_code) || 0;
    const showExit = flavour !== 'subagent' && exit !== 0;
    meta.hidden = !showExit;
    if (showExit) meta.textContent = 'exit ' + exit;
    const json = detail.input_json || '';
    input.hidden = !json;
    if (json && input.textContent !== json) input.textContent = json;
    const text = next.body || (isRunning(next.status) ? '' : '(no output)');
    if (body.textContent !== text) body.textContent = text;
  };
  node.update(step);
  if (state.expanded.has(step.id)) node.classList.add('open');
  return node;
}

// What the turn cost, in one dim line. A field that is zero is left out rather
// than printed as a zero, because a number that is not known and a number that
// is nothing look the same on a phone.
function buildUsage(step) {
  const node = el('div', { class: 'step usage-step', 'data-step': step.id });
  node.update = (next) => {
    const detail = detailOf(next);
    const parts = [];
    if (detail.input) parts.push(fmtTokens(detail.input) + ' in');
    if (detail.output) parts.push(fmtTokens(detail.output) + ' out');
    if (detail.cached) parts.push(fmtTokens(detail.cached) + ' cached');
    if (detail.reasoning) parts.push(fmtTokens(detail.reasoning) + ' reasoning');
    if (detail.cost_usd) parts.push('$' + Number(detail.cost_usd).toFixed(3));
    const text = parts.join(' · ');
    if (node.textContent !== text) node.textContent = text;
    node.hidden = !text;
  };
  node.update(step);
  return node;
}

// A notice is the agent saying something about itself - a restart, a turn
// closed by a backstop, a warning about the model. One line, out of the way,
// but never silent.
function buildNotice(step) {
  const node = el('div', { class: 'step notice-step', 'data-step': step.id });
  const glyph = el('span', { class: 'glyph', html: ICONS.info });
  const text = el('span', { class: 'what' });
  node.append(glyph, text);
  node.update = (next) => {
    const body = next.body || next.title || '';
    if (text.textContent !== body) text.textContent = body;
  };
  node.update(step);
  return node;
}

// effectiveWorkspace is the directory this chat is working in right now: what
// it was pointed at, or the folder it gets by default.
function effectiveWorkspace() {
  const set = state.chat && (state.chat.workspace || '').trim();
  if (set) return set;
  return state.defaultWorkspace || state.effectiveWorkspace || '';
}

// panelFill remembers what this page last wrote into the settings dialog, so a
// live update can tell its own prefill from something the person typed. Their
// half-finished edit is never overwritten; an untouched field is.
const panelFill = { title: '', workspace: '' };

// fillChatPanel keeps the dialog showing the truth. The directory field is
// always prefilled with the directory the chat is actually working in - never
// blank, so there is nothing to guess at - and it follows the chat when the
// directory changes elsewhere, unless the field is being edited.
function fillChatPanel(options = {}) {
  if (!state.chat) return;
  const title = state.chat.title || '';
  const workspace = effectiveWorkspace();
  if (options.force || dom.panelTitle.value === panelFill.title) {
    dom.panelTitle.value = title;
    panelFill.title = title;
  }
  if (options.force || dom.panelWorkspace.value === panelFill.workspace) {
    dom.panelWorkspace.value = workspace;
    panelFill.workspace = workspace;
  }
  dom.panelWorkspace.placeholder = workspace;
  if (dom.panelWorkspaceNow) {
    dom.panelWorkspaceNow.textContent = workspace ? 'Currently running in ' + workspace : '';
    dom.panelWorkspaceNow.hidden = !workspace;
  }
}

function toggleChatPanel() {
  if (!state.chat) return;
  const opening = dom.chatPanel.hidden;
  dom.chatPanel.hidden = !opening;
  if (!opening) return;
  fillChatPanel({ force: true });
  fillBindingFields();
  dom.panelTitle.focus();
}

/* ------------------------------------------------- model and effort here */

// The agent a chat is bound to is fixed for its life, but the model it runs on
// and the effort it runs at are not - as long as nothing is running. A change
// closes the chat's agent session and opens a fresh one on the new model; the
// conversation itself resumes, so this is not a way of starting over.
const binding = { picker: null, model: '', effort: '' };

const EFFORT_LEVELS = [
  { value: '', label: 'Default' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
];

function buildBindingFields() {
  if (!dom.panelModel || binding.picker) return;
  binding.picker = combobox({
    value: '',
    items: () => agents.modelItems(state.chat ? state.chat.agent : ''),
    placeholder: 'sonnet',
    onChange: (value) => {
      binding.model = value.trim();
      renderPanelEffort();
    },
  });
  dom.panelModel.append(binding.picker.node);
  for (const level of EFFORT_LEVELS) {
    const button = el('button', {
      class: 'seg', type: 'button', 'data-value': level.value, 'aria-pressed': 'false',
    }, el('span', { class: 'seg-label', text: level.label }));
    button.addEventListener('click', () => {
      binding.effort = level.value;
      renderPanelEffort();
    });
    dom.panelEffort.append(button);
  }
}

function renderPanelEffort() {
  if (!dom.panelEffort) return;
  const model = agents.modelOf(state.chat ? state.chat.agent : '', binding.model);
  const efforts = model ? (model.efforts || []) : [];
  const entry = state.chat ? agents.agent(state.chat.agent) : null;
  const show = efforts.length > 0 || (!model && entry && entry.has_effort);
  dom.panelEffort.hidden = !show;
  if (binding.effort && efforts.length && !efforts.includes(binding.effort)) binding.effort = '';
  for (const button of dom.panelEffort.querySelectorAll('.seg')) {
    const on = button.dataset.value === binding.effort;
    button.classList.toggle('on', on);
    button.setAttribute('aria-pressed', on ? 'true' : 'false');
    button.disabled = state.busy
      || (button.dataset.value !== '' && efforts.length > 0 && !efforts.includes(button.dataset.value));
  }
}

// fillBindingFields shows what this chat is bound to right now, and says why
// the controls are dead when they are.
function fillBindingFields() {
  if (!dom.panelBinding) return;
  const chat = state.chat;
  const usable = !!(chat && chat.agent) && !state.legacy;
  dom.panelBinding.hidden = !usable;
  if (!usable) return;
  buildBindingFields();
  binding.model = chat.model || '';
  binding.effort = chat.effort || '';
  binding.picker.setValue(binding.model, false);
  renderPanelEffort();
  const input = dom.panelModel.querySelector('input');
  if (input) input.disabled = state.busy;
  const known = agents.agent(chat.agent);
  dom.panelBindingHint.textContent = state.busy
    ? 'The model can only be changed between turns.'
    : (known ? known.label : chat.agent) + ' answers in this chat. A different agent is a different chat.';
}

// saveChatSettings answers on screen and tells the server afterwards. The
// title is what the person is watching while they press Save, so it changes
// there and then; if the write did not land, it goes back and says so.
function saveChatSettings() {
  if (!state.chat) return;
  const chat = state.chat;
  // The field is prefilled with the directory the chat is actually using,
  // which for most chats is the folder they get by default. Saving that back
  // unchanged would pin them to a path that was never chosen, so it is sent
  // as "no directory of my own" - which is what it means.
  const typed = dom.panelWorkspace.value.trim();
  const workspace = typed === state.defaultWorkspace ? '' : typed;
  const title = dom.panelTitle.value.trim();
  const before = { title: chat.title, workspace: chat.workspace, model: chat.model, effort: chat.effort };
  const body = { title, workspace };
  // The model and the effort only travel when they actually changed: a PATCH
  // that names them closes the chat's agent session, and a Save that only
  // renamed the chat has no business doing that.
  const bound = !!chat.agent && !state.legacy && !dom.panelBinding.hidden;
  if (bound && binding.model && binding.model !== chat.model) body.model = binding.model;
  if (bound && (binding.effort || '') !== (chat.effort || '')) body.effort = binding.effort;
  chat.title = title;
  chat.workspace = workspace;
  if (body.model !== undefined) chat.model = body.model;
  if (body.effort !== undefined) chat.effort = body.effort;
  dom.title.textContent = title || 'New chat';
  writeValue(titleKey(chat.id), title);
  patchChatListItem(chat);
  fillChatPanel({ force: true });
  state.modelLabel = '';
  paintAgentBadge();
  dom.chatPanel.hidden = true;
  api('/api/chats/' + encodeURIComponent(chat.id), {
    method: 'PATCH',
    attempts: 2,
    body,
  }).then((data) => {
    if (!data || !data.chat) return;
    if (state.chat && state.chat.id === data.chat.id) {
      state.chat = data.chat;
      updateArchivedMark();
      dom.title.textContent = data.chat.title || 'New chat';
      writeValue(titleKey(data.chat.id), data.chat.title || '');
      fillChatPanel({ force: true });
      state.modelLabel = '';
      paintAgentBadge();
    }
    patchChatListItem(data.chat);
    refreshChats().catch(() => {});
    toast('Chat updated');
  }).catch((err) => {
    chat.title = before.title;
    chat.workspace = before.workspace;
    chat.model = before.model;
    chat.effort = before.effort;
    if (state.chat && state.chat.id === chat.id) {
      dom.title.textContent = before.title || 'New chat';
      writeValue(titleKey(chat.id), before.title || '');
      fillChatPanel({ force: true });
      state.modelLabel = '';
      paintAgentBadge();
    }
    patchChatListItem(chat);
    // A 409 here is the one refusal that passes on its own, and it is the only
    // one worth explaining rather than merely reporting: the model is a thing
    // you change between turns.
    if (isBusyConflict(err)) {
      toast('The model can only be changed between turns.', 'error');
      return;
    }
    toast(isOffline(err) ? 'No connection — the chat was not changed.' : errorMessage(err), 'error');
  });
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
    url: () => '/api/chats/' + encodeURIComponent(chatId) + '/events?rev=' + state.rev,
    onMessage: (payload) => {
      if (state.chatId !== chatId) return;
      state.lastSync = Date.now();
      handleEvent(payload);
    },
    // The status carries how it was reached: an attempt that failed is not a
    // stream being swapped, and the page has to say so at the same moment the
    // status bar does rather than a grace period later.
    onStatus: (status, extra) => {
      if (state.chatId !== chatId) return;
      setLive(status === 'live', extra);
    },
    // A stream that keeps failing looks the same whether the network is gone
    // or the session expired. One ordinary request settles it: a 401 sends the
    // person to the login page instead of leaving them watching a frozen chat
    // that will never come back.
    onFail: (attempt) => {
      if (attempt !== 3) return;
      api(chatsPath(), { attempts: 1 }).then((data) => {
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
  state.liveLostAt = Date.now();
  updateLiveUI();
}

// setLive is the single switch between "what you see is happening now" and
// "what you see is the last thing that got through".
//
// extra is what the stream knows about how it got here. immediate means the
// attempt failed rather than ended, which is the same thing the status bar
// treats as an outage worth showing at once - the two must never disagree.
function setLive(live, extra = {}) {
  const changed = state.live !== live;
  state.live = live;
  if (live) {
    if (!changed) return;
    state.lastSync = Date.now();
    state.spokeOffline = false;
    state.liveLostAt = 0;
    clearTimeout(state.staleTimer);
  } else {
    if (extra.immediate || navigator.onLine === false) {
      // Nothing left to wait for: backdate the loss so it is already past the
      // grace period, and the marks go up with the bar.
      state.liveLostAt = Date.now() - CONNECTION_GRACE;
    } else if (changed || !state.liveLostAt) {
      state.liveLostAt = Date.now();
    }
    // The stale marks appear on the same schedule as the status bar, so the
    // page never contradicts itself: nothing at all while a stream is being
    // swapped, everything the moment the loss is real.
    clearTimeout(state.staleTimer);
    state.staleTimer = setTimeout(() => { updateLiveUI(); updateWorkRow(); }, CONNECTION_GRACE + 60);
  }
  updateLiveUI();
  updateWorkRow();
  updateAutoBusy();
}

// isStale answers the only question the live marks are about: is what is on
// screen still current? A stream between connections is not an outage - a chat
// switch closes one and opens another - so a short gap says nothing, while no
// network at all says it immediately.
//
// The device saying it has no network is checked before the chat id, because a
// chat that does not exist yet is exactly where the offline story starts: a
// message typed into a blank page is queued, and a working row that says
// "Sending…" over it while there is no signal is the one thing this app must
// never do.
function isStale() {
  if (state.live) return false;
  if (navigator.onLine === false) return true;
  if (!state.chatId) return false;
  return Date.now() - (state.liveLostAt || Date.now()) >= CONNECTION_GRACE;
}

// updateLiveUI keeps every "this may be out of date" marker honest, once a
// second, whether or not anything arrived.
function updateLiveUI() {
  const stale = isStale();
  setClass(document.body, 'stale', stale);
  if (stale && !state.staleShown) {
    state.staleShown = true;
    if (state.view === 'auto' && state.busy && state.prefs.speak_in_auto_mode !== false && !state.spokeOffline) {
      // In hands free mode the person is looking at the road. A single spoken
      // notice is the only way they learn the answer they are waiting for has
      // stopped coming - which is why it was rendered while there still was a
      // connection to render it with.
      state.spokeOffline = true;
      playSpeech(offlineClip.blob).catch(() => {});
    }
  } else if (!stale) {
    state.staleShown = false;
  }
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
        if (state.view === 'auto') {
          showAutoAnswer(event.message.content, state.prefs.speak_in_auto_mode !== false);
        } else if (state.prefs.speak_in_chat_mode) {
          speak(event.message.content).catch(() => { /* reported once */ });
        }
      }
      break;
    case 'run': {
      const alive = event.run.status === 'running';
      setBusy(alive);
      if (event.run.status === 'failed' && event.run.error) toast(event.run.error, 'error');
      if (!state.busy) setAutoStatus(state.lastAnswer ? '' : 'Tap the microphone and speak');
      break;
    }
    case 'chat':
      // The chat record changed - renamed, or pointed at another directory,
      // from this page, another tab or the agent itself. Everything that shows
      // it follows at once rather than after the next reload.
      state.chat = event.chat;
      updateArchivedMark();
      dom.chatSettings.hidden = false;
      dom.title.textContent = event.chat.title || 'New chat';
      // Titles are written after the first message, so this is usually where a
      // chat gets the name an offline reload will show.
      writeValue(titleKey(event.chat.id), event.chat.title || '');
      fillChatPanel();
      patchChatListItem(event.chat);
      refreshChats();
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
      setLive(true);
      primeOfflineNotice();
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
  setClass(document.body, 'busy', busy);
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
  setClass(dom.sendBtn, 'stop', stopping);
  dom.sendBtn.disabled = stopping ? false : !dom.input.value.trim();
  dom.sendBtn.title = stopping ? 'Stop generating' : 'Send';
  dom.sendBtn.setAttribute('aria-label', dom.sendBtn.title);
  const icon = stopping ? ICONS.stop : ICONS.send;
  if (dom.sendBtn.dataset.icon !== (stopping ? 'stop' : 'send')) {
    dom.sendBtn.dataset.icon = stopping ? 'stop' : 'send';
    dom.sendBtn.innerHTML = icon;
  }
}

// The microphone follows the run: there is nothing to say into it while
// Socrates is still working.
function updateMicState() {
  const blocked = state.busy || state.legacy;
  setClass(dom.autoMic, 'busy', blocked);
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
    await api('/api/chats/' + encodeURIComponent(state.chatId) + '/stop', { method: 'POST', attempts: 3 });
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
  // A chat with no agent can never be answered, so nothing is queued from it:
  // the outbox must never hold a message that can only ever fail.
  if (state.legacy) {
    toast('This chat was made before Socrates talked to agents directly. Start a new chat.');
    return;
  }
  // The binding the sheet produced is what makes a chat that does not exist
  // yet creatable later, offline included.
  if (!state.chatId && !state.pendingBinding) {
    startNewChat().then(() => { if (state.pendingBinding) submitText(text); }).catch(() => {});
    return;
  }
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
  const binding = state.pendingBinding || {};
  state.outbox.add({
    chatId: state.chatId,
    chatKey: state.chatId ? '' : clientKey(),
    key: clientKey(),
    text,
    auto: state.view === 'auto',
    // Persisted with the item: when this is finally sent the page may have
    // been reloaded, and the create that goes with it still has to say which
    // agent answers in the chat it is making.
    agent: state.chatId ? '' : (binding.agent || ''),
    model: state.chatId ? '' : (binding.model || ''),
    effort: state.chatId ? '' : (binding.effort || ''),
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
    setClass(node, 'stuck', failed);
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
  if (state.busy) {
    toast('Wait until the current run is finished.');
    return;
  }
  stopSpeaking();
  try {
    await state.recorder.start();
  } catch (err) {
    // The microphone failing is the one error the hands free screen has to
    // carry itself: a toast behind the big screen is a silent failure.
    const message = describeMicError(err);
    toast(message, 'error');
    if (origin === 'auto') {
      resetRecordingUI();
      setAutoStatus(message);
    }
    return;
  }
  if (origin === 'auto') {
    setClass(dom.autoScreen, 'answering', false);
    dom.autoAnswer.hidden = true;
    dom.autoActions.hidden = true;
    setClass(dom.autoMic, 'recording', true);
    dom.autoMic.innerHTML = ICONS.stop;
    dom.autoTimer.hidden = false;
    dom.autoTranscript.hidden = true;
    setAutoStatus('Listening…');
    updateAutoBusy();
  } else {
    setClass(dom.micBtn, 'rec', true);
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
  setClass(dom.micBtn, 'rec', false);
  dom.micBtn.innerHTML = ICONS.mic;
  dom.recTime.hidden = true;
  setClass(dom.autoMic, 'recording', false);
  dom.autoMic.innerHTML = ICONS.mic;
  dom.autoTimer.hidden = true;
  updateAutoBusy();
}

/* ------------------------------------------------------------ audio mode */

function setAutoStatus(text) {
  dom.autoStatus.textContent = text;
}

function resetAutoScreen(status) {
  setClass(dom.autoScreen, 'answering', false);
  setAutoStatus(status || '');
  dom.autoAnswer.hidden = true;
  dom.autoActions.hidden = true;
  dom.autoTranscript.hidden = true;
  dom.autoLive.textContent = '';
  updateAutoBusy();
  updateAutoOffline(isStale());
}

// updateAutoBusy shows the same three dots on the hands free screen, so it too
// is never silent about a run that is still going.
function updateAutoBusy() {
  if (!dom.autoBusy) return;
  dom.autoBusy.hidden = !(state.busy && !state.recorder.recording);
}

// updateAutoOffline is the big screen version of the status bar. The car case
// is the whole reason this app has a hands free mode, and it is also the case
// where a frozen answer is least likely to be noticed.
function updateAutoOffline(stale) {
  if (!dom.autoOffline) return;
  dom.autoOffline.hidden = !stale;
  if (!stale) return;
  const queued = queuedHere();
  const away = state.lastSync ? Math.round((Date.now() - state.lastSync) / 1000) : 0;
  const text = queued
    ? 'No connection. What you said is saved and will be sent as soon as there is signal.'
    : 'No connection. Reconnecting…' + (away > 3 ? ' Last update ' + fmtClock(away) + ' ago.' : '');
  if (dom.autoOffline.textContent !== text) dom.autoOffline.textContent = text;
}

function updateAutoLive(step) {
  if (state.view !== 'auto') return;
  const detail = detailOf(step);
  let label = '';
  switch (step.kind) {
    case 'tool': {
      const name = detail.name || step.title || 'A tool';
      label = detail.input ? name + ': ' + detail.input : name + '…';
      break;
    }
    case 'subagent': label = 'A subagent is working…'; break;
    case 'reasoning': label = 'Thinking…'; break;
    case 'draft': label = 'Writing the answer…'; break;
    case 'notice': label = step.body || 'Note'; break;
    case 'error': label = step.title || 'Something went wrong'; break;
    // Usage is a number for afterwards, not something to say out loud while
    // the turn is still going.
    default: return;
  }
  dom.autoLive.textContent = label;
  if (state.busy) setAutoStatus('Working…');
}

// plainAnswer strips markdown so the big audio mode text stays readable.
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
  dom.autoTranscript.hidden = true;
  dom.autoLive.textContent = '';
  setAutoStatus('');
  dom.autoAnswer.hidden = false;
  dom.autoAnswer.textContent = plainAnswer(text);
  dom.autoActions.hidden = false;
  setClass(dom.autoScreen, 'answering', true);
  updateAutoBusy();
  fitAnswer();
  if (doSpeak) speak(text).catch(() => { /* reported once */ });
}

function fitAnswer() {
  const node = dom.autoAnswer;
  const length = node.textContent.length;
  const size = length < 90 ? 46 : length < 220 ? 38 : length < 480 ? 30 : 24;
  node.style.fontSize = 'clamp(20px, ' + (size / 14) + 'vw, ' + size + 'px)';
}

// Last, so that every value this module builds at load time - the working row
// among them - exists before the first line of it runs.
boot();
