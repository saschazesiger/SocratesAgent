// Small helpers shared by every page: JSON fetch, DOM building and toasts.

import { request, NetworkError, mountConnectionBar } from './net.js';

export { clientKey, onWake, LiveStream, Outbox, HttpError, NetworkError, RetryLater, setClass, CONNECTION_GRACE } from './net.js';

// Every page gets the connection bar, because every page can be looking at
// something that stopped being true.
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

// confirmDialog is the app's own version of window.confirm: same promise in,
// same yes or no out, but it looks like the rest of Socrates, can say what the
// button actually does, and does not freeze the page while it is open.
export function confirmDialog(options = {}) {
  const {
    title,
    body = '',
    confirmLabel = 'Confirm',
    danger = false,
  } = options;

  return new Promise((resolve) => {
    const dialog = el('dialog', { class: 'modal' });
    const cancel = el('button', { class: 'btn sm', type: 'button', text: 'Cancel' });
    const accept = el('button', {
      class: 'btn sm ' + (danger ? 'danger' : 'primary'), type: 'button', text: confirmLabel,
    });
    dialog.append(
      el('h2', { class: 'modal-title', text: title }),
      body ? el('p', { class: 'modal-body', text: body }) : null,
      el('div', { class: 'modal-actions' }, cancel, accept),
    );

    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
      dialog.close();
    };
    cancel.addEventListener('click', () => finish(false));
    accept.addEventListener('click', () => finish(true));
    // Escape and a click on the backdrop both mean no.
    dialog.addEventListener('cancel', (event) => {
      event.preventDefault();
      finish(false);
    });
    dialog.addEventListener('click', (event) => {
      if (event.target === dialog) finish(false);
    });
    dialog.addEventListener('close', () => {
      finish(false);
      dialog.remove();
    });

    document.body.append(dialog);
    dialog.showModal();
    // A destructive dialog opens on the way out, not on the way through.
    (danger ? cancel : accept).focus();
  });
}

let toastHost = null;
export function toast(message, kind = '') {
  if (!toastHost) {
    toastHost = document.getElementById('toasts');
    if (!toastHost) {
      toastHost = el('div', { class: 'toasts', id: 'toasts' });
      document.body.append(toastHost);
    }
  }
  const node = el('div', { class: 'toast ' + kind, text: message });
  toastHost.append(node);
  setTimeout(() => {
    node.style.transition = 'opacity .25s ease';
    node.style.opacity = '0';
    setTimeout(() => node.remove(), 260);
  }, kind === 'error' ? 6000 : 3200);
}

export function fmtClock(seconds) {
  const s = Math.max(0, Math.floor(seconds));
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}
