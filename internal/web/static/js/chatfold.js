// The chat as it stands, folded from its events: the browser's copy of the
// server's projection (internal/agent/projection.go, AGENT-HOST.md §3).
//
// The rules are the server's, so that a page that replays the journal from
// any seq ends up with exactly what the server holds:
//
//   * an event whose seq is not past the last one applied is ignored;
//   * a `part` event creates a part or replaces it whole, and keeps it where
//     it is; a part with `parent_part_id` goes into its parent's children;
//   * a delta - the coalesced `part.delta` of the journal and the live
//     `delta` frame alike - is idempotent by its offset (UTF-16 code units,
//     which is what a JavaScript string counts): text the part already has is
//     not appended again, and a delta past the end is a gap left for the
//     journal to fill;
//   * a turn that ends other than completed leaves nothing streaming.
//
// On top of the server's projection it keeps what drawing needs: the order
// of the timeline (messages, the questions, the rest of an answer that went
// on after a question, and the lines between them: notices, account
// switches, model changes, errors, interrupted or limited turns), and a
// revision per message and part, so that the view re-renders only what an
// event touched and never rebuilds a part that did not change.

/** newChat is the fold of an empty journal. */
export function newChat() {
  return {
    lastSeq: 0,
    lastAt: 0,
    messages: new Map(),
    parts: new Map(),
    asks: new Map(),
    // The timeline: { key, type: 'message' | 'ask' | 'more' | 'line', id |
    // line }; a 'more' is the segment-th piece of a message cut by a
    // question (cutAtAsk).
    timeline: [],
    turn: null,
    lastTurn: null,
    model: '',
    effort: '',
    account: '',
    // A switch of account asked for that has not happened yet: { to,
    // clientId }, until the account.switched it ends in (or its failure).
    pendingSwitch: null,
    process: '',
    processDetail: '',
    conversation: '',
    tasks: 0,
    info: null,
    usage: { context: 0, window: 0 },
    cost: 0,
    rateLimits: {},
    // Bumped by every change, so a caller can tell whether a batch of events
    // changed anything at all.
    rev: 0,
    // The revision of the last change to any question: a plan part is drawn
    // from the ask it waits on, so its message has to look again.
    askRev: 0,
  };
}

function touchMessage(chat, id) {
  const message = chat.messages.get(id);
  if (message) message.rev = ++chat.rev;
}

// touchPart moves the revision of a part and of every part and message it is
// inside, so that whatever draws the outer one knows it has to look again.
export function touchPart(chat, part) {
  let at = part;
  const seen = new Set();
  while (at && !seen.has(at.id)) {
    seen.add(at.id);
    at.rev = ++chat.rev;
    at = at.parentId ? chat.parts.get(at.parentId) : null;
  }
  touchMessage(chat, part.messageId);
}

function touchAsk(chat, id) {
  const ask = chat.asks.get(id);
  chat.askRev = ++chat.rev;
  if (ask) ask.rev = chat.askRev;
}

function messageFor(chat, ev) {
  let message = chat.messages.get(ev.message_id);
  if (message) return message;
  message = {
    id: ev.message_id, role: 'assistant', state: 'streaming', clientId: '', text: '', attachments: [],
    answerTo: '', turnId: ev.turn_id || '', model: '', at: ev.at || 0, parts: [], cuts: [], rev: ++chat.rev,
  };
  chat.messages.set(message.id, message);
  add(chat, { key: 'm:' + message.id, type: 'message', id: message.id });
  return message;
}

// waiting says whether a message is the person's and the agent has not
// taken it up yet: it is on its way to the agent, or held behind the turn.
function waiting(message) {
  return !!message && message.role === 'user' && (message.state === 'pending' || message.state === 'queued');
}

// waitingFrom is where the person's messages the agent has not taken up yet
// begin, at the end of the timeline.
function waitingFrom(chat) {
  let at = chat.timeline.length;
  while (at > 0) {
    const one = chat.timeline[at - 1];
    if (one.type !== 'message' || !waiting(chat.messages.get(one.id))) break;
    at -= 1;
  }
  return at;
}

// add puts what the agent did - an answer, a question, a line - into the
// timeline above what the person sent that the agent has not seen yet: such
// a message stays the conversation's newest word until it is taken up.
function add(chat, entry) {
  chat.timeline.splice(waitingFrom(chat), 0, entry);
}

// takenUp puts a message the agent has just taken up (or dropped unread)
// under everything the agent did before it, above what still waits. One
// taken up into a running turn that is not its prompt - Claude reads a
// message sent during a turn at its next step, Codex a steered one - is the
// turn's from then on: the turn's answers are cut there, and what the agent
// says after it is drawn under it (cutTurn), not above it in the answer it
// goes on with.
function takenUp(chat, message) {
  const from = chat.timeline.findIndex((one) => one.type === 'message' && one.id === message.id);
  if (from < 0) return;
  const [entry] = chat.timeline.splice(from, 1);
  add(chat, entry);
  const turn = chat.turn;
  if (message.state !== 'delivered' || !turn || !turn.id || turn.messageId === message.id) return;
  message.turnId = turn.id;
  cutTurn(chat, turn.id);
}

// within says whether part is node or nested anywhere inside node.
function within(chat, part, node) {
  if (part === node) return true;
  return node.children.some((child) => within(chat, part, child));
}

// A part sent light (agent.Lighten) has neither its output nor more of its
// content than its row shows (`lite`), or holds back parts nested in it
// (`held`): opened, it is asked for (fill). `seq` is the last event that
// changed it, so that what is asked for never takes back what came since.
function applyPart(chat, ev) {
  let part = chat.parts.get(ev.part_id);
  if (part) {
    part.kind = ev.kind || part.kind;
    part.state = ev.state || '';
    part.text = ev.text || '';
    part.content = ev.content === undefined ? null : ev.content;
    part.updatedAt = ev.updated_at || ev.at || part.updatedAt;
    part.lite = !!ev.lite;
    // A whole part event says nothing of what was held back: only what
    // is asked for fills that in.
    if (ev.held) part.held = Math.max(part.held, Number(ev.held) || 0);
    if (ev.seq) part.seq = ev.seq;
    touchPart(chat, part);
    return;
  }
  part = {
    id: ev.part_id, messageId: ev.message_id || '', parentId: ev.parent_part_id || '', kind: ev.kind || 'text',
    state: ev.state || '', text: ev.text || '', content: ev.content === undefined ? null : ev.content,
    at: ev.at || 0, updatedAt: ev.updated_at || ev.at || 0, children: [], rev: ++chat.rev,
    lite: !!ev.lite, held: Number(ev.held) || 0, seq: Number(ev.seq) || 0,
  };
  chat.parts.set(part.id, part);
  const parent = part.parentId ? chat.parts.get(part.parentId) : null;
  if (parent && parent !== part && !within(chat, parent, part)) {
    if (!part.messageId) part.messageId = parent.messageId;
    parent.children.push(part);
    touchPart(chat, parent);
    return;
  }
  const message = messageFor(chat, ev);
  part.messageId = message.id;
  message.parts.push(part);
  touchMessage(chat, message.id);
}

/**
 * appendDelta adds text that starts at offset to a part, idempotently: what
 * the part already holds is not added again, and a delta that starts past its
 * end is a gap. It answers whether the part changed.
 */
export function appendDelta(part, offset, text) {
  const have = part.text.length;
  const at = Number(offset) || 0;
  const end = at + text.length;
  if (at > have || end <= have) return false;
  part.text += at < have ? text.slice(have - at) : text;
  return true;
}

function applyDeltaTo(chat, ev) {
  let part = chat.parts.get(ev.part_id);
  if (!part) {
    applyPart(chat, { at: ev.at, message_id: ev.message_id, part_id: ev.part_id, kind: 'text', state: 'streaming' });
    part = chat.parts.get(ev.part_id);
  }
  // A light part's text is not here to add to: it comes whole with the
  // part when it is asked for.
  if (part.lite) return false;
  if (!appendDelta(part, ev.offset, ev.text || '')) return false;
  if (ev.at) part.updatedAt = ev.at;
  if (ev.seq) part.seq = ev.seq;
  touchPart(chat, part);
  return true;
}

function line(chat, ev, data) {
  add(chat, { key: 'l:' + ev.seq, type: 'line', line: { seq: ev.seq, at: ev.at, ...data }, rev: ++chat.rev });
}

// lastLine is the newest line of a kind, or null.
function lastLine(chat, kind) {
  for (let i = chat.timeline.length - 1; i >= 0; i -= 1) {
    const one = chat.timeline[i];
    if (one.type === 'line' && one.line.kind === kind) return one;
  }
  return null;
}

function endTurn(chat, ev) {
  let turn = chat.turn;
  if (turn && ev.turn_id && turn.id && ev.turn_id !== turn.id) return;
  if (!turn) turn = { id: ev.turn_id || '', messageId: '', startedAt: ev.at };
  Object.assign(turn, {
    endedAt: ev.at, outcome: ev.outcome || '', usage: ev.usage || null, cost: Number(ev.cost_usd) || 0,
    error: ev.text || '',
  });
  if (ev.usage) {
    if (ev.usage.context) chat.usage.context = ev.usage.context;
    if (ev.usage.window) chat.usage.window = ev.usage.window;
  }
  chat.cost += turn.cost;
  chat.lastTurn = turn;
  chat.turn = null;
  // The assistant message of the turn carries its usage, behind its "i".
  for (const message of chat.messages.values()) {
    if (message.role === 'assistant' && message.turnId && message.turnId === turn.id) {
      message.turn = turn;
      touchMessage(chat, message.id);
    }
  }
  if (turn.outcome !== 'completed') {
    for (const message of chat.messages.values()) {
      if (message.role === 'assistant' && message.state === 'streaming') {
        message.state = 'interrupted';
        touchMessage(chat, message.id);
      }
    }
    for (const part of chat.parts.values()) {
      if (part.state === 'streaming' || part.state === 'running') {
        part.state = 'interrupted';
        touchPart(chat, part);
      }
    }
    // A limit says itself twice - the error that hit it and the turn it
    // ended - and the person is told once, with when it renews. So does a
    // turn that failed on an error: "The turn failed" says its words.
    const at = waitingFrom(chat) - 1;
    const before = chat.timeline[at];
    const error = before && before.type === 'line' && before.line.kind === 'error' ? before.line : null;
    if (error && ((turn.outcome === 'limited' && error.code === 'rate_limit')
      || (turn.outcome === 'failed' && (!turn.error || turn.error.trim() === String(error.text || '').trim())))) {
      chat.timeline.splice(at, 1);
      if (!turn.error) turn.error = error.text;
      turn.errorCode = error.code || '';
    }
    line(chat, ev, { kind: 'turn', outcome: turn.outcome, text: turn.error, code: turn.errorCode || '', turn });
    // How a turn ended is said right under it, above the messages the
    // person sent while it ran (add): "Interrupted" under a message that was
    // only queued read as if that message had been cut short. A limit goes
    // under them: its card is the chat's newest word while nothing came
    // after it, which is when it offers to go on with another account
    // (chatview.js lastLimit).
    if (turn.outcome === 'limited') {
      const at = chat.timeline.findIndex((one) => one.key === 'l:' + ev.seq);
      chat.timeline.push(...chat.timeline.splice(at, 1));
    }
  }
}

// cutAtAsk keeps what an answer says after a question under the question's
// card. A card that names a tool part the chat has is drawn at that call,
// inside the answer (chatparts.js askPlaces), and needs nothing here. Any
// other - the scripted agent's, an ask_user of OpenCode's, which names no
// part - comes after everything its turn's answers held when it was asked:
// each of them is cut there, and what it gets from then on is drawn as its
// continuation, an entry of its own under the card ('m:<id>#2'). The
// answer was made when the turn started, so without the cut the reply to
// the question went above it, and the answered card ended the turn.
function cutAtAsk(chat, ask) {
  const at = ask.payload && typeof ask.payload === 'object' ? ask.payload.part_id : '';
  if (at && chat.parts.has(at)) return;
  const turnId = ask.turnId || (chat.turn && chat.turn.id) || '';
  if (turnId) cutTurn(chat, turnId);
}

// cutTurn cuts each answer of a turn where it is, and puts what it says from
// then on in the timeline as an entry of its own, a 'more'.
function cutTurn(chat, turnId) {
  for (const message of chat.messages.values()) {
    if (message.role !== 'assistant' || message.turnId !== turnId) continue;
    message.cuts.push(message.parts.length);
    add(chat, { key: 'm:' + message.id + '#' + (message.cuts.length + 1), type: 'more', id: message.id, segment: message.cuts.length });
    touchMessage(chat, message.id);
  }
}

/**
 * apply folds one journal event in, and answers whether it changed anything.
 * Events have to come in seq order; one that does not move the seq forward is
 * ignored.
 */
export function apply(chat, ev) {
  if (!ev || !ev.type) return false;
  const seq = Number(ev.seq) || 0;
  if (seq && seq <= chat.lastSeq) return false;
  if (seq) chat.lastSeq = seq;
  const at = Math.max(Number(ev.at) || 0, Number(ev.updated_at) || 0);
  if (at > chat.lastAt) chat.lastAt = at;
  const before = chat.rev;
  switch (ev.type) {
    case 'turn.started':
      // The account a turn ran on is the one its start names (AGENT-HOST.md
      // §2); a journal from before accounts names none.
      if (ev.account) chat.account = ev.account;
      chat.turn = { id: ev.turn_id || '', messageId: ev.message_id || '', startedAt: ev.at, account: ev.account || chat.account };
      chat.rev += 1;
      break;
    case 'turn.ended':
      endTurn(chat, ev);
      chat.rev += 1;
      break;
    case 'message': {
      const known = chat.messages.get(ev.message_id);
      if (known) {
        const was = waiting(known);
        Object.assign(known, {
          role: ev.role, state: ev.state || '', text: ev.text || '', attachments: ev.attachments || [],
          answerTo: ev.answer_to || '', model: ev.model || '',
        });
        if (was && !waiting(known)) takenUp(chat, known);
        touchMessage(chat, known.id);
        break;
      }
      const message = {
        id: ev.message_id, role: ev.role || 'assistant', state: ev.state || '', clientId: ev.client_id || '',
        text: ev.text || '', attachments: ev.attachments || [], answerTo: ev.answer_to || '',
        turnId: ev.turn_id || '', model: ev.model || '', at: ev.at || 0, parts: [], cuts: [], rev: ++chat.rev,
      };
      chat.messages.set(message.id, message);
      // What the person says is the newest word; an answer goes above what
      // the person sent that the agent has not taken up yet.
      const entry = { key: 'm:' + message.id, type: 'message', id: message.id };
      if (message.role === 'user') chat.timeline.push(entry);
      else add(chat, entry);
      break;
    }
    case 'message.state': {
      const message = chat.messages.get(ev.message_id);
      if (message) {
        const was = waiting(message);
        message.state = ev.state || '';
        if (was && !waiting(message)) takenUp(chat, message);
        touchMessage(chat, message.id);
      }
      break;
    }
    case 'part':
      applyPart(chat, ev);
      break;
    case 'part.delta':
      applyDeltaTo(chat, ev);
      break;
    case 'ask':
      if (!chat.asks.has(ev.ask_id)) {
        chat.asks.set(ev.ask_id, {
          id: ev.ask_id, turnId: ev.turn_id || '', payload: ev.payload || {}, at: ev.at, seq,
          answered: false, answer: null, byDevice: '', clientId: '', delivered: '', deliver: ev.via || '', closed: false,
          rev: 0,
        });
        add(chat, { key: 'a:' + ev.ask_id, type: 'ask', id: ev.ask_id });
        cutAtAsk(chat, chat.asks.get(ev.ask_id));
        touchAsk(chat, ev.ask_id);
      }
      break;
    case 'ask.answered': {
      const ask = chat.asks.get(ev.ask_id);
      if (ask && !ask.answered) {
        Object.assign(ask, {
          answered: true, answer: ev.answer === undefined ? null : ev.answer, clientId: ev.client_id || '',
          byDevice: ev.by_device || '', answeredAt: ev.at,
        });
        touchAsk(chat, ev.ask_id);
      }
      break;
    }
    case 'ask.delivered': {
      const ask = chat.asks.get(ev.ask_id);
      if (ask) {
        ask.delivered = ev.via || '';
        touchAsk(chat, ev.ask_id);
      }
      break;
    }
    case 'ask.closed': {
      // The person wrote something else after the call was gone: nobody
      // waits for the answer any more, though it may still come.
      const ask = chat.asks.get(ev.ask_id);
      if (ask && !ask.answered && !ask.closed) {
        ask.closed = true;
        touchAsk(chat, ev.ask_id);
      }
      break;
    }
    case 'account.switched':
      chat.account = ev.to || '';
      chat.pendingSwitch = null;
      line(chat, ev, { kind: 'account', from: ev.from || '', to: ev.to || '', reason: ev.reason || '' });
      break;
    case 'model.changed': {
      // What the chat had before goes with the line: an event that only
      // names the same model again - the CLI resolving the alias the chat
      // started on ("sonnet" to its full id) at its first turn, and again
      // after an account switch starts it anew - is no change to show
      // (chatparts.js lineHtml).
      const was = chat.model;
      const wasEffort = chat.effort;
      if (ev.model) chat.model = ev.model;
      chat.effort = ev.effort || '';
      line(chat, ev, { kind: 'model', model: ev.model || '', effort: ev.effort || '', was, wasEffort });
      break;
    }
    case 'rate_limit':
      chat.rateLimits[ev.window || ''] = { used: Number(ev.used) || 0, resetsAt: Number(ev.resets_at) || 0, at: ev.at };
      chat.rev += 1;
      break;
    case 'usage':
      if (ev.usage) {
        chat.usage.context = ev.usage.context || 0;
        chat.usage.window = ev.usage.window || 0;
      }
      chat.rev += 1;
      break;
    case 'process':
      chat.process = ev.state || '';
      chat.processDetail = ev.detail || '';
      chat.rev += 1;
      break;
    case 'conversation':
      chat.conversation = ev.conversation_id || '';
      break;
    case 'tasks':
      chat.tasks = Number(ev.count) || 0;
      chat.rev += 1;
      break;
    case 'agent.info':
      chat.info = ev.content || null;
      chat.rev += 1;
      break;
    case 'notice': {
      // Claude's word that it goes on without the thinking of another
      // organization belongs to the switch that caused it: a hint behind the
      // divider's "i", not a line of its own (research/08).
      const divider = ev.code === 'foreign_thinking' ? lastLine(chat, 'account') : null;
      if (divider) {
        divider.line.hint = ev.text || '';
        divider.rev = ++chat.rev;
        break;
      }
      line(chat, ev, { kind: 'notice', code: ev.code || '', text: ev.text || '', askId: ev.ask_id || '' });
      break;
    }
    case 'command':
      // A slash command the person ran is a line of its own; the other
      // commands (interrupt, model, park) say what they did by their effects.
      if (ev.op === 'command') {
        line(chat, ev, { kind: 'command', text: '/' + (ev.text || '') + (ev.args ? ' ' + ev.args : ''), clientId: ev.client_id || '' });
      } else if (ev.op === 'switch_account') {
        // The switch itself is the account.switched that follows; until
        // then the composer says one is on its way.
        chat.pendingSwitch = { to: ev.to || '', clientId: ev.client_id || '' };
        chat.rev += 1;
      }
      break;
    case 'error':
      if (ev.code === 'switch_failed') chat.pendingSwitch = null;
      line(chat, ev, { kind: 'error', code: ev.code || '', text: ev.text || '' });
      break;
    default:
      break;
  }
  return chat.rev !== before;
}

// stateKey names what an event sets that a later event of the same key sets
// again and nothing reads in between, or '' (agent.stateKey).
function stateKey(ev) {
  if (ev.client_id) return '';
  switch (ev.type) {
    case 'rate_limit': return 'rate_limit\u0000' + (ev.window || '');
    case 'tasks':
    case 'agent.info': return ev.type;
    case 'usage': return ev.usage ? ev.type : '';
    default: return '';
  }
}

/**
 * compact is a run of events with what later events of the same run make
 * moot left out, by the server's rules (internal/agent/compact.go): folded
 * on top of what came before the run, it gives what the whole run gives.
 * The events of a part become one part event where it first appears, with
 * what its last part event said (and, in updated_at, when) and the deltas
 * after that; a turn that ends other than completed splits the run; a value
 * set again later (a rate_limit window, tasks, usage, agent.info) goes.
 */
export function compact(events) {
  if (events.length < 2) return events;
  const runs = new Map();
  const keys = new Array(events.length);
  const lastOf = new Map();
  let span = 0;
  events.forEach((ev, i) => {
    if ((ev.type === 'part' || ev.type === 'part.delta') && ev.part_id) {
      const key = ev.part_id + '\u0000' + span;
      keys[i] = key;
      let run = runs.get(key);
      if (!run) { run = { first: i, last: -1 }; runs.set(key, run); }
      if (ev.type === 'part') run.last = i;
    } else if (ev.type === 'turn.ended' && ev.outcome !== 'completed') {
      span += 1;
    }
    const state = stateKey(ev);
    if (state) lastOf.set(state, i);
  });
  const out = [];
  events.forEach((ev, i) => {
    const state = stateKey(ev);
    if (state && lastOf.get(state) !== i) return;
    if (!keys[i]) { out.push(ev); return; }
    const run = runs.get(keys[i]);
    if (run.last < 0 || (i > run.last && i !== run.first)) { out.push(ev); return; }
    if (i !== run.first) return;
    if (i === run.last) { out.push(ev); return; }
    const last = events[run.last];
    const merged = ev.type === 'part.delta'
      ? { t: ev.t, seq: ev.seq, at: ev.at, type: 'part', message_id: ev.message_id, part_id: ev.part_id }
      : { ...ev };
    merged.type = 'part';
    for (const field of ['kind', 'state', 'text', 'content', 'lite']) {
      if (last[field] === undefined) delete merged[field];
      else merged[field] = last[field];
    }
    // What was held back of the part stays held back until it is asked
    // for, whatever came whole after it.
    const held = Math.max(Number(ev.held) || 0, Number(last.held) || 0);
    if (held) merged.held = held; else delete merged.held;
    merged.updated_at = last.at;
    out.push(merged);
  });
  return out;
}

/**
 * applyLive folds a live delta frame in: never journaled as such, applied by
 * the same idempotent rule, and only to a part the events have created.
 */
export function applyLive(chat, frame) {
  const part = chat.parts.get(frame.part_id);
  if (!part || part.lite) return false;
  if (!appendDelta(part, frame.offset, frame.text || '')) return false;
  touchPart(chat, part);
  return true;
}

/**
 * fill puts in what a light part left out, as GET
 * /api/chats/{id}/parts/{part} has it: { seq, part, children } - the part
 * whole and the parts nested directly in it (light, each with how many it
 * holds back in turn) as of the chat's seq. What changed after that seq is
 * kept as the stream brought it, and a nested part the stream brought that
 * the answer does not have yet stays, after the ones it has. It answers
 * whether the part is there to fill.
 */
export function fill(chat, detail) {
  const ev = detail && detail.part;
  const part = ev ? chat.parts.get(ev.part_id) : null;
  if (!part) return false;
  const seq = Number(detail.seq) || 0;
  const take = (one, from) => {
    if ((one.seq || 0) > seq) return;
    one.kind = from.kind || one.kind;
    one.state = from.state || '';
    one.text = from.text || '';
    one.content = from.content === undefined ? null : from.content;
    one.updatedAt = from.updated_at || from.at || one.updatedAt;
    one.lite = !!from.lite;
  };
  take(part, ev);
  const listed = [];
  for (const from of (detail.children || [])) {
    let child = chat.parts.get(from.part_id);
    if (child && child.parentId !== part.id) continue;
    if (!child) {
      child = {
        id: from.part_id, messageId: from.message_id || part.messageId, parentId: part.id, kind: from.kind || 'text',
        state: '', text: '', content: null, at: from.at || 0, updatedAt: 0, children: [], rev: ++chat.rev,
        lite: false, held: 0, seq: 0,
      };
      chat.parts.set(child.id, child);
    }
    take(child, from);
    child.held = Math.max(child.held, Number(from.held) || 0);
    child.rev = ++chat.rev;
    listed.push(child);
  }
  const have = new Set(listed);
  part.children = listed.concat(part.children.filter((one) => !have.has(one)));
  part.held = 0;
  touchPart(chat, part);
  return true;
}

/** openAsks is every question nobody has answered or closed yet, oldest first. */
export function openAsks(chat) {
  return [...chat.asks.values()].filter((ask) => !ask.answered && !ask.closed).sort((a, b) => a.seq - b.seq);
}

/** busy says whether a turn is running. */
export function busy(chat) { return !!chat.turn; }

/** currentTodo is the newest task list part, the one pinned while a turn runs. */
export function currentTodo(chat) {
  let found = null;
  for (const part of chat.parts.values()) {
    if (part.kind === 'todo' && (!found || part.updatedAt >= found.updatedAt)) found = part;
  }
  return found;
}

/**
 * seenClient says whether the journal has what a client_id sent: a user
 * message, or the line of a slash command.
 */
export function seenClient(chat, clientId) {
  if (!clientId) return false;
  if (userMessageByClient(chat, clientId)) return true;
  return chat.timeline.some((entry) => entry.type === 'line' && entry.line.kind === 'command'
    && entry.line.clientId === clientId);
}

/** userMessageByClient finds the user message a client_id produced. */
export function userMessageByClient(chat, clientId) {
  if (!clientId) return null;
  for (const message of chat.messages.values()) {
    if (message.role === 'user' && message.clientId === clientId) return message;
  }
  return null;
}
