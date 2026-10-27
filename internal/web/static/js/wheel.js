// The motion of a .so-wheel slot (so-motion.css): its lines roll like the
// face of a wheel. Written the way a Dashboard-Style module is - it reads
// the motion tokens through DS.motion and names nothing of Socrates - so it
// can move there as it is (Dashboard-Style plans/wheel-and-shimmer.md).
//
// A slot drawn with DS.dom.morphHtml rolls by itself when the morph is
// given wheelHooks: its lines are keyed by what they say, so a new text is
// a new line, which the hooks turn in, while the old one is turned out and
// stays (morph keeps a node whose onRemove returns a promise) until it is
// gone. wheelRoll(slot, text) does the same for a slot of plain text
// without a morph.
//
// The turn: both lines go round one drum behind the text (the slot's
// --so-wheel-depth, set as each line's transform-origin), the old one up
// and away by --so-wheel-angle, the new one up from below by as much, on
// --ds-ease-spatial over --ds-dur-slow. The old line fades out
// (--ds-dur-base, --ds-ease-in) and blurs by --so-wheel-blur as it turns
// away; the new one fades in (--ds-dur-medium, --ds-ease-in-out) and
// sharpens. Every line is shown for at least --ds-hold-status: a change
// during the hold waits, and a later one takes the place of the waiting one
// (which then never shows), so a quick run of changes rolls once, to the
// last. A change during a turn turns each line on from where it is.

import { M } from './ui.js';

// What each slot is doing: when its shown line came (or comes) to rest,
// and the turn the current morph is making, shared by the line it removes
// and the line it inserts.
const slots = new WeakMap();
// The lines wheelRoll() is turning out, which a later roll leaves be.
const going = new WeakSet();

function slotOf(line) {
  const slot = line && line.parentElement;
  return slot && slot.classList.contains('so-wheel') ? slot : null;
}

function stateOf(slot) {
  let state = slots.get(slot);
  if (!state) {
    state = { restAt: -Infinity, turn: null };
    slots.set(slot, state);
  }
  return state;
}

/**
 * turnDelay is how long the turn the slot makes now waits: none once its
 * line has been shown for the hold, else the rest of the hold. The line
 * that goes and the line that comes in one change (one morph, one task)
 * share it.
 */
function turnDelay(slot) {
  const state = stateOf(slot);
  if (!state.turn) {
    const now = performance.now();
    const at = Math.max(now, state.restAt + M.motionDuration(slot, '--ds-hold-status'));
    state.restAt = at;
    state.turn = { at };
    queueMicrotask(() => { state.turn = null; });
  }
  return Math.max(0, state.turn.at - performance.now());
}

// easing is a DS curve as el.animate() takes it: the spring's linear() is
// rejected by engines below the floor of linear(), which get the plain
// deceleration instead.
const easings = new Map();
function easing(el, token) {
  const value = M.motionEasing(el, token);
  if (!easings.has(value)) {
    let ok = true;
    try {
      new KeyframeEffect(null, null, { easing: value });
    } catch {
      ok = false;
    }
    easings.set(value, ok);
  }
  return easings.get(value) ? value : M.motionEasing(el, '--ds-ease-out');
}

function geometry(slot) {
  const style = getComputedStyle(slot);
  // Each line is seen from its own middle (a perspective() in its own
  // transform, not the slot's): a short line in a wide slot turns straight
  // instead of leaning towards the slot's middle.
  const depth = style.getPropertyValue('--so-wheel-depth').trim() || '0.9em';
  return {
    angle: style.getPropertyValue('--so-wheel-angle').trim() || '70deg',
    blur: style.getPropertyValue('--so-wheel-blur').trim() || '0px',
    lens: `perspective(calc(${depth} * 12))`,
  };
}

/** turnIn plays the coming line in, after `delay` ms (hidden until then). */
function turnIn(line, slot, delay) {
  const { angle, blur, lens } = geometry(slot);
  const turn = M.motionDuration(slot, '--ds-dur-slow');
  line.animate([
    { transform: `${lens} rotateX(calc(-1 * ${angle}))`, filter: `blur(${blur})` },
    { transform: `${lens} rotateX(0deg)`, filter: 'blur(0px)' },
  ], { duration: turn, delay, easing: easing(slot, '--ds-ease-spatial'), fill: 'backwards' });
  line.animate([{ opacity: 0 }, { opacity: 1 }], {
    duration: M.motionDuration(slot, '--ds-dur-medium'), delay, easing: easing(slot, '--ds-ease-in-out'), fill: 'backwards',
  });
}

/**
 * turnOut plays the going line out from where it is, after `delay` ms: a
 * promise that resolves once it is gone, or null when it goes at once - a
 * line that never showed (it waited out a hold, or came in the same change,
 * and a later one took its place).
 */
function turnOut(line, slot, delay) {
  const running = line.getAnimations();
  if (running.some((one) => one.playState !== 'finished' && Number(one.currentTime) <= Number(one.effect.getTiming().delay))) {
    running.forEach((one) => one.cancel());
    return null;
  }
  const { angle, blur, lens } = geometry(slot);
  const style = getComputedStyle(line);
  const from = { transform: style.transform === 'none' ? `${lens} rotateX(0deg)` : style.transform, filter: style.filter === 'none' ? 'blur(0px)' : style.filter };
  const opacity = Number(style.opacity);
  running.forEach((one) => one.cancel());
  const turn = line.animate([from, { transform: `${lens} rotateX(${angle})`, filter: `blur(${blur})` }], {
    duration: M.motionDuration(slot, '--ds-dur-slow'), delay, easing: easing(slot, '--ds-ease-spatial'), fill: 'forwards',
  });
  const fade = line.animate([{ opacity }, { opacity: 0 }], {
    duration: M.motionDuration(slot, '--ds-dur-base'), delay, easing: easing(slot, '--ds-ease-in'), fill: 'forwards',
  });
  return Promise.all([turn.finished, fade.finished]).then(() => {}, () => {});
}

/**
 * wheelHooks are the onInsert and onRemove of DS.dom.morphHtml (ui.js
 * morph) for markup with wheels in it: a line inserted into a slot that was
 * already there turns in, a line removed from one turns out first. Any
 * other node is left to the morph (a whole new slot shows its line at
 * once).
 */
export const wheelHooks = {
  onInsert(node) {
    const slot = slotOf(node);
    if (slot) turnIn(node, slot, turnDelay(slot));
  },
  onRemove(node) {
    const slot = slotOf(node);
    return (slot && turnOut(node, slot, turnDelay(slot))) || undefined;
  },
};

/**
 * wheelRoll turns a slot to `text` without a morph: a line of its own for
 * the text, in, and the lines there before, out. The same text again
 * changes nothing. Resolves when the old lines are gone.
 */
export function wheelRoll(slot, text) {
  if (!slot) return Promise.resolve();
  const value = String(text ?? '');
  const lines = [...slot.children].filter((line) => !going.has(line));
  if (lines.length === 1 && lines[0].textContent === value) return Promise.resolve();
  const delay = turnDelay(slot);
  const line = document.createElement('span');
  line.className = 'so-wheel__line';
  line.textContent = value;
  slot.append(line);
  turnIn(line, slot, delay);
  return Promise.all(lines.map((old) => {
    going.add(old);
    old.setAttribute('aria-hidden', 'true');
    return (turnOut(old, slot, delay) || Promise.resolve()).then(() => old.remove());
  })).then(() => {});
}
