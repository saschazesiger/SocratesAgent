// ui.js is the one bridge to Dashboard-Style: window.DS from the classic
// vendor ds.js, which every page loads with `defer` before its modules. No
// other module reads window.DS or imports the vendored modules - a second copy
// of the runtime would mean a second Escape stack, a second tooltip and a
// second ripple root - so everything a page needs from DS comes through here.
//
// It imports nothing, so it can never be part of an import cycle.

export const DS = window.DS;
export const M = DS.motion;

/* ---------------------------------------------------------------- start */

let started = false;

/**
 * startUI wires the page's DS behaviour that is delegated from the document:
 * the press ripple, the tooltips of every [data-tip], and the icons of the
 * static markup - and touch mode, so that sign-in, setup and Costs have a
 * finger's targets on a phone as the session page does. api.js calls it on
 * import, so every page gets it once; a second call does nothing.
 */
export function startUI() {
  if (started) return;
  started = true;
  if (touchWanted()) document.documentElement.classList.add('so-touch', 'ds-touch');
  DS.ripple.attach(document);
  DS.overlay.tooltips(document);
  fillIcons(document);
}

/* ----------------------------------------------------------- touch mode */

// Whether this device is taken for a touch screen: the answer the options
// menu gave on this device ('on', 'off'), or else any sign of one - a
// pointer that is a finger, main or not, touch points, touch events, or a
// browser known to run on one (Tesla's reports a fine pointer that hovers).
// keyboard.js asks the same question and says why it is asked generously;
// <html class="so-touch ds-touch"> is what the stylesheets ask: DS's
// .ds-touch gives every DS target a finger's 48px, .so-touch the project's.
export const TOUCH_PREF = 'socrates.touch';
export const TOUCH_QUERIES = ['(hover: none)', '(pointer: coarse)', '(any-pointer: coarse)', '(any-hover: none)']
  .map((query) => window.matchMedia(query));
const TOUCH_BROWSER = /\bTesla\/|QtCarBrowser/;

/** touchPref is what this device said: 'on', 'off', or '' for "decide by the signs". */
export function touchPref() {
  try {
    const saved = localStorage.getItem(TOUCH_PREF);
    return saved === 'on' || saved === 'off' ? saved : '';
  } catch { return ''; /* a private window: the signs decide */ }
}

/** looksTouch is whether the device shows any sign of a touch screen. */
export function looksTouch() {
  return TOUCH_QUERIES.some((query) => query.matches)
    || navigator.maxTouchPoints > 0
    || 'ontouchstart' in window
    || TOUCH_BROWSER.test(navigator.userAgent);
}

/** touchWanted is whether the page starts in touch mode. */
export function touchWanted() {
  return touchPref() === 'on' || (touchPref() !== 'off' && looksTouch());
}

/* ---------------------------------------------------------------- icons */

// GLYPH names what a control means, and which DS glyph stands for it. Where
// DS has no fair stand-in the value is null and the control is a text button
// instead. Any other name is a DS glyph's own.
export const GLYPH = {
  home: 'home', sidebar: 'panelLeft', paste: 'clipboard', upload: 'upload', summarize: 'activity', stop: 'stopCircle',
  review: 'flag', background: 'server', waiting: 'clock', grip: 'grip',
  mic: 'mic', keyboard: 'keyboard', enter: 'cornerDownLeft', attach: 'paperclip', speak: 'volume', shell: 'terminal',
};

// glyphOf is the inner SVG of a GLYPH key or of any DS glyph name. A name
// that has no glyph throws, so a typo is found the first time it renders
// rather than shipping an empty button.
function glyphOf(name) {
  let glyph = name;
  if (Object.hasOwn(GLYPH, name)) {
    glyph = GLYPH[name];
    if (!glyph) throw new Error(`Dashboard-Style ${DS.version} has no glyph for "${name}": make it a text button`);
  }
  const body = Object.hasOwn(DS.icons, glyph) ? DS.icons[glyph] : undefined;
  if (typeof body !== 'string' || body.charAt(0) !== '<') throw new Error('Unknown icon: ' + name);
  return body;
}

/** icon is the SVG markup of one glyph, for html`` templates. */
export function icon(name, size = 20, className) {
  return DS.icons.icon(glyphOf(name), size, className);
}

/** iconEl is the same glyph as an element, built node by node. */
export function iconEl(name, size = 20, className) {
  return DS.icons.iconElement(glyphOf(name), size, className);
}

/** hasIcon says whether a name draws anything (false for the null glyphs). */
export function hasIcon(name) {
  try {
    glyphOf(name);
    return true;
  } catch {
    return false;
  }
}

/**
 * fillIcons gives every [data-icon] in `root` its DS glyph (DS.icons.fill), at
 * data-icon-size (default 20). Markup names DS's glyphs, not GLYPH's
 * meanings. An element with data-icon-place="start" keeps what it holds and
 * gets the glyph in front of it, as a direct child where DS's rules read it
 * (a labelled button, a drawer item, the leading mark of an alert); an empty
 * one, an icon button, gets the glyph as its content. A second call draws
 * nothing new. A name DS does not have is an error in the console, once the
 * known ones are drawn.
 */
export function fillIcons(root = document) {
  try {
    DS.icons.fill(root);
  } catch (err) {
    console.error(err);
  }
}

/* ------------------------------------------------------------ templates */

class Raw {
  constructor(markup) { this.markup = String(markup); }
  toString() { return this.markup; }
}

const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

/** esc makes any text safe inside markup, as content or as a quoted attribute. */
export function esc(text) {
  return String(text ?? '').replace(/[&<>"']/g, (char) => ESCAPES[char]);
}

/** raw marks markup that is already safe (an icon(), another html``) so html`` keeps it. */
export function raw(markup) {
  return markup instanceof Raw ? markup : new Raw(markup);
}

// One value of a template: escaped text, kept markup, or a list of either.
// null, undefined and false render nothing, so a condition can stand in it.
function part(value) {
  if (value === null || value === undefined || value === false) return '';
  if (value instanceof Raw) return value.markup;
  if (Array.isArray(value)) return value.map(part).join('');
  return esc(value);
}

/**
 * html is the tagged template every markup string is built with: each value is
 * escaped unless raw() (or another html``) made it, so a server's string can
 * never become markup. The result is itself raw, so templates nest.
 */
export function html(strings, ...values) {
  let out = strings[0];
  values.forEach((value, i) => { out += part(value) + strings[i + 1]; });
  return new Raw(out);
}

/** morph patches `target` to the markup, touching only what changed. */
export function morph(target, markup, options) {
  // What a list was drawn from by flipMorph is not what it shows any more.
  drawnFrom.delete(target);
  return DS.dom.morphHtml(target, String(markup), options);
}

/**
 * flipMorph is morph() for a keyed list whose items come, go and reorder:
 * they glide to their places. `live` for data that changes by itself (it waits
 * while the list is pointed at), not for the person's own action.
 *
 * Live data is drawn far more often than it moves, and a FLIP measures every
 * item before and after, which on a slow phone with many sessions is most of
 * what the page does. So a live redraw of the markup the list already shows
 * does nothing at all, and one where no keyed element comes, goes or moves is
 * a plain morph: there is nothing to glide. A device too slow to glide a live
 * list within a frame (slowGlide) has its reorders land where they go. `glide`
 * asks for the FLIP all the same, for a person's own change that moves items
 * without reordering them (a folder shut). A FLIP that holds a reorder until
 * the pointer leaves draws the newest markup when it lets go, never the
 * markup it was called with.
 */
export function flipMorph(container, markup, { items, live = false, glide = !live } = {}) {
  const text = String(markup);
  const last = drawnFrom.get(container);
  if (!glide && last === text) return Promise.resolve();
  drawnFrom.set(container, text);
  if (!glide && keysStay(container, text, items)) {
    DS.dom.morphHtml(container, text);
    return Promise.resolve();
  }
  const slow = live && slowGlide(container);
  if (slow && !glide) {
    // A list under the pointer still keeps its order until it is left, as
    // DS's own hold does, but without measuring anything.
    if (pointedAt(container, items)) {
      DS.dom.morphHtml(container, text, { holdOrder: true });
      holdOrder(container, items);
    } else {
      DS.dom.morphHtml(container, text);
    }
    return Promise.resolve();
  }
  const started = performance.now();
  const done = M.flip(container, (o) => DS.dom.morphHtml(container, drawnFrom.get(container), o),
    { items, live, ...(live && !glide ? { max: slow ? 0 : LIVE_MOVES } : {}) });
  if (live && !slow) glideTook(container, performance.now() - started);
  return done;
}

// How many items of a live list glide at once. Each one that glides is
// measured and lifted on its own, which a slow phone pays for per item; a
// change that moves more of them than this is drawn where it lands, as DS
// does above its own limit. A person's own change keeps DS's limit.
const LIVE_MOVES = 12;

// What a live list's glide may cost before the device is taken for too slow
// to glide it: a frame at 60 Hz. Past it, the list's reorders land without
// motion, and every GLIDE_RETRY-th of them glides again to see whether the
// device has caught up (a slow moment, not a slow phone).
const GLIDE_BUDGET = 16;
const GLIDE_RETRY = 20;
const glideCosts = new WeakMap();

function glideTook(container, cost) {
  const held = glideCosts.get(container);
  glideCosts.set(container, { cost: held ? (held.cost + cost) / 2 : cost, skipped: 0 });
}

function slowGlide(container) {
  const held = glideCosts.get(container);
  if (!held || held.cost <= GLIDE_BUDGET) return false;
  held.skipped += 1;
  if (held.skipped < GLIDE_RETRY) return true;
  held.skipped = 0;
  return false;
}

// holdOrder draws the newest markup of a list whose order was held for the
// pointer once the pointer or the focus has left it, or after as long as DS
// holds a live reorder at most (flip's `hold`).
const LIVE_HOLD = 2000;
const holding = new WeakMap();

function holdOrder(container, items) {
  if (holding.has(container)) return;
  const release = () => {
    const hold = holding.get(container);
    if (!hold) return;
    clearTimeout(hold.timer);
    holding.delete(container);
    container.removeEventListener('pointerleave', hold.leave);
    container.removeEventListener('focusout', hold.leave);
    DS.dom.morphHtml(container, drawnFrom.get(container));
  };
  // Focus has left by the next task, and so has :hover.
  const leave = () => setTimeout(() => { if (!pointedAt(container, hold.items)) release(); }, 0);
  const hold = { timer: setTimeout(release, LIVE_HOLD), leave, items };
  holding.set(container, hold);
  container.addEventListener('pointerleave', leave);
  container.addEventListener('focusout', leave);
}

// pointedAt is DS's own reason to hold a live reorder: a mouse or a pen over
// the list, or the focus in one of its items. A finger never holds: :hover
// stays where a finger tapped, so the kind of the last pointer is asked.
function pointedAt(container, items) {
  if (!pointerKnown) {
    pointerKnown = true;
    document.addEventListener('pointerover', (event) => { lastPointer = event.pointerType || 'mouse'; }, { capture: true, passive: true });
  }
  const active = document.activeElement;
  const item = active && container.contains(active) && active !== container
    ? (items ? active.closest(items) : active) : null;
  if (item && container.contains(item)) return true;
  return lastPointer !== 'touch' && window.matchMedia('(hover: hover)').matches && container.matches(':hover');
}
let pointerKnown = false;
let lastPointer = '';

// What each flipMorph list was last drawn from (flipMorph).
const drawnFrom = new WeakMap();

const KEY_ATTR = /\sdata-ds-key="([^"]*)"/g;

// keysStay says whether drawing `text` into `container` leaves every keyed
// element where it is: the same keys in the same order, and every item of the
// list inside one of them, so that nothing unkeyed can come or go unseen.
// The markup's keys are compared as html`` wrote them, escaped.
function keysStay(container, text, items) {
  const kept = container.querySelectorAll('[data-ds-key]');
  const next = Array.from(text.matchAll(KEY_ATTR), (match) => match[1]);
  if (kept.length !== next.length) return false;
  for (let i = 0; i < next.length; i += 1) {
    if (esc(kept[i].getAttribute('data-ds-key')) !== next[i]) return false;
  }
  for (const item of items ? container.querySelectorAll(items) : container.children) {
    const keyed = item.closest('[data-ds-key]');
    if (!keyed || !container.contains(keyed) || keyed === container) return false;
  }
  return true;
}

/**
 * paced is `draw` for data that arrives faster than a slow device can draw
 * it. `now()` draws at once - the person's own action, which is answered
 * without waiting. `soon()` draws once for any number of calls, after a rest
 * twice as long as the last drawing took: a fast machine draws at once all
 * the same, and a slow one with many sessions draws a little later and keeps
 * two thirds of its time for the person, rather than drawing every frame of
 * a busy stream one after the other and answering nothing in between. A
 * `now()` meanwhile takes the place of the pending `soon()`.
 */
export function paced(draw) {
  let timer = 0;
  let cost = 0;
  let doneAt = 0;
  const now = () => {
    clearTimeout(timer);
    timer = 0;
    const started = performance.now();
    try {
      draw();
    } finally {
      doneAt = performance.now();
      cost = doneAt - started;
    }
  };
  const soon = () => {
    if (timer) return;
    const rest = Math.min(PACE_MAX, cost * 2) - (performance.now() - doneAt);
    timer = setTimeout(now, Math.max(0, rest));
  };
  return { now, soon };
}

// The longest rest paced() takes, however long a drawing took: news is never
// held back by more than this.
const PACE_MAX = 1000;

/** ms is a DS motion token in milliseconds, as this element computes it. */
export function ms(token, el = document.documentElement) {
  return M.motionDuration(el, token);
}

/* --------------------------------------------------------------- frames */

/**
 * faces is the markup of a button's busy and done faces (DS Button): the
 * label, the spinner with `busyLabel`, and - only when `doneLabel` is given -
 * the check with it. DS.motion.busy() switches between them.
 */
export function faces(label, busyLabel, doneLabel) {
  return html`<span class="ds-btn__faces"><span class="ds-btn__face">${label}</span><span class="ds-btn__face ds-btn__face--busy" aria-hidden="true"><span class="ds-spinner ds-spinner--sm"></span>${busyLabel}</span>${
    doneLabel ? html`<span class="ds-btn__face ds-btn__face--done" aria-hidden="true">${raw(icon('check', 16))}${doneLabel}</span>` : ''
  }</span>`;
}

/** region is the page's snackbar region, made right after the frame if the page has none. */
export function region() {
  let node = document.getElementById('toasts');
  if (node) return node;
  node = document.createElement('div');
  node.className = 'ds-toasts';
  node.id = 'toasts';
  const frame = document.querySelector('.ds-app, .ds-auth');
  if (frame) frame.after(node);
  else document.body.append(node);
  return node;
}

/* ------------------------------------------------------------- overlays */

/** showOverlay opens a dialog, sheet or menu, putting it into the page first if it is not there. */
export async function showOverlay(el, opener) {
  if (!el.isConnected) document.body.append(el);
  return DS.overlay.open(el, { opener });
}

/** hideOverlay closes one; `remove` takes it out of the page once its exit has played. */
export async function hideOverlay(el, { remove = false } = {}) {
  await DS.overlay.close(el);
  if (remove) el.remove();
}

/** overlayOpen says whether anything modal is on screen: a dialog or sheet, a menu, the modal drawer (DS.overlay.topmost). */
export function overlayOpen() {
  return !!DS.overlay.topmost();
}
