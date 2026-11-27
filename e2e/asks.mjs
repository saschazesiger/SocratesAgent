// ask_user through the real binary, for each of the three agents: a real
// `socrates` server, real agent host processes, the real adapters, and the
// scripted CLIs (e2e/fakebin/fakeclaude, fakecodex, fakeopencode) as claude,
// codex and opencode on the server's PATH. No browser: this drives the routes
// the ask card and dialog (WP8) drive.
//
//   node e2e/asks.mjs
//
// For every agent (REBUILD.md §5): the model calls ask_user with one
// question, the call returns at once with the question's id and the turn
// ends; the answer goes in through POST /api/chats/{id}/asks/{ask}/answer in
// the shape of REBUILD.md §5.2 (as askdialog.js builds it) and reaches the
// model as a user message that starts a turn, and a second device is told
// the question was answered. A question stays open through a message of the
// person's and through a Stop; while it is open a running turn stays busy
// inside, and the person is shown the chat waiting for it; withdraw_question takes it back with its reason, and
// check_questions says so. Codex on a code-mode model asks the same way.
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
    method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Z-Dash': '1' }, body: JSON.stringify({ password: PASSWORD }),
  });
  const cookie = (res.headers.get('set-cookie') || '').split(';')[0];
  return { ...run, url, cookie };
}

async function call(srv, method, path, body) {
  const res = await fetch(srv.url + path, {
    method, headers: { 'Content-Type': 'application/json', 'X-Z-Dash': '1', Cookie: srv.cookie },
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

// textsBetween is every finished text part after seq a and up to seq b, joined.
const textsBetween = (events, a, b) => events
  .filter((e) => e.seq > a && e.seq <= b && e.type === 'part' && e.kind === 'text' && e.state === 'done')
  .map((e) => e.text).join('\n');

// settled is the chat once its agent is no longer at work (or after a few
// seconds, as it is then).
async function settled(srv, id) {
  const end = Date.now() + 10000;
  for (;;) {
    const got = await call(srv, 'GET', `/api/chats/${id}`);
    if (got.json?.chat?.activity !== 'busy' || Date.now() > end) return got;
    await wait(50);
  }
}

// dialogAnswer is the answer askdialog.js sends for the scripts' call of one
// question (q1, "Which database?"): an option and a note beside it.
const dialogAnswer = {
  answers: {
    q1: { selected: ['sqlite'], other: null, note: 'one file', delegated: false, skipped: false },
  },
  general_note: null,
  follow_up: null,
};

// The scripts' turn that calls ask_user, one that works a few seconds, and
// a call of another tool of the socrates server, per agent.
const askTurn = { claude: '/askuser', codex: '/tool ask_user', opencode: '/askuser' };
const slowTurn = { claude: '/slow 4', codex: '/sleep 4', opencode: '/sleep 4' };
const toolTurn = {
  claude: (name, args) => `/mcptool ${name} ${JSON.stringify(args)}`,
  codex: (name, args) => `/tool ${name} ${JSON.stringify(args)}`,
  opencode: (name, args) => `/mcptool ${name} ${JSON.stringify(args)}`,
};

// textsAfter is every finished text part after seq, joined.
const textsAfter = (events, after) => textsBetween(events, after, Infinity);

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
    // The model asks one question; the call returns at once with its id,
    // and the turn ends.
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m1`, text: askTurn[harness] });
    const ask = await s.next((e) => e.type === 'ask');
    check(ask && ask.payload?.questions?.length === 1 && ask.via === 'message' && ask.payload?.blocks === 'The schema',
      `${harness}: ask_user is one question, answered as a message`,
      ask && { ask_id: ask.ask_id, via: ask.via, questions: ask.payload.questions.map((q) => q.id), blocks: ask.payload.blocks });
    if (!ask) return;
    const ended = await s.next((e) => e.type === 'turn.ended', ask.seq);
    const told = textsBetween(s.events, ask.seq, ended?.seq || 0);
    check(ended?.outcome === 'completed' && told.includes(`Question ${ask.ask_id} is now with the person`),
      `${harness}: the call returns at once and the turn ends`, { outcome: ended?.outcome, said: told.slice(0, 120) });
    const open = await settled(srv, id);
    check(open.json?.asks?.some((a) => a.id === ask.ask_id) && open.json?.chat?.activity === 'waiting',
      `${harness}: the chat lists the open ask and waits`, { open: open.json?.asks?.length, activity: open.json?.chat?.activity });

    // The answer arrives as a message, which starts a turn.
    const first = await call(srv, 'POST', `/api/chats/${id}/asks/${ask.ask_id}/answer`, { client_id: `${harness}-a1`, answer: dialogAnswer, device: 'phone' });
    const second = await call(srv, 'POST', `/api/chats/${id}/asks/${ask.ask_id}/answer`, { client_id: `${harness}-a2`, answer: dialogAnswer, device: 'laptop' });
    check(first.status === 200, `${harness}: the answer is taken`, first.status);
    check(second.status === 409 && second.json?.by_device === 'phone', `${harness}: a second device is told who answered`, second.json);
    const delivered = await s.next((e) => e.type === 'ask.delivered' && e.ask_id === ask.ask_id);
    check(delivered?.via === 'message', `${harness}: the answer goes to the agent as a message`, delivered?.via);
    const started = await s.next((e) => e.type === 'turn.started', ended?.seq || ask.seq);
    const reply = await s.next((e) => e.type === 'turn.ended', started?.seq || ask.seq);
    const got = textsBetween(s.events, started?.seq || 0, reply?.seq || 0);
    check(started && reply && got.includes(`The person answered your question ${ask.ask_id}`)
      && got.includes('"q1":{"selected":["sqlite"],"other":null,"note":"one file"'),
      `${harness}: the model got the answer of §5.2 in a turn of its own`, got.slice(0, 160));

    // A question stays open through a message of the person's and a Stop:
    // the agent did not stop for it.
    const mark = s.events[s.events.length - 1].seq;
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m2`, text: askTurn[harness] });
    const again = await s.next((e) => e.type === 'ask', mark);
    const againEnded = await s.next((e) => e.type === 'turn.ended', again?.seq || mark);
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m3`, text: 'skip that, do X' });
    const wrote = await s.next((e) => e.type === 'turn.ended', againEnded?.seq || mark);
    const afterMessage = await settled(srv, id);
    check(wrote && !s.events.some((e) => e.type === 'ask.closed' && e.ask_id === again?.ask_id)
      && afterMessage.json?.asks?.some((a) => a.id === again?.ask_id) && afterMessage.json?.chat?.activity === 'waiting',
      `${harness}: a message does not close the question`, { open: (afterMessage.json?.asks || []).length, activity: afterMessage.json?.chat?.activity });
    const slowMark = s.events[s.events.length - 1].seq;
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m4`, text: slowTurn[harness] });
    const slow = await s.next((e) => e.type === 'turn.started', slowMark);
    await wait(500);
    const running = await call(srv, 'GET', `/api/chats/${id}`);
    check(slow && running.json?.chat?.activity === 'busy' && running.json?.chat?.questions === 1
      && running.json?.session?.activity?.state === 'waiting' && running.json?.asks?.some((a) => a.id === again?.ask_id),
      `${harness}: a running turn stays busy inside, while the person is shown the chat waiting for the question`,
      { turn: running.json?.chat?.activity, questions: running.json?.chat?.questions, shown: running.json?.session?.activity?.state });
    await call(srv, 'POST', `/api/chats/${id}/interrupt`, { client_id: `${harness}-i1` });
    const cut = await s.next((e) => e.type === 'turn.ended', slow?.seq || slowMark);
    const afterStop = await settled(srv, id);
    check(cut?.outcome === 'interrupted' && !s.events.some((e) => e.type === 'ask.closed' && e.ask_id === again?.ask_id)
      && afterStop.json?.asks?.some((a) => a.id === again?.ask_id) && afterStop.json?.chat?.activity === 'waiting',
      `${harness}: Stop does not close the question`, { outcome: cut?.outcome, open: (afterStop.json?.asks || []).length, activity: afterStop.json?.chat?.activity });

    // The agent withdraws it, with its reason; check_questions says so.
    const reason = 'Found it in the README';
    const wdMark = s.events[s.events.length - 1].seq;
    await call(srv, 'POST', `/api/chats/${id}/messages`, {
      client_id: `${harness}-m5`, text: toolTurn[harness]('withdraw_question', { question_id: again?.ask_id, reason }),
    });
    const withdrawn = await s.next((e) => e.type === 'ask.closed' && e.ask_id === again?.ask_id, wdMark);
    const wdEnded = await s.next((e) => e.type === 'turn.ended', withdrawn?.seq || wdMark);
    const afterWithdraw = await settled(srv, id);
    check(withdrawn?.code === 'withdrawn' && withdrawn?.text === reason && wdEnded
      && !(afterWithdraw.json?.asks || []).length && afterWithdraw.json?.chat?.activity !== 'waiting',
      `${harness}: withdraw_question closes the question with its reason`,
      { code: withdrawn?.code, text: withdrawn?.text, said: textsAfter(s.events, wdMark).slice(0, 100), open: (afterWithdraw.json?.asks || []).length, activity: afterWithdraw.json?.chat?.activity });
    const cqMark = s.events[s.events.length - 1].seq;
    await call(srv, 'POST', `/api/chats/${id}/messages`, { client_id: `${harness}-m6`, text: toolTurn[harness]('check_questions', {}) });
    const cqEnded = await s.next((e) => e.type === 'turn.ended', cqMark);
    const listed = textsBetween(s.events, cqMark, cqEnded?.seq || 0);
    check(cqEnded && listed.includes(again?.ask_id) && /withdrawn/.test(listed) && listed.includes(reason) && listed.includes(ask.ask_id),
      `${harness}: check_questions lists the answered and the withdrawn question`, listed.slice(0, 200));
  } finally {
    s.close();
  }
}

// A code-mode model would poll a blocking tool; no ask_user call blocks
// now, so it asks the way every model does.
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
    check(ask?.via === 'message' && textsAfter(s.events, ask.seq).includes(`Question ${ask.ask_id} is now with the person`),
      'codex code mode: the call returns at once and the turn ends', { via: ask?.via, ended: ended?.outcome });
    await call(srv, 'POST', `/api/chats/${id}/asks/${ask.ask_id}/answer`, { client_id: 'cm-a1', answer: dialogAnswer, device: 'phone' });
    const delivered = await s.next((e) => e.type === 'ask.delivered', ask.seq);
    const reply = await s.next((e) => e.type === 'turn.ended', delivered?.seq || ask.seq);
    const notice = s.events.find((e) => e.type === 'notice' && e.code === 'answer_as_message');
    check(delivered?.via === 'message' && !notice && reply && textsAfter(s.events, delivered.seq).includes(`The person answered your question ${ask.ask_id}`),
      'codex code mode: the answer starts the next turn, with no lost-call notice', textsAfter(s.events, delivered?.seq || 0).slice(0, 80));
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
