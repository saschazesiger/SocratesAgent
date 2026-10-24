// The chat view: an agent session drawn by the page itself (REBUILD.md §7.1).
//
// It takes the stage where a terminal stands for a Shell: the conversation
// above, the dock below - the connection and the agent's own state when they
// are not what they should be, the task list while a turn works through one,
// and the composer.
//
// The conversation is the chat's journal folded the server's way
// (chatfold.js), kept current by its stream (chatstream.js). Every entry of
// the timeline - a message, a question, a line between them - is an element
// of its own that is patched with morph() only when its markup changed, and
// inside it every part is keyed (data-ds-key): a delta re-renders the one
// message it belongs to, an earlier part keeps its element, and nothing that
// turns ever starts turning again. Rendering is gathered into one frame,
// however many events arrive in it.
//
// Nothing on screen claims to be current when it may not be: while the stream
// is not live the conversation is dimmed and the dock says why, and what the
// person sends meanwhile is on screen as "Not sent yet" until it is, then as
// "Sent" until the journal has it.

import { alertDialog, toast, fmtTokens, CONNECTION_GRACE } from './api.js';
import { html, morph, raw, icon } from './ui.js';
import { agentMarkHtml } from './logos.js';
import * as harnesses from './harnesses.js';
import { newChat, apply, applyLive, busy, currentTodo, seenClient } from './chatfold.js';
import { ChatStream } from './chatstream.js';
import {
  assistantMessage, userMessage, lineHtml, askCard, todoList, todoItems, renewsIn, seconds, outboxState,
} from './chatparts.js';
import { mountComposer } from './composer.js';
import { chatOutbox, pendingFor, pendingAnswer, failedItem, queueAnswer, answeredHere } from './chatoutbox.js';
import { askProgress, askDraftsRev, forgetAskDraft } from './askdialog.js';

// How many chats keep their fold in memory, so that going back to one does
// not replay its whole journal.
const KEEP = 8;
// How close to the bottom counts as "at the end", where new content follows.
const STICK = 64;

// What the person said to the chat that it shows as theirs until the journal
// has it: a message, and a slash command. A change of model shows in the
// composer, an answer on its question's card.
const SAID = new Set(['message', 'command']);

// The names a usage window goes by, for the chat's "i".
const WINDOW_WORDS = { five_hour: '5-hour limit', weekly: 'Weekly limit' };

/**
 * mountChat makes the chat view in `ctx.host` (#chat).
 *
 * `ctx`:
 *   status()           the stream's state moved (for the page's stale marks)
 *   openAsk(id, askId) open the question dialog at one ask of this chat
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
  // The parts opened or closed by hand, by key; everything else is drawn in
  // its own default (a reasoning part shut, a group open).
  const open = new Map();
  let openRev = 0;
  // The markup of every timeline entry at its revision, and the element it
  // is drawn into, by the entry's key.
  const renders = new Map();
  const drawn = new Map();
  const askWatchers = new Map();
  // What this browser sent that the server took and the journal does not
  // show yet: kept on screen as "Sent" so a message never blinks out between
  // its answer and its event. Keyed by client_id.
  const sent = new Map();
  // The same for answers, by ask: the card says "Sending" until the journal
  // says the question is answered, rather than asking again in between.
  const sentAnswers = new Map();
  let outboxIds = new Map();
  // Bumped by every change of the Outbox: what a card shows of this
  // browser's own answers and commands is drawn again then, and only then.
  let outboxRev = 0;
  // The asks whose answer has been seen, whose draft has been let go.
  const settledAsks = new Set();

  // "Latest" is the scroller's last child and sticks to its bottom edge, so
  // it is always just above the dock, however tall the dock is.
  morph(host, html`<div class="so-chat__scroll" data-ds-key="scroll"><div class="so-chat__log" role="log" aria-label="Conversation" aria-live="polite" aria-relevant="additions"></div><button class="ds-btn ds-btn--tonal ds-btn--sm so-chat__jump" type="button" data-action="jump" hidden>${raw(icon('arrowDown', 16))}Latest</button></div>
<div class="so-chat__dock" data-ds-key="dock"><div class="so-chat__state" role="status" aria-live="polite"></div><div class="ds-alert so-ticker" hidden>${raw(icon('activity', 20))}<span class="ds-alert__text so-ticker__line" role="status" aria-live="polite"></span></div><div class="so-chat__todo" hidden></div><div class="so-chat__composer"></div></div>`);
  const scroller = host.querySelector('.so-chat__scroll');
  const log = host.querySelector('.so-chat__log');
  const jump = host.querySelector('.so-chat__jump');
  const stateLine = host.querySelector('.so-chat__state');
  // The spoken Status's one line (assist.js), in the dock where a terminal
  // has it over its pane.
  const tickerBox = host.querySelector('.so-ticker');
  const tickerLine = host.querySelector('.so-ticker__line');
  const todoSlot = host.querySelector('.so-chat__todo');
  const composerSlot = host.querySelector('.so-chat__composer');

  /* ---------------------------------------------------------- the view */

  const view = {
    get chat() { return chat; },
    isOpen(key, fallback) { return open.has(key) ? open.get(key) : fallback; },
    askProgress,
    mine: (ask) => answeredHere(ask.clientId),
    pendingAnswer: (ask) => pendingAnswer(session.id, ask.id) || sentAnswers.get(ask.id) || null,
    failedItem: (clientId) => failedItem(clientId),
    modelLabel(model) {
      const found = session && harnesses.modelOf(session.harness, model);
      if (found) return found.label || found.id;
      const listed = chat && chat.info && Array.isArray(chat.info.models)
        ? chat.info.models.find((one) => one.id === model) : null;
      return listed ? (listed.label || listed.id) : model;
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

  // entryMarkup is one timeline entry, from a cache keyed by what it is
  // drawn from, so that a long chat costs one string per change.
  function entryMarkup(entry) {
    let rev;
    let make;
    // Whether this browser is online is in what its own answers say ("Not
    // sent yet" or "Sending").
    const local = outboxRev + ':' + (navigator.onLine !== false);
    if (entry.type === 'message') {
      const message = chat.messages.get(entry.id);
      if (!message) return '';
      if (message.role === 'user') {
        rev = message.rev;
        make = () => userMessage({
          key: entry.key, text: message.text, attachments: message.attachments,
          state: message.state === 'delivered' ? '' : message.state, answerTo: message.answerTo,
        });
      } else {
        // A plan part is drawn from the question it waits on, and from the
        // answer this browser has on its way to it.
        rev = message.rev + ':' + chat.askRev;
        if (message.parts.some((part) => part.kind === 'plan')) rev += ':' + local;
        make = () => assistantMessage(message, view);
      }
    } else if (entry.type === 'ask') {
      const ask = chat.asks.get(entry.id);
      if (!ask) return '';
      // An answered question needs its draft no more, wherever it was
      // answered.
      if (ask.answered && !settledAsks.has(ask.id)) {
        settledAsks.add(ask.id);
        sentAnswers.delete(ask.id);
        forgetAskDraft(ask.id);
      }
      // "2 of 5 answered" is read from this browser's draft of the answer,
      // which the dialog writes.
      rev = ask.rev + ':' + askDraftsRev() + ':' + local;
      make = () => askCard(ask, view);
    } else if (entry.line && entry.line.kind === 'turn' && entry.line.outcome === 'limited') {
      // "Renews in" moves with the clock.
      return String(lineHtml(entry, view));
    } else {
      rev = entry.rev;
      // A command line says when the agent refused what this browser sent.
      if (entry.line && entry.line.kind === 'command') rev += ':' + outboxRev;
      make = () => lineHtml(entry, view);
    }
    const hit = renders.get(entry.key);
    if (hit && hit.rev === rev && hit.openRev === openRev) return hit.markup;
    const markup = String(make());
    renders.set(entry.key, { rev, openRev, markup });
    return markup;
  }

  // noteSent keeps what left the outbox for this chat as sent: an item that
  // went without failing reached the server, which has it once.
  function noteSent(items) {
    const now = new Map();
    for (const item of items) if (item.payload && (SAID.has(item.payload.kind) || item.payload.kind === 'answer')) now.set(item.id, item);
    for (const [id, item] of outboxIds) {
      if (now.has(id) || item.state === 'failed') continue;
      if (item.payload.kind === 'answer') sentAnswers.set(item.payload.ask, { ...item, attempts: 0, error: '' });
      else sent.set(item.payload.client_id, { payload: item.payload, at: Date.now() });
    }
    outboxIds = now;
  }

  // localEntries is what this browser sent to the chat that the journal does
  // not have yet: in the Outbox, on its way or waiting for a connection, or
  // taken by the server a moment ago.
  function localEntries() {
    for (const clientId of [...sent.keys()]) if (seenClient(chat, clientId)) sent.delete(clientId);
    const out = [];
    for (const item of pendingFor(session.id)) {
      if (!SAID.has(item.payload.kind) || seenClient(chat, item.payload.client_id)) continue;
      const state = outboxState(item);
      const failed = state === 'failed';
      out.push({
        key: 'o:' + item.payload.client_id,
        markup: String(html`${userMessage({
          key: 'o:' + item.payload.client_id, text: item.payload.text, attachments: item.payload.attachments, state,
          error: failed ? item.error : '',
        })}${failed ? html`<div class="so-msg__actions" data-ds-key="${'oa:' + item.payload.client_id}"><button class="ds-btn ds-btn--sm" type="button" data-action="outbox-retry" data-id="${item.id}">Try again</button><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="outbox-edit" data-id="${item.id}">Edit</button></div>` : ''}`),
      });
    }
    for (const [clientId, one] of sent) {
      if (one.payload.chat !== session.id) continue;
      out.push({
        key: 'o:' + clientId,
        markup: String(userMessage({ key: 'o:' + clientId, text: one.payload.text, attachments: one.payload.attachments, state: 'pending' })),
      });
    }
    return out;
  }

  // place puts the entries into the log in order: an entry whose markup did
  // not change is not touched at all, a changed one is patched in place, and
  // only entries that came or went are inserted or removed.
  function place(entries) {
    const wanted = new Set();
    let at = log.firstElementChild;
    for (const entry of entries) {
      if (!entry.markup) continue;
      wanted.add(entry.key);
      let one = drawn.get(entry.key);
      if (!one) {
        const node = document.createElement('div');
        node.className = 'so-chat__entry';
        node.dataset.dsKey = entry.key;
        one = { node, markup: '' };
        drawn.set(entry.key, one);
      }
      if (one.markup !== entry.markup) {
        morph(one.node, entry.markup);
        one.markup = entry.markup;
      }
      if (one.node === at) at = at.nextElementSibling;
      else log.insertBefore(one.node, at);
    }
    for (const [key, one] of drawn) {
      if (wanted.has(key)) continue;
      one.node.remove();
      drawn.delete(key);
    }
  }

  function render() {
    if (!session || !chat) return;
    const follow = stuck || atEnd();
    const entries = chat.timeline.map((entry) => ({ key: entry.key, markup: entryMarkup(entry) }));
    const local = localEntries();
    if (synced && !chat.timeline.length && !local.length) {
      entries.push({ key: 'empty', markup: String(html`<div class="ds-empty so-chat__empty"><span class="ds-empty__disc" aria-hidden="true">${raw(agentMarkHtml(session.harness, 24))}</span><p class="ds-empty__title">${'Ask ' + harnesses.label(session.harness)}</p><p class="ds-empty__text">Write the first message below.</p></div>`) });
    }
    if (!synced && !chat.timeline.length) {
      entries.push({ key: 'loading', markup: String(html`<div class="ds-loading so-chat__loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Loading the conversation…</div>`) });
    }
    place(entries.concat(local));
    drawTodo();
    drawState();
    if (composer) composer.paint();
    if (follow) {
      scroller.scrollTop = scroller.scrollHeight;
      stuck = true;
    }
    jump.hidden = atEnd();
  }

  // drawTodo pins the task list above the composer while a turn runs: the
  // newest list, when this turn wrote it or it still has work in it. A list
  // an earlier turn finished is history, and stays where it was written.
  function drawTodo() {
    let todo = busy(chat) ? currentTodo(chat) : null;
    if (todo) {
      const message = chat.messages.get(todo.messageId);
      const thisTurn = !!message && !!chat.turn.id && message.turnId === chat.turn.id;
      if (!thisTurn && todoItems(todo).every((item) => item.status === 'completed')) todo = null;
    }
    todoSlot.hidden = !todo;
    morph(todoSlot, todo ? todoList(todo, { pinned: true }) : '');
  }

  // stale says whether what is on screen may be behind: the stream is not
  // live, and has not been for longer than a blink.
  function stale() {
    if (!session) return false;
    const status = stream ? stream.status() : 'connecting';
    return status !== 'live' && (navigator.onLine === false || (lostAt > 0 && Date.now() - lostAt >= CONNECTION_GRACE));
  }

  // drawState is the dock's line: the connection when it is not live, else
  // the agent when it is not well.
  function drawState() {
    const old = stale();
    host.toggleAttribute('data-stale', old);
    let line = '';
    if (old) {
      line = navigator.onLine === false
        ? html`<div class="ds-alert ds-alert--warning" data-ds-key="offline">${raw(icon('wifiOff', 20))}<span class="ds-alert__text">Offline. The chat may be behind; what you send goes out when the connection is back.</span></div>`
        : html`<div class="ds-alert ds-alert--warning" data-ds-key="reconnecting"><span class="so-chat__spin" aria-hidden="true"><span class="ds-spinner"></span></span><span class="ds-alert__text">Reconnecting… The chat may be behind.</span></div>`;
    } else if (chat.process === 'failed' && !chat.turn) {
      line = html`<div class="ds-alert ds-alert--error" data-ds-key="failed">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">The agent could not start. Send a message to try again.</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" data-action="info" aria-label="Why it did not start" data-tip="${chat.processDetail || 'No detail was given.'}">${raw(icon('info', 18))}</button></div>`;
    } else if (chat.process === 'starting') {
      line = html`<div class="ds-alert" data-ds-key="starting"><span class="so-chat__spin" aria-hidden="true"><span class="ds-spinner"></span></span><span class="ds-alert__text">Starting the agent…</span></div>`;
    }
    morph(stateLine, line);
  }

  // usageFacts is the chat's usage for its "i": the context it holds, what
  // it has cost, and every usage window with when it renews.
  function usageFacts() {
    if (!chat) return [];
    const facts = [];
    const u = chat.usage || {};
    if (u.context && u.window) {
      facts.push('Context: ' + fmtTokens(u.context) + ' of ' + fmtTokens(u.window)
        + ' (' + Math.round((u.context / u.window) * 100) + '%)');
    }
    if (chat.cost) facts.push('Cost so far: $' + chat.cost.toFixed(4));
    for (const [name, limit] of Object.entries(chat.rateLimits)) {
      const renew = renewsIn(limit.resetsAt);
      facts.push((WINDOW_WORDS[name] || name) + ': ' + Math.round(limit.used * 100) + '% used'
        + (renew ? ' · renews in ' + renew : ''));
    }
    if (chat.lastTurn && chat.lastTurn.endedAt && chat.lastTurn.startedAt) {
      facts.push('Last turn: ' + seconds(chat.lastTurn.endedAt - chat.lastTurn.startedAt));
    }
    if (chat.info && chat.info.version) facts.push(harnesses.label(session.harness) + ' ' + chat.info.version);
    if (chat.conversation) facts.push('Conversation ' + chat.conversation);
    return facts;
  }

  /* ------------------------------------------------------------ the stream */

  function onFrame(f) {
    switch (f.t) {
      case 'ev':
        if (apply(chat, f)) schedule();
        if (f.type === 'ask' || f.type === 'ask.answered' || f.type === 'ask.delivered' || f.type === 'ask.closed') tellAsk(f.ask_id);
        break;
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
    if (status !== 'live') {
      staleTimer = setTimeout(() => { schedule(); ctx.status(); }, CONNECTION_GRACE + 50);
    }
    schedule();
    ctx.status();
  }

  function tellAsk(askId) {
    const ask = chat.asks.get(askId);
    const listeners = askWatchers.get(askId);
    if (ask && listeners) for (const listener of [...listeners]) listener(ask);
  }

  /* ------------------------------------------------------------- actions */

  // toggle opens or shuts one part. The open state is the view's own, so a
  // re-render in the middle of a stream can never take the person's tap back.
  function toggle(key, fallback) {
    open.set(key, !view.isOpen(key, fallback));
    openRev += 1;
    render();
  }

  host.addEventListener('click', (event) => {
    if (!session) return;
    const summary = event.target.closest('summary');
    const details = summary && summary.parentElement;
    if (details && details.dataset.openKey && log.contains(details)) {
      event.preventDefault();
      toggle(details.dataset.openKey, details.open);
      return;
    }
    const target = event.target.closest('[data-action]');
    if (!target || !host.contains(target)) return;
    switch (target.dataset.action) {
      case 'copy-code': {
        const code = target.closest('.so-code')?.querySelector('code');
        if (!code) return;
        navigator.clipboard.writeText(code.textContent).then(
          () => toast('Copied.'),
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
      case 'outbox-drop':
        chatOutbox().drop(target.dataset.id);
        break;
      case 'ask-again':
        // The refused answer is let go; the dialog opens on the draft it
        // was written from, which is kept until the question is answered.
        chatOutbox().drop(target.dataset.id);
        ctx.openAsk(session.id, target.dataset.ask);
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
        scroller.scrollTo({ top: scroller.scrollHeight, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
        break;
      default:
        break;
    }
  });

  scroller.addEventListener('scroll', () => {
    stuck = atEnd();
    jump.hidden = stuck;
  }, { passive: true });

  // Files dropped anywhere on the chat go into the composer.
  host.addEventListener('dragover', (event) => {
    if (![...(event.dataTransfer?.types || [])].includes('Files')) return;
    event.preventDefault();
    host.setAttribute('data-dropping', '');
  });
  host.addEventListener('dragleave', (event) => {
    if (!host.contains(event.relatedTarget)) host.removeAttribute('data-dropping');
  });
  host.addEventListener('drop', (event) => {
    host.removeAttribute('data-dropping');
    const dropped = [...(event.dataTransfer?.files || [])];
    if (!dropped.length || !composer) return;
    event.preventDefault();
    composer.addFiles(dropped);
  });

  chatOutbox().onChange((items) => {
    noteSent(items);
    outboxRev += 1;
    if (session) schedule();
  });
  window.addEventListener('online', () => schedule());
  window.addEventListener('offline', () => schedule());

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
      for (const one of drawn.values()) one.node.remove();
      drawn.clear();
      open.clear();
      openRev += 1;
      synced = chat.lastSeq > 0;
      stuck = true;
      lostAt = 0;
      host.hidden = false;
      composer = mountComposer(composerSlot, {
        session: () => session,
        chat: () => chat,
        busy: () => busy(chat),
        stale: () => stale(),
        usage: () => usageFacts(),
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
    stale,
    insert(text) { if (composer) composer.insert(text); },
    focus() { if (composer) composer.focus(); },
    field: () => (composer ? composer.field() : null),

    /**
     * liveChat is the fold of the chat on screen, kept current by its
     * stream, or null for any other chat.
     */
    liveChat: (id) => (session && session.id === id && stream ? chat : null),

    /**
     * watchAsk follows one ask of the chat on screen: listener(ask) with its
     * newest state. It answers the function that stops following, or null
     * when that chat is not on screen and the caller follows it some other
     * way.
     */
    watchAsk(chatId, askId, listener) {
      if (!session || session.id !== chatId || !stream) return null;
      if (!askWatchers.has(askId)) askWatchers.set(askId, new Set());
      askWatchers.get(askId).add(listener);
      return () => askWatchers.get(askId)?.delete(listener);
    },

    /** redraw draws again what this browser's own drafts show. */
    redraw: () => schedule(),

    /** ticker is where the spoken Status says what it is doing. */
    ticker: () => ({ box: tickerBox, line: tickerLine }),
  };
}
