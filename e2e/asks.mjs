// ask_user through the real binary, for each of the three agents: a real
// `socrates` server, real agent host processes, the real adapters, and the
// scripted CLIs (e2e/fakebin/fakeclaude, fakecodex, fakeopencode) as claude,
// codex and opencode on the server's PATH. No browser: this drives the routes
// the ask card and dialog (WP8) drive.
//
//   node e2e/asks.mjs
//
// For every agent: the model calls ask_user, the answer goes in through
// POST /api/chats/{id}/asks/{ask}/answer in the shape of REBUILD.md §5.2 (as
// askdialog.js builds it) and comes back to the model through its transport;
// an answer after an interrupt goes to the agent as a message instead; a
// message sent after Stop closes the question nobody waits on (which still
// takes an answer); and a second device is told the question was answered. Codex on a code-mode
// model gets its answer as the next message by design.
//
// Every check prints what it measured beside its verdict, like run.mjs.

import { execFileSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import {
  binaries, cleanupBuild, freePort, scratchDir, spawnServer, waitForHealth, PASSWORD, REPO,
} from './harness.mjs';

let failures = 0;
function check(condition, message, measured) {
  const verdict = condition ? 'ok  ' : 'FAIL';
  if (!condition) failures += 1;
  console.log(`${verdict} ${message}${measured === undefined ? '' : `  [${typeof measured === 'string' ? measured : JSON.stringify(measured)}]`}`);
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// fakes builds the three scripted CLIs under the names the adapters run.
function fakes() {
  const dir = scratchDir('socrates-e2e-asks-bin-');
  for (const [name, pkg] of [['claude', 'fakeclaude'], ['codex', 'fakecodex'], ['opencode', 'fakeopencode']]) {
    execFileSync('go', ['build', '-o', join(dir, name), './e2e/fakebin/' + pkg], { cwd: REPO });
  }
  return dir;
}

async function boot(data, port, bin) {
  const run = spawnServer({ data, port, pathFirst: bin });
  const url = `http://127.0.0.1:${port}`;
  await waitForHealth(url, 25000, run);
  const res = await fetch(url + '/api/setup', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: PASSWORD }),
  });
  const cookie = (res.headers.get('set-cookie') || '').split(';')[0];
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

// stream opens a chat's stream from the start and collects its events.
function stream(srv, id) {
  const s = { events: [], closed: false };
  const ws = new WebSocket(srv.url.replace('http', 'ws') + `/api/chats/${id}/stream`,
    { protocols: ['socrates.chat.v1'], headers: { Cookie: srv.cookie } });
  ws.onopen = () => ws.send(JSON.stringify({ t: 'hello', since: 0 }));
  ws.onmessage = (m) => {
    const f = JSON.parse(m.data);
    if (f.t === 'ev') s.events.push(f);
  };
  ws.onclose = () => { s.closed = true; };
  s.close = () => ws.close();
  s.next = async (pred, after = 0, ms = 20000) => {
    const end = Date.now() + ms;
    while (Date.now() < end) {
      const ev = s.events.find((e) => e.seq > after && pred(e));
      if (ev) return ev;
      await wait(20);
    }
    return null;
  };
  return s;
}

// lastText is the newest finished text part after seq.
const lastText = (events, after) => [...events].reverse()
  .find((e) => e.seq > after && e.type === 'part' && e.kind === 'text' && e.state === 'done')?.text || '';

// dialogAnswer is the answer askdialog.js sends for the scripts' call of two
// questions: an option of the first, the person's own words for the second.
const dialogAnswer = {
  answers: {
    db: { selected: ['sqlite'], other: null, note: 'one file', delegated: false, skipped: false },
    name: { selected: [], other: 'Atlas', note: null, delegated: false, skipped: false },
  },
  general_note: null,
  follow_up: null,
};

// The scripts' turn that calls ask_user, per agent.
const askTurn = { claude: '/askuser', codex: '/tool ask_user', opencode: '/askuser' };

async function agent(srv, data, harness) {
  const work = join(data, 'work-' + harness);
  mkdirSync(work, { recursive: true });
  const created = await call(srv, 'POST', '/api/chats', {
    client_id: `create-${harness}`, harness, workdir_mode: 'custom', workdir: work,
  });
  const id = created.json?.session?.id;
  check(created.status === 201, `${harness}: a chat is created`, created.status);
  if (!id) return;
  const s = stream(srv, id);
  try {
    // The model asks, and the answer reaches it through its transport.
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m1`, text: askTurn[harness] });
    const ask = await s.next((e) => e.type === 'ask');
    check(ask && ask.payload?.questions?.length === 2 && !ask.via, `${harness}: ask_user is an ask of the turn`, ask && { ask_id: ask.ask_id, turn: ask.turn_id, questions: ask.payload.questions.map((q) => q.id) });
    if (!ask) return;
    const open = await call(srv, 'GET', `/api/chats/${id}`);
    check(open.json?.asks?.some((a) => a.id === ask.ask_id), `${harness}: the chat lists the open ask`, open.json?.asks?.length);
    const first = await call(srv, 'POST', `/api/chats/${id}/asks/${ask.ask_id}/answer`, { client_id: `${harness}-a1`, answer: dialogAnswer, device: 'phone' });
    const second = await call(srv, 'POST', `/api/chats/${id}/asks/${ask.ask_id}/answer`, { client_id: `${harness}-a2`, answer: dialogAnswer, device: 'laptop' });
    check(first.status === 200, `${harness}: the answer is taken`, first.status);
    check(second.status === 409 && second.json?.by_device === 'phone', `${harness}: a second device is told who answered`, second.json);
    const delivered = await s.next((e) => e.type === 'ask.delivered' && e.ask_id === ask.ask_id);
    check(delivered?.via === 'tool', `${harness}: the answer went back through the tool call`, delivered?.via);
    const ended = await s.next((e) => e.type === 'turn.ended', ask.seq);
    const got = lastText(s.events, ask.seq);
    check(ended && got.includes('"db":{"selected":["sqlite"],"other":null,"note":"one file"') && got.includes('"other":"Atlas"'),
      `${harness}: the model got the answer of §5.2`, got.slice(0, 160));

    // Interrupted while it waits, the answer arrives as a message.
    const before = s.events[s.events.length - 1].seq;
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m2`, text: askTurn[harness] });
    const again = await s.next((e) => e.type === 'ask', before);
    await call(srv, 'POST', `/api/chats/${id}/interrupt`, { client_id: `${harness}-i1` });
    const cut = await s.next((e) => e.type === 'turn.ended', again?.seq || before);
    check(cut?.outcome === 'interrupted', `${harness}: the interrupt ends the turn, the ask stays open`, cut?.outcome);
    await call(srv, 'POST', `/api/chats/${id}/asks/${again.ask_id}/answer`, { client_id: `${harness}-a3`, answer: dialogAnswer, device: 'phone' });
    const notice = await s.next((e) => e.type === 'notice' && e.code === 'answer_as_message', cut?.seq || before);
    const reply = await s.next((e) => e.type === 'turn.ended' && e.outcome === 'completed', notice?.seq || before);
    const said = lastText(s.events, notice?.seq || before);
    check(notice && reply && said.includes('Answer to your earlier question (Setup)') && said.includes('SQLite [sqlite]'),
      `${harness}: the answer after the interrupt reached the agent as a message`, said.slice(0, 120));

    // Stopped, and then told something else: the question nobody waits on
    // is closed, the chat stops waiting, and it can still be answered.
    const mark = s.events[s.events.length - 1].seq;
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m3`, text: askTurn[harness] });
    const third = await s.next((e) => e.type === 'ask', mark);
    await call(srv, 'POST', `/api/chats/${id}/interrupt`, { client_id: `${harness}-i2` });
    const stopped = await s.next((e) => e.type === 'turn.ended', third?.seq || mark);
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m4`, text: 'skip that, do X' });
    const closed = await s.next((e) => e.type === 'ask.closed' && e.ask_id === third?.ask_id, stopped?.seq || mark);
    await s.next((e) => e.type === 'turn.ended' && e.outcome === 'completed', closed?.seq || mark);
    const after = await call(srv, 'GET', `/api/chats/${id}`);
    check(closed && !(after.json?.asks || []).length && after.json?.chat?.activity !== 'waiting',
      `${harness}: a message after Stop closes the question`, { closed: !!closed, open: (after.json?.asks || []).length, activity: after.json?.chat?.activity });
    const late = await call(srv, 'POST', `/api/chats/${id}/asks/${third?.ask_id}/answer`, { client_id: `${harness}-a4`, answer: dialogAnswer, device: 'phone' });
    const lateGone = await s.next((e) => e.type === 'ask.delivered' && e.ask_id === third?.ask_id, closed?.seq || mark);
    check(late.status === 200 && lateGone?.via === 'message', `${harness}: a closed question still takes an answer, as a message`, { status: late.status, via: lateGone?.via });
  } finally {
    s.close();
  }
}

// A code-mode model would poll a blocking tool; the call returns at once and
// the answer is the next message.
async function codeMode(srv, data) {
  const work = join(data, 'work-codemode');
  mkdirSync(work, { recursive: true });
  const created = await call(srv, 'POST', '/api/chats', {
    client_id: 'create-codemode', harness: 'codex', model: 'gpt-6-astra', workdir_mode: 'custom', workdir: work,
  });
  const id = created.json?.session?.id;
  if (!id) {
    check(false, 'codex code mode: a chat is created', created.status);
    return;
  }
  const s = stream(srv, id);
  try {
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: 'cm-m1', text: '/tool ask_user' });
    const ask = await s.next((e) => e.type === 'ask');
    const ended = await s.next((e) => e.type === 'turn.ended', ask?.seq || 0);
    check(ask?.via === 'message' && lastText(s.events, ask.seq).includes('Questions delivered. End your turn now'),
      'codex code mode: the call returns at once and the turn ends', { via: ask?.via, ended: ended?.outcome });
    await call(srv, 'POST', `/api/chats/${id}/asks/${ask.ask_id}/answer`, { client_id: 'cm-a1', answer: dialogAnswer, device: 'phone' });
    const delivered = await s.next((e) => e.type === 'ask.delivered', ask.seq);
    const reply = await s.next((e) => e.type === 'turn.ended', delivered?.seq || ask.seq);
    const notice = s.events.find((e) => e.type === 'notice' && e.code === 'answer_as_message');
    check(delivered?.via === 'message' && !notice && reply && lastText(s.events, delivered.seq).includes('Answer to your earlier question (Setup)'),
      'codex code mode: the answer starts the next turn, with no lost-call notice', lastText(s.events, delivered?.seq || 0).slice(0, 80));
  } finally {
    s.close();
  }
}

async function main() {
  binaries();
  const bin = fakes();
  const data = scratchDir('socrates-e2e-asks-');
  const port = await freePort();
  const srv = await boot(data, port, bin);
  try {
    for (const harness of ['claude', 'codex', 'opencode']) await agent(srv, data, harness);
    await codeMode(srv, data);
  } finally {
    srv.server.kill('SIGKILL');
    try { execFileSync('pkill', ['-f', `agent-host --data ${join(data)}`]); } catch { /* none left, as it should be */ }
    cleanupBuild();
  }
  console.log(failures === 0 ? '\nall ask checks passed' : `\n${failures} ask check(s) failed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => { console.error(err); process.exit(1); });
