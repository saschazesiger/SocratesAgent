// pages: a chat's pages (docs/design/PAGES.md). A chat has a pages folder
// (`pages_dir`) its agent writes Markdown into; the Pages button in the
// chat's head shows up with the first page, its badge counts what this
// device has not seen, its menu lists the pages newest first by their
// `# title` and opens one beside the chat; a link in the chat to a page by
// its absolute path opens it there too, and a page open there is marked
// seen.

import { mkdirSync, mkdtempSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { start, setup, ok, wait, shot } from './harness.mjs';
import { tell, signIn } from './chattests.mjs';

const frameText = (page, frame, selector) => page.frameLocator(frame).locator(selector).first().textContent({ timeout: 10000 }).catch(() => '');

async function pagesDirOf(s, id) {
  const end = Date.now() + 20000;
  while (Date.now() < end) {
    const res = await s.context.request.get(s.url + '/api/sessions');
    const body = await res.json().catch(() => ({}));
    const list = Array.isArray(body) ? body : (body.sessions || []);
    const row = list.find((x) => x.id === id);
    if (row && row.pages_dir && existsSync(row.pages_dir)) return row.pages_dir;
    await wait(200);
  }
  return '';
}

const VIEW = '#docsidePanel iframe.so-docs__view';

async function badge(page) {
  return page.$eval('#pagesBtn .ds-badge', (b) => (b.hidden ? '' : b.textContent.trim())).catch(() => '?');
}

export async function pages() {
  const scratch = mkdtempSync(join(process.env.TMPDIR || tmpdir(), 'pages-'));
  const work = join(scratch, 'work');
  mkdirSync(work);
  writeFileSync(join(work, 'main.go'), 'package main\n');
  const s = await start({ viewport: { width: 1440, height: 900 } });
  try {
    const { page } = s;
    await setup(page, s.url);
    await signIn(s);
    const res = await s.context.request.post(s.url + '/api/chats', {
      data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), harness: 'claude', workdir_mode: 'custom', workdir: work, title: 'Explain it' },
    });
    if (res.status() !== 201) throw new Error('could not make the chat: ' + res.status() + ' ' + await res.text());
    const chat = (await res.json()).session.id;

    const dir = await pagesDirOf(s, chat);
    ok(!!dir && dir.endsWith('/chats/' + chat + '/pages'), 'a chat has a pages folder, made for it', dir);

    await page.waitForSelector(`#sessionList .so-row[data-id="${chat}"] .so-row__open`, { timeout: 20000 });
    await page.click(`#sessionList .so-row[data-id="${chat}"] .so-row__open`);
    await tell(s, chat, 'Hello');
    await page.waitForSelector('#chat .so-md', { timeout: 20000 });
    ok(await page.$eval('#pagesBtn', (b) => b.hidden), 'a chat without pages has no Pages button');

    // The agent writes a page: the button comes, with a badge.
    writeFileSync(join(dir, 'plan.md'), '# The plan\n\n> [!NOTE]\n> First things first.\n\n```mermaid\nflowchart LR\n  A --> B\n```\n');
    await page.waitForSelector('#pagesBtn:not([hidden])', { timeout: 12000 }).catch(() => {});
    ok(await page.isVisible('#pagesBtn'), 'the first page brings the Pages button');
    ok((await badge(page)) === '1', 'its badge counts the page not seen yet', await badge(page));

    // A link to it in the chat opens it beside the chat, and it is seen.
    await tell(s, chat, `See [the plan](${dir}/plan.md).`);
    const link = `#chat a[href="${dir}/plan.md"]`;
    await page.waitForSelector(link, { timeout: 20000 });
    await page.click(link);
    await page.waitForSelector('#docsidePanel:not([hidden]) ' + 'iframe.so-docs__view', { timeout: 8000 });
    ok((await frameText(page, VIEW, 'h1')) === 'The plan', 'a link to a page opens it beside the chat');
    await page.waitForFunction(() => {
      const b = document.querySelector('#pagesBtn .ds-badge');
      return b && b.hidden;
    }, null, { timeout: 8000 }).catch(() => {});
    ok((await badge(page)) === '', 'a page shown is seen', await badge(page));

    // A second page: the menu lists both, newest first, by their titles.
    await wait(1100);
    writeFileSync(join(dir, 'notes.md'), '# Notes on the API\n\nShort.\n');
    await page.waitForFunction(() => {
      const b = document.querySelector('#pagesBtn .ds-badge');
      return b && !b.hidden && b.textContent.trim() === '1';
    }, null, { timeout: 12000 }).catch(() => {});
    ok((await badge(page)) === '1', 'a new page counts in the badge', await badge(page));
    await page.click('#pagesBtn');
    const menu = ':is(#sessionMenu:not([hidden]), dialog.so-actions[open])';
    await page.waitForSelector(`${menu} [data-action^="page:"]`, { timeout: 5000 });
    const items = await page.$$eval(`${menu} [data-action^="page:"]`, (list) => list.map((i) => i.dataset.action + '=' + i.querySelector('.ds-menu__label').textContent.trim()));
    ok(items.join('|') === 'page:notes.md=Notes on the API|page:plan.md=The plan', 'the menu lists the pages newest first by their titles', items.join('|'));
    await shot(page, 'pages-menu');
    await page.click(`${menu} [data-action="page:notes.md"]`);
    await page.waitForFunction(() => {
      const t = document.querySelector('#docsidePanel .so-docside__title');
      return t && t.textContent.trim() === 'Notes on the API';
    }, null, { timeout: 8000 }).catch(() => {});
    ok((await frameText(page, VIEW, 'h1')) === 'Notes on the API', 'a page chosen in the menu opens beside the chat');

    // A change on disk is drawn again.
    writeFileSync(join(dir, 'notes.md'), '# Notes on the API, revised\n');
    let h1 = '';
    for (let i = 0; i < 40; i++) {
      h1 = await frameText(page, VIEW, 'h1');
      if (h1 === 'Notes on the API, revised') break;
      await wait(250);
    }
    ok(h1 === 'Notes on the API, revised', 'a page changed by the agent is drawn again', h1);
    await shot(page, 'pages-chat');
  } finally {
    await s.stop();
    rmSync(scratch, { recursive: true, force: true });
  }
}
