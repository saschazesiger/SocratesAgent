// A chat's artifacts on its page (docs/design/ARTIFACTS.md, "The chat
// page"): the Artifacts button in the top bar with its badge, the panel that
// lists them (a column beside the chat at a desk, a side sheet narrower),
// and the page view of one artifact: the column turns into the page and
// widens, or, narrower, a full screen dialog. The page itself runs in a
// sandboxed frame of its own opaque origin, served by /artifacts/.
//
// The artifacts are the agent's, made through the socrates-artifacts MCP
// server; the page reads them from /api/chats/{id}/artifacts and reads them
// again when an artifacts frame names the chat on screen (events.js). The
// badge counts what this device has not shown yet: the latest version shown
// of each artifact is remembered per device and chat (SEEN_KEY).

import { api, el, toast, errorMessage, confirmDialog } from './api.js';
import { M, html, raw, icon, morph, flipMorph, showOverlay, hideOverlay } from './ui.js';
import { clock } from './chatparts.js';
import { agoText } from './mcpview.js';

// Over this width the panel is a column beside the chat; narrower it is a
// side sheet over it, and a page opens in a full screen dialog.
const WIDE = window.matchMedia('(min-width: 1101px)');
// Whether the column is open on this device: 'open' or 'shut'.
const PANEL_PREF = 'socrates.artifacts.panel';
// What this device has shown: {chat: {at, ids: {A1: 3}}}, the latest version
// shown of each artifact. Kept for the chats touched last, no more.
const SEEN_KEY = 'socrates.artifacts.seen';
const SEEN_CHATS = 60;
// The frames of one burst of changes are read as one.
const GATHER = 250;
// How often "2 min ago" is worked out again while the list is on screen.
const TICK = 60 * 1000;
// What a page may do in its frame: run scripts, open a link in a tab of its
// own, a form, an alert, a download; never be of Socrates' origin.
const SANDBOX = 'allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals allow-downloads';

/* ------------------------------------------------------------ seen state */

function readSeen() {
  try {
    const all = JSON.parse(localStorage.getItem(SEEN_KEY) || '{}');
    return all && typeof all === 'object' && !Array.isArray(all) ? all : {};
  } catch {
    return {};
  }
}

function writeSeen(all) {
  const chats = Object.keys(all);
  if (chats.length > SEEN_CHATS) {
    chats.sort((a, b) => (all[b].at || 0) - (all[a].at || 0));
    for (const chat of chats.slice(SEEN_CHATS)) delete all[chat];
  }
  try { localStorage.setItem(SEEN_KEY, JSON.stringify(all)); } catch { /* a private window */ }
}

/** seenOf is what this device has shown of a chat's artifacts: {A1: 3}. */
function seenOf(chat) {
  const entry = readSeen()[chat];
  return (entry && entry.ids && typeof entry.ids === 'object') ? entry.ids : {};
}

/** markSeen remembers that an artifact's version was shown; true when that is news. */
function markSeen(chat, id, version) {
  const all = readSeen();
  const entry = all[chat] && all[chat].ids ? all[chat] : { ids: {} };
  if ((Number(entry.ids[id]) || 0) >= version) return false;
  entry.ids[id] = version;
  entry.at = Date.now();
  all[chat] = entry;
  writeSeen(all);
  return true;
}

/** pruneSeen forgets the artifacts a chat no longer has. */
function pruneSeen(chat, artifacts) {
  const all = readSeen();
  const entry = all[chat];
  if (!entry || !entry.ids) return;
  const ids = new Set(artifacts.map((a) => a.id));
  let changed = false;
  for (const id of Object.keys(entry.ids)) {
    if (!ids.has(id)) { delete entry.ids[id]; changed = true; }
  }
  if (!Object.keys(entry.ids).length) { delete all[chat]; changed = true; }
  if (changed) writeSeen(all);
}

/* -------------------------------------------------------------- helpers */

/** freshness is 'new' (never shown here), 'changed' (an older version shown) or ''. */
function freshness(a, seen) {
  const shown = Number(seen[a.id]) || 0;
  if (!shown) return 'new';
  return shown < (Number(a.version) || 0) ? 'changed' : '';
}

const latestOf = (a) => {
  const versions = a.versions || [];
  return versions[versions.length - 1] || null;
};

/** frameSrc is the address of a version's page: the artifact's url, with ?v=N for an older one. */
function frameSrc(a, pinned) {
  return pinned ? a.url + '?v=' + encodeURIComponent(pinned) : a.url;
}

/** versionWords is one version for the select: "Version 2 · 14:32 · Colours fixed". */
function versionWords(v, latest) {
  const parts = ['Version ' + v.n + (v.n === latest ? ' (latest)' : ''), clock(v.at)];
  if (v.note) parts.push(v.note);
  return parts.join(' · ');
}

let pageSeq = 0;

/**
 * pageView builds the parts of one artifact's page view, shared by the
 * column and the dialog: the actions (Open in a new tab, Delete), the
 * version line (a select when there are several), and the frame. It does
 * not place them. `set(artifact)` draws an artifact (again: a new version
 * reloads the frame unless an older one is picked); it answers
 * { id, set, actions, version, frame, dispose() }.
 */
function pageView({ id, boxed }) {
  const n = ++pageSeq;
  let art = null;
  let pinned = null;
  let loadedKey = '';

  const open = el('a', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon so-artpage__open', target: '_blank', rel: 'noopener',
    'aria-label': 'Open in a new tab', 'data-tip': 'Open in a new tab', 'data-art-act': 'open',
  });
  morph(open, html`${raw(icon('external', 20))}`);
  const del = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon so-artpage__delete', type: 'button',
    'aria-label': 'Delete the artifact', 'data-tip': 'Delete the artifact', 'data-art-act': 'delete',
  });
  morph(del, html`${raw(icon('trash', 20))}`);
  const version = el('div', { class: 'so-artpage__version' });
  const frame = el('div', { class: 'so-artpage__frame' + (boxed ? ' so-artpage__frame--boxed' : '') });

  version.addEventListener('change', (event) => {
    const select = event.target.closest('select');
    if (!select || !art) return;
    const picked = Number(select.value);
    pinned = picked === art.version ? null : picked;
    draw();
  });

  function draw() {
    if (!art) return;
    const versions = art.versions || [];
    // A version picked that is no longer kept: the latest again.
    if (pinned !== null && !versions.some((v) => v.n === pinned)) pinned = null;
    open.setAttribute('href', frameSrc(art, pinned));
    const shownN = pinned || art.version;
    if (versions.length > 1) {
      const selectId = 'artVersion' + n;
      morph(version, html`<label class="ds-sr" for="${selectId}">Version</label><select class="ds-input so-artpage__select" id="${selectId}">${[...versions].reverse().map((v) => html`<option value="${v.n}">${versionWords(v, art.version)}</option>`)}</select>`);
      version.querySelector('select').value = String(shownN);
    } else {
      const v = latestOf(art);
      morph(version, html`<p class="ds-meta so-artpage__meta">${v ? versionWords(v, 0) : 'Version ' + art.version}</p>`);
    }
    // The frame: a new element for each page loaded, so a new version is
    // no navigation inside the old frame (which the browser's Back would
    // step through).
    const src = frameSrc(art, pinned);
    const key = src + '#' + (pinned || art.version);
    if (key !== loadedKey) {
      loadedKey = key;
      const iframe = el('iframe', {
        class: 'so-artpage__iframe', sandbox: SANDBOX, src, title: art.title,
        referrerpolicy: 'no-referrer', 'data-version': String(shownN),
      });
      frame.replaceChildren(iframe);
    } else {
      const iframe = frame.querySelector('iframe');
      if (iframe && iframe.title !== art.title) iframe.title = art.title;
    }
  }

  return {
    id,
    actions: [open, del],
    version,
    frame,
    /** set draws the artifact as it is now. */
    set(next) {
      art = next;
      draw();
    },
    dispose() {
      frame.replaceChildren();
      loadedKey = '';
    },
  };
}

/**
 * mountChatArtifacts wires the Artifacts button, the column, the sheet and
 * the page dialog.
 *
 * `ctx`: button (the top bar's #artifactsBtn), panel (the column,
 * #artifactsPanel), mcpDialog(session, opener, { closed }) the chat's MCP
 * servers dialog, onColumnOpen() called when the column opens (the Tests
 * column shuts then).
 *
 * It answers { show(session), hide(), frame(frame), resync(), shut() }: a
 * chat on the stage, nothing on it, an artifacts frame, a hello (frames may
 * have been missed), and the column shut because another one opened.
 */
export function mountChatArtifacts(ctx) {
  const { button, panel } = ctx;
  const badge = button.querySelector('.ds-badge');

  let session = null;
  let artifacts = [];
  let on = false;
  let rev = null;
  let loaded = false;
  let kept = null;
  let gen = 0;
  let timer = null;
  let ticker = null;
  // The page the column shows ({view, id}), or null: the list.
  let colPage = null;
  // The page dialog of a narrow window ({view, dialog, id, close()}), or null.
  let dlg = null;
  // Artifacts the frames said were made, opened or announced once read.
  const made = new Set();
  // The artifact this page is deleting: its frame is no news.
  let deleting = '';

  /* ------------------------------------------------------------ the hosts */

  const back = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-sm so-artifacts__back', type: 'button', 'data-art-back': true,
    'aria-label': 'Back to the artifacts', 'data-tip': 'Back to the artifacts', hidden: true,
  });
  morph(back, html`${raw(icon('arrowLeft', 18))}`);
  const title = el('h2', { class: 'so-artifacts__title', id: 'artifactsPanelTitle', tabindex: '-1', text: 'Artifacts' });
  const headActions = el('div', { class: 'so-artifacts__actions' });
  const closeBtn = el('button', {
    class: 'ds-btn ds-btn--quiet ds-btn--icon ds-sm', type: 'button', 'data-art-close': true,
    'aria-label': 'Close the artifacts', 'data-tip': 'Close the artifacts',
  });
  morph(closeBtn, html`${raw(icon('close', 18))}`);
  const columnBody = el('div', { class: 'so-artifacts__content' });
  const listScroll = el('div', { class: 'so-artifacts__scroll' }, columnBody);
  const pageBox = el('div', { class: 'so-artpage', hidden: true });
  panel.replaceChildren(
    el('div', { class: 'so-artifacts__head' }, back, title, headActions, closeBtn),
    listScroll,
    pageBox);

  // The sheet, for a window too narrow for the column.
  const sheetBody = el('div', { class: 'so-artifacts__content' });
  const sheet = el('dialog', { class: 'ds-sheet so-artifacts-sheet', id: 'artifactsSheet', 'aria-labelledby': 'artifactsSheetTitle' },
    el('div', { class: 'ds-sheet__backdrop', 'data-ds-close': true }),
    el('section', { class: 'ds-sheet__panel' },
      el('div', { class: 'ds-sheet__head' },
        el('h2', { class: 'ds-sheet__title', id: 'artifactsSheetTitle', text: 'Artifacts' }),
        el('button', {
          class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-ds-close': true,
          'aria-label': 'Close', 'data-tip': 'Close',
        })),
      el('div', { class: 'ds-sheet__body', tabindex: '0' }, sheetBody)));
  morph(sheet.querySelector('.ds-sheet__head [data-ds-close]'), html`${raw(icon('close', 20))}`);
  document.body.append(sheet);

  const hosts = [makeHost(columnBody, 'c'), makeHost(sheetBody, 's')];

  function makeHost(root, key) {
    const top = el('div', { class: 'so-artifacts__top' });
    const list = el('ul', { class: 'ds-list ds-list--interactive so-artifacts__list', 'aria-label': 'Artifacts' });
    const rest = el('div', { class: 'so-artifacts__rest' });
    root.replaceChildren(top, list, rest);
    root.addEventListener('click', (event) => {
      const row = event.target.closest('[data-open]');
      if (row) {
        openPage(row.getAttribute('data-open'), row);
        return;
      }
      const mcp = event.target.closest('[data-art-mcp]');
      if (mcp && session) ctx.mcpDialog(session, mcp, { closed: () => schedule() });
    });
    return { key, root, top, list, rest };
  }

  const sheetOpen = () => !sheet.hidden && sheet.open && sheet.getAttribute('data-state') !== 'closed';

  function prefOpen() {
    try { return localStorage.getItem(PANEL_PREF) === 'open'; } catch { return false; }
  }

  function setPref(value) {
    try { localStorage.setItem(PANEL_PREF, value ? 'open' : 'shut'); } catch { /* a private window */ }
  }

  // available: the chat has the server on or has artifacts.
  const available = () => !!session && loaded && (on || artifacts.length > 0);
  const columnShown = () => available() && WIDE.matches && prefOpen();
  const find = (id) => artifacts.find((a) => a.id === id) || null;

  /** openColumn opens the column (the Tests column shuts). */
  function openColumn() {
    setPref(true);
    if (ctx.onColumnOpen) ctx.onColumnOpen();
  }

  /* -------------------------------------------------------------- drawing */

  function draw({ live = true } = {}) {
    drawButton();
    place();
    drawColumnPage();
    if (dlg) {
      const a = find(dlg.id);
      if (a) {
        dlg.set(a);
        seeShown(a);
      }
    }
    for (const host of hosts) drawHost(host, live);
    tick();
  }

  function unseenCount() {
    if (!session) return 0;
    const seen = seenOf(session.id);
    return artifacts.filter((a) => freshness(a, seen)).length;
  }

  function drawButton() {
    const show = available();
    button.hidden = !show;
    if (!show) return;
    const fresh = unseenCount();
    let label = 'Artifacts';
    if (!artifacts.length) label += ', none yet';
    else if (fresh) label += ': ' + fresh + ' new or changed';
    else label += ': ' + artifacts.length + ', all seen';
    if (button.getAttribute('aria-label') !== label) button.setAttribute('aria-label', label);
    if (button.getAttribute('data-tip') !== label) button.setAttribute('data-tip', label);
    const was = badge.hidden ? '' : badge.textContent;
    badge.textContent = fresh ? String(fresh) : '';
    badge.hidden = !fresh;
    if (fresh && !was) M.enter(badge, 'pop');
  }

  // place shows the column, or not, and points the button at what it opens.
  function place() {
    const column = columnShown();
    panel.hidden = !column;
    if (WIDE.matches) {
      button.setAttribute('aria-controls', 'artifactsPanel');
      button.removeAttribute('aria-haspopup');
      button.setAttribute('aria-expanded', column ? 'true' : 'false');
    } else {
      button.setAttribute('aria-controls', 'artifactsSheet');
      button.setAttribute('aria-haspopup', 'dialog');
      if (!sheetOpen()) button.setAttribute('aria-expanded', 'false');
    }
    if (!available()) {
      if (sheetOpen()) hideOverlay(sheet);
      if (dlg) dlg.close();
    }
  }

  function drawHost(host, live) {
    morph(host.top, html`${loaded && !on && artifacts.length ? html`<div class="ds-alert ds-tone-warning so-artifacts__off" role="status">${raw(icon('alert', 20))}<span class="ds-alert__text">Socrates Artifacts is off in this chat: its agent cannot make or change these pages.</span><button class="ds-btn ds-btn--text ds-sm" type="button" data-art-mcp>Switch it on</button></div>` : ''}`);
    const seen = session ? seenOf(session.id) : {};
    // The newest change first: what the agent made or changed last is what
    // the person looks for.
    const shown = [...artifacts].sort((a, b) => (Date.parse(b.updated_at) || 0) - (Date.parse(a.updated_at) || 0));
    flipMorph(host.list, html`${shown.map((a) => rowHtml(a, host.key, seen))}`, { items: '.so-artifact', live });
    let rest = '';
    if (!loaded) {
      rest = html`<div class="ds-loading" role="status"><span class="ds-loader ds-sm" aria-hidden="true"></span><span>Reading the artifacts…</span></div>`;
    } else if (!artifacts.length && !on) {
      rest = html`<div class="ds-empty so-artifacts__empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('presentation', 24))}</span><p class="ds-empty__title">Socrates Artifacts is off</p><p class="ds-empty__text">Switched on, this chat’s agent can make pages that show what words cannot: a diagram, a chart, a comparison. They appear here.</p><button class="ds-btn ds-btn--tonal" type="button" data-art-mcp>Switch it on…</button></div>`;
    } else if (!artifacts.length) {
      rest = html`<div class="ds-empty so-artifacts__empty"><span class="ds-empty__disc" aria-hidden="true">${raw(icon('presentation', 24))}</span><p class="ds-empty__title">No artifacts yet</p><p class="ds-empty__text">An artifact is a page the agent makes to show what words cannot: a diagram, a chart, a comparison, a timeline. Ask it in the chat: “Explain the architecture with a diagram.”</p></div>`;
    }
    morph(host.rest, html`${rest}`);
  }

  function rowHtml(a, key, seen) {
    const fresh = freshness(a, seen);
    const v = latestOf(a);
    const when = agoText(Date.parse((v && v.at) || a.updated_at) || 0);
    return html`<li class="ds-list__item so-artifact" data-ds-key="${a.id}" data-artifact="${a.id}" data-fresh="${fresh}">
      ${raw(icon(a.format === 'markdown' ? 'fileText' : 'fileCode', 20, 'ds-list__icon'))}
      <span class="ds-list__text">
        <span class="ds-list__title"><button class="ds-list__action" type="button" id="${'artRow-' + key + '-' + a.id}" data-open="${a.id}">${a.title}</button></span>
        ${a.description ? html`<span class="ds-list__meta so-artifact__desc">${a.description}</span>` : ''}
        <span class="ds-list__meta so-artifact__meta">${'Version ' + a.version + (when ? ' · ' + when : '')}</span>
      </span>
      ${fresh ? html`<span class="${'ds-tag so-artifact__tag' + (fresh === 'changed' ? ' ds-tone-neutral' : '')}">${fresh === 'new' ? 'New' : 'Changed'}</span>` : ''}
    </li>`;
  }

  // tick keeps "2 min ago" true while a list is on screen.
  function tick() {
    const showing = (columnShown() && !colPage) || sheetOpen();
    if (showing && !ticker) {
      ticker = setInterval(() => {
        if ((columnShown() && !colPage) || sheetOpen()) for (const host of hosts) drawHost(host, false);
        else { clearInterval(ticker); ticker = null; }
      }, TICK);
    }
  }

  /** seeShown remembers that an artifact's page is on screen; the badge follows. */
  function seeShown(a) {
    if (session && markSeen(session.id, a.id, Number(a.version) || 0)) {
      drawButton();
      for (const host of hosts) drawHost(host, false);
    }
  }

  /* ----------------------------------------------------- the column's page */

  function drawColumnPage() {
    const a = colPage ? find(colPage.id) : null;
    if (colPage && !a) {
      // The artifact went (deleted here or by the agent).
      closeColumnPage({ focus: false });
      return;
    }
    const paging = !!colPage;
    panel.toggleAttribute('data-page', paging);
    back.hidden = !paging;
    listScroll.hidden = paging;
    pageBox.hidden = !paging;
    const words = paging ? a.title : 'Artifacts';
    if (title.textContent !== words) title.textContent = words;
    if (!paging) {
      headActions.replaceChildren();
      return;
    }
    colPage.view.set(a);
    if (columnShown()) seeShown(a);
  }

  function showColumnPage(id, { focus = true } = {}) {
    if (colPage && colPage.id === id) {
      drawColumnPage();
      return;
    }
    if (colPage) colPage.view.dispose();
    const view = pageView({ id, boxed: false });
    colPage = { id, view };
    headActions.replaceChildren(...view.actions);
    pageBox.replaceChildren(el('div', { class: 'so-artpage__bar' }, view.version), view.frame);
    place();
    drawColumnPage();
    if (focus) title.focus({ preventScroll: true });
  }

  function closeColumnPage({ focus = true } = {}) {
    const was = colPage ? colPage.id : '';
    if (colPage) colPage.view.dispose();
    colPage = null;
    pageBox.replaceChildren();
    drawColumnPage();
    tick();
    if (focus && was) {
      const row = columnBody.querySelector('[data-open="' + CSS.escape(was) + '"]');
      (row || title).focus({ preventScroll: true });
    }
  }

  back.addEventListener('click', () => closeColumnPage());
  headActions.addEventListener('click', (event) => {
    const b = event.target.closest('[data-art-act="delete"]');
    if (b && colPage) remove(colPage.id, b);
  });

  /* ------------------------------------------------------ the page dialog */

  function openDialog(id, opener) {
    const a = find(id);
    if (!a) return;
    if (dlg && dlg.id === id) return;
    if (dlg) dlg.close();
    const view = pageView({ id, boxed: true });
    const titleId = 'artDialogTitle' + (++pageSeq);
    const heading = el('h2', { class: 'ds-dialog__title so-artdialog__title', id: titleId, text: a.title });
    const backBtn = el('button', {
      class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-ds-close': true,
      'aria-label': 'Back', 'data-tip': 'Back',
    });
    morph(backBtn, html`${raw(icon('arrowLeft', 20))}`);
    const dialog = el('dialog', { class: 'ds-dialog ds-dialog--full so-artdialog', 'aria-labelledby': titleId },
      el('div', { class: 'ds-dialog__backdrop', 'data-ds-close': true }),
      el('div', { class: 'ds-dialog__box' },
        el('div', { class: 'ds-dialog__head' }, backBtn, heading, ...view.actions),
        el('div', { class: 'so-artpage__bar so-artdialog__bar' }, view.version),
        el('div', { class: 'ds-dialog__body so-artdialog__body' }, view.frame)));
    const mine = {
      id,
      dialog,
      set(next) {
        if (heading.textContent !== next.title) heading.textContent = next.title;
        view.set(next);
      },
      close() {
        if (dialog.isConnected && dialog.open) hideOverlay(dialog);
      },
    };
    dialog.addEventListener('click', (event) => {
      const b = event.target.closest('[data-art-act="delete"]');
      if (b) remove(id, b);
    });
    dialog.addEventListener('close', () => {
      if (dlg === mine) dlg = null;
      hideOverlay(dialog, { remove: true }).then(() => view.dispose());
    }, { once: true });
    dlg = mine;
    mine.set(a);
    showOverlay(dialog, opener && opener.isConnected ? opener : button);
    seeShown(a);
  }

  /* ------------------------------------------------------------ opening */

  /** openPage shows an artifact: the column's page at a desk, the dialog narrower. */
  function openPage(id, opener) {
    if (!session || !find(id)) return;
    if (WIDE.matches) {
      if (!columnShown()) {
        openColumn();
      }
      if (sheetOpen()) hideOverlay(sheet);
      showColumnPage(id);
      return;
    }
    openDialog(id, opener);
  }

  /** announce is a new artifact the agent made in the chat on screen. */
  function announce(a) {
    if (WIDE.matches) {
      // The person looking at another artifact keeps it; the badge counts
      // the new one.
      if (columnShown() && colPage && colPage.id !== a.id) return;
      if (!columnShown()) openColumn();
      showColumnPage(a.id, { focus: false });
      return;
    }
    if (dlg && dlg.id === a.id) return;
    toast('The agent made “' + a.title + '”.', 'info', {
      action: { label: 'Open', run: () => { if (session && find(a.id)) openDialog(a.id, button); } },
    });
  }

  /* ------------------------------------------------------------ deleting */

  async function remove(id, from) {
    const a = find(id);
    if (!a || !session) return;
    const chat = session.id;
    const yes = await confirmDialog({
      title: 'Delete “' + a.title + '”?',
      body: 'Every version of the page goes, with its files. This cannot be undone.',
      confirmLabel: 'Delete the artifact', danger: true, opener: from,
    });
    if (!yes || !session || session.id !== chat) return;
    deleting = id;
    try {
      await api('/api/chats/' + encodeURIComponent(chat) + '/artifacts/' + encodeURIComponent(id), { method: 'DELETE', attempts: 1 });
      toast('“' + a.title + '” is deleted.');
      artifacts = artifacts.filter((x) => x.id !== id);
      if (dlg && dlg.id === id) dlg.close();
      if (colPage && colPage.id === id) closeColumnPage({ focus: false });
      draw({ live: false });
      if (WIDE.matches && columnShown()) title.focus({ preventScroll: true });
      schedule();
    } catch (err) {
      deleting = '';
      toast(errorMessage(err), 'error');
    }
  }

  /* ------------------------------------------------------------- the data */

  async function load() {
    if (!session) return;
    const my = ++gen;
    const id = session.id;
    try {
      const data = await api('/api/chats/' + encodeURIComponent(id) + '/artifacts', { attempts: 2 });
      if (my !== gen || !session || session.id !== id) return;
      const before = new Set(artifacts.map((a) => a.id));
      artifacts = Array.isArray(data && data.artifacts) ? data.artifacts : [];
      on = !!(data && data.on);
      rev = data ? data.rev : null;
      loaded = true;
      pruneSeen(id, artifacts);
      // The dialog of an artifact that went (deleted by the agent) closes.
      if (dlg && !find(dlg.id)) {
        const gone = dlg.id;
        dlg.close();
        if (gone !== deleting) toast('That artifact was deleted.');
      }
      if (deleting && !find(deleting)) deleting = '';
      draw();
      // What the frames said was made, and is new to this list: the newest
      // is opened or offered.
      const fresh = artifacts.filter((a) => made.has(a.id) && !before.has(a.id));
      made.clear();
      if (fresh.length) {
        fresh.sort((x, y) => (Date.parse(x.created_at) || 0) - (Date.parse(y.created_at) || 0));
        announce(fresh[fresh.length - 1]);
      }
    } catch {
      // The list stays as it was; the next frame or hello reads it again.
    }
  }

  function schedule() {
    if (!session) return;
    clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      load();
    }, GATHER);
  }

  /* ----------------------------------------------------------- the button */

  button.addEventListener('click', (event) => {
    if (event.detail > 1 || !available()) return;
    if (WIDE.matches) {
      const opening = !columnShown();
      if (opening) openColumn();
      else setPref(false);
      place();
      drawColumnPage();
      tick();
      if (opening) title.focus({ preventScroll: true });
      return;
    }
    showOverlay(sheet, button).then(() => tick());
  });

  closeBtn.addEventListener('click', () => {
    setPref(false);
    place();
    button.focus();
  });

  // A window that grows past the column's width takes an open sheet or
  // page dialog into the column; one that shrinks under it hides the
  // column (the button opens the sheet then, a row the dialog).
  WIDE.addEventListener('change', () => {
    if (WIDE.matches && (sheetOpen() || dlg)) {
      const id = dlg ? dlg.id : '';
      if (!columnShown()) openColumn();
      if (sheetOpen()) hideOverlay(sheet);
      if (dlg) dlg.close();
      if (id) showColumnPage(id, { focus: false });
    }
    place();
    drawColumnPage();
  });

  /* ------------------------------------------------------------ the page */

  function reset() {
    if (dlg) dlg.close();
    if (sheetOpen()) hideOverlay(sheet);
    if (colPage) colPage.view.dispose();
    colPage = null;
    pageBox.replaceChildren();
    made.clear();
    deleting = '';
  }

  return {
    /** show is a chat put on the stage. */
    show(next) {
      if (session && session.id === next.id) {
        session = next;
        return;
      }
      const backTo = kept && kept.id === next.id ? kept : null;
      kept = null;
      reset();
      session = next;
      artifacts = backTo ? backTo.artifacts : [];
      on = backTo ? backTo.on : false;
      rev = backTo ? backTo.rev : null;
      loaded = !!backTo;
      draw({ live: false });
      load();
    },
    /** hide is the chat taken off the stage. */
    hide() {
      if (!session) return;
      kept = loaded ? { id: session.id, artifacts, on, rev } : null;
      session = null;
      gen += 1;
      clearTimeout(timer);
      timer = null;
      clearInterval(ticker);
      ticker = null;
      artifacts = [];
      loaded = false;
      reset();
      drawColumnPage();
      button.hidden = true;
      panel.hidden = true;
    },
    /** frame is an artifacts frame: the chat on screen changed. */
    frame(f) {
      if (!session || !f || f.session !== session.id) return;
      if (f.what === 'created' && f.artifact) made.add(String(f.artifact));
      if (loaded && rev !== null && f.rev === rev) return;
      schedule();
    },
    /** resync is a hello, or the chat's MCP servers switched: read again. */
    resync() {
      if (session) schedule();
    },
    /** shut closes the column because another one opened beside the chat. */
    shut() {
      if (!prefOpen()) return;
      setPref(false);
      place();
    },
  };
}
