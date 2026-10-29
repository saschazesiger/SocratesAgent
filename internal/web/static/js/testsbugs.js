// The Bugs view of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", Bugs, and "Revision: the reworked Tests page", bugs over runs): the
// repository's bugs over every run, and a bug's own view.
//
// The list: chips by status (Open · Known · Fixed · Not a bug · All, each
// with its count; Open first), a search, the bugs worst first and then by
// when they were last seen. A row says its severity, id and title, its
// status ("came back" for one that was fixed and reported again), how many
// runs saw it and from which to which, its cells and its GitHub issue.
//
// The bug view (#…&view=bugs&bug=B-4): its head (severity, status, title)
// with Fix in a chat, Run its cells again and its GitHub issue (made here,
// or its link); what the analyst found; its cells, each with its latest
// verdict and the screenshot of its first failing step, opening the cell
// viewer; the triage; the runs that saw it; its history.
//
// The triage saves every change by itself, so nothing in it is ever left
// unsaved: a status or a severity the moment it is chosen, with a snackbar
// that can undo it (a wrong tap is taken back from the same place); the
// note when the field is left (or with Ctrl+Enter), with a quiet "Saved"
// under it rather than a snackbar: saving every keystroke would write the
// bug's history full.

import { api, errorMessage, toast, HttpError } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import { markdown } from './md.js';
import * as V from './testsview.js';

const $ = (id) => document.getElementById(id);
const PHONE = window.matchMedia('(max-width: 600px)');
const TYPE_PAUSE = 200;
// How long a triage choice rests before it is saved (arrow keys walk through the others).
const CHOICE_PAUSE = 500;
// The cells whose failing step is read for the bug view (each is a request).
const STEPS_READ = 8;
const NOTE_MAX = 4000;

const CHIPS = [
  { id: 'open', label: 'Open' },
  { id: 'known', label: 'Known' },
  { id: 'fixed', label: 'Fixed' },
  { id: 'not_a_bug', label: 'Not a bug' },
  { id: 'all', label: 'All' },
];

// What a chip with nothing in it says: why there is nothing, and where the
// bugs it would hold come from.
const EMPTY = {
  open: ['No open bug', 'Every bug the analyst found is fixed or triaged.'],
  known: ['No known bug', 'A bug you accept for now is marked Known: it stays listed and is reported again when seen, but no longer counts as open.'],
  fixed: ['No fixed bug yet', 'A bug is fixed once a run passes every one of its cells, or when you mark it fixed.'],
  not_a_bug: ['Nothing is marked Not a bug', 'Mark a finding Not a bug and the analyst stops reporting it, unless the evidence changes.'],
  all: ['No bug yet', 'Once a run is analysed, the bugs it finds are tracked here over the runs.'],
};

const TRIAGE_TITLE = { open: 'Open', known: 'Known – accepted for now', not_a_bug: 'Not a bug', fixed: 'Fixed' };

export function mount(ctx) {
  const { state } = ctx;
  let list = null; // { repo, bugs, error, loading }
  const details = new Map(); // bug id -> { data: { bug, cells, runs }, error, loading, stale }
  const steps = new Map(); // attempt id -> its problem steps, once read
  let github = null; // { repo, status, loading }
  const filter = { chip: 'open', q: '' };
  let drawn = ''; // 'list' or the bug whose view is drawn
  const notes = new Map(); // bug id -> the note as being written (absent: as saved)
  let lastUndo = null; // the snackbar that undoes the latest triage

  /* -------------------------------------------------------------- data */

  async function loadList() {
    const repo = state.repo;
    if (list && list.repo === repo && list.loading) return;
    list = { ...(list && list.repo === repo ? list : { repo, bugs: null }), loading: true };
    try {
      const d = await api(V.paths.repoBugs(repo), { attempts: 2 });
      if (repo !== state.repo) return;
      list = { repo, bugs: d.bugs || [], error: '' };
    } catch (err) {
      if (repo !== state.repo) return;
      list = { repo, bugs: list.bugs, error: errorMessage(err) };
    }
    ctx.redraw('bugs');
  }

  async function loadBug(id) {
    const repo = state.repo;
    const e = details.get(id) || {};
    if (e.loading) return;
    details.set(id, { ...e, loading: true });
    try {
      const data = await api(V.paths.bug(repo, id), { attempts: 2 });
      if (repo !== state.repo) return;
      details.set(id, { data, error: '' });
      readSteps(data);
    } catch (err) {
      if (repo !== state.repo) return;
      details.set(id, { data: e.data || null, error: errorMessage(err) });
    }
    ctx.redraw('bugs');
  }

  // readSteps reads the failing steps of a bug's first cells, which the
  // cards show under their screenshots.
  // A cell is of the bug's last run, or of the newest run that has it when
  // that run does not (row.run).
  function readSteps(data) {
    for (const row of (data.cells || []).slice(0, STEPS_READ)) {
      if (steps.has(row.id) || V.passed(row.final)) continue;
      steps.set(row.id, null);
      V.attemptView(row.run || data.bug.last_run, row.id).then((view) => {
        steps.set(row.id, V.problemSteps(view));
        if (state.route.bug === data.bug.id) ctx.redraw('bugs');
      }, () => steps.delete(row.id));
    }
  }

  async function loadGitHub() {
    const repo = state.repo;
    if (github && github.repo === repo) return;
    github = { repo, status: null, loading: true };
    try {
      const status = await api(V.paths.github(repo), { attempts: 2, timeout: 30000 });
      if (repo === state.repo) github = { repo, status };
    } catch (err) {
      if (repo === state.repo) github = { repo, status: { available: false, reason: errorMessage(err) } };
    }
    ctx.redraw('bugs');
  }

  // setBug puts a bug the server sent (PATCH, issue) everywhere it is shown.
  function setBug(bug) {
    const d = details.get(bug.id);
    if (d && d.data) d.data = { ...d.data, bug };
    if (list && list.bugs) list.bugs = list.bugs.map((b) => (b.id === bug.id ? bug : b));
  }

  const bugOf = (id) => {
    const d = details.get(id);
    return (d && d.data && d.data.bug) || (list && list.bugs ? list.bugs.find((b) => b.id === id) : null) || null;
  };

  /* ------------------------------------------------------------- render */

  function render(panel) {
    if (!panel.dataset.wired) wire(panel);
    if (!list || list.repo !== state.repo) loadList();
    const id = state.route.bug;
    if (id) renderBug(panel, id);
    else renderList(panel);
  }

  /* --------------------------------------------------------------- list */

  function listFrame() {
    return html`<div class="so-tests__stack" data-ds-key="bugs-list">
      <div class="ds-toolbar">
        <div class="ds-filters">
          <div class="ds-field ds-field--grow">
            <label class="ds-sr" for="bgSearch">Search the bugs</label>
            <div class="ds-search">${raw(icon('search', 20))}<input class="ds-input" id="bgSearch" type="search" placeholder="Search the bugs" autocomplete="off" spellcheck="false" aria-keyshortcuts="/" data-search><kbd class="ds-kbd ds-search__key" aria-hidden="true">/</kbd></div>
          </div>
        </div>
        <fieldset class="ds-chips" id="bgChips"><legend class="ds-sr">Show the bugs that are</legend></fieldset>
      </div>
      <div id="bgList" class="so-tests__stack"></div>
    </div>`;
  }

  function haystack(b) {
    return [b.id, b.title, b.description, b.note, b.severity, V.bugStatusWord(b.status), ...(b.cells || [])].filter(Boolean).join('\n').toLowerCase();
  }

  function chipMatch(b, chip) {
    return chip === 'all' || b.status === chip;
  }

  function sorted(bugs) {
    const num = (id) => parseInt(String(id).replace(/^\D+/, ''), 10) || 0;
    return bugs.slice().sort((a, b) => V.sevRank(a.severity) - V.sevRank(b.severity)
      || (b.last_number || 0) - (a.last_number || 0) || num(a.id) - num(b.id));
  }

  // wired is the elements a listener was bound to, which morph keeps.
  const wired = new WeakSet();

  function renderList(panel) {
    if (drawn !== 'list') {
      drawn = 'list';
      morph(panel, listFrame());
      const search = $('bgSearch');
      search.value = filter.q;
      // morph keeps the field when the list is drawn again: bound once.
      if (!wired.has(search)) {
        wired.add(search);
        let typing = 0;
        search.addEventListener('input', () => {
          clearTimeout(typing);
          typing = setTimeout(() => { filter.q = search.value.trim(); ctx.redraw('bugs'); }, TYPE_PAUSE);
        });
      }
    }
    const box = $('bgList');
    if (!list || !list.bugs) {
      $('bgChips').hidden = true;
      morph(box, list && list.error
        ? html`<div class="ds-alert ds-alert--error" role="alert" data-ds-key="error">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${list.error}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-bg-retry>Try again</button></div>`
        : html`<div class="ds-loading" role="status" data-ds-key="loading"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the bugs</div>`);
      return;
    }
    $('bgChips').hidden = false;
    const all = list.bugs;
    const words = filter.q.toLowerCase().split(/\s+/).filter(Boolean);
    const found = words.length ? all.filter((b) => { const t = haystack(b); return words.every((w) => t.includes(w)); }) : all;
    morph($('bgChips'), html`<legend class="ds-sr">Show the bugs that are</legend>${CHIPS.map((c) => {
      const n = found.filter((b) => chipMatch(b, c.id)).length;
      const tone = c.id === 'open' && n ? 'ds-badge ds-badge--warning' : 'ds-badge ds-badge--neutral';
      return html`<label class="ds-chip" data-ds-key="${c.id}"><input class="ds-chip__input" type="radio" name="bgChip" value="${c.id}"${c.id === filter.chip ? raw(' checked') : ''}><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${c.label}</span> <span class="${tone}">${n}</span></span></label>`;
    })}`);
    for (const input of $('bgChips').querySelectorAll('input')) input.checked = input.value === filter.chip;

    const shown = sorted(found.filter((b) => chipMatch(b, filter.chip)));
    if (!shown.length) {
      const [title, text] = words.length ? ['No bug matches', 'Nothing ' + (filter.chip === 'all' ? '' : 'marked ' + CHIPS.find((c) => c.id === filter.chip).label + ' ') + 'has “' + filter.q + '” in it.'] : (all.length ? EMPTY[filter.chip] : EMPTY.all);
      const done = !words.length && filter.chip === 'open' && all.length;
      morph(box, html`<section class="ds-card" data-ds-key="empty" aria-label="${title}"><div class="${'ds-empty' + (done ? ' ds-empty--done' : '')}">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon(words.length ? 'search' : done ? 'checkCircle' : 'flag', 24))}</span>
        <p class="ds-empty__title">${title}</p>
        <p class="ds-empty__text">${text}</p>
        ${words.length ? html`<button class="ds-btn ds-btn--tonal" type="button" data-bg-clear>Clear the search</button>`
          : !all.length && !state.runs.length ? html`<button class="ds-btn ds-btn--tonal" type="button" data-bg-dialog>${raw(icon('play', 16))}Run tests</button>`
            : all.length && filter.chip !== 'all' ? html`<button class="ds-btn ds-btn--tonal" type="button" data-bg-chip="all">${'Show all ' + V.plural(all.length, 'bug')}</button>` : ''}
      </div></section>`);
      return;
    }
    const phone = PHONE.matches;
    const label = CHIPS.find((c) => c.id === filter.chip).label;
    const caption = (filter.chip === 'all' ? 'Every bug' : label + ' bugs') + ', the worst first, then the last seen first';
    morph(box, html`<section class="ds-card" data-ds-key="bugs" aria-labelledby="bgTitle">
      <div class="ds-card__row">
        <h2 class="ds-card__title" id="bgTitle">${filter.chip === 'all' ? 'Every bug' : label + ' bugs'}</h2>
        <span class="ds-meta">${shown.length === found.length && !words.length ? V.plural(shown.length, 'bug') : shown.length + ' of ' + V.plural(all.length, 'bug')}</span>
      </div>
      ${phone ? html`<ul class="ds-list ds-list--interactive" aria-label="${caption}">${shown.map(bugItem)}</ul>`
        : html`<div class="ds-table-wrap"><table class="ds-table ds-table--interactive">
          <caption class="ds-sr">${caption}</caption>
          <thead><tr>
            <th scope="col">Severity</th>
            <th scope="col">Bug</th>
            <th scope="col">Status</th>
            <th scope="col">Seen</th>
            <th scope="col" class="ds-num ds-col--secondary">Cells</th>
            <th scope="col" class="ds-col--secondary"><span class="ds-sr">GitHub issue</span></th>
          </tr></thead>
          <tbody>${shown.map(bugRow)}</tbody>
        </table></div>`}
    </section>`);
  }

  const bugHref = (id) => V.paths.page({ repo: state.repo, view: 'bugs', bug: id });
  const runHref = (id) => V.paths.page({ repo: state.repo, view: 'runs', run: id });

  function issueLink(b) {
    const n = V.issueNumber(b.issue);
    const words = n ? 'GitHub issue #' + n + ' of ' + b.id : 'The GitHub issue of ' + b.id;
    return html`<a class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" href="${b.issue}" target="_blank" rel="noopener noreferrer" aria-label="${words + ' (opens GitHub)'}" data-tip="${n ? 'Issue #' + n + ' on GitHub' : 'Its issue on GitHub'}">${raw(icon('external', 18))}</a>`;
  }

  function bugRow(b) {
    return html`<tr data-ds-key="${b.id}">
      <td>${V.severityChip(b.severity)}</td>
      <td class="ds-col--text"><a class="ds-link-btn ds-table__row-link" href="${bugHref(b.id)}"><span class="so-bugs__id">${b.id}</span> ${b.title}</a>
        ${b.note ? html`<small class="so-tests__sub ds-meta so-bugs__note">${raw(icon('pencil', 16))}<span>${b.note}</span></small>` : ''}</td>
      <td>${V.bugStatusChip(b)}</td>
      <td><span class="so-tests-nowrap">${V.plural(b.seen || (b.runs || []).length, 'run')}</span><small class="so-tests__sub ds-meta">${V.seenWords(b) + (b.status === 'fixed' && b.fixed_number ? ' · fixed in Run ' + b.fixed_number : '')}</small></td>
      <td class="ds-num ds-col--secondary">${String((b.cells || []).length)}</td>
      <td class="ds-col--secondary ds-table__actions">${b.issue ? issueLink(b) : ''}</td>
    </tr>`;
  }

  function bugItem(b) {
    const n = V.issueNumber(b.issue);
    const meta = [
      'Seen in ' + V.plural(b.seen || (b.runs || []).length, 'run') + (V.seenWords(b) ? ', ' + V.seenWords(b) : ''),
      b.status === 'fixed' && b.fixed_number ? 'fixed in Run ' + b.fixed_number : '',
      V.plural((b.cells || []).length, 'cell'),
      b.issue ? (n ? 'issue #' + n : 'has an issue') : '',
    ].filter(Boolean).join(' · ');
    return html`<li class="ds-list__item" data-ds-key="${b.id}">
      <span class="ds-list__text">
        <span class="ds-list__title"><a class="ds-list__action" href="${bugHref(b.id)}">${b.id + ' · ' + b.title}</a></span>
        <span class="ds-list__meta">${meta}</span>
        <span class="so-bugs__chips">${V.severityChip(b.severity)}${V.bugStatusChip(b)}</span>
      </span>
    </li>`;
  }

  /* ----------------------------------------------------------- bug view */

  function renderBug(panel, id) {
    if (drawn !== id) {
      drawn = id;
      morph(panel, html`<div class="so-tests__stack" data-ds-key="${'bug:' + id}" data-bugview>
        <div id="bvHead" class="so-tests__stack"></div>
        <div id="bvBody" class="so-tests__stack"></div>
      </div>`);
      $('main').scrollTo({ top: 0 });
    }
    loadGitHub();
    const e = details.get(id);
    if (!e || ((!e.data || e.stale) && !e.loading && !e.error)) loadBug(id);
    const data = e && e.data;
    if (!data) {
      const known = bugOf(id);
      morph($('bvHead'), html`${crumbs(id)}${known ? html`<h2 class="ds-page-head__title" tabindex="-1">${known.title}</h2>` : ''}`);
      const gone = e && e.error === 'not found';
      morph($('bvBody'), e && e.error
        ? (gone ? html`<section class="ds-card" aria-label="This bug is gone"><div class="ds-empty">
            <span class="ds-empty__disc" aria-hidden="true">${raw(icon('flag', 24))}</span>
            <p class="ds-empty__title">${'There is no ' + id}</p>
            <p class="ds-empty__text">It went with the run that was its only report, or it was never on this repository’s list.</p>
            <a class="ds-btn ds-btn--tonal" href="${V.paths.page({ repo: state.repo, view: 'bugs' })}">Show the bugs</a>
          </div></section>`
          : html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${e.error}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-bv-retry>Try again</button></div>`)
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the bug</div>`);
      return;
    }
    const { bug } = data;
    morph($('bvHead'), head(bug, data));
    morph($('bvBody'), html`
      ${bug.description ? html`<section class="ds-card" data-ds-key="about" aria-labelledby="bvAboutTitle">
        <h3 class="ds-card__title" id="bvAboutTitle">What the analyst found</h3>
        <div class="ds-prose ds-prose--dense so-break">${markdown(V.stripHeading(bug.description))}</div>
      </section>` : ''}
      ${cellsCard(bug, data.cells || [])}
      <div class="ds-grid-2" data-ds-key="grid">
        ${triageCard(bug)}
        <div class="so-tests__stack">
          ${seenCard(bug, data.runs || [])}
          ${historyCard(bug)}
        </div>
      </div>`);
    // The checked state is the property, which a patch of the markup does not carry.
    for (const input of $('bvBody').querySelectorAll('input[name="bvStatus"]')) input.checked = input.value === bug.status;
    const sev = $('bvSeverity');
    if (sev) sev.value = bug.severity;
    noteState(bug);
  }

  function crumbs(id) {
    return html`<nav class="ds-breadcrumbs" aria-label="Breadcrumb"><ol>
      <li><a href="${V.paths.page({ repo: state.repo, view: 'bugs' })}">Bugs</a></li>
      <li><a href="${bugHref(id)}" aria-current="page">${id}</a></li>
    </ol></nav>`;
  }

  function githubAction(bug) {
    if (bug.issue) {
      const n = V.issueNumber(bug.issue);
      return html`<a class="ds-btn" href="${bug.issue}" target="_blank" rel="noopener noreferrer" data-tip="Opens GitHub">${raw(icon('external', 16))}${n ? 'Open issue #' + n : 'Open its issue'}</a>`;
    }
    const st = github && github.repo === state.repo ? github.status : null;
    if (st && st.available) {
      return html`<button class="ds-btn" type="button" id="bvIssue" aria-haspopup="dialog" aria-expanded="false" aria-controls="issueDialog">${raw(icon('link', 16))}Create GitHub issue</button>`;
    }
    const why = st ? 'No GitHub issue can be made: ' + (st.reason || 'GitHub is not reachable from this server') + '.' : 'Checking whether this repository is on GitHub…';
    return html`<button class="ds-btn" type="button" id="bvIssue" aria-disabled="true" aria-describedby="bvIssueWhy" data-ds-guard="off" data-tip="${why}" data-why="${why}">${raw(icon('link', 16))}Create GitHub issue</button>`;
  }

  // issueWhy says under the head why no issue can be made, in words a
  // finger and an eye find without a tooltip.
  function issueWhy(bug) {
    const st = github && github.repo === state.repo ? github.status : null;
    if (bug.issue || !st || st.available) return '';
    return html`<p class="ds-meta so-bugs__why" id="bvIssueWhy">${raw(icon('info', 16))}<span>${'No GitHub issue: ' + (st.reason || 'GitHub is not reachable from this server') + '.'}</span></p>`;
  }

  function head(bug, data) {
    const lead = [
      bug.id,
      'seen in ' + V.plural(bug.seen || (bug.runs || []).length, 'run') + (V.seenWords(bug) ? ', ' + V.seenWords(bug) : ''),
      bug.status === 'fixed' && bug.fixed_number ? 'fixed in Run ' + bug.fixed_number : '',
      V.plural((bug.cells || []).length, 'cell'),
    ].filter(Boolean).join(' · ');
    const cells = (data.cells || []).length || (bug.cells || []).length;
    return html`<header class="ds-page-head so-tests-record">
      ${crumbs(bug.id)}
      <div class="ds-page-head__row">
        <div class="ds-page-head__text">
          <h2 class="ds-page-head__title" tabindex="-1">${bug.title}</h2>
          <p class="ds-page-head__lead">${lead}</p>
          <div class="ds-page-head__status">
            ${V.severityChip(bug.severity)}
            ${V.bugStatusChip(bug)}
            ${bug.severity_by_hand ? V.chip({ word: 'Severity set by you', glyph: 'user' }) : ''}
          </div>
        </div>
        <div class="ds-page-head__actions">
          ${githubAction(bug)}
          ${cells ? html`<button class="ds-btn" type="button" data-bv-rerun>${raw(icon('sync', 16))}${cells === 1 ? 'Run its cell again' : 'Run its cells again'}</button>` : ''}
          <button class="ds-btn ds-btn--primary" type="button" id="bvFix" aria-haspopup="dialog" aria-expanded="false" aria-controls="chatDialog">${raw(icon('chat', 16))}Fix in a chat</button>
        </div>
      </div>
      ${issueWhy(bug)}
    </header>`;
  }

  /* -------------------------------------------------------------- cells */

  function cellsCard(bug, rows) {
    const missing = (bug.cells || []).filter((c) => !rows.some((r) => r.cell_id === c));
    return html`<section class="ds-card" data-ds-key="cells" aria-labelledby="bvCellsTitle">
      <div class="ds-card__row">
        <h3 class="ds-card__title" id="bvCellsTitle">Its cells</h3>
        <span class="ds-meta">${rows.length ? 'as in ' + lastRunName(bug) + ', the run that last reported it' : ''}</span>
      </div>
      ${rows.length ? html`<div class="so-tests-live" role="list" aria-label="${'Cells of ' + bug.id}">${rows.map((r) => cellCard(bug, r))}</div>`
        : html`<p class="ds-muted">${(bug.cells || []).length ? 'Its cells are in no run that is still kept.' : 'The analyst named no cell.'}</p>`}
      ${missing.length && rows.length ? html`<p class="ds-meta so-break">${'Not in a run that is still kept: ' + missing.join(', ')}</p>` : ''}
    </section>`;
  }

  function lastRunName(bug) {
    const run = ctx.runOf(bug.last_run);
    return run ? V.runName(run, { name: false }) : bug.last_number ? 'Run ' + bug.last_number : 'its last run';
  }

  // rowRunName names the run a cell of the bug is shown from.
  function rowRunName(bug, r) {
    if (!r.run || r.run === bug.last_run) return lastRunName(bug);
    const run = ctx.runOf(r.run);
    return run ? V.runName(run, { name: false }) : 'an earlier run';
  }

  function cellCard(bug, r) {
    const run = r.run || bug.last_run;
    const found = steps.get(r.id);
    const first = found && found[0];
    const verdict = r.final ? V.verdictWord(r.final) : V.attemptWord(r.state);
    const words = [r.title, V.dimsWords(r.dims) || r.cell, verdict + ' in ' + rowRunName(bug, r), first ? 'step ' + first.n + ': ' + first.observed : ''].filter(Boolean).join(', ');
    return html`<div class="ds-card ds-card--filled ds-card--interactive so-tests-live__card" role="listitem" data-ds-key="${'c' + r.id}">
      <div class="so-tests-live__shot">${r.shot
        ? html`<img src="${V.paths.file(run, r.id, r.shot)}" alt="${'Screenshot of ' + (first ? 'its failing step ' + first.n : 'the cell')}" loading="lazy" decoding="async">`
        : html`<span class="so-tests-live__none" aria-hidden="true">${raw(icon('image', 24))}</span>`}</div>
      <h4 class="ds-card__title"><button class="ds-card__action" type="button" data-bv-cell="${r.id}" aria-label="${words}">${r.title}</button></h4>
      <p class="ds-meta">${V.dimsWords(r.dims) || r.cell}</p>
      <p class="so-tests__verdict">${V.cellChip(r, { tip: false })}${V.changeMark(r)}</p>
      ${first ? html`<p class="ds-meta so-break so-bugs__step">${'Step ' + first.n + ': ' + (first.observed || 'did not do what it should.')}</p>` : ''}
    </div>`;
  }

  /* ------------------------------------------------------------- triage */

  function triageCard(bug) {
    const draft = notes.has(bug.id) ? notes.get(bug.id) : (bug.note || '');
    return html`<section class="ds-card" data-ds-key="triage" aria-labelledby="bvTriageTitle">
      <h3 class="ds-card__title" id="bvTriageTitle">Triage</h3>
      <p class="ds-muted">Every change is saved by itself: a choice at once (its snackbar can undo it), the note when you leave the field.</p>
      <div class="ds-form so-tests__stack">
        <fieldset class="ds-field">
          <legend class="ds-field__label">Status</legend>
          <div class="ds-radios" role="radiogroup" aria-label="Status">${V.BUG_STATUSES.map((s) => html`<label class="ds-radio" data-ds-key="${s}"><input type="radio" name="bvStatus" value="${s}"${s === bug.status ? raw(' checked') : ''}><span><strong>${s === 'open' && bug.status === 'open' && bug.regressed ? 'Open · came back' : TRIAGE_TITLE[s]}</strong><small>${s === 'open' && bug.status === 'open' && bug.regressed ? 'It was fixed, then a later run reported it again. ' + V.BUG_STATUS[s].hint : V.BUG_STATUS[s].hint}</small></span></label>`)}</div>
        </fieldset>
        <div class="ds-field">
          <label class="ds-field__label" for="bvSeverity">Severity</label>
          <select class="ds-input" id="bvSeverity" aria-describedby="bvSeverityHint">${Object.entries(V.SEVERITY).map(([k, s]) => html`<option value="${k}"${k === bug.severity ? raw(' selected') : ''}>${s.word}</option>`)}</select>
          <small class="ds-field__hint" id="bvSeverityHint">${bug.severity_by_hand ? html`${raw(icon('user', 16))}Set by you: the analyst’s later reports keep it.` : 'The analyst’s. One you set is kept over its later reports.'}</small>
        </div>
        <div class="ds-field">
          <label class="ds-field__label" for="bvNote">Note <span class="ds-field__optional">(optional)</span></label>
          <textarea class="ds-input" id="bvNote" maxlength="${NOTE_MAX}" rows="3" aria-describedby="bvNoteHint" placeholder="Why it is known, who looks at it, what was tried">${draft}</textarea>
          <div class="ds-field__foot">
            <small class="ds-field__hint" id="bvNoteHint">Saved when you leave the field. The analyst reads it with the bug list on the next run.</small>
            <span class="ds-field__counter" aria-hidden="true" id="bvNoteCount">${draft.length + ' / ' + NOTE_MAX}</span>
          </div>
          <p class="ds-meta so-bugs__saved" id="bvNoteState" role="status"></p>
        </div>
      </div>
    </section>`;
  }

  // noteSaved is what the note's quiet line says: bug id -> 'saving' |
  // 'saved' | 'failed' (absent: nothing to say).
  const noteSaved = new Map();

  // noteState says how the note stands under it: an edit not saved yet,
  // saving, saved; and its length.
  function noteState(bug) {
    const field = $('bvNote');
    if (!field) return;
    const edited = notes.has(bug.id) && notes.get(bug.id).trim() !== (bug.note || '');
    const st = noteSaved.get(bug.id);
    const words = st === 'saving' ? 'Saving…' : edited ? (st === 'failed' ? 'Not saved: it is saved when you leave the field' : 'Not saved yet: it is saved when you leave the field')
      : st === 'saved' ? 'Saved' : '';
    const line = $('bvNoteState');
    if (line.textContent !== words) line.textContent = words;
    const count = field.value.length + ' / ' + NOTE_MAX;
    if ($('bvNoteCount').textContent !== count) $('bvNoteCount').textContent = count;
  }

  // dropUndo puts away the snackbar of the latest triage: a dialog opened
  // over the bug is the next step, and the snackbar would stand on its actions.
  function dropUndo() {
    if (lastUndo) lastUndo.close();
    lastUndo = null;
  }

  /**
   * patch triages a bug (PATCH …/bugs/{id}): the change shows at once, the
   * server's answer replaces it, and a failure puts it back. `undo` is the
   * patch that takes it back, offered in the snackbar.
   */
  async function patch(id, change, { done, undo } = {}) {
    const before = bugOf(id);
    if (!before) return false;
    setBug({ ...before, ...change });
    ctx.redraw('bugs');
    try {
      const res = await api(V.paths.bug(state.repo, id), { method: 'PATCH', body: change });
      setBug(res.bug);
      ctx.redraw('bugs');
      ctx.refreshSoon();
      if (done) {
        // One Undo at a time: the snackbars of earlier changes would pile up.
        if (lastUndo) lastUndo.close();
        const shown = toast(done, 'success', undo ? { action: { label: 'Undo', run: () => patch(id, undo, { done: 'Undone: ' + id + ' is as it was.' }) } } : {});
        lastUndo = undo ? shown : null;
      }
      return true;
    } catch (err) {
      setBug(before);
      ctx.redraw('bugs');
      toast(errorMessage(err), 'error');
      return false;
    }
  }

  // A choice made with the arrow keys walks through the others first: what
  // is chosen shows at once, and is saved once the keys rest a moment.
  const choosing = new Map(); // 'id:field' -> { from, timer }

  function choose(id, field, value, words) {
    const bug = bugOf(id);
    if (!bug) return;
    const key = id + ':' + field;
    const was = choosing.get(key);
    const from = was ? was.from : bug[field];
    if (was) clearTimeout(was.timer);
    setBug({ ...bug, [field]: value });
    ctx.redraw('bugs');
    if (value === from) { choosing.delete(key); return; }
    choosing.set(key, {
      from,
      timer: setTimeout(() => {
        choosing.delete(key);
        const now = bugOf(id);
        // The server's change is the one sent; the bug shows the old value
        // for the moment it takes, so that a failure puts back the right one.
        if (now) setBug({ ...now, [field]: from });
        patch(id, { [field]: value }, { done: words(value), undo: { [field]: from } });
      }, CHOICE_PAUSE),
    });
  }

  function setStatus(id, status) {
    choose(id, 'status', status, (v) => id + ' is ' + (v === 'not_a_bug' ? 'marked Not a bug' : 'now ' + V.bugStatusWord(v)) + '.');
  }

  function setSeverity(id, severity) {
    choose(id, 'severity', severity, (v) => id + ' is ' + V.severityWord(v) + ' now.');
  }

  // saveNote saves the note as written, when it differs from the saved one.
  async function saveNote(id) {
    const bug = bugOf(id);
    if (!bug || !notes.has(id)) return;
    const note = notes.get(id).trim();
    if (note === (bug.note || '')) { notes.delete(id); return; }
    noteSaved.set(id, 'saving');
    noteState(bug);
    const ok = await patch(id, { note });
    noteSaved.set(id, ok ? 'saved' : 'failed');
    if (ok && notes.has(id) && notes.get(id).trim() === note) notes.delete(id);
    ctx.redraw('bugs');
  }

  /* ---------------------------------------------------- runs and history */

  function seenCard(bug, runs) {
    return html`<section class="ds-card" data-ds-key="seen" aria-labelledby="bvSeenTitle">
      <div class="ds-card__row">
        <h3 class="ds-card__title" id="bvSeenTitle">Seen in</h3>
        <span class="ds-meta">${V.plural(runs.length, 'run')}</span>
      </div>
      ${runs.length ? html`<ul class="ds-list ds-list--interactive" aria-label="${'Runs that reported ' + bug.id + ', the newest first'}">${runs.map((r) => {
        const known = ctx.runOf(r.id);
        const marks = [r.id === bug.first_run ? 'first seen' : '', r.id === bug.last_run ? 'last seen' : ''].filter(Boolean).join(', ');
        return html`<li class="ds-list__item" data-ds-key="${r.id}">
          <span class="ds-list__text">
            <span class="ds-list__title"><a class="ds-list__action" href="${runHref(r.id)}">${V.runName({ ...r, n: known ? known.n : r.number })}</a></span>
            <span class="ds-list__meta">${[V.when(r.created_at), marks].filter(Boolean).join(' · ')}</span>
          </span>
          ${raw(icon('chevronRight', 20, 'ds-list__chevron'))}
        </li>`;
      })}</ul>` : html`<p class="ds-muted">The runs that reported it are no longer kept.</p>`}
    </section>`;
  }

  // The words of an event (BugEvent.what) as a line: "reported again in
  // run 5" → "Reported again in Run 5".
  function eventWords(what) {
    const text = String(what || '').replace(/\bnot_a_bug\b/g, 'not a bug').replace(/\brun (\d+)/g, 'Run $1');
    return text.charAt(0).toUpperCase() + text.slice(1);
  }

  function eventGlyph(what) {
    if (/by hand|note/.test(what)) return 'user';
    if (/issue/.test(what)) return 'link';
    if (/^fixed/.test(what)) return 'checkCircle';
    if (/^reopened/.test(what)) return 'sync';
    if (/^no longer/.test(what)) return 'minimize';
    return 'flag';
  }

  // events is the history to show, newest first: a run that reopened a bug
  // also reported it again at the same moment, which says the same twice.
  function events(bug) {
    const list = (bug.events || []).slice();
    const out = list.filter((ev, i) => !(/^reported again/.test(ev.what) && list.some((o, j) => j !== i && o.at === ev.at && o.run === ev.run && /^reopened/.test(o.what))));
    return out.reverse();
  }

  const dayFormat = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' });
  const timeFormat = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });

  function historyCard(bug) {
    const evs = events(bug);
    let day = '';
    const items = [];
    evs.forEach((ev, i) => {
      const d = dayFormat.format(new Date(ev.at));
      if (d !== day) {
        day = d;
        items.push(html`<li class="ds-timeline__group" role="presentation" data-ds-key="${'d' + d}"><h4 class="ds-timeline__day">${d}</h4></li>`);
      }
      const url = /^(GitHub issue created|issue set): (\S+)$/.exec(ev.what);
      const what = url ? (url[1] === 'issue set' ? 'Issue set by hand' : 'GitHub issue created') : eventWords(ev.what);
      const run = ev.run ? ctx.runOf(ev.run) : null;
      items.push(html`<li class="ds-timeline__item ds-timeline__item--icon" data-ds-key="${'e' + ev.at + '-' + i}">
        <span class="ds-timeline__marker" aria-hidden="true">${raw(icon(eventGlyph(ev.what), 16))}</span>
        <div class="ds-timeline__head"><p class="ds-timeline__title">${what}</p></div>
        <p class="ds-timeline__meta"><time datetime="${new Date(ev.at).toISOString()}">${timeFormat.format(new Date(ev.at))}</time>${/by hand|note/.test(ev.what) ? ' · you' : ' · Socrates'}${run ? html` · <a href="${runHref(ev.run)}">${V.runName(run, { name: false })}</a>` : ''}</p>
        ${url ? html`<p class="ds-timeline__text"><a href="${url[2]}" target="_blank" rel="noopener noreferrer" class="so-break">${url[2]}</a></p>` : ''}
      </li>`);
    });
    return html`<section class="ds-card" data-ds-key="history" aria-labelledby="bvHistTitle">
      <h3 class="ds-card__title" id="bvHistTitle">History</h3>
      ${evs.length ? html`<ol class="ds-timeline" role="list" aria-label="${'History of ' + bug.id + ', the newest first'}">${items}</ol>` : html`<p class="ds-muted">Nothing happened to it yet.</p>`}
    </section>`;
  }

  /* ------------------------------------------------------- GitHub issue */

  const issue = { bug: '', opener: null };
  const issueDialog = $('issueDialog');
  let issuePrefill = { title: '', body: '' };

  const issueDirty = () => !!$('issueTitleField') && ($('issueTitleField').value !== issuePrefill.title || $('issueBodyField').value !== issuePrefill.body);
  V.guardClose(issueDialog, {
    dirty: issueDirty,
    question: () => ({ title: 'Discard the issue?', body: 'What you changed in its title and text is lost.', confirmLabel: 'Discard', cancelLabel: 'Keep editing' }),
  });

  // issueBody is the issue's text: the bug, its cells with their failing
  // steps, where it was seen, and the way back to it on this Socrates.
  async function issueBody(bug, data) {
    const rows = data.cells || [];
    const views = await Promise.all(rows.slice(0, 10).map((r) => V.attemptView(r.run || bug.last_run, r.id).catch(() => null)));
    const runs = (bug.first_number && bug.last_number && bug.first_number !== bug.last_number)
      ? 'Run ' + bug.first_number + '–' + bug.last_number : 'Run ' + (bug.last_number || bug.first_number || '?');
    const lines = [];
    lines.push('**Severity:** ' + V.severityWord(bug.severity) + ' · **Seen in:** ' + runs + ' (' + V.plural(bug.seen || (bug.runs || []).length, 'run') + ')'
      + (bug.regressed ? ' · came back after it was fixed' : ''));
    lines.push('');
    if (bug.description) lines.push(V.stripHeading(bug.description).trim(), '');
    if (rows.length) {
      lines.push('### Cells', '');
      rows.forEach((r, i) => {
        const verdict = r.final ? V.verdictWord(r.final) : V.attemptWord(r.state);
        lines.push('- **' + r.title + '**' + (V.dimsWords(r.dims) ? ' · ' + V.dimsWords(r.dims) : '') + ' (`' + r.cell_id + '`): ' + verdict);
        const found = i < views.length ? V.problemSteps(views[i]) : [];
        for (const s of found) {
          lines.push('  - Step ' + s.n + ': ' + s.do, '    - Expected: ' + s.expect, '    - Observed: ' + (s.observed || '(nothing said)'));
        }
      });
      if (rows.length > views.length) lines.push('- … and ' + (rows.length - views.length) + ' more');
      lines.push('');
    }
    if (bug.note) lines.push('**Note:** ' + bug.note, '');
    lines.push('---', 'Found by the test runs of Socrates: [' + bug.id + ' on Socrates](' + location.origin + '/tests' + bugHref(bug.id) + ')');
    return lines.join('\n');
  }

  async function openIssue(id, opener) {
    const d = details.get(id);
    const data = d && d.data;
    const st = github && github.status;
    if (!data || !st || !st.available) return;
    issue.bug = id;
    issue.opener = opener;
    dropUndo();
    const bug = data.bug;
    issuePrefill = { title: bug.id + ': ' + bug.title, body: '' };
    morph($('issueBody'), html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Writing the issue</div>`);
    $('issueCreate').setAttribute('aria-disabled', 'true');
    DS.overlay.open(issueDialog, { opener });
    let body = '';
    try {
      body = await issueBody(bug, data);
    } catch (err) {
      body = '';
      toast(errorMessage(err), 'error');
    }
    if (issue.bug !== id) return;
    issuePrefill = { title: issuePrefill.title, body };
    morph($('issueBody'), html`<form class="ds-form so-tests__stack" novalidate id="issueForm">
      <p class="ds-meta so-bugs__issue-repo">${raw(icon('lock', 16))}<span>${'In '}<strong>${st.repo}</strong>${' on GitHub, made by the GitHub CLI signed in on this server.'}</span></p>
      <div class="ds-field">
        <label class="ds-field__label" for="issueTitleField">Title</label>
        <input class="ds-input" id="issueTitleField" maxlength="256" autocomplete="off" required>
      </div>
      <div class="ds-field">
        <label class="ds-field__label" for="issueBodyField">Text</label>
        <textarea class="ds-input so-tests-message" id="issueBodyField" rows="14" spellcheck="true" aria-describedby="issueBodyHint"></textarea>
        <small class="ds-field__hint" id="issueBodyHint">Markdown. It ends with a link back to this bug on this Socrates.</small>
      </div>
      <div id="issueError"></div>
    </form>`);
    $('issueTitleField').value = issuePrefill.title;
    $('issueBodyField').value = issuePrefill.body;
    $('issueCreate').setAttribute('aria-disabled', 'false');
    $('issueTitleField').focus();
  }

  async function createIssue() {
    const id = issue.bug;
    const button = $('issueCreate');
    if (button.getAttribute('aria-disabled') === 'true' || issueDialog.hasAttribute('data-ds-busy')) return;
    const title = $('issueTitleField').value.trim();
    const body = $('issueBodyField').value;
    const err = (text) => morph($('issueError'), text ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${text}</span></div>` : '');
    if (!title) {
      $('issueTitleField').setAttribute('aria-invalid', 'true');
      err('The issue needs a title.');
      $('issueTitleField').focus();
      return;
    }
    $('issueTitleField').removeAttribute('aria-invalid');
    err('');
    issueDialog.setAttribute('data-ds-busy', '');
    const cancel = $('issueCancel');
    cancel.setAttribute('aria-disabled', 'true');
    cancel.setAttribute('data-tip', 'Wait until GitHub answers');
    let url = '';
    let had = false;
    try {
      await M.busy(button, (async () => {
        const res = await api(V.paths.bugIssue(state.repo, id), { method: 'POST', body: { title, body }, timeout: 90000, attempts: 1 });
        url = res.url;
      })(), { done: false });
    } catch (e) {
      // 409: the bug has an issue already (made elsewhere meanwhile), whose
      // link the answer ends with; or one is being made.
      const existing = e instanceof HttpError && e.status === 409 && /(https:\/\/\S+)$/.exec(errorMessage(e));
      if (!existing) {
        err(errorMessage(e));
        return;
      }
      url = existing[1];
      had = true;
    } finally {
      issueDialog.removeAttribute('data-ds-busy');
      cancel.removeAttribute('aria-disabled');
      cancel.removeAttribute('data-tip');
    }
    const bug = bugOf(id);
    if (bug) setBug({ ...bug, issue: url });
    issuePrefill = { title, body };
    await DS.overlay.close(issueDialog, { reason: 'confirm' });
    const n = V.issueNumber(url);
    toast(had ? id + ' has ' + (n ? 'issue #' + n : 'an issue') + ' already: no second one was made.' : (n ? 'Issue #' + n : 'The issue') + ' is on GitHub.',
      had ? 'info' : 'success', { action: { label: 'Open', run: () => window.open(url, '_blank', 'noopener') } });
    const d = details.get(id);
    if (d) d.stale = true;
    loadBug(id);
    ctx.redraw('bugs');
  }

  $('issueCreate').addEventListener('click', createIssue);
  issueDialog.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.isComposing && event.target.id === 'issueTitleField') {
      event.preventDefault();
      createIssue();
    }
  });

  /* ------------------------------------------------------------- wiring */

  function wire(panel) {
    panel.dataset.wired = '1';
    panel.addEventListener('click', (event) => {
      const t = event.target;
      const id = state.route.bug;
      const cellBtn = t.closest('[data-bv-cell]');
      if (cellBtn) {
        const d = details.get(id);
        const last = d && d.data ? d.data.bug.last_run : '';
        const list = d && d.data ? (d.data.cells || []).map((r) => ({ attempt: r.id, run: r.run || last })) : [];
        const here = list.find((x) => x.attempt === cellBtn.dataset.bvCell);
        ctx.openCell(cellBtn.dataset.bvCell, here ? here.run : last, list);
        return;
      }
      if (t.closest('#bvFix')) {
        dropUndo();
        const d = details.get(id);
        ctx.openChat({ kind: 'fix-bug', subject: { repo: state.repo, bug: (d && d.data && d.data.bug) || id, detail: d && d.data } }, t.closest('#bvFix'));
        return;
      }
      const iss = t.closest('#bvIssue');
      if (iss) {
        if (iss.getAttribute('aria-disabled') === 'true') toast(iss.dataset.why || 'No GitHub issue can be made yet.', 'info');
        else openIssue(id, iss);
        return;
      }
      if (t.closest('[data-bv-rerun]')) {
        const bug = bugOf(id);
        if (bug) ctx.startRun({ cells: bug.cells }, t.closest('[data-bv-rerun]'));
        return;
      }
      if (t.closest('[data-bv-retry]')) { details.delete(id); ctx.redraw('bugs'); return; }
      if (t.closest('[data-bg-retry]')) { list = null; ctx.redraw('bugs'); return; }
      if (t.closest('[data-bg-dialog]')) { ctx.openRunDialog(undefined, t.closest('[data-bg-dialog]')); return; }
      if (t.closest('[data-bg-clear]')) {
        filter.q = '';
        $('bgSearch').value = '';
        ctx.redraw('bugs');
        $('bgSearch').focus();
        return;
      }
      const chip = t.closest('[data-bg-chip]');
      if (chip) {
        filter.chip = chip.dataset.bgChip;
        ctx.redraw('bugs');
      }
    });
    panel.addEventListener('change', (event) => {
      const t = event.target;
      if (t.name === 'bgChip' && t.checked) {
        filter.chip = t.value;
        ctx.redraw('bugs');
      } else if (t.name === 'bvStatus' && t.checked) {
        setStatus(state.route.bug, t.value);
      } else if (t.id === 'bvSeverity') {
        setSeverity(state.route.bug, t.value);
      }
    });
    panel.addEventListener('input', (event) => {
      if (event.target.id !== 'bvNote') return;
      notes.set(state.route.bug, event.target.value);
      noteSaved.delete(state.route.bug);
      const bug = bugOf(state.route.bug);
      if (bug) noteState(bug);
    });
    // Leaving the note saves it (a link or a tab taken from it leaves it first).
    panel.addEventListener('focusout', (event) => {
      if (event.target.id === 'bvNote') saveNote(state.route.bug);
    });
    panel.addEventListener('keydown', (event) => {
      // Ctrl+Enter in the note saves it at once.
      if (event.target.id === 'bvNote' && event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        saveNote(state.route.bug);
      }
    });
  }

  PHONE.addEventListener('change', () => ctx.redraw('bugs'));

  return {
    render,
    async refresh() {
      // A frame said the repository changed (a run ended, a bug was triaged
      // here or elsewhere): the list and the bug shown are read again.
      const id = state.route.bug;
      for (const [key, e] of details) if (key !== id) details.delete(key);
      const reads = [loadList()];
      if (id && details.has(id)) {
        details.get(id).stale = true;
        reads.push(loadBug(id));
      }
      await Promise.all(reads);
    },
    close() {
      drawn = '';
      dropUndo();
      if (DS.overlay.topmost() === issueDialog) DS.overlay.close(issueDialog);
    },
    repoChanged() {
      list = null;
      details.clear();
      steps.clear();
      notes.clear();
      github = null;
      drawn = '';
      filter.q = '';
      filter.chip = 'open';
    },
  };
}
