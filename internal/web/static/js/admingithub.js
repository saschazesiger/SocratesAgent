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
// choose its repositories on GitHub) and the repositories they grant, one
// short line each: a filter above them once there are many, and the first
// few until "Show all". A repository's setup script is edited in a dialog
// and saved on its own (PUT /api/github/repos/{owner}/{name}/settings), not
// with the page.
//
// The App is public, so it can be installed on an organisation too ("Add an
// organisation" goes through /api/github/app/install, which adds a state of
// the server's own, to GitHub's install page, where the account is picked).
// Anyone may install a public App, so an installation counts once it is
// accepted: coming back signed in with that state accepts it, and one
// that is installed but not accepted waits on the card with Accept and
// Remove (POST /api/github/installations/{id}/accept, DELETE
// /api/github/installations/{id}, which uninstalls it on GitHub).
//
// An App created before Socrates made them public is private, and GitHub
// installs a private App only on its owner's account: its install page would
// only lead to the existing installation. GET /api/github says so (`public`:
// false; null when GitHub did not say), and then "Add an organisation" opens
// an explanation with the App's Advanced settings, where it is made public,
// and "Check again" instead of going to GitHub.

import { api, el, toast, errorMessage, confirmDialog, openDialog } from './api.js';
import { M, html, raw, icon, morph, faces } from './ui.js';

const $ = (id) => document.getElementById(id);

let hooks = { save: async () => false, dirty: () => false };
let view = null;
let loadError = '';
// The repositories' filter, and whether all of them are shown.
let filter = '';
let showAll = false;
// Whether the explanation of a private App is open ("Add an organisation").
let privateOpen = false;

// More repositories than FILTER_FROM get a filter; without one typed, the
// first SHOW_FIRST are shown until "Show all".
const FILTER_FROM = 6;
const SHOW_FIRST = 8;

/**
 * mountGitHub wires the card once. `save` is Admin's Save (resolves true
 * when it saved), `dirty` whether the page has changes Save has not written.
 */
export function mountGitHub({ save, dirty }) {
  hooks = { save, dirty };
  const card = $('github');
  if (!card) return;
  card.addEventListener('click', onAction);
  card.addEventListener('input', (event) => {
    if (event.target.id !== 'githubFilter') return;
    filter = event.target.value;
    const list = $('githubRepoList');
    if (list) morph(list, repoListHtml());
  });
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
  const waiting = (view.pending || []).length;
  if (waiting) facts.push(waiting + ' waiting to be accepted');
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
  const pending = view.pending || [];
  const repos = view.repos || [];
  // Only a private App's own owner can install it: "Add an organisation"
  // explains instead of going to GitHub.
  const isPrivate = view.public === false;
  return html`<div class="so-gh-part" data-ds-key="app">
    <h3 class="ds-card__subtitle">App</h3>
    <div class="so-rows">
      <div class="so-gh" data-ds-key="app-row">
        <div class="so-gh__main">
          <p class="so-gh__name"><a href="${app.html_url}" target="_blank" rel="noopener">${app.name || app.slug}</a></p>
          <p class="ds-meta">Owned by ${app.owner}</p>
        </div>
        <div class="so-gh__actions">
          ${isPrivate
    ? html`<button class="ds-btn ds-btn--text ds-sm" type="button" data-action="add-org" aria-expanded="${privateOpen ? 'true' : 'false'}" aria-controls="githubPrivate">${raw(icon('plus', 16))}<span>Add an organisation</span></button>`
    : html`<a class="ds-btn ds-btn--text ds-sm" href="${app.install_url}" target="_blank" rel="noopener">${raw(icon('external', 16))}<span>Add an organisation</span></a>`}
          <button class="ds-btn ds-sm" type="button" data-action="disconnect">${faces('Disconnect', 'Disconnecting')}</button>
        </div>
      </div>
    </div>
    ${isPrivate && privateOpen ? html`<div class="ds-alert ds-tone-warning" id="githubPrivate" data-ds-key="private">${raw(icon('alert', 20))}<div class="ds-alert__text"><p>Your App is private, and GitHub installs a private App only on its owner's account. Make it public once: App settings → Advanced → Make public. Socrates still uses only installations you accept.</p></div><div class="ds-alert__actions"><a class="ds-btn ds-btn--text ds-sm" href="${app.advanced_url}" target="_blank" rel="noopener">${raw(icon('external', 16))}<span>Open Advanced settings</span></a><button class="ds-btn ds-sm" type="button" data-action="check-public">${faces('Check again', 'Checking')}</button></div></div>` : ''}
    ${view.public == null ? html`<p class="ds-meta" data-ds-key="public-note">Add an organisation installs the App there; GitHub asks which account. That needs the App to be public. An App created before Socrates made them public is private: make it public once in <a href="${app.advanced_url}" target="_blank" rel="noopener">its settings → Advanced → Make public</a>.</p>` : ''}
  </div>
  ${pending.length ? html`<div class="so-gh-part" data-ds-key="pending">
    <h3 class="ds-card__subtitle">Waiting to be accepted</h3>
    ${pending.map((one) => html`<div class="ds-alert ds-tone-warning" data-ds-key="${'pending-' + one.id}" data-installation="${one.id}">${raw(icon('alert', 20))}<div class="ds-alert__text"><p class="ds-alert__title">Installed on ${one.account} — accept only if that is yours</p><p>Anyone can install the App on their own account. Socrates uses its repositories only once you accept it; Remove uninstalls it there.</p></div><div class="ds-alert__actions"><button class="ds-btn ds-btn--text ds-sm" type="button" data-action="remove-install">${faces('Remove', 'Removing')}</button><button class="ds-btn ds-sm" type="button" data-action="accept-install">${faces('Accept', 'Accepting')}</button></div></div>`)}
  </div>` : ''}
  <div class="so-gh-part" data-ds-key="installs">
    <h3 class="ds-card__subtitle">Installations</h3>
    <div class="so-rows">${installs.length ? installs.map((one) => html`<div class="so-gh" data-ds-key="${'install-' + one.id}" data-installation="${one.id}">
        <div class="so-gh__main">
          <p class="so-gh__name">${one.account}<span class="ds-tag ds-tone-neutral">${one.type === 'Organization' ? 'Organisation' : 'Personal'}</span></p>
        </div>
        <div class="so-gh__actions">
          <a class="ds-btn ds-btn--text ds-sm" href="${one.settings_url}" target="_blank" rel="noopener">${raw(icon('external', 16))}<span>Choose repositories</span></a>
          <button class="ds-btn ds-btn--text ds-sm" type="button" data-action="remove-install">${faces('Remove', 'Removing')}</button>
        </div>
      </div>`) : html`<p class="ds-meta" data-ds-key="no-installs">The App is not installed anywhere yet. <a href="${app.install_url}" target="_blank" rel="noopener">Install it</a> and choose the repositories it may reach.</p>`}</div>
  </div>
  <div class="so-gh-part" data-ds-key="repos">
    <h3 class="ds-card__subtitle">Repositories</h3>
    <p class="ds-muted">Each new chat in a repository gets its own worktree and branch. A setup script runs there with <code>sh -e</code> before the agent starts, for example <code>npm ci</code>; without one, an executable <code>.socrates/setup</code> in the repository runs.</p>
    ${repos.length > FILTER_FROM ? html`<div class="ds-search so-gh__filter" data-ds-key="filter">${raw(icon('search', 20))}<input class="ds-input" type="search" id="githubFilter" value="${filter}" placeholder="Filter repositories" aria-label="Filter repositories" aria-controls="githubRepoList" autocomplete="off" spellcheck="false" autocapitalize="off" enterkeyhint="search"></div>` : ''}
    <div id="githubRepoList" class="so-gh-list" data-ds-key="repo-list">${repoListHtml()}</div>
  </div>
  <p class="ds-meta" data-ds-key="foot">Disconnect forgets the App's keys on this machine; the setup scripts stay. The App itself stays on GitHub until you delete it in <a href="${app.settings_url}" target="_blank" rel="noopener">its settings</a>.</p>`;
}

// sortedRepos is the repositories with a setup script first, then the
// others, each part by name.
function sortedRepos() {
  const set = (repo) => (repo.setup && repo.setup.trim() ? 0 : 1);
  return [...((view && view.repos) || [])].sort((a, b) => set(a) - set(b)
    || a.full_name.localeCompare(b.full_name, undefined, { sensitivity: 'base' }));
}

// repoListHtml is the rows the filter and "Show all" leave, and the toggle.
function repoListHtml() {
  const all = sortedRepos();
  if (!all.length) return html`<p class="ds-meta" data-ds-key="no-repos">No repositories yet. Choose them under an installation above, then refresh.</p>`;
  const words = all.length > FILTER_FROM ? filter.trim().toLowerCase() : '';
  const matching = words ? all.filter((repo) => repo.full_name.toLowerCase().includes(words)) : all;
  const cut = !words && !showAll && matching.length > SHOW_FIRST;
  const shown = cut ? matching.slice(0, SHOW_FIRST) : matching;
  const toggle = !words && matching.length > SHOW_FIRST
    ? html`<div class="ds-card__more" data-ds-key="more"><button class="ds-btn ds-btn--text" type="button" data-action="more" aria-expanded="${showAll ? 'true' : 'false'}" aria-controls="githubRepoList">${showAll ? 'Show fewer' : `Show all ${matching.length}`}</button></div>`
    : '';
  return html`<div class="so-rows" data-ds-key="rows">${shown.length ? shown.map(repoHtml) : html`<p class="ds-meta" data-ds-key="no-match">No repository matches “${filter.trim()}”.</p>`}</div>${toggle}`;
}

function repoHtml(repo) {
  const hasSetup = !!(repo.setup && repo.setup.trim());
  return html`<div class="so-gh so-gh--repo" data-ds-key="${'repo-' + repo.full_name}" data-repo="${repo.full_name}">
    <p class="so-gh__name">${repo.full_name}${repo.private ? html`<span class="ds-tag ds-tone-neutral">Private</span>` : ''}${hasSetup ? html`<span class="ds-tag ds-tone-success">Setup set</span>` : ''}</p>
    <div class="so-gh__actions">
      <button class="ds-btn ds-btn--text ds-sm" type="button" data-action="edit-setup" aria-label="${'Setup of ' + repo.full_name}">${raw(icon('pencil', 16))}<span>Setup</span></button>
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
  else if (action === 'edit-setup') editSetup(button);
  else if (action === 'accept-install') acceptInstall(button);
  else if (action === 'remove-install') removeInstall(button);
  else if (action === 'refresh') refresh(true, button);
  else if (action === 'add-org') {
    privateOpen = !privateOpen;
    render();
  } else if (action === 'check-public') checkPublic(button);
  else if (action === 'more') {
    showAll = !showAll;
    const list = $('githubRepoList');
    if (list) morph(list, repoListHtml());
  }
}

// checkPublic asks GitHub again whether the App is public, after the person
// made it public in its settings.
async function checkPublic(button) {
  await refresh(true, button);
  if (loadError || !view || !view.connected) return;
  if (view.public === true) {
    privateOpen = false;
    toast('The App is public now: Add an organisation installs it there.', 'success');
    const add = document.querySelector('#github .so-gh__actions a[href="' + view.app.install_url + '"]');
    if (add) add.focus();
  } else if (view.public === false) {
    toast('GitHub still says the App is private.');
  } else {
    toast('GitHub did not say whether the App is public. Try again in a moment.', 'error');
  }
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

// editSetup opens a repository's setup script in a dialog; Save keeps it on
// the server at once, an empty script removes it.
async function editSetup(button) {
  const row = button.closest('[data-repo]');
  if (!row || !view) return;
  const fullName = row.dataset.repo;
  const repo = (view.repos || []).find((one) => one.full_name === fullName);
  if (!repo) return;
  const id = 'githubSetupText';
  const text = el('textarea', {
    class: 'ds-input ds-mono', id, rows: 10, autofocus: true, placeholder: 'npm ci',
    spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off', 'aria-describedby': id + 'Hint',
  });
  text.value = repo.setup || '';
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-ds-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--filled', type: 'button' });
  morph(accept, faces('Save setup', 'Saving'));
  const { dialog, shown } = openDialog({
    title: 'Setup of ' + fullName,
    body: 'Runs with sh -e in each new chat’s worktree, on a branch made from ' + (repo.default_branch || 'the default branch')
      + ', before the agent starts.',
    field: el('div', { class: 'ds-form' }, el('div', { class: 'ds-field' },
      el('label', { class: 'ds-field__label', for: id, text: 'Setup script' }),
      text,
      el('small', { class: 'ds-field__hint', id: id + 'Hint', text: 'For example npm ci, or copying an .env. Empty removes it; then an executable .socrates/setup in the repository runs.' }))),
    actions: [cancel, accept],
    opener: button,
  });
  dialog.classList.add('ds-dialog--lg');
  const [owner, name] = fullName.split('/');
  const path = '/api/github/repos/' + encodeURIComponent(owner) + '/' + encodeURIComponent(name) + '/settings';
  accept.addEventListener('click', async () => {
    try {
      const saved = await M.busy(accept, api(path, { method: 'PUT', attempts: 3, body: { setup: text.value } }), { done: false });
      dialog.answer(saved);
      DS.overlay.close(dialog);
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
  });
  const saved = await shown;
  if (!saved) return;
  repo.setup = saved.setup;
  render();
  toast(saved.setup ? 'The setup of ' + fullName + ' is saved.' : 'The setup of ' + fullName + ' is removed.', 'success');
}

function installOf(button) {
  const holder = button.closest('[data-installation]');
  if (!holder || !view) return null;
  const id = Number(holder.dataset.installation);
  return [...(view.installations || []), ...(view.pending || [])].find((one) => one.id === id) || null;
}

async function acceptInstall(button) {
  const one = installOf(button);
  if (!one) return;
  try {
    view = await M.busy(button, api('/api/github/installations/' + one.id + '/accept', { method: 'POST' }), { done: false });
    loadError = '';
    render();
    toast('Socrates now uses the repositories of ' + one.account + '.', 'success');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function removeInstall(button) {
  const one = installOf(button);
  if (!one) return;
  const yes = await confirmDialog({
    title: 'Remove the App from ' + one.account + '?',
    body: 'This uninstalls the App from ' + one.account + ' on GitHub, and new chats cannot work in its repositories any more. Chats already there keep their worktrees but cannot push. Installing it again brings them back.',
    confirmLabel: 'Uninstall from ' + one.account, danger: true, opener: button,
  });
  if (!yes) return;
  try {
    view = await M.busy(button, api('/api/github/installations/' + one.id, { method: 'DELETE' }), { done: false });
    loadError = '';
    render();
    toast('The App is removed from ' + one.account + '.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}
