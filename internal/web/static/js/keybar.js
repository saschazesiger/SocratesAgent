// The phone half of the terminal: the key bar.
//
// A phone keyboard has no Escape, no Tab, no Ctrl and no arrows, and the four
// CLIs this app exists to drive need all of them. So the keys they lack are a
// row of buttons that write the same bytes a real key would.
//
// It is off until it is asked for, on every device. A bar that decided for
// itself whether this machine has a keyboard was wrong on the two devices that
// matter most - a tablet in a case, a laptop with a touch screen - and being
// wrong about it means either a row of buttons nobody wanted or the missing
// keys nowhere to be found. The session menu is one tap, and the answer is
// remembered for this device.

import { el, toast, setClass } from './api.js';
import { lastCopied } from './term.js';

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
 * keyBarWanted is whether this device has asked for the bar. Nothing else
 * decides: no media query, no platform string, no keystroke seen. Off is the
 * default, because the bar is the answer to a question - "where is Escape?" -
 * that most sessions never ask.
 */
export function keyBarWanted() {
  try { return localStorage.getItem(KEYBAR_PREF) === 'on'; } catch { return false; }
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
//
// A phone reaching this app over plain http has no `navigator.clipboard` at
// all, and one that has it can still refuse to be read - so what was last
// copied out of the pane itself is the fallback. That is the round trip a
// phone actually makes: hold a line to copy it, tap Paste to put it back.
async function paste(term, send) {
  let text = '';
  try {
    text = await navigator.clipboard.readText();
  } catch {
    text = lastCopied();
    if (!text) {
      toast('The browser would not let the clipboard be read.', 'error');
      return;
    }
  }
  if (!text) return;
  const bracketed = !!(term && term.modes && term.modes.bracketedPasteMode);
  send(bracketed ? '\x1b[200~' + text + '\x1b[201~' : text);
}

/* --------------------------------------------------------- the viewport */

/**
 * followViewport keeps the app the size of what the person can actually see.
 *
 * When a phone raises its keyboard the layout viewport does not change - only
 * the visual one does - so a page sized to 100% puts the bottom of itself
 * under the keyboard, and the bottom of the chat panel is the field somebody
 * is typing the question into. The height goes into a custom property the
 * shell is sized by, and the window is scrolled back to the top, because iOS
 * scrolls the page instead of resizing it and a scrolled shell hides the top
 * bar.
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
