// The question dialog of a chat: an agent's ask_user call (REBUILD.md §5),
// one question per step, as the biggest buttons the screen allows. On a
// phone and in touch mode it is the whole screen (so-question.css).
//
// A question arrives structured: there is no screen to read and nobody to
// type the answer into a terminal - the answer goes to the chat as JSON,
// through the chats' Outbox, once by its client_id. The shape it answers with is REBUILD §5.2:
//
//   { answers: { <question id>: { selected, other, note, delegated, skipped } },
//     general_note, follow_up }
//
// `selected` holds the ids of the chosen options ("yes" or "no" for a
// confirm question); `other` is the person's own words: "Other" beside the
// options, the whole answer of a text question, and a number question's
// number as it was typed. A question can be answered, delegated ("You
// decide"), given a note, or skipped; instead of answering, the person can
// ask back (follow_up), which the agent answers before it asks again.
//
// What is being answered is kept in this browser while it is written, so
// the card in the chat can say how far it is and a dialog that was closed
// opens where it was left; it stays until the question is answered, so an
// answer the server refused can be written again from it. It is live: a
// question answered on another device says so, and the next one takes the
// dialog. An answer still in the Outbox is shown as on its way, never as a
// question to answer again.
//
// A plan (payload.kind "plan") is not a question: the plan card in the chat
// answers it, and this dialog leaves it out.

import { api, restoreFocus, toast, errorMessage, isOffline } from './api.js';
import { html, raw, icon, morph, showOverlay, hideOverlay, M, faces } from './ui.js';
import { markdown } from './md.js';
import { agentMarkHtml } from './logos.js';
import { askQuestions } from './chatparts.js';
import { queueAnswer, answeredHere, onOutbox, pendingAnswer, chatOutbox } from './chatoutbox.js';
import { newChat, apply, openAsks } from './chatfold.js';
import { ChatStream } from './chatstream.js';
import { record, recPhase } from './dictate.js';
import { touchScreen, typeHere } from './keyboard.js';
import { speak, stopSpeaking } from './voice.js';

// A phone on its side (so-question.css "Phones and short screens"): the
// chrome above a question then takes the room its first option needs, and
// the question is scrolled up to the top.
const SHORT_SCREEN = window.matchMedia('(max-height: 500px)');

// How long "Answered" stays before the next question takes the dialog.
const MOVE_ON = 1200;

// A question - a new one, or the next step of one - is drawn on the tap that
// asked for it, and what is drawn under the finger then takes no tap for this
// long: the second tap of a double tap on Home's Questions card would
// otherwise choose the option drawn where the card was, and a double tap on
// Next would skip the step after it. A key (Enter, a digit) is never a stray
// tap, and is taken at once.
const TAP_ARM = 600;
const ARMED = '.so-q-scroll button, .so-q-actions button, .so-q-open';

// How often the dialog says its connection again while it is not live: the
// age in "Offline · updated 40 s ago" moves on (HOME.md, "The dialog says it
// too").
const CONN_EVERY = 2000;

/* ------------------------------------------------------------ the drafts */

const draftKey = (askId) => 'socrates.ask.' + askId;

// Bumped by every write of a draft, so the chat view draws a card again only
// when what it reads from the drafts may have changed.
let draftsRev = 0;

/** askDraftsRev is the revision of this tab's writes to the drafts. */
export function askDraftsRev() { return draftsRev; }

function readDraft(askId) {
  try {
    const parsed = JSON.parse(localStorage.getItem(draftKey(askId)) || 'null');
    if (parsed && typeof parsed === 'object' && parsed.answers && typeof parsed.answers === 'object') return parsed;
  } catch { /* nothing kept */ }
  return null;
}

function writeDraft(askId, draft) {
  draftsRev += 1;
  try { localStorage.setItem(draftKey(askId), JSON.stringify(draft)); } catch { /* full or private */ }
}

function forgetDraft(askId) {
  draftsRev += 1;
  try { localStorage.removeItem(draftKey(askId)); } catch { /* private */ }
}

/** forgetAskDraft lets the draft of an answered question go. */
export function forgetAskDraft(askId) { forgetDraft(askId); }

// An answer as the dialog keeps it: what is sent (selected, other, note,
// delegated), whether its fields are open, and whether the person has had
// it in hand - changed it, or moved past it - which is what the card's
// "2 of 5 answered" counts: a preselected default the person never saw is
// not an answer they gave yet.
function emptyAnswer() {
  return { selected: [], other: '', note: '', delegated: false, noting: false, othering: false, touched: false };
}

/** kindOf is a question's kind, read the way the schema means it. */
export function kindOf(q) {
  const kind = String(q.kind || '');
  if (['single', 'multi', 'text', 'confirm', 'number'].includes(kind)) return kind;
  return (q.options || []).length ? 'single' : 'text';
}

/** answered says whether one question has an answer of any kind. */
function answered(a) {
  return !!a && (a.delegated || a.selected.length > 0 || !!String(a.other || '').trim());
}

/**
 * askProgress is how far this browser is with an ask: the questions it has
 * an answer for, for the card in the chat ("2 of 5 answered").
 */
export function askProgress(ask) {
  const draft = readDraft(ask.id);
  const set = new Set();
  if (draft) {
    for (const [id, a] of Object.entries(draft.answers)) {
      if (a && a.touched && answered({ ...emptyAnswer(), ...a })) set.add(id);
    }
  }
  return { answered: set, count: set.size };
}

function startDraft(ask) {
  const draft = readDraft(ask.id) || { step: 0, answers: {}, general: '', followUp: '', asking: false };
  for (const q of askQuestions(ask)) {
    const kept = draft.answers[q.id];
    const a = { ...emptyAnswer(), ...(kept || {}) };
    // The question's own default, preselected the first time it is shown.
    if (!kept && q.default !== undefined && q.default !== null) {
      const kind = kindOf(q);
      if (kind === 'confirm') a.selected = [q.default === true || q.default === 'yes' ? 'yes' : 'no'];
      else if (kind === 'number' || kind === 'text') a.other = String(q.default);
      else a.selected = (Array.isArray(q.default) ? q.default : [q.default]).map(String);
    }
    draft.answers[q.id] = a;
  }
  return draft;
}

/** answerOf is what the agent gets back for an ask (REBUILD §5.2). */
export function answerOf(ask, draft) {
  const out = {};
  for (const q of askQuestions(ask)) {
    const a = { ...emptyAnswer(), ...(draft.answers[q.id] || {}) };
    const own = String(a.other || '').trim();
    out[q.id] = {
      selected: a.delegated ? [] : a.selected.map(String),
      other: a.delegated || !own ? null : own,
      note: String(a.note || '').trim() || null,
      delegated: !!a.delegated,
      skipped: !answered(a),
    };
  }
  const followUp = String(draft.followUp || '').trim();
  return {
    answers: out,
    general_note: String(draft.general || '').trim() || null,
    follow_up: draft.asking && followUp ? followUp : null,
  };
}

/**
 * problemOf is what is wrong with one question's answer, in words, or ''.
 * Only what can be wrong is checked: a number out of its range, fewer
 * options than the question asks for, a required question left open
 * (`final`, at Send).
 */
export function problemOf(q, a, { final = false } = {}) {
  if (!a || a.delegated) return '';
  const kind = kindOf(q);
  const own = String(a.other || '').trim();
  if (kind === 'number' && own) {
    const value = Number(own);
    if (!Number.isFinite(value)) return 'That is not a number.';
    if (q.min !== undefined && value < Number(q.min)) return 'The smallest number it takes is ' + q.min + '.';
    if (q.max !== undefined && value > Number(q.max)) return 'The largest number it takes is ' + q.max + '.';
  }
  // "Other" is one of the choices of a multi-select, for its least and its
  // most alike.
  const chosen = a.selected.length + (own ? 1 : 0);
  if (kind === 'multi' && chosen) {
    const min = Number(q.min) || 0;
    const max = Number(q.max) || 0;
    if (min && chosen < min) return 'Choose at least ' + min + '.';
    if (max && chosen > max) return 'Choose up to ' + max + ', your own words count as one.';
  }
  if (final && q.required === true && !answered(a)) return 'This question needs an answer, or “You decide”.';
  return '';
}

/* ----------------------------------------------------------- the asks */

/** fromServer is an open ask as GET /api/chats/{id} has it, as the fold keeps it. */
function fromServer(a) {
  return {
    id: a.id, turnId: a.turn_id || '', payload: a.payload || {}, at: a.at || 0, seq: Number(a.seq) || 0,
    answered: !!a.answered, answer: a.answer === undefined ? null : a.answer, byDevice: a.by_device || '',
    clientId: a.client_id || '', delivered: a.delivered || '', deliver: a.deliver || '',
  };
}

const isPlan = (ask) => !!ask && !!ask.payload && ask.payload.kind === 'plan';

/**
 * watchStream follows one ask of a chat that is not on screen: a stream of
 * its own, from the ask on, folded the way the view folds it. It does not
 * speak for the connection bar - the page is showing something else.
 */
function watchStream(chatId, ask, listener) {
  const fold = newChat();
  fold.lastSeq = Math.max(0, ask.seq - 1);
  const stream = new ChatStream({
    chatId,
    global: false,
    since: () => fold.lastSeq,
    onFrame: (frame) => {
      if (frame.t !== 'ev') return;
      apply(fold, frame);
      if (frame.ask_id !== ask.id) return;
      const now = fold.asks.get(ask.id);
      if (now) listener(now);
    },
  });
  stream.start();
  return () => stream.stop();
}

// collect reads the open questions of chats, the oldest chat first and each
// chat's questions in the order they were asked: from the view's fold for
// the chat on screen, from the server for the others. `all` has the plans,
// which the dialog leaves to their cards; `failed` is the last read that did
// not work.
async function collect({ sessions, chatId = '', askId = '', view }) {
  let failed = null;
  const lists = await Promise.all(sessions.map(async (session) => {
    const live = view ? view.liveChat(session.id) : null;
    if (live) {
      const asks = openAsks(live);
      const closed = session.id === chatId && askId ? live.asks.get(askId) : null;
      if (closed && closed.closed && !closed.answered) asks.unshift(closed);
      return asks.map((ask) => ({ chatId: session.id, session, ask }));
    }
    try {
      const data = await api('/api/chats/' + encodeURIComponent(session.id), { attempts: 2, timeout: 12000 });
      return (data.asks || []).map(fromServer).filter((ask) => !ask.answered)
        .sort((a, b) => a.seq - b.seq)
        .map((ask) => ({ chatId: session.id, session: data.session || session, ask }));
    } catch (err) {
      failed = err;
      return [];
    }
  }));
  const all = lists.flat();
  return { all, items: all.filter((item) => !isPlan(item.ask)), failed };
}

/**
 * openChatAsks opens the dialog over the open questions of chats, the
 * oldest chat first and each chat's questions in the order they were asked.
 *
 *   sessions   the chats to walk, in that order (the one asked for among them)
 *   chatId     the chat to open at
 *   askId      the ask to open at, else that chat's oldest; a closed one
 *              (the chat moved on, ask.closed) is walked too when it is the
 *              one asked for: "Answer anyway" on its card
 *   view       the chat view (chatview.js): the chat on screen is read
 *              from its fold and followed by its stream
 *   openChat(id)  the dialog's "Open chat"; also where a chat whose only
 *              open question is a plan goes, because its card answers it
 *   unreachable()  the questions could not be read for want of a
 *              connection: the caller asks again once there is one, rather
 *              than the dialog saying that it failed
 *   asking()   the chats asking now, for the walk to take up what was asked
 *              while it went, once it is at its end
 *   connection()  the page's connection line (home.js connectionLine), which
 *              the dialog says too: it is a modal over Home's
 */
export async function openChatAsks({ sessions, chatId, askId = '', view, openChat, unreachable, asking, connection }) {
  const { all, items, failed } = await collect({ sessions, chatId, askId, view });
  if (!items.length) {
    if (all.some((item) => item.chatId === chatId) && openChat) openChat(chatId);
    else if (failed && isOffline(failed) && unreachable) unreachable();
    else if (failed) toast('The questions could not be loaded: ' + errorMessage(failed), 'error');
    else toast('Nothing is waiting for an answer any more.');
    return;
  }
  let start = items.findIndex((item) => item.chatId === chatId && (!askId || item.ask.id === askId));
  if (start < 0) start = items.findIndex((item) => item.chatId === chatId);
  // The chat asked for asks nothing any more - its question was answered on
  // another device, or it moved on: another chat's question is not what was
  // asked for.
  if (start < 0 && chatId) {
    toast('Nothing is waiting for an answer any more.');
    return;
  }
  openAskDialog({
    items,
    start: Math.max(0, start),
    watch: (item, onAsk) => (view && view.watchAsk(item.chatId, item.ask.id, onAsk))
      || watchStream(item.chatId, item.ask, onAsk),
    openChat,
    // The card in the chat says how far this browser got: it is drawn again
    // once the dialog is put away.
    closed: () => { if (view) view.redraw(); },
    more: asking ? async () => (await collect({ sessions: asking(), view })).items : null,
    connection,
  });
}

/**
 * refreshAskDialog takes into the open dialog what the chats asking now ask
 * that it does not walk yet - a question asked while it was open, the one
 * that follows an answer - and the new name of a chat renamed meanwhile.
 */
export async function refreshAskDialog({ sessions, view }) {
  if (!current) return;
  const { items } = await collect({ sessions, view });
  if (current) current.extend(items, sessions);
}

/* ------------------------------------------------------------ the dialog */

let current = null;

/** askDialogOpen says whether the dialog is on screen. */
export function askDialogOpen() { return !!current; }

/**
 * closeAskDialog closes the dialog, for a page that is about to put a
 * session in front of the person: a modal left open would hold it inert.
 */
export function closeAskDialog() { if (current) current.close(); }

/**
 * openAskDialog walks open questions.
 *
 *   items       [{ chatId, session, ask }]
 *   start       the index to open at
 *   watch(item, onAsk)  follows one ask live; onAsk(ask) with its newest
 *               state; answers a function that stops following
 *   openChat(id)        the "Open chat" of the dialog
 *   closed()            the dialog has been put away
 *   more()              the questions open now, for the walk to take up at
 *               its end what was asked while it went
 *   connection()        the page's connection line, {kind, words}: while it
 *               is not live the dialog says so under its top bar, that the
 *               question may already have been answered, and dims it
 */
export function openAskDialog({ items, start = 0, watch, openChat, closed, more = null, connection = null }) {
  if (current) { current.jump(items, start); return current; }
  const opener = document.activeElement;
  const dialog = document.createElement('dialog');
  dialog.className = 'ds-dialog so-question so-askdlg';
  dialog.setAttribute('aria-label', 'Questions');
  const backdrop = document.createElement('div');
  backdrop.className = 'ds-dialog__backdrop';
  const shell = document.createElement('div');
  shell.className = 'ds-dialog__box so-q-shell';
  shell.tabIndex = -1;
  dialog.append(backdrop, shell);

  const st = {
    items: items.slice(), at: Math.max(0, Math.min(start, items.length - 1)), draft: null, phase: 'ask',
    details: new Set(), speaking: false, unwatch: null, timer: null, closed: false, problem: null,
    refused: null, armTimer: null, connTimer: null, connSaid: '',
  };

  // arm makes the buttons take no tap for TAP_ARM (the CSS takes the tap
  // away; the capture below is for a click the CSS did not stop).
  function arm() {
    clearTimeout(st.armTimer);
    shell.setAttribute('data-arming', '');
    st.armTimer = setTimeout(() => { st.armTimer = null; shell.removeAttribute('data-arming'); }, TAP_ARM);
  }

  // bringQuestionUp scrolls a question to the top on a phone on its side,
  // the chat and the batch's title and context a swipe above it.
  function bringQuestionUp() {
    if (!SHORT_SCREEN.matches) return;
    const scroller = shell.querySelector('.so-q-scroll');
    const at = shell.querySelector('.so-q-step') || shell.querySelector('.so-q-text');
    if (scroller && at) scroller.scrollTop += at.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
  }

  function item() { return st.items[st.at] || null; }

  function settledPhase(ask) {
    return answeredHere(ask.clientId) ? 'answered' : 'elsewhere';
  }

  function load() {
    if (st.unwatch) { st.unwatch(); st.unwatch = null; }
    const one = item();
    if (!one) return;
    st.draft = startDraft(one.ask);
    st.problem = null;
    st.details.clear();
    st.phase = one.ask.answered ? settledPhase(one.ask) : 'ask';
    // An answer from here is already on its way: the dialog says so. One
    // the server refused is said above the questions, to be sent again.
    const pending = one.ask.answered ? null : pendingAnswer(one.chatId, one.ask.id);
    st.refused = pending && pending.state === 'failed' ? pending : null;
    if (pending && !st.refused) st.phase = 'sent';
    st.unwatch = watch ? watch(one, (ask) => {
      if (st.closed || item() !== one) return;
      one.ask = ask;
      if (ask.answered && (st.phase === 'ask' || st.phase === 'sent')) {
        st.phase = settledPhase(ask);
        forgetDraft(ask.id);
        draw();
        if (st.phase === 'answered') moveOnSoon();
      } else if (st.phase === 'elsewhere') {
        draw();
      }
    }) : null;
  }

  function save() {
    const one = item();
    if (one && st.phase === 'ask') writeDraft(one.ask.id, st.draft);
  }

  function moveOnSoon() {
    clearTimeout(st.timer);
    st.timer = setTimeout(() => next(), MOVE_ON);
  }

  async function next() {
    clearTimeout(st.timer);
    // At its end the walk takes up what was asked while it went: the
    // question that follows the one just answered, another chat's.
    if (st.at >= st.items.length - 1 && more && !st.closed) {
      try { extend(await more()); } catch { /* the walk ends here */ }
      if (st.closed) return;
    }
    if (st.at < st.items.length - 1) {
      st.at += 1;
      load();
      draw(true);
    } else close();
  }

  // extend adds the questions the walk does not have yet, and the new names
  // of its chats, without taking the step it is at away from the person.
  function extend(list, sessions = []) {
    if (st.closed) return;
    const known = new Set(st.items.map((one) => one.ask.id));
    let changed = false;
    for (const one of list || []) {
      if (known.has(one.ask.id) || one.ask.answered) continue;
      known.add(one.ask.id);
      st.items.push(one);
      changed = true;
    }
    for (const session of sessions) {
      for (const one of st.items) {
        if (one.chatId === session.id && one.session && session.title && one.session.title !== session.title) {
          one.session = { ...one.session, title: session.title };
          changed = true;
        }
      }
    }
    if (changed) draw();
  }

  function close() {
    if (st.closed) return;
    st.closed = true;
    clearTimeout(st.timer);
    clearTimeout(st.armTimer);
    clearInterval(st.connTimer);
    if (st.unwatch) st.unwatch();
    unlisten();
    unrefused();
    if (window.visualViewport) window.visualViewport.removeEventListener('resize', keepFieldInView);
    if (st.speaking) stopSpeaking();
    save();
    current = null;
    if (closed) closed();
    hideOverlay(dialog, { remove: true }).then(() => restoreFocus(opener));
  }

  // A keyboard that comes up shrinks what can be seen: the field being
  // typed into is brought into the part of the dialog that is left, above
  // its buttons, once the new height is laid out - with the question it
  // answers above it when the two fit together (on a phone on its side the
  // field alone was brought in, and the question was cut in half above
  // it), else the field alone.
  function keepFieldInView() {
    requestAnimationFrame(() => {
      const field = document.activeElement;
      if (st.closed || !field || !shell.contains(field) || !field.matches('textarea, input')) return;
      const scroller = shell.querySelector('.so-q-scroll');
      const question = shell.querySelector('.so-q-text');
      if (scroller && question && scroller.contains(field) && scroller.contains(question)) {
        const box = scroller.getBoundingClientRect();
        const pad = parseFloat(getComputedStyle(scroller).paddingTop) || 0;
        const top = question.getBoundingClientRect().top - box.top + scroller.scrollTop - pad;
        const bottom = field.getBoundingClientRect().bottom - box.top + scroller.scrollTop;
        if (top >= 0 && bottom - top <= scroller.clientHeight) {
          scroller.scrollTop = top;
          return;
        }
      }
      field.scrollIntoView({ block: 'nearest' });
    });
  }
  if (window.visualViewport) window.visualViewport.addEventListener('resize', keepFieldInView);

  // An answer of this browser that the server refused because another
  // device had answered first: the question is settled, and says by whom
  // once the stream has it.
  const unlisten = onOutbox((news) => {
    const one = item();
    if (news.type !== 'elsewhere' || !one || news.payload.ask !== one.ask.id || st.closed) return;
    st.phase = 'elsewhere';
    forgetDraft(one.ask.id);
    draw();
  });

  // An answer on its way that the server refused for good gives the
  // questions back, with the reason, to be sent again.
  const unrefused = chatOutbox().onChange(() => {
    const one = item();
    if (st.closed || !one || st.phase !== 'sent') return;
    const pending = pendingAnswer(one.chatId, one.ask.id);
    if (!pending || pending.state !== 'failed') {
      // "Saved" or "Sending" follows the connection.
      draw();
      return;
    }
    st.refused = pending;
    st.phase = 'ask';
    draw();
  });

  /* ---------------------------------------------------------- drawing */

  function questionsOf() { return askQuestions(item().ask); }

  // doubt is the connection line while what the dialog shows may be old -
  // connecting again, checking a quiet stream, offline - and null while it
  // is live, or polled and current. The page's first connect is no doubt:
  // the dialog read its questions from the server as it opened.
  function doubt() {
    const conn = connection ? connection() : null;
    return conn && !conn.first && (conn.kind === 'connecting' || conn.kind === 'offline') ? conn : null;
  }

  // connHtml is Home's connection line as the dialog says it: a DS alert
  // under the top bar. A question still to be answered may have been
  // answered on another device meanwhile, and nothing would have said so.
  function connHtml(conn) {
    return html`<div class="${'ds-alert ' + (conn.kind === 'offline' ? 'ds-alert--error' : 'ds-alert--warning') + ' so-q-conn'}" role="status" data-ds-key="conn" data-status="${conn.kind}">${raw(icon('wifiOff', 20))}<span class="ds-alert__text"><span class="so-q-conn-words">${conn.words}</span>${
      st.phase === 'ask' ? html`<span class="so-q-conn-detail">This question may already have been answered.</span>` : ''
    }</span></div>`;
  }

  function draw(focus = false) {
    const one = item();
    if (!one) { close(); return; }
    const ask = one.ask;
    const payload = ask.payload || {};
    const questions = questionsOf();
    const step = Math.max(0, Math.min(st.draft.step || 0, Math.max(0, questions.length - 1)));
    st.draft.step = step;
    const q = questions[step];
    const last = step >= questions.length - 1;
    const session = one.session || {};
    const counter = st.items.length > 1 ? (st.at + 1) + ' of ' + st.items.length : '';
    const settled = st.phase === 'answered' || st.phase === 'elsewhere';
    const conn = settled ? null : doubt();
    st.connSaid = conn ? conn.kind + ':' + conn.words + ':' + st.phase : '';
    shell.toggleAttribute('data-stale', !!conn);
    // The top bar: Close, where the walk is, and Open chat at its end; on a
    // short screen the chat's name too, on one line, in place of the session
    // row (so-question.css). The connection line comes under it while the
    // connection is not live. The batch's title, when it has context, is the
    // context's own row rather than a row of its own.
    const title = session.title || 'Chat';
    morph(shell, html`<div class="so-q-top" data-ds-key="top"><button class="ds-btn ds-btn--quiet ds-btn--icon so-q-close" type="button" data-action="close" aria-label="Close" data-tip="Close">${raw(icon('close', 20))}</button><span class="so-q-top-title">${counter ? 'Questions · ' + counter : 'Question'}</span><span class="so-q-top-session so-ellipsis" data-tip="${title}">${title}</span>${
      openChat ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon so-q-open" type="button" data-action="open-chat" aria-label="Open the chat" data-tip="Open the chat">${raw(icon('external', 20))}</button>` : ''
    }</div>${
      conn ? connHtml(conn) : ''
    }
<div class="so-q-scroll" data-ds-key="${'scroll:' + ask.id}"><div class="so-q-session"><span class="so-mark">${raw(agentMarkHtml(session.harness || '', 24))}</span><span class="so-q-session-text"><span class="so-q-session-title so-break">${title}</span></span></div>${settled ? doneHtml(ask) : html`<div class="so-q-body so-ask-body" data-ds-key="${'q:' + ask.id + ':' + step}"${st.phase === 'sent' ? html` data-state="pending"` : ''}>${
      step === 0 && payload.context ? contextHtml('ctx', payload.context, payload.title || 'About these questions', !!payload.title)
        : step === 0 && payload.title ? html`<p class="so-ask-title">${payload.title}</p>` : ''
    }${st.refused ? html`<div class="ds-alert ds-alert--error so-refused" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${'Your answer was not taken: ' + (st.refused.error || 'the server refused it.') + ' Send it again, or change it first.'}</span></div>` : ''}${q ? questionHtml(q, questions.length, step, last) : ''}${
      st.draft.asking ? followUpHtml() : ''
    }${last && !st.draft.asking ? generalHtml() : ''}</div>`}${
      st.phase === 'sent' ? html`<div class="ds-alert so-q-status" role="status">${raw(icon('clock', 20))}<span class="ds-alert__text">${navigator.onLine === false ? 'Saved. Your answer goes out when the connection is back.' : 'Sending your answer…'}</span></div>` : ''
    }</div>
<div class="so-q-actions" data-ds-key="actions">${actionsHtml({ step, last })}</div>`);
    if (focus) {
      arm();
      const target = shell.querySelector('.so-q-text') || shell;
      target.focus({ preventScroll: true });
      bringQuestionUp();
    }
  }

  // A context behind its row: "Why this matters", or the batch's title
  // (`titled`), which opens onto what the agent said about the questions.
  // On a short screen "Why this matters" is its icon beside the question
  // (so-question.css), and its words are then its tip.
  function contextHtml(key, text, label, titled = false) {
    const open = st.details.has(key);
    const tip = !titled && SHORT_SCREEN.matches ? label : '';
    return html`<details class="ds-collapsible so-ask-context" data-open-key="${key}"${open ? html` open` : ''}><summary class="so-ask-context__head"${tip ? html` data-tip="${tip}"` : ''}>${raw(icon('info', 16))}<span${titled ? html` class="so-ask-title"` : ''}>${label}</span>${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="so-ask-context__body so-md">${markdown(text)}</div></details>`;
  }

  function questionHtml(q, total, step, last) {
    const a = st.draft.answers[q.id];
    const kind = kindOf(q);
    const options = kind === 'confirm'
      ? [{ id: 'yes', label: 'Yes' }, { id: 'no', label: 'No' }]
      : (q.options || []).slice(0, 12);
    const multi = kind === 'multi';
    const delegate = q.allow_delegate !== false;
    const other = q.allow_other !== false && (kind === 'single' || kind === 'multi');
    const hintWords = multi ? multiHint(q) : '';
    const problem = st.problem && st.problem.q === q.id ? st.problem.text : '';
    return html`<div class="so-q-step">${total > 1 ? html`<span>${'Question ' + (step + 1) + ' of ' + total}</span>` : ''}${q.header ? html`<span class="so-q-chip">${q.header}</span>` : ''}${q.required === true ? html`<span class="so-q-chip">Required</span>` : ''}</div>
<h2 class="so-q-text" tabindex="-1">${q.question || q.header || ''}</h2>${q.context ? contextHtml('qctx:' + q.id, q.context, 'Why this matters') : ''}${
      hintWords ? html`<p class="so-q-detail">${hintWords}</p>` : ''
    }${options.length ? html`<div class="so-q-options" role="group" aria-label="${q.question || 'Options'}">${options.map((option, i) => optionHtml(q, option, i, multi, a))}</div>` : ''}${
      kind === 'text' ? fieldHtml(q.id, 'other', 'Your answer', a.other, 3, last ? 'done' : 'next') : ''
    }${kind === 'number' ? numberHtml(q, a, !!problem, last ? 'done' : 'next') : ''}${
      other && (a.othering || a.other) ? fieldHtml(q.id, 'other', 'In your own words', a.other, 2, last ? 'done' : 'next') : ''
    }${a.noting || a.note ? fieldHtml(q.id, 'note', 'A note to the agent', a.note, 2) : ''}${
      problem ? html`<div class="ds-alert ds-alert--error so-ask-problem" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${problem}</span></div>` : ''
    }<div class="so-ask-extras">${
      delegate ? html`<button class="ds-btn ds-btn--quiet so-ask-delegate" type="button" data-action="delegate" data-q="${q.id}" aria-pressed="${a.delegated ? 'true' : 'false'}">${raw(icon('sparkles', 16))}You decide</button>` : ''
    }${other && !(a.othering || a.other) ? html`<button class="ds-btn ds-btn--quiet so-ask-other" type="button" data-action="other" data-q="${q.id}">${raw(icon('pencil', 16))}Other</button>` : ''}${
      !(a.noting || a.note) ? html`<button class="ds-btn ds-btn--quiet so-ask-note" type="button" data-action="note" data-q="${q.id}">${raw(icon('compose', 16))}Add a note</button>` : ''
    }</div>`;
  }

  function multiHint(q) {
    const min = Number(q.min) || 0;
    const max = Number(q.max) || 0;
    if (min && max) return 'Choose ' + (min === max ? min : min + ' to ' + max) + '.';
    if (max) return 'Choose up to ' + max + '.';
    if (min) return 'Choose at least ' + min + '.';
    return 'Choose any.';
  }

  function optionHtml(q, option, i, multi, a) {
    const id = String(option.id ?? option.label);
    const chosen = !a.delegated && a.selected.includes(id);
    const detailKey = 'opt:' + q.id + ':' + id;
    const open = st.details.has(detailKey);
    return html`<div class="so-ask-option" data-ds-key="${'o:' + id}"><button class="ds-card ds-card--interactive so-q-option" type="button" data-action="choose" data-q="${q.id}" data-option="${id}" aria-pressed="${chosen ? 'true' : 'false'}"${multi ? html` data-multi` : ''}><span class="so-q-option-row"><span class="so-q-option-ring" aria-hidden="true"></span><span class="so-q-option-key" aria-hidden="true">${chosen ? raw(icon('check', 20)) : String(i + 1)}</span><span class="so-q-option-text"><span class="so-q-option-label">${option.label || id}</span>${
      option.description ? html`<span class="so-q-option-desc">${option.description}</span>` : ''
    }${option.recommended ? html`<span class="ds-status ds-status--ok so-q-recommended">${raw(icon('thumbUp', 16))}<span>Recommended</span></span>` : ''}</span></span></button>${
      option.details ? html`<button class="ds-btn ds-btn--text ds-btn--sm so-ask-more" type="button" data-action="details" data-key="${detailKey}" aria-expanded="${open ? 'true' : 'false'}">${open ? 'Fewer details' : 'Details'}${raw(icon('chevronDown', 16, 'ds-chevron'))}</button>${open ? html`<div class="so-ask-details so-md">${markdown(option.details)}</div>` : ''}` : ''
    }</div>`;
  }

  // A field of one's own words, with its microphone. An answer's field
  // moves on with the keyboard's own key (`key`: "next", or "done" on the
  // last question, which puts the keyboard away); a note keeps Enter for
  // its lines.
  function fieldHtml(qid, field, label, value, rows, key = 'enter') {
    const fid = 'ask-' + qid + '-' + field;
    return html`<div class="ds-field so-q-own" data-ds-key="${fid}"><label class="ds-field__label" for="${fid}">${label}</label><div class="so-field-row so-field-row--top"><textarea class="ds-input so-q-own-field" id="${fid}" rows="${rows}" enterkeyhint="${key}" data-q="${qid}" data-field="${field}"${key !== 'enter' ? html` data-moves-on` : ''}>${value || ''}</textarea><button class="ds-btn ds-btn--quiet ds-btn--icon so-mic" type="button" data-action="dictate" data-target="${fid}" aria-label="${'Dictate ' + label.charAt(0).toLowerCase() + label.slice(1)}" data-tip="Dictate">${faces(raw(icon('mic', 20)), '')}</button></div></div>`;
  }

  function numberHtml(q, a, invalid, key) {
    const fid = 'ask-' + q.id + '-other';
    const range = [q.min !== undefined ? 'at least ' + q.min : '', q.max !== undefined ? 'at most ' + q.max : ''].filter(Boolean).join(', ');
    return html`<div class="ds-field so-q-own" data-ds-key="${fid}"><label class="ds-field__label" for="${fid}">A number${range ? html` <span class="so-ask-range">${'(' + range + ')'}</span>` : ''}</label><input class="ds-input so-ask-number" id="${fid}" type="text" inputmode="decimal" enterkeyhint="${key}" autocomplete="off" data-q="${q.id}" data-field="other" data-moves-on value="${a.other || ''}"${invalid ? html` aria-invalid="true"` : ''}></div>`;
  }

  function generalHtml() {
    return st.draft.general || st.details.has('general')
      ? fieldHtml('all', 'general', 'Anything else the agent should know', st.draft.general, 2)
      : html`<button class="ds-btn ds-btn--quiet so-ask-general" type="button" data-action="general">${raw(icon('compose', 16))}Anything else?</button>`;
  }

  function followUpHtml() {
    return html`<div class="so-ask-followup">${fieldHtml('all', 'followUp', 'Your question to the agent', st.draft.followUp, 3)}<p class="so-q-detail">The agent answers it first, and then asks again.</p></div>`;
  }

  function doneHtml(ask) {
    const elsewhere = st.phase === 'elsewhere';
    return html`<div class="ds-empty so-q-finished" role="status" data-ds-key="done"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('check', 24))}</span><p class="ds-empty__title">${elsewhere ? 'Answered on another device' : 'Answered'}</p>${
      elsewhere ? html`<p class="ds-empty__text">${(ask.byDevice ? 'On ' + ask.byDevice + '. ' : '') + 'Nothing was sent from here.'}</p>` : ''
    }</div>`;
  }

  function actionsHtml({ step, last }) {
    const reading = st.speaking ? 'Stop reading' : 'Read aloud';
    const read = html`<button class="ds-btn ds-btn--icon so-q-big so-q-read" type="button" data-action="read" aria-pressed="${st.speaking ? 'true' : 'false'}" aria-label="${reading}" data-tip="${reading}">${raw(icon(st.speaking ? 'stop' : 'speak', 20))}</button>`;
    if (st.phase === 'answered' || st.phase === 'elsewhere') {
      return html`<button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="next">${st.at < st.items.length - 1 ? 'Next question' : 'Close'}</button>`;
    }
    if (st.phase === 'sent') {
      const more = st.at < st.items.length - 1;
      return html`${read}<button class="ds-btn so-q-big" type="button" data-action="${more ? 'next' : 'close'}">${more ? 'Next question' : 'Close'}</button>`;
    }
    if (st.draft.asking) {
      return html`${read}<button class="ds-btn ds-btn--icon so-q-big so-q-back" type="button" data-action="ask-cancel" aria-label="Back to the questions" data-tip="Back to the questions">${raw(icon('arrowLeft', 20))}</button><button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="send">Send question</button>`;
    }
    return html`${read}<button class="ds-btn ds-btn--icon so-q-big so-ask-back" type="button" data-action="ask-back" aria-label="Ask the agent back" data-tip="Ask back: a question to the agent first">${raw(icon('chat', 20))}</button>${
      step > 0 ? html`<button class="ds-btn ds-btn--icon so-q-big so-q-back" type="button" data-action="back" aria-label="Previous question" data-tip="Previous question">${raw(icon('arrowLeft', 20))}</button>` : ''
    }<button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="${last ? 'send' : 'forward'}">${last ? 'Send answers' : 'Next'}</button>`;
  }

  /* ---------------------------------------------------------- reading */

  // sayable is what Read aloud says: the batch's title and context with the
  // first question, then the question, its context and its options.
  function sayable() {
    const one = item();
    const payload = one.ask.payload || {};
    const step = st.draft.step || 0;
    const q = questionsOf()[step];
    const parts = [];
    if (step === 0) parts.push(payload.title || '', payload.context || '');
    if (q) {
      parts.push(q.question || q.header || '');
      if (q.context) parts.push(q.context);
      const options = kindOf(q) === 'confirm' ? [{ label: 'Yes' }, { label: 'No' }] : (q.options || []);
      options.forEach((option, i) => {
        parts.push((i + 1) + ': ' + (option.label || option.id) + (option.description ? '. ' + option.description : '') + '.');
      });
    }
    return parts.filter(Boolean).join('\n');
  }

  async function toggleRead() {
    if (st.speaking) { st.speaking = false; stopSpeaking(); draw(); return; }
    st.speaking = true;
    draw();
    try { await speak(sayable()); } catch { /* voice.js says why */ }
    st.speaking = false;
    if (!st.closed) draw();
  }

  /* ---------------------------------------------------------- answering */

  // submit puts the answer on its way. The draft stays until the question is
  // answered: a refusal gives the questions back as they were.
  function submit(answer) {
    const one = item();
    save();
    st.refused = null;
    st.phase = 'sent';
    queueAnswer(one.chatId, one.ask.id, answer);
    draw();
  }

  // check holds the step on a problem with its answer, and says what it is.
  function check(q, final = false) {
    const text = q ? problemOf(q, st.draft.answers[q.id], { final }) : '';
    st.problem = text ? { q: q.id, text } : null;
    return !text;
  }

  function sendAnswers() {
    const questions = questionsOf();
    if (!st.draft.asking) {
      for (let i = 0; i < questions.length; i += 1) {
        if (!check(questions[i], true)) {
          st.draft.step = i;
          save();
          draw(true);
          return;
        }
      }
    }
    submit(answerOf(item().ask, st.draft));
  }

  function choose(qid, optionId) {
    const q = questionsOf().find((one) => one.id === qid);
    const a = st.draft.answers[qid];
    a.delegated = false;
    a.touched = true;
    if (kindOf(q) === 'multi') {
      const at = a.selected.indexOf(optionId);
      if (at >= 0) a.selected.splice(at, 1);
      else if (!(Number(q.max) && a.selected.length + (String(a.other || '').trim() ? 1 : 0) >= Number(q.max))) a.selected.push(optionId);
      else toast('Choose up to ' + q.max + '.');
    } else {
      a.selected = [optionId];
      // One answer to a question of one: an option is not also one's own words.
      if (kindOf(q) === 'single') { a.other = ''; a.othering = false; }
    }
    if (st.problem && st.problem.q === qid) st.problem = null;
    save();
    draw();
  }

  shell.addEventListener('click', async (event) => {
    // Opening and closing a context is the dialog's own state, so that a
    // redraw keeps it the way the person left it.
    const summary = event.target.closest('summary');
    if (summary && summary.parentElement && summary.parentElement.dataset.openKey) {
      event.preventDefault();
      const key = summary.parentElement.dataset.openKey;
      if (st.details.has(key)) st.details.delete(key); else st.details.add(key);
      draw();
      return;
    }
    const target = event.target.closest('[data-action]');
    if (!target || !shell.contains(target)) return;
    const qid = target.dataset.q;
    switch (target.dataset.action) {
      case 'close': close(); break;
      case 'open-chat': { const one = item(); close(); if (openChat) openChat(one.chatId); break; }
      case 'choose': choose(qid, target.dataset.option); break;
      case 'delegate': {
        const a = st.draft.answers[qid];
        a.delegated = !a.delegated;
        a.touched = true;
        if (a.delegated) { a.selected = []; a.other = ''; a.othering = false; }
        st.problem = null;
        save();
        draw();
        break;
      }
      case 'other': st.draft.answers[qid].othering = true; draw(); focusField('ask-' + qid + '-other'); break;
      case 'note': st.draft.answers[qid].noting = true; draw(); focusField('ask-' + qid + '-note'); break;
      case 'general': st.details.add('general'); draw(); focusField('ask-all-general'); break;
      case 'details': {
        const key = target.dataset.key;
        if (st.details.has(key)) st.details.delete(key); else st.details.add(key);
        draw();
        break;
      }
      case 'forward': {
        const q = questionsOf()[st.draft.step || 0];
        if (!check(q)) { draw(); break; }
        if (q) st.draft.answers[q.id].touched = true;
        st.draft.step += 1;
        save();
        draw(true);
        break;
      }
      case 'back': st.problem = null; st.draft.step = Math.max(0, st.draft.step - 1); save(); draw(true); break;
      case 'ask-back': st.draft.asking = true; st.problem = null; save(); draw(); focusField('ask-all-followUp'); break;
      case 'ask-cancel': st.draft.asking = false; save(); draw(); break;
      case 'send':
        if (st.draft.asking && !String(st.draft.followUp || '').trim()) {
          focusField('ask-all-followUp');
          toast('Write the question first.');
          break;
        }
        sendAnswers();
        break;
      case 'next': next(); break;
      case 'read': toggleRead(); break;
      case 'dictate': {
        if (recPhase() !== 'idle') return;
        const field = shell.querySelector('#' + CSS.escape(target.dataset.target));
        const text = await M.busy(target, () => record({ opener: target }), { done: false });
        if (text && field && field.isConnected) {
          field.value = field.value ? field.value.replace(/\s*$/, ' ') + text : text;
          field.dispatchEvent(new Event('input', { bubbles: true }));
        }
        break;
      }
      default: break;
    }
  });

  // focusField is a field a button just brought (Other, Add a note,
  // Anything else?, Ask the agent back): asked for by a tap, so it takes the
  // keyboard at once, not at a second tap.
  function focusField(fid) {
    typeHere(shell.querySelector('#' + CSS.escape(fid)));
  }

  shell.addEventListener('input', (event) => {
    const field = event.target.closest('[data-field]');
    if (!field) return;
    const name = field.dataset.field;
    if (name === 'general' || name === 'followUp') {
      st.draft[name] = field.value;
      save();
      return;
    }
    const qid = field.dataset.q || '';
    const q = questionsOf().find((one) => one.id === qid);
    const a = st.draft.answers[qid];
    if (!a || !q) return;
    a[name] = field.value;
    a.touched = true;
    let redraw = false;
    if (name === 'other' && field.value.trim()) {
      redraw = a.delegated || (kindOf(q) === 'single' && a.selected.length > 0);
      a.delegated = false;
      // One's own words to a question of one take the place of an option.
      if (kindOf(q) === 'single') a.selected = [];
    }
    save();
    // The field keeps its focus and its text through the redraw (morph
    // never replaces the focused element); it only runs when the buttons
    // beside it have something new to say.
    if (redraw) draw();
  });

  // A click with a pointer behind it (detail > 0: a key's click has none)
  // on a button that is still arming is a stray tap, and goes nowhere.
  shell.addEventListener('click', (event) => {
    if (!event.detail || !shell.hasAttribute('data-arming')) return;
    if (!event.target || !event.target.closest || !event.target.closest(ARMED)) return;
    event.preventDefault();
    event.stopPropagation();
  }, true);

  dialog.addEventListener('close', () => close());
  dialog.addEventListener('keydown', (event) => {
    // The keyboard's own key on an answer's field: "next" goes on to the
    // next question, as Next does, and "done" on the last one takes the
    // focus to Send answers, which puts a phone's keyboard away (Shift+Enter
    // is still a new line). That key is a touch screen's: at a desk Enter in
    // a field of several lines is a new line, and Ctrl or Cmd+Enter moves
    // on; a one-line field (a number) moves on with Enter.
    const moving = event.target.closest('[data-moves-on]');
    if (moving && event.key === 'Enter' && !event.shiftKey && !event.isComposing && st.phase === 'ask') {
      if (moving.localName === 'textarea' && !touchScreen() && !event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      if (moving.getAttribute('enterkeyhint') === 'next') shell.querySelector('[data-action="forward"]')?.click();
      else (shell.querySelector('[data-action="send"]') || shell).focus();
      return;
    }
    if (event.target.closest('textarea, input') || st.phase !== 'ask') return;
    if (!/^[1-9]$/.test(event.key)) return;
    const option = shell.querySelectorAll('.so-q-option')[Number(event.key) - 1];
    if (option) { event.preventDefault(); option.click(); }
  });

  current = {
    jump(list, at) {
      st.items = list.slice();
      st.at = Math.max(0, Math.min(at, list.length - 1));
      load();
      draw(true);
    },
    extend,
    close,
  };
  load();
  draw();
  arm();
  // The line follows the connection, and is said again while it is down;
  // a dialog whose line has not changed is not drawn again.
  if (connection) {
    st.connTimer = setInterval(() => {
      if (st.closed || !item()) return;
      const conn = st.phase === 'answered' || st.phase === 'elsewhere' ? null : doubt();
      if ((conn ? conn.kind + ':' + conn.words + ':' + st.phase : '') !== st.connSaid) draw();
    }, CONN_EVERY);
  }
  showOverlay(dialog).then(() => {
    const target = shell.querySelector('.so-q-text') || shell;
    target.focus({ preventScroll: true });
    bringQuestionUp();
  });
  return current;
}
