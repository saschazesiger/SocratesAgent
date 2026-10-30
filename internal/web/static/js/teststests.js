// The Tests view of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", Tests): the catalog and how each test stands, together.
//
// The list (view=tests): the catalog's errors and warnings first, with
// their file and line; a search and chips (Problems, Flaky, a priority);
// then per area a card with its dimensions, its instructions and Run this
// area, and its tests: each with its priority and tags, the latest verdict
// of each of its cells (from the latest run that ran it, a column per value
// of the area's `columns` dimension as in a run), "failing since Run N", a
// flaky mark, its last run and Run. A verdict opens that attempt in the cell
// viewer. At the end the tools: their secrets, pools and Check secrets.
//
// A test (view=tests&test=<id>): its head with the way back, its file,
// priority and tags, Run this test and Change it with an agent; its history
// over the last 20 runs as a grid (a row per cell, a column per run, the
// newest first, so the latest verdict stands next to the cell's name and a
// phone shows the recent runs without scrolling sideways), each verdict
// opening its attempt; what it does (context and steps); its open bugs.

import { api, errorMessage, toast } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import { markdown } from './md.js';
import * as V from './testsview.js';

const $ = (id) => document.getElementById(id);
const PHONE = window.matchMedia('(max-width: 600px)');
const TYPE_PAUSE = 200;
// While a run is active the health of the tests is read again at most this often.
const HEALTH_EVERY = 10000;
// More problems of the catalog than this fold into a list that opens.
const ALERTS_OPEN = 3;

export function mount(ctx) {
  const { state } = ctx;
  const filters = { q: '', problems: false, flaky: false, priority: new Set() };
  const checks = new Map(); // tool -> { result }
  const opened = new Set(); // the disclosures the person opened
  const tests = new Map(); // test id -> { data, error }
  let mode = ''; // 'list' or 'test:<id>': what the panel's frame is
  let visible = []; // the cells the list shows, in order: the cell viewer's previous and next
  let pendingArea = ''; // an area to scroll to once the list is drawn
  let healthKey = '';
  let healthAt = 0;

  /* -------------------------------------------------------------- data */

  async function loadTest(id) {
    const repo = state.repo;
    const have = tests.get(id);
    if (have && have.loading) return;
    tests.set(id, { ...(have || {}), loading: true });
    try {
      const data = await api(V.paths.test(repo, id));
      if (repo !== state.repo) return;
      tests.set(id, { data, error: '' });
    } catch (err) {
      if (repo !== state.repo) return;
      tests.set(id, { data: have && have.data, error: err && err.status === 404 ? 'gone' : errorMessage(err) });
    }
    ctx.redraw('tests');
  }

  // healthStale: the runs that make the tests' health changed since the
  // catalog was read (a run ended, one was deleted), or a run is active and
  // the health is older than HEALTH_EVERY.
  function healthStale() {
    const done = state.runs.filter((r) => !V.ACTIVE_RUN.has(r.state));
    const key = [state.repo, done.length, done[0] ? done[0].id + done[0].state : '', state.runs.length].join('|');
    const active = state.runs.some((r) => V.ACTIVE_RUN.has(r.state));
    if (key !== healthKey) { healthKey = key; return true; }
    return active && Date.now() - healthAt > HEALTH_EVERY;
  }

  const allTests = (cat) => (cat.areas || []).flatMap((a) => (a.tests || []).map((t) => ({ ...t, area: a })));
  const areaOf = (id) => ((state.catalog && state.catalog.areas) || []).find((a) => (a.tests || []).some((t) => t.id === id)) || null;
  const latestOf = (t) => (t.health && t.health.latest) || '';
  const problemOf = (t) => !!latestOf(t) && !V.passed(latestOf(t));
  const flakyOf = (t) => !!(t.health && t.health.flaky);

  /* ------------------------------------------------------------ render */

  function render(panel) {
    if (!panel.dataset.wired) wire(panel);
    const cat = state.catalog;
    if (!cat) {
      mode = '';
      if (!state.catalogError) ctx.ensureCatalog().then(() => ctx.redraw('tests'));
      morph(panel, state.catalogError
        ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${state.catalogError}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-tt-reload>Try again</button></div>`
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the catalog</div>`);
      return;
    }
    if (!healthAt) healthAt = Date.now();
    if (state.route.test) renderTest(panel, state.route.test);
    else renderList(panel, cat);
  }

  /* --------------------------------------------------------- the list */

  function listFrame() {
    return html`<div class="so-tests__stack" id="ttAlerts"></div>
      <div class="so-tests-tt__head">
        <p class="ds-meta" id="ttSize"></p>
        <div class="so-tests-tt__actions">
          <button class="ds-btn ds-btn--quiet" type="button" data-tt-reload>${raw(icon('sync', 16))}Read again</button>
          <button class="ds-btn ds-btn--tonal" type="button" data-tt-write aria-haspopup="dialog">${raw(icon('sparkles', 16))}Write a test with an agent</button>
        </div>
      </div>
      <div class="ds-toolbar so-tests-tt__bar">
        <div class="ds-filters">
          <div class="ds-field ds-field--grow">
            <label class="ds-sr" for="ttSearch">Search the tests by title, id or tag</label>
            <div class="ds-search">${raw(icon('search', 20))}<input class="ds-input" id="ttSearch" type="search" placeholder="Search tests by title, id or tag" autocomplete="off" spellcheck="false" aria-keyshortcuts="/" data-search><kbd class="ds-kbd ds-search__key" aria-hidden="true">/</kbd></div>
          </div>
        </div>
        <fieldset class="ds-chips" id="ttChips"><legend class="ds-sr">Show</legend></fieldset>
      </div>
      <div class="so-tests-countline"><p class="ds-meta" id="ttCount" aria-live="polite"></p><div id="ttLegend"></div></div>
      <div class="so-tests__stack" id="ttAreas"></div>
      <div class="so-tests__stack" id="ttTools"></div>`;
  }

  // wired is the elements a listener was bound to, which morph keeps.
  const wired = new WeakSet();

  function wireSearch() {
    const search = $('ttSearch');
    search.value = filters.q;
    // morph keeps the field when the list is drawn again: bound once.
    if (wired.has(search)) return;
    wired.add(search);
    let pause = 0;
    search.addEventListener('input', () => {
      clearTimeout(pause);
      pause = setTimeout(() => { filters.q = search.value.trim(); ctx.redraw('tests'); }, TYPE_PAUSE);
    });
  }

  function haystack(t) {
    return [t.title, t.id, t.priority, t.area.title, t.area.id, ...(t.tags || []).flatMap((x) => [x, '#' + x]),
      ...(t.cell_health || []).flatMap((c) => (c.dims || []).map((d) => d.label || d.id))]
      .filter(Boolean).join('\n').toLowerCase();
  }

  function matches(t, { skip = '' } = {}) {
    if (skip !== 'problems' && filters.problems && !problemOf(t)) return false;
    if (skip !== 'flaky' && filters.flaky && !flakyOf(t)) return false;
    if (skip !== 'priority' && filters.priority.size && !filters.priority.has(t.priority)) return false;
    if (filters.q) {
      const text = haystack(t);
      if (!filters.q.toLowerCase().split(/\s+/).every((w) => text.includes(w))) return false;
    }
    return true;
  }

  const filtering = () => !!(filters.q || filters.problems || filters.flaky || filters.priority.size);

  function renderList(panel, cat) {
    if (mode !== 'list') {
      mode = 'list';
      morph(panel, listFrame());
      wireSearch();
    }
    const all = allTests(cat);
    const cells = all.reduce((n, t) => n + (t.cells || []).length, 0);
    const size = all.length
      ? V.plural(all.length, 'test') + ' in ' + V.plural((cat.areas || []).length, 'area') + ' · ' + V.plural(cells, 'cell') + ' · as in the repository now'
      : '';
    if ($('ttSize').textContent !== size) $('ttSize').textContent = size;
    morph($('ttAlerts'), alerts(cat));

    const priorities = [...new Set(all.map((t) => t.priority).filter(Boolean))].sort();
    const chip = (kind, value, label, n, glyph = '') => html`<label class="ds-chip" data-ds-key="${kind + ':' + value}"><input class="ds-chip__input" type="checkbox" data-tt-chip="${kind}" value="${value}"${(kind === 'priority' ? filters.priority.has(value) : filters[kind]) ? raw(' checked') : ''}><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}${glyph ? raw(icon(glyph, 18, 'ds-chip__icon')) : ''}<span>${label}</span> <span class="${kind === 'problems' && n ? 'ds-badge ds-badge--warning' : 'ds-badge ds-badge--neutral'}">${n}</span></span></label>`;
    morph($('ttChips'), html`<legend class="ds-sr">Show</legend>
      ${chip('problems', '1', 'Problems', all.filter((t) => problemOf(t) && matches(t, { skip: 'problems' })).length)}
      ${chip('flaky', '1', 'Flaky', all.filter((t) => flakyOf(t) && matches(t, { skip: 'flaky' })).length)}
      ${priorities.length > 1 ? html`<span class="ds-chips__divider" aria-hidden="true"></span>${priorities.map((p) => chip('priority', p, p, all.filter((t) => t.priority === p && matches(t, { skip: 'priority' })).length))}` : ''}`);
    $('ttChips').hidden = !all.length;
    for (const input of $('ttChips').querySelectorAll('input')) {
      input.checked = input.dataset.ttChip === 'priority' ? filters.priority.has(input.value) : !!filters[input.dataset.ttChip];
    }

    const shown = all.filter((t) => matches(t));
    const count = !all.length ? '' : shown.length === all.length ? '' : shown.length + ' of ' + V.plural(all.length, 'test');
    if ($('ttCount').textContent !== count) $('ttCount').textContent = count;

    visible = [];
    const phone = PHONE.matches;
    if (!all.length) {
      morph($('ttAreas'), html`<section class="ds-card" data-ds-key="none"><div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('fileText', 24))}</span>
        <p class="ds-empty__title">No test yet</p>
        <p class="ds-empty__text">A test is a Markdown file in an area of .socrates/tests. An agent can write the first one with you.</p>
        <button class="ds-btn ds-btn--tonal" type="button" data-tt-write aria-haspopup="dialog">${raw(icon('sparkles', 16))}Write a test with an agent</button>
      </div></section>`);
    } else if (!shown.length) {
      morph($('ttAreas'), html`<section class="ds-card" data-ds-key="nomatch"><div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('filter', 24))}</span>
        <p class="ds-empty__title">No test matches</p>
        <p class="ds-empty__text">${filters.problems && !filters.q && !filters.flaky && !filters.priority.size ? 'Every test passed in the latest run that ran it.' : 'Clear the search or a chip to see more.'}</p>
        <button class="ds-btn ds-btn--tonal" type="button" data-tt-clear>Show every test</button>
      </div></section>`);
    } else {
      // Every area's table as wide as the widest, so their columns line up.
      const width = Math.max(1, ...(cat.areas || []).map((area) => columnsOf(area).length));
      morph($('ttAreas'), html`${(cat.areas || []).map((area) => areaCard(area, shown.filter((t) => t.area.id === area.id).sort(V.testOrder), phone, width))}`);
    }
    morph($('ttLegend'), phone ? '' : V.legend(shown.some(flakyOf) ? ['flaky'] : []));
    morph($('ttTools'), toolsCard(cat));
    if (pendingArea) {
      const card = $('tt-area-' + pendingArea);
      pendingArea = '';
      if (card) card.scrollIntoView({ block: 'start' });
    }
  }

  /* ----------------------------------------------- the catalog's problems */

  function problemList(list) {
    const byFile = new Map();
    for (const p of list) {
      const file = p.file || '';
      if (!byFile.has(file)) byFile.set(file, []);
      byFile.get(file).push(p);
    }
    return html`<ul class="so-tests-tt__problems">${[...byFile].map(([file, items]) => html`<li>
      ${file ? html`<code class="so-break">${file}</code>` : html`<span>What to run</span>`}
      <ul>${items.map((p) => html`<li class="so-break">${p.line ? html`<span class="ds-meta">${'line ' + p.line + ': '}</span>` : ''}${p.message}</li>`)}</ul>
    </li>`)}</ul>`;
  }

  function alerts(cat) {
    const errors = cat.errors || [];
    const warnings = cat.warnings || [];
    const where = (p) => (p.file ? p.file + (p.line ? ':' + p.line : '') : '');
    const one = (p, kind) => html`<div class="${'ds-alert ds-alert--' + kind}"${kind === 'error' ? raw(' role="alert"') : ''} data-ds-key="${kind + ':' + where(p) + ':' + p.message}">${raw(icon(kind === 'error' ? 'alertCircle' : 'alert', 20))}<span class="ds-alert__text so-break">${where(p) ? html`<code>${where(p)}</code> ` : ''}${p.message}</span></div>`;
    const many = (list, kind) => {
      const key = 'problems:' + kind;
      const title = kind === 'error'
        ? (list.length === 1 ? 'The catalog has an error' : 'The catalog has ' + list.length + ' errors') + ': no run starts until it is fixed.'
        : (list.length === 1 ? 'The catalog has a warning' : 'The catalog has ' + list.length + ' warnings') + ': runs start, but look at them.';
      return html`<div class="${'ds-alert ds-alert--' + kind}"${kind === 'error' ? raw(' role="alert"') : ''} data-ds-key="${key}">${raw(icon(kind === 'error' ? 'alertCircle' : 'alert', 20))}<div class="ds-alert__text">
        <p class="ds-alert__title">${title}</p>
        <details class="ds-errors ds-collapsible" data-key="${key}"${opened.has(key) ? raw(' open') : ''}>
          <summary>${raw(icon(kind === 'error' ? 'alertCircle' : 'alert', 16))}${'Show ' + (list.length === 1 ? 'it' : 'all ' + list.length) + ', by file'}${raw(icon('chevronDown', 16, 'ds-errors__chevron'))}</summary>
          ${problemList(list)}
        </details>
      </div></div>`;
    };
    const block = (list, kind) => (!list.length ? '' : list.length <= ALERTS_OPEN ? list.map((p) => one(p, kind)) : many(list, kind));
    return html`${block(errors, 'error')}${block(warnings, 'warning')}`;
  }

  /* -------------------------------------------------------- an area */

  function columnsOf(area) {
    const dim = (area.dims || []).find((d) => d.name === area.columns);
    if (!dim) return [];
    const used = new Set((area.tests || []).flatMap((t) => (t.cell_health || []).map((c) => ((c.dims || []).find((d) => d.name === area.columns) || {}).id)));
    return [...(dim.values || []), ...(dim.extra || [])].filter((v) => used.has(v.id));
  }

  function dimsLine(area) {
    const dims = area.dims || [];
    if (!dims.length) return '';
    return dims.map((d) => V.titleCase(d.name) + (d.name === area.columns ? ' (the columns)' : '') + ': ' + [...(d.values || []), ...(d.extra || [])].map((x) => x.label || x.id).join(', ')).join(' · ');
  }

  // latestRun is the run most of an area's tests last ran in: the area says
  // it once ("Latest: Run 6, just now"), a test only where its own differs.
  function latestRun(tests) {
    const count = new Map();
    for (const t of tests) {
      const l = t.health && t.health.last_run;
      if (!l) continue;
      const have = count.get(l.run) || { l, n: 0 };
      have.n += 1;
      count.set(l.run, have);
    }
    let best = null;
    for (const c of count.values()) if (!best || c.n > best.n || (c.n === best.n && c.l.number > best.l.number)) best = c;
    return best ? best.l : null;
  }

  function areaCard(area, shown, phone, width) {
    if (!shown.length) return '';
    const tests = area.tests || [];
    const cells = tests.reduce((n, t) => n + (t.cells || []).length, 0);
    const problems = tests.filter(problemOf).length;
    const latest = latestRun(tests);
    const meta = html`${[V.plural(tests.length, 'test'), V.plural(cells, 'cell'), problems ? V.plural(problems, 'problem') : '',
      shown.length < tests.length ? shown.length + ' shown' : ''].filter(Boolean).join(' · ')}${latest ? html` · Latest: <a href="${runHref(latest.run)}">${'Run ' + latest.number}</a>${latest.at ? ', ' + V.ago(latest.at) : ''}` : ''}`;
    const key = 'area:' + area.id;
    const dims = dimsLine(area);
    return html`<section class="ds-card so-tests-area" data-ds-key="${key}" id="${'tt-area-' + area.id}" aria-labelledby="${'tta-' + area.id}">
      <div class="so-tests-area__head">
        <div class="so-tests-area__title">
          <h2 class="ds-card__title" id="${'tta-' + area.id}">${area.title || area.id}</h2>
          <p class="ds-meta">${meta}</p>
        </div>
        <button class="ds-btn ds-btn--sm" type="button" data-tt-run-area="${area.id}" aria-label="${'Run the area “' + (area.title || area.id) + '”'}">${raw(icon('play', 16))}Run this area</button>
      </div>
      ${dims || area.body || (area.tools || []).length ? html`<details class="ds-collapsible ds-disclosure" data-key="${key}"${opened.has(key) ? raw(' open') : ''}>
        <summary>${dims ? 'Dimensions and instructions' : 'Instructions'}${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body so-tests__stack">
          <dl class="ds-facts ds-facts--rows">
            <div><dt>Folder</dt><dd><code>${area.id}</code></dd></div>
            ${(area.dims || []).map((d) => html`<div><dt>${V.titleCase(d.name)}</dt><dd>${[...(d.values || []), ...(d.extra || [])].map((x) => x.label || x.id).join(', ')}${d.name === area.columns ? html` <span class="ds-tag ds-tag--neutral">the columns</span>` : ''}</dd></div>`)}
            ${(area.tools || []).length ? html`<div><dt>Tools</dt><dd>${area.tools.join(', ')}</dd></div>` : ''}
            <div><dt>Evidence</dt><dd>${(area.evidence || 'none') + (area.timeout_min ? ' · at most ' + area.timeout_min + ' min a cell' : '')}</dd></div>
          </dl>
          ${area.body ? html`<div class="ds-prose ds-prose--dense so-break">${markdown(area.body)}</div>` : ''}
        </div>
      </details>` : ''}
      ${phone ? html`<div class="so-tests__stack">${shown.map((t) => testBlock(t, area, latest))}</div>` : areaTable(area, shown, width, latest)}
    </section>`;
  }

  const testHref = (id) => V.paths.page({ repo: state.repo, view: 'tests', test: id });
  const runHref = (id) => V.paths.page({ repo: state.repo, view: 'runs', run: id });

  function tagsOf(t) {
    return html`<span class="so-tests__sub">${t.priority ? html`<span class="ds-tag ds-tag--neutral">${t.priority}</span>` : ''}${(t.tags || []).map((x) => html`<span class="ds-tag">${x}</span>`)}</span>`;
  }

  // standing is how a test stands in words: "failing since Run N", "an
  // issue since Run N", flaky; and its last run where the area's latest is
  // not its own.
  function standing(t, latest) {
    const h = t.health || {};
    const parts = [];
    if (problemOf(t) && h.since) parts.push(html`<span class="so-tests-nowrap">${raw(icon(V.VERDICT[h.latest] ? V.VERDICT[h.latest].glyph : 'alert', 16))}${V.sinceWords(h.latest, h.since.number)}</span>`);
    if (h.flaky) parts.push(html`<span class="so-tests-nowrap" data-tip="${'Its verdict changed ' + V.plural(h.flips || 0, 'time') + ' between passing and not over the last runs'}" tabindex="0">${raw(icon('activity', 16))}${'flaky' + (h.flips ? ' · ' + V.plural(h.flips, 'flip') : '')}</span>`);
    const l = h.last_run;
    if (!l) parts.push(html`<span>not run yet</span>`);
    else if (!latest || l.run !== latest.run) parts.push(html`<span>last in <a href="${runHref(l.run)}">${'Run ' + l.number}</a></span>`);
    return parts;
  }

  function runButton(t, sm) {
    return html`<button class="${'ds-btn ds-btn--quiet ds-btn--icon' + (sm ? ' ds-btn--sm' : '')}" type="button" data-tt-run-test="${t.id}" aria-label="${'Run “' + t.title + '”'}" data-tip="Run this test">${raw(icon('play', sm ? 18 : 20))}</button>`;
  }

  // cellWords is everything a cell's button says, for the reader.
  function cellWords(t, c) {
    return [t.title, V.dimsWords(c.dims) || c.cell, c.latest ? V.verdictWord(c.latest) + ' in Run ' + c.number : 'not run yet',
      c.flaky ? 'flaky' : '', c.since && c.latest && !V.passed(c.latest) ? V.sinceWords(c.latest, c.since.number) : ''].filter(Boolean).join(', ');
  }

  function cellButton(t, c, area) {
    const others = (c.dims || []).filter((d) => d.name !== area.columns);
    const body = html`<span class="so-tests__verdict">${c.latest ? V.verdictChip(c.latest) : html`<span class="ds-status">${raw(icon('minimize', 16))}<span>Not run</span></span>`}${
      c.flaky ? html`<span class="so-tests__mark" role="img" aria-label="Flaky" data-tip="${'Flaky: ' + V.plural(c.flips || 0, 'flip') + ' in ' + V.plural(c.runs || 0, 'run')}">${raw(icon('activity', 16))}</span>` : ''}</span>${
      others.length ? html`<span class="so-tests__dims">${others.map((d) => d.label || d.id).join(', ')}</span>` : ''}`;
    if (!c.attempt || !c.run) return html`<span class="so-tests__cell" data-ds-key="${c.cell_id}">${body}</span>`;
    visible.push({ attempt: c.attempt, run: c.run });
    return html`<button class="ds-pressable so-tests__cell" type="button" data-cell="${c.attempt}" data-cell-run="${c.run}" data-ds-key="${c.cell_id}" aria-label="${cellWords(t, c)}" data-tip="${'Run ' + c.number + (c.since && !V.passed(c.latest) ? ' · ' + V.sinceWords(c.latest, c.since.number) : '') + ' · ' + V.plural(c.passes || 0, 'pass') + ' in ' + V.plural(c.runs || 0, 'run')}">${body}</button>`;
  }

  // An area's table: as a run's (testsruns.js), a column per value of the
  // area's `columns` dimension, padded to `width` so the areas line up.
  function areaTable(area, shown, width, latest) {
    const columns = columnsOf(area);
    const n = Math.max(1, columns.length);
    const pad = Math.max(0, width - n);
    const dimOf = (c) => ((c.dims || []).find((d) => d.name === area.columns) || {}).id;
    return html`<div class="ds-table-wrap"><table class="ds-table ds-table--dense so-tests__table so-tests-tt__table" style="${'--so-cols: ' + width}">
      <caption class="ds-sr">${(area.title || area.id) + ': a row per test' + (area.columns ? ', a column per ' + area.columns : '') + '; each cell its latest verdict'}</caption>
      <colgroup><col class="so-tests__col-test">${Array.from({ length: width }, () => html`<col>`)}<col class="so-tests__col-act"></colgroup>
      <thead><tr>
        <th scope="col">Test</th>
        ${columns.length ? columns.map((c) => html`<th scope="col">${c.label || c.id}</th>`) : html`<th scope="col">Latest</th>`}${pad ? html`<td colspan="${String(pad)}"></td>` : ''}
        <th scope="col"><span class="ds-sr">Actions</span></th>
      </tr></thead>
      <tbody>${shown.map((t) => html`<tr data-ds-key="${t.id}">
        <th scope="row" class="so-tests__test">
          <a class="ds-link-btn" href="${testHref(t.id)}">${t.title}</a>
          <small class="so-tests__sub ds-meta"><span class="ds-parts"><span class="ds-parts__list"><span>${t.id}</span>${standing(t, latest).map((p) => html`<span>${p}</span>`)}</span></span></small>
          ${tagsOf(t)}
        </th>
        ${columns.length ? columns.map((col) => html`<td><span class="so-tests__cells">${(t.cell_health || []).filter((c) => dimOf(c) === col.id).map((c) => cellButton(t, c, area))}</span></td>`)
          : html`<td><span class="so-tests__cells">${(t.cell_health || []).map((c) => cellButton(t, c, area))}</span></td>`}${pad ? html`<td colspan="${String(pad)}"></td>` : ''}
        <td class="ds-table__actions">${runButton(t)}</td>
      </tr>`)}</tbody>
    </table></div>`;
  }

  function testBlock(t, area, latest) {
    return html`<div class="so-tests-block" data-ds-key="${t.id}">
      <div class="so-tests-block__head">
        <div class="so-tests-block__title">
          <a class="ds-link-btn" href="${testHref(t.id)}">${t.title}</a>
          <small class="so-tests__sub ds-meta"><span class="ds-parts"><span class="ds-parts__list"><span>${t.id}</span>${standing(t, latest).map((p) => html`<span>${p}</span>`)}</span></span></small>
          ${tagsOf(t)}
        </div>
        ${runButton(t, true)}
      </div>
      <ul class="ds-list ds-list--interactive" aria-label="${'Cells of ' + t.title}">${(t.cell_health || []).map((c) => {
        const can = c.attempt && c.run;
        if (can) visible.push({ attempt: c.attempt, run: c.run });
        const label = V.dimsWords(c.dims) || c.cell || 'Result';
        // Its run only where it is not the area's latest (the area's head says that one).
        const meta2 = [!c.latest ? 'not run yet' : latest && c.run === latest.run ? '' : 'Run ' + c.number, c.since && c.latest && !V.passed(c.latest) ? V.sinceWords(c.latest, c.since.number) : '', c.flaky ? 'flaky' : ''].filter(Boolean).join(' · ');
        return html`<li class="ds-list__item" data-ds-key="${c.cell_id}">
          <span class="ds-list__text">
            <span class="ds-list__title">${can ? html`<button class="ds-list__action" type="button" data-cell="${c.attempt}" data-cell-run="${c.run}" aria-label="${cellWords(t, c)}">${label}</button>` : label}</span>
            <span class="ds-list__meta">${meta2}</span>
          </span>
          ${c.latest ? V.verdictChip(c.latest) : html`<span class="ds-status">${raw(icon('minimize', 16))}<span>Not run</span></span>`}
        </li>`;
      })}</ul>
    </div>`;
  }

  /* ------------------------------------------------------- the tools */

  function toolsCard(cat) {
    const tools = cat.tools || [];
    if (!tools.length) return '';
    return html`<section class="ds-card" aria-labelledby="ttToolsTitle" data-ds-key="tools">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="ttToolsTitle">Tools</h2>
        <span class="ds-meta">${V.plural(tools.length, 'tool')}</span>
      </div>
      <p class="ds-muted">What the cells work with: the secrets each tool is given, and the pool of accounts a cell leases one of.</p>
      ${tools.map((tool) => toolBlock(tool))}
    </section>`;
  }

  function toolBlock(tool) {
    const check = checks.get(tool.name);
    const r = check && check.result;
    const optional = new Set(tool.optional || []);
    const key = 'tool:' + tool.name;
    return html`<div class="ds-subsection so-tests__stack" data-ds-key="${key}">
      <div class="ds-card__row">
        <h3 class="ds-card__subtitle">${tool.title || tool.name}</h3>
        ${tool.secrets_command ? html`<button class="ds-btn ds-btn--sm" type="button" data-tt-check="${tool.name}">${raw(icon('key', 16))}Check secrets</button>` : ''}
      </div>
      <dl class="ds-facts ds-facts--rows">
        <div><dt>Name</dt><dd><code>${tool.name}</code></dd></div>
        ${tool.secrets && tool.secrets.length ? html`<div><dt>Secrets</dt><dd><span class="so-tests__sub">${tool.secrets.map((n) => html`<span class="ds-tag ds-tag--neutral">${n + (optional.has(n) ? ' (optional)' : '')}</span>`)}</span></dd></div>` : ''}
        ${tool.pool ? html`<div><dt>Pool</dt><dd>${'The accounts in ' + tool.pool + ', one a cell'}</dd></div>` : ''}
        ${tool.setup ? html`<div><dt>Setup</dt><dd><code class="so-break">${tool.setup}</code></dd></div>` : ''}
        ${tool.end ? html`<div><dt>At a cell’s end</dt><dd><code class="so-break">${tool.end}</code></dd></div>` : ''}
      </dl>
      ${r ? checkResult(r) : ''}
      ${tool.secrets_command ? html`<details class="ds-collapsible ds-disclosure" data-key="${key + ':secrets'}"${opened.has(key + ':secrets') ? raw(' open') : ''}>
        <summary>How secrets are read${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body so-tests__stack">
          <p class="ds-muted">Socrates runs this command of the catalog when a run starts and gives each cell the names above; the values it prints are never shown or kept.</p>
          <pre class="ds-code-block" tabindex="0" aria-label="The command that reads the secrets"><code>${tool.secrets_command}</code></pre>
        </div>
      </details>` : ''}
      ${tool.body ? html`<details class="ds-collapsible ds-disclosure" data-key="${key}"${opened.has(key) ? raw(' open') : ''}>
        <summary>How the agent uses it${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body"><div class="ds-prose ds-prose--dense so-break">${markdown(tool.body)}</div></div>
      </details>` : ''}
    </div>`;
  }

  function checkResult(r) {
    const missing = r.missing || [];
    return html`<div class="so-tests__stack" role="status" data-ds-key="check">
      ${r.error ? html`<div class="ds-alert ds-alert--error">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${r.error}</span></div>` : ''}
      ${(r.found || []).length || missing.length ? html`<p class="so-tests__sub">${(r.found || []).map((n) => html`<span class="ds-status ds-status--ok">${raw(icon('check', 16))}<span>${n}</span></span>`)}${missing.map((n) => html`<span class="ds-status ds-status--error">${raw(icon('alertCircle', 16))}<span>${n + ' missing'}</span></span>`)}</p>` : ''}
      ${(r.members || []).length ? html`<p class="ds-meta">${(r.members.length === 1 ? '1 account in the pool: ' : r.members.length + ' accounts in the pool: ') + r.members.join(', ')}</p>` : ''}
      ${!r.error && !missing.length ? html`<p class="ds-meta">${raw(icon('lock', 16))} The values themselves are never shown.</p>` : ''}
    </div>`;
  }

  async function checkTool(tool, button) {
    try {
      const result = await M.busy(button, api(V.paths.check(state.repo, tool), { method: 'POST', body: {} }), { done: false });
      checks.set(tool, { result });
      const r = result || {};
      DS.overlay.announce((r.missing || []).length ? V.plural(r.missing.length, 'secret') + ' missing' : 'Every secret is there');
    } catch (err) {
      checks.set(tool, { result: { error: errorMessage(err), found: [], missing: [] } });
    }
    ctx.redraw('tests');
  }

  /* --------------------------------------------------------- a test */

  function crumbs(test, area) {
    return html`<nav class="ds-breadcrumbs" aria-label="Breadcrumb"><ol>
      <li><a href="${V.paths.page({ repo: state.repo, view: 'tests' })}">Tests</a></li>
      ${area ? html`<li><a href="${V.paths.page({ repo: state.repo, view: 'tests' })}" data-tt-area="${area.id}">${area.title || area.id}</a></li>` : ''}
      <li><a href="${testHref(test.id)}" aria-current="page">${test.title || test.id}</a></li>
    </ol></nav>`;
  }

  function renderTest(panel, id) {
    if (mode !== 'test:' + id) {
      mode = 'test:' + id;
      morph(panel, html`<div class="so-tests__stack" id="ttTest"></div>`);
      $('main').scrollTo({ top: 0 });
    }
    const e = tests.get(id);
    if (!e || (!e.data && !e.error && !e.loading)) loadTest(id);
    const fromCatalog = allTests(state.catalog).find((t) => t.id === id);
    const area = areaOf(id);
    const box = $('ttTest');
    if (!e || !e.data) {
      const gone = e && e.error === 'gone';
      morph(box, html`${crumbs(fromCatalog || { id, title: id }, area)}${e && e.error
        ? gone ? html`<section class="ds-card"><div class="ds-empty">
            <span class="ds-empty__disc" aria-hidden="true">${raw(icon('fileText', 24))}</span>
            <p class="ds-empty__title">This test is not in the catalog</p>
            <p class="ds-empty__text">${'There is no test ' + id + ' in the repository now: it was renamed or removed.'}</p>
            <a class="ds-btn ds-btn--tonal" href="${V.paths.page({ repo: state.repo, view: 'tests' })}">Show every test</a>
          </div></section>`
          : html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${e.error}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-tt-retest>Try again</button></div>`
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the test</div>`}`);
      return;
    }
    const d = e.data;
    const t = { ...(fromCatalog || {}), ...(d.test || {}) };
    const bugs = (d.bugs || []).filter((b) => b.status !== 'fixed' && b.status !== 'not_a_bug');
    const fixed = (d.bugs || []).length - bugs.length;
    morph(box, html`${testHead(t, area, (d.cells || []).length || (t.cells || []).length)}
      ${historyCard(t, d)}
      <div class="${bugs.length || fixed ? 'ds-grid-2' : 'so-tests__stack'}" data-ds-key="lower">
        ${stepsCard(t)}
        ${bugs.length || fixed ? bugsCard(bugs, fixed) : ''}
      </div>`);
  }

  function testHead(t, area, cellCount) {
    const h = t.health || {};
    const file = t.file ? '.socrates/tests/' + t.file : '';
    return html`<header class="ds-page-head so-tests-record" data-ds-key="head">
      ${crumbs(t, area)}
      <div class="ds-page-head__row">
        <div class="ds-page-head__text">
          <h2 class="ds-page-head__title" tabindex="-1">${t.title || t.id}</h2>
          <p class="ds-page-head__lead">${file ? html`<code class="so-break">${file}</code> · ` : ''}${V.plural(cellCount, 'cell')}</p>
          <div class="ds-page-head__status">
            ${h.latest ? V.chip({ ...V.VERDICT[h.latest], word: V.verdictWord(h.latest) + (h.last_run ? ' in Run ' + h.last_run.number : '') + (h.since && !V.passed(h.latest) ? ', since Run ' + h.since.number : '') }) : V.chip({ word: 'Not run yet', glyph: 'minimize' })}
            ${h.flaky ? V.chip({ word: 'Flaky' + (h.flips ? ' · ' + V.plural(h.flips, 'flip') : ''), glyph: 'activity', tone: 'warning' }) : ''}
            ${t.priority ? html`<span class="ds-tag ds-tag--neutral">${t.priority}</span>` : ''}
            ${(t.tags || []).map((x) => html`<span class="ds-tag">${x}</span>`)}
          </div>
        </div>
        <div class="ds-page-head__actions">
          <button class="ds-btn" type="button" data-tt-change="${t.id}" aria-haspopup="dialog">${raw(icon('sparkles', 16))}Change it with an agent</button>
          <button class="ds-btn ds-btn--tonal" type="button" data-tt-run-test="${t.id}">${raw(icon('play', 16))}Run this test</button>
        </div>
      </div>
    </header>`;
  }

  function stepsCard(t) {
    const steps = t.steps || [];
    return html`<section class="ds-card" aria-labelledby="ttStepsTitle" data-ds-key="steps">
      <h3 class="ds-card__title" id="ttStepsTitle">What it does</h3>
      ${t.context ? html`<div class="ds-prose ds-prose--dense so-break">${markdown(t.context)}</div>` : ''}
      ${steps.length ? html`<ol class="ds-list so-tests-tt__steps" aria-label="Steps">${steps.map((s) => html`<li class="ds-list__item" data-ds-key="${'s' + s.n}">
        <span class="ds-list__text">
          <span class="ds-list__title so-break">${s.n + '. ' + s.do}</span>
          <span class="ds-list__meta so-break">${'Expect: ' + s.expect}</span>
        </span>
      </li>`)}</ol>` : html`<p class="ds-muted">This test has no steps.</p>`}
    </section>`;
  }

  function bugsCard(bugs, fixed) {
    const sorted = bugs.slice().sort((a, b) => V.sevRank(a.severity) - V.sevRank(b.severity));
    return html`<section class="ds-card" aria-labelledby="ttBugsTitle" data-ds-key="bugs">
      <div class="ds-card__row">
        <h3 class="ds-card__title" id="ttBugsTitle">Open bugs</h3>
        ${fixed ? html`<span class="ds-meta">${V.plural(fixed, 'bug') + ' fixed or not a bug'}</span>` : ''}
      </div>
      ${sorted.length ? html`<ul class="ds-list ds-list--interactive" aria-label="Open bugs of this test">${sorted.map((b) => html`<li class="ds-list__item" data-ds-key="${b.id}">
        <span class="ds-list__text">
          <span class="ds-list__title"><a class="ds-list__action" href="${V.paths.page({ repo: state.repo, view: 'bugs', bug: b.id })}">${b.id + ' · ' + b.title}</a></span>
          <span class="ds-list__meta">${[V.bugStateWords(b).toLowerCase(), 'seen in ' + V.plural((b.runs || []).length, 'run'), V.plural((b.cells || []).length, 'cell')].join(' · ')}</span>
        </span>
        ${V.severityChip(b.severity)}
      </li>`)}</ul>` : html`<p class="ds-muted">No bug of this test is open.</p>`}
    </section>`;
  }

  /* ------------------------------------------------- the history grid */

  // mark is a cell's verdict in a run as a glyph that opens the attempt
  // (shape and colour say which, the label and the tip in words).
  function mark(c, run, g) {
    if (!g) return html`<span class="so-tests-hist__none" aria-label="${'Not in Run ' + run.number}" role="img">–</span>`;
    const verdict = g.verdict || '';
    const running = !verdict && V.OPEN_ATTEMPT.has(g.state);
    const word = verdict ? V.verdictWord(verdict) : V.attemptWord(g.state);
    const label = 'Run ' + run.number + ': ' + word + ' · ' + (V.dimsWords(c.dims) || c.cell);
    const glyph = verdict && V.VERDICT[verdict] ? V.VERDICT[verdict].glyph : g.state === 'aborted' || g.state === 'failed' ? 'alertCircle' : g.state === 'stopped' ? 'stopCircle' : 'clock';
    return html`<button class="${'ds-pressable so-tests-hist__mark so-mark so-mark--' + (V.VERDICT[verdict] ? verdict : 'open')}" type="button" data-hist="${g.attempt}" data-hist-run="${run.id}" data-hist-row="${c.cell_id}" aria-label="${label + '. Open it'}" data-tip="${label}">${running ? html`<span class="ds-spinner ds-spinner--sm" aria-hidden="true"></span>` : raw(icon(glyph, 18))}</button>`;
  }

  function cellRecord(c) {
    return [c.runs ? c.passes + ' of ' + V.plural(c.runs, 'run') + ' passed' : '', c.flaky ? 'flaky' : '', c.flips ? V.plural(c.flips, 'flip') : '',
      c.since && c.latest && !V.passed(c.latest) ? V.sinceWords(c.latest, c.since.number) : ''].filter(Boolean).join(' · ');
  }

  function runCellButton(c) {
    return html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-tt-run-cell="${c.cell_id}" aria-label="${'Run the cell ' + (V.dimsWords(c.dims) || c.cell)}" data-tip="Run this cell">${raw(icon('play', 18))}</button>`;
  }

  // On a phone the grid turns into a row per cell with its last runs as a
  // strip of glyphs, each over its run's number: nothing scrolls sideways.
  const PHONE_RUNS = 10;
  function historyList(cells, runs, grid) {
    const shown = runs.slice(0, PHONE_RUNS);
    return html`<ul class="ds-list so-tests-hist__list" aria-label="The verdict of each cell in its last runs, the newest first">${cells.map((c) => {
      const row = grid[c.cell_id] || {};
      return html`<li class="ds-list__item" data-ds-key="${c.cell_id}">
        <span class="ds-list__text">
          <span class="ds-list__title">${V.dimsWords(c.dims) || c.cell || 'Result'}</span>
          <span class="ds-list__meta">${cellRecord(c)}</span>
          <span class="so-tests-hist__strip">${shown.map((r) => html`<span class="so-tests-hist__col"><span class="so-tests-hist__num" aria-hidden="true">${String(r.number)}</span>${mark(c, r, row[r.id])}</span>`)}</span>
        </span>
        <span class="ds-list__actions">${runCellButton(c)}</span>
      </li>`;
    })}</ul>`;
  }

  function historyCard(t, d) {
    const runs = d.runs || [];
    const cells = d.cells && d.cells.length ? d.cells : t.cell_health || [];
    const grid = d.grid || {};
    if (!runs.length) {
      return html`<section class="ds-card so-tests-hist" aria-labelledby="ttHistTitle" data-ds-key="hist">
        <h3 class="ds-card__title" id="ttHistTitle">History</h3>
        <div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('history', 24))}</span><p class="ds-empty__title">Not run yet</p><p class="ds-empty__text">Each run of this test adds a column here: a verdict per cell.</p></div>
      </section>`;
    }
    const used = V.VERDICTS.filter((v) => cells.some((c) => runs.some((r) => ((grid[c.cell_id] || {})[r.id] || {}).verdict === v)));
    return html`<section class="ds-card so-tests-hist" aria-labelledby="ttHistTitle" data-ds-key="hist">
      <div class="ds-card__row">
        <h3 class="ds-card__title" id="ttHistTitle">History</h3>
        <span class="ds-meta">${'The last ' + V.plural(Math.min(runs.length, PHONE.matches ? PHONE_RUNS : runs.length), 'run') + ', the newest first'}</span>
      </div>
      ${PHONE.matches ? historyList(cells, runs, grid) : html`<div class="ds-table-wrap"><table class="ds-table ds-table--dense ds-table--pin-first so-tests-hist__table">
        <caption class="ds-sr">The verdict of each cell of this test in its last runs, the newest first; a verdict opens that attempt</caption>
        <thead><tr>
          <th scope="col">Cell</th>
          ${runs.map((r) => html`<th scope="col" class="so-tests-hist__run"><a href="${runHref(r.id)}" data-tip="${[r.name, V.when(r.created_at)].filter(Boolean).join(' · ')}">${'Run ' + r.number}</a></th>`)}
        </tr></thead>
        <tbody>${cells.map((c) => {
          const row = grid[c.cell_id] || {};
          return html`<tr data-ds-key="${c.cell_id}">
            <th scope="row" class="so-tests-hist__cell">
              <span class="so-tests-hist__name"><span class="so-break">${V.dimsWords(c.dims) || c.cell || 'Result'}</span>${runCellButton(c)}</span>
              <small class="so-tests__sub ds-meta">${cellRecord(c)}</small>
            </th>
            ${runs.map((r) => html`<td class="so-tests-hist__slot">${mark(c, r, row[r.id])}</td>`)}
          </tr>`;
        })}</tbody>
      </table></div>`}
      ${used.length ? html`<p class="ds-meta so-tests-hist__legend" aria-hidden="true"><span class="ds-parts"><span class="ds-parts__list">${used.map((v) => html`<span class="so-tests-nowrap"><span class="${'so-mark so-mark--' + v}">${raw(icon(V.VERDICT[v].glyph, 16))}</span> ${V.verdictWord(v)}</span>`)}<span class="so-tests-nowrap">– not in that run</span></span></span></p>` : ''}
    </section>`;
  }

  /* ------------------------------------------------------------ wiring */

  function wire(panel) {
    panel.dataset.wired = '1';
    panel.addEventListener('click', (event) => {
      const t = event.target;
      const cell = t.closest('[data-cell]');
      if (cell) {
        ctx.openCell(cell.dataset.cell, cell.dataset.cellRun, visible.slice());
        return;
      }
      const hist = t.closest('[data-hist]');
      if (hist) {
        const rowCells = [...panel.querySelectorAll('[data-hist-row="' + CSS.escape(hist.dataset.histRow) + '"]')]
          .map((b) => ({ attempt: b.dataset.hist, run: b.dataset.histRun }));
        ctx.openCell(hist.dataset.hist, hist.dataset.histRun, rowCells);
        return;
      }
      const runTest = t.closest('[data-tt-run-test]');
      if (runTest) { ctx.startRun({ tests: [runTest.dataset.ttRunTest] }, runTest); return; }
      const runCell = t.closest('[data-tt-run-cell]');
      if (runCell) { ctx.startRun({ cells: [runCell.dataset.ttRunCell] }, runCell); return; }
      const runArea = t.closest('[data-tt-run-area]');
      if (runArea) {
        ctx.startRun({ areas: [runArea.dataset.ttRunArea] }, runArea);
        return;
      }
      const write = t.closest('[data-tt-write]');
      if (write) { ctx.openChat({ kind: 'write-test', subject: { repo: state.repo } }, write); return; }
      const change = t.closest('[data-tt-change]');
      if (change) { ctx.openChat({ kind: 'change-test', subject: { repo: state.repo, test: change.dataset.ttChange } }, change); return; }
      const reload = t.closest('[data-tt-reload]');
      if (reload) { readAgain(reload); return; }
      const check = t.closest('[data-tt-check]');
      if (check) { checkTool(check.dataset.ttCheck, check); return; }
      if (t.closest('[data-tt-clear]')) {
        filters.q = '';
        filters.problems = false;
        filters.flaky = false;
        filters.priority.clear();
        if ($('ttSearch')) { $('ttSearch').value = ''; $('ttSearch').focus(); }
        ctx.redraw('tests');
        return;
      }
      if (t.closest('[data-tt-retest]')) { tests.delete(state.route.test); ctx.redraw('tests'); return; }
      const area = t.closest('[data-tt-area]');
      if (area) pendingArea = area.dataset.ttArea;
    });
    panel.addEventListener('change', (event) => {
      const chip = event.target.closest('[data-tt-chip]');
      if (!chip) return;
      const kind = chip.dataset.ttChip;
      if (kind === 'priority') {
        if (chip.checked) filters.priority.add(chip.value);
        else filters.priority.delete(chip.value);
      } else filters[kind] = chip.checked;
      ctx.redraw('tests');
    });
    panel.addEventListener('toggle', (event) => {
      const d = event.target;
      if (!d.dataset || !d.dataset.key) return;
      if (d.open) opened.add(d.dataset.key);
      else opened.delete(d.dataset.key);
    }, true);
  }

  async function readAgain(button) {
    try {
      await M.busy(button, ctx.ensureCatalog(true), { done: false });
      healthAt = Date.now();
      if (state.catalogError) toast(state.catalogError, 'error');
      else DS.overlay.announce('The catalog is read again');
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
    tests.clear();
    ctx.redraw('tests');
  }

  PHONE.addEventListener('change', () => ctx.redraw('tests'));

  return {
    render,
    async refresh() {
      // A frame said the repository changed: the health of the tests (the
      // catalog's) and the shown test's history are read again.
      const id = state.route.test;
      for (const key of [...tests.keys()]) if (key !== id) tests.delete(key);
      if (healthStale()) {
        healthAt = Date.now();
        await ctx.ensureCatalog(true);
      }
      if (id) await loadTest(id);
    },
    close() {
      mode = '';
    },
    repoChanged() {
      tests.clear();
      checks.clear();
      opened.clear();
      mode = '';
      filters.q = '';
      filters.problems = false;
      filters.flaky = false;
      filters.priority.clear();
      healthKey = '';
      healthAt = 0;
    },
  };
}
