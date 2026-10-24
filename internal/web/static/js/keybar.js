// The phone half of the terminal: the key bar.
//
// A phone keyboard has no Escape, no Tab, no Ctrl and no arrows, and a shell
// and the programs run in it need all of them. So the keys it lacks are a
// row of buttons that write the same bytes a real key would.
//
// It is on by default in touch mode and off everywhere else, and the
// options menu turns it on or off for this device, which is remembered and
// wins from then on. A guess about the hardware is wrong on a tablet in a case
// or a laptop with a touch screen, and that is what the menu is for; but a
// phone with no Escape and no arrows in sight is the case the bar exists for,
// and having to go looking for it there was the worse of the two mistakes.

import { el } from './api.js';
import { iconEl } from './ui.js';
import { pasteClipboard, HOLD_MS } from './term.js';
import { touchScreen } from './keyboard.js';

/* ---------------------------------------------------------- the key bar */

// The keys a phone keyboard does not have, in the order a thumb wants them.
// `send` is the bytes; a key with `mod` is a sticky modifier instead. A key
// with a glyph is drawn as it (the arrows, Enter, Paste); the others are the
// names printed on a keyboard's own keys, which no glyph says better.
const KEYS = [
  { label: 'Esc', send: '\x1b', name: 'Escape' },
  { label: 'Tab', send: '\t', name: 'Tab' },
  { label: 'Ctrl', mod: 'ctrl', name: 'Control' },
  { label: 'Alt', mod: 'alt', name: 'Alt' },
  { glyph: 'arrowLeft', send: '\x1b[D', name: 'Left', tip: 'Left arrow' },
  { glyph: 'arrowDown', send: '\x1b[B', name: 'Down', tip: 'Down arrow' },
  { glyph: 'arrowUp', send: '\x1b[A', name: 'Up', tip: 'Up arrow' },
  { glyph: 'arrowRight', send: '\x1b[C', name: 'Right', tip: 'Right arrow' },
  { glyph: 'enter', send: '\r', name: 'Enter', tip: 'Enter' },
  { label: '^C', send: '\x03', name: 'Ctrl-C' },
  { label: '^D', send: '\x04', name: 'Ctrl-D' },
  { label: '^Z', send: '\x1a', name: 'Ctrl-Z' },
  { glyph: 'paste', paste: true, name: 'Paste', tip: 'Paste' },
];

// What a modifier key says it does, armed, locked or neither.
const MOD_TIPS = {
  ctrl: { off: 'Ctrl for the next key; hold to lock', on: 'Ctrl for the next key; tap again to cancel', lock: 'Ctrl is locked; tap to release' },
  alt: { off: 'Alt for the next key; hold to lock', on: 'Alt for the next key; tap again to cancel', lock: 'Alt is locked; tap to release' },
};

const KEYBAR_PREF = 'socrates.term.keybar';

// What a modifier may be applied to: one printable character, which is what a
// key on a keyboard produces. An escape sequence is a report or a reply, and a
// longer string is a paste or an input-method commit.
function isTypedKey(data) {
  return [...data].length === 1 && data.charCodeAt(0) >= 0x20 && data.charCodeAt(0) !== 0x7f;
}

/**
 * keyBarWanted is whether the bar is shown on this device: what the options
 * menu last answered here, or - before it has been asked - whether the page
 * is in touch mode.
 */
export function keyBarWanted() {
  let saved = null;
  try { saved = localStorage.getItem(KEYBAR_PREF); } catch { /* a private window */ }
  if (saved === 'on') return true;
  if (saved === 'off') return false;
  // The same generous question the keyboard switch asks.
  return touchScreen();
}

/** setKeyBarWanted records the answer the options menu gave. */
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
  host.replaceChildren();
  // 'ctrl' | 'alt' -> 'on' (next key only) | 'lock' (until tapped again)
  const armed = new Map();
  const buttons = new Map();

  // An armed modifier is pressed (the tint), and a locked one also wears the
  // lock glyph and says so in its label, because "held for the next key" and
  // "held until I say otherwise" are different promises and colour alone
  // cannot tell them apart.
  const paint = () => {
    for (const [mod, button] of buttons) {
      const state = armed.get(mod) || '';
      button.setAttribute('aria-pressed', state ? 'true' : 'false');
      button.toggleAttribute('data-lock', state === 'lock');
      const name = button.dataset.key;
      button.setAttribute('aria-label', state === 'lock' ? name + ', locked' : name);
      button.setAttribute('data-tip', MOD_TIPS[mod][state || 'off']);
      const glyph = button.querySelector('svg');
      if (state === 'lock' && !glyph) button.prepend(iconEl('lock', 16));
      else if (state !== 'lock' && glyph) glyph.remove();
    }
  };

  // A tap arms it for the next key, and a second tap puts it away again - a
  // tap too many is then simply undone. Held, it locks until it is tapped:
  // that is what makes a sequence like Ctrl-x Ctrl-s possible with one hand,
  // and it never happens by accident, which a third tap that unlocked did
  // (two taps had locked Ctrl, and "exit" went out as ^E^X^I^T).
  const toggle = (mod) => {
    if (armed.get(mod)) armed.delete(mod);
    else armed.set(mod, 'on');
    paint();
  };
  const lock = (mod) => {
    armed.set(mod, 'lock');
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
      class: key.glyph ? 'ds-btn ds-btn--icon so-key' : 'ds-btn so-key', type: 'button', 'data-key': key.name,
      'aria-label': key.name, 'data-tip': key.tip || null, text: key.label || null,
    });
    if (key.glyph) button.append(iconEl(key.glyph, 20));
    if (key.send) button.dataset.send = key.send;
    if (key.mod) {
      button.dataset.mod = key.mod;
      buttons.set(key.mod, button);
      // Held for HOLD_MS - the pane's own long press - it locks, and the
      // click that ends the press is not also a tap.
      let timer = null;
      let held = false;
      const stop = () => { if (timer) { clearTimeout(timer); timer = null; } };
      button.addEventListener('pointerdown', (event) => {
        if (event.button !== 0) return;
        stop();
        held = false;
        timer = setTimeout(() => { timer = null; held = true; lock(key.mod); }, HOLD_MS);
      });
      for (const end of ['pointerup', 'pointercancel', 'pointerleave']) button.addEventListener(end, stop);
      // A long press is not the browser's menu either.
      button.addEventListener('contextmenu', (event) => event.preventDefault());
      button.addEventListener('click', () => {
        if (held) { held = false; return; }
        toggle(key.mod);
      });
    } else if (key.paste) {
      button.addEventListener('click', () => { consume(); pasteClipboard(term, send); });
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
    dispose() { host.replaceChildren(); armed.clear(); buttons.clear(); },
  };
}


/* --------------------------------------------------------- the viewport */

/**
 * followViewport keeps the app the size of what the person can actually see.
 *
 * When a phone raises its keyboard the layout viewport does not change - only
 * the visual one does - so a page sized to 100% puts the bottom of itself
 * under the keyboard, and the bottom of this one is the key bar. The height
 * goes into a custom property the shell is sized by, and the window is
 * scrolled back to the top, because iOS scrolls the page instead of resizing
 * it and a scrolled shell hides the top bar.
 */
// Below this many pixels of visible height - a phone on its side with its
// keyboard up - the top bar steps aside (so-frame.css, data-so-short).
const SHORT_VIEW = 300;

export function followViewport() {
  const vv = window.visualViewport;
  if (!vv) return () => {};
  const apply = () => {
    const root = document.documentElement;
    root.style.setProperty('--so-app-h', Math.round(vv.height) + 'px');
    root.toggleAttribute('data-so-short', vv.height < SHORT_VIEW);
    if (vv.offsetTop > 0) window.scrollTo(0, 0);
  };
  vv.addEventListener('resize', apply);
  vv.addEventListener('scroll', apply);
  apply();
  return () => {
    vv.removeEventListener('resize', apply);
    vv.removeEventListener('scroll', apply);
    document.documentElement.style.removeProperty('--so-app-h');
    document.documentElement.removeAttribute('data-so-short');
  };
}
