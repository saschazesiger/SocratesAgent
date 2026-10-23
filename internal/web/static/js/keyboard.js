// The keyboard switch.
//
// On a device with no keys of its own, half the display becomes a keyboard
// the moment anything takes the focus - over the terminal, over the sheet
// somebody was reading - and in a CLI, where the pane takes the focus back by
// design so that what is typed lands in the session, it opens again on every
// tap. So on a touch screen the on-screen keyboard is not something a field
// raises: it is raised and put away by one button in the top bar, and by
// nothing else.
//
//   * Every field the page has - the pane's hidden textarea, every input, the
//     editor inside the VS Code frame - is muted while the switch is off:
//     `inputmode="none"`, which is the request, and read-only, which is the
//     fact. One of the two is a hint a browser may ignore; the phones this app
//     is used on did.
//   * The button unmutes them and gives the focus to the field that should
//     have it - the one in the dialog on screen, the one last typed into, or
//     the pane - inside the tap, because iOS raises a keyboard for a focus()
//     that a tap caused and for nothing else.
//   * The switch turns itself off when the keyboard goes away by some other
//     road - the focus leaving every field, the system's own "done" or back
//     key - so the button never says open over a screen with no keyboard.
//   * The button is on screen everywhere: a modal dialog makes the rest of the
//     page inert and covers it, so a copy of it is put into whichever dialog
//     is on top, over the very spot the one in the bar occupies.
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
import { M, faces, morph } from './ui.js';
import { record, recPhase } from './dictate.js';

// A touch screen is a device that shows any sign of one: a pointer that is a
// finger, main or not, touch points, touch events, a browser known to run on
// one, or a finger actually seen on the page. Tesla's browser is a Chromium
// that reports a fine pointer that can hover - the media queries alone said
// "mouse" there. Once a device has been taken for one it stays one for the
// page. The class on <html> is what the stylesheet asks.
const TOUCH_QUERIES = ['(hover: none)', '(pointer: coarse)', '(any-pointer: coarse)', '(any-hover: none)']
  .map((query) => window.matchMedia(query));
const TOUCH_BROWSER = /\bTesla\/|QtCarBrowser/;
let touch = false;

// What the options menu last answered on this device: 'on', 'off', or '' for
// "decide by the signs".
const TOUCH_PREF = 'socrates.touch';
function touchPref() {
  try {
    const saved = localStorage.getItem(TOUCH_PREF);
    return saved === 'on' || saved === 'off' ? saved : '';
  } catch { return ''; /* a private window: the signs decide */ }
}

function looksTouch() {
  return TOUCH_QUERIES.some((query) => query.matches)
    || navigator.maxTouchPoints > 0
    || 'ontouchstart' in window
    || TOUCH_BROWSER.test(navigator.userAgent);
}

// becomeTouch puts the page under the switch the first time it learns it is
// on a touch screen, with the button, its copy and every field following -
// unless this device has said it does not want that.
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
  placeCopy();
  // A field that already had the focus may have raised a keyboard before the
  // page knew better; taking the focus again muted puts it away.
  const active = deepActive();
  if (isField(active)) refocus(active);
}

// leaveTouch is touch mode turned off: the button and its copy go, the
// switch's own keyboard is put down, and every field is an ordinary field
// again - one a tap raises the device's keyboard for, as on any other page.
function leaveTouch() {
  if (!touch) return;
  open = false;
  touch = false;
  document.documentElement.classList.remove('so-touch');
  applyAll();
  paint();
  placeCopy();
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

if (touchPref() === 'on' || (touchPref() !== 'off' && looksTouch())) {
  touch = true;
  document.documentElement.classList.add('so-touch');
}

const FIELD = 'input, textarea, [contenteditable]:not([contenteditable="false"])';
// Inputs that are not typed into: a checkbox has no keyboard to raise.
const TYPED = new Set(['', 'text', 'search', 'url', 'email', 'password', 'tel', 'number']);

let open = false;
let button = null;
let copy = null;
let fallback = () => null;
// What each muted field had before it was muted, so a field that was read-only
// or numeric on its own is given back exactly that.
const saved = new Map();
// The documents whose fields are under the switch: this page, and the VS Code
// frame, which is served from the same origin.
const docs = new Set();
// The field that last had the focus, for a tap on the button that lands while
// the focus is somewhere else.
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

// muted is whether a field may not raise a keyboard right now.
function muted() { return touch && !open; }

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

function unmute(node) {
  const was = saved.get(node);
  if (!was) return;
  saved.delete(node);
  if (was.mode === null) node.removeAttribute('inputmode');
  else node.setAttribute('inputmode', was.mode);
  if (was.ro !== null) node.readOnly = was.ro;
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
      if (muted()) mute(node);
      else unmute(node);
    }
  }
  for (const node of [...saved.keys()]) if (!node.isConnected) saved.delete(node);
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

// topDialog is the modal dialog on top, if one is open: the one place a field
// can be typed into while it is there.
function topDialog() {
  const modal = [...document.querySelectorAll('dialog[open]')].filter(isModal);
  return modal.length ? modal[modal.length - 1] : null;
}

// isModal is `:modal`, for a browser old enough not to know the selector -
// the car's Chromium may be one. There a dialog opened with showModal() is
// told apart by the top layer putting it at a fixed position.
function isModal(dialog) {
  try { return dialog.matches(':modal'); } catch { /* an older selector engine */ }
  return getComputedStyle(dialog).position === 'fixed';
}

// target is the field the keyboard is raised for.
function target() {
  const active = deepActive();
  const dialog = topDialog();
  if (dialog) {
    if (isField(active) && dialog.contains(active)) return active;
    if (shown(lastField) && dialog.contains(lastField)) return lastField;
    return [...dialog.querySelectorAll(FIELD)].find((node) => isField(node) && shown(node)) || null;
  }
  if (isField(active)) return active;
  if (shown(lastField)) return lastField;
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
      proxyField().focus({ preventScroll: true });
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
// to think the keyboard went away.
let proxy = null;
function proxyField() {
  if (proxy && proxy.isConnected) return proxy;
  proxy = el('input', {
    type: 'text', class: 'so-kb-proxy', 'aria-hidden': 'true', tabindex: '-1',
    autocomplete: 'off', autocorrect: 'off', autocapitalize: 'none', spellcheck: 'false',
  });
  document.body.append(proxy);
  return proxy;
}

// paint says on the button and its copy whether the switch is on. The button
// is there only in touch mode: a device with keys of its own and no sign of a
// finger has nothing for it to raise.
function paint() {
  const words = open ? 'Hide the keyboard' : 'Show the keyboard';
  if (button) button.hidden = !touch;
  for (const one of [button, copy]) {
    if (!one) continue;
    one.setAttribute('aria-pressed', open ? 'true' : 'false');
    one.setAttribute('aria-label', words);
    one.setAttribute('data-tip', words);
  }
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

/** keyboardOpen is whether the switch is on. */
export function keyboardOpen() { return open; }

/**
 * setKeyboard raises or puts away the on-screen keyboard. The raising half has
 * to be called from inside a tap to work on iOS.
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
  if (isField(active)) refocus(active);
}

/* ------------------------------------------ the keyboard going away itself */

// The focus left one field and did not arrive in another: the keyboard is
// gone, whatever the button says. Asked on the next tick, because a tap on the
// pane blurs its textarea and gives it the focus straight back.
function onFocusOut() {
  if (moving || !open) return;
  setTimeout(() => {
    if (!open || moving || isField(deepActive())) return;
    setKeyboard(false);
  }, 0);
}

function onFocusIn(event) {
  const node = event.target;
  if (!isField(node)) return;
  lastField = node;
  // A field nothing had muted yet - built and focused in the same tick, or
  // made editable after it was built - is muted now, and the focus is handed
  // back so the keyboard it may have asked for does not come.
  if (muted() && mute(node)) {
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
// with a touch screen - lands in a field that is muted while the switch is
// off, and a read-only field takes nothing. No on-screen keyboard is up then,
// so the keydown came from real keys, and the field is given back before the
// key reaches it. The pane is left alone: xterm reads its keys from keydown,
// read-only or not, and an unmuted pane would raise the keyboard on a tap.
const EDITING = new Set(['Backspace', 'Delete', 'Enter']);
function onKeyDown(event) {
  if (open || !event.isTrusted || event.isComposing) return;
  const node = event.target;
  if (!saved.has(node) || node === fallback()) return;
  if ([...(event.key || '')].length !== 1 && !EDITING.has(event.key)) return;
  unmute(node);
}

/* -------------------------------------------------- one button, everywhere */

// placeCopy puts the button's copy into the dialog on top, over the spot the
// real one has in the bar. A modal dialog makes everything outside it inert,
// so the bar's own button can be seen through the backdrop and not pressed.
function placeCopy() {
  if (!copy) return;
  const host = touch ? topDialog() : null;
  if (!host) { copy.remove(); return; }
  if (copy.parentNode !== host) {
    host.append(copy);
    // `position: fixed` inside a dialog is the dialog's own while its opening
    // animation moves it, so the copy waits for the animation to end.
    copy.style.visibility = 'hidden';
    const running = host.getAnimations ? host.getAnimations().map((a) => a.finished) : [];
    Promise.all(running).catch(() => {}).then(() => {
      position();
      copy.style.visibility = '';
    });
  }
  position();
}

// position lays the copy over the bar's button. A bar with no button on
// screen leaves it in the corner the stylesheet gives it.
function position() {
  if (!copy || !copy.isConnected) return;
  const rect = button.getBoundingClientRect();
  const px = (n) => (rect.width ? n + 'px' : '');
  copy.style.top = px(rect.top);
  copy.style.left = px(rect.left);
  copy.style.width = px(rect.width);
  copy.style.height = px(rect.height);
  copy.style.right = rect.width ? 'auto' : '';
}

/* ------------------------------------------------------------------ wiring */

function watch(doc) {
  if (!doc || !doc.documentElement || docs.has(doc)) return;
  docs.add(doc);
  doc.addEventListener('focusin', onFocusIn, true);
  doc.addEventListener('focusout', onFocusOut, true);
  doc.addEventListener('keydown', onKeyDown, true);
  doc.addEventListener('pointerdown', onPointer, true);
  doc.addEventListener('touchstart', onPointer, { capture: true, passive: true });
  new MutationObserver((records) => {
    let dialogs = false;
    for (const r of records) {
      if (r.type === 'attributes') { dialogs = true; continue; }
      if (!muted()) continue;
      for (const node of r.addedNodes) {
        if (node.nodeType !== 1 || node === copy) continue;
        for (const field of fieldsIn(node)) mute(field);
      }
    }
    if (dialogs && doc === document) placeCopy();
  }).observe(doc.documentElement, {
    childList: true, subtree: true, attributes: true, attributeFilter: ['open'],
  });
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
 * guard puts one field under the switch straight away, for a field that is
 * built and focused in the same tick - before the page's own watcher has seen
 * it.
 */
export function guard(input) {
  if (input && muted()) mute(input);
  return input;
}

/**
 * initKeyboard wires the button in the top bar. `pane` names the field the
 * keyboard is for when nothing else has had the focus: the terminal.
 */
export function initKeyboard(bar, pane) {
  button = bar;
  fallback = pane || (() => null);
  // The copy wears the button's own words, long and short, so it reads the
  // same over a dialog as the button does in the bar.
  copy = el('button', { class: 'ds-btn so-talk so-kb-copy', type: 'button' },
    [...bar.childNodes].map((node) => node.cloneNode(true)));
  for (const one of [button, copy]) {
    // The field keeps the focus: a tap that took it would put the keyboard
    // away before the button could say whether that was wanted.
    one.addEventListener('mousedown', (event) => event.preventDefault());
    one.addEventListener('click', () => setKeyboard(!showing()));
  }
  watch(document);
  // A device can turn out to be one later - a tablet taken out of its dock, a
  // screen that reports its finger late. It is never turned back: a button
  // too many is the cheaper mistake.
  for (const query of TOUCH_QUERIES) {
    const changed = () => { if (looksTouch()) becomeTouch(); };
    if (query.addEventListener) query.addEventListener('change', changed);
    else if (query.addListener) query.addListener(changed);
  }
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', () => { onViewport(); position(); });
  }
  window.addEventListener('resize', position);
  paint();
  placeCopy();
}

/* ------------------------------------------------------- the microphone */

/**
 * micButton is a microphone that hands what it heard to `onText`.
 *
 * Dashboard-Style has no microphone glyph, so it is a text button ("Dictate")
 * with the busy face every working button in this app has: the spinner while
 * the words are being listened for.
 *
 * `onError`, when given, is told why a recording failed instead of a toast
 * (see record).
 */
export function micButton(onText, label = 'Dictate', { onError } = {}) {
  const button = el('button', { class: 'ds-btn so-mic', type: 'button' });
  morph(button, faces(label, 'Listening'));
  // A microphone that took the focus out of the pane would leave the terminal
  // unable to be typed into by the person who just filled a field.
  button.addEventListener('mousedown', (event) => event.preventDefault());
  button.addEventListener('click', async () => {
    // The one microphone is already open somewhere else: `record` would answer
    // with nothing, and a button that spun for that would be lying.
    if (recPhase() !== 'idle') return;
    const text = await M.busy(button, () => record({ onError }), { done: false });
    if (text) onText(text);
  });
  return button;
}

/**
 * dictated puts a microphone beside one field and puts the field under the
 * keyboard switch. It hands back the row the two of them now sit in.
 *
 * `control` is what stands in the layout - the field itself, or the wrapper a
 * combobox builds around one - and `input` is where the words go. `onText`
 * takes over from the default, which is to write the words into the field and
 * say so, because a field that is watched for changes has to be told. Beside
 * a textarea (or with `top`) the button stays at the top of the field.
 *
 * It is idempotent: a sheet that is opened twice builds its fields twice, and
 * a second row around the first would be a second microphone beside it.
 */
export function dictated(control, input, onText, { onError, top = false } = {}) {
  guard(input);
  const already = control.parentElement;
  if (already && already.classList.contains('so-field-row')) return already;
  const mic = micButton((text) => {
    if (onText) { onText(text); return; }
    input.value = text;
    input.dispatchEvent(new Event('input', { bubbles: true }));
  }, undefined, { onError });
  const tall = top || input.tagName === 'TEXTAREA';
  const row = el('div', { class: tall ? 'so-field-row so-field-row--top' : 'so-field-row' });
  if (control.parentNode) control.replaceWith(row);
  row.append(control, mic);
  return row;
}
