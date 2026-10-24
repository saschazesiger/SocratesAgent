// The chat API against the real binary: a real `socrates` server, real agent
// host processes, and the scripted agent (SOCRATES_FAKE_AGENT=1) in them. No
// browser: the chat view is WP8. This drives what the view will drive - the
// REST routes and the chat stream - through a server that is killed and
// started again in the middle of a turn.
//
//   node e2e/chats.mjs
//
// Every check prints what it measured beside its verdict, like run.mjs.

import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import {
  binaries, cleanupBuild, freePort, scratchDir, spawnServer, waitForHealth, PASSWORD,
} from './harness.mjs';

let failures = 0;
function check(condition, message, measured) {
  const verdict = condition ? 'ok  ' : 'FAIL';
  if (!condition) failures += 1;
  console.log(`${verdict} ${message}${measured === undefined ? '' : `  [${typeof measured === 'string' ? measured : JSON.stringify(measured)}]`}`);
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// A server on a data folder, and the cookie that signs in to it.
async function boot(data, port, cookie) {
  const run = spawnServer({ data, port, env: { SOCRATES_FAKE_AGENT: '1' } });
  const url = `http://127.0.0.1:${port}`;
  await waitForHealth(url);
  if (!cookie) {
    const res = await fetch(url + '/api/setup', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: PASSWORD }),
    });
    cookie = (res.headers.get('set-cookie') || '').split(';')[0];
  }
  return { ...run, url, cookie };
}

async function call(srv, method, path, body) {
  const res = await fetch(srv.url + path, {
    method, headers: { 'Content-Type': 'application/json', Cookie: srv.cookie },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let json = null;
  try { json = await res.json(); } catch { /* an empty answer */ }
  return { status: res.status, json };
}

// stream opens /api/chats/{id}/stream with hello{since} and collects events.
function stream(srv, id, since) {
  const s = { events: [], deltas: 0, synced: false, closed: false, hello: null };
  const ws = new WebSocket(srv.url.replace('http', 'ws') + `/api/chats/${id}/stream`,
    { protocols: ['socrates.chat.v1'], headers: { Cookie: srv.cookie } });
  ws.onopen = () => ws.send(JSON.stringify({ t: 'hello', since }));
  ws.onmessage = (m) => {
    const f = JSON.parse(m.data);
    if (f.t === 'ev') s.events.push(f);
    else if (f.t === 'delta') s.deltas += 1;
    else if (f.t === 'synced') s.synced = true;
    else if (f.t === 'hello') s.hello = f;
  };
  ws.onclose = () => { s.closed = true; };
  s.close = () => ws.close();
  s.until = async (pred, ms = 15000) => {
    const end = Date.now() + ms;
    while (Date.now() < end) {
      if (s.events.some(pred)) return true;
      await wait(20);
    }
    return false;
  };
  return s;
}

const contiguous = (events, from) => events.every((ev, i) => ev.seq === from + i);
const lastText = (events) => [...events].reverse().find((e) => e.type === 'part' && e.kind === 'text' && e.state === 'done')?.text || '';

async function main() {
  binaries();
  const data = scratchDir('socrates-e2e-chats-');
  const port = await freePort();
  let srv = await boot(data, port);
  let chatID = null;
  try {
    const created = await call(srv, 'POST', '/api/chats', {
      client_id: 'e2e-create', harness: 'fake', workdir_mode: 'custom', workdir: data,
    });
    chatID = created.json?.session?.id;
    check(created.status === 201 && created.json.session.kind === 'chat', 'a chat is created as a session row of kind chat', created.status);
    const again = await call(srv, 'POST', '/api/chats', { client_id: 'e2e-create', harness: 'fake', workdir_mode: 'custom', workdir: data });
    check(again.status === 200 && again.json.session.id === chatID, 'a repeated create finds the same chat', again.status);

    // A long turn, and the server killed in the middle of it.
    const phone = stream(srv, chatID, 0);
    const sent = await call(srv, 'POST', `/api/chats/${chatID}/messages`, { client_id: 'e2e-m1', text: '/stream 300 10' });
    check(sent.status === 200 && sent.json.seq > 0, 'the message is acknowledged once journaled', sent.json);
    await phone.until((e) => e.type === 'part.delta');
    check(phone.deltas > 0, 'live deltas arrive before the coalesced lines', phone.deltas);
    srv.server.kill('SIGKILL');
    await srv.exited;
    const before = phone.events.slice();
    await wait(1500);
    srv = await boot(data, port, srv.cookie);
    const back = stream(srv, chatID, before[before.length - 1].seq);
    const ended = await back.until((e) => e.type === 'turn.ended');
    const all = before.concat(back.events);
    check(ended, 'the turn completed across a killed server');
    check(contiguous(all, 1), 'the stream after the restart continues exactly where the phone stopped', `${before.length} + ${back.events.length} events, last seq ${all[all.length - 1]?.seq}`);
    check(lastText(all).endsWith('w300 '), 'the whole answer is there', lastText(all).slice(-12));
    back.close();
    phone.close();

    // A message sent twice (the phone retried) reaches the agent once.
    const dup = await call(srv, 'POST', `/api/chats/${chatID}/messages`, { client_id: 'e2e-m1', text: '/stream 300 10' });
    check(dup.status === 200 && dup.json.dup === true, 'a retried message is a duplicate, not a second turn', dup.json);

    // A question, answered on two devices.
    const laptop = stream(srv, chatID, 0);
    await call(srv, 'POST', `/api/chats/${chatID}/messages`, { client_id: 'e2e-m2', text: '/ask' });
    await laptop.until((e) => e.type === 'ask');
    const ask = laptop.events.find((e) => e.type === 'ask');
    const first = await call(srv, 'POST', `/api/chats/${chatID}/asks/${ask.ask_id}/answer`, { client_id: 'e2e-a1', answer: 'a', device: 'phone' });
    const second = await call(srv, 'POST', `/api/chats/${chatID}/asks/${ask.ask_id}/answer`, { client_id: 'e2e-a2', answer: 'b', device: 'laptop' });
    check(first.status === 200, 'the first device answers', first.status);
    check(second.status === 409 && second.json.answered_elsewhere && second.json.by_device === 'phone',
      'the second device is told it was answered on another device', second.json);
    const answered = await laptop.until((e) => e.type === 'turn.ended' && e.seq > ask.seq);
    check(answered && lastText(laptop.events) === 'you answered: "a"', 'the agent got the first answer', lastText(laptop.events));
    laptop.close();

    const list = await call(srv, 'GET', '/api/sessions');
    const row = list.json.sessions.find((s) => s.id === chatID);
    check(row && row.activity.state === 'idle', 'the sidebar list carries the chat with its activity', row?.activity);
  } finally {
    if (chatID) await call(srv, 'DELETE', `/api/sessions/${chatID}`).catch(() => {});
    srv.server.kill('SIGKILL');
    try { execFileSync('pkill', ['-f', `agent-host --data ${join(data)}`]); } catch { /* none left, as it should be */ }
    cleanupBuild();
  }
  console.log(failures === 0 ? '\nall chat checks passed' : `\n${failures} chat check(s) failed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => { console.error(err); process.exit(1); });
