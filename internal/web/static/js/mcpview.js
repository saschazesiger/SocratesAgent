// mcpview.js: what Admin, a chat and the Tests page say about an MCP server
// in the same words: where a server comes from, and the outcome of a test of
// one (POST /api/mcp/test, POST /api/sessions/{id}/mcp/test): the server's
// name and version, how long it took, its tools, prompts and resources, and
// what went wrong when it did not work.
//
// A test result ("probe") is
//   {ok, state: ok|auth|error, error, at, latency_ms, protocol_version,
//    server_name, server_title, server_version, instructions, capabilities[],
//    tools[{name,title,description}], prompts[], resources[], stderr, oauth}.

import { el, openDialog, errorMessage } from './api.js';
import { html, raw, icon, morph } from './ui.js';

/* ----------------------------------------------------------------- words */

/** fromWords is where a chat's server comes from, as its switch's hint says it. */
export function fromWords(server) {
  const from = server.from === 'socrates'
    ? 'Built into Socrates'
    : server.from === 'admin'
      ? 'From Admin · ' + (server.default ? 'on' : 'off') + ' in every chat'
      : 'From the folder’s .mcp.json';
  const transport = server.transport && server.transport !== 'stdio' && server.from !== 'socrates'
    ? ' · ' + String(server.transport).toUpperCase() : '';
  return from + transport;
}

/** labelOf is a server's name as a person reads it: its title when it has one. */
export function labelOf(server) {
  return (server && (server.title || server.name)) || '';
}

/** agoText is how long ago a moment (Unix ms) was. */
export function agoText(at, now = Date.now()) {
  if (!at) return '';
  const s = Math.max(0, Math.round((now - at) / 1000));
  if (s < 45) return 'just now';
  if (s < 3600) return Math.round(s / 60) + ' min ago';
  if (s < 86400) return Math.round(s / 3600) + ' h ago';
  const days = Math.round(s / 86400);
  return days === 1 ? 'yesterday' : days + ' days ago';
}

/** untilText is how long until a moment (Unix ms), or that it passed. */
export function untilText(at, now = Date.now()) {
  if (!at) return '';
  const s = Math.round((at - now) / 1000);
  if (s <= 0) return 'expired ' + agoText(at, now);
  if (s < 3600) return 'in ' + Math.max(1, Math.round(s / 60)) + ' min';
  if (s < 86400) return 'in ' + Math.round(s / 3600) + ' h';
  const days = Math.round(s / 86400);
  return 'in ' + days + (days === 1 ? ' day' : ' days');
}

function plural(n, one, many = one + 's') {
  return n + ' ' + (n === 1 ? one : many);
}

/** brief is the first sentence of a server's description, for a switch's hint. */
export function brief(text, max = 110) {
  const flat = String(text || '').trim().replace(/\s+/g, ' ');
  const end = flat.search(/\.(\s|$)/);
  const one = end > 0 ? flat.slice(0, end + 1) : flat;
  return one.length > max ? one.slice(0, max - 1).trimEnd() + '\u2026' : one;
}

/** firstLine is the first line of a text, cut at `max` characters. */
export function firstLine(text, max = 140) {
  const line = String(text || '').trim().split('\n')[0].trim();
  return line.length > max ? line.slice(0, max - 1).trimEnd() + '…' : line;
}

/* ----------------------------------------------------------------- chips */

const CHIPS = {
  ok: ['ds-status ds-status--ok', 'check'],
  warning: ['ds-status ds-status--warning', 'alert'],
  error: ['ds-status ds-status--error', 'alertCircle'],
  running: ['ds-status ds-status--running', null],
  plain: ['ds-status', 'clock'],
  off: ['ds-status', 'ban'],
};

/** chip is a DS status chip: always an icon (or a spinner) and a word. */
export function chip(kind, label, state = kind) {
  const [cls, glyph] = CHIPS[kind] || CHIPS.plain;
  const mark = glyph ? raw(icon(glyph, 16)) : html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>`;
  return html`<span class="${cls}" data-state="${state}">${mark}<span>${label}</span></span>`;
}

/** parts is a run of short facts ("12 tools · 230 ms"); DS wraps it between parts. */
export function parts(list) {
  const shown = list.filter((part) => part !== null && part !== undefined && part !== '' && part !== false);
  if (!shown.length) return '';
  return html`<span class="ds-parts"><span class="ds-parts__list">${shown.map((part) => html`<span>${part}</span>`)}</span></span>`;
}

/** counts is what a server offers, in words: "12 tools · 2 prompts". */
export function counts(probe) {
  const tools = (probe.tools || []).length;
  const prompts = (probe.prompts || []).length;
  const resources = (probe.resources || []).length;
  return [plural(tools, 'tool'), prompts ? plural(prompts, 'prompt') : '', resources ? plural(resources, 'resource') : '']
    .filter(Boolean).join(' · ');
}

/**
 * probeState is a test's outcome as one chip and its facts: what a row says
 * about the last test of its server.
 */
export function probeState(probe) {
  if (!probe) return { kind: 'plain', label: 'Not tested yet', state: 'untested', facts: [] };
  if (probe.state === 'ok') {
    return {
      kind: 'ok', label: plural((probe.tools || []).length, 'tool'), state: 'ok',
      facts: [probe.latency_ms ? probe.latency_ms + ' ms' : '', 'tested ' + agoText(probe.at)],
    };
  }
  if (probe.state === 'auth') return { kind: 'warning', label: 'Needs sign-in', state: 'auth', facts: ['tested ' + agoText(probe.at)] };
  return { kind: 'error', label: 'Failed', state: 'error', facts: ['tested ' + agoText(probe.at)], error: firstLine(probe.error, 160) };
}

/* ---------------------------------------------------------- probe's body */

// shortText is the first paragraph of a tool's description, which is often
// pages long; the whole text is the tool's tooltip.
function shortText(text, max = 220) {
  const para = String(text || '').trim().split(/\n\s*\n/)[0].replace(/\s+/g, ' ');
  return para.length > max ? para.slice(0, max - 1).trimEnd() + '…' : para;
}

/** prose is a text of the server's (its instructions) as paragraphs. */
export function prose(text) {
  const paras = String(text || '').trim().split(/\n\s*\n/).filter(Boolean);
  return html`<div class="ds-prose ds-prose--dense so-mcp-prose">${paras.map((p) => html`<p>${p.trim()}</p>`)}</div>`;
}

/**
 * toolList is a server's tools: each a switch "agents may use it" when
 * `toolOn` says whether it is on (Admin), else a plain row (a chat).
 */
export function toolList(tools, { toolOn = null, idPrefix = 'mcpTool' } = {}) {
  if (!tools.length) return html`<p class="ds-meta">It offers no tool.</p>`;
  if (toolOn) {
    return html`<div class="ds-switch-list" role="group" aria-label="Tools agents may use">${tools.map((tool, i) => html`<label class="ds-switch" data-ds-key="${'tool-' + tool.name}">
      <input type="checkbox" role="switch" data-tool="${tool.name}"${toolOn(tool.name) ? raw(' checked') : ''} aria-labelledby="${idPrefix + i}" aria-describedby="${idPrefix + i + 'Hint'}">
      <span class="ds-switch__track" aria-hidden="true"></span>
      <span class="ds-switch__label so-mcp-tool__name" id="${idPrefix + i}">${tool.name}</span>
      <small class="ds-switch__hint" id="${idPrefix + i + 'Hint'}">${shortText(tool.description) || (tool.title || 'No description.')}</small>
      <span class="ds-switch__state" aria-hidden="true"><span class="ds-switch__on">On</span><span class="ds-switch__off">Off</span></span>
    </label>`)}</div>`;
  }
  return html`<ul class="so-mcp-toollist" aria-label="Tools">${tools.map((tool) => html`<li data-ds-key="${'tool-' + tool.name}">
      <span class="so-mcp-tool__name">${tool.name}</span>
      ${tool.description || tool.title ? html`<span class="ds-meta">${shortText(tool.description) || tool.title}</span>` : ''}
    </li>`)}</ul>`;
}

/**
 * probeView is a test's outcome in full: what happened, who answered, what it
 * offers. `toolOn` makes the tools switches (Admin); `alertActions` is
 * markup for buttons under the outcome (Admin's Connect or Use OAuth).
 */
export function probeView(probe, { toolOn = null, alertActions = '', idPrefix = 'mcpTool', isOpen = (key, dflt) => dflt } = {}) {
  // A re-render keeps a disclosure as the person left it (morph drops an
  // `open` its markup lacks): `isOpen` says, `dflt` when nobody touched it.
  const open = (key, dflt = false) => (isOpen(key, dflt) ? raw(' open') : '');
  const tools = probe.tools || [];
  const prompts = probe.prompts || [];
  const resources = probe.resources || [];
  let outcome;
  if (probe.state === 'ok') {
    outcome = html`<div class="ds-alert ds-alert--success" role="status">${raw(icon('checkCircle', 20))}<span class="ds-alert__text">${'It works: ' + counts(probe) + '.'}</span></div>`;
  } else {
    const kind = probe.state === 'auth' ? 'warning' : 'error';
    outcome = html`<div class="${'ds-alert ds-alert--' + kind}" role="alert">${raw(icon(kind === 'warning' ? 'alert' : 'alertCircle', 20))}<div class="ds-alert__text"><p class="ds-alert__title">${probe.state === 'auth' ? 'It wants a sign-in' : 'It did not work'}</p><p class="so-break">${probe.error || 'No answer.'}</p></div>${alertActions ? html`<div class="ds-alert__actions">${alertActions}</div>` : ''}</div>`;
  }
  const who = [probe.server_title || probe.server_name, probe.server_version].filter(Boolean).join(' ');
  const facts = [
    ['Server', who],
    ['Answered in', probe.latency_ms ? probe.latency_ms + ' ms' : ''],
    ['Protocol', probe.protocol_version],
    ['Tested', agoText(probe.at)],
  ].filter(([, value]) => value);
  return html`<div class="so-mcp-probe">
    ${outcome}
    ${facts.length ? html`<dl class="ds-facts">${facts.map(([label, value]) => html`<div><dt>${label}</dt><dd class="so-break">${value}</dd></div>`)}</dl>` : ''}
    ${probe.state === 'ok' ? html`<section class="so-mcp-probe__part" aria-label="Tools">
      <h3 class="ds-card__subtitle">${'Tools (' + tools.length + ')'}</h3>
      ${toolOn && tools.length ? html`<p class="ds-meta">Switched off, a tool is given to no agent. Saved with the page.</p>` : ''}
      ${toolList(tools, { toolOn, idPrefix })}
    </section>` : ''}
    ${prompts.length ? html`<details class="ds-collapsible ds-disclosure" data-ds-key="prompts"${open('prompts')}><summary>${'Prompts (' + prompts.length + ')'}${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body"><ul class="so-mcp-toollist">${prompts.map((p) => html`<li><span class="so-mcp-tool__name">${p.name}</span>${p.description ? html`<span class="ds-meta">${shortText(p.description)}</span>` : ''}</li>`)}</ul></div></details>` : ''}
    ${resources.length ? html`<details class="ds-collapsible ds-disclosure" data-ds-key="resources"${open('resources')}><summary>${'Resources (' + resources.length + ')'}${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body"><ul class="so-mcp-toollist">${resources.map((r) => html`<li><span class="so-mcp-tool__name">${r.title || r.name || r.uri}</span><span class="ds-meta so-break">${[r.uri, r.mimeType].filter(Boolean).join(' · ')}</span></li>`)}</ul></div></details>` : ''}
    ${probe.instructions ? html`<details class="ds-collapsible ds-disclosure" data-ds-key="instructions"${open('instructions')}><summary>What agents are told${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body">${prose(probe.instructions)}</div></details>` : ''}
    ${probe.stderr ? html`<details class="ds-collapsible ds-disclosure" data-ds-key="stderr"${open('stderr', probe.state !== 'ok')}><summary>What it printed${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="ds-disclosure__body"><pre class="ds-code-block ds-code-block--wrap"><code>${tail(probe.stderr, 40)}</code></pre></div></details>` : ''}
  </div>`;
}

// tail is the last lines of a log.
function tail(text, lines) {
  const all = String(text || '').replace(/\s+$/, '').split('\n');
  return all.slice(-lines).join('\n');
}

/**
 * probeDialog tests a server and shows the outcome in a dialog: `run` is the
 * request that tests it. Used where there is no sheet for it (a chat).
 */
export function probeDialog({ title, run, opener }) {
  const body = el('div', { class: 'ds-dialog__body', tabindex: '0', role: 'region', 'aria-label': 'Test of ' + title });
  morph(body, html`<div class="ds-loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span>Connecting to the server as an agent would</div>`);
  const close = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, autofocus: true, text: 'Close' });
  const { dialog, shown } = openDialog({ title, field: body, actions: [close], opener });
  dialog.classList.add('ds-dialog--wide');
  run().then((probe) => {
    if (dialog.isConnected) morph(body, probeView(probe));
  }, (err) => {
    if (dialog.isConnected) morph(body, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${errorMessage(err)}</span></div>`);
  });
  return shown;
}
