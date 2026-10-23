// The panel of a prepared session ("PreStart").
//
// A prepared session is a session whose settings and first prompt are written
// before anything runs. It has no terminal, so its panel stands where the
// pane would: the same settings the new-session sheet asks for (drawn by the
// same code, harnesses.settingsForm), a large field for the prompt, a time at
// which it may start by itself, and Start. Start is the same launch a new
// session takes on the server, and the prompt goes in as the program's first
// one - after which the session is an ordinary one and this panel is gone.
//
// The prompt is the one thing here that can be long and that somebody may
// spend a quarter of an hour on, on a phone, in a tunnel. So it is never only
// in the field:
//
//   * every keystroke is written to this browser's storage at once
//     (`socrates.draft.<id>`: the text and the revision it was written on),
//     and that copy is only dropped once the server has acknowledged exactly
//     that text;
//   * the server is told after a pause in the typing, with the revision the
//     text was based on. A different revision means another device saved in
//     between, and then both texts exist and only the person can say which
//     one is meant - the panel asks rather than guessing;
//   * a page that comes back with a stored copy the server never saw sends it
//     then, or asks, if somebody else saved in the meantime.
//
// The settings are saved the same way but without the ceremony: they are a
// handful of choices, the last one made wins, and a refused one says so.

import { el, toast, infoTip, onWake } from './api.js';
import * as harnesses from './harnesses.js';
import { dictated } from './keyboard.js';
import { iconEl, faces, setFaceLabel, morph } from './ui.js';

// How long a pause in typing is before the text is sent, and before a change
// of settings is. A setting is one tap; a prompt is a run of keystrokes.
const DRAFT_DEBOUNCE = 600;
const SETTINGS_DEBOUNCE = 400;

// The backoff of a save that did not get through. Each failure doubles it;
// the network coming back, or the page being looked at, tries at once.
const RETRY_MIN = 2000;
const RETRY_MAX = 60000;

// The largest prompt the server keeps (maxDraftBytes in
// internal/server/prestart.go). Checked here too, so that a prompt that is
// too long says so while it is being written rather than failing to save.
const MAX_DRAFT_BYTES = 100 << 10;

// How close a scheduled start has to be before the panel keeps an eye on the
// clock for it. The list arrives on the event stream as it changes (and is
// polled every five to fifteen seconds only while the stream is down); this
// is what makes the switch to the terminal follow the start within a second
// or two even then.
const SCHEDULE_WATCH = 10 * 60 * 1000;

const encoder = new TextEncoder();

/* ------------------------------------------------------- local storage */

function bufferKey(id) { return 'socrates.draft.' + id; }

function readBuffer(id) {
  try {
    const raw = localStorage.getItem(bufferKey(id));
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed.text !== 'string') return null;
    return { text: parsed.text, base_rev: Number(parsed.base_rev) || 0 };
  } catch { return null; }
}

function writeBuffer(id, text, baseRev) {
  try { localStorage.setItem(bufferKey(id), JSON.stringify({ text, base_rev: baseRev })); } catch { /* full, or a private window */ }
}

/** forgetDraft drops this browser's copy of a session's prompt: it was deleted. */
export function forgetDraft(id) {
  try { localStorage.removeItem(bufferKey(id)); } catch { /* private window */ }
}

/* ------------------------------------------------------------- the wire */

/**
 * call is one request whose refusal is an answer worth reading. api() turns a
 * 409 into an error carrying a sentence, and the 409 of a draft carries the
 * other device's text, which is the whole point of it - so these few requests
 * read the body whatever the status. `status` 0 is "it never got there".
 */
async function call(method, path, body, timeout = 15000) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      cache: 'no-store',
      signal: AbortSignal.timeout(timeout),
    });
  } catch {
    return { status: 0, data: {} };
  }
  if (res.status === 401) {
    location.href = '/login';
    // An empty answer rather than none: the callers read data.error.
    return { status: 401, data: {} };
  }
  let data = null;
  try { data = await res.json(); } catch { /* an empty or non-JSON body */ }
  return { status: res.status, data: data || {} };
}

/* --------------------------------------------------------------- words */

// inWords is a distance in time the way a person says it: "in 2 h 15 min",
// "in 3 d 4 h", "in 40 s".
export function inWords(ms) {
  if (ms < 60000) return 'in ' + Math.max(1, Math.ceil(ms / 1000)) + ' s';
  const minutes = Math.round(ms / 60000);
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const rest = minutes % 60;
  if (days > 0) return 'in ' + days + ' d' + (hours ? ' ' + hours + ' h' : '');
  if (hours > 0) return 'in ' + hours + ' h' + (rest ? ' ' + rest + ' min' : '');
  return 'in ' + rest + ' min';
}

const two = (n) => String(n).padStart(2, '0');

/**
 * whenShort is a start time for a sidebar row: the time of day when it is
 * today, the weekday and the time within the week, the date beyond that.
 */
export function whenShort(at, now = Date.now()) {
  const when = new Date(at);
  if (Number.isNaN(when.getTime())) return '';
  const clock = two(when.getHours()) + ':' + two(when.getMinutes());
  const today = new Date(now);
  const sameDay = when.toDateString() === today.toDateString();
  if (sameDay) return clock;
  const days = (at - now) / 86400000;
  if (days > -1 && days < 6) return when.toLocaleDateString(undefined, { weekday: 'short' }) + ' ' + clock;
  return when.toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) + ' ' + clock;
}

// localInput is a moment as a datetime-local field spells it: this browser's
// local time, to the minute.
function localInput(at) {
  const d = new Date(at);
  return d.getFullYear() + '-' + two(d.getMonth() + 1) + '-' + two(d.getDate())
    + 'T' + two(d.getHours()) + ':' + two(d.getMinutes());
}

// settingsOf is a prepared row's settings as the form takes them.
function settingsOf(session) {
  const options = (session && session.options && typeof session.options === 'object') ? session.options : {};
  return {
    harness: session.harness || '',
    model: session.model || '',
    effort: session.effort || '',
    ultracode: !!options.ultracode,
    profile: options.profile || '',
    profile_auto: !!options.profile_auto,
    workdir_mode: session.workdir_mode || harnesses.DYNAMIC,
    workdir: session.workdir || '',
  };
}

// Which fields a change of one kind sends. A program brings its own model
// and effort and takes Ultracode away from anything but Claude Code; the
// profile is not sent with it, because a form whose profiles have not been
// read yet would send "the default" for a profile nobody touched.
const PATCH_FIELDS = {
  harness: ['harness', 'model', 'effort', 'ultracode'],
  model: ['model', 'effort'],
  effort: ['effort'],
  profile: ['profile', 'profile_auto'],
  ultracode: ['ultracode'],
  dir: ['workdir_mode', 'workdir'],
};

/* --------------------------------------------------------------- mount */

/**
 * mountPrestart wires the panel to the page. ctx:
 *
 *   host             the element the panel is drawn in (#prestart)
 *   replace(row)     a newer row of the session from the server
 *   started(row, e)  the session started: attach it the way a new one is
 *   refresh()        read the session list again now
 *   measure()        the {cols, rows} the terminal it becomes will have
 *   nameFromPrompt(session, text)
 *                    name the session after the prompt written so far
 *
 * It hands back show/update/hide for the page, and field(), which is where
 * the keyboard switch raises the keyboard when nothing else had the focus.
 */
export function mountPrestart(ctx) {
  let panel = null;

  return {
    /** show draws the panel for a prepared session, or updates the one drawn. */
    show(session) {
      if (panel && panel.id === session.id) { panel.update(session); return; }
      if (panel) panel.close();
      panel = openPanel(ctx, session);
    },
    /** update hands the panel a newer row of the session it shows. */
    update(session) {
      if (panel && panel.id === session.id) panel.update(session);
    },
    hide() {
      if (!panel) return;
      panel.close();
      panel = null;
    },
    /** shownId is the session the panel is drawn for, or ''. */
    shownId() { return panel ? panel.id : ''; },
    field() { return panel ? panel.field : null; },
  };
}

/**
 * openPanel draws one session's panel and runs it until close(). Everything
 * it keeps is in this closure, so a request still in flight for a panel that
 * was closed cannot reach the next one.
 */
function openPanel(ctx, first) {
  const id = first.id;
  const path = '/api/sessions/' + encodeURIComponent(id);
  const host = ctx.host;
  let session = first;

  const st = {
    closed: false,
    // The draft: the revision the text on screen is based on, and the text
    // the server holds at it - null while that is not known.
    loaded: false,
    loadError: '',
    rev: 0,
    saved: null,
    saving: null,
    again: false,
    offline: false,
    error: '',
    tooLong: false,
    conflict: null,
    saveTimer: null,
    retryTimer: null,
    retryDelay: 0,
    // The settings: what is waiting to be sent, and what is on its way.
    patch: {},
    patchTimer: null,
    patching: null,
    patchError: '',
    patchOffline: false,
    applied: '',
    // A start in progress, and the timers that watch a scheduled one.
    starting: false,
    // Whether something has been written in the field here, and whether the
    // session has been named after it since: that happens once.
    wrote: false,
    named: false,
    dueTimers: [],
    tick: null,
  };

  /* ----------------------------------------------------------- markup */

  // The page: a head that says what this is, the failure of the last start
  // when there was one, and the two columns - the prompt and Start in the
  // main one, the time and the settings at the side (under the prompt on a
  // phone, where the prompt is what the screen is for).
  const textarea = el('textarea', {
    class: 'ds-input so-prestart__text', id: 'psPrompt', rows: '14', spellcheck: 'true',
    placeholder: 'What should it do? This is sent as the first prompt the moment the session starts.',
    'aria-describedby': 'psSave',
  });
  // Read-only until the draft has loaded: a keystroke before that has no
  // revision to be saved on, and the text that arrives would replace it. It
  // is readOnly rather than disabled so the panel can still put the caret
  // there the moment it opens.
  textarea.readOnly = true;
  // Dictation lands where the cursor is, rather than replacing the prompt
  // with one sentence of it.
  const promptRow = dictated(textarea, textarea, (text) => insertText(text));
  promptRow.classList.add('so-prestart__prompt-row');
  const saveLine = el('p', { class: 'so-prestart__status', id: 'psSave', role: 'status', 'aria-live': 'polite' });
  const conflictBox = el('div', { class: 'so-prestart__conflict', id: 'psConflict', hidden: true });
  const failedBox = el('div', { class: 'ds-alert ds-alert--error', id: 'psFailed', role: 'alert', hidden: true });
  const startButton = el('button', {
    class: 'ds-btn ds-btn--primary so-prestart__start', type: 'button', id: 'psStart',
    onclick: () => start(),
  });
  morph(startButton, faces('Start', 'Starting'));
  const keyHint = el('span', {
    class: 'ds-meta so-prestart__key',
    text: (/Mac|iPhone|iPad/.test(navigator.platform || '') ? '⌘' : 'Ctrl') + '+Enter',
  });
  const chip = el('span', { class: 'ds-status', id: 'psState' });

  const settingsHost = el('div', { class: 'so-prestart__settings' }, el('p', { class: 'ds-muted', text: 'Loading…' }));
  const settingsLine = el('p', { class: 'so-prestart__status', id: 'psSettingsStatus', role: 'status' });

  const startAt = el('input', {
    class: 'ds-input', type: 'datetime-local', id: 'psStartAt', 'aria-describedby': 'psStartAtHint',
  });
  const clearAt = el('button', {
    class: 'ds-btn', type: 'button', id: 'psStartAtClear', text: 'Clear', onclick: () => schedule(0),
  });
  const atHint = el('small', { class: 'ds-field__hint', id: 'psStartAtHint' });

  host.replaceChildren(el('div', { class: 'ds-content' },
    el('div', { class: 'ds-page' },
      el('div', { class: 'so-page-head' },
        el('header', { class: 'ds-page-head' },
          el('h2', { class: 'ds-page-head__title', text: 'PreStart' }),
          el('p', { class: 'ds-page-head__lead', text: 'Nothing runs yet. It starts with these settings and this prompt.' })),
        chip),
      failedBox,
      el('div', { class: 'so-prestart__grid' },
        el('section', { class: 'ds-card ds-form so-prestart__main', 'aria-label': 'First prompt' },
          el('div', { class: 'ds-field' },
            el('label', { class: 'ds-field__label', for: 'psPrompt', text: 'First prompt' }),
            promptRow,
            saveLine),
          conflictBox,
          el('div', { class: 'ds-form-actions' }, startButton, keyHint)),
        el('div', { class: 'so-prestart__side' },
          el('section', { class: 'ds-card ds-form', 'aria-labelledby': 'psScheduleTitle' },
            el('h3', { class: 'ds-card__title', id: 'psScheduleTitle', text: 'Start automatically' }),
            el('div', { class: 'ds-field', id: 'psScheduleField' },
              el('label', { class: 'ds-field__label', for: 'psStartAt', text: 'Date and time' }),
              el('div', { class: 'so-field-row' }, startAt, clearAt),
              atHint)),
          el('section', { class: 'ds-card so-prestart__settings-card', 'aria-labelledby': 'psSettingsTitle' },
            el('h3', { class: 'ds-card__title', id: 'psSettingsTitle', text: 'Settings' }),
            settingsHost,
            settingsLine))))));
  host.hidden = false;

  // setAtHint is the line under the time: what is set, or why it is not.
  // An error wears the icon DS gives a field's error, and marks the field.
  function setAtHint(text, error = false) {
    atHint.classList.toggle('ds-field__hint--error', error);
    if (error) startAt.setAttribute('aria-invalid', 'true');
    else startAt.removeAttribute('aria-invalid');
    atHint.replaceChildren(...(error ? [iconEl('alertCircle', 16)] : []), text);
  }

  // drawLine writes one of the two status lines (the prompt's, the
  // settings'): the words and, for anything but "on its way", an icon, so
  // that the state is never told by colour alone.
  function drawLine(line, text, kind, extra) {
    const glyph = { ok: 'checkCircle', offline: 'wifiOff', error: 'alertCircle', conflict: 'alert' }[kind];
    line.dataset.kind = kind;
    line.replaceChildren(...(glyph && text ? [iconEl(glyph, 16)] : []), ...(text ? [el('span', { text })] : []),
      ...(extra ? [extra] : []));
  }

  /* ------------------------------------------------------------- form */

  let form = null;
  harnesses.load().catch(() => { /* the stored copy, or the form says it has none */ }).then(() => {
    if (st.closed) return;
    form = harnesses.settingsForm({
      prefix: 'ps',
      include: (entry) => harnesses.canPrestart(entry.id),
      deferredProfile: () => true,
      onChange: (kind) => settingsChanged(kind),
    });
    form.selectDefault();
    applySettings(true);
    settingsHost.replaceChildren(form.node);
    drawSettingsLine();
    form.refreshProfiles().then(() => { if (!st.closed) applySettings(true); });
  });

  /* ------------------------------------------------------------ events */

  const onInput = () => {
    st.wrote = true;
    grow();
    st.tooLong = false;
    // The copy on this device first: from here on the text survives a tab
    // that is closed, a phone that runs out of battery, a page that reloads.
    if (st.loaded) writeBuffer(id, textarea.value, st.rev);
    drawStatus();
    if (st.conflict) return;
    clearTimeout(st.saveTimer);
    st.saveTimer = setTimeout(() => save(), DRAFT_DEBOUNCE);
  };
  textarea.addEventListener('input', onInput);
  textarea.addEventListener('blur', () => nameFromPrompt());
  host.addEventListener('keydown', onKey);
  startAt.addEventListener('change', () => {
    if (!startAt.value) { schedule(0); return; }
    const at = new Date(startAt.value).getTime();
    if (!Number.isFinite(at) || at <= Date.now() + 5000) {
      setAtHint('Pick a time in the future.', true);
      return;
    }
    schedule(at);
  });
  const unwake = onWake(() => retryNow());
  // The words under the schedule are a distance from now, which changes
  // while the panel is open.
  st.tick = setInterval(() => drawSchedule(), 30000);

  function onKey(event) {
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey) && !event.isComposing) {
      event.preventDefault();
      start();
    }
  }

  /* ------------------------------------------------------------- draft */

  function dirty() { return st.saved === null || textarea.value !== st.saved; }

  // The first time the field is left after something was written in it, the
  // session is named after its prompt, so that the sidebar says roughly what
  // it is for before it has ever run. Once per panel, and never over a name
  // somebody gave it; the arrow beside the name does it again on request.
  function nameFromPrompt() {
    if (st.named || !st.wrote || session.title_source || !textarea.value.trim()) return;
    st.named = true;
    if (ctx.nameFromPrompt) ctx.nameFromPrompt(session, textarea.value);
  }

  function grow() {
    // Auto-growing: the field is as tall as what is in it, and the panel
    // scrolls, so a long prompt is read in one place rather than in a box.
    textarea.style.height = 'auto';
    textarea.style.height = (textarea.scrollHeight + 2) + 'px';
  }

  function insertText(text) {
    // Dictation into a field that has not loaded would be overwritten by
    // what loads; the microphone waits like the keyboard does.
    if (!st.loaded || textarea.readOnly) { toast('The prompt is still loading.', 'error'); return; }
    const value = textarea.value;
    const from = Number.isInteger(textarea.selectionStart) ? textarea.selectionStart : value.length;
    const to = Number.isInteger(textarea.selectionEnd) ? textarea.selectionEnd : from;
    const before = value.slice(0, from);
    const gap = before && !/\s$/.test(before) ? ' ' : '';
    textarea.value = before + gap + text + value.slice(to);
    const caret = from + gap.length + text.length;
    try { textarea.setSelectionRange(caret, caret); } catch { /* not focusable right now */ }
    textarea.dispatchEvent(new Event('input', { bubbles: true }));
  }

  function setText(text) {
    const focused = document.activeElement === textarea;
    const caret = textarea.selectionStart;
    textarea.value = text;
    if (focused && Number.isInteger(caret)) {
      const at = Math.min(caret, text.length);
      try { textarea.setSelectionRange(at, at); } catch { /* fine */ }
    }
    grow();
  }

  async function load() {
    if (st.loading) return;
    st.loading = true;
    const local = readBuffer(id);
    st.loadError = '';
    drawStatus();
    const res = await call('GET', path + '/draft');
    st.loading = false;
    if (st.closed) return;
    if (res.status === 200) {
      const server = { text: String(res.data.text || ''), rev: Number(res.data.rev) || 0 };
      st.loaded = true;
      st.rev = server.rev;
      st.saved = server.text;
      st.retryDelay = 0;
      textarea.disabled = false;
      textarea.readOnly = false;
      if (local && local.text !== server.text) {
        setText(local.text);
        // What this device kept was written on the revision the server still
        // has: it is simply newer, and goes up now. Written on an older one,
        // somebody else saved in between, and the person chooses.
        if (local.base_rev === server.rev) save();
        else enterConflict(server);
      } else {
        setText(server.text);
        forgetDraft(id);
      }
      drawStatus();
      return;
    }
    if (res.status === 404 || res.status === 409) { ctx.refresh(); return; }
    if (local) {
      // No answer, but this device has the text and the revision it was
      // written on: it can be worked on, and it goes up when it can.
      st.loaded = true;
      st.rev = local.base_rev;
      st.saved = null;
      setText(local.text);
      textarea.disabled = false;
      textarea.readOnly = false;
      st.offline = res.status === 0;
      st.error = res.status === 0 ? '' : (res.data.error || 'HTTP ' + res.status);
      drawStatus();
      retryLater();
      return;
    }
    // Nothing to show and nothing to base an edit on: anything typed now
    // would be saved over a prompt this page has never seen.
    textarea.disabled = true;
    st.loadError = res.status === 0
      ? 'Offline – the prompt loads when the connection is back.'
      : 'The prompt could not be loaded: ' + (res.data.error || 'HTTP ' + res.status);
    drawStatus();
    retryLater();
  }

  // save sends the text on screen, once at a time; a change made while one
  // is on its way is sent after it.
  function save() {
    clearTimeout(st.saveTimer);
    st.saveTimer = null;
    if (!st.loaded || st.conflict) return Promise.resolve();
    if (st.saving) { st.again = true; return st.saving; }
    const text = textarea.value;
    if (!dirty()) { forgetDraft(id); drawStatus(); return Promise.resolve(); }
    if (encoder.encode(text).length > MAX_DRAFT_BYTES) {
      st.tooLong = true;
      drawStatus();
      return Promise.resolve();
    }
    drawStatus();
    st.saving = (async () => {
      const res = await call('PUT', path + '/draft', { text, base_rev: st.rev });
      st.saving = null;
      if (res.status === 200) {
        acknowledged(text, Number(res.data.rev) || st.rev + 1);
      } else if (res.status === 409 && res.data.conflict) {
        const theirs = { text: String(res.data.text || ''), rev: Number(res.data.rev) || 0 };
        // Two devices that typed the same thing do not disagree.
        if (theirs.text === text) acknowledged(text, theirs.rev);
        else if (!st.closed) enterConflict(theirs);
      } else if (res.status === 409 || res.status === 404) {
        // Started, from here or anywhere else, or deleted: its prompt is the
        // one it started with now. The list says what it became.
        // Whether text was lost is decided from what this save carried, not
        // from the panel: a scheduled start closes the panel before the
        // answer comes back, and that is exactly when it must be said.
        forgetDraft(id);
        if (res.status === 409 && text !== (st.saved || '')) {
          toast('The session had already started; the last changes to its prompt were not sent.', 'error');
        }
        if (!st.closed) ctx.refresh();
        return;
      } else if (res.status === 413) {
        st.tooLong = true;
      } else {
        st.offline = res.status === 0;
        st.error = res.status === 0 ? '' : (res.data.error || 'HTTP ' + res.status);
        if (!st.closed) retryLater();
      }
      if (st.closed) {
        // Typed on while this was on its way, and closed since: the rest
        // goes up too, rather than waiting in this device for a panel that
        // may never be opened again.
        if (st.again && dirty() && !st.conflict) { st.again = false; save(); }
        return;
      }
      drawStatus();
      if (st.again) {
        st.again = false;
        if (dirty() && !st.conflict) save();
      }
    })();
    return st.saving;
  }

  function acknowledged(text, rev) {
    st.rev = rev;
    st.saved = text;
    st.offline = false;
    st.error = '';
    st.retryDelay = 0;
    clearTimeout(st.retryTimer);
    if (textarea.value === text) {
      forgetDraft(id);
    } else {
      // Typed on while it was on its way: the copy here is now based on the
      // revision just made, and the rest follows.
      writeBuffer(id, textarea.value, rev);
      if (!st.closed && !st.saveTimer) st.saveTimer = setTimeout(() => save(), DRAFT_DEBOUNCE);
    }
  }

  function retryLater() {
    clearTimeout(st.retryTimer);
    st.retryDelay = Math.min(RETRY_MAX, st.retryDelay ? st.retryDelay * 2 : RETRY_MIN);
    st.retryTimer = setTimeout(() => retryNow(), st.retryDelay);
  }

  // retryNow is the network coming back, or the page being looked at again:
  // whatever is waiting is tried at once.
  function retryNow() {
    if (st.closed) return;
    clearTimeout(st.retryTimer);
    if (!st.loaded) { load(); return; }
    if ((st.offline || st.error) && dirty() && !st.conflict) save();
    if (st.patchOffline && Object.keys(st.patch).length) sendPatch();
  }

  // refetch reads the draft again because the list says another device
  // saved a newer one. Only a panel with nothing unsaved takes it: one that
  // has is about to find out by its own save, and to ask.
  async function refetch() {
    if (st.refetching) return;
    st.refetching = true;
    const res = await call('GET', path + '/draft');
    st.refetching = false;
    if (st.closed || res.status !== 200) return;
    refetched(res.data);
  }

  function refetched(data) {
    if (dirty() || st.saving || st.conflict) return;
    const rev = Number(data.rev) || 0;
    if (rev < st.rev) return;
    st.rev = rev;
    st.saved = String(data.text || '');
    setText(st.saved);
    drawStatus();
  }

  function enterConflict(theirs) {
    clearTimeout(st.saveTimer);
    st.conflict = theirs;
    const preview = theirs.text.length > 400 ? theirs.text.slice(0, 400) + '…' : theirs.text;
    conflictBox.replaceChildren(
      el('div', { class: 'ds-alert ds-alert--warning', role: 'alert' },
        iconEl('alert', 20),
        el('span', { class: 'ds-alert__text', text: 'This prompt was changed on another device while this one was editing it. Choose which text to keep.' })),
      el('div', { class: 'so-prestart__theirs' },
        el('p', { class: 'so-prestart__theirs-label', id: 'psTheirsLabel', text: 'The other device’s text' }),
        el('pre', { class: 'so-prestart__theirs-text', 'aria-labelledby': 'psTheirsLabel', tabindex: '0', text: preview || '(empty)' })),
      // Tonal, not filled: Start stays the one filled button of the card,
      // even while it waits for this choice.
      el('div', { class: 'ds-form-actions' },
        el('button', {
          class: 'ds-btn ds-btn--tonal', type: 'button', id: 'psKeepMine', text: 'Keep mine',
          onclick: () => resolveConflict(true),
        }),
        el('button', {
          class: 'ds-btn', type: 'button', id: 'psUseTheirs', text: 'Use the other device’s text',
          onclick: () => resolveConflict(false),
        })));
    conflictBox.hidden = false;
    drawStatus();
  }

  function resolveConflict(keepMine) {
    const theirs = st.conflict;
    if (!theirs) return;
    st.conflict = null;
    conflictBox.hidden = true;
    conflictBox.replaceChildren();
    st.rev = theirs.rev;
    st.saved = theirs.text;
    if (keepMine) {
      // Mine, written over theirs: the same save, on the revision theirs made.
      writeBuffer(id, textarea.value, st.rev);
      save();
    } else {
      setText(theirs.text);
      forgetDraft(id);
    }
    drawStatus();
  }

  function drawStatus() {
    let text = '';
    let kind = '';
    if (!st.loaded) {
      text = st.loadError || 'Loading…';
      kind = st.loadError ? 'offline' : '';
    } else if (st.conflict) {
      text = 'Changed on another device – choose which text to keep.';
      kind = 'conflict';
    } else if (st.tooLong) {
      text = 'Too long to save: a prompt can be at most ' + (MAX_DRAFT_BYTES >> 10) + ' KB. Kept on this device.';
      kind = 'error';
    } else if (dirty() && st.offline) {
      text = 'Offline – kept on this device';
      kind = 'offline';
    } else if (dirty() && st.error) {
      text = 'Not saved yet – kept on this device';
      kind = 'error';
    } else if (dirty()) {
      text = 'Saving…';
    } else {
      text = 'Saved';
      kind = 'ok';
    }
    // The server's own sentence is a fact about the machine, and it goes
    // behind an "i" like every other.
    drawLine(saveLine, text, kind, st.loaded && dirty() && st.error && !st.offline
      ? infoTip([st.error], { label: 'Why it was not saved' }) : null);
    drawStart();
  }

  /* ---------------------------------------------------------- settings */

  // applySettings draws the row's settings into the form - unless the person
  // is in the middle of changing them, when what they are doing wins and the
  // row will be the answer to it in a moment.
  function applySettings(force = false) {
    if (!form) return;
    const values = settingsOf(session);
    const signature = JSON.stringify(values);
    const busy = Object.keys(st.patch).length || st.patching
      || (form.node.contains(document.activeElement) && !force);
    if (busy || (signature === st.applied && !force)) return;
    st.applied = signature;
    form.set(values);
  }

  function settingsChanged(kind) {
    const value = form.value();
    for (const key of PATCH_FIELDS[kind] || []) st.patch[key] = value[key];
    st.patchError = '';
    drawSettingsLine();
    clearTimeout(st.patchTimer);
    st.patchTimer = setTimeout(() => sendPatch(), SETTINGS_DEBOUNCE);
  }

  function schedule(at) {
    st.patch.start_at = at;
    if (!at) startAt.value = '';
    setAtHint(atHint.textContent);
    drawSettingsLine();
    clearTimeout(st.patchTimer);
    st.patchTimer = setTimeout(() => sendPatch(), at ? SETTINGS_DEBOUNCE : 0);
  }

  function sendPatch() {
    clearTimeout(st.patchTimer);
    st.patchTimer = null;
    if (st.patching) return st.patching.then(() => sendPatch());
    const body = st.patch;
    if (!Object.keys(body).length) return Promise.resolve();
    st.patch = {};
    st.patching = (async () => {
      const res = await call('PATCH', path + '/prestart', body);
      st.patching = null;
      if (res.status === 200 && res.data.session) {
        st.patchError = '';
        st.patchOffline = false;
        if (!st.closed) ctx.replace(res.data.session);
      } else if (res.status === 0) {
        // Kept, under whatever was changed since, and sent when the
        // connection is back.
        st.patch = { ...body, ...st.patch };
        st.patchOffline = true;
        if (!st.closed) retryLater();
      } else if (res.status === 409 || res.status === 404) {
        if (!st.closed) ctx.refresh();
      } else if ('start_at' in body && Object.keys(body).length === 1) {
        // A refused time is said under the time, and the field goes back to
        // saying what is really set.
        if (!st.closed) {
          drawSchedule(true);
          setAtHint('Not saved: ' + (res.data.error || 'HTTP ' + res.status), true);
        }
      } else {
        st.patchError = res.data.error || 'HTTP ' + res.status;
        if ('start_at' in body && !st.closed) drawSchedule(true);
      }
      if (st.closed) return;
      drawSettingsLine();
      if (!Object.keys(st.patch).length) applySettings();
    })();
    return st.patching;
  }

  function drawSettingsLine() {
    let text = '';
    let kind = '';
    if (st.patchError) { text = 'Not saved: ' + st.patchError; kind = 'error'; }
    else if (st.patchOffline) { text = 'Offline – the settings are saved when the connection is back.'; kind = 'offline'; }
    else if (Object.keys(st.patch).length || st.patching) text = 'Saving…';
    drawLine(settingsLine, text, kind);
  }

  /* ---------------------------------------------------------- schedule */

  function drawSchedule(force = false) {
    const at = Number(session.start_at) || 0;
    const editing = document.activeElement === startAt || 'start_at' in st.patch;
    if (force || !editing) startAt.value = at ? localInput(at) : '';
    // The earliest a time can be picked is now; the server refuses anything
    // before it anyway.
    startAt.min = localInput(Date.now());
    clearAt.hidden = !at && !startAt.value;
    const now = Date.now();
    if (!at) setAtHint('Not scheduled. Pick a date and time and it starts by itself then.');
    else if (at > now) {
      setAtHint('Starts by itself ' + inWords(at - now) + ' (' + new Date(at).toLocaleString(undefined,
        { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) + ').');
    } else setAtHint('Due now – starting…');
    drawChip();
    watchDue(at);
  }

  // watchDue reads the list right after a scheduled start falls due, so the
  // panel turns into the terminal the moment the server has started it.
  //
  // The time is only remembered once the timers are armed: a schedule set
  // hours ahead is armed by the 30 s tick that finds it within reach.
  function watchDue(at) {
    if (st.dueAt === at) return;
    for (const timer of st.dueTimers) clearTimeout(timer);
    st.dueTimers = [];
    st.dueAt = 0;
    const wait = at - Date.now();
    if (!at || wait > SCHEDULE_WATCH) return;
    st.dueAt = at;
    for (const after of [1500, 5000, 12000, 25000]) {
      st.dueTimers.push(setTimeout(() => { if (!st.closed) ctx.refresh(); }, Math.max(0, wait) + after));
    }
  }

  /* ------------------------------------------------------------- start */

  function drawFailure() {
    const reason = session.fail_reason || '';
    failedBox.hidden = !reason;
    if (!reason) { failedBox.replaceChildren(); return; }
    failedBox.replaceChildren(
      iconEl('alertCircle', 20),
      el('span', { class: 'ds-alert__text', text: 'The last start did not work. Nothing ran; the settings and the prompt are kept.' }),
      infoTip([reason], { label: 'Why it did not start', code: true }));
    drawChip();
  }

  // drawChip is the status chip in the head: what kind of waiting this is.
  // Icon and word, never colour alone.
  function drawChip() {
    const at = Number(session.start_at) || 0;
    let kind = '';
    let glyph = 'prestart';
    let text = 'Not started';
    if (session.fail_reason) { kind = 'error'; glyph = 'alertCircle'; text = 'Start failed'; }
    else if (at) { glyph = 'clock'; text = 'Starts ' + whenShort(at); }
    chip.className = kind ? 'ds-status ds-status--' + kind : 'ds-status';
    chip.replaceChildren(iconEl(glyph, 16), el('span', { text }));
  }

  function drawStart() {
    startButton.disabled = st.starting || !st.loaded || !!st.conflict || st.tooLong;
    setFaceLabel(startButton, Number(session.start_at) ? 'Start now' : 'Start');
    // Busy shows the spinner face (after DS's loader delay, so a quick start
    // shows nothing) and blocks the button; the outcome is said by a toast
    // or by the terminal that takes the panel's place.
    if (st.starting) startButton.setAttribute('aria-busy', 'true');
    else startButton.removeAttribute('aria-busy');
  }

  async function start() {
    if (st.starting || st.closed) return;
    if (st.conflict) { toast('Choose which text to keep first.'); return; }
    if (!st.loaded) { toast('The prompt has not loaded yet.'); return; }
    st.starting = true;
    if (form) form.node.inert = true;
    drawStart();
    try {
      // What is on screen is what it starts with: the settings and the text
      // still waiting for a pause are sent first.
      for (let i = 0; i < 3 && (Object.keys(st.patch).length || st.patching); i += 1) await sendPatch();
      if (Object.keys(st.patch).length || st.patchError) {
        toast(st.patchOffline ? 'No connection – the session was not started.'
          : 'The settings could not be saved, so the session was not started.', 'error');
        return;
      }
      for (let i = 0; i < 3 && dirty() && !st.conflict && !st.tooLong; i += 1) await save();
      if (dirty() || st.conflict) {
        toast(st.offline ? 'No connection – the session was not started.'
          : 'The prompt could not be saved, so the session was not started.', 'error');
        return;
      }
      const size = ctx.measure() || {};
      const res = await call('POST', path + '/start', {
        cols: size.cols || undefined, rows: size.rows || undefined,
      }, 60000);
      if (st.closed) return;
      if (res.status === 200 && res.data.session) {
        forgetDraft(id);
        ctx.started(res.data.session, res.data.error || '');
        return;
      }
      // Not started: the row says why, and it is still the prepared session
      // it was, prompt and all.
      if (res.data && res.data.session) ctx.replace(res.data.session);
      toast(res.status === 0 ? 'No connection – the session was not started.' : 'The session could not start.', 'error');
    } finally {
      st.starting = false;
      if (form) form.node.inert = false;
      if (!st.closed) drawStart();
    }
  }

  /* ------------------------------------------------------------ update */

  function update(next) {
    session = next;
    applySettings();
    drawSchedule();
    drawFailure();
    drawStart();
    // Another device saved: take it while there is nothing here to lose.
    if (st.loaded && Number(next.draft_rev || 0) > st.rev && !dirty() && !st.saving && !st.conflict) refetch();
  }

  function close() {
    if (st.closed) return;
    // What was waiting for a pause is sent now rather than dropped. The copy
    // on this device stays until the server has it, whatever happens here.
    if (st.patchTimer && Object.keys(st.patch).length) sendPatch();
    // That includes text waiting for a retry, not only for the debounce:
    // the answer is then either saved, or "started" with a warning that it
    // was not sent - never silence. Offline, it stays on this device.
    if (st.loaded && dirty() && !st.conflict && !st.tooLong) save();
    // A panel left for another session counts as the field being left.
    nameFromPrompt();
    st.closed = true;
    clearTimeout(st.saveTimer);
    clearTimeout(st.retryTimer);
    clearTimeout(st.patchTimer);
    clearInterval(st.tick);
    for (const timer of st.dueTimers) clearTimeout(timer);
    unwake();
    host.removeEventListener('keydown', onKey);
    host.hidden = true;
    host.replaceChildren();
  }

  drawSchedule();
  drawFailure();
  drawStatus();
  load();

  return { id, field: textarea, update, close };
}
