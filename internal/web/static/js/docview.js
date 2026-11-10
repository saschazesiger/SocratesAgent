// What the Docs page (docs.js) and a chat's doc column (docside.js) share
// (docs/design/DOCS.md): a project folder's Markdown files as the server
// lists them, whether a folder has any, the address of a file in its
// sandboxed frame, the glyph a file's name gets, and the messages of the
// page kit inside that frame.
//
// A file is shown by the artifact kit in a frame of its own opaque origin,
// served at /docs/<token>/<path>: the folder's files are someone's
// repository, and nothing in them may act as the person on Socrates.

import { api } from './api.js';

/** What a docs frame may do: never be of Socrates' origin. */
export const SANDBOX = 'allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads';

// A listing read this recently is used again rather than asked for.
const LISTING_FRESH = 10 * 1000;
// Whether a folder has Markdown is asked again after this long.
const PROBE_FRESH = 60 * 1000;
// The folders asked about in one burst go in one request.
const PROBE_GATHER = 120;
const PROBE_MAX = 300;

const MARKDOWN = /\.(md|markdown)$/i;

/** isMarkdown is whether a path names a Markdown file. */
export const isMarkdown = (path) => MARKDOWN.test(String(path || ''));

/* ------------------------------------------------------------- listings */

const listings = new Map();

/**
 * listing is a folder's Markdown files: {folder, files: [{path, size,
 * mtime}], truncated, base, readme}. `fresh` asks the server even when a
 * recent answer is kept.
 */
export function listing(folder, { fresh = false } = {}) {
  const kept = listings.get(folder);
  if (kept && kept.pending) return kept.pending;
  if (kept && kept.data && !fresh && Date.now() - kept.at < LISTING_FRESH) return Promise.resolve(kept.data);
  const pending = api('/api/docs?folder=' + encodeURIComponent(folder), { attempts: 2 })
    .then((data) => {
      listings.set(folder, { at: Date.now(), data });
      noteProbe(folder, !!(data && data.files && data.files.length));
      return data;
    })
    .catch((err) => {
      if (kept && kept.data) listings.set(folder, kept);
      else listings.delete(folder);
      throw err;
    });
  listings.set(folder, { ...(kept || {}), pending });
  return pending;
}

/** keptListing is the listing last read for a folder, or null. */
export function keptListing(folder) {
  const kept = listings.get(folder);
  return kept && kept.data ? kept.data : null;
}

/* --------------------------------------------------------------- probes */

const probes = new Map();
const probeWanted = new Set();
const probeWaiters = [];
let probeTimer = null;
let probeListeners = [];

function noteProbe(folder, has) {
  const was = probes.get(folder);
  probes.set(folder, { at: Date.now(), has });
  if (!was || was.has !== has) for (const fn of probeListeners) fn(folder, has);
}

/** onProbe calls `fn(folder, has)` when what is known about a folder changes. */
export function onProbe(fn) {
  probeListeners.push(fn);
  return () => { probeListeners = probeListeners.filter((x) => x !== fn); };
}

/** knownDocs is whether a folder has Markdown: true, false, or undefined (not asked yet). */
export function knownDocs(folder) {
  const kept = probes.get(folder);
  return kept ? kept.has : undefined;
}

/**
 * probeDocs asks the server, in one request per burst, which of these
 * folders have Markdown files; what was asked recently is not asked again.
 * It resolves once the answers are in.
 */
export function probeDocs(folders) {
  const now = Date.now();
  for (const folder of folders) {
    const kept = probes.get(folder);
    if (folder && (!kept || now - kept.at > PROBE_FRESH)) probeWanted.add(folder);
  }
  if (!probeWanted.size) return Promise.resolve();
  return new Promise((resolve) => {
    probeWaiters.push(resolve);
    if (!probeTimer) probeTimer = setTimeout(runProbe, PROBE_GATHER);
  });
}

async function runProbe() {
  probeTimer = null;
  const folders = [...probeWanted].slice(0, PROBE_MAX);
  for (const folder of folders) probeWanted.delete(folder);
  const waiters = probeWaiters.splice(0);
  try {
    const data = await api('/api/docs/probe', { method: 'POST', body: { folders }, attempts: 2 });
    const docs = (data && data.docs) || {};
    for (const folder of folders) noteProbe(folder, !!docs[folder]);
  } catch {
    // Unknown stays unknown: the next drawing of the list asks again.
  }
  if (probeWanted.size && !probeTimer) probeTimer = setTimeout(runProbe, PROBE_GATHER);
  for (const resolve of waiters) resolve();
}

/** hasDocs is whether a folder has Markdown, asked of the server when not known. */
export async function hasDocs(folder) {
  if (knownDocs(folder) === undefined) await probeDocs([folder]);
  return !!knownDocs(folder);
}

/* ------------------------------------------------------------ addresses */

/**
 * pageUrl is a file's address in its frame: the listing's base, the path
 * with every part escaped, `y` a scroll position the kit goes back to, and
 * `hash` a heading.
 */
export function pageUrl(base, path, { y = 0, hash = '' } = {}) {
  const parts = String(path || '').split('/').map(encodeURIComponent).join('/');
  return base + parts + (y > 0 ? '?y=' + Math.round(y) : '') + (hash ? '#' + encodeURIComponent(hash) : '');
}

/** stat is one file's size and time of change ({path, size, mtime}), or null when it is gone. */
export async function stat(folder, path) {
  try {
    return await api('/api/docs/stat?folder=' + encodeURIComponent(folder) + '&path=' + encodeURIComponent(path), { attempts: 1 });
  } catch (err) {
    if (err && err.status === 404) return null;
    throw err;
  }
}

/* ---------------------------------------------------------------- glyphs */

// The usual names of a project's Markdown files, each with the glyph that
// tells it at a glance: a name without its extension, upper case, dashes
// and spaces as underscores.
const GLYPHS = [
  [/^README$/, 'info'],
  [/^(AGENTS|AGENT|CLAUDE|GEMINI|COPILOT_INSTRUCTIONS|SKILL|CONVENTIONS|CURSORRULES)(\..*)?$/, 'sparkles'],
  [/^(CHANGELOG|CHANGES|HISTORY|NEWS|RELEASES?|RELEASE_NOTES)$/, 'history'],
  [/^CONTRIBUTING$/, 'userPlus'],
  [/^(CODE_OF_CONDUCT|CODEOWNERS|AUTHORS|MAINTAINERS|GOVERNANCE)$/, 'users'],
  [/^(LICENSE|LICENCE|COPYING|NOTICE|THIRD_PARTY_LICENSES|THIRD_PARTY_NOTICES)$/, 'key'],
  [/^SECURITY$/, 'shield'],
  [/^(TODO|ROADMAP|PLAN|BACKLOG)$/, 'flag'],
  [/^(FAQ|HELP|SUPPORT|TROUBLESHOOTING)$/, 'helpCircle'],
  [/^(INSTALL|INSTALLATION|SETUP|GETTING_STARTED|QUICKSTART|USAGE)$/, 'download'],
  [/^(DEPLOY|DEPLOYMENT|OPERATIONS|RUNBOOK|HOSTING)$/, 'server'],
  [/^(ARCHITECTURE|DESIGN|DECISIONS|OVERVIEW|ADR(_.*)?)$/, 'box'],
  [/^(API|REFERENCE|SPEC|SPECIFICATION)$/, 'fileCode'],
  [/^(TESTING|TESTS?)$/, 'checkCircle'],
  [/^(STYLE|STYLEGUIDE|STYLE_GUIDE|CODING_STYLE)$/, 'pencil'],
];

/** glyphFor is the DS glyph of a Markdown file, by its name. */
export function glyphFor(path) {
  const name = String(path || '').split('/').pop().replace(/\.(md|markdown)$/i, '').replace(/^\./, '');
  const key = name.toUpperCase().replace(/[-\s]+/g, '_');
  for (const [pattern, glyph] of GLYPHS) if (pattern.test(key)) return glyph;
  return 'fileText';
}

/* --------------------------------------------------------------- frames */

/**
 * listenFrame calls `fn(message)` with each message the kit in `frame`
 * sends ({t: 'page' | 'scroll' | 'hash' | 'file' | 'outside', ...}). It
 * answers a function that stops listening.
 */
export function listenFrame(frame, fn) {
  const on = (event) => {
    const data = event.data;
    // The kit's frame is a sandbox, of the opaque origin "null"; a page the
    // frame was taken to elsewhere is not heard.
    if (event.source !== frame.contentWindow || event.origin !== 'null') return;
    if (!data || data.socratesDocs !== 1 || typeof data.t !== 'string') return;
    fn(data);
  };
  window.addEventListener('message', on);
  return () => window.removeEventListener('message', on);
}

/**
 * frameName is a name of its own for a docs frame. A reload of the app
 * would otherwise give a frame back the file it showed last time (the
 * browser restores a frame's address by its name), over the one the
 * address asks for.
 */
export const frameName = () => 'docs-' + Math.random().toString(36).slice(2);

/** scrollFrame asks the kit in `frame` to bring a heading into view. */
export function scrollFrame(frame, id) {
  if (frame.contentWindow) frame.contentWindow.postMessage({ socratesDocs: 1, t: 'scroll', id }, '*');
}

/** fileName is the last part of a path. */
export const fileName = (path) => String(path || '').split('/').pop();
