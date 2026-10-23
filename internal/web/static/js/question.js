// The question dialog: what a session is asking, as the biggest buttons the
// screen allows (HOME.md §2.5).
//
// A session that needs an answer is a menu in a terminal - numbered rows, a
// cursor, a hint about arrow keys - and a terminal is the wrong thing to
// answer it in from a car, or from a phone held in one hand. The digest has
// already read that menu into a question and its options, so this dialog
// shows those: the question in large type, each option a button a finger
// cannot miss, and a field for an answer of one's own with a microphone
// beside it. The answer is not typed by this page. It goes to the server,
// which hands it to the operator, which drives the terminal until the program
// has taken it - so the dialog's job after Send is only to say how that is
// going.
//
// It walks every question there is, oldest first, one after the other, and
// it is live while it does: a question answered somewhere else says so and
// moves on, a question that changes is drawn again, and a new one joins the
// end of the walk.
//
// Errors are said inside the dialog. A toast is drawn under the top layer a
// modal dialog lives in, so a toast from here would be a sentence nobody
// sees.

import { el, restoreFocus } from './api.js';
import { iconEl, showOverlay, hideOverlay } from './ui.js';
import { agentMark } from './logos.js';
import { dictated } from './keyboard.js';
import { speak, stopSpeaking } from './voice.js';

// How long "Answered" and "answered elsewhere" stay on screen before the next
// question takes the dialog, and how long the last word stays before it
// closes. Long enough to be read at a glance, short enough not to be waited
// for.
const MOVE_ON = 1500;

// How often the runs this dialog is following are looked at: asked about
// while the event stream is down and their frames cannot arrive by
// themselves, and checked against their ceiling either way.
const RUN_POLL = 1500;

// The longest a run can take (operatorMaxWall, four minutes) and a margin. A
// run this dialog has heard nothing final about by then is not still going:
// its end was lost - a server that restarted, a frame that never came - and
// the dialog says so rather than "Answering…" for ever.
const RUN_CEILING = 5 * 60 * 1000;

// How long a run whose end was lost waits for its session to be read again
// before the dialog says what it can: a restarted server reads every pane
// again within a few seconds.
const LOST_WAIT = 10000;
const LOST_WAIT_UNREAD = 60000;

// How long a sentence about the voice or the microphone stays on the status
// line. It belongs to no step, so nothing else takes it away.
const VOICE_ERROR_FOR = 10000;

// How often the line that says the dialog may not be current is said again
// while it is shown, as Home's is (HOME.md §2.2).
const CONN_EVERY = 2000;

// Cancel takes the place Send had the moment the answer starts going in, so
// the second tap of a double tap on Send lands on it. A Cancel that has just
// appeared takes no tap for this long. Once keys are being typed, stopping
// leaves the prompt half answered, so it asks for a second tap, which counts
// for this long.
const CANCEL_ARM = 700;
const CANCEL_CONFIRM_FOR = 4000;

// The dialog opens on the first tap of the button that asks for it, and a
// new question or a new step of one is drawn on the tap of the one before:
// what is drawn under the finger then takes no tap for this long. Otherwise
// the second tap of a double tap on Home's Questions card would choose the
// option drawn where the card was, and a third would send it - an answer to
// a question nobody has read. A key (Enter, a digit) is never a stray tap,
// and is taken at once.
const TAP_ARM = 600;
const ARMED = '.so-q-scroll button, .so-q-actions button, .so-q-open';

// How long the request that gives an answer can be on its way (call's
// timeout, and a margin). A send this browser has on record that is younger
// is still waiting for its reply, which will say whose a run is; an older
// one lost its reply.
const SEND_FLIGHT = 25000;

// The tone of the progress line's alert, by its kind; working is the
// neutral alert, with a spinner where its icon would be.
const STATUS_TONE = { done: 'ds-alert--success', error: 'ds-alert--error' };

// spinnerEl is the arc spinner in the place an alert's 20px icon has.
function spinnerEl() {
  return el('span', { class: 'so-q-spin', 'aria-hidden': 'true' }, el('span', { class: 'ds-spinner' }));
}

/**
 * call is one request whose refusal is an answer worth reading - the way
 * prestart.js asks. A 409 here carries the question as it now is, or the run
 * that is already answering, and either is what the dialog draws next.
 * `status` 0 is "it never got there".
 */
async function call(method, path, body, timeout = 20000) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      cache: 'no-store',
      signal: AbortSignal.timeout(timeout),
    });
  } catch {
    return { status: 0, data: {} };
  }
  if (res.status === 401) {
    location.href = '/login';
    return { status: 401, data: {} };
  }
  let data = null;
  try { data = await res.json(); } catch { /* an empty or non-JSON body */ }
  return { status: res.status, data: data || {} };
}

/**
 * isRead says whether a digest is a reading of what the session is asking:
 * there is one, it is not being read again, and it was not made while the
 * session's state was unknown. A server that has just restarted reads every
 * screen before its detector has committed anything, and that digest says
 * what the screen shows - never a question, which is only kept for a session
 * that is asking - so it is "Reading the question…" until the next one.
 */
function isRead(digest) {
  return !!digest && digest.phase !== 'pending'
    && !(digest.phase !== 'error' && digest.state === 'unknown' && !digest.question);
}

// The server's sentence for a digest it could not make for want of an
// OpenRouter key (digestNoKey in internal/server/digest.go). The same server
// refuses every answer for the same reason, so a dialog that shows it offers
// the way to the key, not a field and a Send that can only fail.
const NO_KEY = 'no OpenRouter API key is configured';

function isNoKey(text) {
  return String(text || '').toLowerCase().startsWith(NO_KEY.toLowerCase());
}

function noKey(digest) {
  return !!digest && digest.phase === 'error' && isNoKey(digest.error);
}

/**
 * answerable says whether the server takes an answer to a digest: it has
 * been read (isRead), there is a key to answer with, and a question it still
 * holds was not left over from a reading that failed - the server refuses
 * that one until it has been read again.
 */
function answerable(digest) {
  return isRead(digest) && !noKey(digest) && !(digest.phase === 'error' && digest.question);
}

function sessionPath(id, tail) {
  return '/api/sessions/' + encodeURIComponent(id) + tail;
}

// The one dialog there is. A second call while it is open does not open a
// second one: it brings the session asked for to the front of the walk.
let open = null;

// Numbers the fields of one's own, so each label names its own field.
let ownSeq = 0;

// kept is a small map this browser keeps for every tab of it: in memory,
// and in localStorage for as long as `ttl` says (storage that is off or full
// leaves the tab with what it knows itself).
function kept(key, ttl) {
  const mine = new Map();
  const read = () => {
    try {
      const held = JSON.parse(localStorage.getItem(key) || '{}');
      return held && typeof held === 'object' ? held : {};
    } catch { return {}; }
  };
  const write = (change) => {
    try {
      const held = read();
      const now = Date.now();
      for (const [one, entry] of Object.entries(held)) if (!(now - Number(entry && entry.at) < ttl)) delete held[one];
      change(held);
      localStorage.setItem(key, JSON.stringify(held));
    } catch { /* this tab still knows */ }
  };
  return {
    get(name) {
      const entry = read()[name] || mine.get(name);
      return entry && Date.now() - Number(entry.at) < ttl ? entry : null;
    },
    set(name, entry) {
      const value = { ...entry, at: Number(entry.at) || Date.now() };
      mine.set(name, value);
      write((held) => { held[name] = value; });
    },
    delete(name) {
      mine.delete(name);
      write((held) => { delete held[name]; });
    },
  };
}

// The runs an answer sent from this browser started, by id. They outlive one
// dialog: a dialog closed and opened again while its answer goes in is still
// following its own answer, and one started by another device is not - the
// person is not told their answer went in, and it does not count towards
// "All questions answered".
//
// They are the browser's and not the tab's: a second tab of the same
// browser, or this one reloaded while its answer goes in, is the same
// device, and "another device is answering" there would not be so.
const runsHere = kept('socrates.answer-runs', 10 * 60 * 1000);
const startedHere = {
  has: (runId) => !!runId && !!runsHere.get(runId),
  add: (runId) => { if (runId) runsHere.set(runId, {}); },
};

// The answers this browser has sent whose reply has not named their run yet,
// by session: the run there was before the tap, and when. The stream shows a
// run to every tab before the tab that sent it has its reply, and a reply
// lost to a dialog that was closed never comes at all; a run for that session
// that is not the one there was before is this answer going in - the rule
// checkSent follows.
const sendsHere = kept('socrates.answer-sends', RUN_CEILING);

/**
 * whose says whose a run of a session is, as far as this browser knows:
 * 'mine' for one a reply named (startedHere), 'unsure' while an answer this
 * browser sent is still waiting for its reply, 'theirs' otherwise. A run seen
 * while the reply is on its way is not taken for this answer on sight:
 * another device's that started first reaches here the same way, and the
 * reply then refuses this one for it. A send whose reply was lost - it never
 * came (`lost`), or not within SEND_FLIGHT, the tab that sent it closed - can
 * be told apart no other way, and a run that is not the one held before the
 * tap is taken for it (the rule checkSent follows).
 */
function whose(id, runId) {
  if (!runId) return 'theirs';
  if (startedHere.has(runId)) return 'mine';
  const sent = sendsHere.get(id);
  if (!sent || sent.stale === runId) return 'theirs';
  if (!sent.lost && Date.now() - Number(sent.at) < SEND_FLIGHT) return 'unsure';
  sendsHere.delete(id);
  startedHere.add(runId);
  return 'mine';
}

// The states of a session whose program is not running. Such a session is
// not asking anything, and was not answered either.
const STOPPED_STATES = new Set(['needs_resume', 'exited', 'failed']);
const LIVE_STATES = new Set(['running', 'starting', 'resuming']);

function liveRun(run) {
  return !!run && !!run.run_id && run.phase !== 'done' && run.phase !== 'error';
}

/** questionsOpen says whether the dialog is on screen. */
export function questionsOpen() { return !!open; }

/**
 * closeQuestions closes the dialog, for a page that is about to put a
 * session in front of the person - one a notification or an address asked
 * for. The dialog is a modal, and one left open would hold that session
 * inert behind it.
 */
export function closeQuestions() {
  if (open) open.close();
}

/**
 * updateQuestions tells an open dialog that something it is drawn from has
 * changed - the list, an activity, a digest, a run. It works out for itself
 * what that means for the question on screen.
 */
export function updateQuestions() {
  if (open) open.sync();
}

/**
 * openQuestions opens the dialog, at `startId` if that session is asking and
 * at the oldest question otherwise. ctx:
 *
 *   queue()              the sessions that are asking, oldest first
 *   sessionOf(id)        a session by id, or null
 *   activityOf(session)  what it is doing, or null
 *   digestOf(id)         its digest, or null
 *   answerOf(id)         its answer run, or null
 *   adoptDigest(id, d)   a digest a refusal carried, for the page to keep
 *   adoptRun(id, run)    a run an answer or a poll returned
 *   folderOf(session)    the name of its folder, as the sidebar has it
 *   live()               whether the event stream is delivering
 *   connection()         {kind, words} of Home's connection line
 *                        (connectionLine): 'connecting' or 'offline' is said
 *                        under the top bar, and the question is dimmed
 *   freshAt()            the moment the page's state is known to be the
 *                        server's as of: a hello, a poll, or a pong (see
 *                        settled); a page asking for a question pings
 *   digestsHeard()       whether a hello or a poll has handed over the
 *                        digests since the page loaded (beingRead)
 *   reachable()          whether the last attempt to read it got through
 *   select(id)           open the session itself
 */
export function openQuestions(ctx, startId = '') {
  if (open) {
    if (startId) open.focus(startId);
    return;
  }
  open = mount(ctx, startId);
}

function mount(ctx, startId) {
  // A Dashboard-Style dialog (so-question.css makes it the whole screen on a
  // phone and in touch mode). Its backdrop does not close it: a stray tap
  // beside the box, from a car's seat, is not a wish to leave the question.
  const dialog = el('dialog', { class: 'ds-dialog so-question', 'aria-label': 'Questions' },
    el('div', { class: 'ds-dialog__backdrop' }));
  // Everything is drawn inside the box and never on the dialog itself:
  // keyboard.js parks its copy of the keyboard button on the dialog, and
  // redrawing the dialog's own children would take that away. It can hold
  // the focus itself, for a redraw that took away what had it.
  const shell = el('div', { class: 'ds-dialog__box so-q-shell', tabindex: '-1' });
  dialog.append(shell);

  // The walk: the sessions in the order they are shown, and where it is.
  let queue = [];
  let index = 0;
  // What has been answered here: the rev of the digest it was answered from
  // and the commit (`since`) of the activity it was asked in, as {rev,
  // since}. A session that is still asking after its answer went in is
  // asking the next step of a prompt, and joins again once its digest has
  // moved past the one that was answered - or at once, being read, when it
  // has started asking again since: it took the answer, went on, and asks a
  // follow-up the digest has not read yet, which Home already counts.
  const answered = new Map();
  // How many answers this dialog itself saw go in: the last word is "All
  // questions answered" only when that is what happened here.
  let answeredHere = 0;
  // Per session: which item is shown, what has been chosen and typed for
  // each, and where the answer stands.
  const states = new Map();
  // The questions that are on their way out, each on a timer of its own: one
  // that was answered while another was on screen goes when it is next
  // looked at, without taking the other's timer with it.
  const moves = new Map();
  // The field of one's own on screen, and the state it was built for. It is
  // kept across redraws of the same question - see graft - so the words
  // being typed or dictated into it, its keyboard and its focus all stay.
  const ownOf = new WeakMap();
  let closeTimer = null;
  let runTimer = null;
  let connTimer = null;
  let sayGen = 0;
  let speaking = false;
  let voiceError = '';
  let voiceTimer = null;
  let finished = false;
  let closed = false;
  let drawn = null;
  // What the answering buttons on screen were drawn for (the session, the
  // step, the question), and the timer that ends their TAP_ARM.
  let armedFor = '';
  let armTimer = null;
  const was = document.activeElement;
  // A question asked for by a link or a notification is only called gone
  // once the page holds a state newer than the moment it was asked for
  // (askedAt, or this for one asked for by opening the dialog).
  const openedAt = Date.now();

  function stateOf(id) {
    let st = states.get(id);
    if (!st) {
      st = {
        rev: -1, signature: '', item: 0, choices: new Map(), texts: new Map(), own: '',
        // check: asked for by a link, and not yet known to be asking; ask:
        // waiting for the person; sending: the request is out; run: the
        // operator is answering; done: it has; error: something failed;
        // gone: the question went away by itself; stopped: the session's
        // program stopped with the question unanswered.
        phase: 'ask', error: '', note: '', runId: '', staleRunId: '', retry: null, sentRev: -1, sentSince: 0,
        // checking: the reply to Send was lost, and the server is being asked
        // whether the answer went in. foreign: the run followed is not one
        // this page started; clash: and this dialog's own Send was refused
        // for it; provisional: whose it is waits for the reply to an answer
        // this browser sent (whose), and its end is not taken until then.
        checking: false, foreign: false, clash: false, provisional: false,
        // The question the answer was given to, which is what is drawn while
        // it is being given: the digest's own question is cleared the moment
        // the session moves on.
        sentSig: '', sentDetail: '', asked: null, runSince: 0, runError: false, after: '', lostAt: 0, contextOpen: false,
        // typed: the run followed has been seen typing, so stopping it would
        // leave the prompt half answered; stopAsked: when Cancel asked for a
        // second tap because of that.
        typed: false, stopAsked: 0, cancelAt: 0, cancelWords: '',
        // When a link or a notification asked for it (settled).
        askedAt: 0,
      };
      states.set(id, st);
      // A dialog opened while an answer to this session is already being
      // given - this one closed and opened again, or another device - shows
      // that answer going in rather than a question to be answered twice.
      const run = ctx.answerOf(id);
      if (liveRun(run)) takeOver(id, st, run);
    }
    return st;
  }

  // takeOver follows a run this dialog did not start: the options are locked
  // and the run is shown going in, as the dialog's own would be. Whether it
  // is the person's own answer is whether this browser sent it (whose).
  function takeOver(id, st, run) {
    const digest = ctx.digestOf(id);
    const sending = st.phase === 'sending';
    st.phase = 'run';
    st.runId = run.run_id;
    st.staleRunId = '';
    st.runSince = Date.now();
    const who = whose(id, run.run_id);
    st.foreign = who === 'theirs';
    st.provisional = who === 'unsure';
    st.clash = false;
    st.error = '';
    st.note = '';
    st.after = '';
    st.retry = null;
    st.runError = false;
    st.lostAt = 0;
    st.checking = false;
    st.typed = false;
    st.stopAsked = 0;
    // The asking the run answers: the one this page's own Send went to, or
    // the one on screen now. A later one is the next question (joinable).
    if (!sending || !st.sentSince) st.sentSince = askSince(id);
    if (digest && digest.question) {
      st.sentRev = Number(digest.rev) || 0;
      st.sentSig = signatureOf(digest);
      st.sentDetail = detailOf(digest);
      st.asked = digest;
    } else if (st.sentRev < 0) {
      st.sentRev = Number(digest && digest.rev) || 0;
      st.sentSig = signatureOf(digest);
      st.sentDetail = detailOf(digest);
    }
    startRunWatch();
  }

  // stopped says whether a session is there and its program is not running
  // by itself: a crash, a kill, a quit at the machine. One filed as done has
  // its terminal closed too, but that is the person's doing (filedDone).
  function stopped(id) {
    const session = ctx.sessionOf(id);
    return !!session && session.status !== 'done' && STOPPED_STATES.has(session.state);
  }

  // filedDone says whether a session was marked done - here, on another
  // device - which closes its terminal: it asks nothing any more, and there
  // is nothing in it to open.
  function filedDone(id) {
    const session = ctx.sessionOf(id);
    return !!session && session.status === 'done';
  }

  // unsure is a session whose program is running and whose state the page
  // does not know yet: a server that has just restarted has read no pane
  // again. Not asking is not known about it, so it is neither "answered
  // elsewhere" nor dropped from the walk until it is.
  function unsure(id) {
    const session = ctx.sessionOf(id);
    if (!session || !LIVE_STATES.has(session.state)) return false;
    const act = ctx.activityOf(session);
    return !!act && act.state === 'unknown';
  }

  // beingRead is a session asked for by a link or a notification that is
  // idle, and whose question - if it asks one - only its digest can find,
  // while that digest has not been read: pending, or not handed to this
  // page at all yet (no hello since it loaded). A question in prose is
  // nothing but a digest's finding, and a tap on its notification that
  // wakes a phone, or launches the app, can arrive before the read that
  // found it is in the page. Not asking is not known about it until the
  // read is done, so it is looked for until then rather than called gone.
  function beingRead(id) {
    const session = ctx.sessionOf(id);
    if (!session || !LIVE_STATES.has(session.state)) return false;
    const act = ctx.activityOf(session);
    if (!act || act.state !== 'idle') return false;
    const digest = ctx.digestOf(id);
    if (digest) return digest.phase === 'pending';
    return !!ctx.digestsHeard && !ctx.digestsHeard();
  }

  // undecided is a session asked for that the page cannot yet call not
  // asking, whatever it has heard since: its state, or its question, is
  // still being found out.
  const undecided = (id) => unsure(id) || beingRead(id);

  // waitingNow is a session whose program is waiting for an answer, as the
  // detector last committed it.
  function waitingNow(session) {
    if (!session || !LIVE_STATES.has(session.state)) return false;
    const act = ctx.activityOf(session);
    return !!act && act.state === 'waiting';
  }

  const currentId = () => queue[index] || '';
  const asking = () => ctx.queue().map((s) => s.id);

  // settled says whether what the page holds is new enough to say that a
  // session asked for by a link or a notification is not asking: a whole
  // state read since it was asked for - a hello, a poll sent after it - or a
  // pong to a ping sent after it, which says every frame before it has
  // arrived. A stream that calls itself live is not enough: a socket can be
  // open and carry nothing (half-open, after a tunnel), and it is only found
  // out when it misses a tick - long after a tap on "Needs an answer" would
  // have been told there is no question.
  const settled = (st) => Number(ctx.freshAt && ctx.freshAt()) >= ((st && st.askedAt) || openedAt);

  // A question is its items and their options as the screen fixes them: each
  // item's question as the screen (or the program's own record) spells it,
  // whether it takes several answers or one of one's own, and its option
  // labels in order. Two digests with the same question are the same
  // question, and what was chosen for it is kept; a digest that asks
  // something else starts the choices again. What the model words itself -
  // the header, the text in the person's language, the descriptions, which
  // option it calls recommended - is left out: the same screen read again (a
  // restart, a resize, Read again) may come back worded differently, and it
  // is still the question the person was answering. An item with no
  // screen_text (the model left it out, or the screen shows no question text)
  // has only its text to tell it from another question with the same labels,
  // and keeps it: an old answer shown pressed on a new question is worse than
  // a choice made again.
  function signatureOf(digest) {
    const q = digest && digest.question;
    if (!q) return '';
    const items = (q.items || []).map((item) => [
      String(item.screen_text || item.text || ''), !!item.multi, !!item.free_text,
      (item.options || []).map((option) => String(option.label || '')),
    ]);
    return JSON.stringify(items) + '|' + (q.kind || '');
  }

  function detailOf(digest) {
    const q = digest && digest.question;
    return q ? String(q.detail || '') : '';
  }

  // againRev is the rev an answer sent again from its error goes with (see
  // send): the current one only for what is, by everything the page can see,
  // the question it was given to - the same items and options and the same
  // detail (a server that restarted reads every question again, under a new
  // rev). A prompt that only looks the same - "Proceed? Yes / No" for the
  // next command, whose detail names it - goes with the old rev, and the
  // server refuses it (409) and shows it, rather than taking the answer given
  // to the one before. At worst that is one more tap on Send.
  function againRev(st, digest) {
    const same = signatureOf(digest) === st.sentSig && detailOf(digest) === st.sentDetail;
    return same ? -1 : st.sentRev;
  }

  // askSince is the commit of what a running session is doing now: the
  // moment it started waiting, or started its turn that ended in a question
  // in prose. It only moves when the state does, so two of them tell one
  // asking from the next.
  function askSince(id) {
    const session = ctx.sessionOf(id);
    if (!session || !LIVE_STATES.has(session.state)) return 0;
    const act = ctx.activityOf(session);
    return Number(act && act.since) || 0;
  }

  function joinable(id) {
    if (!answered.has(id)) return true;
    const was = answered.get(id);
    const digest = ctx.digestOf(id);
    if (digest && digest.phase === 'ready' && digest.question && Number(digest.rev) > was.rev) return true;
    const since = askSince(id);
    return was.since > 0 && since > was.since;
  }

  // working is a session whose answer this dialog sent and has not yet seen
  // through. It is never dropped from the walk while it is.
  function working(id) {
    const st = states.get(id);
    return !!st && (st.phase === 'sending' || st.phase === 'run');
  }

  /* ------------------------------------------------------------ the walk */

  function start() {
    const fresh = asking();
    queue = fresh.filter(joinable);
    if (startId) {
      queue = [startId, ...queue.filter((id) => id !== startId)];
      if (!fresh.includes(startId)) askedFor(startId);
    }
    index = 0;
  }

  // askedFor is a session a link or a notification asked for that the page does
  // not count as asking. That is only an answer once the page's state is
  // newer than the link: a tab woken by a notification holds the list from
  // before it slept, and an offline start holds none at all.
  function askedFor(id) {
    const st = stateOf(id);
    if (st.phase !== 'ask') return;
    st.askedAt = Date.now();
    if (settled(st) && !undecided(id)) notAsking(id, st);
    else st.phase = 'check';
  }

  // notAsking is a session the page knows is not asking: there never was a
  // question here as far as this dialog knows, so it does not say there was.
  function notAsking(id, st) {
    st.phase = 'gone';
    st.note = !ctx.sessionOf(id) ? 'This session no longer exists.'
      : filedDone(id) ? 'This session was marked done.'
        : stopped(id) ? 'This session is not running.' : 'No question is waiting here any more.';
    if (id === currentId()) scheduleMove(id);
  }

  // gone is a question that went away while it was on screen: answered in
  // the terminal, on another device, or by the program moving on. Only a
  // question that was waiting for the person goes this way. The failure of
  // an answer this dialog gave stays on screen with what went wrong - the
  // session moving on is not news that the answer went in.
  //
  // Nor is a program that stopped: a session that crashed, was killed or
  // was quit at the machine with its question open was answered nowhere. It
  // says so and stays, with Open session, rather than moving on as if the
  // question had been taken care of. A session marked done is neither: the
  // person filed it, here or on another device, and closing its terminal
  // was part of that. It says so and moves on, with nothing to open.
  function gone(id) {
    const st = stateOf(id);
    if (st.phase === 'check') { if (settled(st) && !undecided(id)) notAsking(id, st); return; }
    // A session that was deleted was answered nowhere: that it is gone is
    // the news, and an answer that failed keeps its error.
    const exists = !!ctx.sessionOf(id);
    const done = filedDone(id);
    const halted = stopped(id);
    if (st.phase === 'error' && (st.runError || !exists || halted || done)) {
      if (st.retry) {
        st.retry = null;
        st.after = !exists ? 'This session no longer exists.'
          : done ? 'This session was marked done.'
            : halted ? 'The session has stopped.' : 'The session is no longer asking.';
      }
      return;
    }
    if (done && (st.phase === 'ask' || st.phase === 'error' || st.phase === 'stopped')) {
      st.phase = 'gone';
      st.error = '';
      st.retry = null;
      st.note = 'This session was marked done.';
      if (id === currentId()) scheduleMove(id);
      return;
    }
    if (st.phase !== 'ask' && st.phase !== 'error') return;
    if (halted) {
      st.phase = 'stopped';
      st.error = '';
      st.retry = null;
      st.note = 'The session stopped before the question was answered.';
      return;
    }
    st.phase = 'gone';
    st.note = exists ? 'This question was answered elsewhere.' : 'This session no longer exists.';
    if (id === currentId()) scheduleMove(id);
  }

  function scheduleMove(id) {
    if (moves.has(id)) return;
    moves.set(id, setTimeout(() => {
      moves.delete(id);
      moveOn(id);
    }, MOVE_ON));
  }

  // moveOn takes a session out of the walk - it was answered, here or
  // elsewhere - and shows the next one, or the last word when there is none.
  function moveOn(id) {
    const at = queue.indexOf(id);
    if (finished || at < 0) return;
    // The digest that was answered, rather than the one there is now: a
    // prompt that has already moved on to its next step has a newer digest,
    // and that one is a question still to be answered.
    const st = states.get(id);
    const digest = ctx.digestOf(id);
    answered.set(id, {
      rev: st && st.sentRev >= 0 ? st.sentRev : Number(digest && digest.rev) || 0,
      // The asking that was answered, not the one there is now: by the time
      // "Answered" has been shown the program may already ask again.
      since: st && st.sentSince > 0 ? st.sentSince : askSince(id),
    });
    // Only an answer this page gave counts: one another device gave is a
    // question that went, not one answered here.
    if (st && st.phase === 'done' && !st.foreign && !st.provisional) answeredHere += 1;
    states.delete(id);
    queue.splice(at, 1);
    // The question on screen stays the one on screen: an earlier one leaving
    // moves it one place up the walk, not one question further.
    if (index > at) index -= 1;
    if (index >= queue.length) index = Math.max(0, queue.length - 1);
    stopReading();
    clearVoiceError();
    // Anything that started asking while this was shown is already at the end
    // of the walk; anything that is asking and is not, joins now.
    for (const fresh of asking()) if (!queue.includes(fresh) && joinable(fresh)) queue.push(fresh);
    if (!queue.length) { finish(); return; }
    draw(true);
  }

  function finish() {
    finished = true;
    drawn = null;
    armedFor = '';
    stopRunWatch();
    stopConnTimer();
    shell.removeAttribute('data-stale');
    // The last word is DS's empty state: nothing is left to answer.
    shell.replaceChildren(el('div', { class: 'ds-empty so-q-finished', role: 'status' },
      el('span', { class: 'ds-empty__disc', 'aria-hidden': 'true' }, iconEl('check', 24)),
      el('h2', { class: 'ds-empty__title so-q-finished-title', text: answeredHere ? 'All questions answered' : 'No question is waiting' }),
      el('button', { class: 'ds-btn ds-btn--tonal so-q-big', type: 'button', text: 'Close', onclick: close })));
    closeTimer = setTimeout(close, MOVE_ON);
  }

  // resume is the walk starting again from its last word: a question asked
  // for, or one that started asking, in the moment before the dialog closes.
  // What was asked for is shown rather than dropped with the dialog.
  function resume() {
    if (closeTimer) { clearTimeout(closeTimer); closeTimer = null; }
    finished = false;
    drawn = null;
    queue = [];
    index = 0;
  }

  function focus(id) {
    if (closed) return;
    if (finished) {
      resume();
      for (const fresh of asking()) if (joinable(fresh)) queue.push(fresh);
    }
    const at = queue.indexOf(id);
    if (at >= 0) index = at;
    else {
      queue.splice(index, 0, id);
      if (!asking().includes(id)) askedFor(id);
    }
    clearVoiceError();
    draw(true);
  }

  function go(step) {
    const next = index + step;
    if (next < 0 || next >= queue.length) return;
    index = next;
    stopReading();
    clearVoiceError();
    draw(true);
  }

  /**
   * sync is something the dialog is drawn from having changed. It follows the
   * walk - questions that went, questions that arrived - and the question on
   * screen: a newer digest, a run moving on.
   */
  function sync() {
    if (closed) return;
    const fresh = asking();
    for (const id of [...answered.keys()]) if (!fresh.includes(id)) answered.delete(id);
    if (finished) {
      // A question that arrives while the last word is on screen is a walk
      // again: new questions join, even the moment before the dialog closes.
      const more = fresh.filter(joinable);
      if (!more.length) return;
      resume();
      queue = more;
      draw(true);
      return;
    }
    for (const id of fresh) if (!queue.includes(id) && joinable(id)) queue.push(id);
    for (const id of queue) {
      const st = states.get(id);
      if (!st) continue;
      // A session asked for by a link that the page now sees asking is a
      // question, and one the page now knows is not asking says so.
      if (st.phase === 'check') {
        if (fresh.includes(id)) st.phase = 'ask';
        else if (settled(st) && !undecided(id)) notAsking(id, st);
        continue;
      }
      // An answer another device started while this one shows the question
      // is followed, not offered again: the options lock and its progress is
      // shown, as for a dialog opened while it runs.
      const run = ctx.answerOf(id);
      if ((st.phase === 'ask' || st.phase === 'error') && liveRun(run) && run.run_id !== st.runId) {
        takeOver(id, st, run);
        continue;
      }
      // A question taken for gone that is asking after all - the list it was
      // judged by was older than the stream's first word - or a session that
      // stopped and asks again once it is started, is a question again, and
      // stays.
      if ((st.phase !== 'gone' && st.phase !== 'stopped') || !fresh.includes(id)) continue;
      st.phase = 'ask';
      st.note = '';
      clearTimeout(moves.get(id));
      moves.delete(id);
    }
    // Every answer this dialog is giving is followed, not only the one on
    // screen: an answer whose end came while another question was shown would
    // otherwise still say "Answering…" when it is looked at again.
    for (const id of queue) {
      const st = states.get(id);
      if (st && (st.phase === 'sending' || st.phase === 'run')) followRun(id, st);
    }
    // A question that went away while it was not on screen leaves quietly.
    // One whose session's state is not known yet has not gone anywhere.
    for (const id of [...queue]) {
      if (id === currentId() || fresh.includes(id) || working(id) || unsure(id)) continue;
      const st = states.get(id);
      if (st && (st.phase === 'gone' || st.phase === 'done' || st.phase === 'check' || st.phase === 'stopped' || st.runError)) continue;
      const at = queue.indexOf(id);
      queue.splice(at, 1);
      states.delete(id);
      if (index > at) index -= 1;
    }
    const id = currentId();
    if (!id) { if (!queue.length) finish(); return; }
    const st = stateOf(id);
    if (!fresh.includes(id) && !working(id) && st.phase !== 'done' && !unsure(id)) gone(id);
    draw(false);
  }

  /* ------------------------------------------------------------- the run */

  // followRun reads where the answer to a session stands from the run the
  // page holds for it, which the event stream keeps current.
  function followRun(id, st) {
    if (st.phase !== 'sending' && st.phase !== 'run') return;
    ownedNow(id, st);
    if (st.lostAt) { settleLost(id, st); return; }
    const run = ctx.answerOf(id);
    // The run this dialog is following is no longer the one the page holds:
    // it ended without its end being heard - the server restarted, or the
    // finished run was forgotten while this tab slept.
    if (st.phase === 'run' && st.runId && (!run || run.run_id !== st.runId)) {
      lostRun(id, st);
      return;
    }
    if (!run || (st.runId && run.run_id !== st.runId) || (!st.runId && run.run_id === st.staleRunId)) return;
    st.runId = run.run_id || st.runId;
    if (run.phase === 'acting') st.typed = true;
    if (st.phase === 'sending') {
      // A run that started after this dialog's Send, heard on the stream
      // before (or instead of) the reply to the request. It is followed, and
      // it is this answer only once the reply names it, or the reply was
      // lost (whose): another device's answer that started first comes here
      // the same way, and the reply then refuses this one for it.
      st.phase = 'run';
      st.runSince = Date.now();
      st.checking = false;
      st.note = '';
      st.provisional = true;
      ownedNow(id, st);
    }
    // Whose answer went in, or failed, is said once it is known: the reply
    // is on its way, and follows the run again when it comes.
    if (st.provisional && (run.phase === 'done' || run.phase === 'error')) return;
    if (run.phase === 'done') {
      st.phase = 'done';
      st.note = run.summary || '';
      if (id === currentId()) scheduleMove(id);
    } else if (run.phase === 'error' && st.foreign) {
      // Another device's answer that did not go in: the question is open
      // here again, to be answered from this one.
      st.phase = 'ask';
      st.runId = '';
      st.foreign = false;
      st.note = 'The answer from another device did not go in.';
    } else if (run.phase === 'error') {
      st.phase = 'error';
      st.error = run.error || 'The answer could not be given.';
      st.retry = 'send';
      st.runError = true;
    }
  }

  // ownedNow settles whose the run followed is, once that is known (whose):
  // one taken for another device's can be this browser's after all - another
  // tab of it sent the answer, and the stream showed the run here before that
  // tab had the reply that names it - and a provisional one is this answer
  // or, when the reply refused it, another device's, which this Send was
  // refused for.
  function ownedNow(id, st) {
    if (!st.runId || (!st.foreign && !st.provisional)) return;
    const who = whose(id, st.runId);
    if (who === 'mine') {
      st.foreign = false;
      st.clash = false;
      st.provisional = false;
    } else if (who === 'theirs' && st.provisional) {
      st.provisional = false;
      st.foreign = true;
      st.clash = true;
    }
  }

  // lostRun is a run that ended without this dialog hearing how. Whether the
  // answer went in is read from the session: still asking the question that
  // was answered is an answer that did not; a session that is doing
  // something else is a question that is no longer waiting, which is all that
  // is known. A session whose state is not known yet - a server that has just
  // restarted reads every pane again - is waited for, a little.
  function lostRun(id, st) {
    st.runId = '';
    st.provisional = false;
    if (!st.lostAt) st.lostAt = Date.now();
    settleLost(id, st);
  }

  function settleLost(id, st) {
    const waiting = asking().includes(id);
    const digest = ctx.digestOf(id);
    // Read, and read as a question: a session read again before it was seen
    // asking has a digest with no question in it, which says nothing yet.
    const readable = isRead(digest) && (!!digest.question || !st.sentSig);
    const still = waiting && readable && signatureOf(digest) === st.sentSig;
    const session = ctx.sessionOf(id);
    const act = session && ctx.activityOf(session);
    // A session whose state has not been read again yet is given longer.
    const unread = !act || !act.state || act.state === 'unknown' || session.state !== 'running';
    const early = Date.now() - st.lostAt < (unread ? LOST_WAIT_UNREAD : LOST_WAIT);
    // Only two things settle it before the wait is over: the session asking
    // a question that has been read (the same one, or the next), or working
    // (it took something). Anything else - a state not read yet, a row that
    // is coming back up - is waited out.
    // A session marked done is not coming back up, and is not waited for.
    const done = filedDone(id);
    if (session && early && !done && !(waiting && readable) && !(act && act.state === 'busy')) return;
    st.lostAt = 0;
    if (still && st.foreign) {
      st.phase = 'ask';
      st.runId = '';
      st.foreign = false;
      st.note = 'The answer from another device was interrupted, and the question is still waiting.';
      return;
    }
    if (done) {
      st.phase = 'gone';
      st.retry = null;
      st.note = 'This session was marked done.';
      if (id === currentId()) scheduleMove(id);
      return;
    }
    if (session && STOPPED_STATES.has(session.state)) {
      st.phase = 'stopped';
      st.retry = null;
      st.note = 'The session stopped while it was being answered.';
      return;
    }
    if (still) {
      st.phase = 'error';
      st.error = 'The answer was interrupted, and the question is still waiting.';
      st.retry = 'send';
      st.runError = true;
      return;
    }
    st.phase = 'gone';
    st.note = session ? 'The question is no longer waiting.' : 'This session no longer exists.';
    if (id === currentId()) scheduleMove(id);
  }

  // The runs are watched while any is being given: each is held to its
  // ceiling, and while the stream is down nothing tells the dialog how one is
  // going, so it asks.
  function startRunWatch() {
    if (runTimer) return;
    runTimer = setInterval(watchRuns, RUN_POLL);
  }

  function stopRunWatch() {
    if (runTimer) { clearInterval(runTimer); runTimer = null; }
  }

  async function watchRuns() {
    if (closed || finished) { stopRunWatch(); return; }
    const ids = [...states.keys()].filter(working);
    if (!ids.length) { stopRunWatch(); return; }
    let moved = false;
    for (const id of ids) {
      const st = states.get(id);
      if (!st) continue;
      if (st.lostAt) { settleLost(id, st); moved = true; continue; }
      // An answer whose reply was lost is asked about until the server says
      // whether it started a run for it.
      if (st.phase === 'sending' && st.checking) { await checkSent(id, st); continue; }
      if (st.phase === 'run' && st.runSince && Date.now() - st.runSince > RUN_CEILING) {
        lostRun(id, st);
        moved = true;
        continue;
      }
      // Whose a run is can be settled by another tab's reply, or by its
      // reply being too late to come at all (whose), which no frame says.
      if (st.provisional) moved = true;
      if (ctx.live() || st.phase !== 'run' || !st.runId || st.asking) continue;
      st.asking = true;
      const { status, data } = await call('GET', sessionPath(id, '/answer'), undefined, 8000);
      st.asking = false;
      if (closed || finished) return;
      if (states.get(id) !== st || st.phase !== 'run' || status !== 200) continue;
      if (data && data.run && data.run.run_id === st.runId) ctx.adoptRun(id, data.run);
      else lostRun(id, st);
      moved = true;
    }
    if (moved) sync();
  }

  // send gives the answer chosen for the question on screen. `again`, when it
  // is not -1, is the rev of the digest an answer that failed was given to,
  // for one sent again from its error (againRev): it goes with that rev and
  // not the one there is now, so a question that may have changed in the
  // meantime is refused by the server (409) and shown, rather than given the
  // answer meant for the one before.
  async function send(id, again = -1) {
    const st = stateOf(id);
    const digest = ctx.digestOf(id);
    // Nothing is sent while the question is being read again: the answer
    // would be to the question as it was, and the server would refuse it.
    // Nor to one the server refuses whatever the answer (usable).
    if (!usable(id, digest)) return;
    const question = digest.question;
    const rev = again >= 0 ? again : Number(digest.rev) || 0;
    const body = { digest_rev: rev, answers: [], text: '' };
    if (question) {
      (question.items || []).forEach((item, at) => {
        const text = (st.texts.get(at) || '').trim();
        const choices = text ? [] : (st.choices.get(at) || []);
        if (text || choices.length) body.answers.push({ item: at, choices, text });
      });
      if (!body.answers.length) return;
    } else {
      body.text = st.own.trim();
      if (!body.text) return;
    }
    st.sentRev = body.digest_rev;
    st.sentSince = askSince(id);
    st.sentSig = signatureOf(digest);
    st.sentDetail = detailOf(digest);
    if (question) st.asked = digest;
    const before = ctx.answerOf(id);
    st.staleRunId = before ? before.run_id : '';
    st.runId = '';
    st.phase = 'sending';
    st.error = '';
    st.note = '';
    st.after = '';
    st.runError = false;
    st.lostAt = 0;
    st.checking = false;
    st.foreign = false;
    st.clash = false;
    st.provisional = false;
    st.typed = false;
    st.stopAsked = 0;
    stopReading();
    draw(true);
    sendsHere.set(id, { stale: st.staleRunId });
    const { status, data } = await call('POST', sessionPath(id, '/answer'), body);
    // The run this answer started is this browser's whatever became of the
    // dialog meanwhile: one closed while the answer was on its way and opened
    // again shows it as the person's own answer, not another device's. A
    // reply that never came leaves the send on record as lost, for checkSent
    // here or for a dialog opened again (whose); any other reply settles it.
    const started = status >= 200 && status < 300 && data && data.run ? data.run : null;
    if (started && started.run_id) startedHere.add(started.run_id);
    if (status !== 0) sendsHere.delete(id);
    else {
      const sent = sendsHere.get(id);
      if (sent) sendsHere.set(id, { ...sent, lost: true });
    }
    if (closed || finished) {
      if (started) ctx.adoptRun(id, started);
      return;
    }
    if (status >= 200 && status < 300) {
      if (started) {
        st.runId = started.run_id || '';
        st.provisional = false;
        st.foreign = false;
        st.clash = false;
        ctx.adoptRun(id, started);
      } else if (st.provisional && st.runId) {
        // Taken, with no run named: the one the stream showed is it.
        startedHere.add(st.runId);
      }
      if (st.phase === 'sending') { st.phase = 'run'; st.runSince = Date.now(); }
      startRunWatch();
      sync();
      return;
    }
    if (st.phase !== 'sending' && status !== 0 && states.get(id) === st) {
      // Refused, while the dialog follows a run the stream showed on the way:
      // that run is not this answer - another device's that started first,
      // which this Send was refused for - and it is said as theirs.
      if (status === 409 && data && data.digest) ctx.adoptDigest(id, data.digest);
      ownedNow(id, st);
      sync();
      return;
    }
    if (status === 409 && data && data.digest) {
      ctx.adoptDigest(id, data.digest);
      st.phase = 'ask';
      if (signatureOf(data.digest) === st.sentSig) {
        // The same question, read again in the moment between the tap and
        // the server: what was chosen still answers it, and is kept. Any
        // other refusal of the same question is said as the server says it,
        // with Try again.
        if (data.digest.phase === 'pending') {
          st.note = 'The question is being read again. Your answer is kept: send it once it is ready.';
        } else if (data.digest.phase === 'error') {
          // Its last reading failed, and the server takes no answer to it
          // until it has been read again: sending the same answer again
          // would be refused the same way for ever. The problem box says
          // what failed, and its Try again reads the question again.
          st.note = 'The question has to be read again before it can be answered. Your answer is kept.';
        } else if (/question has changed/.test(String(data.error || ''))) {
          st.note = 'The question was read again. Your answer is kept: send it again.';
        } else {
          st.error = sentence(data.error) || 'The answer could not be given.';
          st.retry = 'send';
        }
      } else {
        // The question moved on between the tap and the server: the new one
        // is drawn, and nothing was answered. What was typed is kept - it is
        // the person's own words, and very likely still the answer - but the
        // options are the old question's.
        st.choices.clear();
        st.item = 0;
        st.signature = signatureOf(data.digest);
        st.note = 'The question has changed. This is the new one.';
      }
      draw(true);
      // The refusal may be the news that nothing is asking any more - the
      // question was answered at the machine while this one was on its way,
      // and the stream's word of it came while the answer was being sent,
      // when it could not be taken for gone. The walk is followed again now.
      sync();
      return;
    }
    if (status === 409 && data && data.run && data.run.run_id) {
      // An answer is already being given - from another device, or a tap
      // that went twice. That one is followed rather than fought, and one
      // this page did not send says whose it is: this answer was not given.
      takeOver(id, st, data.run);
      st.clash = st.foreign;
      ctx.adoptRun(id, data.run);
      sync();
      return;
    }
    if (status === 0) {
      // The request never got an answer, which is not the same as never
      // getting there: a reply lost on the way back - a signal that dropped,
      // a proxy that reset - is an answer that went in. A run the stream has
      // already shown starting is this one; otherwise the server is asked,
      // and "nothing was answered" is only said once it says so.
      if (st.phase !== 'sending') {
        ownedNow(id, st);
        startRunWatch();
        sync();
        return;
      }
      st.checking = true;
      startRunWatch();
      draw(true);
      checkSent(id, st);
      return;
    }
    st.phase = 'error';
    st.retry = 'send';
    st.error = (data && data.error) || 'The answer could not be given.';
    // No key to give it with: the same answer is refused the same way until
    // somebody adds one in the settings, so there is nothing to try again.
    if (isNoKey(st.error)) st.retry = null;
    // Marked done while the answer was on its way (here or on another
    // device): the server refused it, and its terminal is closed, so there
    // is nothing to try again either.
    if (filedDone(id) || /marked done/.test(String(st.error))) {
      st.retry = null;
      st.after = 'This session was marked done.';
    }
    // Deleted while the answer was on its way: there is nothing to try again.
    if (!ctx.sessionOf(id)) {
      st.retry = null;
      st.after = 'This session no longer exists.';
    }
    draw(true);
  }

  // checkSent asks the server whether an answer whose reply was lost started
  // a run: a run for this session that is not the one there was before the
  // tap is this answer going in, and is followed as if the reply had come.
  // While the server cannot be reached the question stays open - the run
  // watch asks again - and the answer is never called lost on a guess.
  async function checkSent(id, st) {
    if (st.asking) return;
    st.asking = true;
    const { status, data } = await call('GET', sessionPath(id, '/answer'), undefined, 8000);
    st.asking = false;
    if (closed || states.get(id) !== st || st.phase !== 'sending' || !st.checking) return;
    if (status === 0) {
      st.note = 'Socrates cannot be reached yet. It is asked again until it answers.';
      paintProgress();
      return;
    }
    st.checking = false;
    st.note = '';
    sendsHere.delete(id);
    const run = status === 200 && data ? data.run : null;
    if (run && run.run_id && run.run_id !== st.staleRunId) {
      startedHere.add(run.run_id);
      st.runId = run.run_id;
      st.phase = 'run';
      st.runSince = Date.now();
      ctx.adoptRun(id, run);
      startRunWatch();
      sync();
      return;
    }
    st.phase = 'error';
    st.retry = 'send';
    st.error = status === 200
      ? 'The answer did not reach Socrates. Nothing was answered.'
      : (data && data.error) || 'The answer could not be given.';
    if (!ctx.sessionOf(id)) {
      st.retry = null;
      st.after = 'This session no longer exists.';
    }
    draw(true);
  }

  // pressCancel is a tap on Cancel. A Cancel that appeared under the finger a
  // moment ago is the second tap of a double tap on Send, not a wish to stop
  // the answer that tap started, and is not taken. One for a run that is
  // already typing asks to be tapped again: a prompt left half answered is
  // worse than one answered.
  function pressCancel(id, button) {
    const st = stateOf(id);
    if (st.phase !== 'run' || st.lostAt) return;
    if (button.hidden || Date.now() - (st.cancelAt || 0) < CANCEL_ARM) return;
    if (st.typed && !(st.stopAsked && Date.now() - st.stopAsked < CANCEL_CONFIRM_FOR)) {
      st.stopAsked = Date.now();
      paintProgress();
      setTimeout(() => { if (!closed) paintProgress(); }, CANCEL_CONFIRM_FOR + 50);
      return;
    }
    st.stopAsked = 0;
    cancelRun(id);
  }

  async function cancelRun(id) {
    const st = stateOf(id);
    const { status, data } = await call('POST', sessionPath(id, '/answer/cancel'), {});
    if (closed || states.get(id) !== st) return;
    if (status >= 200 && status < 300) {
      if (data && data.cancelled) {
        st.note = 'Stopping…';
        paintProgress();
        return;
      }
      // Nothing was running to stop: the run is over, and how it ended is
      // asked for rather than waited for.
      st.note = '';
      const res = await call('GET', sessionPath(id, '/answer'), undefined, 8000);
      if (closed || states.get(id) !== st || st.phase !== 'run') return;
      if (res.status === 200 && res.data && res.data.run && res.data.run.run_id === st.runId) {
        ctx.adoptRun(id, res.data.run);
        followRun(id, st);
        if (st.phase === 'run') lostRun(id, st);
      } else if (res.status === 200) {
        lostRun(id, st);
      }
      sync();
      return;
    }
    st.note = status === 0 ? 'Could not reach Socrates to stop it.' : (data && data.error) || 'It could not be stopped.';
    paintProgress();
  }

  async function readAgain(id) {
    const st = stateOf(id);
    st.error = '';
    st.note = 'Reading the question again…';
    draw(true);
    const { status, data } = await call('POST', sessionPath(id, '/digest'), {});
    if (closed) return;
    if (status >= 200 && status < 300) {
      if (data && data.digest) ctx.adoptDigest(id, data.digest);
      st.note = '';
      draw(true);
      return;
    }
    st.note = '';
    st.error = status === 0 ? 'Could not reach Socrates.' : (data && data.error) || 'The question could not be read again.';
    st.retry = 'digest';
    draw(true);
  }

  /* --------------------------------------------------------- the reading */

  function sayable(digest, item) {
    const parts = [];
    if (digest && digest.context) parts.push(digest.context);
    if (item) {
      if (item.text) parts.push(item.text);
      // The detail - a plan approval's plan, in two sentences - is drawn
      // under the first item's question, and read where it is drawn.
      const question = digest && digest.question;
      if (question && question.detail && (question.items || [])[0] === item) parts.push(question.detail);
      (item.options || []).forEach((option, at) => {
        parts.push((at + 1) + ': ' + option.label + (option.description ? '. ' + option.description : '') + '.');
      });
    } else if (digest && digest.summary) parts.push(digest.summary);
    return parts.join('\n');
  }

  async function toggleReading(text, button) {
    if (speaking) { stopReading(); return; }
    clearVoiceError();
    const mine = ++sayGen;
    speaking = true;
    paintReading(button);
    try {
      await speak(text);
    } catch (err) {
      if (mine === sayGen) showVoiceError(err && err.message ? err.message : 'It could not be read out.');
    } finally {
      if (mine === sayGen) {
        speaking = false;
        paintReading(shell.querySelector('.so-q-read'));
      }
    }
  }

  function stopReading() {
    sayGen += 1;
    if (speaking) stopSpeaking();
    speaking = false;
    paintReading(shell.querySelector('.so-q-read'));
  }

  function paintReading(button) {
    if (!button || !button.isConnected) return;
    const words = speaking ? 'Stop reading' : 'Read aloud';
    button.setAttribute('aria-pressed', speaking ? 'true' : 'false');
    if (button.textContent !== words) button.textContent = words;
  }

  // A failure that belongs to no step - the voice, the microphone - is said
  // on the line the progress is said on. It is kept here rather than written
  // onto the line, so a redraw for some other session's news does not take
  // it away; it goes with the next tap on Read aloud, a move to another
  // question, or after a while.
  function showVoiceError(text) {
    voiceError = String(text || '');
    if (voiceTimer) clearTimeout(voiceTimer);
    voiceTimer = setTimeout(() => { voiceTimer = null; voiceError = ''; paintProgress(); }, VOICE_ERROR_FOR);
    paintProgress();
  }

  function clearVoiceError() {
    if (voiceTimer) { clearTimeout(voiceTimer); voiceTimer = null; }
    voiceError = '';
  }

  /* ------------------------------------------------------------ drawing */

  // shownOf is the digest the question on screen is drawn from. While an
  // answer is going in, and once it has or the question went, that is the
  // question as the person saw it: the digest's own question is cleared the
  // moment the session moves on, and the summary drawn in its place would be
  // a different screen for the second it takes to move on.
  function shownOf(st, digest) {
    const settling = st.phase === 'sending' || st.phase === 'run' || st.phase === 'done' || st.phase === 'gone'
      || st.phase === 'stopped';
    if (settling && st.asked) return st.asked;
    // A failed answer to a session that was deleted has no digest left: the
    // question it answered is what the error is about.
    if (st.phase === 'error' && !digest && st.asked) return st.asked;
    return digest;
  }

  // draw is the dialog drawn again for the session on screen. It is only done
  // when something it shows has changed, and only the part that has: a
  // counter that moves because another session started asking, or a session
  // that was renamed, changes the top of the dialog and nothing under the
  // person's fingers. A field of one's own is never rebuilt while it shows
  // the same question (graft), so what is typed or dictated into it, its
  // keyboard and its focus all stay where they are.
  function draw(force) {
    if (finished) return;
    const id = currentId();
    if (!id) { finish(); return; }
    const session = ctx.sessionOf(id);
    const digest = ctx.digestOf(id);
    const st = stateOf(id);
    // A question that was answered while another was on screen says so now,
    // and goes the same way.
    if (st.phase === 'done' || st.phase === 'gone') scheduleMove(id);
    // A moment with no question - no digest at all, as a server that has
    // just restarted has until it reads the screen again, or one being read -
    // is not a different question: what was chosen is only cleared by one.
    //
    // st.signature is the question the choices were made for, so it only
    // moves while they can be made (ask, error): a question that changes
    // while an answer is going in, or after the question went, is found to be
    // a different one when the dialog is back to answering - a failed run
    // that comes back to a new question with the same labels never offers
    // the old answer, pressed, to it (one more tap and it would be sent).
    const signature = signatureOf(digest);
    const choosing = st.phase === 'ask' || st.phase === 'error';
    if (signature && signature !== st.signature && (choosing || !st.signature)) {
      if (st.signature && choosing) {
        st.item = 0;
        st.choices.clear();
        st.texts.clear();
        if (st.phase === 'error' || st.error) {
          // What failed was the answer to the question before: this one is
          // new, and nothing has been tried for it.
          st.phase = 'ask';
          st.error = '';
          st.retry = null;
          st.after = '';
          st.runError = false;
          st.note = 'The question has changed. This is the new one.';
        }
      }
      st.signature = signature;
    }
    if (digest) st.rev = Number(digest.rev) || 0;
    if (digest && digest.question && (st.phase === 'ask' || st.phase === 'error')) st.asked = digest;
    const shown = shownOf(st, digest);
    const head = [index, queue.length].join('\u0000');
    const title = session ? session.title + '\u0000' + ctx.folderOf(session) : '';
    const bodyKey = [shown ? shown.rev : '', shown ? shown.phase : '', shown === digest ? '' : 'past',
      digest ? digest.phase : '', st.item, st.phase, st.error, st.note, st.retry || '', st.after,
      st.phase === 'check' ? String(ctx.reachable ? ctx.reachable() : true) : ''].join('\u0000');
    const same = !!drawn && drawn.id === id && !!shell.querySelector('.so-q-scroll');
    if (!force && same && drawn.head === head && drawn.title === title && drawn.body === bodyKey) {
      paintProgress();
      return;
    }
    const memo = focusMemo(id, st);
    if (same && drawn.head !== head) shell.querySelector('.so-q-top').replaceWith(top(id));
    else if (!same) shell.replaceChildren(top(id));
    if (same && drawn.title !== title && drawn.body === bodyKey && !force) patchTitle(session);
    if (!same || force || drawn.body !== bodyKey) drawBody(id, session, shown, st);
    drawn = { id, head, title, body: bodyKey };
    arm([id, st.item, st.signature].join('\u0000'));
    paintProgress();
    // The focus goes back to the thing that had it, or to the question
    // itself - never to an option it did not have, where the next space or
    // Enter would choose and send something the person never picked.
    if (memo || force) placeFocus(memo, force);
  }

  // drawBody draws what is under the top bar again. The field of one's own
  // that is on screen is kept when the new drawing has the same field for
  // the same question, and everything around it is replaced.
  function drawBody(id, session, digest, st) {
    const scroll = body(id, session, digest, st);
    const actions = footer(id, digest, st);
    const oldScroll = shell.querySelector('.so-q-scroll');
    const oldActions = shell.querySelector('.so-q-actions');
    const keep = oldScroll && oldScroll.querySelector('.so-q-own');
    const fresh = scroll.querySelector('.so-q-own');
    if (keep && fresh && ownOf.get(keep) === st && keep.dataset.id === fresh.dataset.id
      && keep.dataset.at === fresh.dataset.at && keep.dataset.sig === fresh.dataset.sig) {
      graft(keep, fresh, oldScroll, scroll);
    } else if (oldScroll) {
      oldScroll.replaceWith(scroll);
    } else {
      shell.append(scroll);
    }
    if (oldActions) oldActions.replaceWith(actions); else shell.append(actions);
  }

  // graft puts what was just drawn around `fresh` around `keep` instead,
  // level by level up to the scroll area, without taking `keep` out of the
  // page: an element taken out loses its focus, its on-screen keyboard and
  // the dictation that is writing into it.
  function graft(keep, fresh, oldRoot, newRoot) {
    let o = keep;
    let n = fresh;
    while (n !== newRoot && n.parentNode && o.parentNode && o !== oldRoot) {
      const op = o.parentNode;
      const np = n.parentNode;
      for (const child of [...op.childNodes]) if (child !== o) child.remove();
      const before = [];
      const after = [];
      let past = false;
      for (const child of [...np.childNodes]) {
        if (child === n) { past = true; continue; }
        (past ? after : before).push(child);
      }
      o.before(...before);
      o.after(...after);
      for (const name of op.getAttributeNames()) if (!np.hasAttribute(name)) op.removeAttribute(name);
      for (const name of np.getAttributeNames()) {
        if (op.getAttribute(name) !== np.getAttribute(name)) op.setAttribute(name, np.getAttribute(name));
      }
      o = op;
      n = np;
    }
  }

  function patchTitle(session) {
    const title = shell.querySelector('.so-q-session-title');
    if (title && session) title.textContent = session.title;
    const folder = shell.querySelector('.so-q-session-folder');
    if (folder && session) folder.textContent = ctx.folderOf(session);
  }

  // focusMemo says what has the focus, by what it is rather than by the
  // element, so the same thing can be given it back after a redraw: the
  // field with its cursor, the same option of the same question, the same
  // button.
  function focusMemo(id, st) {
    const active = document.activeElement;
    if (!active || !shell.contains(active) || active === shell) return null;
    const memo = { id, item: st.item, sig: st.signature, sel: '', start: null, end: null };
    if (active.classList.contains('so-q-own-field')) {
      memo.sel = '.so-q-own-field';
      memo.start = active.selectionStart;
      memo.end = active.selectionEnd;
    } else if (active.classList.contains('so-q-option')) {
      memo.sel = '.so-q-option[data-label="' + CSS.escape(active.dataset.label || '') + '"]';
    } else if (active.classList.contains('so-q-nav-btn')) {
      memo.sel = '.so-q-nav-btn[data-step="' + active.dataset.step + '"]';
    } else {
      const known = ['so-q-close', 'so-q-read', 'so-q-send', 'so-q-back', 'so-q-cancel', 'so-q-retry', 'so-q-open-error', 'so-q-open'];
      const cls = known.find((one) => active.classList.contains(one));
      memo.sel = cls ? '.' + cls : '';
    }
    return memo;
  }

  function placeFocus(memo, force) {
    const id = currentId();
    const st = states.get(id);
    const active = document.activeElement;
    // Still where it was: the redraw did not touch it.
    if (memo && active && shell.contains(active) && active !== shell && active.isConnected) return;
    let target = null;
    if (memo && memo.sel) {
      // What answers a question is only given the focus back for that same
      // question: the first option of the next one is not what was focused.
      const answers = memo.sel.startsWith('.so-q-option') || memo.sel === '.so-q-own-field';
      const sameQuestion = !!st && memo.id === id && memo.item === st.item && memo.sig === st.signature;
      if (!answers || sameQuestion) target = shell.querySelector(memo.sel);
      if (target && (target.disabled || target.hidden)) target = null;
    }
    if (!target && !force && !memo) return;
    if (!target) target = shell.querySelector('.so-q-text') || shell.querySelector('.so-q-session-title') || shell;
    target.focus({ preventScroll: true });
    if (target.classList && target.classList.contains('so-q-own-field') && memo && memo.start !== null) {
      try { target.setSelectionRange(memo.start, memo.end); } catch { /* a field that does not take one */ }
    }
  }

  function top(id) {
    const counter = queue.length > 1 ? (index + 1) + ' of ' + queue.length : '';
    // Named for what they move between: the questions of other sessions.
    // The steps of one prompt are moved through by the big button below.
    const prev = el('button', {
      class: 'ds-btn ds-btn--quiet ds-btn--icon so-q-nav-btn', type: 'button', 'data-tip': 'Previous question',
      'aria-label': 'Previous question', 'data-step': '-1', disabled: index === 0, onclick: () => go(-1),
    }, iconEl('chevronLeft'));
    const next = el('button', {
      class: 'ds-btn ds-btn--quiet ds-btn--icon so-q-nav-btn', type: 'button', 'data-tip': 'Next question',
      'aria-label': 'Next question', 'data-step': '1', disabled: index >= queue.length - 1, onclick: () => go(1),
    }, iconEl('chevronRight'));
    // Close is on the left, where a sheet's way out is on a phone: the top
    // right corner is where the keyboard button lands in touch mode.
    return el('div', { class: 'so-q-top' },
      el('button', {
        class: 'ds-btn ds-btn--quiet ds-btn--icon so-q-close', type: 'button', 'data-tip': 'Close', 'aria-label': 'Close',
        onclick: close,
      }, iconEl('close')),
      queue.length < 2
        ? el('span', { class: 'so-q-top-title', text: 'Question' })
        : el('div', { class: 'so-q-nav' },
          prev, el('span', { class: 'so-q-counter', text: counter, 'aria-live': 'polite' }), next));
  }

  function body(id, session, digest, st) {
    const scroll = el('div', { class: 'so-q-scroll' });
    scroll.append(sessionHead(id, session));
    const wrap = el('div', { class: 'so-q-body' });
    scroll.append(wrap);
    if (st.phase === 'check') {
      // Asked for by a link, and the page cannot yet tell whether it is
      // asking: it is looked for, or there is no signal to look with.
      const online = !ctx.reachable || ctx.reachable();
      scroll.insertBefore(el('div', { class: online ? 'ds-alert so-q-reading' : 'ds-alert ds-alert--warning so-q-reading', role: 'status' },
        online ? spinnerEl() : iconEl('wifiOff'),
        el('span', {
          class: 'ds-alert__text',
          text: online ? 'Looking for this question…' : 'No connection. The question shows up here as soon as there is signal.',
        })), wrap);
      scroll.append(el('div', { class: 'so-q-status', role: 'status', 'aria-live': 'polite', hidden: true }));
      return scroll;
    }
    const settling = st.phase === 'sending' || st.phase === 'run' || st.phase === 'done' || st.phase === 'gone'
      || st.phase === 'stopped';
    const past = settling && !!st.asked && digest === st.asked && ctx.digestOf(id) !== st.asked;
    // A session that no longer exists is not being read, whatever is left.
    const pending = !settling && !!ctx.sessionOf(id) && !readNow(id, digest);
    const question = digest && digest.question;
    // What is drawn but not to be answered yet is dimmed: a question being
    // read again, or the one an answer is going in for (past).
    if (past) wrap.dataset.state = 'past';
    else if (pending && !!digest && !!(digest.context || question)) wrap.dataset.state = 'pending';

    if (pending) {
      scroll.insertBefore(el('div', { class: 'ds-alert so-q-reading', role: 'status' },
        spinnerEl(),
        el('span', { class: 'ds-alert__text', text: 'Reading the question…' })), wrap);
    }
    const keyless = noKey(digest);
    if (digest && digest.phase === 'error' && st.phase === 'ask') {
      // Without a key, reading again fails the same way: the way out is the
      // settings, where the key is added, or the terminal itself.
      // The one filled button of the dialog meanwhile: Send is not offered.
      const fix = keyless
        ? el('a', { class: 'ds-btn ds-btn--primary so-q-big', href: '/admin#voiceCard', text: 'Add a key' })
        : el('button', { class: 'ds-btn ds-btn--primary so-q-big', type: 'button', text: 'Try again', onclick: () => readAgain(id) });
      scroll.insertBefore(el('div', { class: 'so-q-problem', role: 'alert' },
        el('div', { class: 'ds-alert ds-alert--error' },
          iconEl('alertCircle'),
          el('p', { class: 'ds-alert__text', text: digest.error || 'The question could not be read.' })),
        el('div', { class: 'so-q-problem-actions' },
          fix,
          el('button', { class: 'ds-btn so-q-big', type: 'button', text: 'Open session', onclick: () => openSession(id) }))), wrap);
    }
    if (digest && digest.context) {
      // On a short screen the context is two lines, and a tap opens the rest.
      wrap.append(el('p', {
        class: 'so-q-context', 'data-open': st.contextOpen, text: digest.context,
        onclick: (event) => { st.contextOpen = !st.contextOpen; event.currentTarget.toggleAttribute('data-open', st.contextOpen); },
      }));
    }

    const items = question ? (question.items || []) : [];
    if (question && items.length) {
      const at = Math.min(st.item, items.length - 1);
      const item = items[at];
      if (items.length > 1) {
        wrap.append(el('div', { class: 'so-q-step' },
          el('span', { text: 'Question ' + (at + 1) + ' of ' + items.length }),
          item.header ? el('span', { class: 'so-q-chip', text: item.header }) : null));
      } else if (item.header) {
        wrap.append(el('div', { class: 'so-q-step' }, el('span', { class: 'so-q-chip', text: item.header })));
      }
      if (item.text) wrap.append(el('h2', { class: 'so-q-text', tabindex: '-1', text: item.text }));
      if (at === 0 && question.detail) wrap.append(el('p', { class: 'so-q-detail', text: question.detail }));
      wrap.append(options(id, item, at, st, question));
      if (item.free_text || question.kind === 'text' || !(item.options || []).length) {
        wrap.append(ownAnswer(id, st, at, 'Your own answer', signatureOf(digest)));
      }
    } else if (digest && !pending && !keyless && !(st.phase === 'gone' || st.phase === 'done' || st.phase === 'stopped')
      && waitingNow(session)) {
      // Waiting, and nothing the model could make into a question: what the
      // session is doing, and a field whose words are taken to whatever the
      // terminal is asking. Only a session that is waiting gets the field: one
      // at its prompt with nothing asked would take the words as a new task.
      // Nor one with no key: the operator that would type them cannot run.
      if (digest.summary) wrap.append(el('h2', { class: 'so-q-text', tabindex: '-1', text: digest.summary }));
      wrap.append(el('p', { class: 'so-q-detail', text: 'Say what to answer, and it is given to whatever the terminal is asking.' }));
      wrap.append(ownAnswer(id, st, -1, 'Your answer', ''));
    } else if (digest && digest.summary && !digest.context) {
      // A question that is no longer there: what the session is doing, as a
      // line and not as a question with a field to answer it in.
      wrap.append(el('p', { class: 'so-q-context', text: digest.summary }));
    }
    if (st.note && st.phase === 'ask') {
      wrap.prepend(el('div', { class: 'ds-alert ds-alert--warning so-q-note' },
        iconEl('info'), el('span', { class: 'ds-alert__text', text: st.note })));
    }
    scroll.append(el('div', { class: 'so-q-status', role: 'status', 'aria-live': 'polite', hidden: true }));
    return scroll;
  }

  function sessionHead(id, session) {
    const title = session ? session.title : 'This session';
    const folder = session ? ctx.folderOf(session) : '';
    return el('div', { class: 'so-q-session' },
      agentMark(session ? session.harness : '', 32),
      el('span', { class: 'so-q-session-text' },
        el('span', { class: 'so-q-session-title so-ellipsis', tabindex: '-1', text: title }),
        el('span', { class: 'so-q-session-folder so-ellipsis', text: folder, hidden: !folder })),
      el('button', { class: 'ds-btn so-q-open', type: 'button', text: 'Open session', onclick: () => openSession(id) }));
  }

  // The options are buttons that say whether they are chosen (aria-pressed):
  // a group of toggle buttons, which is what they are - a tap chooses, a
  // second tap sends - rather than radio buttons, which arrows would move
  // between and a second press would not send.
  function options(id, item, at, st, question) {
    const list = el('div', { class: 'so-q-options', role: 'group', 'aria-label': item.text || 'Options' });
    const locked = st.phase !== 'ask' && st.phase !== 'error';
    (item.options || []).forEach((option, n) => {
      const chosen = (st.choices.get(at) || []).includes(option.label) && !(st.texts.get(at) || '').trim();
      // A DS interactive card as a toggle: its state layers, ripple and
      // press morph, with the choice said by the key (a filled number, or a
      // ticked box for a multi-select) and a ring (so-question.css).
      const button = el('button', {
        class: 'ds-card ds-card--interactive so-q-option',
        type: 'button',
        'aria-pressed': chosen ? 'true' : 'false',
        'data-label': option.label,
        'data-multi': !!item.multi,
        disabled: locked,
        onclick: () => choose(id, at, item, option.label, question),
      },
      el('span', { class: 'so-q-option-row' },
        el('span', { class: 'so-q-option-ring', 'aria-hidden': 'true' }),
        el('span', { class: 'so-q-option-key', 'aria-hidden': 'true' },
          item.multi ? iconEl('check', 20) : String(n + 1)),
        el('span', { class: 'so-q-option-text' },
          el('span', { class: 'so-q-option-label', text: option.label }),
          option.description ? el('span', { class: 'so-q-option-desc', text: option.description }) : null,
          option.recommended
            ? el('span', { class: 'ds-status ds-status--ok so-q-recommended' }, iconEl('thumbUp', 16), el('span', { text: 'Recommended' }))
            : null)));
      list.append(button);
    });
    return list;
  }

  // choose is a tap on an option. A single choice is chosen by the first tap
  // and sent by the second, so the one thing the person has to do after
  // picking is to pick it again - or press Send, which is the same thing.
  function choose(id, at, item, label, question) {
    const st = stateOf(id);
    if (st.phase !== 'ask' && st.phase !== 'error') return;
    const had = st.choices.get(at) || [];
    const typed = (st.texts.get(at) || '').trim();
    // An option tapped after typing an answer of one's own is the answer now.
    if (typed) {
      st.texts.set(at, '');
      const field = shell.querySelector('.so-q-own textarea');
      if (field) field.value = '';
    }
    if (item.multi) {
      st.choices.set(at, had.includes(label) ? had.filter((one) => one !== label) : [...had, label]);
    } else {
      if (had.length === 1 && had[0] === label && !typed) { advance(id, question); return; }
      st.choices.set(at, [label]);
    }
    paintChoices(st, at);
  }

  function paintChoices(st, at) {
    const chosen = (st.texts.get(at) || '').trim() ? [] : (st.choices.get(at) || []);
    for (const button of shell.querySelectorAll('.so-q-option')) {
      button.setAttribute('aria-pressed', chosen.includes(button.dataset.label) ? 'true' : 'false');
    }
    paintSend();
  }

  // advance is Send for one item: the next item of a prompt that asks
  // several, or the answer itself after the last. It is the one way an
  // answer is sent - Send, a second tap, Enter in the field - so the one rule
  // about when that is allowed lives here (usable).
  function advance(id, question) {
    const st = stateOf(id);
    const items = (question && question.items) || [];
    if (!usable(id, ctx.digestOf(id)) || !answeredItem(st, st.item, question)) return;
    if (st.item < items.length - 1) {
      st.item += 1;
      draw(true);
      return;
    }
    // From an error it is the failed answer sent again, to the question it
    // was given to (see send).
    send(id, st.phase === 'error' ? againRev(st, ctx.digestOf(id)) : -1);
  }

  // usable says whether a digest can be answered: there is one, it is not
  // being read again, and the server takes an answer to it (answerable). A
  // digest whose first reading failed can still be answered in words, which
  // is what its field is for; one that failed while it held a question is
  // read again first, and with no key nothing can be answered at all.
  function usable(id, digest) {
    return answerable(digest) && !answeredRead(id, digest);
  }

  // answeredRead is a digest that is the reading of a question already
  // answered here, held by a session that has started asking again since
  // (joinable): the follow-up has not been read yet, and an answer given to
  // what is on screen would be the one before's again. It is "Reading the
  // question…" until the digest moves past it.
  function answeredRead(id, digest) {
    const was = answered.get(id);
    return !!was && !!digest && Number(digest.rev) <= was.rev;
  }

  // readNow is isRead for the question a session is asking now.
  function readNow(id, digest) {
    return isRead(digest) && !answeredRead(id, digest);
  }

  function answeredItem(st, at, question) {
    if (!question) return !!st.own.trim();
    return !!(st.texts.get(at) || '').trim() || (st.choices.get(at) || []).length > 0;
  }

  // ownAnswer is the field for an answer the options do not have. `at` is
  // the item it answers, or -1 for the answer to a screen with no question.
  function ownAnswer(id, st, at, label, sig) {
    const fieldId = 'so-q-own-' + (++ownSeq);
    const field = el('textarea', {
      class: 'ds-input so-q-own-field', id: fieldId, rows: '2', placeholder: 'Type or dictate an answer…',
      value: at < 0 ? st.own : (st.texts.get(at) || ''),
      disabled: st.phase !== 'ask' && st.phase !== 'error',
    });
    field.addEventListener('input', () => {
      if (at < 0) st.own = field.value;
      else {
        st.texts.set(at, field.value);
        // Typing replaces whatever option was chosen for this item, and
        // clearing the field again brings the choice back.
        if (field.isConnected) paintChoices(st, at);
      }
      if (field.isConnected) paintSend();
    });
    // Enter sends, as it does in every one-line field of this app; a new
    // line is Shift-Enter, for the rare answer that needs one.
    field.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return;
      event.preventDefault();
      const digest = ctx.digestOf(id);
      advance(id, digest && digest.question);
    });
    const own = el('div', { class: 'ds-field so-q-own', 'data-id': id, 'data-at': String(at), 'data-sig': sig },
      el('label', { class: 'ds-field__label', for: fieldId, text: label }),
      dictated(field, field, (text) => {
        field.value = field.value.trim() ? field.value.trimEnd() + ' ' + text : text;
        field.dispatchEvent(new Event('input', { bubbles: true }));
      }, { onError: (sentence) => showVoiceError(sentence) }));
    ownOf.set(own, st);
    return own;
  }

  function footer(id, digest, st) {
    const question = digest && digest.question;
    const items = (question && question.items) || [];
    const at = Math.min(st.item, Math.max(0, items.length - 1));
    const item = items[at] || null;
    // A toggle (aria-pressed), and a text button: DS has no speaker glyph
    // (GLYPH in ui.js).
    const read = el('button', {
      class: 'ds-btn so-q-big so-q-read', type: 'button', 'aria-pressed': speaking ? 'true' : 'false',
      text: speaking ? 'Stop reading' : 'Read aloud',
      onclick: (event) => toggleReading(sayable(digest, item), event.currentTarget),
      disabled: !digest,
    });
    const back = items.length > 1 && st.item > 0 && (st.phase === 'ask' || st.phase === 'error')
      ? el('button', { class: 'ds-btn so-q-big so-q-back', type: 'button', text: 'Back', onclick: () => { st.item -= 1; draw(true); } })
      : null;
    const last = !question || st.item >= items.length - 1;
    // "Continue", not "Next question": the arrows at the top are the next
    // question - another session's - and this is the next step of this one.
    const sendBtn = el('button', {
      class: 'ds-btn ds-btn--primary so-q-big so-q-send', type: 'button', text: last ? 'Send' : 'Continue',
      'aria-label': last ? 'Send' : 'Continue to question ' + (at + 2) + ' of ' + items.length,
      onclick: () => advance(id, question),
    });
    const cancel = el('button', {
      class: 'ds-btn so-q-big so-q-cancel', type: 'button', text: 'Cancel', hidden: true,
      onclick: (event) => pressCancel(id, event.currentTarget),
    });
    const retry = el('button', {
      class: 'ds-btn ds-btn--primary so-q-big so-q-retry', type: 'button', text: 'Try again', hidden: true,
      onclick: () => {
        if (st.retry === 'digest') { readAgain(id); return; }
        const again = st.phase === 'error' ? againRev(st, ctx.digestOf(id)) : -1;
        st.phase = 'ask';
        st.error = '';
        send(id, again);
        // A send that did not go - the question is being read again - still
        // takes the failure off the screen.
        if (st.phase === 'ask') draw(true);
      },
    });
    const openIt = el('button', {
      class: 'ds-btn so-q-big so-q-open-error', type: 'button', text: 'Open session', hidden: true,
      onclick: () => openSession(id),
    });
    return el('div', { class: 'so-q-actions' }, read, back, cancel, openIt, retry, sendBtn);
  }

  // paintSend keeps Send honest: it can only be pressed for an item that has
  // an answer, and only while nothing is being sent.
  function paintSend() {
    const sendBtn = shell.querySelector('.so-q-send');
    if (!sendBtn) return;
    const id = currentId();
    const st = stateOf(id);
    const digest = ctx.digestOf(id);
    const question = digest && digest.question;
    sendBtn.disabled = !usable(id, digest) || !answeredItem(st, st.item, question)
      || (st.phase !== 'ask' && st.phase !== 'error');
  }

  // paintProgress is the line that says where the answer stands, and the
  // buttons that go with each stage. It is drawn in place, because a run
  // moves on several times a second and the rest of the dialog does not.
  function paintProgress() {
    const id = currentId();
    if (!id || finished || closed) return;
    const st = stateOf(id);
    paintConnection(st);
    const line = shell.querySelector('.so-q-status');
    const sendBtn = shell.querySelector('.so-q-send');
    const cancel = shell.querySelector('.so-q-cancel');
    const retry = shell.querySelector('.so-q-retry');
    const openIt = shell.querySelector('.so-q-open-error');
    if (!line || !sendBtn) return;
    if (st.phase === 'run' || st.phase === 'done') ownedNow(id, st);
    const run = ctx.answerOf(id);
    const ours = run && (st.runId ? run.run_id === st.runId : run.run_id !== st.staleRunId);
    let words = '';
    let detail = '';
    let kind = '';
    const confirming = st.phase === 'run' && !!st.stopAsked && Date.now() - st.stopAsked < CANCEL_CONFIRM_FOR;
    switch (st.phase) {
      case 'sending':
        words = st.checking ? 'Checking whether the answer went in…' : 'Sending your answer…';
        detail = st.note;
        kind = 'working';
        break;
      case 'run': {
        kind = 'working';
        const answer = ours && run.answer ? run.answer : '';
        words = answer ? 'Choosing ' + answer + '…' : 'Answering…';
        // Someone else's answer is said as theirs: what this person chose
        // here was not given.
        if (st.foreign) {
          words = 'Another device is ' + (st.clash ? 'already ' : '') + 'answering' + (answer ? ': ' + answer : '…');
        }
        // A run seen before the reply to this Send has said whose it is: the
        // answer is still on its way, and the run may be another device's.
        if (st.provisional) words = 'Sending your answer…';
        if (st.lostAt) words = 'Checking whether the answer went in…';
        if (ours && run.phase === 'acting') st.typed = true;
        if (st.provisional) detail = '';
        else if (ours && run.phase === 'acting' && run.action) detail = sentence(run.action);
        else if (ours && run.phase === 'thinking') detail = 'Looking at the screen';
        if (st.note) detail = st.note;
        if (confirming) detail = 'Keys are already being typed: stopping now can leave the prompt half answered. Tap Stop to stop anyway.';
        break;
      }
      case 'done':
        kind = 'done';
        words = st.foreign ? 'Answered on another device' : 'Answered';
        detail = st.note;
        break;
      case 'stopped':
        kind = 'error';
        words = st.note;
        break;
      case 'error':
        kind = 'error';
        words = st.error;
        detail = st.after;
        break;
      case 'gone':
        kind = 'done';
        words = st.note;
        break;
      default:
        if (st.error) { kind = 'error'; words = st.error; }
        break;
    }
    // The voice's own failure, when nothing about the answer is being said.
    if (!words && voiceError && st.phase !== 'check') { kind = 'error'; words = voiceError; }
    // A DS alert in the tone of the stage, drawn again only when what it
    // says has changed: a run moving on repaints this several times a
    // second, and a spinner made again each time would never turn.
    const said = [kind, words, detail].join('\u0000');
    if (line.dataset.said !== said) {
      line.dataset.said = said;
      line.dataset.kind = kind;
      line.className = 'ds-alert so-q-status' + (STATUS_TONE[kind] ? ' ' + STATUS_TONE[kind] : '');
      line.replaceChildren();
      if (words) {
        line.append(kind === 'working' ? spinnerEl() : iconEl(kind === 'done' ? 'checkCircle' : kind === 'error' ? 'alertCircle' : 'info'),
          el('span', { class: 'ds-alert__text' },
            el('span', { class: 'so-q-status-words', text: words }),
            detail ? el('span', { class: 'so-q-status-detail', text: detail }) : null));
      }
    }
    line.hidden = !words;
    const busy = st.phase === 'sending' || st.phase === 'run';
    const over = st.phase === 'done' || st.phase === 'gone' || st.phase === 'check' || st.phase === 'stopped';
    const failed = st.phase === 'error' || st.phase === 'stopped' || (st.phase === 'ask' && !!st.error);
    // A Cancel that appears, or turns into Stop, takes no tap for a moment:
    // see pressCancel.
    // It is the state's moment, not the button's: a redraw that builds the
    // footer again in the middle of a run does not make Cancel deaf again.
    // Nor is there a Cancel for a run whose reply has not said whose it is:
    // it would stop another device's answer.
    const showCancel = st.phase === 'run' && !st.lostAt && !st.provisional;
    const cancelWords = confirming ? 'Stop' : 'Cancel';
    if (!showCancel) st.cancelAt = 0;
    else if (!st.cancelAt || st.cancelWords !== cancelWords) st.cancelAt = Date.now();
    st.cancelWords = showCancel ? cancelWords : '';
    // Stop is said in words, never in red: there are no red buttons.
    if (cancel.textContent !== cancelWords) cancel.textContent = cancelWords;
    cancel.hidden = !showCancel;
    retry.hidden = !failed || !st.retry;
    openIt.hidden = !failed || !ctx.sessionOf(id) || filedDone(id);
    // With no key nothing can be sent at all: the problem box says so, and
    // offers the way to the key instead.
    sendBtn.hidden = busy || over || st.phase === 'error' || !retry.hidden || noKey(ctx.digestOf(id));
    for (const option of shell.querySelectorAll('.so-q-option')) option.disabled = busy || over;
    const field = shell.querySelector('.so-q-own-field');
    if (field) field.disabled = busy || over;
    if (!sendBtn.hidden) paintSend();
    if (kind) line.scrollIntoView({ block: 'nearest' });
  }

  // paintConnection says, under the top bar, when what the dialog shows may
  // not be current - the stream is down, coming back or being checked - in
  // Home's words (Home's own line is behind the dialog), and dims the
  // question: a question answered at the machine meanwhile would otherwise
  // look as live as one that is still waiting. A question asked for by a
  // link says it in its own words (check), and one that is settling is
  // about an answer already given. While it is shown it is said again every
  // CONN_EVERY, so the age in it keeps counting.
  function paintConnection(st) {
    const conn = ctx.connection ? ctx.connection() : { kind: 'live', words: '' };
    const old = (conn.kind === 'connecting' || conn.kind === 'offline') && st.phase !== 'check';
    let line = shell.querySelector('.so-q-conn');
    if (old && !line) {
      const head = shell.querySelector('.so-q-top');
      if (head) {
        // Home's connection line as net.js draws the page's own: a DS alert,
        // built once, whose words change.
        line = el('div', { class: 'ds-alert ds-alert--warning so-q-conn', role: 'status' },
          iconEl('wifiOff'),
          el('span', { class: 'ds-alert__text' },
            el('span', { class: 'so-q-conn-words' }),
            el('span', { class: 'so-q-conn-detail' })));
        head.after(line);
      }
    }
    if (line) {
      line.hidden = !old;
      line.dataset.status = conn.kind;
      line.classList.toggle('ds-alert--warning', conn.kind !== 'offline');
      line.classList.toggle('ds-alert--error', conn.kind === 'offline');
      const words = line.querySelector('.so-q-conn-words');
      if (words.textContent !== conn.words) words.textContent = conn.words;
      const detail = st.phase === 'ask' || st.phase === 'error'
        ? 'This question may already have been answered.' : '';
      const said = line.querySelector('.so-q-conn-detail');
      if (said.textContent !== detail) said.textContent = detail;
    }
    shell.toggleAttribute('data-stale', old);
    if (old && !connTimer) connTimer = setInterval(sayConnection, CONN_EVERY);
    else if (!old) stopConnTimer();
  }

  function sayConnection() {
    const id = currentId();
    if (closed || finished || !id) { stopConnTimer(); return; }
    paintConnection(stateOf(id));
  }

  function stopConnTimer() {
    if (connTimer) { clearInterval(connTimer); connTimer = null; }
  }

  function sentence(text) {
    const words = String(text || '').trim();
    return words ? words[0].toUpperCase() + words.slice(1) : '';
  }

  /* ----------------------------------------------------------- the dialog */

  // arm makes the buttons that answer or leave the question take no tap for
  // TAP_ARM when what they answer is new: the dialog has just opened, or
  // another question or step of one is drawn. A redraw of the same one - a
  // run moving on, a digest read again to the same question - is not new,
  // and is not armed again. The class takes the tap away in the CSS
  // (pointer-events), so a tap that lands then reaches nothing; the capture
  // below is for a click the CSS did not stop.
  function arm(key) {
    if (key === armedFor) return;
    armedFor = key;
    if (armTimer) clearTimeout(armTimer);
    shell.setAttribute('data-arming', '');
    armTimer = setTimeout(() => { armTimer = null; shell.removeAttribute('data-arming'); }, TAP_ARM);
  }

  function openSession(id) {
    close();
    ctx.select(id);
  }

  function close() {
    if (closed) return;
    closed = true;
    open = null;
    if (closeTimer) { clearTimeout(closeTimer); closeTimer = null; }
    if (armTimer) { clearTimeout(armTimer); armTimer = null; }
    for (const timer of moves.values()) clearTimeout(timer);
    moves.clear();
    stopRunWatch();
    stopConnTimer();
    stopReading();
    clearVoiceError();
    // DS plays the exit and gives the focus back to the opener at once; the
    // dialog leaves the page once the exit has played. An opener that is
    // gone by now - rebuilt meanwhile - leaves the focus nowhere, and the
    // page is asked where it should go.
    hideOverlay(dialog, { remove: true });
    const now = document.activeElement;
    if (!now || now === document.body || dialog.contains(now)) restoreFocus(was);
  }

  // The digits answer a single choice from a keyboard, the way the terminal
  // itself takes them - one press picks, a second sends.
  dialog.addEventListener('keydown', (event) => {
    if (event.target && event.target.closest && event.target.closest('textarea, input')) return;
    if (event.altKey || event.ctrlKey || event.metaKey || !/^[1-9]$/.test(event.key)) return;
    const option = shell.querySelectorAll('.so-q-option')[Number(event.key) - 1];
    if (!option || option.disabled) return;
    event.preventDefault();
    option.click();
  });
  // A click with a pointer behind it (detail > 0: a key's click has none)
  // on a button that is still arming is a stray tap, and goes nowhere.
  shell.addEventListener('click', (event) => {
    if (!event.detail || !shell.hasAttribute('data-arming')) return;
    if (!event.target || !event.target.closest || !event.target.closest(ARMED)) return;
    event.preventDefault();
    event.stopPropagation();
  }, true);
  // Escape, or the dialog closed natively: DS starts the exit, and this
  // closes the walk with it.
  dialog.addEventListener('close', () => { if (!closed) close(); });

  start();
  if (!queue.length) finish(); else draw(true);
  // The first focus is the question itself, as placeFocus would give it,
  // and not the first button DS would otherwise pick (Close).
  const first = shell.querySelector('.so-q-text') || shell.querySelector('.so-q-session-title');
  if (first) first.setAttribute('autofocus', '');
  const opener = was && was !== document.body && was.isConnected ? was : null;
  showOverlay(dialog, opener);

  return { sync, focus, close };
}
