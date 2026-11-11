// The GitHub repositories a chat can work in (REPOS.md): what /api/github
// says this Socrates reaches, the branches of one of them, and which ones
// this device used last. The new-chat sheet (harnesses.js settingsForm) is
// drawn from here; the sidebar groups the chats by the `repo` they carry.
//
// Like the catalogue, the last answer is kept on this device: a sheet opened
// without a connection still lists the repositories it knew. The server
// checks every choice again when the chat is made.

import { api } from './api.js';

const CACHE_KEY = 'socrates.github';
const RECENT_KEY = 'socrates.recentRepos';
const RECENT_MAX = 12;
// How long a branch list is taken as current: long enough for the sheet's
// repository to be switched back and forth without asking again.
const BRANCHES_FRESH = 60000;

/** NO_REPO is the sheet's "No repository": a fresh folder of its own. */
export const NO_REPO = 'none';

const EMPTY = { connected: false, app: null, installations: [], repos: [], problem: '' };

let cached = null;
let fromStorage = false;
let loading = null;
const branchCache = new Map(); // repo -> { at, data } or { at, promise }

function readCache() {
  try {
    const parsed = JSON.parse(localStorage.getItem(CACHE_KEY) || 'null');
    return parsed && Array.isArray(parsed.repos) ? parsed : null;
  } catch { return null; }
}

function normal(data) {
  return {
    connected: !!(data && data.connected),
    app: (data && data.app) || null,
    installations: (data && Array.isArray(data.installations)) ? data.installations : [],
    repos: (data && Array.isArray(data.repos)) ? data.repos.filter((one) => one && one.full_name) : [],
    problem: (data && data.problem) || '',
  };
}

/** snapshot is the last answer about GitHub, this device's copy before any. */
export function snapshot() {
  if (cached) return cached;
  const stored = readCache();
  cached = stored ? normal(stored) : EMPTY;
  fromStorage = !!stored;
  return cached;
}

/** stale says the repositories shown are this device's copy, not the server's answer. */
export function stale() { return fromStorage; }

/**
 * load asks the server which repositories this Socrates reaches. A failure
 * keeps the copy from before; with none, GitHub reads as not connected.
 */
export async function load() {
  if (loading) return loading;
  loading = (async () => {
    try {
      const data = normal(await api('/api/github', { attempts: 1, timeout: 15000 }));
      cached = data;
      fromStorage = false;
      try { localStorage.setItem(CACHE_KEY, JSON.stringify(data)); } catch { /* private window */ }
      return data;
    } catch {
      return snapshot();
    } finally {
      loading = null;
    }
  })();
  return loading;
}

/** repoOf is a repository's entry by its full name, or null. */
export function repoOf(fullName) {
  return snapshot().repos.find((one) => one.full_name === fullName) || null;
}

/** nameOf is a repository's name without its owner. */
export function nameOf(fullName) {
  const text = String(fullName || '');
  return text.slice(text.indexOf('/') + 1) || text;
}

/** ownerOf is a repository's owner. */
export function ownerOf(fullName) {
  const text = String(fullName || '');
  const at = text.indexOf('/');
  return at < 0 ? '' : text.slice(0, at);
}

function readRecent() {
  try {
    const parsed = JSON.parse(localStorage.getItem(RECENT_KEY) || '[]');
    return Array.isArray(parsed) ? parsed.filter((one) => typeof one === 'string') : [];
  } catch { return []; }
}

/**
 * rememberRepo puts a choice of the sheet - a repository or NO_REPO - at the
 * top of this device's recent ones: the sheet starts on it next time and
 * lists the repositories used last first.
 */
export function rememberRepo(choice) {
  if (!choice) return;
  const list = [choice, ...readRecent().filter((one) => one !== choice)].slice(0, RECENT_MAX);
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(list)); } catch { /* private window */ }
}

/** recent is this device's choices of the sheet, the last one first: full names and NO_REPO. */
export function recent() { return readRecent(); }

/**
 * ordered is every repository the sheet offers: those used last first, the
 * rest by name.
 */
export function ordered() {
  const recent = readRecent();
  const rank = (repo) => {
    const at = recent.indexOf(repo.full_name);
    return at < 0 ? Infinity : at;
  };
  return [...snapshot().repos].sort((a, b) => {
    const ra = rank(a);
    const rb = rank(b);
    if (ra !== rb) return ra - rb;
    return a.full_name.localeCompare(b.full_name);
  });
}

/**
 * lastChoice is what the sheet starts on: the choice made last on this
 * device, if it is still on offer; else the first repository; else
 * NO_REPO.
 */
export function lastChoice() {
  const offered = new Set(snapshot().repos.map((one) => one.full_name));
  const last = readRecent().find((one) => one === NO_REPO || offered.has(one));
  if (last) return last;
  const first = ordered()[0];
  return first ? first.full_name : NO_REPO;
}

/**
 * branches lists a repository's branches ({default, branches: [{name, sha,
 * date}]}, newest first). The server fetches its cache of the repository
 * first, so the first asking can take a while; an answer is kept a minute.
 */
export function branches(fullName, { force = false } = {}) {
  const kept = branchCache.get(fullName);
  if (!force && kept && (kept.promise || Date.now() - kept.at < BRANCHES_FRESH)) return kept.promise || Promise.resolve(kept.data);
  const [owner, name] = [ownerOf(fullName), nameOf(fullName)];
  const promise = api('/api/github/repos/' + encodeURIComponent(owner) + '/' + encodeURIComponent(name) + '/branches',
    { attempts: 1, timeout: 60000 })
    .then((data) => {
      const out = {
        default: (data && data.default) || '',
        branches: (data && Array.isArray(data.branches)) ? data.branches.filter((one) => one && one.name) : [],
      };
      branchCache.set(fullName, { at: Date.now(), data: out });
      return out;
    }, (err) => {
      branchCache.delete(fullName);
      throw err;
    });
  branchCache.set(fullName, { at: Date.now(), promise });
  return promise;
}

/**
 * slugOf is the words of a text as a branch name's part: lower case,
 * a-z0-9 and dashes, at most 40 characters, cut at a word.
 */
export function slugOf(text) {
  const words = String(text || '').toLowerCase().normalize('NFKD').replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, ' ').trim().split(' ').filter(Boolean);
  let out = '';
  for (const word of words) {
    const next = out ? out + '-' + word : word;
    if (next.length > 40) break;
    out = next;
  }
  return out || (words[0] || '').slice(0, 40);
}

/**
 * branchFor is the branch a new chat is offered: socrates/<slug> of its
 * first words, or socrates/ and eight letters of its own without any.
 */
export function branchFor(text = '') {
  const slug = slugOf(text);
  if (slug) return 'socrates/' + slug;
  const bytes = new Uint8Array(4);
  crypto.getRandomValues(bytes);
  return 'socrates/' + [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('');
}

/**
 * branchProblem says what is wrong with a branch name, as a sentence, or ''.
 * The server checks it with `git check-ref-format --branch`; this catches
 * what can be seen while typing.
 */
export function branchProblem(name) {
  const text = String(name || '');
  if (!text.trim()) return 'Name the branch.';
  if (/[\s~^:?*[\\\x00-\x1f\x7f]/.test(text) || text.includes('..') || text.includes('@{') || text.includes('//')
    || /^[-/.]|[/.]$|\.lock$|\/\./.test(text) || text === '@') {
    return 'That is not a branch name git takes.';
  }
  return '';
}

/** branchUrl is a chat's branch on GitHub, or its pull request when it has one. */
export function branchUrl(session) {
  if (!session || !session.repo) return '';
  const pr = session.options && session.options.pr;
  if (pr && pr.url) return pr.url;
  if (!session.branch) return 'https://github.com/' + session.repo;
  return 'https://github.com/' + session.repo + '/tree/' + session.branch.split('/').map(encodeURIComponent).join('/');
}
