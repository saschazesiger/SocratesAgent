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
  document.addEventListener('click', rowLinkByPoint, true);
}

// rowLinkByPoint sends a click on an interactive table's stretched row link
// to the row under the pointer. Safari lays table rows out as static
// whatever their position, so every row's link stretches over the whole
// table and the last row's took every click (Tests B-34, checked on macOS
// Safari); Dashboard-Style's rule (.ds-table__row-link::after) wants the
// row as its box. A click from the keyboard (detail 0) has no point and
// stays with its link.
function rowLinkByPoint(event) {
  const link = event.target instanceof Element ? event.target.closest('.ds-table--interactive .ds-table__row-link') : null;
  if (!link || event.detail === 0) return;
  const own = link.closest('tr');
  const inside = (el) => {
    const box = el.getBoundingClientRect();
    return event.clientY >= box.top && event.clientY < box.bottom;
  };
  if (!own || inside(own)) return;
  const row = Array.from(own.parentElement ? own.parentElement.children : []).find((tr) => tr !== own && inside(tr));
  const target = row && row.querySelector('.ds-table__row-link');
  if (!target) return;
  event.preventDefault();
  event.stopImmediatePropagation();
  target.click();
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
  return DS.dom.morphHtml(target, String(markup), options);
}

/**
 * flipMorph is morph() for a keyed list whose items come, go and reorder:
 * they glide to their places. `live` for data that changes by itself (it waits
 * while the list is pointed at), not for the person's own action.
 */
export function flipMorph(container, markup, { items, live = false } = {}) {
  return M.flip(container, (o) => DS.dom.morphHtml(container, String(markup), o), { items, live });
}

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
