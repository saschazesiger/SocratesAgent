// The Tests page's forms (docs/design/TEST-MODE.md, "The Tests page" and
// "Settings"): the Settings view with its save bar, and the Run dialog -
// what to run (everything, the problems of a run, a priority, or a choice of
// areas and tests narrowed by priority, tag and dimension values), the
// settings of this run as one line that opens into the form, a name, and
// how many cells that is, about how long it takes and what it costs.
//
// The three roles - cell agent, reviewer, analyst - each take a program, a
// model, an effort and the accounts of that program they may run on (none
// ticked is every account); among those Socrates picks for each session.
//
// mount(ctx) hands back the Settings view ({ render, close, repoChanged })
// and the Run dialog: openRunDialog(preset, opener) and startDirect(preset,
// opener), which starts a preset after a question that says how many cells.
// A preset is { tests } | { cells } | { problems_of: 'latest' | run id } |
// { priorities } | { choose: true } | nothing (everything).

import { api, errorMessage, toast, confirmDialog } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import * as harnesses from './harnesses.js';
import * as accounts from './accounts.js';
import * as V from './testsview.js';
import { brief, labelOf } from './mcpview.js';

const $ = (id) => document.getElementById(id);
const ESTIMATE_PAUSE = 300;

const ROLES = [
  { key: 'cell', label: 'Cell agent', hint: 'Works through the steps of each cell and judges them on their screenshots.' },
  { key: 'reviewer', label: 'Reviewer', hint: 'Looks again at the doubtful cells and corrects their verdicts.' },
  { key: 'analyst', label: 'Analyst', hint: 'Turns the findings into one bug per root cause and writes the conclusions.' },
];

const PARALLEL = [1, 2, 3, 4, 6, 8, 12, 16];

export function mount(ctx) {
  const { state } = ctx;

  // The catalogue of programs and the accounts are what the role pickers
  // offer; both are read once, then whenever the page draws a form.
  let ready = null;
  function prepare() {
    if (!ready) ready = Promise.all([harnesses.load().catch(() => {}), accounts.load().catch(() => {})]);
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
    return list.map(([v, label]) => html`<option value="${v}"${String(v) === String(value) ? raw(' selected') : ''}>${label}</option>`);
  }

  function accountRows(role, harness, ticked) {
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
    return html`${role === 'cell' && r.harness && r.harness !== 'claude' ? html`<small class="ds-field__hint">${raw(icon('info', 16))}Only Claude Code was tried for judging screenshots; another program works through the same tools.</small>` : ''}
      <div class="ds-form-grid ds-form-grid--2">
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
        ${accountRows(role, r.harness, r.accounts || [])}
      </fieldset>`;
  }

  function roleSection(prefix, spec, r) {
    return html`<section class="ds-subsection so-tests__stack" data-role-box="${spec.key}" aria-labelledby="${prefix + spec.key + 'Title'}">
      <h3 class="ds-card__subtitle" id="${prefix + spec.key + 'Title'}">${spec.label}</h3>
      <p class="ds-muted">${spec.hint}</p>
      <div class="ds-field">
        <label class="ds-field__label" for="${prefix + spec.key + 'Harness'}">Program</label>
        <select class="ds-input" id="${prefix + spec.key + 'Harness'}" data-role-field="harness" data-role="${spec.key}">${options(programOptions(r.harness), r.harness)}</select>
      </div>
      <div class="so-tests__stack" data-role-body="${spec.key}">${roleBody(prefix, spec.key, r)}</div>
    </section>`;
  }

  function selectField(prefix, name, label, value, list, hint) {
    return html`<div class="ds-field">
      <label class="ds-field__label" for="${prefix + name}">${label}</label>
      <select class="ds-input" id="${prefix + name}" data-num="${name}"${hint ? raw(html` aria-describedby="${prefix + name + 'Hint'}"`) : ''}>${options(list, value)}</select>
      ${hint ? html`<small class="ds-field__hint" id="${prefix + name + 'Hint'}">${hint}</small>` : ''}
    </div>`;
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

  // howRunsGo is how a run goes: cells at once, attempts, review, analysis.
  function howRunsGo(prefix, s) {
    const parallel = [[0, 'As many as the tools’ accounts allow'], ...PARALLEL.map((n) => [n, n === 1 ? 'One at a time' : n + ' at once'])];
    if (s.parallel > 0 && !PARALLEL.includes(s.parallel)) parallel.push([s.parallel, s.parallel + ' at once']);
    const attempts = [[1, 'Once'], [2, 'Twice: a blocked cell once more'], [3, 'Up to 3 times'], [4, 'Up to 4 times'], [5, 'Up to 5 times']];
    const reviewers = [1, 2, 3, 4, 6, 8].map((n) => [n, n === 1 ? 'One at a time' : n + ' at once']);
    if (s.review_parallel > 0 && !reviewers.some(([n]) => n === s.review_parallel)) reviewers.push([s.review_parallel, s.review_parallel + ' at once']);
    const max = state.global.max_sessions || 8;
    return html`<div class="ds-form-grid ds-form-grid--2">
        ${selectField(prefix, 'parallel', 'Cells at once', s.parallel || 0, parallel, 'Each cell is an agent session of its own. However many you allow, a cell waits for a free account of its tools, and this server runs at most ' + max + ' test sessions at once.')}
        ${selectField(prefix, 'attempts', 'Attempts per cell', s.attempts || 2, attempts, 'A cell that ended blocked (not a product fault) is run again, at the end of the queue.')}
        ${selectField(prefix, 'review_parallel', 'Reviewers at once', s.review_parallel || 2, reviewers, 'A reviewer takes about six cells.')}
      </div>
      <div class="ds-switch-list">
        ${switchRow(prefix, 'review', 'Review', s.review !== false, 'A reviewer checks every cell that did not pass, and one passed cell in five.')}
        ${switchRow(prefix, 'analysis', 'Analysis', s.analysis !== false, 'An analyst writes one bug per root cause and the conclusions once the cells are reviewed.')}
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
      if (!root.querySelector(`[data-role="${spec.key}"][data-role-field="harness"]`)) continue;
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

  /** summaryWords is a run's settings in one line: "Cells: Claude Code · Sonnet 5.5 · Medium · 2 attempts · review on". */
  function summaryWords(s) {
    return [
      'Cells: ' + (V.roleWords(s.cell) || 'the default agent'),
      s.parallel > 0 ? s.parallel + ' at once' : '',
      V.plural(s.attempts || 1, 'attempt'),
      s.review !== false ? 'review on' : 'no review',
      s.analysis !== false ? 'analysis on' : 'no analysis',
    ].filter(Boolean).join(' · ');
  }

  /* ---------------------------------------------------- the Settings view */

  const savebar = DS.overlay.savebar($('savebar'), { focusTarget: () => $('p-settings').querySelector('select, input') });
  let drawnFor = '';
  let dirty = false;

  function setDirty(on) {
    dirty = on;
    if (on) savebar.show();
    else savebar.hide();
  }

  async function renderSettings(panel) {
    if (!panel.dataset.wired) wireSettings(panel);
    if (!state.settings) {
      if (!panel.querySelector('[data-settings]')) {
        morph(panel, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the settings</div>`);
      }
      ctx.ensureSettings().then(() => ctx.redraw('settings')).catch((err) => {
        morph(panel, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${errorMessage(err)}</span></div>`);
      });
      return;
    }
    if (drawnFor === state.repo && (dirty || panel.querySelector('[data-settings]'))) return;
    await Promise.all([prepare(), loadMcp()]);
    if (state.route.view !== 'settings' || !state.settings) return;
    drawSettings(panel);
  }

  // The MCP servers a cell of the repository could have: built into
  // Socrates, Admin's, and the repository's .mcp.json's - as a chat in the
  // repository's folder would (GET /api/mcp).
  let mcp = { repo: '', servers: null, problems: [], error: '' };
  async function loadMcp() {
    const repo = state.repo;
    if (mcp.repo === repo && mcp.servers) return;
    const info = ctx.repoOf(repo);
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
    return html`<section class="ds-card" aria-labelledby="stMcpTitle">
      <h2 class="ds-card__title" id="stMcpTitle">MCP servers for test cells</h2>
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
    </section>`;
  }

  function drawSettings(panel) {
    drawnFor = state.repo;
    const s = state.settings.settings;
    morph(panel, html`<form class="so-tests__stack" id="settingsForm" data-settings="st" novalidate>
        <section class="ds-card" aria-labelledby="stAgentsTitle">
          <h2 class="ds-card__title" id="stAgentsTitle">Agents</h2>
          <p class="ds-muted">Every run of this repository starts with these; the Run dialog can change them for one run.</p>
          ${ROLES.map((spec) => roleSection('st', spec, s[spec.key] || {}))}
        </section>
        <section class="ds-card" aria-labelledby="stRunTitle">
          <h2 class="ds-card__title" id="stRunTitle">How runs go</h2>
          ${howRunsGo('st', s)}
        </section>
        ${mcpSection(s)}
        <div class="ds-grid-2">
          <section class="ds-card" aria-labelledby="stKeepTitle">
            <h2 class="ds-card__title" id="stKeepTitle">Keeping runs</h2>
            ${numberField('st', 'keep_runs', 'Runs kept', s.keep_runs, { min: 1, max: 1000, hint: 'Older runs are deleted with their screenshots and sessions once a new run is done; pinned runs stay besides these.' })}
          </section>
          <section class="ds-card" aria-labelledby="stServerTitle">
            <h2 class="ds-card__title" id="stServerTitle">This server</h2>
            ${numberField('st', 'max_sessions', 'Test sessions at once', state.global.max_sessions, { min: 1, max: 128, hint: 'Over every repository, reviewers and analysts included: it keeps the machine responsive.' })}
          </section>
        </div>
      </form>`);
  }

  function wireSettings(panel) {
    panel.dataset.wired = '1';
    panel.addEventListener('change', (event) => {
      if (!event.target.closest('#settingsForm')) return;
      if (event.target.dataset.roleField) roleChanged(panel, 'st', event.target);
      setDirty(true);
    });
    panel.addEventListener('input', (event) => { if (event.target.closest('#settingsForm')) setDirty(true); });
    panel.addEventListener('submit', (event) => event.preventDefault());
  }

  $('discard').addEventListener('click', () => {
    setDirty(false);
    savebar.update(() => drawSettings($('p-settings')));
  });

  $('save').addEventListener('click', async () => {
    const panel = $('p-settings');
    const repo = state.repo;
    const body = readSettings(panel, state.settings.settings);
    if (panel.querySelector('[data-mcp-list]')) {
      body.mcp = [...panel.querySelectorAll('input[data-mcp]:checked')].map((node) => node.dataset.mcp);
    }
    const maxNode = panel.querySelector('[data-num="max_sessions"]');
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
    savebar.update(() => drawSettings(panel));
    toast('The settings are saved.', 'success');
  });

  /* ------------------------------------------------------- the Run dialog */

  const sheet = $('runSheet');
  const runBody = $('runBody');
  const problems = new Map(); // run id -> Set of its cell ids that did not pass
  let runOpen = false;
  let preset = {};
  let estimateMissing = false;
  let estimateTimer = 0;
  let asked = 0;

  sheet.addEventListener('ds-close', (event) => { if (event.target === sheet) runOpen = false; });

  // The latest finished run, whose problems the dialog offers.
  const latestDone = () => state.runs.find((r) => !V.ACTIVE_RUN.has(r.state)) || null;

  function problemsRun(id) {
    if (!id || id === 'latest') return latestDone();
    return ctx.runOf(id) || { id, n: '?' };
  }

  async function problemSet(id) {
    if (!problems.has(id)) {
      try {
        const data = await api(V.paths.cells(id));
        problems.set(id, new Set((data.cells || []).filter(V.isProblem).map((r) => r.cell_id)));
      } catch { problems.set(id, new Set()); }
    }
    return problems.get(id);
  }

  async function openRunDialog(given, opener) {
    runOpen = true;
    preset = given || {};
    morph(runBody, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Reading the catalog</div>`);
    $('runCount').textContent = '';
    $('runFootNote').textContent = '';
    DS.overlay.open(sheet, { opener });
    try {
      await Promise.all([ctx.ensureCatalog(), ctx.ensureSettings(), prepare()]);
      const from = preset.problems_of ? problemsRun(preset.problems_of) : latestDone();
      if (from && (preset.problems_of || V.problemCount(from.counts))) await problemSet(from.id);
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
    drawRunDialog(cat);
    update();
  }

  // What the catalog offers to narrow by.
  function facets(cat) {
    const areas = cat.areas || [];
    const tests = areas.flatMap((a) => a.tests || []);
    const priorities = [...new Set(tests.map((t) => t.priority).filter(Boolean))].sort();
    const tags = [...new Set(tests.flatMap((t) => t.tags || []))].sort();
    const dims = new Map();
    for (const area of areas) for (const d of area.dims || []) {
      if (!dims.has(d.name)) dims.set(d.name, new Map());
      for (const v of [...(d.values || []), ...(d.extra || [])]) dims.get(d.name).set(v.id, v.label || v.id);
    }
    return { areas, tests, priorities, tags, dims };
  }

  function tile(value, glyph, title, note, checked) {
    return html`<label class="ds-radio" data-ds-key="${value}"><input type="radio" name="runWhat" value="${value}"${checked ? raw(' checked') : ''}><span class="ds-radio__icon" aria-hidden="true">${raw(icon(glyph, 20))}</span>
      <span><strong>${title}</strong><small data-count-of="${value}">${note}</small></span><span class="ds-radio__mark" aria-hidden="true">${raw(icon('check', 14))}</span></label>`;
  }

  function drawRunDialog(cat) {
    const f = facets(cat);
    const errors = cat.errors || [];
    const from = preset.problems_of ? problemsRun(preset.problems_of) : latestDone();
    const offerProblems = from && (preset.problems_of || V.problemCount(from.counts) > 0);
    const offerCells = preset.cells && preset.cells.length;
    let what = 'all';
    if (offerCells) what = 'cells';
    else if (preset.problems_of && offerProblems) what = 'problems';
    else if (preset.priorities && preset.priorities.length === 1 && f.priorities.includes(preset.priorities[0])) what = 'prio:' + preset.priorities[0];
    else if (preset.tests || preset.choose || (preset.priorities && preset.priorities.length)) what = 'choose';
    const ticked = (id) => !preset.tests || preset.tests.includes(id);
    const narrowed = (name, value) => name === 'priorities' && what === 'choose' && preset.priorities ? preset.priorities.includes(value) : false;
    const s = state.settings.settings;
    const chips = (name, legend, items) => (items.length < 2 ? '' : html`<fieldset class="ds-field">
      <legend class="ds-field__label">${legend}</legend>
      <div class="ds-chips">${items.map(([value, label]) => html`<label class="ds-chip"><input class="ds-chip__input" type="checkbox" data-narrow="${name}" value="${value}"${narrowed(name, value) ? raw(' checked') : ''}><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${label}</span></span></label>`)}</div>
    </fieldset>`);
    morph(runBody, html`<form class="ds-form so-tests__stack" id="runForm" novalidate>
      ${errors.length ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${(errors.length === 1 ? 'The catalog has an error' : 'The catalog has ' + errors.length + ' errors') + ': a run would not start. The Tests view lists them with their files and lines.'}</span></div>` : ''}
      <div id="runEstimateErrors"></div>
      <section class="so-tests__stack" aria-labelledby="runWhatTitle">
        <h3 class="ds-card__subtitle" id="runWhatTitle">What to run</h3>
        <div class="ds-radios ds-radios--tiles" role="radiogroup" aria-labelledby="runWhatTitle">
          ${offerCells ? tile('cells', 'list', 'The chosen cells', '', what === 'cells') : ''}
          ${tile('all', 'play', 'Everything', '', what === 'all')}
          ${offerProblems ? tile('problems', 'sync', 'The problems of ' + V.runName(from, { name: false }), '', what === 'problems') : ''}
          ${f.priorities.length > 1 ? f.priorities.map((p) => tile('prio:' + p, 'flag', 'Only ' + p, '', what === 'prio:' + p)) : ''}
          ${tile('choose', 'filter', 'Choose…', 'Areas, tests, tags and values', what === 'choose')}
        </div>
      </section>
      <div class="so-tests__stack" id="runChoose"${what === 'choose' ? '' : raw(' hidden')}>
        <fieldset class="ds-field">
          <legend class="ds-field__label">Tests</legend>
          <div class="so-tests-tree">${f.areas.map((area) => html`<div class="ds-check-group" data-area="${area.id}">
            <label class="ds-check"><input type="checkbox" data-all data-area-all="${area.id}"><span>${area.title || area.id} <span class="ds-meta">${V.plural((area.tests || []).length, 'test')}</span></span></label>
            <div class="ds-check-group__children" role="group" aria-label="${'Tests of ' + (area.title || area.id)}">${(area.tests || []).map((t) => html`<label class="ds-check"><input type="checkbox" data-test="${t.id}" value="${t.id}"${ticked(t.id) ? raw(' checked') : ''}><span>${t.title} <span class="ds-meta">${[t.priority, V.plural((t.cells || []).length, 'cell')].filter(Boolean).join(' · ')}</span></span></label>`)}</div>
          </div>`)}</div>
        </fieldset>
        ${chips('priorities', 'Only these priorities', f.priorities.map((p) => [p, p]))}
        ${chips('tags', 'Only with these tags', f.tags.map((t) => [t, t]))}
        ${[...f.dims].map(([name, values]) => chips('dim:' + name, 'Only these values of ' + name, [...values]))}
        <p class="ds-field__hint">A row of chips with none ticked takes all of it.</p>
      </div>
      <section class="so-tests__stack" aria-labelledby="runSetTitle">
        <h3 class="ds-card__subtitle" id="runSetTitle">Settings of this run</h3>
        <div class="so-tests-summary">
          <p class="so-break" id="runSummary">${summaryWords(s)}</p>
          <button class="ds-btn ds-btn--disclosure ds-btn--sm" type="button" id="runChange" aria-expanded="false" aria-controls="runSettings">Change</button>
        </div>
        <div class="so-tests__stack" id="runSettings" hidden>
          ${ROLES.map((spec) => roleSection('rs', spec, s[spec.key] || {}))}
          <section class="ds-subsection so-tests__stack" aria-labelledby="rsHowTitle">
            <h3 class="ds-card__subtitle" id="rsHowTitle">How it goes</h3>
            ${howRunsGo('rs', s)}
          </section>
        </div>
      </section>
      <div class="ds-field">
        <label class="ds-field__label" for="runName">Name <span class="ds-field__optional">(optional)</span></label>
        <input class="ds-input" id="runName" type="text" maxlength="80" autocomplete="off" placeholder="Before the release" aria-describedby="runNameHint">
        <small class="ds-field__hint" id="runNameHint">Shown after its number: “Run ${state.runs.length ? Math.max(...state.runs.map((r) => r.n || 0)) + 1 : 1} · Before the release”.</small>
      </div>
    </form>`);
    syncGroups();
  }

  // The parents of the tree follow their tests: ticked, unticked, or some.
  function syncGroups() {
    for (const group of runBody.querySelectorAll('.ds-check-group')) {
      const all = group.querySelector('[data-all]');
      const children = [...group.querySelectorAll('.ds-check-group__children input')];
      const on = children.filter((c) => c.checked).length;
      all.checked = on === children.length && on > 0;
      all.indeterminate = on > 0 && on < children.length;
    }
  }

  /** selectionOf is the run's selection for a choice in the dialog (or a preset). */
  function selectionOf(what) {
    if (what === 'all') return {};
    if (what === 'problems') return { problems_of: (problemsRun(preset.problems_of) || {}).id };
    if (what === 'cells') {
      // API: Selection.Cells (TEST-MODE.md "Selection"); an older server runs their tests instead.
      return state.v2 ? { cells: preset.cells.slice() } : { tests: [...new Set(preset.cells.map((c) => c.split('@')[0]))] };
    }
    if (what.startsWith('prio:')) return { priorities: [what.slice(5)] };
    const sel = {};
    const areasAll = [];
    const tests = [];
    let everyTest = true;
    for (const group of runBody.querySelectorAll('.ds-check-group')) {
      const children = [...group.querySelectorAll('.ds-check-group__children input')];
      const on = children.filter((c) => c.checked);
      if (on.length === children.length) areasAll.push(group.dataset.area);
      else { everyTest = false; tests.push(...on.map((c) => c.value)); }
    }
    if (!everyTest) {
      if (areasAll.length && !tests.length) sel.areas = areasAll;
      else sel.tests = [...tests, ...areasAll.flatMap((a) => [...runBody.querySelectorAll(`[data-area="${CSS.escape(a)}"] .ds-check-group__children input`)].map((c) => c.value))];
      if (!sel.areas && !sel.tests.length) sel.tests = ['-'];
    }
    const picked = (name) => [...runBody.querySelectorAll(`[data-narrow="${name}"]:checked`)].map((n) => n.value);
    for (const name of ['priorities', 'tags']) {
      const list = picked(name);
      if (list.length) sel[name] = list;
    }
    const dims = {};
    for (const node of runBody.querySelectorAll('[data-narrow^="dim:"]:checked')) {
      const name = node.dataset.narrow.slice(4);
      (dims[name] || (dims[name] = [])).push(node.value);
    }
    if (Object.keys(dims).length) sel.dims = dims;
    return sel;
  }

  // presetSelection is the selection of a preset that starts without the dialog.
  function presetSelection(p) {
    if (p.problems_of) return { problems_of: (problemsRun(p.problems_of) || {}).id };
    if (p.cells) return state.v2 ? { cells: p.cells.slice() } : { tests: [...new Set(p.cells.map((c) => c.split('@')[0]))] };
    const sel = {};
    if (p.tests) sel.tests = p.tests.slice();
    if (p.priorities) sel.priorities = p.priorities.slice();
    return sel;
  }

  /** countLocal is how many cells a selection holds, worked out from the catalog the way the server expands it. */
  function countLocal(sel) {
    const cat = state.catalog;
    if (!cat) return { cells: 0, tests: 0 };
    let only = null;
    if (sel.problems_of) only = problems.get(sel.problems_of) || new Set();
    if (sel.cells) only = new Set(sel.cells);
    let n = 0;
    const tests = new Set();
    for (const area of cat.areas || []) {
      if (sel.areas && !sel.areas.includes(area.id)) continue;
      for (const t of area.tests || []) {
        if (sel.tests && !sel.tests.includes(t.id)) continue;
        if (sel.tags && !(t.tags || []).some((tag) => sel.tags.includes(tag))) continue;
        if (sel.priorities && !sel.priorities.includes(t.priority)) continue;
        for (const key of t.cells || []) {
          const parts = key.split('.');
          const ok = (area.dims || []).every((d, i) => !sel.dims || !sel.dims[d.name] || sel.dims[d.name].includes(parts[i]));
          if (ok && (!only || only.has(t.id + '@' + key))) { n += 1; tests.add(t.id); }
        }
      }
    }
    return { cells: n, tests: tests.size };
  }

  /**
   * estimate is what a selection would run: the exact cell count, about how
   * long and what it costs (POST …/estimate), or the count worked out here
   * on a server without it.
   */
  async function estimate(sel, settings) {
    if (state.v2 && !estimateMissing) {
      // API: POST /api/tests/repos/{repo}/estimate (TEST-MODE.md "Times and costs").
      try {
        const body = { selection: sel };
        if (settings) body.settings = settings;
        const e = await api(V.paths.estimate(state.repo), { method: 'POST', body, attempts: 1 });
        if (e && typeof e.cells === 'number') return e;
      } catch (err) {
        if (err && (err.status === 404 || err.status === 405)) estimateMissing = true;
      }
    }
    return countLocal(sel);
  }

  function estimateWords(e) {
    return [V.plural(e.cells || 0, 'cell'), e.eta_ms > 0 ? 'about ' + V.span(e.eta_ms) : '', typeof e.cost === 'number' && e.cost > 0 ? 'about ' + V.money(e.cost) : ''].filter(Boolean).join(' · ');
  }

  function currentWhat() {
    const checked = runBody.querySelector('input[name="runWhat"]:checked');
    return checked ? checked.value : 'all';
  }

  function settingsChanged() {
    return !$('runSettings') || $('runSettings').hidden ? null : readSettings($('runSettings'), state.settings.settings);
  }

  // update counts every tile at once from the catalog, and asks the server
  // for the exact figure of the choice a moment after the last change.
  function update() {
    if (!runOpen || !state.catalog) return;
    const what = currentWhat();
    $('runChoose').hidden = what !== 'choose';
    for (const small of runBody.querySelectorAll('[data-count-of]')) {
      const of = small.dataset.countOf;
      if (of === 'choose' && what !== 'choose') continue;
      const words = V.plural(countLocal(selectionOf(of)).cells, 'cell');
      if (small.textContent !== words) small.textContent = words;
    }
    const settings = settingsChanged();
    if (settings) $('runSummary').textContent = summaryWords(settings);
    const sel = selectionOf(what);
    const local = countLocal(sel);
    show(local, []);
    clearTimeout(estimateTimer);
    const me = ++asked;
    estimateTimer = setTimeout(async () => {
      const e = await estimate(sel, settings);
      if (me === asked && runOpen) show(e, e.errors || []);
    }, ESTIMATE_PAUSE);
  }

  function show(e, errors) {
    const words = estimateWords(e);
    if ($('runFootNote').textContent !== words) $('runFootNote').textContent = words;
    const count = V.plural(e.cells || 0, 'cell');
    if ($('runCount').textContent !== count) $('runCount').textContent = count;
    morph($('runEstimateErrors'), errors.length ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${errors.join(' ')}</span></div>` : '');
    const start = $('runStart');
    const catErrors = state.catalog && state.catalog.errors && state.catalog.errors.length;
    const blocked = !e.cells || catErrors || errors.length;
    if (blocked) {
      start.setAttribute('aria-disabled', 'true');
      start.dataset.tip = !e.cells ? 'Nothing is chosen' : 'The catalog has errors';
    } else {
      start.removeAttribute('aria-disabled');
      delete start.dataset.tip;
    }
    morph(start.querySelector('.ds-btn__face'), e.cells ? 'Start ' + V.plural(e.cells, 'cell') : 'Start run');
  }

  runBody.addEventListener('change', (event) => {
    const t = event.target;
    if (t.dataset.roleField) roleChanged(runBody, 'rs', t);
    if (t.matches('[data-all]')) {
      for (const child of t.closest('.ds-check-group').querySelectorAll('.ds-check-group__children input')) child.checked = t.checked;
    }
    if (t.closest('.ds-check-group')) syncGroups();
    update();
  });
  runBody.addEventListener('click', (event) => {
    const change = event.target.closest('#runChange');
    if (!change) return;
    const box = $('runSettings');
    box.hidden = !box.hidden;
    change.setAttribute('aria-expanded', String(!box.hidden));
    change.textContent = box.hidden ? 'Change' : 'Keep the repository’s';
    if (box.hidden) {
      // Back to the repository's settings: the form forgets the changes.
      const s = state.settings.settings;
      morph(box, html`${ROLES.map((spec) => roleSection('rs', spec, s[spec.key] || {}))}<section class="ds-subsection so-tests__stack" aria-labelledby="rsHowTitle"><h3 class="ds-card__subtitle" id="rsHowTitle">How it goes</h3>${howRunsGo('rs', s)}</section>`);
      $('runSummary').textContent = summaryWords(s);
    } else {
      M.enter(box, 'fade');
      const first = box.querySelector('select');
      if (first) first.focus();
    }
    update();
  });

  $('runStart').addEventListener('click', async () => {
    const start = $('runStart');
    if (start.getAttribute('aria-disabled') === 'true' || !state.settings) return;
    const name = $('runName') ? $('runName').value.trim() : '';
    const body = { name, selection: selectionOf(currentWhat()) };
    const settings = settingsChanged();
    if (settings) body.settings = settings;
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

  /**
   * startDirect starts a preset at once with the repository's settings,
   * after a question that says how many cells it runs (and, where the
   * server knows, about how long and what it costs).
   */
  async function startDirect(p, opener) {
    await ctx.ensureCatalog();
    if (!state.catalog) { toast(state.catalogError || 'The catalog could not be read.', 'error'); return; }
    const given = p || {};
    if (given.problems_of) await problemSet((problemsRun(given.problems_of) || {}).id);
    const sel = presetSelection(given);
    const e = await estimate(sel);
    if (!e.cells) { toast('That runs no cell.', 'info'); return; }
    const what = given.problems_of ? 'the problems of ' + V.runName(problemsRun(given.problems_of), { name: false }) + ' again'
      : given.priorities ? given.priorities.join(' and ') + ' tests'
        : given.tests ? (given.tests.length === 1 ? 'this test' : V.plural(given.tests.length, 'test'))
          : given.cells ? 'these cells' : 'everything';
    const more = [e.eta_ms > 0 ? 'about ' + V.span(e.eta_ms) : '', typeof e.cost === 'number' && e.cost > 0 ? 'about ' + V.money(e.cost) : ''].filter(Boolean).join(', ');
    const yes = await confirmDialog({
      title: 'Run ' + what + '?',
      body: V.plural(e.cells, 'cell') + ', each an agent session of its own, with this repository’s settings' + (more ? ': ' + more + '.' : '.'),
      confirmLabel: 'Run ' + V.plural(e.cells, 'cell'),
      opener,
    });
    if (!yes) return;
    try {
      const run = await api(V.paths.runs(state.repo), { method: 'POST', body: { name: '', selection: sel } });
      toast('The run starts.', 'success');
      ctx.started(run);
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
  }

  return {
    render: renderSettings,
    openRunDialog,
    startDirect,
    close() {
      // Unsaved settings stay in the form and the bar while the person looks elsewhere.
    },
    /** repoChanged forgets what was drawn for the repository before. */
    repoChanged() {
      problems.clear();
      estimateMissing = false;
      if (dirty) setDirty(false);
      drawnFor = '';
    },
  };
}
