// A test run started by a chat's agent (docs/design/TEST-MODE.md, "Runs
// started by chat agents"): a chat in a repository with a catalog has the
// test tools, its agent (the fake Claude Code, /runtests) starts a run of one
// test with run_tests, the run remembers the chat, and when it ends the chat
// is sent the results, which starts its agent; test_results then gives the
// problem cell with its observation and screenshot.

import { setup, start, ok, wait } from './harness.mjs';
import { catalogRepo } from './testmode.mjs';

// The words of an assistant message: its text parts.
const textOf = (m) => (m.parts || []).filter((p) => p.kind === 'text').map((p) => p.text || '').join('\n');

async function messagesOf(s, id) {
  const res = await s.context.request.get(s.url + '/api/chats/' + encodeURIComponent(id) + '?history=1');
  return (((await res.json()).messages) || {}).messages || [];
}

// replyAfter waits for the agent's answer to the user message that `isMine`
// picks, and answers its words ('' when none came).
async function replyAfter(s, id, isMine, timeout = 30000) {
  const until = Date.now() + timeout;
  while (Date.now() < until) {
    const messages = await messagesOf(s, id);
    const at = messages.findIndex((m) => m.role === 'user' && isMine(m.text || ''));
    if (at >= 0) {
      const reply = messages.slice(at + 1).find((m) => m.role === 'assistant' && textOf(m).trim() !== '');
      if (reply) return { user: messages[at], text: textOf(reply) };
    }
    await wait(200);
  }
  return { user: null, text: '' };
}

export async function testchat() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  const api = s.context.request;
  const runs = [];
  try {
    await setup(s.page, s.url);
    const repo = catalogRepo();
    const made = await api.post(s.url + '/api/chats', {
      headers: { Origin: s.url },
      data: { client_id: 'e2e-testchat-' + Date.now(), harness: 'claude', workdir_mode: 'custom', workdir: repo,
        message: { client_id: 'e2e-testchat-m1', text: '/runtests web/search' } },
    });
    ok(made.status() === 201, 'a chat in the repository with a catalog is made', String(made.status()));
    const chat = (await made.json()).session.id;

    const started = await replyAfter(s, chat, (t) => t === '/runtests web/search');
    const runID = (/Started test run \d+ \((r-\S+?)\):/.exec(started.text) || [])[1] || '';
    if (runID) runs.push(runID);
    ok(started.text.startsWith('run_tests returned: Started test run') && /: 1 cell\(s\)/.test(started.text)
      && started.text.includes('End your turn'), 'the agent starts a run of one test with run_tests, and is told to end its turn',
    started.text.slice(0, 200));

    let run = {};
    const deadline = Date.now() + 120000;
    while (runID && Date.now() < deadline) {
      run = await (await api.get(s.url + '/api/tests/runs/' + runID)).json();
      if (['done', 'failed', 'stopped'].includes(run.state)) break;
      await wait(250);
    }
    ok(run.state === 'done', 'the run ends done', String(run.state) + ' ' + (run.error || ''));
    ok(run.started_by && run.started_by.chat === chat && run.counts && run.counts.cells === 1,
      'the run remembers the chat that started it', JSON.stringify(run.started_by));

    const told = await replyAfter(s, chat, (t) => t.startsWith('[From Socrates Tests:'), 30000);
    ok(told.user !== null && told.user.text.includes('web/search@a (fail)') && told.user.text.includes('test_results'),
      'the chat is sent the results when the run ends', (told.user ? told.user.text : 'nothing').replace(/\s+/g, ' ').slice(0, 240));
    ok(told.text.startsWith('you said: [From Socrates Tests:'), 'and its agent takes them up in a turn of its own', told.text.slice(0, 80));

    const res = await api.post(s.url + '/api/chats/' + chat + '/messages', {
      headers: { Origin: s.url }, data: { client_id: 'e2e-testchat-m2', text: '/mcptool test_results {}' },
    });
    ok(res.status() < 300, 'the agent is asked to read the results', String(res.status()));
    const results = await replyAfter(s, chat, (t) => t === '/mcptool test_results {}');
    ok(results.text.startsWith('test_results returned: # ') && results.text.includes('- Run: ' + runID) && results.text.includes('web/search@a')
      && results.text.includes('Step 1, fail') && /Screenshots: \/\S+\/1\.png/.test(results.text),
    'test_results gives the problem cell with its failing step and its screenshot’s path', results.text.replace(/\s+/g, ' ').slice(0, 300));

    // Told once: the message is in the chat a single time.
    await wait(1000);
    const all = (await messagesOf(s, chat)).filter((m) => m.role === 'user' && (m.text || '').startsWith('[From Socrates Tests:'));
    ok(all.length === 1, 'the results are sent once', String(all.length));

    // The chat shows the message under a line that says where it comes
    // from, without the line meant for the agent.
    await s.page.goto(s.url + '/#' + chat, { waitUntil: 'domcontentloaded' });
    const line = s.page.locator('#chat .so-line[data-code="test_results"]').first();
    await line.waitFor({ timeout: 15000 }).catch(() => {});
    const lineText = (await line.count()) ? await line.innerText() : '';
    const bubbles = await s.page.$$eval('#chat .so-msg[data-role="user"]', (els) => els.map((e) => e.innerText));
    ok(lineText.includes('The results of the test run this chat started')
      && bubbles.some((b) => b.includes('web/search@a')) && !bubbles.some((b) => b.includes('[From Socrates Tests')),
    'the chat shows the results under their own line', lineText + ' | ' + bubbles.length + ' messages');
    ok(s.errors.length === 0, 'the page logged no errors', s.errors.join(' | '));
  } finally {
    for (const id of runs) await api.delete(s.url + '/api/tests/runs/' + id, { headers: { Origin: s.url } }).catch(() => {});
    await s.stop();
  }
}
