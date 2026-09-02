// The conversation beside the terminal.
//
// It replaces the one-field "What should it do?" dialog with the thing that
// dialog was a single turn of: you ask the agent something, it answers, and
// when the answer needs the terminal it drives the terminal and says so in the
// same thread. Which of the two it does is the model's decision (server side,
// chat.go), not a second button.
//
// There are two input rows and never both. With auto mode off it is a text
// field and a Send. With auto mode on it is one microphone the size of a
// thumb and *no focusable text input anywhere in this panel*, because the
// promise of that mode is that a phone in a car never opens a keyboard.

import { api, el, toast, setClass, errorMessage, isOffline, fmtClock } from './api.js';
import { dictateOnce } from './voice.js';

const path = (id, suffix) => '/api/sessions/' + encodeURIComponent(id) + suffix;

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
 *   audio    whether auto mode is on
 *   refit    re-measure the pane after the layout changed
 *   say      read one line out loud
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
  let stopDictation = null;
  let loadedFor = '';

  /* ------------------------------------------------------------ the input */

  // The text row: a field, and Enter sends. It exists only when auto mode is
  // off, so that "no focusable text input" is a fact about the DOM and not a
  // rule somebody has to remember.
  function textRow() {
    const input = el('input', {
      class: 'input', type: 'text', id: 'chatText',
      autocorrect: 'off', autocapitalize: 'sentences', autocomplete: 'off',
      enterkeyhint: 'send', placeholder: 'Ask about this session…',
    });
    const send = el('button', {
      class: 'btn primary', type: 'button', id: 'chatSend', text: 'Send',
      onclick: () => submit(input.value, input),
    });
    input.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return;
      event.preventDefault();
      submit(input.value, input);
    });
    return el('div', { class: 'chat-compose' }, input, send);
  }

  // The microphone row: one button, tap to start, tap again to stop, and what
  // was heard is the message. No confirmation step - the whole point of this
  // mode is that it costs one tap.
  function micRow() {
    const mic = el('button', {
      class: 'chat-mic', type: 'button', id: 'chatMic',
      html: '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0 0 14 0M12 18v3"/></svg>',
    });
    mic.append(el('span', { class: 'chat-mic-word', text: 'Speak' }));
    mic.addEventListener('click', () => dictate(mic));
    return mic;
  }

  function buildFoot() {
    dom.chatFoot.innerHTML = '';
    dom.chatFoot.append(ctx.audio() ? micRow() : textRow());
    paint();
  }

  async function dictate(mic) {
    if (stopDictation) {
      const stop = stopDictation;
      stopDictation = null;
      stop();
      return;
    }
    const word = mic.querySelector('.chat-mic-word');
    setClass(mic, 'rec', true);
    word.textContent = 'Stop';
    try {
      const text = await dictateOnce({
        onTime: (secs) => { if (stopDictation) word.textContent = 'Stop · ' + fmtClock(secs); },
        onReady: (stop) => { stopDictation = stop; },
      });
      await submit(text, null);
    } catch (err) {
      toast((err && err.userMessage) || errorMessage(err), 'error');
    } finally {
      stopDictation = null;
      setClass(mic, 'rec', false);
      word.textContent = 'Speak';
      paint();
    }
  }

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
        method: 'POST', attempts: 1, timeout: 30000, body: { text, auto: !!ctx.audio() },
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
      if (ctx.audio() && !msg.failed) ctx.say(msg.text);
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

  function render() {
    const log = dom.chatLog;
    const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 40;
    log.innerHTML = '';
    if (!messages.length && !thinking) {
      log.append(el('div', {
        class: 'chat-empty',
        text: ctx.audio()
          ? 'Hold the microphone and ask. It can answer, or drive the terminal for you.'
          : 'Ask what this session is doing, or what to do next. It can also do it for you.',
      }));
    }
    for (const msg of messages) {
      const bubble = el('div', {
        class: 'chat-msg ' + (msg.role === 'user' ? 'user' : 'assistant') + (msg.failed ? ' failed' : ''),
      });
      renderText(bubble, msg.text);
      const row = msg.run_id ? runRow(msg.run_id) : null;
      if (row) bubble.append(row);
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
    const usable = !!ctx.current() && ctx.live();
    const field = document.getElementById('chatText');
    const send = document.getElementById('chatSend');
    const mic = document.getElementById('chatMic');
    if (field) field.disabled = !usable;
    if (send) send.disabled = !usable || sending;
    // Stopping a recording is always available: the microphone is on this
    // device and turning it off asks the server nothing.
    if (mic) mic.disabled = !usable && !stopDictation;
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
    /** open shows the panel and puts the cursor or the thumb where it goes. */
    open(opts = {}) {
      const session = ctx.current();
      if (!session) return;
      open = true;
      dom.chatPanel.hidden = false;
      ctx.refit();
      load(session);
      render();
      paint();
      // With a keyboard, the field is what was asked for. Without one - auto
      // mode - nothing is focused at all, on purpose.
      if (!ctx.audio()) {
        const field = document.getElementById('chatText');
        if (field) field.focus();
        return;
      }
      // The audio bar's own button asks for the microphone rather than for a
      // panel to then find the microphone in: one tap, which is the budget.
      if (opts.dictate && !stopDictation && ctx.live()) {
        const mic = document.getElementById('chatMic');
        if (mic && !mic.disabled) dictate(mic);
      }
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
      runs.clear();
      replace([]);
      if (open && !ctx.current()) this.close();
      if (open) load(ctx.current());
      paint();
    },

    /** audioChanged rebuilds the input row, which is the whole difference. */
    audioChanged() {
      if (stopDictation) { const stop = stopDictation; stopDictation = null; stop(); }
      buildFoot();
      render();
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
