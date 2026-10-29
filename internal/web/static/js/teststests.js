// The Tests view of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", Tests): the catalog as it is in the repository now, per area its
// tests with their priority, and Run.
//
// Stub: replaced by the full view.
// (Still to come: the health of every test, the test view with its history
// grid, the tools and Check secrets.)

import { html, raw, icon, morph } from './ui.js';
import * as V from './testsview.js';

export function mount(ctx) {
  const { state } = ctx;

  function render(panel) {
    if (!panel.dataset.wired) {
      panel.dataset.wired = '1';
      panel.addEventListener('click', (event) => {
        const run = event.target.closest('[data-run-test]');
        if (run) ctx.openRunDialog({ tests: [run.dataset.runTest] }, run);
      });
    }
    const cat = state.catalog;
    if (!cat) {
      if (!state.catalogError) ctx.ensureCatalog().then(() => ctx.redraw('tests'));
      morph(panel, state.catalogError
        ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${state.catalogError}</span></div>`
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the catalog</div>`);
      return;
    }
    const problem = (p) => (p.file ? p.file + (p.line ? ':' + p.line : '') + ': ' : '') + p.message;
    morph(panel, html`${(cat.errors || []).map((p) => html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${problem(p)}</span></div>`)}
      ${(cat.warnings || []).map((p) => html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text so-break">${problem(p)}</span></div>`)}
      ${(cat.areas || []).map((area) => html`<section class="ds-card" data-ds-key="${'cat:' + area.id}" aria-labelledby="${'cat-' + area.id}">
        <div class="ds-card__row">
          <h2 class="ds-card__title" id="${'cat-' + area.id}">${area.title || area.id}</h2>
          <span class="ds-meta">${V.plural((area.tests || []).length, 'test')}</span>
        </div>
        <ul class="ds-list" aria-label="${'Tests of ' + (area.title || area.id)}">${(area.tests || []).map((t) => html`<li class="ds-list__item" data-ds-key="${t.id}">
          <span class="ds-list__text">
            <span class="ds-list__title">${t.title}</span>
            <span class="ds-list__meta">${[t.priority, V.plural((t.cells || []).length, 'cell'), ...(t.tags || []).map((x) => '#' + x)].filter(Boolean).join(' · ')}</span>
          </span>
          <span class="ds-list__actions"><button class="ds-btn ds-btn--sm" type="button" data-run-test="${t.id}" aria-label="${'Run “' + t.title + '”'}">${raw(icon('play', 16))}Run</button></span>
        </li>`)}</ul>
      </section>`)}`);
  }

  return { render };
}
