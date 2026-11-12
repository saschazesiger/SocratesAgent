// cloudflare.js: reaching Socrates from anywhere through a Cloudflare tunnel,
// drawn into a container of Admin's "Remote access" card and of the second
// step of the first-run page (setup.html).
//
// The person creates a Cloudflare API token through a link with the
// permissions filled in, pastes it and checks it (POST /api/cloudflare/check
// lists the domains it may use, and the Cloudflare user when it may read
// it), then picks a domain, a subdomain and the e-mail addresses that may
// sign in. POST /api/cloudflare/setup creates the tunnel, the DNS record and
// Cloudflare Access in the background; the page polls GET /api/cloudflare
// and shows the steps as a DS task list. A name that is already taken on
// Cloudflare stops the setup with its conflicts, and Replace runs it again
// with replace: true once the person agreed. Through the tunnel Cloudflare
// Access signs the person in; the password stays for the local address.
//
// Once it runs, the card shows the tunnel's state, its address, who may sign
// in (PUT /api/cloudflare/emails), Repair (the setup again, with the token
// Socrates keeps) and Remove (POST /api/cloudflare/remove, in the background
// too). Everything is drawn with morph(), so the fields keep what is typed
// into them while the status changes under them.

import { api, toast, errorMessage, isOffline, confirmDialog, onWake } from './api.js';
import { M, html, raw, icon, morph, faces } from './ui.js';

const $ = (id) => document.getElementById(id);

// How often the page asks while something moves: a job, a tunnel that is
// still connecting, an open log.
const POLL_JOB = 1500;
const POLL_LIVE = 4000;

// The status chip: always an icon (or the spinner) and a word.
const CHIP = {
  ok: ['ds-status ds-tone-success', 'check'],
  warning: ['ds-status ds-tone-warning', 'alert'],
  error: ['ds-status ds-tone-error', 'alertCircle'],
  running: ['ds-status ds-status--running', null],
  off: ['ds-status', 'ban'],
};

function chip(kind, label, state) {
  const [cls, glyph] = CHIP[kind];
  const mark = glyph ? raw(icon(glyph, 16)) : html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>`;
  return html`<span class="${cls}" data-state="${state}">${mark}<span>${label}</span></span>`;
}

// stateOf is the tunnel's state as a chip: [kind, label, machine word].
function stateOf(st) {
  const job = st.job || {};
  if (job.running) return job.kind === 'remove' ? ['running', 'Removing', 'removing'] : ['running', 'Setting up', 'setting-up'];
  if (!st.configured) return st.partial ? ['warning', 'Half set up', 'partial'] : ['off', 'Not set up', 'off'];
  if (st.error) return ['error', 'Problem', 'error'];
  if (st.downloading) return ['running', 'Downloading cloudflared', 'downloading'];
  if (!st.running || !st.connected) return ['running', 'Connecting', 'connecting'];
  if (!st.verified) return ['running', 'Checking from outside', 'checking'];
  return ['ok', 'Up', 'up'];
}

// sentence makes a server's "enter at least one e-mail address" read as one.
function sentence(text) {
  const s = String(text || '').trim();
  if (!s) return '';
  const head = s.charAt(0).toUpperCase() + s.slice(1);
  return /[.!?…]$/.test(head) ? head : head + '.';
}

// emailsOf is the addresses in a textarea: one per line, commas and spaces
// tolerated.
function emailsOf(text) {
  return String(text || '').split(/[\s,;]+/).map((e) => e.trim()).filter(Boolean);
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i;

function plural(n, one, many) {
  return n + ' ' + (n === 1 ? one : many);
}

/**
 * mountCloudflare draws the remote access into `host` and keeps it current.
 * `level` is the heading level of the steps (3 in a card, 2 under a page
 * title), `log` adds Admin's "Show log", `local` is the local address, and
 * `onChange(data)` hears every new GET /api/cloudflare payload (via, email,
 * status). Returns { refresh }.
 */
export function mountCloudflare(host, { level = 3, log = false, local = '', onChange = () => {} } = {}) {
  let data = null;          // GET /api/cloudflare
  let loadError = '';
  let timer = null;
  let watching = false;     // a job this page saw running: its end is shown
  let editing = false;      // the form is open over a tunnel that exists (Repair)
  let logOpen = false;
  let checking = false;     // the stored token is being checked (Repair)
  let checked = null;       // POST /api/cloudflare/check's answer
  let notice = '';          // an API error of the last action
  let errors = {};          // field id -> what is wrong with it
  let focusField = '';
  let addresses = null;     // the "who may sign in" text while edited
  const form = { token: '', zoneId: '', sub: 'socrates', emails: '' };
  const h = 'h' + level;

  /* ------------------------------------------------------------ fetching */

  async function refresh() {
    clearTimeout(timer);
    try {
      take(await api('/api/cloudflare'));
      loadError = '';
    } catch (err) {
      if (isOffline(err)) {
        // The connection bar says so; keep asking.
        timer = setTimeout(refresh, POLL_LIVE);
        return;
      }
      loadError = errorMessage(err);
    }
    render();
  }

  // take keeps a payload and notices a job that ended while the page watched.
  function take(next) {
    const before = data && data.status && data.status.job;
    data = next;
    const st = data.status || {};
    const job = st.job || {};
    if (job.running) watching = true;
    if (before && before.running && !job.running) ended(job);
    if (!form.emails && data.via === 'tunnel' && data.email) form.emails = data.email;
    try { onChange(data); } catch (err) { console.error(err); }
  }

  function ended(job) {
    if (job.kind === 'remove') {
      if (job.ok) {
        toast('Remote access is removed.', 'success');
        watching = false;
        editing = false;
        checked = null;
        form.token = '';
      }
      return;
    }
    if (job.ok) {
      // The alert under the steps says where it is now.
      editing = false;
      form.token = '';
      addresses = null;
    }
  }

  function schedule() {
    clearTimeout(timer);
    const st = data && data.status;
    if (!st) return;
    let wait = 0;
    if (st.job && st.job.running) wait = POLL_JOB;
    else if (logOpen || (st.configured && (!st.connected || !st.verified || st.downloading || st.error))) wait = POLL_LIVE;
    if (wait) timer = setTimeout(refresh, wait);
  }

  /* ------------------------------------------------------------- drawing */

  function render() {
    if (!host.isConnected) return;
    if (!data) {
      morph(host, html`<div class="ds-stack" data-ds-key="cf">
        <div class="ds-cluster" role="status" data-ds-key="state">${loadError ? chip('error', 'Unknown', 'unknown') : chip('running', 'Loading', 'loading')}</div>
        ${loadError ? alertHtml('error', sentence(loadError)) : ''}
      </div>`);
      return;
    }
    const st = data.status || {};
    const job = st.job || {};
    const showForm = !job.running && (editing || (!st.configured && !st.partial));
    morph(host, html`<div class="ds-stack" data-ds-key="cf">
      ${stateHtml(st)}
      ${loadError ? alertHtml('error', sentence(loadError), 'load') : ''}
      ${st.error && !job.running && !(watching && job.error) && !(job.conflicts || []).length ? alertHtml('error', sentence(st.error), 'status-error') : ''}
      ${!job.running && st.configured && !editing ? configuredHtml(st) : ''}
      ${!job.running && !st.configured && st.partial && !editing ? partialHtml() : ''}
      ${showForm ? formHtml(st) : ''}
      ${watching || job.running ? jobHtml(st, job) : ''}
      ${!job.running && (job.conflicts || []).length ? conflictsHtml(job) : ''}
      ${notice ? alertHtml('error', sentence(notice), 'notice') : ''}
      ${log ? logHtml(st) : ''}
    </div>`);
    if (focusField) {
      const field = $(focusField);
      focusField = '';
      if (field) field.focus();
    }
    const pre = $('cfLog');
    if (pre && !pre.hidden) pre.scrollTop = pre.scrollHeight;
    schedule();
  }

  function alertHtml(kind, text, key = kind) {
    const [cls, role, glyph] = kind === 'error' ? ['ds-alert ds-tone-error', 'alert', 'alertCircle']
      : kind === 'warning' ? ['ds-alert ds-tone-warning', 'status', 'alert']
        : kind === 'ok' ? ['ds-alert ds-tone-success', 'status', 'checkCircle'] : ['ds-alert', 'status', 'info'];
    return html`<div class="${cls}" role="${role}" data-ds-key="${'alert-' + key}">${raw(icon(glyph, 20))}<span class="ds-alert__text">${text}</span></div>`;
  }

  function stateHtml(st) {
    const [kind, label, word] = stateOf(st);
    const facts = [];
    if (st.configured && st.public_url) facts.push(html`<a href="${st.public_url}" target="_blank" rel="noopener" class="so-break">${st.public_url}</a>`);
    return html`<div class="ds-cluster" role="status" data-ds-key="state">${chip(kind, label, word)}${facts.length
      ? html`<span class="ds-parts"><span class="ds-parts__list">${facts.map((f) => html`<span>${f}</span>`)}</span></span>` : ''}</div>`;
  }

  // jobHtml is the steps of the setup or removal: done, running, failed.
  function jobHtml(st, job) {
    const done = job.done || [];
    const items = done.map((text) => ({ text, status: 'done' }));
    if (job.step) items.push({ text: job.step, status: job.running ? 'running' : job.error ? 'failed' : 'done' });
    if (!items.length && job.running) items.push({ text: 'Starting', status: 'running' });
    const title = job.kind === 'remove' ? 'Removing remote access' : 'Setting up remote access';
    const tail = job.running ? '' : job.ok
      ? (job.kind === 'remove' ? alertHtml('ok', 'Removed. Socrates is on its local address only now.', 'job-ok')
        : html`<div class="ds-alert ds-tone-success" role="status" data-ds-key="alert-job-ok">${raw(icon('checkCircle', 20))}<span class="ds-alert__text">Socrates is at <a href="${st.public_url}" target="_blank" rel="noopener" class="so-break">${st.public_url}</a>. Opening it asks for a Cloudflare login: a one-time code goes by e-mail to the addresses that may sign in.</span></div>`)
      : job.error && !(job.conflicts || []).length ? alertHtml('error', sentence(job.error), 'job-error') : '';
    return html`<div class="ds-stack" data-ds-key="${'job-' + (job.kind || 'none')}">
      <div class="ds-tasks__head"><${raw(h)} class="ds-card__subtitle">${title}</${raw(h)}><p class="ds-meta">${plural(done.length + (job.ok && job.step ? 1 : 0), 'step', 'steps')} done</p></div>
      <ol class="ds-tasks" aria-label="${title}">${items.map((item, i) => html`<li class="ds-tasks__item" data-ds-key="${'s' + i}" data-status="${item.status}"><span class="ds-tasks__mark" aria-hidden="true">${
        item.status === 'running' ? html`<span class="ds-spinner ds-spinner--ink ds-sm"></span>`
          : item.status === 'done' ? raw(icon('checkCircle', 20))
            : raw(icon('alertCircle', 20))
      }</span><span class="ds-tasks__text">${item.text}<span class="ds-sr">${': ' + (item.status === 'running' ? 'running' : item.status)}</span></span></li>`)}</ol>
      ${tail}
    </div>`;
  }

  function conflictsHtml(job) {
    return html`<div class="ds-alert ds-tone-warning" role="status" data-ds-key="conflicts">${raw(icon('alert', 20))}<div class="ds-alert__text">
      <p class="ds-alert__title">This address is already taken on Cloudflare</p>
      <ul class="ds-plain-list">${job.conflicts.map((c) => html`<li class="so-break">${c.detail || c.name}</li>`)}</ul>
      <p>Choose another subdomain, or let Socrates replace what is there.</p>
    </div><div class="ds-alert__actions"><button class="ds-btn ds-sm" type="button" data-cf="replace">${faces('Replace', 'Starting')}</button></div></div>`;
  }

  function configuredHtml(st) {
    const bin = st.cloudflared || {};
    const binary = bin.installed
      ? [bin.version || 'installed', bin.managed ? 'downloaded by Socrates' : bin.path].filter(Boolean).join(' · ')
      : (bin.can_install ? 'not installed; Socrates downloads it' : 'not installed');
    const facts = [
      ['Cloudflare account', st.account || '—'],
      ['Domain', st.zone || '—'],
      local ? ['Local address', local] : null,
      ['cloudflared', binary],
    ].filter(Boolean);
    const text = addresses !== null ? addresses : (st.emails || []).join('\n');
    return html`<div class="ds-stack" data-ds-key="configured">
      <dl class="ds-facts">${facts.map(([label, value]) => html`<div><dt>${label}</dt><dd class="so-break">${value}</dd></div>`)}</dl>
      <p class="ds-meta">Opening the address asks for a Cloudflare login: a one-time code goes by e-mail to the addresses below. The password is only needed on the local address.</p>
      <div class="ds-field">
        <label class="ds-field__label" for="cfAddresses">Who may sign in</label>
        <textarea class="ds-input" id="cfAddresses" rows="3" spellcheck="false" autocomplete="off" autocapitalize="off" aria-describedby="${errors.cfAddresses ? 'cfAddressesError cfAddressesHint' : 'cfAddressesHint'}"${raw(errors.cfAddresses ? ' aria-invalid="true"' : '')}>${text}</textarea>
        ${errorHtml('cfAddresses')}
        <small class="ds-field__hint" id="cfAddressesHint">One e-mail address per line.</small>
      </div>
      <div class="ds-form-actions">
        <button class="ds-btn" type="button" data-cf="save-emails">${faces('Save addresses', 'Saving', 'Saved')}</button>
        <button class="ds-btn ds-btn--text" type="button" data-cf="repair">${raw(icon('refresh', 16))}<span>Repair</span></button>
        <button class="ds-btn ds-btn--text" type="button" data-cf="remove">${faces('Remove remote access', 'Starting')}</button>
      </div>
    </div>`;
  }

  function partialHtml() {
    return html`<div class="ds-alert ds-tone-warning" role="status" data-ds-key="partial">${raw(icon('alert', 20))}<div class="ds-alert__text"><p>A setup stopped half way: some of it exists on Cloudflare, but no tunnel runs. Run the setup again to finish it, or remove what was made.</p></div><div class="ds-alert__actions">
      <button class="ds-btn ds-btn--text ds-sm" type="button" data-cf="remove">${faces('Remove', 'Starting')}</button>
      <button class="ds-btn ds-sm" type="button" data-cf="repair">Set up again</button>
    </div></div>`;
  }

  function errorHtml(id) {
    return errors[id]
      ? html`<small class="ds-field__hint ds-field__hint--error" id="${id + 'Error'}">${raw(icon('alertCircle', 16))}${errors[id]}</small>`
      : '';
  }

  function described(id, hint) {
    return [errors[id] ? id + 'Error' : '', hint].filter(Boolean).join(' ');
  }

  function invalid(id) {
    return raw(errors[id] ? ' aria-invalid="true"' : '');
  }

  function zoneOf(id) {
    return ((checked && checked.zones) || []).find((z) => z.id === id) || null;
  }

  function hostname() {
    const zone = zoneOf(form.zoneId);
    const sub = form.sub.trim().toLowerCase();
    return zone && sub ? sub + '.' + zone.name : '';
  }

  function hostHint() {
    const name = hostname();
    return name ? 'Socrates will be at https://' + name + '.' : 'Enter the name Socrates gets under the domain.';
  }

  function formHtml(st) {
    const perms = data.permissions || [];
    const stored = !!st.has_token;
    const zones = (checked && checked.zones) || [];
    const accounts = [...new Set(zones.map((z) => z.account_name).filter(Boolean))];
    const zone = zoneOf(form.zoneId);
    return html`<div class="ds-stack" data-ds-key="form">
      <section class="ds-card__section" aria-labelledby="cfStep1" data-ds-key="step1">
        <${raw(h)} class="ds-card__subtitle" id="cfStep1">1. Create an API token</${raw(h)}>
        <p>The link opens Cloudflare with the permissions filled in. Choose the account and the domain there, then create the token and copy it.</p>
        <div class="ds-cluster"><a class="ds-btn" href="${data.token_url}" target="_blank" rel="noopener">${raw(icon('external', 16))}<span>Create the token at Cloudflare</span></a></div>
        <div class="ds-table-wrap">
          <table class="ds-table ds-density-tight">
            <caption class="ds-sr">Permissions of the API token</caption>
            <thead><tr><th scope="col" class="ds-nowrap">Scope</th><th scope="col">Permission</th><th scope="col" class="ds-nowrap">Access</th></tr></thead>
            <tbody>${perms.map((p) => html`<tr><td class="ds-nowrap">${p.scope}</td><td>${p.name}${p.optional ? html` <span class="ds-tag ds-tone-neutral">Optional</span>` : ''}</td><td class="ds-nowrap">${p.level}</td></tr>`)}</tbody>
          </table>
        </div>
        <p class="ds-meta">Cloudflare Zero Trust's free plan is enough. Socrates keeps the token encrypted on this machine and needs it again only to change the addresses, to repair or to remove the tunnel.</p>
      </section>

      <section class="ds-card__section" aria-labelledby="cfStep2" data-ds-key="step2">
        <${raw(h)} class="ds-card__subtitle" id="cfStep2">2. Paste and check the token</${raw(h)}>
        <div class="ds-field">
          <label class="ds-field__label" for="cfToken">API token${stored ? html` <span class="ds-field__optional">· optional</span>` : ''}</label>
          <div class="ds-field__row">
            <input class="ds-input" type="password" id="cfToken" value="${form.token}" autocomplete="off" spellcheck="false" autocapitalize="off" enterkeyhint="go" aria-describedby="${described('cfToken', stored ? 'cfTokenHint' : '')}"${invalid('cfToken')}>
            <button class="ds-btn" type="button" data-cf="check">${faces('Check token', 'Checking')}</button>
          </div>
          ${errorHtml('cfToken')}
          ${stored ? html`<small class="ds-field__hint" id="cfTokenHint">Empty uses the token Socrates keeps.</small>` : ''}
        </div>
        ${checking ? html`<div class="ds-loading" role="status" data-ds-key="checking"><span class="ds-loader ds-sm" aria-hidden="true"></span>Asking Cloudflare which domains the token may use</div>` : ''}
        ${checked ? html`<div class="ds-alert ds-tone-success" role="status" data-ds-key="checked">${raw(icon('checkCircle', 20))}<div class="ds-alert__text">
          <p>Valid: may use ${plural(zones.length, 'domain', 'domains')}.</p>
          ${checked.user ? html`<p>Cloudflare user: ${checked.user}</p>` : ''}
          ${accounts.length ? html`<p>${accounts.length === 1 ? 'Account' : 'Accounts'}: ${accounts.join(', ')}</p>` : ''}
        </div></div>` : ''}
      </section>

      <section class="ds-card__section" aria-labelledby="cfStep3" data-ds-key="step3">
        <${raw(h)} class="ds-card__subtitle" id="cfStep3">3. Address and who may sign in</${raw(h)}>
        ${!checked ? html`<p class="ds-meta" data-ds-key="wait">Check the token first: then the domains it may use are listed here.</p>` : html`<div class="ds-form" data-ds-key="fields">
          <div class="ds-field">
            <label class="ds-field__label" for="cfZone">Domain</label>
            <select class="ds-input" id="cfZone" aria-describedby="${described('cfZone', '')}"${invalid('cfZone')}>
              ${zone ? '' : html`<option value="" selected>Choose a domain</option>`}
              ${zones.map((z) => html`<option value="${z.id}"${raw(z.id === form.zoneId ? ' selected' : '')}>${z.name}</option>`)}
            </select>
            ${errorHtml('cfZone')}
          </div>
          <div class="ds-field">
            <label class="ds-field__label" for="cfSub">Subdomain</label>
            <div class="ds-input-group">
              <input class="ds-input" type="text" id="cfSub" value="${form.sub}" spellcheck="false" autocomplete="off" autocapitalize="off" enterkeyhint="next" aria-describedby="${described('cfSub', 'cfSubHint')}"${invalid('cfSub')}>
              <span class="ds-input-group__suffix" aria-hidden="true">${zone ? '.' + zone.name : ''}</span>
            </div>
            ${errorHtml('cfSub')}
            <small class="ds-field__hint" id="cfSubHint">${hostHint()}</small>
          </div>
          <div class="ds-field">
            <label class="ds-field__label" for="cfEmails">Who may sign in</label>
            <textarea class="ds-input" id="cfEmails" rows="3" spellcheck="false" autocomplete="off" autocapitalize="off" aria-describedby="${described('cfEmails', 'cfEmailsHint')}"${invalid('cfEmails')}>${form.emails}</textarea>
            ${errorHtml('cfEmails')}
            <small class="ds-field__hint" id="cfEmailsHint">One e-mail address per line. Cloudflare sends each a one-time code to sign in with.</small>
          </div>
          <div class="ds-form-actions">
            <button class="ds-btn ds-btn--filled" type="button" data-cf="setup">${faces('Set up the tunnel', 'Starting')}</button>
            ${editing ? html`<button class="ds-btn ds-btn--text" type="button" data-cf="cancel">Cancel</button>` : ''}
          </div>
        </div>`}
        ${!checked && editing ? html`<div class="ds-form-actions" data-ds-key="cancel-only"><button class="ds-btn ds-btn--text" type="button" data-cf="cancel">Cancel</button></div>` : ''}
      </section>
    </div>`;
  }

  function logHtml(st) {
    const lines = (st.logs || []).join('\n') || 'no output yet';
    return html`<div class="ds-stack" data-ds-key="log">
      <div class="ds-cluster"><button class="ds-btn ds-btn--text ds-sm" type="button" data-cf="log" aria-expanded="${logOpen ? 'true' : 'false'}" aria-controls="cfLog">${logOpen ? 'Hide log' : 'Show log'}</button></div>
      <pre class="ds-code-block ds-code-block--wrap so-log" id="cfLog" tabindex="0" aria-label="Tunnel log"${raw(logOpen ? '' : ' hidden')}>${logOpen ? lines : ''}</pre>
    </div>`;
  }

  /* ------------------------------------------------------------- actions */

  function setError(id, text) {
    errors = { ...errors, [id]: text };
    focusField = id;
    render();
  }

  async function check(button, { quiet = false } = {}) {
    const token = form.token.trim();
    const st = data.status || {};
    errors = {};
    notice = '';
    if (!token && !st.has_token) {
      setError('cfToken', 'Paste the API token first.');
      return;
    }
    const work = api('/api/cloudflare/check', { method: 'POST', body: { api_token: token }, attempts: 1, timeout: 70000 });
    if (!button) { checking = true; render(); }
    try {
      const res = button ? await M.busy(button, work, { done: false }) : await work;
      checked = { zones: res.zones || [], user: res.user || '', stored: !!res.stored };
      if (!zoneOf(form.zoneId)) form.zoneId = checked.zones.length === 1 ? checked.zones[0].id : '';
      if (!form.emails.trim() && checked.user) form.emails = checked.user;
      if (!checked.zones.length) errors = { cfToken: 'The token may use no domain. Give it the domain under Zone Resources.' };
      else if (!quiet) focusField = form.zoneId ? 'cfSub' : 'cfZone';
    } catch (err) {
      checked = null;
      if (isOffline(err)) notice = errorMessage(err);
      else { errors = { cfToken: sentence(errorMessage(err)) }; if (!quiet) focusField = 'cfToken'; }
    } finally {
      checking = false;
    }
    render();
  }

  // validated is the setup's request, or null after marking what is wrong.
  function validated() {
    errors = {};
    if (!checked) { setError('cfToken', 'Check the token first.'); return null; }
    const zone = zoneOf(form.zoneId);
    if (!zone) { setError('cfZone', 'Choose a domain.'); return null; }
    const sub = form.sub.trim().toLowerCase();
    if (!sub) { setError('cfSub', 'Enter a subdomain, like socrates.'); return null; }
    if (!sub.split('.').every((part) => LABEL.test(part))) {
      setError('cfSub', 'Use letters, digits and hyphens, like socrates.');
      return null;
    }
    const emails = emailsOf(form.emails);
    if (!emails.length) { setError('cfEmails', 'Enter at least one e-mail address.'); return null; }
    const bad = emails.find((e) => !EMAIL.test(e));
    if (bad) { setError('cfEmails', '“' + bad + '” is not an e-mail address.'); return null; }
    if (data.via === 'tunnel' && data.email && !emails.some((e) => e.toLowerCase() === data.email.toLowerCase())) {
      setError('cfEmails', 'Keep your own address (' + data.email + ') on the list, or you lock yourself out.');
      return null;
    }
    return { api_token: form.token.trim(), zone_id: zone.id, hostname: sub + '.' + zone.name, emails };
  }

  async function setup(button, replace = false) {
    const body = validated();
    if (!body) return;
    if (replace) {
      const yes = await confirmDialog({
        title: 'Replace what is on Cloudflare?',
        body: 'Socrates deletes the existing DNS record or Access application for ' + body.hostname
          + ' and creates its own. Whatever it pointed to is no longer reachable under that name.',
        confirmLabel: 'Delete and replace', danger: true, opener: button,
      });
      if (!yes) return;
    }
    notice = '';
    try {
      const res = await M.busy(button, api('/api/cloudflare/setup', {
        method: 'POST', body: { ...body, replace }, attempts: 1, timeout: 30000,
      }), { done: false });
      watching = true;
      take(res);
    } catch (err) {
      notice = errorMessage(err);
    }
    render();
  }

  async function saveEmails(button) {
    const text = addresses !== null ? addresses : ((data.status || {}).emails || []).join('\n');
    const emails = emailsOf(text);
    errors = {};
    if (!emails.length) { setError('cfAddresses', 'Enter at least one e-mail address.'); return; }
    const bad = emails.find((e) => !EMAIL.test(e));
    if (bad) { setError('cfAddresses', '“' + bad + '” is not an e-mail address.'); return; }
    notice = '';
    try {
      const res = await M.busy(button, api('/api/cloudflare/emails', { method: 'PUT', body: { emails }, attempts: 1, timeout: 70000 }));
      addresses = null;
      take(res);
      toast('Saved: ' + plural(emails.length, 'address', 'addresses') + ' may sign in.', 'success');
    } catch (err) {
      if (isOffline(err)) notice = errorMessage(err);
      else errors = { cfAddresses: sentence(errorMessage(err)) };
    }
    render();
  }

  async function remove(button) {
    const st = data.status || {};
    const away = data.via === 'tunnel';
    const yes = await confirmDialog({
      title: 'Remove remote access?',
      body: 'Socrates stops the tunnel and deletes it on Cloudflare, with its DNS record and its Access applications. '
        + 'Socrates stays reachable on its local address' + (local ? ' (' + local + ')' : '') + ' with the password.'
        + (away ? ' You came through the tunnel: this page loses its connection once the tunnel is gone.' : ''),
      confirmLabel: 'Remove' + (st.public_url ? ' ' + st.hostname : ''), danger: true, opener: button,
    });
    if (!yes) return;
    notice = '';
    try {
      const res = await M.busy(button, api('/api/cloudflare/remove', { method: 'POST', body: {}, attempts: 1 }), { done: false });
      watching = true;
      editing = false;
      take(res);
    } catch (err) {
      notice = errorMessage(err);
    }
    render();
  }

  function repair() {
    const st = data.status || {};
    editing = true;
    errors = {};
    notice = '';
    form.token = '';
    if (st.zone_id) form.zoneId = st.zone_id;
    if (st.hostname && st.zone && st.hostname.endsWith('.' + st.zone)) form.sub = st.hostname.slice(0, -st.zone.length - 1);
    if ((st.emails || []).length) form.emails = st.emails.join('\n');
    checked = null;
    watching = false;
    if (!st.has_token) focusField = 'cfToken';
    render();
    if (st.has_token) check(null, { quiet: true });
  }

  function onClick(event) {
    const button = event.target.closest('[data-cf]');
    if (!button || !data) return;
    const what = button.dataset.cf;
    if (what === 'check') check(button);
    else if (what === 'setup') setup(button);
    else if (what === 'replace') setup(button, true);
    else if (what === 'save-emails') saveEmails(button);
    else if (what === 'remove') remove(button);
    else if (what === 'repair') repair();
    else if (what === 'cancel') {
      editing = false;
      errors = {};
      notice = '';
      render();
    } else if (what === 'log') {
      logOpen = !logOpen;
      if (logOpen) refresh(); else render();
    }
  }

  // What is typed is kept in `form`, so that a redraw puts it back.
  function onInput(event) {
    const t = event.target;
    const clear = (id) => {
      if (!errors[id]) return false;
      const { [id]: _gone, ...rest } = errors;
      errors = rest;
      return true;
    };
    if (t.id === 'cfToken') {
      form.token = t.value;
      // Another token is another set of domains: check it again.
      const had = !!checked;
      checked = null;
      if (clear('cfToken') || had) render();
    } else if (t.id === 'cfSub') {
      form.sub = t.value;
      const hint = $('cfSubHint');
      if (hint) hint.textContent = hostHint();
      if (clear('cfSub')) render();
    } else if (t.id === 'cfEmails') {
      form.emails = t.value;
      if (clear('cfEmails')) render();
    } else if (t.id === 'cfAddresses') {
      addresses = t.value;
      if (clear('cfAddresses')) render();
    }
  }

  function onChangeField(event) {
    if (event.target.id !== 'cfZone') return;
    form.zoneId = event.target.value;
    const { cfZone: _gone, ...rest } = errors;
    errors = rest;
    render();
  }

  // Enter in the token checks it; in the subdomain it sets up.
  function onKey(event) {
    if (event.key !== 'Enter' || event.isComposing) return;
    const id = event.target.id;
    if (id === 'cfToken') {
      event.preventDefault();
      const button = host.querySelector('[data-cf="check"]');
      if (button) check(button);
    } else if (id === 'cfSub') {
      event.preventDefault();
      const button = host.querySelector('[data-cf="setup"]');
      if (button) setup(button);
    }
  }

  host.addEventListener('click', onClick);
  host.addEventListener('input', onInput);
  host.addEventListener('change', onChangeField);
  host.addEventListener('keydown', onKey);
  onWake(() => {
    if (document.visibilityState !== 'hidden') refresh();
  });

  render();
  refresh();
  return { refresh };
}
