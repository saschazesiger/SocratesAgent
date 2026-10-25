// The accounts the chats run on (REBUILD.md §4): one agent's subscription or
// API key each. The Accounts card of the dashboard manages them, the
// new-session sheet and a chat's composer choose between them, and this
// module is what all three know of them: the list, how an account is said
// (its state, its usage, when its limit renews), and the sign-in flows.
//
// The list is the server's, read again whenever it matters (a sheet opening,
// a menu opening, after every change) and kept in this browser for the moment
// in between, so a choice is never drawn empty. Nothing here ever holds a
// key: the server never answers one.

import { api, el, toast, errorMessage, clientKey, openDialog } from './api.js';
import { DS, html, raw, icon, iconEl, morph } from './ui.js';
import * as harnesses from './harnesses.js';
import { typeHere } from './keyboard.js';

const CACHE_KEY = 'socrates.accounts';
// How often a sign-in in progress is asked about.
const POLL_MS = 1500;

let cached = readCache();
let loading = null;
const listeners = new Set();

function readCache() {
  try {
    const parsed = JSON.parse(localStorage.getItem(CACHE_KEY) || 'null');
    if (parsed && Array.isArray(parsed.accounts)) return parsed;
  } catch { /* private window */ }
  return { accounts: [], recommended: {}, agents: [] };
}

function writeCache(data) {
  try { localStorage.setItem(CACHE_KEY, JSON.stringify(data)); } catch { /* full or private */ }
}

/** load reads the list from the server; the calls of one moment share it. */
export function load() {
  if (loading) return loading;
  loading = (async () => {
    try {
      const data = await api('/api/accounts', { attempts: 1, timeout: 15000 });
      cached = { accounts: data.accounts || [], recommended: data.recommended || {}, agents: data.agents || [] };
      writeCache(cached);
      for (const listener of [...listeners]) listener(cached);
      return cached;
    } finally {
      loading = null;
    }
  })();
  return loading;
}

/** onChange calls listener with every new list; it answers the unsubscribe. */
export function onChange(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** all is every account, in the person's order. */
export function all() { return cached.accounts; }

/** agents is every agent an account can belong to. */
export function agents() { return cached.agents && cached.agents.length ? cached.agents : ['claude', 'codex', 'opencode']; }

/** forAgent is an agent's accounts, in order. */
export function forAgent(agent) { return cached.accounts.filter((a) => a.agent === agent); }

/** byId is one account, or null. */
export function byId(id) { return cached.accounts.find((a) => a.id === id) || null; }

/** recommended is the account a new chat of an agent should run on. */
export function recommended(agent) { return (cached.recommended || {})[agent] || ''; }

/** labelOf is what an account is called, for an id the list may not have. */
export function labelOf(id) {
  const found = byId(id);
  return found ? found.label : '';
}

export const KIND_WORDS = { subscription: 'Subscription', api_key: 'API key' };

/** usable says whether a chat can run on an account. */
export function usable(account) { return !!account && account.state === 'signed_in'; }

/**
 * stateChip is an account's state as a DS status chip: always a glyph and a
 * word, the machine's own word in data-state.
 */
export function stateChip(account) {
  switch (account.state) {
    case 'signed_in':
      return html`<span class="ds-status ds-status--ok" data-state="signed_in">${raw(icon('check', 16))}<span>Signed in</span></span>`;
    case 'error':
      return html`<span class="ds-status ds-status--error" data-state="error">${raw(icon('alertCircle', 16))}<span>Sign-in failed</span></span>`;
    default:
      return html`<span class="ds-status ds-status--warning" data-state="needs_sign_in">${raw(icon('alert', 16))}<span>${account.state_detail ? 'Sign in again' : 'Not signed in'}</span></span>`;
  }
}

// limitEnd is the moment an account that ran into its limit can be used
// again, in milliseconds, or 0.
function limitEnd(account) {
  let at = Number(account.limited_until) || 0;
  for (const w of account.limits || []) {
    const reset = w.resets_at ? new Date(w.resets_at).getTime() : 0;
    if ((Number(w.used_percent) || 0) >= 100 && reset > at) at = reset;
  }
  return at;
}

/**
 * renewal is when an account that ran into its limit can be used again, as
 * "renews in 2 h 10 min" (or "in 3 d 4 h" for a week's limit), or '' when it
 * is not at a limit.
 */
export function renewal(account, now = Date.now()) {
  const at = limitEnd(account);
  if (!(at > now)) return '';
  // A number and its unit never part at the end of a line (a no-break
  // space): the account menu at 390 broke "renews in 2" from its "h".
  const minutes = Math.ceil((at - now) / 60000);
  if (minutes < 60) return 'renews in ' + minutes + '\u00a0min';
  if (minutes >= 24 * 60) {
    const d = Math.floor(minutes / (24 * 60));
    const h = Math.floor((minutes % (24 * 60)) / 60);
    return 'renews in ' + d + '\u00a0d' + (h ? ' ' + h + '\u00a0h' : '');
  }
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return 'renews in ' + h + '\u00a0h' + (m ? ' ' + m + '\u00a0min' : '');
}

/**
 * renewalAt is the same moment by the clock: "today at 15:42", "tomorrow at
 * 09:10", "on Thu at 09:10" within the week and the date after it, or '' when
 * the account is not at a limit. The countdown is what a glance at the button
 * needs; this is what a tap says (DECISIONS rev 7: "a tap says exactly when"),
 * for a phone has no tooltip.
 */
export function renewalAt(account, now = Date.now()) {
  const at = limitEnd(account);
  if (!(at > now)) return '';
  const when = new Date(at);
  const clock = when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((day(when) - day(new Date(now))) / 86400000);
  if (days === 0) return 'today at ' + clock;
  if (days === 1) return 'tomorrow at ' + clock;
  if (days < 7) return 'on ' + when.toLocaleDateString([], { weekday: 'short' }) + ' at ' + clock;
  return 'on ' + when.toLocaleDateString([], { day: 'numeric', month: 'short' }) + ' at ' + clock;
}

/**
 * usageWords is an account's windows in a line: "5h 40% · 7d 60%". A window
 * whose reset has passed is empty again, whatever the last reading said.
 */
export function usageWords(account, now = Date.now()) {
  return (account.limits || []).filter((w) => w.label === '5h' || w.label === '7d')
    .map((w) => {
      const reset = w.resets_at ? new Date(w.resets_at).getTime() : 0;
      const used = reset && reset <= now ? 0 : Number(w.used_percent) || 0;
      return w.label + ' ' + Math.round(used) + '%';
    }).join(' · ');
}

/** meters is an account's windows as the sheet's flat bars. */
export function meters(account) { return harnesses.limitMeters(account.limits || []); }

/**
 * who is the quiet line beside an account's name: its kind. The e-mail and
 * the plan stay behind the account's "i" (facts).
 */
export function who(account) { return KIND_WORDS[account.kind] || account.kind; }

/**
 * entry is an account as a list to choose from shows it - the sheet's
 * dropdown and a chat's account menu: its name, who it is, why it cannot be
 * used or when its limit renews, the "most left" tag when it is the one to
 * take, and its usage meters, so the choice is made with the numbers in view.
 */
export function entry(account, { recommended: best = false, current = false } = {}) {
  const meta = [html`<span>${who(account)}</span>`];
  if (!usable(account)) {
    meta.push(html`<span data-tag="signin">${account.state === 'error' ? 'sign-in failed' : 'not signed in'}</span>`);
  }
  const renew = renewal(account);
  if (renew) meta.push(html`<span data-tag="limited">${'at its limit, ' + renew + ', ' + renewalAt(account)}</span>`);
  if (current) meta.push(html`<span data-tag="current">this chat’s</span>`);
  if (best) meta.push(html`<span data-tag="recommended">most left</span>`);
  return html`<span class="ds-combo__name">${account.label}</span><span class="ds-combo__meta">${meta.map((part, i) => (i ? html` · ${part}` : part))}</span>${meters(account)}`;
}

/**
 * best is the account a new chat of an agent should start on: the one
 * recommended, else the first that is signed in, else the first.
 */
export function best(agent) {
  const list = forAgent(agent);
  return list.find((a) => a.id === recommended(agent))
    || list.find((a) => usable(a) && !renewal(a)) || list.find(usable) || list[0] || null;
}

/**
 * facts is what an account's "i" says: who it is, what kind, where it came
 * from. Technical detail stays behind the "i" (DESIGN §E.10).
 */
export function facts(account) {
  const out = [KIND_WORDS[account.kind] || account.kind];
  if (account.email) out.push('Account: ' + account.email);
  if (account.plan) out.push((account.agent === 'opencode' ? 'Providers: ' : 'Plan: ') + account.plan);
  if (account.provider) out.push('Provider: ' + account.provider);
  if (account.state_detail) out.push(account.state_detail);
  const origin = originWords(account.origin);
  if (origin) out.push(origin);
  const renew = renewal(account);
  if (renew) out.push('At its limit, ' + renew);
  out.push('Chats on it: ' + (account.chats || 0));
  return out;
}

// originWords is where the migration found an account: an agent's own
// folder in HOME, or an old profile's. One made of a profile whose folders
// were lost says so in its state instead.
function originWords(origin) {
  if (!origin) return '';
  if (origin.startsWith('~/')) return 'Moved in from ' + origin;
  if (origin.startsWith('profile:')) return 'Moved in from the old profile of this name';
  return '';
}

/* ----------------------------------------------------------------- signing in */

// signIn runs an account's sign-in to its end: the page Claude Code's own
// sign-in shows and the code it gives, Codex's device code, an OpenCode
// provider's OAuth, or nothing for the scripted agent. It answers true when
// the account is signed in, false otherwise.
export async function signIn(account, opener) {
  let data;
  try {
    data = await api('/api/accounts/' + encodeURIComponent(account.id) + '/signin', { method: 'POST', body: {}, attempts: 1, timeout: 95000 });
  } catch (err) {
    toast(errorMessage(err), 'error');
    return false;
  }
  const flow = data.signin || {};
  if (flow.state === 'done') {
    await load().catch(() => {});
    toast(account.label + ' is signed in.', 'success');
    return true;
  }
  return flowDialog(account, flow, opener);
}

// flowDialog shows a sign-in under way and follows it to its end: the
// providers to choose from (OpenCode), the page to open and the code to type
// there, or the code the page shows to paste here.
function flowDialog(account, first, opener) {
  const content = el('div', { class: 'so-signin' });
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const next = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', hidden: true, text: 'Continue' });
  const { dialog, shown } = openDialog({
    title: 'Sign in to ' + account.label,
    body: account.agent === 'claude'
      ? 'Claude Code signs in with its own page. Your password stays there; the code it shows is handed to Claude Code on this machine.'
      : harnesses.label(account.agent) + ' signs in with its own page. Nothing you type there reaches Socrates.',
    field: content, actions: [cancel, next], opener,
  });
  const path = '/api/accounts/' + encodeURIComponent(account.id) + '/signin';
  let flow = first;
  let timer = 0;
  let choice = null;
  let ended = false;

  function draw() {
    next.hidden = true;
    if (flow.state === 'choose') {
      drawProviders();
      return;
    }
    const parts = [];
    if (flow.code) {
      parts.push(html`<p class="ds-meta">Open the page, and type this code there:</p><p class="so-signin__code" data-ds-key="code"><span class="so-signin__value" aria-label="${'The code ' + flow.code}">${flow.code}</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="copy-code" aria-label="Copy the code" data-tip="Copy the code">${raw(icon('copy', 18))}</button></p>`);
    } else if (flow.instructions) {
      parts.push(html`<p class="ds-meta" data-ds-key="instructions">${flow.instructions}</p>`);
    }
    if (flow.url) {
      parts.push(html`<p data-ds-key="url"><a class="ds-btn ds-btn--tonal" href="${flow.url}" target="_blank" rel="noopener">${raw(icon('external', 18))}<span>Open the sign-in page</span></a></p>`);
    }
    if (flow.state === 'code') {
      parts.push(html`<div class="ds-field" data-ds-key="paste"><label class="ds-field__label" for="signinCode">The code the page shows</label><input class="ds-input" id="signinCode" type="text" autocomplete="off" spellcheck="false" enterkeyhint="go"></div>`);
      next.hidden = false;
      next.textContent = 'Sign in';
    }
    if (flow.state === 'waiting') {
      parts.push(html`<p class="ds-loading so-signin__wait" role="status" data-ds-key="wait"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Waiting for the sign-in…</p>`);
    }
    if (flow.state === 'failed') {
      parts.push(html`<div class="ds-alert ds-alert--error" role="alert" data-ds-key="failed">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${flow.error || 'The sign-in did not finish.'}</span></div>`);
    }
    morph(content, html`${parts}`);
  }

  // drawProviders is OpenCode's providers, each with the ways a subscription
  // signs in to it, and the questions the chosen one asks first.
  function drawProviders() {
    const options = [];
    for (const p of flow.providers || []) {
      for (const m of p.methods || []) options.push({ key: p.id + ':' + m.index, provider: p.id, method: m });
    }
    if (!options.length) {
      morph(content, html`<div class="ds-alert" role="status">${raw(icon('info', 20))}<span class="ds-alert__text">OpenCode offers no provider to sign in to here. An API key is added as an account of its own.</span></div>`);
      return;
    }
    if (!choice) choice = options[0];
    const prompts = (choice.method.prompts || []);
    morph(content, html`<fieldset class="ds-field"><legend class="ds-field__label">Provider</legend><div class="ds-radios so-signin__providers">${options.map((o) => html`<label class="ds-radio" data-ds-key="${o.key}"><input type="radio" name="signinProvider" value="${o.key}"><span><strong>${o.provider}</strong><small>${o.method.label}</small></span></label>`)}</div></fieldset>${prompts.map((q) => promptField(q))}`);
    for (const input of content.querySelectorAll('input[name="signinProvider"]')) input.checked = input.value === choice.key;
    next.hidden = false;
    next.textContent = 'Continue';
  }

  function promptField(q) {
    const id = 'signinPrompt-' + q.key;
    if (q.type === 'select' && q.options && q.options.length) {
      return html`<div class="ds-field" data-ds-key="${id}"><label class="ds-field__label" for="${id}">${q.message}</label><select class="ds-input" id="${id}" data-prompt="${q.key}">${q.options.map((o) => html`<option value="${o.value}">${o.label}</option>`)}</select></div>`;
    }
    return html`<div class="ds-field" data-ds-key="${id}"><label class="ds-field__label" for="${id}">${q.message}</label><input class="ds-input" id="${id}" type="text" data-prompt="${q.key}" placeholder="${q.placeholder || ''}" autocomplete="off" spellcheck="false" enterkeyhint="next"></div>`;
  }

  content.addEventListener('change', (event) => {
    if (event.target.name !== 'signinProvider') return;
    const [provider, index] = event.target.value.split(':');
    for (const p of flow.providers || []) {
      if (p.id !== provider) continue;
      const method = (p.methods || []).find((m) => String(m.index) === index);
      if (method) choice = { key: event.target.value, provider, method };
    }
    drawProviders();
  });
  content.addEventListener('click', (event) => {
    const target = event.target.closest('[data-action="copy-code"]');
    if (!target) return;
    navigator.clipboard.writeText(flow.code || '').then(() => toast('Copied.'), () => toast('The code could not be copied.', 'error'));
  });

  async function poll() {
    if (ended) return;
    try {
      const data = await api(path, { attempts: 1, timeout: 10000 });
      flow = data.signin || flow;
    } catch { /* the next poll asks again */ }
    if (flow.state === 'done') {
      finish(true);
      return;
    }
    draw();
    if (flow.state === 'waiting' || flow.state === 'code') timer = setTimeout(poll, POLL_MS);
  }

  async function finish(ok) {
    ended = true;
    clearTimeout(timer);
    if (ok) {
      dialog.answer(true);
      DS.overlay.close(dialog);
      await load().catch(() => {});
      toast(account.label + ' is signed in.', 'success');
    }
  }

  next.addEventListener('click', async () => {
    if (next.disabled) return;
    next.disabled = true;
    try {
      if (flow.state === 'choose' && choice) {
        const inputs = {};
        for (const field of content.querySelectorAll('[data-prompt]')) inputs[field.dataset.prompt] = field.value;
        const data = await api(path, { method: 'POST', body: { provider: choice.provider, method: choice.method.index, inputs }, attempts: 1, timeout: 60000 });
        flow = data.signin || flow;
        draw();
        clearTimeout(timer);
        timer = setTimeout(poll, POLL_MS);
        return;
      }
      if (flow.state === 'code') {
        const code = content.querySelector('#signinCode');
        const data = await api(path + '/code', { method: 'POST', body: { code: code ? code.value : '' }, attempts: 1, timeout: 60000 });
        flow = data.signin || flow;
        if (flow.state === 'done') finish(true);
        else draw();
      }
    } catch (err) {
      toast(errorMessage(err), 'error');
    } finally {
      next.disabled = false;
    }
  });

  // Enter in the code's field is Sign in.
  content.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter' || event.isComposing || event.target.id !== 'signinCode') return;
    event.preventDefault();
    if (!next.hidden && !next.disabled) next.click();
  });

  draw();
  if (flow.state === 'waiting' || flow.state === 'code') timer = setTimeout(poll, POLL_MS);
  return shown.then(async (answer) => {
    if (!ended && !answer) {
      ended = true;
      clearTimeout(timer);
      api(path, { method: 'DELETE', attempts: 1 }).catch(() => {});
    }
    return !!answer;
  });
}

/* ----------------------------------------------------------- adding an account */

/**
 * addAccountDialog asks for a new account - its agent, a subscription or an
 * API key, its name, and the key - adds it, and signs a subscription in
 * straight away. It answers the account, or null.
 */
export function addAccountDialog(opener, agent = '') {
  const choices = agents();
  const pick = { agent: agent || choices[0], kind: 'subscription' };
  const content = el('div', { class: 'so-account-form' });
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const add = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Add' });
  // Drawn before it opens, so that the focus goes to the Name (autofocus),
  // not to Cancel, the only control of an empty dialog.
  draw();
  const { dialog, shown } = openDialog({ title: 'Add an account', field: content, actions: [cancel, add], opener });

  function draw() {
    const key = pick.kind === 'api_key';
    morph(content, html`<fieldset class="ds-field" data-ds-key="agent"><legend class="ds-field__label">Agent</legend><div class="ds-chips">${choices.map((a) => html`<label class="ds-chip" data-ds-key="${'agent-' + a}"><input class="ds-chip__input" type="radio" name="accountAgent" value="${a}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${harnesses.label(a)}</span></span></label>`)}</div></fieldset><fieldset class="ds-field" data-ds-key="kind"><legend class="ds-field__label">Kind</legend><div class="ds-chips">${Object.entries(KIND_WORDS).map(([value, label]) => html`<label class="ds-chip" data-ds-key="${'kind-' + value}"><input class="ds-chip__input" type="radio" name="accountKind" value="${value}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${label}</span></span></label>`)}</div><small class="ds-field__hint">${key ? 'The key is kept encrypted on this machine and is never shown again.' : 'You sign in with the agent’s own page once the account is added.'}</small></fieldset><div class="ds-field" data-ds-key="label"><label class="ds-field__label" for="accountLabel">Name</label><input class="ds-input" id="accountLabel" type="text" maxlength="40" placeholder="Work" autocomplete="off" autofocus enterkeyhint="${key ? 'next' : 'done'}"></div>${
      key ? html`<div class="ds-field" data-ds-key="key"><label class="ds-field__label" for="accountKey">API key</label><input class="ds-input" id="accountKey" type="password" autocomplete="off" spellcheck="false" enterkeyhint="${pick.agent === 'opencode' ? 'next' : 'done'}"></div>` : ''
    }${
      key && pick.agent === 'opencode' ? html`<div class="ds-field" data-ds-key="provider"><label class="ds-field__label" for="accountProvider">Provider</label><input class="ds-input" id="accountProvider" type="text" placeholder="openrouter" autocomplete="off" spellcheck="false" list="accountProviders" enterkeyhint="done"><datalist id="accountProviders">${providerIds().map((p) => html`<option value="${p}"></option>`)}</datalist><small class="ds-field__hint">The OpenCode provider the key is for.</small></div>` : ''
    }`);
    for (const input of content.querySelectorAll('input[name="accountAgent"]')) input.checked = input.value === pick.agent;
    for (const input of content.querySelectorAll('input[name="accountKind"]')) input.checked = input.value === pick.kind;
  }

  content.addEventListener('change', (event) => {
    if (event.target.name === 'accountAgent') pick.agent = event.target.value;
    else if (event.target.name === 'accountKind') pick.kind = event.target.value;
    else return;
    const label = content.querySelector('#accountLabel').value;
    draw();
    content.querySelector('#accountLabel').value = label;
  });

  // Enter does what the keyboard's key says (enterkeyhint): from the name
  // to the key when there is one, from the key to OpenCode's provider, and
  // from the last field it adds, as Add does.
  content.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter' || event.isComposing || event.shiftKey || event.ctrlKey || event.altKey || event.metaKey) return;
    const order = ['#accountLabel', '#accountKey', '#accountProvider'].map((one) => content.querySelector(one)).filter(Boolean);
    const at = order.indexOf(event.target);
    if (at < 0) return;
    event.preventDefault();
    if (at < order.length - 1) typeHere(order[at + 1]);
    else add.click();
  });

  let made = null;
  add.addEventListener('click', async () => {
    const body = { agent: pick.agent, kind: pick.kind, label: content.querySelector('#accountLabel').value.trim() };
    const key = content.querySelector('#accountKey');
    const provider = content.querySelector('#accountProvider');
    if (key) body.key = key.value;
    if (provider) body.provider = provider.value.trim();
    if (!body.label) {
      toast('Give the account a name.');
      content.querySelector('#accountLabel').focus();
      return;
    }
    try {
      const data = await api('/api/accounts', { method: 'POST', body, attempts: 1, timeout: 45000 });
      made = data.account;
      dialog.answer(true);
      DS.overlay.close(dialog);
    } catch (err) {
      toast(errorMessage(err), 'error');
    }
  });

  return shown.then(async () => {
    if (!made) return null;
    await load().catch(() => {});
    if (made.state === 'error') toast(made.label + ': ' + (made.state_detail || 'the key was not taken.'), 'error');
    else if (made.kind === 'subscription' && made.state !== 'signed_in') await signIn(made, opener);
    else toast(made.label + ' was added.', 'success');
    return made;
  });
}

// providerIds is the OpenCode providers the catalogue's models name, for the
// API-key form's suggestions.
function providerIds() {
  const seen = new Set();
  for (const m of harnesses.offered('opencode')) {
    const [provider] = String(m.id || '').split('/');
    if (provider) seen.add(provider);
  }
  return [...seen].sort();
}

/* ----------------------------------------------------------- switching a chat */

/**
 * switchChat moves a chat to another account (POST /api/chats/{id}/account):
 * now, interrupting a turn that runs, or after it; with go on set the new
 * account is told to continue the turn a limit stopped.
 */
export async function switchChat(chatId, accountId, { when = 'now', goOn = false } = {}) {
  return api('/api/chats/' + encodeURIComponent(chatId) + '/account', {
    method: 'POST', body: { client_id: clientKey(), account: accountId, when, continue: goOn }, attempts: 2, timeout: 20000,
  });
}

/** setAutoSwitch turns a chat's automatic switch on a limit on or off. */
export async function setAutoSwitch(chatId, on) {
  return api('/api/chats/' + encodeURIComponent(chatId) + '/auto-switch', { method: 'PUT', body: { on }, attempts: 2 });
}

/** glyph is the icon an account is marked with in a list. */
export function glyph(account) { return account && account.kind === 'api_key' ? 'key' : 'user'; }

/** iconFor is the same glyph as an element. */
export function iconFor(account, size = 16) { return iconEl(glyph(account), size); }
