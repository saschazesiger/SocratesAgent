// The motion of a .so-wheel slot (so-motion.css): its lines roll. Written
// the way a Dashboard-Style module is - it reads the motion tokens through
// DS.motion and names nothing of Socrates - so it can move there as it is
// (Dashboard-Style plans/wheel-and-shimmer.md).
//
// A slot drawn with DS.dom.morphHtml rolls by itself when the morph is
// given wheelHooks: its lines are keyed by what they say, so a new text is
// a new line, which the hooks bring in, while the old one is taken out and
// stays (morph keeps a node whose onRemove returns a promise) until it is
// gone. wheelRoll(slot, text) does the same for a slot of plain text
// without a morph.
//
// The roll is quiet on purpose, a line in the background that changes
// rather than a thing that moves: the old line glides up by the slot's
// --so-wheel-travel (--ds-dur-medium, --ds-ease-in) and is faded out within
// --ds-dur-fast (--ds-ease-out); the new one glides up into place from as
// far below and fades in (--ds-dur-slow, --ds-ease-out, no overshoot) once
// the old one has faded, so the two never stand on each other. Only
// translate and opacity: the text stays sharp all the way. Every line is
// shown for at least --ds-hold-status: a change during the hold waits, and a
// later one takes the place of the waiting one (which then never shows), so
// a quick run of changes rolls once, to the last. A change during a roll
// carries each line on from where it is.

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
    state = { restAt: -Infinity, turn: null, last: null };
    slots.set(slot, state);
  }
  return state;
}

/**
 * turnDelay is how long the roll the slot makes now waits: none once its
 * line has been shown for the hold, else the rest of the hold. A line that
 * has not shown yet (it waits, or has not started to come in) holds
 * nothing: the line that takes its place takes its time, so the slot is
 * never left empty. The line that goes and the line that comes in one
 * change (one morph, one task) share it.
 */
function turnDelay(slot) {
  const state = stateOf(slot);
  if (!state.turn) {
    const now = performance.now();
    const at = unseen(state.last) ? Math.max(now, state.restAt)
      : Math.max(now, state.restAt + M.motionDuration(slot, '--ds-hold-status'));
    state.restAt = at;
    state.turn = { at };
    queueMicrotask(() => { state.turn = null; });
  }
  return Math.max(0, state.turn.at - performance.now());
}

// unseen says a line is still in the delay of its entrance: it has not
// shown.
function unseen(line) {
  return !!line && line.isConnected && line.getAnimations()
    .some((one) => one.playState !== 'finished' && Number(one.currentTime) <= Number(one.effect.getTiming().delay));
}

function travelOf(slot) {
  return getComputedStyle(slot).getPropertyValue('--so-wheel-travel').trim() || '0.5em';
}

/** turnIn brings the coming line in, after `delay` ms (hidden until then). */
function turnIn(line, slot, delay) {
  stateOf(slot).last = line;
  const travel = travelOf(slot);
  const late = delay + M.motionDuration(slot, '--ds-dur-fast');
  const slow = M.motionDuration(slot, '--ds-dur-slow');
  line.animate([{ transform: `translateY(${travel})` }, { transform: 'translateY(0)' }], {
    duration: slow, delay: late, easing: M.motionEasing(slot, '--ds-ease-out'), fill: 'backwards',
  });
  line.animate([{ opacity: 0 }, { opacity: 1 }], {
    duration: slow, delay: late, easing: M.motionEasing(slot, '--ds-ease-in-out'), fill: 'backwards',
  });
}

/**
 * turnOut takes the going line out from where it is, after `delay` ms: a
 * promise that resolves once it is gone, or null when it goes at once - a
 * line that never showed (it waited out a hold, or came in the same change,
 * and a later one took its place).
 */
function turnOut(line, slot, delay) {
  const running = line.getAnimations();
  if (unseen(line)) {
    running.forEach((one) => one.cancel());
    return null;
  }
  const style = getComputedStyle(line);
  const from = style.transform === 'none' ? 'translateY(0)' : style.transform;
  const opacity = Number(style.opacity);
  running.forEach((one) => one.cancel());
  const glide = line.animate([{ transform: from }, { transform: `translateY(calc(-1 * ${travelOf(slot)}))` }], {
    duration: M.motionDuration(slot, '--ds-dur-medium'), delay, easing: M.motionEasing(slot, '--ds-ease-in'), fill: 'forwards',
  });
  const fade = line.animate([{ opacity }, { opacity: 0 }], {
    duration: M.motionDuration(slot, '--ds-dur-fast'), delay, easing: M.motionEasing(slot, '--ds-ease-out'), fill: 'forwards',
  });
  return Promise.all([glide.finished, fade.finished]).then(() => {}, () => {});
}

/**
 * wheelHooks are the onInsert and onRemove of DS.dom.morphHtml (ui.js
 * morph) for markup with wheels in it: a line inserted into a slot that was
 * already there rolls in, a line removed from one rolls out first. Any
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
