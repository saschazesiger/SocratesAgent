// Status, the ticker, auto mode and the chat: the things you can ask a session
// for without typing into it.
//
// They are one module because they are one idea. A terminal is a picture, and
// a picture is the one thing a phone in a car cannot use: Status turns the
// screen into a sentence and reads it out loud, the chat turns sentences back
// into keystrokes, and auto mode is the layout where those two are the whole
// interface - no field anywhere, not even the terminal's own - and the pane is
// merely still there, honest and shorter.
//
// The one line that says what is happening right now is here too, and there is
// exactly one of it: a status being made, a run taking a step, or - in auto
// mode, continuously - what the session is doing. A second indicator would be
// a second thing to read at a traffic light.

import { api, el, toast, setClass, errorMessage, isOffline, isBusyConflict } from './api.js';
import { speak, stopSpeaking, onSpeechError } from './voice.js';
import { label as harnessLabel } from './harnesses.js';
import { mountChat } from './chat.js';

// Per device, not per account: whether this phone is the one in the car is a
// fact about the phone.
const AUDIO_KEY = 'socrates.audio.mode';

// How long a finished status or a finished run keeps the ticker before it goes
// back to whatever is true underneath. Long enough to read one line.
const TICKER_LINGER = 6000;

// How long a leaving line is given to leave. It is the CSS transition plus a
// frame, and it only decides when a detached element is removed.
const TICKER_SWAP = 320;

// What a phase of a run is called in the ticker. The technical words - the
// phase itself, the run id, the model's note - are not on the page at all any
// more; the chat message the run belongs to is where a run is looked at.
const PHASE_WORDS = {
  thinking: 'thinking',
  acting: 'working',
  waiting: 'waiting for the session',
  done: 'done',
  error: 'stopped',
};

// What a session is doing, as the ticker says it in auto mode. The harness
// names itself, because "it" is not enough when three sessions are open.
const ACTIVITY_WORDS = {
  busy: ' is working',
  idle: ' is idle',
  waiting: ' is waiting for you',
};

const path = (id, suffix) => '/api/sessions/' + encodeURIComponent(id) + suffix;

// audioWanted is read before anything is mounted, because the class it decides
// is a layout and a layout applied late is a terminal that resizes under the
// person looking at it.
export function audioWanted() {
  try { return localStorage.getItem(AUDIO_KEY) === 'on'; } catch { return false; }
}

/**
 * mountAssist wires the header, the audio bar, the ticker and the chat to one
 * session page.
 *
 * `ctx` is everything this module is not allowed to own:
 *   dom       the shared id map
 *   notice    session.js's one-line banner, (kind, text, onDismiss, facts, extra)
 *   refit     re-measure the pane after the layout changed
 *   current   the session on screen, or null
 *   live      whether the socket is up
 *   setTyping open or close the terminal's own field
 */
export function mountAssist(ctx) {
  const { dom } = ctx;

  // A render that is somebody's answer being read out loud is the one thing
  // this page can fail at silently, so the reason is shown where the answer
  // was expected. Registered once, here, because this is the only place in
  // the app that reads anything out by itself.
  onSpeechError((message, kind) => toast(message, kind));

  let audio = audioWanted();
  let busyStatus = false;
  let speaking = false;
  // The auto-status that arrived while the voice was still busy. One deep: a
  // third transition replaces the second, because what a listener wants is
  // the latest state of the session, not a queue of old ones.
  let queuedAuto = false;
  // Which utterance is the current one. See say().
  let sayGen = 0;
  let run = null;
  // The line a status is currently putting in the ticker, and the timer that
  // takes it away again once it has been read.
  let statusLine = '';
  let statusTimer = null;
  let runTimer = null;

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

  // What the ticker should be saying, from what is true. There is one of these
  // and it is the only writer: a status being made wins, then a run in
  // progress, then - in auto mode only - the session's own state.
  function tickerText() {
    const session = ctx.current();
    if (!session) return '';
    if (statusLine) return statusLine;
    if (run) {
      // A finished run holds the window for a moment - `linger` is what lets
      // go of it - because a line that vanished the instant it ended would be
      // one nobody read.
      if (run.done) return run.error ? 'The run stopped before it finished.' : 'The run finished.';
      const step = Math.max(1, Number(run.step) || 1);
      const said = String(run.action || '').trim() || PHASE_WORDS[run.phase] || 'working';
      return 'Step ' + step + ' · ' + said;
    }
    if (!audio) return '';
    const state = (ctx.activityOf(session.id) || {}).state || 'unknown';
    const words = ACTIVITY_WORDS[state];
    if (!words) return harnessLabel(session.harness) + ' is starting up';
    return harnessLabel(session.harness) + words;
  }

  function paintTicker() {
    const text = tickerText();
    if (text) ticker.show(text); else ticker.hide();
  }

  // linger keeps a finished line up for a moment and then lets whatever is
  // true underneath take the window back.
  function linger(which) {
    if (which === 'status') {
      if (statusTimer) clearTimeout(statusTimer);
      statusTimer = setTimeout(() => { statusTimer = null; statusLine = ''; paintTicker(); }, TICKER_LINGER);
      return;
    }
    if (runTimer) clearTimeout(runTimer);
    runTimer = setTimeout(() => { runTimer = null; run = null; paintTicker(); }, TICKER_LINGER);
  }

  /* ------------------------------------------------------------- painting */

  function paint() {
    const session = ctx.current();
    const usable = !!session && ctx.live();
    if (dom.agentBtn) {
      dom.agentBtn.hidden = !session;
      dom.agentBtn.disabled = !usable;
    }
    if (dom.audioModeBtn) {
      dom.audioModeBtn.hidden = !session;
      dom.audioModeBtn.setAttribute('aria-checked', audio ? 'true' : 'false');
      // Entering auto mode with the socket down would hand somebody two
      // buttons that cannot do anything, so it is unavailable - but *leaving*
      // it always works, because auto mode is the mode in which nothing on
      // this page can be typed, and being unable to get out of it during an
      // outage would be the worse trap. The same rule as the Stop above: what
      // asks the server nothing stays available.
      dom.audioModeBtn.disabled = !usable && !audio;
    }
    // The stop is the same button: what is being asked for while the voice is
    // reading is silence, and a second control for it is a second thing to
    // find on a screen nobody is looking at.
    if (dom.statusBtn) {
      setClass(dom.statusBtn, 'speaking', speaking);
      // The spinner is the answer to "did that tap land?", and it is on the
      // button that was tapped rather than somewhere else on the page.
      setClass(dom.statusBtn, 'working', busyStatus);
      const label = speaking ? 'Stop reading' : 'Say what this is doing';
      dom.statusBtn.title = label;
      dom.statusBtn.setAttribute('aria-label', label);
      dom.statusBtn.hidden = !session;
      // Stopping is always available, even with the socket down: the sound is
      // already on this device and turning it off asks the server nothing.
      dom.statusBtn.disabled = speaking ? false : (!usable || busyStatus);
    }
    if (dom.audioBar) dom.audioBar.hidden = !audio || !session;
    if (dom.audioStatus) {
      dom.audioStatus.disabled = !usable && !speaking;
      dom.audioStatus.textContent = speaking ? 'Stop' : (busyStatus ? 'Asking' : 'Status');
    }
    if (dom.audioAgent) dom.audioAgent.disabled = !usable;
    chat.live();
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

  async function runStatus({ spoken = true } = {}) {
    const session = ctx.current();
    if (!session || busyStatus) return;
    // A run that is typing is halfway through a screen, and a description of
    // half a keystroke is worse than saying what is actually going on.
    if (run && !run.done) {
      ctx.notice('status', 'The agent is typing — ask again when it is done.');
      return;
    }
    busyStatus = true;
    // The first phase locally, before the request has even left: on a bad
    // connection the server's own first frame is a second away, and a tap with
    // nothing on screen reads as a tap that did not land.
    setStatusPhase('capturing', 'Reading the screen');
    paint();
    // Whether this answer reached the voice at all. say()'s finally is the
    // one place a queued auto-status is drained, so an answer that is never
    // read out - a request that failed, a screen with nothing to say about
    // it - has to drain it here instead, or the queue survives and is spoken
    // later, unasked, at the end of the next thing somebody presses Status
    // for.
    let voiced = false;
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
      if (spoken) { voiced = true; await say(text); }
    } catch (err) {
      setStatusPhase('error', assistFailed(err, 'That session could not be summarised.'));
      toast(assistFailed(err, 'That session could not be summarised.'), 'error');
    } finally {
      if (!voiced) queuedAuto = false;
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
    if (phase === 'done' || phase === 'error') linger('status');
    paintTicker();
  }

  // say reads one line and keeps the button honest while it does. A failed
  // render has already reached the page through onSpeechError, so it is not
  // said twice.
  async function say(text) {
    // Every line read out belongs to a generation, the way voice.js's own
    // utterances do. A second say() - a run's summary landing while an
    // auto-status is still playing - takes the voice over, and the one it
    // interrupted must not clear a flag, or drain a queue, that now belongs
    // to a sentence still being spoken.
    const mine = ++sayGen;
    speaking = true;
    paint();
    try {
      await speak(text);
    } catch { /* onSpeechError said why */ } finally {
      if (mine === sayGen) {
        speaking = false;
        paint();
        if (queuedAuto) {
          queuedAuto = false;
          // Out of this call stack: the runStatus awaiting this say is about
          // to run its own finally, and it would clear the busy flag the
          // queued one has just set.
          setTimeout(() => runStatus({ spoken: true }), 0);
        }
      }
    }
  }

  function stopReading() {
    queuedAuto = false;
    sayGen++;
    stopSpeaking();
    speaking = false;
    paint();
  }

  /* ---------------------------------------------------------- audio mode */

  function setAudio(on) {
    audio = !!on;
    try { localStorage.setItem(AUDIO_KEY, audio ? 'on' : 'off'); } catch { /* a private window still works */ }
    setClass(document.body, 'audio-mode', audio);
    // The whole of the promise: with auto mode on, nothing on this page can
    // open a keyboard - not the composer, not the key bar, and not the
    // terminal's own hidden field. Leaving it gives all three back, and the
    // pane takes the focus again: somebody who has just switched typing back
    // on means to type, and making them tap the pane first would be one tap
    // for nothing.
    ctx.setTyping(!audio);
    if (!audio) ctx.focusTerm();
    chat.audioChanged();
    paint();
    // The pane is not clipped by the bar above it, it is measured against it.
    ctx.refit();
  }

  /* ----------------------------------------------------------- the chat */

  const chat = mountChat({
    dom,
    current: () => ctx.current(),
    live: () => ctx.live(),
    audio: () => audio,
    refit: () => ctx.refit(),
    say: (text) => say(text),
  });

  /* --------------------------------------------------------------- wiring */

  if (dom.statusBtn) {
    dom.statusBtn.addEventListener('click', () => {
      if (speaking) stopReading(); else runStatus();
    });
  }
  // The former Agent button. It opens the conversation rather than a one-field
  // dialog, because "what should I do?" is a question and a question has an
  // answer, not a form.
  if (dom.agentBtn) dom.agentBtn.addEventListener('click', () => chat.open());
  if (dom.audioModeBtn) dom.audioModeBtn.addEventListener('click', () => setAudio(!audio));
  if (dom.audioStatus) {
    dom.audioStatus.addEventListener('click', () => {
      if (speaking) stopReading(); else runStatus();
    });
  }
  // In the car the same panel is wanted with the microphone already running:
  // opening it and then finding the button is two taps for one sentence.
  if (dom.audioAgent) dom.audioAgent.addEventListener('click', () => chat.open({ dictate: true }));

  setClass(document.body, 'audio-mode', audio);
  ctx.setTyping(!audio);
  paint();

  return {
    /** attached is a session becoming the one on screen, or nothing being. */
    attached() {
      if (statusTimer) { clearTimeout(statusTimer); statusTimer = null; }
      if (runTimer) { clearTimeout(runTimer); runTimer = null; }
      run = null;
      statusLine = '';
      stopReading();
      chat.attached();
      ctx.setTyping(!audio);
      paint();
    },

    /** live repaints what an outage takes away. */
    live() { paint(); },

    /**
     * activity is one committed change, already merged into the page's map.
     *
     * Two things happen here. The ticker follows the attached session's state
     * continuously while auto mode is on, and a session that stops working
     * says what it did out loud - only the session being looked at, because
     * three of them talking over each other in a car is worse than silence.
     */
    activity(id, next, prev) {
      const session = ctx.current();
      if (!session || session.id !== id) return;
      paintTicker();
      if (!audio) return;
      // A run that is still typing owns this line and this voice: every step
      // of it ends in a busy-to-idle of the session, runStatus would refuse
      // and overwrite the notice with the refusal, and the sentence audio mode
      // wants is the summary the run itself posts when it ends.
      if (run && !run.done) return;
      if (!prev || prev.state !== 'busy' || next.state === 'busy') return;
      if (speaking || busyStatus) { queuedAuto = true; return; }
      runStatus();
    },

    /** statusFrame is one phase of a status being made, from the server. */
    statusFrame(frame) {
      setStatusPhase(frame.phase || '', String(frame.text || '').trim());
    },

    /** chatFrame is one message of this session's conversation. */
    chatFrame(frame) {
      if (frame && frame.msg) chat.message(frame.msg);
    },

    /** helloChat is the conversation a fresh socket found. */
    helloChat(list) { chat.history(list); },

    /** agentFrame is one phase change of the run on this session. */
    agentFrame(frame) {
      if (runTimer) { clearTimeout(runTimer); runTimer = null; }
      run = {
        run_id: frame.run_id || '', step: frame.step || 0, phase: frame.phase || '',
        action: frame.action || '', note: frame.note || '', prompt: frame.prompt || '',
        summary: frame.summary || '', done: !!frame.done, error: frame.error || '',
      };
      chat.run(run);
      // The words a run ended with are the chat's, which stores them; the
      // ticker only says that it ended, and then gives the window back.
      if (run.done) linger('run');
      paintTicker();
    },

    /**
     * helloAgent is the run a fresh socket found, or null.
     *
     * A run lives in the server's memory only, so a server that restarted
     * mid-run answers null - and a progress line left over from before it
     * would go on claiming a run that no longer exists.
     */
    helloAgent(payload) {
      if (payload && payload.run_id) { this.agentFrame(payload); return; }
      if (run && !run.done) {
        run = null;
        chat.runGone();
        paintTicker();
        toast('That run is no longer running.');
      }
    },

    /** on says whether audio mode is on, for the code that measures the pane. */
    on() { return audio; },
  };
}

// assistFailed is what a refused request says out loud. A lost connection says
// so in its own words; anything else the server said is a sentence with an
// identifier in it, and those belong behind an "i" or nowhere.
function assistFailed(err, sentence) {
  if (isOffline(err)) return errorMessage(err);
  if (isBusyConflict(err)) return 'That session already has a run going.';
  // A 400 from these routes is the one case where the server's own words are
  // the instruction - "open /admin and pick a model" - and there is nothing
  // else the page could say instead.
  if (err && err.status === 400) return errorMessage(err);
  return sentence;
}
