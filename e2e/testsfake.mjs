// The test mode on the Scripted agent (internal/agent/fake_tests.go): the
// QA instance's demo shop (scripts/qa-demo-tests) run four times the way
// `scripts/qa-instance.sh reset` runs it, with every role - the cells, the
// reviewers, the analyst - on the Scripted agent, which costs nothing. The
// cells draw their own screenshots into the evidence, the reviewer corrects
// a pass whose action failed, the analyst keeps a bug's id over the runs;
// run 4 has a regression and a fix against run 3. Its runs are deleted at
// the end.

import { appendFileSync, cpSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { REPO, scratchDir, setup, start, ok, wait } from './harness.mjs';

// The commits qa-instance.sh makes before a run, so that runs differ in
// their commit as they would.
const COMMITS = { 3: 'Cart: recompute the total when the quantity changes', 4: 'Search: rank by sales; profile: wrap long streets' };

// demoShop is a copy of the demo shop as a repository.
function demoShop() {
  const repo = scratchDir('socrates-demo-shop-');
  cpSync(join(REPO, 'scripts', 'qa-demo-tests'), repo, { recursive: true });
  git(repo, 'init', '-q');
  git(repo, 'add', '-A');
  git(repo, 'commit', '-qm', 'The demo shop and its test catalog');
  return repo;
}

function git(repo, ...args) {
  execFileSync('git', ['-C', repo, '-c', 'user.name=Socrates QA', '-c', 'user.email=qa@socrates.invalid', ...args], { stdio: 'ignore' });
}

const SCRIPTED = { harness: 'fake', model: 'fake-small', effort: 'low' };

// What each cell comes to in each run, by the tests' `fake` parameters.
function expected(n) {
  const v = {
    'shop/home': 'pass', 'shop/search': n >= 4 ? 'fail' : 'pass', 'shop/product-page': 'issue',
    'shop/cart': n < 3 ? 'fail' : 'pass', 'shop/checkout': 'pass', 'shop/wishlist': n % 2 ? 'fail' : 'pass',
    'account/sign-in': 'pass', 'account/password-reset': 'blocked', 'account/profile': n < 4 ? 'issue' : 'pass',
    'account/order-history': 'issue',
  };
  return v;
}

export async function testsfake() {
  const s = await start({ viewport: { width: 1280, height: 900 }, env: { SOCRATES_FAKE_AGENT: '1' } });
  const api = s.context.request;
  const headers = { Origin: s.url };
  const get = async (path) => (await api.get(s.url + path)).json();
  const runs = [];
  try {
    await setup(s.page, s.url);
    const repo = demoShop();
    const added = await api.post(s.url + '/api/tests/repos', { data: { path: repo }, headers });
    ok(added.status() === 201, 'the demo shop is added as a test repository', String(added.status()));
    const key = (await added.json()).key;
    const catalog = await get('/api/tests/repos/' + key + '/catalog');
    ok(catalog.errors.length === 0 && catalog.cells === 21 && catalog.areas.length === 2,
      'its catalog reads clean: two areas, 21 cells', catalog.errors.length + ' errors, ' + catalog.cells + ' cells');

    const current = (await get('/api/tests/repos/' + key + '/settings')).settings;
    const put = await api.put(s.url + '/api/tests/repos/' + key + '/settings', {
      data: { ...current, cell: SCRIPTED, reviewer: SCRIPTED, analyst: SCRIPTED, parallel: 8, review: true, analysis: true },
      headers,
    });
    const saved = put.ok() ? (await put.json()).settings : {};
    ok(put.status() === 200 && ['cell', 'reviewer', 'analyst'].every((r) => saved[r].harness === 'fake' && saved[r].model === 'fake-small'),
      'every role may run on the Scripted agent', put.status() + ' ' + JSON.stringify(saved.cell));

    const began = Date.now();
    for (let n = 1; n <= 4; n += 1) {
      if (COMMITS[n]) {
        appendFileSync(join(repo, 'CHANGELOG.md'), '- ' + COMMITS[n] + '\n');
        git(repo, 'commit', '-qam', COMMITS[n]);
      }
      const started = await api.post(s.url + '/api/tests/repos/' + key + '/runs', { data: { name: 'demo ' + n }, headers });
      if (started.status() !== 201) throw new Error('run ' + n + ' did not start: ' + await started.text());
      const id = (await started.json()).id;
      runs.push(id);
      const deadline = Date.now() + 120000;
      let run = {};
      while (Date.now() < deadline) {
        run = await get('/api/tests/runs/' + id);
        if (['done', 'failed', 'stopped'].includes(run.state)) break;
        await wait(250);
      }
      const cells = (await get('/api/tests/runs/' + id + '/cells')).cells || [];
      if (run.state !== 'done') {
        console.log('  cells: ' + cells.map((c) => c.cell_id + ' ' + c.state + ' ' + (c.wait || c.end_reason || '')).join(' | '));
      }
      const want = expected(n);
      const wrong = cells.filter((c) => c.final !== want[c.test]).map((c) => c.cell_id + '=' + c.final + ' (wanted ' + want[c.test] + ')');
      ok(run.state === 'done' && run.number === n && cells.length === 21 && wrong.length === 0,
        `run ${n} ends done with every cell as its fake parameter says`,
        `${run.state}, run ${run.number}, ${cells.length} cells, ${Math.round((run.duration_ms || 0) / 1000)} s; ${wrong.join(', ') || 'all as wanted'}`);
      if (n === 1) {
        const cell = cells.find((c) => c.cell_id.startsWith('shop/cart@phone'));
        const detail = await get('/api/tests/runs/' + id + '/attempts/' + cell.id);
        ok(cell.shots === 2 && detail.steps.every((st) => st.actions.length === 1) && detail.actions[0].screenshot === 'step-1.png'
          && detail.steps[0].verdict === 'fail', 'a cell has a screenshot of its own per step as the steps\' evidence',
        JSON.stringify(detail.actions[0]));
        const file = await api.get(s.url + '/api/tests/runs/' + id + '/attempts/' + cell.id + '/files/step-1.png');
        const body = await file.body();
        ok(file.status() === 200 && body.subarray(1, 4).toString() === 'PNG', 'its screenshot is served as a PNG',
          file.status() + ' ' + body.length + ' bytes');
        const misjudged = cells.find((c) => c.test === 'account/order-history');
        ok(misjudged.verdict === 'pass' && misjudged.reviewed === 'issue' && misjudged.review === 'done',
          'the reviewer turned a pass whose action failed into an issue', JSON.stringify(misjudged));
        const blocked = cells.find((c) => c.test === 'account/password-reset');
        ok(blocked.attempt === 2, 'the blocked cell was tried again', String(blocked.attempt));
        const bugs = await get('/api/tests/runs/' + id + '/bugs');
        ok(bugs.bugs.length === 5 && bugs.bugs.every((b) => /^B-\d+$/.test(b.id)) && bugs.conclusions.includes('## Areas'),
          'the analyst wrote a bug per test that failed or has an issue, and the conclusions',
          bugs.bugs.map((b) => b.id + ' ' + b.severity + ' ' + b.title).join('; '));
      }
      if (n === 4) {
        const row = (test) => cells.find((c) => c.test === test);
        ok(run.changes.regressed >= 1 && row('shop/search').change === 'regressed' && row('account/profile').change === 'fixed',
          'run 4 has the search\'s regression and the profile\'s fix', JSON.stringify(run.changes));
        const commits = new Set();
        for (const r of runs) commits.add((await get('/api/tests/runs/' + r)).commit);
        ok(commits.size === 3, 'the runs record the commits between them', String(commits.size));
      }
    }
    const elapsed = Math.round((Date.now() - began) / 1000);
    ok(elapsed < 100, 'the four runs take less than the reset may', elapsed + ' s');
    const repoBugs = (await get('/api/tests/repos/' + key + '/bugs')).bugs;
    const status = Object.fromEntries(repoBugs.map((b) => [b.cells[0].split('@')[0], b.status]));
    ok(repoBugs.length === 6 && status['shop/cart'] === 'fixed' && status['shop/search'] === 'open' && status['shop/product-page'] === 'open',
      'the bugs over the runs: one kept open, one fixed, one new', JSON.stringify(status));
    const sessions = (await get('/api/sessions')).sessions;
    ok(sessions.length === 0, 'the test sessions are not in the session list', String(sessions.length));
  } finally {
    for (const id of runs) await api.delete(s.url + '/api/tests/runs/' + id, { headers }).catch(() => {});
    await s.stop();
  }
}
