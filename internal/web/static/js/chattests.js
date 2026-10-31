// A chat's tests on its page (docs/design/CHAT-TESTS.md, "The chat page"):
// the Tests button in the top bar with its badge, the panel that lists the
// tests (a column beside the chat at a desk, a side sheet narrower), the
// viewer of one test with its runs, steps and evidence, the lightbox over a
// run's screenshots, and the dialogs the person changes a test with: Edit,
// Override and Delete, and Run again, which asks the chat's agent.
//
// The tests are the agent's, written and run through the socrates-tests
// MCP server; the page reads them from /api/chats/{id}/tests and reads them
// again when a chattests frame names the chat on screen (events.js), so the
// list and an open viewer follow a run step by step. Everything is drawn
// with morph(), so a redraw keeps the scroll, the focus and the images.

import { api, el, toast, errorMessage, confirmDialog, clientKey } from './api.js';
import { M, html, raw, icon, morph, flipMorph, showOverlay, hideOverlay } from './ui.js';
import { markdown } from './md.js';
import { clock } from './chatparts.js';

// Over this width the panel is a column beside the chat; narrower it is a
// side sheet over it (on a phone, DS's sheet rises from the bottom).
const WIDE = window.matchMedia('(min-width: 1101px)');
// Whether the column is open on this device: 'open' or 'shut'.
const PANEL_PREF = 'socrates.tests.panel';
// The frames of one burst of tool calls (a step judged with its files) are
// read as one: the list is fetched once they have stopped for this long.
const GATHER = 250;

// A test's status: its word, its glyph and the tone of its chip. Status is
// never said by colour alone: every chip has its glyph and its word.
const STATUS = {
  draft: { word: 'Draft', glyph: 'pencil', tone: '' },
  running: { word: 'Running', glyph: '', tone: 'running' },
  passed: { word: 'Passed', glyph: 'check', tone: 'ok' },
  issue: { word: 'Issue', glyph: 'alert', tone: 'warning' },
  failed: { word: 'Failed', glyph: 'alertCircle', tone: 'error' },
  blocked: { word: 'Blocked', glyph: 'ban', tone: '' },
  skipped: { word: 'Skipped', glyph: 'chevronLast', tone: '' },
};

// The order the summary line counts them in: what needs the person first.
const ORDER = ['failed', 'issue', 'running', 'blocked', 'passed', 'draft', 'skipped'];

// countWords is "2 failed", "1 issue", "3 drafts".
function countWords(status, n) {
  if (status === 'issue') return n + (n === 1 ? ' issue' : ' issues');
  if (status === 'draft') return n + (n === 1 ? ' draft' : ' drafts');
  return n + ' ' + STATUS[status].word.toLowerCase();
}

// A step's verdict, as a chip.
const VERDICT = {
  pass: { word: 'Passed', glyph: 'check', tone: 'ok' },
  issue: { word: 'Issue', glyph: 'alert', tone: 'warning' },
  fail: { word: 'Failed', glyph: 'alertCircle', tone: 'error' },
  blocked: { word: 'Blocked', glyph: 'ban', tone: '' },
  na: { word: 'Does not apply', glyph: 'minimize', tone: '' },
};

// What the person may set as an override, with what each means.
const OVERRIDES = [
  ['passed', 'Passed', 'Everything expected happens, and nothing looks wrong.'],
  ['issue', 'Issue', 'It works, but something is wrong a person would notice.'],
  ['failed', 'Failed', 'It cannot be done, or the result is wrong.'],
  ['blocked', 'Blocked', 'Not the product’s fault: something else is in the way.'],
  ['skipped', 'Skipped', 'It does not need to run now.'],
];

// What an event of a test's history says.
const EVENTS = {
  created: 'Written',
  edited: 'Edited',
  run: 'Run started',
  finished: 'Run finished',
  override: 'Result overridden',
  'override-removed': 'Override removed',
  skip: 'Set aside',
  unskip: 'Brought back',
};

const problem = (t) => t.status === 'failed' || t.status === 'issue';
// Open: what still wants a run - never run, running, blocked, or changed
// since its last run.
const open = (t) => t.status === 'draft' || t.status === 'running' || t.status === 'blocked' || !!t.stale;
const FILTERS = [
  ['all', 'All', () => true],
  ['problems', 'Problems', problem],
  ['open', 'Open', open],
];

const isOpenRun = (run) => !!run && !run.ended_at;
const byWords = (by) => (by === 'person' ? 'you' : 'the agent');

/** chip is a status as DS's status chip: glyph (or the turning arc) and word. */
function chip(kind, extra = '') {
  const mark = kind.tone === 'running'
    ? html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>`
    : raw(icon(kind.glyph, 16));
  return html`<span class="${'ds-status' + (kind.tone ? ' ds-status--' + kind.tone : '') + (extra ? ' ' + extra : '')}">${mark}<span>${kind.word}</span></span>`;
}

const statusChip = (status, extra) => chip(STATUS[status] || STATUS.blocked, extra);

/** fileUrl is where an evidence file of a test is served. */
function fileUrl(chat, test, file) {
  return '/api/chats/' + encodeURIComponent(chat) + '/tests/' + encodeURIComponent(test) + '/files/'
    + String(file).split('/').map(encodeURIComponent).join('/');
}

/** sizeWords is a file's size: "812 B", "14 KB", "2.1 MB". */
function sizeWords(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return Math.round(n / 1024) + ' KB';
  return (n / 1024 / 1024).toFixed(1).replace(/\.0$/, '') + ' MB';
}

/** imagesOf is every image of a run, its steps' in their order and then its own. */
function imagesOf(run) {
  if (!run) return [];
  const out = [];
  (run.steps || []).forEach((step, i) => {
    for (const ev of step.evidence || []) if (ev.kind === 'image') out.push({ ...ev, step: i + 1, src: ev });
  });
  for (const ev of run.evidence || []) if (ev.kind === 'image') out.push({ ...ev, step: 0, src: ev });
  return out;
}

/** latestImage is the newest screenshot of a run, or null. */
function latestImage(run) {
  let best = null;
  for (const ev of imagesOf(run)) if (!best || (Date.parse(ev.at) || 0) >= (Date.parse(best.at) || 0)) best = ev;
  return best;
}

/** counts is how many tests are in each status. */
function countsOf(tests) {
  const counts = {};
  for (const t of tests) counts[t.status] = (counts[t.status] || 0) + 1;
  return counts;
}

/** summaryWords is the counts in words, problems first: ["2 failed", "5 passed"]. */
function summaryWords(tests) {
  const counts = countsOf(tests);
  return ORDER.filter((s) => counts[s]).map((s) => countWords(s, counts[s]));
}

/** stepNow is how far an open run is: "Step 2 of 5". */
function stepNow(run) {
  const steps = run.steps || [];
  const judged = steps.filter((s) => s.verdict).length;
  return 'Step ' + Math.min(judged + 1, Math.max(steps.length, 1)) + ' of ' + steps.length;
}

/** whoSet says who gave a test its status. */
function whoSet(t) {
  if (t.override && t.override.status) return 'Set by you';
  if (t.status === 'skipped' && t.skip) return 'Set aside by ' + byWords(t.skip.by);
  if (t.status === 'draft') return 'Not run yet';
  if (t.status === 'running') return 'The agent is running it';
  return 'Judged by the agent';
}

/**
 * mountChatTests wires the Tests button, the column and the sheet.
 *
 * `ctx`: button (the top bar's #testsBtn), panel (the column, #testsPanel),
 * mcpDialog(session, opener, { closed }) the chat's MCP servers dialog.
 *
 * It answers { show(session), hide(), frame(frame), resync() }: a chat on
 * the stage, nothing on it, a chattests frame, and a hello (frames may
 * have been missed).
 */
export function mountChatTests(ctx) {
  const { button, panel } = ctx;
  const badge = button.querySelector('.ds-badge');

  let session = null;
  let tests = [];
  let on = false;
  let rev = null;
  let loaded = false;
  // kept is the last chat's list, taken back when it is shown again (show).
  let kept = null;
  let gen = 0;
  let filter = 'all';
  let timer = null;
  let viewer = null;

  /* ------------------------------------------------------------ the hosts */

  // The column: its head and a body that scrolls.
  const columnBody = el('div', { class: 'so-tests__content' });
  panel.replaceChildren(
    el('div', { class: 'so-tests__head' },
      el('h2', { class: 'so-tests__title', id: 'testsPanelTitle', tabindex: '-1', text: 'Tests' }),
      el('button', {
        class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button', 'data-tests-close': true,
        'aria-label': 'Close the tests', 'data-tip': 'Close the tests',
      })),
    el('div', { class: 'so-tests__scroll' }, columnBody));
  morph(panel.querySelector('[data-tests-close]'), html`${raw(icon('close', 18))}`);

  // The sheet, for a window too narrow for the column.
  const sheetBody = el('div', { class: 'so-tests__content' });
  const sheet = el('dialog', { class: 'ds-sheet so-tests-sheet', id: 'testsSheet', 'aria-labelledby': 'testsSheetTitle' },
    el('div', { class: 'ds-sheet__backdrop', 'data-close': true }),
    el('section', { class: 'ds-sheet__panel' },
      el('div', { class: 'ds-sheet__head' },
        el('h2', { class: 'ds-sheet__title', id: 'testsSheetTitle', text: 'Tests' }),
        el('button', {
          class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-close': true,
          'aria-label': 'Close', 'data-tip': 'Close',
        })),
      el('div', { class: 'ds-sheet__body', tabindex: '0' }, sheetBody)));
  morph(sheet.querySelector('.ds-sheet__head [data-close]'), html`${raw(icon('close', 20))}`);
  document.body.append(sheet);

  const hosts = [makeHost(columnBody, 'c'), makeHost(sheetBody, 's')];

  function makeHost(root, key) {
    const top = el('div', { class: 'so-tests__top' });
    const list = el('ul', { class: 'ds-list ds-list--interactive so-tests__list', 'aria-label': 'Tests' });
    const rest = el('div', { class: 'so-tests__rest' });
    root.replaceChildren(top, list, rest);
    root.addEventListener('change', (event) => {
      const input = event.target.closest('input[data-filter]');
      if (!input || !input.checked) return;
      filter = input.value;
      draw({ live: false });
    });
    root.addEventListener('click', (event) => {
      const row = event.target.closest('[data-open]');
      if (row) {
        openViewer(row.getAttribute('data-open'), row);
        return;
      }
      const mcp = event.target.closest('[data-tests-mcp]');
      if (mcp && session) ctx.mcpDialog(session, mcp, { closed: () => schedule() });
    });
    return { key, root, top, list, rest };
  }

  const sheetOpen = () => !sheet.hidden && sheet.open && sheet.getAttribute('data-state') !== 'closed';

  function prefOpen() {
    try { return localStorage.getItem(PANEL_PREF) === 'open'; } catch { return false; }
  }

  function setPref(value) {
    try { localStorage.setItem(PANEL_PREF, value ? 'open' : 'shut'); } catch { /* a private window */ }
  }

  // available: the chat has the server on or has tests.
  const available = () => !!session && loaded && (on || tests.length > 0);
  const columnShown = () => available() && WIDE.matches && prefOpen();

  /* -------------------------------------------------------------- drawing */

  function draw({ live = true } = {}) {
    drawButton();
    place();
    for (const host of hosts) drawHost(host, live);
  }

  function drawButton() {
    const show = available();
    button.hidden = !show;
    if (!show) return;
    const counts = countsOf(tests);
    const problems = (counts.failed || 0) + (counts.issue || 0);
    const running = counts.running || 0;
    const words = summaryWords(tests);
    const label = 'Tests' + (words.length ? ': ' + words.join(', ') : ', none yet');
    if (button.getAttribute('aria-label') !== label) button.setAttribute('aria-label', label);
    if (button.getAttribute('data-tip') !== label) button.setAttribute('data-tip', label);
    const value = problems || running || 0;
    const tone = problems ? 'ds-badge ds-badge--error so-tests-btn__badge' : 'ds-badge so-tests-btn__badge';
    const was = badge.hidden ? '' : badge.textContent;
    if (badge.className !== tone) badge.className = tone;
    badge.textContent = value ? String(value) : '';
    badge.hidden = !value;
    // A count that appears pops in; the number itself changes at once.
    if (value && !was) M.enter(badge, 'pop');
  }

  // place shows the column, or not, and points the button at what it opens.
  function place() {
    const column = columnShown();
    panel.hidden = !column;
    if (WIDE.matches) {
      button.setAttribute('aria-controls', 'testsPanel');
      button.removeAttribute('aria-haspopup');
      button.setAttribute('aria-expanded', column ? 'true' : 'false');
    } else {
      button.setAttribute('aria-controls', 'testsSheet');
      button.setAttribute('aria-haspopup', 'dialog');
      if (!sheetOpen()) button.setAttribute('aria-expanded', 'false');
    }
    if (!available() && sheetOpen()) hideOverlay(sheet);
  }

  function drawHost(host, live) {
    const words = summaryWords(tests);
    const counts = { all: tests.length };
    for (const [id, , test] of FILTERS) counts[id] = tests.filter(test).length;
    const shown = tests.filter((FILTERS.find(([id]) => id === filter) || FILTERS[0])[2]);
    const name = 'testsFilter-' + host.key;
    morph(host.top, html`${tests.length ? html`<p class="ds-meta so-tests__summary"><span class="ds-parts"><span class="ds-parts__list">${words.map((w) => html`<span>${w}</span>`)}</span></span></p>
      <fieldset class="ds-chips so-tests__filters"><legend class="ds-sr">Show</legend>${FILTERS.map(([id, word]) => html`<label class="ds-chip"${id === 'open' ? html` data-tip="Not run yet, running, blocked, or changed since the last run"` : ''}><input class="ds-chip__input" type="radio" name="${name}" value="${id}" data-filter="${id}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${word}</span>${id !== 'all' && counts[id] ? html` <span class="${'ds-badge' + (id === 'problems' ? ' ds-badge--error' : ' ds-badge--neutral')}">${counts[id]}</span>` : ''}</span></label>`)}</fieldset>` : ''}
      ${loaded && !on && tests.length ? html`<div class="ds-alert ds-alert--warning so-tests__off" role="status">${raw(icon('alert', 20))}<span class="ds-alert__text">Socrates Tests is off in this chat: its agent cannot run these tests.</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-tests-mcp>Switch it on</button></div>` : ''}`);
    for (const input of host.top.querySelectorAll('input[data-filter]')) input.checked = input.value === filter;
    flipMorph(host.list, html`${shown.map((t) => rowHtml(t, host.key))}`, { items: '.so-test', live });
    let rest = '';
    if (!loaded) {
      rest = html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span><span>Reading the tests…</span></div>`;
    } else if (!tests.length && !on) {
      rest = html`<div class="ds-empty so-tests__empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('checkCircle', 24))}</span><p class="ds-empty__title">Socrates Tests is off</p><p class="ds-empty__text">Switched on, this chat’s agent can write tests, run them with its tools and show here what it saw, step by step.</p><button class="ds-btn ds-btn--tonal" type="button" data-tests-mcp>Switch it on…</button></div>`;
    } else if (!tests.length) {
      rest = html`<div class="ds-empty so-tests__empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('checkCircle', 24))}</span><p class="ds-empty__title">No tests yet</p><p class="ds-empty__text">A test is a flow the agent works through step by step, judging each on what it saw. Ask it in the chat: “Write and run tests for the sign-in.”</p></div>`;
    } else if (!shown.length) {
      rest = html`<p class="ds-meta so-tests__none">${filter === 'problems' ? 'No test has a problem.' : 'Every test has run as it is.'}</p>`;
    }
    morph(host.rest, html`${rest}`);
  }

  function rowHtml(t, key) {
    const run = t.latest;
    const running = isOpenRun(run);
    const meta = [];
    if (t.environment) meta.push(t.environment);
    if (running) meta.push(stepNow(run));
    if (run) meta.push(running ? 'Started ' + clock(run.started_at) : 'Ran ' + clock(run.ended_at || run.started_at));
    const shot = running ? latestImage(run) : null;
    const marks = [];
    if (t.override && t.override.status) marks.push(html`<span class="ds-tag so-test__mark" data-mark="override">Set by you</span>`);
    if (t.stale) marks.push(html`<span class="ds-tag ds-tag--neutral so-test__mark" data-mark="stale">Changed since the last run</span>`);
    return html`<li class="ds-list__item so-test" data-ds-key="${t.id}" data-test="${t.id}" data-status="${t.status}">
      <span class="ds-list__text">
        <span class="ds-list__title"><button class="ds-list__action" type="button" id="${'testRow-' + key + '-' + t.id}" data-open="${t.id}"><span class="so-test__id">${t.id}</span> ${t.title}</button></span>
        ${meta.length ? html`<span class="ds-list__meta so-test__meta">${meta.join(' · ')}</span>` : ''}
        ${t.skip && t.status === 'skipped' ? html`<span class="ds-list__meta so-test__skip">${'Set aside: ' + t.skip.reason}</span>` : ''}
        ${marks.length ? html`<span class="so-test__marks">${marks}</span>` : ''}
      </span>
      ${statusChip(t.status, 'so-test__status')}
      ${shot ? html`<img class="so-test__shot" src="${fileUrl(session.id, t.id, shot.file)}" alt="${'Latest screenshot' + (shot.caption ? ': ' + shot.caption : '')}" loading="lazy">` : ''}
    </li>`;
  }

  /* ------------------------------------------------------------- the data */

  async function load() {
    if (!session) return;
    const my = ++gen;
    const id = session.id;
    try {
      const data = await api('/api/chats/' + encodeURIComponent(id) + '/tests', { attempts: 2 });
      if (my !== gen || !session || session.id !== id) return;
      tests = Array.isArray(data && data.tests) ? data.tests : [];
      on = !!(data && data.on);
      rev = data ? data.rev : null;
      loaded = true;
      draw();
    } catch {
      // The list stays as it was; the next frame or hello reads it again.
    }
  }

  function schedule() {
    if (!session) return;
    clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      load();
      if (viewer) viewer.reload();
    }, GATHER);
  }

  /* ----------------------------------------------------------- the button */

  button.addEventListener('click', (event) => {
    // The second click of a double click toggles nothing.
    if (event.detail > 1 || !available()) return;
    if (WIDE.matches) {
      const opening = !columnShown();
      setPref(opening);
      place();
      if (opening) panel.querySelector('.so-tests__title').focus({ preventScroll: true });
      return;
    }
    showOverlay(sheet, button);
  });

  panel.querySelector('[data-tests-close]').addEventListener('click', () => {
    setPref(false);
    place();
    button.focus();
  });

  // A window that grows past the column's width takes an open sheet into
  // the column; one that shrinks under it hides the column (the button
  // opens the sheet then).
  WIDE.addEventListener('change', () => {
    if (WIDE.matches && sheetOpen()) {
      setPref(true);
      hideOverlay(sheet);
    }
    place();
  });

  /* ----------------------------------------------------------- the viewer */

  function openViewer(id, opener) {
    if (!session) return;
    if (viewer) viewer.close();
    const mine = testViewer({ chat: session.id, id, opener, onGone: () => { if (viewer === mine) viewer = null; } });
    viewer = mine;
  }

  /* ------------------------------------------------------------ the page */

  return {
    /** show is a chat put on the stage. */
    show(next) {
      if (session && session.id === next.id) {
        session = next;
        return;
      }
      // The chat that was on the stage a moment ago (attach takes every
      // chat off first) comes back as it was, then is read again.
      const back = kept && kept.id === next.id ? kept : null;
      kept = null;
      session = next;
      tests = back ? back.tests : [];
      on = back ? back.on : false;
      rev = back ? back.rev : null;
      loaded = !!back;
      if (!back) filter = 'all';
      if (viewer) viewer.close();
      if (sheetOpen()) hideOverlay(sheet);
      draw();
      load();
    },
    /** hide is the chat taken off the stage. */
    hide() {
      if (!session) return;
      kept = loaded ? { id: session.id, tests, on, rev } : null;
      session = null;
      gen += 1;
      clearTimeout(timer);
      timer = null;
      tests = [];
      loaded = false;
      if (viewer) viewer.close();
      if (sheetOpen()) hideOverlay(sheet);
      button.hidden = true;
      panel.hidden = true;
    },
    /** frame is a chattests frame: the chat on screen changed. */
    frame(f) {
      if (!session || !f || f.session !== session.id) return;
      if (loaded && rev !== null && f.rev === rev && !viewer) return;
      schedule();
    },
    /** resync is a hello: frames may have been missed. */
    resync() {
      if (session) schedule();
    },
  };
}

/* ================================================================ viewer */

let viewerSeq = 0;

/**
 * testViewer opens one test in a dialog: its head (status and who set it,
 * id, environment, tags, the run switcher), its goal, the steps of the run
 * shown with their verdicts, observations and evidence, the run's summary,
 * findings and own evidence, and its history; Run again, Override, Edit and
 * Delete (in More on a phone). It answers { reload(), close() }.
 */
function testViewer({ chat, id, opener, onGone }) {
  const n = ++viewerSeq;
  const titleId = 'testvTitle' + n;
  let test = null;
  let pinned = null;
  let open = true;
  let rerunKey = '';
  let reading = 0;

  const title = el('h2', { class: 'ds-dialog__title so-testv__title', id: titleId, text: id });
  const close = el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-close': true, 'aria-label': 'Close', 'data-tip': 'Close' });
  morph(close, html`${raw(icon('close', 20))}`);
  const body = el('div', { class: 'ds-dialog__body so-testv__body', tabindex: '0', role: 'region', 'aria-label': 'The test' });
  const rerun = el('button', { class: 'ds-btn ds-btn--primary so-testv__rerun', type: 'button', 'data-act': 'rerun' });
  morph(rerun, html`${raw(icon('sync', 18))}<span>Run again</span>`);
  const more = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon so-testv__more', type: 'button', 'aria-label': 'More actions', 'data-tip': 'More actions',
    'aria-haspopup': 'menu', 'aria-expanded': 'false', 'aria-controls': 'testvMenu' + n,
  });
  morph(more, html`${raw(icon('moreVertical', 20))}`);
  const action = (act, word) => {
    const b = el('button', { class: 'ds-btn ds-btn--text so-testv__secondary', type: 'button', 'data-act': act, text: word });
    return b;
  };
  const overrideBtn = action('override', 'Override…');
  const editBtn = action('edit', 'Edit…');
  const deleteBtn = action('delete', 'Delete…');
  const menu = el('div', { class: 'ds-menu', role: 'menu', id: 'testvMenu' + n, 'aria-label': 'Test actions', hidden: true });
  morph(menu, html`<button class="ds-menu__item" role="menuitem" type="button" data-act="override">${raw(icon('flag', 20))}<span class="ds-menu__label">Override…</span></button><button class="ds-menu__item" role="menuitem" type="button" data-act="edit">${raw(icon('pencil', 20))}<span class="ds-menu__label">Edit…</span></button><button class="ds-menu__item" role="menuitem" type="button" data-act="delete">${raw(icon('trash', 20))}<span class="ds-menu__label">Delete…</span></button>`);
  const actions = el('div', { class: 'ds-dialog__actions so-testv__actions' }, deleteBtn, editBtn, overrideBtn, more, rerun);
  const dialog = el('dialog', { class: 'ds-dialog ds-dialog--wide ds-dialog--full-narrow so-testv', 'aria-labelledby': titleId },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box' },
      el('div', { class: 'ds-dialog__head' }, close, title),
      body, actions, menu));

  const base = '/api/chats/' + encodeURIComponent(chat) + '/tests/' + encodeURIComponent(id);

  // The run on screen: the one picked, else the latest.
  const runOf = () => {
    const runs = (test && test.runs) || [];
    return (pinned !== null && runs.find((r) => r.n === pinned)) || runs[runs.length - 1] || null;
  };

  function draw() {
    if (!test || !open) return;
    if (title.textContent !== test.title) title.textContent = test.title;
    const runs = test.runs || [];
    const run = runOf();
    const finished = runs.some((r) => !isOpenRun(r));
    const overridable = finished || !!(test.override && test.override.status);
    for (const b of [overrideBtn, menu.querySelector('[data-act="override"]')]) {
      if (overridable) {
        b.removeAttribute('aria-disabled');
        b.removeAttribute('data-tip');
      } else {
        b.setAttribute('aria-disabled', 'true');
        b.setAttribute('data-tip', 'A result can be overridden once a run has finished');
      }
    }
    const meta = html`<div class="so-testv__meta"><div class="so-testv__line">${statusChip(test.status, 'so-testv__status')}<span class="ds-meta so-testv__who">${whoSet(test)}</span></div>
      <p class="ds-meta so-testv__facts"><span class="ds-parts"><span class="ds-parts__list"><span class="so-testv__id">${test.id}</span>${test.environment ? html`<span class="so-testv__env">${test.environment}</span>` : ''}</span></span></p>
      ${test.tags && test.tags.length ? html`<div class="so-testv__tags">${test.tags.map((tag) => html`<span class="ds-tag ds-tag--neutral">${tag}</span>`)}</div>` : ''}
      ${runs.length ? html`<div class="so-testv__runs"><label class="ds-meta" for="${'testvRun' + n}">Run</label><select class="ds-input so-testv__run" id="${'testvRun' + n}">${[...runs].reverse().map((r) => html`<option value="${r.n}">${r.n + ' of ' + runs.length + ' · ' + (isOpenRun(r) ? 'Running' : (STATUS[verdictStatus(r.verdict)] || STATUS.blocked).word) + ' · ' + clock(r.started_at)}</option>`)}</select></div>` : ''}</div>`;
    morph(body, bodyHtml(test, run, meta));
    const select = body.querySelector('.so-testv__run');
    if (select && run) select.value = String(run.n);
  }

  function bodyHtml(t, run, meta) {
    const alerts = [];
    if (t.override && t.override.status) {
      alerts.push(html`<div class="ds-alert so-testv__override" role="status">${raw(icon('flag', 20))}<span class="ds-alert__text"><strong>${'You set it to ' + (STATUS[t.override.status] || STATUS.blocked).word + '.'}</strong>${t.override.note ? ' ' + t.override.note : ''}<span class="ds-meta so-testv__when">${' ' + clock(t.override.at)}</span></span></div>`);
    }
    if (t.skip) {
      alerts.push(html`<div class="ds-alert so-testv__skip" role="status">${raw(icon('chevronLast', 20))}<span class="ds-alert__text">${'Set aside by ' + byWords(t.skip.by) + ': ' + t.skip.reason}</span></div>`);
    }
    if (t.stale) {
      alerts.push(html`<div class="ds-alert ds-alert--warning so-testv__stale" role="status">${raw(icon('alert', 20))}<span class="ds-alert__text">Changed since the last run: the steps below are the ones that run was given.</span></div>`);
    }
    const images = imagesOf(run);
    const shotIndex = new Map(images.map((ev, i) => [ev.src, i]));
    const steps = run ? run.steps || [] : t.steps.map((s) => ({ action: s.action, expect: s.expect }));
    const live = isOpenRun(run);
    const current = live ? steps.findIndex((s) => !s.verdict) : -1;
    const stepList = steps.map((s, i) => {
      let mark = '';
      if (s.verdict) mark = chip(VERDICT[s.verdict] || VERDICT.blocked, 'so-step__verdict');
      else if (live && i === current) mark = chip({ word: 'Checking', tone: 'running' }, 'so-step__verdict');
      else if (live) mark = chip({ word: 'Waiting', glyph: 'clock', tone: '' }, 'so-step__verdict');
      return html`<li class="so-step" data-ds-key="${'s' + (i + 1)}" data-step="${i + 1}" data-verdict="${s.verdict || ''}">
        <div class="so-step__head"><span class="so-step__n" aria-hidden="true">${i + 1}</span><p class="so-step__action"><span class="ds-sr">${'Step ' + (i + 1) + ': '}</span>${s.action}</p>${mark}</div>
        <p class="so-step__line"><span class="so-step__label">Expected</span> ${s.expect}</p>
        ${s.observed ? html`<p class="so-step__line so-step__observed"><span class="so-step__label">Observed</span> ${s.observed}</p>` : ''}
        ${s.note ? html`<p class="so-step__line so-step__note"><span class="so-step__label">Note</span> ${s.note}</p>` : ''}
        ${evidenceHtml(s.evidence, shotIndex)}
      </li>`;
    });
    const events = [...(t.events || [])].reverse();
    return html`<div class="so-testv__content">${meta}${alerts}
      ${t.goal ? html`<section class="so-testv__section" aria-labelledby="${'testvGoal' + n}"><h3 class="so-testv__h" id="${'testvGoal' + n}">Goal</h3><div class="so-md so-testv__goal">${markdown(t.goal)}</div></section>` : ''}
      <section class="so-testv__section" aria-labelledby="${'testvSteps' + n}"><h3 class="so-testv__h" id="${'testvSteps' + n}">${run ? 'Steps of run ' + run.n : 'Steps'}</h3>${run ? '' : html`<p class="ds-meta">Not run yet.</p>`}<ol class="so-testv__steps" role="list">${stepList}</ol></section>
      ${run && run.summary ? html`<section class="so-testv__section" aria-labelledby="${'testvSum' + n}"><h3 class="so-testv__h" id="${'testvSum' + n}">Summary</h3><div class="so-md so-testv__summary">${markdown(run.summary)}</div></section>` : ''}
      ${run && run.findings ? html`<section class="so-testv__section" aria-labelledby="${'testvFind' + n}"><h3 class="so-testv__h" id="${'testvFind' + n}">Findings</h3><div class="so-md so-testv__findings">${markdown(run.findings)}</div></section>` : ''}
      ${run && run.evidence && run.evidence.length ? html`<section class="so-testv__section" aria-labelledby="${'testvEv' + n}"><h3 class="so-testv__h" id="${'testvEv' + n}">Evidence of the run</h3>${evidenceHtml(run.evidence, shotIndex)}</section>` : ''}
      ${events.length ? html`<section class="so-testv__section" aria-labelledby="${'testvHist' + n}"><h3 class="so-testv__h" id="${'testvHist' + n}">History</h3><ol class="ds-timeline so-testv__history" role="list" aria-label="History">${events.map((e, i) => html`<li class="ds-timeline__item" data-ds-key="${'e' + (events.length - i)}"><div class="ds-timeline__head"><p class="ds-timeline__title">${EVENTS[e.what] || e.what}</p></div><p class="ds-timeline__meta"><time datetime="${e.at}">${clock(e.at)}</time>${' · ' + byWords(e.by)}</p>${e.detail ? html`<p class="ds-timeline__text">${e.detail}</p>` : ''}</li>`)}</ol></section>` : ''}</div>`;
  }

  function evidenceHtml(list, shotIndex) {
    if (!list || !list.length) return '';
    return html`<div class="so-evidence">${list.map((ev) => {
      const url = fileUrl(chat, id, ev.file);
      const name = ev.caption || ev.file.split('/').pop();
      if (ev.kind === 'image') {
        return html`<button class="so-shot ds-pressable" type="button" data-shot="${shotIndex.has(ev) ? shotIndex.get(ev) : ''}" aria-label="${'Open the screenshot: ' + name}"><img class="so-shot__img" src="${url}" alt="" loading="lazy"><span class="so-shot__caption">${name}</span></button>`;
      }
      const glyph = ev.kind === 'video' ? 'video' : ev.kind === 'text' ? 'fileText' : 'file';
      return html`<a class="so-file ds-pressable" href="${url}" target="_blank" rel="noopener">${raw(icon(glyph, 18))}<span class="so-file__name">${name}</span><span class="so-file__size">${sizeWords(ev.size)}</span></a>`;
    })}</div>`;
  }

  async function reload() {
    const my = ++reading;
    try {
      const data = await api(base, { attempts: 2 });
      if (!open || my !== reading) return;
      test = data && data.test;
      draw();
    } catch (err) {
      if (!open || my !== reading) return;
      if (err && err.status === 404) {
        toast(id + ' is no longer there.');
        shut();
        return;
      }
      if (!test) {
        morph(body, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${errorMessage(err)}</span></div>`);
      }
    }
  }

  function shut() {
    if (!open || !dialog.isConnected) return;
    // Closed from here on: a reload the closing frame starts (the test
    // just deleted) has nothing to say.
    reading += 1;
    hideOverlay(dialog);
  }

  /* -------------------------------------------------------------- actions */

  async function act(what, from) {
    if (!test) return;
    if (what === 'rerun') {
      try {
        // One key until it went: a press again after a timeout is the same
        // message, not a second one.
        rerunKey = rerunKey || clientKey();
        await M.busy(rerun, api(base + '/rerun', { method: 'POST', body: { client_id: rerunKey }, attempts: 2 }), { done: false });
        rerunKey = '';
        toast('Asked the agent to run ' + test.id + ' again.');
        shut();
      } catch (err) {
        toast(errorMessage(err), 'error');
      }
    } else if (what === 'override') {
      const next = await overrideDialog(test, base, from);
      if (next && open) { reading += 1; test = next; draw(); }
    } else if (what === 'edit') {
      const next = await editDialog(test, base, from);
      if (next && open) { reading += 1; test = next; draw(); }
    } else if (what === 'delete') {
      const yes = await confirmDialog({
        title: 'Delete ' + test.id + '?',
        body: '“' + test.title + '” goes with its runs, screenshots and files. This cannot be undone.',
        confirmLabel: 'Delete the test', danger: true, opener: from,
      });
      if (!yes) return;
      try {
        await api(base, { method: 'DELETE', attempts: 1 });
        toast(test.id + ' is deleted.');
        shut();
      } catch (err) {
        toast(errorMessage(err), 'error');
      }
    }
  }

  actions.addEventListener('click', (event) => {
    const b = event.target.closest('[data-act]');
    if (b) act(b.getAttribute('data-act'), b);
  });
  more.addEventListener('click', (event) => {
    if (event.detail > 1) return;
    if (more.getAttribute('aria-expanded') === 'true') hideOverlay(menu);
    else showOverlay(menu, more);
  });
  menu.addEventListener('click', (event) => {
    const b = event.target.closest('[data-act]');
    if (b && b.getAttribute('aria-disabled') !== 'true') act(b.getAttribute('data-act'), more);
  });
  body.addEventListener('change', (event) => {
    const select = event.target.closest('.so-testv__run');
    if (!select) return;
    const runs = (test && test.runs) || [];
    const picked = Number(select.value);
    pinned = runs.length && picked === runs[runs.length - 1].n ? null : picked;
    draw();
  });
  body.addEventListener('click', (event) => {
    const shot = event.target.closest('[data-shot]');
    if (shot && shot.getAttribute('data-shot') !== '') {
      lightbox(imagesOf(runOf()), Number(shot.getAttribute('data-shot')), chat, id, shot);
      return;
    }
    const copy = event.target.closest('[data-action="copy-code"]');
    if (copy) {
      const code = copy.closest('.so-code')?.querySelector('code');
      if (code && navigator.clipboard) navigator.clipboard.writeText(code.textContent).then(() => toast('Copied.'), () => {});
    }
  });

  dialog.addEventListener('close', () => {
    open = false;
    onGone();
    hideOverlay(dialog, { remove: true });
  }, { once: true });
  showOverlay(dialog, opener);
  morph(body, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span><span>Reading the test…</span></div>`);
  reload();
  return { reload, close: shut };
}

/** verdictStatus is the status a finished run's verdict gives (chattests.VerdictStatus). */
function verdictStatus(verdict) {
  return { pass: 'passed', issue: 'issue', fail: 'failed', blocked: 'blocked', na: 'passed' }[verdict] || 'blocked';
}

/* ============================================================== lightbox */

/**
 * lightbox shows the images of a run large, one at a time: ← and → (the
 * buttons and the keys) go through them, Escape closes.
 */
function lightbox(images, start, chat, test, opener) {
  if (!images.length) return;
  let at = Math.max(0, Math.min(images.length - 1, start || 0));
  const titleId = 'lightboxTitle' + (++viewerSeq);
  const title = el('h2', { class: 'ds-dialog__title', id: titleId });
  const close = el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-close': true, 'aria-label': 'Close', 'data-tip': 'Close' });
  morph(close, html`${raw(icon('close', 20))}`);
  const figure = el('figure', { class: 'so-lightbox__figure' });
  const prev = el('button', { class: 'ds-btn', type: 'button', 'data-go': '-1' });
  morph(prev, html`${raw(icon('arrowLeft', 18))}<span>Previous</span>`);
  const next = el('button', { class: 'ds-btn', type: 'button', 'data-go': '1' });
  morph(next, html`<span>Next</span>${raw(icon('arrowRight', 18))}`);
  const dialog = el('dialog', { class: 'ds-dialog ds-dialog--full so-lightbox', 'aria-labelledby': titleId },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box' },
      el('div', { class: 'ds-dialog__head' }, close, title),
      el('div', { class: 'ds-dialog__body so-lightbox__body' }, figure),
      el('div', { class: 'ds-dialog__actions so-lightbox__actions' }, prev, next)));

  const draw = () => {
    const ev = images[at];
    const name = ev.caption || ev.file.split('/').pop();
    title.textContent = 'Screenshot ' + (at + 1) + ' of ' + images.length;
    morph(figure, html`<img class="so-lightbox__img" src="${fileUrl(chat, test, ev.file)}" alt="${name}"><figcaption class="so-lightbox__caption">${ev.step ? html`<span class="ds-meta">${'Step ' + ev.step + ' \u00b7 '}</span>` : ''}${name}</figcaption>`);
    for (const [b, off] of [[prev, at === 0], [next, at === images.length - 1]]) {
      if (off) b.setAttribute('aria-disabled', 'true'); else b.removeAttribute('aria-disabled');
    }
  };
  const go = (step) => {
    const to = at + step;
    if (to < 0 || to >= images.length) return;
    at = to;
    draw();
  };
  dialog.addEventListener('click', (event) => {
    const b = event.target.closest('[data-go]');
    if (b && b.getAttribute('aria-disabled') !== 'true') go(Number(b.getAttribute('data-go')));
  });
  dialog.addEventListener('keydown', (event) => {
    if (event.key === 'ArrowLeft') { event.preventDefault(); go(-1); }
    if (event.key === 'ArrowRight') { event.preventDefault(); go(1); }
  });
  dialog.addEventListener('close', () => hideOverlay(dialog, { remove: true }), { once: true });
  draw();
  showOverlay(dialog, opener);
}

/* =============================================================== dialogs */

let formSeq = 0;

/**
 * formDialog builds a dialog with a form in its body and Cancel and a
 * primary action; `save()` runs busy on that action and answers what the
 * dialog resolves with, or throws to keep it open. `extra` are buttons
 * before Cancel.
 */
function formDialog({ title, text, fields, saveLabel, save, extra = [], opener, wide = true }) {
  const id = 'testForm' + (++formSeq);
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const ok = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: saveLabel });
  const dialog = el('dialog', {
    class: 'ds-dialog ds-dialog--full-narrow so-testform' + (wide ? ' ds-dialog--wide' : ''), 'aria-labelledby': id + '-title',
    'aria-describedby': text ? id + '-text' : null,
  },
  el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
  el('div', { class: 'ds-dialog__box' },
    el('h2', { class: 'ds-dialog__title', id: id + '-title', text: title }),
    text ? el('p', { class: 'ds-dialog__text', id: id + '-text', text }) : null,
    el('div', { class: 'ds-dialog__body' }, el('div', { class: 'so-testform__fields' }, fields)),
    el('div', { class: 'ds-dialog__actions' }, ...extra, cancel, ok)));
  let answer = null;
  const run = async (work) => {
    if (dialog.hasAttribute('data-ds-busy')) return;
    dialog.setAttribute('data-ds-busy', '');
    cancel.setAttribute('aria-disabled', 'true');
    try {
      answer = await work();
    } catch (err) {
      toast(errorMessage(err), 'error');
      return;
    } finally {
      dialog.removeAttribute('data-ds-busy');
      cancel.removeAttribute('aria-disabled');
    }
    hideOverlay(dialog);
  };
  ok.addEventListener('click', () => run(() => M.busy(ok, save(), { done: false })));
  for (const b of extra) b.addEventListener('click', () => run(() => M.busy(b, b.work(), { done: false })));
  return new Promise((resolve) => {
    dialog.addEventListener('close', () => {
      hideOverlay(dialog, { remove: true });
      resolve(answer);
    }, { once: true });
    showOverlay(dialog, opener);
  });
}

/** overrideDialog sets the person's word on a test's result, or removes it. */
function overrideDialog(test, base, opener) {
  const id = 'override' + (++formSeq);
  const current = test.override && test.override.status;
  const radios = el('div', { class: 'ds-radios so-testform__radios', role: 'radiogroup', 'aria-label': 'Result' },
    OVERRIDES.map(([value, word, hint]) => el('label', { class: 'ds-radio' },
      el('input', { type: 'radio', name: id, value, checked: value === (current || '') || null }),
      el('span', {}, el('strong', { text: word }), el('small', { text: hint })))));
  const note = el('textarea', { class: 'ds-input', id: id + '-note', rows: '3', value: (test.override && test.override.note) || '' });
  const fields = [radios, el('div', { class: 'ds-field' },
    el('label', { class: 'ds-field__label', for: id + '-note' }, 'Note for the agent ', el('span', { class: 'ds-field__optional', text: '(optional)' })),
    note)];
  const extra = [];
  if (current) {
    const remove = el('button', { class: 'ds-btn ds-btn--text', type: 'button', text: 'Remove the override' });
    remove.work = () => api(base + '/override', { method: 'POST', body: { status: '', note: '' }, attempts: 1 }).then((d) => {
      toast('The override of ' + test.id + ' is removed.');
      return d.test;
    });
    extra.push(remove);
  }
  return formDialog({
    title: 'Override ' + test.id,
    text: 'Your word on its result stands until you remove it or the test runs again. The agent sees it.',
    fields, saveLabel: 'Set the result', extra, opener, wide: true,
    save: async () => {
      const picked = radios.querySelector('input:checked');
      if (!picked) throw new Error('Choose a result.');
      const d = await api(base + '/override', { method: 'POST', body: { status: picked.value, note: note.value.trim() }, attempts: 1 });
      toast(test.id + ' is set to ' + (STATUS[picked.value] || STATUS.blocked).word + '.');
      return d.test;
    },
  });
}

/** editDialog changes a test: its fields, its steps, and whether it is set aside. */
function editDialog(test, base, opener) {
  const id = 'edit' + (++formSeq);
  const field = (key, label, control, optional = false) => el('div', { class: 'ds-field' },
    el('label', { class: 'ds-field__label', for: id + '-' + key }, label + (optional ? ' ' : ''),
      optional ? el('span', { class: 'ds-field__optional', text: '(optional)' }) : null),
    control);
  const titleIn = el('input', { class: 'ds-input', id: id + '-title', value: test.title, autocomplete: 'off' });
  const goalIn = el('textarea', { class: 'ds-input', id: id + '-goal', rows: '4', value: test.goal || '' });
  const envIn = el('input', { class: 'ds-input', id: id + '-env', value: test.environment || '', autocomplete: 'off' });
  const tagsIn = el('input', { class: 'ds-input', id: id + '-tags', value: (test.tags || []).join(', '), autocomplete: 'off', 'aria-describedby': id + '-tags-hint' });

  const steps = el('ol', { class: 'so-tedit__steps', role: 'list', 'aria-label': 'Steps' });
  let stepSeq = 0;
  const stepRow = (s) => {
    const k = id + '-s' + (++stepSeq);
    const tool = (what, glyph) => {
      const b = el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm', type: 'button', 'data-step-act': what });
      morph(b, html`${raw(icon(glyph, 18))}`);
      return b;
    };
    return el('li', { class: 'so-tedit__step', 'data-ds-key': k },
      el('span', { class: 'so-tedit__n', 'aria-hidden': 'true' }),
      el('div', { class: 'so-tedit__fields' },
        el('div', { class: 'ds-field' }, el('label', { class: 'ds-field__label', for: k + '-a', text: 'What to do' }),
          el('textarea', { class: 'ds-input so-tedit__action', id: k + '-a', rows: '2', value: s.action || '' })),
        el('div', { class: 'ds-field' }, el('label', { class: 'ds-field__label', for: k + '-e', text: 'What must be seen' }),
          el('textarea', { class: 'ds-input so-tedit__expect', id: k + '-e', rows: '2', value: s.expect || '' }))),
      el('div', { class: 'so-tedit__tools' },
        tool('up', 'arrowUp'), tool('down', 'arrowDown'), tool('remove', 'trash')));
  };
  // rows is the steps as they stand: a row being removed stays in the list,
  // inert, while it fades out (M.flip), and is none of them.
  const rows = () => [...steps.children].filter((row) => !row.inert);
  const sibling = (row, dir) => {
    const list = rows();
    return list[list.indexOf(row) + dir] || null;
  };
  // number writes each row's number, and what its buttons say and allow.
  const number = () => {
    const list = rows();
    list.forEach((row, i) => {
      row.querySelector('.so-tedit__n').textContent = String(i + 1);
      const set = (what, label, off, why) => {
        const b = row.querySelector('[data-step-act="' + what + '"]');
        b.setAttribute('aria-label', label);
        b.setAttribute('data-tip', off ? why : label);
        if (off) b.setAttribute('aria-disabled', 'true'); else b.removeAttribute('aria-disabled');
      };
      set('up', 'Move step ' + (i + 1) + ' up', i === 0, 'It is the first step');
      set('down', 'Move step ' + (i + 1) + ' down', i === list.length - 1, 'It is the last step');
      set('remove', 'Remove step ' + (i + 1), list.length === 1, 'A test has at least one step');
    });
  };
  for (const s of test.steps || []) steps.append(stepRow(s));
  if (!steps.children.length) steps.append(stepRow({}));
  steps.addEventListener('click', (event) => {
    const b = event.target.closest('[data-step-act]');
    if (!b || b.getAttribute('aria-disabled') === 'true') return;
    const row = b.closest('.so-tedit__step');
    const what = b.getAttribute('data-step-act');
    if (what === 'remove') {
      M.flip(steps, () => row.remove(), { refocus: () => add });
    } else if (what === 'up' && sibling(row, -1)) {
      const before = sibling(row, -1);
      M.flip(steps, () => steps.insertBefore(row, before));
    } else if (what === 'down' && sibling(row, 1)) {
      const after = sibling(row, 1);
      M.flip(steps, () => steps.insertBefore(after, row));
    }
    number();
    if (what !== 'remove' && b.getAttribute('aria-disabled') === 'true') {
      const other = row.querySelector(what === 'up' ? '[data-step-act="down"]' : '[data-step-act="up"]');
      if (other) other.focus();
    } else if (what !== 'remove') b.focus();
  });
  const add = el('button', { class: 'ds-btn ds-btn--text ds-add-row', type: 'button' });
  morph(add, html`${raw(icon('plus', 16))} Add a step`);
  add.addEventListener('click', () => {
    const row = stepRow({});
    M.flip(steps, () => steps.append(row));
    number();
    row.querySelector('textarea').focus();
  });
  number();

  const aside = el('input', { type: 'checkbox', checked: !!test.skip || null });
  const reasonIn = el('input', { class: 'ds-input', id: id + '-reason', value: (test.skip && test.skip.reason) || '', autocomplete: 'off' });
  const reasonField = field('reason', 'Why it is set aside', reasonIn);
  reasonField.hidden = !test.skip;
  aside.addEventListener('change', () => {
    reasonField.hidden = !aside.checked;
    if (aside.checked) reasonIn.focus();
  });

  const fields = [
    field('title', 'Title', titleIn),
    field('goal', 'Goal', goalIn, true),
    el('div', { class: 'ds-form-grid ds-form-grid--2' },
      field('env', 'Environment', envIn, true),
      el('div', { class: 'ds-field' },
        el('label', { class: 'ds-field__label', for: id + '-tags' }, 'Tags ', el('span', { class: 'ds-field__optional', text: '(optional)' })),
        tagsIn,
        el('small', { class: 'ds-field__hint', id: id + '-tags-hint', text: 'Separated by commas' }))),
    el('fieldset', { class: 'ds-field so-tedit__group' }, el('legend', { class: 'ds-field__label', text: 'Steps' }), steps, add),
    el('div', { class: 'so-tedit__aside' },
      el('label', { class: 'ds-check' }, aside, el('span', { text: 'Set this test aside' })),
      el('small', { class: 'ds-field__hint', text: 'A test set aside is skipped until it is brought back.' }),
      reasonField),
  ];

  return formDialog({
    title: 'Edit ' + test.id, fields, saveLabel: 'Save', opener,
    save: async () => {
      const body = {
        title: titleIn.value.trim(),
        goal: goalIn.value,
        environment: envIn.value.trim(),
        tags: tagsIn.value.split(',').map((t) => t.trim()).filter(Boolean),
        steps: rows().map((row) => ({
          action: row.querySelector('.so-tedit__action').value.trim(),
          expect: row.querySelector('.so-tedit__expect').value.trim(),
        })),
      };
      if (aside.checked) {
        if (!reasonIn.value.trim()) {
          reasonIn.focus();
          throw new Error('Say why the test is set aside.');
        }
        body.skip = reasonIn.value.trim();
      } else if (test.skip) {
        body.skip = '';
      }
      const d = await api(base, { method: 'PATCH', body, attempts: 1 });
      toast(test.id + ' is saved.');
      return d.test;
    },
  });
}
