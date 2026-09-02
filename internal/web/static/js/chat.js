// The conversation beside the terminal.
//
// It replaces the one-field "What should it do?" dialog with the thing that
// dialog was a single turn of: you ask the agent something, it answers, and
// when the answer needs the terminal it drives the terminal and says so in the
// same thread. Which of the two it does is the model's decision (server side,
// chat.go), not a second button.
//
// There is one input row and everybody gets it: a field, a microphone and a
// Send. How a question was asked is what decides how it is answered - a
// question that was spoken is phrased for the ear and read out loud, a typed
// one is answered on the screen - so there is no mode to be in, only the way
// the last question happened to be asked.

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
  // How many questions asked out loud are still waiting for their answer. It
  // is a count and not a mode: a spoken question is owed a spoken answer even
  // if the next thing typed is a written one, and the answers arrive in the
  // order the questions were asked.
  let owed = 0;
  let loadedFor = '';
  // The row under the log, built once so that what is half typed into it
  // survives everything that redraws the conversation above it.
  const foot = {};

  /* ------------------------------------------------------------ the input */

  const MIC_SVG = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0 0 14 0M12 18v3"/></svg>';
  const OK_SVG = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 13l4 4L19 7"/></svg>';
  const NO_SVG = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>';

  // The row is built once and its parts are shown or hidden afterwards, so
  // that a recording started halfway through a sentence does not take the
  // sentence with it.
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
    const mic = el('button', {
      class: 'icon-btn', type: 'button', id: 'chatMic',
      title: 'Speak', 'aria-label': 'Speak', html: MIC_SVG,
      onclick: () => dictate(),
    });
    const clock = el('span', { class: 'rec-time', id: 'chatRecTime', text: fmtClock(0) });
    // A running recording has two endings and both are on screen, because
    // "I did not mean that" is the half a single Stop button could not say.
    const keep = el('button', {
      class: 'icon-btn', type: 'button', id: 'chatRecSend',
      title: 'Send recording', 'aria-label': 'Send recording', html: OK_SVG,
      onclick: () => { if (dictation) dictation.stop(); },
    });
    const drop = el('button', {
      class: 'icon-btn rec-drop', type: 'button', id: 'chatRecCancel',
      title: 'Discard recording', 'aria-label': 'Discard recording', html: NO_SVG,
      onclick: () => { if (dictation) dictation.cancel(); },
    });
    const send = el('button', {
      class: 'btn primary', type: 'button', id: 'chatSend', text: 'Send',
      onclick: () => submit(input.value, input),
    });
    const actions = el('span', { class: 'rec-actions' }, mic, clock, keep, drop);
    Object.assign(foot, { input, mic, clock, keep, drop, send, actions });
    dom.chatFoot.append(el('div', { class: 'chat-compose' }, input, actions, send));
    paint();
  }

  // dictate records one question and sends it as one. A question asked out
  // loud is answered out loud, which is what `owed` is for.
  async function dictate() {
    if (dictation || opening) return;
    opening = true;
    try {
      const text = await dictateOnce({
        onTime: (secs) => { foot.clock.textContent = fmtClock(secs); },
        onReady: (ends) => { dictation = ends; paint(); },
      });
      dictation = null;
      paint();
      // A discarded recording resolves to nothing, and nothing is what it
      // does: no request, no message, and nothing said about it.
      if (text) await submit(text, null, { spoken: true });
    } catch (err) {
      toast((err && err.userMessage) || errorMessage(err), 'error');
    } finally {
      dictation = null;
      opening = false;
      foot.clock.textContent = fmtClock(0);
      paint();
    }
  }

  // submit sends one question. `spoken` says it was dictated, which is both
  // how the server is asked to phrase the answer - short, no code, for the
  // ear - and why the answer will be read out when it arrives.
  async function submit(raw, field, { spoken = false } = {}) {
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
    if (spoken) owed += 1;
    if (field) field.value = '';
    render();
    paint();
    try {
      const data = await api(path(session.id, '/chat'), {
        method: 'POST', attempts: 1, timeout: 30000, body: { text, auto: spoken },
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
      // The answer that is not coming is not owed a voice either.
      if (owed) owed -= 1;
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
      // The oldest question asked out loud is the one this answers.
      if (owed) {
        owed -= 1;
        if (!msg.failed) ctx.say(msg.text);
      }
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
    const recording = !!dictation;
    foot.input.disabled = !usable;
    foot.send.disabled = !usable || sending;
    foot.mic.disabled = !usable;
    // While it is recording, the microphone is the two endings instead: what
    // is being asked for at that moment is send it or throw it away, and
    // both of them are always available - the microphone is on this device
    // and turning it off asks the server nothing.
    foot.mic.hidden = recording;
    foot.clock.hidden = !recording;
    foot.keep.hidden = !recording;
    foot.drop.hidden = !recording;
    setClass(foot.actions, 'rec', recording);
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
      owed = 0;
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
