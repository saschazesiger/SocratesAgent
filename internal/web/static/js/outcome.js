// The outcome of a turn: how the read Home makes of every turn's end judged
// the work it left - complete, partly done, waiting on the person, blocked -
// when the dashboard has it switched on (internal/server/outcome.go).
//
// The sidebar, the header, Home and the notifications all say it, so the
// words and the marks are here, once.

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

// The hourglass of a turn that is over while something about it still goes
// on: background work it started, or the read of its verdict.
export const HOURGLASS_SVG = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
  + '<path d="M5 2.5h14M5 21.5h14"/><path d="M7 2.5v2.2c0 2.6 5 4.6 5 7.3 0 2.7-5 4.7-5 7.3v2.2"/>'
  + '<path d="M17 2.5v2.2c0 2.6-5 4.6-5 7.3 0 2.7 5 4.7 5 7.3v2.2"/>'
  + '<path d="M8.5 20.5c0-1.6 3.5-3 3.5-4.5 0 1.5 3.5 2.9 3.5 4.5z" fill="currentColor" stroke-width="1"/></svg>';

// One small drawing per kind, on the corner of the agent's mark where the
// hourglass of background work sits: two ticks for done, a list for work
// left, a speech bubble for a session that waits on the person, a triangle
// for one that is stuck - and an exclamation mark for a verdict that could
// not be had.
export const OUTCOME_SVG = {
  complete: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M1.5 13l4.5 4.5L15 8"/><path d="M11 16l1.5 1.5L22.5 8"/></svg>',
  partial: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M9 6h12M9 12h12M9 18h12"/><path d="M3 6h.01M3 12h.01M3 18h.01" stroke-width="4"/></svg>',
  needs_input: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M21 11.5a8.5 8.5 0 0 1-12.3 7.6L3 21l1.9-5.7A8.5 8.5 0 1 1 21 11.5z"/></svg>',
  blocked: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
    + '<path d="M12 3L2 20.5h20L12 3z"/><path d="M12 10v4.5M12 17.5h.01"/></svg>',
  error: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" aria-hidden="true">'
    + '<path d="M12 4v10M12 19.5h.01"/></svg>',
};

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
