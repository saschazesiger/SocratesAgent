// A cell of a test run in a side sheet (docs/design/TEST-MODE.md, "The Tests
// page", Cell): its verdict as the agent gave it and as the reviewer left
// it, every attempt, the steps as a timeline with their screenshots and the
// actions behind them, its links, logs and bugs. A screenshot opens full
// size with the cell's others before and after it. A test opens in the same
// sheet: its text and how each of its cells stood over the last runs.

import { api, errorMessage, toast } from './api.js';
import { DS, html, raw, icon, morph } from './ui.js';
import * as accounts from './accounts.js';
import * as harnesses from './harnesses.js';
import * as V from './testsview.js';

const $ = (id) => document.getElementById(id);

export function mountCell(ctx) {
  const { state } = ctx;
  const sheet = $('cellSheet');
  const body = $('cellBody');
  const foot = $('cellFoot');
  const viewer = $('shotDialog');
  let open = false;
  let shown = null; // { kind: 'cell' | 'test', id, run }
  let view = null; // the attempt (AttemptView)
  let testHistory = {}; // cell id -> points, for the open test
  let shots = []; // { name, caption } of the open attempt, in step order
  let shotAt = 0;
  let asked = 0;

  sheet.addEventListener('ds-close', (event) => {
    if (event.target !== sheet) return;
    open = false;
    shown = null;
    view = null;
    ctx.closeRoute();
  });

  function title(text, sub) {
    if ($('cellTitle').textContent !== text) $('cellTitle').textContent = text;
    if ($('cellSub').textContent !== sub) $('cellSub').textContent = sub;
  }

  async function show() {
    if (open) return;
    open = true;
    await DS.overlay.open(sheet, { opener: document.activeElement });
  }

  function loading(words) {
    morph(body, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>${words}</div>`);
    morph(foot, '');
  }

  function failed(err) {
    morph(body, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${errorMessage(err)}</span></div>`);
  }

  /* ------------------------------------------------------------- a cell */

  async function openCell(id) {
    if (open && shown && shown.kind === 'cell' && shown.id === id && shown.run === state.run) return;
    const fresh = !open || !shown || shown.kind !== 'cell' || !view || !view.attempts.some((a) => a.id === id);
    shown = { kind: 'cell', id, run: state.run };
    if (fresh) {
      title('Cell', '');
      loading('Reading the cell');
    }
    show();
    await load();
  }

  async function load() {
    if (!shown || shown.kind !== 'cell') return;
    const me = ++asked;
    const { id, run } = shown;
    try {
      const data = await api(V.paths.attempt(run, id));
      if (me !== asked) return;
      view = data;
    } catch (err) {
      if (me === asked) failed(err);
      return;
    }
    if (view.bugs && view.bugs.length) await ctx.ensureBugs();
    if (me === asked) drawCell();
  }

  function drawCell() {
    const a = view;
    const dims = (a.dims || []).map((d) => d.label || d.id).join(' · ');
    title(a.title || a.test, [dims, 'attempt ' + a.attempt].filter(Boolean).join(' · '));
    shots = collectShots(a);
    const reviewChanged = a.reviewed && a.reviewed !== a.verdict;
    const review = a.review || null;
    morph(body, html`<div class="so-tests__stack">
      <div class="so-tests__verdict">
        ${a.final ? V.verdictChip(a.final) : V.cellChip({ state: a.state, wait: a.wait })}
        ${reviewChanged ? html`<span class="ds-meta">The agent said ${V.verdictWord(a.verdict)}; the reviewer made it ${V.verdictWord(a.reviewed)}.</span>`
          : review && review.state === 'done' ? html`<span class="ds-meta">${raw(icon('eye', 16))}Reviewed, unchanged</span>` : ''}
      </div>
      ${review && review.note ? html`<div class="ds-alert">${raw(icon('eye', 20))}<div class="ds-alert__text"><p class="ds-alert__title">The reviewer</p><p>${review.note}</p></div></div>` : ''}
      ${a.state === 'waiting' && a.wait ? html`<div class="ds-alert">${raw(icon('clock', 20))}<span class="ds-alert__text">${a.wait}</span></div>` : ''}
      ${a.end_reason ? html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text">${a.end_reason}</span></div>` : ''}
      ${a.summary ? html`<p class="so-break">${a.summary}</p>` : ''}
      ${a.attempts && a.attempts.length > 1 ? attemptSwitch(a) : ''}
      ${facts(a)}
      <section class="so-tests__stack" aria-labelledby="stepsTitle">
        <h3 class="ds-card__subtitle" id="stepsTitle">Steps</h3>
        <ol class="ds-timeline" role="list" aria-label="Steps">${(a.steps || []).map((s) => stepItem(a, s))}</ol>
      </section>
      ${otherShots(a)}
      ${linksAndLogs(a)}
      ${bugsOf(a)}
    </div>`);
    morph(foot, html`${a.chat ? html`<a class="ds-btn ds-btn--text" href="${'/#open=' + encodeURIComponent(a.chat)}">${raw(icon('chat', 16))}Open transcript</a>` : ''}
      <button class="ds-btn" type="button" data-retry-cell="${a.cell_id}"${V.OPEN_ATTEMPT.has(a.state) ? raw(' aria-disabled="true" data-tip="The cell is still going"') : ''}>${raw(icon('sync', 16))}Retry</button>`);
  }

  function attemptSwitch(a) {
    return html`<fieldset class="ds-chips">
      <legend class="ds-sr">Attempt</legend>
      ${a.attempts.map((x) => html`<label class="ds-chip"><input class="ds-chip__input" type="radio" name="cellAttempt" value="${x.id}"${x.id === a.id ? raw(' checked') : ''} data-attempt="${x.id}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${'Attempt ' + x.attempt + ' · ' + (x.final ? V.verdictWord(x.final) : V.attemptWord(x.state))}</span></span></label>`)}
    </fieldset>`;
  }

  function facts(a) {
    const role = a.role || {};
    const model = role.model ? (harnesses.modelOf(role.harness, role.model) || {}).label || role.model : '';
    const rows = [
      ['Test', a.test],
      ...(a.dims || []).map((d) => [d.name.charAt(0).toUpperCase() + d.name.slice(1), d.label ? d.label + ' (' + d.id + ')' : d.id]),
      ['Priority', [a.priority, ...(a.tags || [])].filter(Boolean).join(' · ')],
      ['State', V.attemptWord(a.state)],
      ['Agent', [harnesses.label(role.harness), model, role.effort ? harnesses.effortLabel(role.effort) : ''].filter(Boolean).join(' · ')],
      ['Account', role.account ? accounts.labelOf(role.account) || role.account : ''],
      ['Lease', (a.leases || []).map((l) => (l.label || 'member ' + l.member) + ' (' + l.tool + ')').join(', ')],
      ['Queued', V.whenLong(a.queued_at)],
      ['Started', V.whenLong(a.started_at)],
      ['Ended', V.whenLong(a.ended_at)],
      ['Took', a.started_at ? V.span((a.ended_at || Date.now()) - a.started_at) : ''],
      ['Waited', V.span(a.waited_ms)],
    ].filter(([, value]) => value);
    return html`<dl class="ds-facts ds-facts--rows">${rows.map(([k, v]) => html`<div><dt>${k}</dt><dd class="so-break">${v}</dd></div>`)}</dl>`;
  }

  // The screenshots of a step: those of its actions, then what it attached.
  function stepShots(a, step) {
    const out = [];
    for (const n of step.actions || []) {
      const act = (a.actions || []).find((x) => x.n === n);
      if (act && act.screenshot && !out.some((s) => s.name === act.screenshot)) out.push({ name: act.screenshot, caption: 'Step ' + step.n + ' · ' + (act.summary || act.action) });
    }
    for (const name of step.attach || []) if (!out.some((s) => s.name === name)) out.push({ name, caption: 'Step ' + step.n + ' · attached' });
    return out;
  }

  function collectShots(a) {
    const out = [];
    for (const step of a.steps || []) for (const s of stepShots(a, step)) if (!out.some((x) => x.name === s.name)) out.push(s);
    for (const act of a.actions || []) {
      if (act.screenshot && !out.some((x) => x.name === act.screenshot)) out.push({ name: act.screenshot, caption: 'Action ' + act.n + ' · ' + (act.summary || act.action) });
    }
    return out;
  }

  function thumbs(a, list) {
    if (!list.length) return '';
    return html`<div class="so-tests__shots">${list.map((s) => {
      const i = shots.findIndex((x) => x.name === s.name);
      return html`<button class="ds-pressable so-tests__shot" type="button" data-shot="${i}" aria-label="${'Screenshot: ' + s.caption}" data-tip="${s.caption}"><img src="${V.paths.file(a.run, a.id, s.name)}" alt="" loading="lazy" decoding="async"></button>`;
    })}</div>`;
  }

  function stepItem(a, step) {
    const r = step.review;
    const verdict = r && r.verdict ? r.verdict : step.verdict;
    const acts = (step.actions || []).map((n) => (a.actions || []).find((x) => x.n === n)).filter(Boolean);
    return html`<li class="${'ds-timeline__item' + (verdict ? '' : ' ds-timeline__item--muted')}" data-ds-key="${'s' + step.n}">
      <div class="ds-timeline__head">
        <p class="ds-timeline__title">${step.n + '. ' + step.do}</p>
        ${verdict ? V.verdictChip(verdict) : html`<span class="ds-status">${raw(icon('minimize', 16))}<span>Not judged</span></span>`}
      </div>
      <p class="ds-timeline__meta">${'Expect: ' + step.expect}${step.at ? ' · ' + V.clock(step.at) : ''}</p>
      ${step.observed ? html`<p class="ds-timeline__text so-break">${step.observed}</p>` : ''}
      ${r ? html`<div class="ds-alert ds-alert--plain">${raw(icon('eye', 20))}<span class="ds-alert__text"><strong class="ds-alert__title">${'Reviewer: ' + V.verdictWord(r.verdict)}</strong> ${step.verdict && step.verdict !== r.verdict ? '(the agent said ' + V.verdictWord(step.verdict) + ') ' : ''}${r.note}</span></div>` : ''}
      ${thumbs(a, stepShots(a, step))}
      ${acts.length ? html`<details class="ds-collapsible ds-disclosure">
        <summary>${acts.length === 1 ? '1 action' : acts.length + ' actions'}${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body">${actionList(acts)}</div>
      </details>` : ''}
    </li>`;
  }

  function actionList(acts) {
    return html`<ol class="ds-plain-list so-tests__actions">${acts.map((x) => html`<li class="so-break">
      <strong>${x.action}</strong> ${x.summary}${x.ms ? html` <span class="ds-meta">${V.span(x.ms) || x.ms + ' ms'}</span>` : ''}${x.interaction ? html` <span class="ds-tag ds-tag--neutral">interaction</span>` : ''}
      ${x.url ? html`<br><span class="ds-meta">${x.url}</span>` : ''}
      ${x.error ? html`<br><span class="ds-status ds-status--error">${raw(icon('alertCircle', 16))}<span>${x.error}</span></span>` : ''}
    </li>`)}</ol>`;
  }

  // The screenshots no step took as its own.
  function otherShots(a) {
    const inSteps = new Set((a.steps || []).flatMap((s) => stepShots(a, s).map((x) => x.name)));
    const rest = shots.filter((s) => !inSteps.has(s.name));
    const loose = (a.actions || []).filter((x) => !(a.steps || []).some((s) => (s.actions || []).includes(x.n)));
    if (!rest.length && !loose.length) return '';
    return html`<section class="so-tests__stack" aria-labelledby="otherTitle">
      <h3 class="ds-card__subtitle" id="otherTitle">Outside the steps</h3>
      ${thumbs(a, rest)}
      ${loose.length ? html`<details class="ds-collapsible ds-disclosure"><summary>${loose.length === 1 ? '1 action' : loose.length + ' actions'}${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body">${actionList(loose)}</div></details>` : ''}
    </section>`;
  }

  function linksAndLogs(a) {
    const links = Object.entries(a.links || {});
    const files = a.files || [];
    if (!links.length && !files.length) return '';
    return html`<section class="so-tests__stack" aria-labelledby="linksTitle">
      <h3 class="ds-card__subtitle" id="linksTitle">Links and logs</h3>
      <ul class="ds-plain-list">
        ${links.map(([label, url]) => html`<li><a href="${url}" target="_blank" rel="noopener noreferrer">${label}${raw(icon('external', 16))}</a></li>`)}
        ${files.map((name) => html`<li><a href="${V.paths.file(a.run, a.id, name)}" target="_blank" rel="noopener">${name.replace(/^logs\//, '')}</a></li>`)}
      </ul>
    </section>`;
  }

  function bugsOf(a) {
    if (!a.bugs || !a.bugs.length) return '';
    const known = (state.bugs && state.bugs.bugs) || [];
    return html`<section class="so-tests__stack" aria-labelledby="cellBugsTitle">
      <h3 class="ds-card__subtitle" id="cellBugsTitle">Bugs</h3>
      <ul class="ds-plain-list">${a.bugs.map((id) => {
        const bug = known.find((b) => b.id === id);
        return html`<li>${bug ? html`${V.severityChip(bug.severity)} <strong>${id}</strong> ${bug.title}` : id}</li>`;
      })}</ul>
    </section>`;
  }

  body.addEventListener('change', (event) => {
    const input = event.target.closest('[data-attempt]');
    if (input && input.checked) ctx.go({ cell: input.dataset.attempt });
  });

  body.addEventListener('click', (event) => {
    const thumb = event.target.closest('[data-shot]');
    if (thumb) { openShot(Number(thumb.dataset.shot), thumb); return; }
    const cellBtn = event.target.closest('[data-cell]');
    if (cellBtn) ctx.go({ cell: cellBtn.dataset.cell, test: '' });
  });

  foot.addEventListener('click', async (event) => {
    const retry = event.target.closest('[data-retry-cell]');
    if (!retry || !shown) return;
    try {
      const made = await DS.motion.busy(retry, api(V.paths.action(shown.run, 'retry'), { method: 'POST', body: { cell: retry.dataset.retryCell } }), { done: false });
      toast('Another attempt is queued.', 'success');
      ctx.refreshSoon();
      if (made && made.id) ctx.go({ cell: made.id });
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
  });

  /* -------------------------------------------------------- the viewer */

  function drawShot() {
    const s = shots[shotAt];
    if (!s || !view) return;
    $('shotTitle').textContent = 'Screenshot ' + (shotAt + 1) + ' of ' + shots.length;
    morph($('shotBody'), html`<figure class="so-shotview__figure">
      <img class="so-shotview__img" src="${V.paths.file(view.run, view.id, s.name)}" alt="${s.caption}">
      <figcaption class="ds-meta">${s.caption}</figcaption>
    </figure>`);
    $('shotPrev').setAttribute('aria-disabled', String(shotAt === 0));
    $('shotNext').setAttribute('aria-disabled', String(shotAt >= shots.length - 1));
  }

  function openShot(i, opener) {
    if (!shots[i]) return;
    shotAt = i;
    drawShot();
    DS.overlay.open(viewer, { opener });
  }

  function step(by) {
    const next = shotAt + by;
    if (next < 0 || next >= shots.length) return;
    shotAt = next;
    drawShot();
  }

  $('shotPrev').addEventListener('click', () => step(-1));
  $('shotNext').addEventListener('click', () => step(1));
  viewer.addEventListener('keydown', (event) => {
    if (event.key === 'ArrowLeft') { event.preventDefault(); step(-1); }
    if (event.key === 'ArrowRight') { event.preventDefault(); step(1); }
  });

  /* ------------------------------------------------------------- a test */

  async function openTest(id) {
    if (open && shown && shown.kind === 'test' && shown.id === id) return;
    shown = { kind: 'test', id, run: state.run };
    view = null;
    title('Test', id);
    loading('Reading the test');
    show();
    const me = ++asked;
    const cells = state.cells.filter((r) => r.test === id);
    const [, ...histories] = await Promise.all([
      ctx.ensureCatalog(),
      ...cells.map((r) => api(V.paths.history(state.repo) + '&cell=' + encodeURIComponent(r.cell_id)).catch(() => ({ history: {} }))),
    ]);
    if (me !== asked) return;
    testHistory = {};
    for (const h of histories) Object.assign(testHistory, h.history || {});
    drawTest();
  }

  function drawTest() {
    const id = shown.id;
    let test = null;
    for (const area of (state.catalog && state.catalog.areas) || []) {
      test = (area.tests || []).find((t) => t.id === id) || test;
    }
    const cells = state.cells.filter((r) => r.test === id);
    const first = cells[0];
    title((test && test.title) || (first && first.title) || id, [id, (test || first || {}).priority].filter(Boolean).join(' · '));
    morph(body, html`<div class="so-tests__stack">
      ${test && test.tags && test.tags.length ? html`<p>${test.tags.map((t) => html`<span class="ds-tag">${t}</span> `)}</p>` : ''}
      ${test && test.context ? html`<p class="so-break">${test.context}</p>` : ''}
      ${test ? html`<section class="so-tests__stack" aria-labelledby="testStepsTitle">
        <h3 class="ds-card__subtitle" id="testStepsTitle">Steps</h3>
        <ol class="ds-plain-list">${test.steps.map((s) => html`<li class="so-break">${s.n + '. ' + s.do}<br><span class="ds-meta">${'Expect: ' + s.expect}</span></li>`)}</ol>
      </section>` : html`<p class="ds-muted">The catalog no longer has this test.</p>`}
      <section class="so-tests__stack" aria-labelledby="testCellsTitle">
        <h3 class="ds-card__subtitle" id="testCellsTitle">Its cells in this run</h3>
        ${cells.length ? html`<ul class="ds-list ds-list--interactive" aria-label="Cells">${cells.map((r) => html`<li class="ds-list__item" data-ds-key="${r.id}">
          <span class="ds-list__text">
            <span class="ds-list__title"><button class="ds-list__action" type="button" data-cell="${r.id}">${(r.dims || []).map((d) => d.label || d.id).join(' · ') || r.cell}</button></span>
            <span class="ds-list__meta">${historyLine(r.cell_id)}</span>
          </span>
          ${V.cellChip({ ...r, wait: '' })}
        </li>`)}</ul>` : html`<p class="ds-muted">This run has no cell of it.</p>`}
      </section>
    </div>`);
    morph(foot, '');
  }

  function historyLine(cellId) {
    const points = (testHistory[cellId] || []).slice().reverse();
    if (!points.length) return '';
    return html`<span class="so-tests__history" role="list" aria-label="Last runs, oldest first">${points.map((p) => html`<span role="listitem">${V.mark(p.verdict, (V.VERDICT[p.verdict] ? V.verdictWord(p.verdict) : V.attemptWord(p.verdict)) + ' · ' + V.when(p.created_at))}</span>`)}</span>`;
  }

  return {
    openCell,
    openTest,
    close() {
      if (!open) return;
      open = false;
      shown = null;
      view = null;
      DS.overlay.close(sheet);
    },
    /** refresh reads the open cell again (its run changed). */
    refresh() {
      if (!open || !shown) return;
      if (shown.kind === 'cell' && view && (V.OPEN_ATTEMPT.has(view.state) || (view.review && (view.review.state === 'pending' || view.review.state === 'running')))) load();
      else if (shown.kind === 'test') drawTest();
    },
  };
}
