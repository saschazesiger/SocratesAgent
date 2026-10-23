// The outcome of a turn: how the read Home makes of every turn's end judged
// the work it left - complete, partly done, waiting on the person, blocked -
// when the dashboard has it switched on (internal/server/outcome.go).
//
// The sidebar, the header, Home and the notifications all say it, so the
// words and the marks are here, once.

import { icon, iconEl } from './ui.js';

// The words for each kind, as a row's tooltip, the header's tag and a
// notification say them. "Waiting on you" is not "Needs you": that is Home's
// list, which holds these and more.
export const OUTCOME_WORDS = {
  complete: 'Complete',
  partial: 'Partly done',
  needs_input: 'Waiting on you',
  blocked: 'Blocked',
};

// What a session says while its verdict is being read: the turn is over, and
// something about it is still going on.
export const CHECKING_WORDS = 'Checking how the turn went…';

// One glyph per kind, on the corner of the agent's mark: a check in a circle
// for done, a list for work left, a speech bubble for a session that waits on
// the person, a triangle for one that is stuck - and the exclamation mark in a
// circle for a verdict that could not be had.
export const OUTCOME_ICON = {
  complete: 'checkCircle',
  partial: 'list',
  needs_input: 'chat',
  blocked: 'alert',
  error: 'alertCircle',
};

// The mark of a turn that is over while something about it still goes on:
// background work it started, or the read of its verdict.
export const TASKS_ICON = 'waiting';

// glyphFor is the glyph a badge kind draws: an outcome, 'tasks', or nothing.
function glyphFor(kind) {
  return kind === 'tasks' ? TASKS_ICON : OUTCOME_ICON[kind] || '';
}

/** outcomeIcon is the glyph of one kind as an element, or null for a kind without one. */
export function outcomeIcon(kind, size = 16) {
  const glyph = glyphFor(kind);
  return glyph ? iconEl(glyph, size) : null;
}

/** outcomeIconHtml is the same glyph as markup, for html`` templates ('' without one). */
export function outcomeIconHtml(kind, size = 16) {
  const glyph = glyphFor(kind);
  return glyph ? icon(glyph, size) : '';
}

/**
 * outcomeCurrent is an outcome if it is the verdict on the turn the session is
 * resting after: made for the very idle its activity is at. Anything else is
 * about a turn that is over - the server takes it away, and a frame that is
 * late or out of order is not drawn meanwhile.
 */
export function outcomeCurrent(outcome, act) {
  if (!outcome || !act || act.state !== 'idle') return null;
  return Number(outcome.since) === Number(act.since) ? outcome : null;
}

/** outcomeMark is which mark an outcome draws: a kind, 'error', or ''. */
export function outcomeMark(outcome) {
  if (!outcome) return '';
  if (outcome.phase === 'error') return 'error';
  if (outcome.phase === 'ready' && OUTCOME_WORDS[outcome.kind]) return outcome.kind;
  return '';
}

/**
 * outcomeLine is an outcome in one line: its word and why. A verdict that
 * failed says what failed - nothing fails without saying so.
 */
export function outcomeLine(outcome) {
  if (!outcome) return '';
  if (outcome.phase === 'pending') return CHECKING_WORDS;
  if (outcome.phase === 'error') return 'Outcome not checked' + (outcome.error ? ': ' + outcome.error : '');
  const word = OUTCOME_WORDS[outcome.kind];
  if (!word) return '';
  return outcome.reason ? word + ' · ' + outcome.reason : word;
}

/**
 * outcomeNews is what a notification of a judged turn says, and what a tap on
 * it opens - the server's push says the same (push.go outcomeBody). A turn
 * that ends by asking is a question; one with no verdict is "Finished".
 */
export function outcomeNews(outcome, question) {
  if (!outcome || outcome.phase !== 'ready') return { body: 'Finished', kind: 'finished' };
  const word = OUTCOME_WORDS[outcome.kind];
  const todos = Array.isArray(outcome.todos) ? outcome.todos : [];
  const rest = outcome.kind === 'partial' && todos.length
    ? todos.length + ' open: ' + todos[0]
    : (outcome.reason || '');
  if (outcome.kind === 'needs_input' && question) {
    const text = question.items && question.items[0] && question.items[0].text;
    return { body: clip(text || 'Needs an answer'), kind: 'question' };
  }
  if (!word) return { body: 'Finished', kind: 'finished' };
  return { body: clip(rest ? word + ' · ' + rest : word), kind: 'finished' };
}

function clip(text) {
  const words = String(text || '').split(/\s+/).filter(Boolean).join(' ');
  return words.length > 180 ? words.slice(0, 179).trim() + '…' : words;
}
