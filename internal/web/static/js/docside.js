// A Markdown file of a chat's folder beside the chat (docs/design/DOCS.md,
// "Links in a chat"): a link in the conversation to a .md file inside the
// chat's working directory - `docs/ARCHITECTURE.md`, its absolute path, a
// file:// address - opens the file here rather than in a tab that could not
// show it, and so does a page of the chat's pages folder (pages.js). At a
// desk it is a column on the chat's right, like the tests' (never open with
// it); narrower a full screen dialog. A file the folder does not have is
// looked for in the other working trees of its git repository: an agent
// working in a `git worktree` of its own links what it wrote there.
// "Open in Docs" takes it to the folder's Docs page with its tree.

import { api, el, toast } from './api.js';
import { html, raw, icon, iconEl, morph, showOverlay, hideOverlay } from './ui.js';
import { SANDBOX, frameName, listing, pageUrl, stat, locate, listenFrame, fileName, isMarkdown } from './docview.js';
import { docsHash } from './docs.js';
import { mountFileView, kindGlyph, kindWord, sizeText, withVersion } from './fileview.js';

// Over this width the file is a column beside the chat (as the tests').
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
 * outsideTarget is a path link that leads out of `folder` (docLinkTarget
 * said null) as {path, at}, `path` absolute: a path relative to the folder
 * is joined to it. null for anything that is no path to a Markdown file.
 */
export function outsideTarget(href, folder) {
  let ref = String(href || '').trim().replace(/^file:\/\/(localhost)?/i, '');
  if (!isPathLink(ref) || !folder) return null;
  if (!ref.startsWith('/')) ref = folder.replace(/\/+$/, '') + '/' + ref;
  const target = docLinkTarget(ref, '/');
  return target ? { path: '/' + target.path, at: target.at } : null;
}

/**
 * outsideHref is a link in a file shown beside the chat (`path` in `folder`)
 * that leads out of the folder, as an absolute path with its heading: one
 * relative to the file is joined to the file's own folder. '' for anything
 * that is no path to a Markdown file.
 */
export function outsideHref(href, folder, path) {
  const ref = String(href || '').trim();
  if (!isPathLink(ref) || !folder) return '';
  const dir = String(path || '').split('/').slice(0, -1).join('/');
  const base = folder.replace(/\/+$/, '') + (dir ? '/' + dir : '');
  const target = outsideTarget(ref, base);
  return target ? target.path + (target.at ? '#' + target.at : '') : '';
}

/**
 * forgeLinkTarget is a link to a file of the folder by its address on the
 * repository's forge - https://github.com/owner/name/blob/main/docs/x.md,
 * GitLab's /-/blob/ - as docLinkTarget's {path, at}, when `repo` (docview.js
 * knownRepo: {remote, prefix}) is the repository the address names and the
 * file is a Markdown file inside the folder; else null. The branch in the
 * address is not looked at: the file beside the chat is the one on disk.
 */
export function forgeLinkTarget(href, repo) {
  if (!repo || !repo.remote) return null;
  let url;
  try { url = new URL(String(href || '')); } catch { return null; }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') return null;
  const parts = url.pathname.split('/').filter(Boolean);
  const blob = parts.indexOf('blob');
  if (blob < 2 || parts.length < blob + 3) return null;
  let name = parts.slice(0, blob);
  if (name[name.length - 1] === '-') name = name.slice(0, -1);
  let remote;
  try { remote = (url.hostname + '/' + name.map(decodeURIComponent).join('/')).toLowerCase(); } catch { return null; }
  if (remote !== String(repo.remote).toLowerCase()) return null;
  let rel = parts.slice(blob + 2).join('/');
  const prefix = String(repo.prefix || '').replace(/^\/+|\/+$/g, '');
  if (prefix) {
    if (!rel.startsWith(prefix + '/')) return null;
    rel = rel.slice(prefix.length + 1);
  }
  // The heading GitHub's own anchors name is the kit's too.
  return docLinkTarget(rel + url.hash, '/');
}

/**
 * isPathLink is whether a link's target is a path to a Markdown file rather
 * than an address (no scheme): one that names no file of the folder is said
 * so, rather than left to a tab that would show Socrates' own page.
 */
export function isPathLink(href) {
  const ref = String(href || '').trim().split(/[?#]/)[0];
  return !!ref && !/^[a-z][a-z0-9+.-]*:/i.test(ref) && !ref.startsWith('//') && isMarkdown(ref);
}

/**
 * fileLinkRef is the path a link names when it is a path to a file that is
 * not Markdown (a video, a PDF, a log): absolute, a file:// address, or
 * relative to the chat's folder, as the agent wrote it (percent-encoding
 * undone, as marked adds it), without a query or a heading. '' for an
 * address of another scheme, a Markdown file, or a name with no extension.
 */
export function fileLinkRef(href) {
  let ref = String(href || '').trim();
  if (!ref || ref.startsWith('#') || ref.startsWith('//')) return '';
  if (/^file:\/\//i.test(ref)) ref = ref.replace(/^file:\/\/(localhost)?/i, '');
  else if (/^[a-z][a-z0-9+.-]*:/i.test(ref)) return '';
  ref = ref.split(/[?#]/)[0];
  try { ref = decodeURI(ref); } catch { /* as it is */ }
  const name = ref.split('/').pop();
  if (!name || !/\.[a-z0-9]{1,12}$/i.test(name) || isMarkdown(ref) || ref.startsWith('~')) return '';
  return ref;
}

/**
 * fileIn is the listed file a link's path means: the path itself, else -
 * an agent naming a file by its name or the end of its path - the one file
 * whose path ends in it. null when there is none or more than one.
 */
export function fileIn(files, path) {
  const list = (files || []).map((f) => f.path);
  if (list.includes(path)) return path;
  const tail = '/' + path;
  const found = list.filter((p) => p.endsWith(tail));
  return found.length === 1 ? found[0] : null;
}

/**
 * findDoc is the file a link to `path` in `folder` means, as the column and
 * a new tab show it: {folder, path, base} (`base` the folder's address,
 * docview.js pageUrl), or {error} with the words that say why there is none.
 * A path the listing does not have is the one file it may mean, a file
 * written since the listing kept was read, or (`elsewhere`) the file in
 * another working tree of the folder's repository, where the chat's agent
 * may have written it.
 */
export async function findDoc(folder, path, { elsewhere = true } = {}) {
  const unread = { error: 'The files of this folder could not be read.' };
  const missing = { error: `${path} is not a Markdown file of this chat's folder.` };
  let data;
  try {
    data = await listing(folder);
  } catch { return unread; }
  let found = fileIn(data.files, path);
  if (!found && !data.truncated) {
    try {
      data = await listing(folder, { fresh: true });
    } catch { return unread; }
    found = fileIn(data.files, path);
  }
  // A listing cut short may not name a file the folder has.
  if (!found && data.truncated) {
    try {
      const s = await stat(folder, path);
      if (s) found = s.path;
    } catch { /* looked for below */ }
  }
  if (found) return { folder, path: found, base: data.base };
  if (!elsewhere) return missing;
  let there = null;
  try {
    there = await locate(folder, path);
  } catch { /* said below */ }
  if (!there) return missing;
  if (there.folder !== folder) return findDoc(there.folder, there.path, { elsewhere: false });
  return { folder, path: there.path, base: data.base };
}

/**
 * findOutside is findDoc for a link out of the folder, by an absolute path
 * or one with "..": the file when it lies in another working tree of the
 * folder's repository, with the heading the link names ({..., at}).
 */
export async function findOutside(folder, href) {
  const target = outsideTarget(href, folder);
  let there = null;
  if (target) {
    try { there = await locate(folder, target.path); } catch { /* said below */ }
  }
  if (!there) return { error: `${href} is not a Markdown file of this chat's folder.` };
  const found = await findDoc(there.folder, there.path, { elsewhere: false });
  return found.error ? found : { ...found, at: target.at };
}

/**
 * folderFile is a file of a folder the Docs routes serve every file of (a
 * chat's pages folder) as the viewer beside the chat shows it: {src,
 * download, name, kind, size, mtime, folder, path} - `src` its address,
 * `download` the one that saves it - or {error}.
 */
export async function folderFile(folder, path) {
  let data;
  let s = null;
  try {
    data = await listing(folder);
    s = await stat(folder, path);
  } catch {
    return { error: 'The files of this folder could not be read.' };
  }
  if (!s) return { error: `${fileName(path)} is not there.` };
  const src = pageUrl(data.base, s.path);
  return {
    src, download: src + '?download=1', name: fileName(s.path), kind: s.kind || 'file',
    size: s.size, mtime: s.mtime, folder, path: s.path,
  };
}

/**
 * chatFile is a file the chat's agent named, wherever on the machine it
 * lies (`ref` as the agent wrote it: absolute, file://, or relative to the
 * chat's folder), as folderFile has it, from the chat's file route; or
 * {error}.
 */
export async function chatFile(chatId, ref) {
  const q = '?path=' + encodeURIComponent(ref);
  const base = '/api/chats/' + encodeURIComponent(chatId) + '/file';
  let info;
  try {
    info = await api(base + '/info' + q, { attempts: 1 });
  } catch (err) {
    return { error: err && err.status === 404 ? `${ref.split('/').pop()} is not there, or the chat never named it.` : 'The file could not be read.' };
  }
  return {
    src: base + q, download: base + q + '&download=1', name: info.name, kind: info.kind || 'file',
    size: info.size, mtime: info.mtime, chatId, ref, abs: info.path,
  };
}

/**
 * mountChatDocside wires the column (`panel`, #docsidePanel) and the
 * dialog. `onColumnOpen()` is called when the column opens (the tests'
 * column shuts then); `onPage(folder, path)` each time the frame shows a
 * file (opened, followed to by a link, drawn again after a change);
 * `follow(href)` with a link in the file that leads out of its folder, as an
 * absolute path, answering whether it took it (else a snackbar says it
 * leads out); `allFiles(folder)` whether a folder serves every file (a
 * chat's pages folder), whose other files open in the file viewer. It answers
 *   open(folder, path, {at, opener})   a file of a chat's folder (or of
 *                                      another working tree of its repository)
 *   openOutside(folder, href, {opener}) a path link out of the folder, when
 *                                      another working tree has the file
 *   openFile(source, {opener})         any other file, in a player or viewer
 *                                      for its kind (fileview.js): source is
 *                                      {folder, path} of a pages folder or
 *                                      {chatId, ref} of a file the agent named
 *   hide()                             the chat left the stage
 *   shut()                             another column opened
 *   isOpen()                           whether a file is shown
 *   shown()                            {folder, path} of the file shown, or null
 */
export function mountChatDocside({ panel, onColumnOpen, onPage, follow: onFollow = null, allFiles = () => false }) {
  // What is shown: {folder, path, base, mtime, y, title}, or null.
  let doc = null;
  // Or a file that is not Markdown (fileview.js): {src, download, name,
  // kind, size, mtime, ...source}, or null.
  let file = null;
  let dlg = null;
  let timer = null;
  let gen = 0;

  const title = el('h2', { class: 'so-docside__title', id: 'docsideTitle', tabindex: '-1', text: 'Docs' });
  const actions = el('div', { class: 'so-docside__actions' });
  const closeBtn = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-sm', type: 'button',
    'aria-label': 'Close the file', 'data-tip': 'Close the file',
  }, iconEl('close', 18));
  const frame = el('iframe', { class: 'so-docs__view', title: 'Document', sandbox: SANDBOX, referrerpolicy: 'no-referrer', name: frameName() });
  const view = el('div', { class: 'so-docside__file', hidden: true });
  const viewer = mountFileView(view);
  // What the column or the dialog shows: the frame or the file's view.
  const stage = el('div', { class: 'so-docside__stage' }, frame, view);
  const box = el('div', { class: 'so-docside__frame' }, stage);
  const glyph = el('span', { class: 'so-docside__glyph', 'aria-hidden': 'true' }, iconEl('fileText', 18));
  panel.replaceChildren(el('div', { class: 'so-docside__head' }, glyph, title, actions, closeBtn), box);
  let glyphName = 'fileText';

  function fileActionsHtml() {
    return html`<a class="ds-btn ds-btn--quiet ds-btn--icon ds-sm" href="${file.download}" download="${file.name}" aria-label="${'Download ' + file.name}" data-tip="Download">${raw(icon('download', 18))}</a><a class="ds-btn ds-btn--quiet ds-btn--icon ds-sm" href="${withVersion(file.src, file.mtime)}" target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab" data-tip="Open in a new tab">${raw(icon('external', 18))}</a>`;
  }

  function actionsHtml() {
    if (file) return fileActionsHtml();
    if (!doc) return '';
    return html`<a class="ds-btn ds-btn--quiet ds-btn--icon ds-sm" href="${docsHash(doc.folder, doc.path)}" data-docside="docs" aria-label="Open in Docs, with every file of the folder" data-tip="Open in Docs">${raw(icon('list', 18))}</a><a class="ds-btn ds-btn--quiet ds-btn--icon ds-sm" href="${pageUrl(doc.base, doc.path)}" target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab" data-tip="Open in a new tab">${raw(icon('external', 18))}</a>`;
  }

  function drawHead(heading) {
    const words = file ? file.name : doc ? (doc.title || fileName(doc.path)) : 'Docs';
    if (heading.textContent !== words) heading.textContent = words;
    if (file) heading.setAttribute('data-tip', kindWord(file.kind) + ' · ' + sizeText(file.size) + ' · ' + (file.abs || file.path || file.name));
    else if (doc) heading.setAttribute('data-tip', doc.path);
    const want = file ? kindGlyph(file.kind, file.name) : 'fileText';
    if (want !== glyphName) {
      glyphName = want;
      glyph.replaceChildren(iconEl(want, 18));
    }
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
    box.append(stage);
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
    const back = el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-ds-close': true, 'aria-label': 'Back', 'data-tip': 'Back' }, iconEl('arrowLeft', 20));
    const body = el('div', { class: 'ds-dialog__body so-docside__dialog-body' }, stage);
    const dialog = el('dialog', { class: 'ds-dialog ds-dialog--full so-docside-dialog', 'aria-labelledby': 'docsideDialogTitle' },
      el('div', { class: 'ds-dialog__backdrop', 'data-ds-close': true }),
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
    if (file) {
      lookFile();
      return;
    }
    if (!doc || document.hidden) return;
    const shown = doc;
    try {
      const s = await stat(shown.folder, shown.path);
      if (doc !== shown || !s) return;
      if (shown.mtime && s.mtime !== shown.mtime) frame.src = pageUrl(shown.base, shown.path, { y: shown.y });
      shown.mtime = s.mtime;
    } catch { /* offline: the next look tries again */ }
  }

  // A file that is not Markdown, looked at again: drawn again when it
  // changed on disk (a player where it was).
  async function lookFile() {
    if (!file || document.hidden) return;
    const shown = file;
    const now = shown.chatId ? await chatFile(shown.chatId, shown.ref) : await folderFile(shown.folder, shown.path);
    if (file !== shown || now.error) return;
    if (now.mtime !== shown.mtime || now.size !== shown.size || now.kind !== shown.kind) {
      file = now;
      viewer.reload(now);
      draw();
    }
  }

  function stop() {
    clearInterval(timer);
    timer = null;
    doc = null;
    gen += 1;
    frame.removeAttribute('src');
    if (file) {
      file = null;
      viewer.clear();
    }
    frame.hidden = false;
    view.hidden = true;
  }

  // showFrame and showView put the frame (a Markdown file) or the file's
  // view on the stage.
  function showFrame() {
    if (file) {
      file = null;
      viewer.clear();
    }
    view.hidden = true;
    frame.hidden = false;
  }

  function showView() {
    frame.removeAttribute('src');
    frame.hidden = true;
    view.hidden = false;
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
      if (onPage) onPage(doc.folder, doc.path);
    } else if (m.t === 'scroll') {
      doc.y = Number(m.y) || 0;
    } else if (m.t === 'file') {
      // A file of the folder that is not Markdown: shown here when the
      // folder serves every file (a chat's pages folder), else where the
      // same link in the chat would go (a file the agent named).
      const folder = doc.folder;
      const path = String(m.path || '');
      if (allFiles(folder)) openFile({ folder, path });
      else if (!(folder.startsWith('/') && onFollow && onFollow(folder.replace(/\/+$/, '') + '/' + path))) {
        toast('“' + fileName(path) + '” is not a Markdown file: only Markdown is shown here.');
      }
    } else if (m.t === 'outside') {
      // Out of the folder shown: a page's link to a file of the chat's
      // working directory, by its absolute path, or a path with ".." out of
      // a working tree. It goes where the same link in the chat would.
      const href = outsideHref(String(m.href || ''), doc.folder, doc.path);
      if (!href || !onFollow || !onFollow(href)) toast('That link leads out of this folder.');
    }
  });

  /* --------------------------------------------------------------- open */

  async function open(folder, path, { at = '', opener = null, elsewhere = true } = {}) {
    const my = ++gen;
    const found = await findDoc(folder, path, { elsewhere });
    if (my !== gen) return;
    if (found.error) {
      toast(found.error, 'error');
      return;
    }
    ({ folder, path } = found);
    showFrame();
    // sent: the file the frame was sent to, until it says it shows it (a
    // frame the browser gives its old address back is sent there again).
    doc = { folder, path, base: found.base, mtime: 0, y: 0, title: '', sent: path };
    frame.src = pageUrl(found.base, path, { hash: at });
    present(opener);
  }

  // present shows what is on the stage: the column at a desk, the dialog
  // narrower; and watches it for changes on disk.
  function present(opener) {
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

  // Any file that is not Markdown (fileview.js): of a folder the Docs
  // routes serve every file of ({folder, path}), or one the chat's agent
  // named ({chatId, ref}). A Markdown file goes to the frame. `fallback` is
  // tried when the folder does not serve it, before the snackbar.
  async function openFile(source, { opener = null, fallback = null } = {}) {
    const my = ++gen;
    const found = source.chatId ? await chatFile(source.chatId, source.ref) : await folderFile(source.folder, source.path);
    if (my !== gen) return;
    if (found.error) {
      if (!(fallback && fallback())) toast(found.error, 'error');
      return;
    }
    if (found.kind === 'markdown' && found.folder) {
      open(found.folder, found.path, { opener, elsewhere: false });
      return;
    }
    doc = null;
    file = found;
    showView();
    viewer.show(found);
    present(opener);
    if (onPage && found.folder) onPage(found.folder, found.path);
  }

  // A link out of the folder, by an absolute path or one with "..": the
  // file when it lies in another working tree of the folder's repository.
  async function openOutside(folder, href, { opener = null } = {}) {
    const my = ++gen;
    const found = await findOutside(folder, href);
    if (my !== gen) return;
    if (found.error) {
      toast(found.error, 'error');
      return;
    }
    open(found.folder, found.path, { at: found.at, opener, elsewhere: false });
  }

  function close() {
    shutColumn();
    if (dlg) hideOverlay(dlg.dialog);
    stop();
  }

  // A window that crosses the column's width takes the file along.
  WIDE.addEventListener('change', () => {
    if (!doc && !file) return;
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
    openOutside,
    openFile,
    hide: close,
    shut() {
      if (!panel.hidden) close();
    },
    isOpen: () => !!doc || !!file,
    shown: () => (doc ? { folder: doc.folder, path: doc.path }
      : file ? (file.folder ? { folder: file.folder, path: file.path } : { chatId: file.chatId, ref: file.ref, abs: file.abs }) : null),
  };
}
