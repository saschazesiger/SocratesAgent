// Boards: the notes of a project folder, as columns of cards.
//
// Every project folder of the sidebar has a board of its own (its notes
// mark, beside the folder's +): columns the person names, and in them cards
// with a title, a subtitle and one of the folder's colours. A colour has a
// name - "Bug", "Idea" - and the names and colours are the folder's too, so
// one project's "Bug" is not another's.
//
// The board is one document per folder on the server (server boards.go),
// saved whole with the revision it was edited from. Every change is drawn at
// once and saved behind it, one save at a time; a change made while a save
// is on its way goes with the next one. Until the server has a change it is
// kept in this browser as well (localStorage), so a tab closed in a tunnel
// or a phone that lost its signal sends it the next time the board is
// opened. A save another device beat is not forced through: the page shows
// the board as it is now and says so.
//
// A card is moved by dragging it (a mouse at once, a finger after holding
// it a moment, so a swipe still scrolls) or in its dialog, which also names
// the column - the way a keyboard moves one. A column is dragged the same
// way by its head, or moved with the Position of its dialog.

import { el, toast, confirmDialog, promptDialog } from './api.js';
import { M, html, raw, icon, iconEl, morph, ms, showOverlay, hideOverlay } from './ui.js';

// The colours a label can be, in the order the pickers offer them. The
// server knows the same names (boardColors); the theme maps each to a token
// (--so-label-*).
export const BOARD_COLORS = [
  { id: 'blue', name: 'Blue' },
  { id: 'teal', name: 'Teal' },
  { id: 'green', name: 'Green' },
  { id: 'yellow', name: 'Amber' },
  { id: 'orange', name: 'Orange' },
  { id: 'red', name: 'Red' },
  { id: 'purple', name: 'Purple' },
  { id: 'grey', name: 'Grey' },
];

// The limits the server keeps (boards.go), said before it has to.
const MAX_TITLE = 200;
const MAX_SUBTITLE = 4000;
const MAX_LABEL = 40;

// Where a change waits in this browser until the server has it.
const BUFFER = 'socrates.board.';

// How long a finger holds a card before it lifts, and how far a pointer
// moves before a press becomes a drag.
const HOLD_MS = 350;
const DRAG_SLOP = 6;

// How close to the edge of what scrolls a dragged card scrolls it.
const EDGE = 48;

// newId is an id for a column, a card or a colour: unique enough inside one
// board, and only letters, digits and dashes (the server's rule).
function newId(prefix) {
  return prefix + '-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

// call is fetch with the status kept: a 409 carries the board that won.
async function call(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(15000),
    });
  } catch {
    return { status: 0, data: {} };
  }
  if (res.status === 401) { location.href = '/login'; return { status: 401, data: {} }; }
  let data = null;
  try { data = await res.json(); } catch { /* empty */ }
  return { status: res.status, data: data || {} };
}

function readBuffer(folder) {
  try {
    const held = JSON.parse(localStorage.getItem(BUFFER + folder) || 'null');
    return held && held.board && Array.isArray(held.board.columns) ? held : null;
  } catch {
    return null;
  }
}

function writeBuffer(folder, board, baseRev) {
  try { localStorage.setItem(BUFFER + folder, JSON.stringify({ board, base_rev: baseRev })); } catch { /* full or private */ }
}

function forgetBuffer(folder) {
  try { localStorage.removeItem(BUFFER + folder); } catch { /* nothing to forget */ }
}

// normal is a board as the page works on it: every list there, whatever
// the server or an old buffer left out.
function normal(board) {
  const b = board && typeof board === 'object' ? board : {};
  return {
    labels: (Array.isArray(b.labels) ? b.labels : []).map((l) => ({
      id: String(l.id), name: String(l.name || ''), color: String(l.color || 'grey'),
    })),
    columns: (Array.isArray(b.columns) ? b.columns : []).map((c) => ({
      id: String(c.id),
      title: String(c.title || ''),
      cards: (Array.isArray(c.cards) ? c.cards : []).map((k) => ({
        id: String(k.id), title: String(k.title || ''), subtitle: String(k.subtitle || ''), label: String(k.label || ''),
      })),
    })),
  };
}

const copy = (board) => JSON.parse(JSON.stringify(board));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

let seq = 0;

/**
 * mountBoard draws a folder's board into `host` (the stage's #board
 * section) and keeps it saved. It answers:
 *
 *   show(folder, label)   the board of a folder, read from the server
 *   hide()                off the stage; what is unsaved goes on saving
 *   shown()               whether it is on the stage
 *   folder()              whose board it is
 *   frame(frame)          a board frame of /api/events
 *   resync()              read again (the stream came back)
 *   flush()               save now what waits
 */
export function mountBoard({ host }) {
  const st = {
    shown: false,
    folder: '',
    label: '',
    board: null,
    rev: 0,
    loaded: false,
    error: '',
    saving: null,
    savingFolder: '',
    // Boards sent whose answer never came: the server may hold any of them.
    unanswered: [],
    again: false,
    retry: null,
    retryDelay: 0,
    unsaved: false,
    drag: null,
  };

  /* ------------------------------------------------------------ drawing */

  function labelOf(id) {
    return st.board ? st.board.labels.find((l) => l.id === id) : null;
  }

  function tagHtml(label) {
    if (!label) return '';
    return html`<span class="ds-tag so-board__tag" data-color="${label.color}"><span class="so-board__dot" aria-hidden="true"></span>${label.name || colorName(label.color)}</span>`;
  }

  function cardHtml(card) {
    const label = labelOf(card.label);
    return html`<li class="so-board__card" data-ds-key="${'card:' + card.id}" data-card="${card.id}"${label ? html` data-color="${label.color}"` : ''}><button class="ds-card ds-card--interactive so-board__card-btn" type="button" data-open-card="${card.id}"><span class="so-board__card-title">${card.title}</span>${
      card.subtitle ? html`<span class="ds-muted so-board__card-sub">${card.subtitle}</span>` : ''
    }${label ? html`${tagHtml(label)}<span class="so-board__edge" aria-hidden="true"></span>` : ''}</button></li>`;
  }

  function columnHtml(column) {
    const headId = 'boardCol-' + column.id;
    const name = column.title || 'Untitled';
    return html`<section class="so-board__col" data-ds-key="${'col:' + column.id}" data-col="${column.id}" aria-labelledby="${headId}">
      <div class="so-board__col-head">
        <h2 class="so-board__col-name" id="${headId}"><button class="so-board__col-btn ds-pressable ds-pressable--inset" type="button" data-edit-col="${column.id}" data-tip="Rename, move or delete this column; drag it to move it">${name}</button></h2>
        <span class="ds-badge ds-badge--neutral so-board__count" aria-label="${column.cards.length + (column.cards.length === 1 ? ' card' : ' cards')}">${column.cards.length}</span>
        <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-add-card="${column.id}" aria-label="${'Add a card to ' + name}" data-tip="Add a card">${raw(icon('plus', 16))}</button>
      </div>
      <ol class="so-board__cards" data-cards="${column.id}" aria-label="${name}">${column.cards.map(cardHtml)}</ol>
      <button class="ds-btn ds-btn--text ds-add-row so-board__add" type="button" data-add-card="${column.id}">${raw(icon('plus', 16))}Add a card</button>
    </section>`;
  }

  function bodyHtml() {
    if (!st.loaded) {
      if (st.error) {
        return html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('alertCircle', 24))}</span><p class="ds-empty__title">The notes could not be read</p><p class="ds-empty__text">${st.error}</p><button class="ds-btn ds-btn--tonal" type="button" data-board="reload">Try again</button></div>`;
      }
      return html`<div class="so-board__loading"><span class="ds-spinner" role="status" aria-label="Loading the notes"></span></div>`;
    }
    const columns = st.board.columns;
    if (!columns.length) {
      return html`<div class="ds-empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('columns', 24))}</span><p class="ds-empty__title">No columns yet</p><p class="ds-empty__text">A column holds cards: "To do", "Ideas", "Done".</p><button class="ds-btn ds-btn--tonal" type="button" data-board="add-column">Add a column</button></div>`;
    }
    return html`<div class="so-board__columns" data-columns>${columns.map(columnHtml)}<div class="so-board__col so-board__col--new" data-ds-key="new-col"><button class="ds-btn ds-btn--text ds-add-row" type="button" data-board="add-column">${raw(icon('plus', 16))}Add a column</button></div></div>`;
  }

  function saveWords() {
    if (st.unsaved && st.retry) return 'Not saved yet: the server cannot be reached. It is kept on this device.';
    if ((st.saving && st.savingFolder === st.folder) || st.unsaved) return 'Saving…';
    return '';
  }

  function draw() {
    if (!st.shown) return;
    // A card in the hand is not redrawn from under it: the drop draws.
    if (st.drag && st.drag.started) { st.drawHeld = true; return; }
    st.drawHeld = false;
    const saving = saveWords();
    morph(host, html`<div class="so-board__frame">
      <div class="so-board__top"><header class="ds-page-head">
        <div class="ds-page-head__row">
          <div class="ds-page-head__text">
            <h1 class="ds-page-head__title so-board__title" tabindex="-1">Notes</h1>
            <p class="ds-page-head__lead"><span class="so-board__folder" data-tip="${st.folder}">${st.label}</span>${
              saving ? html`<span class="ds-meta so-board__saving" role="status">${saving}</span>` : html`<span class="ds-sr" role="status"></span>`
            }</p>
          </div>
          <div class="ds-page-head__actions">
            <button class="ds-btn" type="button" data-board="labels"${st.loaded ? '' : html` aria-disabled="true"`}>${raw(icon('tag', 16))}Colours</button>
            <button class="ds-btn ds-btn--tonal" type="button" data-board="add-column"${st.loaded ? '' : html` aria-disabled="true"`}>${raw(icon('plus', 16))}Column</button>
          </div>
        </div>
        ${st.loaded && st.board.labels.length ? html`<div class="so-board__legend" role="group" aria-label="Colours">${st.board.labels.map(tagHtml)}</div>` : ''}
      </header></div>
      ${bodyHtml()}
    </div>`);
  }

  // drawMoved is a redraw after cards changed places: the others glide.
  function drawMoved() {
    const list = host.querySelector('[data-columns]');
    if (!list) { draw(); return; }
    M.flip(list, () => draw(), { items: '.so-board__card' });
  }

  /* ------------------------------------------------------------- saving */

  // change runs `edit` on the board and saves what it did.
  function change(edit, { moved = false } = {}) {
    if (!st.loaded) return;
    const before = copy(st.board);
    edit(st.board);
    if (same(before, st.board)) return;
    st.unsaved = true;
    writeBuffer(st.folder, st.board, st.rev);
    if (moved) drawMoved(); else draw();
    save();
  }

  // save sends the board on screen, one save at a time: a save asked for
  // while another is on its way (this folder's or the one shown before)
  // goes when that one is back.
  function save() {
    if (st.saving) { st.again = true; return st.saving; }
    if (!st.unsaved || !st.loaded) return Promise.resolve();
    clearTimeout(st.retry);
    st.retry = null;
    const folder = st.folder;
    const board = copy(st.board);
    const baseRev = st.rev;
    st.savingFolder = folder;
    st.saving = (async () => {
      const res = await call('PUT', '/api/boards', { folder, base_rev: baseRev, board });
      st.saving = null;
      st.savingFolder = '';
      if (folder === st.folder) saved(res, board);
      else elsewhere(folder, res, board);
      draw();
      st.again = false;
      if (st.unsaved && !st.retry) save();
    })();
    draw();
    return st.saving;
  }

  // saved is what a save of the folder on screen came back with.
  function saved(res, board) {
    const theirs = res.status === 409 && res.data.conflict ? normal(res.data.board) : null;
    const ours = theirs && [board, ...st.unanswered].some((one) => same(theirs, normal(one)));
    if (res.status === 0) st.unanswered = [...st.unanswered.slice(-19), board];
    else st.unanswered = [];
    if (res.status === 200 || ours) {
      // Stored - or stored by an earlier try whose answer was lost on the
      // way, and this one found that board there: it goes on from it.
      st.rev = Number(res.data.rev) || st.rev + 1;
      st.retryDelay = 0;
      if (res.status === 200 ? same(board, st.board) : same(theirs, normal(st.board))) {
        // What the server stored is the board with its text tidied.
        st.board = normal(res.data.board);
        st.unsaved = false;
        forgetBuffer(st.folder);
      } else {
        writeBuffer(st.folder, st.board, st.rev);
      }
    } else if (theirs) {
      // Another device saved first: its board is the one there is now.
      st.board = theirs;
      st.rev = Number(res.data.rev) || 0;
      st.unsaved = false;
      forgetBuffer(st.folder);
      toast('These notes were changed on another device. They show the latest version now.', 'info');
    } else if (res.status === 0 || res.status >= 500) {
      retryLater();
    } else {
      // The server refused the board itself: what it holds stays, and the
      // person is told why.
      st.unsaved = false;
      forgetBuffer(st.folder);
      toast(res.data.error || 'The notes could not be saved.', 'error');
      load();
    }
  }

  // elsewhere is what a save came back with after its folder was left:
  // what this browser still keeps of that folder follows the server, so it
  // is sent - or let go - the next time the folder is opened.
  function elsewhere(folder, res, board) {
    if (res.status !== 200) return;
    const held = readBuffer(folder);
    if (!held) return;
    if (same(normal(held.board), normal(board))) forgetBuffer(folder);
    else writeBuffer(folder, held.board, Number(res.data.rev) || held.base_rev);
  }

  function retryLater() {
    st.retryDelay = Math.min(30000, (st.retryDelay || 1000) * 2);
    clearTimeout(st.retry);
    st.retry = setTimeout(() => { st.retry = null; save(); }, st.retryDelay);
  }

  window.addEventListener('online', () => { if (st.unsaved) { clearTimeout(st.retry); st.retry = null; save(); } });

  async function load() {
    const folder = st.folder;
    const res = await call('GET', '/api/boards?folder=' + encodeURIComponent(folder));
    if (folder !== st.folder) return;
    if (res.status !== 200) {
      if (!st.loaded) {
        st.error = res.status === 0 ? 'The server cannot be reached.' : String(res.data.error || 'The server said no.');
      }
      draw();
      return;
    }
    const rev = Number(res.data.rev) || 0;
    // A save on its way or waiting for the network decides; and an answer
    // older than the board on screen (a save came back meanwhile) is not
    // drawn over it.
    if (st.loaded && (st.unsaved || st.saving || rev < st.rev)) return;
    const server = normal(res.data.board);
    const local = readBuffer(folder);
    st.error = '';
    st.loaded = true;
    if (local && !same(normal(local.board), server)) {
      if (local.base_rev === rev) {
        // Changed here on the revision the server still has, and never sent.
        st.board = normal(local.board);
        st.rev = rev;
        st.unsaved = true;
        draw();
        save();
        return;
      }
      toast('Changes made on this device could not be kept: these notes were changed on another device meanwhile.', 'info');
    }
    if (local) forgetBuffer(folder);
    st.board = server;
    st.rev = rev;
    draw();
  }

  /* ------------------------------------------------------------ dialogs */

  function colorName(id) {
    const c = BOARD_COLORS.find((one) => one.id === id);
    return c ? c.name : id;
  }

  // formDialog is a dialog with a form: `fields` in its body, Cancel and the
  // primary action, `extra` buttons before Cancel. `submit()` returns false
  // to keep it open (a field it marked).
  function formDialog({ title, fields, okLabel, submit, extra = [], opener }) {
    const id = 'boardForm' + (++seq);
    const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
    const ok = el('button', { class: 'ds-btn ds-btn--primary', type: 'submit', text: okLabel });
    const form = el('form', { class: 'ds-dialog__box so-board__form', method: 'dialog', novalidate: true },
      el('h2', { class: 'ds-dialog__title', id: id + '-title', text: title }),
      el('div', { class: 'ds-dialog__body so-board__fields' }, fields),
      el('div', { class: 'ds-dialog__actions' }, ...extra, cancel, ok));
    const dialog = el('dialog', { class: 'ds-dialog ds-dialog--full-narrow', 'aria-labelledby': id + '-title' },
      el('div', { class: 'ds-dialog__backdrop', 'data-close': true }), form);
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      if (submit() === false) return;
      hideOverlay(dialog);
    });
    for (const button of extra) {
      button.addEventListener('click', async () => {
        if (await button.work() === false) return;
        hideOverlay(dialog);
      });
    }
    dialog.addEventListener('close', () => hideOverlay(dialog, { remove: true }), { once: true });
    document.body.append(dialog);
    showOverlay(dialog, opener);
    return dialog;
  }

  // field is a labelled control with its hint, which says what is wrong.
  function field(label, control, { full = true, hint = '' } = {}) {
    const hintId = control.id + '-hint';
    const small = el('small', { class: 'ds-field__hint', id: hintId, text: hint, hidden: !hint });
    control.setAttribute('aria-describedby', hintId);
    return el('div', { class: 'ds-field' + (full ? ' ds-field--full' : '') },
      el('label', { class: 'ds-field__label', for: control.id, text: label }), control, small);
  }

  function invalid(control, words) {
    control.setAttribute('aria-invalid', 'true');
    const hint = document.getElementById(control.id + '-hint');
    if (hint) {
      hint.className = 'ds-field__hint ds-field__hint--error';
      morph(hint, html`${raw(icon('alertCircle', 16))}${words}`);
      hint.hidden = false;
    }
    control.focus();
    return false;
  }

  // colorChips is the choice of a card's colour: none, or one of the
  // board's named colours.
  function colorChips(name, chosen) {
    const chip = (value, label, color) => el('label', { class: 'ds-chip' },
      el('input', { class: 'ds-chip__input', type: 'radio', name, value, checked: value === chosen }),
      el('span', { class: 'ds-chip__body' },
        iconEl('check', 18, 'ds-chip__check'),
        color ? el('span', { class: 'so-board__dot', 'data-color': color, 'aria-hidden': 'true' }) : null,
        el('span', { class: 'ds-chip__label', text: label })));
    return el('fieldset', { class: 'ds-chips ds-chips--wrap so-board__chips' },
      el('legend', { class: 'ds-field__label', text: 'Colour' }),
      chip('', 'None', ''),
      st.board.labels.map((l) => chip(l.id, l.name || colorName(l.color), l.color)));
  }

  function cardDialog(columnId, cardId, opener) {
    if (!st.loaded) return;
    const id = 'boardCard' + (++seq);
    const column = st.board.columns.find((c) => c.id === columnId);
    const card = column && cardId ? column.cards.find((k) => k.id === cardId) : null;
    if (!column || (cardId && !card)) return;
    const title = el('input', {
      class: 'ds-input', id: id + '-title', name: 'title', autocomplete: 'off', maxlength: MAX_TITLE,
      value: card ? card.title : '', enterkeyhint: 'done',
    });
    const sub = el('textarea', {
      class: 'ds-input so-board__sub-input', id: id + '-sub', name: 'subtitle', maxlength: MAX_SUBTITLE, rows: 3,
      value: card ? card.subtitle : '',
    });
    const chips = colorChips(id + '-color', card ? card.label : '');
    const where = el('select', { class: 'ds-input', id: id + '-col', name: 'column' },
      st.board.columns.map((c) => el('option', { value: c.id, selected: c.id === columnId, text: c.title || 'Untitled' })));
    const fields = [field('Title', title), field('Subtitle', sub, { hint: 'Optional' }), chips, field('Column', where)];
    const extra = [];
    if (card) {
      const del = el('button', { class: 'ds-btn ds-btn--text so-board__delete', type: 'button', text: 'Delete card' });
      del.work = async () => {
        const yes = await confirmDialog({
          title: 'Delete this card?', body: card.title, confirmLabel: 'Delete card', danger: true, opener: del,
        });
        if (!yes) return false;
        change((b) => {
          for (const c of b.columns) c.cards = c.cards.filter((k) => k.id !== card.id);
        });
        return true;
      };
      extra.push(del);
    }
    formDialog({
      title: card ? 'Edit card' : 'New card in ' + (column.title || 'Untitled'),
      fields,
      okLabel: card ? 'Save' : 'Add card',
      extra,
      opener,
      submit: () => {
        const text = title.value.trim();
        if (!text) return invalid(title, 'A card needs a title.');
        const picked = chips.querySelector('input:checked');
        const next = {
          id: card ? card.id : newId('card'),
          title: text,
          subtitle: sub.value.replace(/\s+$/, ''),
          label: picked ? picked.value : '',
        };
        const target = where.value || columnId;
        change((b) => {
          // Every column deleted meanwhile on another device: nothing to put it in.
          const into = b.columns.find((c) => c.id === target) || b.columns[0];
          if (!into) return;
          let index = -1;
          for (const c of b.columns) {
            const at = c.cards.findIndex((k) => k.id === next.id);
            if (at >= 0 && c.id === target) index = at;
            if (at >= 0) c.cards.splice(at, 1);
          }
          if (index >= 0 && into.id === target) into.cards.splice(index, 0, next); else into.cards.push(next);
        }, { moved: !!card && target !== columnId });
        return true;
      },
    });
    setTimeout(() => title.focus(), 0);
  }

  function columnDialog(columnId, opener) {
    if (!st.loaded) return;
    const id = 'boardCol' + (++seq);
    const at = st.board.columns.findIndex((c) => c.id === columnId);
    const column = st.board.columns[at];
    if (!column) return;
    const name = el('input', {
      class: 'ds-input', id: id + '-name', autocomplete: 'off', maxlength: MAX_TITLE, value: column.title, enterkeyhint: 'done',
    });
    const count = st.board.columns.length;
    const place = el('select', { class: 'ds-input', id: id + '-place' },
      st.board.columns.map((c, i) => el('option', {
        value: String(i), selected: i === at,
        text: (i + 1) + (i === at ? ' (here now)' : i < at ? ' (before ' + (c.title || 'Untitled') + ')' : ' (after ' + (c.title || 'Untitled') + ')'),
      })));
    const del = el('button', { class: 'ds-btn ds-btn--text so-board__delete', type: 'button', text: 'Delete column' });
    del.work = async () => {
      const cards = column.cards.length;
      const yes = await confirmDialog({
        title: 'Delete the column "' + (column.title || 'Untitled') + '"?',
        body: cards ? 'Its ' + (cards === 1 ? 'card is' : cards + ' cards are') + ' deleted with it.' : 'It holds no cards.',
        confirmLabel: 'Delete column', danger: true, opener: del,
      });
      if (!yes) return false;
      change((b) => { b.columns = b.columns.filter((c) => c.id !== column.id); });
      return true;
    };
    formDialog({
      title: 'Column',
      fields: [field('Name', name), count > 1 ? field('Position', place) : null],
      okLabel: 'Save',
      extra: [del],
      opener,
      submit: () => {
        const text = name.value.trim();
        if (!text) return invalid(name, 'A column needs a name.');
        const to = Number(place.value);
        change((b) => {
          const from = b.columns.findIndex((c) => c.id === column.id);
          if (from < 0) return;
          const [moved] = b.columns.splice(from, 1);
          moved.title = text;
          b.columns.splice(Math.max(0, Math.min(b.columns.length, Number.isFinite(to) ? to : from)), 0, moved);
        });
        return true;
      },
    });
    setTimeout(() => name.focus(), 0);
  }

  async function addColumn(opener) {
    if (!st.loaded) return;
    const text = await promptDialog({ title: 'New column', label: 'Name', confirmLabel: 'Add column', opener });
    if (!text) return;
    change((b) => { b.columns.push({ id: newId('col'), title: text.slice(0, MAX_TITLE), cards: [] }); });
    // The new column is at the end of a row that may scroll: it is shown.
    const list = host.querySelector('[data-columns]');
    if (list) list.scrollTo({ left: list.scrollWidth, behavior: 'smooth' });
  }

  // labelsDialog edits the board's colours: each a name and one of the
  // colours, added, renamed, recoloured and deleted together, saved once.
  function labelsDialog(opener) {
    if (!st.loaded) return;
    const id = 'boardLabels' + (++seq);
    let rows = st.board.labels.map((l) => ({ ...l }));
    const list = el('div', { class: 'so-board__labels', role: 'list' });
    const used = (labelId) => st.board.columns.reduce((n, c) => n + c.cards.filter((k) => k.label === labelId).length, 0);
    const drawRows = () => {
      list.replaceChildren(...rows.map((row, i) => {
        const nameId = id + '-name-' + i;
        const name = el('input', {
          class: 'ds-input', id: nameId, autocomplete: 'off', maxlength: MAX_LABEL, value: row.name,
          'aria-label': 'Name of colour ' + (i + 1), placeholder: 'Name, e.g. Bug',
        });
        name.addEventListener('input', () => { row.name = name.value; name.removeAttribute('aria-invalid'); });
        const pick = el('select', { class: 'ds-input so-board__pick', 'aria-label': 'Colour of ' + (row.name || 'colour ' + (i + 1)) },
          BOARD_COLORS.map((c) => el('option', { value: c.id, selected: c.id === row.color, text: c.name })));
        const dot = el('span', { class: 'so-board__dot so-board__dot--lg', 'data-color': row.color, 'aria-hidden': 'true' });
        pick.addEventListener('change', () => { row.color = pick.value; dot.setAttribute('data-color', pick.value); });
        const n = used(row.id);
        const remove = el('button', {
          class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button',
          'aria-label': 'Delete ' + (row.name || 'this colour'), 'data-tip': n ? 'Delete (' + n + (n === 1 ? ' card keeps' : ' cards keep') + ' no colour)' : 'Delete',
        }, iconEl('trash', 20));
        remove.addEventListener('click', () => { rows = rows.filter((one) => one !== row); drawRows(); });
        return el('div', { class: 'so-board__label-row', role: 'listitem' }, dot, pick, name, remove);
      }));
      if (!rows.length) list.append(el('p', { class: 'ds-muted', text: 'No colours: cards are plain.' }));
    };
    drawRows();
    const add = el('button', { class: 'ds-btn ds-btn--text ds-add-row', type: 'button' },
      iconEl('plus', 16), 'Add a colour');
    add.addEventListener('click', () => {
      const taken = new Set(rows.map((r) => r.color));
      const free = BOARD_COLORS.find((c) => !taken.has(c.id)) || BOARD_COLORS[0];
      rows.push({ id: newId('label'), name: '', color: free.id });
      drawRows();
      const inputs = list.querySelectorAll('input');
      if (inputs.length) inputs[inputs.length - 1].focus();
    });
    formDialog({
      title: 'Colours',
      fields: [el('p', { class: 'ds-dialog__text', text: 'Name the colours of these notes - Bug, Idea, Feature - and give a card one of them.' }), list, add],
      okLabel: 'Save',
      opener,
      submit: () => {
        const empty = rows.findIndex((r) => !r.name.trim());
        if (empty >= 0) {
          const input = list.querySelectorAll('input')[empty];
          input.setAttribute('aria-invalid', 'true');
          input.focus();
          toast('Every colour needs a name.', 'error');
          return false;
        }
        change((b) => {
          b.labels = rows.map((r) => ({ id: r.id, name: r.name.trim().slice(0, MAX_LABEL), color: r.color }));
          const kept = new Set(b.labels.map((l) => l.id));
          for (const c of b.columns) for (const k of c.cards) if (!kept.has(k.label)) k.label = '';
        });
        return true;
      },
    });
  }

  /* ------------------------------------------------------------ dragging */

  // A press on a card, or on a column's head, may become a drag: a mouse
  // once it has moved a few pixels, a finger once it has held still for
  // HOLD_MS (until then a swipe is the page scrolling). While a card or a
  // column is dragged it is held in its place as a gap (data-lifted), a
  // copy of it follows the pointer, and the gap moves to where it would
  // land; letting go puts it there.
  function pressItem(event) {
    if (event.button !== 0 || !st.loaded || st.drag) return;
    const card = event.target.closest('.so-board__card');
    const head = card ? null : event.target.closest('.so-board__col-head');
    // The head's + adds a card; it lifts nothing.
    if (head && event.target.closest('[data-add-card]')) return;
    const item = card || (head && head.closest('.so-board__col[data-col]'));
    if (!item) return;
    const kind = card ? 'card' : 'col';
    const touch = event.pointerType === 'touch';
    const press = {
      kind, item, id: card ? item.dataset.card : item.dataset.col,
      x: event.clientX, y: event.clientY, pointer: event.pointerId, touch, timer: null, started: false,
    };
    st.drag = press;
    if (touch) press.timer = setTimeout(() => lift(press), HOLD_MS);
    window.addEventListener('pointermove', movePress, { passive: false });
    window.addEventListener('pointerup', endPress);
    window.addEventListener('pointercancel', cancelPress);
  }

  function movePress(event) {
    const press = st.drag;
    if (!press || event.pointerId !== press.pointer) return;
    // A mouse let go outside the window never sent its pointerup.
    if (!press.touch && event.buttons === 0) { cancelPress(); return; }
    const far = Math.hypot(event.clientX - press.x, event.clientY - press.y) > DRAG_SLOP;
    if (!press.started) {
      if (press.touch) {
        // Moved before it was held: the finger is scrolling.
        if (far) cancelPress();
        return;
      }
      if (!far) return;
      lift(press);
    }
    event.preventDefault();
    follow(event.clientX, event.clientY);
  }

  // A finger that holds a card must not scroll the page under it.
  host.addEventListener('touchmove', (event) => { if (st.drag && st.drag.started) event.preventDefault(); }, { passive: false });

  function lift(press) {
    if (st.drag !== press || press.started) return;
    const shape = press.kind === 'col' ? press.item : press.item.querySelector('.so-board__card-btn');
    if (!shape) { cancelPress(); return; }
    press.started = true;
    const box = shape.getBoundingClientRect();
    press.dx = press.x - box.left;
    press.dy = press.y - box.top;
    const copyOf = shape.cloneNode(true);
    // The copy is a picture: no second element with the column's ids.
    for (const one of [copyOf, ...copyOf.querySelectorAll('[id]')]) one.removeAttribute('id');
    const ghost = el('div', { class: 'so-board__ghost', 'aria-hidden': 'true', 'data-kind': press.kind }, copyOf);
    ghost.style.width = box.width + 'px';
    if (press.kind === 'col') ghost.style.height = box.height + 'px';
    ghost.style.transform = `translate(${box.left}px, ${box.top}px)`;
    // Beside the page rather than in it: a redraw of the board leaves it be.
    document.body.append(ghost);
    press.ghost = ghost;
    press.item.setAttribute('data-lifted', '');
    host.setAttribute('data-dragging', '');
    if (press.touch && navigator.vibrate) { try { navigator.vibrate(10); } catch { /* not allowed */ } }
    press.lastX = press.x;
    press.lastY = press.y;
    scrollLoop(press);
  }

  function follow(x, y) {
    const press = st.drag;
    if (!press || !press.started) return;
    press.lastX = x;
    press.lastY = y;
    press.ghost.style.transform = `translate(${x - press.dx}px, ${y - press.dy}px)`;
    if (press.kind === 'col') { followColumn(press, x); return; }
    const lists = [...host.querySelectorAll('.so-board__cards')];
    let list = null;
    for (const one of lists) {
      const col = one.closest('.so-board__col').getBoundingClientRect();
      if (x >= col.left && x <= col.right) { list = one; break; }
    }
    if (!list) return;
    const others = [...list.querySelectorAll('.so-board__card:not([data-lifted])')];
    let before = null;
    for (const other of others) {
      const r = other.getBoundingClientRect();
      if (y < r.top + r.height / 2) { before = other; break; }
    }
    if (press.item.parentElement === list && press.item.nextElementSibling === before) return;
    if (before === press.item) return;
    const columns = host.querySelector('[data-columns]');
    M.flip(columns, () => { list.insertBefore(press.item, before); }, { items: '.so-board__card:not([data-lifted])' });
  }

  // followColumn moves a held column's gap to where it would land: before
  // the first other column whose middle is right of the pointer, and never
  // after "Add a column".
  function followColumn(press, x) {
    const row = host.querySelector('[data-columns]');
    if (!row) return;
    const others = [...row.querySelectorAll('.so-board__col[data-col]:not([data-lifted])')];
    let before = row.querySelector('.so-board__col--new');
    for (const other of others) {
      const r = other.getBoundingClientRect();
      if (x < r.left + r.width / 2) { before = other; break; }
    }
    if (press.item.nextElementSibling === before || before === press.item) return;
    M.flip(row, () => { row.insertBefore(press.item, before); }, { items: '.so-board__col:not([data-lifted])' });
  }

  // While a card is held near the edge of the columns or of a column's
  // cards, they scroll towards it.
  function scrollLoop(press) {
    const step = () => {
      if (st.drag !== press || !press.started) return;
      const columns = host.querySelector('[data-columns]');
      const list = press.kind === 'card' ? press.item.closest('.so-board__cards') : null;
      const was = [columns && columns.scrollLeft, list && list.scrollTop];
      if (columns) {
        const r = columns.getBoundingClientRect();
        if (press.lastX < r.left + EDGE) columns.scrollLeft -= 12;
        else if (press.lastX > r.right - EDGE) columns.scrollLeft += 12;
      }
      if (list) {
        const r = list.getBoundingClientRect();
        if (press.lastY < r.top + EDGE) list.scrollTop -= 10;
        else if (press.lastY > r.bottom - EDGE) list.scrollTop += 10;
      }
      // What scrolled under a still pointer moves the gap as well.
      if (was[0] !== (columns && columns.scrollLeft) || was[1] !== (list && list.scrollTop)) follow(press.lastX, press.lastY);
      requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
  }

  function endPress(event) {
    const press = st.drag;
    if (!press || (event && event.pointerId !== press.pointer)) return;
    if (!press.started) { cancelPress(); return; }
    // The click that follows the release is the drag's, not an open.
    const swallow = (click) => { click.stopPropagation(); click.preventDefault(); };
    window.addEventListener('click', swallow, { capture: true, once: true });
    setTimeout(() => window.removeEventListener('click', swallow, { capture: true }), 0);
    if (press.kind === 'col') { dropColumn(press); return; }
    const list = press.item.closest('.so-board__cards');
    const target = list ? list.dataset.cards : '';
    const order = list ? [...list.querySelectorAll('.so-board__card')].map((one) => one.dataset.card) : [];
    settleGhost(press);
    finish();
    if (!target) { draw(); return; }
    change((b) => {
      let moving = null;
      for (const c of b.columns) {
        const at = c.cards.findIndex((k) => k.id === press.id);
        if (at >= 0) [moving] = c.cards.splice(at, 1);
      }
      const into = b.columns.find((c) => c.id === target);
      if (!moving || !into) return;
      const index = order.indexOf(press.id);
      // Where it is among the cards of the list that are on the board.
      const after = order.slice(0, index).filter((cardId) => into.cards.some((k) => k.id === cardId));
      const prev = after.length ? into.cards.findIndex((k) => k.id === after[after.length - 1]) + 1 : 0;
      into.cards.splice(prev, 0, moving);
    });
    draw();
  }

  // dropColumn puts the columns in the order the row shows them in.
  function dropColumn(press) {
    const row = press.item.parentElement;
    const order = row ? [...row.querySelectorAll('.so-board__col[data-col]')].map((one) => one.dataset.col) : [];
    settleGhost(press);
    finish();
    change((b) => {
      const at = (c) => { const i = order.indexOf(c.id); return i < 0 ? order.length : i; };
      b.columns = b.columns.map((c, i) => ({ c, i })).sort((p, q) => at(p.c) - at(q.c) || p.i - q.i).map((one) => one.c);
    });
    draw();
  }

  // settleGhost lets the copy glide into the gap before it goes.
  function settleGhost(press) {
    const ghost = press.ghost;
    if (!ghost) return;
    const gap = press.item.getBoundingClientRect();
    ghost.setAttribute('data-settling', '');
    ghost.style.transform = `translate(${gap.left}px, ${gap.top}px)`;
    setTimeout(() => ghost.remove(), ms('--ds-dur-fast'));
  }

  function cancelPress() {
    const press = st.drag;
    if (!press) return;
    if (press.ghost) press.ghost.remove();
    finish();
    if (press.started) draw();
  }

  function finish() {
    const press = st.drag;
    if (press) {
      clearTimeout(press.timer);
      press.item.removeAttribute('data-lifted');
    }
    host.removeAttribute('data-dragging');
    st.drag = null;
    if (st.drawHeld) draw();
    window.removeEventListener('pointermove', movePress);
    window.removeEventListener('pointerup', endPress);
    window.removeEventListener('pointercancel', cancelPress);
  }

  // A held card or column opens no context menu.
  host.addEventListener('contextmenu', (event) => { if (event.target.closest('.so-board__card, .so-board__col-head')) event.preventDefault(); });
  host.addEventListener('pointerdown', pressItem);
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape' && st.drag) cancelPress(); });
  window.addEventListener('blur', () => { if (st.drag) cancelPress(); });

  /* ------------------------------------------------------------ clicks */

  host.addEventListener('click', (event) => {
    const target = event.target.closest('button');
    if (!target || !host.contains(target)) return;
    if (target.getAttribute('aria-disabled') === 'true') return;
    if (target.dataset.openCard) {
      const col = target.closest('[data-col]');
      if (col) cardDialog(col.dataset.col, target.dataset.openCard, target);
      return;
    }
    if (target.dataset.addCard) { cardDialog(target.dataset.addCard, '', target); return; }
    if (target.dataset.editCol) { columnDialog(target.dataset.editCol, target); return; }
    switch (target.dataset.board) {
      case 'add-column': addColumn(target); break;
      case 'labels': labelsDialog(target); break;
      case 'reload': st.error = ''; draw(); load(); break;
      default: break;
    }
  });

  /* ------------------------------------------------------------ the API */

  return {
    show(folder, label) {
      const other = folder !== st.folder;
      if (other) {
        if (st.drag) cancelPress();
        st.folder = folder;
        st.board = null;
        st.loaded = false;
        st.error = '';
        st.rev = 0;
        st.unsaved = false;
        st.again = false;
        st.unanswered = [];
        clearTimeout(st.retry);
        st.retry = null;
      }
      st.label = label || folder;
      st.shown = true;
      host.hidden = false;
      draw();
      load();
      const heading = host.querySelector('.so-board__title');
      if (heading && other) heading.focus({ preventScroll: true });
    },
    hide() {
      if (st.drag) cancelPress();
      st.shown = false;
      host.hidden = true;
    },
    shown: () => st.shown,
    folder: () => st.folder,
    label(label) {
      if (label && label !== st.label) { st.label = label; draw(); }
    },
    frame(frame) {
      if (!frame || frame.folder !== st.folder || !st.shown) return;
      if ((Number(frame.rev) || 0) <= st.rev || st.saving || st.unsaved || st.drag) return;
      load();
    },
    resync() {
      if (st.shown && !st.saving && !st.unsaved && !st.drag) load();
    },
    flush: () => save(),
  };
}
