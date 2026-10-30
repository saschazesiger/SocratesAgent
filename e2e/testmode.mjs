// The test mode (docs/design/TEST-MODE.md) end to end: a repository with a
// catalog and a tool whose pool has two accounts, a run of five cells whose
// sessions are the fake Claude Code working each cell through the real tool
// and the test tools, the queue for the pool, the review, the analysis, and
// the Tests page showing the result down to a cell's screenshots. A second
// run of three cells is set against the first: its changes, a bug that keeps
// its id and one that is fixed, an override, the report and the estimate.

import { chmodSync, copyFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { REPO, scratchDir, setup, shot, start, ok, wait } from './harness.mjs';

function write(path, text, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
  chmodSync(path, mode);
}

// catalogRepo is a repository with a catalog: one area on the devices a, b
// and c, a test of two steps on every device, one that fails on a and one
// on b that fails in odd runs only.
export function catalogRepo() {
  const repo = scratchDir('socrates-catalog-');
  mkdirSync(join(repo, '.git'));
  const base = join(repo, '.socrates', 'tests');
  write(join(base, 'TESTS.md'), '---\ntitle: The fixture shop\n---\nTest the fixture shop. Never touch production.\n');
  write(join(base, 'web', 'AREA.md'), `---
title: The web shop
tools: [fakedevice]
columns: device
dimensions:
  device:
    values: {a: Phone A, b: Tablet B, c: Desktop C}
---
Work each cell on its device.
`);
  write(join(base, 'web', 'checkout.md'), `---
title: The checkout takes an order
priority: P0
device: all
tags: [smoke]
---
Signed in as the fixture customer.

## Steps

1. Open the cart.
   Expect: the cart with one item.
2. Pay with the test card.
   Expect: the order confirmation.
`);
  write(join(base, 'web', 'search.md'), `---
title: The search finds a product
priority: P1
device: [a]
params: {fake: fail}
---
## Steps

1. Search for "lamp".
   Expect: a list of lamps.
`);
  write(join(base, 'web', 'flaky.md'), `---
title: The wish list keeps a product
priority: P1
device: [b]
params: {fake: flaky}
---
## Steps

1. Add the lamp to the wish list.
   Expect: the wish list with the lamp.
`);
  const tool = join(base, 'tools', 'fakedevice');
  write(join(tool, 'TOOL.md'), `---
title: Fake devices
secrets:
  command: printf 'DEVICE_ACCOUNTS=alice:alice-device-key,bob:bob-device-key\\n'
  names: [DEVICE_ACCOUNTS]
pool:
  secret: DEVICE_ACCOUNTS
end: bin/stop
---
\`shot <n>\` takes a screenshot of the device.
`);
  mkdirSync(join(tool, 'bin'), { recursive: true });
  copyFileSync(join(REPO, 'internal', 'web', 'static', 'img', 'logo.png'), join(tool, 'bin', 'shot.png'));
  write(join(tool, 'bin', 'shot'), `#!/bin/sh
# A screenshot of the fake device: the picture, and its line of evidence.
set -e
lease=$(sed -n 's/^SOCRATES_LEASE_FAKEDEVICE=//p' "$SOCRATES_TEST_VAULT")
sleep 1
cp "$(dirname "$0")/shot.png" "$SOCRATES_TEST_EVIDENCE/$1.png"
printf '{"at":"2026-10-29T10:00:00Z","tool":"fakedevice","action":"shot","summary":"screenshot %s on %s with account %s","screenshot":"%s.png","interaction":true}\\n' \\
  "$1" "$SOCRATES_TEST_DIM_DEVICE" "$lease" "$1" >> "$SOCRATES_TEST_EVIDENCE/actions.jsonl"
echo "took screenshot $1"
`, 0o755);
  write(join(tool, 'bin', 'stop'), '#!/bin/sh\necho stopped > "$SOCRATES_TEST_EVIDENCE/stopped.txt"\n', 0o755);
  return repo;
}

// runToEnd waits for a run to be over and answers its state.
async function runToEnd(api, url, id, ms = 120000) {
  const deadline = Date.now() + ms;
  let state = '';
  while (Date.now() < deadline) {
    state = (await (await api.get(url + '/api/tests/runs/' + id)).json()).state;
    if (['done', 'failed', 'stopped'].includes(state)) break;
    await wait(200);
  }
  if (state !== 'done') {
    const now = (await (await api.get(url + '/api/tests/runs/' + id + '/cells')).json()).cells || [];
    console.log('  cells: ' + now.map((c) => c.cell_id + ' ' + c.state + ' ' + (c.wait || c.end_reason || '')).join(' | '));
  }
  return state;
}

export async function testmode() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  const api = s.context.request;
  const post = (path, data) => api.post(s.url + path, { data, headers: { Origin: s.url } });
  const get = async (path) => (await api.get(s.url + path)).json();
  let runID = '';
  let secondID = '';
  try {
    await setup(s.page, s.url);
    const repo = catalogRepo();
    const added = await api.post(s.url + '/api/tests/repos', { data: { path: repo }, headers: { Origin: s.url } });
    ok(added.status() === 201, 'a repository with a catalog is added', String(added.status()));
    const key = (await added.json()).key;
    const catalog = await (await api.get(s.url + '/api/tests/repos/' + key + '/catalog')).json();
    ok(catalog.errors.length === 0 && catalog.cells === 5, 'its catalog reads clean, with five cells',
      catalog.errors.length + ' errors, ' + catalog.cells + ' cells');
    const check = await (await api.post(s.url + '/api/tests/repos/' + key + '/tools/fakedevice/check', { headers: { Origin: s.url } })).json();
    ok(check.found.includes('DEVICE_ACCOUNTS') && check.members.join(',') === 'alice,bob' && !JSON.stringify(check).includes('device-key'),
      'checking the secrets names them and the members, never a value', JSON.stringify(check));

    const started = await api.post(s.url + '/api/tests/repos/' + key + '/runs', { data: { name: 'e2e' }, headers: { Origin: s.url } });
    ok(started.status() === 201, 'a run starts', String(started.status()));
    runID = (await started.json()).id;

    // The cells queue for the pool: never more than its two members at once.
    let most = 0;
    let waited = '';
    let state = '';
    const deadline = Date.now() + 120000;
    while (Date.now() < deadline) {
      const run = await (await api.get(s.url + '/api/tests/runs/' + runID)).json();
      state = run.state;
      const cells = (await (await api.get(s.url + '/api/tests/runs/' + runID + '/cells')).json()).cells || [];
      most = Math.max(most, cells.filter((c) => c.state === 'running' || c.state === 'starting').length);
      for (const c of cells) if (c.wait && c.wait.includes('Fake devices account')) waited = c.wait;
      if (['done', 'failed', 'stopped'].includes(state)) break;
      await wait(150);
    }
    if (state !== 'done') {
      const now = (await (await api.get(s.url + '/api/tests/runs/' + runID + '/cells')).json()).cells || [];
      console.log('  cells: ' + now.map((c) => c.cell_id + ' ' + c.state + ' ' + (c.wait || c.end_reason || '')).join(' | '));
    }
    ok(state === 'done', 'the run ends done', state);
    const first = await get('/api/tests/runs/' + runID);
    ok(first.number === 1, 'the repository\'s first run is run 1', String(first.number));
    ok(most === 2, 'two cells ran at once, one per account of the pool', String(most));
    ok(waited !== '', 'a cell waited for a free account of the pool', waited);

    const cells = (await (await api.get(s.url + '/api/tests/runs/' + runID + '/cells')).json()).cells;
    const verdicts = cells.map((c) => c.cell_id + '=' + c.final).sort().join(' ');
    ok(verdicts === 'web/checkout@a=pass web/checkout@b=pass web/checkout@c=pass web/flaky@b=fail web/search@a=fail',
      'every cell has the verdict its agent gave', verdicts);
    const failed = cells.find((c) => c.cell_id === 'web/search@a');
    ok(failed.review === 'done', 'the failed cell was reviewed', failed.review);
    const detail = await (await api.get(s.url + '/api/tests/runs/' + runID + '/attempts/' + failed.id)).json();
    ok(detail.steps[0].actions.length === 1 && detail.actions[0].screenshot === '1.png' && !JSON.stringify(detail).includes('device-key'),
      'its step has the tool\'s screenshot as evidence, and no secret', JSON.stringify(detail.actions[0]));
    ok(/account [12]$/.test(detail.actions[0].summary), 'the tool knew its leased account', detail.actions[0].summary);
    const bugs = await (await api.get(s.url + '/api/tests/runs/' + runID + '/bugs')).json();
    ok(bugs.bugs.length === 2 && bugs.bugs.every((b) => /^B-\d+$/.test(b.id)) && bugs.conclusions.includes('The fake run is analysed'),
      'the analyst wrote the bugs, with ids Socrates gave, and the conclusions', bugs.bugs.map((b) => b.id).join(','));
    const searchBug = bugs.bugs.find((b) => b.cells.includes('web/search@a'));
    const flakyBug = bugs.bugs.find((b) => b.cells.includes('web/flaky@b'));
    const sessions = (await (await api.get(s.url + '/api/sessions')).json()).sessions;
    ok(sessions.length === 0, 'the test sessions are not in the session list', String(sessions.length));

    // The page: the table, and a cell with its screenshot.
    await s.page.goto(s.url + '/tests#repo=' + key + '&run=' + runID, { waitUntil: 'domcontentloaded' });
    const table = s.page.locator('table').filter({ hasText: 'The checkout takes an order' }).first();
    await table.waitFor({ timeout: 15000 });
    const rowText = await table.innerText();
    ok(rowText.includes('Phone A') && rowText.includes('Desktop C') && /fail/i.test(rowText), 'the results table shows the devices and the failure',
      rowText.replace(/\s+/g, ' ').slice(0, 160));
    await shot(s.page, 'tests-results');
    await s.page.goto(s.url + '/tests#repo=' + key + '&run=' + runID + '&cell=' + failed.id, { waitUntil: 'domcontentloaded' });
    const img = s.page.locator('img[src*="/files/1.png"]').first();
    await img.waitFor({ state: 'attached', timeout: 15000 });
    // A thumbnail loads lazily: once it is on screen.
    await img.scrollIntoViewIfNeeded().catch(() => {});
    const loaded = await img.evaluate((el) => new Promise((resolve) => {
      if (el.complete && el.naturalWidth) resolve(el.naturalWidth);
      else {
        el.addEventListener('load', () => resolve(el.naturalWidth), { once: true });
        setTimeout(() => resolve(el.naturalWidth), 10000);
      }
    }));
    ok(loaded > 0, 'the cell shows its step\'s screenshot', String(loaded));
    await shot(s.page, 'tests-cell');
    ok(s.errors.length === 0, 'the Tests page logged no errors', s.errors.join(' | '));

    // A second run of three cells, set against the first.
    const estimate = await (await post('/api/tests/repos/' + key + '/estimate', { selection: {} })).json();
    ok(estimate.cells === 5 && estimate.tests === 3 && estimate.eta_ms > 0 && estimate.errors.length === 0,
      'the estimate of everything counts its cells and tests and how long they take', JSON.stringify(estimate));
    const nothing = await (await post('/api/tests/repos/' + key + '/estimate', { selection: { tests: ['web/none'] } })).json();
    ok(nothing.cells === 0 && nothing.errors.length === 1, 'the estimate of a selection of nothing says why', JSON.stringify(nothing));
    const again = await post('/api/tests/repos/' + key + '/runs',
      { name: 'second', selection: { cells: ['web/search@a', 'web/flaky@b', 'web/checkout@a'] } });
    ok(again.status() === 201, 'a run of three cells starts', String(again.status()));
    secondID = (await again.json()).id;
    ok(await runToEnd(api, s.url, secondID) === 'done', 'the second run ends done');
    const second = await get('/api/tests/runs/' + secondID);
    ok(second.number === 2 && second.counts.cells === 3 && second.changes.fixed === 1 && second.changes.regressed === 0,
      'the second run is run 2, with one cell fixed against run 1', JSON.stringify({ number: second.number, changes: second.changes }));
    const rows = (await get('/api/tests/runs/' + secondID + '/cells')).cells;
    const row = (id) => rows.find((c) => c.cell_id === id);
    ok(row('web/flaky@b').final === 'pass' && row('web/flaky@b').change === 'fixed' && row('web/flaky@b').prev.verdict === 'fail' &&
      row('web/flaky@b').prev.number === 1, 'the flaky cell passes in an even run, fixed against its fail in run 1',
    JSON.stringify(row('web/flaky@b').prev));
    ok(!row('web/search@a').change && row('web/search@a').prev.verdict === 'fail' && row('web/search@a').shot === '1.png',
      'the failing cell fails as before, its failing step\'s screenshot its picture', JSON.stringify(row('web/search@a')));
    const repoBugs = (await get('/api/tests/repos/' + key + '/bugs')).bugs;
    const search = repoBugs.find((b) => b.id === searchBug.id);
    const flaky = repoBugs.find((b) => b.id === flakyBug.id);
    ok(repoBugs.length === 2 && search.status === 'open' && search.runs.length === 2 && search.last_number === 2,
      'the failing cell\'s bug kept its id over both runs', JSON.stringify(search && { id: search.id, runs: search.runs }));
    ok(flaky.status === 'fixed' && flaky.fixed_in === secondID && flaky.fixed_number === 2,
      'the flaky cell\'s bug is fixed by the run its cell passes in', JSON.stringify(flaky && { status: flaky.status, fixed_in: flaky.fixed_in }));
    const overview = await get('/api/tests/repos/' + key + '/overview');
    ok(overview.latest.id === secondID && overview.previous.id === runID && overview.trend.length === 2 &&
      overview.fixed.length === 1 && overview.failing.length === 1 && overview.failing[0].since.number === 1,
    'the overview has both runs, the fixed cell and the one failing since run 1',
    JSON.stringify({ fixed: overview.fixed.length, failing: overview.failing.map((f) => f.since) }));
    const repos = (await get('/api/tests/repos')).repos;
    ok(repos.find((r) => r.key === key).open_bugs === 1, 'the repository counts its open bug', JSON.stringify(repos.map((r) => r.open_bugs)));

    // The cell viewer: a failed cell of run 2 opened from its run, its
    // step's screenshot large, an override from the page and its removal.
    const failing = row('web/search@a');
    await s.page.goto(s.url + '/tests#repo=' + key + '&view=runs&run=' + secondID, { waitUntil: 'domcontentloaded' });
    const cellButton = s.page.locator('[data-cell="' + failing.id + '"]').first();
    await cellButton.waitFor({ timeout: 15000 });
    await cellButton.click();
    const viewer = s.page.locator('#cellDialog');
    const large = viewer.locator('.so-cell-shot__img').first();
    await large.waitFor({ timeout: 15000 });
    const largeWidth = await large.evaluate((el) => new Promise((resolve) => {
      if (el.complete && el.naturalWidth) resolve(el.naturalWidth);
      else {
        el.addEventListener('load', () => resolve(el.naturalWidth), { once: true });
        setTimeout(() => resolve(el.naturalWidth), 10000);
      }
    }));
    const title = await viewer.locator('#cellTitle').innerText();
    ok(largeWidth > 0 && title === 'The search finds a product' && s.page.url().includes('cell=' + failing.id),
      'the cell viewer opens the failed cell with its step\'s screenshot large', title + ' ' + largeWidth);
    const said = async (text, present) => s.page.waitForFunction(([t, p]) => {
      const who = document.querySelector('#cellDialog .so-cell__who');
      return !!who && who.textContent.includes(t) === p;
    }, [text, present], { timeout: 10000 }).then(() => true).catch(() => false);
    await viewer.locator('[data-cell-act="override"]').click();
    await s.page.locator('#overrideDialog input[value="pass"]').check();
    await s.page.fill('#overrideNote', 'Seen by hand: it works.');
    await s.page.click('#overrideSave');
    const madePass = await said('you made it Pass', true);
    const byPage = await get('/api/tests/runs/' + secondID);
    ok(madePass && byPage.counts.verdicts.pass === 3 && !byPage.counts.verdicts.fail,
      'an override from the cell viewer says who made it pass, and the run counts it', JSON.stringify(byPage.counts.verdicts));
    await shot(s.page, 'tests-cell-override');
    await viewer.locator('[data-cell-act="override"]').click();
    await s.page.click('#overrideRemove');
    const removed = await said('you made it', false);
    const backAgain = await get('/api/tests/runs/' + secondID);
    ok(removed && backAgain.counts.verdicts.fail === 1, 'removing it puts the agent\'s fail back', JSON.stringify(backAgain.counts.verdicts));
    await s.page.keyboard.press('Escape');
    const closed = await s.page.waitForFunction(() => !location.hash.includes('cell='), null, { timeout: 10000 }).then(() => true).catch(() => false);
    const gone = await viewer.waitFor({ state: 'hidden', timeout: 5000 }).then(() => true).catch(() => false);
    ok(closed && gone, 'Escape closes the cell viewer and its place in the address', s.page.url());

    // The test view: its history over both runs, a verdict opening its attempt.
    await s.page.goto(s.url + '/tests#repo=' + key + '&view=tests&test=web/search', { waitUntil: 'domcontentloaded' });
    await s.page.locator('.so-tests-hist__table').waitFor({ timeout: 15000 });
    const runHeads = await s.page.locator('.so-tests-hist__run').allInnerTexts();
    const marks = s.page.locator('.so-tests-hist [data-hist]');
    ok(runHeads.join(',') === 'Run 2,Run 1' && await marks.count() === 2, 'the test view\'s history has a column per run, the newest first',
      runHeads.join(',') + ' · ' + await marks.count() + ' verdicts');
    await shot(s.page, 'tests-test');
    await marks.last().click();
    await viewer.locator('.so-cell__band').waitFor({ timeout: 15000 });
    ok(s.page.url().includes('run=' + runID), 'a verdict of the history opens that run\'s attempt', s.page.url());
    await s.page.keyboard.press('Escape');
    await s.page.goto(s.url + '/tests#repo=' + key + '&view=tests', { waitUntil: 'domcontentloaded' });
    const list = s.page.locator('.so-tests-area').first();
    await list.waitFor({ timeout: 15000 });
    const listText = (await list.innerText()).replace(/\s+/g, ' ');
    ok(listText.includes('The search finds a product') && /Fail/.test(listText) && listText.includes('failing since Run 1'),
      'the Tests view shows each test with its latest verdict and since when it fails', listText.slice(0, 200));
    // Change for this run opens the settings and leaves focus on its button:
    // focus on the first select opened its picker by itself on iOS (B-36).
    await s.page.click('#runBtn');
    await s.page.locator('#runChange').waitFor({ timeout: 15000 });
    await s.page.click('#runChange');
    await s.page.locator('#runSettings select').first().waitFor({ timeout: 5000 });
    const focused = await s.page.evaluate(() => ({ id: document.activeElement && document.activeElement.id,
      tag: document.activeElement && document.activeElement.tagName, text: document.getElementById('runChange').textContent }));
    ok(focused.id === 'runChange' && focused.text === 'Use the defaults',
      'Change for this run shows the settings and keeps focus on its button, not on a select', JSON.stringify(focused));
    await s.page.keyboard.press('Escape');
    await s.page.locator('#runSheet').waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {});
    ok(s.errors.length === 0, 'the cell viewer and the Tests view logged no errors', s.errors.join(' | '));

    // An override changes what counts.
    const over = await post('/api/tests/runs/' + secondID + '/attempts/' + row('web/search@a').id + '/override',
      { verdict: 'pass', note: 'The error is the fixture\'s.' });
    const overridden = await get('/api/tests/runs/' + secondID);
    ok(over.status() === 200 && overridden.counts.verdicts.pass === 3 && !overridden.counts.verdicts.fail && overridden.changes.fixed === 2,
      'an override to pass makes the run all pass', JSON.stringify(overridden.counts.verdicts));
    const bad = await post('/api/tests/runs/' + secondID + '/attempts/' + row('web/search@a').id + '/override', { verdict: 'great' });
    ok(bad.status() === 400, 'an override must be a verdict', String(bad.status()));

    // The report, to read and to download.
    const report = await api.get(s.url + '/api/tests/runs/' + secondID + '/report.md');
    const md = await report.text();
    ok(report.headers()['content-type'].startsWith('text/markdown') && md.includes('· Run 2: second') && md.includes('3 cells: 3 pass') &&
      md.includes('## Every cell') && !md.includes('device-key'), 'the report has the run in Markdown, without a secret', md.slice(0, 120));
    const download = await api.get(s.url + '/api/tests/runs/' + secondID + '/report.md?download=1');
    ok(/attachment; filename=".+-run-2\.md"/.test(download.headers()['content-disposition'] || ''), 'the report downloads as a file',
      download.headers()['content-disposition']);
    const patched = await api.patch(s.url + '/api/tests/repos/' + key + '/bugs/' + searchBug.id,
      { data: { status: 'known', note: 'We know.' }, headers: { Origin: s.url } });
    ok(patched.status() === 200 && (await patched.json()).bug.status === 'known', 'a bug is triaged by hand', String(patched.status()));
  } finally {
    // The test sessions belong to the run, not to the session list the
    // sweep deletes: deleting the run deletes them.
    for (const id of [runID, secondID]) {
      if (id) await api.delete(s.url + '/api/tests/runs/' + id, { headers: { Origin: s.url } }).catch(() => {});
    }
    await s.stop();
  }
}
