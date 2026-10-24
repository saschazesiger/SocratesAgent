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

import { api, alertDialog, promptDialog, toast, errorMessage, fmtTokens, CONNECTION_GRACE } from './api.js';
import { M, html, morph, raw, icon } from './ui.js';
import { connectionSaidElsewhere, retryConnection } from './net.js';
import { agentMarkHtml } from './logos.js';
import * as harnesses from './harnesses.js';
import * as accounts from './accounts.js';
import './accountpicker.js';
import { newChat, apply, applyLive, busy, currentTodo, seenClient } from './chatfold.js';
import { ChatStream } from './chatstream.js';
import {
  assistantMessage, userMessage, lineHtml, askCard, todoList, todoItems, renewsIn, seconds, outboxState,
} from './chatparts.js';
import { mountComposer } from './composer.js';
import { chatOutbox, pendingFor, pendingAnswer, pendingModel, failedItem, queueAnswer, answeredHere } from './chatoutbox.js';
import { askProgress, askDraftsRev, forgetAskDraft } from './askdialog.js';
import { record, recPhase } from './dictate.js';
import { typeHere } from './keyboard.js';

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

// How long the dock says that something asked for did not happen, unless it
// is closed first. Data cadence, not motion.
const NOTE_MS = 10000;

/**
 * mountChat makes the chat view in `ctx.host` (#chat).
 *
 * `ctx`:
 *   status()           the stream's state moved (for the page's stale marks)
 *   openAsk(id, askId) open the question dialog at one ask of this chat
 *   toastRoom(px)      the room the snackbars need at the bottom of the
 *                      app while the chat shows (0 when it does not)
 */
export function mountChat(ctx) {
  const host = ctx.host;
  const chats = new Map();
  let session = null;
  let chat = null;
  let stream = null;
  let composer = null;
  let frame = 0;
  // Whether the conversation follows its end as it grows. Only the person
  // unsticks it, by moving up (scroll, wheel, keys); moving back down to
  // within STICK of the end sticks it again.
  let stuck = true;
  // A finger is on the conversation: nothing scrolls it to its end under the
  // finger, whatever the stream draws meanwhile.
  let held = false;
  // Where the conversation was scrolled to at the last scroll event, or by
  // this view itself.
  let lastTop = 0;
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
  // The chat a "Continue with" is being sent for, until the server answers.
  let continuing = '';
  // The asks whose answer has been seen, whose draft has been let go.
  const settledAsks = new Set();
  // What the person asked for that did not happen - a model refused, a Stop
  // that did not reach the agent: said in the dock, beside the composer,
  // for a while (NOTE_MS), never over Send. { text, timer } or null.
  let note = null;
  // The room the snackbars need at the bottom of the app while this view
  // shows, in px (ctx.toastRoom).
  let toastRoom = 0;

  // "Latest" is the scroller's last child and sticks to its bottom edge, so
  // it is always just above the dock, however tall the dock is.
  morph(host, html`<div class="so-chat__scroll" data-ds-key="scroll"><div class="so-chat__log" role="log" aria-label="Conversation" aria-live="polite" aria-relevant="additions"></div><button class="ds-btn ds-btn--tonal ds-btn--icon so-chat__jump" type="button" data-action="jump" aria-label="Jump to the latest message" data-tip="Latest" hidden>${raw(icon('arrowDown', 20))}</button></div>
<div class="so-chat__dock" data-ds-key="dock"><div class="so-chat__state" role="status" aria-live="polite"></div><div class="ds-alert so-notice" role="status" hidden></div><div class="ds-alert so-ticker" hidden>${raw(icon('activity', 20))}<span class="ds-alert__text so-ticker__line" role="status" aria-live="polite"></span></div><div class="so-chat__todo" hidden></div><div class="so-chat__composer"></div></div>`);
  const scroller = host.querySelector('.so-chat__scroll');
  const log = host.querySelector('.so-chat__log');
  const jump = host.querySelector('.so-chat__jump');
  const stateLine = host.querySelector('.so-chat__state');
  // The spoken Status's one line (assist.js) and the notice with its whole
  // answer (session.js), in the dock where a terminal has them over its
  // pane.
  const noticeBox = host.querySelector('.so-notice');
  const tickerBox = host.querySelector('.so-ticker');
  const tickerLine = host.querySelector('.so-ticker__line');
  const todoSlot = host.querySelector('.so-chat__todo');
  const composerSlot = host.querySelector('.so-chat__composer');
  const dock = host.querySelector('.so-chat__dock');

  // The snackbars stand above the dock while a chat is shown, and above
  // "Latest" while it is: the room they need at the bottom of the app is
  // the dock's height and, with "Latest" shown, the button's height and its
  // edge with a little air (the page places them, session.js
  // placeToasts, keyboard included). Heights only, never where the dock
  // stands: a keyboard that comes or goes moves it before or after this
  // is asked, as the listeners happen to run.
  function liftToasts() {
    let room = 0;
    if (session && !host.hidden) {
      room = dock.offsetHeight;
      if (!jump.hidden) {
        const edge = scroller.getBoundingClientRect().bottom - jump.getBoundingClientRect().top;
        if (edge > 0) room += edge + parseFloat(getComputedStyle(jump).marginTop || '0');
      }
    }
    room = Math.round(room);
    if (room === toastRoom) return;
    toastRoom = room;
    if (ctx.toastRoom) ctx.toastRoom(room);
  }
  new ResizeObserver(() => liftToasts()).observe(dock);

  // "Latest" comes and goes as the person scrolls; the snackbars move with it.
  function showJump(shown) {
    if (jump.hidden === !shown) return;
    jump.hidden = !shown;
    liftToasts();
  }

  /** sayError puts what did not happen into the dock for a while. */
  function sayError(text) {
    if (note) clearTimeout(note.timer);
    note = { text: String(text || ''), timer: setTimeout(() => { note = null; schedule(); }, NOTE_MS) };
    schedule();
  }

  function dropNote() {
    if (note) clearTimeout(note.timer);
    note = null;
  }

  // The task list pinned over the composer is for a list that is out of
  // sight: while the one in the conversation can be read, it is not said a
  // second time.
  let todoInView = false;
  let todoWatched = null;
  const todoWatch = new IntersectionObserver((entries) => {
    const entry = entries.at(-1);
    const seen = !!entry && entry.isIntersecting && entry.intersectionRatio >= 0.5;
    if (seen === todoInView) return;
    todoInView = seen;
    schedule();
  }, { root: scroller, threshold: [0, 0.5, 1] });

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
    // What the chat was started on, for the first model line (lineHtml).
    started: () => (session ? { model: session.model || '', effort: session.effort || '' } : null),
    // where is a path as the chat's folder sees it: relative to it when it
    // is inside it - an agent names files by their whole path, which on a
    // phone is mostly the folder's own name over and over - and whole
    // otherwise.
    where(path) {
      const p = String(path || '');
      const dir = session && session.workdir ? String(session.workdir).replace(/\/+$/, '') : '';
      return dir && p.startsWith(dir + '/') ? p.slice(dir.length + 1) : p;
    },
    renewal() {
      let latest = 0;
      for (const limit of Object.values(chat.rateLimits)) if (limit.used >= 1 && limit.resetsAt > latest) latest = limit.resetsAt;
      return latest ? renewsIn(latest) : '';
    },
    // An id the list does not have (an account deleted since) is said as
    // the journal has it.
    accountLabel: (id) => (id ? accounts.labelOf(id) || id : ''),
    // accountWho is who an account is signed in as, as the agent last said:
    // the e-mail and the plan, for the switch's "i".
    accountWho(id) {
      const a = id ? accounts.byId(id) : null;
      if (!a) return [];
      return [a.email ? 'Signed in as ' + a.email : '', a.plan ? 'Plan: ' + a.plan : ''];
    },
    // continueWith is the accounts a limit card offers to go on with: only
    // under the newest limit nothing has happened after, while no turn
    // runs; the agent's signed-in accounts not at a limit themselves, the
    // one with the most left first.
    continueWith(entry) {
      // One tap is the choice: while it is on its way - here, or a switch
      // from anywhere the journal already says - the buttons are gone, so a
      // second tap or a redraw cannot send it again.
      if (!session || chat.turn || !lastLimit(entry) || chat.pendingSwitch || continuing === session.id) return [];
      const best = accounts.recommended(session.harness);
      return accounts.forAgent(session.harness)
        .filter((a) => a.id !== chat.account && accounts.usable(a) && !accounts.renewal(a))
        .sort((a, b) => (b.id === best) - (a.id === best))
        .slice(0, 3);
    },
  };

  // lastLimit says whether a limit card is the newest word of the chat:
  // no message, account change or other turn came after it.
  function lastLimit(entry) {
    for (let i = chat.timeline.length - 1; i >= 0; i -= 1) {
      const one = chat.timeline[i];
      if (one.key === entry.key) return true;
      if (one.type !== 'line' || one.line.kind === 'account' || one.line.kind === 'turn') return false;
    }
    return false;
  }

  // The accounts are read once a chat is shown and whenever they change, and
  // what names them (the switch lines, the limit card) is drawn again then.
  let accountsRev = 0;
  accounts.onChange(() => {
    accountsRev += 1;
    if (session) schedule();
  });

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
      // A switch line says the accounts' names, which may come later.
      if (entry.line && entry.line.kind === 'account') rev += ':' + accountsRev;
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
        })}${failed ? html`<div class="so-msg__actions" data-ds-key="${'oa:' + item.payload.client_id}"><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="outbox-edit" data-id="${item.id}" aria-label="Edit the message" data-tip="Edit">${raw(icon('pencil', 18))}</button><button class="ds-btn ds-btn--tonal ds-btn--icon ds-btn--sm" type="button" data-action="outbox-retry" data-id="${item.id}" aria-label="Send again" data-tip="Send again">${raw(icon('sync', 18))}</button></div>` : ''}`),
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
    // Not "stuck or near the end": a finger that moves the conversation up
    // a few pixels between two updates of a streaming answer is still near
    // the end, and was put back at it at every update (measured: a drag of
    // 8 or 25 px a move never got away from the end).
    const follow = stuck && !held;
    // A line with nothing to say (a model "change" to the same model) is
    // no entry at all.
    const entries = chat.timeline.map((entry) => ({ key: entry.key, markup: entryMarkup(entry) })).filter((one) => one.markup);
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
      lastTop = scroller.scrollTop;
    }
    showJump(!atEnd());
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
    // The list as the conversation has it, watched for whether it can be
    // read where it is.
    const inline = todo ? log.querySelector('[data-ds-key="' + CSS.escape('p:' + todo.id) + '"] .so-todo') : null;
    if (inline !== todoWatched) {
      if (todoWatched) todoWatch.unobserve(todoWatched);
      todoWatched = inline;
      todoInView = false;
      if (inline) todoWatch.observe(inline);
    }
    if (todo && inline && todoInView) todo = null;
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

  // diedInTurn is an agent that exited during a turn: the conversation's
  // last line says so already ("The agent stopped during the turn", with
  // the exit behind its "i"), and the dock does not say it again - least of
  // all as "could not start".
  function diedInTurn() {
    const last = chat.timeline[chat.timeline.length - 1];
    return !!last && last.type === 'line' && last.line.kind === 'turn' && last.line.outcome === 'failed'
      && /^The agent exited during the turn: /.test(String(last.line.text || ''));
  }

  // drawState is the dock's line: the connection when it is not live, else
  // the agent when it is not well.
  function drawState() {
    const old = stale();
    host.toggleAttribute('data-stale', old);
    // While this line says the connection, the page's bar does not say it
    // again (net.js).
    connectionSaidElsewhere(old);
    const said = note ? html`<div class="ds-alert ds-alert--error" data-ds-key="note" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${note.text}</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="note-close" aria-label="Dismiss" data-tip="Dismiss">${raw(icon('close', 18))}</button></div>` : '';
    let line = '';
    if (old) {
      line = navigator.onLine === false
        ? html`<div class="ds-alert ds-alert--warning" data-ds-key="offline">${raw(icon('wifiOff', 20))}<span class="ds-alert__text">Offline. The chat may be behind; what you send goes out when the connection is back.</span></div>`
        : html`<div class="ds-alert ds-alert--warning" data-ds-key="reconnecting"><span class="so-chat__spin" aria-hidden="true"><span class="ds-spinner"></span></span><span class="ds-alert__text">Reconnecting… The chat may be behind.</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="reconnect" aria-label="Try to connect now" data-tip="Retry">${raw(icon('sync', 18))}</button></div>`;
    } else if (session.workdir_missing && !chat.turn) {
      // A restore brings the chats back, not the code they worked on
      // (REBUILD §6.2): the conversation stays readable, and the agent can
      // go on once it is told where the folder is on this machine.
      line = html`<div class="ds-alert ds-alert--warning" data-ds-key="workdir">${raw(icon('alert', 20))}<span class="ds-alert__text">This chat's folder is not on this machine.</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" data-action="info" aria-label="The missing folder" data-tip="${session.workdir}">${raw(icon('info', 18))}</button><button class="ds-btn ds-btn--sm" type="button" data-action="point-folder">Point to folder</button></div>`;
    } else if (chat.process === 'failed' && !chat.turn && !diedInTurn()) {
      line = html`<div class="ds-alert ds-alert--error" data-ds-key="failed">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">The agent could not start. Send a message to try again.</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" data-action="info" aria-label="Why it did not start" data-tip="${chat.processDetail || 'No detail was given.'}">${raw(icon('info', 18))}</button></div>`;
    } else if (chat.process === 'starting') {
      line = html`<div class="ds-alert" data-ds-key="starting"><span class="so-chat__spin" aria-hidden="true"><span class="ds-spinner"></span></span><span class="ds-alert__text">Starting the agent…</span></div>`;
    }
    morph(stateLine, html`${said}${line}`);
  }

  // usageFacts is the chat's usage for its "i", as named facts ([label,
  // value], the rows Info has): the context it holds, what it has cost,
  // every usage window with when it renews, how long the last turn took
  // (when that is worth a number), the program and the conversation.
  function usageFacts() {
    if (!chat) return [];
    const facts = [];
    const u = chat.usage || {};
    if (u.context && u.window) {
      facts.push(['Context', fmtTokens(u.context) + ' of ' + fmtTokens(u.window)
        + ' (' + Math.round((u.context / u.window) * 100) + '%)']);
    }
    if (chat.cost) facts.push(['Cost so far', '$' + chat.cost.toFixed(4)]);
    for (const [name, limit] of Object.entries(chat.rateLimits)) {
      const renew = renewsIn(limit.resetsAt);
      facts.push([WINDOW_WORDS[name] || name, Math.round(limit.used * 100) + '% used'
        + (renew ? ' · renews in ' + renew : '')]);
    }
    const took = chat.lastTurn && chat.lastTurn.endedAt && chat.lastTurn.startedAt
      ? seconds(chat.lastTurn.endedAt - chat.lastTurn.startedAt) : '';
    if (took) facts.push(['Last turn', took]);
    if (chat.info && chat.info.version) facts.push(['Program', harnesses.label(session.harness) + ' ' + chat.info.version]);
    if (chat.conversation) facts.push(['Conversation', chat.conversation]);
    return facts;
  }

  /* ------------------------------------------------------------ the stream */

  function onFrame(f) {
    switch (f.t) {
      case 'ev':
        if (apply(chat, f)) schedule();
        if (f.type === 'ask' || f.type === 'ask.answered' || f.type === 'ask.delivered' || f.type === 'ask.closed') tellAsk(f.ask_id);
        // A switch, or a limit, changes what the accounts' list says: the
        // account now in use (perhaps one made since the list was read),
        // and which ones have room left. A replay of old events does not.
        if (synced && (f.type === 'account.switched' || (f.type === 'turn.ended' && f.outcome === 'limited'))) {
          accounts.load().catch(() => {});
        }
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
        copy(code.textContent, 'The code could not be copied.');
        break;
      }
      case 'copy-answer': {
        const message = chat.messages.get(target.dataset.message);
        if (!message) return;
        copy(answerText(message), 'The answer could not be copied.');
        break;
      }
      case 'note-close':
        dropNote();
        drawState();
        break;
      case 'reconnect':
        retryConnection();
        break;
      case 'info': {
        event.preventDefault();
        // Named facts are rows, as Info's are; any other line a line.
        let said = null;
        try { said = target.dataset.facts ? JSON.parse(target.dataset.facts) : null; } catch { said = null; }
        const lines = said ? said.filter((one) => !Array.isArray(one)) : String(target.dataset.tip || '').split('\n');
        const facts = said ? said.filter((one) => Array.isArray(one)) : [];
        alertDialog({ title: target.getAttribute('aria-label') || 'Details', lines, facts, confirmLabel: 'Close', opener: target });
        break;
      }
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
        // Keep planning was asked for by a tap: its note takes the keyboard.
        typeHere(host.querySelector('[data-plan-note="' + CSS.escape(target.dataset.ask) + '"]'));
        break;
      case 'plan-dictate': {
        // The note's own microphone, as every other field of the chat has:
        // the words go after what is written.
        if (recPhase() !== 'idle') break;
        M.busy(target, () => record({ opener: target }), { done: false }).then((words) => {
          const field = host.querySelector('#' + CSS.escape(target.dataset.target));
          if (!words || !field) return;
          field.value = field.value ? field.value.replace(/\s*$/, ' ') + words : words;
          field.dispatchEvent(new Event('input', { bubbles: true }));
        });
        break;
      }
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
      case 'point-folder':
        pointFolder(target);
        break;
      case 'continue-with':
        continueWith(target);
        break;
      case 'jump':
        stuck = true;
        scroller.scrollTo({ top: scroller.scrollHeight, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
        break;
      default:
        break;
    }
  });

  // A conversation read at its end stays at its end when it gets shorter:
  // a phone's keyboard coming up, the dock growing, a turn of the screen;
  // and when its end moves down: the room kept for a message that stays
  // (session.js placeToasts). What the browser scrolls by itself while the
  // height changes is not the person scrolling away from the end.
  //
  // Which way it moved is what counts: up, by however little, is the person
  // reading back, and lets go of the end; down to within STICK of the end
  // takes it again. A conversation that got shorter under an end it was
  // read at moves up as well, onto the new end: that is not the person.
  let scrollerHeight = 0;
  scroller.addEventListener('scroll', () => {
    const top = scroller.scrollTop;
    const was = lastTop;
    lastTop = top;
    if (scroller.clientHeight !== scrollerHeight) return;
    const atBottom = scroller.scrollHeight - top - scroller.clientHeight <= 1;
    if (top < was - 1 && !atBottom) stuck = false;
    else if (top > was) stuck = stuck || atEnd();
    showJump(!atEnd());
  }, { passive: true });
  scroller.addEventListener('wheel', (event) => {
    if (event.deltaY < 0) stuck = false;
  }, { passive: true });
  scroller.addEventListener('keydown', (event) => {
    if (event.target.closest('input, textarea, select, [contenteditable]')) return;
    if (['ArrowUp', 'PageUp', 'Home'].includes(event.key)) stuck = false;
  });
  scroller.addEventListener('touchstart', () => { held = true; }, { passive: true });
  // The finger is lifted: a conversation it left at its end follows it from
  // here. scrollend is there too because a touch whose element the stream
  // replaced ends on that element, out of the document, and never bubbles.
  const letGo = () => {
    if (!held) return;
    held = false;
    if (stuck) schedule();
  };
  scroller.addEventListener('touchend', (event) => { if (!event.touches.length) letGo(); }, { passive: true });
  scroller.addEventListener('touchcancel', letGo, { passive: true });
  scroller.addEventListener('scrollend', letGo, { passive: true });

  const keepEnd = new ResizeObserver(() => {
    scrollerHeight = scroller.clientHeight;
    if (!session || !stuck || held) return;
    scroller.scrollTop = scroller.scrollHeight;
    lastTop = scroller.scrollTop;
    showJump(false);
  });
  keepEnd.observe(scroller);
  keepEnd.observe(log, { box: 'border-box' });

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

  // copy puts text on the clipboard and says so above the dock.
  function copy(text, failed) {
    navigator.clipboard.writeText(text).then(
      () => toast('Copied.'),
      () => sayError(failed),
    );
  }

  // answerText is an answer as the agent wrote it: its text parts, in
  // Markdown, without the tool calls and the thinking around them.
  function answerText(message) {
    return message.parts.filter((part) => part.kind === 'text' && part.text)
      .map((part) => part.text.trim()).join('\n\n');
  }

  // continueWith moves the chat to the account a limit card's button names
  // and has the stopped turn carried on there.
  async function continueWith(button) {
    const current = session;
    if (continuing === current.id) return;
    continuing = current.id;
    button.disabled = true;
    schedule();
    try {
      await accounts.switchChat(current.id, button.dataset.account, { goOn: true });
    } catch (err) {
      sayError('The chat could not go on with that account: ' + errorMessage(err));
    } finally {
      // From here the journal's command says the switch is on its way.
      continuing = '';
      schedule();
    }
  }

  // pointFolder asks where the chat's code is on this machine and points the
  // chat there; the agent works in it from the next message on.
  async function pointFolder(opener) {
    const current = session;
    const dir = await promptDialog({
      title: 'Point to folder',
      body: 'Where is this chat\'s code on this machine? The agent works in that folder from the next message on.',
      label: 'Folder',
      value: current.workdir,
      confirmLabel: 'Use this folder',
      opener,
    });
    if (!dir) return;
    try {
      const data = await api('/api/sessions/' + encodeURIComponent(current.id) + '/workdir', { method: 'PUT', body: { workdir: dir } });
      if (session && session.id === current.id && data.session) {
        session = data.session;
        schedule();
      }
      toast('The chat works in that folder now.', 'success');
    } catch (err) {
      sayError('The folder was not taken: ' + errorMessage(err));
    }
  }

  /* --------------------------------------------------------------- api */

  function stop() {
    if (stream) { stream.stop(); stream = null; }
    if (composer) { composer.destroy(); composer = null; }
    if (frame) { cancelAnimationFrame(frame); frame = 0; }
    clearTimeout(staleTimer);
    dropNote();
    if (todoWatched) { todoWatch.unobserve(todoWatched); todoWatched = null; }
    todoInView = false;
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
        error: (text) => sayError(text),
      });
      stream = new ChatStream({ chatId: next.id, since: () => chat.lastSeq, onFrame, onStatus });
      stream.start();
      // Their usage moves, so the accounts are read again for every chat
      // opened; the copy this browser kept is drawn meanwhile.
      accounts.load().catch(() => {});
      render();
      liftToasts();
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
      connectionSaidElsewhere(false);
      liftToasts();
    },

    shownId: () => (session ? session.id : ''),
    live: () => !!stream && stream.live(),
    stale,
    insert(text) { if (composer) composer.insert(text); },
    focus() { if (composer) composer.focus(); },
    field: () => (composer ? composer.field() : null),

    /**
     * modelOf is the model and effort a chat's next message goes to, as its
     * composer says: a change still on its way, else what its journal said
     * last. Null for a chat this page has not read (its row knows only the
     * model it started with).
     */
    modelOf(id) {
      const asked = pendingModel(id);
      if (asked) return { model: asked.model, effort: asked.effort || '' };
      const fold = session && session.id === id ? chat : chats.get(id);
      return fold && fold.model ? { model: fold.model, effort: fold.effort || '' } : null;
    },

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

    /** noticeBox is where a notice about the chat is drawn. */
    noticeBox: () => noticeBox,
  };
}
