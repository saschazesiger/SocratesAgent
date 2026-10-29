// The chat dialog of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", the chat dialog): Fix in a chat, Write a test with an agent, Change
// it with an agent. mount(ctx) hands back { open(spec, opener) } with spec
// { kind: 'fix-bug' | 'fix-cell' | 'write-test' | 'change-test', subject }:
//
//   fix-bug      { repo, bug }              bug: the BugView or its id (and
//                                          `detail`, GET …/bugs/{id}, if the
//                                          caller has it)
//   fix-cell     { run, attempt, view? }    view: the attempt in full
//   write-test   { repo }
//   change-test  { repo, test }             test: a test's id
//
// The dialog asks what a new chat is bound by - the program, its model and
// effort, the account (automatic unless one is picked) - shows the folder
// (the repository's, not to be changed) and the first message, written for
// the task and editable. Start chat makes the chat (POST /api/chats, with
// the message and `tests: true`, so the chat has the test tools even before
// the repository has a catalog) and opens it on this tab.
//
// The program chosen is remembered for the next chat started from here; the
// model and effort start on what Admin set as the program's default.

import { api, clientKey, errorMessage } from './api.js';
import { DS, M, html, raw, icon, morph } from './ui.js';
import * as harnesses from './harnesses.js';
import * as accounts from './accounts.js';
import * as V from './testsview.js';

const $ = (id) => document.getElementById(id);
const PROGRAM_KEY = 'socrates.tests.chatProgram';
// The cells of a bug whose steps the message spells out; the others are named.
const CELLS_IN_FULL = 3;
const TITLE_MAX = 80;

// The first line of a message the person still has to write, by kind.
const PROMPT = {
  'write-test': 'What the test should check: ',
  'change-test': 'What to change: ',
};

export function mount(ctx) {
  const { state } = ctx;
  const dialog = $('chatDialog');
  let ready = null;
  let gen = 0; // the opening shown; a slow read for an earlier one is dropped
  let shown = null; // { spec, repo, path, title, prefill, loading, error }

  function prepare() {
    if (!ready) ready = Promise.all([harnesses.load().catch(() => {}), accounts.load().catch(() => {})]);
    return ready;
  }

  const dirty = () => !!shown && !shown.loading && !!$('chatMessage') && $('chatMessage').value !== shown.prefill;
  V.guardClose(dialog, {
    dirty,
    question: () => ({ title: 'Discard the message?', body: 'What you wrote for the agent is lost.', confirmLabel: 'Discard', cancelLabel: 'Keep editing' }),
  });

  /* ------------------------------------------------------------ pickers */

  const usable = (h) => harnesses.isChat(h.id) && h.id !== 'vscode' && h.enabled !== false && h.installed !== false;

  function programs() {
    return harnesses.list().filter(usable);
  }

  function defaultProgram() {
    const list = programs();
    const ok = (id) => list.some((h) => h.id === id);
    let last = '';
    try { last = localStorage.getItem(PROGRAM_KEY) || ''; } catch { /* private window */ }
    if (ok(last)) return last;
    const admin = harnesses.workspace().default_harness;
    if (ok(admin)) return admin;
    if (ok('claude')) return 'claude';
    return list.length ? list[0].id : '';
  }

  // defaultEffort is the effort a model starts on: Admin's for the program's
  // default model, else the model's own.
  function defaultEffort(harness, model) {
    const entry = harnesses.harness(harness);
    const known = harnesses.modelOf(harness, model);
    if (entry && entry.default_effort && model === entry.default_model) return entry.default_effort;
    return known && known.default_effort ? known.default_effort : '';
  }

  const options = (list, value) => list.map(([v, label]) => html`<option value="${v}"${String(v) === String(value) ? raw(' selected') : ''}>${label}</option>`);

  function pickers(pick) {
    const progs = programs();
    const models = harnesses.offered(pick.harness).map((m) => [m.id, m.label || m.id]);
    if (pick.model && !models.some(([id]) => id === pick.model)) models.unshift([pick.model, pick.model]);
    if (!models.length) models.push(['', 'Its default']);
    const efforts = harnesses.effortsFor(pick.harness, pick.model);
    const list = accounts.forAgent(pick.harness);
    const best = accounts.best(pick.harness);
    const accountWords = (a) => a.label + ' · ' + [accounts.who(a), accounts.usable(a) ? '' : 'not signed in', accounts.renewal(a) ? 'at its limit' : ''].filter(Boolean).join(', ');
    return html`<div class="ds-form-grid ds-form-grid--3">
        <div class="ds-field">
          <label class="ds-field__label" for="chatProgram">Program</label>
          <select class="ds-input" id="chatProgram" data-pick="harness">${options(progs.map((h) => [h.id, h.label]), pick.harness)}</select>
        </div>
        <div class="ds-field">
          <label class="ds-field__label" for="chatModel">Model</label>
          <select class="ds-input" id="chatModel" data-pick="model">${options(models, pick.model)}</select>
        </div>
        <div class="ds-field">
          <label class="ds-field__label" for="chatEffort">Effort</label>
          <select class="ds-input" id="chatEffort" data-pick="effort"${efforts.length ? '' : raw(' disabled')}>${options([['', efforts.length ? 'Default' : 'Not offered'], ...efforts.map((e) => [e, harnesses.effortLabel(e)])], pick.effort)}</select>
        </div>
      </div>
      <div class="ds-form-grid ds-form-grid--2">
        <div class="ds-field">
          <label class="ds-field__label" for="chatAccount">Account</label>
          <select class="ds-input" id="chatAccount" data-pick="account" aria-describedby="chatAccountHint"${list.length ? '' : raw(' disabled')}>${options([
            ['', list.length ? 'Automatic' + (best && list.length > 1 ? ' (now ' + best.label + ')' : '') : 'None yet'],
            ...accounts.ranked(pick.harness).map((a) => [a.id, accountWords(a)]),
          ], pick.account)}</select>
          <small class="ds-field__hint" id="chatAccountHint">${list.length
            ? 'Automatic takes the account with the most left when the chat starts.'
            : html`${'No ' + harnesses.label(pick.harness) + ' account yet: the chat starts on a new one that has to be signed in. '}<a href="/admin#accountsCard">Add one in Admin</a>`}</small>
        </div>
        <div class="ds-field" role="group" aria-labelledby="chatFolderLabel">
          <span class="ds-field__label" id="chatFolderLabel">Folder</span>
          <span class="ds-field__value so-break">${shown.path || shown.repo}<span class="ds-lock" role="img" aria-label="The repository’s folder" data-tip="The repository’s folder: a chat about its tests works there">${raw(icon('lock', 16))}</span></span>
        </div>
      </div>
      ${signInAlert(pick, list)}`;
  }

  // signInAlert says when the chat would start on an account Socrates does
  // not know to be signed in: it asks for the sign-in in the chat, unless
  // the program is signed in on its own.
  function signInAlert(pick, list) {
    const chosen = pick.account ? list.find((a) => a.id === pick.account) : null;
    const bad = chosen ? !accounts.usable(chosen) : list.length > 0 && !list.some(accounts.usable);
    if (!bad) return '';
    const name = harnesses.label(pick.harness);
    const text = chosen ? chosen.label + ' is not signed in: the chat asks for the sign-in first, unless ' + name + ' is signed in on its own.'
      : 'No ' + name + ' account is signed in: the chat starts on one and asks for the sign-in first, unless ' + name + ' is signed in on its own.';
    return html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text so-break">${text}</span><a class="ds-btn ds-btn--text ds-btn--sm" href="/admin#accountsCard">Accounts in Admin</a></div>`;
  }

  function readPick() {
    const val = (id) => ($(id) ? $(id).value : '');
    return { harness: val('chatProgram'), model: val('chatModel'), effort: val('chatEffort'), account: val('chatAccount') };
  }

  function drawPickers(pick) {
    const box = $('chatPickers');
    if (!box) return;
    if (!programs().length) {
      morph(box, html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text">No agent program is installed and switched on here: Admin lists the programs.</span></div>`);
      return;
    }
    morph(box, pickers(pick));
    for (const [id, v] of [['chatProgram', pick.harness], ['chatModel', pick.model], ['chatEffort', pick.effort], ['chatAccount', pick.account]]) {
      if ($(id)) $(id).value = v;
    }
  }

  /* ----------------------------------------------------------- messages */

  const runLabel = (id, number) => {
    const run = ctx.runOf(id);
    return run ? V.runName(run, { name: false }) : number ? 'Run ' + number : 'the run ' + id;
  };

  // stepLines spells out the steps of a cell that did not pass.
  function stepLines(view, indent = '') {
    const found = V.problemSteps(view);
    if (!found.length) return [indent + '(No step was judged a failure: ' + (view.end_reason || 'the cell ended without a verdict for its steps') + '.)'];
    const out = [];
    for (const s of found) {
      out.push(indent + '- Step ' + s.n + ' (' + V.verdictWord(s.verdict) + '): ' + s.do);
      out.push(indent + '  - Expected: ' + s.expect);
      out.push(indent + '  - Observed: ' + (s.observed || '(the agent said nothing)'));
      if (s.review) out.push(indent + '  - Reviewer: ' + s.review);
    }
    out.push('');
    return out;
  }

  function verdictLine(view) {
    const parts = ['The agent said ' + V.verdictWord(view.verdict || '—')];
    if (view.reviewed && view.reviewed !== view.verdict) parts.push('the reviewer made it ' + V.verdictWord(view.reviewed));
    if (view.override) parts.push('a person set it to ' + V.verdictWord(view.override.verdict) + (view.override.note ? ' ("' + view.override.note + '")' : ''));
    return parts.join(', ') + '.';
  }

  const finish = (cells) => [
    '## What to do',
    '',
    '1. Find the root cause in this repository. The test catalog is in .socrates/tests/: the test files say what each step does and expects.',
    '2. Fix it, then explain in a few sentences what the cause was and what you changed.',
    '3. Verify the fix: call run_tests with cells `' + JSON.stringify(cells) + '` and end your turn. Socrates sends you the results when the run is done; read them with test_results and say whether the problem is gone.',
  ];

  async function fixBugMessage(subject) {
    const repo = subject.repo || state.repo;
    let detail = subject.detail;
    const id = typeof subject.bug === 'string' ? subject.bug : subject.bug.id;
    if (!detail || !detail.bug) detail = await api(V.paths.bug(repo, id), { attempts: 2 });
    const bug = detail.bug;
    const rows = detail.cells || [];
    const views = await Promise.all(rows.slice(0, CELLS_IN_FULL).map((r) => V.attemptView(bug.last_run, r.id).catch(() => null)));
    const seen = V.seenWords(bug);
    const lines = [
      'Fix bug ' + bug.id + ' of this repository: "' + bug.title + '".',
      '',
      'Socrates\' test runs found it. Severity: ' + V.severityWord(bug.severity) + '. Status: ' + V.bugStatusWord(bug.status).toLowerCase()
        + (bug.regressed ? ' (it came back after it was fixed)' : '') + '. Seen in ' + V.plural(bug.seen || (bug.runs || []).length, 'run') + (seen ? ' (' + seen + ')' : '') + '.',
      '',
    ];
    if (bug.description) lines.push('## What the analyst found', '', V.stripHeading(bug.description).trim(), '');
    if (bug.note) lines.push('Note from the person: ' + bug.note, '');
    if (rows.length) {
      lines.push('## The cells that show it', '');
      rows.forEach((r, i) => {
        const verdict = r.final ? V.verdictWord(r.final) : V.attemptWord(r.state);
        lines.push('### ' + V.cellName(r), '', 'Cell `' + r.cell_id + '`: ' + verdict + ' in ' + runLabel(bug.last_run, bug.last_number) + '.', '');
        const view = views[i];
        if (view) {
          lines.push(...stepLines(view));
          if (view.summary) lines.push('Summary: ' + view.summary, '');
          if (view.review && view.review.note) lines.push('Reviewer: ' + view.review.note, '');
        } else if (i >= CELLS_IN_FULL) {
          lines.push('Its failing steps are in test_results.', '');
        }
      });
    }
    lines.push('## Evidence', '',
      'The screenshots and the agents\' records are in Socrates\' test run `' + bug.last_run + '` (' + runLabel(bug.last_run, bug.last_number) + '). '
      + 'Call test_results with run `' + bug.last_run + '` for every problem cell\'s failing step, observation and the paths of its screenshots.', '');
    lines.push(...finish(bug.cells && bug.cells.length ? bug.cells : rows.map((r) => r.cell_id)));
    return {
      text: lines.join('\n'),
      title: 'Fix ' + bug.id + ': ' + bug.title,
      heading: 'Fix ' + bug.id + ' in a chat',
      lead: html`${V.severityChip(bug.severity)} <span>${bug.id + ' · ' + bug.title}</span>`,
    };
  }

  async function fixCellMessage(subject) {
    const view = subject.view && subject.view.steps ? subject.view : await V.attemptView(subject.run, subject.attempt);
    const run = ctx.runOf(subject.run);
    const name = runLabel(subject.run, run ? run.n : view.number);
    const lines = [
      'Fix the problem the test cell `' + view.cell_id + '` found in this repository.',
      '',
      '## The cell',
      '',
      'Test: "' + view.title + '" (`' + view.test + '`)' + (V.dimsWords(view.dims) ? ', on ' + V.dimsWords(view.dims) : '') + '.',
      '',
      'Verdict in ' + name + ': ' + V.verdictWord(view.final || view.verdict || view.state) + '.' + (view.override || (view.reviewed && view.reviewed !== view.verdict) ? ' ' + verdictLine(view) : ''),
      '',
      '## What went wrong',
      '',
      ...stepLines(view),
    ];
    if (view.summary) lines.push('Summary of the agent: ' + view.summary, '');
    if (view.review && view.review.note) lines.push('The reviewer\'s note: ' + view.review.note, '');
    if (view.bugs && view.bugs.length) lines.push('The analyst filed it under ' + view.bugs.join(', ') + '.', '');
    lines.push('## Evidence', '',
      'The screenshots and the agent\'s record are in Socrates\' test run `' + subject.run + '` (' + name + '), attempt `' + view.id + '`. '
      + 'Call test_results with run `' + subject.run + '` for its failing step, observation and the paths of its screenshots.', '');
    lines.push(...finish([view.cell_id]));
    return {
      text: lines.join('\n'),
      title: 'Fix: ' + V.cellName(view),
      heading: 'Fix this cell in a chat',
      lead: html`${V.verdictChip(view.final || view.verdict)} <span>${V.cellName(view)}</span>`,
    };
  }

  const checkAndRun = (selection) => [
    '1. Read the catalog format with tests_format.',
    '2. Look at the catalog in .socrates/tests/ (TESTS.md, each area\'s AREA.md and its tests) and keep to its ways: the areas, the dimensions, priorities and tags.',
    selection.write,
    '4. Validate the catalog with tests_check and fix every error and warning it reports for your files.',
    '5. Run ' + selection.what + ' with run_tests (' + selection.how + ') and end your turn. Socrates sends you the results when the run is done; read them with test_results and say whether the test works as meant: a fail that shows a real bug is fine, a blocked cell or a wrong expectation is not.',
  ];

  async function writeTestMessage() {
    const lines = [
      PROMPT['write-test'],
      '',
      '---',
      'Write a test for that in this repository\'s test catalog (Socrates runs it: an agent works through its steps in each of its cells and judges each step).',
      '',
      ...checkAndRun({
        write: '3. Write the test file (or files): front matter, the context, and numbered steps, each with what to do and an "Expect:" line.',
        what: 'only the new test',
        how: 'tests: its id',
      }),
    ];
    return { text: lines.join('\n'), title: 'New test', heading: 'Write a test with an agent', lead: html`<span>The agent writes it into the catalog, checks it and runs it once.</span>`, caret: PROMPT['write-test'].length };
  }

  async function changeTestMessage(subject) {
    const cat = await ctx.ensureCatalog();
    const test = cat ? (cat.areas || []).flatMap((a) => a.tests || []).find((t) => t.id === subject.test) : null;
    const title = test ? test.title : subject.test;
    const file = test && test.file ? test.file : '';
    const path = file ? (/^\.socrates\//.test(file) ? file : '.socrates/tests/' + file) : '.socrates/tests/' + subject.test + '.md';
    const lines = [
      PROMPT['change-test'],
      '',
      '---',
      'Change the test "' + title + '" (`' + subject.test + '`, in `' + path + '`) of this repository\'s test catalog as said above.',
      '',
      ...checkAndRun({
        write: '3. Change the test file; keep its id (its path) unless the change asks otherwise.',
        what: 'the changed test',
        how: 'tests `["' + subject.test + '"]`',
      }),
    ];
    return {
      text: lines.join('\n'),
      title: 'Change test: ' + title,
      heading: 'Change “' + title + '” with an agent',
      lead: html`<span>${subject.test + ' · ' + path}</span>`,
      caret: PROMPT['change-test'].length,
    };
  }

  const BUILD = { 'fix-bug': fixBugMessage, 'fix-cell': fixCellMessage, 'write-test': writeTestMessage, 'change-test': changeTestMessage };
  const HEADING = { 'fix-bug': 'Fix the bug in a chat', 'fix-cell': 'Fix this cell in a chat', 'write-test': 'Write a test with an agent', 'change-test': 'Change the test with an agent' };

  /* ------------------------------------------------------------ the dialog */

  function frame() {
    return html`<form class="ds-form so-tests__stack" id="chatForm" novalidate>
      <p class="ds-dialog__text so-tests-chat__lead" id="chatLead"></p>
      <div id="chatPickers" class="so-tests__stack"></div>
      <div class="ds-field">
        <label class="ds-field__label" for="chatMessage">First message</label>
        <textarea class="ds-input so-tests-message" id="chatMessage" rows="14" spellcheck="true" aria-describedby="chatMessageHint" readonly></textarea>
        <div class="ds-field__foot">
          <small class="ds-field__hint" id="chatMessageHint">Written from the test runs; change anything before it goes.</small>
          <span class="ds-field__counter" aria-hidden="true" id="chatCount"></span>
        </div>
      </div>
      <div id="chatError"></div>
      <p class="ds-meta so-tests-chat__note">${raw(icon('info', 16))}<span id="chatNote"></span></p>
    </form>`;
  }

  function setError(text) {
    morph($('chatError'), text ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text so-break">${text}</span></div>` : '');
  }

  function counter() {
    const field = $('chatMessage');
    const n = field.value.length;
    const words = n.toLocaleString() + ' characters';
    if ($('chatCount').textContent !== words) $('chatCount').textContent = words;
  }

  // folder is how the chat is told where to work: a preset directory of
  // Admin's when the repository is one, else its path typed in.
  function folder(path) {
    const space = harnesses.workspace();
    const preset = (space.presets || []).find((p) => String(p.path || '').replace(/\/+$/, '') === path.replace(/\/+$/, ''));
    if (preset) return { workdir_mode: 'preset', workdir: preset.path, ok: true };
    return { workdir_mode: 'custom', workdir: path, ok: space.allow_custom !== false };
  }

  async function open(spec, opener) {
    const my = ++gen;
    const subject = spec.subject || {};
    const repo = subject.repo || state.repo;
    const info = ctx.repoOf(repo);
    shown = { spec, repo, path: info ? info.path : '', prefill: '', loading: true, title: '' };
    $('chatDialogTitle').textContent = HEADING[spec.kind] || 'Start a chat';
    morph($('chatBody'), frame());
    $('chatMessage').value = '';
    $('chatMessage').placeholder = 'Writing the message…';
    $('chatStart').setAttribute('aria-disabled', 'true');
    $('chatNote').textContent = 'Start chat makes the chat in the repository’s folder, sends it this message and opens it on this tab.';
    $('chatMessageHint').textContent = PROMPT[spec.kind] ? 'Say on the first line what you want; the rest tells the agent how. Change anything before it goes.'
      : 'Written from the test runs; change anything before it goes.';
    counter();
    DS.overlay.open(dialog, { opener });
    await prepare();
    if (my !== gen) return;
    const harness = defaultProgram();
    const model = harnesses.defaultModelFor(harness) || '';
    drawPickers({ harness, model, effort: defaultEffort(harness, model), account: '' });
    const where = shown.path ? folder(shown.path) : { ok: true };
    if (!where.ok) setError('This Socrates does not allow a chat in a folder that is not a preset: add ' + shown.path + ' to the preset directories in Admin, or allow typed-in folders there.');
    let built = null;
    try {
      const build = BUILD[spec.kind];
      if (!build) throw new Error('There is no chat for ' + spec.kind + '.');
      built = await build(subject);
    } catch (err) {
      if (my !== gen) return;
      shown.loading = false;
      $('chatMessage').placeholder = '';
      setError('The message could not be written: ' + errorMessage(err) + '. Write it yourself, or close and try again.');
      $('chatMessage').readOnly = false;
      $('chatStart').setAttribute('aria-disabled', 'false');
      return;
    }
    if (my !== gen) return;
    Object.assign(shown, { loading: false, prefill: built.text, title: built.title });
    $('chatDialogTitle').textContent = built.heading;
    morph($('chatLead'), built.lead || '');
    const field = $('chatMessage');
    field.readOnly = false;
    field.placeholder = '';
    field.value = built.text;
    counter();
    $('chatStart').setAttribute('aria-disabled', 'false');
    // Where there is something left to write, the cursor waits there.
    const at = built.caret || 0;
    field.focus();
    field.setSelectionRange(at, at);
    field.scrollTop = 0;
  }

  /** missingAsk is what the person still has to write on the first line, '' when nothing. */
  function missingAsk() {
    const prompt = PROMPT[shown.spec.kind];
    if (!prompt) return '';
    const first = $('chatMessage').value.split('\n')[0];
    if (first.startsWith(prompt) && !first.slice(prompt.length).trim()) {
      return shown.spec.kind === 'write-test' ? 'Say on the first line what the test should check.' : 'Say on the first line what to change.';
    }
    return '';
  }

  function chatTitle(text) {
    let title = shown.title;
    const prompt = PROMPT[shown.spec.kind];
    if (shown.spec.kind === 'write-test') {
      const first = text.split('\n')[0];
      const ask = (first.startsWith(prompt) ? first.slice(prompt.length) : first).trim();
      if (ask) title = 'New test: ' + ask;
    }
    return title.length > TITLE_MAX ? title.slice(0, TITLE_MAX - 1) + '…' : title;
  }

  async function start() {
    const button = $('chatStart');
    if (!shown || shown.loading || button.getAttribute('aria-disabled') === 'true' || dialog.hasAttribute('data-ds-busy')) return;
    const field = $('chatMessage');
    const text = field.value;
    const ask = !text.trim() ? 'The message is empty.' : missingAsk();
    if (ask) {
      field.setAttribute('aria-invalid', 'true');
      setError(ask);
      field.focus();
      const at = (PROMPT[shown.spec.kind] || '').length;
      field.setSelectionRange(at, at);
      return;
    }
    field.removeAttribute('aria-invalid');
    setError('');
    const pick = readPick();
    if (!pick.harness) { setError('Pick a program first.'); return; }
    const where = folder(shown.path);
    dialog.setAttribute('data-ds-busy', '');
    const cancel = $('chatCancel');
    cancel.setAttribute('aria-disabled', 'true');
    cancel.setAttribute('data-tip', 'Wait until the chat is made');
    let id = '';
    try {
      await M.busy(button, (async () => {
        const data = await api('/api/chats', {
          method: 'POST', attempts: 2, timeout: 30000,
          body: {
            client_id: clientKey(),
            harness: pick.harness,
            model: pick.model,
            effort: pick.effort,
            account: pick.account || undefined,
            workdir_mode: where.workdir_mode,
            workdir: where.workdir,
            title: chatTitle(text),
            message: { client_id: clientKey(), text },
            tests: true,
          },
        });
        id = data.session.id;
      })(), { done: false });
    } catch (err) {
      dialog.removeAttribute('data-ds-busy');
      cancel.removeAttribute('aria-disabled');
      cancel.removeAttribute('data-tip');
      setError(errorMessage(err));
      return;
    }
    try { localStorage.setItem(PROGRAM_KEY, pick.harness); } catch { /* private window */ }
    // Made: what was written is in the chat now, so leaving loses nothing.
    shown.prefill = text;
    location.assign('/#open=' + encodeURIComponent(id));
  }

  /* -------------------------------------------------------------- wiring */

  dialog.addEventListener('change', (event) => {
    const t = event.target.closest('[data-pick]');
    if (!t || !shown) return;
    const pick = readPick();
    if (t.dataset.pick === 'harness') {
      pick.model = harnesses.defaultModelFor(pick.harness) || '';
      pick.effort = defaultEffort(pick.harness, pick.model);
      pick.account = '';
    } else if (t.dataset.pick === 'model') {
      pick.effort = defaultEffort(pick.harness, pick.model);
    } else if (t.dataset.pick !== 'account') {
      return;
    }
    drawPickers(pick);
  });
  dialog.addEventListener('input', (event) => {
    if (event.target.id !== 'chatMessage') return;
    counter();
    if (event.target.getAttribute('aria-invalid') === 'true' && !missingAsk() && event.target.value.trim()) {
      event.target.removeAttribute('aria-invalid');
      setError('');
    }
  });
  dialog.addEventListener('keydown', (event) => {
    // Ctrl+Enter (⌘+Enter) starts the chat, as a composer sends.
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey) && !event.isComposing) {
      event.preventDefault();
      start();
    }
  });
  $('chatStart').addEventListener('click', start);
  dialog.addEventListener('ds-close', (event) => {
    if (event.target !== dialog) return;
    gen += 1;
    shown = null;
  });

  return { open };
}
