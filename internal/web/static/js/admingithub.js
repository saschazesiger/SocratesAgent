// admingithub.js: Admin's card "GitHub" (docs/design/REPOS.md §3).
//
// Chats work in GitHub repositories through a GitHub App of this Socrates'
// own. Without one the card offers Connect: the server hands out a manifest
// and the address of GitHub's "new App" page (POST /api/github/app/start),
// and the page posts the manifest there as a form, so that the person's own
// GitHub login creates the App. GitHub sends the browser back to the server
// (/api/github/app/created), which keeps the App and sends it on to install
// it; the installation comes back to /admin#github. A problem on the way
// comes back as /admin?github_error=<words>#github.
//
// With an App the card shows it, its installations (each with a link to
// choose its repositories on GitHub) and the repositories they grant, each
// with its setup script, which is saved on its own (PUT
// /api/github/repos/{owner}/{name}/settings), not with the page.

import { api, el, toast, errorMessage, confirmDialog } from './api.js';
import { M, html, raw, icon, morph, faces } from './ui.js';

const $ = (id) => document.getElementById(id);

let hooks = { save: async () => false, dirty: () => false };
let view = null;
let loadError = '';

/**
 * mountGitHub wires the card once. `save` is Admin's Save (resolves true
 * when it saved), `dirty` whether the page has changes Save has not written.
 */
export function mountGitHub({ save, dirty }) {
  hooks = { save, dirty };
  const card = $('github');
  if (!card) return;
  card.addEventListener('click', onAction);
  // Enter in the organisation field is Connect.
  card.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && event.target.id === 'githubOrg') {
      event.preventDefault();
      const button = card.querySelector('button[data-action="connect"]');
      if (button) connect(button);
    }
  });
  cameBack();
  refresh(false);
}

// cameBack says what went wrong on the way through GitHub and takes the words
// out of the address, so a reload does not say it again.
function cameBack() {
  const query = new URLSearchParams(location.search);
  const problem = query.get('github_error');
  if (problem === null) return;
  toast(problem, 'error');
  query.delete('github_error');
  const rest = query.toString();
  history.replaceState(null, '', location.pathname + (rest ? '?' + rest : '') + (location.hash || '#github'));
}

/** refresh asks the server what GitHub grants; `fresh` has it ask GitHub now. */
export async function refresh(fresh, button = null) {
  const work = api('/api/github' + (fresh ? '?refresh=1' : ''));
  try {
    view = button ? await M.busy(button, work, { done: false }) : await work;
    loadError = '';
  } catch (err) {
    loadError = errorMessage(err);
  }
  render();
}

/* ------------------------------------------------------------- render */

function plural(n, one, many) {
  return n + ' ' + (n === 1 ? one : many);
}

function statusLine() {
  if (!view) {
    if (loadError) return html`<span class="ds-status ds-tone-error" data-state="error">${raw(icon('alertCircle', 16))}<span>Unknown</span></span>`;
    return html`<span class="ds-status"><span class="ds-spinner ds-status__spinner" aria-hidden="true"></span><span>Loading</span></span>`;
  }
  if (!view.connected) {
    if (view.problem) return html`<span class="ds-status ds-tone-error" data-state="broken">${raw(icon('alertCircle', 16))}<span>Not usable</span></span>`;
    return html`<span class="ds-status" data-state="off">${raw(icon('ban', 16))}<span>Not connected</span></span>`;
  }
  const facts = [plural(view.installations.length, 'installation', 'installations'), plural(view.repos.length, 'repository', 'repositories')];
  const chip = view.problem
    ? html`<span class="ds-status ds-tone-warning" data-state="problem">${raw(icon('alert', 16))}<span>Connected, GitHub not answering</span></span>`
    : html`<span class="ds-status ds-tone-success" data-state="connected">${raw(icon('check', 16))}<span>Connected</span></span>`;
  return html`${chip}<span class="ds-parts"><span class="ds-parts__list">${facts.map((f) => html`<span>${f}</span>`)}</span></span>`;
}

function render() {
  const status = $('githubStatus');
  const body = $('githubBody');
  if (!status || !body) return;
  morph(status.querySelector('.so-state__line'), statusLine());
  $('githubRefresh').hidden = !(view && view.connected);
  const problem = loadError || (view && view.problem) || '';
  const alert = problem
    ? html`<div class="ds-alert ds-tone-error" role="alert" data-ds-key="problem">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${problem}</span></div>`
    : '';
  if (!view) {
    morph(body, html`${alert}`);
    return;
  }
  morph(body, view.connected ? html`${alert}${connectedHtml()}` : html`${alert}${connectHtml()}`);
}

function connectHtml() {
  return html`<div class="ds-form" data-ds-key="connect">
    <div class="ds-field">
      <label class="ds-field__label" for="githubOrg">Organisation</label>
      <input class="ds-input" type="text" id="githubOrg" spellcheck="false" autocomplete="off" autocapitalize="off" placeholder="Your own account" aria-describedby="githubOrgHint">
      <small class="ds-field__hint" id="githubOrgHint">Empty creates the App on your own GitHub account. An organisation's name creates it there, which needs you to be one of its owners.</small>
    </div>
    <div class="ds-form-actions">
      <button class="ds-btn" type="button" data-action="connect">${faces('Connect GitHub', 'Opening GitHub')}</button>
      <p class="ds-meta">GitHub asks you to confirm the App's name, then to choose the repositories it may reach. Socrates keeps the App's private key encrypted on this machine.</p>
    </div>
  </div>`;
}

function connectedHtml() {
  const app = view.app || {};
  const installs = view.installations || [];
  const repos = view.repos || [];
  return html`<div class="so-gh-part" data-ds-key="app">
    <h3 class="ds-card__subtitle">App</h3>
    <div class="so-rows">
      <div class="so-gh" data-ds-key="app-row">
        <div class="so-gh__main">
          <p class="so-gh__name"><a href="${app.html_url}" target="_blank" rel="noopener">${app.name || app.slug}</a></p>
          <p class="ds-meta">Owned by ${app.owner}</p>
        </div>
        <div class="so-gh__actions">
          <a class="ds-btn ds-btn--text ds-sm" href="${app.install_url}" target="_blank" rel="noopener">${raw(icon('external', 16))}<span>Install on another account</span></a>
          <button class="ds-btn ds-sm" type="button" data-action="disconnect">${faces('Disconnect', 'Disconnecting')}</button>
        </div>
      </div>
    </div>
  </div>
  <div class="so-gh-part" data-ds-key="installs">
    <h3 class="ds-card__subtitle">Installations</h3>
    <div class="so-rows">${installs.length ? installs.map((one) => html`<div class="so-gh" data-ds-key="${'install-' + one.id}">
        <div class="so-gh__main">
          <p class="so-gh__name">${one.account}<span class="ds-tag ds-tone-neutral">${one.type === 'Organization' ? 'Organisation' : 'Personal'}</span></p>
        </div>
        <div class="so-gh__actions">
          <a class="ds-btn ds-sm" href="${one.settings_url}" target="_blank" rel="noopener">${raw(icon('external', 16))}<span>Choose repositories</span></a>
        </div>
      </div>`) : html`<p class="ds-meta" data-ds-key="no-installs">The App is not installed anywhere yet. <a href="${app.install_url}" target="_blank" rel="noopener">Install it</a> and choose the repositories it may reach.</p>`}</div>
  </div>
  <div class="so-gh-part" data-ds-key="repos">
    <h3 class="ds-card__subtitle">Repositories</h3>
    <p class="ds-muted">Each new chat in a repository gets its own worktree and branch. A setup script runs there with <code>sh -e</code> before the agent starts, for example <code>npm ci</code>; without one, an executable <code>.socrates/setup</code> in the repository runs.</p>
    <div class="so-rows">${repos.length ? repos.map((repo, i) => repoHtml(repo, i)) : html`<p class="ds-meta" data-ds-key="no-repos">No repositories yet. Choose them under an installation above, then refresh.</p>`}</div>
  </div>
  <p class="ds-meta" data-ds-key="foot">Disconnect forgets the App's keys on this machine; the setup scripts stay. The App itself stays on GitHub until you delete it in <a href="${app.settings_url}" target="_blank" rel="noopener">its settings</a>.</p>`;
}

function repoHtml(repo, i) {
  const id = 'githubSetup' + i;
  return html`<div class="so-gh" data-ds-key="${'repo-' + repo.full_name}" data-repo="${repo.full_name}">
    <div class="so-gh__main">
      <p class="so-gh__name">${repo.full_name}<span class="ds-tag ds-tone-neutral">${repo.private ? 'Private' : 'Public'}</span></p>
      <p class="ds-meta">Default branch ${repo.default_branch}</p>
    </div>
    <div class="ds-field so-gh__setup">
      <label class="ds-field__label" for="${id}">Setup</label>
      <textarea class="ds-input" id="${id}" rows="3" spellcheck="false" autocomplete="off" autocapitalize="off" placeholder="npm ci">${repo.setup || ''}</textarea>
    </div>
    <div class="so-gh__actions so-gh__save">
      <button class="ds-btn ds-sm" type="button" data-action="save-setup">${faces('Save setup', 'Saving', 'Saved')}</button>
    </div>
  </div>`;
}

/* ------------------------------------------------------------ actions */

function onAction(event) {
  const button = event.target.closest('button[data-action]');
  if (!button) return;
  const action = button.dataset.action;
  if (action === 'connect') connect(button);
  else if (action === 'disconnect') disconnect(button);
  else if (action === 'save-setup') saveSetup(button);
  else if (action === 'refresh') refresh(true, button);
}

// connect posts the manifest to GitHub. That leaves the page, so changes
// that are not saved yet are saved first.
async function connect(button) {
  if (hooks.dirty()) {
    const yes = await confirmDialog({
      title: 'Save the changes first?',
      body: 'Connecting takes this page to GitHub, and changes that are not saved yet would be lost on the way. Socrates saves them, then connects.',
      confirmLabel: 'Save and connect',
      opener: button,
    });
    if (!yes || !await hooks.save()) return;
  }
  const org = ($('githubOrg') || { value: '' }).value.trim();
  try {
    const start = await M.busy(button, api('/api/github/app/start', { method: 'POST', body: { org } }), { done: false });
    if (!start || !start.action) throw new Error('The server gave no address to create the App at.');
    const form = el('form', { method: 'post', action: start.action, hidden: true },
      el('input', { type: 'hidden', name: 'manifest', value: JSON.stringify(start.manifest) }));
    document.body.append(form);
    form.submit();
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function disconnect(button) {
  const yes = await confirmDialog({
    title: 'Disconnect GitHub?',
    body: 'Socrates forgets the App and its keys on this machine, and new chats cannot work in a repository until GitHub is connected again. The setup scripts stay. The App itself stays on GitHub until you delete it there.',
    confirmLabel: 'Disconnect', danger: true, opener: button,
  });
  if (!yes) return;
  try {
    view = await M.busy(button, api('/api/github/app', { method: 'DELETE' }), { done: false });
    loadError = '';
    render();
    toast('GitHub is disconnected.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function saveSetup(button) {
  const row = button.closest('[data-repo]');
  if (!row) return;
  const fullName = row.dataset.repo;
  const [owner, name] = fullName.split('/');
  const setup = row.querySelector('textarea').value;
  const path = '/api/github/repos/' + encodeURIComponent(owner) + '/' + encodeURIComponent(name) + '/settings';
  try {
    const saved = await M.busy(button, api(path, { method: 'PUT', attempts: 3, body: { setup } }));
    const repo = view && (view.repos || []).find((one) => one.full_name === fullName);
    if (repo) repo.setup = saved.setup;
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}
