// Small helpers shared by every page: JSON fetch, DOM building, snackbars and
// the dialogs every page asks the same questions with.

import { request, NetworkError, HttpError, mountConnectionBar } from './net.js';
import { DS, startUI, iconEl, region } from './ui.js';

export { clientKey, onWake, LiveStream, Outbox, HttpError, NetworkError, RetryLater, setClass, CONNECTION_GRACE } from './net.js';

// Every page gets Dashboard-Style's delegated behaviour (ripple, tooltips, the
// icons of its static markup), and the connection line, because every page
// can be looking at something that stopped being true.
startUI();
mountConnectionBar();

// The offline shell. Without it, opening or reloading Socrates with no signal
// replaces the app with the browser's error page and takes the queued messages
// and the draft with it. Registration is best effort: it needs a secure
// context, which plain http on a LAN address is not.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => { /* http on a LAN address */ });
  });
}

// api is the JSON front door. It keeps the shape callers already expect and
// gets retries, per attempt deadlines and connection reporting from net.js.
//
// A 401 normally means the session is gone and the person has to sign in
// again. Signing in is the one place where it means "wrong password", and
// bouncing back to the login page there would only reload it and swallow the
// message - which is why net.js leaves /api/login alone.
export async function api(path, options = {}) {
  return request(path, options);
}

// isOffline separates "the request never got there" from "the server said no".
// The first is worth retrying and worth saying out loud; the second is a real
// answer that the user has to see.
export function isOffline(err) {
  return err instanceof NetworkError;
}

// errorMessage turns any failure into one sentence a person can act on.
export function errorMessage(err) {
  if (!err) return 'Something went wrong.';
  if (isOffline(err)) return err.message;
  return err.message || String(err);
}

export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === 'class') node.className = value;
    else if (key === 'html') node.innerHTML = value;
    else if (key === 'text') node.textContent = value;
    // textarea and select ignore a value attribute, so set the property
    else if (key === 'value') node.value = value;
    else if (key.startsWith('on') && typeof value === 'function') node.addEventListener(key.slice(2), value);
    else if (value === true) node.setAttribute(key, '');
    else node.setAttribute(key, value);
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child.nodeType ? child : document.createTextNode(String(child)));
  }
  return node;
}

// The page's answer to "the focus has ended up nowhere". A dialog cannot know
// where a page would rather put it - the session page says the terminal, and
// nothing else has an opinion - so it asks, and the default is to do nothing.
//
// It matters because a page whose focus is on the body swallows every key
// silently: the terminal is still drawn, the socket is still live, and nothing
// typed arrives anywhere.
let focusFallback = () => {};

/** setFocusFallback says where the focus goes when a dialog cannot put it back. */
export function setFocusFallback(handler) { focusFallback = handler || (() => {}); }

// restoreFocus puts the focus back on `was`, or asks the page where it should
// go when `was` is gone or was never anything.
export function restoreFocus(was) {
  if (was && was.isConnected && was !== document.body && was !== document.documentElement
    && typeof was.focus === 'function') {
    was.focus();
    return;
  }
  focusFallback();
}

let dialogSeq = 0;

// openDialog builds a Dashboard-Style dialog for one question, shows it and
// resolves with what the person answered. The native `close` event comes at
// the start of every way out - Escape, the backdrop, a [data-close] button,
// an accept that calls close - so it is the one place the answer is given.
// The dialog leaves the page once its exit has played. `field` is any node
// the dialog holds between its text and its actions; a caller with a form of
// its own sets the answer with dialog.answer(value) before closing it.
export function openDialog({ title, body, lines, code, field, actions, alert, opener }) {
  const id = 'dlg' + (++dialogSeq);
  const box = el('div', { class: 'ds-dialog__box' },
    el('h2', { class: 'ds-dialog__title', id: id + '-title', text: title }),
    body ? el('p', { class: 'ds-dialog__text', id: id + '-text', text: body }) : null,
    lines && lines.length
      ? el('ul', { class: 'ds-plain-list', id: body ? null : id + '-text' },
        lines.map((line) => el('li', {}, code ? el('code', { class: 'so-break', text: line }) : line)))
      : null,
    field || null,
    el('div', { class: 'ds-dialog__actions' }, actions));
  const dialog = el('dialog', {
    class: 'ds-dialog',
    role: alert ? 'alertdialog' : null,
    'aria-labelledby': id + '-title',
    'aria-describedby': body || (lines && lines.length) ? id + '-text' : null,
  }, el('div', { class: 'ds-dialog__backdrop', 'data-close': true }), box);

  // Where the focus was. DS gives it back to the opener; when the opener is
  // gone by then - very often the terminal's textarea, rebuilt meanwhile -
  // the page is asked where it should go, because a page whose focus is on
  // the body swallows every key.
  const was = opener || document.activeElement;
  return {
    dialog,
    shown: new Promise((resolve) => {
      let answer = false;
      dialog.answer = (value) => { answer = value; };
      dialog.addEventListener('close', () => resolve(answer), { once: true });
      document.body.append(dialog);
      DS.overlay.open(dialog, { opener: was });
    }).then(async (answer) => {
      await DS.overlay.close(dialog);
      dialog.remove();
      const now = document.activeElement;
      if (!now || now === document.body || now.id === 'main' || now.id === 'sidebar') restoreFocus(was);
      return answer;
    }),
  };
}

// confirmDialog is the app's own version of window.confirm: same promise in,
// same yes or no out, but it looks like the rest of Socrates, can say what the
// button actually does, and does not freeze the page while it is open.
//
// There are no red buttons: a destructive question is an alertdialog that
// opens on Cancel, and its accept button names the consequence.
export function confirmDialog(options = {}) {
  const {
    title,
    body = '',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    danger = false,
    opener = null,
  } = options;
  const cancel = el('button', {
    class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: danger, text: cancelLabel,
  });
  const accept = el('button', {
    class: 'ds-btn ds-btn--primary', type: 'button', autofocus: !danger, text: confirmLabel,
  });
  const { dialog, shown } = openDialog({ title, body, actions: [cancel, accept], alert: danger, opener });
  accept.addEventListener('click', () => {
    dialog.answer(true);
    DS.overlay.close(dialog);
  });
  return shown;
}

// alertDialog is window.alert in the app's own clothes: it resolves once the
// message has been dismissed - true when its button was pressed. `lines` are
// shown as a list, each in <code> when `code` is set (a path, an exit status).
export function alertDialog({ title, body = '', lines = [], code = false, confirmLabel = 'OK', opener = null } = {}) {
  const ok = el('button', {
    class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: true, text: confirmLabel,
  });
  const { dialog, shown } = openDialog({ title, body, lines, code, actions: [ok], opener });
  ok.addEventListener('click', () => dialog.answer(true));
  return shown;
}

// promptDialog is window.prompt in the app's own clothes: one field with its
// label, and a button that says what is done with what was typed. It
// resolves with the trimmed text, or null when the person cancelled.
export function promptDialog({
  title, body = '', label, value = '', placeholder = '', confirmLabel = 'OK', cancelLabel = 'Cancel', opener = null,
} = {}) {
  const fieldId = 'dlg-field' + (dialogSeq + 1);
  const input = el('input', {
    class: 'ds-input', type: 'text', id: fieldId, value, placeholder, spellcheck: 'false', autocomplete: 'off', autofocus: true,
    enterkeyhint: 'done',
  });
  const field = el('div', { class: 'ds-field' }, el('label', { class: 'ds-field__label', for: fieldId, text: label }), input);
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: cancelLabel });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: confirmLabel });
  const { dialog, shown } = openDialog({ title, body, field, actions: [cancel, accept], opener });
  const done = () => {
    dialog.answer(input.value.trim());
    DS.overlay.close(dialog);
  };
  accept.addEventListener('click', done);
  input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.isComposing) {
      event.preventDefault();
      done();
    }
  });
  return shown.then((answer) => (answer === false ? null : answer));
}

// A message that is on screen already is not shown again: an offline burst
// would otherwise stack the same error three times and queue the rest. The
// key is the kind and the words.
const showing = new Map();

/**
 * toast shows a snackbar (DS.overlay.toast). `kind` is 'error' (stays until
 * closed), 'success', or anything else for a neutral 'info'. `action` is
 * { label, run }: a button in the message, which then stays until it is used
 * or closed. Returns DS's handle, { el, close, closed }.
 */
export function toast(message, kind = 'info', { persistent = false, timeout, action } = {}) {
  const text = String(message ?? '');
  const sort = kind === 'error' ? 'error' : kind === 'success' ? 'success' : 'info';
  const key = sort + '\n' + text;
  const open = showing.get(key);
  if (open) return open;
  let content = text;
  if (action) {
    // The documented message, built by hand because it holds a button of its
    // own: DS takes a .ds-toast element as it is.
    const run = el('button', { class: 'ds-btn ds-btn--text', type: 'button', text: action.label });
    content = el('div', { class: sort === 'success' ? 'ds-toast' : 'ds-toast ds-toast--' + sort },
      iconEl(sort === 'error' ? 'alertCircle' : sort === 'info' ? 'info' : 'checkCircle', 20),
      el('span', { class: 'ds-toast__text', text }),
      run,
      el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon ds-toast__close', type: 'button', 'aria-label': 'Close', 'data-tip': 'Close' },
        iconEl('close', 20)));
    run.addEventListener('click', () => {
      handle.close();
      action.run();
    });
  }
  const handle = DS.overlay.toast(region(), content, { kind: sort, persistent, timeout });
  watchDialogRoom();
  // DS draws the close button of a message it builds itself; it says what
  // it does on hover like every other icon button.
  const close = handle.el && handle.el.querySelector('.ds-toast__close');
  if (close && !close.hasAttribute('data-tip')) close.setAttribute('data-tip', close.getAttribute('aria-label') || 'Close');
  showing.set(key, handle);
  const stays = persistent || sort === 'error' || !!action;
  if (stays) {
    staying.add(handle);
    watchReserve();
    requestAnimationFrame(reserveToasts);
  }
  handle.closed.then(() => {
    if (showing.get(key) === handle) showing.delete(key);
    if (staying.delete(handle)) requestAnimationFrame(reserveToasts);
  });
  return handle;
}

/** toastsStay says whether a message that stays until it is closed is on screen. */
export function toastsStay() { return staying.size > 0; }

/**
 * onToastsStay is told when that may have changed - a page that keeps room
 * of its own for such a message (the session page's chat, session.js
 * placeToasts) - and hands back its own undo.
 */
export function onToastsStay(fn) {
  stayWatchers.add(fn);
  return () => stayWatchers.delete(fn);
}

// A message that stays until it is closed keeps room at the end of what
// scrolls under it - the open sheet's body, or the page's view (Admin,
// Costs) - so that the last actions there can scroll clear of it (DS
// data-toast-reserve and --ds-toast-reserve): its height and its distance
// from the bottom edge. The session page's view does not scroll; what
// stands at its bottom moves the messages instead, and a chat's
// conversation keeps the room at its own end (session.js placeToasts).
// The region moves into a sheet that opens and back out when it closes
// (DS), and the room moves with it; in a dialog, the dialog keeps it
// (keepDialogRoom).
const staying = new Set();
const stayWatchers = new Set();
let reserveWatched = false;

function watchReserve() {
  if (reserveWatched) return;
  reserveWatched = true;
  new MutationObserver(() => { if (staying.size || document.querySelector('[data-toast-reserve]')) reserveToasts(); })
    .observe(document.body, { childList: true });
}

function reserveToasts() {
  for (const fn of [...stayWatchers]) {
    try { fn(); } catch { /* one bad listener must not stop the others */ }
  }
  const node = document.getElementById('toasts');
  const sheet = node && staying.size ? node.closest('.ds-sheet') : null;
  const target = !node || !staying.size ? null
    : sheet ? sheet.querySelector('.ds-sheet__body') : document.querySelector('.ds-view:not(.so-view)');
  for (const one of document.querySelectorAll('[data-toast-reserve]')) {
    if (one === target) continue;
    one.removeAttribute('data-toast-reserve');
    one.style.removeProperty('--ds-toast-reserve');
  }
  if (!target) return;
  const room = Math.max(0, Math.round(window.innerHeight - node.getBoundingClientRect().top));
  target.setAttribute('data-toast-reserve', '');
  target.style.setProperty('--ds-toast-reserve', room + 'px');
}

// A message shown while a dialog is open stands in the dialog, above its
// scrim, where it stood: DS moves the region in and leaves it at the
// bottom edge - across the width, on a phone. There it lay over the
// dialog's own last controls: an action sheet's Delete, the question's
// options at the end of its body, Rename over a keyboard. So while a
// message shows over its box, the dialog keeps that strip, and the box ends
// above the messages rather than under them (so-shared.css
// [data-so-toast-room]): the region's height and its distance from the
// bottom edge as the page placed it (session.js lifts it past a keyboard,
// and asks again when it does), and a little air. A box that stands clear
// of the messages - a short form in the middle of a tall screen, beside a
// short message at a desk - does not move. Where things stand is read from
// the layout (offsets and the region's own bottom), without the room and
// without the transforms of an entrance. A dialog on its way out keeps the
// room until it has gone: its box would drop under the messages as it fades.
export function keepDialogRoom() {
  const node = document.getElementById('toasts');
  const host = node && !node.closest('.ds-sheet') ? node.closest('dialog.ds-dialog[open]') : null;
  const box = host ? host.querySelector('.ds-dialog__box') : null;
  let room = '';
  if (box && node.querySelector('.ds-toast') && node.offsetHeight) {
    host.removeAttribute('data-so-toast-room');
    const look = getComputedStyle(node);
    const edge = (parseFloat(look.bottom) || 0) + (parseFloat(look.marginBottom) || 0);
    const top = host.clientHeight - edge - node.offsetHeight;
    const left = parseFloat(look.left) || 0;
    const air = parseFloat(getComputedStyle(host).getPropertyValue('--ds-space-3')) || 0;
    const over = box.offsetTop + box.offsetHeight > top - air
      && box.offsetLeft < left + node.offsetWidth && box.offsetLeft + box.offsetWidth > left;
    if (over) room = 'calc(' + Math.round(edge + node.offsetHeight) + 'px + var(--ds-space-3))';
  }
  for (const one of document.querySelectorAll('[data-so-toast-room]')) {
    if (one === host || one.classList.contains('ds-dialog--closing')) continue;
    one.removeAttribute('data-so-toast-room');
    one.style.removeProperty('--so-toast-room');
  }
  // A box that grows (a question's next step, a longer form) can grow into
  // the messages: it is watched while it holds them.
  if (dialogRoomWatcher && roomBox !== box) {
    if (roomBox) dialogRoomWatcher.unobserve(roomBox);
    if (box) dialogRoomWatcher.observe(box);
    roomBox = box;
  }
  if (!host) return;
  if (!room) {
    host.removeAttribute('data-so-toast-room');
    host.style.removeProperty('--so-toast-room');
    return;
  }
  if (host.style.getPropertyValue('--so-toast-room') !== room) host.style.setProperty('--so-toast-room', room);
  host.setAttribute('data-so-toast-room', '');
}

// The room follows the messages (the region's height), a dialog opening
// or closing over them, and the region going back to the page. Watched
// from the first message on: before it there is nothing to keep room for.
let dialogRoomWatched = false;
let dialogRoomWatcher = null;
let roomBox = null;
function watchDialogRoom() {
  const node = document.getElementById('toasts');
  if (dialogRoomWatched || !node) return;
  dialogRoomWatched = true;
  if (window.ResizeObserver) {
    dialogRoomWatcher = new ResizeObserver(() => keepDialogRoom());
    dialogRoomWatcher.observe(node);
  }
  new MutationObserver(() => keepDialogRoom()).observe(document.body, { childList: true });
  new MutationObserver((records) => {
    if (records.some((r) => r.target.localName === 'dialog')) keepDialogRoom();
  }).observe(document.body, { attributes: true, attributeFilter: ['open'], subtree: true });
}

// isBusyConflict is the one refusal that passes on its own: the session is still
// working on the previous turn. Everything permanent uses another status, so
// this is the only one worth waiting out rather than showing as a failure.
export function isBusyConflict(err) {
  return err instanceof HttpError && err.status === 409;
}

// fmtTokens is the usage line's unit: 12345 -> "12.3k". Whole thousands lose
// the trailing zero, because "12.0k" reads as more precision than there is.
export function fmtTokens(n) {
  const value = Number(n) || 0;
  if (value < 1000) return String(Math.round(value));
  if (value < 1000000) {
    const k = value / 1000;
    return (k < 10 ? k.toFixed(1) : String(Math.round(k))).replace(/\.0$/, '') + 'k';
  }
  const m = value / 1000000;
  return (m < 10 ? m.toFixed(1) : String(Math.round(m))).replace(/\.0$/, '') + 'M';
}

export function fmtClock(seconds) {
  const s = Math.max(0, Math.floor(seconds));
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}

// ------------------------------------------------------------- the "i"

// infoTip is the small "i" that stands in for a line of detail nobody needs
// until they do: a build number, a path, the full binding behind a badge. A
// pointer or the keyboard gets the lines as a tooltip; a press opens them in a
// dialog, because a tooltip never shows for a finger and an "i" a finger
// cannot open would be decoration. The lines are in the button's data-tip the
// whole time, so a test or a script finds them without opening anything.
export function infoTip(content, { label = 'Details', code = false } = {}) {
  const lines = (Array.isArray(content) ? content : [content])
    .filter((line) => line !== null && line !== undefined && line !== '')
    .map((line) => (line && line.nodeType ? line.textContent : String(line)));
  const button = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info',
    type: 'button',
    'aria-label': label,
    'data-tip': lines.join('\n'),
  }, iconEl('info', 18));
  button.addEventListener('click', (event) => {
    // It often sits inside something that reacts to a click of its own: a
    // row, a summary.
    event.preventDefault();
    event.stopPropagation();
    alertDialog({ title: label, lines, code, confirmLabel: 'Close', opener: button });
  });
  return button;
}
