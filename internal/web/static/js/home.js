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

import { setClass } from './api.js';
import { M, html, raw, icon, morph, flipMorph } from './ui.js';
import { agentMarkHtml } from './logos.js';
import { isTool } from './harnesses.js';
import { foldersOf, folderKeyOf, isPrepared, momentOf } from './folders.js';
import { whenShort } from './prestart.js';
import {
  OUTCOME_WORDS, OUTCOME_ICON, CHECKING_WORDS, outcomeMark, outcomeLine, outcomeIconHtml,
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

// The tiles, in the order they are drawn, each with the DS glyph its label
// wears. Questions is not among them: it is the first and the largest card,
// and the only one a tap answers rather than filters with.
const CARDS = [
  { key: 'running', label: 'Running', glyph: 'activity', none: 'Nothing is working' },
  { key: 'tasks', label: 'Background tasks', glyph: 'background', none: 'No background work' },
  { key: 'finished', label: 'Finished', glyph: 'checkCircle', none: 'Nothing unread' },
  { key: 'ready', label: 'Ready', glyph: 'compose', none: 'Nothing is idle' },
  { key: 'review', label: 'Review pending', glyph: 'review', none: 'Nothing to review' },
  { key: 'pending', label: 'Pending', glyph: 'waiting', none: 'Nothing pending' },
  { key: 'prepared', label: 'Prepared', glyph: 'prestart', none: 'Nothing prepared' },
  { key: 'stopped', label: 'Not running', glyph: 'ban', none: 'Everything is running' },
  { key: 'done', label: 'Done', glyph: 'archive', none: 'Nothing done yet' },
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

// The status-chip colour of each verdict: green for work that is complete,
// amber for a session that waits on the person, red for one that is stuck;
// work left over is said in words and the list glyph, without a colour.
const OUTCOME_TONE = { complete: 'ok', needs_input: 'warning', blocked: 'error' };

/**
 * waitsOnYou says whether the verdict on a session's last turn is one the
 * person has to act on (NEEDS_YOU_OUTCOMES).
 */
function waitsOnYou(ctx, session) {
  const outcome = ctx.outcomeOf ? ctx.outcomeOf(session) : null;
  return !!outcome && outcome.phase === 'ready' && NEEDS_YOU_OUTCOMES.has(outcome.kind);
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
  if (act.state === 'waiting') cards.push('questions');
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
 * or 'offline' (old: dimmed); `first` marks the first look, before anything
 * has been heard.
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
    return { kind: 'connecting', words: 'Connecting…', first: true };
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

/**
 * connectionChip is the connection line as a DS status chip: always an icon
 * and the words, in the colour of how far the numbers can be trusted. A
 * stream on its way back turns the chip's own spinner, the one DS draws for
 * something that is happening now.
 */
function connectionChip({ kind, words }) {
  if (kind === 'connecting') {
    return html`<span class="ds-status ds-status--running"><span class="ds-spinner ds-status__spinner" aria-hidden="true"></span><span>${words}</span></span>`;
  }
  const [modifier, glyph] = {
    live: [' ds-status--ok', 'wifi'],
    polling: ['', 'clock'],
    offline: [' ds-status--warning', 'wifiOff'],
  }[kind] || ['', 'info'];
  return html`<span class="ds-status${modifier}">${raw(icon(glyph, 16))}<span>${words}</span></span>`;
}

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

  // The frame is drawn once; what changes in it - the figures, the notes,
  // the connection chip, the list - is patched in place from then on, so a
  // figure counts on from what it shows and a ring that turns keeps turning.
  function build() {
    morph(host, html`<div class="ds-content">
      <div class="ds-page so-home__page">
        <div class="so-page-head">
          <header class="ds-page-head">
            <h1 class="ds-page-head__title" tabindex="-1">Home</h1>
            <div class="so-home__conn" role="status"></div>
          </header>
          <button class="ds-btn so-home__new" type="button" data-action="new">${raw(icon('plus', 16))}New session</button>
        </div>
        <section class="so-home__numbers" aria-label="At a glance">
          <div class="ds-card ds-card--interactive so-home__questions" data-card="questions" data-value="0" data-state="none">
            <div class="so-home__q">
              <span class="so-home__q-figure" aria-hidden="true">0</span>
              <span class="so-home__q-words">
                <button class="ds-card__action so-home__q-action" type="button" data-action="questions" aria-pressed="false">Questions</button>
                <span class="so-home__q-note"></span>
                <span class="ds-meta so-home__q-meta" hidden></span>
              </span>
              <span class="so-home__q-end">
                <span class="ds-status ds-status--warning so-home__q-status" hidden>${raw(icon('waiting', 16))}<span>Waiting for you</span></span>
                <button class="ds-btn ds-btn--primary so-home__q-answer" type="button" data-action="questions" hidden>Answer</button>
              </span>
            </div>
          </div>
          <div class="so-home__tiles">${CARDS.map((card) => html`<button class="ds-tile so-home__tile" type="button" data-action="card" data-card="${card.key}" data-value="0" aria-pressed="false">
            <span class="ds-tile__label">${raw(icon(card.glyph, 20))}<span>${card.label}</span></span>
            <span class="ds-tile__value">0</span>
            <span class="ds-tile__note">${card.none}</span>
          </button>`)}</div>
        </section>
        <section class="so-home__sessions" aria-labelledby="homeListTitle">
          <div class="so-home__list-head">
            <h2 class="so-home__list-title" id="homeListTitle">Needs you</h2>
            <div class="so-home__segs" role="group" aria-label="Show">
              <button class="ds-btn ds-btn--quiet so-home__seg" type="button" data-action="seg" data-filter="needs" aria-pressed="true">Needs you</button>
              <button class="ds-btn ds-btn--quiet so-home__seg" type="button" data-action="seg" data-filter="all" aria-pressed="false">All</button>
            </div>
          </div>
          <div class="so-home__list"></div>
          <div class="so-home__empty" hidden></div>
        </section>
      </div>
    </div>`);
    host.addEventListener('click', onClick);
    const $ = (selector) => host.querySelector(selector);
    const tiles = new Map();
    for (const node of host.querySelectorAll('.so-home__tile')) {
      tiles.set(node.dataset.card, {
        node, value: node.querySelector('.ds-tile__value'), note: node.querySelector('.ds-tile__note'),
      });
    }
    return {
      conn: $('.so-home__conn'),
      numbers: $('.so-home__numbers'),
      questions: $('.so-home__questions'),
      qFigure: $('.so-home__q-figure'),
      qAction: $('.so-home__q-action'),
      qNote: $('.so-home__q-note'),
      qMeta: $('.so-home__q-meta'),
      qStatus: $('.so-home__q-status'),
      qAnswer: $('.so-home__q-answer'),
      tiles,
      segs: [...host.querySelectorAll('.so-home__seg')],
      listTitle: $('.so-home__list-title'),
      list: $('.so-home__list'),
      empty: $('.so-home__empty'),
    };
  }

  // One listener for every control on Home: the list is patched from markup,
  // so its buttons carry what they do rather than a handler each.
  function onClick(event) {
    const target = event.target.closest('[data-action]');
    if (!target || !host.contains(target)) return;
    const card = target.closest('.so-home__card');
    switch (target.dataset.action) {
      case 'new': ctx.newSession(); break;
      case 'questions':
        if (questionQueue(ctx).length) ctx.openQuestions('');
        else { choose('questions'); showList(); }
        break;
      case 'card':
        choose(filter === target.dataset.card ? 'all' : target.dataset.card);
        showList();
        break;
      case 'seg': choose(target.dataset.filter); break;
      case 'open': if (card) ctx.select(card.dataset.id); break;
      case 'answer': if (card) ctx.openQuestions(card.dataset.id); break;
      default: break;
    }
  }

  function choose(key) {
    filter = key;
    render({ live: false });
  }

  // showList brings the list a tile filtered into view when nothing of its
  // first row can be seen - on a phone the nine tiles fill the screen, and a
  // tap that only changed a list nobody could see looked like a tap that did
  // nothing. Only as far as that row: the tile just tapped and the page's
  // head stay where they are (at a desk, where the list shows already,
  // nothing moves at all).
  const SEEN = 48;
  function showList() {
    if (!built) return;
    const head = built.listTitle.closest('.so-home__list-head');
    const first = [...built.list.children].find((node) => node.getClientRects().length)
      || (built.empty && !built.empty.hidden ? built.empty : null) || head;
    if (!first) return;
    const room = host.getBoundingClientRect();
    const at = first.getBoundingClientRect();
    if (at.top >= room.top && at.top + Math.min(at.height, SEEN) <= room.bottom) return;
    const calm = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    first.scrollIntoView({ block: 'nearest', behavior: calm ? 'auto' : 'smooth' });
  }

  // `live` is false only for the person's own tap: a list they reordered
  // glides at once, one the data reordered waits while it is pointed at.
  function render({ live = true } = {}) {
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

    // The Questions card: the first and the largest, and the one that is
    // answered rather than filtered with.
    const waiting = asking.length;
    const q = built;
    q.questions.dataset.state = waiting ? 'asking' : 'none';
    q.questions.dataset.value = String(waiting);
    figure(q.qFigure, waiting);
    q.qAction.setAttribute('aria-pressed', filter === 'questions' ? 'true' : 'false');
    setText(q.qAction, waiting === 1 ? 'Question' : 'Questions');
    q.qAction.setAttribute('aria-label', waiting
      ? waiting + (waiting === 1 ? ' question' : ' questions') + '. Answer'
      : 'No questions');
    // The question itself, as the push and the dialog say it, and under it
    // whose it is; a session whose words are not read yet is named instead.
    const first = waiting ? asking[0] : null;
    const words = first ? questionWords(first) : '';
    setText(q.qNote, !first ? 'Nothing to answer' : words || first.title);
    const meta = !first ? '' : [words ? first.title : '', waiting > 1 ? 'the oldest of ' + waiting : '']
      .filter(Boolean).join(' · ');
    setText(q.qMeta, meta.charAt(0).toUpperCase() + meta.slice(1));
    q.qMeta.hidden = !meta;
    q.qStatus.hidden = !waiting;
    q.qAnswer.hidden = !waiting;

    for (const card of CARDS) {
      const tile = built.tiles.get(card.key);
      const members = byCard.get(card.key);
      const count = members.length;
      tile.node.dataset.value = String(count);
      tile.node.setAttribute('aria-pressed', filter === card.key ? 'true' : 'false');
      figure(tile.value, count);
      setText(tile.note, count ? cardSub(card.key, members, now) : card.none);
    }
    for (const button of built.segs) {
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
    setText(built.listTitle, filter === 'needs' ? 'Needs you' : filter === 'all' ? 'All sessions' : CARD_LABELS[filter]);
    const labels = folderLabels();
    const cards = shown.map((session) => cardHtml(session, cardsBy.get(session.id) || [], labels, now));
    flipMorph(built.list, html`${cards}`, { items: '.so-home__card', live });
    drawEmpty(sessions, shown);
  }

  // A figure counts from what it shows to what it is now (DS.motion.countUp);
  // the number itself is kept in data-value beside it, which is what is read
  // back - a figure halfway through its count is not the count.
  function figure(node, value) {
    M.countUp(node, value);
  }

  function setText(node, text) {
    if (node.textContent !== text) node.textContent = text;
  }

  // questionWords is the first open question of a session, in its own words
  // (REBUILD.md §5.4), from its digest: '' while it has not been read.
  function questionWords(session) {
    const digest = ctx.digestOf(session.id);
    const question = digest && digest.question;
    if (!question) return '';
    const item = Array.isArray(question.items) ? question.items.find((one) => one && String(one.text || '').trim()) : null;
    return String((item && item.text) || '').trim();
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

  // The chip under the title says how live the numbers are, in words, and
  // the numbers are dimmed while they are not (DS .ds-stale): a count that is
  // a minute old and drawn as if it were current is the one thing this page
  // must not do.
  function drawConnection(now) {
    const line = connectionLine(ctx.connection(), ctx.reachable(), now);
    built.conn.dataset.status = line.kind;
    morph(built.conn, connectionChip(line));
    const stale = line.kind !== 'live' && line.kind !== 'polling';
    setClass(built.numbers, 'ds-stale', stale);
    if (stale) built.numbers.setAttribute('aria-busy', 'true');
    else built.numbers.removeAttribute('aria-busy');
    host.toggleAttribute('data-stale', stale);
    // While it is not live the chip counts the seconds, so "updated 40 s ago"
    // is never a quarter of a minute behind.
    if (line.kind === 'live') stopConnTimer();
    else if (!connTimer) connTimer = setInterval(() => { if (built && !host.hidden) drawConnection(Date.now()); }, CONN_EVERY);
  }

  function stopConnTimer() {
    if (connTimer) { clearInterval(connTimer); connTimer = null; }
  }

  /* ----------------------------------------------------------- the cards */

  // The folder a card works in, by the name the sidebar gives its folder -
  // which is the directory's own name, widened only as far as it takes to
  // tell two of them apart.
  function folderLabels() {
    const labels = new Map();
    for (const folder of foldersOf(ctx.sessions())) labels.set(folder.key, folder.label);
    return labels;
  }

  // One session is one card, keyed by its id, so a patch keeps its node: a
  // node taken out and put back restarts its animation, and the ring of a
  // working session that starts its turn again every second is a page that
  // looks broken.
  //
  // The card is two buttons side by side - its title, which opens the
  // session and whose hit area DS stretches over the whole card, and Answer
  // - and never one button with another inside it, which a screen reader
  // flattens into one.
  function cardHtml(session, cards, labels, now) {
    const asking = cards.includes('questions');
    const live = LIVE_STATES.has(session.state);
    const act = ctx.activityOf(session);
    const digest = ctx.digestOf(session.id);
    const done = cards.includes('done');
    const unread = cards.includes('finished');
    // The hourglass of background work, and of a verdict being read; the
    // verdict's own mark once it is in - the sidebar's corner, larger.
    const outcome = live && ctx.outcomeOf ? ctx.outcomeOf(session) : null;
    const judging = !!outcome && outcome.phase === 'pending';
    const tasks = live && !!act && act.state === 'tasks';
    const badge = tasks || judging ? 'tasks' : outcomeMark(outcome);
    const ring = asking ? 'waiting' : live && !!act && act.state === 'busy' ? 'busy' : '';
    let mark = agentMarkHtml(session.harness, 24);
    if (done) mark = mark.replace('class="so-agent', 'class="so-agent so-agent--quiet');

    const folder = labels.get(folderKeyOf(session)) || '';
    const at = act && Number(act.since) && live ? Number(act.since) : momentOf(session);
    const meta = [folder, agoWords(at, now)].filter(Boolean).join(' · ');

    let line = '';
    if (digest && digest.summary) line = digest.summary;
    else if (digest && digest.phase === 'pending') line = '…';
    else if (isPrepared(session) && session.draft_preview) line = String(session.draft_preview);
    const offer = asking;
    const state = asking ? 'asking' : unread ? 'unread' : done ? 'done' : '';

    return html`<div class="ds-card ds-card--interactive so-home__card" data-ds-key="${session.id}" data-id="${session.id}" data-state="${state}">
      <div class="so-home__card-head">
        <span class="so-mark"${ring ? html` data-ring="${ring}"` : ''}>${raw(mark)}${
          badge ? html`<span class="so-mark__badge" data-kind="${badge}">${raw(outcomeIconHtml(badge, 16))}</span>` : ''}</span>
        <span class="so-home__card-text">
          <button class="ds-card__action so-home__card-title" type="button" data-action="open">${session.title}</button>
          <span class="so-home__card-status">${stateChip(session, cards, act, outcome, judging, now)}<span class="ds-meta so-home__card-meta">${meta}</span></span>
        </span>
        <button class="ds-btn ds-btn--tonal so-home__answer" type="button" data-action="answer"${offer ? '' : ' hidden'}>Answer</button>
      </div>
      <p class="so-home__card-summary"${digest && digest.phase === 'pending' ? ' data-state="pending"' : ''}${line ? '' : ' hidden'}>${line}</p>
      ${verdictHtml(judging ? null : outcome)}
    </div>`;
  }

  // verdictHtml is the block under a card's summary that says why its turn
  // was judged so, and what it left open: the reason, then each open task.
  function verdictHtml(outcome) {
    const kind = outcomeMark(outcome);
    const todos = kind === 'partial' && Array.isArray(outcome.todos) ? outcome.todos : [];
    const reason = !kind ? '' : kind === 'error' ? outcomeLine(outcome) : (outcome.reason || '');
    if (!reason && !todos.length) return '';
    return html`<div class="so-home__verdict" data-kind="${kind}">${
      reason ? html`<p class="so-home__reason">${reason}</p>` : ''}${
      todos.length ? html`<ul class="so-home__todos">${todos.map((todo) => html`<li>${todo}</li>`)}</ul>` : ''}</div>`;
  }

  // The state of a card as a DS status chip: always an icon and the words,
  // the words being the card it is counted in, said the way the sidebar says
  // it. The colour is the chip's for what the state asks of the person: amber
  // for a question, green for work that finished, the spinner for work that
  // goes on.
  function stateChip(session, cards, act, outcome, judging, now) {
    const said = stateWords(session, cards, act, now);
    let tone = '';
    let glyph = 'info';
    if (cards.includes('done')) glyph = 'archive';
    else if (cards.includes('questions')) {
      tone = 'warning';
      glyph = 'waiting';
    } else if (cards.includes('running') || cards.includes('tasks') || judging) tone = 'running';
    else if (outcome && outcome.phase === 'ready' && OUTCOME_ICON[outcome.kind]
      && (cards.includes('finished') || cards.includes('ready'))) {
      tone = OUTCOME_TONE[outcome.kind] || '';
      glyph = OUTCOME_ICON[outcome.kind];
    } else if (cards.includes('finished')) { tone = 'ok'; glyph = 'checkCircle'; }
    else if (cards.includes('stopped')) { tone = session.state === 'failed' ? 'error' : ''; glyph = session.state === 'failed' ? 'alertCircle' : 'ban'; }
    else if (cards.includes('ready')) glyph = 'compose';
    else if (isPrepared(session)) glyph = 'prestart';
    if (!said) return '';
    const lead = tone === 'running'
      ? html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>`
      : raw(icon(glyph, 16));
    return html`<span class="ds-status${tone ? ' ds-status--' + tone : ''} so-home__chip">${lead}<span>${said}</span></span>`;
  }

  // The state of a card in words: the card it is counted in, said the way
  // the sidebar says it, and what it was filed as when that is not the
  // ordinary.
  function stateWords(session, cards, act, now) {
    const words = ctx.words;
    if (cards.includes('done')) return 'Done';
    let said = '';
    if (cards.includes('stopped')) {
      // Not running, and what it finished before it stopped is unread.
      said = words.state[session.state] || '';
      if (cards.includes('finished')) said = said ? said + ' · Finished' : 'Finished';
    } else if (cards.includes('questions')) said = words.activity.waiting;
    else if (cards.includes('running')) said = words.activity.busy;
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

  // What stands where the list would (DS empty state): the one thing to do
  // on an empty machine, or a plain sentence for a filter with nothing in it.
  function drawEmpty(sessions, shown) {
    const empty = built.empty;
    if (shown.length) { empty.hidden = true; return; }
    let markup;
    if (!sessions.length && ctx.loading()) {
      markup = html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Loading…</div>`;
    } else if (!sessions.length && !ctx.reachable()) {
      markup = html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('wifiOff', 24))}</span>
        <p class="ds-empty__title">No connection</p>
        <p class="ds-empty__text">The sessions show up here again as soon as there is signal.</p></div>`;
    } else if (!sessions.length) {
      markup = html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('apps', 24))}</span>
        <p class="ds-empty__title">No sessions yet</p>
        <p class="ds-empty__text">Start one and it shows up here, and in the list.</p>
        <button class="ds-btn ds-btn--tonal" type="button" data-action="new">New session</button></div>`;
    } else {
      markup = html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon(filter === 'needs' ? 'checkCircle' : 'filter', 24))}</span>
        <p class="ds-empty__title">${filter === 'needs' ? 'Nothing needs you right now.' : 'Nothing here right now.'}</p></div>`;
    }
    morph(empty, markup);
    if (empty.hidden) {
      // A filter or live update left nothing: the cards have faded out in
      // place through the flip, and the empty state fades in without a rise.
      empty.hidden = false;
      M.enter(empty, 'fade');
    }
  }

  // countOf is how many sessions a tile's filter holds now.
  function countOf(key) {
    if (key === 'questions') return questionQueue(ctx).length;
    return ctx.sessions().filter((session) => cardsOf(ctx, session).includes(key)).length;
  }

  return {
    show() {
      if (!built) built = build();
      // Coming back to Home from a session is a new visit: it opens at its
      // head, where a question that came meanwhile is, rather than where the
      // list was scrolled to last time, and a tile's filter that has nothing
      // left in it is "Needs you" again rather than a list that says
      // nothing is here. Within a visit both stay as they were put.
      if (host.hidden) {
        if (filter !== 'needs' && filter !== 'all' && !countOf(filter)) filter = 'needs';
        host.hidden = false;
        host.scrollTop = 0;
      }
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
