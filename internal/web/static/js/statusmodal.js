// The Status of a chat, as a modal of its own (assist.js opens it).
//
// It opens the moment Status is pressed and says what is being done until the
// answer is in. Then it answers the one question a glance at it asks - when
// will it be done? - and below it what the agent does right now, what is
// still to come, why the estimate is what it is, and the measured figures.
// The estimate is for the whole of the work the person asked for, not for the
// step that runs (chatstatus.go statusETA). Nothing is read out loud by
// itself: the modal's Read aloud button reads the spoken version.

import { el } from './api.js';
import { DS, html, raw, icon, morph } from './ui.js';
import { agentMarkHtml } from './logos.js';

// The words of the chip, by committed state, and the DS status it wears.
const STATE = {
  busy: { label: 'Working', kind: 'running' },
  tasks: { label: 'Background work', kind: 'running' },
  waiting: { label: 'Waiting for you', kind: 'warning', icon: 'chat' },
  idle: { label: 'Done', kind: 'ok', icon: 'checkCircle' },
};

// spanWords is a duration as a person reads it: "40 s", "12 min",
// "3 h 20 min", "2 d 4 h".
export function spanWords(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return s + ' s';
  const m = Math.floor(s / 60);
  if (m < 60) return m + ' min';
  const h = Math.floor(m / 60);
  if (h < 24) return h + ' h' + (m % 60 ? ' ' + (m % 60) + ' min' : '');
  return Math.floor(h / 24) + ' d' + (h % 24 ? ' ' + (h % 24) + ' h' : '');
}

// spanShort is the same span as a figure: "40 s", "12 min", "1:23 h",
// "2 d 4 h", "13 d". "1 h 23 min" takes two lines at a figure's size.
export function spanShort(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(s / 60);
  if (m < 60) return spanWords(ms);
  const h = Math.floor(m / 60);
  if (h < 24) return h + (m % 60 ? ':' + String(m % 60).padStart(2, '0') : '') + ' h';
  const d = Math.floor(h / 24);
  return d >= 10 || !(h % 24) ? d + ' d' : d + ' d ' + (h % 24) + ' h';
}

// minutesWords is an estimate in minutes as a person says it: "1 min",
// "25 min", "1 h 20 min".
const minutesWords = (minutes) => spanWords(Math.max(1, Math.round(minutes)) * 60000);

// A task of the agent's list as a DS task (.ds-tasks): its mark's shape and
// its text's weight say where it stands, and a word does for a reader.
const TASK = {
  completed: { status: 'done', word: 'done', mark: () => raw(icon('checkCircle', 20)) },
  in_progress: { status: 'running', word: 'running', mark: () => html`<span class="ds-spinner ds-spinner--sm"></span>` },
};

function taskHtml(t) {
  const kind = TASK[t.status] || { status: 'pending', word: 'waiting', mark: () => '' };
  return html`<li class="ds-tasks__item" data-status="${kind.status}"><span class="ds-tasks__mark" aria-hidden="true">${kind.mark()}</span><span class="ds-tasks__text">${t.subject}<span class="ds-sr">${': ' + kind.word}</span></span></li>`;
}

const clockTime = (at) => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

// sectionHtml is one titled part of the modal.
const sectionHtml = (name, glyph, label, body, aside) => html`<section class="${'so-status__section so-status__' + name}" aria-label="${label}"><div class="so-status__section-head"><p class="so-status__label">${raw(icon(glyph, 16))}<span>${label}</span></p>${aside || ''}</div>${body}</section>`;

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
  // Only the body scrolls: the head and Read aloud stay in view on a phone.
  // Nothing in it takes the focus, so it does, for the keyboard to scroll it.
  const body = el('div', { class: 'ds-dialog__body so-status__body', 'aria-live': 'polite', tabindex: '0', role: 'region', 'aria-label': 'Status' });
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

  // hero is the answer to "when will it be done": a label, the figure and
  // the line under it, by what the chat is doing.
  function hero(a, c) {
    const eta = typeof a.eta_minutes === 'number' ? a.eta_minutes : null;
    if (data.state === 'waiting') {
      return {
        label: 'Waiting for your answer',
        value: c.asked_at ? spanShort(Date.now() - skew - c.asked_at) : 'Your turn',
        live: c.asked_at ? 'asked' : '',
        note: (c.asked_at ? 'Asked at ' + clockTime(c.asked_at) : 'It asked you something')
          + (eta !== null ? ' · then about ' + minutesWords(eta) + ' more' : ''),
      };
    }
    if (eta !== null) {
      const done = Number(c.now || Date.now()) + eta * 60000;
      const range = typeof a.eta_low === 'number' && typeof a.eta_high === 'number'
        ? 'likely ' + minutesWords(a.eta_low) + ' to ' + minutesWords(a.eta_high) : '';
      return {
        label: 'Everything done in about',
        value: minutesWords(eta),
        note: ['around ' + clockTime(done), range].filter(Boolean).join(' · '),
      };
    }
    if (data.state === 'idle') {
      return {
        label: 'Everything done at',
        value: c.last_ended ? clockTime(c.last_ended) : 'Done',
        note: 'after ' + spanWords(Number(c.work_ms) || 0) + ' of work on the request',
      };
    }
    return { label: 'Everything done in', value: '—', note: 'No estimate this time' };
  }

  function draw() {
    const a = data.analysis || {};
    const c = data.clock || {};
    const state = STATE[data.state] || { label: 'Unknown', kind: '' };
    const chip = html`<span class="${'ds-status' + (state.kind ? ' ds-status--' + state.kind : '')}">${
      state.kind === 'running' ? html`<span class="ds-spinner ds-status__spinner" aria-hidden="true"></span>` : state.icon ? raw(icon(state.icon, 16)) : ''
    }<span>${state.label}</span></span>`;
    const h = hero(a, c);

    // How far along it is: the model's share of the whole work.
    // Once it is done there is nothing to measure it against.
    const share = typeof a.progress === 'number' && data.state !== 'idle' ? Math.min(100, Math.max(0, a.progress)) : null;
    const progress = share === null ? '' : html`<div class="ds-progress so-status__progress" style="${'--ds-progress: ' + share / 100}"><div class="ds-progress__track" role="progressbar" aria-label="Work done" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${share}"><div class="ds-progress__bar"></div></div><p class="ds-progress__label">${share + '% done'}</p>${a.goal ? html`<p class="ds-progress__detail so-status__goal">${a.goal}</p>` : ''}</div>`;

    // What runs at this moment.
    const call = c.call && c.call_started
      ? html`<p class="so-status__call"><code class="so-break">${c.call}</code><span data-live="call">${' · ' + since(c.call_started)}</span></p>` : '';
    const now = a.now ? sectionHtml('now', 'activity', 'Right now', html`<p class="so-status__text">${a.now}</p>${call}`) : '';

    // What is still to come: the model's steps, the first of them running
    // while the agent works.
    const steps = Array.isArray(a.remaining) ? a.remaining : [];
    const working = data.state === 'busy' || data.state === 'tasks';
    const next = steps.length ? sectionHtml('next', 'list', 'Still to do',
      html`<ol class="ds-tasks so-status__list" aria-label="Still to do">${steps.map((s, i) => taskHtml({ subject: s, status: i === 0 && working ? 'in_progress' : 'pending' }))}</ol>`,
      html`<p class="ds-meta">${steps.length === 1 ? '1 step' : steps.length + ' steps'}</p>`) : '';

    // The agent's own task list, as it keeps it.
    const tasks = Array.isArray(data.tasks) ? data.tasks : [];
    const done = tasks.filter((t) => t.status === 'completed').length;
    const list = tasks.length ? sectionHtml('tasks', 'checkCircle', 'The agent’s task list',
      html`<ol class="ds-tasks so-status__list" aria-label="The agent's task list">${tasks.map((t) => taskHtml(t))}</ol>`,
      html`<p class="ds-meta">${done + ' of ' + tasks.length + ' done'}</p>`) : '';

    // Why the estimate is what it is: the model's reasons, and what is
    // measured beside them.
    const parts = [];
    if (c.work_calls) parts.push(c.work_calls + (c.work_calls === 1 ? ' call' : ' calls'));
    if (c.background) parts.push(c.background + ' in the background');
    if (c.queued) parts.push(c.queued + (c.queued === 1 ? ' message' : ' messages') + ' not read yet');
    const why = a.eta_reason || parts.length ? sectionHtml('why', 'info', typeof a.eta_minutes === 'number' ? 'About the estimate' : 'Where it stands',
      html`${a.eta_reason ? html`<p class="so-status__text">${a.eta_reason}</p>` : ''}${parts.length ? html`<p class="ds-meta so-status__measured">${parts.join(' · ')}</p>` : ''}`) : '';

    // The measured figures of the request and the session.
    const turns = Number(c.turns) || 0;
    const facts = html`<dl class="ds-facts so-status__facts">${c.work_started ? html`<div><dt>This request</dt><dd>${spanWords(Number(c.work_ms) || 0) + ' of work since ' + clockTime(c.work_started)}</dd></div>` : ''}<div><dt>Session</dt><dd>${(c.first_message ? since(c.first_message) + ' old · ' : '') + turns + (turns === 1 ? ' turn' : ' turns') + ' · ' + spanWords(Number(c.worked_ms) || 0) + ' of work'}</dd></div></dl>`;

    morph(body, html`<section class="so-status__hero" aria-label="When it will be done"><div class="so-status__hero-head">${chip}${data.model ? html`<span class="so-status__by">${'Read by ' + data.model.replace(/^[^/]*\//, '')}</span>` : ''}</div><p class="so-status__hero-label">${h.label}</p><p class="so-status__hero-value" data-live="${h.live || ''}">${h.value}</p>${h.note ? html`<p class="so-status__hero-note">${h.note}</p>` : ''}${progress}</section>
${now}${next}${list}${why}${facts}`);
  }

  // tick moves the live figures on, without drawing anything else again.
  function tick() {
    if (!data || !open) return;
    const c = data.clock || {};
    const asked = body.querySelector('[data-live="asked"]');
    if (asked && c.asked_at) asked.textContent = spanShort(Date.now() - skew - c.asked_at);
    const call = body.querySelector('[data-live="call"]');
    if (call && c.call_started) call.textContent = ' · ' + since(c.call_started);
  }

  function paint() {
    if (!open) return;
    const on = speaking();
    speakBtn.hidden = !data;
    speakBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
    morph(speakBtn, html`${raw(icon(on ? 'stop' : 'speak', 18))}<span>${on ? 'Stop reading' : 'Read aloud'}</span>`);
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
