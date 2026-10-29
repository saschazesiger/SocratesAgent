// The Overview of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", Overview): how the repository stands. While a run is active, Live:
// a card per running cell with its latest screenshot and step, the waiting
// cells with their reasons, the pools. Then the latest finished run as tiles
// per verdict with the change against the run before; what needs attention
// (cells that broke, cells still failing, flaky tests, the open critical and
// major bugs); what was fixed; and the trend of the last 20 runs.
//
// The numbers are the server's (GET …/overview); the live part is the
// frame's active run (state.live), read every few seconds.

import { api, toast, errorMessage } from './api.js';
import { M, html, raw, icon, morph } from './ui.js';
import * as V from './testsview.js';

const SHOWN = 8; // rows per group of Needs attention before "all of them"
const TREND = 20;

export function mount(ctx) {
  const { state } = ctx;
  let data = null; // the overview, normalised (below)
  let loadingKey = '';
  let error = '';
  // The cells of each list drawn (its label → [{ attempt, run }]): what the
  // cell viewer's previous and next walk when a cell of it is opened.
  const lists = new Map();

  // key says which runs the overview was worked out from: a new finished run
  // (or one deleted) makes it stale; a running run's frames do not.
  function key() {
    const done = state.runs.filter((r) => !V.ACTIVE_RUN.has(r.state));
    return [state.repo, done.length, done[0] ? done[0].id + done[0].state + (done[0].bugs || 0) : '', state.openBugs, state.runs.length].join('|');
  }

  async function load() {
    const k = key();
    if (loadingKey === k) return;
    loadingKey = k;
    try {
      const next = await fetchOverview();
      if (k !== key()) return;
      data = { ...next, key: k };
      error = '';
    } catch (err) {
      if (k !== key()) return;
      error = errorMessage(err);
      data = data || null;
    } finally {
      if (loadingKey === k) loadingKey = '';
    }
    ctx.redraw('overview');
  }

  // fetchOverview reads GET …/overview: the two latest finished runs, the
  // trend, the cells that changed or keep failing, flaky tests, open bugs.
  async function fetchOverview() {
    const o = await api(V.paths.overview(state.repo));
    const runOf = (r) => (r ? { ...r, n: (ctx.runOf(r.id) || {}).n || r.number } : null);
    const latest = runOf(o.latest);
    const previous = runOf(o.previous);
    const rowRun = (row) => ({ ...row, run: row.run || (latest && latest.id) });
    return {
      latest,
      previous,
      trend: (o.trend || []).map(runOf),
      regressed: (o.regressed || []).map(rowRun),
      // Still failing: the problems that are not new since the run before.
      failing: (o.failing || []).filter((r) => r.change !== 'regressed').map(rowRun),
      fixed: (o.fixed || []).map(rowRun),
      flaky: o.flaky || [],
      bugs: (o.bugs || []).filter((b) => (!b.status || b.status === 'open') && (b.severity === 'critical' || b.severity === 'major')),
    };
  }

  /* ------------------------------------------------------------ drawing */

  function render(panel) {
    if (!panel.dataset.wired) wire(panel);
    if (!state.runs.length) {
      morph(panel, html`${liveCard()}${firstRun()}`);
      return;
    }
    if (!data || data.key !== key()) load();
    if (!data) {
      morph(panel, html`${liveCard()}${error
        ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${error}</span></div>`
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading how the repository stands</div>`}`);
      return;
    }
    const d = data;
    morph(panel, html`${liveCard()}
      ${d.latest ? latestCard(d) : ''}
      ${d.latest ? attentionCard(d) : ''}
      <div class="ds-grid-2">
        ${trendCard(d)}
        ${d.latest ? fixedCard(d) : ''}
      </div>`);
    for (const value of panel.querySelectorAll('[data-count]')) M.countUp(value, Number(value.dataset.count));
  }

  // The empty repository: the catalog's size, and Run everything.
  function firstRun() {
    const cat = state.catalog;
    if (!cat && !state.catalogError) {
      ctx.ensureCatalog().then(() => ctx.redraw('overview'));
    }
    const areas = cat ? cat.areas || [] : [];
    const tests = areas.reduce((n, a) => n + (a.tests || []).length, 0);
    const errors = cat ? (cat.errors || []).length : 0;
    const size = cat ? V.plural(tests, 'test') + ' in ' + V.plural(areas.length, 'area') + ': ' + V.plural(cat.cells || 0, 'cell') + ', each run by an agent of its own.' : '';
    return html`${errors ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${errors === 1 ? 'The catalog has an error: a run would not start.' : 'The catalog has ' + errors + ' errors: a run would not start.'}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-go-view="tests">Show</button></div>` : ''}
    ${state.catalogError ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${state.catalogError}</span></div>` : ''}
    <section class="ds-card" aria-label="No run yet">
      <div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('play', 24))}</span>
        <p class="ds-empty__title">No run yet</p>
        <p class="ds-empty__text">${size ? 'The catalog has ' + size : 'Run the catalog, and how every test stands shows up here.'}</p>
        <button class="ds-btn ds-btn--tonal" type="button" data-ov-run="all"${errors || !cat ? raw(' aria-disabled="true"') : ''}>${raw(icon('play', 16))}Run everything</button>
      </div>
    </section>`;
  }

  /* ----------------------------------------------------------------- live */

  function liveCard() {
    const live = state.live;
    if (!live) return '';
    const run = ctx.runOf(live.id) || { ...live.detail, n: live.detail.number };
    const cells = live.cells || [];
    const running = cells.filter((r) => r.state === 'running' || r.state === 'starting');
    const waiting = cells.filter((r) => r.state === 'waiting' || r.state === 'queued');
    const pools = (live.detail && live.detail.pools) || [];
    const now = Date.now();
    return html`<section class="ds-card" aria-labelledby="ovLiveTitle" data-ds-key="live">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="ovLiveTitle">${'Live: ' + V.runName(run)}</h2>
        <button class="ds-btn ds-btn--sm" type="button" data-watch="${live.id}">${raw(icon('eye', 16))}Watch</button>
      </div>
      ${running.length ? html`<div class="so-tests-live" role="list" aria-label="Running cells">${running.map((r) => liveCell(r, live.id, now))}</div>`
        : html`<p class="ds-muted">${waiting.length ? 'No cell is running yet.' : 'The cells are done; the reviewer and the analyst are at work.'}</p>`}
      ${waiting.length ? html`<h3 class="ds-card__subtitle">${'Waiting · ' + waiting.length}</h3>
        <ul class="ds-list ds-list--interactive" aria-label="Waiting cells">${waiting.slice(0, SHOWN).map((r) => html`<li class="ds-list__item" data-ds-key="${'w' + r.id}">
          <span class="ds-list__text">
            <span class="ds-list__title"><button class="ds-list__action" type="button" data-cell="${r.id}" data-run="${live.id}">${r.title}</button></span>
            <span class="ds-list__meta">${[V.dimsWords(r.dims), r.state === 'waiting' ? r.wait || 'Waiting' : 'Queued'].filter(Boolean).join(' · ')}</span>
          </span>
          ${V.cellChip(r, { tip: false })}
        </li>`)}</ul>
        ${waiting.length > SHOWN ? html`<p class="ds-meta">${'And ' + (waiting.length - SHOWN) + ' more in the queue.'}</p>` : ''}` : ''}
      ${pools.length ? html`<h3 class="ds-card__subtitle">Accounts of the tools</h3>
        ${V.poolList(pools, cells)}` : ''}
    </section>`;
  }

  function liveCell(r, runId, now) {
    const l = r.live || {};
    const shot = l.shot || r.shot;
    const step = l.steps ? 'Step ' + Math.min(l.step || 1, l.steps) + ' of ' + l.steps + (l.title ? ': ' + l.title : '') : r.state === 'starting' ? 'Starting the session' : '';
    const took = r.started_at ? V.span(now - r.started_at) : '';
    return html`<div class="ds-card ds-card--filled ds-card--interactive so-tests-live__card" role="listitem" data-ds-key="${'l' + r.id}">
      <div class="so-tests-live__shot">${shot
        ? html`<img src="${V.paths.file(runId, r.id, shot)}" alt="${'Latest screenshot of ' + r.title}" loading="lazy" decoding="async">`
        : html`<span class="so-tests-live__none" aria-hidden="true">${raw(icon('image', 24))}</span>`}</div>
      <h3 class="ds-card__title"><button class="ds-card__action" type="button" data-cell="${r.id}" data-run="${runId}" data-list="live">${r.title}</button></h3>
      <p class="ds-meta">${[V.dimsWords(r.dims), took ? 'for ' + took : ''].filter(Boolean).join(' · ')}</p>
      ${step ? html`<p class="so-tests-live__step so-break">${step}</p>` : ''}
      ${l.activity ? html`<p class="ds-meta so-break">${l.activity}</p>` : ''}
    </div>`;
  }

  /* --------------------------------------------------------------- latest */

  function delta(now, before, prevRun) {
    if (!prevRun) return '';
    const d = now - before;
    const name = V.runName(prevRun, { name: false });
    return d === 0 ? 'same as ' + name : (d > 0 ? '+' + d : '−' + -d) + ' since ' + name;
  }

  function latestCard(d) {
    const run = d.latest;
    const v = (run.counts && run.counts.verdicts) || {};
    const pv = (d.previous && d.previous.counts && d.previous.counts.verdicts) || {};
    const rate = V.passRate(run.counts);
    const tile = (verdict, extra) => {
      const n = v[verdict] || 0;
      const note = [extra, delta(n, pv[verdict] || 0, d.previous)].filter(Boolean).join(' · ');
      return html`<button class="ds-tile" type="button" data-ov-verdict="${verdict}" aria-label="${n + ' ' + V.verdictWord(verdict) + ' in ' + V.runName(run, { name: false }) + (note ? ', ' + note : '') + ': show them'}">
        <span class="ds-tile__label">${raw(icon(V.VERDICT[verdict].glyph, 16))}<span>${V.verdictWord(verdict)}</span></span>
        <span class="ds-tile__value" data-count="${n}">${n}</span>
        <span class="ds-tile__note">${note}</span>
      </button>`;
    };
    const meta = [
      (V.ACTIVE_RUN.has(run.state) ? 'Started ' : 'Finished ') + V.ago(run.ended_at || run.created_at),
      V.selectionWords(run.selection, state.runs),
      V.runSpan(run),
      V.money(run.cost),
    ].filter(Boolean).join(' · ');
    return html`<section class="ds-card" aria-labelledby="ovLatestTitle" data-ds-key="latest">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="ovLatestTitle">${'Latest run: ' + V.runName(run)}</h2>
        <button class="ds-btn ds-btn--sm" type="button" data-open-run="${run.id}">Open${raw(icon('arrowRight', 16))}</button>
      </div>
      <p class="ds-meta">${meta}</p>
      ${rate !== null ? html`<div class="so-tests-ov__rate">
        <span class="ds-meter" role="meter" aria-label="Passed" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(rate * 100)}" aria-valuetext="${Math.round(rate * 100) + '% of the judged cells passed'}" style="${'--ds-share: ' + rate.toFixed(4)}"></span>
        <p class="ds-meta"><strong>${Math.round(rate * 100) + '%'}</strong> of the judged cells passed${d.previous && V.passRate(d.previous.counts) !== null ? html` (${Math.round(V.passRate(d.previous.counts) * 100) + '% in ' + V.runName(d.previous, { name: false })})` : ''}</p>
      </div>` : ''}
      <div class="ds-tiles so-tests-ov__tiles" role="group" aria-label="${'Cells of ' + V.runName(run, { name: false }) + ' by verdict'}">
        ${tile('fail')}${tile('issue')}${tile('blocked')}${tile('pass', v.na ? v.na + ' n/a' : '')}
      </div>
    </section>`;
  }

  /* -------------------------------------------------------- needs attention */

  function cellRows(rows, label, meta) {
    lists.set(label, rows.map((r) => ({ attempt: r.id, run: r.run })));
    return html`<ul class="ds-list ds-list--interactive" aria-label="${label}">${rows.slice(0, SHOWN).map((r) => html`<li class="ds-list__item" data-ds-key="${label + r.id}">
      <span class="ds-list__text">
        <span class="ds-list__title"><button class="ds-list__action" type="button" data-cell="${r.id}" data-run="${r.run}" data-list="${label}">${r.title}</button></span>
        <span class="ds-list__meta">${[V.dimsWords(r.dims), meta(r)].filter(Boolean).join(' · ')}</span>
      </span>
      ${V.cellChip(r, { tip: false })}
    </li>`)}</ul>${rows.length > SHOWN ? html`<p class="ds-meta">${'And ' + (rows.length - SHOWN) + ' more.'}</p>` : ''}`;
  }

  function attentionCard(d) {
    const prevName = d.previous ? V.runName(d.previous, { name: false }) : 'the run before';
    const was = (r) => (r.prev && r.prev.verdict ? 'was ' + V.verdictWord(r.prev.verdict) + ' in Run ' + (r.prev.number || '?') : '');
    const since = (r) => {
      const s = r.since;
      const n = s && typeof s === 'object' ? s.number || (ctx.runOf(s.run) || {}).n : typeof s === 'string' ? (ctx.runOf(s) || {}).n : s;
      return n ? 'failing since Run ' + n : '';
    };
    const groups = [];
    if (d.regressed.length) {
      groups.push(html`<h3 class="ds-card__subtitle">${'Broke since ' + prevName} <span class="ds-badge ds-badge--error">${d.regressed.length}</span></h3>
        ${cellRows(d.regressed, 'Broke since ' + prevName, was)}`);
    }
    if (d.failing.length) {
      groups.push(html`<h3 class="ds-card__subtitle">Still failing <span class="ds-badge ds-badge--warning">${d.failing.length}</span></h3>
        ${cellRows(d.failing, 'Still failing', since)}`);
    }
    if (d.flaky.length) {
      groups.push(html`<h3 class="ds-card__subtitle">Flaky tests <span class="ds-badge ds-badge--neutral">${d.flaky.length}</span></h3>
        <ul class="ds-list ds-list--interactive" aria-label="Flaky tests">${d.flaky.slice(0, SHOWN).map((t) => html`<li class="ds-list__item" data-ds-key="${'f' + t.test}">
          <span class="ds-list__text">
            <span class="ds-list__title"><button class="ds-list__action" type="button" data-test="${t.test}">${t.title || t.test}</button></span>
            <span class="ds-list__meta">${'Passed and failed by turns, ' + V.plural(t.flips || 0, 'time') + ' over the last runs' + ((t.cells || []).length ? ' · ' + V.plural(t.cells.length, 'cell') : '')}</span>
          </span>
          ${V.chip({ word: 'Flaky', glyph: 'activity', tone: 'warning' })}
        </li>`)}</ul>`);
    }
    if (d.bugs.length) {
      groups.push(html`<h3 class="ds-card__subtitle">Open critical and major bugs <span class="ds-badge ds-badge--error">${d.bugs.length}</span></h3>
        <ul class="ds-list ds-list--interactive" aria-label="Open critical and major bugs">${d.bugs.slice(0, SHOWN).map((b) => html`<li class="ds-list__item" data-ds-key="${'b' + b.id}">
          <span class="ds-list__text">
            <span class="ds-list__title"><button class="ds-list__action" type="button" data-bug="${b.id}">${b.id + ' · ' + b.title}</button></span>
            <span class="ds-list__meta">${[b.seen || (b.runs && b.runs.length) ? 'seen in ' + V.plural(b.seen || b.runs.length, 'run') : '', b.last_number ? 'last in Run ' + b.last_number : '', V.plural((b.cells || []).length, 'cell')].filter(Boolean).join(' · ')}</span>
          </span>
          ${V.severityChip(b.severity)}
        </li>`)}</ul>`);
    }
    const problems = V.problemCount(d.latest.counts);
    return html`<section class="ds-card" aria-labelledby="ovAttentionTitle" data-ds-key="attention">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="ovAttentionTitle">Needs attention</h2>
        ${problems ? html`<button class="ds-btn ds-btn--sm" type="button" data-ov-rerun="${d.latest.id}">${raw(icon('sync', 16))}Run the problems again</button>` : ''}
      </div>
      ${groups.length ? groups : html`<div class="ds-alert ds-alert--success">${raw(icon('checkCircle', 20))}<span class="ds-alert__text">${'Nothing: ' + (problems ? 'every problem of ' + V.runName(d.latest, { name: false }) + ' is one it had before and is not failing.' : 'every judged cell of ' + V.runName(d.latest, { name: false }) + ' passed.')}</span></div>`}
    </section>`;
  }

  function fixedCard(d) {
    const was = (r) => (r.prev && r.prev.verdict ? 'was ' + V.verdictWord(r.prev.verdict) + ' in Run ' + (r.prev.number || '?') : '');
    const title = d.previous ? 'Fixed since ' + V.runName(d.previous, { name: false }) : 'Fixed since the run before';
    return html`<section class="ds-card" aria-labelledby="ovFixedTitle" data-ds-key="fixed">
      <h2 class="ds-card__title" id="ovFixedTitle">${title}</h2>
      ${d.fixed.length ? cellRows(d.fixed, title, was)
        : html`<p class="ds-muted">${d.previous ? 'No cell that failed in ' + V.runName(d.previous, { name: false }) + ' passes now.' : 'There is no run before it to compare with.'}</p>`}
    </section>`;
  }

  /* ---------------------------------------------------------------- trend */

  function trendCard(d) {
    const runs = d.trend.filter(Boolean);
    const most = runs.reduce((m, r) => Math.max(m, (r.counts && r.counts.cells) || 0), 0) || 1;
    const words = (r) => V.runName(r) + ', ' + V.when(r.created_at) + ': ' + V.countsWords(r.counts);
    return html`<section class="ds-card" aria-labelledby="ovTrendTitle" data-ds-key="trend">
      <h2 class="ds-card__title" id="ovTrendTitle">${'The last ' + V.plural(runs.length, 'run')}</h2>
      ${runs.length ? html`<figure class="so-tests-trend">
        <figcaption class="ds-sr">Cells of each run by verdict, oldest first. Each column opens its run.</figcaption>
        <ol class="so-tests-trend__plot">${runs.map((r) => {
          const c = r.counts || {};
          const v = c.verdicts || {};
          const share = Math.max(0.04, (c.cells || 0) / most);
          return html`<li class="so-tests-trend__item" data-ds-key="${'t' + r.id}">
            <button class="ds-pressable so-tests-trend__col" type="button" data-open-run="${r.id}" aria-label="${words(r)}" data-tip="${words(r)}"${r.id === (d.latest && d.latest.id) ? raw(' aria-current="true"') : ''}>
              <span class="so-tests-trend__stack" style="${'--so-share: ' + share.toFixed(4)}">${['blocked', 'fail', 'issue', 'pass'].filter((k) => v[k]).map((k) =>
                html`<span class="${'so-tests-bar__part so-tests-bar__part--' + k}" style="${'flex-grow: ' + v[k]}"></span>`)}${
                V.ACTIVE_RUN.has(r.state) ? html`<span class="so-tests-bar__part so-tests-bar__part--rest" style="${'flex-grow: ' + Math.max(1, (c.cells || 0) - (c.done || 0))}"></span>` : ''}</span>
              <span class="so-tests-trend__label">${String(r.n || '?')}</span>
            </button>
          </li>`;
        })}</ol>
      </figure>
      <ul class="ds-legend">
        <li><span class="ds-legend__key so-tests-key--pass"></span>Pass</li>
        <li><span class="ds-legend__key ds-legend__key--warning"></span>Issue</li>
        <li><span class="ds-legend__key ds-legend__key--error"></span>Fail</li>
        <li><span class="ds-legend__key ds-legend__key--neutral"></span>Blocked</li>
      </ul>` : html`<p class="ds-muted">No run has cells yet.</p>`}
    </section>`;
  }

  /* -------------------------------------------------------------- actions */

  function listOf(button) {
    const name = button.dataset.list;
    if (name === 'live') {
      const live = state.live;
      return live ? live.cells.filter((r) => r.state === 'running' || r.state === 'starting').map((r) => ({ attempt: r.id, run: live.id })) : [];
    }
    return lists.get(name) || [];
  }

  function wire(panel) {
    panel.dataset.wired = '1';
    panel.addEventListener('click', (event) => {
      const t = event.target;
      const cellBtn = t.closest('[data-cell]');
      if (cellBtn) { ctx.openCell(cellBtn.dataset.cell, cellBtn.dataset.run, listOf(cellBtn)); return; }
      const test = t.closest('[data-test]');
      if (test) { ctx.go({ view: 'tests', test: test.dataset.test, run: '', bug: '', cell: '' }, { push: true }); return; }
      const bug = t.closest('[data-bug]');
      if (bug) { ctx.go({ view: 'bugs', bug: bug.dataset.bug, run: '', test: '', cell: '' }, { push: true }); return; }
      const open = t.closest('[data-open-run]');
      if (open) { ctx.go({ view: 'runs', run: open.dataset.openRun, test: '', bug: '', cell: '' }, { push: true }); return; }
      const verdict = t.closest('[data-ov-verdict]');
      if (verdict && data && data.latest) {
        state.runPreset = { verdicts: [verdict.dataset.ovVerdict] };
        ctx.go({ view: 'runs', run: data.latest.id, test: '', bug: '', cell: '' }, { push: true });
        return;
      }
      const rerun = t.closest('[data-ov-rerun]');
      if (rerun) { ctx.startRun({ problems_of: rerun.dataset.ovRerun }, rerun); return; }
      if (t.closest('[data-ov-run="all"]')) {
        if (!state.catalog) toast('The catalog could not be read.', 'error');
        else ctx.startRun({}, t.closest('[data-ov-run]'));
      }
    });
  }

  return {
    render,
    refresh() { /* key() notices a new finished run; the live part is the frame's */ },
    repoChanged() { data = null; error = ''; loadingKey = ''; },
  };
}
