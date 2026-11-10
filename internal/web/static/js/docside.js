// A Markdown file of a chat's folder beside the chat (docs/design/DOCS.md,
// "Links in a chat"): a link in the conversation to a .md file inside the
// chat's working directory - `docs/ARCHITECTURE.md`, its absolute path, a
// file:// address - opens the file here rather than in a tab that could not
// show it. At a desk it is a column on the chat's right, like the tests'
// and the artifacts' (never open with them); narrower a full screen dialog.
// "Open in Docs" takes it to the folder's Docs page with its tree.

import { el, toast } from './api.js';
import { html, raw, icon, iconEl, morph, showOverlay, hideOverlay } from './ui.js';
import { SANDBOX, frameName, listing, pageUrl, stat, listenFrame, fileName, isMarkdown } from './docview.js';
import { docsHash } from './docs.js';

// Over this width the file is a column beside the chat (as the artifacts').
const WIDE = window.matchMedia('(min-width: 1101px)');
// How often the file on screen is checked for a change on disk.
const WATCH = 4 * 1000;

/**
 * docLinkTarget is what a link in a chat points to in the chat's folder:
 * {path, at} for a Markdown file inside `folder` (relative to it, `at` a
 * heading), or null for anything else - a web address, a file outside the
 * folder, a file that is not Markdown.
 */
export function docLinkTarget(href, folder) {
  let ref = String(href || '').trim();
  if (!ref || !folder || ref.startsWith('#') || ref.startsWith('//')) return null;
  if (/^file:\/\//i.test(ref)) ref = ref.replace(/^file:\/\/(localhost)?/i, '');
  else if (/^[a-z][a-z0-9+.-]*:/i.test(ref)) return null;
  let at = '';
  const hashAt = ref.indexOf('#');
  if (hashAt >= 0) {
    at = ref.slice(hashAt + 1);
    ref = ref.slice(0, hashAt);
  }
  const queryAt = ref.indexOf('?');
  if (queryAt >= 0) ref = ref.slice(0, queryAt);
  try {
    ref = decodeURIComponent(ref);
    at = decodeURIComponent(at);
  } catch { return null; }
  if (!isMarkdown(ref)) return null;
  const root = folder.replace(/\/+$/, '');
  let rest;
  if (ref.startsWith('/')) {
    if (!ref.startsWith(root + '/')) return null;
    rest = ref.slice(root.length + 1);
  } else {
    rest = ref;
  }
  const parts = [];
  for (const part of rest.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') {
      if (!parts.length) return null;
      parts.pop();
      continue;
    }
    parts.push(part);
  }
  if (!parts.length) return null;
  return { path: parts.join('/'), at };
}

/**
 * mountChatDocside wires the column (`panel`, #docsidePanel) and the
 * dialog. `onColumnOpen()` is called when the column opens (the tests' and
 * the artifacts' columns shut then). It answers
 *   open(folder, path, {at, opener})   a file of a chat's folder
 *   hide()                             the chat left the stage
 *   shut()                             another column opened
 *   isOpen()                           whether a file is shown
 */
export function mountChatDocside({ panel, onColumnOpen }) {
  // What is shown: {folder, path, base, mtime, y, title}, or null.
  let doc = null;
  let dlg = null;
  let timer = null;
  let gen = 0;

  const title = el('h2', { class: 'so-docside__title', id: 'docsideTitle', tabindex: '-1', text: 'Docs' });
  const actions = el('div', { class: 'so-docside__actions' });
  const closeBtn = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button',
    'aria-label': 'Close the file', 'data-tip': 'Close the file',
  }, iconEl('close', 18));
  const frame = el('iframe', { class: 'so-docs__view', title: 'Document', sandbox: SANDBOX, referrerpolicy: 'no-referrer', name: frameName() });
  const box = el('div', { class: 'so-docside__frame' }, frame);
  panel.replaceChildren(el('div', { class: 'so-docside__head' }, el('span', { class: 'so-docside__glyph', 'aria-hidden': 'true' }, iconEl('fileText', 18)), title, actions, closeBtn), box);

  function actionsHtml() {
    if (!doc) return '';
    return html`<a class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" href="${docsHash(doc.folder, doc.path)}" data-docside="docs" aria-label="Open in Docs, with every file of the folder" data-tip="Open in Docs">${raw(icon('list', 18))}</a><a class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" href="${pageUrl(doc.base, doc.path)}" target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab" data-tip="Open in a new tab">${raw(icon('external', 18))}</a>`;
  }

  function drawHead(heading) {
    const words = doc ? (doc.title || fileName(doc.path)) : 'Docs';
    if (heading.textContent !== words) heading.textContent = words;
    if (doc) heading.setAttribute('data-tip', doc.path);
  }

  function draw() {
    drawHead(title);
    morph(actions, html`${actionsHtml()}`);
    if (dlg) dlg.draw();
  }

  /* ------------------------------------------------------------- column */

  function showColumn() {
    if (onColumnOpen) onColumnOpen();
    panel.hidden = false;
    box.append(frame);
  }

  function shutColumn() {
    panel.hidden = true;
  }

  closeBtn.addEventListener('click', () => close());

  /* ------------------------------------------------------------- dialog */

  function openDialog(opener) {
    if (dlg) return;
    const heading = el('h2', { class: 'ds-dialog__title so-docside__title', id: 'docsideDialogTitle' });
    const head = el('div', { class: 'so-docside__actions' });
    const back = el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-close': true, 'aria-label': 'Back', 'data-tip': 'Back' }, iconEl('arrowLeft', 20));
    const body = el('div', { class: 'ds-dialog__body so-docside__dialog-body' }, frame);
    const dialog = el('dialog', { class: 'ds-dialog ds-dialog--full so-docside-dialog', 'aria-labelledby': 'docsideDialogTitle' },
      el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
      el('div', { class: 'ds-dialog__box' },
        el('div', { class: 'ds-dialog__head' }, back, heading, head),
        body));
    const mine = {
      dialog,
      draw() {
        drawHead(heading);
        morph(head, html`${actionsHtml()}`);
      },
    };
    dialog.addEventListener('click', (event) => {
      if (event.target.closest('[data-docside="docs"]')) hideOverlay(dialog);
    });
    dialog.addEventListener('close', () => {
      if (dlg === mine) {
        dlg = null;
        stop();
      }
      hideOverlay(dialog, { remove: true });
    }, { once: true });
    dlg = mine;
    mine.draw();
    showOverlay(dialog, opener && opener.isConnected ? opener : null);
  }

  /* ------------------------------------------------------------ watching */

  async function look() {
    if (!doc || document.hidden) return;
    const shown = doc;
    try {
      const s = await stat(shown.folder, shown.path);
      if (doc !== shown || !s) return;
      if (shown.mtime && s.mtime !== shown.mtime) frame.src = pageUrl(shown.base, shown.path, { y: shown.y });
      shown.mtime = s.mtime;
    } catch { /* offline: the next look tries again */ }
  }

  function stop() {
    clearInterval(timer);
    timer = null;
    doc = null;
    gen += 1;
    frame.removeAttribute('src');
  }

  listenFrame(frame, (m) => {
    if (!doc) return;
    if (m.t === 'page' && typeof m.path === 'string') {
      if (doc.sent && m.path !== doc.sent) {
        frame.src = pageUrl(doc.base, doc.sent);
        return;
      }
      doc.sent = '';
      if (m.path !== doc.path) {
        doc.path = m.path;
        doc.mtime = 0;
        look();
      }
      doc.title = String(m.title || '');
      draw();
    } else if (m.t === 'scroll') {
      doc.y = Number(m.y) || 0;
    } else if (m.t === 'file') {
      toast('“' + fileName(m.path) + '” is not a Markdown file: only Markdown is shown here.');
    } else if (m.t === 'outside') {
      toast('That link leads out of this folder.');
    }
  });

  /* --------------------------------------------------------------- open */

  async function open(folder, path, { at = '', opener = null } = {}) {
    const my = ++gen;
    let data;
    try {
      data = await listing(folder);
    } catch {
      toast('The files of this folder could not be read.', 'error');
      return;
    }
    if (my !== gen) return;
    // sent: the file the frame was sent to, until it says it shows it (a
    // frame the browser gives its old address back is sent there again).
    doc = { folder, path, base: data.base, mtime: 0, y: 0, title: '', sent: path };
    frame.src = pageUrl(data.base, path, { hash: at });
    if (WIDE.matches) {
      if (dlg) hideOverlay(dlg.dialog);
      showColumn();
      draw();
      title.focus({ preventScroll: true });
    } else {
      shutColumn();
      draw();
      openDialog(opener);
    }
    clearInterval(timer);
    timer = setInterval(look, WATCH);
    look();
  }

  function close() {
    shutColumn();
    if (dlg) hideOverlay(dlg.dialog);
    stop();
  }

  // A window that crosses the column's width takes the file along.
  WIDE.addEventListener('change', () => {
    if (!doc) return;
    if (WIDE.matches && dlg) {
      const was = dlg;
      dlg = null;
      showColumn();
      draw();
      hideOverlay(was.dialog, { remove: true });
    } else if (!WIDE.matches && !panel.hidden) {
      shutColumn();
      openDialog(null);
    }
  });

  return {
    open,
    hide: close,
    shut() {
      if (!panel.hidden) close();
    },
    isOpen: () => !!doc,
  };
}
