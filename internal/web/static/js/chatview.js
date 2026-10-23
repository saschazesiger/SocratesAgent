// The chat view: an agent session drawn by the page itself (REBUILD.md §7.1).
//
// It takes the stage where a terminal stands for a Shell: the conversation
// above, the dock below - the connection and the agent's own state when they
// are not what they should be, the task list while a turn works through one,
// and the composer.
//
// The conversation is the chat's journal folded the server's way
// (chatfold.js), kept current by its stream (chatstream.js), and drawn with
// morph(): every message, part and line is keyed, so a delta re-renders the
// one message it belongs to, an earlier part keeps its element, and nothing
// that turns ever starts turning again. Rendering is gathered into one frame,
// however many events arrive in it.
//
// Nothing on screen claims to be current when it may not be: while the stream
// is not live the conversation is dimmed and the dock says why, and what the
// person sends meanwhile is on screen as "Not sent yet" until it is.

import { alertDialog, toast, CONNECTION_GRACE } from './api.js';
import { html, morph, raw, icon, DS } from './ui.js';
import { agentMarkHtml } from './logos.js';
import * as harnesses from './harnesses.js';
import { newChat, apply, applyLive, busy, currentTodo, userMessageByClient } from './chatfold.js';
import { ChatStream } from './chatstream.js';
import {
  assistantMessage, userMessage, lineHtml, askCard, todoList, renewsIn, partHtml,
} from './chatparts.js';
import { mountComposer } from './composer.js';
import { chatOutbox, pendingFor, queueAnswer, answeredHere } from './chatoutbox.js';
import { askProgress } from './askdialog.js';

// How many chats keep their fold in memory, so that going back to one does
// not replay its whole journal.
const KEEP = 8;
// How close to the bottom counts as "at the end", where new content follows.
const STICK = 64;

/**
 * mountChat makes the chat view in `host` (#chat).
 *
 * `ctx`:
 *   status(status)     the stream's state, for the page's stale marks
 *   read(id)           the chat on screen finished a turn: it is not unread
 *   openAsk(id, askId) open the question dialog at one ask of this chat
 *   sessionOf(id)      the newest row of a session
 */
export function mountChat(ctx) {
  const host = ctx.host;
  const chats = new Map();
  let session = null;
  let chat = null;
  let stream = null;
  let composer = null;
  let frame = 0;
  let stuck = true;
  let synced = false;
  let lostAt = 0;
  let staleTimer = null;
  const open = new Map();
  let openRev = 0;
  const renders = new Map();
  const askWatchers = new Map();

  morph(host, html`<div class="so-chat__scroll" data-ds-key="scroll"><div class="so-chat__log" role="log" aria-label="Conversation" aria-live="polite" aria-relevant="additions"></div></div>
<button class="ds-btn ds-btn--tonal ds-btn--sm so-chat__jump" type="button" data-action="jump" hidden>${raw(icon('arrowDown', 16))}Latest</button>
<div class="so-chat__dock" data-ds-key="dock"><div class="so-chat__state" role="status" aria-live="polite"></div><div class="so-chat__todo" hidden></div><div class="so-chat__composer"></div></div>`);
  const scroller = host.querySelector('.so-chat__scroll');
  const log = host.querySelector('.so-chat__log');
  const jump = host.querySelector('.so-chat__jump');
  const stateLine = host.querySelector('.so-chat__state');
  const todoSlot = host.querySelector('.so-chat__todo');
  const composerSlot = host.querySelector('.so-chat__composer');

  /* ---------------------------------------------------------- the view */

  const view = {
    get chat() { return chat; },
    isOpen(key, fallback) { return open.has(key) ? open.get(key) : fallback; },
    askProgress,
    mine: (ask) => answeredHere(ask.clientId),
    modelLabel(model) {
      const found = session && harnesses.modelOf(session.harness, model);
      return found ? (found.label || found.id) : model;
    },
    effortLabel: (effort) => harnesses.effortLabel(effort),
    renewal() {
      let latest = 0;
      for (const limit of Object.values(chat.rateLimits)) if (limit.used >= 1 && limit.resetsAt > latest) latest = limit.resetsAt;
      return latest ? renewsIn(latest) : '';
    },
  };

  /* ------------------------------------------------------------- render */

  function schedule() {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      render();
    });
  }

  function atEnd() {
    return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= STICK;
  }

  // entryMarkup is one timeline entry, from a cache keyed by its revision and
  // the open/closed choices, so a long chat costs one string per change.
  function entryMarkup(entry) {
    let rev;
    let make;
    if (entry.type === 'message') {
      const message = chat.messages.get(entry.id);
      if (!message) return '';
      rev = message.rev;
      make = () => (message.role === 'user' ? userMessage({
        key: entry.key, text: message.text, attachments: message.attachments,
        state: message.state === 'delivered' ? '' : message.state, answerTo: message.answerTo,
      }) : assistantMessage(message, view));
    } else if (entry.type === 'ask') {
      const ask = chat.asks.get(entry.id);
      if (!ask) return '';
      rev = chat.rev;
      make = () => askCard(ask, view);
    } else {
      rev = entry.rev;
      make = () => lineHtml(entry, view);
    }
    const hit = renders.get(entry.key);
    if (hit && hit.rev === rev && hit.openRev === openRev && hit.chat === chat) return hit.markup;
    const markup = make();
    renders.set(entry.key, { rev, openRev, chat, markup });
    return markup;
  }

  // localMessages is what this browser sent to the chat that the journal does
  // not have yet: in the Outbox, on its way or waiting for a connection.
  function localMessages() {
    return pendingFor(session.id)
      .filter((item) => item.payload.kind !== 'answer')
      .filter((item) => !userMessageByClient(chat, item.payload.client_id))
      .map((item) => {
        let state = 'sending';
        if (item.state === 'failed') state = 'failed';
        else if (navigator.onLine === false || (item.attempts > 0 && item.error)) state = 'local';
        const failed = item.state === 'failed';
        return html`${userMessage({
          key: 'o:' + item.payload.client_id, text: item.payload.text, attachments: item.payload.attachments, state,
          error: failed ? item.error : '',
        })}${failed ? html`<div class="so-msg__actions" data-ds-key="${'oa:' + item.payload.client_id}"><button class="ds-btn ds-btn--sm" type="button" data-action="outbox-retry" data-id="${item.id}">Try again</button><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="outbox-edit" data-id="${item.id}">Edit</button></div>` : ''}`;
      });
  }

  function render() {
    if (!session || !chat) return;
    const follow = stuck || atEnd();
    const entries = chat.timeline.map(entryMarkup);
    const local = localMessages();
    const empty = synced && !chat.timeline.length && !local.length;
    morph(log, html`${empty ? html`<div class="ds-empty so-chat__empty" data-ds-key="empty"><span class="ds-empty__disc" aria-hidden="true">${raw(agentMarkHtml(session.harness, 24))}</span><p class="ds-empty__title">${'Ask ' + harnesses.label(session.harness)}</p><p class="ds-empty__text">Write the first message below.</p></div>` : ''}${entries}${local}${
      !synced && !chat.timeline.length ? html`<div class="ds-loading so-chat__loading" role="status" data-ds-key="loading"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Loading the conversation…</div>` : ''
    }`);
    drawTodo();
    drawState();
    if (composer) composer.paint();
    if (follow) {
      scroller.scrollTop = scroller.scrollHeight;
      stuck = true;
    }
    jump.hidden = atEnd();
  }

  function drawTodo() {
    const todo = busy(chat) ? currentTodo(chat) : null;
    todoSlot.hidden = !todo;
    morph(todoSlot, todo ? todoList(todo, { pinned: true }) : '');
  }

  // drawState is the dock's line: the connection when it is not live, else
  // the agent when it is not well.
  function drawState() {
    const status = stream ? stream.status() : 'connecting';
    const stale = status !== 'live' && (navigator.onLine === false || (lostAt && Date.now() - lostAt >= CONNECTION_GRACE));
    host.toggleAttribute('data-stale', stale);
    let line = '';
    if (stale) {
      line = navigator.onLine === false
        ? html`<div class="ds-alert ds-alert--warning" data-ds-key="offline">${raw(icon('wifiOff', 20))}<span class="ds-alert__text">Offline. The chat may be behind; what you send goes out when the connection is back.</span></div>`
        : html`<div class="ds-alert ds-alert--warning" data-ds-key="reconnecting"><span class="so-q-spin" aria-hidden="true"><span class="ds-spinner"></span></span><span class="ds-alert__text">Reconnecting… The chat may be behind.</span></div>`;
    } else if (chat.process === 'failed' && !chat.turn) {
      line = html`<div class="ds-alert ds-alert--error" data-ds-key="failed">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">The agent could not start. Send a message to try again.</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" data-action="info" aria-label="Details" data-tip="${chat.processDetail || 'no detail'}">${raw(icon('info', 18))}</button></div>`;
    } else if (chat.process === 'starting') {
      line = html`<div class="ds-alert" data-ds-key="starting"><span class="so-q-spin" aria-hidden="true"><span class="ds-spinner"></span></span><span class="ds-alert__text">Starting the agent…</span></div>`;
    }
    morph(stateLine, line);
  }

  /* ------------------------------------------------------------ the stream */

  function onFrame(f) {
    switch (f.t) {
      case 'ev': {
        const wasBusy = busy(chat);
        if (apply(chat, f)) schedule();
        if (f.type === 'turn.ended' && wasBusy && synced && document.visibilityState === 'visible') ctx.read(session.id);
        if (f.type === 'ask' || f.type === 'ask.answered' || f.type === 'ask.delivered') tellAsk(f.ask_id);
        break;
      }
      case 'delta':
        if (applyLive(chat, f)) schedule();
        break;
      case 'synced': {
        const first = !synced;
        synced = true;
        if (first) stuck = true;
        schedule();
        break;
      }
      default:
        break;
    }
  }

  function onStatus(status) {
    if (status === 'live') lostAt = 0;
    else if (!lostAt) lostAt = Date.now();
    clearTimeout(staleTimer);
    if (status !== 'live') staleTimer = setTimeout(() => schedule(), CONNECTION_GRACE + 50);
    schedule();
    ctx.status(status);
  }

  function tellAsk(askId) {
    const ask = chat.asks.get(askId);
    const listeners = askWatchers.get(askId);
    if (ask && listeners) for (const listener of listeners) listener(ask);
  }

  /* ------------------------------------------------------------- actions */

  host.addEventListener('click', (event) => {
    const target = event.target.closest('[data-action]');
    if (!target || !host.contains(target)) return;
    switch (target.dataset.action) {
      case 'copy-code': {
        const code = target.closest('.so-code')?.querySelector('code');
        if (!code) return;
        navigator.clipboard.writeText(code.textContent).then(
          () => DS.motion.crossfade(target, icon('check', 18), { kind: 'icon' }),
          () => toast('The code could not be copied.', 'error'),
        );
        break;
      }
      case 'info':
        event.preventDefault();
        alertDialog({ title: target.getAttribute('aria-label') || 'Details', lines: String(target.dataset.tip || '').split('\n'), confirmLabel: 'Close', opener: target });
        break;
      case 'ask-open':
        ctx.openAsk(session.id, target.dataset.ask);
        break;
      case 'plan-approve':
        queueAnswer(session.id, target.dataset.ask, { decision: 'approve' });
        break;
      case 'plan-note':
        open.set('note:' + target.dataset.part, true);
        openRev += 1;
        render();
        host.querySelector('[data-plan-note="' + CSS.escape(target.dataset.ask) + '"]')?.focus();
        break;
      case 'plan-send': {
        const field = host.querySelector('[data-plan-note="' + CSS.escape(target.dataset.ask) + '"]');
        queueAnswer(session.id, target.dataset.ask, { decision: 'keep_planning', note: field ? field.value.trim() : '' });
        break;
      }
      case 'outbox-retry':
        chatOutbox().retry(target.dataset.id);
        break;
      case 'outbox-edit': {
        const item = chatOutbox().items.find((one) => one.id === target.dataset.id);
        if (!item) return;
        chatOutbox().drop(item.id);
        if (composer) composer.insert(item.payload.text || '');
        break;
      }
      case 'jump':
        stuck = true;
        scroller.scrollTo({ top: scroller.scrollHeight, behavior: DS.motion.reduced && DS.motion.reduced() ? 'auto' : 'smooth' });
        break;
      default:
        break;
    }
  });

  // A part opened or closed by hand stays that way through every re-render.
  host.addEventListener('toggle', (event) => {
    const node = event.target;
    if (!node.dataset || !node.dataset.openKey || !log.contains(node)) return;
    const was = view.isOpen(node.dataset.openKey, !node.open);
    if (was === node.open && open.has(node.dataset.openKey)) return;
    open.set(node.dataset.openKey, node.open);
    openRev += 1;
    schedule();
  }, true);

  scroller.addEventListener('scroll', () => {
    stuck = atEnd();
    jump.hidden = stuck;
  }, { passive: true });

  // Files dropped anywhere on the chat go into the composer.
  host.addEventListener('dragover', (event) => {
    if ([...(event.dataTransfer?.types || [])].includes('Files')) event.preventDefault();
  });
  host.addEventListener('drop', (event) => {
    const dropped = [...(event.dataTransfer?.files || [])];
    if (!dropped.length || !composer) return;
    event.preventDefault();
    composer.addFiles(dropped);
  });

  chatOutbox().onChange(() => schedule());

  /* --------------------------------------------------------------- api */

  function stop() {
    if (stream) { stream.stop(); stream = null; }
    if (composer) { composer.destroy(); composer = null; }
    if (frame) { cancelAnimationFrame(frame); frame = 0; }
    clearTimeout(staleTimer);
  }

  return {
    /** show puts a chat on the stage. */
    show(next) {
      if (session && session.id === next.id && stream) { session = next; schedule(); return; }
      stop();
      session = next;
      chat = chats.get(next.id) || newChat();
      chats.delete(next.id);
      chats.set(next.id, chat);
      while (chats.size > KEEP) chats.delete(chats.keys().next().value);
      renders.clear();
      open.clear();
      openRev += 1;
      synced = chat.lastSeq > 0;
      stuck = true;
      lostAt = 0;
      host.hidden = false;
      morph(log, '');
      composer = mountComposer(composerSlot, {
        session: () => session,
        chat: () => chat,
        busy: () => busy(chat),
        live: () => !!stream && stream.live(),
        sent: () => { stuck = true; schedule(); },
        changed: () => schedule(),
      });
      stream = new ChatStream({ chatId: next.id, since: () => chat.lastSeq, onFrame, onStatus });
      stream.start();
      render();
    },

    /** update takes a newer row of the chat on screen. */
    update(next) {
      if (session && session.id === next.id) { session = next; schedule(); }
    },

    hide() {
      stop();
      session = null;
      host.hidden = true;
      host.removeAttribute('data-stale');
    },

    shownId: () => (session ? session.id : ''),
    live: () => !!stream && stream.live(),
    insert(text) { if (composer) composer.insert(text); },
    focus() { if (composer) composer.focus(); },
    field: () => (composer ? composer.field() : null),

    /** chatOf is the fold of a chat this view holds, or null. */
    chatOf: (id) => chats.get(id) || null,

    /**
     * watchAsk follows one ask of the chat on screen; it answers false when
     * that chat is not on screen, and the caller follows it some other way.
     */
    watchAsk(chatId, askId, listener) {
      if (!session || session.id !== chatId) return null;
      if (!askWatchers.has(askId)) askWatchers.set(askId, new Set());
      askWatchers.get(askId).add(listener);
      return () => askWatchers.get(askId)?.delete(listener);
    },

    /** partHtml is exported for the dialog's plan text and for tests. */
    partHtml,
  };
}
