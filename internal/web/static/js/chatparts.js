// The parts of a chat, as markup: what an agent's message is made of (text,
// reasoning, tool calls, diffs, task lists, plans, subagents and workflows),
// the user's messages, the questions, and the lines between them.
//
// Every function here is a pure function of the chat state and the view's
// open/closed choices, and answers html`` markup whose list items are keyed
// (data-ds-key): the view morphs it into the page, so a part that did not
// change keeps its element, and a spinner that is turning keeps turning.
//
// The content of a part is the adapter's. The shapes read here are Claude's
// and Codex's (AGENT-HOST.md §5.1, §5.2) and the scripted agent's, which uses
// Claude's: whatever a shape lacks is simply not drawn.

import { html, raw, icon } from './ui.js';
import { markdown, codeBlock } from './md.js';
import { fmtTokens } from './api.js';

/* ------------------------------------------------------------- the basics */

const STATE_WORDS = {
  running: 'Running', streaming: 'Writing', done: 'Done', error: 'Failed', interrupted: 'Interrupted',
};

/** statusMark is a part's state as an icon (or the turning arc) and a word. */
export function statusMark(state) {
  const word = STATE_WORDS[state] || '';
  if (state === 'running' || state === 'streaming') {
    return html`<span class="so-status-mark" data-state="${state}" role="img" aria-label="${word}" data-tip="${word}"><span class="ds-spinner" aria-hidden="true"></span></span>`;
  }
  const glyph = state === 'error' ? 'alertCircle' : state === 'interrupted' ? 'ban' : 'check';
  if (!word) return '';
  return html`<span class="so-status-mark" data-state="${state}" role="img" aria-label="${word}" data-tip="${word}">${raw(icon(glyph, 16))}</span>`;
}

/** seconds is a duration the way the rows say it: 0.4 s, 12 s, 3 min 5 s. */
export function seconds(ms) {
  const value = Number(ms) || 0;
  if (value <= 0) return '';
  if (value < 10000) return (value / 1000).toFixed(1).replace(/\.0$/, '') + ' s';
  const s = Math.round(value / 1000);
  if (s < 60) return s + ' s';
  return Math.floor(s / 60) + ' min' + (s % 60 ? ' ' + (s % 60) + ' s' : '');
}

/** infoButton is the "i" of infoTip() as markup; the view opens it on a tap. */
export function infoButton(lines, label = 'Details') {
  const shown = lines.filter((line) => line !== null && line !== undefined && line !== '');
  if (!shown.length) return '';
  return html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" data-action="info" aria-label="${label}" data-tip="${shown.join('\n')}">${raw(icon('info', 18))}</button>`;
}

function chevron() {
  return raw(icon('chevronDown', 20, 'ds-chevron'));
}

function pretty(value) {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

/* ----------------------------------------------------------- tool content */

/**
 * toolOf reads a tool part's content into what a row shows: the tool's name,
 * one line about the call, what it was called with, how long it took, and
 * whether it failed.
 */
export function toolOf(part) {
  const c = (part.content && typeof part.content === 'object') ? part.content : {};
  const input = c.input || c.arguments || null;
  const field = (key) => (input && typeof input === 'object' && typeof input[key] === 'string' ? input[key] : '');
  let name = c.tool || c.name || 'Tool';
  // Codex names its items by their type; the row says what they are.
  if (c.tool === 'command') name = 'Command';
  else if (c.tool === 'mcp' || c.tool === 'dynamic') name = c.name || c.tool;
  else if (c.tool === 'subagent') name = 'Subagent';
  else if (c.tool === 'collab') name = 'Agents';
  else if (c.tool === 'file_change') name = 'Edit';
  else if (c.tool === 'turn_diff') name = 'Changes';
  const agent = c.agent && typeof c.agent === 'object' ? c.agent : null;
  const summary = c.summary || c.command || field('command') || c.path || field('file_path') || field('pattern')
    || field('description') || field('url') || field('query') || (agent && (agent.label || agent.description))
    || (c.workflow && (c.workflow.description || c.workflow.name)) || c.name || '';
  let duration = Number(c.duration_ms) || 0;
  if (!duration && part.state !== 'running' && part.state !== 'streaming' && part.updatedAt > part.at) {
    duration = part.updatedAt - part.at;
  }
  const exit = c.exit_code;
  const failed = part.state === 'error' || c.is_error === true || (typeof exit === 'number' && exit !== 0);
  return { name, summary: String(summary).split('\n')[0], input, duration, failed, agent, workflow: c.workflow || null,
    command: c.command || field('command'), content: c };
}

// The glyph a tool is drawn with, by what it does.
function toolGlyph(name) {
  const n = String(name).toLowerCase();
  if (/bash|command|shell|monitor/.test(n)) return 'monitor';
  if (/^read|notebookread/.test(n)) return 'fileText';
  if (/edit|write|change|patch/.test(n)) return 'pencil';
  if (/grep|glob|search|find/.test(n)) return 'search';
  if (/web|fetch|url/.test(n)) return 'globe';
  if (/workflow/.test(n)) return 'apps';
  if (/agent|task|subagent|collab/.test(n)) return 'users';
  if (/todo/.test(n)) return 'list';
  if (/plan/.test(n)) return 'flag';
  return 'wrench';
}

function isGroup(part, tool) {
  if (part.children.length) return true;
  if (tool.workflow) return true;
  return ['Task', 'Agent', 'Subagent', 'Workflow'].includes(tool.name) || !!tool.agent;
}

/* -------------------------------------------------------------- the diffs */

/**
 * parseDiff reads unified diff text into files of hunks of lines. A diff with
 * no file headers (Codex's per-change diff) is one file named `path`.
 */
export function parseDiff(text, path = '') {
  const files = [];
  let file = null;
  let hunk = null;
  let oldNo = 0;
  let newNo = 0;
  const newFile = (name) => {
    file = { path: name, hunks: [], added: 0, removed: 0 };
    files.push(file);
    hunk = null;
  };
  const strip = (name) => name.replace(/^[ab]\//, '').replace(/\t.*$/, '');
  const lines = String(text || '').replace(/\n$/, '').split('\n');
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    if (line.startsWith('diff --git ')) {
      const m = /^diff --git a\/(.*) b\/(.*)$/.exec(line);
      newFile(m ? m[2] : line.slice(11));
      continue;
    }
    if (line.startsWith('--- ') && i + 1 < lines.length && lines[i + 1].startsWith('+++ ')) {
      const from = strip(line.slice(4));
      const to = strip(lines[i + 1].slice(4));
      const name = to === '/dev/null' ? from : to;
      if (!file || file.hunks.length) newFile(name);
      else file.path = name;
      if (from === '/dev/null') file.status = 'added';
      if (to === '/dev/null') file.status = 'deleted';
      i += 1;
      continue;
    }
    const head = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(line);
    if (head) {
      if (!file) newFile(path);
      oldNo = Number(head[1]);
      newNo = Number(head[2]);
      hunk = { header: line, lines: [] };
      file.hunks.push(hunk);
      continue;
    }
    if (!hunk) continue;
    if (line.startsWith('+')) {
      hunk.lines.push({ type: 'add', text: line.slice(1), newNo: newNo++ });
      file.added += 1;
    } else if (line.startsWith('-')) {
      hunk.lines.push({ type: 'del', text: line.slice(1), oldNo: oldNo++ });
      file.removed += 1;
    } else if (line.startsWith('\\')) {
      hunk.lines.push({ type: 'meta', text: line });
    } else {
      hunk.lines.push({ type: 'ctx', text: line.slice(1), oldNo: oldNo++, newNo: newNo++ });
    }
  }
  return files.filter((one) => one.hunks.length);
}

/** diffFiles is every file a diff part changes, from whichever shape it has. */
export function diffFiles(part) {
  const c = (part.content && typeof part.content === 'object') ? part.content : {};
  if (Array.isArray(c.changes)) {
    return c.changes.flatMap((change) => {
      const files = parseDiff(change.diff || '', change.move_path || change.path || '');
      for (const one of files) {
        if (!one.path) one.path = change.path || '';
        if (change.kind === 'add') one.status = 'added';
        if (change.kind === 'delete') one.status = 'deleted';
      }
      return files;
    });
  }
  if (Array.isArray(c.files)) return c.files.flatMap((one) => parseDiff(one.diff || '', one.path || ''));
  if (typeof c.diff === 'string' && c.diff) return parseDiff(c.diff, c.path || '');
  if (/^(---|diff --git|@@ )/m.test(part.text || '')) return parseDiff(part.text, c.path || '');
  return [];
}

const SIGN = { add: '+', del: '−', ctx: ' ', meta: '' };
const LINE_WORDS = { add: 'Added', del: 'Removed' };

function diffFileHtml(file, key, open) {
  const name = file.path || 'the file';
  const status = file.status === 'added' ? 'New file' : file.status === 'deleted' ? 'Deleted' : '';
  return html`<details class="so-diff__file" data-ds-key="${key}" data-open-key="${key}"${open ? html` open` : ''}><summary class="so-diff__head"><span class="so-diff__path so-break">${name}</span>${status ? html`<span class="so-diff__status">${status}</span>` : ''}<span class="so-diff__count"><span class="so-diff__added" aria-label="${file.added + ' added'}">+${file.added}</span><span class="so-diff__removed" aria-label="${file.removed + ' removed'}">−${file.removed}</span></span>${chevron()}</summary>${
    open ? html`<div class="so-diff__body" role="table" aria-label="${'Changes to ' + name}">${file.hunks.map((hunk) => html`<div class="so-diff__hunk" role="row"><span class="so-diff__hunk-head" role="cell">${hunk.header}</span></div>${
      hunk.lines.map((line) => html`<div class="so-diff__line" data-type="${line.type}" role="row"><span class="so-diff__no" role="cell" aria-hidden="true">${line.oldNo ?? ''}</span><span class="so-diff__no" role="cell" aria-hidden="true">${line.newNo ?? ''}</span><span class="so-diff__sign" role="cell"${LINE_WORDS[line.type] ? html` aria-label="${LINE_WORDS[line.type]}"` : ''}>${SIGN[line.type]}</span><span class="so-diff__code" role="cell">${line.text}</span></div>`)
    }`)}</div>` : ''
  }</details>`;
}

/** diffViewer is every file of a diff, each one collapsible. */
export function diffViewer(part, view) {
  const files = diffFiles(part);
  if (!files.length) return part.text ? html`<pre class="so-mono">${part.text}</pre>` : '';
  return html`<div class="so-diff">${files.map((file, i) => {
    const key = 'f:' + part.id + ':' + i;
    return diffFileHtml(file, key, view.isOpen(key, files.length <= 3));
  })}</div>`;
}

/* ------------------------------------------------------------- the todos */

/** todoItems reads a task list from Claude's, Codex's or a plain shape. */
export function todoItems(part) {
  const c = (part.content && typeof part.content === 'object') ? part.content : {};
  const list = c.items || c.todos || c.steps || [];
  return list.map((item) => {
    const status = String(item.status || 'pending').replace(/^inProgress$/, 'in_progress');
    return {
      text: item.subject || item.content || item.step || item.text || '',
      active: item.active_form || item.activeForm || '',
      status: status === 'completed' || status === 'done' ? 'completed' : status === 'in_progress' ? 'in_progress' : 'pending',
    };
  });
}

const TODO_WORDS = { completed: 'Done', in_progress: 'In progress', pending: 'To do' };

/** todoList is a task list, each task with its state as a mark and a word. */
export function todoList(part, { pinned = false } = {}) {
  const items = todoItems(part);
  const done = items.filter((item) => item.status === 'completed').length;
  return html`<div class="so-todo"${pinned ? html` data-pinned` : ''}><div class="so-todo__head">${raw(icon('list', 18))}<span>Tasks</span><span class="so-todo__count">${done} of ${items.length} done</span></div><ul class="so-todo__list">${
    items.map((item, i) => html`<li class="so-todo__item" data-ds-key="${'t' + i}" data-status="${item.status}"><span class="so-todo__mark" role="img" aria-label="${TODO_WORDS[item.status]}">${
      item.status === 'in_progress' ? html`<span class="ds-spinner" aria-hidden="true"></span>`
        : raw(icon(item.status === 'completed' ? 'checkCircle' : 'clock', 16))
    }</span><span class="so-todo__text">${item.status === 'in_progress' && item.active ? item.active : item.text}</span></li>`)
  }</ul></div>`;
}

/* ------------------------------------------------------------- the parts */

/** partHtml is one part, wherever it is: in a message or inside a group. */
export function partHtml(part, view) {
  switch (part.kind) {
    case 'text':
      return html`<div class="so-part so-md" data-ds-key="${'p:' + part.id}" data-kind="text" data-state="${part.state}">${markdown(part.text)}</div>`;
    case 'reasoning':
      return reasoningHtml(part, view);
    case 'tool':
      return toolHtml(part, view);
    case 'diff':
      return toolHtml(part, view, { diff: true });
    case 'todo':
      return html`<div class="so-part" data-ds-key="${'p:' + part.id}" data-kind="todo">${todoList(part)}</div>`;
    case 'plan':
      return planHtml(part, view);
    case 'image':
    case 'file':
      return fileHtml(part);
    case 'notice':
      return html`<div class="so-part so-line" data-ds-key="${'p:' + part.id}" data-kind="notice">${raw(icon('info', 16))}<span class="so-line__text">${part.text || ''}</span></div>`;
    default:
      return part.text ? html`<div class="so-part so-md" data-ds-key="${'p:' + part.id}">${markdown(part.text)}</div>` : '';
  }
}

function reasoningHtml(part, view) {
  const key = 'p:' + part.id;
  const open = view.isOpen(key, false);
  const c = part.content || {};
  const took = seconds(c.duration_ms || (part.state === 'done' ? part.updatedAt - part.at : 0));
  const word = part.state === 'streaming' ? 'Thinking…' : 'Thought' + (took ? ' for ' + took : '');
  return html`<details class="ds-collapsible so-part so-reasoning" data-ds-key="${key}" data-open-key="${key}" data-kind="reasoning" data-state="${part.state}"${open ? html` open` : ''}><summary class="so-reasoning__head">${raw(icon('sparkles', 16))}<span>${word}</span>${chevron()}</summary>${
    open ? html`<div class="so-reasoning__body so-md">${markdown(part.text || (c.redacted ? 'The reasoning is not shown.' : ''))}</div>` : ''
  }</details>`;
}

function fileHtml(part) {
  const c = part.content || {};
  const name = c.name || String(c.path || '').split('/').pop() || 'file';
  return html`<div class="so-part so-attachment" data-ds-key="${'p:' + part.id}" data-kind="${part.kind}">${raw(icon(part.kind === 'image' ? 'image' : 'file', 18))}<span class="so-ellipsis">${name}</span>${infoButton([c.path, c.mime], 'File details')}</div>`;
}

function toolHtml(part, view, { diff = false } = {}) {
  const tool = toolOf(part);
  const group = !diff && isGroup(part, tool);
  const key = 'p:' + part.id;
  const files = diff ? diffFiles(part) : [];
  // A group shows what its agents are doing, and an edit what it changed, so
  // both start open; a plain call is one line until it is asked for.
  const open = view.isOpen(key, group || (diff && files.length > 0));
  const state = tool.failed && part.state !== 'running' ? 'error' : part.state;
  const added = files.reduce((n, f) => n + f.added, 0);
  const removed = files.reduce((n, f) => n + f.removed, 0);
  let progress = '';
  if (tool.workflow && Number(tool.workflow.agents)) {
    progress = (Number(tool.workflow.agents_done) || 0) + ' of ' + tool.workflow.agents + ' agents';
  } else if (tool.agent && tool.agent.activity && part.state === 'running') {
    progress = tool.agent.activity;
  }
  const facts = [
    tool.name,
    tool.content.tool_use_id ? 'call ' + tool.content.tool_use_id : '',
    tool.agent && tool.agent.model ? 'model ' + tool.agent.model : '',
    tool.agent && tool.agent.tokens ? fmtTokens(tool.agent.tokens) + ' tokens' : '',
    typeof tool.content.exit_code === 'number' ? 'exit status ' + tool.content.exit_code : '',
    tool.content.cwd ? tool.content.cwd : '',
  ];
  return html`<details class="so-part so-tool" data-ds-key="${key}" data-open-key="${key}" data-kind="${part.kind}" data-state="${state}"${group ? html` data-group` : ''}${open ? html` open` : ''}><summary class="so-tool__head"><span class="so-tool__icon" aria-hidden="true">${raw(icon(toolGlyph(tool.name), 18))}</span><span class="so-tool__name">${tool.name}</span><span class="so-tool__summary so-ellipsis">${tool.summary}</span>${
    progress ? html`<span class="so-tool__progress so-ellipsis">${progress}</span>` : ''
  }${diff && files.length ? html`<span class="so-diff__count"><span class="so-diff__added">+${added}</span><span class="so-diff__removed">−${removed}</span></span>` : ''}${
    statusMark(state)
  }<span class="so-tool__time">${seconds(tool.duration)}</span>${chevron()}</summary>${
    open ? html`<div class="so-tool__body">${toolBody(part, tool, view, { diff, group })}${infoButton(facts, 'Call details')}</div>` : ''
  }</details>`;
}

function toolBody(part, tool, view, { diff, group }) {
  if (diff) return diffViewer(part, view);
  if (tool.workflow) return workflowBody(part, tool, view);
  const blocks = [];
  if (group) {
    if (tool.agent && tool.agent.prompt) blocks.push(html`<div class="so-tool__prompt so-md">${markdown(tool.agent.prompt)}</div>`);
    blocks.push(html`<div class="so-group">${part.children.map((child) => partHtml(child, view))}</div>`);
    const summary = (tool.agent && tool.agent.summary) || (part.state !== 'running' ? part.text : '');
    if (summary) blocks.push(html`<div class="so-tool__result so-md">${markdown(summary)}</div>`);
    return html`${blocks}`;
  }
  if (tool.command) blocks.push(codeBlock(tool.command, 'shell'));
  else if (tool.input && Object.keys(tool.input).length) blocks.push(html`<pre class="so-mono so-tool__input">${pretty(tool.input)}</pre>`);
  if (part.text) blocks.push(html`<pre class="so-mono so-tool__out" data-state="${part.state}">${part.text}</pre>`);
  else if (part.state === 'running') blocks.push(html`<p class="so-tool__wait ds-meta">No output yet.</p>`);
  if (part.children.length) blocks.push(html`<div class="so-group">${part.children.map((child) => partHtml(child, view))}</div>`);
  return html`${blocks}`;
}

const PHASE_WORDS = { done: 'Done', running: 'Running', pending: 'Waiting', error: 'Failed' };

// workflowBody is a workflow's phases, each with its agents and their live
// state, the way /workflows shows them.
function workflowBody(part, tool, view) {
  const phases = Array.isArray(tool.workflow.phases) ? tool.workflow.phases : [];
  const agents = part.children;
  const phaseOf = (child) => {
    const a = child.content && child.content.agent;
    return a && a.phase ? String(a.phase) : '';
  };
  const titles = phases.map((phase) => String(phase.title || phase.name || phase.id || ''));
  const rows = titles.map((title, i) => {
    const members = agents.filter((child) => phaseOf(child) === title);
    let state = 'pending';
    if (members.some((child) => child.state === 'running')) state = 'running';
    else if (members.length && members.every((child) => child.state === 'done')) state = 'done';
    else if (members.some((child) => child.state === 'error')) state = 'error';
    if (phases[i].state) state = phases[i].state;
    return html`<li class="so-phase" data-ds-key="${'ph' + i}" data-state="${state}"><div class="so-phase__head"><span class="so-phase__no">${i + 1}</span><span class="so-phase__title">${title}</span><span class="so-phase__state">${statusMark(state === 'pending' ? '' : state)}<span>${PHASE_WORDS[state] || ''}</span></span></div>${
      members.length ? html`<div class="so-group">${members.map((child) => partHtml(child, view))}</div>` : ''
    }</li>`;
  });
  const loose = agents.filter((child) => !titles.includes(phaseOf(child)));
  return html`<ol class="so-phases">${rows}</ol>${loose.length ? html`<div class="so-group">${loose.map((child) => partHtml(child, view))}</div>` : ''}${
    tool.workflow.summary ? html`<div class="so-tool__result so-md">${markdown(tool.workflow.summary)}</div>` : ''
  }`;
}

/* -------------------------------------------------------------- the plan */

// planAskOf is the ask a plan part waits on: the one its content names
// (Claude's and the scripted agent's plan.ask_id), else the plan ask that
// names the part.
function planAskOf(part, chat) {
  const c = part.content || {};
  const id = (c.plan && typeof c.plan === 'object' && c.plan.ask_id) || c.ask_id || '';
  if (id) return chat.asks.get(id) || null;
  for (const ask of chat.asks.values()) {
    if (ask.payload && ask.payload.kind === 'plan' && ask.payload.part_id === part.id) return ask;
  }
  return null;
}

const DECISION_WORDS = { approved: 'Approved', keep_planning: 'Sent back to planning', pending: 'Waiting for you' };

function planHtml(part, view) {
  const c = part.content || {};
  const meta = c.plan && typeof c.plan === 'object' ? c.plan : {};
  const text = part.text || (typeof c.plan === 'string' ? c.plan : '');
  const ask = planAskOf(part, view.chat);
  let decision = meta.decision || '';
  if (ask && ask.answered && (!decision || decision === 'pending')) {
    const answer = ask.answer || {};
    decision = answer.decision === 'approve' || answer.approved === true ? 'approved' : 'keep_planning';
  }
  const waiting = !!ask && !ask.answered;
  const noting = view.isOpen('note:' + part.id, false);
  return html`<section class="ds-card so-part so-plan" data-ds-key="${'p:' + part.id}" data-kind="plan" data-decision="${decision || (waiting ? 'pending' : '')}"><div class="ds-card__row"><h3 class="ds-card__title so-plan__title">${raw(icon('flag', 18))}<span>Plan</span></h3>${
    decision && DECISION_WORDS[decision] ? html`<span class="${decision === 'approved' ? 'ds-status ds-status--ok' : 'ds-status'}">${raw(icon(decision === 'approved' ? 'check' : 'pencil', 16))}<span>${DECISION_WORDS[decision]}</span></span>` : ''
  }</div><div class="so-plan__text so-md">${markdown(text)}</div>${
    meta.note ? html`<p class="ds-meta so-plan__note">${'Note: ' + meta.note}</p>` : ''
  }${waiting ? html`<div class="so-plan__answer">${
    noting ? html`<div class="ds-field so-plan__field"><label class="ds-field__label" for="${'note-' + part.id}">What should change?</label><textarea class="ds-input so-plan__input" id="${'note-' + part.id}" rows="3" data-plan-note="${ask.id}"></textarea></div>` : ''
  }<div class="ds-form-actions"><button class="ds-btn ds-btn--primary" type="button" data-action="plan-approve" data-ask="${ask.id}">Approve</button><button class="ds-btn ds-btn--tonal" type="button" data-action="${noting ? 'plan-send' : 'plan-note'}" data-ask="${ask.id}" data-part="${part.id}">${noting ? 'Send back' : 'Keep planning'}</button></div></div>` : ''}</section>`;
}

/* -------------------------------------------------------------- the asks */

/** askQuestions is an ask's questions, whatever shape its payload has. */
export function askQuestions(ask) {
  const payload = ask && ask.payload && typeof ask.payload === 'object' ? ask.payload : {};
  return Array.isArray(payload.questions) ? payload.questions : [];
}

/** askCard is a question in the chat: its title, how far it is, and the first open question. */
export function askCard(ask, view) {
  const payload = ask.payload || {};
  if (payload.kind === 'plan') {
    // The plan card answers it, when the plan part is there; on its own it
    // is a plan card of its own.
    for (const part of view.chat.parts.values()) {
      if (part.kind === 'plan' && planAskOf(part, view.chat) === ask) return '';
    }
    return planHtml({ id: 'ask-' + ask.id, kind: 'plan', text: payload.plan || '', content: { plan: { ask_id: ask.id } } }, view);
  }
  const questions = askQuestions(ask);
  const progress = view.askProgress(ask);
  const first = questions.find((q) => !progress.answered.has(q.id)) || questions[0];
  const title = payload.title || (questions.length === 1 ? 'A question' : 'Questions');
  let state = 'open';
  if (ask.answered) state = view.mine(ask) ? 'answered' : 'elsewhere';
  return html`<section class="ds-card so-ask" data-ds-key="${'a:' + ask.id}" data-ask="${ask.id}" data-state="${state}"><div class="ds-card__row"><h3 class="ds-card__title so-ask__title">${raw(icon('chat', 18))}<span>${title}</span></h3>${
    state === 'open' ? html`<span class="ds-status ds-status--warning">${raw(icon('clock', 16))}<span>Waiting for you</span></span>`
      : html`<span class="ds-status ds-status--ok">${raw(icon('check', 16))}<span>${state === 'elsewhere' ? 'Answered on another device' : 'Answered'}</span></span>`
  }</div>${
    state === 'open' ? html`<p class="so-ask__progress ds-meta">${questions.length > 1 ? progress.count + ' of ' + questions.length + ' answered' : 'One question'}</p>${
      first ? html`<p class="so-ask__question">${first.question || first.header || ''}</p>` : ''
    }<div class="ds-form-actions"><button class="ds-btn ds-btn--primary so-ask__open" type="button" data-action="ask-open" data-ask="${ask.id}">Answer</button></div>`
      : html`<p class="so-ask__done ds-meta">${answerSummary(ask)}</p>`
  }</section>`;
}

// answerSummary is an answered ask in one line: what was chosen or said.
function answerSummary(ask) {
  const answer = ask.answer;
  if (typeof answer === 'string') return answer;
  if (!answer || typeof answer !== 'object') return '';
  if (answer.follow_up) return 'Asked back: ' + answer.follow_up;
  const answers = answer.answers || {};
  const questions = askQuestions(ask);
  const words = questions.map((q) => {
    const a = answers[q.id];
    if (!a) return '';
    if (a.delegated) return (q.header || q.id) + ': you decide';
    if (a.skipped) return '';
    const labels = (a.selected || []).map((id) => {
      const option = (q.options || []).find((o) => String(o.id) === String(id));
      return option ? option.label : String(id);
    });
    if (a.other) labels.push(a.other);
    return labels.length ? (q.header || q.id) + ': ' + labels.join(', ') : '';
  }).filter(Boolean);
  return words.join(' · ');
}

/* ------------------------------------------------------------ the messages */

const USER_STATE = {
  local: { word: 'Not sent yet', glyph: 'wifiOff' },
  sending: { word: 'Sending', glyph: 'clock' },
  pending: { word: 'Sent', glyph: 'clock' },
  queued: { word: 'Queued', glyph: 'clock' },
  failed: { word: 'Not sent', glyph: 'alertCircle' },
};

function attachmentsHtml(list) {
  if (!list || !list.length) return '';
  return html`<ul class="so-msg__files">${list.map((file, i) => html`<li class="so-attachment" data-ds-key="${'f' + i}">${raw(icon(String(file.mime || '').startsWith('image/') ? 'image' : 'file', 16))}<span class="so-ellipsis">${file.name}</span></li>`)}</ul>`;
}

/**
 * userMessage is what the person sent: the text as written, the files, and -
 * while it is not the agent's yet - where it is.
 */
export function userMessage({ key, text, attachments, state, answerTo, error }) {
  const chip = USER_STATE[state];
  return html`<article class="so-msg" data-ds-key="${key}" data-role="user" data-state="${state || 'delivered'}"><div class="so-msg__bubble">${
    answerTo ? html`<p class="so-msg__label ds-meta">Answer to a question</p>` : ''
  }${text ? html`<p class="so-msg__text">${text}</p>` : ''}${attachmentsHtml(attachments)}</div>${
    chip ? html`<span class="${state === 'failed' ? 'ds-status ds-status--error so-msg__chip' : 'ds-status so-msg__chip'}" data-chip="${state}">${raw(icon(chip.glyph, 16))}<span>${chip.word}</span></span>` : ''
  }${error ? html`<p class="so-msg__error ds-meta">${error}</p>` : ''}</article>`;
}

/** turnFacts is what a turn cost, for the "i" under the answer. */
export function turnFacts(message) {
  const turn = message.turn;
  if (!turn) return [];
  const u = turn.usage || {};
  const facts = [];
  if (message.model) facts.push('Model ' + message.model);
  if (u.input || u.output) facts.push(fmtTokens(u.input || 0) + ' in · ' + fmtTokens(u.output || 0) + ' out');
  if (u.cache_read || u.cache_write) facts.push('cache ' + fmtTokens(u.cache_read || 0) + ' read · ' + fmtTokens(u.cache_write || 0) + ' written');
  if (u.reasoning) facts.push(fmtTokens(u.reasoning) + ' reasoning');
  if (u.context && u.window) facts.push('context ' + fmtTokens(u.context) + ' of ' + fmtTokens(u.window));
  if (turn.cost) facts.push('$' + turn.cost.toFixed(4));
  if (turn.endedAt && turn.startedAt) facts.push(seconds(turn.endedAt - turn.startedAt));
  if (turn.outcome && turn.outcome !== 'completed') facts.push('outcome ' + turn.outcome);
  return facts;
}

/** assistantMessage is an answer: its parts in order, and what it cost. */
export function assistantMessage(message, view) {
  const facts = turnFacts(message);
  // An answer that has said nothing yet is working - unless it waits for the
  // person, which its question card says.
  const asking = [...view.chat.asks.values()].some((ask) => !ask.answered && ask.turnId && ask.turnId === message.turnId);
  const working = message.state === 'streaming' && !message.parts.length && !asking;
  return html`<article class="so-msg" data-ds-key="${'m:' + message.id}" data-role="assistant" data-state="${message.state}">${
    message.parts.map((part) => partHtml(part, view))
  }${working ? html`<p class="so-msg__wait ds-meta"><span class="ds-spinner" aria-hidden="true"></span><span>Working…</span></p>` : ''}${
    message.state === 'interrupted' || facts.length ? html`<div class="so-msg__foot">${
      message.state === 'interrupted' ? html`<span class="ds-status">${raw(icon('ban', 16))}<span>Interrupted</span></span>` : ''
    }${infoButton(facts, 'Usage of this answer')}</div>` : ''
  }</article>`;
}

/* --------------------------------------------------------------- the lines */

// What a notice says when the event brings no words of its own, by its code
// (AGENT-HOST.md §2 and §5: the host's, every adapter's rebuild, the
// adapters' own). A code nobody wrote words for is said in general terms,
// the code itself behind the "i".
const NOTICE_WORDS = {
  resumed: 'Resumed after a restart.',
  compacted: 'The conversation was compacted.',
  conversation_lost: 'The agent could not resume the conversation. It starts fresh; the history here is kept.',
  answer_as_message: 'The answer went to the agent as a message.',
  rebuilt: 'The history was rebuilt from the agent’s own record.',
  history_unavailable: 'The agent’s own record of this conversation could not be read.',
  api_retry: 'The agent’s service did not answer; it tries again.',
  retrying: 'The agent’s service did not answer; it tries again.',
  retry: 'The agent’s service did not answer; it tries again.',
  plan_mode: 'Plan mode changed.',
  review_started: 'A review started.',
  codex_reasoning_dropped: 'The agent’s earlier reasoning was left behind to go on with another account.',
  codex_handoff: 'The conversation went on in a new thread, with the transcript handed over.',
  elicitation_declined: 'A request of a tool was declined.',
  question_declined: 'A question the agent asked its own way was declined.',
  question_rejected: 'A question the agent asked its own way was declined.',
  permission_allowed: 'A permission the agent asked for was allowed.',
  reverted: 'The conversation was rewound.',
  unreverted: 'The rewind was undone.',
  forked: 'The conversation was forked.',
  agent: 'The agent’s mode changed.',
};

// The glyph a line between messages leads with, by its notice code.
const NOTICE_GLYPH = {
  compacted: 'archive', resumed: 'history', rebuilt: 'history', conversation_lost: 'alert', history_unavailable: 'alert',
  api_retry: 'sync', retrying: 'sync', retry: 'sync',
};

/** renewsIn is a reset moment as "2 h 10 min", from now. */
export function renewsIn(resetsAt, now = Date.now()) {
  const ms = Number(resetsAt) * 1000 - now;
  if (!(ms > 0)) return '';
  const minutes = Math.ceil(ms / 60000);
  if (minutes < 60) return minutes + ' min';
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h + ' h' + (m ? ' ' + m + ' min' : '');
}

/** lineHtml is one line between messages. */
export function lineHtml(entry, view) {
  const l = entry.line;
  const key = entry.key;
  switch (l.kind) {
    case 'notice': {
      const words = l.text || NOTICE_WORDS[l.code] || 'A notice from the agent.';
      return html`<div class="so-line" data-ds-key="${key}" data-kind="notice" data-code="${l.code}">${raw(icon(NOTICE_GLYPH[l.code] || 'info', 16))}<span class="so-line__text">${words}</span>${
        !l.text && !NOTICE_WORDS[l.code] && l.code ? infoButton(['code ' + l.code], 'Notice details') : ''
      }</div>`;
    }
    case 'command':
      return html`<div class="so-line" data-ds-key="${key}" data-kind="command">${raw(icon('chevronRight', 16))}<span class="so-line__text so-line__command">${l.text}</span></div>`;
    case 'account':
      return html`<div class="so-line" data-ds-key="${key}" data-kind="account">${raw(icon('user', 16))}<span class="so-line__text">${'Switched from ' + (l.from || 'one account') + ' to ' + (l.to || 'another')}</span>${infoButton([l.reason], 'Why the account changed')}</div>`;
    case 'model':
      return html`<div class="so-line" data-ds-key="${key}" data-kind="model">${raw(icon('cpu', 16))}<span class="so-line__text">${'Model: ' + view.modelLabel(l.model) + (l.effort ? ' · ' + view.effortLabel(l.effort) : '')}</span></div>`;
    case 'error':
      return html`<div class="ds-alert ds-alert--error so-line-alert" data-ds-key="${key}" data-kind="error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${l.text || 'Something went wrong.'}</span>${infoButton([l.code ? 'code ' + l.code : ''], 'Error details')}</div>`;
    case 'turn': {
      if (l.outcome === 'limited') {
        const renew = view.renewal();
        return html`<div class="ds-alert ds-alert--warning so-line-alert" data-ds-key="${key}" data-kind="limit" role="status">${raw(icon('clock', 20))}<span class="ds-alert__text">${'Limit reached' + (renew ? ' — renews in ' + renew : '')}</span>${infoButton([l.text], 'About the limit')}</div>`;
      }
      if (l.outcome === 'failed') {
        return html`<div class="ds-alert ds-alert--error so-line-alert" data-ds-key="${key}" data-kind="failed" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${'The turn failed.' + (l.text ? ' ' + l.text : '')}</span></div>`;
      }
      return html`<div class="so-line" data-ds-key="${key}" data-kind="interrupted">${raw(icon('ban', 16))}<span class="so-line__text">Interrupted</span></div>`;
    }
    default:
      return '';
  }
}
