// A chat's pages (docs/design/PAGES.md): the files its agent writes into the
// chat's own pages folder (`pages_dir` of the chat) and links in the
// conversation - Markdown pages, and any other file it wants to show (a
// video, a recording, a PDF, a report) - and the files it linked or named
// elsewhere on the machine (GET /api/chats/<id>/files). The Pages button in
// the top bar is there while there is at least one; its badge counts the
// files of the folder new or changed since this device last opened them,
// and a press opens the menu of them all, newest first. One chosen opens
// beside the chat (docside.js, fileview.js), like a link to it.
//
// The folder is read through the docs API (docview.js listing, with the
// pages' titles) every few seconds while the chat is on the stage and the
// tab is seen, and when the tab is seen again. What this device has opened
// is remembered per chat: the time of change of each page as it was then
// (SEEN_KEY).

import { M } from './ui.js';
import { api } from './api.js';
import { listing, glyphFor, fileName } from './docview.js';
import { kindGlyph } from './fileview.js';
import { agoText } from './mcpview.js';

// How often the folder of the chat on the stage is read again.
const POLL = 5 * 1000;
// What this device has opened: {chat: {at, paths: {"plan.md": mtime}}}. Kept
// for the chats touched last, no more.
const SEEN_KEY = 'socrates.pages.seen';
const SEEN_CHATS = 60;

/* ------------------------------------------------------------ seen state */

function readSeen() {
  try {
    const all = JSON.parse(localStorage.getItem(SEEN_KEY) || '{}');
    return all && typeof all === 'object' && !Array.isArray(all) ? all : {};
  } catch {
    return {};
  }
}

function writeSeen(all) {
  const chats = Object.keys(all);
  if (chats.length > SEEN_CHATS) {
    chats.sort((a, b) => (all[b].at || 0) - (all[a].at || 0));
    for (const chat of chats.slice(SEEN_CHATS)) delete all[chat];
  }
  try { localStorage.setItem(SEEN_KEY, JSON.stringify(all)); } catch { /* a private window */ }
}

/** seenOf is what this device has opened of a chat's pages: {path: mtime}. */
function seenOf(chat) {
  const entry = readSeen()[chat];
  return (entry && entry.paths && typeof entry.paths === 'object') ? entry.paths : {};
}

/** markSeen remembers that a page was opened as of `mtime`; true when that is news. */
function markSeen(chat, path, mtime) {
  const all = readSeen();
  const entry = all[chat] && all[chat].paths ? all[chat] : { paths: {} };
  if ((Number(entry.paths[path]) || 0) >= mtime) return false;
  entry.paths[path] = mtime;
  entry.at = Date.now();
  all[chat] = entry;
  writeSeen(all);
  return true;
}

/** pruneSeen forgets the pages a chat's folder no longer has. */
function pruneSeen(chat, files) {
  const all = readSeen();
  const entry = all[chat];
  if (!entry || !entry.paths) return;
  const paths = new Set(files.map((f) => f.path));
  let changed = false;
  for (const path of Object.keys(entry.paths)) {
    if (!paths.has(path)) { delete entry.paths[path]; changed = true; }
  }
  if (!Object.keys(entry.paths).length) { delete all[chat]; changed = true; }
  if (changed) writeSeen(all);
}

/** freshness is 'new' (never opened here), 'changed' (opened before it last changed) or ''. */
function freshness(file, seen) {
  const opened = Number(seen[file.path]) || 0;
  if (!opened) return 'new';
  return opened < (Number(file.mtime) || 0) ? 'changed' : '';
}

/** pageTitle is a page's name in the menu: its first heading, else its file name without .md. */
export function pageTitle(file) {
  const title = String(file.title || '').trim();
  return title || fileName(file.path).replace(/\.(md|markdown)$/i, '');
}

/** isPage is whether a file of the folder is a Markdown page (the Docs viewer's) rather than any other file. */
const isPage = (file) => !file.kind || file.kind === 'markdown';

/** fileGlyph is the glyph of a file of the menu. */
const fileGlyph = (file) => (isPage(file) ? glyphFor(file.path) : kindGlyph(file.kind, file.path));

/** sameFiles is whether two listings name the same pages, titles and times. */
const sameFiles = (a, b) => a.length === b.length
  && a.every((f, i) => f.path === b[i].path && f.mtime === b[i].mtime && (f.title || '') === (b[i].title || '') && (f.kind || '') === (b[i].kind || ''));

/** sameLinked is whether two lists of linked files are the same. */
const sameLinked = (a, b) => a.length === b.length
  && a.every((f, i) => f.path === b[i].path && f.mtime === b[i].mtime && f.size === b[i].size);

/**
 * mountChatPages wires the Pages button (`button`, #pagesBtn).
 *
 * `ctx`: button; menu(anchor, title, entries) opens a menu of entries
 * ({action, label, glyph, meta, run}) hanging from the button (an action
 * sheet on a phone); open(folder, path, opener) shows a page beside the
 * chat; openFile(source, opener) any other file ({folder, path} of the
 * folder, {chatId, ref} of a file the agent named); shown() is the
 * {folder, path} beside the chat, or null.
 *
 * It answers { show(session), hide(), seen(folder, path), refresh() }: a
 * chat on the stage, nothing on it, a page of it shown beside the chat,
 * and a read of its folder now.
 */
export function mountChatPages(ctx) {
  const { button } = ctx;
  const badge = button.querySelector('.ds-badge');

  // The chat on the stage: {id, folder}, or null.
  let chat = null;
  // Its pages, newest first, as last read.
  let files = [];
  // The files the agent linked elsewhere: [{ref, path, name, size, mtime, kind}].
  let linked = [];
  let timer = null;
  let gen = 0;

  /* ----------------------------------------------------------- the button */

  function draw() {
    const show = !!chat && (files.length > 0 || linked.length > 0);
    button.hidden = !show;
    if (!show) {
      badge.hidden = true;
      badge.textContent = '';
      return;
    }
    const seen = seenOf(chat.id);
    const fresh = files.filter((f) => freshness(f, seen)).length;
    const label = 'Pages and files' + (fresh ? ': ' + fresh + ' new or changed' : '');
    if (button.getAttribute('aria-label') !== label) button.setAttribute('aria-label', label);
    if (button.getAttribute('data-tip') !== label) button.setAttribute('data-tip', label);
    const was = badge.hidden ? '' : badge.textContent;
    const value = fresh ? String(fresh) : '';
    if (badge.textContent !== value) badge.textContent = value;
    badge.hidden = !fresh;
    // A count that appears pops in; the number itself changes at once.
    if (fresh && !was) M.enter(badge, 'pop');
  }

  /* ------------------------------------------------------------- reading */

  async function load() {
    if (!chat) return;
    const my = ++gen;
    const { id, folder } = chat;
    let data;
    const linking = api('/api/chats/' + encodeURIComponent(id) + '/files', { attempts: 1 }).catch(() => null);
    try {
      data = await listing(folder, { fresh: true, titles: true });
    } catch {
      // No folder (yet), or offline: no pages.
      data = { files: [] };
    }
    const links = await linking;
    if (my !== gen || !chat || chat.id !== id) return;
    const nextLinked = links && Array.isArray(links.files) ? links.files : linked;
    const linkedChanged = !sameLinked(linked, nextLinked);
    linked = nextLinked;
    const next = ((data && data.files) || []).slice().sort((a, b) => (b.mtime || 0) - (a.mtime || 0) || a.path.localeCompare(b.path));
    const changed = !sameFiles(files, next);
    files = next;
    if (changed) pruneSeen(id, files);
    // The page beside the chat is seen as it is now.
    const marked = markShown();
    if (changed || linkedChanged || marked || button.hidden !== !(files.length || linked.length)) draw();
  }

  // markShown marks the page beside the chat seen, when it is one of these.
  function markShown() {
    const shown = ctx.shown ? ctx.shown() : null;
    if (!chat || !shown || shown.folder !== chat.folder) return false;
    const file = files.find((f) => f.path === shown.path);
    return !!file && markSeen(chat.id, file.path, Number(file.mtime) || 0);
  }

  function start() {
    stop();
    if (!chat || document.hidden) return;
    load();
    timer = setInterval(() => { if (!document.hidden) load(); }, POLL);
  }

  function stop() {
    clearInterval(timer);
    timer = null;
  }

  document.addEventListener('visibilitychange', () => {
    if (!chat) return;
    if (document.hidden) stop();
    else start();
  });

  /* ------------------------------------------------------------- the menu */

  button.addEventListener('click', (event) => {
    // The second click of a double click opens nothing.
    if (event.detail > 1 || !chat || !(files.length || linked.length)) return;
    const { folder, id } = chat;
    const seen = seenOf(chat.id);
    const now = Date.now();
    const entries = files.map((f) => {
      const state = freshness(f, seen);
      const when = agoText(f.mtime, now);
      const meta = state === 'new' ? 'New · ' + when : state === 'changed' ? 'Changed · ' + when : when;
      return {
        mtime: Number(f.mtime) || 0,
        action: 'page:' + f.path,
        label: isPage(f) ? pageTitle(f) : fileName(f.path),
        glyph: fileGlyph(f),
        meta,
        run: () => (isPage(f) ? ctx.open(folder, f.path, button) : ctx.openFile({ folder, path: f.path }, button)),
      };
    });
    for (const f of linked) {
      entries.push({
        mtime: Number(f.mtime) || 0,
        action: 'linked:' + f.path,
        label: f.name || fileName(f.path),
        glyph: kindGlyph(f.kind, f.path),
        meta: 'Linked · ' + agoText(f.mtime, now),
        run: () => ctx.openFile({ chatId: id, ref: f.ref }, button),
      });
    }
    entries.sort((a, b) => b.mtime - a.mtime);
    ctx.menu(button, 'Pages and files', entries);
  });

  return {
    /** show is a chat put on the stage. */
    show(session) {
      const folder = (session && session.pages_dir) || '';
      if (chat && chat.id === session.id && chat.folder === folder) return;
      gen += 1;
      chat = folder ? { id: session.id, folder } : null;
      files = [];
      linked = [];
      draw();
      start();
    },
    /** hide is the chat taken off the stage. */
    hide() {
      stop();
      gen += 1;
      chat = null;
      files = [];
      linked = [];
      draw();
    },
    /** seen is a page shown beside the chat: of this chat's folder, it is seen as it is now. */
    seen(folder, path) {
      if (!chat || folder !== chat.folder) return;
      const file = files.find((f) => f.path === path);
      // A page not listed yet (just written) is marked at the next read.
      if (!file) { load(); return; }
      if (markSeen(chat.id, file.path, Number(file.mtime) || 0)) draw();
    },
    /** refresh reads the folder again now. */
    refresh() {
      if (chat && !document.hidden) load();
    },
  };
}
