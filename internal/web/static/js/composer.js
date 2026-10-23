// The composer of a chat: where the next message is written, and everything
// that goes with it - its files, the slash commands, the model it goes to,
// and Stop while the agent is working.
//
// Nothing written here is ever lost (DESIGN §E.10, REBUILD §7.1):
//
//   * the draft is in this browser's storage at every keystroke
//     (`socrates.draft.<id>`, the text and the revision it was written on)
//     and on the server after a pause, with the revision rule of a prepared
//     session's prompt: when another device saved in between, both texts
//     exist and the person chooses;
//   * Send puts the message into the chats' Outbox (chatoutbox.js) with a
//     client_id, so a message written in a tunnel goes out once the
//     connection is back, exactly once, and is on screen as "Not sent yet"
//     until then.

import { api, toast, errorMessage, clientKey, onWake, alertDialog } from './api.js';
import { html, raw, icon, morph, faces, showOverlay, hideOverlay } from './ui.js';
import * as harnesses from './harnesses.js';
import { guard, touchScreen, micButton } from './keyboard.js';
import { queueMessage, queueCommand } from './chatoutbox.js';

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
    const local = readBuffer(id);
    if (local) write(local.text);
    const res = await call('GET', path);
    if (st.closed) return;
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
    if (local && local.text !== server.text) {
      // Written on the revision the server still has: simply newer. On an
      // older one, somebody else saved in between, and the person chooses.
      if (local.base_rev === server.rev) save();
      else conflict(server);
    } else if (!local || local.text === server.text) {
      if (read() !== server.text && !local) write(server.text);
      forgetBuffer(id);
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
        if (theirs.text === text) { st.rev = theirs.rev; st.saved = text; } else conflict(theirs);
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

  const unwake = onWake(() => retryNow());
  load();
  return {
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

/* ----------------------------------------------------------- the account */

// The account picker is WP7's (REBUILD.md §4.3): the account a chat runs on,
// next to its model, with each account's usage. Until it exists the slot in
// the composer's bar stays empty and hidden. WP7 hands its drawing here:
// draw(slot, { session, chat }) fills the slot and answers whether it shows
// anything.
let accountPicker = null;

/** useAccountPicker is the hook the account picker plugs into. */
export function useAccountPicker(draw) { accountPicker = draw; }

/* -------------------------------------------------------------- composer */

/**
 * paletteOf is the commands a chat offers after "/": what the agent reported
 * about itself in its newest agent.info (AGENT-HOST.md §5, one palette source).
 */
export function paletteOf(harness, info) {
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
 *   live()        whether the chat stream is live
 *   usage()       the chat's usage and cost, as lines for its "i"
 *   sent()        a message or command was put on its way
 *   changed()     something the dock shows changed size
 */
export function mountComposer(host, ctx) {
  const id = ctx.session().id;
  const files = [];
  let paletteAt = 0;
  let paletteShown = [];
  let interrupting = false;

  morph(host, html`<div class="so-composer" data-ds-key="${'composer:' + id}">
    <div class="so-composer__conflict" hidden></div>
    <ul class="so-composer__palette" role="listbox" aria-label="Commands" hidden></ul>
    <ul class="so-composer__files" hidden></ul>
    <div class="so-composer__field">
      <label class="ds-sr" for="${'composer-' + id}">Message</label>
      <textarea class="ds-input so-composer__input" id="${'composer-' + id}" rows="1" spellcheck="true"
        placeholder="Message" aria-describedby="${'composer-hint-' + id}"></textarea>
    </div>
    <div class="so-composer__bar">
      <button class="ds-btn ds-btn--quiet ds-btn--icon so-composer__attach" type="button" data-action="attach" aria-label="Attach files" data-tip="Attach files">${raw(icon('plus', 20))}</button>
      <input class="so-composer__picker" type="file" multiple hidden tabindex="-1" aria-hidden="true">
      <button class="ds-btn ds-btn--quiet ds-btn--sm so-composer__model" type="button" data-action="model" aria-haspopup="menu" aria-expanded="false"><span class="so-composer__model-text so-ellipsis"></span>${raw(icon('chevronDown', 16))}</button>
      <span class="so-composer__account" data-slot="account" hidden></span>
      <button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-info so-composer__usage" type="button" data-action="usage" aria-label="Usage of this chat" data-tip="Usage of this chat" hidden>${raw(icon('info', 18))}</button>
      <span class="so-composer__hint ds-meta" id="${'composer-hint-' + id}" role="status"></span>
      <span class="so-composer__mic"></span>
      <button class="ds-btn ds-btn--tonal so-composer__stop" type="button" data-action="stop" hidden>${faces('Stop', 'Stopping')}</button>
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
  const mic = micButton((text) => insert(text));
  mic.classList.add('so-composer__dictate');
  $('.so-composer__mic').replaceWith(mic);
  // Send and Stop leave the cursor in the field: the next message is
  // written where the last one was, and a phone keeps its keyboard up.
  for (const button of [sendBtn, stopBtn]) button.addEventListener('mousedown', (event) => event.preventDefault());

  const draft = draftSync(id, {
    read: () => input.value,
    write: (text) => { input.value = text; grow(); paint(); },
    onConflict: (theirs) => drawConflict(theirs),
    onStatus: () => paint(),
  });

  /* ---------------------------------------------------------- the field */

  function grow() {
    input.style.height = 'auto';
    input.style.height = input.scrollHeight + 'px';
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
    return paletteOf(ctx.session().harness, chat && chat.info);
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
    }<span class="so-ellipsis">${entry.file.name || 'Pasted image'}</span>${
      FILE_WORDS[entry.state] ? html`<span class="ds-meta">${FILE_WORDS[entry.state]}</span>` : ''
    }${entry.state === 'failed' ? html`<button class="ds-btn ds-btn--text ds-btn--sm" type="button" data-action="file-retry" data-key="${entry.key}" data-tip="${entry.error}">Try again</button>` : ''}<button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm" type="button" data-action="file-remove" data-key="${entry.key}" aria-label="${'Remove ' + (entry.file.name || 'the file')}" data-tip="Remove">${raw(icon('close', 16))}</button></li>`)}`);
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
  onWake(() => {
    for (const entry of files) if (entry.state === 'failed' && navigator.onLine !== false) upload(entry);
  });

  /* -------------------------------------------------------- the model */

  function modelLabel(model) {
    const found = harnesses.modelOf(ctx.session().harness, model);
    if (found) return found.label || found.id;
    const chat = ctx.chat();
    const info = chat && chat.info && Array.isArray(chat.info.models) ? chat.info.models : [];
    const listed = info.find((one) => one.id === model);
    return listed ? (listed.label || listed.id) : model;
  }

  function current() {
    const chat = ctx.chat();
    const session = ctx.session();
    return { model: (chat && chat.model) || session.model || '', effort: (chat && chat.model ? chat.effort : session.effort) || '' };
  }

  function models() {
    const harness = ctx.session().harness;
    const offered = harnesses.offered(harness);
    if (offered.length) return offered.map((m) => ({ id: m.id, label: m.label || m.id, efforts: harnesses.effortsFor(harness, m.id) }));
    const chat = ctx.chat();
    const info = chat && chat.info && Array.isArray(chat.info.models) ? chat.info.models : [];
    return info.map((m) => ({ id: m.id, label: m.label || m.id, efforts: m.efforts || [] }));
  }

  let menu = null;
  function openModels() {
    const now = current();
    const list = models();
    if (!list.length) { toast('This agent has no models to choose from.'); return; }
    const chosen = list.find((m) => m.id === now.model) || list[0];
    if (menu) menu.remove();
    menu = document.createElement('div');
    menu.className = 'ds-menu so-model-menu';
    menu.setAttribute('role', 'menu');
    menu.setAttribute('aria-label', 'Model and effort');
    menu.hidden = true;
    morph(menu, html`${list.map((m) => html`<button class="ds-menu__item" role="menuitemradio" type="button" aria-checked="${m.id === now.model ? 'true' : 'false'}" data-model="${m.id}">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">${m.label}</span></button>`)}${
      chosen.efforts && chosen.efforts.length ? html`<div class="ds-menu__divider" role="separator"></div>${chosen.efforts.map((e) => html`<button class="ds-menu__item" role="menuitemradio" type="button" aria-checked="${e === now.effort ? 'true' : 'false'}" data-effort="${e}">${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label">${'Effort: ' + harnesses.effortLabel(e)}</span></button>`)}` : ''
    }`);
    menu.addEventListener('click', (event) => {
      const item = event.target.closest('[data-model], [data-effort]');
      if (!item) return;
      const next = { ...now };
      if (item.dataset.model) {
        next.model = item.dataset.model;
        const m = list.find((one) => one.id === next.model);
        if (m && m.efforts && m.efforts.length && !m.efforts.includes(next.effort)) next.effort = m.efforts.includes('medium') ? 'medium' : m.efforts[0];
      } else next.effort = item.dataset.effort;
      setModel(next);
    });
    showOverlay(menu, modelBtn);
  }

  async function setModel(next) {
    const now = current();
    if (next.model === now.model && next.effort === now.effort) return;
    modelBtn.setAttribute('aria-busy', 'true');
    try {
      await api('/api/chats/' + encodeURIComponent(id) + '/model', {
        method: 'POST', body: { client_id: clientKey(), model: next.model, effort: next.effort },
      });
    } catch (err) {
      toast('The model could not be changed: ' + errorMessage(err), 'error');
    } finally {
      modelBtn.removeAttribute('aria-busy');
      if (menu && !menu.hidden) hideOverlay(menu);
    }
  }

  /* --------------------------------------------------------- the account */

  // drawAccount hands the slot to the account picker when there is one.
  function drawAccount() {
    const slot = $('.so-composer__account');
    const shown = accountPicker ? !!accountPicker(slot, { session: ctx.session(), chat: ctx.chat() }) : false;
    slot.hidden = !shown;
  }

  /* ----------------------------------------------------------- the usage */

  function drawUsage() {
    const button = $('.so-composer__usage');
    const lines = ctx.usage ? ctx.usage() : [];
    button.hidden = !lines.length;
    const tip = lines.join('\n');
    if (button.dataset.tip !== tip && lines.length) button.dataset.tip = tip;
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
      toast('The agent could not be stopped: ' + errorMessage(err), 'error');
    } finally {
      interrupting = false;
      paint();
    }
  }

  host.addEventListener('click', (event) => {
    const target = event.target.closest('[data-action]');
    if (!target || !host.contains(target)) return;
    switch (target.dataset.action) {
      case 'send': send(); break;
      case 'stop': stop(); break;
      case 'attach': picker.click(); break;
      case 'model': openModels(); break;
      case 'usage':
        alertDialog({ title: 'Usage of this chat', lines: String(target.dataset.tip || '').split('\n'), confirmLabel: 'Close', opener: target });
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
  });

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
    const busy = ctx.busy();
    stopBtn.hidden = !busy;
    if (interrupting) stopBtn.setAttribute('aria-busy', 'true'); else stopBtn.removeAttribute('aria-busy');
    const empty = !input.value.trim() && !files.some((entry) => entry.state === 'ready');
    sendBtn.disabled = empty || !ready();
    const now = current();
    const words = now.model ? modelLabel(now.model) + (now.effort ? ' · ' + harnesses.effortLabel(now.effort) : '') : 'Model';
    const text = modelBtn.querySelector('.so-composer__model-text');
    if (text.textContent !== words) text.textContent = words;
    modelBtn.setAttribute('aria-label', 'Model: ' + words);
    modelBtn.hidden = !models().length;
    let said = '';
    if (draft.conflict()) said = 'Changed on another device';
    else if (busy && !empty) said = 'Sent now, it waits until the agent is done';
    else if (!ctx.live()) said = 'Offline: what you send goes out when the connection is back';
    if (hint.textContent !== said) hint.textContent = said;
    drawAccount();
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
  paint();

  return {
    paint,
    focus() { input.focus({ preventScroll: true }); },
    field: () => input,
    insert,
    addFiles,
    destroy() {
      draft.close();
      if (menu) { menu.remove(); menu = null; }
    },
  };
}
