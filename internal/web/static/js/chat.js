// The conversation beside the terminal.
//
// It replaces the one-field "What should it do?" dialog with the thing that
// dialog was a single turn of: you ask the agent something, it answers, and
// when the answer needs the terminal it drives the terminal and says so in the
// same thread. Which of the two it does is the model's decision (server side,
// chat.go), not a second button.
//
// The input row is a field and a Send, and nothing else. Speaking is a pill in
// the header instead, big enough for a thumb in a car, and it opens a sheet
// with the level it is hearing and the two endings a recording has.
//
// Whether answers are read out loud is one switch beside it, off until it is
// asked for, and dictating once asks for it. It is a switch and not a property
// of each question because the two things a person does in a car - speak, and
// listen - are the same decision, and a rule that had to be re-earned by every
// question meant a typed follow-up in a car came back silently.

import { api, el, toast, setClass, errorMessage, isOffline, fmtClock } from './api.js';
import { dictateOnce } from './voice.js';

const path = (id, suffix) => '/api/sessions/' + encodeURIComponent(id) + suffix;

// How long a second tap on a bubble is still part of the first one. A touch
// screen has no dblclick of its own, so the gesture is measured here; 350 ms
// is what the platforms themselves use.
const DOUBLE_TAP = 350;

// How long after a tap a mouse event is that tap being replayed. A touch
// screen does synthesize click and dblclick from taps, after the fact, and a
// gesture counted twice would read an answer out loud and stop it again in
// the same movement.
const MOUSE_AFTER_TOUCH = 700;

// What a phase of a run is called in the one line a run message carries.
const RUN_WORDS = {
  thinking: 'thinking',
  acting: 'working',
  waiting: 'waiting for the session',
  done: 'done',
  error: 'stopped',
};

// Whether answers are read out loud, remembered per device and per browser.
const SPEAK_KEY = 'socrates.chat.speak';

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

// How many bars the level meter has, and how far a frame may fall. The decay
// is what makes a voice look like a voice: the peak of a syllable stays up for
// a moment instead of flickering out between two of them.
const METER_BARS = 15;
const METER_DECAY = 0.06;

/**
 * mountChat wires the panel to one page.
 *
 * `ctx` is what the panel is not allowed to own:
 *   dom      the shared id map
 *   current  the session on screen, or null
 *   live     whether the socket is up
 *   refit    re-measure the pane after the layout changed
 *   say      read one line out loud
 *   read     read one line out loud, or stop if something is being read
 */
export function mountChat(ctx) {
  const { dom } = ctx;

  let messages = [];
  // Every message already on screen, by the timestamp the server gave it: the
  // POST answers with the message it stored and the socket broadcasts the
  // same one, and a chat that showed both would stutter every question.
  const seen = new Set();
  // The live operator runs this conversation started, by id.
  const runs = new Map();
  let open = false;
  let thinking = false;
  let sending = false;
  // The recording that is running, as the two endings it has, or null, and
  // whether one is being opened - the microphone takes a moment to answer,
  // and a second tap in that moment must not open a second one.
  let dictation = null;
  let opening = false;
  // Whether the transcript of a finished recording is still on its way. The
  // pill says so and stays shut until it lands.
  let transcribing = false;
  // Whether answers are read out loud. Per device, like the header's sound and
  // notification switches, and for the same reason: it is about this phone's
  // speaker, not about the account.
  let speaking = stored(SPEAK_KEY, false);
  let loadedFor = '';
  // The row under the log, built once so that what is half typed into it
  // survives everything that redraws the conversation above it.
  const foot = {};

  /* ------------------------------------------------------------ the input */

  // The row is built once, so that what is half typed into it survives
  // everything above it being redrawn.
  function buildFoot() {
    dom.chatFoot.innerHTML = '';
    const input = el('input', {
      class: 'input', type: 'text', id: 'chatText',
      autocorrect: 'off', autocapitalize: 'sentences', autocomplete: 'off',
      enterkeyhint: 'send', placeholder: 'Ask about this session…',
    });
    input.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return;
      event.preventDefault();
      submit(input.value, input);
    });
    const send = el('button', {
      class: 'btn primary', type: 'button', id: 'chatSend', text: 'Send',
      onclick: () => submit(input.value, input),
    });
    Object.assign(foot, { input, send });
    dom.chatFoot.append(el('div', { class: 'chat-compose' }, input, send));
    paint();
  }

  /* ----------------------------------------------------------- the meter */

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
    const host = dom.chatRecMeter;
    if (!host) return;
    const plain = calm.matches;
    const count = plain ? 1 : METER_BARS;
    setClass(host, 'plain', plain);
    if (bars.length === count && host.childNodes.length === count) return;
    host.innerHTML = '';
    bars = [];
    for (let i = 0; i < count; i += 1) {
      const bar = el('span', { class: 'rec-bar' });
      bars.push(bar);
      host.append(bar);
    }
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

  /* ------------------------------------------------------- the recording */

  function closeSheet() {
    stopMeter();
    if (dom.chatRecSheet && dom.chatRecSheet.open) dom.chatRecSheet.close();
  }

  // dictate records one question and sends it as one. Dictating is also how the
  // voice is asked for: somebody who is talking to the page is not reading it.
  async function dictate() {
    if (dictation || opening || transcribing) return;
    opening = true;
    paint();
    try {
      const text = await dictateOnce({
        onTime: (secs) => {
          if (dom.chatRecTime) dom.chatRecTime.textContent = fmtClock(secs);
        },
        onReady: (ends) => {
          dictation = ends;
          if (dom.chatRecSheet && !dom.chatRecSheet.open) dom.chatRecSheet.showModal();
          startMeter(ends.analyser);
          paint();
        },
      });
      // The microphone is closed by now either way, so the sheet has nothing
      // left to show: what happens next happens at the pill.
      dictation = null;
      transcribing = false;
      closeSheet();
      paint();
      // A discarded recording resolves to nothing, and nothing is what it
      // does: no request, no message, and nothing said about it.
      if (text) {
        setSpeaking(true);
        await submit(text, null);
      }
    } catch (err) {
      toast((err && err.userMessage) || errorMessage(err), 'error');
    } finally {
      dictation = null;
      transcribing = false;
      opening = false;
      closeSheet();
      if (dom.chatRecTime) dom.chatRecTime.textContent = fmtClock(0);
      paint();
    }
  }

  /* ------------------------------------------------------- the loudspeaker */

  function setSpeaking(on) {
    if (speaking === !!on) return;
    speaking = !!on;
    remember(SPEAK_KEY, speaking);
    paintSpeak();
  }

  function paintSpeak() {
    const btn = dom.chatSpeak;
    if (!btn) return;
    const words = speaking ? 'Answers are read aloud' : 'Answers are not read aloud';
    btn.setAttribute('aria-pressed', speaking ? 'true' : 'false');
    btn.title = words;
    btn.setAttribute('aria-label', words);
  }

  // submit sends one question. `auto` in the body is the server's instruction
  // to phrase the answer for the ear - one or two spoken sentences, no code
  // (chat.go, chatSystemPrompt) - so it is exactly the loudspeaker switch: an
  // answer that is about to be read out is written to be listened to, and one
  // that is not keeps the code in it. It is read at send time rather than at
  // arrival, because that is when the phrasing is decided.
  async function submit(raw, field) {
    const text = String(raw || '').trim();
    const session = ctx.current();
    if (!text || !session) return;
    // A dictated message has no field to stay in: the words exist only in this
    // call, so a socket that went down while the microphone was running would
    // take them with it, in silence. They are put in the log instead, with the
    // reason they did not go - the same rule the rest of the page's input
    // follows.
    if (sending || !ctx.live()) {
      messages = messages.concat([
        { role: 'user', text, ts: Date.now() },
        {
          role: 'assistant',
          text: ctx.live()
            ? 'That was not sent: the question before it is still being answered.'
            : 'That was not sent: this device has no connection. Ask again when it is back.',
          failed: true,
          ts: Date.now(),
        },
      ]);
      render();
      return;
    }
    sending = true;
    thinking = true;
    if (field) field.value = '';
    render();
    paint();
    try {
      const data = await api(path(session.id, '/chat'), {
        method: 'POST', attempts: 1, timeout: 30000, body: { text, auto: speaking },
      });
      // The socket usually beats this, and take() drops whichever arrives
      // second. With no socket at all it is the only copy there is.
      if (data && data.msg) take(data.msg);
    } catch (err) {
      thinking = false;
      // Nothing was asked, so nothing was said: the words go back in the field
      // they were cleared out of. Retyping a question because the key was not
      // configured yet is a punishment for the server's problem.
      if (field && !field.value) field.value = text;
      // A 400 from this route is the one case where the server's own words are
      // the instruction - "open /admin and pick an agent model" - so they are
      // shown where the answer was expected rather than in a toast that goes.
      const why = (err && err.status === 400) || isOffline(err)
        ? errorMessage(err) : 'That question could not be sent.';
      messages = messages.concat([{ role: 'assistant', text: why, failed: true, ts: Date.now() }]);
      render();
    } finally {
      sending = false;
      paint();
    }
  }

  /* --------------------------------------------------------- the messages */

  function keyOf(msg) { return (msg.role || '') + '|' + (msg.ts || 0) + '|' + (msg.text || ''); }

  // take adds one message unless it is already here. The reply is what ends
  // the thinking placeholder.
  function take(msg) {
    if (!msg || !msg.text) return;
    const key = keyOf(msg);
    if (seen.has(key)) return;
    seen.add(key);
    messages = messages.concat([msg]);
    if (msg.role === 'assistant') {
      thinking = false;
      // The switch is the whole rule: with it on every answer is read out, and
      // with it off none is. An answer that failed is a sentence about this
      // page rather than an answer, and is not read.
      if (speaking && !msg.failed) ctx.say(msg.text);
    }
    render();
  }

  function replace(list) {
    messages = Array.isArray(list) ? list.slice() : [];
    seen.clear();
    for (const msg of messages) seen.add(keyOf(msg));
    // The stored conversation is the record, so it also settles whether an
    // answer is still owed. Without this, a socket that dropped between the
    // question and the answer came back with the answer in the log and the
    // "Thinking…" placeholder still sitting under it, for ever: the frame that
    // would have cleared it is the one the outage ate.
    const last = messages[messages.length - 1];
    if (last && last.role === 'assistant') thinking = false;
    render();
  }

  // renderText is markdown-lite and nothing more: blank lines are paragraphs
  // and backticks are code. A model told to answer plainly still writes the
  // occasional `--flag`, and rendering that as a word with two grave accents
  // round it is worse than the four lines it costs to do properly. Everything
  // else - headings, lists, links, images - stays literal, because a chat
  // beside a terminal is not a document viewer.
  function renderText(host, text) {
    for (const block of String(text).split(/\n{2,}/)) {
      const para = el('p');
      const parts = block.split(/`([^`\n]+)`/);
      for (let i = 0; i < parts.length; i += 1) {
        if (parts[i] === '') continue;
        para.append(i % 2 ? el('code', { text: parts[i] }) : document.createTextNode(parts[i]));
      }
      if (para.childNodes.length) host.append(para);
    }
    if (!host.childNodes.length) host.append(el('p', { text: String(text) }));
  }

  // runRow is the progress of the run a message started, and the way to stop
  // it. It is inside the bubble that asked for it, so a conversation with two
  // runs in it never has to say which one is which.
  function runRow(runId) {
    const run = runs.get(runId);
    if (!run || run.done) return null;
    const step = Math.max(1, Number(run.step) || 1);
    const said = String(run.action || '').trim() || RUN_WORDS[run.phase] || 'working';
    return el('div', { class: 'chat-run' },
      el('span', { class: 'chat-run-line', text: 'Step ' + step + ' · ' + said }),
      el('button', {
        class: 'btn sm', type: 'button', text: 'Cancel',
        onclick: () => cancelRun(),
      }));
  }

  // hearable makes one answer readable out loud by the gesture that means
  // "again, but say it": a double-tap. It is the way back to the voice for an
  // answer that arrived while somebody was looking at the screen and is now
  // being read in a car - and the same gesture while it is being read is
  // silence, because that is what is wanted at the second tap.
  function hearable(bubble, text) {
    bubble.title = 'Double-tap to read aloud';
    // The second tap is timed here, because a touch screen has no double-tap
    // gesture of its own to listen for. It is deliberately not a
    // preventDefault: selecting a sentence out of an answer has to keep
    // working, exactly as a double-click already lets it.
    let last = 0;
    let touched = 0;
    bubble.addEventListener('touchend', () => {
      const now = Date.now();
      touched = now;
      if (now - last <= DOUBLE_TAP) { last = 0; ctx.read(text); return; }
      last = now;
    });
    bubble.addEventListener('dblclick', () => {
      if (Date.now() - touched <= MOUSE_AFTER_TOUCH) return;
      ctx.read(text);
    });
  }

  function render() {
    const log = dom.chatLog;
    const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 40;
    log.innerHTML = '';
    if (!messages.length && !thinking) {
      log.append(el('div', {
        class: 'chat-empty',
        text: 'Ask what this session is doing, or what to do next. It can also do it for you.',
      }));
    }
    for (const msg of messages) {
      const bubble = el('div', {
        class: 'chat-msg ' + (msg.role === 'user' ? 'user' : 'assistant') + (msg.failed ? ' failed' : ''),
      });
      renderText(bubble, msg.text);
      const row = msg.run_id ? runRow(msg.run_id) : null;
      if (row) bubble.append(row);
      if (msg.role !== 'user' && !msg.failed) hearable(bubble, msg.text);
      log.append(bubble);
    }
    if (thinking) log.append(el('div', { class: 'chat-msg assistant thinking', text: 'Thinking…' }));
    if (atEnd || thinking) log.scrollTop = log.scrollHeight;
  }

  async function cancelRun() {
    const session = ctx.current();
    if (!session) return;
    try {
      await api(path(session.id, '/agent/cancel'), { method: 'POST', attempts: 1, timeout: 15000 });
    } catch (err) {
      toast(isOffline(err) ? errorMessage(err) : 'That run could not be stopped.', 'error');
    }
  }

  /* ------------------------------------------------------------- painting */

  function paint() {
    if (!foot.input) return;
    const usable = !!ctx.current() && ctx.live();
    foot.input.disabled = !usable;
    foot.send.disabled = !usable || sending;
    if (dom.chatMic) {
      // The pill is shut while a recording is being opened, is running, or is
      // being turned into words: all three are one recording, and a second tap
      // in any of them would start a second microphone.
      dom.chatMic.disabled = !usable || opening || !!dictation || transcribing;
      const words = transcribing ? 'Transcribing…' : 'Speak';
      if (dom.chatMicText) dom.chatMicText.textContent = words;
      dom.chatMic.title = words;
      dom.chatMic.setAttribute('aria-label', words);
    }
  }

  async function load(session) {
    if (!session || loadedFor === session.id) return;
    loadedFor = session.id;
    try {
      const data = await api(path(session.id, '/chat'), { attempts: 1, timeout: 10000 });
      if (ctx.current() && ctx.current().id === session.id) replace((data && data.messages) || []);
    } catch { /* hello carries the same list, and the panel says nothing rather than lying */ }
  }

  /* --------------------------------------------------------------- wiring */

  if (dom.chatClose) dom.chatClose.addEventListener('click', () => handle.close());
  if (dom.chatMic) dom.chatMic.addEventListener('click', () => dictate());
  if (dom.chatSpeak) dom.chatSpeak.addEventListener('click', () => setSpeaking(!speaking));
  if (dom.chatRecSend) {
    dom.chatRecSend.addEventListener('click', () => {
      if (!dictation) return;
      // The words are on their way to the server from here, so the sheet has
      // said everything it can: the pill carries the wait.
      transcribing = true;
      stopMeter();
      dictation.stop();
      dictation = null;
      if (dom.chatRecSheet && dom.chatRecSheet.open) dom.chatRecSheet.close();
      paint();
    });
  }
  if (dom.chatRecCancel) dom.chatRecCancel.addEventListener('click', () => { if (dictation) dictation.cancel(); });
  if (dom.chatRecSheet) {
    // Escape and a tap on the backdrop are the same answer as Cancel: a sheet
    // that closed while the microphone stayed open would be a recording
    // nobody could see and nobody could stop.
    dom.chatRecSheet.addEventListener('cancel', () => { if (dictation) dictation.cancel(); });
    dom.chatRecSheet.addEventListener('close', () => {
      stopMeter();
      if (dictation) dictation.cancel();
    });
    dom.chatRecSheet.addEventListener('click', (event) => {
      if (event.target === dom.chatRecSheet && dictation) dictation.cancel();
    });
  }
  buildMeter();
  paintSpeak();
  buildFoot();

  const handle = {
    /** open shows the panel and puts the cursor where it goes. */
    open() {
      const session = ctx.current();
      if (!session) return;
      open = true;
      dom.chatPanel.hidden = false;
      ctx.refit();
      load(session);
      render();
      paint();
      foot.input.focus();
    },
    close() {
      open = false;
      dom.chatPanel.hidden = true;
      ctx.refit();
    },
    toggle() { if (open) this.close(); else this.open(); },
    isOpen() { return open; },

    /** attached is a session becoming the one on screen, or nothing being. */
    attached() {
      loadedFor = '';
      thinking = false;
      // A recording belongs to the session it was started in: sending its
      // transcript to the one that has just taken the screen would put the
      // question to the wrong agent.
      if (dictation) dictation.cancel();
      runs.clear();
      replace([]);
      if (open && !ctx.current()) this.close();
      if (open) load(ctx.current());
      paint();
    },

    /** live repaints what an outage takes away. */
    live() { paint(); },

    /** message is one chat frame off the socket. */
    message(msg) { take(msg); },

    /** history is the conversation a hello or a GET carried. */
    history(list) {
      if (!Array.isArray(list)) return;
      loadedFor = (ctx.current() || {}).id || '';
      replace(list);
    },

    /** run is one phase change of an operator run, from any of its sources. */
    run(frame) {
      if (!frame || !frame.run_id) return;
      runs.set(frame.run_id, frame);
      if (open) render();
    },

    /** runGone is a run that the server no longer knows about. */
    runGone() {
      let changed = false;
      for (const [id, run] of runs) {
        if (run.done) continue;
        runs.set(id, { ...run, done: true });
        changed = true;
      }
      if (changed && open) render();
    },
  };
  return handle;
}
