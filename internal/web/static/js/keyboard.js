// The keyboard switch, and the fields that take a tap.
//
// On a device with no keys of its own, half the display becomes a keyboard
// the moment a field takes the focus. In a terminal that is wrong every time:
// the pane takes the focus back by design, so that what is typed lands in the
// session, and a keyboard that came with it would open again on every tap
// that was only meant to scroll or to select. So on a touch screen the
// terminal's keyboard is not something the pane raises: it is raised and put
// away by one button in the top bar, and by nothing else.
//
//   * The pane - the terminal's hidden textarea, and every field of the VS
//     Code frame, which takes its focus back the same way - is muted while the
//     switch is off: `inputmode="none"`, which is the request, and read-only,
//     which is the fact. One of the two is a hint a browser may ignore; the
//     phones this app is used on did.
//   * The button unmutes it and gives it the focus, inside the tap, because
//     iOS raises a keyboard for a focus() that a tap caused and for nothing
//     else.
//   * The switch turns itself off when the keyboard goes away by some other
//     road - the focus leaving every field, the system's own "done" or back
//     key - so the button never says open over a screen with no keyboard.
//   * The button is in the bar only while a terminal (or an editor) is on the
//     stage: anywhere else it would raise nothing.
//
// Every other field - the chat's composer, a dialog's, the sheet's - is an
// ordinary field: a tap on it raises the
// keyboard and takes what is typed at once. What it does not do is raise one
// for a focus the page gave it by itself - a dialog that opens on its field, a
// chat that puts its cursor in the composer - because that is a keyboard over
// the sheet somebody was reading. So such a field is muted too until a finger
// lands on it, freed by that tap (before the browser moves the focus, so the
// tap's own focus raises the keyboard natively), and muted again once the
// focus has left it. A field a button brings because the person asked to
// type - Rename, the question dialog's Other, a plan's Keep planning - is
// not the page's own doing: that tap frees it (typeHere).
//
// None of this applies to a device with a mouse and no sign of a finger:
// there the fields are ordinary fields and the button is not shown. Whether a
// device has a touch screen is asked generously, because the two mistakes do
// not cost the same: a button too many is a button, but a car's browser that
// says it has a mouse would otherwise get no button at all, and a keyboard
// over half the display on every tap. Beside every field is still the
// microphone that fills it without any keys at all.
//
// The guess is only the default. "Touch mode" in the options menu answers
// the question for this device - on, for a browser that hides its finger; off,
// for a tablet in a keyboard case that would rather have ordinary fields - and
// the answer is remembered and wins over every sign from then on.

import { el } from './api.js';
import {
  M, morph, html, raw, icon, TOUCH_PREF, TOUCH_QUERIES, touchPref, looksTouch, touchWanted,
} from './ui.js';
import { record, recPhase } from './dictate.js';

// A touch screen is a device that shows any sign of one (ui.js looksTouch),
// or a finger actually seen on the page. Once a device has been taken for one
// it stays one for the page. The class on <html> is what the stylesheet asks.
let touch = false;

// becomeTouch puts the page under the switch the first time it learns it is
// on a touch screen, with the button and every field following - unless this
// device has said it does not want that.
function becomeTouch() {
  if (touchPref() === 'off') return;
  enterTouch();
}

function enterTouch() {
  if (touch) return;
  touch = true;
  document.documentElement.classList.add('so-touch');
  applyAll();
  paint();
  // A field that already had the focus may have raised a keyboard before the
  // page knew better; taking the focus again muted puts it away.
  const active = deepActive();
  if (isField(active)) refocus(active);
}

// leaveTouch is touch mode turned off: the button goes, the switch's own
// keyboard is put down, and every field is an ordinary field again - one a
// tap raises the device's keyboard for, as on any other page.
function leaveTouch() {
  if (!touch) return;
  open = false;
  touch = false;
  freed = null;
  document.documentElement.classList.remove('so-touch');
  applyAll();
  paint();
}

/** touchScreen is whether the page is in touch mode on this device. */
export function touchScreen() { return touch; }

/**
 * setTouchMode is the options menu's answer, remembered for this device. It
 * takes effect at once: no reload, and nothing typed so far is touched.
 */
export function setTouchMode(on) {
  try { localStorage.setItem(TOUCH_PREF, on ? 'on' : 'off'); } catch { /* still for this page */ }
  if (on) enterTouch();
  else leaveTouch();
}

if (touchWanted()) {
  touch = true;
  document.documentElement.classList.add('so-touch');
}

const FIELD = 'input, textarea, [contenteditable]:not([contenteditable="false"])';
// Inputs that are not typed into: a checkbox has no keyboard to raise.
const TYPED = new Set(['', 'text', 'search', 'url', 'email', 'password', 'tel', 'number']);

let open = false;
let button = null;
let fallback = () => null;
// Whether a pane - a terminal or an editor - is on the stage, which is the
// only time the button has anything to raise.
let paneShown = false;
// The one ordinary field a tap (or a key of the device's own) has given the
// keyboard to. It stays unmuted while it has the focus.
let freed = null;
// The tap in progress on an ordinary field: the field, and whether it already
// had the focus - muted - when the finger came down.
let tap = null;
// What each muted field had before it was muted, so a field that was read-only
// or numeric on its own is given back exactly that.
const saved = new Map();
// The documents whose fields are under the switch: this page, and the VS Code
// frame, which is served from the same origin.
const docs = new Set();
// The pane's field that last had the focus, for a tap on the button that
// lands while the focus is somewhere else.
let lastField = null;
// Set while the switch itself moves the focus, so its own blur is not read as
// the keyboard being put away.
let moving = false;

function isField(node) {
  if (!node || node.nodeType !== 1 || node === proxy) return false;
  if (node.localName === 'input') return TYPED.has((node.getAttribute('type') || '').toLowerCase());
  if (node.localName === 'textarea') return true;
  return node.isContentEditable || !!node.editContext;
}

// underSwitch is whether a field is the pane's: the terminal's textarea, or
// any field of the editor's frame. Only those wait for the button.
function underSwitch(node) {
  if (!node) return false;
  if (node.ownerDocument !== document) return true;
  return node === fallback() || (!!node.classList && node.classList.contains('xterm-helper-textarea'));
}

// wantsMute is whether a field may not raise a keyboard right now: in touch
// mode, with the switch off, every field but the one a tap freed.
function wantsMute(node) { return touch && !open && node !== freed; }

function mute(node) {
  if (saved.has(node)) return false;
  saved.set(node, {
    mode: node.getAttribute('inputmode'),
    ro: 'readOnly' in node ? node.readOnly : null,
  });
  node.setAttribute('inputmode', 'none');
  if ('readOnly' in node) node.readOnly = true;
  return true;
}

// unmute gives a field back what it had. A page that made the field
// writable itself while it was muted has said what it wants, and keeps it.
function unmute(node) {
  const was = saved.get(node);
  if (!was) return;
  saved.delete(node);
  if (was.mode === null) node.removeAttribute('inputmode');
  else node.setAttribute('inputmode', was.mode);
  if (was.ro !== null && node.readOnly) node.readOnly = was.ro;
}

function fieldsIn(root) {
  const out = [];
  if (isField(root)) out.push(root);
  if (root.querySelectorAll) {
    for (const node of root.querySelectorAll(FIELD)) if (isField(node)) out.push(node);
  }
  return out;
}

function applyAll() {
  for (const doc of [...docs]) {
    if (!doc.defaultView) { docs.delete(doc); continue; }
    for (const node of fieldsIn(doc.documentElement)) {
      if (wantsMute(node)) mute(node);
      else unmute(node);
    }
  }
  for (const node of [...saved.keys()]) if (!node.isConnected) saved.delete(node);
  if (freed && !freed.isConnected) freed = null;
}

// deepActive is the focused element, looking into the VS Code frame.
function deepActive() {
  let node = document.activeElement;
  while (node && node.localName === 'iframe') {
    let inner = null;
    try { inner = node.contentDocument && node.contentDocument.activeElement; } catch { break; }
    if (!inner || inner === node.contentDocument.body) break;
    node = inner;
  }
  return node;
}

function shown(node) {
  return !!node && node.isConnected && !node.disabled
    && (!node.checkVisibility || node.checkVisibility());
}

// target is the pane's field the button raises the keyboard for: the one
// that has the focus, the one that last had it, or the terminal.
function target() {
  const active = deepActive();
  if (isField(active) && underSwitch(active)) return active;
  if (shown(lastField) && underSwitch(lastField)) return lastField;
  const pane = fallback();
  return isField(pane) ? pane : null;
}

// refocus hands the focus straight back to `node`. `inputmode` and read-only
// are read when a field takes the focus, not while it holds it, so this is
// what raises or drops the keyboard for the field that already has it.
//
// Raising goes by way of the proxy, an editable field of the switch's own:
// iOS raises a keyboard for a focus() inside a tap, but a blur() and focus()
// of the very same element in one go is something it answers only some of the
// time. Focus moving from one editable field to another inside the tap is
// what it reliably keeps a keyboard up for.
function refocus(node, raise = false) {
  moving = true;
  try {
    if (raise && node.ownerDocument === document) {
      proxyField(node).focus({ preventScroll: true });
    } else {
      node.blur();
    }
    node.focus({ preventScroll: true });
    // A field that would not take the focus at the first time of asking -
    // inside the frame, or straight after the proxy - is asked again.
    if (node.ownerDocument.activeElement !== node) node.focus({ preventScroll: true });
  } finally { moving = false; }
}

// The proxy is built once, and is never a field as far as the rest of this
// module is concerned: never muted, never the one remembered, never a reason
// to think the keyboard went away. It stands beside the field it hands the
// focus to - inside that field's dialog when it is in one, because a modal
// dialog makes everything outside it inert, and an inert field takes no
// focus.
let proxy = null;
function proxyField(near) {
  if (!proxy) {
    proxy = el('input', {
      type: 'text', class: 'so-kb-proxy', 'aria-hidden': 'true', tabindex: '-1', 'data-ds-keep': '',
      autocomplete: 'off', autocorrect: 'off', autocapitalize: 'none', spellcheck: 'false',
    });
  }
  const host = (near && near.closest && near.closest('dialog')) || document.body;
  if (proxy.parentNode !== host) host.append(proxy);
  return proxy;
}

// paint says on the button whether the switch is on. The button is there
// only in touch mode, and only while a pane is on the stage: a device with
// keys of its own has nothing for it to raise, and neither has a chat or
// Home, whose fields raise the keyboard themselves.
function paint() {
  if (!button) return;
  const words = open ? 'Hide the keyboard' : 'Show the keyboard';
  button.hidden = !touch || !paneShown;
  button.setAttribute('aria-pressed', open ? 'true' : 'false');
  button.setAttribute('aria-label', words);
  button.setAttribute('data-tip', words);
}

// openedAt is when the switch last raised the keyboard, and `reports` is
// whether this browser has been seen to shrink its visual viewport for one -
// which is what makes the viewport evidence worth believing here.
let openedAt = 0;
let reports = false;
// How long a keyboard may take to slide in before its absence means anything.
const SLIDE_MS = 1500;

/**
 * showing is whether the keyboard is actually up, rather than whether the
 * switch says so. The system puts it away by roads the page does not always
 * hear about - iOS's "done", a swipe, Android's back key - and a switch that
 * still said open then made the next press put away a keyboard nobody could
 * see, so that it took a second press to raise one.
 */
function showing() {
  if (!open || !isField(deepActive())) return false;
  const vv = window.visualViewport;
  if (!vv || !reports || Date.now() - openedAt < SLIDE_MS) return true;
  return window.innerHeight - vv.height > 150;
}

/**
 * typingInPage is whether the focus is in a field of the page itself - the
 * composer, a sheet's or a dialog's field - rather than the pane's: the
 * terminal's or the editor's, whose keyboard is the switch's and whose bar
 * holds that switch.
 */
export function typingInPage() {
  const node = deepActive();
  return isField(node) && !underSwitch(node);
}

/** keyboardOpen is whether the switch is on. */
export function keyboardOpen() { return open; }

/**
 * setKeyboard raises or puts away the on-screen keyboard of the pane. The
 * raising half has to be called from inside a tap to work on iOS.
 */
export function setKeyboard(on) {
  if (on && touch) {
    const node = target();
    // Nothing on screen to type into, and a button that said open over no
    // keyboard would be lying.
    if (!node) return;
    open = true;
    seenUp = false;
    openedAt = Date.now();
    applyAll();
    paint();
    refocus(node, true);
    return;
  }
  if (!open) return;
  open = false;
  applyAll();
  paint();
  const active = deepActive();
  if (isField(active) && wantsMute(active)) refocus(active);
}

/**
 * showKeyboardSwitch says whether a pane - the terminal, or the editor - is
 * on the stage, which is when the button is in the bar. A pane that goes
 * takes the keyboard it had with it.
 */
export function showKeyboardSwitch(on) {
  paneShown = !!on;
  if (!paneShown && open) setKeyboard(false);
  paint();
}

/* ------------------------------------------------- a finger on a field */

// fieldOf is the ordinary field a tap landed on: the field itself, or the
// field a label it landed on is for.
function fieldOf(node) {
  if (!node || node.nodeType !== 1 || !node.closest) return null;
  const field = node.closest(FIELD);
  if (isField(field)) return field;
  const label = node.closest('label');
  const control = label && label.control;
  return isField(control) ? control : null;
}

// free gives the keyboard to one ordinary field. The one freed before it is
// muted again unless it still has the focus, in which case its own blur
// mutes it (onFocusOut).
function free(node) {
  const was = freed;
  freed = node;
  if (was && was !== node && was !== deepActive() && wantsMute(was)) mute(was);
  unmute(node);
}

/**
 * typeHere is a field the person has just asked to type into: Rename, the
 * question dialog's "Other", "Add a note", "Anything else?" and "Ask the
 * agent back", a plan's "Keep planning", the sheet's "Custom path…". A tap on
 * a button that brings a field is a request for the keyboard, unlike a focus
 * the page gives a field by itself (a dialog that opens on one, the
 * composer of a chat that opens), which stays muted. The field is freed and
 * given the focus - from inside the tap, which is when iOS raises a keyboard
 * for it. With `focus: false` it is only freed, for a field that takes the
 * focus by itself in the same tap (a dialog's autofocus). Anywhere but in
 * touch mode it is a plain focus.
 *
 * The focus is handed over with preventScroll, as every focus here is, so
 * the field is then brought into view of whatever scrolls it, "nearest":
 * a field already in sight stays where it is, and one a button has just
 * brought - below that button, often under the end of a dialog's scroller -
 * comes up into sight.
 */
export function typeHere(node, { focus = true } = {}) {
  if (!node) return;
  let handed = false;
  if (touch && isField(node) && !underSwitch(node)) {
    const had = deepActive() === node;
    free(node);
    // A field that had the focus muted took no keyboard with it; handing
    // the focus over again inside the tap raises one.
    if (had && focus) { refocus(node, true); handed = true; }
  }
  if (!focus) return;
  if (!handed && deepActive() !== node) node.focus({ preventScroll: true });
  node.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

// A finger coming down on an ordinary field frees it before the browser
// moves the focus, so the tap's own focus raises the keyboard the way it
// does on any page.
function onFieldDown(event) {
  if (!touch) return;
  const node = fieldOf(event.target);
  if (!node || underSwitch(node)) return;
  const at = pointOf(event);
  tap = { node, focused: deepActive() === node && saved.has(node), x: at.x, y: at.y, moved: 0 };
  free(node);
}

// pointOf is where a pointer or the first finger of a touch is.
function pointOf(event) {
  const one = event.touches && event.touches[0] ? event.touches[0] : event;
  return { x: Number(one.clientX) || 0, y: Number(one.clientY) || 0 };
}

// How far a finger may wander and still be holding still: the system's own
// slop before it takes a touch for a scroll.
const STILL_PX = 10;

// onFieldMove follows the finger of a tap on a field, to tell a scroll from
// a long press when the system cancels the pointer (onFieldCancel).
function onFieldMove(event) {
  const now = tap || held;
  if (!now) return;
  const at = pointOf(event);
  now.moved = Math.max(now.moved, Math.hypot(at.x - now.x, at.y - now.y));
  // A press taken for a long one turned out to be a scroll after all: the
  // touch's own moves can come after the pointer was cancelled.
  if (now === held && now.moved >= STILL_PX) {
    held = null;
    settle(now);
  }
}

// The press the system cancelled while the finger still held still: a long
// press, until its finger moves after all or lifts.
let held = null;

// A field that already had the focus - muted, because the page put it there
// - took no keyboard with that focus, and keeps none by being tapped; the tap
// hands the focus over again, which raises one.
function onFieldClick(event) {
  const now = tap;
  tap = null;
  if (!now || !touch || fieldOf(event.target) !== now.node) return;
  if (now.focused && deepActive() === now.node) refocus(now.node, true);
}

// A finger that came down on a field and went on to scroll the page is no
// tap: the field goes back to how it was. A finger that held still is a long
// press - iOS cancels the pointer for its own menu - and keeps the field
// writable: muted, a field is read-only, and iOS offers Copy on it but never
// Paste.
function onFieldCancel() {
  const now = tap;
  tap = null;
  if (!now || freed !== now.node) return;
  if (now.moved < STILL_PX) {
    held = now;
    return;
  }
  settle(now);
}

// settle puts back a field a finger freed and then scrolled with.
function settle(now) {
  if (freed !== now.node) return;
  if (now.focused || deepActive() !== now.node) {
    freed = null;
    if (wantsMute(now.node)) mute(now.node);
  }
}

/* ------------------------------------------ the keyboard going away itself */

// The focus left one field and did not arrive in another: the keyboard is
// gone, whatever the button says. Asked on the next tick, because a tap on the
// pane blurs its textarea and gives it the focus straight back. An ordinary
// field the focus left is muted again, so the next focus the page gives it
// does not raise a keyboard either.
function onFocusOut(event) {
  const node = event.target;
  if (!moving && isField(node) && !underSwitch(node)) {
    setTimeout(() => {
      if (moving || deepActive() === node) return;
      if (freed === node) freed = null;
      if (wantsMute(node)) mute(node);
    }, 0);
  }
  if (moving || !open) return;
  setTimeout(() => {
    if (!open || moving || isField(deepActive())) return;
    setKeyboard(false);
  }, 0);
}

function onFocusIn(event) {
  const node = event.target;
  if (!isField(node)) return;
  if (underSwitch(node)) lastField = node;
  // A field nothing had muted yet - built and focused in the same tick, or
  // made editable after it was built - is muted now, and the focus is handed
  // back so the keyboard it may have asked for does not come.
  if (wantsMute(node) && mute(node)) {
    setTimeout(() => {
      if (node.ownerDocument.activeElement === node) refocus(node);
    }, 0);
  }
}

// Android's back key and iOS's own "done" put the keyboard away without
// asking, and the first leaves the focus where it was. What is left to see is
// the visual viewport growing back once it had shrunk.
let seenUp = false;
function onViewport() {
  const vv = window.visualViewport;
  if (!vv || !open || vv.scale > 1.05) return;
  const gap = window.innerHeight - vv.height;
  if (gap > 150) { seenUp = true; reports = true; }
  else if (seenUp && gap < 80) setKeyboard(false);
}

// A finger on the page settles it, whatever the browser said about itself.
function onPointer(event) {
  if (event.type === 'touchstart' || event.pointerType === 'touch' || event.pointerType === 'pen') becomeTouch();
}

// A key pressed on keys of the device's own - a tablet in its case, a laptop
// with a touch screen - lands in a field that is muted while nothing freed
// it, and a read-only field takes nothing. No on-screen keyboard is up then,
// so the keydown came from real keys, and the field is given back before the
// key reaches it. The pane is left alone: xterm reads its keys from keydown,
// read-only or not, and an unmuted pane would raise the keyboard on a tap.
const EDITING = new Set(['Backspace', 'Delete', 'Enter']);
function onKeyDown(event) {
  if (open || !event.isTrusted || event.isComposing) return;
  const node = event.target;
  if (!saved.has(node) || underSwitch(node)) return;
  if ([...(event.key || '')].length !== 1 && !EDITING.has(event.key)) return;
  free(node);
}

/* ------------------------------------------------------------------ wiring */

// Pointer events where there are any, touch events on a browser too old for
// them: the two would otherwise both report one finger.
const DOWN = window.PointerEvent ? 'pointerdown' : 'touchstart';
const CANCEL = window.PointerEvent ? 'pointercancel' : 'touchcancel';

function watch(doc) {
  if (!doc || !doc.documentElement || docs.has(doc)) return;
  docs.add(doc);
  doc.addEventListener('focusin', onFocusIn, true);
  doc.addEventListener('focusout', onFocusOut, true);
  doc.addEventListener('keydown', onKeyDown, true);
  doc.addEventListener('pointerdown', onPointer, true);
  doc.addEventListener('touchstart', onPointer, { capture: true, passive: true });
  if (doc === document) {
    doc.addEventListener(DOWN, onFieldDown, { capture: true, passive: true });
    doc.addEventListener(CANCEL, onFieldCancel, true);
    // Touch events go on while the system scrolls, after the pointer is
    // cancelled; the pointer's own moves are what a device without them has.
    doc.addEventListener('touchmove', onFieldMove, { capture: true, passive: true });
    doc.addEventListener('pointermove', onFieldMove, { capture: true, passive: true });
    doc.addEventListener('touchend', () => { held = null; }, { capture: true, passive: true });
    doc.addEventListener('click', onFieldClick, true);
  }
  new MutationObserver((records) => {
    for (const r of records) {
      for (const node of r.addedNodes) {
        if (node.nodeType !== 1) continue;
        for (const field of fieldsIn(node)) if (wantsMute(field)) mute(field);
      }
    }
  }).observe(doc.documentElement, { childList: true, subtree: true });
  applyAll();
}

/**
 * watchFrame puts the fields of a same-origin frame - VS Code's editor and
 * every box it opens - under the switch, and again every time it navigates.
 */
export function watchFrame(frame) {
  const attach = () => {
    try { watch(frame.contentDocument); } catch { /* another origin: nothing to reach */ }
  };
  frame.addEventListener('load', attach);
  attach();
}

/**
 * guard mutes one field straight away, for a field that is built and focused
 * in the same tick - before the page's own watcher has seen it. Only on a
 * page whose fields are under the switch (initKeyboard): Admin and Costs have
 * no pane and no switch, nothing there frees a muted field, and a field a
 * microphone was put beside (dictated) stayed read-only after every tap in
 * touch mode - its id could not be typed at all.
 */
export function guard(input) {
  if (input && isField(input) && docs.has(input.ownerDocument) && wantsMute(input)) mute(input);
  return input;
}

/**
 * initKeyboard wires the button in the top bar. `pane` names the field the
 * keyboard is for when nothing else has had the focus: the terminal's.
 */
export function initKeyboard(bar, pane) {
  button = bar;
  fallback = pane || (() => null);
  // The field keeps the focus: a tap that took it would put the keyboard
  // away before the button could say whether that was wanted.
  button.addEventListener('mousedown', (event) => event.preventDefault());
  button.addEventListener('click', () => setKeyboard(!showing()));
  watch(document);
  // A device can turn out to be one later - a tablet taken out of its dock, a
  // screen that reports its finger late. It is never turned back: a button
  // too many is the cheaper mistake.
  for (const query of TOUCH_QUERIES) {
    const changed = () => { if (looksTouch()) becomeTouch(); };
    if (query.addEventListener) query.addEventListener('change', changed);
    else if (query.addListener) query.addListener(changed);
  }
  if (window.visualViewport) window.visualViewport.addEventListener('resize', onViewport);
  paint();
}

/* ------------------------------------------------------- the microphone */

/**
 * micButton is a microphone that hands what it heard to `onText`: an icon
 * button a thumb finds beside its field, with the busy face every working
 * button in this app has - the spinner while the words are listened for.
 *
 * `field` names what it fills, for the label a reader hears ("Dictate the
 * folder"); the tip is the one word. `onError`, when given, is told why a
 * recording failed instead of a toast (see record).
 */
export function micButton(onText, field = '', { onError } = {}) {
  const label = field ? 'Dictate ' + field : 'Dictate';
  const button = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon so-mic', type: 'button', 'aria-label': label, 'data-tip': 'Dictate',
  });
  morph(button, html`<span class="ds-btn__faces"><span class="ds-btn__face">${raw(icon('mic'))}</span><span class="ds-btn__face ds-btn__face--busy" aria-hidden="true"><span class="ds-spinner"></span></span></span>`);
  // A microphone that took the focus out of the pane would leave the terminal
  // unable to be typed into by the person who just filled a field.
  button.addEventListener('mousedown', (event) => event.preventDefault());
  button.addEventListener('click', async () => {
    // The one microphone is already open somewhere else: `record` would answer
    // with nothing, and a button that spun for that would be lying.
    if (recPhase() !== 'idle') return;
    const text = await M.busy(button, () => record({ onError, opener: button }), { done: false });
    if (text) onText(text);
  });
  return button;
}

// fieldName is what a field is called where it stands - its label, the label
// it points at, or its placeholder - in the lower case a sentence wants:
// "Dictate the folder".
function fieldName(input) {
  let words = '';
  if (input.labels && input.labels.length) words = input.labels[0].textContent;
  else if (input.getAttribute('aria-labelledby')) {
    const named = document.getElementById(input.getAttribute('aria-labelledby').split(/\s+/)[0]);
    words = named ? named.textContent : '';
  } else words = input.getAttribute('aria-label') || '';
  words = words.replace(/\s+/g, ' ').trim();
  if (!words) return '';
  return words.charAt(0).toLowerCase() + words.slice(1);
}

/**
 * dictated puts a microphone beside one field and puts the field under the
 * keyboard's rules. It hands back the row the two of them now sit in.
 *
 * `control` is what stands in the layout - the field itself, or the wrapper a
 * combobox builds around one - and `input` is where the words go. `onText`
 * takes over from the default, which is to write the words into the field and
 * say so, because a field that is watched for changes has to be told. Beside
 * a textarea (or with `top`) the button stays at the top of the field.
 * `name` is what the field is called in the microphone's label, when its own
 * label cannot be found yet (a field not in the page).
 *
 * It is idempotent: a sheet that is opened twice builds its fields twice, and
 * a second row around the first would be a second microphone beside it.
 */
export function dictated(control, input, onText, { onError, top = false, name = '' } = {}) {
  guard(input);
  const already = control.parentElement;
  if (already && already.classList.contains('so-field-row')) return already;
  const mic = micButton((text) => {
    if (onText) { onText(text); return; }
    input.value = text;
    input.dispatchEvent(new Event('input', { bubbles: true }));
  }, name, { onError });
  const tall = top || input.tagName === 'TEXTAREA';
  const row = el('div', { class: tall ? 'so-field-row so-field-row--top' : 'so-field-row' });
  if (control.parentNode) control.replaceWith(row);
  row.append(control, mic);
  // The field's label is found once the row is in the page: a sheet builds
  // its fields before it is shown.
  if (!name) {
    const label = () => {
      const found = fieldName(input);
      if (found) mic.setAttribute('aria-label', 'Dictate ' + found);
      return !!found;
    };
    if (!label()) requestAnimationFrame(() => { if (!label()) setTimeout(label, 500); });
  }
  return row;
}
