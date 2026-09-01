// The end-to-end suite. Every scenario drives the real binary through a real
// browser, against a real detached agent host, and every assertion prints the
// value it measured beside its verdict.
//
//   make e2e                     the whole suite
//   node e2e/run.mjs             the same
//   node e2e/run.mjs streaming   one scenario, by name
//
// See e2e/README.md for what it needs and where the artefacts land.

import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  start, setup, shot, ok, scenario, skipScenario, finish, ensureNav, wait,
  REPO, OUT, PASSWORD, LONG_SCRIPT, LIVE,
} from './harness.mjs';

// The one console error this environment produces on its own: a machine
// without Piper answers the prefetch of the spoken offline notice with a 503.
// §10.3 says that path is tolerated - and it is tolerated by where it came
// from, not by its wording, so a 503 from /messages during a shutdown drain
// can never hide behind it.
const EXPECTED_ERROR = /503 \(Service Unavailable\) @ .*\/api\/voice\/speak$/;
// A browser that has been switched offline reports its own failed requests.
// They are the point of those scenarios, not a defect in the page.
const OFFLINE_NOISE = /ERR_INTERNET_DISCONNECTED|ERR_NETWORK_CHANGED|ERR_FAILED|Failed to fetch/;
// And a server that is taken away mid-stream leaves its SSE response truncated
// and its next request refused. Only the restart scenario tolerates these: the
// offline ones must not, because there the transport goes and the server stays.
const RESTART_NOISE = new RegExp(OFFLINE_NOISE.source
  + '|ERR_INCOMPLETE_CHUNKED_ENCODING|ERR_CONNECTION_REFUSED|ERR_EMPTY_RESPONSE|ERR_CONNECTION_RESET');
const unexpected = (errors, extra) =>
  errors.filter((e) => !EXPECTED_ERROR.test(e) && !(extra && extra.test(e)));

const idle = (page, t = 30000) =>
  page.waitForFunction(() => !document.body.classList.contains('busy'), null, { timeout: t });
const busy = (page, t = 10000) =>
  page.waitForFunction(() => document.body.classList.contains('busy'), null, { timeout: t });

// pickModel chooses a model in the new-chat sheet from the keyboard. It is the
// keyboard and not the mouse on purpose: tapping an option cancels the sheet,
// which is what the quarantined `modelpick` scenario below documents.
async function pickModel(page, container, model) {
  const input = page.locator(container + ' input');
  await input.click();
  await input.fill(model);
  await page.waitForFunction((sel) => {
    const list = document.querySelector(sel + ' .combo-list');
    return list && !list.hidden && list.querySelector('.combo-option');
  }, container, { timeout: 5000 });
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Enter');
}

async function openSheetAndStart(page, { model, effort } = {}) {
  await ensureNav(page);
  await page.click('#newChat');
  await page.waitForSelector('#newChatSheet[open]');
  if (model) await pickModel(page, '#ncModel', model);
  if (effort) await page.click(`#ncEffort .seg[data-value="${effort}"]`);
  await page.click('#ncStart');
  await page.waitForSelector('#newChatSheet[open]', { state: 'detached', timeout: 5000 }).catch(() => {});
}

async function send(page, text) {
  await page.fill('#input', text);
  await page.click('#sendBtn');
}

// What the transcript looks like, counted. Every offline and restart scenario
// compares this against the server's own view.
const census = (page) => page.evaluate(() => {
  const ids = [...document.querySelectorAll('[data-step]')].map((n) => n.dataset.step);
  const msgs = [...document.querySelectorAll('[data-msg]')].map((n) => n.dataset.msg);
  return {
    assistant: document.querySelectorAll('.msg.assistant').length,
    user: document.querySelectorAll('.msg.user').length,
    pending: document.querySelectorAll('.msg.user.pending').length,
    draft: document.querySelectorAll('.step.text-step').length,
    tool: document.querySelectorAll('.step.tool-step').length,
    reasoning: document.querySelectorAll('.step.collapsible:not(.tool-step)').length,
    usage: document.querySelectorAll('.step.usage-step').length,
    notice: document.querySelectorAll('.step.notice-step').length,
    stepNodes: ids.length,
    distinctSteps: new Set(ids).size,
    msgNodes: msgs.length,
    distinctMsgs: new Set(msgs).size,
    spinning: [...document.querySelectorAll('.step .spinner')].filter((n) => !n.hidden).length,
    turns: document.querySelectorAll('.turn').length,
    stale: document.body.classList.contains('stale'),
    busy: document.body.classList.contains('busy'),
    workLabel: (document.querySelector('.working-label') || {}).textContent || '',
  };
});

const serverView = (page, id) => page.evaluate(async (chatId) => {
  const data = await (await fetch('/api/chats/' + chatId)).json();
  return {
    assistant: data.messages.filter((m) => m.role === 'assistant').length,
    user: data.messages.filter((m) => m.role === 'user').length,
    userTexts: data.messages.filter((m) => m.role === 'user').map((m) => m.content),
    steps: data.steps.length,
    stepIds: data.steps.map((s) => s.id),
    running: data.steps.filter((s) => s.status === 'running').map((s) => s.kind + ':' + s.id),
    statuses: data.steps.map((s) => s.kind + '=' + s.status),
    busy: data.busy,
    chat: data.chat,
  };
}, id);

// Unbinding a chat's agent is the one thing no endpoint can do, and on purpose
// (§6.3): a different agent is a different conversation. So a chat that
// predates the rewrite is made the only way it can exist - in the database.
const LEGACY_TOOL = `package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "file:"+os.Args[1]+"?_pragma=busy_timeout(5000)")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE chats SET agent='', model='', effort='' WHERE id=?", os.Args[2]); err != nil {
		panic(err)
	}
	fmt.Println("unbound", os.Args[2])
}
`;

function unbind(dbPath, id) {
  const dir = join(OUT, 'legacytool');
  mkdirSync(dir, { recursive: true });
  const src = join(dir, 'main.go');
  writeFileSync(src, LEGACY_TOOL);
  // Run from the repository so the module's own modernc.org/sqlite is used,
  // but from a file outside it so nothing is written into the worktree.
  return execFileSync('go', ['run', src, dbPath, id], { cwd: REPO }).toString();
}

// --------------------------------------------------------------- 1. newchat

async function newchat() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await ensureNav(s.page);
    await s.page.click('#newChat');
    await s.page.waitForSelector('#newChatSheet[open]');
    await shot(s.page, 'newchat-sheet');

    const agentButtons = await s.page.$$eval('#ncAgent .seg', (nodes) => nodes.map((n) => ({
      value: n.dataset.value, on: n.classList.contains('on'), disabled: n.disabled, text: n.textContent,
    })));
    ok(agentButtons.length === 3, 'the sheet offers all three agents', agentButtons.map((a) => a.value).join(','));
    ok(agentButtons[0].on && agentButtons[0].value === 'claude',
      'the first usable agent is preselected', JSON.stringify(agentButtons[0]));

    const model = await s.page.inputValue('#ncModel input');
    ok(model === 'sonnet', "the model starts on Claude's default", model);

    const effortShown = await s.page.isVisible('#ncEffortField');
    ok(effortShown, 'the effort control is shown for a model that has efforts', String(effortShown));

    await s.page.click('#ncEffort .seg[data-value="medium"]');
    const pressed = await s.page.$eval('#ncEffort .seg.on', (n) => n.dataset.value);
    ok(pressed === 'medium', 'the chosen effort is the pressed one', pressed);

    await s.page.click('#ncStart');
    await s.page.waitForSelector('#newChatSheet[open]', { state: 'detached' }).catch(() => {});

    // The chat does not exist until the first message is delivered, which is
    // what makes the offline case work - so the badge is proven after a send.
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('#chatAgent:not([hidden])', { timeout: 15000 });
    await s.page.waitForFunction(() => document.getElementById('chatAgent').textContent.includes('·'));
    const badge = await s.page.textContent('#chatAgent');
    ok(badge === 'Claude Code · Sonnet · medium', 'the header badge reads agent · model · effort', badge);

    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 20000 });
    const chatId = await s.page.evaluate(() => location.hash.slice(1));
    const chat = await s.page.evaluate(async (id) => (await (await fetch('/api/chats/' + id)).json()).chat, chatId);
    ok(chat.agent === 'claude' && chat.model === 'sonnet' && chat.effort === 'medium',
      'the server stored the binding the sheet produced', `${chat.agent}/${chat.model}/${chat.effort}`);

    // Mobile first: the chat's own name has to survive beside the badge. A
    // real, long title is given to it, so what is measured is the room the
    // title is allowed rather than the width of the words "New chat".
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    await s.page.evaluate(async (id) => {
      await fetch('/api/chats/' + id, {
        method: 'PATCH',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ title: 'Fix the failing store tests and explain what was wrong' }),
      });
    }, chatId);
    await s.page.waitForFunction(() => document.getElementById('chatTitle').textContent.startsWith('Fix the'),
      null, { timeout: 15000 });
    await wait(400);
    const bar = await s.page.evaluate(() => {
      const title = document.getElementById('chatTitle');
      const badgeEl = document.getElementById('chatAgent');
      const seen = (sel) => {
        const n = badgeEl.querySelector(sel);
        return n ? getComputedStyle(n).display !== 'none' : false;
      };
      return {
        titleText: title.textContent,
        titleWidth: title.getBoundingClientRect().width,
        titleVisible: getComputedStyle(title).visibility,
        badgeWidth: Math.round(badgeEl.getBoundingClientRect().width),
        agentPart: seen('.b-agent'),
        modelPart: seen('.b-model'),
        badgeTitle: badgeEl.title,
        overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      };
    });
    ok(bar.titleWidth >= 111 && bar.titleVisible === 'visible',
      'the chat title keeps at least 111 px beside the badge at 390',
      `${bar.titleWidth.toFixed(1)} px "${bar.titleText}" (${bar.titleVisible})`);
    ok(bar.agentPart === false && bar.modelPart === true,
      'the badge drops the agent name and keeps the model at 390',
      JSON.stringify({ agent: bar.agentPart, model: bar.modelPart, badgeWidth: bar.badgeWidth }));
    ok(bar.badgeTitle === 'Claude Code · Sonnet · medium',
      'and the whole binding stays in the title attribute', bar.badgeTitle);
    ok(bar.overflow <= 1, 'the top bar still does not scroll sideways', bar.overflow + 'px');
    await shot(s.page, 'newchat-badge');
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// ------------------------------------------------------------- 2. streaming

async function streaming() {
  const s = await start();
  try {
    // The console is watched with its locations, so the shape of the one
    // tolerated error is checked rather than assumed.
    const consoleSeen = [];
    s.page.on('console', (msg) => {
      if (msg.type() === 'error') consoleSeen.push({ text: msg.text(), url: (msg.location() || {}).url || '' });
    });
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, { effort: 'medium' });
    // The draft is removed at the end of the turn, so its growth is recorded
    // as it happens rather than sampled afterwards.
    await s.page.evaluate(() => {
      window.__draft = [];
      new MutationObserver(() => {
        const node = document.querySelector('.step.text-step');
        const len = node ? node.textContent.length : 0;
        if (window.__draft[window.__draft.length - 1] !== len) window.__draft.push(len);
      }).observe(document.getElementById('threadInner'), { subtree: true, childList: true, characterData: true });
    });
    await send(s.page, 'Run the tests.');

    await s.page.waitForSelector('.step.text-step', { timeout: 15000 });
    await s.page.waitForSelector('.step.tool-step', { timeout: 15000 });
    await shot(s.page, 'streaming-draft-and-tool');

    const toolHead = await s.page.$eval('.step.tool-step > .head', (n) => ({
      tag: n.querySelector('.tag').textContent,
      name: n.querySelector('.name').textContent,
      val: n.querySelector('.val').textContent,
    }));
    ok(toolHead.tag === 'tool' && toolHead.name === 'Bash' && toolHead.val === 'go test ./...',
      'the tool card shows its tag, name and command', JSON.stringify(toolHead));

    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    await wait(600);

    const counts = await s.page.evaluate(() => ({
      assistant: document.querySelectorAll('.msg.assistant').length,
      draft: document.querySelectorAll('.step.text-step').length,
      tool: document.querySelectorAll('.step.tool-step').length,
      reasoning: document.querySelectorAll('.step.collapsible:not(.tool-step)').length,
      usage: document.querySelectorAll('.step.usage-step').length,
      notice: document.querySelectorAll('.step.notice-step').length,
      usageText: (document.querySelector('.step.usage-step') || {}).textContent || '',
      noticeText: (document.querySelector('.step.notice-step') || {}).textContent || '',
      tags: [...document.querySelectorAll('.step.tool-step .tag')].map((n) => n.textContent),
      icons: [...document.querySelectorAll('.step.tool-step .step-icon')].map((n) => n.className),
    }));
    const draftSteps = await s.page.evaluate(() => window.__draft);
    const grew = draftSteps.filter((n) => n > 0);
    ok(grew.length >= 2 && Math.max(...grew) > grew[0],
      'the draft appeared and grew while the text streamed', 'lengths seen: ' + draftSteps.join(' -> '));
    ok(draftSteps[draftSteps.length - 1] === 0, 'and was removed at the end of the turn',
      'last length ' + draftSteps[draftSteps.length - 1]);

    ok(counts.assistant === 1, 'exactly one assistant message at the end of the turn', String(counts.assistant));
    ok(counts.draft === 0, 'no draft step is left behind', String(counts.draft));
    ok(counts.tool === 2 && counts.tags.join(',') === 'tool,agent',
      'the tool and the subagent both render, with their own tags', counts.tags.join(','));
    ok(counts.reasoning === 1, 'the reasoning step renders collapsed', String(counts.reasoning));
    ok(counts.icons.every((c) => c.includes('tick')), 'both cards ended on a done icon', counts.icons.join(' | '));
    ok(counts.usage === 1 && counts.usageText === '100 in · 20 out · $0.001',
      'the usage line renders the numbers it was given', counts.usageText);
    ok(counts.notice === 1 && counts.noticeText === 'The model was restarted once.',
      'the notice renders its one line', counts.noticeText);

    await s.page.click('.step.tool-step > .head');
    await wait(150);
    const body = await s.page.$eval('.step.tool-step .body', (n) => ({
      visible: getComputedStyle(n).display !== 'none', text: n.textContent,
    }));
    ok(body.visible && body.text.includes('ok  github.com/example/store'),
      'opening the tool card shows the output it captured', JSON.stringify(body).slice(0, 120));
    await shot(s.page, 'streaming-final');

    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
    ok(consoleSeen.every((e) => /voice\/speak/.test(e.url)),
      'every tolerated console error came from /api/voice/speak',
      consoleSeen.map((e) => e.url || e.text).join(',') || 'none at all');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------- 3. twoturns

async function twoturns() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);

    const first = await s.page.textContent('.msg.assistant');
    ok(first.trim().endsWith('Shall I commit this?'), 'the first turn ends on a question', first.trim().slice(-24));

    const composer = await s.page.$eval('#sendBtn', (n) => ({ disabled: n.disabled, stop: n.classList.contains('stop') }));
    ok(!composer.stop, 'the composer is a send button again between turns', JSON.stringify(composer));

    await send(s.page, 'Yes, commit it.');
    await s.page.waitForFunction(() => document.querySelectorAll('.msg.assistant').length === 2, null, { timeout: 25000 });
    await idle(s.page);
    await wait(400);

    const order = await s.page.$$eval('.thread-inner .msg, .thread-inner .turn', (nodes) => nodes.map((n) => {
      if (n.classList.contains('msg')) return (n.classList.contains('user') ? 'user:' : 'assistant:') + n.textContent.trim().slice(0, 18);
      return 'turn';
    }));
    ok(order.filter((o) => o.startsWith('user:')).length === 2, 'both questions are in the transcript', order.join(' / '));
    ok(order.filter((o) => o.startsWith('assistant:')).length === 2, 'both answers are in the transcript', String(order.length));
    const seq = await s.page.$$eval('.thread-inner > *', (nodes) =>
      nodes.map((n) => n.className.split(' ')[0] + (n.classList.contains('user') ? '.user' : '')));
    ok(seq.join(',') === 'msg.user,turn,msg.user,turn', 'the two turns render in the order they happened', seq.join(','));
    await shot(s.page, 'twoturns');
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// ------------------------------------------------------------ 4. audioturns

async function audioturns() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    // Every 'ready' frame also re-primes the spoken offline notice, which is
    // the same endpoint, so the answer is counted by what was asked for.
    const speaks = [];
    s.page.on('request', (r) => {
      if (r.url().endsWith('/api/voice/speak') && r.method() === 'POST') {
        speaks.push({ at: Date.now(), body: String(r.postData() || '') });
      }
    });
    await s.page.click('.view-slider .stop[data-view="auto"]');
    await s.page.waitForFunction(() => document.body.classList.contains('auto'));

    // The composer is hidden in audio mode, so a message is sent the way the
    // microphone sends it.
    const say = (text) => s.page.evaluate((t) => {
      const input = document.getElementById('input');
      input.value = t;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      document.getElementById('composer').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    }, text);

    await s.page.evaluate(() => {
      window.__live = [];
      new MutationObserver(() => {
        const t = document.getElementById('autoLive').textContent;
        if (t && window.__live[window.__live.length - 1] !== t) window.__live.push(t);
      }).observe(document.getElementById('autoLive'), { childList: true, characterData: true, subtree: true });
    });

    await say('Run the tests.');
    await s.page.waitForSelector('#autoAnswer:not([hidden])', { timeout: 25000 });
    await idle(s.page);
    await wait(1200);

    const answerText = await s.page.textContent('#autoAnswer');
    ok(answerText.includes('The tests pass.') && answerText.includes('Let me look at the tests first.'),
      'the whole turn is shown as one spoken answer', JSON.stringify(answerText).slice(0, 110));
    const panels = await s.page.$$eval('#autoAnswer', (n) => n.length);
    ok(panels === 1, 'there is exactly one answer panel', String(panels));
    const autoBusy = await s.page.$eval('#autoBusy', (n) => n.hidden);
    ok(autoBusy === true, 'the busy indicator clears when the turn ends', 'hidden=' + autoBusy);
    const liveNow = await s.page.textContent('#autoLive');
    ok(liveNow === '', 'the live narration is cleared by the answer', JSON.stringify(liveNow));

    const turn1 = speaks.length;
    const answer1 = speaks.filter((x) => x.body.includes('The tests pass.')).length;
    ok(answer1 === 1, 'turn 1: the answer was sent to be spoken exactly once',
      `${answer1} of ${turn1} POST /api/voice/speak carried the answer`);
    const mic1 = await s.page.$eval('#autoMic', (n) => ({ disabled: n.disabled, busy: n.classList.contains('busy') }));
    ok(!mic1.disabled && !mic1.busy, 'turn 1: the recording button is usable after the answer', JSON.stringify(mic1));
    await shot(s.page, 'audio-turn1');

    await say('Yes, commit it.');
    await s.page.waitForFunction(() => document.querySelectorAll('.msg.assistant').length === 2, null, { timeout: 25000 });
    await idle(s.page);
    await wait(1200);

    const answer2 = speaks.slice(turn1).filter((x) => x.body.includes('The tests pass.')).length;
    ok(answer2 === 1, 'turn 2: the answer was sent to be spoken exactly once',
      `${answer2} of ${speaks.length - turn1} speak requests`);
    const intermediate = speaks.filter((x) => !x.body.includes('The tests pass.') && !x.body.includes('connection dropped'));
    ok(intermediate.length === 0, 'nothing intermediate was ever sent to be spoken',
      intermediate.map((x) => x.body.slice(0, 80)).join(' | ') || 'none');
    const spokenTexts = speaks.filter((x) => x.body.includes('The tests pass.'))
      .map((x) => { try { return JSON.parse(x.body).text; } catch { return x.body; } });
    const leaked = spokenTexts.filter((t) => /Ran a command|Bash|Reasoning|restarted once|failing test/.test(t));
    ok(leaked.length === 0, 'the spoken answer carries no tool title, reasoning or notice', leaked.join(' | ') || 'clean');
    ok(spokenTexts.every((t) => t.startsWith('Let me look at the tests first.')),
      'the spoken answer is the whole turn text', JSON.stringify(spokenTexts[0]).slice(0, 100));
    const live = await s.page.evaluate(() => window.__live);
    ok(live.length > 0, 'the live narration line changed while working (shown, never spoken)',
      live.join(' / ').slice(0, 160));
    const mic2 = await s.page.$eval('#autoMic', (n) => ({ disabled: n.disabled, busy: n.classList.contains('busy') }));
    ok(!mic2.disabled && !mic2.busy, 'turn 2: the recording button is usable again', JSON.stringify(mic2));
    await shot(s.page, 'audio-turn2');
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// ----------------------------------------------------------- 5. modelchange

async function modelchange() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, { effort: 'medium' });
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));

    await s.page.click('#chatSettings');
    await s.page.waitForSelector('#panelBinding:not([hidden])');
    const idleHint = await s.page.textContent('#panelBindingHint');
    ok(/answers in this chat/.test(idleHint), 'the popover says which agent this chat is bound to', idleHint.trim());
    const modelInput = s.page.locator('#panelModel input');
    await modelInput.click();
    await modelInput.fill('opus');
    await s.page.locator('.combo-option', { hasText: /^Opus/ }).first().click();
    await s.page.click('#panelEffort .seg[data-value="high"]');
    await shot(s.page, 'modelchange-picker');
    await s.page.click('#panelSave');
    await s.page.waitForFunction(() => document.getElementById('chatAgent').textContent.includes('Opus'),
      null, { timeout: 10000 });

    const after = await s.page.evaluate(async (chatId) => (await (await fetch('/api/chats/' + chatId)).json()).chat, id);
    ok(after.model === 'opus' && after.effort === 'high', 'the server took the new model and effort',
      `${after.model}/${after.effort}`);
    const badge = await s.page.textContent('#chatAgent');
    ok(badge === 'Claude Code · Opus · high', 'the header badge followed', badge);

    // While a turn is running the same change is a 409 - the one refusal that
    // passes on its own, and the only one this endpoint may answer with.
    await send(s.page, 'And again.');
    await busy(s.page);
    const busyPatch = await s.page.evaluate(async (chatId) => {
      const res = await fetch('/api/chats/' + chatId, {
        method: 'PATCH', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'haiku' }),
      });
      return { code: res.status, body: (await res.json()).error };
    }, id);
    ok(busyPatch.code === 409, 'a model change while busy is refused with a 409', String(busyPatch.code));
    ok(/between turns/.test(busyPatch.body), 'and says it can be done between turns', busyPatch.body);

    await s.page.click('#chatSettings');
    await s.page.waitForSelector('#panelBinding:not([hidden])');
    const busyShape = await s.page.evaluate(() => ({
      hint: document.getElementById('panelBindingHint').textContent,
      disabled: document.querySelector('#panelModel input').disabled,
      efforts: [...document.querySelectorAll('#panelEffort .seg')].every((n) => n.disabled),
    }));
    ok(busyShape.disabled && busyShape.efforts, 'the picker is dead while the chat is working', JSON.stringify(busyShape));
    ok(/only be changed between turns/.test(busyShape.hint), 'and says why', busyShape.hint.trim());
    await shot(s.page, 'modelchange-busy');
    // The 409 above is this probe's own doing, and Chrome logs it.
    const bad = unexpected(s.errors).filter((e) => !/409/.test(e));
    ok(bad.length === 0, 'no unexpected console errors', bad.join(' | ') || '0');
    await idle(s.page);
  } finally { await s.stop(); }
}

// ------------------------------------------------------------- 6. errorstep

async function errorstep() {
  const s = await start({
    // The adapter refuses to start at all, which is the one path that reaches
    // pump.fatal and writes an error step. A `die` mid-turn ends the turn with
    // outcome error instead, and that is a failed run rather than an error
    // step.
    script: JSON.stringify([{ do: 'failstart' }]),
  });
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    // The working row is transient, so what it said is recorded as it says it.
    await s.page.evaluate(() => {
      window.__labels = [];
      new MutationObserver(() => {
        const node = document.querySelector('.working-label');
        const text = node ? node.textContent : '';
        if (text && window.__labels[window.__labels.length - 1] !== text) window.__labels.push(text);
      }).observe(document.body, { subtree: true, childList: true, characterData: true });
    });
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.step.error-step', { timeout: 25000 });
    await idle(s.page);
    await wait(500);

    const shape = await s.page.evaluate(() => ({
      error: (document.querySelector('.step.error-step') || {}).textContent || '',
      drafts: document.querySelectorAll('.step.text-step').length,
      labels: window.__labels,
    }));
    ok(/could not answer/i.test(shape.error), 'the error renders in the transcript', shape.error.trim().slice(0, 60));
    ok(shape.drafts === 0, 'the draft is gone', String(shape.drafts));
    // §8.2: error -> step.title. It used to be the empty string, which left
    // the row saying whatever the step before it had said.
    ok(shape.labels.includes('The agent could not answer'),
      'the working row named the error while it was still up', shape.labels.join(' / '));
    await shot(s.page, 'errorstep');
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------- 7. stoptool

async function stoptool() {
  const script = JSON.stringify([
    { do: 'text', text: 'Starting the slow thing.' },
    { do: 'tool', name: 'Slow', input: 'sleep 60', output: 'never' },
    { do: 'sleep', ms: 12000 },
    { do: 'text', text: 'Done.' },
    { do: 'end', outcome: 'ok' },
  ]);
  const s = await start({ script, env: { SOCRATES_E2E_DROP_FINISH: 'Slow' } });
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Do the slow thing.');
    await s.page.waitForSelector('.step.tool-step', { timeout: 15000 });
    await wait(500);
    const before = await census(s.page);
    ok(before.spinning >= 1, 'the tool card is running before Stop', 'spinning=' + before.spinning);
    await shot(s.page, 'stoptool-running');

    const id = await s.page.evaluate(() => location.hash.slice(1));
    await s.page.click('#sendBtn'); // the send button is a Stop button while busy
    await idle(s.page, 15000);
    await wait(1200);

    const after = await census(s.page);
    const srv = await serverView(s.page, id);
    const icon = await s.page.$eval('.step.tool-step .step-icon',
      (n) => ({ cls: n.className, spinnerHidden: n.querySelector('.spinner').hidden }));
    await shot(s.page, 'stoptool-stopped');
    ok(after.spinning === 0, 'no card is still spinning after Stop (DOM)',
      'spinning=' + after.spinning + ' icon=' + JSON.stringify(icon));
    ok(srv.running.length === 0, 'no step is still running after Stop (server)', srv.statuses.join(','));
    ok(after.draft === 0, 'the draft is gone', String(after.draft));
    ok(after.assistant <= 1 && srv.assistant === after.assistant,
      'at most one assistant message, DOM agrees with server', `dom=${after.assistant} server=${srv.assistant}`);
    ok(!after.busy && !srv.busy, 'idle after Stop', JSON.stringify({ dom: after.busy, server: srv.busy }));
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------- 8. dropconn

async function dropconn() {
  const s = await start({ script: LONG_SCRIPT });
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.step.tool-step', { timeout: 15000 });
    const t0 = Date.now();
    await s.context.setOffline(true);
    await s.page.waitForFunction(() => { const b = document.querySelector('.conn-bar'); return b && !b.hidden; },
      null, { timeout: 6000 });
    const barAt = Date.now() - t0;
    await s.page.waitForFunction(() => document.body.classList.contains('stale'), null, { timeout: 6000 });
    const staleAt = Date.now() - t0;
    await wait(1200);
    const down = await census(s.page);
    const bar = (await s.page.textContent('.conn-bar')).trim();
    await shot(s.page, 'dropconn-down');
    ok(barAt < 3300, 'the connection bar appears within the grace period', barAt + ' ms; text=' + JSON.stringify(bar));
    ok(down.stale, 'body is marked stale while down', 'stale=' + down.stale + ' after ' + staleAt + ' ms');
    ok(/Reconnecting|last update/i.test(down.workLabel), 'the working row says it is reconnecting',
      JSON.stringify(down.workLabel));

    await s.context.setOffline(false);
    await s.page.waitForSelector('.msg.assistant', { timeout: 40000 });
    await idle(s.page, 40000);
    await wait(800);
    const after = await census(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));
    const srv = await serverView(s.page, id);
    const domSteps = await s.page.$$eval('[data-step]', (n) => n.map((x) => x.dataset.step).sort());
    ok(after.assistant === 1 && srv.assistant === 1, 'exactly one assistant message (DOM and server)',
      `dom=${after.assistant} server=${srv.assistant}`);
    ok(after.stepNodes === after.distinctSteps, 'no duplicated step node',
      `${after.stepNodes} nodes / ${after.distinctSteps} ids`);
    ok(after.msgNodes === after.distinctMsgs, 'no duplicated message node',
      `${after.msgNodes} nodes / ${after.distinctMsgs} ids`);
    ok(JSON.stringify(domSteps) === JSON.stringify([...srv.stepIds].sort()), 'DOM steps equal server steps',
      `dom=${domSteps.length} server=${srv.stepIds.length}`);
    ok(after.draft === 0 && after.spinning === 0 && !after.stale, 'no draft, no spinner, not stale at the end',
      JSON.stringify({ draft: after.draft, spinning: after.spinning, stale: after.stale }));
    ok(after.tool === 3, 'two tools and one subagent', String(after.tool));
    const exitMeta = await s.page.$$eval('.step.tool-step .meta', (n) => n.map((x) => ({ hidden: x.hidden, text: x.textContent })));
    ok(exitMeta.filter((m) => !m.hidden).length === 1 && exitMeta.some((m) => !m.hidden && m.text === 'exit 3'),
      'the exit code is shown only for the non-zero exit', JSON.stringify(exitMeta));
    const icons = await s.page.$$eval('.step.tool-step .step-icon', (n) => n.map((x) => x.className));
    ok(icons.filter((c) => c.includes('cross')).length === 1, 'the failed tool carries the cross', icons.join(' | '));
    await shot(s.page, 'dropconn-recovered');
    const bad = unexpected(s.errors, OFFLINE_NOISE);
    ok(bad.length === 0, 'no console errors beyond the failed requests being offline causes', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// --------------------------------------------------------------- 9. sigterm

async function sigterm() {
  // The one scenario that must NOT close its chats before its assertions:
  // §3.4 has SIGTERM leave the agent host running on purpose, and that is what
  // is being measured. stop() closes them at the very end, as it does for
  // every other scenario.
  const s = await start({ script: LONG_SCRIPT });
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.step.tool-step', { timeout: 15000 });
    const id = await s.page.evaluate(() => location.hash.slice(1));
    const t0 = Date.now();
    const outcome = await s.restart();
    const downFor = Date.now() - t0;
    ok(outcome !== 'timeout', 'the server exited on SIGTERM rather than being killed',
      `exit=${JSON.stringify(outcome)}, down for ${downFor} ms`);

    await s.page.waitForSelector('.msg.assistant', { timeout: 45000 }).catch(() => {});
    await idle(s.page, 45000).catch(() => {});
    await wait(1500);
    const after = await census(s.page);
    const srv = await serverView(s.page, id);
    await shot(s.page, 'sigterm-after-restart');
    ok(after.assistant === 1, 'exactly one assistant message in the DOM', String(after.assistant));
    ok(srv.assistant === 1, 'exactly one assistant message on the server', String(srv.assistant));
    ok(after.spinning === 0, 'no tool card is still spinning in the DOM', String(after.spinning));
    ok(srv.running.length === 0, 'no step is still running on the server', srv.running.join(',') || 'none');
    ok(after.draft === 0, 'no draft step is left', String(after.draft));
    ok(after.stepNodes === after.distinctSteps && after.msgNodes === after.distinctMsgs, 'no duplicated rows',
      `${after.stepNodes}/${after.distinctSteps} steps, ${after.msgNodes}/${after.distinctMsgs} msgs`);
    ok(!after.stale && !after.busy, 'the page is live and idle again',
      JSON.stringify({ stale: after.stale, busy: after.busy }));
    const bad = unexpected(s.errors, RESTART_NOISE);
    ok(bad.length === 0, 'no console errors beyond the ones the restart caused', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// ------------------------------------------------------------- 10. retry503

async function retry503() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));

    let posts = 0;
    const bodies = [];
    await s.page.route('**/api/chats/*/messages', async (route) => {
      posts += 1;
      bodies.push(route.request().postData());
      if (posts === 1) {
        await route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Socrates is restarting - your message will be sent in a moment' }),
        });
        return;
      }
      await route.continue();
    });

    const t0 = Date.now();
    await send(s.page, 'And commit it.');
    const pendingSeen = await s.page.waitForSelector('.msg.user.pending', { timeout: 3000 })
      .then(() => true).catch(() => false);
    const pendingLine = pendingSeen
      ? (await s.page.textContent('.msg.user.pending .msg-state').catch(() => '')) : '';
    await s.page.waitForFunction(() => document.querySelectorAll('.msg.assistant').length === 2, null, { timeout: 45000 });
    const deliveredAt = Date.now() - t0;
    await idle(s.page);
    await wait(500);
    await s.page.unroute('**/api/chats/*/messages').catch(() => {});

    const srv = await serverView(s.page, id);
    const copies = srv.userTexts.filter((t) => t === 'And commit it.').length;
    const clientIds = new Set(bodies.map((b) => { try { return JSON.parse(b).client_id; } catch { return b; } }));
    ok(posts === 2, 'POST /messages was sent exactly twice (the 503 and the retry)', posts + ' posts');
    ok(clientIds.size === 1, 'both carried the same client_id', [...clientIds].join(','));
    ok(copies === 1, 'the message exists exactly once on the server', copies + ' copies');
    ok(pendingSeen, 'the bubble showed as pending while the retry waited',
      JSON.stringify(pendingLine.trim()) + ' after ' + deliveredAt + ' ms');
    const after = await census(s.page);
    ok(after.pending === 0 && after.user === 2, 'the pending bubble was adopted, not duplicated',
      JSON.stringify({ pending: after.pending, user: after.user }));
    await shot(s.page, 'retry503');
    // The 503 on /messages is the one this scenario injected itself, and Chrome
    // logs it. It is tolerated here by its URL and nowhere else in the suite.
    const bad = unexpected(s.errors).filter((e) => !/503 \(Service Unavailable\) @ .*\/messages$/.test(e));
    ok(bad.length === 0, 'no console errors beyond the injected 503', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------- 11. offline

async function offline() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const firstChat = await s.page.evaluate(() => location.hash.slice(1));

    const wentOffline = Date.now();
    await s.context.setOffline(true);
    await s.page.waitForFunction(() => {
      const bar = document.querySelector('.conn-bar');
      return bar && !bar.hidden;
    }, null, { timeout: 6000 });
    const barAfter = Date.now() - wentOffline;
    ok(barAfter < 3300, 'the connection bar appears within the grace period', barAfter + ' ms');
    const barText = await s.page.textContent('.conn-bar');
    ok(/no network|connection|reconnect/i.test(barText), 'the bar says what is wrong', barText.trim());
    await shot(s.page, 'offline-bar');

    await send(s.page, 'And commit it.');
    await s.page.waitForSelector('.msg.user.pending', { timeout: 5000 });
    const pendingLine = await s.page.textContent('.msg.user.pending .msg-state');
    ok(/will be sent automatically|Sending/.test(pendingLine), 'the queued message says it is waiting', pendingLine.trim());

    await s.context.setOffline(false);
    await s.page.waitForFunction(() => document.querySelectorAll('.msg.assistant').length === 2, null, { timeout: 45000 });
    await idle(s.page, 45000);
    await wait(600);
    const delivered = await s.page.evaluate(async (id) => {
      const data = await (await fetch('/api/chats/' + id)).json();
      const mine = data.messages.filter((m) => m.role === 'user' && m.content === 'And commit it.');
      return { copies: mine.length, ids: [...new Set(mine.map((m) => m.client_id))].length };
    }, firstChat);
    ok(delivered.copies === 1, 'the message that waited was delivered exactly once', delivered.copies + ' copies');
    ok(delivered.ids === 1, 'it carried one client id', delivered.ids + ' client ids');
    const pendingLeft = await s.page.$$eval('.msg.user.pending', (n) => n.length);
    ok(pendingLeft === 0, 'the pending bubble was adopted rather than duplicated', String(pendingLeft));

    // A whole new chat, started and typed into with no connection at all, in a
    // page that was itself loaded with no connection: the reload proves the
    // service worker shell, the sheet proves the stored catalogue.
    await s.context.setOffline(true);
    await wait(300);
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newChat', { timeout: 15000 });
    ok(true, 'the app itself still loads with no network', 'reloaded offline');
    await wait(600);
    await ensureNav(s.page);
    await s.page.click('#newChat');
    await s.page.waitForSelector('#newChatSheet[open]', { timeout: 5000 });
    const offlineSheet = await s.page.$$eval('#ncAgent .seg', (nodes) => nodes.map((n) => n.dataset.value));
    ok(offlineSheet.length === 3, 'the sheet renders from the stored catalogue with no network', offlineSheet.join(','));
    await s.page.waitForFunction(() => /last visit/i.test(document.getElementById('ncHint').textContent),
      null, { timeout: 10000 }).catch(() => {});
    const hint = await s.page.textContent('#ncHint');
    ok(/last visit/i.test(hint), 'and says where the list came from', hint.trim());
    await shot(s.page, 'offline-sheet');
    await s.page.click('#ncEffort .seg[data-value="high"]');
    await s.page.click('#ncStart');
    await s.page.waitForSelector('#newChatSheet[open]', { state: 'detached' }).catch(() => {});
    await send(s.page, 'Look at the README.');
    await s.page.waitForSelector('.msg.user.pending', { timeout: 5000 });

    await ensureNav(s.page);
    await s.page.click('#newChat');
    await wait(300);
    const sheetOpen = await s.page.$$eval('#newChatSheet[open]', (n) => n.length);
    const toastText = await s.page.textContent('.toasts').catch(() => '');
    ok(sheetOpen === 0, 'the sheet refuses to open while an unbound chat is queued', 'open=' + sheetOpen);
    ok(/finish sending/i.test(toastText), 'and says why', toastText.trim());

    await s.context.setOffline(false);
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 45000 });
    // The turn only starts once the message has landed, so waiting on "not
    // busy" alone can be satisfied before it has begun.
    await s.page.waitForSelector('.msg.assistant', { timeout: 60000 });
    await idle(s.page, 45000);
    await wait(600);
    const second = await s.page.evaluate(async () => {
      const id = location.hash.slice(1);
      const data = await (await fetch('/api/chats/' + id)).json();
      return {
        id,
        agent: data.chat.agent, model: data.chat.model, effort: data.chat.effort,
        copies: data.messages.filter((m) => m.role === 'user' && m.content === 'Look at the README.').length,
        assistant: data.messages.filter((m) => m.role === 'assistant').length,
      };
    });
    ok(second.id !== firstChat, 'a second chat was created', second.id);
    ok(second.agent === 'claude' && second.model === 'sonnet' && second.effort === 'high',
      'it was created with the binding chosen while offline',
      `${second.agent}/${second.model}/${second.effort}`);
    ok(second.copies === 1, 'the message typed offline arrived exactly once', second.copies + ' copies');
    ok(second.assistant === 1, 'and was answered once', second.assistant + ' answers');
    await shot(s.page, 'offline-recovered');
    const bad = unexpected(s.errors, OFFLINE_NOISE);
    ok(bad.length === 0, 'no console errors beyond the failed requests being offline causes', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// ------------------------------------------------------------ 12. blankchat

// A chat that has been started and typed into but not yet delivered is a real
// place with nothing behind it: no id, no stream, no row in the sidebar.
async function blankchat() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const firstChat = await s.page.evaluate(() => location.hash.slice(1));

    // Hold the create back without touching anything else, so the chat list
    // still loads: this is the state a reload has to survive.
    await s.context.route('**/api/chats', (route) => {
      if (route.request().method() === 'POST') return route.abort();
      return route.continue();
    });

    await openSheetAndStart(s.page, { effort: 'high' });
    await send(s.page, 'A second conversation.');
    await s.page.waitForSelector('.msg.user.pending', { timeout: 8000 });
    const before = await s.page.evaluate(() => ({
      hash: location.hash, badge: document.getElementById('chatAgent').textContent,
    }));
    ok(before.hash === '' && before.badge === 'Claude Code · Sonnet · high',
      'the blank chat shows the binding it was given', JSON.stringify(before));

    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('.msg.user.pending', { timeout: 15000 });
    await wait(800);
    const afterReload = await s.page.evaluate(() => ({
      hash: location.hash,
      pending: document.querySelectorAll('.msg.user.pending').length,
      pendingText: (document.querySelector('.msg.user.pending') || {}).textContent || '',
      badge: document.getElementById('chatAgent').textContent,
      sidebar: document.querySelectorAll('.chat-item').length,
    }));
    ok(afterReload.hash === '', 'a reload stays on the blank chat rather than opening the newest one',
      JSON.stringify({ hash: afterReload.hash, sidebar: afterReload.sidebar }));
    ok(afterReload.sidebar >= 1, 'even though the chat list did load', afterReload.sidebar + ' rows');
    ok(afterReload.pending === 1 && afterReload.pendingText.includes('A second conversation.'),
      'the message the person is waiting on is on the screen they are looking at',
      afterReload.pendingText.trim().slice(0, 40));
    ok(afterReload.badge.startsWith('Claude Code'), 'and the badge came back with it', afterReload.badge);
    await shot(s.page, 'blankchat-reload');

    await s.context.setOffline(true);
    await s.page.waitForFunction(() => document.body.classList.contains('stale'), null, { timeout: 6000 });
    const row = await s.page.evaluate(() => ({
      stale: document.body.classList.contains('stale'),
      label: (document.querySelector('.working-label') || {}).textContent || '',
      lost: (document.querySelector('.working') || { classList: { contains: () => false } }).classList.contains('lost'),
    }));
    ok(row.stale, 'a blank chat with no network is stale', 'body.stale=' + row.stale);
    ok(row.label === 'Saved — it will send itself when there is signal',
      'and the working row says so instead of "Sending…"', JSON.stringify(row.label));
    ok(row.lost, 'the row is marked lost', 'working.lost=' + row.lost);
    await shot(s.page, 'blankchat-offline');

    await s.page.click('.view-slider .stop[data-view="auto"]');
    await wait(400);
    const banner = await s.page.evaluate(() => ({
      shown: !document.getElementById('autoOffline').hidden,
      text: document.getElementById('autoOffline').textContent,
    }));
    ok(banner.shown && /saved and will be sent/i.test(banner.text),
      'the hands free offline banner shows on a blank chat too', banner.text.trim());
    await s.page.click('.view-slider .stop[data-view="chat"]');

    await s.context.unroute('**/api/chats');
    await s.context.setOffline(false);
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 45000 });
    await s.page.waitForSelector('.msg.assistant', { timeout: 60000 });
    await idle(s.page, 45000);
    await wait(600);
    const delivered = await s.page.evaluate(async () => {
      const id = location.hash.slice(1);
      const data = await (await fetch('/api/chats/' + id)).json();
      return {
        id, agent: data.chat.agent, model: data.chat.model, effort: data.chat.effort,
        copies: data.messages.filter((m) => m.content === 'A second conversation.').length,
      };
    });
    ok(delivered.id !== firstChat, 'the held-back chat was created in the end', delivered.id);
    ok(delivered.agent === 'claude' && delivered.effort === 'high',
      'with the binding it was started with', `${delivered.agent}/${delivered.model}/${delivered.effort}`);
    ok(delivered.copies === 1, 'and its message arrived exactly once', delivered.copies + ' copies');
    const bad = unexpected(s.errors, OFFLINE_NOISE);
    ok(bad.length === 0, 'no console errors beyond the aborted and offline requests', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// ----------------------------------------------------------- 13. queuedchat

// A chat started while offline, typed into, and then the page is reloaded
// before anything reached the server. Nothing exists yet but the outbox.
async function queuedchat() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await s.page.waitForFunction(() => !!localStorage.getItem('socrates.agents'), null, { timeout: 10000 });
    await s.context.setOffline(true);
    await wait(300);
    await ensureNav(s.page);
    await s.page.click('#newChat');
    await s.page.waitForSelector('#newChatSheet[open]');
    // The catalogue this page loaded while it still had a connection is the
    // one the sheet paints from, so it opens complete with no network at all.
    // It does NOT claim to be from the last visit here, and it should not: the
    // list on screen is the one this session was actually given. The staleness
    // line belongs to a page that booted with no network and had to read the
    // catalogue back out of localStorage, and `offline` asserts it there.
    const agentsOffline = await s.page.$$eval('#ncAgent .seg', (n) => n.map((x) => x.dataset.value));
    ok(agentsOffline.length === 3, 'the sheet opens complete with no network', agentsOffline.join(','));
    await s.page.click('#ncEffort .seg[data-value="high"]');
    await s.page.click('#ncStart');
    await s.page.waitForSelector('#newChatSheet[open]', { state: 'detached' }).catch(() => {});
    const badgeBefore = await s.page.textContent('#chatAgent');
    await send(s.page, 'First, offline.');
    await s.page.waitForSelector('.msg.user.pending', { timeout: 5000 });
    ok(badgeBefore === 'Claude Code · Sonnet · high',
      'the badge shows the pending binding before any chat exists', badgeBefore);

    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newChat', { timeout: 15000 });
    await wait(800);
    const after = await s.page.evaluate(() => ({
      badge: document.getElementById('chatAgent').textContent,
      badgeHidden: document.getElementById('chatAgent').hidden,
      pending: document.querySelectorAll('.msg.user.pending').length,
      hash: location.hash,
      workLabel: (document.querySelector('.working-label') || {}).textContent || '',
      outbox: JSON.parse(localStorage.getItem('socrates.outbox.messages') || '[]')
        .map((i) => ({ chatId: i.payload.chatId, agent: i.payload.agent, model: i.payload.model, effort: i.payload.effort })),
    }));
    await shot(s.page, 'queuedchat-reloaded-offline');
    ok(!after.badgeHidden && after.badge === 'Claude Code · Sonnet · high',
      'after the reload the badge is truthful about the queued binding', after.badge);
    ok(after.pending === 1 && after.hash === '', 'the queued bubble is shown on the blank chat',
      JSON.stringify({ pending: after.pending, hash: after.hash }));
    ok(after.outbox.length === 1 && after.outbox[0].agent === 'claude' && after.outbox[0].effort === 'high'
      && !after.outbox[0].chatId, 'the queued item carries the binding and no chat id', JSON.stringify(after.outbox));
    ok(/Saved|signal/i.test(after.workLabel), 'the working row says it is saved for later', JSON.stringify(after.workLabel));

    await ensureNav(s.page);
    await s.page.click('#newChat');
    await wait(300);
    const refused = await s.page.evaluate(() => ({
      open: document.querySelectorAll('#newChatSheet[open]').length,
      toast: (document.querySelector('.toasts') || {}).textContent || '',
    }));
    ok(refused.open === 0 && /finish sending/i.test(refused.toast),
      'the sheet refuses to open while the unbound item waits', JSON.stringify(refused));
    await s.page.keyboard.press('Escape');

    await s.context.setOffline(false);
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 45000 });
    await s.page.waitForSelector('.msg.assistant', { timeout: 45000 });
    await idle(s.page, 45000);
    const first = await s.page.evaluate(() => location.hash.slice(1));
    await send(s.page, 'Second, online.');
    await s.page.waitForFunction(() => document.querySelectorAll('.msg.assistant').length === 2, null, { timeout: 45000 });
    await idle(s.page, 45000);
    await wait(500);
    const second = await s.page.evaluate(() => location.hash.slice(1));
    const srv = await serverView(s.page, first);
    const chats = await s.page.evaluate(async () => (await (await fetch('/api/chats?scope=all')).json()).chats.length);
    ok(first === second, 'the second message landed in the same chat', first + ' == ' + second);
    ok(srv.userTexts.join('|') === 'First, offline.|Second, online.', 'both messages, in order, once each',
      srv.userTexts.join('|'));
    ok(srv.chat.agent === 'claude' && srv.chat.effort === 'high',
      'the chat was created with the binding from before the reload',
      `${srv.chat.agent}/${srv.chat.model}/${srv.chat.effort}`);
    ok(chats === 1, 'exactly one chat exists', chats + ' chats');
    const badge = await s.page.textContent('#chatAgent');
    ok(badge === 'Claude Code · Sonnet · high', 'the badge after creation matches', badge);
    await shot(s.page, 'queuedchat-after');
    const bad = unexpected(s.errors, OFFLINE_NOISE);
    ok(bad.length === 0, 'no console errors beyond the failed requests being offline causes', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// ----------------------------------------------------- 14. queuedchatbeside

// The same, but with a chat that already exists: the boot path opens the newest
// chat, so the queued message has somewhere else it could wrongly end up.
async function queuedchatbeside() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const existing = await s.page.evaluate(() => location.hash.slice(1));

    await s.context.setOffline(true);
    await wait(300);
    await openSheetAndStart(s.page, { effort: 'high' });
    await send(s.page, 'Second chat, offline.');
    await s.page.waitForSelector('.msg.user.pending', { timeout: 5000 });
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#newChat', { timeout: 15000 });
    await wait(1200);
    const after = await s.page.evaluate(() => ({
      hash: location.hash,
      badge: document.getElementById('chatAgent').textContent,
      pending: document.querySelectorAll('.msg.user.pending').length,
      outbox: JSON.parse(localStorage.getItem('socrates.outbox.messages') || '[]').length,
      sidebar: [...document.querySelectorAll('.chat-item .label')].length,
    }));
    await shot(s.page, 'queuedchatbeside-reloaded');
    ok(after.outbox === 1, 'the queued item survived the reload', after.outbox + ' items');
    ok(after.pending === 1 && after.hash === '',
      'the page came back on the blank chat, with the queued message on it', JSON.stringify(after));
    ok(after.badge === 'Claude Code · Sonnet · high', 'and on the binding it was started with', after.badge);

    await ensureNav(s.page);
    await s.page.click('#newChat');
    await wait(300);
    const refused = await s.page.evaluate(() => document.querySelectorAll('#newChatSheet[open]').length);
    ok(refused === 0, 'the sheet still refuses while the unbound item waits', 'open=' + refused);
    await s.page.keyboard.press('Escape');

    await s.context.setOffline(false);
    await s.page.waitForFunction(async () => (await (await fetch('/api/chats?scope=all')).json()).chats.length === 2,
      null, { timeout: 45000 });
    await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 45000 });
    await s.page.waitForSelector('.msg.assistant', { timeout: 45000 });
    await idle(s.page, 45000);
    await wait(800);
    const chats = await s.page.evaluate(async () => (await (await fetch('/api/chats?scope=all')).json())
      .chats.map((c) => ({ id: c.id, agent: c.agent, effort: c.effort })));
    const created = chats.find((c) => c.id !== existing);
    ok(!!created && created.agent === 'claude' && created.effort === 'high',
      'the queued chat was created with its binding', JSON.stringify(created));
    const srv = await serverView(s.page, created.id);
    ok(srv.userTexts.join('|') === 'Second chat, offline.', 'its one message arrived once', srv.userTexts.join('|'));
    const existingView = await serverView(s.page, existing);
    ok(existingView.userTexts.join('|') === 'Run the tests.',
      'and nothing leaked into the chat that already existed', existingView.userTexts.join('|'));
    await shot(s.page, 'queuedchatbeside-after');
    const bad = unexpected(s.errors, OFFLINE_NOISE);
    ok(bad.length === 0, 'no console errors beyond the failed requests being offline causes', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// --------------------------------------------------------------- 15. legacy

async function legacy() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));
    ok(/unbound/.test(unbind(join(s.data, 'socrates.db'), id)), 'the chat was unbound in the database', id);

    // A reload, not a goto: the page is already at this hash, so a goto would
    // be a same-document navigation and the page would keep what it knows.
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#composerLegacy:not([hidden])', { timeout: 15000 });

    const shape = await s.page.evaluate(() => ({
      legacyLine: document.getElementById('composerLegacy').textContent.trim(),
      composerShown: getComputedStyle(document.getElementById('composer')).display !== 'none',
      badge: document.getElementById('chatAgent').textContent,
      badgeWarn: document.getElementById('chatAgent').classList.contains('warn'),
      messages: document.querySelectorAll('.msg').length,
    }));
    ok(shape.legacyLine === 'This chat was made before Socrates talked to agents directly. Start a new chat.',
      'the composer is replaced by the one sentence', shape.legacyLine);
    ok(shape.composerShown === false, 'the composer itself is gone', 'display none = ' + !shape.composerShown);
    ok(shape.badge === 'No agent' && shape.badgeWarn, 'the header says the chat has no agent', shape.badge);
    ok(shape.messages >= 2, 'the transcript still renders', shape.messages + ' messages');

    // §8.2: the composer, the mic button and the Audio stop are replaced, not
    // merely styled out. A microphone that records, spends a transcription and
    // is then told no is a worse way of saying no than not offering it.
    const handsFree = await s.page.evaluate(() => ({
      stop: document.querySelector('.view-slider .stop[data-view="auto"]').getAttribute('aria-disabled'),
      autoMic: document.getElementById('autoMic').disabled,
      chatMic: document.getElementById('micBtn').disabled,
    }));
    ok(handsFree.stop === 'true', 'the Audio stop is disabled on a legacy chat', 'aria-disabled=' + handsFree.stop);
    ok(handsFree.autoMic && handsFree.chatMic, 'both microphones are dead', JSON.stringify(handsFree));
    // Playwright refuses to click an aria-disabled control - as a screen
    // reader would - so the tap is dispatched by hand to prove the handler
    // refuses it too, not just the browser's actionability check. The arrow
    // key is the other way into the view slider.
    await s.page.dispatchEvent('.view-slider .stop[data-view="auto"]', 'click');
    await wait(250);
    const afterClick = await s.page.evaluate(() => document.body.classList.contains('auto'));
    await s.page.focus('.view-slider .stop[data-view="chat"]');
    await s.page.keyboard.press('ArrowRight');
    await wait(300);
    const afterKey = await s.page.evaluate(() => document.body.classList.contains('auto'));
    ok(!afterClick && !afterKey, 'neither a dispatched tap nor the arrow key opens hands free',
      JSON.stringify({ afterClick, afterKey }));

    // A chat that was left in the Audio view before it became legacy: the
    // remembered view has to be overridden.
    await s.page.evaluate((chatId) => localStorage.setItem('socrates.view.' + chatId, 'auto'), id);
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#composerLegacy:not([hidden])', { timeout: 15000 });
    await wait(400);
    const remembered = await s.page.evaluate(() => ({
      auto: document.body.classList.contains('auto'),
      micDisabled: document.getElementById('autoMic').disabled,
      stopAria: document.querySelector('.view-slider .stop[data-view="auto"]').getAttribute('aria-disabled'),
    }));
    ok(!remembered.auto && remembered.micDisabled && remembered.stopAria === 'true',
      'a remembered Audio view is overridden and the mic stays dead', JSON.stringify(remembered));
    await shot(s.page, 'legacy');

    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');

    // Nothing may be queued from here, so the endpoint's own refusal is the
    // backstop and it must be the permanent one. The 422 it answers with is a
    // console error of this probe's own making, so it is asserted last.
    const status = await s.page.evaluate(async (chatId) => {
      const res = await fetch('/api/chats/' + chatId + '/messages', {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ text: 'hello', client_id: 'e2e-legacy' }),
      });
      return { code: res.status, body: (await res.json()).error };
    }, id);
    ok(status.code === 422, 'the server refuses a message permanently, not with a 409', String(status.code));
    ok(/before Socrates talked to agents/.test(status.body), 'and says why in one sentence', status.body);
  } finally { await s.stop(); }
}

// ------------------------------------------------------------ 16. legacy422

// The only way the outbox can meet a 422: a message queued while offline, and
// the chat unbound underneath it before it could be sent. A permanent refusal
// must stop the retry loop dead rather than hammer the endpoint forever.
async function legacy422() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    await openSheetAndStart(s.page, {});
    await send(s.page, 'Run the tests.');
    await s.page.waitForSelector('.msg.assistant', { timeout: 25000 });
    await idle(s.page);
    const id = await s.page.evaluate(() => location.hash.slice(1));

    await s.context.setOffline(true);
    await wait(300);
    await send(s.page, 'And commit it.');
    await s.page.waitForSelector('.msg.user.pending', { timeout: 5000 });
    ok(/unbound/.test(unbind(join(s.data, 'socrates.db'), id)), 'the chat was unbound while the message waited', id);

    let posts = 0;
    s.page.on('request', (r) => {
      if (/\/api\/chats\/[^/]+\/messages$/.test(r.url()) && r.method() === 'POST') posts += 1;
    });
    await s.context.setOffline(false);
    await s.page.waitForSelector('.msg.user.pending.stuck', { timeout: 20000 }).catch(() => {});
    const t0 = Date.now();
    await wait(25000);
    const shape = await s.page.evaluate(() => ({
      stuck: document.querySelectorAll('.msg.user.pending.stuck').length,
      line: (document.querySelector('.msg.user.pending .msg-state') || {}).textContent || '',
      retryBtn: !!document.querySelector('.msg.user.pending .msg-state button'),
    }));
    ok(posts === 1, 'POST /messages was sent exactly once and never retried',
      posts + ' posts over ' + (Date.now() - t0) + ' ms');
    ok(shape.stuck === 1 && shape.retryBtn, 'the message is shown as failed with a Try again offered',
      JSON.stringify(shape.line.trim()));
    ok(/before Socrates talked to agents/.test(shape.line), 'the failure line carries the server sentence',
      shape.line.trim().slice(0, 90));
    await shot(s.page, 'legacy422-failed');

    posts = 0;
    await s.page.reload({ waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('#composerLegacy:not([hidden])', { timeout: 15000 });
    await wait(8000);
    const after = await s.page.evaluate(() => ({
      composerShown: getComputedStyle(document.getElementById('composer')).display !== 'none',
      legacyLine: document.getElementById('composerLegacy').textContent.trim(),
      badge: document.getElementById('chatAgent').textContent,
      warn: document.getElementById('chatAgent').classList.contains('warn'),
      busy: document.body.classList.contains('busy'),
    }));
    ok(!after.composerShown && after.legacyLine.startsWith('This chat was made before'),
      'after a reload the composer is replaced', after.legacyLine);
    ok(after.badge === 'No agent' && after.warn, 'the badge says No agent', after.badge);
    ok(posts <= 1, 'a reload does not start a retry loop', posts + ' posts in 8 s after the reload');
    ok(!after.busy, 'the page is not left looking busy', 'busy=' + after.busy);
    await shot(s.page, 'legacy422-reload');
    const bad = unexpected(s.errors, OFFLINE_NOISE).filter((e) => !/422/.test(e));
    ok(bad.length === 0, 'no console errors beyond the 422 and the offline requests', bad.join(' | ') || '0');
  } finally { await s.stop(); }
}

// ----------------------------------------------------------- 17. sheetphone

// The sheet on a phone with the keyboard up: 390x500 is the worst case it has
// to survive, and the combobox has to open inside it without being clipped.
async function sheetphone() {
  const s = await start({ viewport: { width: 390, height: 500 } });
  try {
    await setup(s.page, s.url);
    await ensureNav(s.page);
    await s.page.click('#newChat');
    await s.page.waitForSelector('#newChatSheet[open]');
    await wait(400);
    const geom = await s.page.evaluate(() => {
      const r = (sel) => { const n = document.querySelector(sel); return n ? n.getBoundingClientRect().toJSON() : null; };
      const sheet = document.getElementById('newChatSheet');
      return {
        vw: innerWidth, vh: innerHeight,
        overflowX: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        sheet: r('#newChatSheet'), start: r('#ncStart'), model: r('#ncModel input'),
        sheetScroll: sheet.scrollHeight - sheet.clientHeight,
      };
    });
    await shot(s.page, 'sheetphone-sheet');
    const within = (b) => b && b.left >= -1 && b.right <= geom.vw + 1 && b.top >= -1 && b.bottom <= geom.vh + 1;
    ok(geom.overflowX <= 1, 'no horizontal overflow with the sheet open', geom.overflowX + 'px');
    ok(within(geom.sheet), 'the sheet fits the 390x500 viewport', JSON.stringify(geom.sheet));
    ok(within(geom.start) && geom.start.bottom > geom.vh * 0.5,
      'Start is on screen and in the lower half (thumb reach)', JSON.stringify(geom.start));
    ok(within(geom.model), 'the model field is on screen', JSON.stringify(geom.model));

    await s.page.click('#ncModel input');
    await wait(300);
    const list = await s.page.evaluate(() => {
      const el = document.querySelector('#ncModel .combo-list');
      const sheet = document.getElementById('newChatSheet').getBoundingClientRect();
      const opts = [...document.querySelectorAll('#ncModel .combo-option')].map((n) => n.getBoundingClientRect().toJSON());
      const visible = opts.filter((o) => o.bottom <= sheet.bottom && o.top >= sheet.top && o.bottom <= innerHeight);
      return { hidden: el ? el.hidden : null, options: opts.length, visible: visible.length };
    });
    await shot(s.page, 'sheetphone-combobox');
    ok(list.hidden === false && list.options === 4, 'the model list opens with four models',
      JSON.stringify({ hidden: list.hidden, options: list.options }));
    ok(list.visible === list.options, 'every option is visible, not clipped by the sheet or the viewport',
      `${list.visible} of ${list.options}`);

    await s.page.keyboard.press('Escape');
    await wait(200);
    const stillOpen = await s.page.$$eval('#newChatSheet[open]', (n) => n.length);
    ok(stillOpen === 1, 'Escape on the combobox did not close the sheet', 'open=' + stillOpen);

    await s.page.evaluate(() => document.querySelector('#ncAgent .seg').focus());
    for (let i = 0; i < 9; i += 1) await s.page.keyboard.press('Tab');
    const inDialog = await s.page.evaluate(() => document.getElementById('newChatSheet').contains(document.activeElement));
    ok(inDialog, 'focus stays inside the modal sheet after nine tabs', 'inDialog=' + inDialog);

    await s.page.click('#ncCancel');
    await wait(200);
    const closed = await s.page.$$eval('#newChatSheet[open]', (n) => n.length);
    ok(closed === 0, 'Cancel closes the sheet', 'open=' + closed);
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// ---------------------------------------------------------------- 18. admin

async function admin() {
  const s = await start();
  try {
    await setup(s.page, s.url);
    let refreshes = 0;
    s.page.on('request', (r) => {
      if (r.url().endsWith('/api/agents/refresh') && r.method() === 'POST') refreshes += 1;
    });
    await s.page.goto(s.url + '/admin', { waitUntil: 'domcontentloaded' });
    await s.page.waitForSelector('.agent-card', { timeout: 15000 });
    await s.page.evaluate(() => { document.querySelector('.agent-card').dataset.old = '1'; });
    const buttons = await s.page.$$eval('.agent-card button', (n) => n.map((b) => b.textContent));
    await s.page.click('.agent-card button:has-text("Refresh models")');
    await s.page.waitForFunction(() => !document.querySelector('.agent-card[data-old]'), null, { timeout: 20000 });
    await s.page.waitForSelector('.agent-card button:has-text("Refresh models")', { timeout: 20000 });
    await wait(300);
    const cards = await s.page.$$eval('.agent-card', (nodes) => nodes.map((n) => ({
      label: n.querySelector('.switch span:last-child').textContent,
      facts: n.querySelector('.agent-facts').textContent,
      on: n.querySelector('input[type=checkbox]').checked,
    })));
    ok(refreshes === 1, 'one POST /api/agents/refresh per tap', refreshes + ' requests');
    ok(cards.length === 3, 'the card re-rendered with three agents', cards.map((c) => c.label).join(','));
    ok(cards.every((c) => c.on), 'all three are enabled by default', cards.map((c) => c.on).join(','));
    ok(/4 models · curated/.test(cards[0].facts), "Claude's list says it is curated", cards[0].facts);
    ok(buttons.length === 3, 'one Refresh button per agent', buttons.join(','));

    // The switch is written into the settings object and survives a save.
    await s.page.click('.agent-card:nth-child(3) label.switch');
    await s.page.fill('.agent-card:nth-child(3) input[type=text] >> nth=1', '--foo "bar baz"');
    await s.page.click('#saveTop');
    await wait(1500);
    const saved = await s.page.evaluate(async () => (await (await fetch('/api/settings')).json()).settings.agents);
    ok(saved && saved.opencode && saved.opencode.enabled === false,
      'the third switch (OpenCode) was saved as agents.opencode.enabled=false', JSON.stringify(saved && saved.opencode));
    ok(saved && saved.opencode && JSON.stringify(saved.opencode.extra_args) === JSON.stringify(['--foo', 'bar baz']),
      'extra args were split with quoting', JSON.stringify(saved && saved.opencode && saved.opencode.extra_args));

    const dead = await s.page.evaluate(() => ({
      skills: !!document.getElementById('skills'), prompt: !!document.getElementById('systemPrompt'),
      maxIter: !!document.getElementById('maxIterations'), temp: !!document.getElementById('temperature'),
      orChat: !!document.getElementById('orChat'),
      headings: [...document.querySelectorAll('h2')].map((h) => h.textContent),
    }));
    ok(!dead.skills && !dead.prompt && !dead.maxIter && !dead.temp && !dead.orChat,
      'no skills or orchestration fields remain', dead.headings.join(' / '));

    await s.page.click('#runChecks');
    await s.page.waitForFunction(() => document.querySelectorAll('#checks .check').length > 0, null, { timeout: 30000 });
    await wait(500);
    const checks = await s.page.$$eval('#checks .check', (n) => n.map((c) => c.textContent.replace(/\s+/g, ' ').trim()));
    ok(checks.some((c) => /Agent hosts/.test(c)), 'diagnostics has an Agent hosts row', checks.find((c) => /Agent hosts/.test(c)));
    ok(checks.filter((c) => /Claude Code|Codex|OpenCode/.test(c)).length === 2,
      'one row per enabled agent (OpenCode was switched off above)',
      checks.filter((c) => /Claude Code|Codex|OpenCode/.test(c)).join(' | '));
    ok(!checks.some((c) => /Terminal|PTY/i.test(c)), 'no Terminal row',
      checks.length + ' rows: ' + checks.map((c) => c.slice(0, 30)).join(' | '));
    await shot(s.page, 'admin');
    const overflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    ok(overflow <= 1, '/admin does not scroll sideways at 390', overflow + 'px');
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// ---------------------------------------------------------------- 19. pages

async function pages() {
  for (const viewport of [{ width: 390, height: 844 }, { width: 1440, height: 900 }]) {
    const tag = viewport.width + 'x' + viewport.height;
    const s = await start({ viewport });
    try {
      // /setup is only itself before there is a password, so it is looked at
      // first - and afterwards it is expected to hand over to /login.
      await s.page.goto(s.url + '/setup', { waitUntil: 'domcontentloaded' });
      await wait(1200);
      const setupBad = unexpected(s.errors);
      ok(setupBad.length === 0, `/setup at ${tag} has no console errors`, setupBad.join(' | ') || '0 errors');
      const setupOverflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      ok(setupOverflow <= 1, `/setup at ${tag} does not scroll sideways`, setupOverflow + 'px');
      await shot(s.page, 'pages-setup-' + tag);

      await setup(s.page, s.url);
      for (const path of ['/', '/admin']) {
        s.errors.length = 0;
        await s.page.goto(s.url + path, { waitUntil: 'domcontentloaded' });
        await wait(1800);
        const bad = unexpected(s.errors);
        ok(bad.length === 0, `${path} at ${tag} has no console errors`, bad.join(' | ') || '0 errors');
        const overflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        ok(overflow <= 1, `${path} at ${tag} does not scroll sideways`, overflow + 'px');
        await shot(s.page, 'pages-' + (path === '/' ? 'chat' : path.slice(1)) + '-' + tag);
      }

      // The sheet is a bottom sheet on a phone and a centred dialog on a desk.
      await s.page.goto(s.url + '/', { waitUntil: 'domcontentloaded' });
      await s.page.waitForSelector('#newChat', { timeout: 15000 });
      await ensureNav(s.page);
      await s.page.click('#newChat');
      await s.page.waitForSelector('#newChatSheet[open]');
      await wait(300);
      const rect = await s.page.$eval('#newChatSheet', (n) => n.getBoundingClientRect().toJSON());
      if (viewport.width >= 1000) {
        ok(rect.left > 300 && rect.top > 100, `the sheet is a centred dialog at ${tag}`, JSON.stringify(rect));
      } else {
        ok(rect.left <= 1 && Math.round(rect.width) >= viewport.width - 2,
          `the sheet is a full-width bottom sheet at ${tag}`, JSON.stringify(rect));
      }
      await shot(s.page, 'pages-sheet-' + tag);
      await s.page.click('#ncCancel');

      // Signed in, /login and /setup both redirect to the chat, so they are
      // only themselves once the session is gone.
      await s.context.clearCookies();
      for (const path of ['/login']) {
        s.errors.length = 0;
        await s.page.goto(s.url + path, { waitUntil: 'domcontentloaded' });
        await wait(1500);
        const here = await s.page.evaluate(() => location.pathname);
        ok(here === path, `${path} at ${tag} renders itself when signed out`, here);
        const bad = unexpected(s.errors);
        ok(bad.length === 0, `${path} at ${tag} has no console errors`, bad.join(' | ') || '0 errors');
        const overflow = await s.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        ok(overflow <= 1, `${path} at ${tag} does not scroll sideways`, overflow + 'px');
        await shot(s.page, 'pages-login-' + tag);
      }
      await s.page.goto(s.url + '/setup', { waitUntil: 'domcontentloaded' });
      await wait(800);
      const landed = await s.page.evaluate(() => location.pathname);
      ok(landed === '/login', `/setup at ${tag} hands over once a password exists`, landed);

      // Signed back in, so stop() can sweep with the context's own cookies.
      await s.page.goto(s.url + '/login', { waitUntil: 'domcontentloaded' });
      await s.page.fill('#password', PASSWORD);
      await s.page.click('#submit');
      await s.page.waitForFunction(() => !location.pathname.startsWith('/login'), null, { timeout: 15000 });
    } finally { await s.stop(); }
  }
}

// ----------------------------------------------------------- 20. modelpick

// QUARANTINED — a reproduced defect in a file this work package does not own.
//
// Tapping a model in the new-chat sheet cancels the sheet. The mechanism is
// exact and the two halves are each reasonable on their own:
//
//   * `combobox.js` chooses an option on **mousedown** ("the input must not
//     lose focus first") and hides the list in the same handler;
//   * `agents.js` treats a click whose `event.target` is the `<dialog>` itself
//     as a tap on the backdrop and answers it with `finish(null)`.
//
// By the time the browser dispatches the `click` that follows that mousedown,
// the option it started on has been hidden, so the event retargets to the
// nearest element still under the pointer — the dialog — and the sheet cancels
// itself. Keyboard selection is unaffected, which is why the rest of the suite
// picks models with ArrowDown+Enter and why nothing else caught this.
//
// The whole visible effect is that a person who taps a model gets no chat.
// `e2e/**` is WP6's; `agents.js` and `combobox.js` are WP5's, so this scenario
// records the defect rather than working around it. It fails, loudly, and does
// not fail the run; SOCRATES_E2E_STRICT=1 takes that exemption away, which is
// what the fix should be checked against.
async function modelpick() {
  const s = await start({ viewport: { width: 1280, height: 900 } });
  try {
    await setup(s.page, s.url);
    await ensureNav(s.page);
    await s.page.click('#newChat');
    await s.page.waitForSelector('#newChatSheet[open]');

    const input = s.page.locator('#ncModel input');
    await input.click();
    await input.fill('opus');
    await s.page.waitForSelector('#ncModel .combo-option', { timeout: 5000 });
    const options = await s.page.$$eval('#ncModel .combo-option', (n) => n.map((x) => x.textContent));
    ok(options.length >= 1, 'the model list filters down to the typed model', options.join(' | '));

    await s.page.locator('#ncModel .combo-option').first().click();
    await wait(400);
    const afterTap = await s.page.evaluate(() => ({
      open: !!document.querySelector('#newChatSheet[open]'),
      value: document.querySelector('#ncModel input').value,
    }));
    await shot(s.page, 'modelpick-after-tap');
    ok(afterTap.value === 'opus', 'the tapped model is written into the field', afterTap.value);
    ok(afterTap.open === true, 'the sheet is still open after a model is tapped', 'open=' + afterTap.open);

    // And if it is still open, the chat it starts carries the tapped model.
    if (afterTap.open) {
      await s.page.click('#ncStart');
      await s.page.waitForSelector('#newChatSheet[open]', { state: 'detached', timeout: 5000 }).catch(() => {});
    }
    await send(s.page, 'Run the tests.');
    const created = await s.page.waitForFunction(() => location.hash.length > 1, null, { timeout: 20000 })
      .then(() => true).catch(() => false);
    ok(created, 'a chat is created from the sheet the model was tapped in', 'hash set = ' + created);
    if (created) {
      await s.page.waitForSelector('.msg.assistant', { timeout: 25000 }).catch(() => {});
      await idle(s.page).catch(() => {});
      const chat = await s.page.evaluate(async () => {
        const id = location.hash.slice(1);
        return (await (await fetch('/api/chats/' + id)).json()).chat;
      });
      ok(chat.model === 'opus', 'and it is bound to the model that was tapped', chat.model);
    } else {
      ok(false, 'and it is bound to the model that was tapped', 'no chat was created at all');
    }
  } finally { await s.stop(); }
}

// ----------------------------------------------------------- 21. liveclaude

// The one scenario that talks to a real CLI. Gated exactly like the Go live
// tests (§10.2): SOCRATES_LIVE_AGENTS=1 and the binary on PATH, skipped
// otherwise, never in CI. One tiny tool-using turn on the cheapest model.
async function liveclaude() {
  const s = await start({ live: true, viewport: { width: 1280, height: 900 } });
  try {
    await setup(s.page, s.url);
    await ensureNav(s.page);
    await s.page.click('#newChat');
    await s.page.waitForSelector('#newChatSheet[open]');
    const agents = await s.page.$$eval('#ncAgent .seg', (nodes) =>
      nodes.map((n) => n.dataset.value + (n.disabled ? ':missing' : ':installed')));
    ok(agents.includes('claude:installed'), 'the real Claude Code CLI is reported as installed', agents.join(','));
    await pickModel(s.page, '#ncModel', 'haiku');
    await s.page.click('#ncEffort .seg[data-value="low"]').catch(() => {});
    await s.page.click('#ncStart');
    await s.page.waitForSelector('#newChatSheet[open]', { state: 'detached' }).catch(() => {});

    await send(s.page, 'Run the shell command `echo socrates-live` with your Bash tool, then reply with exactly the word OK and nothing else.');
    // A real turn can also end badly, and a scenario that only waits for the
    // happy shape reports a timeout instead of what went wrong. So it waits
    // for the turn to be over however it ends, and asserts on what is there.
    await s.page.waitForFunction(
      () => document.querySelector('.msg.assistant') || document.querySelector('.step.error-step'),
      null, { timeout: 180000 },
    ).catch(() => {});
    await idle(s.page, 180000).catch(() => {});
    await wait(1000);
    const serverLog = s.log.join('').replace(/\s+/g, ' ').trim();
    ok(true, 'what the server said while the real turn ran', serverLog.slice(-400) || '(nothing)');

    const id = await s.page.evaluate(() => location.hash.slice(1));
    const srv = await serverView(s.page, id);
    const view = await s.page.evaluate(() => ({
      assistant: document.querySelectorAll('.msg.assistant').length,
      tool: document.querySelectorAll('.step.tool-step').length,
      toolNames: [...document.querySelectorAll('.step.tool-step .name')].map((n) => n.textContent),
      answer: (document.querySelector('.msg.assistant') || {}).textContent || '',
      draft: document.querySelectorAll('.step.text-step').length,
      error: document.querySelectorAll('.step.error-step').length,
      errorText: (document.querySelector('.step.error-step') || {}).textContent || '',
      steps: [...document.querySelectorAll('[data-step]')].map((n) => n.className.split(' ')[1] || n.className),
      working: (document.querySelector('.working-label') || {}).textContent || '',
    }));
    ok(view.error === 0, 'the real turn produced no error step',
      view.error + (view.errorText ? ': ' + view.errorText.trim().slice(0, 200) : ''));
    // A `fork/exec <cli>: no such file or directory` for a CLI that plainly
    // exists is Go reporting a missing `cmd.Dir`, not a missing binary. It is
    // the one failure this scenario can name for whoever reads the log.
    if (/fork\/exec .*no such file or directory/.test(view.errorText)) {
      ok(false, 'the per-chat workspace directory exists before the agent is started',
        'engine.Workspace() is <workspace_root>/<chat id> and nothing creates it, so cmd.Dir is '
        + 'missing and every real chat dies at its first message. Create it by hand and the same '
        + 'chat answers. internal/engine is WP1\'s: this suite reports it, it does not patch it.');
    }
    ok(true, 'the steps the real turn produced', view.steps.join(',') || 'none, working row: ' + view.working);
    ok(view.assistant >= 1 && srv.assistant >= 1, 'the real agent answered (DOM and server)',
      `dom=${view.assistant} server=${srv.assistant}`);
    ok(view.tool >= 1, 'the real agent used at least one tool, rendered as a card',
      view.tool + ' cards: ' + view.toolNames.join(','));
    ok(/OK/i.test(view.answer), 'the answer is the one word it was asked for', JSON.stringify(view.answer.trim()).slice(0, 120));
    ok(view.draft === 0, 'no draft step is left behind', String(view.draft));
    ok(srv.running.length === 0, 'no step is still running on the server', srv.running.join(',') || 'none');
    ok(srv.chat.agent === 'claude' && srv.chat.model === 'haiku',
      'the chat is bound to the real agent and the cheap model', `${srv.chat.agent}/${srv.chat.model}/${srv.chat.effort}`);
    await shot(s.page, 'liveclaude');
    ok(unexpected(s.errors).length === 0, 'no unexpected console errors', unexpected(s.errors).join(' | ') || '0');
  } finally { await s.stop(); }
}

// -------------------------------------------------------------------- run

const ALL = [
  ['newchat', 'the sheet binds a chat to an agent, a model and an effort', newchat],
  ['streaming', 'a draft that grows, a tool card, and one final message', streaming],
  ['twoturns', 'a question, an answer, and a composer that comes back', twoturns],
  ['audioturns', 'two turns in the Audio view: one spoken answer each, nothing intermediate', audioturns],
  ['modelchange', 'the model moves between turns, and only between turns', modelchange],
  ['errorstep', 'a turn that dies says so, in the transcript and in the row', errorstep],
  ['stoptool', 'Stop while a tool card is still open', stoptool],
  ['dropconn', 'the connection drops mid-stream and comes back', dropconn],
  ['sigterm', 'the server dies mid-turn and comes back on the same port', sigterm],
  ['retry503', 'a message answered 503 once is delivered exactly once', retry503],
  ['offline', 'nothing typed is lost, and nothing arrives twice', offline],
  ['blankchat', 'a chat that does not exist yet is still the chat you are looking at', blankchat],
  ['queuedchat', 'a chat started offline survives a reload and is created once', queuedchat],
  ['queuedchatbeside', 'the same, beside a chat that already exists', queuedchatbeside],
  ['legacy', 'a chat from before the rewrite is a transcript, not a conversation', legacy],
  ['legacy422', 'a queued message for a legacy chat fails once, without a retry loop', legacy422],
  ['sheetphone', 'the sheet at 390x500, with the keyboard up', sheetphone],
  ['admin', 'the Agents card, refresh, save and diagnostics', admin],
  ['pages', 'every page is clean at a phone and at a desk', pages],
  ['modelpick', 'a model tapped in the new-chat sheet is the model the chat gets', modelpick, {
    quarantine: 'tapping a model cancels the sheet - a mousedown/click retarget between '
      + 'combobox.js and agents.js, both of which belong to WP5. See the comment above modelpick().',
  }],
  ['liveclaude', 'one real turn against the real Claude Code CLI', liveclaude, { live: true }],
];

const wanted = process.argv.slice(2);
try {
  for (const [name, title, fn, opts] of ALL) {
    if (wanted.length && !wanted.includes(name)) continue;
    if (opts && opts.live && !LIVE) {
      skipScenario(name, title, 'set SOCRATES_LIVE_AGENTS=1 to run the live scenario');
      continue;
    }
    await scenario(name, title, fn, opts);
  }
} finally {
  finish();
}
