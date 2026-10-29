// The Tests page's forms and its catalog (docs/design/TEST-MODE.md, "The
// Tests page" and "Settings"): the repository's settings with a save bar,
// the Run sheet (what to run, the settings for this run, a name), and the
// catalog as a run would see it, with its tools' secrets checked by name.
//
// The three roles - cell agent, reviewer, analyst - each take a program, a
// model, an effort and the accounts of that program they may run on (none
// ticked is every account); among those Socrates picks for each session.

import { api, errorMessage, toast } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import { markdown } from './md.js';
import * as harnesses from './harnesses.js';
import * as accounts from './accounts.js';
import * as V from './testsview.js';
import { brief, labelOf } from './mcpview.js';

const $ = (id) => document.getElementById(id);

const ROLES = [
  { key: 'cell', label: 'Cell agent', hint: 'Works through the steps of each cell and judges them on their screenshots.' },
  { key: 'reviewer', label: 'Reviewer', hint: 'Looks again at the doubtful cells and corrects their verdicts.' },
  { key: 'analyst', label: 'Analyst', hint: 'Turns the findings into one bug per root cause and writes the conclusions.' },
];

export function mountForms(ctx) {
  const { state } = ctx;
  const checks = new Map(); // tool -> { busy, result }
  const openDetails = new Set(); // keys of the <details> the person opened

  // The <details> of a re-rendered part stay as the person left them.
  const remember = (event) => {
    const d = event.target;
    if (!(d instanceof HTMLDetailsElement) || !d.dataset.key) return;
    if (d.open) openDetails.add(d.dataset.key);
    else openDetails.delete(d.dataset.key);
  };
  document.addEventListener('toggle', remember, true);
  const isOpen = (key) => (openDetails.has(key) ? raw(' open') : '');

  // The catalogue of programs and the accounts are what the role pickers
  // offer; both are read once, then whenever the page draws a form.
  let ready = null;
  function prepare() {
    if (!ready) {
      ready = Promise.all([harnesses.load().catch(() => {}), accounts.load().catch(() => {})]);
    }
    return ready;
  }

  /* -------------------------------------------------------- role pickers */

  function programOptions(current) {
    const list = harnesses.list().filter((h) => harnesses.isChat(h.id) && h.id !== 'vscode');
    const out = list.map((h) => [h.id, h.label + (h.enabled && h.installed ? '' : ' (not available)')]);
    if (current && !out.some(([id]) => id === current)) out.push([current, harnesses.label(current) + ' (not available)']);
    return out;
  }

  function modelOptions(harness, current) {
    const out = harnesses.offered(harness).map((m) => [m.id, m.label || m.id]);
    if (current && !out.some(([id]) => id === current)) {
      const known = harnesses.modelOf(harness, current);
      out.unshift([current, known ? (known.label || known.id) : current]);
    }
    if (!out.length) out.push(['', 'Its default']);
    return out;
  }

  function effortOptions(harness, model, current) {
    const out = [['', 'Default'], ...harnesses.effortsFor(harness, model).map((e) => [e, harnesses.effortLabel(e)])];
    if (current && !out.some(([id]) => id === current)) out.push([current, harnesses.effortLabel(current)]);
    return out;
  }

  function options(list, value) {
    return list.map(([v, label]) => html`<option value="${v}"${v === value ? raw(' selected') : ''}>${label}</option>`);
  }

  function accountRows(prefix, role, harness, ticked) {
    const list = accounts.forAgent(harness);
    if (!list.length) {
      return html`<p class="ds-muted">${'No ' + harnesses.label(harness) + ' account yet: add one in Admin.'}</p>`;
    }
    return html`<div class="ds-check-list">${list.map((a) => {
      const words = [accounts.who(a), accounts.usable(a) ? '' : (a.state === 'error' ? 'sign-in failed' : 'not signed in'), accounts.renewal(a) ? 'at its limit, ' + accounts.renewal(a) : '']
        .filter(Boolean).join(' · ');
      return html`<label class="ds-check" data-ds-key="${a.id}"><input type="checkbox" data-account="${role}" value="${a.id}"${ticked.includes(a.id) ? raw(' checked') : ''}><span>${a.label} <span class="ds-meta">${words}</span></span></label>`;
    })}</div>
    <small class="ds-field__hint">${'None ticked: every ' + harnesses.label(harness) + ' account. Socrates takes the one with the most left for each session.'}</small>`;
  }

  // roleBody is a role's model, effort and accounts, which follow its program.
  function roleBody(prefix, role, r) {
    return html`<div class="ds-form-grid ds-form-grid--2">
        <div class="ds-field">
          <label class="ds-field__label" for="${prefix + role + 'Model'}">Model</label>
          <select class="ds-input" id="${prefix + role + 'Model'}" data-role-field="model" data-role="${role}">${options(modelOptions(r.harness, r.model), r.model || '')}</select>
        </div>
        <div class="ds-field">
          <label class="ds-field__label" for="${prefix + role + 'Effort'}">Effort</label>
          <select class="ds-input" id="${prefix + role + 'Effort'}" data-role-field="effort" data-role="${role}">${options(effortOptions(r.harness, r.model, r.effort), r.effort || '')}</select>
        </div>
      </div>
      <fieldset class="ds-field">
        <legend class="ds-field__label">Accounts</legend>
        ${accountRows(prefix, role, r.harness, r.accounts || [])}
      </fieldset>`;
  }

  function roleSection(prefix, spec, r) {
    return html`<section class="so-tests__role" data-role-box="${spec.key}" aria-labelledby="${prefix + spec.key + 'Title'}">
      <h3 class="ds-card__subtitle" id="${prefix + spec.key + 'Title'}">${spec.label}</h3>
      <p class="ds-muted">${spec.hint}</p>
      <div class="ds-field">
        <label class="ds-field__label" for="${prefix + spec.key + 'Harness'}">Program</label>
        <select class="ds-input" id="${prefix + spec.key + 'Harness'}" data-role-field="harness" data-role="${spec.key}">${options(programOptions(r.harness), r.harness)}</select>
      </div>
      <div class="so-tests__role" data-role-body="${spec.key}">${roleBody(prefix, spec.key, r)}</div>
    </section>`;
  }

  function numberField(prefix, name, label, value, { min, max, hint }) {
    return html`<div class="ds-field">
      <label class="ds-field__label" for="${prefix + name}">${label}</label>
      <input class="ds-input" type="number" inputmode="numeric" id="${prefix + name}" data-num="${name}" min="${min}" max="${max}" value="${String(value)}"${hint ? raw(html` aria-describedby="${prefix + name + 'Hint'}"`) : ''}>
      ${hint ? html`<small class="ds-field__hint" id="${prefix + name + 'Hint'}">${hint}</small>` : ''}
    </div>`;
  }

  function switchRow(prefix, name, label, on, hint) {
    return html`<label class="ds-switch">
      <input type="checkbox" role="switch" data-flag="${name}"${on ? raw(' checked') : ''} aria-labelledby="${prefix + name + 'Name'}" aria-describedby="${prefix + name + 'Hint'}">
      <span class="ds-switch__track" aria-hidden="true"></span>
      <span class="ds-switch__label" id="${prefix + name + 'Name'}">${label}</span>
      <small class="ds-switch__hint" id="${prefix + name + 'Hint'}">${hint}</small>
    </label>`;
  }

  // settingsForm is the settings of a run: the roles, then how it runs.
  function settingsForm(prefix, s) {
    return html`<div class="so-tests__stack" data-settings="${prefix}">
      ${ROLES.map((spec) => roleSection(prefix, spec, s[spec.key] || {}))}
      <section class="so-tests__stack" aria-labelledby="${prefix + 'RunTitle'}">
        <h3 class="ds-card__subtitle" id="${prefix + 'RunTitle'}">How a run goes</h3>
        <div class="ds-form-grid ds-form-grid--3">
          ${numberField(prefix, 'parallel', 'Cells at once', s.parallel, { min: 0, max: 64, hint: '0: as many as the pools have accounts, else 4.' })}
          ${numberField(prefix, 'attempts', 'Attempts per cell', s.attempts, { min: 1, max: 5, hint: 'A blocked cell is tried again up to this.' })}
          ${numberField(prefix, 'review_parallel', 'Reviewers at once', s.review_parallel, { min: 1, max: 16 })}
        </div>
        <div class="ds-switch-list">
          ${switchRow(prefix, 'review', 'Review', s.review, 'A reviewer checks the doubtful cells and one passed cell in five.')}
          ${switchRow(prefix, 'analysis', 'Analysis', s.analysis, 'An analyst writes the bugs and the conclusions once the cells are reviewed.')}
        </div>
      </section>
    </div>`;
  }

  function readSettings(root, base) {
    const out = JSON.parse(JSON.stringify(base));
    for (const spec of ROLES) {
      const r = out[spec.key] || (out[spec.key] = {});
      const val = (field) => {
        const node = root.querySelector(`[data-role="${spec.key}"][data-role-field="${field}"]`);
        return node ? node.value : '';
      };
      r.harness = val('harness');
      r.model = val('model');
      r.effort = val('effort');
      r.accounts = [...root.querySelectorAll(`[data-account="${spec.key}"]:checked`)].map((n) => n.value);
      delete r.account;
    }
    for (const node of root.querySelectorAll('[data-num]')) {
      if (node.dataset.num === 'max_sessions') continue;
      const n = parseInt(node.value, 10);
      out[node.dataset.num] = Number.isFinite(n) ? n : 0;
    }
    for (const node of root.querySelectorAll('[data-flag]')) out[node.dataset.flag] = node.checked;
    return out;
  }

  // A program or a model changed: what follows it is drawn again.
  function roleChanged(root, prefix, target) {
    const role = target.dataset.role;
    const field = target.dataset.roleField;
    if (!role || (field !== 'harness' && field !== 'model')) return;
    const box = root.querySelector(`[data-role-body="${role}"]`);
    const harness = root.querySelector(`[data-role="${role}"][data-role-field="harness"]`).value;
    const model = field === 'harness' ? (harnesses.defaultModelFor(harness) || '') : target.value;
    const effortNode = root.querySelector(`[data-role="${role}"][data-role-field="effort"]`);
    const keep = field === 'model' ? [...root.querySelectorAll(`[data-account="${role}"]:checked`)].map((n) => n.value) : [];
    const effort = field === 'model' && effortNode ? effortNode.value : '';
    morph(box, roleBody(prefix, role, { harness, model, effort, accounts: keep }));
  }

  /* ------------------------------------------------------ the settings tab */

  const savebar = DS.overlay.savebar($('savebar'), { focusTarget: () => $('p-settings').querySelector('select, input') });
  let drawnFor = '';
  let dirty = false;

  function setDirty(on) {
    dirty = on;
    if (on) savebar.show();
    else savebar.hide();
  }

  async function renderSettings(panel) {
    if (!state.settings) {
      morph(panel, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the settings</div>`);
      return;
    }
    if (drawnFor === state.repo && (dirty || panel.querySelector('[data-settings]'))) return;
    await Promise.all([prepare(), loadMcp()]);
    drawSettings(panel);
  }

  // The MCP servers a cell of the repository could have: built into
  // Socrates, Admin's, and the repository's .mcp.json's - as a chat in the
  // repository's folder would (GET /api/mcp).
  let mcp = { repo: '', servers: null, problems: [], error: '' };
  async function loadMcp() {
    const repo = state.repo;
    if (mcp.repo === repo && mcp.servers) return;
    const info = state.repos.find((one) => one.key === repo);
    if (!info || !info.path) {
      mcp = { repo, servers: [], problems: [], error: '' };
      return;
    }
    try {
      const data = await api('/api/mcp?workdir_mode=custom&workdir=' + encodeURIComponent(info.path), { attempts: 2, timeout: 10000 });
      mcp = { repo, servers: (data && data.servers) || [], problems: (data && data.problems) || [], error: '' };
    } catch (err) {
      mcp = { repo, servers: null, problems: [], error: errorMessage(err) };
    }
  }

  const MCP_FROM = { socrates: 'Built into Socrates', admin: 'From Admin', folder: 'From the repository’s .mcp.json' };

  // mcpSection is the switches of the MCP servers test cells get besides
  // those their area names; saved as the settings' mcp list.
  function mcpSection(s) {
    const chosen = s.mcp || [];
    const known = mcp.servers || [];
    const gone = chosen.filter((name) => !known.some((one) => one.name === name)).map((name) => ({ name, gone: true }));
    const list = [...known, ...gone];
    return html`<div class="ds-subsection so-tests__stack" aria-labelledby="stMcpTitle">
      <h3 class="ds-card__subtitle" id="stMcpTitle">MCP servers for test cells</h3>
      <p class="ds-muted">A cell’s agent gets the servers switched on here, and those its area names under <code>mcp:</code> in its AREA.md, which are always on. Every other server is off in a test cell.</p>
      ${mcp.error ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${'The servers could not be listed: ' + mcp.error}</span></div>` : ''}
      ${list.length ? html`<div class="ds-switch-list" data-mcp-list>${list.map((server, i) => html`<label class="ds-switch" data-ds-key="${'mcp-' + server.name}">
        <input type="checkbox" role="switch" data-mcp="${server.name}"${chosen.includes(server.name) ? raw(' checked') : ''} aria-labelledby="${'stMcp' + i}" aria-describedby="${'stMcp' + i + 'Hint'}">
        <span class="ds-switch__track" aria-hidden="true"></span>
        <span class="ds-switch__label" id="${'stMcp' + i}">${labelOf(server)}</span>
        <small class="ds-switch__hint" id="${'stMcp' + i + 'Hint'}">${server.gone ? 'Not found any more: switch it off.' : [MCP_FROM[server.from] || '', brief(server.description)].filter(Boolean).join('. ')}</small>
        <span class="ds-switch__state" aria-hidden="true"><span class="ds-switch__on">On</span><span class="ds-switch__off">Off</span></span>
      </label>`)}</div>` : mcp.error ? '' : html`<p class="ds-meta">There is none: add one in Admin, or in a .mcp.json in the repository.</p>`}
      ${mcp.problems.length ? html`<p class="ds-meta">${mcp.problems.join(' ')}</p>` : ''}
    </div>`;
  }

  function drawSettings(panel) {
    drawnFor = state.repo;
    const s = state.settings.settings;
    morph(panel, html`<form class="ds-card ds-form" id="settingsForm" aria-labelledby="settingsTitle" novalidate>
        <h2 class="ds-card__title" id="settingsTitle">Settings of this repository</h2>
        <p class="ds-muted">Every run starts with these; the Run sheet can change them for one run.</p>
        ${settingsForm('st', s)}
        ${mcpSection(s)}
        <div class="ds-subsection so-tests__stack">
          <h3 class="ds-card__subtitle">Keeping runs</h3>
          ${numberField('st', 'keep_runs', 'Runs kept', s.keep_runs, { min: 1, max: 1000, hint: 'Older runs are deleted with their screenshots and sessions; pinned runs stay on top of these.' })}
        </div>
      </form>
      <form class="ds-card ds-form" aria-labelledby="globalTitle" novalidate>
        <h2 class="ds-card__title" id="globalTitle">This server</h2>
        ${numberField('st', 'max_sessions', 'Test sessions at once', state.global.max_sessions, { min: 1, max: 128, hint: 'Over every repository: it keeps the machine responsive.' })}
      </form>`);
  }

  const settingsPanel = $('p-settings');
  settingsPanel.addEventListener('change', (event) => {
    if (event.target.dataset.roleField) roleChanged(settingsPanel, 'st', event.target);
    setDirty(true);
  });
  settingsPanel.addEventListener('input', () => setDirty(true));
  settingsPanel.addEventListener('submit', (event) => event.preventDefault());

  $('discard').addEventListener('click', () => {
    setDirty(false);
    savebar.update(() => drawSettings(settingsPanel));
  });

  $('save').addEventListener('click', async () => {
    const repo = state.repo;
    const body = readSettings(settingsPanel, state.settings.settings);
    if (settingsPanel.querySelector('[data-mcp-list]')) {
      body.mcp = [...settingsPanel.querySelectorAll('input[data-mcp]:checked')].map((node) => node.dataset.mcp);
    }
    const maxNode = settingsPanel.querySelector('[data-num="max_sessions"]');
    const max = maxNode ? parseInt(maxNode.value, 10) : state.global.max_sessions;
    try {
      await M.busy($('save'), (async () => {
        const saved = await api(V.paths.settings(repo), { method: 'PUT', body });
        if (Number.isFinite(max) && max !== state.global.max_sessions) {
          state.global = await api(V.paths.global(), { method: 'PUT', body: { max_sessions: max } });
        }
        if (repo === state.repo) state.settings = { ...state.settings, settings: saved.settings };
      })(), { done: false });
    } catch (err) {
      toast(errorMessage(err), 'error');
      return;
    }
    setDirty(false);
    savebar.update(() => drawSettings(settingsPanel));
    toast('The settings are saved.', 'success');
  });

  /* --------------------------------------------------------- the Run sheet */

  const sheet = $('runSheet');
  const runBody = $('runBody');
  const problems = new Map(); // run id -> Set of cell ids that did not pass
  let runOpen = false;

  sheet.addEventListener('ds-close', (event) => { if (event.target === sheet) runOpen = false; });

  async function openRunSheet(opener) {
    runOpen = true;
    morph(runBody, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the catalog</div>`);
    $('runCount').textContent = '';
    $('runFootNote').textContent = '';
    DS.overlay.open(sheet, { opener });
    try {
      await Promise.all([ctx.ensureCatalog(), ctx.ensureSettings(), prepare()]);
    } catch (err) {
      morph(runBody, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${errorMessage(err)}</span></div>`);
      return;
    }
    if (!runOpen) return;
    const cat = state.catalog;
    if (!cat) {
      morph(runBody, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${state.catalogError || 'The catalog could not be read.'}</span></div>`);
      return;
    }
    drawRunSheet(cat);
    count();
  }

  function pickList(name, items, { chips = false } = {}) {
    if (chips) {
      return html`<div class="ds-chips">${items.map(([value, label]) => html`<label class="ds-chip"><input class="ds-chip__input" type="checkbox" data-pick="${name}" value="${value}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${label}</span></span></label>`)}</div>`;
    }
    return html`<div class="ds-check-list">${items.map(([value, label, count]) => html`<label class="ds-check"><input type="checkbox" data-pick="${name}" value="${value}"><span>${label}</span>${count !== undefined ? html`<span class="ds-check__count">${count}</span>` : ''}</label>`)}</div>`;
  }

  function drawRunSheet(cat) {
    const areas = cat.areas || [];
    const tests = areas.flatMap((a) => a.tests || []);
    const priorities = [...new Set(tests.map((t) => t.priority).filter(Boolean))].sort();
    const tags = [...new Set(tests.flatMap((t) => t.tags || []))].sort();
    const dims = new Map();
    for (const area of areas) for (const d of area.dims || []) {
      if (!dims.has(d.name)) dims.set(d.name, new Map());
      for (const v of [...(d.values || []), ...(d.extra || [])]) dims.get(d.name).set(v.id, v.label || v.id);
    }
    const done = state.runs.filter((r) => !V.ACTIVE_RUN.has(r.state));
    const errors = cat.errors || [];
    morph(runBody, html`<form class="ds-form so-tests__stack" id="runForm" novalidate>
      ${errors.length ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${'The catalog has ' + (errors.length === 1 ? 'an error' : errors.length + ' errors') + ': see the Catalog tab. A run would not start.'}</span></div>` : ''}
      <div class="ds-field">
        <label class="ds-field__label" for="runName">Name <span class="ds-field__optional">(optional)</span></label>
        <input class="ds-input" id="runName" type="text" maxlength="80" autocomplete="off" placeholder="Before the release">
      </div>
      <p class="ds-muted">Everything is run unless you narrow it: a group with nothing ticked takes all of it.</p>
      ${areas.length > 1 ? html`<fieldset class="ds-field"><legend class="ds-field__label">Areas</legend>${pickList('areas', areas.map((a) => [a.id, a.title || a.id, (a.tests || []).reduce((n, t) => n + t.cells.length, 0)]))}</fieldset>` : ''}
      ${priorities.length > 1 ? html`<fieldset class="ds-field"><legend class="ds-field__label">Priorities</legend>${pickList('priorities', priorities.map((p) => [p, p]), { chips: true })}</fieldset>` : ''}
      ${tags.length ? html`<fieldset class="ds-field"><legend class="ds-field__label">Tags</legend>${pickList('tags', tags.map((t) => [t, t]), { chips: true })}</fieldset>` : ''}
      ${[...dims].map(([name, values]) => html`<fieldset class="ds-field"><legend class="ds-field__label">${name.charAt(0).toUpperCase() + name.slice(1)}</legend>${pickList('dim:' + name, [...values])}</fieldset>`)}
      <fieldset class="ds-field"><legend class="ds-field__label">Tests</legend>${pickList('tests', tests.map((t) => [t.id, t.priority + ' · ' + t.title, t.cells.length]))}</fieldset>
      ${done.length ? html`<div class="ds-field">
        <label class="ds-field__label" for="runProblems">Only the problems of</label>
        <select class="ds-input" id="runProblems" aria-describedby="runProblemsHint">
          <option value="">No: every cell picked above</option>
          ${done.map((r) => html`<option value="${r.id}">${V.runLabel(r) + ' · ' + V.countsWords(r.counts)}</option>`)}
        </select>
        <small class="ds-field__hint" id="runProblemsHint">Its cells that did not pass, among those picked above.</small>
      </div>` : ''}
      <details class="ds-collapsible ds-disclosure">
        <summary>Settings for this run${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body">${settingsForm('rs', state.settings.settings)}</div>
      </details>
    </form>`);
  }

  function selection() {
    const picked = (name) => [...runBody.querySelectorAll(`[data-pick="${name}"]:checked`)].map((n) => n.value);
    const sel = {};
    for (const name of ['areas', 'tests', 'tags', 'priorities']) {
      const list = picked(name);
      if (list.length) sel[name] = list;
    }
    const dims = {};
    for (const node of runBody.querySelectorAll('[data-pick^="dim:"]:checked')) {
      const name = node.dataset.pick.slice(4);
      (dims[name] || (dims[name] = [])).push(node.value);
    }
    if (Object.keys(dims).length) sel.dims = dims;
    const problemsOf = $('runProblems') ? $('runProblems').value : '';
    if (problemsOf) sel.problems_of = problemsOf;
    return sel;
  }

  // count is how many cells the selection holds, worked out from the
  // catalog the way the server expands it.
  async function count() {
    const cat = state.catalog;
    if (!cat || !runOpen) return;
    const sel = selection();
    let only = null;
    if (sel.problems_of) {
      if (!problems.has(sel.problems_of)) {
        try {
          const data = await api(V.paths.cells(sel.problems_of));
          problems.set(sel.problems_of, new Set((data.cells || []).filter((r) => {
            if (r.final) return r.final !== 'pass' && r.final !== 'na';
            return true;
          }).map((r) => r.cell_id)));
        } catch { problems.set(sel.problems_of, new Set()); }
      }
      only = problems.get(sel.problems_of);
    }
    let n = 0;
    for (const area of cat.areas || []) {
      if (sel.areas && !sel.areas.includes(area.id)) continue;
      for (const t of area.tests || []) {
        if (sel.tests && !sel.tests.includes(t.id)) continue;
        if (sel.tags && !(t.tags || []).some((tag) => sel.tags.includes(tag))) continue;
        if (sel.priorities && !sel.priorities.includes(t.priority)) continue;
        for (const key of t.cells) {
          const parts = key.split('.');
          const ok = (area.dims || []).every((d, i) => !sel.dims || !sel.dims[d.name] || sel.dims[d.name].includes(parts[i]));
          if (ok && (!only || only.has(t.id + '@' + key))) n += 1;
        }
      }
    }
    $('runCount').textContent = n === 1 ? '1 cell' : n + ' cells';
    const start = $('runStart');
    const blocked = !n || (cat.errors && cat.errors.length);
    if (blocked) {
      start.setAttribute('aria-disabled', 'true');
      start.dataset.tip = !n ? 'Nothing is picked' : 'The catalog has errors';
    } else {
      start.removeAttribute('aria-disabled');
      delete start.dataset.tip;
    }
    $('runFootNote').textContent = n ? 'Each cell is a session of its own.' : '';
  }

  runBody.addEventListener('change', (event) => {
    if (event.target.dataset.roleField) roleChanged(runBody, 'rs', event.target);
    count();
  });

  $('runStart').addEventListener('click', async () => {
    const start = $('runStart');
    if (start.getAttribute('aria-disabled') === 'true' || !state.settings) return;
    const name = $('runName') ? $('runName').value.trim() : '';
    const settings = readSettings(runBody, state.settings.settings);
    const body = { name, selection: selection(), settings };
    let run;
    sheet.setAttribute('data-ds-busy', '');
    try {
      run = await M.busy(start, api(V.paths.runs(state.repo), { method: 'POST', body }), { done: false });
    } catch (err) {
      toast(errorMessage(err), 'error');
      return;
    } finally {
      sheet.removeAttribute('data-ds-busy');
    }
    await DS.overlay.close(sheet, { reason: 'confirm' });
    toast('The run starts.', 'success');
    ctx.started(run);
  });

  /* ------------------------------------------------------------ the catalog */

  async function checkTool(tool, button) {
    checks.set(tool, { busy: true });
    try {
      const result = await M.busy(button, api(V.paths.check(state.repo, tool), { method: 'POST', body: {} }), { done: false });
      checks.set(tool, { result });
    } catch (err) {
      checks.set(tool, { result: { error: errorMessage(err), found: [], missing: [] } });
    }
    renderCatalog($('p-catalog'));
  }

  function renderCatalog(panel) {
    const cat = state.catalog;
    if (!cat) {
      morph(panel, state.catalogError
        ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${state.catalogError}</span><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-reload-catalog>Try again</button></div>`
        : html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the catalog</div>`);
      return;
    }
    const problem = (p) => (p.file ? p.file + (p.line ? ':' + p.line : '') + ': ' : '') + p.message;
    const areas = cat.areas || [];
    const testCount = areas.reduce((n, a) => n + (a.tests || []).length, 0);
    morph(panel, html`<div class="so-tests__stack">
      ${(cat.errors || []).map((p) => html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${problem(p)}</span></div>`)}
      ${(cat.warnings || []).map((p) => html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text so-break">${problem(p)}</span></div>`)}
      <section class="ds-card" aria-labelledby="catTitle">
        <div class="ds-card__row">
          <h2 class="ds-card__title" id="catTitle">${cat.title || 'Catalog'}</h2>
          <button class="ds-btn ds-btn--sm" type="button" data-reload-catalog>${raw(icon('sync', 16))}Read again</button>
        </div>
        <p class="ds-muted">${(testCount === 1 ? '1 test' : testCount + ' tests') + ' in ' + (areas.length === 1 ? '1 area' : areas.length + ' areas') + ', ' + (cat.cells === 1 ? '1 cell' : cat.cells + ' cells') + ' in all, as it is in the repository now.'}</p>
      </section>
      ${areas.map((area) => areaCard(area))}
      ${(cat.tools || []).length ? html`<section class="ds-card" aria-labelledby="toolsTitle">
        <h2 class="ds-card__title" id="toolsTitle">Tools</h2>
        ${cat.tools.map((tool) => toolBlock(tool))}
      </section>` : ''}
    </div>`);
  }

  function areaCard(area) {
    const dims = (area.dims || []).map((d) => d.name + ' (' + (d.values || []).length + (d.extra && d.extra.length ? ' + ' + d.extra.length + ' extra' : '') + ')').join(', ');
    return html`<section class="ds-card" data-ds-key="${'cat:' + area.id}" aria-labelledby="${'cat-' + area.id}">
      <h2 class="ds-card__title" id="${'cat-' + area.id}">${area.title || area.id}</h2>
      <dl class="ds-facts ds-facts--rows">
        <div><dt>Folder</dt><dd>${area.id}</dd></div>
        ${dims ? html`<div><dt>Dimensions</dt><dd>${dims}${area.columns ? html` · columns by <strong>${area.columns}</strong>` : ''}</dd></div>` : ''}
        ${area.tools && area.tools.length ? html`<div><dt>Tools</dt><dd>${area.tools.join(', ')}</dd></div>` : ''}
        <div><dt>Evidence</dt><dd>${area.evidence || 'none'}${area.timeout_min ? ' · ' + area.timeout_min + ' min per cell' : ''}</dd></div>
      </dl>
      ${area.body ? html`<details class="ds-collapsible ds-disclosure" data-key="${'body:' + area.id}"${isOpen('body:' + area.id)}>
        <summary>Instructions${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body"><div class="ds-prose so-break">${markdown(area.body)}</div></div>
      </details>` : ''}
      <div class="ds-disclosure-list">${(area.tests || []).map((t) => html`<details class="ds-collapsible ds-disclosure ds-disclosure--row" data-key="${'test:' + t.id}"${isOpen('test:' + t.id)}>
        <summary><span class="ds-disclosure__title">${t.title}</span><span class="ds-disclosure__meta">${t.priority + ' · ' + (t.cells.length === 1 ? '1 cell' : t.cells.length + ' cells')}</span>${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body so-tests__stack">
          <p class="ds-meta">${t.id}${(t.tags || []).map((tag) => html` <span class="ds-tag">${tag}</span>`)}</p>
          ${t.context ? html`<p class="so-break">${t.context}</p>` : ''}
          <ol class="ds-plain-list">${(t.steps || []).map((s) => html`<li class="so-break">${s.n + '. ' + s.do}<br><span class="ds-meta">${'Expect: ' + s.expect}</span></li>`)}</ol>
          <p>${t.cells.map((c) => html`<span class="ds-tag ds-tag--neutral">${c}</span> `)}</p>
        </div>
      </details>`)}</div>
    </section>`;
  }

  function toolBlock(tool) {
    const check = checks.get(tool.name);
    const r = check && check.result;
    const optional = new Set(tool.optional || []);
    return html`<div class="ds-subsection so-tests__stack" data-ds-key="${'tool:' + tool.name}">
      <div class="ds-card__row">
        <h3 class="ds-card__subtitle">${tool.title || tool.name}</h3>
        ${tool.secrets_command ? html`<button class="ds-btn ds-btn--sm" type="button" data-check-tool="${tool.name}">${raw(icon('key', 16))}Check secrets</button>` : ''}
      </div>
      <dl class="ds-facts ds-facts--rows">
        <div><dt>Name</dt><dd>${tool.name}</dd></div>
        ${tool.secrets_command ? html`<div><dt>Secrets from</dt><dd><code class="so-break">${tool.secrets_command}</code></dd></div>` : ''}
        ${tool.secrets && tool.secrets.length ? html`<div><dt>Secrets</dt><dd>${tool.secrets.map((n) => html`<span class="ds-tag ds-tag--neutral">${n + (optional.has(n) ? ' (optional)' : '')}</span> `)}</dd></div>` : ''}
        ${tool.pool ? html`<div><dt>Pool</dt><dd>${'The accounts in ' + tool.pool}</dd></div>` : ''}
        ${tool.setup ? html`<div><dt>Setup</dt><dd><code class="so-break">${tool.setup}</code></dd></div>` : ''}
        ${tool.end ? html`<div><dt>At a cell’s end</dt><dd><code class="so-break">${tool.end}</code></dd></div>` : ''}
      </dl>
      ${r ? checkResult(r) : ''}
      ${tool.body ? html`<details class="ds-collapsible ds-disclosure" data-key="${'tool:' + tool.name}"${isOpen('tool:' + tool.name)}>
        <summary>Instructions${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
        <div class="ds-disclosure__body"><div class="ds-prose so-break">${markdown(tool.body)}</div></div>
      </details>` : ''}
    </div>`;
  }

  function checkResult(r) {
    return html`<div class="so-tests__stack" role="status">
      ${r.error ? html`<div class="ds-alert ds-alert--error">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${r.error}</span></div>` : ''}
      ${r.found && r.found.length ? html`<p>${r.found.map((n) => html`<span class="ds-status ds-status--ok">${raw(icon('check', 16))}<span>${n}</span></span> `)}</p>` : ''}
      ${r.missing && r.missing.length ? html`<p>${r.missing.map((n) => html`<span class="ds-status ds-status--error">${raw(icon('alertCircle', 16))}<span>${n + ' missing'}</span></span> `)}</p>` : ''}
      ${r.members && r.members.length ? html`<p class="ds-meta">${(r.members.length === 1 ? '1 account in the pool: ' : r.members.length + ' accounts in the pool: ') + r.members.join(', ')}</p>` : ''}
      ${!r.error && !(r.missing && r.missing.length) ? html`<p class="ds-meta">The values themselves are never shown.</p>` : ''}
    </div>`;
  }

  $('p-catalog').addEventListener('click', async (event) => {
    const check = event.target.closest('[data-check-tool]');
    if (check) { checkTool(check.dataset.checkTool, check); return; }
    if (event.target.closest('[data-reload-catalog]')) {
      await ctx.ensureCatalog(true);
      renderCatalog($('p-catalog'));
    }
  });

  return {
    renderSettings,
    renderCatalog,
    openRunSheet,
    /** repoChanged forgets what was drawn for the repository before. */
    repoChanged() {
      checks.clear();
      problems.clear();
      if (dirty) setDirty(false);
      drawnFor = '';
    },
  };
}
