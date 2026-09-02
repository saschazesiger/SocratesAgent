// Small helpers shared by every page: JSON fetch, DOM building and toasts.

import { request, NetworkError, HttpError, mountConnectionBar } from './net.js';

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

// ------------------------------------------------------------- the tooltip

// infoTip is the small "i" that stands in for a line of detail nobody needs
// until they do: a build number, a path, the full binding behind a badge. The
// detail is in the page the whole time - a test or a screen reader finds it -
// but it is only drawn when the mark is hovered, focused, or tapped.
//
// A tap is the phone's hover: the mark toggles the bubble, and a tap anywhere
// else closes it. The bubble is nudged back inside the viewport once it is
// open, because a phone puts a mark close enough to the edge for a centred
// bubble to hang off it.
//
// content is a string, a node, or a list of either; every string is its own
// line. The returned element is the whole control, with the bubble inside it.
let openTip = null;
let tipSeq = 0;

export function infoTip(content, options = {}) {
  const { label = 'Details', bubbleClass = '', variant = '' } = options;
  const id = 'tip-' + (++tipSeq);
  const lines = (Array.isArray(content) ? content : [content]).filter((line) => line !== null && line !== undefined && line !== '');
  const bubble = el('span', { class: ('tip-bubble ' + bubbleClass).trim(), id, role: 'tooltip' },
    ...lines.map((line) => (line && line.nodeType ? line : el('span', { class: 'tip-line', text: String(line) }))));
  const mark = el('button', {
    class: 'tip-mark',
    type: 'button',
    'aria-label': label,
    'aria-describedby': id,
  });
  mark.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/></svg>';
  const tip = el('span', { class: ('tip ' + variant).trim() }, mark, bubble);

  const close = () => {
    tip.classList.remove('open');
    if (openTip === tip) openTip = null;
  };
  const open = () => {
    if (openTip && openTip !== tip) openTip.classList.remove('open');
    openTip = tip;
    tip.classList.add('open');
    placeTip(tip, bubble);
  };
  mark.addEventListener('click', (event) => {
    event.preventDefault();
    event.stopPropagation();
    if (tip.classList.contains('open')) close(); else open();
  });
  mark.addEventListener('mouseenter', () => placeTip(tip, bubble));
  mark.addEventListener('focus', () => placeTip(tip, bubble));
  // The finger that opens a bubble leaves it open: the mark's own click
  // handler is the one that closes it, and the document's handler below
  // closes it for a tap anywhere else.
  bubble.addEventListener('click', (event) => event.stopPropagation());
  mark.addEventListener('keydown', (event) => { if (event.key === 'Escape') { close(); mark.blur(); } });
  return tip;
}

// placeTip puts the bubble under its mark. The bubble is fixed to the
// viewport rather than to the mark for two reasons: a bubble inside a badge
// that clips its overflow would be clipped with it, and a hidden bubble that
// hangs past the right edge of a phone would make the page scroll sideways
// even while nobody can see it. It is measured after the bubble is shown, so
// it is called from the same events that show it.
function placeTip(tip, bubble) {
  placeBubble(tip.querySelector('.tip-mark') || tip, bubble);
}

/**
 * placeBubble sets a fixed bubble's position from its anchor: centred under
 * it, then slid sideways until the whole bubble is on screen.
 */
export function placeBubble(anchor, bubble) {
  const rect = anchor.getBoundingClientRect();
  const margin = 10;
  bubble.style.top = Math.round(rect.bottom + 7) + 'px';
  bubble.style.left = Math.round(rect.left + rect.width / 2) + 'px';
  bubble.style.setProperty('--tip-shift', '0px');
  requestAnimationFrame(() => {
    const box = bubble.getBoundingClientRect();
    if (!box.width) return;
    let shift = 0;
    if (box.left < margin) shift = margin - box.left;
    else if (box.right > window.innerWidth - margin) shift = (window.innerWidth - margin) - box.right;
    bubble.style.setProperty('--tip-shift', Math.round(shift) + 'px');
    // A mark at the bottom of a phone's screen gets its bubble above it.
    if (box.bottom > window.innerHeight - margin) {
      bubble.style.top = Math.round(rect.top - 7 - box.height) + 'px';
    }
  });
}

// A tap or click anywhere else puts an open bubble away - including a tap on
// the scrim of a drawer, which is how the same gesture behaves everywhere.
document.addEventListener('click', (event) => {
  if (openTip && !openTip.contains(event.target)) {
    openTip.classList.remove('open');
    openTip = null;
  }
});
// Escape closes the bubble and nothing else: the dialog under it stays open,
// which is why this runs before the browser's own Escape handling.
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && openTip) {
    event.preventDefault();
    event.stopPropagation();
    openTip.classList.remove('open');
    // The mark keeps focus after a tap, and focus alone keeps the bubble
    // drawn; Escape means "put it away", so the focus goes with it.
    if (openTip.contains(document.activeElement)) document.activeElement.blur();
    openTip = null;
  }
}, true);
// A bubble is placed for where its mark was; once the page moves under it,
// it is put away rather than left floating over the wrong thing.
document.addEventListener('scroll', () => {
  if (openTip) {
    openTip.classList.remove('open');
    openTip = null;
  }
}, { capture: true, passive: true });
