// A Socrates with a history of test runs, to look at and to build the Tests
// page against:
//
//   TMPDIR=/somewhere/on/disk node e2e/testsdemo.mjs [--live]
//
// It boots a server the way the suite does (harness.mjs: the real binary,
// the protocol fakes as the agents), adds a repository whose catalog has two
// areas and nine tests (a device dimension of three values as the columns, a
// rotated locale, priorities P0 to P2, tags, a tool whose pool has two
// accounts), and runs it five times, one run after another. The fake agent
// does what each test's `fake` parameter says, so the runs come out mixed:
// passes, an issue that stays, a cell that fails in odd runs only, a blocked
// cell, a failure fixed in run 3 and an issue fixed in run 4, and a
// regression in run 5; the fake analyst keeps one bug per failing test over
// the runs. With --live a sixth run is started and left running: its
// web/browse cells take about 30 s a step, so the live view has a while to
// show them.
//
// Then it prints the server's address, the password, the repository's key
// and the runs, and leaves the server running until Ctrl+C, which stops it
// and removes everything it made.

import { chmodSync, copyFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { REPO, PASSWORD, scratchDir, setup, start, wait } from './harness.mjs';

const live = process.argv.includes('--live');

function write(path, text, mode = 0o644) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
  chmodSync(path, mode);
}

// test is a test file: its front matter and its steps.
function test(front, steps, context = '') {
  const lines = steps.map(([action, expect], i) => `${i + 1}. ${action}\n   Expect: ${expect}`);
  return `---\n${front.trim()}\n---\n${context ? context + '\n\n' : ''}## Steps\n\n${lines.join('\n')}\n`;
}

const AREA_DIMS = `columns: device
dimensions:
  device:
    values: {phone: iPhone 15 · Safari, tablet: iPad Air · Safari, desktop: Windows · Chrome}
    default: all
  locale:
    values: {en: English, de: German, fr: French}
    default: rotate`;

// demoRepo is the demo shop's repository: a git checkout with a catalog.
function demoRepo() {
  const repo = scratchDir('socrates-demo-shop-');
  const base = join(repo, '.socrates', 'tests');
  write(join(repo, 'README.md'), '# The demo shop\n\nA shop that exists to be tested.\n');
  write(join(base, 'TESTS.md'), `---
title: The demo shop
---
Test the demo shop on its staging site. Never place a real order; the test card is 4242 4242 4242 4242.
`);
  write(join(base, 'README.md'), 'The demo catalog of e2e/testsdemo.mjs.\n');
  write(join(base, 'web', 'AREA.md'), `---
title: The web shop
tools: [fakedevice]
${AREA_DIMS}
evidence: screenshot
---
Work each cell on its device, in its locale. A banner of the staging site is not a finding.
`);
  write(join(base, 'account', 'AREA.md'), `---
title: Accounts and sign-in
tools: [fakedevice]
${AREA_DIMS}
evidence: screenshot
---
Sign in with the fixture customer. A mail arrives within a minute.
`);
  write(join(base, 'web', 'checkout.md'), test(`
title: The checkout takes an order
priority: P0
tags: [smoke, payments]`, [
    ['Open the cart with one lamp in it.', 'the cart with the lamp and its price.'],
    ['Pay with the test card.', 'the order confirmation with an order number.'],
  ], 'Signed in as the fixture customer, with a lamp in the cart.'));
  write(join(base, 'web', 'search.md'), test(`
title: The search finds a product
priority: P1
tags: [smoke]
device: [phone, desktop]
params: {fake: failfrom5}`, [
    ['Search for "lamp" from the header.', 'a list of lamps, the best match first.'],
    ['Open the first result.', 'the lamp\'s product page.'],
  ]));
  write(join(base, 'web', 'wishlist.md'), test(`
title: The wish list keeps a product
priority: P1
device: [tablet]
params: {fake: flaky}`, [
    ['Add the lamp to the wish list.', 'the heart fills and the wish list counts one.'],
    ['Open the wish list.', 'the wish list with the lamp.'],
  ]));
  write(join(base, 'web', 'product-page.md'), test(`
title: The product page shows the photos and the price
priority: P2
tags: [visual]
params: {fake: issue}`, [
    ['Open the lamp\'s product page.', 'the photos, the price and Add to cart.'],
    ['Swipe through the photos.', 'every photo, none cut off.'],
  ]));
  write(join(base, 'web', 'cart.md'), test(`
title: The cart updates its total
priority: P1
tags: [payments]
device: [phone, desktop]
params: {fake: failuntil3}`, [
    ['Raise the lamp\'s quantity to two.', 'the total doubles.'],
    ['Remove the lamp.', 'the empty cart.'],
  ]));
  write(join(base, 'web', 'browse.md'), test(`
title: The categories lead to their products
priority: P2
tags: [live]
params: {fake: crawlfrom6}`, [
    ['Open Lighting from the menu.', 'the lighting category with its products.'],
    ['Filter by price, lowest first.', 'the cheapest product first.'],
    ['Open the second product.', 'its product page.'],
  ]));
  write(join(base, 'account', 'sign-in.md'), test(`
title: Signing in with a password
priority: P0
tags: [smoke, auth]`, [
    ['Open Sign in from the header.', 'the sign-in form.'],
    ['Sign in as the fixture customer.', 'the account page with the customer\'s name.'],
  ]));
  write(join(base, 'account', 'password-reset.md'), test(`
title: A forgotten password is reset by mail
priority: P1
tags: [auth]
device: [phone]
params: {fake: blocked}`, [
    ['Ask for a reset link on the sign-in form.', 'a message that a mail was sent.'],
    ['Open the link in the mail.', 'the form for a new password.'],
  ]));
  write(join(base, 'account', 'profile.md'), test(`
title: The profile keeps a new address
priority: P2
device: [desktop, tablet]
params: {fake: issueuntil4}`, [
    ['Open the profile.', 'the profile with the saved address.'],
    ['Change the street and save.', 'a confirmation and the new street.'],
  ]));

  // The tool: a pool of two accounts, each carrying two cells at once; a
  // screenshot is one of the README's pictures, the phone's on a phone.
  const tool = join(base, 'tools', 'fakedevice');
  write(join(tool, 'TOOL.md'), `---
title: Demo devices
secrets:
  command: printf 'DEVICE_ACCOUNTS=alice:alice-device-key,bob:bob-device-key\\n'
  names: [DEVICE_ACCOUNTS]
pool:
  secret: DEVICE_ACCOUNTS
  per_member: 2
end: bin/stop
---
\`shot <n>\` takes a screenshot of the device.
`);
  mkdirSync(join(tool, 'bin', 'shots'), { recursive: true });
  const pictures = ['home', 'session', 'tests', 'admin'];
  pictures.forEach((name, i) => copyFileSync(join(REPO, 'docs', `screenshot-${name}.png`), join(tool, 'bin', 'shots', `wide-${i}.png`)));
  copyFileSync(join(REPO, 'docs', 'screenshot-phone.png'), join(tool, 'bin', 'shots', 'phone-0.png'));
  write(join(tool, 'bin', 'shot'), `#!/bin/sh
# A screenshot of the demo device: a picture, and its line of evidence.
set -e
here=$(dirname "$0")
case "$SOCRATES_TEST_DIM_DEVICE" in
phone) pic="$here/shots/phone-0.png" ;;
*) pic="$here/shots/wide-$(( ($1 - 1) % ${pictures.length} )).png" ;;
esac
sleep 0.2
cp "$pic" "$SOCRATES_TEST_EVIDENCE/$1.png"
printf '{"at":"%s","tool":"fakedevice","action":"shot","summary":"step %s on %s in %s","url":"https://staging.demo-shop.example/%s/","screenshot":"%s.png","interaction":true}\\n' \\
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$SOCRATES_TEST_DIM_DEVICE" "$SOCRATES_TEST_DIM_LOCALE" "$SOCRATES_TEST_DIM_LOCALE" "$1" \\
  >> "$SOCRATES_TEST_EVIDENCE/actions.jsonl"
echo "took screenshot $1"
`, 0o755);
  write(join(tool, 'bin', 'stop'), '#!/bin/sh\necho stopped > "$SOCRATES_TEST_EVIDENCE/stopped.txt"\n', 0o755);

  const git = (...args) => execFileSync('git', ['-C', repo, '-c', 'user.name=Demo', '-c', 'user.email=demo@example.com', ...args],
    { stdio: 'ignore' });
  git('init', '-q');
  git('remote', 'add', 'origin', 'https://github.com/example/demo-shop.git');
  git('add', '-A');
  git('commit', '-qm', 'The demo shop and its test catalog');
  return repo;
}

async function main() {
  const began = Date.now();
  const s = await start({ viewport: { width: 1280, height: 900 } });
  await setup(s.page, s.url);
  // The page is somebody else's: the browser goes, the server stays, and
  // the API is called with the browser's session cookie.
  const cookie = (await s.context.cookies()).map((c) => c.name + '=' + c.value).join('; ');
  await s.context.close().catch(() => {});
  await s.browser.close().catch(() => {});
  const call = async (method, path, data) => {
    const res = await fetch(s.url + path, {
      method, headers: { Cookie: cookie, Origin: s.url, 'Content-Type': 'application/json' },
      body: data === undefined ? undefined : JSON.stringify(data),
    });
    const text = await res.text();
    return { status: () => res.status, text: async () => text, json: async () => JSON.parse(text) };
  };
  const api = {
    get: (url) => call('GET', url.slice(s.url.length)),
    post: (url, { data } = {}) => call('POST', url.slice(s.url.length), data || {}),
  };
  const headers = {};
  const repo = demoRepo();
  const added = await api.post(s.url + '/api/tests/repos', { data: { path: repo }, headers });
  if (added.status() !== 201) throw new Error('the repository was not added: ' + await added.text());
  const key = (await added.json()).key;
  const catalog = await (await api.get(s.url + '/api/tests/repos/' + key + '/catalog')).json();
  if (catalog.errors.length) throw new Error('the catalog has errors: ' + JSON.stringify(catalog.errors));
  console.log(`catalog: ${catalog.cells} cells in ${catalog.areas.length} areas`);

  const names = { 3: 'after the cart fix', 5: 'release candidate' };
  const runs = [];
  for (let n = 1; n <= 5; n += 1) {
    const started = await api.post(s.url + '/api/tests/repos/' + key + '/runs', { data: { name: names[n] || '' }, headers });
    if (started.status() !== 201) throw new Error('run ' + n + ' did not start: ' + await started.text());
    const id = (await started.json()).id;
    const deadline = Date.now() + 240000;
    let run = {};
    while (Date.now() < deadline) {
      run = await (await api.get(s.url + '/api/tests/runs/' + id)).json();
      if (['done', 'failed', 'stopped'].includes(run.state)) break;
      await wait(300);
    }
    if (run.state !== 'done') throw new Error(`run ${n} ended ${run.state}: ${run.error || 'it took too long'}`);
    const v = run.counts.verdicts;
    console.log(`run ${run.number}: ${run.counts.cells} cells, ${v.pass || 0} pass, ${v.issue || 0} issue, ${v.fail || 0} fail, `
      + `${v.blocked || 0} blocked; ${run.changes.regressed} regressed, ${run.changes.fixed} fixed (${id})`);
    runs.push(run);
  }
  if (live) {
    const started = await api.post(s.url + '/api/tests/repos/' + key + '/runs', { data: { name: 'live' }, headers });
    const run = await started.json();
    runs.push(run);
    console.log(`run ${run.number}: started and left running (${run.id})`);
  }

  const bugs = (await (await api.get(s.url + '/api/tests/repos/' + key + '/bugs')).json()).bugs;
  console.log('');
  console.log('Socrates is running with a demo history of test runs (' + Math.round((Date.now() - began) / 1000) + ' s).');
  console.log('');
  console.log('  URL:        ' + s.url + '/tests#repo=' + key);
  console.log('  Password:   ' + PASSWORD + '   (sign in at ' + s.url + '/login)');
  console.log('  Repository: ' + key + '   (' + repo + ')');
  console.log('  Runs:       ' + runs.map((r) => r.number + '=' + r.id).join('  '));
  console.log('  Bugs:       ' + bugs.map((b) => b.id + ' ' + b.status + ' ' + b.severity).join(', '));
  console.log('  Data:       ' + s.data);
  console.log('');
  console.log('Ctrl+C stops the server and removes everything it made.');
  // The harness stops the server and sweeps its folders on SIGINT.
  setInterval(() => {}, 1 << 30);
}

main().catch((err) => {
  console.error('testsdemo: ' + (err && err.stack || err));
  process.exit(1);
});
