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
export function openDialog({ title, body, lines, facts, code, field, actions, alert, opener }) {
  const id = 'dlg' + (++dialogSeq);
  const listed = !!(lines && lines.length);
  const named = !!(facts && facts.length);
  // The text and the lines are often a path, a URL or an error that quotes
  // one: they break anywhere rather than run out of the box (so-break).
  const box = el('div', { class: 'ds-dialog__box' },
    el('h2', { class: 'ds-dialog__title', id: id + '-title', text: title }),
    body ? el('p', { class: 'ds-dialog__text so-break', id: id + '-text', text: body }) : null,
    listed
      ? el('ul', { class: 'ds-plain-list', id: body ? null : id + '-text' },
        lines.map((line) => el('li', { class: 'so-break' }, code ? el('code', { class: 'so-break', text: line }) : line)))
      : null,
    // Named facts ([label, value]) as the DS facts list, Info's shape.
    named
      ? el('dl', { class: 'ds-facts so-facts', id: body || listed ? null : id + '-text' },
        facts.map(([label, value]) => el('div', {}, el('dt', { text: label }), el('dd', { class: 'so-fact', text: value }))))
      : null,
    field || null,
    el('div', { class: 'ds-dialog__actions' }, actions));
  const dialog = el('dialog', {
    class: 'ds-dialog',
    role: alert ? 'alertdialog' : null,
    'aria-labelledby': id + '-title',
    'aria-describedby': body || listed || named ? id + '-text' : null,
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
      afterDialog(was);
      return answer;
    }),
  };
}

// afterDialog asks the page where the focus goes when a dialog could not
// give it back: DS returns it to the opener, else to the page's landmark,
// and on the session page that swallows the keys meant for the terminal.
function afterDialog(was) {
  const now = document.activeElement;
  if (!now || now === document.body || now.id === 'main' || now.id === 'sidebar') restoreFocus(was);
}

// confirmDialog is window.confirm in the app's own clothes: DS's question
// with yes or no (DS.overlay.confirm), same promise in, same yes or no
// out, and a button that says what it actually does. There are no red
// buttons: a destructive question (`danger`) is an alertdialog that opens
// on Cancel, and its accept button names the consequence.
export async function confirmDialog({
  title, body = '', confirmLabel = 'Confirm', cancelLabel = 'Cancel', danger = false, opener = null,
} = {}) {
  const was = opener || document.activeElement;
  const yes = await DS.overlay.confirm({ title, text: body, confirmLabel, cancelLabel, destructive: danger, opener: was });
  afterDialog(was);
  return yes;
}

// alertDialog is window.alert in the app's own clothes: it resolves once the
// message has been dismissed - true when its button was pressed. `lines` are
// shown as a list, each in <code> when `code` is set (a path, an exit status);
// `facts` ([label, value]) as labelled rows after them.
export function alertDialog({ title, body = '', lines = [], facts = [], code = false, confirmLabel = 'OK', opener = null } = {}) {
  const ok = el('button', {
    class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: true, text: confirmLabel,
  });
  const { dialog, shown } = openDialog({ title, body, lines, facts, code, actions: [ok], opener });
  ok.addEventListener('click', () => dialog.answer(true));
  return shown;
}

// promptDialog is window.prompt in the app's own clothes: DS's one-field
// dialog (DS.overlay.prompt), its label, and a button that says what is
// done with what was typed. It resolves with the trimmed text, or null when
// the person cancelled.
//
// What is typed here is a name or a path (Point to folder): a phone must
// neither correct nor capitalise it, and the spell checker has nothing to
// underline. DS's prompt() takes only the field's type, autocomplete,
// inputmode and required, so the rest is set on its field, which is in the
// page as soon as prompt() returns (DS.overlay.prompt builds the dialog and
// opens it synchronously; its autofocus lands a frame later).
export async function promptDialog({
  title, body = '', label, value = '', confirmLabel = 'OK', cancelLabel = 'Cancel', opener = null,
} = {}) {
  const was = opener || document.activeElement;
  const before = new Set(document.querySelectorAll('.ds-dialog'));
  const asked = DS.overlay.prompt({
    title, text: body, label, value, confirmLabel, cancelLabel, opener: was, input: { autocomplete: 'off' },
  });
  const dialog = [...document.querySelectorAll('.ds-dialog')].find((node) => !before.has(node));
  const field = dialog && dialog.querySelector('.ds-input');
  if (field) {
    field.spellcheck = false;
    field.setAttribute('autocapitalize', 'off');
    field.setAttribute('autocorrect', 'off');
    field.setAttribute('enterkeyhint', 'done');
  }
  const answer = await asked;
  afterDialog(was);
  return answer === null ? null : answer.trim();
}

/**
 * toast shows a snackbar (DS.overlay.toast). `kind` is 'error' (stays until
 * closed), 'success', or anything else for a neutral 'info'. `action` is
 * { label, run }: a text button in the message, which then stays until it is
 * used or closed. Returns DS's handle, { el, close, closed }.
 *
 * DS does what this once did by hand: the same words while they show
 * restart the message rather than stack it (an offline burst says it once),
 * a message with a button replaces its twin (by key), the close button says
 * what it does on hover, and a message that stays keeps room at the end of
 * what scrolls under it - an open sheet's body, an open dialog, the page's
 * view (Admin, Costs). DS decides where whenever the region moves, so a
 * message raised before a dialog or the action sheet opens keeps room in
 * it too, and one raised in a dialog gives that room back when it closes.
 * The session page's view does not scroll (.ds-view--fixed): its stage
 * fills it, and it keeps no room (DS data-ds-toast-room="off" on it in
 * index.html); what stands at its bottom moves the messages instead, and a
 * chat's conversation keeps the room at its own end (session.js
 * placeToasts).
 */
export function toast(message, kind = 'info', { persistent = false, timeout, action } = {}) {
  const text = String(message ?? '');
  const sort = kind === 'error' ? 'error' : kind === 'success' ? 'success' : 'info';
  const node = region();
  const options = { kind: sort, persistent, timeout };
  if (action) {
    options.action = { label: action.label, onClick: () => action.run() };
    options.key = sort + '\n' + text;
  }
  const handle = DS.overlay.toast(node, text, options);
  const stays = persistent || sort === 'error' || !!action;
  if (stays && !staying.has(handle)) {
    staying.add(handle);
    tellStayWatchers();
    handle.closed.then(() => { if (staying.delete(handle)) tellStayWatchers(); });
  }
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

const staying = new Set();
const stayWatchers = new Set();

// Once the message is laid out: the watchers measure the stack.
function tellStayWatchers() {
  requestAnimationFrame(() => {
    for (const fn of [...stayWatchers]) {
      try { fn(); } catch { /* one bad listener must not stop the others */ }
    }
  });
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
