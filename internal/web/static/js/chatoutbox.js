// What this browser has said to its chats and the server has not taken yet.
//
// One Outbox (net.js) for every chat and every tab: a message, a slash
// command, a change of model or an answer to a question is written to this
// browser's storage first, sent after, and tried again - across reloads,
// across an hour in a tunnel - until the server has it. Each carries a
// client_id, so a send that is repeated because its answer was lost is the
// same message, never a second one (AGENT-HOST.md §7). Strict order is what
// makes a model chosen offline the model of the message written after it.
// It is mounted when the page starts, not when a chat is opened: what was
// typed into a chat that is not on screen any more still goes out.

import { Outbox, HttpError, clientKey, request } from './net.js';

let box = null;
const listeners = new Set();

const path = (chat, tail) => '/api/chats/' + encodeURIComponent(chat) + tail;

/**
 * deviceName is what this device is called where another one is told that it
 * answered first ("Answered on another device: iPhone").
 */
export function deviceName() {
  const ua = navigator.userAgent || '';
  if (/Tesla\/|QtCarBrowser/.test(ua)) return 'Car';
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? 'Android phone' : 'Android tablet';
  if (/Macintosh/.test(ua)) return 'Mac';
  if (/Windows/.test(ua)) return 'Windows PC';
  if (/Linux/.test(ua)) return 'Linux';
  return 'Browser';
}

// The client_ids of the answers this browser gave, so that an answered
// question can say whether it was answered here or somewhere else.
const MINE_KEY = 'socrates.answers.mine';
function mineList() {
  try {
    const list = JSON.parse(localStorage.getItem(MINE_KEY) || '[]');
    return Array.isArray(list) ? list : [];
  } catch { return []; }
}

/** answeredHere says whether an answer's client_id is this browser's. */
export function answeredHere(clientId) {
  return !!clientId && mineList().includes(clientId);
}

function rememberMine(clientId) {
  try {
    const list = mineList().filter((one) => one !== clientId);
    list.push(clientId);
    localStorage.setItem(MINE_KEY, JSON.stringify(list.slice(-200)));
  } catch { /* a private window: the stream still says who answered */ }
}

// A message the server took is in the chat's journal, but this page sees
// it only once the chat's stream brings it. Until then the page shows it as
// sent (chatview.js), and the device keeps it, so that a reload in between
// does not make it vanish: an iPad whose stream did not come back showed the
// message as sent, and after a reload it was gone, with nothing to say the
// server had it (B-3). One key per message, as the Outbox has, and only for
// a while: one the journal never shows to this device - its chat deleted,
// say - is let go after SENT_KEEP.
const SENT_PREFIX = 'socrates.sent.';
const SENT_KEEP = 2 * 24 * 3600 * 1000;

function keepSent(payload) {
  try {
    localStorage.setItem(SENT_PREFIX + payload.client_id, JSON.stringify({ payload, at: Date.now() }));
  } catch { /* a private window: the page's own copy is all there is */ }
}

/** forgetSent lets go of a sent message once the journal shows it. */
export function forgetSent(clientId) {
  try { localStorage.removeItem(SENT_PREFIX + clientId); } catch { /* ignore */ }
}

/** forgetAllSent drops every sent message kept, for a browser that signs out. */
export function forgetAllSent() {
  for (const [clientId] of keptSent()) forgetSent(clientId);
}

/**
 * keptSent is every message this device sent that a page has not seen in
 * its chat's journal yet, as [client_id, { payload, at }].
 */
export function keptSent() {
  const out = [];
  try {
    const keys = [];
    for (let i = 0; i < localStorage.length; i += 1) {
      const key = localStorage.key(i);
      if (key && key.startsWith(SENT_PREFIX)) keys.push(key);
    }
    for (const key of keys) {
      let entry = null;
      try { entry = JSON.parse(localStorage.getItem(key)); } catch { /* damaged: dropped below */ }
      if (!entry || !entry.payload || !entry.payload.client_id || !(Date.now() - Number(entry.at) < SENT_KEEP)) {
        localStorage.removeItem(key);
        continue;
      }
      out.push([entry.payload.client_id, entry]);
    }
  } catch { /* no storage */ }
  return out.sort((a, b) => a[1].at - b[1].at);
}

async function send(payload) {
  const { kind, chat } = payload;
  if (kind === 'message') {
    const answer = await request(path(chat, '/messages'), {
      method: 'POST', attempts: 1, timeout: 20000,
      body: { client_id: payload.client_id, text: payload.text, attachments: payload.attachments || [] },
    });
    keepSent(payload);
    return answer;
  }
  if (kind === 'command') {
    return request(path(chat, '/command'), {
      method: 'POST', attempts: 1, timeout: 20000,
      body: { client_id: payload.client_id, name: payload.name, args: payload.args || '' },
    });
  }
  if (kind === 'model') {
    return request(path(chat, '/model'), {
      method: 'POST', attempts: 1, timeout: 20000,
      body: { client_id: payload.client_id, model: payload.model, effort: payload.effort || '' },
    });
  }
  if (kind === 'answer') {
    try {
      return await request(path(chat, '/asks/' + encodeURIComponent(payload.ask) + '/answer'), {
        method: 'POST', attempts: 1, timeout: 20000,
        body: { client_id: payload.client_id, answer: payload.answer, device: payload.device },
      });
    } catch (err) {
      // Another device answered first. That is an answer, not a failure:
      // the question is settled, and the page says by whom.
      if (err instanceof HttpError && err.status === 409) {
        for (const listener of listeners) listener({ type: 'elsewhere', payload });
        return null;
      }
      throw err;
    }
  }
  return null;
}

/** chatOutbox is the one outbox, made on first use. */
export function chatOutbox() {
  if (!box) box = new Outbox('chats', send);
  return box;
}

/** onOutbox hears about answers that turned out to be another device's. */
export function onOutbox(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** queueMessage puts a message on its way and answers its client_id. */
export function queueMessage(chat, text, attachments = []) {
  const clientId = clientKey();
  chatOutbox().add({ kind: 'message', chat, client_id: clientId, text, attachments, at: Date.now() });
  return clientId;
}

/** queueCommand puts a slash command on its way. */
export function queueCommand(chat, name, args, text) {
  const clientId = clientKey();
  chatOutbox().add({ kind: 'command', chat, client_id: clientId, name, args, text, at: Date.now() });
  return clientId;
}

/** queueModel puts a change of the chat's model and effort on its way. */
export function queueModel(chat, model, effort) {
  const clientId = clientKey();
  chatOutbox().add({ kind: 'model', chat, client_id: clientId, model, effort, at: Date.now() });
  return clientId;
}

/**
 * queueAnswer puts the answer to a question on its way. An earlier answer of
 * this browser that the server refused is replaced by it.
 */
export function queueAnswer(chat, ask, answer) {
  const refused = pendingAnswer(chat, ask);
  if (refused && refused.state === 'failed') chatOutbox().drop(refused.id);
  const clientId = clientKey();
  rememberMine(clientId);
  chatOutbox().add({ kind: 'answer', chat, ask, client_id: clientId, answer, device: deviceName(), at: Date.now() });
  return clientId;
}

/** pendingFor is every item of one chat still on its way, oldest first. */
export function pendingFor(chat) {
  return chatOutbox().items.filter((item) => item.payload && item.payload.chat === chat);
}

/**
 * pendingAnswer is this browser's answer to one ask that the server does not
 * have yet - on its way, or refused for good (state "failed") - or null.
 */
export function pendingAnswer(chat, ask) {
  const items = pendingFor(chat).filter((item) => item.payload.kind === 'answer' && item.payload.ask === ask);
  return items.length ? items[items.length - 1] : null;
}

/** pendingModel is the newest change of model still on its way to a chat, or null. */
export function pendingModel(chat) {
  const items = pendingFor(chat).filter((item) => item.payload.kind === 'model' && item.state !== 'failed');
  return items.length ? items[items.length - 1].payload : null;
}

/** failedItem is the item a client_id sent if the server refused it for good, or null. */
export function failedItem(clientId) {
  if (!clientId) return null;
  return chatOutbox().items.find((item) => item.state === 'failed' && item.payload && item.payload.client_id === clientId) || null;
}

/**
 * settled answers once what a client_id sent is out of this browser's hands:
 * it resolves when the item has left the Outbox (the server has it), and
 * rejects with the server's words when the server refused it for good.
 */
export function settled(clientId) {
  return new Promise((resolve, reject) => {
    let stop = null;
    let done = false;
    const listener = (items) => {
      if (done) return;
      const item = items.find((one) => one.payload && one.payload.client_id === clientId);
      if (item && item.state !== 'failed') return;
      done = true;
      if (stop) stop();
      if (item) reject(new Error(item.error || 'It was refused.'));
      else resolve();
    };
    stop = chatOutbox().onChange(listener);
    if (done) stop();
  });
}
