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

import { html, raw, icon, faces } from './ui.js';
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

/**
 * seconds is a duration the way the rows say it: 0.4 s, 12 s, 3 min 5 s,
 * each number held to its unit by a no-break space.
 */
export function seconds(ms) {
  const value = Number(ms) || 0;
  // Less than a tenth of a second is not worth a number.
  if (value < 100) return '';
  if (value < 10000) return (value / 1000).toFixed(1).replace(/\.0$/, '') + '\u00a0s';
  const s = Math.round(value / 1000);
  if (s < 60) return s + '\u00a0s';
  return Math.floor(s / 60) + '\u00a0min' + (s % 60 ? ' ' + (s % 60) + '\u00a0s' : '');
}

/** infoButton is the "i" of infoTip() as markup; the view opens it on a tap. */
export function infoButton(lines, label = 'Details') {
  const shown = lines.filter((line) => line !== null && line !== undefined && line !== ''
    && !(Array.isArray(line) && (line[1] === null || line[1] === undefined || line[1] === '')));
  if (!shown.length) return '';
  // A fact with a name ([label, value]) is a row of the dialog the "i"
  // opens, as Info's are (chatview.js), and "Label: value" in its tip.
  const named = shown.some((line) => Array.isArray(line));
  return html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info" type="button" data-action="info" aria-label="${label}" data-tip="${shown.map(factLine).join('\n')}"${named ? html` data-facts="${JSON.stringify(shown)}"` : ''}>${raw(icon('info', 18))}</button>`;
}

/** factLine is one fact as a line of text: "Label: value", or the line itself. */
function factLine(fact) {
  return Array.isArray(fact) ? fact[0] + ': ' + fact[1] : String(fact);
}

function chevron() {
  return raw(icon('chevronDown', 20, 'ds-chevron'));
}

function pretty(value) {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

/**
 * outboxState is where an item of the Outbox is, in the words the page uses
 * for it: refused for good ("failed"), written but not out of this device
 * yet ("local": offline, or tried and not through), or on its way.
 */
export function outboxState(item) {
  if (item.state === 'failed') return 'failed';
  if (navigator.onLine === false || (item.attempts > 0 && item.error)) return 'local';
  return 'sending';
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
  // A workflow's agent is one of its agents, under its label.
  else if (c.tool === 'WorkflowAgent') name = 'Agent';
  const agent = c.agent && typeof c.agent === 'object' ? c.agent : null;
  const summary = c.summary || c.command || field('command') || c.path || field('file_path') || field('pattern')
    || field('description') || field('url') || field('query') || (agent && (agent.label || agent.description))
    || (c.workflow && (c.workflow.description || c.workflow.name)) || c.name || '';
  // The file a call is about, when it is the call's summary: a Read, an
  // Edit, a Write, a NotebookEdit.
  const path = c.path || field('file_path') || field('notebook_path') || '';
  let duration = Number(c.duration_ms) || 0;
  if (!duration && part.state !== 'running' && part.state !== 'streaming' && part.updatedAt > part.at) {
    duration = part.updatedAt - part.at;
  }
  const exit = c.exit_code;
  const failed = part.state === 'error' || c.is_error === true || (typeof exit === 'number' && exit !== 0);
  return { name, summary: String(summary).split('\n')[0], path: path && summary === path ? String(path).split('\n')[0] : '',
    input, duration, failed, agent, workflow: c.workflow || null, command: c.command || field('command'), content: c };
}

// isAskTool says whether a tool part is a call of Socrates' own question
// tool, ask_user, as each agent names it (mcp__socrates__ask_user,
// socrates_ask_user, ask_user): the question card in the chat is that call,
// so its row is not drawn beside it.
export function isAskTool(part) {
  const c = (part.content && typeof part.content === 'object') ? part.content : {};
  return [c.tool, c.name].some((name) => /(^|[_.:/])ask_user$/.test(String(name || '')));
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

// withPath gives the files of a diff the path the tool named back where the
// diff's own header lost it: a diff of an absolute path names it git's way,
// "a/srv/x.go", which reads as "srv/x.go" once the a/ is taken off.
function withPath(files, path) {
  const bare = String(path || '').replace(/^\/+/, '');
  for (const one of files) if (bare && bare !== path && one.path === bare) one.path = path;
  return files;
}

/** diffFiles is every file a diff part changes, from whichever shape it has. */
export function diffFiles(part) {
  const c = (part.content && typeof part.content === 'object') ? part.content : {};
  if (Array.isArray(c.changes)) {
    return c.changes.flatMap((change) => {
      const files = withPath(parseDiff(change.diff || '', change.move_path || change.path || ''), change.move_path || change.path);
      for (const one of files) {
        if (!one.path) one.path = change.path || '';
        if (change.kind === 'add') one.status = 'added';
        if (change.kind === 'delete') one.status = 'deleted';
      }
      return files;
    });
  }
  if (Array.isArray(c.files)) return c.files.flatMap((one) => withPath(parseDiff(one.diff || '', one.path || ''), one.path));
  if (typeof c.diff === 'string' && c.diff) return withPath(parseDiff(c.diff, c.path || ''), c.path);
  if (/^(---|diff --git|@@ )/m.test(part.text || '')) return withPath(parseDiff(part.text, c.path || ''), c.path);
  return [];
}

const SIGN = { add: '+', del: '−', ctx: ' ', meta: '' };
const LINE_WORDS = { add: 'Added', del: 'Removed' };

function diffFileHtml(file, key, open, view) {
  const name = view.where(file.path) || 'the file';
  const status = file.status === 'added' ? 'New file' : file.status === 'deleted' ? 'Deleted' : '';
  return html`<details class="so-diff__file" data-ds-key="${key}" data-open-key="${key}"${open ? html` open` : ''}><summary class="so-diff__head"><span class="so-diff__path so-break">${name}</span>${status ? html`<span class="so-diff__status">${status}</span>` : ''}<span class="so-diff__count"><span class="so-diff__added" aria-label="${file.added + ' added'}">+${file.added}</span><span class="so-diff__removed" aria-label="${file.removed + ' removed'}">−${file.removed}</span></span>${chevron()}</summary>${open ? diffBodyHtml(file, name) : ''}</details>`;
}

function diffBodyHtml(file, name) {
  return html`<div class="so-diff__body" role="table" aria-label="${'Changes to ' + name}">${file.hunks.map((hunk) => html`<div class="so-diff__hunk" role="row"><span class="so-diff__hunk-head" role="cell">${hunk.header}</span></div>${
    hunk.lines.map((line) => html`<div class="so-diff__line" data-type="${line.type}" role="row"><span class="so-diff__no" role="cell" aria-hidden="true">${line.oldNo ?? ''}</span><span class="so-diff__no" role="cell" aria-hidden="true">${line.newNo ?? ''}</span><span class="so-diff__sign" role="cell"${LINE_WORDS[line.type] ? html` aria-label="${LINE_WORDS[line.type]}"` : ''}>${SIGN[line.type]}</span><span class="so-diff__code" role="cell">${line.text}</span></div>`)
  }`)}</div>`;
}

// singleFileHtml is the one file of a diff: its lines under its name, open
// with the card - a second collapsible inside it would be a second tap.
// The head holds the call's "i" (`info`), which would cost the lines a
// column of their own beside them.
function singleFileHtml(file, key, info, view) {
  const name = view.where(file.path) || 'the file';
  const status = file.status === 'added' ? 'New file' : file.status === 'deleted' ? 'Deleted' : '';
  return html`<div class="so-diff__file" data-ds-key="${key}" data-single><div class="so-diff__head"><span class="so-diff__path so-break">${name}</span>${status ? html`<span class="so-diff__status">${status}</span>` : ''}${info || ''}</div>${diffBodyHtml(file, name)}</div>`;
}

/**
 * diffViewer is every file of a diff: one is shown as it is (with `info`,
 * the call's "i", in its head), several each collapsible.
 */
export function diffViewer(part, view, info = '') {
  const files = diffFiles(part);
  if (!files.length) return part.text ? html`<pre class="so-mono">${part.text}</pre>` : '';
  if (files.length === 1) return html`<div class="so-diff">${singleFileHtml(files[0], 'f:' + part.id + ':0', info, view)}</div>`;
  return html`<div class="so-diff">${files.map((file, i) => {
    const key = 'f:' + part.id + ':' + i;
    return diffFileHtml(file, key, view.isOpen(key, files.length <= 3), view);
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
      return html`<div class="so-part so-md" data-ds-key="${'p:' + part.id}" data-kind="text" data-state="${part.state}">${markdown(part.text, { keep: part.state !== 'streaming' })}</div>`;
    case 'reasoning':
      return reasoningHtml(part, view);
    case 'tool':
      return isAskTool(part) ? '' : toolHtml(part, view);
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
    open ? html`<div class="so-reasoning__body so-md">${markdown(part.text || (c.redacted ? 'The reasoning is not shown.' : ''), { keep: part.state !== 'streaming' })}</div>` : ''
  }</details>`;
}

function fileHtml(part) {
  const c = part.content || {};
  const name = c.name || String(c.path || '').split('/').pop() || 'file';
  return html`<div class="so-part so-attachment" data-ds-key="${'p:' + part.id}" data-kind="${part.kind}">${raw(icon(part.kind === 'image' ? 'image' : 'file', 18))}<span class="so-ellipsis">${name}</span>${infoButton([c.path, c.mime], 'File details')}</div>`;
}

// summaryHtml is the call's one line. A file the call worked on is named as
// the chat's folder sees it, file first and then its folder -
// "handler.go · server" - because the end of the line is what a narrow
// screen cuts, and it cut the file name: at 360 an edit read
// "/srv/app/server/handl…". The whole path is its tip.
function summaryHtml(tool, view) {
  const path = tool.path ? view.where(tool.path).replace(/\/+$/, '') : '';
  if (!path) return html`<span class="so-tool__summary so-ellipsis">${tool.summary}</span>`;
  const cut = path.lastIndexOf('/');
  const words = cut > 0 ? path.slice(cut + 1) + ' · ' + path.slice(0, cut) : path;
  return html`<span class="so-tool__summary so-ellipsis"${words !== tool.path ? html` data-tip="${tool.path}"` : ''}>${words}</span>`;
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
    tool.path && view.where(tool.path) !== tool.path ? tool.path : '',
  ];
  return html`<details class="so-part so-tool" data-ds-key="${key}" data-open-key="${key}" data-kind="${part.kind}" data-state="${state}"${group ? html` data-group` : ''}${open ? html` open` : ''}><summary class="so-tool__head"><span class="so-tool__icon" aria-hidden="true">${raw(icon(toolGlyph(tool.name), 18))}</span><span class="so-tool__name">${tool.name}</span>${summaryHtml(tool, view)}${
    progress ? html`<span class="so-tool__progress so-ellipsis">${progress}</span>` : ''
  }${diff && files.length ? html`<span class="so-diff__count"><span class="so-diff__added">+${added}</span><span class="so-diff__removed">−${removed}</span></span>` : ''}${
    statusMark(state)
  }<span class="so-tool__time">${seconds(tool.duration)}</span>${chevron()}</summary>${
    open ? html`<div class="so-tool__body">${bodyWithInfo(part, tool, view, { diff, group, files, info: infoButton(facts, 'Call details') })}</div>` : ''
  }</details>`;
}

// bodyWithInfo places the call's "i" where it costs no row of its own: in a
// single file's head, beside what a call was given or what an agent was
// told; under a workflow's phases or several files, which want the whole
// width, it closes the body in a line no taller than a line of meta.
function bodyWithInfo(part, tool, view, { diff, group, files, info }) {
  if (diff && files.length === 1) return diffViewer(part, view, info);
  const body = toolBody(part, tool, view, { diff, group });
  if (diff || tool.workflow) return html`${body}${info}`;
  return html`${info}${body}`;
}

function toolBody(part, tool, view, { diff, group }) {
  if (diff) return diffViewer(part, view);
  if (tool.workflow) return workflowBody(part, tool, view);
  const blocks = [];
  if (group) {
    if (tool.agent && tool.agent.prompt) blocks.push(html`<div class="so-tool__prompt so-md">${markdown(tool.agent.prompt)}</div>`);
    if (part.children.length) blocks.push(html`<div class="so-group">${part.children.map((child) => partHtml(child, view))}</div>`);
    const summary = (tool.agent && tool.agent.summary) || (part.state !== 'running' ? part.text : '');
    if (summary) blocks.push(html`<div class="so-tool__result so-md">${markdown(summary)}</div>`);
    return html`${blocks}`;
  }
  if (tool.command) blocks.push(codeBlock(tool.command, 'shell'));
  else if (tool.input && Object.keys(tool.input).length) blocks.push(html`<pre class="so-mono">${pretty(tool.input)}</pre>`);
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
const DECISION_TONE = { approved: 'ds-status ds-status--ok', keep_planning: 'ds-status', pending: 'ds-status ds-status--warning' };
const DECISION_GLYPH = { approved: 'check', keep_planning: 'pencil', pending: 'clock' };

// What an answer of this browser that the server does not have yet says on
// its card, by outboxState().
const ANSWER_ON_ITS_WAY = {
  local: { word: 'Not sent yet', glyph: 'wifiOff', tone: 'ds-status' },
  sending: { word: 'Sending', glyph: 'clock', tone: 'ds-status' },
  failed: { word: 'Not sent', glyph: 'alertCircle', tone: 'ds-status ds-status--error' },
};

function onItsWayChip(state) {
  const chip = ANSWER_ON_ITS_WAY[state];
  return html`<span class="${chip.tone}" data-chip="${state}">${raw(icon(chip.glyph, 16))}<span>${chip.word}</span></span>`;
}

// refusedHtml is why the server would not take an answer, and what can be
// done about it: send it again as it is, or answer afresh.
function refusedHtml(item, again) {
  return html`<div class="ds-alert ds-alert--error so-refused" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${'Your answer was not taken: ' + (item.error || 'the server refused it.')}</span></div><div class="ds-form-actions"><button class="ds-btn ds-btn--tonal ds-btn--icon" type="button" data-action="outbox-retry" data-id="${item.id}" aria-label="Send the answer again" data-tip="Send again">${raw(icon('sync', 20))}</button>${again}</div>`;
}

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
  // An answer this browser gave that the server does not have yet holds
  // the buttons - a second tap would be a second answer - until it lands,
  // and gives them back if it was refused.
  const pending = waiting ? view.pendingAnswer(ask) : null;
  const sending = pending ? outboxState(pending) : '';
  const asked = pending && pending.payload.answer ? pending.payload.answer : {};
  const noting = view.isOpen('note:' + part.id, false) && !sending;
  let answer = '';
  if (waiting && sending && sending !== 'failed') {
    answer = html`<div class="so-plan__answer"><div class="ds-card__row">${onItsWayChip(sending)}<span class="ds-meta">${asked.decision === 'approve' ? 'Approve' : 'Keep planning' + (asked.note ? ': ' + asked.note : '')}</span></div></div>`;
  } else if (waiting) {
    answer = html`<div class="so-plan__answer">${sending === 'failed' ? refusedHtml(pending, '') : ''}${
      noting ? html`<div class="ds-field so-plan__field"><label class="ds-field__label" for="${'note-' + part.id}">What should change?</label><div class="so-field-row so-field-row--top"><textarea class="ds-input so-plan__input" id="${'note-' + part.id}" rows="3" enterkeyhint="enter" data-plan-note="${ask.id}"></textarea><button class="ds-btn ds-btn--quiet ds-btn--icon so-mic" type="button" data-action="plan-dictate" data-target="${'note-' + part.id}" aria-label="Dictate what should change" data-tip="Dictate">${faces(raw(icon('mic', 20)), '')}</button></div></div>` : ''
    }<div class="ds-form-actions"><button class="ds-btn ds-btn--primary" type="button" data-action="plan-approve" data-ask="${ask.id}">Approve</button><button class="ds-btn ds-btn--tonal" type="button" data-action="${noting ? 'plan-send' : 'plan-note'}" data-ask="${ask.id}" data-part="${part.id}">${noting ? 'Send back' : 'Keep planning'}</button></div></div>`;
  }
  return html`<section class="ds-card so-part so-plan" data-ds-key="${'p:' + part.id}" data-kind="plan" data-decision="${decision || (waiting ? 'pending' : '')}"${sending ? html` data-sending="${sending}"` : ''}><div class="ds-card__row"><h3 class="ds-card__title so-plan__title">${raw(icon('flag', 18))}<span>Plan</span></h3>${
    decision && DECISION_WORDS[decision] ? html`<span class="${DECISION_TONE[decision]}">${raw(icon(DECISION_GLYPH[decision], 16))}<span>${DECISION_WORDS[decision]}</span></span>` : ''
  }</div><div class="so-md">${markdown(text)}</div>${
    meta.note ? html`<p class="ds-meta so-plan__note">${'Note: ' + meta.note}</p>` : ''
  }${answer}</section>`;
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
  const title = payload.title || (questions.length === 1 ? 'A question' : 'Questions');
  // An answer this browser gave that the server does not have yet: the
  // card says where it is, and what it says, rather than asking again.
  const pending = ask.answered ? null : view.pendingAnswer(ask);
  let state = 'open';
  if (ask.answered) state = view.mine(ask) ? 'answered' : 'elsewhere';
  else if (pending) state = outboxState(pending);
  else if (ask.closed) state = 'closed';
  const head = html`<div class="ds-card__row"><h3 class="ds-card__title so-ask__title">${raw(icon('chat', 18))}<span>${title}</span></h3>${
    state === 'open' ? html`<span class="ds-status ds-status--warning">${raw(icon('clock', 16))}<span>Waiting for you</span></span>`
      : state === 'closed' ? html`<span class="ds-status">${raw(icon('chat', 16))}<span>Not answered</span></span>`
      : ANSWER_ON_ITS_WAY[state] ? onItsWayChip(state)
        : html`<span class="ds-status ds-status--ok">${raw(icon('check', 16))}<span>${state === 'elsewhere' ? 'Answered on another device' : 'Answered'}</span></span>`
  }</div>`;
  let body;
  if (state === 'open') {
    const progress = view.askProgress(ask);
    const first = questions.find((q) => !progress.answered.has(q.id)) || questions[0];
    body = html`<p class="so-ask__progress ds-meta">${questions.length > 1 ? progress.count + ' of ' + questions.length + ' answered' : 'One question'}</p>${
      first ? html`<p class="so-ask__question">${first.question || first.header || ''}</p>` : ''
    }<div class="ds-form-actions"><button class="ds-btn ds-btn--primary so-ask__open" type="button" data-action="ask-open" data-ask="${ask.id}">Answer</button></div>`;
  } else if (pending) {
    const summary = answerSummary({ ...ask, answer: pending.payload.answer });
    body = html`${summary ? html`<p class="so-ask__done ds-meta">${summary}</p>` : ''}${
      state === 'failed' ? refusedHtml(pending, html`<button class="ds-btn" type="button" data-action="ask-again" data-ask="${ask.id}" data-id="${pending.id}">Answer again</button>`)
        : state === 'local' ? html`<p class="so-ask__progress ds-meta">It goes out when the connection is back.</p>` : ''
    }`;
  } else if (state === 'closed') {
    // A message moved the chat on; an answer can still be sent, as a message.
    body = html`<p class="so-ask__done ds-meta">You wrote something else instead. The agent no longer waits for this.</p><div class="ds-form-actions"><button class="ds-btn ds-btn--tonal so-ask__open" type="button" data-action="ask-open" data-ask="${ask.id}">Answer anyway</button></div>`;
  } else {
    body = html`<p class="so-ask__done ds-meta">${answerSummary(ask)}</p>`;
  }
  return html`<section class="ds-card so-ask" data-ds-key="${'a:' + ask.id}" data-ask="${ask.id}" data-state="${state}">${head}${body}</section>`;
}

// answerSummary is an answered ask in one line: what was chosen or said.
function answerSummary(ask) {
  const answer = ask.answer;
  if (typeof answer === 'string') return answer;
  if (!answer || typeof answer !== 'object') return '';
  if (answer.follow_up) return 'Asked back: ' + answer.follow_up;
  const answers = answer.answers || {};
  const questions = askQuestions(ask);
  // A header that is the question itself ("What is it called?") loses its
  // own mark before the colon: "What is it called: Atlas", not "?: Atlas".
  const named = (q) => String(q.header || q.id).trim().replace(/\s*[?:？：]+$/, '');
  const words = questions.map((q) => {
    const a = answers[q.id];
    if (!a) return '';
    if (a.delegated) return named(q) + ': you decide';
    if (a.skipped) return '';
    const labels = (a.selected || []).map((id) => {
      const option = (q.options || []).find((o) => String(o.id) === String(id));
      return option ? option.label : String(id);
    });
    if (a.other) labels.push(a.other);
    return labels.length ? named(q) + ': ' + labels.join(', ') : '';
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
  // The agent dropped it unread (a turn that ended before taking it up).
  cancelled: { word: 'Not taken up', glyph: 'ban' },
};

function attachmentsHtml(list) {
  if (!list || !list.length) return '';
  return html`<ul class="so-msg__files">${list.map((file, i) => html`<li class="so-attachment" data-ds-key="${'f' + i}">${raw(icon(String(file.mime || '').startsWith('image/') ? 'image' : 'file', 16))}<span class="so-break">${file.name}</span></li>`)}</ul>`;
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

// How a turn that did not complete ended, for its facts.
const ENDED_WORDS = {
  interrupted: 'Interrupted', failed: 'Failed', limited: 'At a usage limit', refused: 'Refused',
};

/**
 * turnFacts is what a turn cost, for the "i" under the answer, as named
 * facts ([label, value], the rows Info has): the model by the name the
 * composer gives it (view.modelLabel), not the id the journal holds; the
 * tokens; the context; the cost; how long it took, when that is worth a
 * number. `outcome` false leaves out how it ended, for the line that says so.
 */
export function turnFacts(message, view = null, { outcome = true } = {}) {
  const turn = message.turn;
  if (!turn) return [];
  const u = turn.usage || {};
  const facts = [];
  if (message.model) facts.push(['Model', view && view.modelLabel ? view.modelLabel(message.model) : message.model]);
  if (u.input || u.output) facts.push(['Tokens', fmtTokens(u.input || 0) + ' in · ' + fmtTokens(u.output || 0) + ' out']);
  if (u.cache_read || u.cache_write) facts.push(['Cache', fmtTokens(u.cache_read || 0) + ' read · ' + fmtTokens(u.cache_write || 0) + ' written']);
  if (u.reasoning) facts.push(['Reasoning', fmtTokens(u.reasoning) + ' tokens']);
  if (u.context && u.window) facts.push(['Context', fmtTokens(u.context) + ' of ' + fmtTokens(u.window)]);
  if (turn.cost) facts.push(['Cost', '$' + turn.cost.toFixed(4)]);
  if (turn.endedAt && turn.startedAt) facts.push(['Time', seconds(turn.endedAt - turn.startedAt)]);
  if (outcome && turn.outcome && turn.outcome !== 'completed') facts.push(['Ended', ENDED_WORDS[turn.outcome] || turn.outcome]);
  return facts;
}

// shownParts is what an answer shows (of `list`, a piece of its parts): its
// parts, less a text that has no words (an agent opens one before it knows
// whether it will say anything), the ask_user call its question card stands
// for, and the agent's own notice of a limit or failure that the line after
// it says (lineHtml).
function shownParts(message, list = message.parts) {
  const ended = message.turn && (message.turn.outcome === 'limited' || message.turn.outcome === 'failed')
    ? String(message.turn.error || '').trim() : '';
  return list.filter((part) => !(part.kind === 'text' && !String(part.text || '').trim())
    && !(part.kind === 'tool' && isAskTool(part))
    && !(ended && part.kind === 'notice' && String(part.text || '').trim() === ended));
}

// silentTurn is a turn whose answer showed nothing - a limit, a refusal, a
// Stop before a word - so that the line saying how it ended carries what it
// cost behind its own "i", rather than a row of its own with only an "i".
function silentTurnFacts(turn, view) {
  if (!turn || !view || !view.chat) return [];
  for (const message of view.chat.messages.values()) {
    if (message.role !== 'assistant' || !(message.turn === turn || (turn.id && message.turnId === turn.id))) continue;
    return shownParts(message).length ? [] : turnFacts(message, view, { outcome: false });
  }
  return [];
}

// laterInTurn is whether another answer of the same turn comes after this
// one in the chat.
function laterInTurn(message, view) {
  if (!view || !view.chat || !message.turnId) return false;
  let after = false;
  for (const one of view.chat.messages.values()) {
    if (one === message) { after = true; continue; }
    if (after && one.role === 'assistant' && one.turnId === message.turnId) return true;
  }
  return false;
}

// Where the questions are drawn, by the chat's revision: a part or a
// question that came changes it.
const placed = new WeakMap();

/**
 * askPlaces is where each question that names its tool part is drawn: right
 * after the part of its answer that holds the call - the hidden ask_user
 * call itself, or the subagent or workflow it was made in - rather than at
 * the end of the turn, where whatever the agent did after the answer went
 * above it. A plan is its plan part's (planHtml), and a question whose part
 * the chat does not have stays an entry of its own (chatfold.js cutAtAsk).
 * It answers { byPart: top part id -> asks, byMessage: message id -> asks,
 * ids: the asks so placed }.
 */
export function askPlaces(chat) {
  const hit = placed.get(chat);
  if (hit && hit.rev === chat.rev) return hit.places;
  const places = { byPart: new Map(), byMessage: new Map(), ids: new Set() };
  const asks = [...chat.asks.values()].sort((a, b) => a.seq - b.seq);
  for (const ask of asks) {
    const payload = ask.payload && typeof ask.payload === 'object' ? ask.payload : {};
    if (payload.kind === 'plan' || !payload.part_id) continue;
    let top = chat.parts.get(payload.part_id);
    const seen = new Set();
    while (top && top.parentId && !seen.has(top.id)) {
      seen.add(top.id);
      const parent = chat.parts.get(top.parentId);
      if (!parent) break;
      top = parent;
    }
    const message = top ? chat.messages.get(top.messageId) : null;
    if (!message || !message.parts.includes(top)) continue;
    for (const [map, key] of [[places.byPart, top.id], [places.byMessage, message.id]]) {
      if (!map.has(key)) map.set(key, []);
      map.get(key).push(ask);
    }
    places.ids.add(ask.id);
  }
  placed.set(chat, { rev: chat.rev, places });
  return places;
}

// pieces is an answer cut where questions were asked (chatfold.js cutAtAsk):
// for each piece, its parts, those it shows, and the question cards drawn
// at them.
function pieces(message, chat) {
  const places = askPlaces(chat);
  const cuts = message.cuts || [];
  const out = [];
  let from = 0;
  for (let i = 0; i <= cuts.length; i += 1) {
    const to = i < cuts.length ? Math.max(from, cuts[i]) : message.parts.length;
    const list = message.parts.slice(from, to);
    const shown = new Set(shownParts(message, list));
    let cards = 0;
    // Whether nothing was said after the last question of the piece.
    let quiet = !shown.size;
    for (const part of list) {
      if (shown.has(part)) quiet = false;
      const asks = places.byPart.get(part.id);
      if (asks) {
        cards += asks.length;
        quiet = true;
      }
    }
    out.push({ list, shown, cards, said: shown.size + cards, quiet });
    from = to;
  }
  return out;
}

/**
 * assistantMessage is an answer - or the piece of it after a question, the
 * segment-th (chatfold.js cutAtAsk): its parts in order, the question cards
 * where they were asked, Copy for its text, and what it cost behind the "i",
 * under the last piece that says anything. How a turn ended other than done
 * is the line after it (lineHtml), said once: not again on the answer, and
 * not by the agent's own notice of the same words (Claude's "usage limit
 * reached").
 */
export function assistantMessage(message, view, segment = 0) {
  const all = pieces(message, view.chat);
  const piece = all[segment];
  if (!piece) return '';
  const last = all.length - 1;
  // The foot goes under the last piece that says anything, else the last.
  let foot = last;
  for (let i = last; i >= 0; i -= 1) {
    if (all[i].said) {
      foot = i;
      break;
    }
  }
  // An answer that showed nothing of its own - a limit, a refusal, a Stop
  // before a word - leaves what it cost to the line of how its turn ended
  // (silentTurnFacts).
  const ended = message.turn && message.turn.outcome && message.turn.outcome !== 'completed';
  const silent = ended && !all.some((one) => one.shown.size);
  // What a turn cost is the turn's, so it is said once, under its last
  // answer: OpenCode answers a step at a time, and a tool-only step drew a
  // row of its own with an "i" that said the next one's usage again.
  const facts = segment === foot && !silent && !laterInTurn(message, view) ? turnFacts(message, view) : [];
  // An answer that has said nothing yet, or nothing since its last
  // question, is working - unless it waits for the person, which its
  // question card says.
  const asking = [...view.chat.asks.values()].some((ask) => !ask.answered && ask.turnId && ask.turnId === message.turnId);
  const working = segment === last && message.state === 'streaming' && piece.quiet && !asking;
  if (!piece.said && !working && !facts.length) return '';
  const places = askPlaces(view.chat);
  const copyable = segment === foot && message.state !== 'streaming'
    && message.parts.some((part) => part.kind === 'text' && String(part.text || '').trim());
  const key = 'm:' + message.id + (segment ? '#' + (segment + 1) : '');
  return html`<article class="so-msg" data-ds-key="${key}" data-role="assistant" data-state="${message.state}">${
    piece.list.map((part) => html`${piece.shown.has(part) ? partHtml(part, view) : ''}${
      (places.byPart.get(part.id) || []).map((ask) => askCard(ask, view))
    }`)
  }${working ? html`<p class="so-msg__wait ds-meta"><span class="ds-spinner" aria-hidden="true"></span><span>Working…</span></p>` : ''}${
    copyable || facts.length ? html`<div class="so-msg__foot">${
      copyable ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-msg__copy" type="button" data-action="copy-answer" data-message="${message.id}" aria-label="Copy the answer" data-tip="Copy">${raw(icon('copy', 18))}</button>` : ''
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

/**
 * renewsIn is a reset moment as "2 h 10 min", from now, each number held to
 * its unit by a no-break space.
 */
export function renewsIn(resetsAt, now = Date.now()) {
  const ms = Number(resetsAt) * 1000 - now;
  if (!(ms > 0)) return '';
  const minutes = Math.ceil(ms / 60000);
  if (minutes < 60) return minutes + '\u00a0min';
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h + '\u00a0h' + (m ? ' ' + m + '\u00a0min' : '');
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
    case 'command': {
      // The journal has the command, and the agent refused it after: the
      // line says it did not run, to the browser that sent it.
      const refused = view.failedItem(l.clientId);
      return html`<div class="so-line" data-ds-key="${key}" data-kind="command"${refused ? html` data-state="failed"` : ''}>${raw(icon('chevronRight', 16))}<span class="so-line__text so-line__command">${l.text}</span>${
        refused ? html`<span class="ds-status ds-status--error" data-chip="failed">${raw(icon('alertCircle', 16))}<span>Not run</span></span>${infoButton([refused.error], 'Why it did not run')}<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="outbox-drop" data-id="${refused.id}" aria-label="Dismiss" data-tip="Dismiss">${raw(icon('close', 18))}</button>` : ''
      }</div>`;
    }
    case 'account': {
      // The journal names accounts by id; the line says what they are
      // called now.
      const from = view.accountLabel ? view.accountLabel(l.from) : '';
      const to = view.accountLabel ? view.accountLabel(l.to) : '';
      const words = from ? 'Switched from ' + from + ' to ' + (to || 'another account') : 'Now on ' + (to || 'another account');
      // Behind the "i": why, who the new account is signed in as, what the
      // agent set aside for it, and that its first reply builds the cache
      // again (research/08).
      const about = [l.reason, ...(view.accountWho ? view.accountWho(l.to) : []), l.hint,
        l.from ? 'The next reply may take longer: the new account starts without the cached conversation.' : ''];
      return html`<div class="so-line" data-ds-key="${key}" data-kind="account" data-from="${l.from}" data-to="${l.to}">${raw(icon('user', 16))}<span class="so-line__text">${words}</span>${infoButton(about, 'About the switch')}</div>`;
    }
    case 'model': {
      // Only a change is a line: the same model by its name (an alias and
      // its full id read the same) at the same effort says nothing. The
      // chat's first event is measured against what it was started on.
      const before = l.was ? { model: l.was, effort: l.wasEffort || '' } : (view.started ? view.started() : null);
      const sameModel = !!before && !!before.model && view.modelLabel(before.model) === view.modelLabel(l.model);
      // A chat started on its CLI's default effort is not changed by the
      // CLI naming it.
      const sameEffort = l.was ? (l.wasEffort || '') === (l.effort || '') : (!before || !before.effort || before.effort === l.effort);
      if (sameModel && sameEffort) return '';
      return html`<div class="so-line" data-ds-key="${key}" data-kind="model">${raw(icon('cpu', 16))}<span class="so-line__text">${'Model: ' + view.modelLabel(l.model) + (l.effort ? ' · ' + view.effortLabel(l.effort) : '')}</span></div>`;
    }
    case 'error':
      return html`<div class="ds-alert ds-alert--error so-line-alert" data-ds-key="${key}" data-kind="error" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${l.text || 'Something went wrong.'}</span>${infoButton([l.code ? 'code ' + l.code : ''], 'Error details')}</div>`;
    case 'turn': {
      if (l.outcome === 'limited') {
        const renew = view.renewal();
        // Under the newest limit, the accounts the chat could go on with:
        // one tap switches and has the turn carried on there.
        const offers = view.continueWith ? view.continueWith(entry) : [];
        // The words and the buttons stack in the alert's text column, each
        // on a line of its own.
        return html`<div class="ds-alert ds-alert--warning so-line-alert" data-ds-key="${key}" data-kind="limit" role="status">${raw(icon('clock', 20))}<span class="ds-alert__text so-line-alert__stack"><span>${'Limit reached' + (renew ? ' — renews in ' + renew : '')}</span>${
          offers.length ? html`<span class="so-line-alert__actions">${offers.map((a) => html`<button class="ds-btn ds-btn--sm" type="button" data-action="continue-with" data-account="${a.id}">${'Continue with ' + a.label}</button>`)}</span>` : ''
        }</span>${infoButton([l.text, ...silentTurnFacts(l.turn, view)], 'About the limit')}</div>`;
      }
      if (l.outcome === 'failed') {
        // An agent that died mid-turn is said in plain words; how it died
        // (the CLI's exit, a signal) is the "i"'s (agenthost cliExited).
        const died = /^The agent exited during the turn: ([\s\S]+)$/.exec(String(l.text || ''));
        const words = died ? 'The agent stopped during the turn. The next message starts it again.' : 'The turn failed.' + (l.text ? ' ' + l.text : '');
        return html`<div class="ds-alert ds-alert--error so-line-alert" data-ds-key="${key}" data-kind="failed" role="alert">${raw(icon('alertCircle', 20))}<span class="ds-alert__text">${words}</span>${infoButton([died ? died[1] : '', l.code ? 'code ' + l.code : '', ...silentTurnFacts(l.turn, view)], 'Error details')}</div>`;
      }
      return html`<div class="so-line" data-ds-key="${key}" data-kind="interrupted">${raw(icon('ban', 16))}<span class="so-line__text">Interrupted</span>${infoButton(silentTurnFacts(l.turn, view), 'Usage of this answer')}</div>`;
    }
    default:
      return '';
  }
}
