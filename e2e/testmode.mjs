// The test mode (docs/design/TEST-MODE.md) end to end: a repository with a
// catalog and a tool whose pool has two accounts, a run of four cells whose
// sessions are the fake Claude Code working each cell through the real tool
// and the test tools, the queue for the pool, the review, the analysis, and
// the Tests page showing the result down to a cell's screenshots.

import { chmodSync, copyFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { REPO, scratchDir, setup, shot, start, ok, wait } from './harness.mjs';

function write(path, text, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
  chmodSync(path, mode);
}

// catalogRepo is a repository with a catalog: one area on the devices a, b
// and c, a test of two steps on every device and one that fails on a.
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

export async function testmode() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  const api = s.context.request;
  let runID = '';
  try {
    await setup(s.page, s.url);
    const repo = catalogRepo();
    const added = await api.post(s.url + '/api/tests/repos', { data: { path: repo }, headers: { Origin: s.url } });
    ok(added.status() === 201, 'a repository with a catalog is added', String(added.status()));
    const key = (await added.json()).key;
    const catalog = await (await api.get(s.url + '/api/tests/repos/' + key + '/catalog')).json();
    ok(catalog.errors.length === 0 && catalog.cells === 4, 'its catalog reads clean, with four cells',
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
    ok(most === 2, 'two cells ran at once, one per account of the pool', String(most));
    ok(waited !== '', 'a cell waited for a free account of the pool', waited);

    const cells = (await (await api.get(s.url + '/api/tests/runs/' + runID + '/cells')).json()).cells;
    const verdicts = cells.map((c) => c.cell_id + '=' + c.final).sort().join(' ');
    ok(verdicts === 'web/checkout@a=pass web/checkout@b=pass web/checkout@c=pass web/search@a=fail',
      'every cell has the verdict its agent gave', verdicts);
    const failed = cells.find((c) => c.cell_id === 'web/search@a');
    ok(failed.review === 'done', 'the failed cell was reviewed', failed.review);
    const detail = await (await api.get(s.url + '/api/tests/runs/' + runID + '/attempts/' + failed.id)).json();
    ok(detail.steps[0].actions.length === 1 && detail.actions[0].screenshot === '1.png' && !JSON.stringify(detail).includes('device-key'),
      'its step has the tool\'s screenshot as evidence, and no secret', JSON.stringify(detail.actions[0]));
    ok(/account [12]$/.test(detail.actions[0].summary), 'the tool knew its leased account', detail.actions[0].summary);
    const bugs = await (await api.get(s.url + '/api/tests/runs/' + runID + '/bugs')).json();
    ok(bugs.bugs.length === 1 && bugs.conclusions.includes('The fake run is analysed'), 'the analyst wrote the bugs and the conclusions',
      bugs.bugs.length + ' bugs');
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
  } finally {
    // The test sessions belong to the run, not to the session list the
    // sweep deletes: deleting the run deletes them.
    if (runID) await api.delete(s.url + '/api/tests/runs/' + runID, { headers: { Origin: s.url } }).catch(() => {});
    await s.stop();
  }
}
