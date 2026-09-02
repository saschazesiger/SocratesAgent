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
// document.
//
// This module knows nothing about the session page. It is handed a completion
// - which session, what it went to, what it came from - and the two ways to
// say so.

import { toast } from './api.js';

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

// What a notification says under the session's name. The words are the
// sidebar's own, so the phone and the page agree about what happened.
const BODY = { idle: 'Finished', waiting: 'Needs an answer' };

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
 * mountNotify wires the two header toggles and hands back the one thing the
 * page calls into.
 *
 * `ctx` is everything this module is not allowed to own:
 *   dom        the shared id map
 *   sessionOf  a session by id, or null - the name a notification is titled with
 *   select     open a session, for a notification that is clicked
 */
export function mountNotify(ctx) {
  const { dom } = ctx;

  // iOS Safari has no Notification outside a home-screen app, so the button
  // says so rather than failing when it is pressed.
  const available = typeof window.Notification === 'function';

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
  // A change reaches the page twice by two honest routes - the broadcast
  // frame and the fifteen-second poll - and a poll that was in flight when
  // the turn ended answers with the state before it. That reads as
  // idle -> busy -> idle, and the second edge is the same commit as the
  // first. One commit is one piece of news.
  const rung = new Map();

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

  function show(id, next, session) {
    if (!notify || !available) return;
    if (window.Notification.permission !== 'granted') return;
    const body = BODY[next.state];
    if (!body) return;
    try {
      // One notification per session: the tag replaces the previous one, so a
      // session that finishes twice while the phone is locked leaves one line
      // in the tray and not a column of them.
      const note = new window.Notification(session.title, {
        body,
        tag: 'socrates:' + id,
        renotify: true,
        icon: '/static/img/logo.png',
      });
      note.onclick = () => {
        try { window.focus(); } catch { /* a browser that will not come forward */ }
        ctx.select(id);
        note.close();
      };
    } catch { /* a browser that has the constructor and refuses the call */ }
  }

  /* ------------------------------------------------------- the completion */

  /**
   * completed is the one door: a session went from working to not working.
   *
   * `prev` is what the page believed a moment ago, so a first sighting and a
   * handshake replay - both of which arrive with no previous state - say
   * nothing. A session coming back from `unknown` says nothing either: nothing
   * finished, the detector merely found its footing again.
   */
  function completed(id, next, prev) {
    if (!prev || !next) return;
    if (prev.state !== 'busy') return;
    if (next.state !== 'idle' && next.state !== 'waiting') return;
    if (next.since && rung.get(id) === next.since) return;
    rung.set(id, next.since || Date.now());
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
    if (session) show(id, next, session);
  }

  /* -------------------------------------------------------- the two toggles */

  function paintSound() {
    const btn = dom.soundBtn;
    if (!btn) return;
    btn.setAttribute('aria-pressed', sound ? 'true' : 'false');
    const words = sound ? 'Sound on' : 'Sound off';
    btn.title = words;
    btn.setAttribute('aria-label', words);
  }

  function paintNotify() {
    const btn = dom.notifyBtn;
    if (!btn) return;
    const words = !available
      ? 'Notifications are not available in this browser'
      : (notify ? 'Notifications on' : 'Notifications off');
    btn.disabled = !available;
    btn.setAttribute('aria-pressed', notify ? 'true' : 'false');
    btn.title = words;
    btn.setAttribute('aria-label', words);
  }

  function setSound(on) {
    sound = !!on;
    remember(SOUND_KEY, sound);
    paintSound();
    // Turning it on is the gesture the browser wanted, and hearing it once is
    // the only way to know what has been turned on. It counts against the
    // rate limit like any other chime.
    if (sound) {
      lastChime = Date.now();
      chime();
    }
  }

  async function toggleNotify() {
    if (!available) return;
    if (notify) {
      notify = false;
      remember(NOTIFY_KEY, false);
      paintNotify();
      return;
    }
    // Asked from inside the click, because a permission prompt raised out of a
    // gesture is a prompt the browser dismisses on the page's behalf.
    let verdict;
    try { verdict = await window.Notification.requestPermission(); }
    catch { verdict = null; }
    // The old callback form of the same call answers nothing and writes the
    // verdict onto the constructor instead.
    if (!verdict) verdict = window.Notification.permission;
    notify = verdict === 'granted';
    remember(NOTIFY_KEY, notify);
    paintNotify();
    if (!notify) toast('Notifications are blocked for this site in the browser.', 'error');
  }

  if (dom.soundBtn) dom.soundBtn.addEventListener('click', () => setSound(!sound));
  if (dom.notifyBtn) {
    dom.notifyBtn.addEventListener('click', () => {
      toggleNotify().catch(() => { /* the toast above is the whole of the report */ });
    });
  }
  paintSound();
  paintNotify();

  return {
    completed,
    soundOn: () => sound,
    notifyOn: () => notify && available,
  };
}
