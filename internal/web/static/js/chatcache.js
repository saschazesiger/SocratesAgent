// The chats this browser has read, kept on the device (IndexedDB) as the
// events their streams brought: a chat opened again - after a reload, or a
// phone that put the page away - is drawn at once from what it said last
// time, marked as such, and its stream asks only for what came after
// (chatview.js). Without it every opening replayed the whole journal over
// the socket, and the conversation was seen building itself up.
//
// A cache and nothing more: anything that goes wrong here - no IndexedDB, a
// private window, a full disk - is a chat that loads from the server as it
// always did.

const DB = 'socrates-chats';
// Version 2 keeps the events compacted (chatfold.js compact); what version 1
// kept - every copy a long workflow journaled, hundreds of megabytes for a
// chat - is let go unread.
const VERSION = 2;
const STORE = 'chats';
// The most chats kept; the ones read longest ago go first.
const KEEP = 40;

let opening = null;

function db() {
  if (opening) return opening;
  opening = new Promise((resolve, reject) => {
    if (!window.indexedDB) { reject(new Error('no IndexedDB')); return; }
    const req = indexedDB.open(DB, VERSION);
    req.onupgradeneeded = () => {
      if (req.result.objectStoreNames.contains(STORE)) req.result.deleteObjectStore(STORE);
      const store = req.result.createObjectStore(STORE, { keyPath: 'id' });
      store.createIndex('at', 'at');
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
    req.onblocked = () => reject(new Error('blocked'));
  }).catch((err) => { opening = null; throw err; });
  return opening;
}

function done(req) {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

/** loadChat is the events kept for a chat, oldest first, or [] when none. */
export async function loadChat(id) {
  try {
    const store = (await db()).transaction(STORE, 'readonly').objectStore(STORE);
    const row = await done(store.get(id));
    return row && Array.isArray(row.frames) ? row.frames : [];
  } catch {
    return [];
  }
}

/** saveChat keeps a chat's events, and lets the oldest chats go. */
export async function saveChat(id, frames) {
  try {
    const tx = (await db()).transaction(STORE, 'readwrite');
    const store = tx.objectStore(STORE);
    store.put({ id, at: Date.now(), frames });
    const count = await done(store.count());
    if (count > KEEP) {
      let extra = count - KEEP;
      const cursor = store.index('at').openCursor();
      cursor.onsuccess = () => {
        const at = cursor.result;
        if (!at || extra <= 0) return;
        if (at.primaryKey !== id) { at.delete(); extra -= 1; }
        at.continue();
      };
    }
  } catch { /* a cache that could not be written is a slower next opening */ }
}

/** forgetChat drops what is kept of one chat. */
export async function forgetChat(id) {
  try {
    (await db()).transaction(STORE, 'readwrite').objectStore(STORE).delete(id);
  } catch { /* nothing kept, nothing to drop */ }
}

/** forgetAll drops every chat kept, for a browser that signs out. */
export async function forgetAll() {
  try {
    const tx = (await db()).transaction(STORE, 'readwrite');
    tx.objectStore(STORE).clear();
    await new Promise((resolve) => { tx.oncomplete = resolve; tx.onerror = resolve; tx.onabort = resolve; });
  } catch { /* nothing kept */ }
}
