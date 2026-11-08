// artifacts: a chat's artifacts on its page (docs/design/ARTIFACTS.md, "The
// chat page"). The fake Claude Code makes, edits and deletes artifacts
// through the real socrates-artifacts server (/mcpcall, one tool call per
// line): a Markdown page with a Mermaid diagram and an HTML page with an
// attached picture. The page follows live - the button and its badge of
// what this device has not shown, the column that opens by itself on a new
// artifact and turns into its page, the sandboxed frame, the version select
// and the frame reloaded on a new version, Open in a new tab, Delete - the
// Tests and Artifacts columns never open together, and on a phone the list
// is a side sheet, a page the whole screen and a new one offered by a
// snackbar. Mermaid comes from a CDN: the scenario keeps it away (the page
// shows the diagram's source then) and asserts only that the page loaded.

import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { start, setup, ok, wait, shot } from './harness.mjs';
import { png, signIn, tell, openChat, still } from './chattests.mjs';

const call = (tool, args) => '/mcpcall socrates-artifacts ' + tool + ' ' + JSON.stringify(args);

const MARKDOWN = [
  '# How a request flows',
  '',
  'The browser asks, the **server** answers.',
  '',
  '```mermaid',
  'graph LR',
  '  A[Browser] --> B[Server] --> C[(Database)]',
  '```',
  '',
  '| Part | Does |',
  '|---|---|',
  '| Server | Routes the request |',
].join('\n');

const HTML = '<h1 id="head">Sales by quarter</h1><p class="lead" id="lead">Up every quarter.</p>'
  + '<figure><img id="chart" src="chart.png" alt="The chart"><figcaption>Revenue</figcaption></figure>';

/** artChat makes a chat with Socrates Artifacts (and Tests) on, over the API. */
async function artChat(s, title) {
  const res = await s.context.request.post(s.url + '/api/chats', {
    data: {
      client_id: 'e2e-' + Math.random().toString(36).slice(2), harness: 'claude', workdir_mode: 'dynamic', title,
      mcp: { 'socrates-artifacts': true, 'socrates-tests': true },
    },
  });
  if (res.status() !== 201) throw new Error('could not make the chat: ' + res.status() + ' ' + await res.text());
  return (await res.json()).session.id;
}

/** artifactsOf is the chat's artifacts as the API has them. */
async function artifactsOf(s, id) {
  return (await (await s.context.request.get(s.url + '/api/chats/' + encodeURIComponent(id) + '/artifacts')).json());
}

async function untilArtifacts(s, id, test, timeout = 40000) {
  const end = Date.now() + timeout;
  let data = { artifacts: [] };
  while (Date.now() < end) {
    data = await artifactsOf(s, id);
    if (test(data.artifacts || [])) return data.artifacts;
    await wait(250);
  }
  return data.artifacts || [];
}

const button = (page) => page.evaluate(() => {
  const b = document.getElementById('artifactsBtn');
  const badge = b.querySelector('.ds-badge');
  return {
    shown: !b.hidden && b.offsetWidth > 0,
    label: b.getAttribute('aria-label'),
    badge: badge && !badge.hidden ? badge.textContent : '',
    expanded: b.getAttribute('aria-expanded'),
  };
});

const rows = (page, host) => page.$$eval(host + ' .so-artifact', (nodes) => nodes.map((n) => ({
  id: n.dataset.artifact,
  title: n.querySelector('.ds-list__action').textContent.trim(),
  desc: (n.querySelector('.so-artifact__desc') || {}).textContent || '',
  meta: (n.querySelector('.so-artifact__meta') || {}).textContent || '',
  tag: (n.querySelector('.so-artifact__tag') || {}).textContent || '',
})));

// framed is the frame of the page shown in a host, once its document has
// the element wanted.
async function framed(page, host, selector, timeout = 15000) {
  const handle = await page.waitForSelector(host + ' iframe.so-artpage__iframe', { timeout });
  const frame = await handle.contentFrame();
  await frame.waitForSelector(selector, { timeout });
  return frame;
}

const frameOf = (page, host) => page.$eval(host + ' iframe.so-artpage__iframe', (f) => ({
  src: f.getAttribute('src'), version: f.dataset.version, sandbox: f.getAttribute('sandbox'), title: f.title,
}));

// sideways is whether anything on the page is wider than the window.
const sideways = (page) => page.evaluate(() => {
  const doc = document.scrollingElement || document.documentElement;
  return doc.scrollWidth > innerWidth + 1;
});

export async function artifacts() {
  const s = await start({ viewport: { width: 1280, height: 860 } });
  const { page } = s;
  // Mermaid comes from jsDelivr; this machine may be offline, and the page
  // must load whether it can be fetched or not: it never is here.
  await s.context.route('https://cdn.jsdelivr.net/**', (route) => route.abort());
  try {
    await setup(page, s.url);
    await signIn(s);
    const dir = join(s.data, 'artifact-files');
    mkdirSync(dir, { recursive: true });
    const chart = join(dir, 'chart.png');
    writeFileSync(chart, png(240, 120, (x, y) => (y > 120 - x / 2 ? [26, 115, 232] : [255, 255, 255])));

    // A chat without the server and without artifacts has no button.
    const plain = await s.context.request.post(s.url + '/api/chats', {
      data: { client_id: 'e2e-plain-art', harness: 'claude', workdir_mode: 'dynamic', title: 'No artifacts here' },
    });
    const plainId = (await plain.json()).session.id;
    const id = await artChat(s, 'Architecture explained');
    await page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
    await openChat(page, plainId);
    await wait(1200);
    ok(!(await button(page)).shown, 'a chat without Socrates Artifacts and without artifacts has no Artifacts button', JSON.stringify(await button(page)));

    await openChat(page, id);
    const shown = await page.waitForFunction(() => !document.getElementById('artifactsBtn').hidden, null, { timeout: 10000 }).then(() => true, () => false);
    let b = await button(page);
    ok(shown && !b.badge && b.label === 'Artifacts, none yet', 'a chat with the server on has the button, with no badge', JSON.stringify(b));
    await page.click('#artifactsBtn');
    const empty = await page.waitForSelector('#artifactsPanel:not([hidden]) .so-artifacts__empty', { timeout: 5000 }).then(() => true, () => false);
    const emptyText = empty ? await page.$eval('#artifactsPanel .so-artifacts__empty', (n) => n.innerText.replace(/\s+/g, ' ')) : '';
    ok(empty && /No artifacts yet/.test(emptyText) && /Explain the architecture with a diagram/.test(emptyText),
      'at a desk the button opens the column, which says how to ask for an artifact', emptyText);

    // The Tests and the Artifacts column are never open together.
    await page.waitForFunction(() => !document.getElementById('testsBtn').hidden, null, { timeout: 10000 });
    await page.click('#testsBtn');
    await wait(300);
    const testsOpen = await page.evaluate(() => ({ tests: !document.getElementById('testsPanel').hidden, artifacts: !document.getElementById('artifactsPanel').hidden }));
    await page.click('#artifactsBtn');
    await wait(300);
    const artsOpen = await page.evaluate(() => ({ tests: !document.getElementById('testsPanel').hidden, artifacts: !document.getElementById('artifactsPanel').hidden }));
    ok(testsOpen.tests && !testsOpen.artifacts && artsOpen.artifacts && !artsOpen.tests,
      'opening the Tests column shuts the Artifacts column, and the other way round', JSON.stringify({ testsOpen, artsOpen }));
    // Shut: the next artifact opens it by itself.
    await page.click('#artifactsPanel [data-art-close]');
    await wait(300);

    // The agent makes a Markdown page with a diagram: the column opens on it.
    await tell(s, id, call('artifact_create', {
      title: 'How a request flows', description: 'From the browser to the database', format: 'markdown', content: MARKDOWN,
    }));
    const opened = await page.waitForSelector('#artifactsPanel:not([hidden])[data-page] iframe.so-artpage__iframe', { timeout: 30000 }).then(() => true, () => false);
    let list = await untilArtifacts(s, id, (l) => l.length === 1);
    const a1 = list[0] || {};
    let frame = opened ? await frameOf(page, '#artifactsPanel') : {};
    ok(opened && a1.id === 'A1' && frame.src === a1.url && /^\/artifacts\/[^/]+\/A1\/[0-9a-f]{32}\/$/.test(a1.url || '')
      && frame.sandbox === 'allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals allow-downloads' && frame.title === 'How a request flows',
    'a new artifact opens the column on its page by itself: the page in a sandboxed frame, titled', JSON.stringify({ frame, url: a1.url }));
    let inside = opened ? await framed(page, '#artifactsPanel', 'h1').then((f) => f.evaluate(() => ({
      h1: document.querySelector('h1').textContent, strong: !!document.querySelector('strong'), table: !!document.querySelector('table'),
      origin: (() => { try { return String(window.origin); } catch { return '?'; } })(),
    })), (err) => ({ err: String(err) })) : {};
    ok(inside.h1 === 'How a request flows' && inside.strong && inside.table && inside.origin === 'null',
      'the Markdown is drawn by the kit in a frame of an opaque origin', JSON.stringify(inside));
    const widths = await page.evaluate(() => ({
      stage: document.getElementById('stage').getBoundingClientRect().width,
      panel: document.getElementById('artifactsPanel').getBoundingClientRect().width,
      frame: document.querySelector('#artifactsPanel iframe').getBoundingClientRect().height,
    }));
    ok(Math.abs(widths.panel - widths.stage / 2) <= 4 && widths.frame > 500, 'the page widens the column to half the stage, and fills its height', JSON.stringify(widths));
    const head = await page.$eval('#artifactsPanel', (n) => ({
      title: n.querySelector('.so-artifacts__title').textContent,
      back: !n.querySelector('[data-art-back]').hidden,
      open: n.querySelector('a.so-artpage__open')?.getAttribute('href'), target: n.querySelector('a.so-artpage__open')?.target,
      rel: n.querySelector('a.so-artpage__open')?.rel, del: !!n.querySelector('.so-artpage__delete'),
      version: n.querySelector('.so-artpage__version').textContent, select: !!n.querySelector('.so-artpage__select'),
    }));
    ok(head.title === 'How a request flows' && head.back && head.open === a1.url && head.target === '_blank' && head.rel === 'noopener' && head.del
      && /^Version 1/.test(head.version) && !head.select,
    'its head: Back, the title, Open in a new tab, Delete, and the one version', JSON.stringify(head));
    b = await button(page);
    ok(!b.badge, 'a page shown is seen: no badge', JSON.stringify(b));
    await still(page);
    await shot(page, 'artifacts-desk-page');

    // The page by itself: the login's, sandboxed, never cached.
    const direct = await s.context.request.get(s.url + a1.url);
    const csp = direct.headers()['content-security-policy'] || '';
    ok(direct.status() === 200 && /sandbox allow-scripts/.test(csp) && /frame-ancestors 'self'/.test(csp) && /no-cache/.test(direct.headers()['cache-control'] || ''),
      'the page is served sandboxed, framed only by Socrates, and not cached', JSON.stringify({ status: direct.status(), csp, cache: direct.headers()['cache-control'] }));
    const anon = await fetch(s.url + a1.url, { redirect: 'manual' });
    ok(anon.status !== 200, 'without the login the page is not served', String(anon.status));

    // A second artifact while the first is on screen: the column stays, the
    // badge counts it.
    await tell(s, id, call('artifact_create', {
      title: 'Sales by quarter', description: 'Revenue per quarter', content: HTML, files: [chart],
    }));
    const counted = await page.waitForFunction(() => document.querySelector('#artifactsBtn .ds-badge')?.textContent === '1', null, { timeout: 30000 }).then(() => true, () => false);
    frame = await frameOf(page, '#artifactsPanel');
    ok(counted && frame.src === a1.url, 'a new artifact while another is on screen leaves it there, and the badge counts it', JSON.stringify({ counted, frame }));
    await page.click('#artifactsPanel [data-art-back]');
    await page.waitForSelector('#artifactsPanel:not([data-page]) .so-artifact', { timeout: 5000 });
    list = await rows(page, '#artifactsPanel');
    ok(list.length === 2 && list[0].id === 'A2' && list[0].tag === 'New' && list[1].tag === '' && /^Version 1 · /.test(list[0].meta)
      && list[0].desc === 'Revenue per quarter', 'Back: the list, the newest first, the unseen one tagged New, "Version 1 · when"', JSON.stringify(list));
    await still(page);
    await shot(page, 'artifacts-desk-list');
    await page.click('#artifactsPanel .so-artifact[data-artifact="A2"] .ds-list__action');
    let f2 = await framed(page, '#artifactsPanel', '#chart');
    const pic = await f2.waitForFunction(() => {
      const img = document.getElementById('chart');
      return img.complete && img.naturalWidth === 240;
    }, null, { timeout: 10000 }).then(() => true, () => false);
    b = await button(page);
    ok(pic && !b.badge, 'the HTML page shows its attached picture, named relative; shown, it is seen', JSON.stringify({ pic, b }));

    // A new version reloads the frame.
    const before = await page.$('#artifactsPanel iframe');
    await tell(s, id, call('artifact_update', {
      artifact: 'A2', edits: [{ old: 'Up every quarter.', new: 'Up every quarter, most in Q4.' }], note: 'Q4 called out',
    }));
    const reloaded = await page.waitForFunction(() => document.querySelector('#artifactsPanel iframe')?.dataset.version === '2', null, { timeout: 30000 }).then(() => true, () => false);
    f2 = await framed(page, '#artifactsPanel', '#lead');
    const lead = await f2.waitForFunction(() => /most in Q4/.test(document.getElementById('lead').textContent), null, { timeout: 10000 }).then(() => true, () => false);
    const replaced = await before.evaluate((n) => !n.isConnected);
    const options = await page.$$eval('#artifactsPanel .so-artpage__select option', (n) => n.map((o) => o.textContent));
    ok(reloaded && lead && replaced && options.length === 2 && /^Version 2 \(latest\)/.test(options[0]) && /Q4 called out/.test(options[0]) && /^Version 1/.test(options[1]),
      'an edit makes version 2: the frame reloads with it, and a select lists both versions with their notes', JSON.stringify({ reloaded, lead, replaced, options }));
    await still(page);
    await shot(page, 'artifacts-desk-versions');

    // An older version picked stays when a newer one comes.
    await page.selectOption('#artifactsPanel .so-artpage__select', '1');
    const old = await page.waitForFunction(() => /\?v=1$/.test(document.querySelector('#artifactsPanel iframe')?.getAttribute('src') || ''), null, { timeout: 5000 }).then(() => true, () => false);
    f2 = await framed(page, '#artifactsPanel', '#lead');
    const oldText = await f2.$eval('#lead', (n) => n.textContent);
    const openOld = await page.$eval('#artifactsPanel a.so-artpage__open', (n) => n.getAttribute('href'));
    ok(old && oldText === 'Up every quarter.' && /\?v=1$/.test(openOld), 'picking version 1 shows it (?v=1), and opens it in a new tab', JSON.stringify({ old, oldText, openOld }));
    await tell(s, id, call('artifact_update', { artifact: 'A2', content: HTML.replace('Up every quarter.', 'Version three.'), note: 'Rewritten' }));
    await untilArtifacts(s, id, (l) => l.some((a) => a.id === 'A2' && a.version === 3));
    const three = await page.waitForFunction(() => document.querySelectorAll('#artifactsPanel .so-artpage__select option').length === 3, null, { timeout: 15000 }).then(() => true, () => false);
    const pinned = await page.evaluate(() => ({
      src: document.querySelector('#artifactsPanel iframe').getAttribute('src'),
      value: document.querySelector('#artifactsPanel .so-artpage__select').value,
    }));
    ok(three && /\?v=1$/.test(pinned.src) && pinned.value === '1', 'a new version while an older one is picked: the select has it, the frame stays', JSON.stringify(pinned));
    await page.selectOption('#artifactsPanel .so-artpage__select', '3');
    f2 = await framed(page, '#artifactsPanel', '#lead');
    const latest = await f2.waitForFunction(() => document.getElementById('lead')?.textContent === 'Version three.', null, { timeout: 10000 }).then(() => true, () => false);
    const back3 = await frameOf(page, '#artifactsPanel');
    ok(latest && !/\?v=/.test(back3.src) && back3.version === '3', 'the latest picked again shows version 3, at the page’s own address', JSON.stringify(back3));

    // Delete.
    await page.click('#artifactsPanel .so-artpage__delete');
    await page.waitForSelector('dialog[open] button:has-text("Delete the artifact")', { timeout: 5000 });
    await still(page);
    await shot(page, 'artifacts-delete');
    await page.click('dialog[open] button:has-text("Delete the artifact")');
    const gone = await page.waitForFunction(() => !document.querySelector('#artifactsPanel').hasAttribute('data-page')
      && document.querySelectorAll('#artifactsPanel .so-artifact').length === 1, null, { timeout: 10000 }).then(() => true, () => false);
    list = (await artifactsOf(s, id)).artifacts || [];
    ok(gone && list.length === 1 && list[0].id === 'A1', 'Delete, confirmed, removes it: the column is the list again, the server has one', JSON.stringify(list.map((a) => a.id)));

    // The column is remembered: a reload finds it open again.
    await page.reload({ waitUntil: 'domcontentloaded' });
    await openChat(page, id);
    const kept = await page.waitForSelector('#artifactsPanel:not([hidden]) .so-artifact', { timeout: 10000 }).then(() => true, () => false);
    ok(kept, 'the column stays open on this device across a reload', String(kept));
    const deskErrors = s.errors.filter((e) => !/jsdelivr|mermaid|ERR_FAILED/.test(e));
    ok(deskErrors.length === 0, 'no console errors at a desk', deskErrors.join(' | ') || '0');

    // A phone: the list is a side sheet, a page the whole screen.
    await page.setViewportSize({ width: 390, height: 844 });
    await wait(600);
    const bar = await page.evaluate(() => {
      const r = document.getElementById('artifactsBtn').getBoundingClientRect();
      return { left: Math.round(r.left), right: Math.round(r.right), width: innerWidth, column: document.getElementById('artifactsPanel').hidden };
    });
    ok(bar.column && bar.left >= 0 && bar.right <= bar.width, 'on a phone the column is gone and the button fits in the top bar', JSON.stringify(bar));
    await page.click('#artifactsBtn');
    const sheet = await page.waitForSelector('dialog.so-artifacts-sheet[open] .so-artifact', { timeout: 5000 }).then(() => true, () => false);
    await wait(500);
    ok(sheet && !(await sideways(page)), 'at 390px the button opens the side sheet, and nothing scrolls sideways', String(sheet));
    await still(page);
    await shot(page, 'artifacts-phone-sheet');
    await page.click('dialog.so-artifacts-sheet .so-artifact[data-artifact="A1"] .ds-list__action');
    const phoneFrame = await framed(page, 'dialog.so-artdialog[open]', 'h1').then(() => true, () => false);
    await wait(600);
    const box = await page.$eval('dialog.so-artdialog .ds-dialog__box', (n) => ({
      width: Math.round(n.getBoundingClientRect().width),
      frame: Math.round(n.querySelector('iframe').getBoundingClientRect().height),
      title: n.querySelector('.ds-dialog__title').textContent,
      back: !!n.querySelector('.ds-dialog__head [data-close]'),
      open: !!n.querySelector('.ds-dialog__head a.so-artpage__open'), del: !!n.querySelector('.ds-dialog__head .so-artpage__delete'),
    }));
    ok(phoneFrame && box.width === 390 && box.frame > 500 && box.title === 'How a request flows' && box.back && box.open && box.del,
      'a row opens the page in a full screen dialog with the same head, the frame filling it', JSON.stringify(box));
    await still(page);
    await shot(page, 'artifacts-phone-page');
    await page.keyboard.press('Escape');
    await page.waitForSelector('dialog.so-artdialog', { state: 'detached', timeout: 5000 }).catch(() => {});
    await page.keyboard.press('Escape');
    await page.waitForSelector('dialog.so-artifacts-sheet[open]', { state: 'detached', timeout: 5000 }).catch(() => {});

    // A new artifact on a phone: the badge counts it, a snackbar offers Open.
    await tell(s, id, call('artifact_create', { title: 'Timeline', content: '<h1>Timeline</h1><ol class="steps"><li>Plan</li><li>Build</li></ol>' }));
    const offered = await page.waitForSelector('.ds-toast:has-text("Timeline") .ds-toast__action', { timeout: 30000 }).then(() => true, () => false);
    b = await button(page);
    ok(offered && b.badge === '1', 'on a phone a new artifact is counted and a snackbar offers Open', JSON.stringify({ offered, b }));
    await still(page);
    await shot(page, 'artifacts-phone-toast');
    await page.click('.ds-toast:has-text("Timeline") .ds-toast__action');
    const viaToast = await framed(page, 'dialog.so-artdialog[open]', 'h1').then((fr) => fr.$eval('h1', (n) => n.textContent)).catch(() => '');
    b = await button(page);
    ok(viaToast === 'Timeline' && !b.badge, 'Open shows it in the dialog, and it is seen', JSON.stringify({ viaToast, b }));
    await page.keyboard.press('Escape');
    await page.waitForSelector('dialog.so-artdialog', { state: 'detached', timeout: 5000 }).catch(() => {});
    const errors = s.errors.filter((e) => !/jsdelivr|mermaid|ERR_FAILED/.test(e));
    ok(errors.length === 0, 'no console errors', errors.join(' | ') || '0');
  } finally {
    await s.stop();
  }
}
