// The Tests page's Bugs view and its chat dialog (docs/design/TEST-MODE.md,
// "The Tests page", Bugs and the chat dialog): after a run of the fixture
// catalog the analyst's bugs are listed; a bug's view shows its cells with
// their screenshots; triage from the page changes the bug's status and the
// tab's badge, and Undo takes it back; GitHub, which the fixture has no
// remote for, says why no issue can be made; Fix in a chat makes a chat in
// the repository with the test tools and a message about the bug, and opens
// it. Everything made is deleted again.

import { setup, shot, start, ok, wait } from './harness.mjs';
import { catalogRepo } from './testmode.mjs';

export async function testsbugs() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  const api = s.context.request;
  const headers = { Origin: s.url };
  const get = async (path) => (await api.get(s.url + path)).json();
  let runID = '';
  let chatID = '';
  try {
    await setup(s.page, s.url);
    const repo = catalogRepo();
    const added = await api.post(s.url + '/api/tests/repos', { data: { path: repo }, headers });
    const key = (await added.json()).key;
    const started = await api.post(s.url + '/api/tests/repos/' + key + '/runs', { data: { name: 'bugs' }, headers });
    runID = (await started.json()).id;
    let state = '';
    const deadline = Date.now() + 120000;
    while (Date.now() < deadline) {
      state = (await get('/api/tests/runs/' + runID)).state;
      if (['done', 'failed', 'stopped'].includes(state)) break;
      await wait(250);
    }
    ok(state === 'done', 'the run of the fixture catalog ends done', state);
    const bugs = (await get('/api/tests/repos/' + key + '/bugs')).bugs;
    const bug = bugs.find((b) => b.cells.includes('web/search@a'));
    ok(bugs.length === 2 && !!bug && bug.status === 'open', 'the analyst left two open bugs', JSON.stringify(bugs.map((b) => b.id + ' ' + b.status)));

    // The list: open bugs, each a row that opens it.
    const page = s.page;
    await page.goto(s.url + '/tests#repo=' + key + '&view=bugs', { waitUntil: 'domcontentloaded' });
    const row = page.locator('#p-bugs table tbody tr').filter({ hasText: bug.id });
    await row.waitFor({ timeout: 15000 });
    ok(await page.locator('#p-bugs table tbody tr').count() === 2, 'the Bugs view lists both open bugs');
    const badge = async () => (await page.locator('#t-bugs .ds-badge').textContent().catch(() => '')) || '';
    ok(await badge() === '2', 'the Bugs tab counts two open bugs', await badge());
    await shot(page, 'tests-bugs');

    // The bug view: its cell with the screenshot of its failing step.
    await row.locator('a.ds-table__row-link').click();
    const card = page.locator('#bvBody .so-tests-live__card').first();
    await card.waitFor({ timeout: 15000 });
    const cardText = await card.innerText();
    ok(cardText.includes('The search finds a product') && cardText.includes('Phone A') && /fail/i.test(cardText),
      'the bug view shows its cell, its device and its verdict', cardText.replace(/\s+/g, ' '));
    const img = card.locator('img');
    ok(await img.count() === 1 && /\/files\/1\.png$/.test(await img.getAttribute('src')), 'the cell shows the screenshot of its failing step',
      await img.getAttribute('src').catch(() => ''));
    ok(/#repo=.*&view=bugs&bug=B-\d+/.test(page.url()), 'the bug view has an address of its own', page.url());

    // Triage: Known at once, the badge follows; Undo puts it back.
    await page.locator('#bvBody label.ds-radio[data-ds-key="known"]').click();
    const until = async (want, what) => {
      const end = Date.now() + 10000;
      let got = '';
      while (Date.now() < end) {
        got = (await get('/api/tests/repos/' + key + '/bugs/' + bug.id)).bug.status;
        if (got === want) break;
        await wait(150);
      }
      ok(got === want, what, got);
    };
    await until('known', 'choosing Known in the triage saves the status at once');
    await page.waitForFunction(() => (document.querySelector('#t-bugs .ds-badge') || {}).textContent === '1', null, { timeout: 10000 }).catch(() => {});
    ok(await badge() === '1', 'the Bugs tab counts one open bug then', await badge());
    const head = await page.locator('#bvHead .ds-page-head__status').innerText();
    ok(/Known/.test(head), 'the bug\'s head says Known', head.replace(/\s+/g, ' '));
    await page.locator('.ds-toast__action', { hasText: 'Undo' }).click();
    await until('open', 'Undo in the snackbar puts the status back');

    // GitHub: the fixture has no remote, and the button says so.
    const issue = page.locator('#bvIssue');
    await page.waitForFunction(() => /no remote|GitHub/.test((document.getElementById('bvIssue') || {}).dataset?.why || ''), null, { timeout: 15000 }).catch(() => {});
    ok(await issue.getAttribute('aria-disabled') === 'true' && /origin/.test(await issue.getAttribute('data-why') || ''),
      'Create GitHub issue is unavailable and says why', await issue.getAttribute('data-why'));

    // Fix in a chat: the dialog writes the message; Start chat makes the chat and opens it.
    await page.locator('#bvFix').click();
    await page.waitForFunction(() => { const f = document.getElementById('chatMessage'); return f && !f.readOnly && f.value.length > 0; }, null, { timeout: 15000 });
    const text = await page.inputValue('#chatMessage');
    ok(text.includes(bug.id) && text.includes('web/search@a') && text.includes('run_tests'),
      'the prefilled message names the bug, its cell and how to verify', text.slice(0, 120));
    await shot(page, 'tests-chat-dialog');
    await page.locator('#chatStart').click();
    await page.waitForURL((u) => u.pathname === '/' && u.hash.length > 1, { timeout: 20000 });
    const made = ((await get('/api/sessions')).sessions || []).find((x) => (x.title || '').startsWith('Fix ' + bug.id));
    chatID = made ? made.id : '';
    if (chatID) await page.waitForFunction((id) => location.hash.includes(id), chatID, { timeout: 15000 }).catch(() => {});
    ok(!!made && page.url().includes(chatID), 'Start chat makes the chat and opens it on the same tab', page.url());
    const chat = await get('/api/chats/' + chatID);
    const session = chat.session || {};
    ok(session.workdir === repo && session.options && session.options.test_tools === true,
      'the chat works in the repository, with the test tools', JSON.stringify({ workdir: session.workdir, options: session.options }));
    ok(session.title.startsWith('Fix ' + bug.id) && JSON.stringify(chat).includes(bug.id), 'the chat is named for the bug and its first message is about it', session.title);
    ok(s.errors.length === 0, 'the Bugs view and the chat dialog logged no errors', s.errors.join(' | '));
  } finally {
    if (chatID) await api.delete(s.url + '/api/sessions/' + chatID, { headers }).catch(() => {});
    if (runID) await api.delete(s.url + '/api/tests/runs/' + runID, { headers }).catch(() => {});
    await s.stop();
  }
}
