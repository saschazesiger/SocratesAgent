// A project folder's documentation on the stage (docs/design/DOCS.md): every
// Markdown file of the folder in its own tree of folders, the file chosen
// drawn by the page kit in a sandboxed frame (Mermaid diagrams, tables,
// GitHub's alerts), and the headings of the page beside it. The address is
// `#docs=<folder>&page=<path>`; session.js puts the page on the stage and
// takes it off again, as it does a folder's notes.
//
// A file that changes on disk (an agent at work) is drawn again where the
// person was reading it; the tree is read again now and then and whenever
// the tab comes back.

import { el, toast, errorMessage } from './api.js';
import { html, raw, icon, iconEl, morph, showOverlay, hideOverlay } from './ui.js';
import {
  SANDBOX, frameName, listing, pageUrl, stat, glyphFor, listenFrame, scrollFrame, fileName, isMarkdown,
} from './docview.js';

// How often the file on screen is checked for a change on disk, and the
// tree read again, while the page is on screen.
const WATCH = 4 * 1000;
const RELIST = 30 * 1000;
// The tree is a column at least this wide; narrower it is a side sheet.
const WIDE = window.matchMedia('(min-width: 900px)');

/** docsHash is the address of a folder's docs, on a page or its README. */
export function docsHash(folder, page = '', heading = '') {
  return '#docs=' + encodeURIComponent(folder) + (page ? '&page=' + encodeURIComponent(page) : '')
    + (heading ? '&at=' + encodeURIComponent(heading) : '');
}

/** parseDocsRoute reads what follows `#docs=`: {folder, page, at}. */
export function parseDocsRoute(rest) {
  const parts = String(rest || '').split('&');
  const out = { folder: '', page: '', at: '' };
  try {
    out.folder = decodeURIComponent(parts[0] || '');
    for (const part of parts.slice(1)) {
      const at = part.indexOf('=');
      if (at < 0) continue;
      const key = part.slice(0, at);
      const value = decodeURIComponent(part.slice(at + 1));
      if (key === 'page') out.page = value;
      else if (key === 'at') out.at = value;
    }
  } catch { /* a broken address shows the folder's README */ }
  return out;
}

/* ------------------------------------------------------------------ tree */

// treeOf turns the listing's paths into folders: {dirs: Map, files: []}.
function treeOf(files) {
  const root = { name: '', path: '', dirs: new Map(), files: [] };
  for (const file of files) {
    const parts = file.path.split('/');
    let node = root;
    for (let i = 0; i < parts.length - 1; i += 1) {
      const name = parts[i];
      if (!node.dirs.has(name)) node.dirs.set(name, { name, path: parts.slice(0, i + 1).join('/'), dirs: new Map(), files: [] });
      node = node.dirs.get(name);
    }
    node.files.push(file);
  }
  return root;
}

const byName = (a, b) => a.localeCompare(b, undefined, { sensitivity: 'base', numeric: true });
const isReadme = (path) => /^readme\.(md|markdown)$/i.test(fileName(path));

// ancestors are the folders a path is in: "a/b/c.md" → ["a", "a/b"].
function ancestors(path) {
  const parts = String(path || '').split('/');
  const out = [];
  for (let i = 1; i < parts.length; i += 1) out.push(parts.slice(0, i).join('/'));
  return out;
}

/**
 * mountDocs draws the Docs page into `host` (#docs). It answers
 *   show(folder, label, page, at)   a folder's docs, on a file or its README
 *   hide()                          off the stage
 *   shown()                         whether it is on the stage
 *   folder()                        whose docs they are
 *   label(label)                    the sidebar's name for the folder changed
 *   resync()                        read the tree again (the stream came back)
 * `onPage(folder, page)` is told the file the frame shows, for the address.
 */
export function mountDocs({ host, onPage }) {
  const st = {
    shown: false,
    folder: '',
    label: '',
    data: null,
    error: '',
    page: '',
    title: '',
    headings: [],
    expanded: new Set(),
    filter: '',
    y: 0,
    mtime: 0,
    // The file open() sent the frame to, until the frame says it shows it:
    // a reload of the app may first give the frame back the file it showed
    // before (the browser restores a frame's address), which is not taken.
    sent: '',
    watch: null,
    relisted: 0,
    gen: 0,
  };

  /* ----------------------------------------------------------- skeleton */

  const head = el('div', { class: 'so-docs__top' });
  const search = (id) => {
    const input = el('input', {
      class: 'ds-input', type: 'search', id, placeholder: 'Filter files', 'aria-label': 'Filter files',
      autocomplete: 'off', spellcheck: 'false', enterkeyhint: 'search',
    });
    const box = el('div', { class: 'ds-search so-docs__search' }, iconEl('search', 20), input);
    input.addEventListener('input', () => {
      st.filter = input.value;
      for (const other of host.ownerDocument.querySelectorAll('.so-docs__search .ds-input')) {
        if (other !== input) other.value = input.value;
      }
      drawTrees();
    });
    return box;
  };
  const navTree = el('div', { class: 'so-docs__tree' });
  const nav = el('nav', { class: 'so-docs__nav', 'aria-label': 'Files' }, search('docsFilter'), navTree);
  const frame = el('iframe', { class: 'so-docs__view', title: 'Document', sandbox: SANDBOX, referrerpolicy: 'no-referrer', name: frameName(), hidden: true });
  const state = el('div', { class: 'so-docs__state' });
  const main = el('div', { class: 'so-docs__main' }, frame, state);
  const tocList = el('div', { class: 'so-docs__toc-list' });
  const toc = el('aside', { class: 'so-docs__toc', 'aria-labelledby': 'docsTocTitle', hidden: true },
    el('h2', { class: 'so-docs__toc-title', id: 'docsTocTitle', text: 'On this page' }), tocList);
  const body = el('div', { class: 'so-docs__body' }, nav, main, toc);
  host.replaceChildren(el('div', { class: 'so-docs__frame' }, head, body));

  // The tree narrower: a side sheet from the start edge.
  const sheetTree = el('div', { class: 'so-docs__tree' });
  const sheet = el('dialog', { class: 'ds-sheet so-docs-sheet', id: 'docsSheet', 'aria-labelledby': 'docsSheetTitle' },
    el('div', { class: 'ds-sheet__backdrop', 'data-ds-close': true }),
    el('section', { class: 'ds-sheet__panel' },
      el('div', { class: 'ds-sheet__head' },
        el('h2', { class: 'ds-sheet__title', id: 'docsSheetTitle', text: 'Files' }),
        el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-ds-close': true, 'aria-label': 'Close', 'data-tip': 'Close' }, iconEl('close', 20))),
      el('div', { class: 'ds-sheet__body so-docs-sheet__body', tabindex: '0' }, search('docsSheetFilter'), sheetTree)));
  document.body.append(sheet);
  const sheetOpen = () => !sheet.hidden && sheet.open && sheet.getAttribute('data-state') !== 'closed';

  /* ------------------------------------------------------------ drawing */

  function crumbsHtml() {
    if (!st.page) return '';
    const parts = st.page.split('/');
    const files = st.data ? st.data.files : [];
    const readmeOf = (dir) => {
      const hit = files.find((f) => isReadme(f.path) && (dir ? f.path.startsWith(dir + '/') && !f.path.slice(dir.length + 1).includes('/') : !f.path.includes('/')));
      return hit ? hit.path : '';
    };
    const items = [];
    const rootReadme = readmeOf('');
    items.push(rootReadme && rootReadme !== st.page
      ? html`<li><a href="${docsHash(st.folder, rootReadme)}">${st.label}</a></li>`
      : html`<li><span>${st.label}</span></li>`);
    for (let i = 0; i < parts.length - 1; i += 1) {
      const dir = parts.slice(0, i + 1).join('/');
      const readme = readmeOf(dir);
      items.push(readme && readme !== st.page
        ? html`<li><a href="${docsHash(st.folder, readme)}">${parts[i]}</a></li>`
        : html`<li><span>${parts[i]}</span></li>`);
    }
    items.push(html`<li><a href="${docsHash(st.folder, st.page)}" aria-current="page">${parts[parts.length - 1]}</a></li>`);
    return html`<nav class="ds-breadcrumbs so-docs__crumbs" aria-label="Where this file is"><ol>${items}</ol></nav>`;
  }

  function drawHead() {
    const url = st.data && st.page ? pageUrl(st.data.base, st.page) : '';
    morph(head, html`<header class="ds-page-head">
      ${crumbsHtml()}
      <div class="ds-page-head__row">
        <div class="ds-page-head__text">
          <h1 class="ds-page-head__title so-docs__title" tabindex="-1">Docs</h1>
          <p class="ds-page-head__lead"><span class="so-docs__folder" data-tip="${st.folder}">${st.label}</span>${
            st.data && st.data.truncated ? html`<span class="ds-meta so-docs__more"> · the first ${st.data.files.length} files</span>` : ''
          }</p>
        </div>
        <div class="ds-page-head__actions">
          <button class="ds-btn so-docs__files-btn" type="button" data-docs="files" aria-haspopup="dialog" aria-controls="docsSheet" aria-expanded="false">${raw(icon('list', 16))}Files</button>
          <button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" data-docs="reload" aria-label="Read the files again" data-tip="Read the files again">${raw(icon('sync', 20))}</button>
          ${url ? html`<a class="ds-btn ds-btn--quiet ds-btn--icon" href="${url}" target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab" data-tip="Open in a new tab">${raw(icon('external', 20))}</a>` : ''}
        </div>
      </div>
    </header>`);
  }

  function fileRowHtml(file, { full = false } = {}) {
    const current = file.path === st.page;
    const where = full && file.path.includes('/') ? file.path.slice(0, file.path.lastIndexOf('/')) : '';
    return html`<li class="so-docs__item" data-ds-key="${'f:' + file.path}"><button class="so-docs__row ds-pressable ds-pressable--inset" type="button" data-doc="${file.path}"${current ? html` aria-current="page"` : ''} data-tip="${file.path}">${raw(icon(glyphFor(file.path), 18, 'so-docs__glyph'))}<span class="so-docs__name">${fileName(file.path)}</span>${
      where ? html`<span class="so-docs__where">${where}</span>` : ''
    }</button></li>`;
  }

  function dirHtml(node, key) {
    const open = st.expanded.has(node.path);
    const id = 'docsDir-' + key + '-' + node.path.replace(/[^A-Za-z0-9_-]/g, '_');
    return html`<li class="so-docs__item" data-ds-key="${'d:' + node.path}"><button class="so-docs__row so-docs__row--dir ds-pressable ds-pressable--inset" type="button" data-dir="${node.path}" aria-expanded="${open ? 'true' : 'false'}" aria-controls="${id}">${raw(icon('chevronDown', 16, 'ds-chevron so-docs__chevron'))}${raw(icon('folder', 18, 'so-docs__glyph'))}<span class="so-docs__name">${node.name}</span></button><ul class="so-docs__list" id="${id}"${open ? '' : html` hidden`}>${levelHtml(node, key)}</ul></li>`;
  }

  // A folder shows its README first, its other files, then its folders.
  function levelHtml(node, key) {
    const files = [...node.files].sort((a, b) => (isReadme(b.path) - isReadme(a.path)) || byName(fileName(a.path), fileName(b.path)));
    const dirs = [...node.dirs.values()].sort((a, b) => byName(a.name, b.name));
    return html`${files.map((f) => fileRowHtml(f))}${dirs.map((d) => dirHtml(d, key))}`;
  }

  function treeHtml(key) {
    if (!st.data) return '';
    const files = st.data.files || [];
    const words = st.filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
    if (words.length) {
      const hits = files.filter((f) => words.every((w) => f.path.toLowerCase().includes(w)));
      if (!hits.length) return html`<p class="ds-muted so-docs__none">No file’s name has “${st.filter.trim()}”.</p>`;
      return html`<ul class="so-docs__list so-docs__list--top" aria-label="Files found">${hits.map((f) => fileRowHtml(f, { full: true }))}</ul>`;
    }
    return html`<ul class="so-docs__list so-docs__list--top">${levelHtml(treeOf(files), key)}</ul>`;
  }

  function drawTrees() {
    morph(navTree, html`${treeHtml('n')}`);
    morph(sheetTree, html`${treeHtml('s')}`);
  }

  function drawToc() {
    const heads = st.headings.filter((h) => h.level >= 2);
    toc.hidden = heads.length < 2;
    morph(tocList, html`<ul class="so-docs__toc-items">${heads.map((h) => html`<li data-ds-key="${h.id}" data-level="${h.level}"><a class="so-docs__toc-link" href="${docsHash(st.folder, st.page, h.id)}" data-heading="${h.id}">${h.text}</a></li>`)}</ul>`);
  }

  function drawState() {
    let markup = '';
    if (!st.data) {
      markup = st.error
        ? html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('alertCircle', 24))}</span><p class="ds-empty__title">The files could not be read</p><p class="ds-empty__text">${st.error}</p><button class="ds-btn ds-btn--tonal" type="button" data-docs="reload">Try again</button></div>`
        : html`<div class="so-docs__loading"><span class="ds-spinner" role="status" aria-label="Reading the files"></span></div>`;
    } else if (!st.data.files.length) {
      markup = html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('fileText', 24))}</span><p class="ds-empty__title">No Markdown here</p><p class="ds-empty__text">This folder has no .md files yet. A README.md at its top is where the docs start.</p></div>`;
    }
    morph(state, html`${markup}`);
    state.hidden = !markup;
    frame.hidden = !!markup;
  }

  function draw() {
    if (!st.shown) return;
    drawHead();
    drawTrees();
    drawToc();
    drawState();
    host.toggleAttribute('data-toc', !toc.hidden);
  }

  /* ------------------------------------------------------------ the page */

  function expandTo(path) {
    for (const dir of ancestors(path)) st.expanded.add(dir);
  }

  // open shows a file in the frame: `y` a scroll position, `at` a heading.
  function open(path, { y = 0, at = '' } = {}) {
    if (!st.data || !path) return;
    st.page = path;
    st.y = y;
    st.mtime = 0;
    st.headings = [];
    st.sent = path;
    expandTo(path);
    frame.src = pageUrl(st.data.base, path, { y, hash: at });
    draw();
    if (sheetOpen()) hideOverlay(sheet);
    remember();
  }

  // first is the file a folder opens on: the one asked for, its README, or
  // the first one listed.
  function first(asked) {
    const files = st.data.files;
    if (asked && files.some((f) => f.path === asked)) return asked;
    if (asked && isMarkdown(asked)) return asked; // listed later, or over the limit: the server says
    return st.data.readme || (files[0] && files[0].path) || '';
  }

  async function load({ fresh = false, page = '', at = '' } = {}) {
    const my = ++st.gen;
    const folder = st.folder;
    try {
      const data = await listing(folder, { fresh });
      if (my !== st.gen || folder !== st.folder) return;
      const had = st.data;
      st.data = data;
      st.error = '';
      st.relisted = Date.now();
      if (!had || page) {
        const path = first(page || st.page);
        if (path) open(path, { at });
        else { st.page = ''; frame.removeAttribute('src'); }
      }
      draw();
    } catch (err) {
      if (my !== st.gen) return;
      if (!st.data) st.error = errorMessage(err);
      draw();
    }
  }

  function remember() {
    if (onPage && st.shown) onPage(st.folder, st.page);
  }

  /* ----------------------------------------------------- the frame's news */

  listenFrame(frame, (m) => {
    if (m.t === 'page' && typeof m.path === 'string') {
      if (st.sent && m.path !== st.sent) {
        if (st.data) frame.src = pageUrl(st.data.base, st.sent);
        return;
      }
      st.sent = '';
      st.page = m.path;
      st.title = String(m.title || '');
      st.headings = Array.isArray(m.headings) ? m.headings.filter((h) => h && typeof h.id === 'string') : [];
      expandTo(m.path);
      st.mtime = 0;
      draw();
      remember();
      baseline();
    } else if (m.t === 'scroll') {
      st.y = Number(m.y) || 0;
    } else if (m.t === 'file') {
      toast('“' + fileName(m.path) + '” is not a Markdown file: only Markdown is shown here.');
    } else if (m.t === 'outside') {
      toast('That link leads out of this folder.');
    }
  });

  // baseline notes when the file on screen last changed, so that a change
  // after it is seen.
  async function baseline() {
    const folder = st.folder;
    const path = st.page;
    try {
      const s = await stat(folder, path);
      if (folder === st.folder && path === st.page && s) st.mtime = s.mtime;
    } catch { /* the next look tries again */ }
  }

  async function look() {
    if (!st.shown || document.hidden || !st.data || !st.page) return;
    const folder = st.folder;
    const path = st.page;
    try {
      const s = await stat(folder, path);
      if (folder !== st.folder || path !== st.page) return;
      if (!s) {
        toast('“' + fileName(path) + '” is gone.');
        await load({ fresh: true });
        if (folder === st.folder && st.data && !st.data.files.some((f) => f.path === st.page)) open(first(''));
        return;
      }
      if (st.mtime && s.mtime !== st.mtime) {
        st.mtime = s.mtime;
        frame.src = pageUrl(st.data.base, path, { y: st.y });
      } else if (!st.mtime) {
        st.mtime = s.mtime;
      }
    } catch { /* offline: the next look tries again */ }
    if (Date.now() - st.relisted > RELIST) load({ fresh: true });
  }

  function watch(on) {
    clearInterval(st.watch);
    st.watch = on ? setInterval(look, WATCH) : null;
  }

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && st.shown) look();
  });

  /* ------------------------------------------------------------- presses */

  function onTreeClick(event) {
    const dir = event.target.closest('[data-dir]');
    if (dir) {
      const path = dir.getAttribute('data-dir');
      if (st.expanded.has(path)) st.expanded.delete(path);
      else st.expanded.add(path);
      drawTrees();
      return;
    }
    const doc = event.target.closest('[data-doc]');
    if (doc) open(doc.getAttribute('data-doc'));
  }
  navTree.addEventListener('click', onTreeClick);
  sheetTree.addEventListener('click', onTreeClick);

  host.addEventListener('click', (event) => {
    const b = event.target.closest('[data-docs]');
    if (b) {
      const what = b.getAttribute('data-docs');
      if (what === 'reload') {
        load({ fresh: true });
        if (st.data && st.page) frame.src = pageUrl(st.data.base, st.page, { y: st.y });
      } else if (what === 'files') {
        showOverlay(sheet, b);
      }
      return;
    }
    const link = event.target.closest('[data-heading]');
    if (link && event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey) {
      event.preventDefault();
      scrollFrame(frame, link.getAttribute('data-heading'));
    }
  });

  WIDE.addEventListener('change', () => { if (WIDE.matches && sheetOpen()) hideOverlay(sheet); });

  /* ------------------------------------------------------------ the API */

  return {
    show(folder, label, page = '', at = '') {
      const other = folder !== st.folder;
      st.label = label || folder;
      if (other) {
        st.gen += 1;
        st.folder = folder;
        st.data = null;
        st.error = '';
        // The file asked for, until the listing is in: a load that starts
        // meanwhile (the stream's resync) opens it too.
        st.page = page;
        st.headings = [];
        st.expanded = new Set();
        st.filter = '';
        for (const input of document.querySelectorAll('.so-docs__search .ds-input')) input.value = '';
        frame.removeAttribute('src');
      }
      const wasShown = st.shown;
      st.shown = true;
      host.hidden = false;
      watch(true);
      if (other || !st.data) {
        draw();
        load({ fresh: true, page: page || st.page, at });
      } else if (page && page !== st.page) {
        open(page, { at });
      } else if (at) {
        scrollFrame(frame, at);
      } else {
        draw();
      }
      const heading = host.querySelector('.so-docs__title');
      if (heading && (other || !wasShown)) heading.focus({ preventScroll: true });
    },
    hide() {
      st.shown = false;
      host.hidden = true;
      watch(false);
      if (sheetOpen()) hideOverlay(sheet);
    },
    shown: () => st.shown,
    folder: () => st.folder,
    page: () => st.page,
    label(label) {
      if (label && label !== st.label) { st.label = label; draw(); }
    },
    resync() {
      if (st.shown) load({ fresh: true });
    },
  };
}
