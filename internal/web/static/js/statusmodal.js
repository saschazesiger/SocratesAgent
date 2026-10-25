// The Status of a chat, as a modal of its own (assist.js opens it).
//
// It opens the moment Status is pressed and says what is being done until the
// answer is in; then it shows, in the order a glance wants them: where the
// chat is (a status chip), what runs right now, three figures - how long it
// has been at it, how long the rest will probably take, how old the session
// is - the task list with its progress, and the summary. The times are the
// server's clock, counted on here while the modal is open. The spoken version
// of the same is read out loud beside it, and the modal's own button stops
// or starts it.

import { el } from './api.js';
import { DS, html, raw, icon, morph } from './ui.js';
import { agentMarkHtml } from './logos.js';

// The words of the chip, by committed state, and the DS status it wears.
const STATE = {
  busy: { label: 'Working', kind: 'running' },
  tasks: { label: 'Background work', kind: 'running' },
  waiting: { label: 'Waiting for you', kind: 'warning', icon: 'chat' },
  idle: { label: 'Idle', kind: 'ok', icon: 'checkCircle' },
};

// spanWords is a duration as a person reads it: "40 s", "12 min",
// "3 h 20 min", "2 d 4 h".
export function spanWords(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return s + ' s';
  const m = Math.floor(s / 60);
  if (m < 60) return m + ' min';
  const h = Math.floor(m / 60);
  if (h < 24) return h + ' h' + (m % 60 ? ' ' + (m % 60) + ' min' : '');
  return Math.floor(h / 24) + ' d' + (h % 24 ? ' ' + (h % 24) + ' h' : '');
}

const clockTime = (at) => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

/**
 * openStatusModal shows the modal at once, in its loading state.
 *
 * `opts`: session, opener, speaking() whether a reading is on, toggle() the
 * read-aloud button's press.
 *
 * It answers { phase(text), show(data), fail(text), paint(), isOpen() }.
 */
export function openStatusModal({ session, opener, speaking, toggle }) {
  const titleId = 'statusTitle' + Date.now();
  const body = el('div', { class: 'so-status__body', 'aria-live': 'polite' });
  const speakBtn = el('button', { class: 'ds-btn ds-btn--tonal so-status__speak', type: 'button', hidden: true });
  const close = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', 'data-close': true, text: 'Close' });
  const dialog = el('dialog', { class: 'ds-dialog so-status', 'aria-labelledby': titleId },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box so-status__box' }));
  const box = dialog.querySelector('.so-status__box');
  morph(box, html`<header class="so-status__head"><span class="so-status__mark" aria-hidden="true">${raw(agentMarkHtml(session.harness, 24))}</span><div class="so-status__titles"><p class="so-status__overline">Status</p><h2 class="ds-dialog__title so-status__title" id="${titleId}" tabindex="-1" autofocus>${session.title || 'Chat'}</h2></div><button class="ds-btn ds-btn--quiet ds-btn--icon so-status__x" type="button" data-close aria-label="Close" data-tip="Close">${raw(icon('close', 20))}</button></header>`);
  box.append(body, el('div', { class: 'ds-dialog__actions so-status__actions' }, speakBtn, close));
  speakBtn.addEventListener('click', () => toggle());

  let data = null;
  let skew = 0;
  let open = true;
  let timer = null;

  function phase(text) {
    if (data || !open) return;
    morph(body, html`<div class="ds-loading so-status__loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span><span>${text}</span></div>`);
  }

  function fail(text) {
    if (!open) return;
    morph(body, html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${text}</span></div>`);
  }

  // since is how long ago a moment of the server's clock was, by this one.
  const since = (at) => spanWords(Date.now() - skew - at);

  function draw() {
    const a = data.analysis || {};
    const c = data.clock || {};
    const state = STATE[data.state] || { label: 'Unknown', kind: '' };
    const chip = html`<span class="${'ds-status' + (state.kind ? ' ds-status--' + state.kind : '')}">${
      state.kind === 'running' ? html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>` : state.icon ? raw(icon(state.icon, 16)) : ''
    }<span>${state.label}</span></span>`;

    // The first figure: how long it has been at what it is doing now.
    let run = null;
    if (c.asked_at) run = { label: 'Waiting for you', value: since(c.asked_at), note: 'since ' + clockTime(c.asked_at), glyph: 'chat' };
    else if (c.turn_started) run = { label: 'Running for', value: since(c.turn_started), note: 'since ' + clockTime(c.turn_started), glyph: 'activity' };
    else if (c.last_ended) run = { label: 'Idle for', value: since(c.last_ended), note: 'finished ' + clockTime(c.last_ended), glyph: 'clock' };

    // The second: the estimate, or what stands instead of one.
    const minutes = a.eta_minutes;
    let left = '';
    if (typeof minutes === 'number') left = minutes < 1 ? '< 1 min' : '~' + spanWords(minutes * 60000);
    else if (data.state === 'waiting') left = 'Your turn';
    else if (data.state === 'idle') left = 'Done';
    else left = '—';

    // The third: the session as a whole.
    const turns = Number(c.turns) || 0;
    const age = c.first_message ? since(c.first_message) : '—';

    const tasks = Array.isArray(data.tasks) ? data.tasks : [];
    const done = tasks.filter((t) => t.status === 'completed').length;
    const share = tasks.length ? done / tasks.length : 0;

    morph(body, html`<div class="so-status__state">${chip}${data.model ? html`<span class="so-status__by">${'Read by ' + data.model.replace(/^[^/]*\//, '')}</span>` : ''}</div>
<section class="so-status__now" aria-label="Right now"><p class="so-status__label">${raw(icon('activity', 16))}<span>Right now</span></p><p class="so-status__now-text">${a.now || '—'}</p>${
  c.call && c.call_started ? html`<p class="so-status__call"><code class="so-break">${c.call}</code><span>${' · ' + since(c.call_started)}</span></p>` : ''
}</section>
<div class="ds-tiles so-status__tiles">${run ? html`<div class="ds-tile"><p class="ds-tile__label">${raw(icon(run.glyph, 16))}<span>${run.label}</span></p><p class="ds-tile__value" data-live="run">${run.value}</p><p class="ds-tile__note">${run.note}</p></div>` : ''}<div class="ds-tile so-status__eta"><p class="ds-tile__label">${raw(icon('sparkles', 16))}<span>Expected</span></p><p class="ds-tile__value">${left}</p><p class="ds-tile__note so-status__clamp" data-tip="${a.eta || ''}">${a.eta || ''}</p></div><div class="ds-tile"><p class="ds-tile__label">${raw(icon('history', 16))}<span>Session</span></p><p class="ds-tile__value" data-live="age">${age}</p><p class="ds-tile__note">${turns + (turns === 1 ? ' turn' : ' turns') + ' · ' + spanWords(Number(c.worked_ms) || 0) + ' of work'}</p></div></div>
${tasks.length ? html`<section class="so-status__tasks" aria-label="Tasks"><div class="so-status__tasks-head"><p class="so-status__label">${raw(icon('list', 16))}<span>Tasks</span></p><span class="so-status__count">${done + ' of ' + tasks.length + ' done'}</span></div><div class="ds-progress ds-progress--flat" style="${'--ds-progress: ' + share}"><div class="ds-progress__track" role="progressbar" aria-label="Tasks done" aria-valuemin="0" aria-valuemax="${tasks.length}" aria-valuenow="${done}"><div class="ds-progress__bar"></div></div></div><ul class="so-status__list">${tasks.map((t) => html`<li class="so-status__task" data-status="${t.status}">${
  t.status === 'completed' ? raw(icon('checkCircle', 16)) : t.status === 'in_progress' ? html`<span class="ds-spinner ds-spinner--sm" aria-hidden="true"></span>` : html`<span class="so-status__dot" aria-hidden="true"></span>`
}<span>${t.subject}</span></li>`)}</ul></section>` : ''}
${a.summary ? html`<section class="so-status__summary" aria-label="Summary"><p class="so-status__label">${raw(icon('fileText', 16))}<span>Summary</span></p><p class="so-status__text">${a.summary}</p></section>` : ''}`);
  }

  // tick moves the live figures on, without drawing anything else again.
  function tick() {
    if (!data || !open) return;
    const c = data.clock || {};
    const run = body.querySelector('[data-live="run"]');
    const at = c.asked_at || c.turn_started || c.last_ended;
    if (run && at) run.textContent = since(at);
    const age = body.querySelector('[data-live="age"]');
    if (age && c.first_message) age.textContent = since(c.first_message);
  }

  function paint() {
    if (!open) return;
    const on = speaking();
    speakBtn.hidden = !data;
    speakBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
    morph(speakBtn, html`${raw(icon(on ? 'circleStop' : 'volume', 18))}<span>${on ? 'Stop reading' : 'Read aloud'}</span>`);
  }

  function show(next) {
    if (!open) return;
    data = next;
    skew = Date.now() - (Number((next.clock || {}).now) || Date.now());
    draw();
    paint();
    clearInterval(timer);
    timer = setInterval(tick, 1000);
  }

  const was = opener || document.activeElement;
  document.body.append(dialog);
  DS.overlay.open(dialog, { opener: was });
  dialog.addEventListener('close', async () => {
    open = false;
    clearInterval(timer);
    await DS.overlay.close(dialog);
    dialog.remove();
  }, { once: true });
  phase('Reading the chat…');
  paint();
  return { phase, show, fail, paint, isOpen: () => open, dialog };
}
