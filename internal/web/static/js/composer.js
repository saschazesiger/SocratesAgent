// The composer of a chat: where the next message is written, and everything
// that goes with it - its files, the slash commands, the model it goes to,
// and Stop while the agent is working.
//
// Nothing written here is ever lost (DESIGN §E.10, REBUILD §7.1):
//
//   * the draft is in this browser's storage at every keystroke
//     (`socrates.draft.<id>`, the text and the revision it was written on)
//     and on the server after a pause, with a revision rule: when another
//     device saved in between, both texts
//     exist and the person chooses;
//   * Send puts the message into the chats' Outbox (chatoutbox.js) with a
//     client_id, so a message written in a tunnel goes out once the
//     connection is back, exactly once, and is on screen as "Not sent yet"
//     until then.

import { api, el, toast, errorMessage, clientKey, onWake, alertDialog, openDialog, ANY_FILE } from './api.js';
import { DS, html, raw, icon, morph, faces, showOverlay, hideOverlay } from './ui.js';
import { combobox } from './combobox.js';
import * as harnesses from './harnesses.js';
import { guard, touchScreen, micButton } from './keyboard.js';
import { queueMessage, queueCommand, queueModel, pendingModel, settled, chatOutbox, failedItem } from './chatoutbox.js';

const DRAFT_DEBOUNCE = 600;
const RETRY_MIN = 2000;
const RETRY_MAX = 60000;
const MAX_DRAFT_BYTES = 100 << 10;
const encoder = new TextEncoder();

/* ---------------------------------------------------------------- drafts */

function bufferKey(id) { return 'socrates.draft.' + id; }

function readBuffer(id) {
  try {
    const parsed = JSON.parse(localStorage.getItem(bufferKey(id)) || 'null');
    if (!parsed || typeof parsed.text !== 'string') return null;
    return { text: parsed.text, base_rev: Number(parsed.base_rev) || 0 };
  } catch { return null; }
}

function writeBuffer(id, text, baseRev) {
  try { localStorage.setItem(bufferKey(id), JSON.stringify({ text, base_rev: baseRev })); } catch { /* full or private */ }
}

function forgetBuffer(id) {
  try { localStorage.removeItem(bufferKey(id)); } catch { /* private window */ }
}

/** forgetDraft drops this browser's copy of a chat's draft: it was deleted. */
export function forgetDraft(id) { forgetBuffer(id); }

// call reads the body of a refusal too: a draft's 409 carries the other
// device's text. status 0 is "it never got there".
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

/**
 * draftSync keeps one chat's composer text in step with the server: load,
 * save after a pause, a conflict when another device saved first.
 */
function draftSync(id, { read, write, onConflict, onStatus }) {
  const path = '/api/sessions/' + encodeURIComponent(id) + '/draft';
  const st = { loaded: false, rev: 0, saved: null, saving: null, again: false, timer: null, retry: null,
    delay: 0, conflict: null, closed: false, offline: false };

  const dirty = () => st.saved === null || read() !== st.saved;

  async function load() {
    const kept = readBuffer(id);
    if (kept) {
      write(kept.text);
      st.rev = kept.base_rev;
    }
    const res = await call('GET', path);
    if (st.closed) return;
    // The buffer is read again: what was typed while the server was being
    // asked is in it now (changed() writes it at every keystroke), and it is
    // the person's text, never something the server's answer may replace.
    const local = readBuffer(id);
    if (res.status !== 200) {
      st.loaded = !!local;
      st.rev = local ? local.base_rev : 0;
      st.offline = res.status === 0;
      onStatus();
      retryLater();
      return;
    }
    const server = { text: String(res.data.text || ''), rev: Number(res.data.rev) || 0 };
    st.loaded = true;
    st.rev = server.rev;
    st.saved = server.text;
    st.offline = false;
    if (!local || local.text === server.text) {
      if (!local && read() !== server.text) write(server.text);
      forgetBuffer(id);
    } else if (local.base_rev === server.rev || !server.text) {
      // Written on the revision the server still has, or over an empty
      // draft: simply newer, and nothing of anybody else's is lost by it.
      save();
    } else {
      // On an older revision, somebody else saved in between, and the
      // person chooses.
      conflict(server);
    }
    onStatus();
  }

  function changed() {
    writeBuffer(id, read(), st.rev);
    if (st.conflict) return;
    clearTimeout(st.timer);
    st.timer = setTimeout(() => save(), DRAFT_DEBOUNCE);
  }

  function save() {
    clearTimeout(st.timer);
    st.timer = null;
    if (!st.loaded || st.conflict) return Promise.resolve();
    if (st.saving) { st.again = true; return st.saving; }
    const text = read();
    if (!dirty()) { forgetBuffer(id); return Promise.resolve(); }
    if (encoder.encode(text).length > MAX_DRAFT_BYTES) return Promise.resolve();
    st.saving = (async () => {
      const res = await call('PUT', path, { text, base_rev: st.rev });
      st.saving = null;
      if (res.status === 200) {
        st.rev = Number(res.data.rev) || st.rev + 1;
        st.saved = text;
        st.offline = false;
        st.delay = 0;
        if (read() === text) forgetBuffer(id);
        else writeBuffer(id, read(), st.rev);
      } else if (res.status === 409 && res.data.conflict) {
        const theirs = { text: String(res.data.text || ''), rev: Number(res.data.rev) || 0 };
        if (theirs.text === text) { st.rev = theirs.rev; st.saved = text; } else if (!theirs.text) {
          // Emptied elsewhere - sent, by hand or by its schedule: nothing of
          // anybody else's is lost by writing over it.
          st.rev = theirs.rev;
          st.saved = '';
          st.again = true;
        } else conflict(theirs);
      } else if (res.status === 0 || res.status >= 500) {
        st.offline = res.status === 0;
        retryLater();
      }
      onStatus();
      if (st.again && !st.closed) { st.again = false; if (dirty() && !st.conflict) save(); }
    })();
    return st.saving;
  }

  function retryLater() {
    clearTimeout(st.retry);
    if (st.closed) return;
    st.delay = Math.min(RETRY_MAX, st.delay ? st.delay * 2 : RETRY_MIN);
    st.retry = setTimeout(() => retryNow(), st.delay);
  }

  function retryNow() {
    if (st.closed) return;
    clearTimeout(st.retry);
    if (!st.loaded) { load(); return; }
    if (dirty() && !st.conflict) save();
  }

  function conflict(theirs) {
    clearTimeout(st.timer);
    st.conflict = theirs;
    onConflict(theirs);
  }

  function resolve(keepMine) {
    const theirs = st.conflict;
    if (!theirs) return;
    st.conflict = null;
    st.rev = theirs.rev;
    st.saved = theirs.text;
    if (keepMine) {
      writeBuffer(id, read(), st.rev);
      save();
    } else {
      write(theirs.text);
      forgetBuffer(id);
    }
    onStatus();
  }

  // newer is the list saying the server's draft moved on (`draft_rev`):
  // another device wrote, or the chat's scheduled start sent it and emptied
  // it. With nothing written here since the last save, the server's text is
  // read again; with something, the next save meets it as a conflict.
  function newer(rev) {
    if (!st.loaded || st.closed || st.saving || st.conflict || !(rev > st.rev)) return;
    if (read() !== st.saved) return;
    load();
  }

  const unwake = onWake(() => retryNow());
  load();
  return {
    newer,
    changed,
    save,
    resolve,
    conflict: () => st.conflict,
    offline: () => st.offline,
    close() {
      st.closed = true;
      clearTimeout(st.retry);
      unwake();
      // What is not on the server yet stays in this browser and goes up the
      // next time the chat is opened.
      if (st.timer) { clearTimeout(st.timer); save(); }
    },
  };
}

/* -------------------------------------------------------------- composer */

/**
 * paletteOf is the commands a chat offers after "/": what the agent reported
 * about itself in its newest agent.info (AGENT-HOST.md §5, one palette source).
 */
export function paletteOf(info) {
  const seen = new Map();
  const reported = info && Array.isArray(info.commands) ? info.commands : [];
  for (const one of reported) {
    const name = String(one.name || '').replace(/^\//, '');
    if (name && !seen.has(name)) {
      seen.set(name, { name, description: one.description || '', args: one.args || one.argument_hint || '' });
    }
  }
  return [...seen.values()];
}

/**
 * mountComposer draws one chat's composer into host.
 *
 * `ctx`:
 *   session()     the chat's row
 *   chat()        the chat's fold (chatfold.js)
 *   busy()        whether a turn is running
 *   stale()       whether the chat may be behind: its stream has not been
 *                 live for longer than a blink
 *   usage()       the chat's usage and cost, as named facts for its "i"
 *   sent()        a message or command was put on its way
 *   changed()     something the dock shows changed size
 *   error(text)   something the person asked for did not happen: the dock
 *                 says it, beside the composer (chatview.js)
 */
export function mountComposer(host, ctx) {
  const id = ctx.session().id;
  const files = [];
  let paletteAt = 0;
  let paletteShown = [];
  let interrupting = false;

  // The bar's last slot is the action: Send, Stop while a turn runs and
  // nothing is written, both while something written waits for the turn.
  // Enter is a new line on a touch screen, where Send is the button, and
  // sends at a desk: the keyboard's key says which.
  morph(host, html`<div class="so-composer" data-ds-key="${'composer:' + id}">
    <div class="so-composer__conflict" hidden></div>
    <ul class="so-composer__palette" role="listbox" aria-label="Commands" hidden></ul>
    <ul class="so-composer__files" hidden></ul>
    <div class="so-composer__field">
      <label class="ds-sr" for="${'composer-' + id}">Message</label>
      <textarea class="ds-input so-composer__input" id="${'composer-' + id}" rows="1" spellcheck="true"
        placeholder="Message" enterkeyhint="${touchScreen() ? 'enter' : 'send'}" aria-describedby="${'composer-hint-' + id}"></textarea>
    </div>
    <p class="so-composer__hint ds-meta" id="${'composer-hint-' + id}" role="status"></p>
    <div class="so-composer__bar">
      <button class="ds-btn ds-btn--quiet ds-btn--icon so-composer__attach" type="button" data-action="attach" aria-label="Attach files" data-tip="Attach files">${raw(icon('plus', 20))}</button>
      <input class="so-composer__picker" type="file" multiple${ANY_FILE ? html` accept="${ANY_FILE}"` : ''} hidden tabindex="-1" aria-hidden="true">
      <button class="ds-btn ds-btn--quiet ds-btn--sm so-composer__model" type="button" data-action="model" aria-haspopup="menu" aria-expanded="false"><span class="so-composer__model-text so-ellipsis"><span class="so-composer__model-name"></span><span class="so-composer__model-effort"></span></span>${raw(icon('chevronDown', 16))}</button>
      <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info so-composer__usage" type="button" data-action="usage" aria-label="Usage of this chat" data-tip="Usage of this chat" hidden>${raw(icon('info', 18))}</button>
      <span class="so-composer__gap" aria-hidden="true"></span>
      <span class="so-composer__mic"></span>
      <button class="ds-btn ds-btn--tonal ds-btn--icon so-composer__stop" type="button" data-action="stop" aria-label="Stop the agent" data-tip="Stop" hidden>${faces(raw(icon('stop', 20)), '')}</button>
      <button class="ds-btn ds-btn--primary ds-btn--icon so-composer__send" type="button" data-action="send" aria-label="Send" data-tip="Send">${raw(icon('arrowUp', 20))}</button>
    </div>
  </div>`);
  const $ = (sel) => host.querySelector(sel);
  const input = $('.so-composer__input');
  const picker = $('.so-composer__picker');
  const palette = $('.so-composer__palette');
  const fileList = $('.so-composer__files');
  const conflictBox = $('.so-composer__conflict');
  const hint = $('.so-composer__hint');
  const sendBtn = $('.so-composer__send');
  const stopBtn = $('.so-composer__stop');
  const modelBtn = $('.so-composer__model');
  guard(input);
  input.placeholder = 'Message ' + harnesses.label(ctx.session().harness);
  // Dictation lands where the cursor is, like typing would (dictate.js).
  const mic = micButton((text) => insert(text), 'a message');
  mic.classList.add('so-composer__dictate');
  $('.so-composer__mic').replaceWith(mic);
  // Send and Stop leave the cursor in the field: the next message is
  // written where the last one was, and a phone keeps its keyboard up.
  for (const button of [sendBtn, stopBtn]) button.addEventListener('mousedown', (event) => event.preventDefault());

  // A draft kept in this browser is written into the field while draftSync
  // is still being set up (its load() starts at once), when neither `draft`
  // nor what paint() draws exists yet: the composer is painted once at the
  // end of mount, and only from then on by the draft.
  let mounted = false;
  const draft = draftSync(id, {
    read: () => input.value,
    write: (text) => { input.value = text; grow(); if (mounted) paint(); },
    onConflict: (theirs) => drawConflict(theirs),
    onStatus: () => { if (mounted) paint(); },
  });

  /* ---------------------------------------------------------- the field */

  // As tall as its text - except as a chat's first message, where the field
  // fills the view by itself (so-chat.css [data-fresh]).
  function grow() {
    if (input.closest('[data-fresh]')) {
      input.style.height = '';
      return;
    }
    // The field is border-box and scrollHeight leaves its borders out: a
    // height of scrollHeight alone was 2px short of the text, and a
    // platform with classic scrollbars (Windows) drew one, arrows and
    // all, beside an empty line (B-39). Its overflow stays the CSS's
    // (auto): its cap follows the keyboard (--so-composer-max), and a
    // field that is capped meanwhile must still scroll.
    const style = getComputedStyle(input);
    const borders = style.boxSizing === 'border-box'
      ? (parseFloat(style.borderTopWidth) || 0) + (parseFloat(style.borderBottomWidth) || 0) : 0;
    input.style.height = 'auto';
    input.style.height = Math.ceil(input.scrollHeight + borders) + 'px';
  }

  input.addEventListener('input', () => {
    grow();
    draft.changed();
    drawPalette();
    paint();
  });

  input.addEventListener('keydown', (event) => {
    if (!palette.hidden && paletteShown.length) {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        paletteAt = (paletteAt + (event.key === 'ArrowDown' ? 1 : -1) + paletteShown.length) % paletteShown.length;
        drawPalette(false);
        return;
      }
      if (event.key === 'Tab' || (event.key === 'Enter' && !event.shiftKey)) {
        event.preventDefault();
        choose(paletteShown[paletteAt]);
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        palette.hidden = true;
        return;
      }
    }
    if (event.key !== 'Enter' || event.isComposing) return;
    // Ctrl or Cmd with Enter sends everywhere; Enter alone sends at a desk,
    // and is a new line on a touch screen, where Send is a button.
    if (event.ctrlKey || event.metaKey || (!event.shiftKey && !touchScreen())) {
      event.preventDefault();
      send();
    }
  });

  /* -------------------------------------------------------- the palette */

  function palettes() {
    const chat = ctx.chat();
    return paletteOf(chat && chat.info);
  }

  function drawPalette(reset = true) {
    const m = /^\/(\S*)$/.exec(input.value);
    const all = m ? palettes() : [];
    paletteShown = m ? all.filter((one) => one.name.toLowerCase().startsWith(m[1].toLowerCase())) : [];
    if (reset) paletteAt = 0;
    palette.hidden = !paletteShown.length;
    if (palette.hidden) {
      input.removeAttribute('aria-activedescendant');
      return;
    }
    morph(palette, html`${paletteShown.map((one, i) => html`<li class="so-palette__item" id="${'cmd-' + id + '-' + i}" data-ds-key="${one.name}" role="option" aria-selected="${i === paletteAt ? 'true' : 'false'}" data-command="${one.name}"><span class="so-palette__name">/${one.name}${one.args ? html`<span class="so-palette__args">${' ' + one.args}</span>` : ''}</span><span class="so-palette__desc so-ellipsis">${one.description}</span></li>`)}`);
    input.setAttribute('aria-activedescendant', 'cmd-' + id + '-' + paletteAt);
    ctx.changed();
  }

  function choose(one) {
    if (!one) return;
    input.value = '/' + one.name + ' ';
    palette.hidden = true;
    grow();
    draft.changed();
    input.focus();
    paint();
  }

  palette.addEventListener('mousedown', (event) => event.preventDefault());
  palette.addEventListener('click', (event) => {
    const item = event.target.closest('[data-command]');
    if (item) choose(paletteShown.find((one) => one.name === item.dataset.command));
  });

  /* ---------------------------------------------------------- the files */

  async function upload(entry) {
    entry.state = 'uploading';
    drawFiles();
    const form = new FormData();
    form.append('file', entry.file, entry.file.name || 'pasted.png');
    try {
      const res = await fetch('/api/chats/' + encodeURIComponent(id) + '/attachments', {
        method: 'POST', body: form, credentials: 'same-origin', signal: AbortSignal.timeout(120000),
      });
      if (res.status === 401) { location.href = '/login'; return; }
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || 'The file could not be uploaded.');
      entry.attachment = (data.files || [])[0];
      entry.state = 'ready';
    } catch (err) {
      entry.state = 'failed';
      entry.error = err && err.name === 'TimeoutError' ? 'The upload took too long.' : errorMessage(err);
    }
    if (files.includes(entry)) drawFiles();
    paint();
  }

  function addFiles(list) {
    for (const file of list) {
      if (!file) continue;
      const entry = { key: clientKey(), file, state: 'uploading', attachment: null, error: '' };
      files.push(entry);
      upload(entry);
    }
    drawFiles();
    paint();
  }

  const FILE_WORDS = { uploading: 'Uploading', failed: 'Not uploaded' };

  function drawFiles() {
    fileList.hidden = !files.length;
    morph(fileList, html`${files.map((entry) => html`<li class="so-attachment so-composer__file" data-ds-key="${entry.key}" data-state="${entry.state}">${
      entry.state === 'uploading' ? html`<span class="ds-spinner" aria-hidden="true"></span>`
        : raw(icon(entry.state === 'failed' ? 'alertCircle' : (entry.file.type || '').startsWith('image/') ? 'image' : 'file', 16))
    }<span class="so-break so-composer__file-name">${entry.file.name || 'Pasted image'}</span>${
      FILE_WORDS[entry.state] ? html`<span class="ds-meta">${FILE_WORDS[entry.state]}</span>` : ''
    }${entry.state === 'failed' ? html`<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="file-retry" data-key="${entry.key}" aria-label="${'Upload ' + (entry.file.name || 'the file') + ' again'}" data-tip="${'Upload again' + (entry.error ? ' (' + entry.error + ')' : '')}">${raw(icon('sync', 18))}</button>` : ''}<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="file-remove" data-key="${entry.key}" aria-label="${'Remove ' + (entry.file.name || 'the file')}" data-tip="Remove">${raw(icon('close', 16))}</button></li>`)}`);
    ctx.changed();
  }

  picker.addEventListener('change', () => {
    addFiles([...picker.files]);
    picker.value = '';
  });
  input.addEventListener('paste', (event) => {
    const pasted = [...(event.clipboardData ? event.clipboardData.files : [])];
    if (!pasted.length) return;
    event.preventDefault();
    addFiles(pasted);
  });
  const unwakeFiles = onWake(() => {
    for (const entry of files) if (entry.state === 'failed' && navigator.onLine !== false) upload(entry);
  });

  /* -------------------------------------------------------- the model */

  // The chat's folder: one with a configuration of the agent of its own
  // (OpenCode's opencode.json) has models of its own to offer, asked for
  // once the composer is there and drawn again when they come.
  const folder = () => ctx.session().workdir || '';
  harnesses.loadFolder({ session: id }).then(() => { if (host.isConnected) paint(); }, () => { /* the kept answer, or none */ });

  function modelLabel(model) {
    const found = harnesses.modelOf(ctx.session().harness, model, folder());
    if (found) return found.label || found.id;
    const chat = ctx.chat();
    const info = chat && chat.info && Array.isArray(chat.info.models) ? chat.info.models : [];
    const listed = info.find((one) => one.id === model);
    return listed ? (listed.label || listed.id) : harnesses.readableModel(model);
  }

  // canonical is a model as the list offers it: the journal holds what an
  // alias resolved to ("claude-sonnet-…" for "sonnet").
  function canonical(model) {
    const found = harnesses.modelOf(ctx.session().harness, model, folder());
    return found ? found.id : model;
  }

  // current is the model the next message goes to: a change still on its
  // way in the Outbox, else what the journal says, else the session's own.
  function current() {
    const asked = pendingModel(id);
    if (asked) return { model: asked.model, effort: asked.effort || '' };
    const chat = ctx.chat();
    const session = ctx.session();
    return { model: (chat && chat.model) || session.model || '', effort: (chat && chat.model ? chat.effort : session.effort) || '' };
  }

  function models() {
    const harness = ctx.session().harness;
    const offered = harnesses.offered(harness, folder());
    if (offered.length) {
      return offered.map((m) => ({
        id: m.id, label: m.label || m.id, efforts: harnesses.effortsFor(harness, m.id, folder()), default_effort: m.default_effort || '',
      }));
    }
    const chat = ctx.chat();
    const info = chat && chat.info && Array.isArray(chat.info.models) ? chat.info.models : [];
    return info.map((m) => ({ id: m.id, label: m.label || m.id, efforts: m.efforts || [], default_effort: m.default_effort || '' }));
  }

  // effortAfter is the effort a model is switched to with: the one the chat
  // is on when the model has it, else the model's own starting one, else the
  // middle of its levels - never "none" by accident, which turns an OpenCode
  // model's reasoning off.
  function effortAfter(model, effort) {
    const levels = (model && model.efforts) || [];
    if (!levels.length || levels.includes(effort)) return levels.length ? effort : '';
    if (model.default_effort && levels.includes(model.default_effort)) return model.default_effort;
    for (const want of ['medium', 'high', 'low']) if (levels.includes(want)) return want;
    return levels.find((one) => one !== 'none') || '';
  }

  // A menu holds a dozen models; more than that and it holds the few that
  // matter - the one the chat is on, the favourites, the ones used last -
  // and opens the search for the rest (chooseModel).
  const MENU_MODELS = 12;
  const MENU_SHORT = 8;

  function shortList(list, nowId) {
    const harness = ctx.session().harness;
    const ids = [nowId, ...harnesses.favorites(harness, folder()), ...harnesses.recent(harness, folder())];
    const out = [];
    for (const one of ids) {
      const m = list.find((entry) => entry.id === one);
      if (m && !out.includes(m)) out.push(m);
      if (out.length >= MENU_SHORT) break;
    }
    return out;
  }

  let menu = null;
  function openModels() {
    const now = current();
    const nowId = canonical(now.model);
    const all = models();
    if (!all.length) { toast('This agent has no models to choose from.'); return; }
    const many = all.length > MENU_MODELS;
    const list = many ? shortList(all, nowId) : all;
    const chosen = all.find((m) => m.id === nowId) || list[0] || all[0];
    if (menu) menu.remove();
    menu = document.createElement('div');
    menu.className = 'ds-menu so-model-menu';
    menu.setAttribute('role', 'menu');
    menu.setAttribute('aria-label', 'Model and effort');
    menu.hidden = true;
    // A phone's bar has no room for the chat's "i": its usage is the
    // menu's last item there.
    const usage = $('.so-composer__usage');
    const usageHere = !usage.hidden && getComputedStyle(usage).display === 'none';
    // It opens on the model the chat is on (autofocus), not on the first.
    morph(menu, html`${list.map((m) => html`<button class="ds-menu__item" role="menuitemradio" type="button" aria-checked="${m.id === nowId ? 'true' : 'false'}"${m.id === nowId ? html` autofocus` : ''} data-model="${m.id}">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">${m.label}</span></button>`)}${
      many ? html`<button class="ds-menu__item" role="menuitem" type="button" data-all-models${list.length ? '' : html` autofocus`}>${raw(icon('search', 20))}<span class="ds-menu__label">${'All ' + all.length + ' models…'}</span></button>` : ''
    }${
      chosen.efforts && chosen.efforts.length ? html`<div class="ds-menu__divider" role="separator"></div>${chosen.efforts.map((e) => html`<button class="ds-menu__item" role="menuitemradio" type="button" aria-checked="${e === now.effort ? 'true' : 'false'}" data-effort="${e}">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">${'Effort: ' + harnesses.effortLabel(e)}</span></button>`)}` : ''
    }${usageHere ? html`<div class="ds-menu__divider" role="separator"></div><button class="ds-menu__item" role="menuitem" type="button" data-usage>${raw(icon('info', 20))}<span class="ds-menu__label">Usage of this chat</span></button>` : ''}`);
    menu.addEventListener('click', (event) => {
      if (event.target.closest('[data-usage]')) {
        hideOverlay(menu);
        showUsage(modelBtn);
        return;
      }
      if (event.target.closest('[data-all-models]')) {
        hideOverlay(menu);
        chooseModel(all);
        return;
      }
      const item = event.target.closest('[data-model], [data-effort]');
      if (!item) return;
      const next = { ...now };
      if (item.dataset.model) {
        next.model = item.dataset.model;
        next.effort = effortAfter(all.find((one) => one.id === next.model), next.effort);
      } else next.effort = item.dataset.effort;
      setModel(next);
    });
    showOverlay(menu, modelBtn);
  }

  // chooseModel is every model of the agent in a dialog: a search field over
  // names, providers, ids and what each costs - the favourites and the ones
  // used last at the top before anything is typed - and the chosen model's
  // efforts as chips under it. An id typed that is not on the list is sent
  // as it was typed: the CLI may know a model its listing does not.
  function chooseModel(all) {
    const harness = ctx.session().harness;
    const now = current();
    const pick = { model: canonical(now.model), effort: now.effort };
    const byId = (value) => all.find((m) => m.id === value) || null;
    const effortRow = el('div', { class: 'ds-chips', role: 'radiogroup', 'aria-label': 'Effort' });
    const effortField = el('fieldset', { class: 'ds-field so-model-search__effort' },
      el('legend', { class: 'ds-field__label', text: 'Effort' }), effortRow);
    const picker = combobox({
      value: pick.model,
      items: () => harnesses.modelItems(harness, folder()),
      placeholder: 'Search by name, provider or id',
      display: (value) => { const m = byId(value); return m ? m.label : harnesses.readableModel(value); },
      onChange: (value) => {
        const typed = String(value || '').trim();
        const named = all.find((m) => m.id === typed || m.label.toLowerCase() === typed.toLowerCase());
        pick.model = named ? named.id : typed;
        pick.effort = effortAfter(byId(pick.model), pick.effort);
        drawEffort();
      },
    });
    picker.input.id = 'modelSearch-' + id;
    picker.input.setAttribute('autofocus', '');
    const field = el('div', { class: 'so-model-search' },
      el('div', { class: 'ds-field' }, el('label', { class: 'ds-field__label', for: picker.input.id, text: 'Model' }), picker.node),
      effortField);
    function drawEffort() {
      const levels = (byId(pick.model) || {}).efforts || [];
      effortField.hidden = !levels.length;
      morph(effortRow, html`${levels.map((value) => html`<label class="ds-chip" data-value="${value}" data-ds-key="${'effort-' + value}"><input class="ds-chip__input" type="radio" name="${'modelSearchEffort-' + id}" value="${value}"><span class="ds-chip__body">${raw(icon('check', 18, 'ds-chip__check'))}<span>${harnesses.effortLabel(value)}</span></span></label>`)}`);
      for (const input of effortRow.querySelectorAll('input')) input.checked = input.value === pick.effort;
    }
    effortRow.addEventListener('change', (event) => { pick.effort = event.target.value; });
    drawEffort();
    const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
    const use = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', text: 'Use this model' });
    const { dialog } = openDialog({ title: 'Model', field, actions: [cancel, use], opener: modelBtn });
    dialog.classList.add('so-model-search__dialog');
    use.addEventListener('click', () => {
      if (!pick.model) { picker.focus(); return; }
      dialog.answer(true);
      DS.overlay.close(dialog);
      setModel({ model: pick.model, effort: pick.effort });
    });
    // Enter takes the highlighted entry while the list is open (the
    // combobox's), and with it closed uses the model, as the button does.
    picker.input.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' || event.isComposing || picker.isOpen()) return;
      event.preventDefault();
      use.click();
    });
  }

  // setModel goes through the Outbox like a message: chosen offline, it is
  // the model of what is written after it, in that order. The button shows
  // the choice at once; a refusal says so and leaves the queue.
  function setModel(next) {
    const now = current();
    if (menu && !menu.hidden) hideOverlay(menu);
    if (canonical(next.model) === canonical(now.model) && next.effort === now.effort) return;
    harnesses.rememberModel(ctx.session().harness, canonical(next.model));
    const clientId = queueModel(id, next.model, next.effort);
    paint();
    settled(clientId).catch((err) => {
      const refused = failedItem(clientId);
      if (refused) chatOutbox().drop(refused.id);
      ctx.error('The model could not be changed: ' + errorMessage(err));
    });
  }

  /* ----------------------------------------------------------- the usage */

  // The usage is named facts ([label, value]): rows in the dialog, as
  // Info's are, and "Label: value" lines in the tip.
  function showUsage(opener) {
    const facts = ctx.usage ? ctx.usage() : [];
    alertDialog({ title: 'Usage of this chat', facts, lines: facts.length ? [] : ['Nothing is known yet.'], confirmLabel: 'Close', opener });
  }

  function drawUsage() {
    const button = $('.so-composer__usage');
    const facts = ctx.usage ? ctx.usage() : [];
    button.hidden = !facts.length;
    const tip = facts.map(([label, value]) => label + ': ' + value).join('\n');
    if (button.dataset.tip !== tip && facts.length) button.dataset.tip = tip;
  }

  /* --------------------------------------------------------- send, stop */

  function ready() {
    return files.every((entry) => entry.state === 'ready');
  }

  function send() {
    const text = input.value.trim();
    const attached = files.filter((entry) => entry.state === 'ready').map((entry) => entry.attachment);
    if (!text && !attached.length) return;
    if (!ready()) {
      toast(files.some((entry) => entry.state === 'failed')
        ? 'A file was not uploaded. Try it again or remove it.' : 'Wait for the files to finish uploading.');
      return;
    }
    // A slash command the agent has goes to it as a command; anything else
    // that starts with a slash is passed on as text, which is what the CLI
    // would do with it typed at its own prompt.
    const m = /^\/(\S+)(?:\s+([\s\S]*))?$/.exec(text);
    const known = m && !attached.length && palettes().find((one) => one.name === m[1]);
    if (known) queueCommand(id, m[1], (m[2] || '').trim(), text);
    else queueMessage(id, text, attached);
    input.value = '';
    files.length = 0;
    drawFiles();
    palette.hidden = true;
    grow();
    draft.changed();
    draft.save();
    paint();
    ctx.sent();
  }

  async function stop() {
    if (interrupting) return;
    interrupting = true;
    paint();
    try {
      await api('/api/chats/' + encodeURIComponent(id) + '/interrupt', { method: 'POST', body: { client_id: clientKey() } });
    } catch (err) {
      ctx.error('The agent could not be stopped: ' + errorMessage(err));
    } finally {
      interrupting = false;
      paint();
    }
  }

  // The host is the dock's slot, which every chat's composer is drawn into
  // in turn: this one's listener goes with it (destroy), or a Stop would
  // also stop the chat shown before.
  const listening = new AbortController();
  host.addEventListener('click', (event) => {
    const target = event.target.closest('[data-action]');
    if (!target || !host.contains(target)) return;
    switch (target.dataset.action) {
      case 'send': send(); break;
      case 'stop': stop(); break;
      case 'attach': picker.click(); break;
      case 'model': openModels(); break;
      case 'usage':
        showUsage(target);
        break;
      case 'file-remove': {
        const at = files.findIndex((entry) => entry.key === target.dataset.key);
        if (at >= 0) files.splice(at, 1);
        drawFiles();
        paint();
        input.focus();
        break;
      }
      case 'file-retry': {
        const entry = files.find((one) => one.key === target.dataset.key);
        if (entry) upload(entry);
        break;
      }
      case 'keep-mine': hideConflict(); draft.resolve(true); break;
      case 'use-theirs': hideConflict(); draft.resolve(false); grow(); break;
      default: break;
    }
  }, { signal: listening.signal });

  /* ------------------------------------------------------- the conflict */

  function drawConflict(theirs) {
    const preview = theirs.text.length > 400 ? theirs.text.slice(0, 400) + '…' : theirs.text;
    conflictBox.hidden = false;
    morph(conflictBox, html`<div class="ds-alert ds-alert--warning" role="alert">${raw(icon('alert', 20))}<span class="ds-alert__text">This message was changed on another device while this one was writing it. Choose which text to keep.</span></div><pre class="so-mono so-composer__theirs" tabindex="0" aria-label="The other device’s text">${preview || '(empty)'}</pre><div class="ds-form-actions"><button class="ds-btn ds-btn--tonal" type="button" data-action="keep-mine">Keep mine</button><button class="ds-btn" type="button" data-action="use-theirs">Use the other device’s text</button></div>`);
    ctx.changed();
  }

  function hideConflict() {
    conflictBox.hidden = true;
    morph(conflictBox, '');
    ctx.changed();
  }

  /* -------------------------------------------------------------- paint */

  function paint() {
    // A save that ends after the chat has left the stage - another chat, a
    // terminal, Home - finds no chat of this composer's to paint.
    const row = ctx.session();
    if (!row || row.id !== id) return;
    if (mounted) draft.newer(Number(row.draft_rev) || 0);
    const busy = ctx.busy();
    const empty = !input.value.trim() && !files.some((entry) => entry.state === 'ready');
    // One slot: Stop while the turn runs and nothing is written, Send
    // beside it once something is.
    stopBtn.hidden = !busy;
    sendBtn.hidden = busy && empty;
    if (interrupting) stopBtn.setAttribute('aria-busy', 'true'); else stopBtn.removeAttribute('aria-busy');
    sendBtn.disabled = empty || !ready();
    const keyHint = touchScreen() ? 'enter' : 'send';
    if (input.getAttribute('enterkeyhint') !== keyHint) input.setAttribute('enterkeyhint', keyHint);
    // A phone's bar says the model's name; its effort is in the menu.
    const now = current();
    const name = now.model ? modelLabel(now.model) : 'Model';
    const effort = now.model && now.effort ? ' · ' + harnesses.effortLabel(now.effort) : '';
    const words = name + effort;
    const nameText = modelBtn.querySelector('.so-composer__model-name');
    const effortText = modelBtn.querySelector('.so-composer__model-effort');
    if (nameText.textContent !== name) nameText.textContent = name;
    if (effortText.textContent !== effort) effortText.textContent = effort;
    modelBtn.setAttribute('aria-label', 'Model: ' + words);
    if (modelBtn.dataset.tip !== 'Model: ' + words) modelBtn.dataset.tip = 'Model: ' + words;
    modelBtn.hidden = !models().length;
    // What is not plain from the buttons, in a line of its own over them:
    // that the draft was changed on another device. The connection is the
    // dock's to say (chatview.js).
    let said = '';
    if (draft.conflict()) said = 'Changed on another device';
    if (hint.textContent !== said) {
      hint.textContent = said;
      ctx.changed();
    }
    drawUsage();
  }

  // insert puts words where the cursor is, as if they had been typed.
  function insert(text) {
    const words = String(text || '').trim();
    if (!words) return;
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? input.value.length;
    const before = input.value.slice(0, start);
    const join = before && !/\s$/.test(before) ? ' ' : '';
    input.value = before + join + words + input.value.slice(end);
    const at = (before + join + words).length;
    input.setSelectionRange(at, at);
    grow();
    draft.changed();
    paint();
    input.focus({ preventScroll: true });
  }

  grow();
  mounted = true;
  paint();

  return {
    paint,
    grow,
    focus() { input.focus({ preventScroll: true }); },
    field: () => input,
    insert,
    addFiles,
    destroy() {
      listening.abort();
      unwakeFiles();
      draft.close();
      if (menu) { menu.remove(); menu = null; }
      // The slot is the same for every chat, and morph() would adopt this
      // composer's buttons - with their listeners: a microphone's record()
      // under the next composer's Stop - and its field, with the keyboard's
      // record of it. The next composer starts from nothing.
      host.replaceChildren();
    },
  };
}
