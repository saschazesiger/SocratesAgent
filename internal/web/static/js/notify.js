// The two things that reach somebody who is not looking at the page: a chime
// and a notification, when a session stops working.
//
// The sidebar already says what every session is doing, but a phone in a
// pocket is not reading the sidebar. There is exactly one moment worth
// interrupting a person for - a harness that finished its turn, or one that
// has started needing an answer - and that moment is the same edge §A.4 marks
// unread on. It fires for every session, attached or not: the whole point of
// it is the session nobody is watching.
//
// Both switches are per device and per browser, because that is what they
// are: a speaker this phone has, and a permission this browser granted this
// origin. Neither is a setting on the server, so neither is in the settings
// document. They are in the options menu of the top bar, with the other
// choices that belong to this device rather than to a session.
//
// This module knows nothing about the session page. It is handed a completion
// - which session, what it went to, what it came from - and the two ways to
// say so.

import { api, toast, errorMessage } from './api.js';
import { outcomeNews } from './outcome.js';

const SOUND_KEY = 'socrates.sound';
const NOTIFY_KEY = 'socrates.notify';

// Two notes, up. The whole of it is under a third of a second: long enough to
// be heard over a car, short enough that three sessions finishing together do
// not become a melody.
const TONE_ONE = 880;
const TONE_TWO = 1175;
const PEAK = 0.18;

// One chime at a time, whatever the machine is doing. Six sessions that all
// finish inside a second are one piece of news.
const CHIME_EVERY = 1500;

// How long a session that was just opened or just came up says nothing. A
// harness that is starting sets its status once and then changes it again
// within moments, and that flurry is the program finding its feet, not a
// turn that finished.
const SETTLE = 5000;

// What a notification says under the session's name. The words are the
// sidebar's own, so the phone and the page agree about what happened, and
// a permission prompt is said as one, as the server's push says it.
const BODY = { idle: 'Finished', waiting: 'Needs an answer' };
const PERMISSION_BODY = 'Needs your permission';

// The worker's own icon and status-bar silhouette (sw.js), so a notification
// the page shows looks like one the worker shows.
const NOTE_ICON = '/static/img/icon-192.png';
const NOTE_BADGE = '/static/img/badge-96.png';

// iOS and iPadOS - which calls itself a Mac, and is told apart by its touch
// screen. Safari there has notifications only inside an app added to the
// Home Screen, so the options menu says how to get them rather than that
// they do not exist.
const IOS = /iPad|iPhone|iPod/.test(navigator.userAgent || '')
  || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

function standalone() {
  if (navigator.standalone === true) return true;
  try { return !!window.matchMedia && window.matchMedia('(display-mode: standalone)').matches; } catch { return false; }
}

// targetOf is where a tap on a piece of news goes, by the same rule as the
// worker's (sw.js): it rides along in the notification's data, so a
// notification the page shows is handled by the worker's click as well.
function targetOf(id, kind) {
  if (!id) return '/';
  if (kind === 'question') return '/#q=' + encodeURIComponent(id);
  if (kind === 'finished' || kind === 'started') return '/#open=' + encodeURIComponent(id);
  return '/';
}

// A tap on a notification reaches the page as a message from the worker, and
// on a phone the page it reaches may be the one the tap has just woken or
// launched: the message can be on its way before boot() has awaited the
// fonts and mounted anything. So the listener is attached the moment this
// module is evaluated, and what arrives before mountNotify is kept, in
// order, until it hands the messages to the page.
//
// The worker also says when a push has just written the app's icon and the
// tray ({t:'pushed'}), with the asking set as it was counted when the push
// went out - which a page that is open may know better. One that arrives
// before the mount is not kept: the page's first state writes both anyway.
const early = [];
let onOpen = null;
let onPushed = null;
if ('serviceWorker' in navigator) {
  try {
    navigator.serviceWorker.addEventListener('message', (event) => {
      const msg = event.data;
      if (msg && msg.t === 'pushed') {
        if (onPushed) onPushed();
        return;
      }
      if (!msg || msg.t !== 'open' || !msg.id) return;
      const kind = typeof msg.kind === 'string' ? msg.kind : '';
      if (onOpen) onOpen(String(msg.id), kind);
      else early.push({ id: String(msg.id), kind });
    });
    // A page's messages from its worker are held until it says it is
    // listening; DOMContentLoaded says so too, but later than this.
    if (typeof navigator.serviceWorker.startMessages === 'function') navigator.serviceWorker.startMessages();
  } catch { /* a browser whose worker cannot talk to the page: the address carries it instead */ }
}

function stored(key, fallback) {
  try {
    const raw = localStorage.getItem(key);
    if (raw === 'on') return true;
    if (raw === 'off') return false;
  } catch { /* a private window remembers nothing, and that is allowed */ }
  return fallback;
}

function remember(key, on) {
  try { localStorage.setItem(key, on ? 'on' : 'off'); } catch { /* nothing to do about it */ }
}

/**
 * mountNotify sets up the chime and the notification and hands back what the
 * page calls into: the completion, the questions for the app's badge, and
 * the two switches the options menu draws.
 *
 * `ctx` is everything this module is not allowed to own:
 *   sessionOf    a session by id, or null - the name a notification is titled with
 *   select       open a session, for a notification that is clicked
 *   question     open the question a session is asking, for a "Needs an
 *                answer" that is clicked - Home, and the dialog over it
 *   asksInProse  whether a session's turn ended on a question in prose
 *   pushed       told when a push has just written the app's icon and the
 *                tray, so that a page whose state is newer writes them again
 *   outcome      the verdict a session's last turn has, or is waiting for
 *   questionOf   the question a session's digest holds, or null
 *   chatNews     what a chat's notification says (its reading's news): a
 *                string for a chat, '' for news that is none, and
 *                undefined for a terminal
 *   changed      told whenever either switch moves, so a menu that is open
 *                shows where it now is
 */
export function mountNotify(ctx) {
  const changed = () => { try { ctx.changed?.(); } catch { /* a menu that went away */ } };

  // iOS Safari has no Notification outside a home-screen app, so the button
  // says so rather than failing when it is pressed.
  const available = typeof window.Notification === 'function';
  // Safari on an iPhone, not opened from the Home Screen: notifications are
  // there to be had, by adding the app first.
  const addToHome = IOS && !available && !standalone();

  let sound = stored(SOUND_KEY, true);
  // Off by default: it is the one switch that cannot be honoured without
  // asking the browser first, and asking unprompted is what a page does that
  // is about to be blocked for ever.
  let notify = available && stored(NOTIFY_KEY, false);
  // A permission that was revoked between two visits leaves the switch
  // claiming something it cannot do, so it is turned off where that is found
  // out rather than silently ignored on every completion.
  if (notify && window.Notification.permission !== 'granted') {
    notify = false;
    remember(NOTIFY_KEY, false);
  }

  let audio = null;
  let lastChime = 0;
  // Which commit of each session has already been rung for, by its `since`.
  //
  // A change reaches the page more than once by honest routes - the terminal
  // socket's frame, the event stream's frame, and the activity a list (a
  // hello, a fallback poll, a fetch) carries - and a list that was read just
  // before the turn ended answers with the state before it. That reads as
  // idle -> busy -> idle, and the second edge is the same commit as the
  // first. One commit is one piece of news.
  const rung = new Map();
  // Until when each session is still settling, by id: see hush.
  const quiet = new Map();

  /* ------------------------------------------------------------ the chime */

  // A browser will not let a page make a sound until somebody has touched it.
  // The context is therefore made the first time one is wanted and woken by
  // the first gesture the page sees; every step of it fails silently, because
  // a chime that did not happen is not worth a sentence on the screen.
  function context() {
    if (audio) return audio;
    const Ctx = window.AudioContext || window.webkitAudioContext;
    if (!Ctx) return null;
    try { audio = new Ctx(); } catch { return null; }
    return audio;
  }

  function wake() {
    const ctxAudio = context();
    if (!ctxAudio) return null;
    if (ctxAudio.state === 'suspended' && ctxAudio.resume) {
      try {
        const resumed = ctxAudio.resume();
        if (resumed && resumed.catch) resumed.catch(() => { /* still asleep */ });
      } catch { /* the same answer, thrown instead of rejected */ }
    }
    return ctxAudio;
  }

  function chime() {
    const a = wake();
    if (!a) return;
    try {
      const t = a.currentTime;
      const osc = a.createOscillator();
      const gain = a.createGain();
      osc.type = 'sine';
      // One oscillator and two notes: a second oscillator would be a second
      // voice, and this is one chime.
      osc.frequency.setValueAtTime(TONE_ONE, t);
      osc.frequency.setValueAtTime(TONE_TWO, t + 0.12);
      // In and out on a ramp, with a dip between the notes so the second one
      // is heard as a second note rather than as a slide. A square edge on a
      // sine is a click, which is the sound of a broken speaker.
      gain.gain.setValueAtTime(0, t);
      gain.gain.linearRampToValueAtTime(PEAK, t + 0.015);
      gain.gain.setValueAtTime(PEAK, t + 0.1);
      gain.gain.linearRampToValueAtTime(PEAK / 4, t + 0.12);
      gain.gain.linearRampToValueAtTime(PEAK, t + 0.14);
      gain.gain.setValueAtTime(PEAK, t + 0.21);
      gain.gain.linearRampToValueAtTime(0, t + 0.25);
      osc.connect(gain);
      gain.connect(a.destination);
      osc.start(t);
      osc.stop(t + 0.26);
      osc.onended = () => {
        try { osc.disconnect(); gain.disconnect(); } catch { /* already gone */ }
      };
    } catch { /* silent, by design */ }
  }

  // The first touch of the page is the gesture the browser was waiting for. It
  // only matters for a context that was made before one arrived - a page that
  // has been used already gets a running context the moment it asks for one.
  const gesture = () => { if (audio) wake(); };
  addEventListener('pointerdown', gesture, { once: true, capture: true });
  addEventListener('keydown', gesture, { once: true, capture: true });

  /* ----------------------------------------------------- the notification */

  // show is the notification of a page that is open and has no push
  // subscription. It is shown through the worker's registration wherever
  // there is one, in the very shape the worker shows a push in - the same
  // tag, the same data - so that a tap on it is handled by the worker's
  // click like any other, and goes through the same door into the page.
  // That is also the only way there is on iOS and on Android, which have no
  // `new Notification` at all; the constructor is left for a browser with no
  // worker running.
  async function show(id, next, session, news = null) {
    if (!notify || !available) return;
    // A device the server pushes to is told by its worker, page or no page.
    if (pushing) return;
    if (window.Notification.permission !== 'granted') return;
    let body = news ? news.body : BODY[next.state];
    if (!body) return;
    if (!news && next.state === 'waiting' && next.note === 'permission prompt') body = PERMISSION_BODY;
    // A chat says what its journal says, as its push does: its question, or
    // how its turn ended with a line of the answer.
    if (!news && ctx.chatNews) {
      const said = ctx.chatNews(id);
      if (said === '') return;
      if (said) body = said;
    }
    const kind = news ? news.kind : (next.state === 'waiting' ? 'question' : 'finished');
    const title = session.title || 'Socrates';
    // One notification per session: the tag replaces the previous one, so a
    // session that finishes twice while the phone is locked leaves one line
    // in the tray and not a column of them.
    const options = {
      body,
      tag: 'socrates:' + id,
      renotify: true,
      icon: NOTE_ICON,
      badge: NOTE_BADGE,
      data: { id, kind, url: targetOf(id, kind) },
    };
    let reg = null;
    if ('serviceWorker' in navigator) {
      try { reg = await navigator.serviceWorker.getRegistration(); } catch { /* none to be had */ }
    }
    if (reg && reg.active && typeof reg.showNotification === 'function') {
      try { await reg.showNotification(title, options); } catch { /* a browser that refuses the call */ }
      return;
    }
    try {
      const note = new window.Notification(title, options);
      note.onclick = () => {
        try { window.focus(); } catch { /* a browser that will not come forward */ }
        follow(id, kind);
        note.close();
      };
    } catch { /* a browser that has the constructor and refuses the call */ }
  }

  /* ------------------------------------------------------- the app's badge */

  // The number on the app's icon is the number of questions waiting - every
  // session the Questions card counts, a menu or a permission prompt or a
  // question in prose - and the worker sets it from every push. The page
  // sets it too, whenever what it holds changes, so that answering a
  // question here takes it off the icon now rather than at the next push.
  // What it last set, and for which sessions: -1 and null are "nothing yet".
  let badgeSet = -1;
  let askingKey = null;

  function setBadge(count) {
    try {
      const done = count > 0
        ? (typeof navigator.setAppBadge === 'function' ? navigator.setAppBadge(count) : null)
        : (typeof navigator.clearAppBadge === 'function' ? navigator.clearAppBadge() : null);
      // iOS draws a badge only for an app that may notify, and says so by
      // refusing; a badge that is not drawn is not worth a word.
      if (done && done.catch) done.catch(() => { /* no badge on this device */ });
    } catch { /* the same answer, thrown instead of rejected */ }
  }

  // closeAnswered takes out of the tray the questions that are not asking any
  // more: a question answered here, or at the machine, or on another device,
  // is not something to tap on later and be told there is nothing there.
  async function closeAnswered(still) {
    if (!('serviceWorker' in navigator)) return;
    try {
      const reg = await navigator.serviceWorker.getRegistration();
      if (!reg || typeof reg.getNotifications !== 'function') return;
      for (const note of await reg.getNotifications()) {
        const data = note.data || {};
        if (data.kind === 'question' && data.id && !still.has(data.id)) note.close();
      }
    } catch { /* a tray that cannot be read is left as it is */ }
  }

  /**
   * asking is told the sessions that are asking whenever they may have
   * changed, by a page whose state is known to be current. The badge is only
   * written when its number moved, and the tray only looked through when the
   * set did - this is called on every frame. `force` is a page coming back
   * into view: a push may have written the icon meanwhile, so the page's own
   * memory of it proves nothing.
   */
  function asking(ids, { force = false } = {}) {
    const list = [...new Set((ids || []).map(String))].sort();
    const key = list.join('\n');
    if (!force && key === askingKey) return;
    askingKey = key;
    if (force || list.length !== badgeSet) {
      badgeSet = list.length;
      setBadge(list.length);
    }
    closeAnswered(new Set(list));
  }

  /* ------------------------------------------------------------ web push */

  // The notification above needs the page to be running, and a phone in a
  // pocket has suspended it. Where this browser can be reached by Web Push -
  // a worker, a push manager and a secure context, which plain http on a LAN
  // address is not - the server sends the same news through the browser's
  // push service and the worker shows it (internal/server/push.go), so the
  // page stays quiet to say it once.
  const pushable = available && window.isSecureContext
    && 'serviceWorker' in navigator && 'PushManager' in window;
  // Whether this device has a subscription the server has accepted.
  let pushing = false;

  function keyBytes(text) {
    const b64 = text.replace(/-/g, '+').replace(/_/g, '/');
    const raw = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
    return Uint8Array.from(raw, (c) => c.charCodeAt(0));
  }

  function sameKey(buffer, want) {
    if (!buffer) return false;
    const have = new Uint8Array(buffer);
    return have.length === want.length && have.every((b, i) => b === want[i]);
  }

  // The worker's registration. `ready` never settles where none was
  // registered, so the wait is bounded.
  function registration() {
    return Promise.race([
      navigator.serviceWorker.ready,
      new Promise((_, reject) => setTimeout(
        () => reject(new Error('The offline worker is not running, and push needs it.')), 10000)),
    ]);
  }

  /**
   * subscribe makes sure this browser is subscribed with the server's key and
   * that the server knows it. With `test`, the server is asked to push once,
   * which is the only proof that the whole road works.
   */
  async function subscribe(test) {
    const reg = await registration();
    const { key } = await api('/api/push');
    const want = keyBytes(key);
    let sub = await reg.pushManager.getSubscription();
    // A subscription made for another key - a reinstalled server - is one the
    // push service will refuse every message for.
    if (sub && !sameKey(sub.options && sub.options.applicationServerKey, want)) {
      await sub.unsubscribe();
      sub = null;
    }
    if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: want });
    await api('/api/push/subscribe', { method: 'POST', body: { subscription: sub.toJSON() } });
    pushing = true;
    if (test) await api('/api/push/test', { method: 'POST', body: { endpoint: sub.endpoint } });
  }

  async function unsubscribe() {
    pushing = false;
    const reg = await registration();
    const sub = await reg.pushManager.getSubscription();
    if (!sub) return;
    const endpoint = sub.endpoint;
    try { await sub.unsubscribe(); } catch { /* the server is told either way */ }
    await api('/api/push/unsubscribe', { method: 'POST', body: { endpoint } });
  }

  // follow is a notification that was tapped. A question opens the dialog that
  // answers it, which is what the notification was about; anything else
  // opens the session. A "Finished" whose turn ended on a question in prose
  // is a question by the time it is tapped - the digest that read it came
  // after the news - and opens the dialog as well.
  function follow(id, kind) {
    const asks = kind === 'question' || (!!ctx.asksInProse && ctx.asksInProse(id));
    if (asks && ctx.question) ctx.question(id);
    else ctx.select(id);
  }

  // A tap on a notification lands in a tab that was already open, and the
  // worker says which session it was about, and what kind of news it was
  // (the listener at the top of this module). What came before this mount is
  // handed over once the page's own start-up has run to its first wait: a
  // tap opens a session or a question, and the page's boot, which puts Home
  // on the stage, must not come after it and undo it.
  queueMicrotask(() => {
    onOpen = follow;
    for (const msg of early.splice(0)) follow(msg.id, msg.kind);
  });
  onPushed = () => { try { ctx.pushed?.(); } catch { /* the next push or the next look puts it right */ } };

  /* ------------------------------------------------------- the completion */

  /**
   * completed is the one door: a session went from working to not working.
   *
   * `prev` is what the page believed a moment ago, so a first sighting and a
   * handshake replay - both of which arrive with no previous state - say
   * nothing. A session coming back from `unknown` says nothing either: nothing
   * finished, the detector merely found its footing again.
   *
   * `next.unread` is the server's own answer to whether this is news at all,
   * and it is the same edge the sidebar goes bold on. A harness is busy while
   * it starts up and idle the moment it is ready, and that is not a turn
   * ending: the server marks a completion unread only for a session somebody
   * has actually typed at, and this is where a phone is told the difference.
   *
   * It has to be a mark that was just set, though, and not one that was
   * already there. A start, a restart and a resume all keep the unread mark
   * they found - work that finished before the relaunch is still work nobody
   * has seen - and the new process then runs its own start-up, which is a
   * busy that ends in idle carrying that old mark. Chiming there would
   * announce the press of the Start button as a turn that finished. Every
   * real completion begins with a keystroke, and typing clears the mark, so
   * on a genuine one `prev.unread` is false.
   *
   * A turn that ended with a shell or a monitor still running (`tasks`) has
   * not finished: the server sets no mark for it, and the chime waits for the
   * change out of it - the agent's real last word, or the work behind it
   * ending on its own.
   */
  function completed(id, next, prev) {
    if (next) followHeld(id, next);
    if (!prev || !next) return;
    if (prev.state !== 'busy' && prev.state !== 'tasks') return;
    if (next.state !== 'idle' && next.state !== 'waiting') return;
    if (!next.unread || prev.unread) return;
    if (next.since && rung.get(id) === next.since) return;
    rung.set(id, next.since || Date.now());
    // A commit inside the settling window is counted as rung, so the poll
    // that repeats it a moment later does not ring for it either.
    if (Date.now() < (quiet.get(id) || 0)) return;
    // A chat's turn whose end is no news, as its push has it, is neither
    // heard nor shown, and waits for no verdict: one the person stopped
    // themselves, and one a limit stopped, which the server tells once it
    // knows the chat stays stopped (told).
    if (ctx.chatNews && ctx.chatNews(id) === '') return;
    // A turn whose verdict is being read waits for it, as its push does: the
    // chime and the notification are one piece of news, and the news is how
    // the turn left the work (judged).
    const outcome = next.state === 'idle' && ctx.outcome ? ctx.outcome(id) : null;
    if (outcome && outcome.phase === 'pending' && Number(outcome.since) === Number(next.since)) {
      hold(id, next, outcome);
      return;
    }
    ring(id, next, null);
  }

  /**
   * told is news the server found outside the activity, with its words: a
   * chat stopped at its account's limit, said once it is known that the chat
   * does not go on with another account by itself - with when the limit
   * renews and on which account, as its push says it. Heard and shown as a
   * turn's end is.
   */
  function told(id, news) {
    if (!news || !news.body) return;
    ring(id, null, news);
  }

  // ring is the chime and the notification of one turn's end, saying news
  // when there is a verdict to say.
  function ring(id, next, news) {
    if (sound) {
      const now = Date.now();
      if (now - lastChime >= CHIME_EVERY) {
        lastChime = now;
        chime();
      }
    }
    // A notification is titled with the session's name, so one that is not in
    // the list yet is heard and not shown.
    const session = ctx.sessionOf(id);
    if (session) show(id, next, session, news);
  }

  // The turns' ends waiting for their verdicts, keyed by session: what the
  // change was, the idle it ended on, and the end of the wait.
  const judging = new Map();

  function hold(id, next, outcome) {
    drop(id);
    const wait = Math.max(0, (Number(outcome.due) || 0) - Date.now());
    const timer = setTimeout(() => {
      const held = judging.get(id);
      if (!held) return;
      drop(id);
      ring(id, held.next, null);
    }, wait);
    judging.set(id, { next, since: Number(next.since), timer });
  }

  function drop(id) {
    const held = judging.get(id);
    if (!held) return;
    clearTimeout(held.timer);
    judging.delete(id);
  }

  // follow lets go of a turn's end that turned out not to be one - the
  // session is working, asking or running something again - or that somebody
  // has read meanwhile. A tick of unknown is nothing, and the prompt it comes
  // back to is the same idle in a new commit.
  function followHeld(id, next) {
    const held = judging.get(id);
    if (!held || next.state === 'unknown') return;
    if (next.state !== 'idle' || !next.unread) drop(id);
    else held.since = Number(next.since);
  }

  /**
   * judged is the verdict on a turn. A turn's end waiting for exactly this
   * idle is heard and shown now, saying it; one that failed to be judged is
   * "Finished", as it would have been without.
   */
  function judged(id, outcome) {
    const held = judging.get(id);
    if (!held || !outcome || Number(outcome.since) !== held.since) return;
    drop(id);
    const question = ctx.questionOf ? ctx.questionOf(id) : null;
    ring(id, held.next, outcomeNews(outcome, question));
  }

  /**
   * hush opens the settling window of one session: for the next SETTLE
   * milliseconds a completion of it is neither heard nor shown. The page calls
   * it when a session is opened and again when its program comes up, because
   * a relaunch can take longer than the window and the start-up it hides is
   * the one that follows.
   */
  function hush(id) {
    if (id) quiet.set(id, Date.now() + SETTLE);
  }

  /* -------------------------------------------------------- the two switches */

  function setSound(on) {
    sound = !!on;
    remember(SOUND_KEY, sound);
    changed();
    // Turning it on is the gesture the browser wanted, and hearing it once is
    // the only way to know what has been turned on. It counts against the
    // rate limit like any other chime.
    if (sound) {
      lastChime = Date.now();
      chime();
    }
  }

  // One question at a time: a second tap while the browser is still asking
  // for permission would ask twice, and the second verdict could undo the
  // first.
  let prompting = null;

  async function setNotify(on) {
    if (!available || prompting) return;
    if (!on) {
      if (!notify) return;
      notify = false;
      remember(NOTIFY_KEY, false);
      changed();
      if (pushable) unsubscribe().catch(() => { /* the server forgets it when the push service says gone */ });
      return;
    }
    if (notify) return;
    // Asked from inside the click, because a permission prompt raised out of a
    // gesture is a prompt the browser dismisses on the page's behalf - which
    // is why nothing is awaited before it.
    prompting = (async () => {
      try { return await window.Notification.requestPermission(); } catch { return null; }
    })();
    let verdict;
    try { verdict = await prompting; } finally { prompting = null; }
    // The old callback form of the same call answers nothing and writes the
    // verdict onto the constructor instead.
    if (!verdict) verdict = window.Notification.permission;
    notify = verdict === 'granted';
    remember(NOTIFY_KEY, notify);
    changed();
    if (!notify) {
      // On an iPhone there is no site setting to point at: the app's own
      // entry in the Settings app is where it is turned on again.
      toast(IOS ? 'Notifications are off for Socrates: turn them on in Settings → Notifications → Socrates.'
        : 'Notifications are blocked for this site in the browser.', 'error');
      return;
    }
    if (!pushable) return;
    try {
      await subscribe(true);
    } catch (err) {
      // The page's own notification still works while it is open, so this is
      // a warning about the locked phone, not a switch that failed.
      toast('Notifications only work while this page is open: ' + errorMessage(err), 'error');
      return;
    }
    // Turned off again while the subscription was being made: the switch is
    // what the person sees, so the server is told to stop as well.
    if (!notify) unsubscribe().catch(() => { /* the server forgets it when the push service says gone */ });
  }

  // A device that turned notifications on before is subscribed again on every
  // load, which is how a server that lost its list - or got a new key - finds
  // it again.
  if (notify && pushable) subscribe(false).catch(() => { /* the page's own notification stands in */ });

  return {
    completed,
    judged,
    told,
    hush,
    soundOn: () => sound,
    notifyOn: () => notify && available,
    /** notifyAvailable is false where this browser has no notifications. */
    notifyAvailable: () => available,
    /**
     * notifyHint is the line under the switch in the options menu: what it
     * does, or why it cannot here and what would change that.
     */
    notifyHint() {
      if (addToHome) return 'Add to Home Screen for notifications';
      if (!available) return IOS ? 'Needs iOS 16.4 or later' : 'Not available in this browser';
      let denied = false;
      try { denied = window.Notification.permission === 'denied'; } catch { /* asked when pressed */ }
      if (IOS && denied) return 'Turned off in Settings → Notifications → Socrates';
      return 'When a session finishes or needs an answer';
    },
    asking,
    setSound,
    /** setNotify turns notifications on - asking the browser - or off. */
    setNotify: (on) => setNotify(on).catch(() => { /* the toast is the whole of the report */ }),
    /**
     * signOut stops pushes to this device: a browser that has signed out must
     * not go on showing the names of the sessions. Signing in again
     * subscribes it again, because the switch itself stays on.
     */
    async signOut() {
      if (pushable) await unsubscribe().catch(() => { /* signing out anyway */ });
    },
  };
}
