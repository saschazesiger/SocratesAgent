// The Bugs view of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", Bugs): the repository's bugs over every run.
//
// Stub: replaced by the full view.
// (Still to come: status chips, triage, the bug view, GitHub issues, Fix in
// a chat.)

import { api } from './api.js';
import { html, raw, icon, morph } from './ui.js';
import * as V from './testsview.js';

export function mount(ctx) {
  const { state } = ctx;
  let data = null; // { repo, bugs } or { repo, missing: true }

  async function load() {
    const repo = state.repo;
    // API: GET /api/tests/repos/{repo}/bugs (TEST-MODE.md "Bugs over runs"); asked for only from a
    // server that has the reworked page's routes.
    if (!state.v2) { data = { repo, missing: true }; return; }
    try {
      const d = await api(V.paths.repoBugs(repo), { attempts: 1 });
      data = { repo, bugs: d.bugs || [] };
    } catch {
      data = { repo, missing: true };
    }
    ctx.redraw('bugs');
  }

  function render(panel) {
    if (!data || data.repo !== state.repo) {
      data = { repo: state.repo, loading: true };
      load().then(() => ctx.redraw('bugs'));
    }
    if (data.loading) {
      morph(panel, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the bugs</div>`);
      return;
    }
    const bugs = (data.bugs || []).slice().sort((a, b) => V.sevRank(a.severity) - V.sevRank(b.severity));
    if (data.missing || !bugs.length) {
      morph(panel, html`<section class="ds-card"><div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('flag', 24))}</span>
        <p class="ds-empty__title">${data.missing ? 'Each run lists its own bugs' : 'No bug yet'}</p>
        <p class="ds-empty__text">${data.missing ? 'Open a run and its Bugs part: the analyst writes one bug per root cause there.' : 'Once a run is analysed, its bugs are tracked here over the runs.'}</p>
        ${data.missing && state.runs.length ? html`<button class="ds-btn ds-btn--tonal" type="button" data-go-view="runs">Show the runs</button>` : ''}
      </div></section>`);
      return;
    }
    morph(panel, html`<section class="ds-card" aria-labelledby="bugsTitle">
      <h2 class="ds-card__title" id="bugsTitle">Bugs</h2>
      <ul class="ds-list" aria-label="Bugs">${bugs.map((b) => html`<li class="ds-list__item" data-ds-key="${b.id}">
        <span class="ds-list__text">
          <span class="ds-list__title">${b.id + ' · ' + b.title}</span>
          <span class="ds-list__meta">${[b.status || 'open', V.plural((b.cells || []).length, 'cell')].join(' · ')}</span>
        </span>
        ${V.severityChip(b.severity)}
      </li>`)}</ul>
    </section>`);
  }

  return {
    render,
    refresh() { data = null; },
    repoChanged() { data = null; },
  };
}
