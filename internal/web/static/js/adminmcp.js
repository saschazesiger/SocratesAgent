// adminmcp.js: Admin's card "MCP servers".
//
// Two kinds of server live here. Those built into Socrates (BrowserStack's
// real devices, internal/mcpserver) are off in chats until switched on, and
// keep accounts of their own that act at once (POST/DELETE
// /api/mcp/builtin/...). The person's own servers - a local command, or a
// remote endpoint over HTTP or SSE - are part of the settings document: the
// card edits a copy (servers) that Admin's Save writes with the rest, as it
// does the presets, and so is whether each built-in server is on in every
// chat (settings.mcp.builtin).
//
// Beside the settings the card shows what the server knows about each one
// (GET /api/mcp/overview): the last test of every server, the sign-in of
// those that use OAuth, and the built-in servers with their accounts and
// what they have in use. A test connects to a server as an agent would
// (POST /api/mcp/test) and says what it offers; its tools can be switched
// off one by one (disabled_tools, saved with the page).
//
// An OAuth sign-in leaves the page for the authorization server and comes
// back to /admin?mcp_connected=<name> (or ?mcp_error=<words>), so it asks for
// unsaved changes to be saved first.

import { api, el, toast, errorMessage, confirmDialog, openDialog } from './api.js';
import { DS, M, html, raw, icon, iconEl, morph, flipMorph, faces } from './ui.js';
import * as V from './mcpview.js';

const $ = (id) => document.getElementById(id);

const TYPES = [['stdio', 'Local command (stdio)'], ['http', 'Remote, streamable HTTP'], ['sse', 'Remote, SSE']];
const NAME = /^[A-Za-z0-9_-]+$/;

// GALLERY is the starting points "Add an MCP server" offers: common servers,
// prefilled and editable before they are added.
const GALLERY = [
  { id: 'context7', title: 'Context7', what: 'Current docs and code examples for libraries', server: {
    name: 'context7', type: 'http', url: 'https://mcp.context7.com/mcp', description: 'Current docs and code examples for libraries.' } },
  { id: 'linear', title: 'Linear', what: 'Issues, projects and cycles', server: {
    name: 'linear', type: 'http', url: 'https://mcp.linear.app/mcp', auth: 'oauth', description: 'Linear: issues, projects and cycles.' } },
  { id: 'notion', title: 'Notion', what: 'Pages and databases of a workspace', server: {
    name: 'notion', type: 'http', url: 'https://mcp.notion.com/mcp', auth: 'oauth', description: 'Notion: pages and databases.' } },
  { id: 'sentry', title: 'Sentry', what: 'Errors, issues and releases', server: {
    name: 'sentry', type: 'http', url: 'https://mcp.sentry.dev/mcp', auth: 'oauth', description: 'Sentry: errors, issues and releases.' } },
  { id: 'atlassian', title: 'Atlassian', what: 'Jira and Confluence', server: {
    name: 'atlassian', type: 'sse', url: 'https://mcp.atlassian.com/v1/sse', auth: 'oauth', description: 'Jira and Confluence.' } },
  { id: 'cloudflare-docs', title: 'Cloudflare docs', what: 'Cloudflare’s documentation', server: {
    name: 'cloudflare-docs', type: 'http', url: 'https://docs.mcp.cloudflare.com/mcp', description: 'Cloudflare’s documentation.' } },
  { id: 'playwright', title: 'Playwright', what: 'A browser the agent drives, on this machine', server: {
    name: 'playwright', type: 'stdio', command: 'npx', args: ['@playwright/mcp@latest'], description: 'A browser the agent drives (Playwright), on this machine.' } },
  { id: 'github', title: 'GitHub', what: 'Repositories, issues and pull requests, with a token', server: {
    name: 'github', type: 'http', url: 'https://api.githubcopilot.com/mcp/', headers: { Authorization: 'Bearer ${GITHUB_TOKEN}' },
    description: 'GitHub: repositories, issues and pull requests.' } },
];

let hooks = { save: async () => false, dirty: () => false };
let servers = [];
let builtinChoice = new Map();
let overview = { builtin: [], tests: {}, oauth: {}, redirect_uri: '' };
let overviewError = '';
let overviewLoaded = false;
const testing = new Set();
let sheetOf = null; // { kind: 'builtin' | 'server', name }
let sheetTimer = 0;
// The disclosures of the sheet the person opened or shut, by key: a
// re-render keeps them so (morph drops an `open` its markup lacks).
const sheetOpen = new Map();
const isOpen = (key, dflt = false) => (sheetOpen.has(key) ? sheetOpen.get(key) : dflt);
const openAttr = (key, dflt = false) => (isOpen(key, dflt) ? raw(' open') : '');

const byName = (a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);

/**
 * mountMCP wires the card once. `save` is Admin's Save (resolves true when
 * it saved), `dirty` whether the page has changes Save has not written.
 */
export function mountMCP({ save, dirty }) {
  hooks = { save, dirty };
  $('mcpBuiltin').addEventListener('change', onBuiltinSwitch);
  $('mcpBuiltin').addEventListener('click', (event) => {
    const button = event.target.closest('button[data-action="manage"]');
    if (button) openSheet({ kind: 'builtin', name: button.closest('[data-builtin]').dataset.builtin }, button);
  });
  $('mcpServers').addEventListener('change', (event) => {
    const server = event.target.matches('input[data-action="default"]') ? serverOf(event.target) : null;
    if (server) server.default = event.target.checked;
  });
  $('mcpServers').addEventListener('click', onServerAction);
  $('mcpAdd').addEventListener('click', (event) => addServer(event.currentTarget));
  $('mcpImport').addEventListener('click', (event) => importServers(event.currentTarget));
  const sheet = $('mcpSheet');
  sheet.addEventListener('click', onSheetAction);
  sheet.addEventListener('change', onSheetChange);
  sheet.addEventListener('toggle', (event) => {
    const box = event.target;
    if (box instanceof HTMLDetailsElement && box.dataset.dsKey) sheetOpen.set(box.dataset.dsKey, box.open);
  }, true);
  sheet.addEventListener('ds-close', (event) => {
    if (event.target !== sheet) return;
    sheetOf = null;
    clearInterval(sheetTimer);
  });
  cameBack();
  refresh();
}

/** load takes the servers and the built-in choices from saved settings. */
export function load(settings) {
  const mcp = (settings && settings.mcp) || {};
  servers = (mcp.servers || []).map((server) => ({ ...server })).sort(byName);
  builtinChoice = new Map((mcp.builtin || []).map((one) => [one.name, !!one.default]));
  render();
}

/** collect is settings.mcp as the card has it now, for Save. */
export function collect() {
  const out = { servers: servers.map((server) => ({ ...server })) };
  if (builtinChoice.size) {
    out.builtin = [...builtinChoice].map(([name, on]) => ({ name, default: on })).sort(byName);
  }
  return out;
}

/** refresh asks the server again what it knows about the servers. */
export async function refresh() {
  try {
    overview = { builtin: [], tests: {}, oauth: {}, ...(await api('/api/mcp/overview')) };
    overviewError = '';
  } catch (err) {
    overviewError = errorMessage(err);
  }
  overviewLoaded = true;
  render();
}

// cameBack says how an OAuth sign-in ended and takes its words out of the
// address, so a reload does not say it again.
function cameBack() {
  const query = new URLSearchParams(location.search);
  const name = query.get('mcp_connected');
  const problem = query.get('mcp_error');
  if (name === null && problem === null) return;
  if (name !== null) toast(name + ' is connected. Chats get its tools from their next message.', 'success');
  if (problem !== null) toast('The sign-in did not work: ' + problem, 'error');
  query.delete('mcp_connected');
  query.delete('mcp_error');
  const rest = query.toString();
  history.replaceState(null, '', location.pathname + (rest ? '?' + rest : '') + (location.hash || '#mcpCard'));
}

/* ------------------------------------------------------------- render */

function render() {
  renderBuiltin();
  renderServers();
  renderSheet();
}

function builtinOn(view) {
  return builtinChoice.has(view.name) ? builtinChoice.get(view.name) : !!view.default;
}

// accountsState is a built-in server's accounts in one chip.
function accountsState(view) {
  const list = view.accounts || [];
  if (!(view.account_fields || []).length) return { kind: 'ok', label: 'Ready', state: 'ready' };
  if (!list.length) return { kind: 'warning', label: 'No account yet', state: 'none' };
  const bad = list.filter((a) => a.check && a.check.state === 'invalid').length;
  const unknown = list.filter((a) => !a.check || a.check.state !== 'valid').length - bad;
  if (bad) return { kind: 'error', label: bad + ' of ' + list.length + (list.length === 1 ? ' account' : ' accounts') + ' invalid', state: 'invalid' };
  if (unknown) return { kind: 'plain', label: list.length + (list.length === 1 ? ' account' : ' accounts') + ', ' + unknown + ' not checked', state: 'unknown' };
  return { kind: 'ok', label: list.length === 1 ? '1 account, valid' : list.length + ' accounts, all valid', state: 'valid' };
}

function switchHtml(label, on, action, aria) {
  return html`<label class="ds-switch"><input type="checkbox" role="switch" data-action="${action}"${on ? raw(' checked') : ''} aria-label="${aria}"><span class="ds-switch__track" aria-hidden="true"></span><span class="ds-switch__label">${label}</span><span class="ds-switch__state" aria-hidden="true"><span class="ds-switch__on">On</span><span class="ds-switch__off">Off</span></span></label>`;
}

function renderBuiltin() {
  const host = $('mcpBuiltin');
  if (!host) return;
  if (!overviewLoaded) {
    morph(host, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Asking which servers Socrates has</div>`);
    return;
  }
  if (overviewError && !overview.builtin.length) {
    morph(host, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${overviewError}</span></div>`);
    return;
  }
  if (!overview.builtin.length) {
    morph(host, html`<p class="ds-meta">This version of Socrates has none.</p>`);
    return;
  }
  morph(host, html`${overview.builtin.map((view) => {
    const state = accountsState(view);
    const uses = (view.uses || []).length;
    return html`<div class="so-mcp" data-ds-key="${'builtin-' + view.name}" data-builtin="${view.name}">
      <div class="so-mcp__main">
        <p class="so-mcp__name">${view.title || view.name}<span class="ds-tag ds-tag--neutral">Built in</span></p>
        ${view.description ? html`<p class="ds-meta so-mcp__desc">${view.description}</p>` : ''}
        <div class="so-mcp__state"><span class="so-mcp__fact">${V.chip(state.kind, state.label, state.state)}<span class="ds-meta">${V.parts([uses ? uses + ' in use now' : '', (view.tools || []).length + ' tools'])}</span></span></div>
      </div>
      ${switchHtml('In every chat', builtinOn(view), 'builtin-default', (view.title || view.name) + ' in every chat')}
      <div class="so-mcp__actions">
        <button class="ds-btn ds-btn--sm" type="button" data-action="manage" aria-haspopup="dialog" aria-expanded="false">${state.state === 'none' ? 'Add an account' : 'Manage'}</button>
      </div>
    </div>`;
  })}`);
  for (const input of host.querySelectorAll('input[data-action="builtin-default"]')) {
    const view = builtinOf(input.closest('[data-builtin]').dataset.builtin);
    if (view) input.checked = builtinOn(view);
  }
}

function onBuiltinSwitch(event) {
  const input = event.target;
  if (!input.matches('input[data-action="builtin-default"]')) return;
  builtinChoice.set(input.closest('[data-builtin]').dataset.builtin, input.checked);
}

function builtinOf(name) {
  return overview.builtin.find((one) => one.name === name) || null;
}

function serverOf(node) {
  const row = node.closest('[data-name]');
  return row ? servers.find((one) => one.name === row.dataset.name) || null : null;
}

function summary(server) {
  if (server.type === 'stdio') return [server.command, ...(server.args || [])].join(' ');
  return (server.type === 'sse' ? 'SSE · ' : 'HTTP · ') + server.url;
}

function isRemote(server) {
  return server.type === 'http' || server.type === 'sse';
}

function oauthOf(server) {
  return (overview.oauth || {})[server.name] || null;
}

// signIn is an OAuth server's sign-in as a chip, its facts, and the action
// it calls for: connect, reconnect, or none.
function signIn(server) {
  if (!isRemote(server) || server.auth !== 'oauth') return null;
  const o = oauthOf(server);
  if (!o || !o.connected) {
    if (o && o.error) return { kind: 'error', label: 'Sign-in failed', state: 'signin-error', facts: [V.firstLine(o.error, 90)], action: 'connect', actionLabel: 'Connect' };
    return { kind: 'warning', label: 'Not connected', state: 'signin-none', facts: [], action: 'connect', actionLabel: 'Connect' };
  }
  const expired = o.expires_at && o.expires_at < Date.now() && !o.refreshable;
  if (expired) return { kind: 'warning', label: 'Sign-in expired', state: 'signin-expired', facts: [], action: 'connect', actionLabel: 'Reconnect' };
  if (o.error) return { kind: 'error', label: 'Sign-in failing', state: 'signin-error', facts: [V.firstLine(o.error, 90)], action: 'connect', actionLabel: 'Reconnect' };
  const facts = [o.refreshable ? 'renews by itself' : (o.expires_at ? 'expires ' + V.untilText(o.expires_at) : '')];
  return { kind: 'ok', label: 'Connected', state: 'signin-ok', facts, action: '', actionLabel: '' };
}

function testOf(server) {
  return (overview.tests || {})[server.name] || null;
}

function renderServers() {
  const host = $('mcpServers');
  if (!host) return;
  const rows = servers.map((server) => {
    const sign = signIn(server);
    const test = testing.has(server.name)
      ? { kind: 'running', label: 'Testing', state: 'testing', facts: [] }
      : V.probeState(testOf(server));
    const off = (server.disabled_tools || []).length;
    const type = server.type === 'stdio' ? 'Local' : server.type === 'sse' ? 'SSE' : 'HTTP';
    return html`<div class="so-mcp" data-ds-key="${'server-' + server.name}" data-name="${server.name}">
      <div class="so-mcp__main">
        <p class="so-mcp__name">${server.name}<span class="ds-tag ds-tag--neutral">${type}</span></p>
        ${server.description ? html`<p class="ds-meta so-mcp__desc">${server.description}</p>` : ''}
        <p class="ds-meta so-mcp__what">${summary(server)}</p>
        <div class="so-mcp__state">
          ${sign ? html`<span class="so-mcp__fact">${V.chip(sign.kind, sign.label, sign.state)}<span class="ds-meta">${V.parts(sign.facts)}</span></span>` : ''}
          <span class="so-mcp__fact" data-test="${test.state}">${V.chip(test.kind, test.label, test.state)}<span class="ds-meta">${V.parts([...test.facts, off ? off + (off === 1 ? ' tool off' : ' tools off') : ''])}</span>${testOf(server) && !testing.has(server.name) ? html`<button class="ds-link-btn" type="button" data-action="details">Details</button>` : ''}</span>
          ${test.error ? html`<p class="ds-meta so-mcp__error">${test.error}</p>` : ''}
        </div>
      </div>
      ${switchHtml('In every chat', !!server.default, 'default', server.name + ' in every chat')}
      <div class="so-mcp__actions">
        <button class="ds-btn ds-btn--sm" type="button" data-action="test">${faces('Test', 'Testing')}</button>
        ${sign && sign.action ? html`<button class="ds-btn ds-btn--sm" type="button" data-action="connect">${faces(sign.actionLabel, 'Opening')}</button>` : ''}
        <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="edit" aria-label="${'Edit ' + server.name}" data-tip="Edit">${raw(icon('pencil', 18))}</button>
        <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="delete" aria-label="${'Remove ' + server.name}" data-tip="Remove">${raw(icon('trash', 18))}</button>
      </div>
    </div>`;
  });
  flipMorph(host, servers.length ? html`${rows}`
    : html`<p class="ds-meta" data-ds-key="none">None yet. Add one below: a common server, one of your own, or a .mcp.json pasted in.</p>`);
  // morph keeps an input's checked as the page left it; the list says.
  for (const input of host.querySelectorAll('input[data-action="default"]')) {
    const server = serverOf(input);
    if (server) input.checked = !!server.default;
  }
}

/* ------------------------------------------------------- row actions */

function onServerAction(event) {
  const button = event.target.closest('button[data-action]');
  const server = button && serverOf(button);
  if (!server) return;
  const action = button.dataset.action;
  if (action === 'edit') editServer(server, button);
  else if (action === 'delete') removeServer(server);
  else if (action === 'test') testServer(server, button);
  else if (action === 'details') openSheet({ kind: 'server', name: server.name }, button);
  else if (action === 'connect') connect(server, button);
}

function removeServer(server) {
  const at = servers.indexOf(server);
  servers = servers.filter((one) => one !== server);
  render();
  toast(server.name + ' removed. Save to keep it so.', 'info', {
    action: { label: 'Undo', run: () => {
      if (servers.some((one) => one.name === server.name)) return;
      servers.splice(Math.min(at, servers.length), 0, server);
      servers.sort(byName);
      render();
    } },
  });
}

// testServer tests a server of the list and shows the outcome in the sheet.
async function testServer(server, button) {
  if (testing.has(server.name)) return;
  const name = server.name;
  testing.add(name);
  render();
  try {
    const work = api('/api/mcp/test', { method: 'POST', body: { server: plain(server) }, timeout: 100000, attempts: 1 });
    const probe = button && button.isConnected ? await M.busy(button, work, { done: false }) : await work;
    overview.tests = { ...(overview.tests || {}), [name]: probe };
    if (probe.oauth) overview.oauth = { ...(overview.oauth || {}), [name]: probe.oauth };
  } catch (err) {
    toast(errorMessage(err), 'error');
  } finally {
    testing.delete(name);
    render();
  }
  const opener = $('mcpServers').querySelector('[data-name="' + CSS.escape(name) + '"] button[data-action="test"]');
  if (testOf(server) && (!sheetOf || sheetOf.name !== name)) openSheet({ kind: 'server', name }, opener);
}

// plain is a server as the settings document has it, for a request.
function plain(server) {
  return JSON.parse(JSON.stringify(server));
}

// connect signs Socrates in to an OAuth server: the browser goes to the
// authorization server and comes back to Admin. Unsaved changes would be
// lost on the way, so they are saved first.
async function connect(server, button) {
  if (!isRemote(server) || server.auth !== 'oauth') return;
  if (hooks.dirty()) {
    const host = hostOf(server.url);
    const yes = await confirmDialog({
      title: 'Save the changes first?',
      body: 'Connecting takes this page to ' + host + ' to sign in, and changes that are not saved yet would be lost on the way. Socrates saves them, then connects.',
      confirmLabel: 'Save and connect',
      opener: button,
    });
    if (!yes) return;
    if (!await hooks.save()) return;
    server = servers.find((one) => one.name === server.name);
    if (!server) return;
    button = $('mcpServers').querySelector('[data-name="' + CSS.escape(server.name) + '"] button[data-action="connect"]') || button;
  }
  try {
    const work = api('/api/mcp/oauth/start', { method: 'POST', body: { name: server.name, url: server.url, scope: server.scope || '', headers: server.headers || {} } });
    const answer = button && button.isConnected ? await M.busy(button, work, { done: false }) : await work;
    if (!answer || !answer.url) throw new Error('The server gave no address to sign in at.');
    location.href = answer.url;
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function disconnect(server, button) {
  const yes = await confirmDialog({
    title: 'Disconnect ' + server.name + '?',
    body: 'Socrates asks the server to revoke its sign-in and forgets it. Chats have no ' + server.name + ' tools until it is connected again.',
    confirmLabel: 'Disconnect', danger: true, opener: button,
  });
  if (!yes) return;
  try {
    const work = api('/api/mcp/oauth?url=' + encodeURIComponent(server.url), { method: 'DELETE' });
    const o = button && button.isConnected ? await M.busy(button, work, { done: false }) : await work;
    overview.oauth = { ...(overview.oauth || {}), [server.name]: o || { connected: false } };
    render();
    toast(server.name + ' is disconnected.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

function hostOf(url) {
  try { return new URL(url).host; } catch { return 'the server'; }
}

/* -------------------------------------------------------------- sheet */

function openSheet(what, opener) {
  if (!sheetOf || sheetOf.kind !== what.kind || sheetOf.name !== what.name) sheetOpen.clear();
  sheetOf = what;
  renderSheet();
  const sheet = $('mcpSheet');
  const body = sheet.querySelector('.ds-sheet__body');
  body.scrollTop = 0;
  if (!sheet.open) DS.overlay.open(sheet, { opener });
  clearInterval(sheetTimer);
  if (what.kind === 'builtin') {
    // What a built-in server has in use changes by itself: the sheet asks
    // again while it is open.
    pollBuiltin();
    sheetTimer = setInterval(pollBuiltin, 15000);
  }
}

async function pollBuiltin() {
  try {
    const list = await api('/api/mcp/builtin', { attempts: 1 });
    if (Array.isArray(list)) {
      overview.builtin = list;
      render();
    }
  } catch { /* the next poll tries again */ }
}

function setBuiltin(view) {
  if (!view || !view.name) return;
  const at = overview.builtin.findIndex((one) => one.name === view.name);
  if (at >= 0) overview.builtin[at] = view;
  else overview.builtin.push(view);
  render();
}

function renderSheet() {
  if (!sheetOf) return;
  const sheet = $('mcpSheet');
  const title = $('mcpSheetTitle');
  const sub = $('mcpSheetSub');
  const body = sheet.querySelector('.ds-sheet__body');
  const foot = $('mcpSheetFoot');
  if (sheetOf.kind === 'builtin') {
    const view = builtinOf(sheetOf.name);
    if (!view) return;
    title.textContent = view.title || view.name;
    sub.textContent = 'Built into Socrates';
    morph(body, builtinBody(view));
    morph(foot, html`<button class="ds-btn ds-btn--text" type="button" data-close>Close</button>`);
    return;
  }
  const server = servers.find((one) => one.name === sheetOf.name);
  if (!server) return;
  title.textContent = server.name;
  sub.textContent = summary(server);
  morph(body, serverBody(server));
  morph(foot, html`<button class="ds-btn ds-btn--text" type="button" data-action="sheet-edit">Edit</button><button class="ds-btn" type="button" data-action="sheet-test"${testing.has(server.name) ? raw(' aria-disabled="true"') : ''}>${faces(testOf(server) ? 'Test again' : 'Test', 'Testing')}</button>`);
}

function builtinBody(view) {
  const accounts = view.accounts || [];
  const fields = view.account_fields || [];
  const uses = view.uses || [];
  const state = accountsState(view);
  const secretOf = (account) => fields.filter((f) => f.secret).map((f) => f.label + ' ' + (account.values[f.name] || '')).join(' · ');
  const plainOf = (account) => fields.filter((f) => !f.secret && account.values[f.name] && account.values[f.name] !== account.label)
    .map((f) => f.label + ' ' + account.values[f.name]).join(' · ');
  return html`<div class="so-mcp-sheet">
    ${view.description ? html`<p class="ds-muted">${view.description}</p>` : ''}
    ${fields.length ? html`<section class="so-mcp-sheet__part" aria-labelledby="mcpAccountsTitle">
      <div class="so-mcp-sheet__head">
        <h3 class="ds-card__subtitle" id="mcpAccountsTitle">Accounts</h3>
        ${V.chip(state.kind, state.label, state.state)}
      </div>
      ${view.pooled ? html`<p class="ds-meta">A chat that needs one borrows a free account and gives it back when it is done: more accounts, more at once.</p>` : ''}
      ${accounts.length ? html`<ul class="ds-list" aria-label="Accounts">${accounts.map((account) => {
        const check = account.check || { state: 'unknown' };
        const kind = check.state === 'valid' ? 'ok' : check.state === 'invalid' ? 'error' : 'plain';
        const label = check.state === 'valid' ? 'Valid' : check.state === 'invalid' ? 'Invalid' : 'Not checked';
        return html`<li class="ds-list__item" data-ds-key="${'account-' + account.id}" data-account="${account.id}">${raw(icon('key', 20, 'ds-list__icon'))}
          <span class="ds-list__text">
            <span class="ds-list__title so-break">${account.label || account.id}</span>
            <span class="ds-list__meta so-break">${[plainOf(account), secretOf(account)].filter(Boolean).join(' · ')}</span>
            <span class="ds-list__meta so-break">${[check.detail, check.at ? 'checked ' + V.agoText(check.at) : ''].filter(Boolean).join(' · ')}</span>
            ${account.in_use ? html`<span class="ds-list__meta so-break">${'In use: ' + account.in_use}</span>` : ''}
          </span>
          ${V.chip(kind, label, 'account-' + check.state)}
          <span class="ds-list__actions">
            <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="account-check" aria-label="${'Check ' + (account.label || account.id)}" data-tip="Check it now">${raw(icon('sync', 18))}</button>
            <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="account-remove" aria-label="${'Remove ' + (account.label || account.id)}" data-tip="Remove">${raw(icon('trash', 18))}</button>
          </span>
        </li>`;
      })}</ul>` : html`<div class="ds-empty">
        <span class="ds-empty__disc" aria-hidden="true">${raw(icon('key', 24))}</span>
        <p class="ds-empty__title">No account yet</p>
        <p class="ds-empty__text">${'Add one so chats can use ' + (view.title || view.name) + '. It is checked with the service first.'}</p>
      </div>`}
      <div class="so-admin-actions">
        <button class="ds-btn ds-btn--text ds-add-row" type="button" data-action="account-add">${raw(icon('plus', 16))}Add an account</button>
        ${accounts.length ? html`<button class="ds-btn ds-btn--text" type="button" data-action="account-check-all">${faces('Check all', 'Checking')}</button>` : ''}
      </div>
    </section>` : ''}
    <section class="so-mcp-sheet__part" aria-labelledby="mcpUsesTitle">
      <h3 class="ds-card__subtitle" id="mcpUsesTitle">In use now</h3>
      ${uses.length ? html`<ul class="ds-plain-list">${uses.map((use) => html`<li class="so-break">${use.what}</li>`)}</ul>` : html`<p class="ds-meta">Nothing right now.</p>`}
    </section>
    <details class="ds-collapsible ds-disclosure" data-ds-key="tools"${openAttr('tools')}><summary>${'Its tools (' + (view.tools || []).length + ')'}${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
      <div class="ds-disclosure__body">${V.toolList(view.tools || [])}</div></details>
    ${view.instructions ? html`<details class="ds-collapsible ds-disclosure" data-ds-key="instructions"${openAttr('instructions')}><summary>What agents are told${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary>
      <div class="ds-disclosure__body">${V.prose(view.instructions)}</div></details>` : ''}
  </div>`;
}

function serverBody(server) {
  const probe = testOf(server);
  const sign = signIn(server);
  const o = oauthOf(server) || {};
  const disabled = new Set(server.disabled_tools || []);
  let alertActions = '';
  if (probe && probe.state === 'auth') {
    alertActions = server.auth === 'oauth'
      ? html`<button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="sheet-connect">Connect</button>`
      : isRemote(server) ? html`<button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="sheet-use-oauth">Sign in with OAuth</button>` : '';
  }
  const signFacts = sign ? [
    ['Authorization server', o.authorization_server],
    ['Scope', o.scope || server.scope],
    ['Connected', o.connected && o.connected_at ? V.agoText(o.connected_at) : ''],
    ['Expires', o.connected && o.expires_at ? (o.refreshable ? V.untilText(o.expires_at) + ', renews by itself' : V.untilText(o.expires_at)) : ''],
    ['Problem', o.error],
    ['Redirect URL', overview.redirect_uri],
  ].filter(([, value]) => value) : [];
  return html`<div class="so-mcp-sheet">
    ${server.description ? html`<p class="ds-muted">${server.description}</p>` : ''}
    ${sign ? html`<section class="so-mcp-sheet__part" aria-labelledby="mcpSignTitle">
      <div class="so-mcp-sheet__head"><h3 class="ds-card__subtitle" id="mcpSignTitle">Sign-in (OAuth)</h3>${V.chip(sign.kind, sign.label, sign.state)}</div>
      <p class="ds-meta">Socrates signs in once, here, and gives every chat’s agent the token; the agents never see the sign-in.</p>
      ${signFacts.length ? html`<dl class="ds-facts ds-facts--rows">${signFacts.map(([label, value]) => html`<div><dt>${label}</dt><dd class="so-break">${value}</dd></div>`)}</dl>` : ''}
      <div class="so-admin-actions">
        <button class="ds-btn ds-btn--sm" type="button" data-action="sheet-connect">${faces(o.connected ? 'Reconnect' : 'Connect', 'Opening')}</button>
        ${o.connected ? html`<button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="sheet-disconnect">${faces('Disconnect', 'Disconnecting')}</button>` : ''}
      </div>
    </section>` : ''}
    <section class="so-mcp-sheet__part" aria-labelledby="mcpTestTitle">
      <h3 class="ds-card__subtitle" id="mcpTestTitle">Connection</h3>
      ${testing.has(server.name)
        ? html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Connecting to it as an agent would; a server npx installs first can take a minute</div>`
        : probe ? V.probeView(probe, { toolOn: (name) => !disabled.has(name), alertActions, idPrefix: 'mcpSheetTool', isOpen })
          : html`<p class="ds-meta">Not tested yet. A test connects to it as an agent would, and lists its tools.</p>`}
      ${!probe && disabled.size ? html`<p class="ds-meta">${disabled.size + (disabled.size === 1 ? ' tool is' : ' tools are') + ' switched off: ' + [...disabled].join(', ')}</p>` : ''}
    </section>
  </div>`;
}

function onSheetChange(event) {
  const input = event.target;
  if (!input.matches('input[data-tool]') || !sheetOf || sheetOf.kind !== 'server') return;
  const server = servers.find((one) => one.name === sheetOf.name);
  if (!server) return;
  const off = new Set(server.disabled_tools || []);
  if (input.checked) off.delete(input.dataset.tool);
  else off.add(input.dataset.tool);
  server.disabled_tools = [...off].sort();
  renderServers();
}

function onSheetAction(event) {
  const button = event.target.closest('button[data-action]');
  if (!button || !sheetOf) return;
  const action = button.dataset.action;
  if (sheetOf.kind === 'builtin') {
    const view = builtinOf(sheetOf.name);
    if (!view) return;
    const row = button.closest('[data-account]');
    const account = row ? (view.accounts || []).find((one) => one.id === row.dataset.account) : null;
    if (action === 'account-add') addAccount(view, button);
    else if (action === 'account-check-all') checkAccount(view, 'all', button);
    else if (action === 'account-check' && account) checkAccount(view, account.id, button);
    else if (action === 'account-remove' && account) removeAccount(view, account, button);
    return;
  }
  const server = servers.find((one) => one.name === sheetOf.name);
  if (!server) return;
  if (action === 'sheet-test') testServer(server, button);
  else if (action === 'sheet-edit') {
    DS.overlay.close($('mcpSheet'));
    const opener = $('mcpServers').querySelector('[data-name="' + CSS.escape(server.name) + '"] button[data-action="edit"]');
    editServer(server, opener);
  } else if (action === 'sheet-connect') connect(server, button);
  else if (action === 'sheet-disconnect') disconnect(server, button);
  else if (action === 'sheet-use-oauth') {
    server.auth = 'oauth';
    render();
    toast('Sign-in is OAuth now. Connect it to sign in.');
  }
}

/* ---------------------------------------------------- built-in accounts */

const accountsPath = (view) => '/api/mcp/builtin/' + encodeURIComponent(view.name) + '/accounts';

async function checkAccount(view, id, button) {
  try {
    const work = api(accountsPath(view) + '/' + encodeURIComponent(id) + '/check', { method: 'POST', timeout: 60000, attempts: 1 });
    setBuiltin(await M.busy(button, work, { done: false }));
    toast(id === 'all' ? 'The accounts are checked.' : 'The account is checked.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

async function removeAccount(view, account, button) {
  const yes = await confirmDialog({
    title: 'Remove ' + (account.label || 'this account') + '?',
    body: account.in_use
      ? 'It is in use now (' + account.in_use + '). Socrates forgets it; what runs on it may stop.'
      : 'Socrates forgets the account. It can be added again later.',
    confirmLabel: 'Remove the account', danger: true, opener: button,
  });
  if (!yes) return;
  try {
    setBuiltin(await api(accountsPath(view) + '/' + encodeURIComponent(account.id), { method: 'DELETE' }));
    toast('The account is removed.');
  } catch (err) {
    toast(errorMessage(err), 'error');
  }
}

// addAccount asks for the fields the server names (a user and a key for
// BrowserStack); the server checks the account with the service before it
// keeps it, and says why when it refuses it.
async function addAccount(view, opener) {
  const inputs = {};
  const fields = (view.account_fields || []).map((spec, i) => {
    const id = 'mcpAccount' + i;
    const input = el('input', {
      class: 'ds-input', id, type: spec.secret ? 'password' : 'text', placeholder: spec.placeholder || null,
      autocomplete: spec.secret ? 'new-password' : 'off', spellcheck: 'false', autocapitalize: 'off',
      autofocus: i === 0, 'aria-describedby': spec.hint ? id + 'Hint' : null,
    });
    inputs[spec.name] = input;
    return el('div', { class: 'ds-field' },
      el('label', { class: 'ds-field__label', for: id, text: spec.label }),
      input,
      spec.hint ? el('small', { class: 'ds-field__hint', id: id + 'Hint', text: spec.hint }) : null);
  });
  const problem = el('div', { class: 'so-admin-notice' });
  problem.hidden = true;
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button' });
  morph(accept, faces('Check and add', 'Checking'));
  const { dialog, shown } = openDialog({
    title: 'A ' + (view.title || view.name) + ' account',
    body: 'Socrates checks it with ' + (view.title || view.name) + ' first and keeps it encrypted on this machine.',
    field: el('div', { class: 'ds-form' }, problem, ...fields),
    actions: [cancel, accept], opener,
  });
  const run = async () => {
    if (dialog.hasAttribute('data-ds-busy')) return;
    const values = {};
    for (const [name, input] of Object.entries(inputs)) values[name] = input.value.trim();
    const empty = Object.entries(inputs).find(([name]) => !values[name]);
    if (empty) {
      empty[1].setAttribute('aria-invalid', 'true');
      showProblem(problem, 'Fill in every field.');
      empty[1].focus();
      return;
    }
    dialog.setAttribute('data-ds-busy', '');
    cancel.setAttribute('aria-disabled', 'true');
    try {
      const next = await M.busy(accept, api(accountsPath(view), { method: 'POST', body: { values }, timeout: 60000, attempts: 1 }), { done: false });
      dialog.answer(next);
      dialog.removeAttribute('data-ds-busy');
      DS.overlay.close(dialog);
    } catch (err) {
      showProblem(problem, errorMessage(err));
    } finally {
      dialog.removeAttribute('data-ds-busy');
      cancel.removeAttribute('aria-disabled');
    }
  };
  accept.addEventListener('click', run);
  for (const input of Object.values(inputs)) {
    input.addEventListener('input', () => input.removeAttribute('aria-invalid'));
    input.addEventListener('keydown', (event) => { if (event.key === 'Enter' && !event.isComposing) run(); });
  }
  const next = await shown;
  if (!next) return;
  setBuiltin(next);
  toast('The account is added and valid.', 'success');
}

function showProblem(slot, text, kind = 'error') {
  morph(slot, html`<div class="${'ds-alert ds-alert--' + kind}" role="alert">${raw(icon(kind === 'error' ? 'alertCircle' : 'alert', 20))}<span class="ds-alert__text">${text}</span></div>`);
  slot.hidden = false;
}

/* ------------------------------------------------------- the editor */

// The environment and the headers are one entry per line: KEY=value and
// Name: value, as they are written in a shell and in a request.
function linesOf(map, sep) {
  return Object.entries(map || {}).map(([key, value]) => key + sep + value).join('\n');
}

function mapOf(text, sep) {
  const out = {};
  for (const line of String(text || '').split('\n')) {
    const at = line.indexOf(sep);
    if (at <= 0) continue;
    out[line.slice(0, at).trim()] = line.slice(at + sep.length).trim();
  }
  return out;
}

// quoteArgs is splitArgs undone: an argument with a space keeps its quotes.
function quoteArgs(args) {
  return (args || []).map((a) => (/\s/.test(a) || a === '' ? (a.includes('"') ? "'" + a + "'" : '"' + a + '"') : a)).join(' ');
}

// splitArgs splits a command line as a shell would, quotes and all.
function splitArgs(text) {
  const out = [];
  let cur = '';
  let quote = '';
  let has = false;
  for (const ch of String(text || '')) {
    if (quote) {
      if (ch === quote) quote = '';
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has || cur) out.push(cur);
      cur = '';
      has = false;
    } else {
      cur += ch;
    }
  }
  if (has || cur) out.push(cur);
  return out;
}

function builtinNames() {
  return new Set([...overview.builtin.map((one) => one.name), ...builtinChoice.keys()]);
}

// problemOf is what keeps a server from being saved, in words; the server
// checks the same on Save.
function problemOf(server, previous) {
  if (!server.name) return ['name', 'The server needs a name.'];
  if (!NAME.test(server.name)) return ['name', 'A name may only have letters, digits, - and _.'];
  if (server.name === 'socrates') return ['name', 'The name socrates is Socrates’ own.'];
  if (builtinNames().has(server.name)) return ['name', 'The name ' + server.name + ' is taken by a server built into Socrates.'];
  if (servers.some((one) => one !== previous && one.name === server.name)) return ['name', 'There is a server named ' + server.name + ' already.'];
  if (server.type === 'stdio' && !server.command) return ['command', 'A local server needs its command.'];
  if (server.type !== 'stdio' && !server.url) return ['url', 'A remote server needs its URL.'];
  if (server.type !== 'stdio' && !/^https?:\/\//i.test(server.url)) return ['url', 'The URL starts with https:// (or http://).'];
  return null;
}

function field(label, control, hint, id) {
  return el('div', { class: 'ds-field' },
    el('label', { class: 'ds-field__label', for: id, text: label }),
    control,
    hint ? el('small', { class: 'ds-field__hint', id: id + 'Hint', text: hint }) : null);
}

function textInput(id, value, attrs = {}) {
  const multi = !!attrs.rows;
  return el(multi ? 'textarea' : 'input', {
    class: 'ds-input', id, value: value || '', spellcheck: 'false', autocomplete: 'off', autocapitalize: 'off',
    type: multi ? null : (attrs.type || 'text'), ...attrs,
  });
}

// editServer adds a server (previous null, `start` its prefilled fields) or
// changes one, in a dialog that can test what is typed before it is kept.
async function editServer(previous, opener, start = null) {
  const server = previous || { type: 'stdio', default: true, ...(start || {}) };
  const name = textInput('mcpEditName', server.name, { autofocus: true, placeholder: 'github', 'aria-describedby': 'mcpEditNameHint' });
  const description = textInput('mcpEditDescription', server.description, { placeholder: 'What it is for', spellcheck: 'true', autocapitalize: 'sentences' });
  const type = el('select', { class: 'ds-input', id: 'mcpEditType' }, TYPES.map(([id, label]) => el('option', { value: id, text: label })));
  type.value = server.type || 'stdio';
  const command = textInput('mcpEditCommand', server.command, { placeholder: 'npx' });
  const args = textInput('mcpEditArgs', quoteArgs(server.args), { placeholder: '-y @modelcontextprotocol/server-github' });
  const env = textInput('mcpEditEnv', linesOf(server.env, '='), { rows: 3, placeholder: 'GITHUB_TOKEN=${GITHUB_TOKEN}', 'aria-describedby': 'mcpEditEnvHint' });
  const url = textInput('mcpEditURL', server.url, { placeholder: 'https://example.com/mcp', inputmode: 'url', type: 'url' });
  const headers = textInput('mcpEditHeaders', linesOf(server.headers, ': '), { rows: 3, placeholder: 'Authorization: Bearer ${API_TOKEN}', 'aria-describedby': 'mcpEditHeadersHint' });
  const auth = el('select', { class: 'ds-input', id: 'mcpEditAuth', 'aria-describedby': 'mcpEditAuthHint' },
    el('option', { value: '', text: 'None: the headers carry what it needs' }),
    el('option', { value: 'oauth', text: 'OAuth: sign in once, here' }));
  auth.value = server.auth === 'oauth' ? 'oauth' : '';
  const scope = textInput('mcpEditScope', server.scope, { placeholder: 'Empty: what the server asks for', 'aria-describedby': 'mcpEditScopeHint' });
  const timeout = textInput('mcpEditTimeout', server.tool_timeout ? String(server.tool_timeout) : '', { type: 'number', inputmode: 'numeric', min: '0', max: '86400', placeholder: 'The agent’s own', 'aria-describedby': 'mcpEditTimeoutHint' });
  const on = el('input', { type: 'checkbox', role: 'switch', id: 'mcpEditDefault', checked: server.default !== false, 'aria-describedby': 'mcpEditDefaultHint' });

  const nameField = field('Name', name, 'Letters, digits, - and _. Agents see it in the names of its tools.', 'mcpEditName');
  const descField = field('Description', description, '', 'mcpEditDescription');
  descField.querySelector('label').append(' ', el('span', { class: 'ds-field__optional', text: '(optional)' }));
  const typeField = field('How it runs', type, '', 'mcpEditType');
  const local = [
    field('Command', command, '', 'mcpEditCommand'),
    field('Arguments', args, '', 'mcpEditArgs'),
    field('Environment', env, 'One KEY=value per line. ${VAR} is filled in from the chat’s environment.', 'mcpEditEnv'),
  ];
  const authField = field('Sign-in', auth, '', 'mcpEditAuth');
  const remote = [
    field('URL', url, '', 'mcpEditURL'),
    field('Headers', headers, 'One Name: value per line. ${VAR} is filled in from the chat’s environment.', 'mcpEditHeaders'),
    authField,
  ];
  const authHint = authField.querySelector('.ds-field__hint') || el('small', { class: 'ds-field__hint', id: 'mcpEditAuthHint' });
  authField.append(authHint);
  const scopeField = field('OAuth scope', scope, 'Empty asks for the scope the server names.', 'mcpEditScope');
  const timeoutField = field('Tool timeout (seconds)', timeout, 'How long one call of a tool may take. Empty or 0 leaves it to the agent.', 'mcpEditTimeout');
  const advanced = el('details', { class: 'ds-collapsible ds-disclosure' },
    el('summary', {}, 'Advanced', iconEl('chevronDown', 20, 'ds-chevron')),
    el('div', { class: 'ds-disclosure__body ds-form' }, scopeField, timeoutField));
  if (server.scope || server.tool_timeout) advanced.open = true;
  const onField = el('div', { class: 'ds-field' }, el('label', { class: 'ds-switch' }, on,
    el('span', { class: 'ds-switch__track', 'aria-hidden': 'true' }),
    el('span', { class: 'ds-switch__label', text: 'In every chat' }),
    el('span', { class: 'ds-switch__state', 'aria-hidden': 'true' },
      el('span', { class: 'ds-switch__on', text: 'On' }), el('span', { class: 'ds-switch__off', text: 'Off' }))),
  el('small', { class: 'ds-field__hint', id: 'mcpEditDefaultHint', text: 'Off, a chat has it only once it is switched on there (⋮ → MCP servers).' }));

  const test = el('button', { class: 'ds-btn ds-btn--sm', type: 'button', id: 'mcpEditTest' });
  morph(test, faces('Test connection', 'Testing'));
  const outcome = el('div', { class: 'so-mcp-outcome', id: 'mcpEditOutcome', 'aria-live': 'polite' });
  const testRow = el('div', { class: 'so-mcp-test' }, test, outcome);

  const problem = el('div', { class: 'so-admin-notice' });
  problem.hidden = true;
  const show = () => {
    const stdio = type.value === 'stdio';
    for (const node of local) node.hidden = !stdio;
    for (const node of remote) node.hidden = stdio;
    scopeField.hidden = stdio || auth.value !== 'oauth';
    authHint.textContent = auth.value === 'oauth'
      ? 'Once the server is saved, Connect on its row signs Socrates in; chats get the token.'
      : 'A token in a header, or nothing for a public server.';
  };
  type.addEventListener('change', show);
  auth.addEventListener('change', show);
  show();

  const form = el('div', { class: 'ds-form' }, problem, nameField, descField, typeField, ...local, ...remote, advanced, onField, testRow);
  const body = el('div', { class: 'ds-dialog__body' }, form);
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: previous ? 'Keep the changes' : 'Add the server' });
  const { dialog, shown } = openDialog({
    title: previous ? 'MCP server ' + previous.name : start && start.name ? 'Add ' + start.name : 'A new MCP server',
    field: body, actions: [cancel, accept], opener,
  });
  dialog.classList.add('ds-dialog--wide');

  const read = () => {
    const next = {
      name: name.value.trim(), type: type.value, default: on.checked, description: description.value.trim(),
      disabled_tools: previous ? [...(previous.disabled_tools || [])] : [],
    };
    const secs = Math.max(0, Math.round(Number(timeout.value) || 0));
    if (secs) next.tool_timeout = secs;
    if (next.type === 'stdio') {
      Object.assign(next, { command: command.value.trim(), args: splitArgs(args.value), env: mapOf(env.value, '=') });
    } else {
      Object.assign(next, { url: url.value.trim(), headers: mapOf(headers.value, ':') });
      if (auth.value === 'oauth') {
        next.auth = 'oauth';
        if (scope.value.trim()) next.scope = scope.value.trim();
      }
    }
    if (!next.description) delete next.description;
    if (!next.disabled_tools.length) delete next.disabled_tools;
    return next;
  };

  let lastTest = null;
  test.addEventListener('click', async () => {
    const next = read();
    if ((next.type === 'stdio' && !next.command) || (next.type !== 'stdio' && !next.url)) {
      morph(outcome, html`<small class="ds-field__hint ds-field__hint--error">${raw(icon('alertCircle', 16))}${next.type === 'stdio' ? 'Type its command first.' : 'Type its URL first.'}</small>`);
      return;
    }
    morph(outcome, html`<small class="ds-field__hint">Connecting as an agent would; a server npx installs first can take a minute.</small>`);
    try {
      const probe = await M.busy(test, api('/api/mcp/test', { method: 'POST', body: { server: next }, timeout: 100000, attempts: 1 }), { done: false });
      lastTest = { name: next.name, probe };
      if (!dialog.isConnected) return;
      morph(outcome, outcomeHtml(probe, next));
      outcome.scrollIntoView({ block: 'nearest' });
    } catch (err) {
      if (dialog.isConnected) morph(outcome, html`<small class="ds-field__hint ds-field__hint--error">${raw(icon('alertCircle', 16))}${errorMessage(err)}</small>`);
    }
  });
  outcome.addEventListener('click', (event) => {
    if (!event.target.closest('button[data-action="use-oauth"]')) return;
    auth.value = 'oauth';
    show();
    auth.focus();
    morph(outcome, html`<small class="ds-field__hint ds-field__hint--success">${raw(icon('checkCircle', 16))}Sign-in is OAuth now. Add the server, save, then Connect it on its row.</small>`);
  });

  accept.addEventListener('click', () => {
    const next = read();
    const bad = problemOf(next, previous);
    for (const node of [name, command, url]) node.removeAttribute('aria-invalid');
    if (bad) {
      const [which, text] = bad;
      const node = { name, command, url }[which];
      node.setAttribute('aria-invalid', 'true');
      showProblem(problem, text);
      body.scrollTop = 0;
      node.focus();
      return;
    }
    dialog.answer(next);
    DS.overlay.close(dialog);
  });

  const answer = await shown;
  if (!answer) return;
  if (previous) {
    if (previous.name !== answer.name || summary(previous) !== summary(answer)) {
      const tests = { ...(overview.tests || {}) };
      delete tests[previous.name];
      overview.tests = tests;
    }
    servers[servers.indexOf(previous)] = answer;
  } else {
    servers.push(answer);
  }
  if (lastTest && lastTest.name === answer.name) overview.tests = { ...(overview.tests || {}), [answer.name]: lastTest.probe };
  servers.sort(byName);
  if (sheetOf && previous && sheetOf.name === previous.name) sheetOf = { kind: 'server', name: answer.name };
  render();
  toast(previous ? 'Changed. Save to keep it.' : answer.name + ' added. Save to keep it.');
}

// outcomeHtml is a test's outcome under the editor's button: a line when it
// worked, an alert with what to do when it did not.
function outcomeHtml(probe, server) {
  if (probe.state === 'ok') {
    const who = [probe.server_title || probe.server_name, probe.server_version].filter(Boolean).join(' ');
    const tools = probe.tools || [];
    return html`<small class="ds-field__hint ds-field__hint--success">${raw(icon('checkCircle', 16))}${'It works' + (who ? ' (' + who + ')' : '') + ': ' + V.counts(probe) + (probe.latency_ms ? ', ' + probe.latency_ms + ' ms' : '') + '.'}</small>
      ${tools.length ? html`<details class="ds-collapsible ds-disclosure"><summary>Its tools${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body">${V.toolList(tools)}</div></details>` : ''}`;
  }
  if (probe.state === 'auth') {
    const canOAuth = server.type !== 'stdio' && server.auth !== 'oauth';
    return html`<div class="ds-alert ds-alert--warning" role="alert">${raw(icon('alert', 20))}<span class="ds-alert__text">${canOAuth
      ? 'It wants a sign-in. Most hosted servers sign in with OAuth: switch Sign-in to OAuth, or give it a token in a header.'
      : 'It wants a sign-in. Add the server, save, then Connect it on its row.'}</span>${canOAuth ? html`<div class="ds-alert__actions"><button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="use-oauth">Use OAuth</button></div>` : ''}</div>`;
  }
  return html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<div class="ds-alert__text"><p class="ds-alert__title">It did not work</p><p class="so-break">${probe.error || 'No answer.'}</p></div></div>
    ${probe.stderr ? html`<details class="ds-collapsible ds-disclosure" open><summary>What it printed${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body"><pre class="ds-code-block ds-code-block--wrap"><code>${String(probe.stderr).trim().split('\n').slice(-20).join('\n')}</code></pre></div></details>` : ''}`;
}

/* ---------------------------------------------------------- adding */

// addServer offers the starting points: a common server, prefilled; one of
// the person's own; or a .mcp.json pasted in.
async function addServer(opener) {
  const list = el('ul', { class: 'ds-list ds-list--interactive', 'aria-label': 'Starting points' });
  const row = (id, glyph, title, meta) => html`<li class="ds-list__item" data-ds-key="${id}">${raw(icon(glyph, 20, 'ds-list__icon'))}
    <span class="ds-list__text"><span class="ds-list__title"><button class="ds-list__action" type="button" data-pick="${id}">${title}</button></span><span class="ds-list__meta">${meta}</span></span>
    ${raw(icon('chevronRight', 20, 'ds-list__chevron'))}</li>`;
  const how = (server) => [
    server.type === 'stdio' ? 'Runs here: ' + [server.command, ...(server.args || [])].join(' ') : (server.type === 'sse' ? 'SSE' : 'HTTP'),
    server.auth === 'oauth' ? 'sign in with OAuth' : server.headers ? 'needs a token' : server.type === 'stdio' ? '' : 'no sign-in',
  ].filter(Boolean).join(' · ');
  morph(list, html`${GALLERY.map((one) => row(one.id, one.server.type === 'stdio' ? 'terminal' : 'globe', one.title, one.what + ' · ' + how(one.server)))}
    ${row('custom', 'plus', 'Another server', 'A local command or a URL of your own')}
    ${row('paste', 'clipboard', 'Paste JSON', 'Servers as a .mcp.json or a program’s docs write them')}`);
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const { dialog, shown } = openDialog({
    title: 'Add an MCP server',
    body: 'Start from a common one, or set up your own. Everything stays editable before it is added.',
    field: el('div', { class: 'ds-dialog__body' }, list), actions: [cancel], opener,
  });
  dialog.classList.add('ds-dialog--wide');
  list.addEventListener('click', (event) => {
    const pick = event.target.closest('button[data-pick]');
    if (!pick) return;
    dialog.answer(pick.dataset.pick);
    DS.overlay.close(dialog);
  });
  const picked = await shown;
  if (!picked) return;
  if (picked === 'paste') return importServers(opener);
  const preset = GALLERY.find((one) => one.id === picked);
  return editServer(null, opener, preset ? JSON.parse(JSON.stringify(preset.server)) : null);
}

// importServers takes servers as a .mcp.json or a program's docs write them -
// {"mcpServers": {name: {...}}}, or the inner object alone - and adds them,
// replacing one of the same name; Save keeps them.
async function importServers(opener) {
  const text = el('textarea', { class: 'ds-input', id: 'mcpImportText', rows: 8, spellcheck: 'false', autocomplete: 'off', autofocus: true,
    placeholder: '{"mcpServers": {"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"]}}}' });
  const problem = el('div', { class: 'so-admin-notice' });
  problem.hidden = true;
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const accept = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Add the servers' });
  const { dialog, shown } = openDialog({
    title: 'Paste MCP servers',
    field: el('div', { class: 'ds-form' }, problem, field('JSON', text, 'As a .mcp.json has it. Each server comes in on in every chat.', 'mcpImportText')),
    actions: [cancel, accept], opener,
  });
  accept.addEventListener('click', () => {
    let list;
    try {
      list = fromJSON(text.value);
    } catch (err) {
      text.setAttribute('aria-invalid', 'true');
      showProblem(problem, err.message);
      text.focus();
      return;
    }
    dialog.answer(list);
    DS.overlay.close(dialog);
  });
  const list = await shown;
  if (!list) return;
  const names = new Set(list.map((one) => one.name));
  servers = [...servers.filter((one) => !names.has(one.name)), ...list].sort(byName);
  render();
  toast(list.length === 1 ? 'One server added. Save to keep it.' : list.length + ' servers added. Save to keep them.');
}

function fromJSON(text) {
  let data;
  try {
    data = JSON.parse(text);
  } catch (err) {
    throw new Error('That is not JSON: ' + err.message);
  }
  if (data && typeof data === 'object' && data.mcpServers) data = data.mcpServers;
  if (!data || typeof data !== 'object' || Array.isArray(data) || !Object.keys(data).length) {
    throw new Error('There is no server in it: write {"mcpServers": {"name": {...}}}.');
  }
  const out = [];
  for (const [name, entry] of Object.entries(data)) {
    if (!entry || typeof entry !== 'object') throw new Error('The server ' + name + ' is not an object.');
    const command = String(entry.command || '').trim();
    const url = String(entry.url || '').trim();
    let type = String(entry.type || '').trim().toLowerCase();
    if (!type) type = !command && url ? 'http' : 'stdio';
    if (type === 'streamable-http' || type === 'streamablehttp') type = 'http';
    if (!['stdio', 'http', 'sse'].includes(type)) throw new Error('The server ' + name + ' has the type ' + type + '; stdio, http and sse are known.');
    const server = type === 'stdio'
      ? { name, type, command, args: (entry.args || []).map(String), env: entry.env || {}, default: true }
      : { name, type, url, headers: entry.headers || {}, default: true };
    if (entry.description) server.description = String(entry.description);
    const bad = problemOf(server, servers.find((one) => one.name === name));
    if (bad) throw new Error(name + ': ' + bad[1]);
    out.push(server);
  }
  return out;
}
