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
  if (touchWanted()) document.documentElement.classList.add('so-touch');
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
// <html class="so-touch"> is what the stylesheets ask.
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

// GLYPH names what a control means, and which glyph stands for it: a DS 1.6.2
// glyph, or one of LUCIDE's below where DS has none. Where neither has a fair
// stand-in the value is null and the control is a text button instead. When
// the missing glyphs ship upstream, only this map changes.
export const GLYPH = {
  home: 'overview', sidebar: 'columns', paste: 'clipboard', upload: 'upload', summarize: 'activity', stop: 'circleStop',
  review: 'flag', background: 'server', waiting: 'clock', grip: 'menu', prestart: 'fileText',
  mic: 'mic', keyboard: 'keyboard', enter: 'cornerDownLeft', attach: 'paperclip', speak: 'volume', shell: 'terminal',
};

// LUCIDE holds the few glyphs a phone needs that DS 1.6.2 does not ship yet,
// from the set DS draws its own from - Lucide (https://lucide.dev), ISC
// License, Copyright (c) Lucide Icons and Contributors; the notice is in
// vendor/dashboard-style/LICENSE - on the same 24px grid, stroked by DS's own
// icon(), in DS's flat element style (a polygon is written as a path). They
// are not a style of our own; delete each one here when DS has it.
const LUCIDE = {
  mic: '<path d="M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/>' +
    '<line x1="12" x2="12" y1="19" y2="22"/>',
  circleStop: '<circle cx="12" cy="12" r="10"/><rect x="9" y="9" width="6" height="6" rx="1"/>',
  keyboard: '<path d="M10 8h.01"/><path d="M12 12h.01"/><path d="M14 8h.01"/><path d="M16 12h.01"/>' +
    '<path d="M18 8h.01"/><path d="M6 8h.01"/><path d="M7 16h10"/><path d="M8 12h.01"/>' +
    '<rect width="20" height="16" x="2" y="4" rx="2"/>',
  upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/>' +
    '<line x1="12" x2="12" y1="3" y2="15"/>',
  clipboard: '<rect width="8" height="4" x="8" y="2" rx="1" ry="1"/>' +
    '<path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>',
  cornerDownLeft: '<polyline points="9 10 4 15 9 20"/><path d="M20 4v7a4 4 0 0 1-4 4H4"/>',
  paperclip: '<path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57' +
    'a2 2 0 0 1-2.83-2.83l8.49-8.48"/>',
  volume: '<path d="M11 5 6 9H2v6h4l5 4V5z"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/>' +
    '<path d="M19.07 4.93a10 10 0 0 1 0 14.14"/>',
  terminal: '<polyline points="4 17 10 11 4 5"/><line x1="12" x2="20" y1="19" y2="19"/>',
};

// glyphOf is the inner SVG of a GLYPH key or of any DS or LUCIDE glyph name.
// A name that has no glyph throws, so a typo is found the first time it
// renders rather than shipping an empty button.
function glyphOf(name) {
  let glyph = name;
  if (Object.hasOwn(GLYPH, name)) {
    glyph = GLYPH[name];
    if (!glyph) throw new Error(`Dashboard-Style ${DS.version} has no glyph for "${name}": make it a text button`);
  }
  const body = Object.hasOwn(DS.icons, glyph) ? DS.icons[glyph] : LUCIDE[glyph];
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
 * fillIcons gives every [data-icon] in `root` its icon, at data-icon-size
 * (default 20), in front of what it holds. A [data-icon-slot] element is
 * replaced by the icon itself, for the places DS wants the <svg> as a direct
 * child (the leading mark of an alert). The attributes go afterwards, so a
 * second call never draws a second icon.
 */
export function fillIcons(root = document) {
  const found = [...root.querySelectorAll('[data-icon]')];
  if (root.nodeType === 1 && root.hasAttribute('data-icon')) found.unshift(root);
  for (const node of found) {
    const name = node.getAttribute('data-icon');
    const size = Number(node.getAttribute('data-icon-size')) || 20;
    let svg;
    try {
      svg = iconEl(name, size);
    } catch (err) {
      console.error(err);
      continue;
    }
    if (node.hasAttribute('data-icon-slot')) {
      node.replaceWith(svg);
      continue;
    }
    node.removeAttribute('data-icon');
    node.removeAttribute('data-icon-size');
    node.prepend(svg);
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
 * watchScrolled sets data-scrolled on `target` while `scroller` is scrolled,
 * from a 1px sentinel at its top (the App frame snippet of the DS docs). A
 * sheet body passes its flex gap, which the sentinel takes back as well.
 */
export function watchScrolled(scroller, target, { gap = '' } = {}) {
  const sentinel = document.createElement('div');
  sentinel.setAttribute('aria-hidden', 'true');
  sentinel.setAttribute('data-ds-keep', '');
  sentinel.style.cssText = `height: 1px; margin-bottom: ${gap ? `calc(-1px - ${gap})` : '-1px'}; flex-shrink: 0`;
  scroller.prepend(sentinel);
  new IntersectionObserver((entries) => {
    const entry = entries.at(-1);
    if (!entry.boundingClientRect.height) return;
    if (entry.isIntersecting === !target.hasAttribute('data-scrolled')) return;
    target.toggleAttribute('data-scrolled', !entry.isIntersecting);
    if (entry.isIntersecting) M.resyncScroll(scroller);
  }, { root: scroller }).observe(sentinel);
}

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

/**
 * setFaceLabel changes the words of a button's resting face. Writing the
 * button's textContent would throw its faces away with the old words.
 */
export function setFaceLabel(button, text) {
  const face = button.querySelector('.ds-btn__face:not(.ds-btn__face--busy):not(.ds-btn__face--done)') || button;
  const words = [...face.childNodes].filter((node) => node.nodeType === 3);
  if (words.length === 1 && words[0].data === text) return;
  for (const node of words) node.remove();
  face.append(text);
}

/** region is the page's snackbar region, made right after the frame if the page has none. */
export function region() {
  let node = document.getElementById('toasts');
  if (node) return node;
  node = document.createElement('div');
  node.className = 'ds-toasts';
  node.id = 'toasts';
  const frame = document.querySelector('.ds-app, .so-auth');
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

/** overlayOpen says whether anything modal is on screen: a dialog or sheet, a menu, the modal drawer. */
export function overlayOpen() {
  if (document.querySelector('dialog[open]')) return true;
  if ([...document.querySelectorAll('.ds-menu')].some((menu) => !menu.hidden)) return true;
  return !!document.querySelector('#app[data-drawer-open]');
}
