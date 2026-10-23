// The question dialog of a chat: an agent's ask_user call (REBUILD.md §5),
// one question per step, as the biggest buttons the screen allows.
//
// It is the question dialog of the terminal days (question.js) for a
// question that arrives structured: no screen to read, no operator to type
// the answer - the answer goes to the chat as JSON, through the chats'
// Outbox, once by its client_id. The shape it answers with is §5.2:
//
//   { answers: { <question id>: { selected, other, note, delegated, skipped, value? } },
//     general_note, follow_up }
//
// A question can be answered, delegated ("You decide"), answered in one's
// own words ("Other"), given a note, or skipped; instead of answering, a
// question can be asked back (follow_up). A plan is approved or sent back
// with a note ({decision}). What is being answered is kept in this browser
// while it is written, so the card in the chat can say how far it is and a
// closed dialog opens where it was left.
//
// It is live: a question answered on another device says so, and the next
// one takes the dialog.

import { html, raw, icon, morph, showOverlay, hideOverlay, M, faces } from './ui.js';
import { markdown } from './md.js';
import { agentMarkHtml } from './logos.js';
import { askQuestions } from './chatparts.js';
import { queueAnswer, answeredHere } from './chatoutbox.js';
import { record, recPhase } from './dictate.js';
import { speak, stopSpeaking } from './voice.js';
import { restoreFocus } from './api.js';

// How long "Answered" stays before the next question takes the dialog.
const MOVE_ON = 1200;

/* ------------------------------------------------------------ the drafts */

const draftKey = (askId) => 'socrates.ask.' + askId;

function readDraft(askId) {
  try {
    const parsed = JSON.parse(localStorage.getItem(draftKey(askId)) || 'null');
    if (parsed && typeof parsed === 'object') return parsed;
  } catch { /* nothing kept */ }
  return null;
}

function writeDraft(askId, draft) {
  try { localStorage.setItem(draftKey(askId), JSON.stringify(draft)); } catch { /* full or private */ }
}

function forgetDraft(askId) {
  try { localStorage.removeItem(draftKey(askId)); } catch { /* private */ }
}

function emptyAnswer() {
  return { selected: [], other: '', note: '', delegated: false, value: '', noting: false, othering: false };
}

/** answered says whether one question has an answer of any kind. */
function answered(a) {
  return !!a && (a.delegated || a.selected.length > 0 || !!String(a.other || '').trim() || String(a.value ?? '') !== '');
}

/**
 * askProgress is how far this browser is with an ask: the questions it has
 * an answer for, for the card in the chat ("2 of 5 answered").
 */
export function askProgress(ask) {
  const draft = readDraft(ask.id);
  const set = new Set();
  if (draft && draft.answers) {
    for (const [id, a] of Object.entries(draft.answers)) if (answered({ ...emptyAnswer(), ...a })) set.add(id);
  }
  return { answered: set, count: set.size };
}

function startDraft(ask) {
  const draft = readDraft(ask.id) || { step: 0, answers: {}, general: '', followUp: '', asking: false };
  for (const q of askQuestions(ask)) {
    const a = { ...emptyAnswer(), ...(draft.answers[q.id] || {}) };
    // The question's own default, preselected the first time it is shown.
    if (!draft.answers[q.id] && q.default !== undefined && q.default !== null) {
      if (q.kind === 'confirm') a.selected = [q.default === true || q.default === 'yes' ? 'yes' : 'no'];
      else if (q.kind === 'number') a.value = String(q.default);
      else if (q.kind === 'text') a.other = String(q.default);
      else a.selected = Array.isArray(q.default) ? q.default.map(String) : [String(q.default)];
    }
    draft.answers[q.id] = a;
  }
  return draft;
}

/** answerOf is what the agent gets back for an ask, from the draft. */
function answerOf(ask, draft) {
  const out = {};
  for (const q of askQuestions(ask)) {
    const a = { ...emptyAnswer(), ...(draft.answers[q.id] || {}) };
    const entry = {
      selected: a.delegated ? [] : a.selected.slice(),
      other: !a.delegated && String(a.other || '').trim() ? String(a.other).trim() : null,
      note: String(a.note || '').trim() || null,
      delegated: !!a.delegated,
      skipped: !answered(a),
    };
    if (q.kind === 'number' && !a.delegated && String(a.value ?? '') !== '') {
      entry.value = Number(a.value);
      entry.other = String(a.value);
    }
    out[q.id] = entry;
  }
  return {
    answers: out,
    general_note: String(draft.general || '').trim() || null,
    follow_up: draft.asking && String(draft.followUp || '').trim() ? String(draft.followUp).trim() : null,
  };
}

/* ------------------------------------------------------------ the dialog */

let current = null;

/** askDialogOpen says whether the dialog is on screen. */
export function askDialogOpen() { return !!current; }

/**
 * openAskDialog walks open questions, oldest first.
 *
 *   items       [{ chatId, session, ask }]
 *   start       the index to open at
 *   watch(item, onAsk)  follows one ask live; onAsk(ask) with its newest
 *               state; answers a function that stops following
 *   openChat(id)        the "Open chat" of a question asked from Home
 */
export function openAskDialog({ items, start = 0, watch, openChat }) {
  if (current) { current.jump(items, start); return; }
  const opener = document.activeElement;
  const dialog = document.createElement('dialog');
  dialog.className = 'ds-dialog so-question so-askdlg';
  dialog.setAttribute('aria-label', 'Questions');
  const backdrop = document.createElement('div');
  backdrop.className = 'ds-dialog__backdrop';
  const shell = document.createElement('div');
  shell.className = 'ds-dialog__box so-q-shell';
  shell.tabIndex = -1;
  dialog.append(backdrop, shell);

  const st = { items: items.slice(), at: Math.max(0, Math.min(start, items.length - 1)), draft: null, phase: 'ask',
    details: new Set(), speaking: false, unwatch: null, timer: null, closed: false };

  function item() { return st.items[st.at] || null; }

  function load() {
    if (st.unwatch) { st.unwatch(); st.unwatch = null; }
    const one = item();
    if (!one) return;
    st.draft = startDraft(one.ask);
    st.phase = one.ask.answered ? (answeredHere(one.ask.clientId) ? 'answered' : 'elsewhere') : 'ask';
    st.unwatch = watch ? watch(one, (ask) => {
      if (st.closed || item() !== one) return;
      one.ask = ask;
      if (ask.answered && (st.phase === 'ask' || st.phase === 'sent')) {
        st.phase = answeredHere(ask.clientId) ? 'answered' : 'elsewhere';
        forgetDraft(ask.id);
        draw();
        if (st.phase === 'answered') moveOnSoon();
      }
    }) : null;
  }

  function save() {
    const one = item();
    if (one && st.phase === 'ask') writeDraft(one.ask.id, st.draft);
  }

  function moveOnSoon() {
    clearTimeout(st.timer);
    st.timer = setTimeout(() => next(), MOVE_ON);
  }

  function next() {
    clearTimeout(st.timer);
    if (st.at < st.items.length - 1) {
      st.at += 1;
      load();
      draw(true);
    } else close();
  }

  function close() {
    if (st.closed) return;
    st.closed = true;
    clearTimeout(st.timer);
    if (st.unwatch) st.unwatch();
    if (st.speaking) stopSpeaking();
    save();
    current = null;
    hideOverlay(dialog, { remove: true }).then(() => restoreFocus(opener));
  }

  /* ---------------------------------------------------------- drawing */

  function questionsOf() { return askQuestions(item().ask); }

  function draw(focus = false) {
    const one = item();
    if (!one) { close(); return; }
    const ask = one.ask;
    const payload = ask.payload || {};
    const plan = payload.kind === 'plan';
    const questions = questionsOf();
    const step = Math.max(0, Math.min(st.draft.step || 0, Math.max(0, questions.length - 1)));
    st.draft.step = step;
    const q = questions[step];
    const last = step >= questions.length - 1;
    const session = one.session || {};
    const counter = st.items.length > 1 ? (st.at + 1) + ' of ' + st.items.length : '';
    morph(shell, html`<div class="so-q-top" data-ds-key="top"><button class="ds-btn ds-btn--quiet ds-btn--icon so-q-close" type="button" data-action="close" aria-label="Close" data-tip="Close">${raw(icon('close', 20))}</button><span class="so-q-top-title">${counter ? 'Question ' + counter : 'Question'}</span></div>
<div class="so-q-scroll" data-ds-key="${'scroll:' + ask.id}"><div class="so-q-session"><span class="so-mark">${raw(agentMarkHtml(session.harness || '', 24))}</span><span class="so-q-session-text"><span class="so-q-session-title so-ellipsis">${session.title || 'Chat'}</span></span>${
      openChat ? html`<button class="ds-btn so-q-open" type="button" data-action="open-chat">Open chat</button>` : ''
    }</div>${st.phase !== 'ask' && st.phase !== 'sent' ? doneHtml(ask) : plan ? planHtml(payload) : html`<div class="so-q-body so-ask-body" data-ds-key="${'q:' + ask.id + ':' + step}">${
      step === 0 && (payload.title || payload.context) ? html`${payload.title ? html`<p class="so-ask-title">${payload.title}</p>` : ''}${payload.context ? contextHtml('ctx', payload.context, 'About these questions') : ''}` : ''
    }${q ? questionHtml(q, questions.length, step) : ''}${
      st.draft.asking ? followUpHtml() : ''
    }${last && !st.draft.asking ? generalHtml() : ''}</div>`}${
      st.phase === 'sent' ? html`<div class="ds-alert so-q-status" role="status">${raw(icon('clock', 20))}<span class="ds-alert__text">${navigator.onLine === false ? 'Saved. Your answer goes out when the connection is back.' : 'Sending your answer…'}</span></div>` : ''
    }</div>
<div class="so-q-actions" data-ds-key="actions">${actionsHtml({ plan, step, last, q })}</div>`);
    if (focus) {
      const target = shell.querySelector('.so-q-text') || shell;
      target.focus({ preventScroll: true });
    }
  }

  function contextHtml(key, text, label) {
    const open = st.details.has(key);
    return html`<details class="ds-collapsible so-ask-context" data-open-key="${key}"${open ? html` open` : ''}><summary class="so-ask-context__head">${raw(icon('info', 16))}<span>${label}</span>${raw(icon('chevronDown', 20, 'ds-chevron'))}</summary><div class="so-ask-context__body so-md">${markdown(text)}</div></details>`;
  }

  function questionHtml(q, total, step) {
    const a = st.draft.answers[q.id];
    const kind = q.kind || ((q.options || []).length ? 'single' : 'text');
    const options = kind === 'confirm'
      ? [{ id: 'yes', label: 'Yes' }, { id: 'no', label: 'No' }]
      : (q.options || []).slice(0, 12);
    const multi = kind === 'multi';
    const delegate = q.allow_delegate !== false;
    const other = q.allow_other !== false && (kind === 'single' || kind === 'multi');
    const hintWords = multi ? multiHint(q) : '';
    return html`<div class="so-q-step">${total > 1 ? html`<span>${'Question ' + (step + 1) + ' of ' + total}</span>` : ''}${q.header ? html`<span class="so-q-chip">${q.header}</span>` : ''}</div>
<h2 class="so-q-text" tabindex="-1">${q.question || q.header || ''}</h2>${q.context ? contextHtml('qctx:' + q.id, q.context, 'Why this matters') : ''}${
      hintWords ? html`<p class="so-q-detail">${hintWords}</p>` : ''
    }${options.length ? html`<div class="so-q-options" role="group" aria-label="${q.question || 'Options'}">${options.map((option, i) => optionHtml(q, option, i, multi, a))}</div>` : ''}${
      kind === 'text' ? fieldHtml(q.id, 'other', 'Your answer', a.other, 3) : ''
    }${kind === 'number' ? numberHtml(q, a) : ''}${
      other ? (a.othering || a.other ? fieldHtml(q.id, 'other', 'In your own words', a.other, 2) : '') : ''
    }${a.noting || a.note ? fieldHtml(q.id, 'note', 'A note to the agent', a.note, 2) : ''}<div class="so-ask-extras">${
      delegate ? html`<button class="ds-btn ds-btn--quiet so-ask-delegate" type="button" data-action="delegate" data-q="${q.id}" aria-pressed="${a.delegated ? 'true' : 'false'}">${raw(icon('sparkles', 16))}You decide</button>` : ''
    }${other && !(a.othering || a.other) ? html`<button class="ds-btn ds-btn--quiet" type="button" data-action="other" data-q="${q.id}">${raw(icon('pencil', 16))}Other</button>` : ''}${
      !(a.noting || a.note) ? html`<button class="ds-btn ds-btn--quiet" type="button" data-action="note" data-q="${q.id}">${raw(icon('compose', 16))}Add a note</button>` : ''
    }</div>`;
  }

  function multiHint(q) {
    const min = Number(q.min) || 0;
    const max = Number(q.max) || 0;
    if (min && max) return 'Choose ' + (min === max ? min : min + ' to ' + max) + '.';
    if (max) return 'Choose up to ' + max + '.';
    if (min) return 'Choose at least ' + min + '.';
    return 'Choose any.';
  }

  function optionHtml(q, option, i, multi, a) {
    const id = String(option.id ?? option.label);
    const chosen = !a.delegated && a.selected.includes(id);
    const detailKey = 'opt:' + q.id + ':' + id;
    const open = st.details.has(detailKey);
    return html`<div class="so-ask-option" data-ds-key="${'o:' + id}"><button class="ds-card ds-card--interactive so-q-option" type="button" data-action="choose" data-q="${q.id}" data-option="${id}" aria-pressed="${chosen ? 'true' : 'false'}"${multi ? html` data-multi` : ''}><span class="so-q-option-row"><span class="so-q-option-ring" aria-hidden="true"></span><span class="so-q-option-key" aria-hidden="true">${chosen ? raw(icon('check', 20)) : String(i + 1)}</span><span class="so-q-option-text"><span class="so-q-option-label">${option.label || id}</span>${
      option.description ? html`<span class="so-q-option-desc">${option.description}</span>` : ''
    }${option.recommended ? html`<span class="ds-status ds-status--ok so-q-recommended">${raw(icon('thumbUp', 16))}<span>Recommended</span></span>` : ''}</span></span></button>${
      option.details ? html`<button class="ds-btn ds-btn--text ds-btn--sm so-ask-more" type="button" data-action="details" data-key="${detailKey}" aria-expanded="${open ? 'true' : 'false'}">${open ? 'Fewer details' : 'Details'}</button>${open ? html`<div class="so-ask-details so-md">${markdown(option.details)}</div>` : ''}` : ''
    }</div>`;
  }

  function fieldHtml(qid, field, label, value, rows) {
    const fid = 'ask-' + qid + '-' + field;
    return html`<div class="ds-field so-q-own" data-ds-key="${fid}"><label class="ds-field__label" for="${fid}">${label}</label><div class="so-field-row so-field-row--top"><textarea class="ds-input so-q-own-field" id="${fid}" rows="${rows}" data-q="${qid}" data-field="${field}">${value || ''}</textarea><button class="ds-btn so-mic" type="button" data-action="dictate" data-target="${fid}">${faces('Dictate', 'Listening')}</button></div></div>`;
  }

  function numberHtml(q, a) {
    const fid = 'ask-' + q.id + '-value';
    return html`<div class="ds-field so-q-own" data-ds-key="${fid}"><label class="ds-field__label" for="${fid}">A number${q.min !== undefined || q.max !== undefined ? html` <span class="ds-meta">${'(' + [q.min !== undefined ? 'at least ' + q.min : '', q.max !== undefined ? 'at most ' + q.max : ''].filter(Boolean).join(', ') + ')'}</span>` : ''}</label><input class="ds-input so-ask-number" id="${fid}" type="number" inputmode="decimal" data-q="${q.id}" data-field="value" value="${a.value ?? ''}"${q.min !== undefined ? html` min="${q.min}"` : ''}${q.max !== undefined ? html` max="${q.max}"` : ''}></div>`;
  }

  function generalHtml() {
    return st.draft.general || st.details.has('general')
      ? fieldHtml('all', 'general', 'Anything else the agent should know', st.draft.general, 2)
      : html`<button class="ds-btn ds-btn--quiet so-ask-general" type="button" data-action="general">${raw(icon('compose', 16))}Anything else?</button>`;
  }

  function followUpHtml() {
    return html`<div class="so-ask-followup">${fieldHtml('all', 'followUp', 'Your question to the agent', st.draft.followUp, 3)}<p class="so-q-detail">The agent answers it before it goes on, and asks again.</p></div>`;
  }

  function planHtml(payload) {
    const noting = st.details.has('plan-note');
    return html`<div class="so-q-body so-ask-body" data-ds-key="plan"><p class="so-ask-title">${payload.title || 'Approve the plan'}</p><div class="so-ask-plan so-md">${markdown(payload.plan || '')}</div>${
      noting ? fieldHtml('plan', 'planNote', 'What should change?', st.draft.planNote, 3) : ''
    }</div>`;
  }

  function doneHtml(ask) {
    const elsewhere = st.phase === 'elsewhere';
    return html`<div class="ds-empty so-q-finished" role="status" data-ds-key="done"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('check', 24))}</span><p class="ds-empty__title">${elsewhere ? 'Answered on another device' : 'Answered'}</p>${
      elsewhere && ask.byDevice ? html`<p class="ds-empty__text">${'On ' + ask.byDevice + '.'}</p>` : ''
    }</div>`;
  }

  function actionsHtml({ plan, step, last }) {
    const read = html`<button class="ds-btn so-q-big so-q-read" type="button" data-action="read" aria-pressed="${st.speaking ? 'true' : 'false'}">${st.speaking ? 'Stop reading' : 'Read aloud'}</button>`;
    if (st.phase === 'answered' || st.phase === 'elsewhere') {
      return html`<button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="next">${st.at < st.items.length - 1 ? 'Next question' : 'Close'}</button>`;
    }
    if (st.phase === 'sent') return html`${read}`;
    if (plan) {
      const noting = st.details.has('plan-note');
      return html`${read}<button class="ds-btn so-q-big" type="button" data-action="${noting ? 'plan-send' : 'plan-note'}">${noting ? 'Send back' : 'Keep planning'}</button><button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="plan-approve">Approve</button>`;
    }
    if (st.draft.asking) {
      return html`${read}<button class="ds-btn so-q-big so-q-back" type="button" data-action="ask-cancel">Back to the questions</button><button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="send">Send question</button>`;
    }
    return html`${read}<button class="ds-btn ds-btn--text so-q-big so-ask-back" type="button" data-action="ask-back">Ask back</button>${
      step > 0 ? html`<button class="ds-btn so-q-big so-q-back" type="button" data-action="back">Back</button>` : ''
    }<button class="ds-btn ds-btn--primary so-q-big so-q-send" type="button" data-action="${last ? 'send' : 'forward'}">${last ? 'Send answers' : 'Next'}</button>`;
  }

  /* ---------------------------------------------------------- reading */

  function sayable() {
    const one = item();
    const payload = one.ask.payload || {};
    if (payload.kind === 'plan') return [payload.title, payload.plan].filter(Boolean).join('\n');
    const q = questionsOf()[st.draft.step || 0];
    if (!q) return payload.title || '';
    const parts = [q.question || q.header || ''];
    if (q.context) parts.push(q.context);
    (q.options || []).forEach((option, i) => {
      parts.push((i + 1) + ': ' + (option.label || option.id) + (option.description ? '. ' + option.description : '') + '.');
    });
    return parts.join('\n');
  }

  async function toggleRead() {
    if (st.speaking) { st.speaking = false; stopSpeaking(); draw(); return; }
    st.speaking = true;
    draw();
    try { await speak(sayable()); } catch { /* voice.js says why */ }
    st.speaking = false;
    if (!st.closed) draw();
  }

  /* ---------------------------------------------------------- answering */

  function submit(answer) {
    const one = item();
    queueAnswer(one.chatId, one.ask.id, answer);
    forgetDraft(one.ask.id);
    st.phase = 'sent';
    draw();
  }

  function choose(qid, optionId) {
    const q = questionsOf().find((one) => one.id === qid);
    const a = st.draft.answers[qid];
    a.delegated = false;
    if (q.kind === 'multi') {
      const at = a.selected.indexOf(optionId);
      if (at >= 0) a.selected.splice(at, 1);
      else if (!(Number(q.max) && a.selected.length >= Number(q.max))) a.selected.push(optionId);
    } else {
      a.selected = [optionId];
    }
    save();
    draw();
  }

  shell.addEventListener('click', async (event) => {
    const target = event.target.closest('[data-action]');
    if (!target || !shell.contains(target)) return;
    const qid = target.dataset.q;
    switch (target.dataset.action) {
      case 'close': close(); break;
      case 'open-chat': { const one = item(); close(); openChat(one.chatId); break; }
      case 'choose': choose(qid, target.dataset.option); break;
      case 'delegate': {
        const a = st.draft.answers[qid];
        a.delegated = !a.delegated;
        if (a.delegated) a.selected = [];
        save();
        draw();
        break;
      }
      case 'other': st.draft.answers[qid].othering = true; draw(); focusField('ask-' + qid + '-other'); break;
      case 'note': st.draft.answers[qid].noting = true; draw(); focusField('ask-' + qid + '-note'); break;
      case 'general': st.details.add('general'); draw(); focusField('ask-all-general'); break;
      case 'details': {
        const key = target.dataset.key;
        if (st.details.has(key)) st.details.delete(key); else st.details.add(key);
        draw();
        break;
      }
      case 'forward': st.draft.step += 1; save(); draw(true); break;
      case 'back': st.draft.step = Math.max(0, st.draft.step - 1); save(); draw(true); break;
      case 'ask-back': st.draft.asking = true; save(); draw(); focusField('ask-all-followUp'); break;
      case 'ask-cancel': st.draft.asking = false; save(); draw(); break;
      case 'send': submit(answerOf(item().ask, st.draft)); break;
      case 'plan-approve': submit({ decision: 'approve' }); break;
      case 'plan-note': st.details.add('plan-note'); draw(); focusField('ask-plan-planNote'); break;
      case 'plan-send': submit({ decision: 'keep_planning', note: String(st.draft.planNote || '').trim() }); break;
      case 'next': next(); break;
      case 'read': toggleRead(); break;
      case 'dictate': {
        if (recPhase() !== 'idle') return;
        const field = shell.querySelector('#' + CSS.escape(target.dataset.target));
        const text = await M.busy(target, () => record(), { done: false });
        if (text && field && field.isConnected) {
          field.value = field.value ? field.value.replace(/\s*$/, ' ') + text : text;
          field.dispatchEvent(new Event('input', { bubbles: true }));
        }
        break;
      }
      default: break;
    }
  });

  function focusField(fid) {
    const field = shell.querySelector('#' + CSS.escape(fid));
    if (field) field.focus();
  }

  shell.addEventListener('input', (event) => {
    const field = event.target.closest('[data-field]');
    if (!field) return;
    const name = field.dataset.field;
    if (name === 'general' || name === 'followUp' || name === 'planNote') st.draft[name] = field.value;
    else {
      const a = st.draft.answers[field.dataset.q];
      if (!a) return;
      a[name] = field.value;
      if ((name === 'other' || name === 'value') && field.value) a.delegated = false;
    }
    save();
    const button = shell.querySelector('[data-action="delegate"][data-q="' + CSS.escape(field.dataset.q || '') + '"]');
    if (button) button.setAttribute('aria-pressed', st.draft.answers[field.dataset.q]?.delegated ? 'true' : 'false');
  });

  shell.addEventListener('toggle', (event) => {
    const node = event.target;
    if (!node.dataset || !node.dataset.openKey) return;
    if (node.open) st.details.add(node.dataset.openKey); else st.details.delete(node.dataset.openKey);
  }, true);

  dialog.addEventListener('close', () => close());
  dialog.addEventListener('keydown', (event) => {
    if (event.target.closest('textarea, input') || st.phase !== 'ask') return;
    if (!/^[1-9]$/.test(event.key)) return;
    const option = shell.querySelectorAll('.so-q-option')[Number(event.key) - 1];
    if (option) { event.preventDefault(); option.click(); }
  });

  current = {
    jump(list, at) {
      st.items = list.slice();
      st.at = Math.max(0, Math.min(at, list.length - 1));
      load();
      draw(true);
    },
  };
  load();
  draw();
  showOverlay(dialog).then(() => {
    const target = shell.querySelector('.so-q-text') || shell;
    target.focus({ preventScroll: true });
  });
  return current;
}
