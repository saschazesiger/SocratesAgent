// Speaking, where the alternative is typing.
//
// A terminal is a picture, and a picture is the one thing a phone in a car
// cannot use. This is the other half of that: the keyboard is the one thing a
// phone in a car cannot use either, so the microphone is how a session is
// typed into without looking at it.
//
// One tap opens a recording, and a recording is the whole screen while it
// runs, because what is being asked of it - keep this, or throw it away - is
// the only thing being asked and both answers have to be findable without
// looking. What comes back is put into the pane as if it had been typed, and
// no further: the Enter that runs it is the person's, because a transcript is
// a guess about what was said and a guess is not something to run by itself.
//
// There is exactly one sheet and one microphone on the page, and the top half
// of this file is them. `record()` is the whole round trip - open, listen,
// transcribe - and it is what the pill in the top bar calls, what every
// field's own microphone calls (keyboard.js), and what anything that ever
// needs words from a person calls. Two microphones open at once is a
// recording of a recording, so a second caller is answered with nothing
// rather than with a second microphone.

import { el, toast, errorMessage, fmtClock, openDialog } from './api.js';
import { showOverlay, hideOverlay } from './ui.js';
import { dictateOnce, voiceKey, onVoiceKey, keyMissing } from './voice.js';

// How many bars the level meter has, and how far a frame may fall. The decay
// is what makes a voice look like a voice: the peak of a syllable stays up for
// a moment instead of flickering out between two of them.
const METER_BARS = 15;
const METER_DECAY = 0.06;

// The sheet is one document's worth of elements and there is one of it, so it
// is looked up by id rather than handed in: a module that owns a singleton
// does not need a map of it passed to every call.
const node = (id) => document.getElementById(id);

/* ------------------------------------------------------------- the meter */

// The level meter. It is the answer to the one question a recording sheet
// has to answer before it is trusted - is it hearing me - and it is drawn
// from the recording's own analyser, so a meter that moves is proof that
// audio is arriving rather than that the page is animating.
const calm = matchMedia('(prefers-reduced-motion: reduce)');
let meterFrame = 0;
let meterTimer = 0;
let bars = [];
let levels = [];

function buildMeter() {
  const host = node('recMeter');
  if (!host) return;
  const plain = calm.matches;
  const count = plain ? 1 : METER_BARS;
  host.toggleAttribute('data-plain', plain);
  if (bars.length === count && host.childNodes.length === count) return;
  bars = [];
  for (let i = 0; i < count; i += 1) bars.push(el('span', { class: 'so-meter__bar' }));
  host.replaceChildren(...bars);
  levels = new Array(count).fill(0);
}

// level is the loudness of one frame, 0..1, from the time-domain samples.
// RMS rather than peak: a click is not a voice, and a meter that jumps to
// full on one sample says nothing about whether words are getting through.
function level(analyser, buf) {
  analyser.getByteTimeDomainData(buf);
  let sum = 0;
  for (let i = 0; i < buf.length; i += 1) {
    const v = (buf[i] - 128) / 128;
    sum += v * v;
  }
  // The square root is the amplitude; the multiplier puts ordinary speech
  // near the top of the meter instead of in the bottom tenth of it.
  return Math.min(1, Math.sqrt(sum / buf.length) * 4.5);
}

function startMeter(analyser) {
  stopMeter();
  buildMeter();
  if (!analyser || !bars.length) return;
  const buf = new Uint8Array(analyser.fftSize);
  if (calm.matches) {
    // No animation: the level is read a few times a second and the one bar
    // is set to it. It still moves with the room, which is the information.
    const tick = () => {
      bars[0].style.setProperty('--level', Math.round(level(analyser, buf) * 100) + '%');
    };
    tick();
    meterTimer = setInterval(tick, 150);
    return;
  }
  const draw = () => {
    const now = level(analyser, buf);
    // The newest reading enters at the middle and the older ones walk out to
    // both edges, so the shape reads as a voice rather than as a bar chart.
    levels.pop();
    levels.unshift(now);
    const middle = (bars.length - 1) / 2;
    for (let i = 0; i < bars.length; i += 1) {
      const age = Math.round(Math.abs(i - middle));
      const value = Math.max(levels[age] - age * METER_DECAY, 0);
      bars[i].style.transform = 'scaleY(' + (0.07 + value * 0.93).toFixed(3) + ')';
    }
    meterFrame = requestAnimationFrame(draw);
  };
  meterFrame = requestAnimationFrame(draw);
}

function stopMeter() {
  if (meterFrame) { cancelAnimationFrame(meterFrame); meterFrame = 0; }
  if (meterTimer) { clearInterval(meterTimer); meterTimer = 0; }
  levels = levels.map(() => 0);
  for (const bar of bars) {
    bar.style.transform = '';
    bar.style.removeProperty('--level');
  }
}

/* --------------------------------------------------------- the recording */

// The recording that is running, as the two endings it has, or null, and what
// the one microphone on this page is doing:
//
//   idle          nothing is open
//   asking        the server is asked whether an OpenRouter key is set yet
//   opening       the microphone has been asked for and has not answered yet
//   recording     the sheet is up and audio is arriving
//   transcribing  the audio has gone to the server, the words are coming back
//
// Everything that draws a microphone draws it from that phase, and it is
// broadcast rather than handed back to whoever started the recording: the pill
// in the top bar has to look shut while a field's own microphone is running,
// or a second tap opens a second one.
let dictation = null;
let phase = 'idle';
const watchers = new Set();
let wired = false;

/** recPhase is what the one microphone on this page is doing. */
export function recPhase() { return phase; }

/** onRecPhase is told whenever that changes, and hands back its own undo. */
export function onRecPhase(fn) {
  watchers.add(fn);
  return () => watchers.delete(fn);
}

function setPhase(next) {
  if (phase === next) return;
  phase = next;
  for (const fn of [...watchers]) {
    try { fn(next); } catch { /* one bad listener must not stop the others */ }
  }
}

function closeSheet() {
  stopMeter();
  const sheet = node('recSheet');
  if (sheet && sheet.open) hideOverlay(sheet);
}

/**
 * mountRecSheet wires the one recording sheet to the page. It is idempotent,
 * so anything that is about to record may call it and the first caller wins.
 */
export function mountRecSheet() {
  if (wired) return;
  wired = true;
  const sheet = node('recSheet');
  const send = node('recSend');
  const cancel = node('recCancel');
  if (send) {
    send.addEventListener('click', () => {
      if (!dictation) return;
      // The words are on their way to the server from here, so the sheet has
      // said everything it can: whichever microphone opened it carries the
      // wait.
      const ends = dictation;
      dictation = null;
      setPhase('transcribing');
      stopMeter();
      ends.stop();
      if (sheet && sheet.open) hideOverlay(sheet);
    });
  }
  if (cancel) cancel.addEventListener('click', () => { if (dictation) dictation.cancel(); });
  if (sheet) {
    // Escape and a tap on the backdrop close the dialog (DS.overlay), and a
    // close is the same answer as Cancel: a sheet that closed while the
    // microphone stayed open would be a recording nobody could see and nobody
    // could stop. Send has already let go of the recording when it closes.
    sheet.addEventListener('close', () => {
      stopMeter();
      if (dictation) dictation.cancel();
    });
  }
  buildMeter();
}

/* ---------------------------------------------------------- no key yet */

const NO_KEY_WORDS = {
  dictate: {
    title: 'Dictation needs an OpenRouter key',
    body: 'What you say is turned into text through OpenRouter, and no key is set yet. Add one in Admin, under Voice.',
  },
  status: {
    title: 'Status needs an OpenRouter key',
    body: 'Status is written by a model on OpenRouter, and no key is set yet. Add one in Admin, under Voice.',
  },
  title: {
    title: 'Naming needs an OpenRouter key',
    body: 'A session is named by a model on OpenRouter, and no key is set yet. Add one in Admin, under Voice.',
  },
  read: {
    title: 'Read aloud needs a Text-to-Speech key',
    body: 'Questions are read out by Google Cloud Text-to-Speech, and no key is set yet. Add one in Admin, under Voice.',
  },
};

/**
 * explainNoKey says, before anything is recorded or sent, that what was
 * pressed needs a key (OpenRouter's, or Text-to-Speech's for Read aloud) and
 * where it is added - in a dialog rather
 * than a toast, because it is the answer to the tap and has a way on: Admin.
 * It works over a dialog too (a field's own microphone in the rename dialog).
 */
export function explainNoKey(what = 'dictate', opener = null) {
  const words = NO_KEY_WORDS[what] || NO_KEY_WORDS.dictate;
  const admin = el('a', { class: 'ds-btn ds-btn--primary', href: '/admin#voiceCard', text: 'Open Admin' });
  const close = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: true, text: 'Close' });
  return openDialog({ title: words.title, body: words.body, actions: [close, admin], opener }).shown;
}

/**
 * record listens once and hands back what was said, as one line.
 *
 * A recording that was thrown away resolves to the empty string, and so does
 * one that failed - the reason has already been said out loud in a toast, and
 * a caller with nothing to put anywhere does nothing either way. A second
 * caller while one is running is answered with the empty string and no
 * microphone is opened at all.
 *
 * `onError` takes the sentence over from the toast, for a caller that lives
 * in a modal dialog: a toast is drawn under the top layer such a dialog is
 * in, and would be a sentence nobody sees.
 */
export async function record({ onError, opener = null } = {}) {
  if (phase !== 'idle') return '';
  // Nothing is recorded that cannot be transcribed. Whether the key is
  // still missing is a round trip, and the microphone is taken for it
  // ('asking'): a second press meanwhile - a double click, a tap again on a
  // slow network - found the phase still idle and opened a second "needs a
  // key" dialog, or with a key a second recording that the sheet could not
  // stop.
  if (voiceKey() === false) {
    setPhase('asking');
    let missing = false;
    try {
      missing = await keyMissing();
    } finally {
      setPhase('idle');
    }
    if (missing) {
      await explainNoKey('dictate', opener);
      return '';
    }
  }
  mountRecSheet();
  setPhase('opening');
  try {
    const text = await dictateOnce({
      onTime: (secs) => {
        const clock = node('recTime');
        if (clock) clock.textContent = fmtClock(secs);
      },
      onReady: (ends) => {
        dictation = ends;
        const sheet = node('recSheet');
        if (sheet && !sheet.open) showOverlay(sheet);
        startMeter(ends.analyser);
        setPhase('recording');
      },
    });
    // A discarded recording resolves to nothing, and nothing is what it does:
    // no keystroke, no request, and nothing said about it.
    return oneLine(text);
  } catch (err) {
    if (err && err.noKey) {
      // Learnt only now, from the refusal: from here on it is said before
      // the next recording. The dialog waits for the sheet to have gone.
      setTimeout(() => explainNoKey('dictate', opener), 0);
      return '';
    }
    const sentence = (err && err.userMessage) || errorMessage(err);
    if (onError) onError(sentence); else toast(sentence, 'error');
    return '';
  } finally {
    dictation = null;
    closeSheet();
    const clock = node('recTime');
    if (clock) clock.textContent = fmtClock(0);
    setPhase('idle');
  }
}

/** cancelRecording throws away whatever is being recorded, if anything is. */
export function cancelRecording() { if (dictation) dictation.cancel(); }

/* ----------------------------------------------------------- the top bar */

/**
 * mountDictation wires the top bar's microphone to one page.
 *
 * `ctx` is what this module is not allowed to own:
 *   dom      the shared id map
 *   current  the session on screen, or null
 *   live     whether the socket is up
 *   insert   put one line of text into the pane, as if it had been typed
 */
export function mountDictation(ctx) {
  const { dom } = ctx;
  mountRecSheet();

  // dictate records one line and puts it in the pane.
  async function dictate() {
    // Which pane these words are for. A transcript takes a moment to come
    // back, and a session switched in that moment would be typed into by a
    // sentence that was never about it - which is the one thing a control that
    // types into somebody's terminal must not do.
    const spokenTo = (ctx.current() || {}).id || '';
    const text = await record({ opener: dom.micBtn });
    if (!text) return;
    if (((ctx.current() || {}).id || '') !== spokenTo) {
      toast('That was said to another session, so it was not typed.');
      return;
    }
    ctx.insert(text);
  }

  function paint() {
    const btn = dom.micBtn;
    if (!btn) return;
    const session = ctx.current();
    // An editor is typed into with its own keys; there is no pane to speak
    // into.
    btn.hidden = !session || session.harness === 'vscode';
    // The tip says where the words go, or - with no key - why a press only
    // explains. A press is still taken: it is the one place that says how
    // to get a key.
    const into = session && session.kind === 'chat' ? 'Dictate into this chat' : 'Dictate into the terminal';
    const tip = voiceKey() === false ? 'Needs an OpenRouter key (Admin, Voice)' : into;
    if (btn.getAttribute('data-tip') !== tip) btn.setAttribute('data-tip', tip);
    // The pill is shut while a recording is being opened, is running, or is
    // being turned into words: all three are one recording, and a second tap
    // in any of them would start a second microphone. It is shut with no
    // connection too, because transcribing is a request to the server.
    // Transcribing is the button's busy face instead (the spinner and the
    // word), which blocks the pointer as well; a disabled button would grey
    // the spinner out. The resting face keeps saying Speak.
    // Asking the server whether a key is set is the busy face too: the tap
    // landed, and a second one does nothing.
    btn.disabled = !session || !ctx.live() || (phase !== 'idle' && phase !== 'transcribing' && phase !== 'asking');
    if (phase === 'transcribing' || phase === 'asking') btn.setAttribute('aria-busy', 'true');
    else btn.removeAttribute('aria-busy');
    btn.setAttribute('aria-label', phase === 'transcribing' ? 'Transcribing…' : 'Speak');
  }

  if (dom.micBtn) dom.micBtn.addEventListener('click', () => dictate());
  onRecPhase(paint);
  onVoiceKey(paint);
  paint();

  return {
    /** attached is a session becoming the one on screen, or nothing being. */
    attached() {
      // A recording belongs to the session it was started in: putting its
      // transcript into the one that has just taken the screen would type
      // into the wrong terminal.
      cancelRecording();
      paint();
    },

    /** live repaints what an outage takes away. */
    live() { paint(); },
  };
}

// oneLine is a transcript as a terminal can take it. A model handed a long
// sentence sometimes breaks it, and a newline in a pane is the Enter that was
// deliberately not sent - so the line breaks become the spaces they stand for.
function oneLine(text) {
  return String(text).replace(/\s*[\r\n]+\s*/g, ' ').trim();
}
