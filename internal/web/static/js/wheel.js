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
// rather than a thing that moves: the new line pushes the old one out. The
// two glide up together by one line (the slot's --so-wheel-travel, 100% of
// a line's own height) on --ds-ease-in-out over --ds-dur-slow, like one
// strip: the old line leaves through the slot's soft top edge and fades
// out on the way (--ds-ease-in: it stays until it is at the edge), the new
// one comes up through the soft bottom edge and fades in (--ds-ease-out).
// Only translate and opacity: the text stays sharp all the way. Every line
// is shown for at least --ds-hold-status: a change during the hold waits,
// and a later one takes the place of the waiting one (which then never
// shows), so a quick run of changes rolls once, to the last; a line that
// has not shown yet holds nothing. A line still rolling in when the next
// change comes finishes its roll during the hold, and the two then go as
// one strip; only a roll that would outlast the hold is carried on from
// where it is.

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

// remaining is how long an animation has left to run, in ms.
function remaining(animation) {
  if (animation.playState === 'finished') return 0;
  const end = Number(animation.effect && animation.effect.getComputedTiming().endTime);
  const now = Number(animation.currentTime);
  return Number.isFinite(end) && Number.isFinite(now) ? Math.max(0, end - now) : Infinity;
}

function travelOf(slot) {
  return getComputedStyle(slot).getPropertyValue('--so-wheel-travel').trim() || '100%';
}

/**
 * roll is one line's part of the turn: the glide on --ds-ease-in-out over
 * --ds-dur-slow - the same for the line that goes and the one that comes,
 * so the two move as one strip - and the opacity along with it.
 */
function roll(line, slot, from, to, opacity, delay, fill) {
  const duration = M.motionDuration(slot, '--ds-dur-slow');
  const glide = line.animate([{ transform: from }, { transform: to }], {
    duration, delay, easing: M.motionEasing(slot, '--ds-ease-in-out'), fill,
  });
  const fade = line.animate([{ opacity: opacity[0] }, { opacity: opacity[1] }], {
    duration, delay, easing: M.motionEasing(slot, opacity[1] ? '--ds-ease-out' : '--ds-ease-in'), fill,
  });
  return Promise.all([glide.finished, fade.finished]).then(() => {}, () => {});
}

/** turnIn brings the coming line up from below, after `delay` ms (hidden until then). */
function turnIn(line, slot, delay) {
  stateOf(slot).last = line;
  roll(line, slot, `translateY(${travelOf(slot)})`, 'translateY(0)', [0, 1], delay, 'backwards');
}

/**
 * turnOut pushes the going line up and out from where it is, after `delay`
 * ms: a promise that resolves once it is gone, or null when it goes at once
 * - a line that never showed (it waited out a hold, or came in the same
 * change, and a later one took its place).
 */
function turnOut(line, slot, delay) {
  const running = line.getAnimations();
  if (unseen(line)) {
    running.forEach((one) => one.cancel());
    return null;
  }
  // A line still coming in when the change comes finishes coming in while
  // the change waits out its hold, and goes from where it then rests: the
  // two lines move as one strip, one line apart. Cut off where it was and
  // sent up from there, it went from part of a line below its place while
  // the new one came from a whole line below - the two lay on each other
  // ("Thinking" over "Running seq 1 2000") - and it stood at its place,
  // whole, while it waited.
  if (running.every((one) => remaining(one) <= delay)) {
    return roll(line, slot, 'translateY(0)', `translateY(calc(-1 * ${travelOf(slot)}))`, [1, 0], delay, 'forwards');
  }
  const style = getComputedStyle(line);
  const from = style.transform === 'none' ? 'translateY(0)' : style.transform;
  const opacity = Number(style.opacity);
  running.forEach((one) => one.cancel());
  return roll(line, slot, from, `translateY(calc(-1 * ${travelOf(slot)}))`, [opacity, 0], delay, 'forwards');
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
