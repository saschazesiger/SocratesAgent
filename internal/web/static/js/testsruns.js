// The Runs view of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", Runs): every run of the repository, newest first, and a run's own
// view with its head (state, commit, when, how long, what it cost, the
// roles), its actions, and three parts: Results (this run's cells by area,
// searchable and filtered, each opening the cell viewer), Bugs (this run's,
// and the conclusions) and Details (settings, reviewers and analyst with
// their transcripts, pools, why it failed).
//
// The run shown while it is active is the frame's live run (state.live),
// read again every few seconds; any other run is read here once and again
// when a frame says it changed.

import { api, errorMessage, toast } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import { markdown } from './md.js';
import * as harnesses from './harnesses.js';
import * as V from './testsview.js';

const $ = (id) => document.getElementById(id);
const PHONE = window.matchMedia('(max-width: 600px)');
const TYPE_PAUSE = 200;

const CHIPS = [
  { id: 'all', label: 'All' },
  { id: 'problems', label: 'Problems' },
  { id: 'changed', label: 'Changed' },
  { id: 'reviewed', label: 'Reviewed' },
  { id: 'overridden', label: 'Overridden' },
];

const PARTS = [
  { id: 'results', label: 'Results' },
  { id: 'bugs', label: 'Bugs' },
  { id: 'details', label: 'Details' },
];

export function mount(ctx) {
  const { state } = ctx;
  const cache = new Map(); // run id -> { detail, cells, bugs, error, loading, bugsLoading }
  let part = 'results';
  let shown = ''; // the run whose view is drawn
  let partTabs = null; // the indicator of the parts' tabs
  const filters = { q: '', chip: 'all', verdict: new Set(), area: new Set(), priority: new Set(), tag: new Set(), dims: {} };
  let visible = []; // the cells shown, in order: what the cell viewer's previous and next walk

  harnesses.load().catch(() => {}).then(() => ctx.redraw('runs'));

  /* -------------------------------------------------------------- data */

  function entry(id) {
    if (!cache.has(id)) cache.set(id, { detail: null, cells: null, bugs: null, error: '' });
    const e = cache.get(id);
    if (state.live && state.live.id === id) {
      e.detail = state.live.detail;
      e.cells = state.live.cells;
    }
    return e;
  }

  // prevRunOf is the finished run before a run, the one its cells compare with.
  function prevRunOf(id) {
    const i = state.runs.findIndex((r) => r.id === id);
    if (i < 0) return null;
    return state.runs.slice(i + 1).find((r) => !V.ACTIVE_RUN.has(r.state) && r.counts && r.counts.cells) || null;
  }

  async function load(id) {
    const e = entry(id);
    if (e.loading) return;
    e.loading = true;
    try {
      const live = state.live && state.live.id === id;
      const [detail, cells] = live ? [e.detail, e.cells]
        : await Promise.all([api(V.paths.run(id)), api(V.paths.cells(id)).then((d) => d.cells || [])]);
      e.detail = detail;
      e.cells = cells;
      e.error = '';
    } catch (err) {
      e.error = errorMessage(err);
    } finally {
      e.loading = false;
    }
    ctx.redraw('runs');
  }

  async function loadBugs(id) {
    const e = entry(id);
    if (e.bugsLoading) return;
    e.bugsLoading = true;
    try {
      const data = await api(V.paths.bugs(id));
      e.bugs = { bugs: data.bugs || [], conclusions: data.conclusions || '' };
      e.bugsStale = false;
    } catch (err) {
      e.bugs = { bugs: [], conclusions: '', error: errorMessage(err) };
      e.bugsStale = false;
    } finally {
      e.bugsLoading = false;
    }
    ctx.redraw('runs');
  }

  // rowsOf is a run's cells, each with its change against the run before (CellRow.prev, .change).
  function rowsOf(id) {
    return entry(id).cells || [];
  }

  /* ------------------------------------------------------------- render */

  function render(panel) {
    if (!panel.dataset.wired) wire(panel);
    const id = state.route.run;
    if (!id) {
      shown = '';
      partTabs = null;
      renderList(panel);
      return;
    }
    if (id !== shown) {
      shown = id;
      if (state.runPreset) {
        filters.verdict = new Set(state.runPreset.verdicts || []);
        filters.chip = state.runPreset.chip || 'all';
        part = 'results';
        state.runPreset = null;
      } else {
        part = 'results';
      }
      morph(panel, html`<div class="so-tests__stack" data-runview>
        <div id="rvHead" class="so-tests__stack"></div>
        <div class="ds-tabs" role="tablist" aria-label="Parts of the run" id="rvTabs">${PARTS.map((p) => html`<button class="ds-tabs__tab" role="tab" type="button" id="${'rvt-' + p.id}" aria-selected="${p.id === part ? 'true' : 'false'}" aria-controls="rvPart"${p.id === part ? '' : raw(' tabindex="-1"')} data-part="${p.id}" data-label="${p.label}">${p.label}</button>`)}</div>
        <div class="ds-tabs__panel so-tests__stack" role="tabpanel" id="rvPart" aria-labelledby="${'rvt-' + part}" tabindex="0" data-part-shown=""></div>
      </div>`);
      partTabs = DS.indicator.createIndicator($('rvTabs'), { active: '[aria-selected="true"]', className: 'ds-tabs__indicator', reveal: true });
      DS.dom.roving($('rvTabs'));
      $('main').scrollTo({ top: 0 });
    }
    const e = entry(id);
    const run = ctx.runOf(id);
    if (!e.detail && !e.error) load(id);
    if (!e.detail) {
      morph($('rvHead'), html`${crumbs(run || { id })}${e.error
        ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${e.error === 'not found' ? 'This run is gone: it was deleted, or it is older than the runs kept.' : e.error}</span></div>`
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the run</div>`}`);
      $('rvTabs').hidden = true;
      $('rvPart').hidden = true;
      return;
    }
    $('rvTabs').hidden = false;
    $('rvPart').hidden = false;
    const d = { ...e.detail, n: run ? run.n : e.detail.number };
    const rows = rowsOf(id);
    morph($('rvHead'), head(d, rows));
    renderParts(d, rows);
  }

  /* --------------------------------------------------------------- list */

  function renderList(panel) {
    if (!state.runs.length) {
      morph(panel, html`<section class="ds-card" aria-label="No run yet"><div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('history', 24))}</span>
        <p class="ds-empty__title">No run yet</p>
        <p class="ds-empty__text">Every run of this repository is listed here, the newest first.</p>
        <button class="ds-btn ds-btn--tonal" type="button" data-rv-dialog>${raw(icon('play', 16))}Run tests</button>
      </div></section>`);
      return;
    }
    const phone = PHONE.matches;
    const changes = state.runs.some((r) => r.changes);
    const costs = state.runs.some((r) => typeof r.cost === 'number');
    const keep = state.settings && state.settings.settings ? state.settings.settings.keep_runs : 0;
    morph(panel, html`<section class="ds-card" aria-labelledby="runsTitle">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="runsTitle">Runs</h2>
        <span class="ds-meta">${V.plural(state.runs.length, 'run') + (keep ? ' · the last ' + keep + ' are kept, and pinned ones' : '')}</span>
      </div>
      ${phone ? html`<ul class="ds-list ds-list--interactive" aria-label="Runs">${state.runs.map((r) => runItem(r))}</ul>`
        : html`<div class="ds-table-wrap"><table class="ds-table ds-table--interactive so-tests-runs">
          <caption class="ds-sr">Every run of this repository, the newest first</caption>
          <thead><tr>
            <th scope="col">Run</th>
            <th scope="col">Result</th>
            ${changes ? html`<th scope="col" class="ds-col--secondary">Against the run before</th>` : ''}
            <th scope="col" class="ds-num ds-col--secondary">Took</th>
            ${costs ? html`<th scope="col" class="ds-num ds-col--secondary">Cost</th>` : ''}
            <th scope="col">State</th>
          </tr></thead>
          <tbody>${state.runs.map((r) => runRow(r, changes, costs))}</tbody>
        </table></div>`}
    </section>`);
  }

  const runHref = (id) => V.paths.page({ repo: state.repo, view: 'runs', run: id });

  function runMeta(r) {
    return [V.when(r.created_at), V.selectionWords(r.selection, state.runs), V.startedBy(r) ? 'started by ' + V.startedBy(r) : ''].filter(Boolean).join(' · ');
  }

  function changeWords(c) {
    if (!c) return '';
    return html`<span class="ds-parts"><span class="ds-parts__list">${c.regressed ? html`<span><span class="so-tests-change so-tests-change--regressed">${raw(icon('arrowDown', 16))}${c.regressed + ' broke'}</span></span>` : ''}${
      c.fixed ? html`<span><span class="so-tests-change so-tests-change--fixed">${raw(icon('arrowUp', 16))}${c.fixed + ' fixed'}</span></span>` : ''}${
      !c.regressed && !c.fixed ? html`<span class="ds-muted">No change</span>` : ''}</span></span>`;
  }

  function runRow(r, changes, costs) {
    return html`<tr data-ds-key="${r.id}">
      <td class="ds-col--text"><a class="ds-link-btn ds-table__row-link" href="${runHref(r.id)}">${V.runName(r)}</a>${r.pinned ? html` <span class="so-tests-pin" role="img" aria-label="Pinned" data-tip="Pinned: kept whatever the retention says">${raw(icon('lock', 16))}</span>` : ''}
        <small class="so-tests__sub ds-meta">${runMeta(r)}</small></td>
      <td><span class="so-tests-result">${V.resultBar(r.counts)}${V.resultParts(r.counts)}</span></td>
      ${changes ? html`<td class="ds-col--secondary">${prevRunOf(r.id) ? changeWords(r.changes) : html`<span class="ds-muted">First run</span>`}</td>` : ''}
      <td class="ds-num ds-col--secondary">${V.runSpan(r)}</td>
      ${costs ? html`<td class="ds-num ds-col--secondary">${V.money(r.cost)}</td>` : ''}
      <td>${V.runChip(r.state)}</td>
    </tr>`;
  }

  function runItem(r) {
    return html`<li class="ds-list__item" data-ds-key="${r.id}">
      <span class="ds-list__text">
        <span class="ds-list__title"><a class="ds-list__action" href="${runHref(r.id)}">${V.runName(r)}</a></span>
        <span class="ds-list__meta">${[runMeta(r), V.runSpan(r), V.money(r.cost)].filter(Boolean).join(' · ')}</span>
        <span class="so-tests-result">${V.resultBar(r.counts)}${V.resultParts(r.counts)}</span>
      </span>
      ${r.state === 'done' ? '' : V.runChip(r.state)}
    </li>`;
  }

  /* -------------------------------------------------------- the run head */

  function crumbs(run) {
    return html`<nav class="ds-breadcrumbs" aria-label="Breadcrumb"><ol>
      <li><a href="${V.paths.page({ repo: state.repo, view: 'runs' })}">Runs</a></li>
      <li><a href="${runHref(run.id)}" aria-current="page">${V.runName(run, { name: false })}</a></li>
    </ol></nav>`;
  }

  function changesOf(d, rows) {
    if (d.changes) return d.changes;
    if (!rows.some((r) => r.change !== undefined)) return null;
    return { regressed: rows.filter((r) => r.change === 'regressed').length, fixed: rows.filter((r) => r.change === 'fixed').length };
  }

  function head(d, rows) {
    const active = V.ACTIVE_RUN.has(d.state);
    const problems = V.problemCount(d.counts);
    const changes = changesOf(d, rows);
    const s = d.settings || {};
    const by = V.startedBy(d);
    const prev = prevRunOf(d.id);
    const facts = [
      ['Commit', d.commit ? html`<span class="so-tests-nowrap">${d.commit.slice(0, 10)}</span>${d.dirty ? ' with changes' : ''}` : 'none (not a Git checkout)'],
      ['Started', d.started_at || d.created_at ? html`<span data-tip="${V.whenLong(d.started_at || d.created_at)}">${V.ago(d.started_at || d.created_at)}</span>` : ''],
      [active ? 'Running for' : 'Took', V.runSpan(d) || '—'],
      // The cost is unknown for a run whose sessions are not in the costs ledger.
      typeof d.cost === 'number' ? ['Cost', V.money(d.cost)] : ['Cells', V.countsWords(d.counts)],
    ];
    const factsHtml = PHONE.matches
      ? html`<p class="ds-meta"><span class="ds-parts"><span class="ds-parts__list">${facts.filter(([, v]) => v).map(([k, v]) => html`<span>${k + ' '}${v}</span>`)}</span></span></p>`
      : html`<dl class="ds-facts">${facts.map(([k, v]) => html`<div><dt>${k}</dt><dd>${v}</dd></div>`)}</dl>`;
    return html`<header class="ds-page-head so-tests-record">
        ${crumbs(d)}
        <div class="ds-page-head__row">
          <div class="ds-page-head__text">
            <h2 class="ds-page-head__title" tabindex="-1">${V.runName(d)}</h2>
            <p class="ds-page-head__lead">${V.selectionWords(d.selection, state.runs) + ': ' + V.plural((d.counts && d.counts.cells) || 0, 'cell')}${by ? html` · started by ${V.startedByHref(d) ? html`<a href="${V.startedByHref(d)}">${by}</a>` : by}` : ''}</p>
            <div class="ds-page-head__status">
              ${V.runChip(d.state)}
              ${changes && changes.regressed ? V.chip({ word: changes.regressed + ' broke since ' + (prev ? V.runName(prev, { name: false }) : 'the run before'), glyph: 'arrowDown', tone: 'error' }) : ''}
              ${changes && changes.fixed ? V.chip({ word: changes.fixed + ' fixed', glyph: 'arrowUp', tone: 'ok' }) : ''}
              ${d.pinned ? V.chip({ word: 'Pinned', glyph: 'lock' }) : ''}
            </div>
          </div>
          <div class="ds-page-head__actions">
            ${active ? html`<button class="ds-btn" type="button" data-run-act="stop" data-run="${d.id}">${raw(icon('stopCircle', 16))}Stop</button>` : ''}
            ${!active && problems ? html`<button class="ds-btn ds-btn--tonal" type="button" data-rv-rerun="${d.id}">${raw(icon('sync', 16))}Run the problems again</button>` : ''}
            ${!active && s.analysis !== false ? html`<button class="ds-btn" type="button" data-run-act="analyse" data-run="${d.id}">${raw(icon('sparkles', 16))}Analyse again</button>` : ''}
            <button class="ds-btn ds-btn--quiet ds-btn--icon" type="button" id="rvMenuBtn" aria-haspopup="menu" aria-expanded="false" aria-controls="runViewMenu" aria-label="${'More for ' + V.runName(d, { name: false })}" data-tip="More">${raw(icon('moreVertical', 20))}</button>
          </div>
        </div>
      </header>
      ${d.error && !active ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break"><strong class="ds-alert__title">${'Why it ' + (d.state === 'stopped' ? 'stopped' : 'failed')}</strong> ${d.error}</span></div>` : ''}
      ${factsHtml}
      <p class="ds-meta so-tests-agents">${raw(icon('users', 16))}${[
        s.cell ? 'Cells: ' + V.roleWords(s.cell) : '',
        s.review !== false && s.reviewer ? 'review: ' + V.roleWords(s.reviewer) : 'no review',
        s.analysis !== false && s.analyst ? 'analysis: ' + V.roleWords(s.analyst) : 'no analysis',
      ].filter(Boolean).join(' · ')}</p>`;
  }

  /* -------------------------------------------------------------- parts */

  function renderParts(d, rows) {
    const e = entry(d.id);
    const problems = rows.filter(V.isProblem).length;
    const bugCount = e.bugs ? e.bugs.bugs.length : d.bugs || 0;
    for (const tab of $('rvTabs').querySelectorAll('[role="tab"]')) {
      const on = tab.dataset.part === part;
      tab.setAttribute('aria-selected', String(on));
      tab.tabIndex = on ? 0 : -1;
      const p = PARTS.find((x) => x.id === tab.dataset.part);
      const n = p.id === 'results' ? problems : p.id === 'bugs' ? bugCount : 0;
      morph(tab, html`${p.label}${n ? html`<span class="${p.id === 'results' ? 'ds-badge ds-badge--warning' : 'ds-badge'}">${n}</span>` : ''}`);
      tab.setAttribute('aria-label', p.label + (n ? ', ' + (p.id === 'results' ? V.plural(n, 'problem') : V.plural(n, 'bug')) : ''));
    }
    if (partTabs) partTabs.update();
    const box = $('rvPart');
    box.setAttribute('aria-labelledby', 'rvt-' + part);
    if (box.dataset.partShown !== part) {
      box.dataset.partShown = part;
      morph(box, part === 'results' ? resultsFrame() : '');
      if (part === 'results') wireResults();
    }
    if (part === 'results') drawResults(d, rows);
    else if (part === 'bugs') drawBugs(d, rows, box);
    else drawDetails(d, box);
  }

  /* ------------------------------------------------------------ results */

  function resultsFrame() {
    return html`<div class="ds-toolbar so-tests-results__bar">
        <div class="ds-filters">
          <div class="ds-field ds-field--grow">
            <label class="ds-sr" for="rvSearch">Search the cells of this run</label>
            <div class="ds-search">${raw(icon('search', 20))}<input class="ds-input" id="rvSearch" type="search" placeholder="Search this run" autocomplete="off" spellcheck="false" aria-keyshortcuts="/" data-search><kbd class="ds-kbd ds-search__key" aria-hidden="true">/</kbd></div>
          </div>
          <button class="ds-btn ds-btn--quiet" type="button" id="rvFilters" aria-haspopup="dialog" aria-expanded="false" aria-controls="filterSheet"></button>
        </div>
        <fieldset class="ds-chips" id="rvChips"><legend class="ds-sr">Show</legend></fieldset>
        <div class="ds-chips" role="group" aria-label="Active filters" id="rvActive"></div>
      </div>
      <p class="ds-meta" id="rvCount" aria-live="polite"></p>
      <div class="so-tests__stack" id="rvGrid"></div>`;
  }

  function wireResults() {
    const search = $('rvSearch');
    search.value = filters.q;
    let typing = 0;
    search.addEventListener('input', () => {
      clearTimeout(typing);
      typing = setTimeout(() => { filters.q = search.value.trim(); ctx.redraw('runs'); }, TYPE_PAUSE);
    });
    $('rvFilters').addEventListener('click', () => openFilters($('rvFilters')));
  }

  const dimOf = (row, name) => (row.dims || []).find((x) => x.name === name);

  function haystack(row) {
    return [row.title, row.test, row.cell_id, row.summary, row.end_reason, row.wait, row.priority, ...(row.tags || []),
      ...(row.dims || []).flatMap((x) => [x.id, x.label]), row.final ? V.verdictWord(row.final) : V.attemptWord(row.state)]
      .filter(Boolean).join('\n').toLowerCase();
  }

  function chipMatch(row, chip) {
    switch (chip) {
      case 'problems': return V.isProblem(row);
      case 'changed': return row.change === 'regressed' || row.change === 'fixed';
      case 'reviewed': return row.review === 'done' || !!row.reviewed;
      case 'overridden': return !!row.override;
      default: return true;
    }
  }

  function filterMatch(row) {
    if (filters.verdict.size && !filters.verdict.has(row.final || 'open')) return false;
    if (filters.area.size && !filters.area.has(row.area)) return false;
    if (filters.priority.size && !filters.priority.has(row.priority)) return false;
    if (filters.tag.size && !(row.tags || []).some((t) => filters.tag.has(t))) return false;
    for (const [name, set] of Object.entries(filters.dims)) {
      if (set.size && !set.has((dimOf(row, name) || {}).id)) return false;
    }
    if (filters.q) {
      const text = haystack(row);
      if (!filters.q.toLowerCase().split(/\s+/).every((w) => text.includes(w))) return false;
    }
    return true;
  }

  function activeFilters() {
    const out = [];
    const area = (id) => ((entry(shown).detail.areas || []).find((a) => a.id === id) || {}).title || id;
    for (const v of filters.verdict) out.push({ kind: 'verdict', value: v, label: v === 'open' ? 'Not judged' : V.verdictWord(v) });
    for (const a of filters.area) out.push({ kind: 'area', value: a, label: area(a) });
    for (const p of filters.priority) out.push({ kind: 'priority', value: p, label: p });
    for (const t of filters.tag) out.push({ kind: 'tag', value: t, label: '#' + t });
    for (const [name, set] of Object.entries(filters.dims)) for (const id of set) out.push({ kind: 'dim', name, value: id, label: V.titleCase(name) + ': ' + id });
    return out;
  }

  function drawResults(d, rows) {
    // Chips count what the search and the filters leave.
    const base = rows.filter(filterMatch);
    const canChange = rows.some((r) => r.change !== undefined);
    const canOverride = rows.some((r) => r.override);
    const chips = CHIPS.filter((c) => (c.id !== 'changed' || canChange) && (c.id !== 'overridden' || canOverride));
    if (!chips.some((c) => c.id === filters.chip)) filters.chip = 'all';
    morph($('rvChips'), html`<legend class="ds-sr">Show</legend>${chips.map((c) => {
      const n = base.filter((r) => chipMatch(r, c.id)).length;
      return html`<label class="ds-chip" data-ds-key="${c.id}"><input class="ds-chip__input" type="radio" name="rvChip" value="${c.id}"${c.id === filters.chip ? raw(' checked') : ''}><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${c.label}</span> <span class="${c.id === 'problems' && n ? 'ds-badge ds-badge--warning' : 'ds-badge ds-badge--neutral'}">${n}</span></span></label>`;
    })}`);
    for (const input of $('rvChips').querySelectorAll('input')) input.checked = input.value === filters.chip;
    const act = activeFilters();
    morph($('rvFilters'), html`${raw(icon('filter', 16))}Filters${act.length ? html` <span class="ds-badge">${act.length}</span>` : ''}`);
    $('rvFilters').setAttribute('aria-label', act.length ? 'Filters, ' + act.length + ' on' : 'Filters');
    morph($('rvActive'), act.length ? html`${act.map((f) => html`<button class="ds-chip ds-chip--input" type="button" data-unfilter="${f.kind}" data-name="${f.name || ''}" data-value="${f.value}" data-ds-key="${f.kind + ':' + (f.name || '') + ':' + f.value}" data-tip="${'Remove the filter ' + f.label}"><span class="ds-chip__body"><span class="ds-chip__label"><span class="ds-sr">Remove filter </span>${f.label}</span>${raw(icon('close', 18, 'ds-chip__trailing'))}</span></button>`)}
      <button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-unfilter="all">Clear filters</button>` : '');

    const shownRows = base.filter((r) => chipMatch(r, filters.chip));
    visible = shownRows;
    const countText = shownRows.length === rows.length ? V.plural(rows.length, 'cell') : shownRows.length + ' of ' + V.plural(rows.length, 'cell');
    if ($('rvCount').textContent !== countText) $('rvCount').textContent = countText;
    if ($('filterCount')) $('filterCount').textContent = countText;
    const showWords = shownRows.length ? 'Show ' + V.plural(shownRows.length, 'cell') : 'No cell matches';
    if ($('filterShow').textContent !== showWords) $('filterShow').textContent = showWords;

    const grid = $('rvGrid');
    if (!rows.length) {
      morph(grid, html`<section class="ds-card" data-ds-key="empty"><div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('clock', 24))}</span>
        <p class="ds-empty__title">No cells yet</p>
        <p class="ds-empty__text">${V.ACTIVE_RUN.has(d.state) ? 'The run is being prepared: its cells show up here as they are queued.' : 'The run ended before any cell was queued.'}</p>
      </div></section>`);
      return;
    }
    if (!shownRows.length) {
      morph(grid, html`<section class="ds-card" data-ds-key="empty"><div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('filter', 24))}</span>
        <p class="ds-empty__title">No cell matches</p>
        <p class="ds-empty__text">${filters.chip === 'problems' && !filters.q && !act.length ? 'Every cell of this run passed.' : 'Clear the search or a filter to see more.'}</p>
        ${filters.q || act.length || filters.chip !== 'all' ? html`<button class="ds-btn ds-btn--tonal" type="button" data-unfilter="everything">Show every cell</button>` : ''}
      </div></section>`);
      return;
    }
    const areas = (d.areas && d.areas.length) ? d.areas : [...new Set(rows.map((r) => r.area))].map((id) => ({ id, title: id }));
    const phone = PHONE.matches;
    morph(grid, html`${areas.map((area) => areaCard(area, shownRows.filter((r) => r.area === area.id), rows.filter((r) => r.area === area.id), phone))}`);
  }

  function columnsOf(area, rows) {
    const dim = (area.dims || []).find((x) => x.name === area.columns);
    if (!dim) return [{ id: '', label: 'Result' }];
    const used = new Set(rows.map((r) => (dimOf(r, area.columns) || {}).id));
    const cols = [...(dim.values || []), ...(dim.extra || [])].filter((v) => used.has(v.id));
    for (const id of used) if (id && !cols.some((c) => c.id === id)) cols.push({ id, label: id });
    return cols;
  }

  function byTest(rows) {
    const order = [];
    const map = new Map();
    for (const r of rows) {
      if (!map.has(r.test)) { map.set(r.test, []); order.push(r); }
      map.get(r.test).push(r);
    }
    return order.map((first) => ({ first, cells: map.get(first.test) }));
  }

  function areaCard(area, rows, all, phone) {
    if (!rows.length) return '';
    const problems = all.filter(V.isProblem).length;
    const meta = V.plural(all.length, 'cell') + (problems ? ' · ' + V.plural(problems, 'problem') : '') + (rows.length < all.length ? ' · ' + rows.length + ' shown' : '');
    const tests = byTest(rows);
    return html`<section class="ds-card" data-ds-key="${'area:' + area.id}" aria-labelledby="${'ra-' + area.id}">
      <div class="ds-card__row">
        <h3 class="ds-card__title" id="${'ra-' + area.id}">${area.title || area.id}</h3>
        <span class="ds-meta">${meta}</span>
      </div>
      ${phone ? tests.map((t) => testBlock(t, area)) : areaTable(area, tests, rows)}
    </section>`;
  }

  function tagsOf(row) {
    return html`<span class="so-tests__sub">${row.priority ? html`<span class="ds-tag ds-tag--neutral">${row.priority}</span>` : ''}${(row.tags || []).map((t) => html`<span class="ds-tag">${t}</span>`)}</span>`;
  }

  function rerunButton(row, sm) {
    return html`<button class="${'ds-btn ds-btn--quiet ds-btn--icon' + (sm ? ' ds-btn--sm' : '')}" type="button" data-rerun-test="${row.test}" aria-label="${'Run “' + row.title + '” again'}" data-tip="Run this test again">${raw(icon('play', sm ? 18 : 20))}</button>`;
  }

  function areaTable(area, tests, rows) {
    const columns = columnsOf(area, rows);
    return html`<div class="ds-table-wrap"><table class="ds-table ds-table--dense ds-table--pin-first so-tests__table">
      <caption class="ds-sr">${(area.title || area.id) + (area.columns ? ': a row per test, a column per ' + area.columns : '')}</caption>
      <thead><tr>
        <th scope="col">Test</th>
        ${columns.map((c) => html`<th scope="col">${c.label || c.id}</th>`)}
        <th scope="col"><span class="ds-sr">Actions</span></th>
      </tr></thead>
      <tbody>${tests.map(({ first, cells }) => html`<tr data-ds-key="${first.test}">
        <th scope="row" class="so-tests__test"><button class="ds-link-btn" type="button" data-test="${first.test}">${first.title}</button>${tagsOf(first)}</th>
        ${columns.map((c) => {
          const here = cells.filter((r) => !area.columns || (dimOf(r, area.columns) || {}).id === c.id);
          return html`<td><span class="so-tests__cells">${here.map((r) => cellButton(r, area))}</span></td>`;
        })}
        <td class="ds-table__actions">${rerunButton(first)}</td>
      </tr>`)}</tbody>
    </table></div>`;
  }

  // cellWords is everything a cell's button says, for the reader.
  function cellWords(row, area) {
    const parts = [row.title, V.dimsWords(row.dims) || row.cell, row.final ? V.verdictWord(row.final) : V.attemptWord(row.state)];
    if (row.state === 'waiting' && row.wait) parts.push(row.wait);
    if (row.change === 'regressed' || row.change === 'fixed') parts.push(V.CHANGE[row.change].word + (row.prev && row.prev.verdict ? ', was ' + V.verdictWord(row.prev.verdict) : ''));
    if (row.override) parts.push('overridden');
    else if (row.reviewed && row.verdict && row.reviewed !== row.verdict) parts.push('the reviewer changed it from ' + V.verdictWord(row.verdict));
    else if (row.review === 'done') parts.push('reviewed');
    if (row.attempt > 1) parts.push('attempt ' + row.attempt);
    return parts.filter(Boolean).join(', ');
  }

  function marks(row) {
    return html`${V.changeMark(row)}${row.override ? html`<span class="so-tests__mark" role="img" aria-label="Overridden" data-tip="${'You set it: ' + (row.override.note || V.verdictWord(row.override.verdict))}">${raw(icon('pencil', 16))}</span>`
      : row.reviewed && row.verdict && row.reviewed !== row.verdict ? html`<span class="so-tests__mark" role="img" aria-label="${'The reviewer changed it from ' + V.verdictWord(row.verdict)}" data-tip="${'The agent said ' + V.verdictWord(row.verdict) + '; the reviewer changed it'}">${raw(icon('eye', 16))}</span>`
        : row.review === 'done' ? html`<span class="so-tests__mark" role="img" aria-label="Reviewed" data-tip="Reviewed, unchanged">${raw(icon('eye', 16))}</span>` : ''}${
      row.attempt > 1 ? html`<span class="ds-meta" data-tip="${'Attempt ' + row.attempt}">${'#' + row.attempt}</span>` : ''}`;
  }

  function cellButton(row, area) {
    const others = (row.dims || []).filter((x) => x.name !== area.columns);
    const tip = row.state === 'waiting' && row.wait ? row.wait : row.summary || '';
    return html`<button class="ds-pressable so-tests__cell" type="button" data-cell="${row.id}" data-ds-key="${row.id}" aria-label="${cellWords(row, area)}"${tip ? raw(html` data-tip="${tip.length > 160 ? tip.slice(0, 157) + '…' : tip}"`) : ''}>
      ${V.cellChip(row, { tip: false })}${others.map((x) => html`<span class="ds-tag ds-tag--neutral">${x.label || x.id}</span>`)}${marks(row)}
    </button>`;
  }

  function testBlock({ first, cells }, area) {
    return html`<div class="so-tests-block" data-ds-key="${first.test}">
      <div class="so-tests-block__head">
        <div class="so-tests-block__title"><button class="ds-link-btn" type="button" data-test="${first.test}">${first.title}</button>${tagsOf(first)}</div>
        ${rerunButton(first, true)}
      </div>
      <ul class="ds-list ds-list--interactive" aria-label="${'Cells of ' + first.title}">${cells.map((r) => {
        const meta = [
          r.change === 'regressed' || r.change === 'fixed' ? V.CHANGE[r.change].word + (r.prev && r.prev.verdict ? ': was ' + V.verdictWord(r.prev.verdict) : '') : '',
          r.override ? 'overridden' : r.reviewed && r.verdict && r.reviewed !== r.verdict ? 'the reviewer changed it' : r.review === 'done' ? 'reviewed' : '',
          r.attempt > 1 ? 'attempt ' + r.attempt : '',
          r.state === 'waiting' ? r.wait : '',
        ].filter(Boolean).join(' · ');
        return html`<li class="ds-list__item" data-ds-key="${r.id}">
          <span class="ds-list__text">
            <span class="ds-list__title"><button class="ds-list__action" type="button" data-cell="${r.id}" aria-label="${cellWords(r, area)}">${V.dimsWords(r.dims) || r.cell || 'Result'}</button></span>
            ${meta ? html`<span class="ds-list__meta">${meta}</span>` : ''}
          </span>
          ${V.cellChip(r, { tip: false })}
        </li>`;
      })}</ul>
    </div>`;
  }

  /* -------------------------------------------------------------- filters */

  function openFilters(opener) {
    drawFilters();
    DS.overlay.open($('filterSheet'), { opener });
  }

  function drawFilters() {
    const e = entry(shown);
    const rows = e.cells || [];
    const areas = (e.detail && e.detail.areas) || [];
    const seen = (fn) => [...new Set(rows.flatMap(fn))].filter(Boolean).sort();
    const verdicts = [...V.VERDICTS.filter((v) => rows.some((r) => r.final === v)), ...(rows.some((r) => !r.final) ? ['open'] : [])];
    const group = (title, kind, items, set, name = '') => (items.length < 2 && !set.size ? '' : html`<fieldset class="ds-field" data-ds-key="${kind + name}">
      <legend class="ds-field__label">${title}</legend>
      <div class="ds-chips">${items.map(([value, label]) => html`<label class="ds-chip"><input class="ds-chip__input" type="checkbox" data-filter="${kind}" data-name="${name}" value="${value}"${set.has(value) ? raw(' checked') : ''}><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${label}</span> <span class="ds-badge ds-badge--neutral">${rows.filter((r) => match(r, kind, name, value)).length}</span></span></label>`)}</div>
    </fieldset>`);
    const dims = new Map();
    for (const r of rows) for (const x of r.dims || []) {
      if (!dims.has(x.name)) dims.set(x.name, new Map());
      dims.get(x.name).set(x.id, x.label || x.id);
    }
    const parts = [
      group('Verdict', 'verdict', verdicts.map((v) => [v, v === 'open' ? 'Not judged' : V.verdictWord(v)]), filters.verdict),
      group('Area', 'area', (areas.length ? areas.map((a) => [a.id, a.title || a.id]) : seen((r) => [r.area]).map((a) => [a, a])), filters.area),
      group('Priority', 'priority', seen((r) => [r.priority]).map((p) => [p, p]), filters.priority),
      group('Tag', 'tag', seen((r) => r.tags || []).map((t) => [t, t]), filters.tag),
      ...[...dims].map(([name, values]) => group(V.titleCase(name), 'dim', [...values], filters.dims[name] || new Set(), name)),
    ].filter(Boolean);
    morph($('filterBody'), html`<form class="ds-form so-tests__stack" novalidate>${parts.length ? parts : html`<p class="ds-muted">Every cell of this run has the same area, priority and values: there is nothing to narrow.</p>`}</form>`);
  }

  function match(r, kind, name, value) {
    if (kind === 'verdict') return (r.final || 'open') === value;
    if (kind === 'area') return r.area === value;
    if (kind === 'priority') return r.priority === value;
    if (kind === 'tag') return (r.tags || []).includes(value);
    return (dimOf(r, name) || {}).id === value;
  }

  function setOf(kind, name) {
    if (kind !== 'dim') return filters[kind];
    if (!filters.dims[name]) filters.dims[name] = new Set();
    return filters.dims[name];
  }

  function clearFilters() {
    filters.verdict.clear();
    filters.area.clear();
    filters.priority.clear();
    filters.tag.clear();
    filters.dims = {};
  }

  $('filterBody').addEventListener('change', (event) => {
    const input = event.target.closest('[data-filter]');
    if (!input) return;
    const set = setOf(input.dataset.filter, input.dataset.name);
    if (input.checked) set.add(input.value);
    else set.delete(input.value);
    ctx.redraw('runs');
  });
  $('filterReset').addEventListener('click', () => {
    clearFilters();
    drawFilters();
    ctx.redraw('runs');
  });

  /* ----------------------------------------------------------------- bugs */

  function drawBugs(d, rows, box) {
    const e = entry(d.id);
    if (!e.bugs || e.bugsStale) loadBugs(d.id);
    if (!e.bugs) {
      morph(box, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the bugs</div>`);
      return;
    }
    const { bugs, conclusions } = e.bugs;
    const analysis = d.analysis;
    const analysing = analysis && analysis.state === 'running';
    const s = d.settings || {};
    const sorted = bugs.slice().sort((a, b) => V.sevRank(a.severity) - V.sevRank(b.severity));
    const byCell = new Map(rows.map((r) => [r.cell_id, r]));
    const empty = analysing ? 'The analyst is reading the run.'
      : analysis ? 'The analyst found no bug.'
        : s.analysis === false ? 'This run is not analysed.'
          : V.ACTIVE_RUN.has(d.state) ? 'The analyst reads the run once its cells are reviewed.' : 'There is no analysis of this run.';
    morph(box, html`<section class="ds-card" aria-labelledby="rvBugsTitle" data-ds-key="bugs">
        <div class="ds-card__row">
          <h3 class="ds-card__title" id="rvBugsTitle">${'Bugs of ' + V.runName(d, { name: false })}</h3>
          ${analysing ? V.chip({ word: 'Analysing', spin: true }) : ''}
        </div>
        ${e.bugs.error ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${e.bugs.error}</span></div>` : ''}
        ${analysis && analysis.error ? html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text so-break">${analysis.error}</span></div>` : ''}
        ${sorted.length ? html`<div class="ds-disclosure-list">${sorted.map((bug) => html`<details class="ds-collapsible ds-disclosure ds-disclosure--row" data-ds-key="${'bug-' + bug.id}">
          <summary><span class="ds-disclosure__title">${bug.id + ' · ' + bug.title}</span><span class="ds-disclosure__meta">${V.severityWord(bug.severity) + ' · ' + V.plural((bug.cells || []).length, 'cell')}</span>${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
          <div class="ds-disclosure__body so-tests__stack">
            <p>${V.severityChip(bug.severity)}</p>
            ${bug.description ? html`<div class="ds-prose ds-prose--dense so-break">${markdown(bug.description)}</div>` : ''}
            ${(bug.cells || []).length ? html`<p class="so-tests__links">${(bug.cells || []).map((ref) => {
              const r = byCell.get(ref);
              return r ? html`<button class="ds-link-btn" type="button" data-cell="${r.id}" data-bug-cells="${bug.id}">${r.title + ' · ' + (V.dimsWords(r.dims) || r.cell)}</button>` : html`<span class="ds-meta">${ref}</span>`;
            })}</p>` : ''}
          </div>
        </details>`)}</div>` : html`<p class="ds-muted">${empty}</p>`}
      </section>
      ${conclusions ? html`<section class="ds-card" aria-labelledby="rvConclTitle" data-ds-key="conclusions">
        <h3 class="ds-card__title" id="rvConclTitle">Conclusions</h3>
        <div class="ds-prose ds-prose--dense so-break">${markdown(V.stripHeading(conclusions))}</div>
      </section>` : ''}`);
  }

  /* -------------------------------------------------------------- details */

  function drawDetails(d, box) {
    const s = d.settings || {};
    const sel = V.selectionWords(d.selection, state.runs);
    const role = (r) => {
      if (!r || !r.harness) return html`<span class="ds-muted">Not set</span>`;
      const n = (r.accounts || []).length;
      return V.roleWords(r) + ' · ' + (n ? V.plural(n, 'account') + ' ticked' : 'any account');
    };
    const helpers = [...(d.reviews || []).map((h, i) => ({ ...h, label: 'Reviewer ' + (i + 1) })), ...(d.analysis ? [{ ...d.analysis, label: 'Analyst' }] : [])];
    const helperWord = { running: { word: 'Working', spin: true }, done: { word: 'Done', glyph: 'checkCircle', tone: 'ok' }, failed: { word: 'Failed', glyph: 'alertCircle', tone: 'error' } };
    const errors = [d.error, ...helpers.map((h) => (h.error ? h.label + ': ' + h.error : ''))].filter(Boolean);
    morph(box, html`${errors.length ? html`<section class="ds-card" aria-labelledby="rvErrTitle" data-ds-key="errors">
        <h3 class="ds-card__title" id="rvErrTitle">What went wrong</h3>
        ${errors.map((text) => html`<div class="ds-alert ds-alert--plain ds-alert--error">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${text}</span></div>`)}
      </section>` : ''}
      <section class="ds-card" aria-labelledby="rvSetTitle" data-ds-key="settings">
        <h3 class="ds-card__title" id="rvSetTitle">How this run was set up</h3>
        <dl class="ds-facts ds-facts--rows">
          <div><dt>What it ran</dt><dd>${sel + ': ' + V.plural((d.counts && d.counts.cells) || 0, 'cell')}</dd></div>
          <div><dt>Cell agent</dt><dd>${role(s.cell)}</dd></div>
          <div><dt>Reviewer</dt><dd>${s.review === false ? html`<span class="ds-muted">No review</span>` : role(s.reviewer)}</dd></div>
          <div><dt>Analyst</dt><dd>${s.analysis === false ? html`<span class="ds-muted">No analysis</span>` : role(s.analyst)}</dd></div>
          <div><dt>Cells at once</dt><dd>${s.parallel > 0 ? String(s.parallel) : 'As many as the tools’ accounts allow'}</dd></div>
          <div><dt>Attempts per cell</dt><dd>${s.attempts > 1 ? s.attempts + ' (a blocked cell is tried again)' : String(s.attempts || 1)}</dd></div>
          ${s.mcp && s.mcp.length ? html`<div><dt>MCP servers</dt><dd>${s.mcp.join(', ')}</dd></div>` : ''}
          <div><dt>Commit</dt><dd class="so-break">${d.commit ? d.commit + (d.dirty ? ' with changes' : '') : 'Not a Git checkout'}</dd></div>
          <div><dt>Created</dt><dd>${V.whenLong(d.created_at)}</dd></div>
          ${d.ended_at ? html`<div><dt>Ended</dt><dd>${V.whenLong(d.ended_at)}</dd></div>` : ''}
          <div><dt>Run id</dt><dd class="so-break">${d.id}</dd></div>
        </dl>
      </section>
      <section class="ds-card" aria-labelledby="rvHelpTitle" data-ds-key="helpers">
        <h3 class="ds-card__title" id="rvHelpTitle">Reviewers and analyst</h3>
        ${helpers.length ? html`<ul class="ds-list ds-list--interactive" aria-label="Reviewers and analyst">${helpers.map((h) => html`<li class="ds-list__item" data-ds-key="${h.id}">
          <span class="ds-list__text">
            <span class="ds-list__title">${h.chat ? html`<a class="ds-list__action" href="${'/#open=' + encodeURIComponent(h.chat)}">${h.label}</a>` : h.label}</span>
            <span class="ds-list__meta">${[(h.cells || []).length ? V.plural(h.cells.length, 'cell') : '', V.roleWords(h.role), h.ended_at ? V.span(h.ended_at - h.started_at) : '', h.chat ? 'opens its transcript' : ''].filter(Boolean).join(' · ')}</span>
          </span>
          ${V.chip(helperWord[h.state] || { word: h.state, glyph: 'helpCircle' })}
        </li>`)}</ul>` : html`<p class="ds-muted">${V.ACTIVE_RUN.has(d.state) ? 'They start once there are cells to review.' : 'No reviewer or analyst worked on this run.'}</p>`}
      </section>
      ${(d.pools || []).length ? html`<section class="ds-card" aria-labelledby="rvPoolTitle" data-ds-key="pools">
        <h3 class="ds-card__title" id="rvPoolTitle">Accounts of the tools</h3>
        ${d.pools.map((pool) => html`<h4 class="ds-card__subtitle">${pool.title || pool.tool}</h4><ul class="ds-kv-list">${(pool.members || []).map((m) => html`<li><span>${m.label || 'Account ' + m.n}</span><strong>${
          m.holders && m.holders.length ? V.holderWords(m.holders, entry(d.id).cells)
            : m.busy ? html`${raw(icon('ban', 16, 'ds-kv-list__icon ds-kv-list__icon--warning'))}<span class="ds-sr">Busy:</span> in use outside Socrates`
              : html`<span class="ds-muted">Free</span>`}</strong></li>`)}</ul>`)}
      </section>` : ''}`);
  }

  /* ---------------------------------------------------------------- menu */

  function fillMenu() {
    const d = entry(shown).detail;
    if (!d) return;
    const item = (glyph, label, attrs) => html`<button class="ds-menu__item" role="menuitem" type="button" ${raw(attrs)}>${raw(icon(glyph, 20))}<span class="ds-menu__label">${label}</span></button>`;
    morph($('runViewMenu'), html`${item('copy', 'Copy report', 'data-rv-menu="copy"')}
      ${item('download', 'Download report', 'data-rv-menu="download"')}
      <div class="ds-menu__divider" role="separator"></div>
      <button class="ds-menu__item" role="menuitemcheckbox" type="button" aria-checked="${d.pinned ? 'true' : 'false'}" data-rv-menu="pin">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">Pinned: kept whatever the retention says</span></button>
      <div class="ds-menu__divider" role="separator"></div>
      ${item('trash', 'Delete run…', 'data-rv-menu="delete"' + (V.ACTIVE_RUN.has(d.state) ? ' aria-disabled="true"' : ''))}`);
  }

  // report is the run in Markdown (GET …/report.md): its head, the counts
  // and changes, the bugs, every problem cell with its failing steps.
  async function report(id) {
    const res = await api(V.paths.report(id), { raw: true });
    return res.text();
  }

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      const area = document.createElement('textarea');
      area.value = text;
      area.setAttribute('readonly', '');
      area.className = 'ds-sr';
      document.body.append(area);
      area.select();
      const done = document.execCommand('copy');
      area.remove();
      return done;
    }
  }

  async function menuAction(what) {
    const id = shown;
    const d = entry(id).detail;
    if (!d) return;
    if (what === 'copy') {
      try {
        toast((await copy(await report(id))) ? 'The report is copied.' : 'The report could not be copied.', 'success');
      } catch (err) { toast(errorMessage(err), 'error'); }
    } else if (what === 'download') {
      const a = document.createElement('a');
      a.href = V.paths.report(id, true);
      a.download = '';
      document.body.append(a);
      a.click();
      a.remove();
    } else if (what === 'pin') {
      await ctx.runAction('pin', id, $('rvMenuBtn'));
      entry(id).detail = { ...d, pinned: !d.pinned };
      ctx.redraw('runs');
    } else if (what === 'delete') {
      await ctx.runAction('delete', id, $('rvMenuBtn'));
    }
  }

  $('runViewMenu').addEventListener('click', (event) => {
    const it = event.target.closest('[data-rv-menu]');
    if (it && it.getAttribute('aria-disabled') !== 'true') menuAction(it.dataset.rvMenu);
  });

  /* ------------------------------------------------------------- wiring */

  function wire(panel) {
    panel.dataset.wired = '1';
    panel.addEventListener('click', (event) => {
      const t = event.target;
      const menuBtn = t.closest('#rvMenuBtn');
      if (menuBtn) {
        if (event.detail > 1) return;
        if (menuBtn.getAttribute('aria-expanded') === 'true') DS.overlay.close($('runViewMenu'));
        else { fillMenu(); DS.overlay.open($('runViewMenu'), { opener: menuBtn }); }
        return;
      }
      const tab = t.closest('#rvTabs [role="tab"]');
      if (tab) {
        if (tab.dataset.part !== part) {
          part = tab.dataset.part;
          ctx.redraw('runs');
          M.enter($('rvPart'), 'swap');
        }
        return;
      }
      const cellBtn = t.closest('[data-cell]');
      if (cellBtn) {
        const bugOf = cellBtn.dataset.bugCells;
        let list = visible.map((r) => ({ attempt: r.id, run: shown }));
        if (bugOf) {
          const e = entry(shown);
          const bug = ((e.bugs && e.bugs.bugs) || []).find((b) => b.id === bugOf);
          const byCell = new Map(rowsOf(shown).map((r) => [r.cell_id, r]));
          list = ((bug && bug.cells) || []).map((ref) => byCell.get(ref)).filter(Boolean).map((r) => ({ attempt: r.id, run: shown }));
        }
        ctx.openCell(cellBtn.dataset.cell, shown, list);
        return;
      }
      const test = t.closest('[data-test]');
      if (test) { ctx.go({ view: 'tests', test: test.dataset.test, run: '', bug: '', cell: '' }, { push: true }); return; }
      const rerunTest = t.closest('[data-rerun-test]');
      if (rerunTest) { ctx.openRunDialog({ tests: [rerunTest.dataset.rerunTest] }, rerunTest); return; }
      const rerun = t.closest('[data-rv-rerun]');
      if (rerun) { ctx.startRun({ problems_of: rerun.dataset.rvRerun }, rerun); return; }
      if (t.closest('[data-rv-dialog]')) { ctx.openRunDialog(undefined, t.closest('[data-rv-dialog]')); return; }
      const un = t.closest('[data-unfilter]');
      if (un) {
        const kind = un.dataset.unfilter;
        if (kind === 'all') clearFilters();
        else if (kind === 'everything') {
          clearFilters();
          filters.q = '';
          filters.chip = 'all';
          if ($('rvSearch')) $('rvSearch').value = '';
        } else setOf(kind, un.dataset.name).delete(un.dataset.value);
        ctx.redraw('runs');
        if (kind === 'all' || kind === 'everything' || !$('rvActive').querySelector('[data-unfilter]')) $('rvSearch').focus();
      }
    });
    panel.addEventListener('change', (event) => {
      const chip = event.target.closest('input[name="rvChip"]');
      if (chip && chip.checked) {
        filters.chip = chip.value;
        ctx.redraw('runs');
      }
    });
  }

  PHONE.addEventListener('change', () => ctx.redraw('runs'));

  return {
    render,
    async refresh() {
      // A frame said the repository changed: the shown run is read again
      // (the live one is the frame's), and its bugs once more if they are open.
      const id = state.route.run;
      for (const [key, e] of cache) {
        if (key === id) continue;
        if (e.detail && !V.ACTIVE_RUN.has(e.detail.state)) continue;
        cache.delete(key);
      }
      if (!id) return;
      const e = entry(id);
      if (!(state.live && state.live.id === id)) {
        try {
          const [detail, cells] = await Promise.all([api(V.paths.run(id)), api(V.paths.cells(id))]);
          e.detail = detail;
          e.cells = cells.cells || [];
          e.error = '';
        } catch (err) {
          e.error = errorMessage(err);
          e.detail = null;
        }
      }
      if (e.bugs) e.bugsStale = true;
    },
    close() {
      if (DS.overlay.topmost() === $('filterSheet')) DS.overlay.close($('filterSheet'));
    },
    repoChanged() {
      cache.clear();
      shown = '';
      clearFilters();
      filters.q = '';
      filters.chip = 'all';
    },
  };
}
