// docs: a project folder's Markdown files (internal/server/docs.go, docs.js,
// docside.js). A folder's ⋮ has Docs when the folder has Markdown files
// and not otherwise; Docs opens the folder's README.md on the stage at
// #docs=<folder>&page=<path>, beside a tree of every Markdown file in the
// repository's own folders (hidden folders and node_modules left out),
// with the page's headings. A link in the page to another file stays in
// the frame and the address follows; a file changed on disk is drawn
// again by itself; the filter finds a file; a reload comes back to it. The
// frame is a sandbox and serves no file outside the docs. In a chat, a
// link to a Markdown file of its folder opens the file beside the chat, as
// does a `code` path to one (by its name alone when only one file has it)
// and a link to it on the folder's own GitHub repository; a `code` path to
// a file that is not there says so.

import { mkdirSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { start, setup, ok, wait, shot } from './harness.mjs';
import { tell, signIn } from './chattests.mjs';

async function folderChat(s, title, workdir) {
  const res = await s.context.request.post(s.url + '/api/chats', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), harness: 'claude', workdir_mode: 'custom', workdir, title },
  });
  if (res.status() !== 201) throw new Error('could not make the chat: ' + res.status() + ' ' + await res.text());
  return (await res.json()).session.id;
}

function write(root, path, text) {
  const parts = path.split('/');
  mkdirSync(join(root, ...parts.slice(0, -1)), { recursive: true });
  writeFileSync(join(root, ...parts), text);
}

const README = `# Guide

The guide to the project.

> [!NOTE]
> Read the setup first.

## Setup

See [the setup](docs/setup.md).

## Architecture

\`\`\`mermaid
flowchart LR
  A[Browser] --> B[Server]
\`\`\`
`;

// openMenu opens a folder's ⋮ and answers the actions in its menu.
async function menuOf(page, folder) {
  await page.click(`#sessionList .so-folder__more[data-folder-more="${folder}"]`);
  await page.waitForSelector('#sessionMenu:not([hidden]) [data-action]');
  return page.$$eval('#sessionMenu [data-action]', (items) => items.map((i) => i.dataset.action));
}

const frameText = (page, frame, selector) => page.frameLocator(frame).locator(selector).first().textContent({ timeout: 10000 }).catch(() => '');

export async function docs() {
  const scratch = mkdtempSync(join(process.env.TMPDIR || tmpdir(), 'docs-'));
  const guide = join(scratch, 'guide');
  const bare = join(scratch, 'bare');
  write(guide, 'README.md', README);
  write(guide, 'AGENTS.md', '# For agents\n');
  write(guide, 'CHANGELOG.md', '# Changes\n');
  write(guide, 'docs/setup.md', '# Setup\n\nInstall it. Back to the [guide](../README.md). The [code](../main.go).\n');
  write(guide, 'docs/design/ARCHITECTURE.md', '# Architecture\n\n## Parts\n\n## Flow\n');
  write(guide, 'node_modules/lib/README.md', '# A dependency\n');
  write(guide, '.private/notes.md', '# Private\n');
  write(guide, '.git/config', '[core]\n\tbare = false\n[remote "origin"]\n\turl = git@github.com:Acme/Guide.git\n');
  write(guide, '.env', 'SECRET=1\n');
  write(guide, 'main.go', 'package main\n');
  mkdirSync(bare);
  write(bare, 'main.go', 'package main\n');
  const s = await start({ viewport: { width: 1440, height: 900 } });
  try {
    const { page } = s;
    await setup(page, s.url);
    await signIn(s);
    const chat = await folderChat(s, 'Guide work', guide);
    await folderChat(s, 'Bare work', bare);

    // The ⋮: Docs only where there is Markdown.
    await page.waitForSelector(`#sessionList .so-folder__more[data-folder-more="${guide}"]`, { timeout: 20000 });
    let actions = await menuOf(page, bare);
    ok(!actions.includes('docs') && actions.includes('notes'), 'a folder without Markdown has Notes and no Docs', actions.join('|'));
    await page.keyboard.press('Escape');
    await page.waitForSelector('#sessionMenu[hidden]', { state: 'attached' });
    actions = await menuOf(page, guide);
    ok(actions.includes('docs') && actions.includes('notes'), 'a folder with Markdown has Docs in its ⋮', actions.join('|'));
    await page.click('#sessionMenu [data-action="docs"]');

    // The page: the README, the tree, the headings.
    await page.waitForSelector('#docs:not([hidden]) .so-docs__row[data-doc]', { timeout: 10000 });
    const view = '#docs iframe.so-docs__view';
    ok((await frameText(page, view, 'h1')) === 'Guide', 'Docs opens on the README of the folder');
    await page.waitForFunction((want) => decodeURIComponent(location.hash) === '#docs=' + want + '&page=README.md', guide, { timeout: 8000 })
      .catch(() => {});
    ok(await page.evaluate(() => decodeURIComponent(location.hash)) === '#docs=' + guide + '&page=README.md', 'the address names the folder and the file',
      await page.evaluate(() => location.hash));
    const top = await page.$$eval('#docs .so-docs__nav .so-docs__list--top > .so-docs__item > .so-docs__row', (rows) => rows.map((r) => r.textContent.trim()));
    ok(top[0] === 'README.md', 'the README is first in the tree', top.join('|'));
    ok(top.includes('AGENTS.md') && top.includes('CHANGELOG.md') && top.includes('docs'), 'the tree has the files and folders of the repository', top.join('|'));
    ok(!top.includes('node_modules') && !top.includes('.private'), 'hidden folders and node_modules are left out', top.join('|'));
    const current = await page.$eval('#docs .so-docs__nav [aria-current="page"]', (r) => r.dataset.doc);
    ok(current === 'README.md', 'the file on screen is marked in the tree', current);
    const glyphs = await page.$$eval('#docs .so-docs__nav .so-docs__row[data-doc]', (rows) => Object.fromEntries(rows.map((r) => [r.dataset.doc, r.querySelector('svg')?.innerHTML.length || 0])));
    ok(glyphs['AGENTS.md'] !== glyphs['CHANGELOG.md'] && glyphs['CHANGELOG.md'] !== glyphs['docs/setup.md'], 'well-known files have glyphs of their own', JSON.stringify(glyphs));
    await page.waitForSelector('#docs[data-toc] .so-docs__toc:not([hidden]) [data-heading="setup"]', { timeout: 8000 }).catch(() => {});
    const heads = await page.$$eval('#docs .so-docs__toc [data-heading]', (links) => links.map((l) => l.dataset.heading));
    ok(heads.join('|') === 'setup|architecture', 'the page’s headings stand beside it', heads.join('|'));
    ok((await frameText(page, view, '.callout.info .callout-title')) === 'Note', 'a GitHub alert is drawn as a callout');
    // The diagram, when Mermaid could be loaded (jsDelivr), for the picture.
    await page.frameLocator(view).locator('pre.mermaid svg').waitFor({ timeout: 10000 }).catch(() => {});
    await shot(page, 'docs-readme');

    // A link in the page to another file stays in the frame.
    await page.frameLocator(view).locator('a[href="docs/setup.md"]').click();
    await page.waitForFunction(() => decodeURIComponent(location.hash).endsWith('&page=docs/setup.md'), null, { timeout: 8000 }).catch(() => {});
    ok(await page.evaluate(() => decodeURIComponent(location.hash).endsWith('&page=docs/setup.md')), 'a link to another file follows in the address',
      await page.evaluate(() => location.hash));
    ok((await frameText(page, view, 'h1')) === 'Setup', 'and shows that file');
    ok(await page.$eval('#docs .so-docs__nav [aria-current="page"]', (r) => r.dataset.doc) === 'docs/setup.md', 'the tree opens to it and marks it');
    // A link to a file that is not Markdown is not followed.
    await page.frameLocator(view).locator('a[href="../main.go"]').click();
    await page.waitForSelector('.ds-toast:has-text("not a Markdown file")', { timeout: 5000 }).catch(() => {});
    ok(await page.$('.ds-toast:has-text("not a Markdown file")') !== null, 'a link to code says only Markdown is shown');
    ok((await frameText(page, view, 'h1')) === 'Setup', 'and the frame stays on its file');

    // A change on disk is drawn by itself.
    write(guide, 'docs/setup.md', '# Setup, revised\n\nInstall it again.\n');
    const end = Date.now() + 15000;
    let h1 = '';
    while (Date.now() < end) {
      h1 = await frameText(page, view, 'h1');
      if (h1 === 'Setup, revised') break;
      await wait(400);
    }
    ok(h1 === 'Setup, revised', 'a file changed on disk is drawn again by itself', h1);

    // The filter.
    await page.fill('#docsFilter', 'arch');
    const found = await page.$$eval('#docs .so-docs__nav .so-docs__row[data-doc]', (rows) => rows.map((r) => r.dataset.doc));
    ok(found.join('|') === 'docs/design/ARCHITECTURE.md', 'the filter finds a file by its name', found.join('|'));
    await page.click('#docs .so-docs__nav .so-docs__row[data-doc="docs/design/ARCHITECTURE.md"]');
    await page.waitForFunction(() => decodeURIComponent(location.hash).endsWith('&page=docs/design/ARCHITECTURE.md'), null, { timeout: 8000 });
    ok((await frameText(page, view, 'h1')) === 'Architecture', 'a file chosen in the tree is shown');
    await page.fill('#docsFilter', '');

    // A reload comes back to it.
    await page.reload();
    await page.waitForSelector('#docs:not([hidden]) .so-docs__row[data-doc]', { timeout: 15000 });
    const after = await frameText(page, view, 'h1');
    ok(after === 'Architecture', 'a reload comes back to the same file',
      after + ' ' + await page.evaluate(() => location.hash + ' ' + document.querySelector('#docs iframe').src));

    // The frame's address: a sandbox, and nothing outside the docs.
    const src = await page.$eval(view, (f) => f.src);
    const base = src.slice(0, src.indexOf('/docs/design/'));
    const res = await s.context.request.get(src.split('#')[0].split('?')[0]);
    ok(/sandbox/.test(res.headers()['content-security-policy'] || ''), 'a page goes as a sandbox', res.headers()['content-security-policy']);
    for (const path of ['main.go', '.env', '.private/notes.md', 'node_modules/lib/README.md', '../bare/main.go']) {
      const one = await s.context.request.get(base + '/' + path, { maxRedirects: 0 });
      ok(one.status() === 404, 'the docs do not serve ' + path, String(one.status()));
    }
    const forged = await s.context.request.get(s.url + '/docs/' + Buffer.from(bare).toString('base64url') + '.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/README.md');
    ok(forged.status() === 404, 'a made-up address serves nothing', String(forged.status()));

    // A link in a chat opens the file beside the chat.
    await tell(s, chat, 'Read [the architecture](docs/design/ARCHITECTURE.md#flow) and [the site](https://example.com).');
    await page.click(`#sessionList .so-row[data-id="${chat}"] .so-row__open`);
    const link = '#chat a[href="docs/design/ARCHITECTURE.md#flow"]';
    await page.waitForSelector(link, { timeout: 20000 });
    await page.click(link);
    await page.waitForSelector('#docsidePanel:not([hidden]) iframe.so-docs__view', { timeout: 8000 });
    ok((await frameText(page, '#docsidePanel iframe.so-docs__view', 'h1')) === 'Architecture', 'a link in the chat opens the file beside it');
    ok(await page.evaluate(() => !decodeURIComponent(location.hash).startsWith('#docs=')), 'and the chat stays on the stage');
    ok(await page.$eval('#docsidePanel .so-docside__title', (t) => t.textContent.trim()) === 'Architecture', 'the column is named after the page');
    await shot(page, 'docs-chat');

    // A `code` path, a name alone, the folder's own GitHub address.
    await tell(s, chat, 'See `docs/setup.md`, `ARCHITECTURE.md`, `MISSING.md`, [on GitHub](https://github.com/acme/guide/blob/main/CHANGELOG.md) and [elsewhere](https://github.com/other/repo/blob/main/README.md).');
    const opens = async (selector, h1, what) => {
      await page.waitForSelector(selector, { timeout: 20000 });
      await page.click(selector);
      await page.waitForFunction((want) => {
        const t = document.querySelector('#docsidePanel .so-docside__title');
        return t && t.textContent.trim() === want;
      }, h1, { timeout: 8000 }).catch(() => {});
      ok((await frameText(page, '#docsidePanel iframe.so-docs__view', 'h1')) === h1, what);
    };
    await opens('#chat a[href="docs/setup.md"] code', 'Setup, revised', 'a `code` path opens the file beside the chat');
    await opens('#chat a[href="ARCHITECTURE.md"] code', 'Architecture', 'a file named alone opens the one file of that name');
    await opens('#chat a[href="https://github.com/acme/guide/blob/main/CHANGELOG.md"]', 'Changes', 'a link to the folder\'s own GitHub repository opens beside the chat');
    await page.click('#chat a[href="MISSING.md"] code');
    await page.waitForSelector('.ds-toast:has-text("MISSING.md is not a Markdown file")', { timeout: 5000 }).catch(() => {});
    ok(await page.$('.ds-toast:has-text("MISSING.md is not a Markdown file")') !== null, 'a `code` path to a file that is not there says so');
    ok(await page.$eval('#chat a[href="https://github.com/other/repo/blob/main/README.md"]', (a) => a.target) === '_blank', 'another repository stays a link of the browser');
    await shot(page, 'docs-chat-paths');
    await opens(link, 'Architecture', 'the first link opens its file again');
    await page.click('#docsidePanel [data-docside="docs"]');
    await page.waitForSelector('#docs:not([hidden])', { timeout: 8000 });
    ok(await page.evaluate(() => decodeURIComponent(location.hash).endsWith('&page=docs/design/ARCHITECTURE.md')), 'Open in Docs shows it with the tree',
      await page.evaluate(() => location.hash));

    // On a phone the tree is a sheet.
    await page.setViewportSize({ width: 390, height: 800 });
    await wait(300);
    ok(!(await page.isVisible('#docs .so-docs__nav')), 'on a phone the tree is not a column');
    await page.click('#docs [data-docs="files"]');
    await page.waitForSelector('#docsSheet[open] .so-docs__row[data-doc]', { timeout: 5000 });
    ok(true, 'Files opens the tree in a sheet');
    await page.click('#docsSheet .so-docs__row[data-doc="AGENTS.md"]');
    await page.waitForFunction(() => decodeURIComponent(location.hash).endsWith('&page=AGENTS.md'), null, { timeout: 8000 });
    ok((await frameText(page, view, 'h1')) === 'For agents', 'a file chosen there is shown and the sheet goes');
    await shot(page, 'docs-phone');
  } finally {
    await s.stop();
    rmSync(scratch, { recursive: true, force: true });
  }
}
