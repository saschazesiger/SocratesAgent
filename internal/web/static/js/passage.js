// Ask about a passage: select a few words of a chat - a term you do not know,
// a line whose reason you missed - and the bubble in the top bar asks about
// them. The answer comes from the model Status uses, which is given the
// chat's whole record, the passage and the question (chatask.go). It is a
// side conversation of the person's own: nothing of it reaches the chat's
// agent, follow-ups stay in the dialog, and closing it forgets them.

import { api, el, errorMessage, isOffline } from './api.js';
import { DS, html, raw, icon, morph, ms } from './ui.js';
import { agentMarkHtml } from './logos.js';
import { markdown } from './md.js';

// The ready-made questions, for a phone where typing is the slow part. They
// are answered in the language Socrates speaks (chatask.go askRequest).
const PRESETS = [
  'What does this mean?',
  'Why did the agent do this?',
  'Explain it more simply.',
  'What led up to this?',
];

// The longest passage the page sends; the server keeps the ends of a longer
// one, so this only spares the request a whole chat selected by accident.
const QUOTE_MAX = 20000;

const path = (id) => '/api/sessions/' + encodeURIComponent(id) + '/ask';

/**
 * selectedPassage is the text selected in a chat's conversation, or '' when
 * nothing is or the selection reaches outside it.
 */
export function selectedPassage() {
  const sel = document.getSelection();
  if (!sel || sel.isCollapsed || !sel.rangeCount) return '';
  const inLog = (node) => {
    const elem = node && (node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement);
    return !!(elem && elem.closest('#chat .so-chat__log'));
  };
  if (!inLog(sel.anchorNode) || !inLog(sel.focusNode)) return '';
  return sel.toString().trim().slice(0, QUOTE_MAX);
}

/**
 * mountPassage wires the top bar's bubble.
 *
 * `ctx`: button, current() the session on screen, live() whether the
 * connection is up.
 * It answers { paint() }.
 */
export function mountPassage({ button, current, live }) {
  if (!button) return { paint() {} };
  // The passage the button would ask about. It is held a moment after the
  // selection goes: a tap on the button clears a phone's selection before the
  // click arrives, and the button must still be there for it.
  let held = '';
  let dropTimer = null;
  let dialog = null;

  function usable() {
    const session = current();
    return !!session && session.kind === 'chat';
  }

  function paint() {
    const show = usable() && !!held;
    if (button.hidden === show) button.hidden = !show;
    button.disabled = show && !live();
  }

  function track() {
    const text = usable() ? selectedPassage() : '';
    if (text) {
      clearTimeout(dropTimer);
      dropTimer = null;
      held = text;
      paint();
      return;
    }
    if (!held || dropTimer) return;
    dropTimer = setTimeout(() => {
      dropTimer = null;
      held = '';
      paint();
    }, ms('--ds-hold-status'));
  }

  document.addEventListener('selectionchange', track);
  // A press on the button keeps the selection where it is (a pointer that
  // presses a button would otherwise move the focus and clear it).
  button.addEventListener('pointerdown', (e) => {
    clearTimeout(dropTimer);
    dropTimer = null;
    if (e.pointerType === 'mouse') e.preventDefault();
  });
  button.addEventListener('click', () => {
    const session = current();
    const quote = selectedPassage() || held;
    if (!session || !quote || (dialog && dialog.isOpen())) return;
    dialog = openPassageDialog({ session, quote, opener: button });
  });
  paint();
  return {
    paint() {
      if (!usable()) held = '';
      paint();
    },
  };
}

/**
 * openPassageDialog opens the question about `quote` in a chat at once, ready
 * for the question. It answers { isOpen(), dialog }.
 */
export function openPassageDialog({ session, quote, opener }) {
  const titleId = 'passageTitle' + Date.now();
  const fieldId = 'passageQ' + Date.now();
  // Every question asked so far and its answer, or its error while it has
  // none; the newest is the one being asked.
  const thread = [];
  let busy = false;
  let open = true;

  const dialog = el('dialog', { class: 'ds-dialog so-status so-passage', 'aria-labelledby': titleId },
    el('div', { class: 'ds-dialog__backdrop', 'data-close': true }),
    el('div', { class: 'ds-dialog__box so-status__box' }));
  const box = dialog.querySelector('.so-status__box');
  morph(box, html`<header class="so-status__head"><span class="so-status__mark" aria-hidden="true">${raw(agentMarkHtml(session.harness, 24))}</span><div class="so-status__titles"><p class="so-status__overline">Ask about this passage</p><h2 class="ds-dialog__title so-status__title" id="${titleId}">${session.title || 'Chat'}</h2></div><button class="ds-btn ds-btn--quiet ds-btn--icon so-status__x" type="button" data-close aria-label="Close" data-tip="Close">${raw(icon('close', 20))}</button></header>`);
  const body = el('div', { class: 'ds-dialog__body so-status__body so-passage__body', tabindex: '0', role: 'region', 'aria-label': 'Questions and answers' });
  const form = el('form', { class: 'so-passage__form' });
  box.append(body, form);
  morph(form, html`<div class="ds-field ds-field--full"><label class="ds-field__label" for="${fieldId}">Your question</label><div class="ds-field__row ds-field__row--top"><textarea class="ds-input so-passage__input" id="${fieldId}" rows="2" enterkeyhint="send" placeholder="What does this mean?" autofocus></textarea><button class="ds-btn ds-btn--primary ds-btn--icon so-passage__send" type="submit" aria-label="Ask" data-tip="Ask (Enter)">${raw(icon('send', 20))}</button></div></div>`);
  const input = form.querySelector('textarea');
  const send = form.querySelector('.so-passage__send');

  function draw() {
    if (!open) return;
    const presets = !thread.length
      ? html`<div class="so-passage__presets" role="group" aria-label="Quick questions">${PRESETS.map((q) => html`<button class="ds-btn ds-btn--tonal ds-btn--sm" type="button" data-preset="${q}">${q}</button>`)}</div>` : '';
    const turns = thread.map((t, i) => html`<section class="so-passage__turn" data-ds-key="${'t' + i}" aria-label="${'Question ' + (i + 1)}"><p class="so-passage__q">${raw(icon('chat', 16))}<span>${t.question}</span></p>${
      t.answer ? html`<div class="so-md so-passage__a">${markdown(t.answer)}</div>`
        : t.error ? html`<div class="ds-alert ds-alert--error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${t.error}</span></div>`
          : html`<div class="ds-loading so-passage__loading" role="status"><span class="ds-loader ds-loader--sm" aria-hidden="true"></span><span>Reading the chat…</span></div>`
    }</section>`);
    morph(body, html`<figure class="so-passage__quote" data-ds-key="quote"><figcaption class="ds-meta">You selected</figcaption><blockquote class="so-passage__text">${quote}</blockquote></figure>${presets}${turns}`);
    if (busy) send.setAttribute('aria-busy', 'true'); else send.removeAttribute('aria-busy');
    input.placeholder = thread.length ? 'Ask a follow-up…' : 'What does this mean?';
  }

  async function ask(question, preset) {
    question = String(question || '').trim();
    if (!question || busy || !open) return;
    const earlier = thread.filter((t) => t.answer).map((t) => ({ question: t.question, answer: t.answer }));
    const turn = { question, answer: '', error: '' };
    // A question that failed is not carried into the next one.
    for (let i = thread.length - 1; i >= 0; i -= 1) if (thread[i].error) thread.splice(i, 1);
    thread.push(turn);
    busy = true;
    input.value = '';
    draw();
    scrollToEnd();
    try {
      const data = await api(path(session.id), {
        method: 'POST', attempts: 1, timeout: 150000,
        body: { quote, question, thread: earlier, preset: !!preset },
      });
      turn.answer = String((data && data.answer) || '').trim() || 'There was no answer.';
    } catch (err) {
      turn.error = isOffline(err) || (err && err.status === 400) ? errorMessage(err) : 'That question could not be answered.';
      // The question stays to be sent again.
      if (!input.value) input.value = question;
    } finally {
      busy = false;
      draw();
      scrollToEnd();
      if (open) input.focus({ preventScroll: true });
    }
  }

  function scrollToEnd() {
    const last = body.lastElementChild;
    if (last && thread.length) last.scrollIntoView({ block: 'nearest' });
  }

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    ask(input.value, false);
  });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      ask(input.value, false);
    }
  });
  body.addEventListener('click', (e) => {
    const b = e.target.closest('[data-preset]');
    if (b) ask(b.dataset.preset, true);
  });

  draw();
  document.body.append(dialog);
  DS.overlay.open(dialog, { opener: opener || document.activeElement });
  dialog.addEventListener('close', async () => {
    open = false;
    await DS.overlay.close(dialog);
    dialog.remove();
  }, { once: true });
  return { isOpen: () => open, dialog };
}
