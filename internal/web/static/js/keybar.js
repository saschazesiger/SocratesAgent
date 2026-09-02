// The phone half of the terminal: the key bar, the line input and the
// microphone.
//
// A phone keyboard has no Escape, no Tab, no Ctrl and no arrows, and the four
// CLIs this app exists to drive need all of them. So the keys they lack are a
// row of buttons that write the same bytes a real key would.
//
// The line input beside them is not a convenience, it is the fix for a real
// defect: iOS autocorrect rewrites characters *after* they have been typed,
// and a terminal has already sent them one at a time by then - the correction
// arrives as a burst of backspaces into a shell that had moved on. A whole
// line, composed in an ordinary text field and sent with one carriage return,
// cannot be corrected behind the app's back.
//
// Dictation is the same field reached by voice: voice.js records, the server
// transcribes, and the text lands in the field *unsent*. Nothing here speaks;
// the text-to-speech half of voice.js stays exported and unused, as §E.6 says.

import { api, el, toast, fmtClock, isOffline, setClass } from './api.js';
import { Recorder, describeMicError } from './voice.js';

/* ---------------------------------------------------------- the key bar */

// The keys a phone keyboard does not have, in the order a thumb wants them.
// `send` is the bytes; a key with `mod` is a sticky modifier instead.
const KEYS = [
  { label: 'Esc', send: '\x1b', name: 'Escape' },
  { label: 'Tab', send: '\t', name: 'Tab' },
  { label: 'Ctrl', mod: 'ctrl', name: 'Control' },
  { label: 'Alt', mod: 'alt', name: 'Alt' },
  { label: '←', send: '\x1b[D', name: 'Left' },
  { label: '↓', send: '\x1b[B', name: 'Down' },
  { label: '↑', send: '\x1b[A', name: 'Up' },
  { label: '→', send: '\x1b[C', name: 'Right' },
  { label: '⏎', send: '\r', name: 'Enter' },
  { label: '^C', send: '\x03', name: 'Ctrl-C' },
  { label: '^D', send: '\x04', name: 'Ctrl-D' },
  { label: '^Z', send: '\x1a', name: 'Ctrl-Z' },
  { label: 'Paste', paste: true, name: 'Paste' },
  { label: '⌨', keyboard: true, name: 'Keyboard' },
];

const KEYBAR_PREF = 'socrates.term.keybar';

// What a modifier may be applied to: one printable character, which is what a
// key on a keyboard produces. An escape sequence is a report or a reply, and a
// longer string is a paste or an input-method commit.
function isTypedKey(data) {
  return [...data].length === 1 && data.charCodeAt(0) >= 0x20 && data.charCodeAt(0) !== 0x7f;
}

/**
 * keyBarWanted is whether this device gets the key bar without being asked.
 *
 * A coarse pointer is a finger, and a narrow window is a phone held upright.
 * Either way the keyboard on screen is missing the keys a TUI needs. The
 * session menu can turn it on anywhere, and the answer is remembered.
 */
export function keyBarWanted() {
  try {
    const stored = localStorage.getItem(KEYBAR_PREF);
    if (stored === 'on') return true;
    if (stored === 'off') return false;
  } catch { /* a browser with no storage gets the default */ }
  return matchMedia('(pointer: coarse)').matches || window.innerWidth < 900;
}

/** setKeyBarWanted records the answer the session menu gave. */
export function setKeyBarWanted(on) {
  try { localStorage.setItem(KEYBAR_PREF, on ? 'on' : 'off'); } catch { /* not fatal */ }
}

/**
 * mountKeyBar draws the key bar into `host` and wires it to one session.
 *
 * The returned `apply` is the other half of the sticky modifiers: what the
 * on-screen keyboard types goes through it before it reaches the socket, so
 * that arming Ctrl and then typing `c` sends 0x03 - which is the only way a
 * phone can send Ctrl-C to a program at all.
 */
export function mountKeyBar(host, term, socket) {
  host.innerHTML = '';
  // 'ctrl' | 'alt' -> 'on' (next key only) | 'lock' (until tapped again)
  const armed = new Map();
  const buttons = new Map();

  const paint = () => {
    for (const [mod, button] of buttons) {
      const state = armed.get(mod) || '';
      setClass(button, 'on', state === 'on' || state === 'lock');
      setClass(button, 'lock', state === 'lock');
      button.setAttribute('aria-pressed', state ? 'true' : 'false');
    }
  };

  // A tap arms, a second tap locks, a third puts it away. Locking is what
  // makes a sequence like Ctrl-x Ctrl-s possible with one hand.
  const toggle = (mod) => {
    const state = armed.get(mod);
    if (!state) armed.set(mod, 'on');
    else if (state === 'on') armed.set(mod, 'lock');
    else armed.delete(mod);
    paint();
  };

  /**
   * apply transforms one typed key by whatever modifier is armed.
   *
   * xterm's data channel carries more than keys: focus in and out reports,
   * the replies to the device-attribute and size questions tmux asks on every
   * attach, and whole pasted or composed strings. None of those is "the next
   * key", and treating them as one is how tapping the keyboard button ate the
   * Ctrl that had just been armed - the focus-in report got it - and how a
   * locked Alt corrupted every reply the terminal sent. So anything that is
   * not a single printable character passes through untouched, and leaves the
   * modifier armed for the key the person is about to press.
   */
  const apply = (data) => {
    if (!armed.size || typeof data !== 'string' || !data) return data;
    if (!isTypedKey(data)) return data;
    let out = data;
    if (armed.has('ctrl')) {
      // Ctrl of a printable key is that key's low five bits: `c` is 0x03.
      out = String.fromCharCode(out.charCodeAt(0) & 0x1f);
      if (armed.get('ctrl') === 'on') armed.delete('ctrl');
    }
    if (armed.has('alt')) {
      out = '\x1b' + out;
      if (armed.get('alt') === 'on') armed.delete('alt');
    }
    paint();
    return out;
  };

  // consume spends whatever is armed for one key, for a key-bar action that
  // sends bytes of its own. A locked modifier stays locked.
  const consume = () => {
    let changed = false;
    for (const mod of ['ctrl', 'alt']) {
      if (armed.get(mod) === 'on') { armed.delete(mod); changed = true; }
    }
    if (changed) paint();
  };

  const send = (bytes) => { if (socket) socket.sendInput(bytes); };

  for (const key of KEYS) {
    const button = el('button', {
      class: 'key', type: 'button', 'data-key': key.name,
      'aria-label': key.name, text: key.label,
    });
    if (key.send) button.dataset.send = key.send;
    if (key.mod) {
      button.dataset.mod = key.mod;
      buttons.set(key.mod, button);
      button.addEventListener('click', () => toggle(key.mod));
    } else if (key.paste) {
      button.addEventListener('click', () => { consume(); paste(term, send); });
    } else if (key.keyboard) {
      // Synchronously, inside the handler: iOS raises the keyboard for a
      // focus() that a tap caused and for nothing else, and an `await`
      // anywhere before this line is what loses that permission.
      button.addEventListener('click', () => { if (term && term.textarea) term.textarea.focus(); });
    } else {
      button.addEventListener('click', () => send(apply(key.send)));
    }
    // A key bar is for thumbs, and a thumb that lands on a button must not
    // also take the focus out of the terminal.
    button.addEventListener('mousedown', (event) => event.preventDefault());
    host.append(button);
  }

  paint();
  return {
    apply,
    /** clear disarms everything, for a bar that is being put away. */
    clear() { if (armed.size) { armed.clear(); paint(); } },
    /** armedNow is what the scenarios and the bar itself read back. */
    armedNow: () => [...armed.entries()].map(([mod, state]) => mod + ':' + state).join(','),
    dispose() { host.innerHTML = ''; armed.clear(); buttons.clear(); },
  };
}

// paste puts the clipboard into the pane, bracketed when the program asked to
// be told that a paste is a paste - which is what stops an editor from
// auto-indenting every line of it.
async function paste(term, send) {
  let text = '';
  try {
    text = await navigator.clipboard.readText();
  } catch {
    toast('The clipboard could not be read — paste with the keyboard instead.', 'error');
    return;
  }
  if (!text) return;
  const bracketed = !!(term && term.modes && term.modes.bracketedPasteMode);
  send(bracketed ? '\x1b[200~' + text + '\x1b[201~' : text);
}

/* ------------------------------------------------------ the line composer */

const draftKey = (sessionId) => 'socrates.term.' + sessionId + '.draft';

function readDraft(sessionId) {
  try {
    const raw = localStorage.getItem(draftKey(sessionId));
    if (!raw) return { draft: '', pending: [] };
    const parsed = JSON.parse(raw);
    return { draft: parsed.draft || '', pending: Array.isArray(parsed.pending) ? parsed.pending : [] };
  } catch { return { draft: '', pending: [] }; }
}

function writeDraft(sessionId, value) {
  try {
    if (!value.draft && !value.pending.length) localStorage.removeItem(draftKey(sessionId));
    else localStorage.setItem(draftKey(sessionId), JSON.stringify(value));
  } catch { /* a line that cannot be saved is still on screen */ }
}

/**
 * mountComposer wires the line input and the microphone to one session.
 *
 * A submitted line goes through the same numbered input path a keystroke
 * does, so it is subject to the same exactly-once rule; what is different is
 * that it stays in localStorage until the server has acknowledged it. A
 * half-typed keystroke is not worth persisting across a page being killed. A
 * composed line is - it is the thing the person actually meant to say.
 */
export function mountComposer({ form, input, mic, recTime, sessionId, socket, term }) {
  const stored = readDraft(sessionId);
  // What was in flight when the tab died is not in flight any more: no socket
  // holds it, and the server was never told about it twice. So it comes back
  // as an ordinary draft in the field, and the pending list starts empty -
  // otherwise a line sent after a reload stays in storage for ever and is
  // handed back on every later reload of the same session.
  const pending = [];
  input.value = [...stored.pending, stored.draft].filter(Boolean).join(' ');
  const save = () => writeDraft(sessionId, { draft: input.value, pending });
  save();

  const recorder = new Recorder();
  let ticker = null;

  const onInput = () => save();
  input.addEventListener('input', onInput);

  const onSubmit = (event) => {
    event.preventDefault();
    const text = input.value;
    input.value = '';
    // The line and its carriage return are one frame, because they are one
    // thing: half a line delivered is worse than none.
    const entry = { text };
    pending.push(text);
    save();
    const done = () => {
      const at = pending.indexOf(text);
      if (at >= 0) pending.splice(at, 1);
      save();
    };
    if (socket) {
      socket.sendInput(text + '\r', { text, onDelivered: done, onLost: done });
    } else {
      done();
    }
    input.focus();
  };
  form.addEventListener('submit', onSubmit);

  const stopTicker = () => { if (ticker) { clearInterval(ticker); ticker = null; } };
  const paintMic = (recording) => {
    setClass(mic, 'rec', recording);
    mic.setAttribute('aria-pressed', recording ? 'true' : 'false');
    mic.title = recording ? 'Stop' : 'Speak';
    recTime.hidden = !recording;
    if (!recording) recTime.textContent = fmtClock(0);
  };

  const onMic = async () => {
    if (recorder.recording) { await finish(); return; }
    try {
      await recorder.start();
    } catch (err) {
      toast(describeMicError(err), 'error');
      return;
    }
    paintMic(true);
    ticker = setInterval(() => { recTime.textContent = fmtClock(recorder.seconds); }, 200);
  };

  async function finish() {
    stopTicker();
    const result = await recorder.stop();
    paintMic(false);
    if (!result) { toast('I did not hear anything.'); return; }
    if (result.seconds < 0.4) { toast('That was too short.'); return; }
    mic.disabled = true;
    try {
      // Transcription only reads the audio back as words, so retrying it
      // costs a moment and loses nothing - which is what a bad line wants.
      const data = await api('/api/voice/transcribe', {
        method: 'POST', attempts: 3, timeout: 60000,
        body: { audio: result.base64, format: result.format },
      });
      const text = (data && data.text) || '';
      if (!text) { toast('I did not catch that.'); return; }
      // Into the field, never into the pane: dictation is a draft, and the
      // person presses Send.
      input.value = input.value ? input.value + ' ' + text : text;
      save();
      input.focus();
    } catch (err) {
      // The status line and the gateway's own words are not a sentence
      // anybody can act on, and §E.10 keeps technical strings out of what is
      // shown. The mic is still there and the recording can be repeated.
      toast(isOffline(err)
        ? 'No connection — that recording could not be transcribed.'
        : 'The recording could not be transcribed. Try again.', 'error');
    } finally {
      mic.disabled = false;
    }
  }

  mic.addEventListener('click', onMic);

  return {
    /**
     * restore puts text the server never acknowledged back in the field.
     *
     * This is the `viewer_fresh` path of §D.6: the server has no memory of
     * this viewer, so it cannot tell a resend from new input. Nothing is sent
     * twice; the line is handed back and the person decides.
     */
    restore(lines) {
      const text = lines.filter(Boolean).join(' ');
      if (!text) return;
      for (const line of lines) {
        const at = pending.indexOf(line);
        if (at >= 0) pending.splice(at, 1);
      }
      input.value = input.value ? text + ' ' + input.value : text;
      save();
    },
    dispose() {
      stopTicker();
      if (recorder.recording) recorder.stop().catch(() => {});
      paintMic(false);
      input.removeEventListener('input', onInput);
      form.removeEventListener('submit', onSubmit);
      mic.removeEventListener('click', onMic);
    },
  };
}

/* --------------------------------------------------------- the viewport */

/**
 * followViewport keeps the app the size of what the person can actually see.
 *
 * When a phone raises its keyboard the layout viewport does not change - only
 * the visual one does - so a page sized to 100% keeps the composer under the
 * keyboard, which is the field the whole mobile design is built around. The
 * height goes into a custom property the shell is sized by, and the window is
 * scrolled back to the top, because iOS scrolls the page instead of resizing
 * it and a scrolled shell hides the top bar.
 */
export function followViewport() {
  const vv = window.visualViewport;
  if (!vv) return () => {};
  const apply = () => {
    document.documentElement.style.setProperty('--app-h', Math.round(vv.height) + 'px');
    if (vv.offsetTop > 0) window.scrollTo(0, 0);
  };
  vv.addEventListener('resize', apply);
  vv.addEventListener('scroll', apply);
  apply();
  return () => {
    vv.removeEventListener('resize', apply);
    vv.removeEventListener('scroll', apply);
    document.documentElement.style.removeProperty('--app-h');
  };
}
