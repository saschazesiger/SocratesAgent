// Hands-free.
//
// A tablet in a case and a phone in a car have the same problem, and it is not
// the screen: it is the keyboard. Touch anything on a device that has no keys
// of its own and half the display becomes a keyboard - over the terminal, over
// the sheet somebody was reading, over the thing they were about to press -
// and it opens again on the next tap, and the next. In a CLI, where the pane
// takes the focus back by design so that what is typed lands in the session,
// that is not an inconvenience: it is the interaction ending.
//
// So: a mode, armed from the top bar and remembered for this device, whose
// whole promise is that nothing on this page may raise it.
//
//   * Every field the app has is muted - `inputmode="none"`, which is the
//     request, and read-only, which is the fact. One of the two is a hint a
//     browser may ignore; a phone that ignored it would break the promise, and
//     a promise that holds on some phones is not one.
//   * The pane's own hidden textarea is muted with them, and the key bar's
//     keyboard key - which exists for no other purpose than to raise the
//     keyboard - stands down.
//   * The key bar itself comes on and stays on, whatever this device asked
//     for, because it is now the only keyboard there is: Escape, Tab, the
//     arrows and the Enter that runs a dictated line are on it and nowhere
//     else.
//   * Beside every field is the microphone that replaces the keys, opening
//     the same recording sheet the top bar's own microphone opens.
//
// The microphones are there whether or not the mode is armed. A control that
// only appears in a mode is a control nobody finds, and dictating into a field
// is worth having on a desk too; what the mode adds is the guarantee that
// nothing else can happen.

import { el, setClass } from './api.js';
import { record, recPhase } from './dictate.js';

// Per device and per browser, like the sound and notification switches and
// like the key bar: it is a fact about this screen and these hands, not about
// the account.
const KEY = 'socrates.handsfree';

function stored() {
  try { return localStorage.getItem(KEY) === 'on'; } catch { return false; }
}

let armed = stored();

// Every field that has been put under the mode's protection, and everything
// that wants to be told when it changes.
const fields = new Set();
const listeners = new Set();

/** handsFree is whether the mode is armed. */
export function handsFree() { return armed; }

/** setHandsFree arms or disarms it, and remembers the answer. */
export function setHandsFree(on) {
  if (armed === !!on) return;
  armed = !!on;
  try { localStorage.setItem(KEY, armed ? 'on' : 'off'); } catch { /* a private window remembers nothing */ }
  applyAll();
  for (const fn of [...listeners]) {
    try { fn(armed); } catch { /* one bad listener must not stop the others */ }
  }
}

/**
 * onHandsFree is told when the mode changes, and once straight away with what
 * it already is - so a caller has one code path rather than two.
 */
export function onHandsFree(fn) {
  listeners.add(fn);
  try { fn(armed); } catch { /* the caller's problem, not this module's */ }
  return () => listeners.delete(fn);
}

/**
 * guard puts one field under the mode: while it is armed the field cannot
 * raise a keyboard, and while it is not the field is an ordinary one.
 *
 * It is called with the field itself rather than a selector, and remembered,
 * because the fields this app has are built by scripts and half of them live
 * in dialogs that come and go - there is no moment at which they could all be
 * found at once.
 */
export function guard(input) {
  if (!input) return input;
  fields.add(input);
  apply(input);
  return input;
}

function apply(input) {
  if (armed) {
    input.setAttribute('inputmode', 'none');
    input.readOnly = true;
    return;
  }
  input.removeAttribute('inputmode');
  input.readOnly = false;
}

function applyAll() {
  for (const input of [...fields]) {
    // A field whose dialog has been and gone is not a field any more. They are
    // dropped here rather than tracked, because this is the only moment at
    // which the whole set is walked anyway.
    if (!input.isConnected) { fields.delete(input); continue; }
    apply(input);
    // A field that already has the keyboard up keeps it up: `inputmode` is
    // read when a field takes the focus, not while it holds it. So the focus
    // is taken away and given straight back, which closes the keyboard and,
    // now, does not open it again.
    if (armed && document.activeElement === input) {
      input.blur();
      input.focus();
    }
  }
}

/* ------------------------------------------------------- the microphone */

// One drawing, the same one the top bar's pill wears, because it is the same
// microphone and the same sheet behind it.
const MIC = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" '
  + 'stroke-linecap="round"><rect x="9" y="3" width="6" height="11" rx="3"/>'
  + '<path d="M5 11a7 7 0 0 0 14 0M12 18v3"/></svg>';

/**
 * micButton is a microphone that hands what it heard to `onText`.
 *
 * It wears the same ring every other control in this app wears while it is
 * working - around the mark rather than on it - so a field waiting for its
 * words looks like the sidebar looks while a session is working.
 */
export function micButton(onText, label = 'Dictate') {
  const button = el('button', {
    class: 'icon-btn mic-btn', type: 'button', title: label, 'aria-label': label,
    html: MIC,
  });
  // A microphone that took the focus out of the pane would leave the terminal
  // unable to be typed into by the person who just filled a field.
  button.addEventListener('mousedown', (event) => event.preventDefault());
  button.addEventListener('click', async () => {
    // The one microphone is already open somewhere else: `record` would answer
    // with nothing, and a button that spun for that would be lying.
    if (recPhase() !== 'idle') return;
    button.disabled = true;
    setClass(button, 'working', true);
    try {
      const text = await record();
      if (text) onText(text);
    } finally {
      button.disabled = false;
      setClass(button, 'working', false);
    }
  });
  return button;
}

/**
 * dictated puts a microphone beside one field and puts the field under the
 * mode. It hands back the row the two of them now sit in.
 *
 * `control` is what stands in the layout - the field itself, or the wrapper a
 * combobox builds around one - and `input` is where the words go. `onText`
 * takes over from the default, which is to write the words into the field and
 * say so, because a field that is watched for changes has to be told.
 *
 * It is idempotent: a sheet that is opened twice builds its fields twice, and
 * a second row around the first would be a second microphone beside it.
 */
export function dictated(control, input, onText) {
  guard(input);
  const already = control.parentElement;
  if (already && already.classList.contains('dictate')) return already;
  const mic = micButton((text) => {
    if (onText) { onText(text); return; }
    input.value = text;
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const row = el('div', { class: 'dictate' });
  if (control.parentNode) control.replaceWith(row);
  row.append(control, mic);
  return row;
}
