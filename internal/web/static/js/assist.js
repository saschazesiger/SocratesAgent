// Status, Agent and audio mode: the three things you can ask a session for
// without typing into it.
//
// They are one module because they are one idea. A terminal is a picture, and
// a picture is the one thing a phone in a car cannot use: Status turns the
// screen into a sentence and reads it out loud, Agent turns a sentence back
// into keystrokes, and audio mode is the layout where those two are the whole
// interface and the terminal is merely still there, honest and shorter.
//
// Everything here talks to the page through the handle `mountAssist` is given.
// The sidebar, the socket and the frames stay in session.js; what lives here
// is what the three buttons do, and the one rule that ties them together - in
// audio mode, a session that stops working says so by itself.

import { api, el, toast, setClass, errorMessage, isOffline, isBusyConflict } from './api.js';
import { speak, stopSpeaking, onSpeechError, dictateOnce } from './voice.js';

// Per device, not per account: whether this phone is the one in the car is a
// fact about the phone.
const AUDIO_KEY = 'socrates.audio.mode';

// How long a finished run stays on screen before it fades. Long enough to read
// one sentence, short enough that it is gone before it is in the way.
const DONE_LINGER = 6000;

// What a phase is called in the one line the progress notice gets. The
// technical words - the phase itself, the run id, the model's note - go behind
// the "i" beside it, like every other identifier on this page.
const PHASE_WORDS = {
  thinking: 'thinking',
  acting: 'working',
  waiting: 'waiting for the session',
  done: 'done',
  error: 'stopped',
};

const path = (id, suffix) => '/api/sessions/' + encodeURIComponent(id) + suffix;

function fmtClock(seconds) {
  const whole = Math.max(0, Math.floor(seconds));
  return Math.floor(whole / 60) + ':' + String(whole % 60).padStart(2, '0');
}

// audioWanted is read before anything is mounted, because the class it decides
// is a layout and a layout applied late is a terminal that resizes under the
// person looking at it.
export function audioWanted() {
  try { return localStorage.getItem(AUDIO_KEY) === 'on'; } catch { return false; }
}

/**
 * mountAssist wires the three buttons to one session page.
 *
 * `ctx` is everything this module is not allowed to own:
 *   dom      the shared id map
 *   notice   session.js's one-line banner, (kind, text, onDismiss, facts, extra)
 *   refit    re-measure the pane after the layout changed
 *   current  the session on screen, or null
 *   live     whether the socket is up
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
  let run = null;
  let doneTimer = null;
  let stopDictation = null;

  /* ------------------------------------------------------------ painting */

  function paint() {
    const session = ctx.current();
    const usable = !!session && ctx.live();
    if (dom.agentBtn) {
      dom.agentBtn.hidden = !session;
      dom.agentBtn.disabled = !usable;
    }
    if (dom.audioModeBtn) {
      dom.audioModeBtn.hidden = !session;
      dom.audioModeBtn.setAttribute('aria-pressed', audio ? 'true' : 'false');
      setClass(dom.audioModeBtn, 'on', audio);
    }
    // The stop is the same button: what is being asked for while the voice is
    // reading is silence, and a second control for it is a second thing to
    // find on a screen nobody is looking at.
    if (dom.statusBtn) {
      setClass(dom.statusBtn, 'speaking', speaking);
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
      dom.audioStatus.textContent = speaking ? 'Stop' : (busyStatus ? 'Asking…' : 'Status');
    }
    if (dom.audioAgent) {
      dom.audioAgent.disabled = !usable && !stopDictation;
      if (!stopDictation) dom.audioAgent.textContent = 'Agent';
    }
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
    paint();
    try {
      const data = await api(path(session.id, '/status'), {
        method: 'POST', attempts: 1, timeout: 120000,
      });
      const text = String((data && data.text) || '').trim();
      if (!text) { toast('There was nothing to say about that screen.'); return; }
      ctx.notice('status', text, null, statusFacts(data || {}));
      busyStatus = false;
      paint();
      if (spoken) await say(text);
    } catch (err) {
      toast(assistFailed(err, 'That session could not be summarised.'), 'error');
    } finally {
      busyStatus = false;
      paint();
    }
  }

  // say reads one line and keeps the button honest while it does. A failed
  // render has already reached the page through onSpeechError, so it is not
  // said twice.
  async function say(text) {
    speaking = true;
    paint();
    try {
      await speak(text);
    } catch { /* onSpeechError said why */ } finally {
      speaking = false;
      paint();
      if (queuedAuto) { queuedAuto = false; runStatus({ spoken: true }); }
    }
  }

  function stopReading() {
    queuedAuto = false;
    stopSpeaking();
    speaking = false;
    paint();
  }

  /* --------------------------------------------------------------- agent */

  // promptFor is the one-field dialog the Agent button opens on a device with
  // a keyboard. It is built the way renameSession's is - the app's own shape,
  // not the browser's, which a phone renders as a system alert.
  function promptFor() {
    return new Promise((resolve) => {
      const dialog = el('dialog', { class: 'modal' });
      const input = el('input', {
        class: 'input', type: 'text', placeholder: 'Pick the fastest model and send the prompt',
      });
      const cancel = el('button', { class: 'btn sm', type: 'button', text: 'Cancel' });
      const accept = el('button', { class: 'btn sm primary', type: 'button', text: 'Run' });
      dialog.append(
        el('h2', { class: 'modal-title', text: 'What should it do?' }),
        el('p', { class: 'modal-body', text: 'It reads the screen and types for you, one small step at a time. You can stop it at any point.' }),
        el('div', { class: 'field' }, input),
        el('div', { class: 'modal-actions' }, cancel, accept),
      );
      let settled = false;
      const finish = (value) => {
        if (settled) return;
        settled = true;
        resolve(value);
        dialog.close();
      };
      accept.addEventListener('click', () => finish(input.value.trim() || null));
      cancel.addEventListener('click', () => finish(null));
      input.addEventListener('keydown', (event) => {
        if (event.key === 'Enter') { event.preventDefault(); finish(input.value.trim() || null); }
      });
      dialog.addEventListener('cancel', (event) => { event.preventDefault(); finish(null); });
      dialog.addEventListener('close', () => { finish(null); dialog.remove(); });
      document.body.append(dialog);
      dialog.showModal();
      input.focus();
    });
  }

  async function startAgent(prompt) {
    const session = ctx.current();
    if (!session || !prompt) return;
    try {
      const data = await api(path(session.id, '/agent'), {
        method: 'POST', attempts: 1, timeout: 30000, body: { prompt },
      });
      // The first frame may be a moment away, and a tap with nothing on screen
      // reads as a tap that did not land. So the run starts here, from what
      // the POST already knows.
      setRun({
        run_id: (data && data.run_id) || '', step: 1, phase: 'thinking',
        action: '', note: '', prompt, summary: '', done: false, error: '',
      });
    } catch (err) {
      toast(assistFailed(err, 'That run could not be started.'), 'error');
    }
  }

  async function cancelAgent() {
    const session = ctx.current();
    if (!session) return;
    try {
      await api(path(session.id, '/agent/cancel'), { method: 'POST', attempts: 1, timeout: 15000 });
    } catch (err) {
      toast(assistFailed(err, 'That run could not be stopped.'), 'error');
    }
  }

  function setRun(next) {
    if (doneTimer) { clearTimeout(doneTimer); doneTimer = null; }
    run = next;
    renderRun();
  }

  // renderRun is the progress line: one sentence about the step it is on, a
  // Cancel beside it, and the identifiers behind the "i".
  function renderRun() {
    if (!run) return;
    const facts = [
      run.run_id ? 'run ' + run.run_id : '',
      run.phase ? 'phase ' + run.phase : '',
      run.note || '',
      run.prompt ? 'goal: ' + run.prompt : '',
    ].filter(Boolean);

    if (run.phase === 'error' || run.error) {
      ctx.notice('agent', 'The run stopped before it finished.', null, facts);
      toast(run.error || 'The run stopped before it finished.', 'error');
      run = { ...run, done: true };
      fadeSoon();
      return;
    }
    if (run.done) {
      const summary = String(run.summary || '').trim() || 'Done.';
      ctx.notice('agent', summary, null, facts);
      fadeSoon();
      if (audio) say(summary);
      return;
    }
    const step = Math.max(1, Number(run.step) || 1);
    const said = String(run.action || '').trim() || PHASE_WORDS[run.phase] || 'working';
    const cancel = el('button', {
      class: 'btn sm', type: 'button', text: 'Cancel', onclick: () => cancelAgent(),
    });
    ctx.notice('agent', 'Step ' + step + ' · ' + said, null, facts, cancel);
  }

  function fadeSoon() {
    if (doneTimer) clearTimeout(doneTimer);
    doneTimer = setTimeout(() => {
      doneTimer = null;
      if (dom.termNotice && dom.termNotice.dataset.kind === 'agent') dom.termNotice.hidden = true;
    }, DONE_LINGER);
  }

  /* ---------------------------------------------------------- audio mode */

  function setAudio(on) {
    audio = !!on;
    try { localStorage.setItem(AUDIO_KEY, audio ? 'on' : 'off'); } catch { /* a private window still works */ }
    setClass(document.body, 'audio-mode', audio);
    paint();
    // The pane is not clipped by the bar above it, it is measured against it.
    ctx.refit();
  }

  // The Agent button in audio mode records instead of asking: the words are
  // the instruction, and a confirmation step is one tap too many for somebody
  // who is driving. What was heard is shown in the progress line, so a
  // misheard goal is visible rather than merely obeyed.
  async function dictateAgent() {
    if (stopDictation) {
      const stop = stopDictation;
      stopDictation = null;
      stop();
      return;
    }
    setClass(dom.audioAgent, 'rec', true);
    dom.audioAgent.textContent = 'Stop';
    try {
      const text = await dictateOnce({
        onTime: (secs) => {
          if (stopDictation) dom.audioAgent.textContent = 'Stop · ' + fmtClock(secs);
        },
        onReady: (stop) => { stopDictation = stop; },
      });
      await startAgent(text);
    } catch (err) {
      toast((err && err.userMessage) || errorMessage(err), 'error');
    } finally {
      stopDictation = null;
      setClass(dom.audioAgent, 'rec', false);
      dom.audioAgent.textContent = 'Agent';
      paint();
    }
  }

  /* --------------------------------------------------------------- wiring */

  if (dom.statusBtn) {
    dom.statusBtn.addEventListener('click', () => {
      if (speaking) stopReading(); else runStatus();
    });
  }
  if (dom.agentBtn) {
    dom.agentBtn.addEventListener('click', async () => {
      const prompt = await promptFor();
      if (prompt) startAgent(prompt);
    });
  }
  if (dom.audioModeBtn) dom.audioModeBtn.addEventListener('click', () => setAudio(!audio));
  if (dom.audioStatus) {
    dom.audioStatus.addEventListener('click', () => {
      if (speaking) stopReading(); else runStatus();
    });
  }
  if (dom.audioAgent) dom.audioAgent.addEventListener('click', () => dictateAgent());

  setClass(document.body, 'audio-mode', audio);
  paint();

  return {
    /** attached is a session becoming the one on screen, or nothing being. */
    attached() {
      if (doneTimer) { clearTimeout(doneTimer); doneTimer = null; }
      run = null;
      stopReading();
      paint();
    },

    /** live repaints what an outage takes away. */
    live() { paint(); },

    /**
     * activity is one committed change, already merged into the page's map.
     *
     * The only thing this module does with it is audio mode's own rule: a
     * session that stops working says what it did, and only the session being
     * looked at - three of them talking over each other in a car is worse
     * than silence, and they are all visible in the list anyway.
     */
    activity(id, next, prev) {
      if (!audio) return;
      const session = ctx.current();
      if (!session || session.id !== id) return;
      if (!prev || prev.state !== 'busy' || next.state === 'busy') return;
      if (speaking || busyStatus) { queuedAuto = true; return; }
      runStatus();
    },

    /** agentFrame is one phase change of the run on this session. */
    agentFrame(frame) {
      setRun({
        run_id: frame.run_id || '', step: frame.step || 0, phase: frame.phase || '',
        action: frame.action || '', note: frame.note || '', prompt: frame.prompt || '',
        summary: frame.summary || '', done: !!frame.done, error: frame.error || '',
      });
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
        if (dom.termNotice && dom.termNotice.dataset.kind === 'agent') dom.termNotice.hidden = true;
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
  // A 400 from these two routes is the one case where the server's own words
  // are the instruction - "open /admin and pick a model" - and there is
  // nothing else the page could say instead.
  if (err && err.status === 400) return errorMessage(err);
  return sentence;
}
