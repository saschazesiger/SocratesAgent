// pages: a chat's pages (docs/design/PAGES.md). A chat has a pages folder
// (`pages_dir`) its agent writes Markdown into; the Pages button in the
// chat's head shows up with the first page, its badge counts what this
// device has not seen, its menu lists the pages newest first by their
// `# title` and opens one beside the chat; a link in the chat to a page by
// its absolute path opens it there too, and a page open there is marked
// seen. Any other file (PAGES.md, "Any file"): a video, a recording or a CSV
// of the folder is listed with the pages and opens in a player or a viewer
// beside the chat; a file of the working directory the agent names (a
// `code` path) is listed as linked and opens the same way; a file the agent
// never named is not served.

import { mkdirSync, mkdtempSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';
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

    // A page links a file of the chat's working directory by its absolute
    // path (agent.DocsGuidance): the page lies outside it, and the link opens
    // the file beside the chat rather than saying it leads out.
    mkdirSync(join(work, 'docs'));
    writeFileSync(join(work, 'docs', 'open.md'), '# Still open\n');
    writeFileSync(join(dir, 'notes.md'), `# Notes on the API, revised\n\nWhat is left: [open.md](${work}/docs/open.md).\n`);
    const out = page.frameLocator(VIEW).locator(`a[href="${work}/docs/open.md"]`).first();
    await out.waitFor({ timeout: 12000 });
    await out.click();
    h1 = '';
    for (let i = 0; i < 40; i++) {
      h1 = await frameText(page, VIEW, 'h1');
      if (h1 === 'Still open') break;
      await wait(250);
    }
    ok(h1 === 'Still open', "a page's link to a file of the working directory opens it beside the chat", h1);

    await files(s, chat, dir, work);
  } finally {
    await s.stop();
    rmSync(scratch, { recursive: true, force: true });
  }
}

// wav is a second of silence as a WAV file: a recording any browser plays.
function wav() {
  const rate = 8000;
  const n = rate;
  const buf = Buffer.alloc(44 + n);
  buf.write('RIFF', 0); buf.writeUInt32LE(36 + n, 4); buf.write('WAVE', 8);
  buf.write('fmt ', 12); buf.writeUInt32LE(16, 16); buf.writeUInt16LE(1, 20); buf.writeUInt16LE(1, 22);
  buf.writeUInt32LE(rate, 24); buf.writeUInt32LE(rate, 28); buf.writeUInt16LE(1, 32); buf.writeUInt16LE(8, 34);
  buf.write('data', 36); buf.writeUInt32LE(n, 40); buf.fill(128, 44);
  return buf;
}

const FILEVIEW = '#docsidePanel:not([hidden]) .so-docside__file:not([hidden])';

async function titleIs(page, words) {
  await page.waitForFunction((w) => {
    const t = document.querySelector('#docsidePanel .so-docside__title');
    return t && t.textContent.trim() === w;
  }, words, { timeout: 8000 }).catch(() => {});
  return page.$eval('#docsidePanel .so-docside__title', (t) => t.textContent.trim()).catch(() => '');
}

async function files(s, chat, dir, work) {
  const { page } = s;
  const menu = ':is(#sessionMenu:not([hidden]), dialog.so-actions[open])';
  await page.keyboard.press('Escape');

  // A video in the pages folder: listed with the pages, played beside the
  // chat. ffmpeg makes a real one where it is there (VP8: the headless
  // shell has no H.264).
  const made = spawnSync('ffmpeg', ['-v', 'error', '-f', 'lavfi', '-i', 'testsrc=duration=1:size=160x120:rate=10',
    '-c:v', 'libvpx', '-y', join(dir, 'demo.webm')]).status === 0;
  if (!made) writeFileSync(join(dir, 'demo.webm'), Buffer.alloc(64));
  writeFileSync(join(dir, 'numbers.csv'), 'name,count\n"Smith, Jo",3\nLee,5\n');
  await page.waitForFunction(() => {
    const b = document.querySelector('#pagesBtn .ds-badge');
    return b && !b.hidden && Number(b.textContent.trim()) >= 2;
  }, null, { timeout: 12000 }).catch(() => {});
  await page.click('#pagesBtn');
  await page.waitForSelector(`${menu} [data-action="page:demo.webm"]`, { timeout: 5000 }).catch(() => {});
  ok(await page.isVisible(`${menu} [data-action="page:demo.webm"]`), 'a video of the pages folder is listed with the pages');
  await page.click(`${menu} [data-action="page:demo.webm"]`);
  await page.waitForSelector(`${FILEVIEW} video.so-fileview__video`, { timeout: 8000 }).catch(() => {});
  ok(await page.isVisible(`${FILEVIEW} video.so-fileview__video`), 'a video chosen in the menu plays beside the chat');
  ok((await titleIs(page, 'demo.webm')) === 'demo.webm', 'the column names the video');
  if (made) {
    const ready = await page.waitForFunction(() => {
      const v = document.querySelector('#docsidePanel video');
      return v && v.readyState >= 1 && v.duration > 0;
    }, null, { timeout: 8000 }).then(() => true).catch(() => false);
    ok(ready, 'the video loads in the player (served with its type and ranges)');
  }
  const src = await page.$eval('#docsidePanel video', (v) => v.getAttribute('src')).catch(() => '');
  const ranged = await s.context.request.get(new URL(src, s.url).href, { headers: { Range: 'bytes=0-9' } });
  ok(ranged.status() === 206 && (ranged.headers()['content-type'] || '').startsWith('video/webm'), 'a video is served in ranges, as a video', ranged.status() + ' ' + ranged.headers()['content-type']);
  await shot(page, 'pages-video');

  // A CSV: a table.
  await page.click('#pagesBtn');
  await page.click(`${menu} [data-action="page:numbers.csv"]`);
  await page.waitForSelector(`${FILEVIEW} table.ds-table`, { timeout: 8000 }).catch(() => {});
  const cells = await page.$$eval(`${FILEVIEW} table.ds-table td`, (list) => list.map((td) => td.textContent)).catch(() => []);
  ok(cells.join('|') === 'Smith, Jo|3|Lee|5', 'a CSV is drawn as a table', cells.join('|'));
  await shot(page, 'pages-csv');

  // An HTML report: its script runs, in a sandbox of its own, and its
  // relative picture loads from the folder. Any other file: a download.
  mkdirSync(join(dir, 'report'), { recursive: true });
  writeFileSync(join(dir, 'report', 'index.html'), '<!doctype html><title>R</title><h1 id="h">static</h1><img id="i" src="dot.svg"><script>document.getElementById("h").textContent = "scripted " + (document.domain === "" ? "sandboxed" : "same-origin");</script>');
  writeFileSync(join(dir, 'report', 'dot.svg'), '<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"><rect width="4" height="4"/></svg>');
  writeFileSync(join(dir, 'bundle.zip'), Buffer.from('PK\x03\x04' + 'x'.repeat(40), 'binary'));
  let entry = false;
  for (let i = 0; i < 20 && !entry; i++) {
    await page.click('#pagesBtn');
    entry = await page.waitForSelector(`${menu} [data-action="page:report/index.html"]`, { timeout: 1000 }).then(() => true).catch(() => false);
    if (!entry) { await page.keyboard.press('Escape'); await wait(500); }
  }
  await page.click(`${menu} [data-action="page:report/index.html"]`);
  const report = page.frameLocator(`${FILEVIEW} iframe.so-fileview__frame`);
  const scripted = await report.locator('#h').textContent({ timeout: 8000 }).catch(() => '');
  ok(scripted === 'scripted sandboxed', "an HTML page's script runs, in a sandbox", scripted);
  const pic = await report.locator('#i').evaluate((img) => img.naturalWidth).catch(() => 0);
  ok(pic === 4, 'its relative picture loads from the folder', pic);
  await page.click('#pagesBtn');
  await page.click(`${menu} [data-action="page:bundle.zip"]`);
  await page.waitForSelector(`${FILEVIEW} .so-fileview__empty a[download]`, { timeout: 8000 }).catch(() => {});
  ok(await page.isVisible(`${FILEVIEW} .so-fileview__empty a[download]`), 'a file no browser shows is offered for download');
  const zip = await s.context.request.get(new URL(await page.$eval(`${FILEVIEW} .so-fileview__empty a[download]`, (a) => a.getAttribute('href')), s.url).href);
  ok((zip.headers()['content-disposition'] || '').startsWith('attachment'), 'and served as one', zip.headers()['content-disposition']);

  // A recording in the working directory, named by the agent as a `code`
  // path: a link in the chat, a player beside it, listed as linked.
  mkdirSync(join(work, 'out'), { recursive: true });
  writeFileSync(join(work, 'out', 'voice.wav'), wav());
  writeFileSync(join(work, 'out', 'secret.txt'), 'not named\n');
  await tell(s, chat, 'The take is `out/voice.wav`.');
  const link = '#chat a[href="out/voice.wav"]';
  await page.waitForSelector(link, { timeout: 20000 });
  await page.click(link);
  await page.waitForSelector(`${FILEVIEW} audio.so-fileview__audio`, { timeout: 8000 }).catch(() => {});
  ok(await page.isVisible(`${FILEVIEW} audio.so-fileview__audio`), 'a recording the agent names opens in a player beside the chat');
  const played = await page.waitForFunction(() => {
    const a = document.querySelector('#docsidePanel audio');
    return a && a.readyState >= 1 && a.duration > 0;
  }, null, { timeout: 8000 }).then(() => true).catch(() => false);
  ok(played, 'the recording loads in the player');
  await shot(page, 'pages-audio');
  await page.waitForFunction(() => !document.querySelector('#pagesBtn').hidden, null, { timeout: 2000 }).catch(() => {});
  let linked = false;
  for (let i = 0; i < 30 && !linked; i++) {
    await page.click('#pagesBtn');
    linked = await page.waitForSelector(`${menu} [data-action^="linked:"][data-action$="/out/voice.wav"]`, { timeout: 1000 }).then(() => true).catch(() => false);
    await page.keyboard.press('Escape');
    if (!linked) await wait(500);
  }
  ok(linked, 'a file the agent named is listed among the pages as linked');

  // What the agent never named is not served, however it is asked for.
  const base = s.url + '/api/chats/' + encodeURIComponent(chat) + '/file?path=';
  ok((await s.context.request.get(base + encodeURIComponent('out/secret.txt'))).status() === 404, 'a file of the folder the agent never named is not served');
  ok((await s.context.request.get(base + encodeURIComponent('/etc/passwd'))).status() === 404, 'nor any other file of the machine');
  ok((await s.context.request.get(base + encodeURIComponent('out/voice.wav'))).status() === 200, 'what it named is');

  // On a phone the file is a full screen dialog, as a page is.
  // The file open in the column goes along into the dialog.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForSelector('dialog.so-docside-dialog[open] audio.so-fileview__audio', { timeout: 8000 }).catch(() => {});
  ok(await page.isVisible('dialog.so-docside-dialog[open] audio.so-fileview__audio'), 'a narrower window takes the file into a full screen dialog');
  // Back: the focused button's tooltip takes the first Escape.
  await page.click('dialog.so-docside-dialog[open] [data-ds-close].ds-btn');
  await page.waitForSelector('dialog.so-docside-dialog', { state: 'detached', timeout: 5000 }).catch(() => {});
  await page.click('#pagesBtn');
  await page.waitForSelector(`${menu} [data-action="page:demo.webm"]`, { timeout: 5000 });
  await page.click(`${menu} [data-action="page:demo.webm"]`);
  await page.waitForSelector('dialog.so-docside-dialog[open] video.so-fileview__video', { timeout: 8000 }).catch(() => {});
  ok(await page.isVisible('dialog.so-docside-dialog[open] video.so-fileview__video'), 'on a phone a video plays in a full screen dialog');
  await wait(800);
  await shot(page, 'pages-video-phone');
  await page.keyboard.press('Escape');
}
