// Status and the ticker: what you can ask a session for without typing into
// it, and the one line that says what is happening right now.
//
// A terminal is a picture, and a picture is the one thing a phone in a car
// cannot use. Status turns the screen into a sentence and reads it out loud;
// the microphone beside it (dictate.js) is the way back, and this module
// mounts it because the two are one gesture: listen, then answer.
//
// There is exactly one ticker line, and this is the only thing that writes it.
// A second indicator would be a second thing to read at a traffic light.

import { api, toast, setClass, errorMessage, isOffline, el } from './api.js';
import { speak, stopSpeaking, onSpeechError } from './voice.js';
import { mountDictation } from './dictate.js';

// How long a finished status keeps the ticker before the line goes. Long
// enough to read one line, and the same number as NOTICE_LINGER in session.js
// on purpose: the notice and the ticker are two lines stacked over the same
// pane, and one rhythm is easier to live with than two.
const TICKER_LINGER = 6000;

// How long a leaving line is given to leave. It is the CSS transition plus a
// frame, and it only decides when a detached element is removed.
const TICKER_SWAP = 320;

const path = (id, suffix) => '/api/sessions/' + encodeURIComponent(id) + suffix;

/**
 * mountAssist wires the header buttons and the ticker to one session page.
 *
 * `ctx` is everything this module is not allowed to own:
 *   dom       the shared id map
 *   notice    session.js's one-line banner, (kind, text, onDismiss, facts, extra)
 *   current   the session on screen, or null
 *   live      whether the socket is up
 *   insert    put one line of text into the pane, as if it had been typed
 */
export function mountAssist(ctx) {
  const { dom } = ctx;

  // A render that is somebody's answer being read out loud is the one thing
  // this page can fail at silently, so the reason is shown where the answer
  // was expected. Registered once, here, because this is the only place in
  // the app that reads anything out by itself.
  onSpeechError((message, kind) => toast(message, kind));

  let busyStatus = false;
  let speaking = false;
  // Which utterance is the current one. See say().
  let sayGen = 0;
  // The line a status is currently putting in the ticker, and the timer that
  // takes it away again once it has been read.
  let statusLine = '';
  let statusTimer = null;

  /* ------------------------------------------------------------ the ticker */

  // One line at a time, the newest rising into the window and the old one
  // leaving upwards. Under reduced motion the CSS turns this into a plain
  // swap; the JavaScript is the same either way.
  const ticker = (() => {
    let current = null;
    let shown = '';
    return {
      show(text) {
        if (!text || text === shown) return;
        shown = text;
        dom.termTicker.hidden = false;
        const line = el('span', { class: 'ticker-line enter', text });
        dom.tickerWindow.append(line);
        const previous = current;
        current = line;
        requestAnimationFrame(() => {
          line.classList.remove('enter');
          if (!previous) return;
          previous.classList.add('leave');
          setTimeout(() => previous.remove(), TICKER_SWAP);
        });
      },
      hide() {
        if (dom.termTicker.hidden) return;
        dom.termTicker.hidden = true;
        dom.tickerWindow.innerHTML = '';
        current = null;
        shown = '';
      },
    };
  })();

  // What the ticker should be saying, from what is true. The line is written
  // in exactly one place - setStatusPhase - and read in exactly this one, and
  // with no session on screen there is nothing to say at all.
  function paintTicker() {
    const text = ctx.current() ? statusLine : '';
    if (text) ticker.show(text); else ticker.hide();
  }

  // linger keeps a finished line up for a moment and then gives the window
  // back, because a line that vanished the instant it ended would be one
  // nobody read.
  function linger() {
    if (statusTimer) clearTimeout(statusTimer);
    statusTimer = setTimeout(() => { statusTimer = null; statusLine = ''; paintTicker(); }, TICKER_LINGER);
  }

  /* ------------------------------------------------------------- painting */

  function paint() {
    const session = ctx.current();
    const usable = !!session && ctx.live();
    // The stop is the same button: what is being asked for while the voice is
    // reading is silence, and a second control for it is a second thing to
    // find on a screen nobody is looking at.
    if (dom.statusBtn) {
      setClass(dom.statusBtn, 'speaking', speaking);
      // The spinner is the answer to "did that tap land?", and it is on the
      // button that was tapped rather than somewhere else on the page.
      setClass(dom.statusBtn, 'working', busyStatus);
      const label = speaking ? 'Stop reading' : 'Summarize this session';
      dom.statusBtn.title = label;
      dom.statusBtn.setAttribute('aria-label', label);
      dom.statusBtn.hidden = !session;
      // Stopping is always available, even with the socket down: the sound is
      // already on this device and turning it off asks the server nothing.
      dom.statusBtn.disabled = speaking ? false : (!usable || busyStatus);
    }
    dictation.live();
    paintTicker();
  }

  /* -------------------------------------------------------------- status */

  // statusFacts is the technical half of an answer: which model wrote it, what
  // the detector thinks the session is doing, which language it was written
  // in. None of it belongs in a sentence that is about to be read out loud.
  function statusFacts(data) {
    return [
      data.state ? 'state ' + data.state : '',
      data.model || '',
      data.language || '',
    ].filter(Boolean);
  }

  async function runStatus() {
    const session = ctx.current();
    if (!session || busyStatus) return;
    busyStatus = true;
    // The first phase locally, before the request has even left: on a bad
    // connection the server's own first frame is a second away, and a tap with
    // nothing on screen reads as a tap that did not land.
    setStatusPhase('capturing', 'Reading the screen');
    paint();
    try {
      const data = await api(path(session.id, '/status'), {
        method: 'POST', attempts: 1, timeout: 120000,
      });
      const text = String((data && data.text) || '').trim();
      if (!text) {
        setStatusPhase('error', 'There was nothing to say about that screen.');
        toast('There was nothing to say about that screen.');
        return;
      }
      ctx.notice('status', text, null, statusFacts(data || {}));
      setStatusPhase('done', text);
      busyStatus = false;
      paint();
      await say(text);
    } catch (err) {
      setStatusPhase('error', assistFailed(err, 'That session could not be summarised.'));
      toast(assistFailed(err, 'That session could not be summarised.'), 'error');
    } finally {
      busyStatus = false;
      paint();
    }
  }

  // setStatusPhase puts one phase of a status into the ticker. It is called
  // from here and from the server's own frames, which is why "done" and
  // "error" are the two that stop holding the window.
  function setStatusPhase(phase, text) {
    if (!text) return;
    statusLine = text;
    if (statusTimer) { clearTimeout(statusTimer); statusTimer = null; }
    if (phase === 'done' || phase === 'error') linger();
    paintTicker();
  }

  // say reads one line and keeps the button honest while it does. A failed
  // render has already reached the page through onSpeechError, so it is not
  // said twice.
  async function say(text) {
    // Every line read out belongs to a generation, the way voice.js's own
    // utterances do. A second say() - an answer landing while a status is
    // still playing - takes the voice over, and the one it interrupted must
    // not clear a flag that now belongs to a sentence still being spoken.
    const mine = ++sayGen;
    speaking = true;
    paint();
    try {
      await speak(text);
    } catch { /* onSpeechError said why */ } finally {
      if (mine === sayGen) {
        speaking = false;
        paint();
      }
    }
  }

  function stopReading() {
    sayGen++;
    stopSpeaking();
    speaking = false;
    paint();
  }

  /* ------------------------------------------------------- the microphone */

  const dictation = mountDictation({
    dom,
    current: () => ctx.current(),
    live: () => ctx.live(),
    insert: (text) => ctx.insert(text),
  });

  /* --------------------------------------------------------------- wiring */

  if (dom.statusBtn) {
    dom.statusBtn.addEventListener('click', () => {
      if (speaking) stopReading(); else runStatus();
    });
  }
  paint();

  return {
    /** attached is a session becoming the one on screen, or nothing being. */
    attached() {
      if (statusTimer) { clearTimeout(statusTimer); statusTimer = null; }
      statusLine = '';
      stopReading();
      dictation.attached();
      paint();
    },

    /** live repaints what an outage takes away. */
    live() { paint(); },

    /** statusFrame is one phase of a status being made, from the server. */
    statusFrame(frame) {
      setStatusPhase(frame.phase || '', String(frame.text || '').trim());
    },
  };
}

// assistFailed is what a refused request says out loud. A lost connection says
// so in its own words; anything else the server said is a sentence with an
// identifier in it, and those belong behind an "i" or nowhere.
function assistFailed(err, sentence) {
  if (isOffline(err)) return errorMessage(err);
  // A 400 from this route is the one case where the server's own words are
  // the instruction - "open /admin and pick a model" - and there is nothing
  // else the page could say instead.
  if (err && err.status === 400) return errorMessage(err);
  return sentence;
}
