// boards: a project folder's notes (internal/server/boards.go, boards.js).
// A folder's ⋮ beside its + has Notes, which opens its board on the stage at
// #board=<folder>: the default columns and colours, a card added with a
// title, a subtitle and a colour, a column added, a card dragged into
// another column, the colours renamed, recoloured and one added, a card
// moved in its dialog, a column moved in its dialog and dragged by its
// head, a column deleted with its cards. Everything is the
// server's after a reload; another folder has a board of its own; another
// device's save shows up live; and on a phone the columns scroll sideways
// one by one and the page itself does not.

import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { start, setup, ok, wait, shot } from './harness.mjs';
import { signIn } from './chattests.mjs';

async function folderChat(s, title, workdir) {
  const res = await s.context.request.post(s.url + '/api/chats', {
    data: { client_id: 'e2e-' + Math.random().toString(36).slice(2), harness: 'claude', workdir_mode: 'custom', workdir, title },
  });
  if (res.status() !== 201) throw new Error('could not make the chat: ' + res.status() + ' ' + await res.text());
  return (await res.json()).session.id;
}

// openNotes opens a folder's notes from its ⋮ in the sidebar.
async function openNotes(page, folder) {
  await page.click(`#sessionList .so-folder__more[data-folder-more="${folder}"]`);
  await page.click('#sessionMenu:not([hidden]) [data-action="notes"]');
}

async function boardOf(s, folder) {
  return (await s.context.request.get(s.url + '/api/boards?folder=' + encodeURIComponent(folder))).json();
}

// columns is the board as the page shows it: each column's name and its
// cards' titles and tags.
const columns = (page) => page.$$eval('#board .so-board__col[data-col]', (cols) => cols.map((col) => ({
  name: col.querySelector('.so-board__col-btn').textContent.trim(),
  cards: [...col.querySelectorAll('.so-board__card')].map((card) => ({
    title: card.querySelector('.so-board__card-title').textContent.trim(),
    tag: (card.querySelector('.so-board__tag') || { textContent: '' }).textContent.trim(),
  })),
})));

const titles = (cols, name) => (cols.find((c) => c.name === name) || { cards: [] }).cards.map((c) => c.title);

async function dialogGone(page) {
  await page.waitForFunction(() => !document.querySelector('dialog[open]'), null, { timeout: 5000 });
}

async function addCard(page, column, { title, subtitle = '', colour = '' }) {
  await page.click(`#board .so-board__col:has(.so-board__col-btn:text-is("${column}")) .so-board__add`);
  await page.waitForSelector('dialog[open] input[name="title"]');
  await page.fill('dialog[open] input[name="title"]', title);
  if (subtitle) await page.fill('dialog[open] textarea[name="subtitle"]', subtitle);
  if (colour) await page.click(`dialog[open] .ds-chip:has(.ds-chip__label:text-is("${colour}"))`);
  await page.click('dialog[open] button[type="submit"]');
  await dialogGone(page);
}

// drag moves a card by the mouse onto a column, below its last card.
async function drag(page, title, column) {
  const card = await page.$(`#board .so-board__card:has(.so-board__card-title:text-is("${title}")) .so-board__card-btn`);
  const from = await card.boundingBox();
  const list = await page.$(`#board .so-board__col:has(.so-board__col-btn:text-is("${column}")) .so-board__cards`);
  const to = await list.boundingBox();
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  const steps = 12;
  for (let i = 1; i <= steps; i += 1) {
    await page.mouse.move(
      from.x + from.width / 2 + (to.x + to.width / 2 - from.x - from.width / 2) * i / steps,
      from.y + from.height / 2 + (to.y + to.height - 8 - from.y - from.height / 2) * i / steps,
    );
    await wait(20);
  }
  await page.mouse.up();
}

// dragColumn moves a column by its head, the mouse let go at `x`.
async function dragColumn(page, column, x) {
  const head = await (await page.$(`#board .so-board__col-btn:text-is("${column}")`)).boundingBox();
  const y = head.y + head.height / 2;
  await page.mouse.move(head.x + 20, y);
  await page.mouse.down();
  const steps = 12;
  for (let i = 1; i <= steps; i += 1) {
    await page.mouse.move(head.x + 20 + (x - head.x - 20) * i / steps, y);
    await wait(20);
  }
  await page.mouse.up();
}

async function saved(s, folder, test, timeout = 8000) {
  const end = Date.now() + timeout;
  let data = null;
  while (Date.now() < end) {
    data = await boardOf(s, folder);
    if (test(data)) return data;
    await wait(150);
  }
  return data;
}

export async function boards() {
  const scratch = mkdtempSync(join(process.env.TMPDIR || tmpdir(), 'boards-'));
  const shop = join(scratch, 'shop');
  const blog = join(scratch, 'blog');
  mkdirSync(shop);
  mkdirSync(blog);
  const s = await start({ viewport: { width: 1280, height: 800 } });
  try {
    const { page } = s;
    await setup(page, s.url);
    await signIn(s);
    await folderChat(s, 'Checkout', shop);
    await folderChat(s, 'Posts', blog);

    // The folder's ⋮ beside its +, and Notes in its menu.
    const mark = `#sessionList .so-folder__more[data-folder-more="${shop}"]`;
    await page.waitForSelector(mark, { timeout: 20000 });
    const beside = await page.$eval(mark, (b) => b.previousElementSibling && b.previousElementSibling.classList.contains('so-folder__new'));
    ok(beside, 'the folder’s ⋮ stands right after its +');
    ok(await page.$eval(mark, (b) => b.getAttribute('aria-label')) === 'More for shop', 'the ⋮ says whose it is',
      await page.$eval(mark, (b) => b.getAttribute('aria-label')));
    await openNotes(page, shop);
    await page.waitForSelector('#board:not([hidden]) .so-board__col[data-col]', { timeout: 10000 });
    ok(await page.evaluate(() => decodeURIComponent(location.hash)) === '#board=' + shop, 'the address names the folder’s board',
      await page.evaluate(() => location.hash));
    ok(await page.$eval('#home', (h) => h.hidden), 'Home is off the stage');
    ok(await page.waitForFunction(() => document.getElementById('sessionMenu').hidden, null, { timeout: 5000 }).then(() => true, () => false),
      'the menu is put away');
    let cols = await columns(page);
    ok(cols.map((c) => c.name).join('|') === 'To do|In progress|Done', 'a new board has three columns', cols.map((c) => c.name).join('|'));
    const legend = await page.$$eval('#board .so-board__legend .so-board__tag', (t) => t.map((one) => one.textContent.trim()));
    ok(legend.join('|') === 'Bug|Idea|Feature', 'and named colours to start with', legend.join('|'));
    ok((await boardOf(s, shop)).rev === 0, 'nothing is stored before something is changed');

    // Cards.
    await addCard(page, 'To do', { title: 'Login fails on Safari', subtitle: 'Only on iOS 18.\nThe cookie is dropped.', colour: 'Bug' });
    await addCard(page, 'To do', { title: 'Dark mode', colour: 'Idea' });
    await addCard(page, 'To do', { title: 'Write the changelog' });
    cols = await columns(page);
    ok(titles(cols, 'To do').join('|') === 'Login fails on Safari|Dark mode|Write the changelog', 'cards are added at the end of their column',
      titles(cols, 'To do').join('|'));
    ok(cols[0].cards[0].tag === 'Bug' && cols[0].cards[1].tag === 'Idea' && cols[0].cards[2].tag === '',
      'a card wears its colour’s name, or none', cols[0].cards.map((c) => c.tag || '-').join('|'));
    const sub = await page.$eval('#board .so-board__card-sub', (n) => n.textContent);
    ok(sub === 'Only on iOS 18.\nThe cookie is dropped.', 'the subtitle keeps its lines', JSON.stringify(sub));
    let data = await saved(s, shop, (d) => d.rev >= 3 && d.board.columns[0].cards.length === 3);
    ok(data.board.columns[0].cards[0].label === 'bug', 'the server has the cards with their colours', JSON.stringify(data.board.columns[0].cards[0]));

    // A card needs a title: the dialog says so and stays.
    await page.click('#board .so-board__col:first-child .so-board__add');
    await page.waitForSelector('dialog[open] input[name="title"]');
    await page.click('dialog[open] button[type="submit"]');
    await wait(200);
    ok(await page.$eval('dialog[open] input[name="title"]', (i) => i.getAttribute('aria-invalid')) === 'true',
      'a card without a title is refused in its field');
    await page.keyboard.press('Escape');
    await dialogGone(page);

    // A column.
    await page.click('#board [data-board="add-column"]');
    await page.waitForSelector('dialog[open] input');
    await page.fill('dialog[open] input', 'Later');
    await page.keyboard.press('Enter');
    await dialogGone(page);
    cols = await columns(page);
    ok(cols.map((c) => c.name).join('|') === 'To do|In progress|Done|Later', 'a column is added at the end', cols.map((c) => c.name).join('|'));

    // A save the server took whose answer was lost: the retry finds its
    // own board there, keeps it, and blames nobody.
    {
      let cut = true;
      await page.route('**/api/boards', async (route) => {
        if (route.request().method() === 'PUT' && cut) {
          cut = false;
          await route.fetch();
          await route.abort('connectionreset');
          return;
        }
        await route.continue();
      });
      await addCard(page, 'Later', { title: 'Answer lost on the way' });
      await addCard(page, 'Later', { title: 'Made while it retried' });
      data = await saved(s, shop, (d) => (d.board.columns[3]?.cards || []).length === 2, 15000);
      ok((data.board.columns[3]?.cards || []).map((k) => k.title).join('|') === 'Answer lost on the way|Made while it retried',
        'a save whose answer was lost is not taken for another device\u2019s, and what came after it is saved too',
        JSON.stringify(data.board.columns[3]));
      const told = await page.$$eval('.ds-toast', (t) => t.map((one) => one.textContent).join(' | '));
      ok(!/another device/.test(told), 'and nobody is blamed for it', told || 'no message');
      await page.unroute('**/api/boards');
      // The two cards go again, by their dialogs, so the rest reads as before.
      for (const title of ['Answer lost on the way', 'Made while it retried']) {
        await page.click(`#board .so-board__card-btn:has(.so-board__card-title:text-is("${title}"))`);
        await page.waitForSelector('dialog[open] .so-board__delete');
        await page.click('dialog[open] .so-board__delete');
        await page.waitForSelector('dialog[role="alertdialog"][open]');
        await page.click('dialog[role="alertdialog"][open] .ds-btn--primary');
        await dialogGone(page);
      }
    }

    // A drag. The row of columns has scrolled to the new one at its end:
    // it is let settle and put back to its start first.
    await wait(600);
    await page.$eval('#board .so-board__columns', (r) => { r.scrollLeft = 0; });
    await wait(100);
    await drag(page, 'Dark mode', 'In progress');
    await wait(400);
    cols = await columns(page);
    ok(titles(cols, 'In progress').join('|') === 'Dark mode' && titles(cols, 'To do').join('|') === 'Login fails on Safari|Write the changelog',
      'a card dragged into another column lands there', JSON.stringify(cols.map((c) => c.cards.map((k) => k.title))));
    ok(!(await page.$('dialog[open]')), 'the release of a drag opens no card');
    data = await saved(s, shop, (d) => d.board.columns[1].cards.some((k) => k.title === 'Dark mode'));
    ok(data.board.columns[1].cards[0].title === 'Dark mode', 'the server has the card where it was dropped');
    await drag(page, 'Write the changelog', 'To do');
    await wait(300);
    // Up within the same column, onto the first card's upper half.
    {
      const first = await (await page.$('#board .so-board__col:first-child .so-board__card:first-child')).boundingBox();
      const card = await (await page.$('#board .so-board__card:has(.so-board__card-title:text-is("Write the changelog")) .so-board__card-btn')).boundingBox();
      await page.mouse.move(card.x + 40, card.y + card.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 10; i += 1) {
        await page.mouse.move(card.x + 40, card.y + card.height / 2 + (first.y + 4 - card.y - card.height / 2) * i / 10);
        await wait(20);
      }
      await page.mouse.up();
      await wait(400);
    }
    cols = await columns(page);
    ok(titles(cols, 'To do').join('|') === 'Write the changelog|Login fails on Safari', 'a card dragged up its own column goes before the one it was dropped on',
      titles(cols, 'To do').join('|'));

    // The colours: renamed, recoloured, one added.
    await page.click('#board [data-board="labels"]');
    await page.waitForSelector('dialog[open] .so-board__label-row');
    const rows = await page.$$('dialog[open] .so-board__label-row');
    ok(rows.length === 3, 'the colours dialog lists the folder’s colours', String(rows.length));
    await rows[1].$eval('input', (i) => { i.value = ''; });
    await (await rows[1].$('input')).fill('Idee');
    await (await rows[1].$('select')).selectOption('yellow');
    await page.click('dialog[open] .ds-add-row');
    const added = (await page.$$('dialog[open] .so-board__label-row')).at(-1);
    await (await added.$('input')).fill('Chore');
    await (await added.$('select')).selectOption('grey');
    await wait(400);
    await shot(page, 'boards-colours');
    await page.click('dialog[open] button[type="submit"]');
    await dialogGone(page);
    cols = await columns(page);
    const dark = cols.find((c) => c.name === 'In progress').cards[0];
    ok(dark.tag === 'Idee', 'a renamed colour is renamed on its cards', dark.tag);
    ok(await page.$eval('#board .so-board__card:has(.so-board__card-title:text-is("Dark mode"))', (c) => c.dataset.color) === 'yellow',
      'and recoloured');
    const legend2 = await page.$$eval('#board .so-board__legend .so-board__tag', (t) => t.map((one) => one.textContent.trim()));
    ok(legend2.join('|') === 'Bug|Idee|Feature|Chore', 'an added colour is in the legend', legend2.join('|'));

    // A card moved and recoloured in its dialog.
    await page.click('#board .so-board__card-btn:has(.so-board__card-title:text-is("Login fails on Safari"))');
    await page.waitForSelector('dialog[open] select[name="column"]');
    ok(await page.$eval('dialog[open] input[name="title"]', (i) => i.value) === 'Login fails on Safari', 'a card opens with its own title');
    await page.selectOption('dialog[open] select[name="column"]', { label: 'Done' });
    await page.click('dialog[open] .ds-chip:has(.ds-chip__label:text-is("Chore"))');
    await wait(400);
    await shot(page, 'boards-card');
    await page.click('dialog[open] button[type="submit"]');
    await dialogGone(page);
    cols = await columns(page);
    ok(titles(cols, 'Done').join('|') === 'Login fails on Safari' && cols.find((c) => c.name === 'Done').cards[0].tag === 'Chore',
      'a card’s dialog moves it to another column and gives it another colour', JSON.stringify(cols.find((c) => c.name === 'Done')));
    await wait(500);
    await shot(page, 'boards-desk');

    // A column renamed and moved to the front.
    await page.click('#board .so-board__col-btn:text-is("Later")');
    await page.waitForSelector('dialog[open] select');
    await page.fill('dialog[open] input', 'Someday');
    await page.selectOption('dialog[open] select', '0');
    await page.click('dialog[open] button[type="submit"]');
    await dialogGone(page);
    cols = await columns(page);
    ok(cols.map((c) => c.name).join('|') === 'Someday|To do|In progress|Done', 'a column is renamed and moved', cols.map((c) => c.name).join('|'));

    // A column dragged by its head past In progress, and back to the front.
    {
      // The column's dialog may still be on its way out, its backdrop over
      // the board.
      await page.waitForFunction(() => !document.querySelector('dialog.ds-dialog--closing'), null, { timeout: 5000 });
      await page.$eval('#board .so-board__columns', (r) => { r.scrollLeft = 0; });
      await wait(100);
      const past = await (await page.$('#board .so-board__col:has(.so-board__col-btn:text-is("In progress"))')).boundingBox();
      await dragColumn(page, 'Someday', past.x + past.width * 0.75);
      await wait(400);
      cols = await columns(page);
      ok(cols.map((c) => c.name).join('|') === 'To do|In progress|Someday|Done', 'a column dragged by its head lands where it is let go',
        cols.map((c) => c.name).join('|'));
      ok(!(await page.$('dialog[open]')), 'the release of a column’s drag opens no dialog');
      ok(await page.$eval('#board .so-board__columns > :last-child', (c) => c.classList.contains('so-board__col--new')), '"Add a column" stays last');
      ok(!(await page.$('#board [data-lifted], .so-board__ghost')), 'nothing is left lifted after the drop');
      data = await saved(s, shop, (d) => d.board.columns[2].title === 'Someday');
      ok(data.board.columns.map((c) => c.title).join('|') === 'To do|In progress|Someday|Done', 'the server has the columns in the new order',
        data.board.columns.map((c) => c.title).join('|'));
      ok(data.board.columns.find((c) => c.title === 'In progress').cards[0].title === 'Dark mode', 'a moved column keeps its cards and the others theirs');
      const first = await (await page.$('#board .so-board__col[data-col]')).boundingBox();
      await dragColumn(page, 'Someday', first.x + 10);
      await wait(400);
      cols = await columns(page);
      ok(cols.map((c) => c.name).join('|') === 'Someday|To do|In progress|Done', 'and dragged back to the front', cols.map((c) => c.name).join('|'));
    }

    // After a reload, from the server.
    data = await saved(s, shop, (d) => d.board.columns[0].title === 'Someday');
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#board:not([hidden]) .so-board__col[data-col]', { timeout: 15000 });
    const again = await columns(page);
    ok(JSON.stringify(again) === JSON.stringify(cols), 'a reload comes back to the same board', JSON.stringify(again.map((c) => c.name)));

    // Another device saves: this page follows.
    const next = JSON.parse(JSON.stringify(data.board));
    next.columns[1].cards.push({ id: 'from-phone', title: 'Added on the phone', subtitle: '', label: '' });
    const put = await s.context.request.put(s.url + '/api/boards', { data: { folder: shop, base_rev: data.rev, board: next } });
    ok(put.status() === 200, 'another device saves the board', String(put.status()));
    await page.waitForSelector('#board .so-board__card-title:text-is("Added on the phone")', { timeout: 8000 }).catch(() => {});
    ok(titles(await columns(page), 'To do').includes('Added on the phone'), 'the page shows another device’s save by itself');

    // Another folder has notes of its own.
    await openNotes(page, blog);
    await page.waitForFunction((want) => decodeURIComponent(location.hash) === '#board=' + want
      && document.querySelector('#board .so-board__folder')?.textContent === 'blog'
      && document.querySelectorAll('#board .so-board__col[data-col]').length === 3, blog, { timeout: 10000 });
    cols = await columns(page);
    ok(cols.every((c) => c.cards.length === 0), 'another folder’s board is its own', JSON.stringify(cols));
    await page.goBack();
    await page.waitForFunction((want) => document.querySelector('#board .so-board__folder')?.textContent === 'shop', shop, { timeout: 10000 });
    ok(true, 'Back returns to the first folder’s notes');

    // A column deleted with its cards, after asking.
    await page.click('#board .so-board__col-btn:text-is("Done")');
    await page.waitForSelector('dialog[open] .so-board__delete');
    await page.click('dialog[open] .so-board__delete');
    await page.waitForSelector('dialog[role="alertdialog"][open]');
    await page.click('dialog[role="alertdialog"][open] .ds-btn--primary');
    await page.waitForFunction(() => ![...document.querySelectorAll('#board .so-board__col-btn')].some((b) => b.textContent.trim() === 'Done'), null, { timeout: 5000 });
    data = await saved(s, shop, (d) => !d.board.columns.some((c) => c.title === 'Done'));
    ok(!data.board.columns.some((c) => c.title === 'Done'), 'a deleted column is gone on the server too');

    // Home takes the stage back.
    await page.click('#homeBtn');
    const homeBack = await page.waitForFunction(() => !document.getElementById('home').hidden && document.getElementById('board').hidden, null, { timeout: 5000 })
      .then(() => true, () => false);
    ok(homeBack, 'Home takes the notes off the stage');

    // A phone.
    const phone = await s.browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, colorScheme: 'light' });
    const cookies = await s.context.cookies();
    await phone.addCookies(cookies);
    const p2 = await phone.newPage();
    await p2.goto(s.url + '/#board=' + encodeURIComponent(shop), { waitUntil: 'domcontentloaded' });
    await p2.waitForSelector('#board:not([hidden]) .so-board__col[data-col]', { timeout: 15000 });
    await wait(300);
    const sideways = await p2.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    ok(sideways <= 0, 'on a phone the page itself does not scroll sideways', String(sideways));
    const row = await p2.$eval('#board .so-board__columns', (r) => ({ scroll: r.scrollWidth, width: r.clientWidth }));
    ok(row.scroll > row.width, 'the columns scroll sideways instead', row.scroll + ' > ' + row.width);
    const colWidth = await p2.$eval('#board .so-board__col[data-col]', (c) => c.getBoundingClientRect().width);
    ok(colWidth <= 390 * 0.85 + 1, 'a column fits the phone with the next one peeking in', String(colWidth));
    await shot(p2, 'boards-phone');
    await phone.close();
  } finally {
    await s.stop();
    rmSync(scratch, { recursive: true, force: true });
  }
}
