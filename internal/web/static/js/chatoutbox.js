// What this browser has said to its chats and the server has not taken yet.
//
// One Outbox (net.js) for every chat: a message, a slash command or an answer
// to a question is written to this browser's storage first, sent after, and
// tried again - across reloads, across an hour in a tunnel - until the server
// has it. Each carries a client_id, so a send that is repeated because its
// answer was lost is the same message, never a second one (AGENT-HOST.md §7).
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

async function send(payload) {
  const { kind, chat } = payload;
  if (kind === 'message') {
    return request(path(chat, '/messages'), {
      method: 'POST', attempts: 1, timeout: 20000,
      body: { client_id: payload.client_id, text: payload.text, attachments: payload.attachments || [] },
    });
  }
  if (kind === 'command') {
    return request(path(chat, '/command'), {
      method: 'POST', attempts: 1, timeout: 20000,
      body: { client_id: payload.client_id, name: payload.name, args: payload.args || '' },
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

/** queueAnswer puts the answer to a question on its way. */
export function queueAnswer(chat, ask, answer) {
  const clientId = clientKey();
  rememberMine(clientId);
  chatOutbox().add({ kind: 'answer', chat, ask, client_id: clientId, answer, device: deviceName(), at: Date.now() });
  return clientId;
}

/** pendingFor is every item of one chat still on its way, oldest first. */
export function pendingFor(chat) {
  return chatOutbox().items.filter((item) => item.payload && item.payload.chat === chat);
}
