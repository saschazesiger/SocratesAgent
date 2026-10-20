// Home: what is going on, at a glance and always live (HOME.md §2.4).
//
// The sidebar already says everything Home says - a ring that turns, an amber
// ring, a bold name, a mark after it - but it says it one row at a time, in a
// column that is a drawer on a phone and a rail on a desk. Home says it as
// numbers: how many sessions need an answer, how many are working, how many
// finished something nobody has read. And under the numbers, the sessions
// behind them, each with the one line a model wrote about what it is doing.
//
// Every number is worked out here from the same list and the same activity
// the sidebar is drawn from, with the sidebar's own tests - whether a session
// is a tool, whether it is live, what it was filed as - handed in by the page
// rather than written again. Two drawings of one state that are each
// computed their own way are two drawings that will one day disagree, and a
// count that says three where the list shows two is a page nobody trusts.
//
// The module draws and nothing else: what a tap does - open a session, open
// the questions, start a new one - is the page's.

import { el, setClass } from './api.js';
import { agentMark } from './logos.js';
import { isTool, label as harnessLabel } from './harnesses.js';
import { foldersOf, folderKeyOf, isPrepared, momentOf } from './folders.js';
import { whenShort } from './prestart.js';
import {
  OUTCOME_WORDS, OUTCOME_SVG, HOURGLASS_SVG, CHECKING_WORDS, outcomeMark, outcomeLine,
} from './outcome.js';

// How often the times on the page are worked out again: "2 min ago" turns
// into "3 min ago" from the data the page already has, never from a guess at
// what happened in between.
const REDRAW_EVERY = 15000;

// How long a page that has heard nothing may still call itself current. Past
// this, the line under the title stops saying "Reconnecting…" and says how old
// the numbers are.
const STALE_AFTER = 30000;

// How often the connection line is said again while it is not live: the age
// it gives is counted in seconds, and a count that only moved every fifteen
// would be the stale number this line is there to warn about.
const CONN_EVERY = 2000;

const LIVE_STATES = new Set(['running', 'starting', 'resuming']);
const STOPPED_STATES = new Set(['needs_resume', 'exited', 'failed']);

// The cards, in the order they are drawn. Questions is not in the grid: it is
// the first and the largest card, and the only one a tap answers rather than
// filters with.
const CARDS = [
  { key: 'running', label: 'Running', none: 'Nothing is working' },
  { key: 'tasks', label: 'Background tasks', none: 'No background work' },
  { key: 'finished', label: 'Finished', none: 'Nothing unread' },
  { key: 'ready', label: 'Ready', none: 'Nothing is idle' },
  { key: 'review', label: 'Review pending', none: 'Nothing to review' },
  { key: 'pending', label: 'Pending', none: 'Nothing pending' },
  { key: 'prepared', label: 'Prepared', none: 'Nothing prepared' },
  { key: 'stopped', label: 'Not running', none: 'Everything is running' },
  { key: 'done', label: 'Done', none: 'Nothing done yet' },
];
const CARD_LABELS = { questions: 'Questions', ...Object.fromEntries(CARDS.map((c) => [c.key, c.label])) };

// The list under the cards. "Needs you" is what the page opens on: the
// sessions that are waiting for the person, in one of the three ways a
// session can be.
const NEEDS_YOU = ['questions', 'finished', 'review'];

// The verdicts on a turn that are the person's to act on, read or not: a
// session waiting on them, and one that is stuck. They are in "Needs you"
// too, wherever else they are counted.
const NEEDS_YOU_OUTCOMES = new Set(['needs_input', 'blocked']);

/**
 * waitsOnYou says whether the verdict on a session's last turn is one the
 * person has to act on (NEEDS_YOU_OUTCOMES).
 */
function waitsOnYou(ctx, session) {
  const outcome = ctx.outcomeOf ? ctx.outcomeOf(session) : null;
  return !!outcome && outcome.phase === 'ready' && NEEDS_YOU_OUTCOMES.has(outcome.kind);
}

/**
 * textQuestion says whether a session that is idle ended its last answer by
 * asking something in prose. That is a question too, as far as the person is
 * concerned - it is only not a menu - and the digest is the one thing that
 * can tell.
 */
function textQuestion(digest) {
  return !!(digest && digest.question && digest.question.source === 'text');
}

/**
 * unconfirmed says whether a session counted as asking only by a question in
 * prose that a restarted server has not read yet (session.js `carriedOver`):
 * it may have been answered at the machine while there was no server. It
 * stays counted - a question still open must not leave the card for the
 * moment of the read - but is said to be checked, with no Answer, until the
 * read says whether it still asks.
 */
function unconfirmed(act, digest) {
  return !!act && act.state !== 'waiting' && textQuestion(digest) && !!digest.carried;
}

/**
 * cardsOf is every card one session counts in. A session is in several at
 * once - working and waiting for a review - except a done one, which is in
 * Done and nowhere else: what is finished is not also running or ready.
 */
export function cardsOf(ctx, session) {
  const status = ctx.statusOf(session);
  if (status === 'done') return ['done'];
  // A tool has no turn to be in, so it has no activity to be counted by and
  // no work to be filed: it is in the list, and in "Not running" when it is
  // not.
  if (isTool(session.harness)) return STOPPED_STATES.has(session.state) ? ['stopped'] : [];
  const cards = [];
  if (status === 'review') cards.push('review');
  if (status === 'pending') cards.push('pending');
  if (isPrepared(session)) {
    cards.push('prepared');
    return cards;
  }
  const act = ctx.activityOf(session);
  // Work nobody has read is Finished whatever the session is doing now, as
  // the sidebar's bold name says it is: a pane that died mid-turn, a reboot
  // that left a finished turn behind, a session the detector cannot read.
  // A service filed in the background finishes turns so as not to interrupt
  // anyone, so its turns are not news (no chime, no notification) and it
  // is Ready rather than Finished.
  const background = status === 'background';
  const unread = !!act && !!act.unread && !background;
  if (STOPPED_STATES.has(session.state)) {
    cards.push('stopped');
    if (unread) cards.push('finished');
    return cards;
  }
  if (!LIVE_STATES.has(session.state)) return cards;
  if (!act) return cards;
  const asksInProse = textQuestion(ctx.digestOf(session.id));
  if (act.state === 'waiting' || (act.state === 'idle' && asksInProse)) cards.push('questions');
  else if (act.state === 'busy') cards.push('running');
  else if (act.state === 'tasks') cards.push('tasks');
  // A turn whose verdict is still being read is over, with something about
  // it still going on, as background work is - and it is counted there until
  // the verdict is in.
  else if (act.state === 'idle' && ctx.checking && ctx.checking(session)) cards.push('tasks');
  else if (act.state === 'idle') cards.push(unread ? 'finished' : 'ready');
  else if (unread) cards.push('finished');
  return cards;
}

// When a session started asking: the commit of the activity it is in, which
// is what "oldest question first" is ordered by.
function askedAt(ctx, session) {
  const act = ctx.activityOf(session);
  return Number(act && act.since) || momentOf(session);
}

/**
 * questionQueue is every session that is asking something, oldest question
 * first. The question dialog walks it, and the Questions card counts it, so
 * the two are the same list by construction.
 */
export function questionQueue(ctx) {
  return ctx.sessions()
    .filter((session) => cardsOf(ctx, session).includes('questions'))
    .sort((a, b) => askedAt(ctx, a) - askedAt(ctx, b));
}

/**
 * agoWords is how long ago a moment was, the way a person says it. A moment
 * in the future - a clock that is a little ahead of the server's - is now.
 */
export function agoWords(at, now = Date.now()) {
  const ms = now - Number(at);
  if (!Number(at) || Number.isNaN(ms)) return '';
  if (ms < 45000) return 'just now';
  const minutes = Math.round(ms / 60000);
  if (minutes < 60) return minutes + ' min ago';
  const hours = Math.round(minutes / 60);
  if (hours < 24) return hours + ' h ago';
  const days = Math.round(hours / 24);
  return days === 1 ? 'yesterday' : days + ' d ago';
}

/**
 * staleWords is how old the numbers on a page that has stopped hearing from
 * the server are. Unlike agoWords it never says "just now": it is only used
 * once the numbers are past STALE_AFTER, so its least is half a minute, and
 * it says that in seconds.
 */
export function staleWords(at, now = Date.now()) {
  const ms = now - Number(at);
  if (!Number(at) || Number.isNaN(ms)) return '';
  if (ms < 90000) return Math.max(1, Math.round(ms / 1000)) + ' s ago';
  return agoWords(at, now);
}

/**
 * connectionLine is how live what the page shows is, in words, by one rule
 * for Home and for the question dialog over it (HOME.md §2.2). `conn` is the
 * event stream's {status, updatedAt, blocked, pollEvery, resuming, quiet};
 * `reachable` whether the last attempt to read the list got through. The
 * kind is 'live' or 'polling' (current: nothing is dimmed), or 'connecting'
 * or 'offline' (old: dimmed).
 */
export function connectionLine(conn, reachable, now = Date.now()) {
  const { status, updatedAt, blocked, pollEvery, resuming, quiet } = conn;
  if (status === 'live' && quiet) {
    // Live by its own account, and its ping not answered: what is shown may
    // be old, and in a few seconds it is either confirmed or opened again.
    return { kind: 'connecting', words: 'Checking…' };
  }
  if (status === 'live') return { kind: 'live', words: 'Live' };
  if (!updatedAt && status !== 'offline' && reachable) {
    // The first look, before anything has been heard at all - and before
    // anything has failed: a page whose every read has failed is offline,
    // not still connecting, whatever it has never heard.
    return { kind: 'connecting', words: 'Connecting…' };
  }
  if (resuming) {
    // Shown again after its stream was closed for being hidden, and on its
    // way back: old numbers, dimmed, but nothing has failed - not offline.
    return { kind: 'connecting', words: 'Reconnecting…' };
  }
  const old = now - updatedAt > STALE_AFTER;
  if (status === 'offline' || old || !reachable) {
    const ago = updatedAt ? staleWords(updatedAt, now) : '';
    return { kind: 'offline', words: 'Offline' + (ago ? ' · updated ' + ago : '') };
  }
  if (blocked) {
    // The stream is refused every time - a proxy that does not pass
    // WebSockets - and the poll keeps the numbers a few seconds old. That
    // is current, said as what it is, and not dimmed as if it were not.
    return { kind: 'polling', words: 'Updated every ' + Math.round((pollEvery || 5000) / 1000) + ' s' };
  }
  return { kind: 'connecting', words: 'Reconnecting…' };
}

const HOUSE_SVG ='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
  + '<path d="M3.5 10.5L12 3.5l8.5 7"/><path d="M5.5 9v10.5a1 1 0 0 0 1 1h3.5v-6h4v6h3.5a1 1 0 0 0 1-1V9"/></svg>';
const PLUS_SVG = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>';

/**
 * mountHome wires the page to its host. ctx:
 *
 *   host               the section Home is drawn in (#home)
 *   sessions()         the list, as the sidebar has it
 *   activityOf(s)      what a session is doing, or null (a tool has none)
 *   statusOf(s)        what it was filed as
 *   digestOf(id)       its digest, or null
 *   words              {state, activity}: the sidebar's own words for both
 *   connection()       {status, updatedAt, blocked, pollEvery, resuming,
 *                      quiet} of the event stream (connectionLine)
 *   serverNow()        the server's clock now, which the ages are measured on
 *   reachable()        whether the last attempt to read the list got through
 *   loading()          whether the first list is still being read
 *   select(id)         open a session
 *   newSession()       start one
 *   openQuestions(id)  open the question dialog, at that session or the oldest
 *
 * It hands back show, hide, shown and render.
 */
export function mountHome(ctx) {
  const host = ctx.host;
  let built = null;
  let filter = 'needs';
  let timer = null;
  let connTimer = null;

  function build() {
    const conn = el('span', { class: 'home-conn', role: 'status' },
      el('span', { class: 'home-conn-dot', 'aria-hidden': 'true' }),
      el('span', { class: 'home-conn-text' }));
    const newBtn = el('button', {
      class: 'btn home-new', type: 'button', html: PLUS_SVG, onclick: () => ctx.newSession(),
    });
    newBtn.append(el('span', { text: 'New session' }));
    const questions = el('button', {
      class: 'home-card home-questions', type: 'button', 'data-card': 'questions',
      onclick: () => {
        if (questionQueue(ctx).length) ctx.openQuestions('');
        else choose('questions');
      },
    },
    el('span', { class: 'home-q-main' },
      el('span', { class: 'home-count' }),
      el('span', { class: 'home-q-words' },
        el('span', { class: 'home-card-label', text: 'Questions' }),
        el('span', { class: 'home-card-sub' }))),
    el('span', { class: 'home-q-answer', text: 'Answer' }));
    const cards = new Map();
    const grid = el('div', { class: 'home-grid' });
    for (const card of CARDS) {
      const node = el('button', {
        class: 'home-card', type: 'button', 'data-card': card.key, 'aria-pressed': 'false',
        onclick: () => choose(filter === card.key ? 'all' : card.key),
      },
      el('span', { class: 'home-count' }),
      el('span', { class: 'home-card-label', text: card.label }),
      el('span', { class: 'home-card-sub' }));
      cards.set(card.key, node);
      grid.append(node);
    }
    const seg = (key, text) => el('button', {
      class: 'home-seg', type: 'button', 'data-filter': key, 'aria-pressed': 'false', text,
      onclick: () => choose(key),
    });
    const listTitle = el('h2', { class: 'home-list-title' });
    const segs = el('div', { class: 'home-segs', role: 'group', 'aria-label': 'Show' },
      seg('needs', 'Needs you'), seg('all', 'All'));
    const list = el('div', { class: 'home-list' });
    const empty = el('div', { class: 'home-empty', hidden: true });
    const inner = el('div', { class: 'home-inner' },
      el('div', { class: 'home-head' },
        el('div', { class: 'home-head-text' },
          el('h1', { class: 'home-title' },
            el('span', { class: 'home-title-mark', html: HOUSE_SVG }), el('span', { text: 'Home' })),
          conn),
        newBtn),
      el('div', { class: 'home-numbers' }, questions, grid),
      el('div', { class: 'home-list-head' }, listTitle, segs),
      list,
      empty);
    host.replaceChildren(el('div', { class: 'home-scroll' }, inner));
    return { conn, questions, cards, segs, listTitle, list, empty, rows: new Map() };
  }

  function choose(key) {
    filter = key;
    render();
  }

  function render() {
    if (!built || host.hidden) return;
    // The connection line is this device's own reckoning (when it last heard
    // anything); every other age on the page is a server timestamp, and is
    // measured on the server's clock - a phone a few minutes fast would
    // otherwise say "10 min ago" of a question asked a second ago.
    const local = Date.now();
    const now = ctx.serverNow ? ctx.serverNow() : local;
    const sessions = ctx.sessions();
    const byCard = new Map([['questions', []], ...CARDS.map((card) => [card.key, []])]);
    const cardsBy = new Map();
    for (const session of sessions) {
      const cards = cardsOf(ctx, session);
      cardsBy.set(session.id, cards);
      for (const key of cards) byCard.get(key).push(session);
    }
    const asking = questionQueue(ctx);
    drawConnection(local);

    // The Questions card: the one in colour, and the one that is answered
    // rather than filtered with.
    const q = built.questions;
    const waiting = asking.length;
    setClass(q, 'asking', waiting > 0);
    setClass(q, 'zero', waiting === 0);
    q.setAttribute('aria-pressed', filter === 'questions' ? 'true' : 'false');
    q.querySelector('.home-count').textContent = String(waiting);
    q.querySelector('.home-card-label').textContent = waiting === 1 ? 'Question' : 'Questions';
    q.querySelector('.home-card-sub').textContent = waiting
      ? (waiting === 1 ? asking[0].title : 'The oldest: ' + asking[0].title)
      : 'Nothing to answer';
    q.setAttribute('aria-label', waiting
      ? waiting + (waiting === 1 ? ' question' : ' questions') + '. Answer'
      : 'No questions');

    for (const card of CARDS) {
      const node = built.cards.get(card.key);
      const members = byCard.get(card.key);
      const count = members.length;
      setClass(node, 'zero', count === 0);
      node.setAttribute('aria-pressed', filter === card.key ? 'true' : 'false');
      node.querySelector('.home-count').textContent = String(count);
      node.querySelector('.home-card-sub').textContent = count ? cardSub(card.key, members, now) : card.none;
    }
    for (const button of built.segs.querySelectorAll('.home-seg')) {
      button.setAttribute('aria-pressed', button.dataset.filter === filter ? 'true' : 'false');
    }

    // The list of the filter. "Needs you" puts the questions first, oldest
    // first, because that is the order they are answered in; everything else
    // is the most recent first, the way the sidebar orders a folder.
    let shown;
    if (filter === 'needs') {
      const seen = new Set();
      shown = [];
      const judged = sessions.filter((session) => waitsOnYou(ctx, session));
      for (const key of [...NEEDS_YOU, 'outcomes']) {
        const members = key === 'questions' ? asking
          : [...(key === 'outcomes' ? judged : byCard.get(key))].sort((a, b) => momentOf(b) - momentOf(a));
        for (const session of members) {
          if (seen.has(session.id)) continue;
          seen.add(session.id);
          shown.push(session);
        }
      }
    } else if (filter === 'all') {
      shown = [...sessions].sort((a, b) => momentOf(b) - momentOf(a));
    } else if (filter === 'questions') {
      shown = asking;
    } else {
      shown = [...byCard.get(filter)].sort((a, b) => momentOf(b) - momentOf(a));
    }
    built.listTitle.textContent = filter === 'needs' ? 'Needs you' : filter === 'all' ? 'All sessions' : CARD_LABELS[filter];
    drawRows(shown, cardsBy, now);
    drawEmpty(sessions, shown);
  }

  // What a card says under its number: the one fact about its sessions that
  // is worth a second glance. Prepared says when the next one starts by
  // itself, because that is the thing about a prepared session that happens
  // without anybody pressing anything.
  function cardSub(key, members, now) {
    if (key === 'prepared') {
      const starts = members.map((s) => Number(s.start_at) || 0).filter((at) => at > 0).sort((a, b) => a - b);
      if (!starts.length) return 'Started by hand';
      const next = starts.find((at) => at >= now) || starts[0];
      return starts.length + ' scheduled · next ' + whenShort(next, now);
    }
    if (key === 'tasks') {
      const judging = members.filter((s) => ctx.checking && ctx.checking(s)).length;
      if (judging) return judging === members.length ? CHECKING_WORDS : judging + ' checking how the turn went';
    }
    if (members.length === 1) return members[0].title;
    const newest = [...members].sort((a, b) => momentOf(b) - momentOf(a))[0];
    return 'Latest: ' + newest.title;
  }

  // The line under the title says how live the numbers are, in words, and
  // the numbers are dimmed while they are not: a count that is a minute old
  // and drawn as if it were current is the one thing this page must not do.
  function drawConnection(now) {
    const { kind, words } = connectionLine(ctx.connection(), ctx.reachable(), now);
    built.conn.dataset.status = kind;
    const text = built.conn.querySelector('.home-conn-text');
    if (text.textContent !== words) text.textContent = words;
    setClass(host, 'not-live', kind !== 'live' && kind !== 'polling');
    // While it is not live the line counts the seconds, so "updated 40 s ago"
    // is never a quarter of a minute behind.
    if (kind === 'live') stopConnTimer();
    else if (!connTimer) connTimer = setInterval(() => { if (built && !host.hidden) drawConnection(Date.now()); }, CONN_EVERY);
  }

  function stopConnTimer() {
    if (connTimer) { clearInterval(connTimer); connTimer = null; }
  }

  /* ------------------------------------------------------------ the rows */

  // The rows are patched rather than rebuilt, for the sidebar's reason: a
  // node taken out and put back restarts its animation, and the ring of a
  // working session that starts its turn again every second is a page that
  // looks broken.
  function drawRows(shown, cardsBy, now) {
    const labels = folderLabels();
    const keep = new Set();
    let previous = null;
    for (const session of shown) {
      keep.add(session.id);
      let row = built.rows.get(session.id);
      if (!row) {
        row = buildRow(session.id);
        built.rows.set(session.id, row);
      }
      updateRow(row, session, cardsBy.get(session.id) || [], labels, now);
      const node = row.node;
      if (previous ? previous.nextElementSibling !== node : built.list.firstElementChild !== node) {
        if (previous) previous.after(node); else built.list.prepend(node);
      }
      previous = node;
    }
    for (const [id, row] of built.rows) {
      if (keep.has(id)) continue;
      row.node.remove();
      built.rows.delete(id);
    }
  }

  // The folder a row works in, by the name the sidebar gives its folder -
  // which is the directory's own name, widened only as far as it takes to
  // tell two of them apart.
  function folderLabels() {
    const labels = new Map();
    for (const folder of foldersOf(ctx.sessions())) labels.set(folder.key, folder.label);
    return labels;
  }

  // A row is two buttons side by side - its title, which opens the session,
  // and Answer - and never one button with another inside it, which a
  // screen reader flattens into one. A tap anywhere else on the row opens
  // the session too, for a finger; that is a convenience, not the control.
  function buildRow(id) {
    const mark = el('span', { class: 'home-mark' });
    const badge = el('span', { class: 'home-badge' });
    const title = el('button', {
      class: 'home-row-title', type: 'button',
      onclick: (event) => { event.stopPropagation(); ctx.select(id); },
    });
    const meta = el('span', { class: 'home-row-meta' });
    const summary = el('span', { class: 'home-row-summary' });
    const verdict = el('span', { class: 'home-row-outcome', hidden: true });
    const answer = el('button', {
      class: 'btn primary home-answer', type: 'button', text: 'Answer', hidden: true,
      onclick: (event) => { event.stopPropagation(); ctx.openQuestions(id); },
    });
    const node = el('div', { class: 'home-row', 'data-id': id },
      el('span', { class: 'home-mark-wrap' }, mark, badge),
      el('span', { class: 'home-row-text' }, title, meta, summary, verdict), answer);
    node.addEventListener('click', () => ctx.select(id));
    return { node, mark, badge, title, meta, summary, verdict, answer, harness: null, badgeKind: null, verdictKey: null };
  }

  function updateRow(row, session, cards, labels, now) {
    if (row.harness !== session.harness) {
      row.harness = session.harness;
      row.mark.replaceChildren(agentMark(session.harness, 26));
      row.mark.title = harnessLabel(session.harness) || '';
    }
    const asking = cards.includes('questions');
    const live = LIVE_STATES.has(session.state);
    const act = ctx.activityOf(session);
    setClass(row.mark, 'busy', live && !!act && act.state === 'busy');
    setClass(row.mark, 'waiting', asking);
    // The hourglass of background work, and of a verdict being read; the
    // verdict's own mark once it is in - the sidebar's corner, larger.
    const outcome = live && ctx.outcomeOf ? ctx.outcomeOf(session) : null;
    const judging = !!outcome && outcome.phase === 'pending';
    const tasks = live && !!act && act.state === 'tasks';
    const kind = tasks || judging ? 'tasks' : outcomeMark(outcome);
    if (row.badgeKind !== kind) {
      row.badgeKind = kind;
      row.badge.dataset.kind = kind;
      row.badge.innerHTML = kind === 'tasks' ? HOURGLASS_SVG : (OUTCOME_SVG[kind] || '');
    }
    row.badge.hidden = !kind;
    row.badge.title = judging ? CHECKING_WORDS : tasks ? ctx.words.activity.tasks : outcomeLine(outcome);
    setClass(row.node, 'asking', asking);
    setClass(row.node, 'unread', cards.includes('finished'));
    setClass(row.node, 'done', cards.includes('done'));
    if (row.title.textContent !== session.title) row.title.textContent = session.title;

    const folder = labels.get(folderKeyOf(session)) || '';
    const at = act && Number(act.since) && live ? Number(act.since) : momentOf(session);
    const meta = [folder, stateWords(session, cards, act, now), agoWords(at, now)].filter(Boolean).join(' · ');
    if (row.meta.textContent !== meta) row.meta.textContent = meta;

    const digest = ctx.digestOf(session.id);
    let line = '';
    if (digest && digest.summary) line = digest.summary;
    else if (digest && digest.phase === 'pending') line = '…';
    else if (isPrepared(session) && session.draft_preview) line = String(session.draft_preview);
    if (row.summary.textContent !== line) row.summary.textContent = line;
    row.summary.hidden = !line;
    setClass(row.summary, 'pending', !!digest && digest.phase === 'pending');
    drawVerdict(row, judging ? null : outcome);
    row.answer.hidden = !asking || unconfirmed(act, digest);
  }

  // drawVerdict is the line under a row's summary that says why its turn was
  // judged so, and what it left open: the reason, then each open task.
  function drawVerdict(row, outcome) {
    const kind = outcomeMark(outcome);
    const todos = kind === 'partial' && Array.isArray(outcome.todos) ? outcome.todos : [];
    const reason = !kind ? '' : kind === 'error' ? outcomeLine(outcome) : (outcome.reason || '');
    const key = kind + '|' + reason + '|' + todos.join('\n');
    if (row.verdictKey === key) return;
    row.verdictKey = key;
    row.verdict.hidden = !reason && !todos.length;
    row.verdict.dataset.kind = kind;
    row.verdict.replaceChildren(
      ...(reason ? [el('span', { class: 'home-row-reason', text: reason })] : []),
      ...(todos.length ? [el('ul', { class: 'home-row-todos' }, ...todos.map((todo) => el('li', { text: todo })))] : []));
  }

  // The state of a row in words: the card it is counted in, said the way the
  // sidebar says it, and what it was filed as when that is not the ordinary.
  function stateWords(session, cards, act, now) {
    const words = ctx.words;
    if (cards.includes('done')) return 'Done';
    let said = '';
    if (cards.includes('stopped')) {
      // Not running, and what it finished before it stopped is unread.
      said = words.state[session.state] || '';
      if (cards.includes('finished')) said = said ? said + ' · Finished' : 'Finished';
    } else if (cards.includes('questions')) {
      if (act && act.state === 'waiting') said = words.activity.waiting;
      else said = unconfirmed(act, ctx.digestOf(session.id)) ? 'Checking whether it still asks…' : 'Asks in its last answer';
    } else if (cards.includes('running')) said = words.activity.busy;
    else if (cards.includes('tasks')) {
      said = act && act.state === 'idle' ? (act.unread ? 'Finished · ' : '') + 'Checking how it went…'
        : 'A background task is running';
    } else if (cards.includes('finished')) said = 'Finished';
    else if (cards.includes('ready')) said = words.activity.idle;
    else if (isPrepared(session)) {
      const at = Number(session.start_at) || 0;
      said = at ? 'Starts ' + whenShort(at, now) : words.state.prestart;
    } else said = words.state[session.state] || '';
    // How the turn left the work, once that is known.
    const outcome = ctx.outcomeOf ? ctx.outcomeOf(session) : null;
    if (outcome && outcome.phase === 'ready' && OUTCOME_WORDS[outcome.kind]
      && (cards.includes('finished') || cards.includes('ready'))) {
      const todos = Array.isArray(outcome.todos) ? outcome.todos.length : 0;
      said = (cards.includes('finished') ? 'Finished · ' : '') + OUTCOME_WORDS[outcome.kind]
        + (outcome.kind === 'partial' && todos ? ': ' + todos + ' open' : '');
    }
    if (cards.includes('review')) said = said ? said + ' · Review pending' : 'Review pending';
    else if (cards.includes('pending')) said = said ? said + ' · Pending' : 'Pending';
    return said;
  }

  // What stands where the list would: the one thing to do on an empty
  // machine, or a plain sentence for a filter with nothing in it.
  function drawEmpty(sessions, shown) {
    const empty = built.empty;
    empty.replaceChildren();
    if (shown.length) { empty.hidden = true; return; }
    empty.hidden = false;
    if (!sessions.length) {
      if (ctx.loading()) {
        empty.append(el('p', { class: 'home-empty-body', text: 'Loading…' }));
        return;
      }
      if (!ctx.reachable()) {
        empty.append(
          el('h2', { class: 'empty-title', text: 'No connection' }),
          el('p', { class: 'empty-body', text: 'The sessions show up here again as soon as there is signal.' }));
        return;
      }
      empty.append(
        el('h2', { class: 'empty-title', text: 'No sessions yet' }),
        el('p', { class: 'empty-body', text: 'Start one and it shows up here, and in the list.' }),
        el('button', { class: 'btn primary', type: 'button', text: 'New session', onclick: () => ctx.newSession() }));
      return;
    }
    const text = filter === 'needs' ? 'Nothing needs you right now.' : 'Nothing here right now.';
    empty.append(el('p', { class: 'home-empty-body', text }));
  }

  return {
    show() {
      if (!built) built = build();
      host.hidden = false;
      if (!timer) timer = setInterval(render, REDRAW_EVERY);
      render();
    },
    hide() {
      host.hidden = true;
      if (timer) { clearInterval(timer); timer = null; }
      stopConnTimer();
    },
    shown: () => !host.hidden,
    render,
  };
}
