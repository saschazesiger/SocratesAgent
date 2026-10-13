// Copy, paste and upload: the three ways something crosses between this
// device and the pane, as buttons.
//
// The keys already do the first two at a desk. A phone has no keys, and
// selecting on a terminal with a finger is the one gesture a phone is worst
// at - so Copy with nothing selected opens the pane's text in a sheet, where
// the phone's own selection handles work. Paste is the key bar's Paste,
// without having to put the key bar up first.
//
// Upload is how a screenshot gets to an agent at all: the file is written into
// the session's working directory and its path is pasted onto the prompt,
// which Claude Code turns into an attached image and the others read as a
// file. A button, a file dropped on the pane and an image pasted into it all
// take the same road.

import { el, toast, setClass, errorMessage } from './api.js';
import { copyText, pasteClipboard } from './term.js';

// The server's own ceiling on one upload (internal/server/upload.go), checked
// here first so a large video is refused before it is sent over a phone link.
const UPLOAD_MAX = 64 << 20;

// How much of the scrollback the copy sheet shows. Enough for the last
// answer and what led to it, little enough to stay quick on a phone.
const SHEET_LINES = 1000;

// A path that needs no quoting on a command line. Anything else is put in
// single quotes, which every shell and every one of the agents reads.
const PLAIN_PATH = /^[A-Za-z0-9_@%+=:,./-]+$/;

function quotePath(path) {
  return PLAIN_PATH.test(path) ? path : "'" + path.replaceAll("'", "'\\''") + "'";
}

/**
 * paneText is what the pane holds, as text: the last `limit` lines of the
 * buffer with the empty ones at the end dropped.
 */
function paneText(term, limit) {
  const buffer = term.buffer.active;
  const lines = [];
  for (let i = Math.max(0, buffer.length - limit); i < buffer.length; i += 1) {
    const line = buffer.getLine(i);
    if (!line) continue;
    // A wrapped line continues the one before it rather than starting anew.
    if (line.isWrapped && lines.length) lines[lines.length - 1] += line.translateToString(true);
    else lines.push(line.translateToString(true));
  }
  while (lines.length && !lines[lines.length - 1].trim()) lines.pop();
  return lines.join('\n');
}

/**
 * mountTransfer wires the three buttons.
 *
 * `ctx` is what this module is not allowed to own:
 *   dom      the shared id map
 *   current  the session on screen, or null
 *   term     the xterm Terminal of that session, or null
 *   send     write bytes to the pane
 *   focus    give the focus back to the pane
 */
export function mountTransfer(ctx) {
  const { dom } = ctx;
  let uploading = false;

  /* ---------------------------------------------------------------- copy */

  function copySheet(term) {
    const text = paneText(term, SHEET_LINES);
    if (!text) {
      toast('There is nothing on the screen to copy.');
      return;
    }
    const area = el('textarea', {
      class: 'textarea mono copy-text', readonly: true, spellcheck: 'false',
      'aria-label': 'Terminal text', value: text,
    });
    const copy = el('button', { class: 'btn sm primary', type: 'button', text: 'Copy all' });
    const close = el('button', { class: 'btn sm', type: 'button', text: 'Close' });
    const dialog = el('dialog', { class: 'modal copy-sheet' },
      el('h2', { class: 'modal-title', text: 'Copy from the terminal' }),
      el('p', { class: 'hint', text: 'Select a part to copy just that, or copy everything.' }),
      area,
      el('div', { class: 'modal-actions' }, close, copy));
    const selected = () => area.value.slice(area.selectionStart, area.selectionEnd);
    const paint = () => { copy.textContent = selected() ? 'Copy selection' : 'Copy all'; };
    document.addEventListener('selectionchange', paint);
    area.addEventListener('select', paint);
    copy.addEventListener('click', () => {
      const part = selected();
      if (copyText(part || area.value)) {
        toast(part ? 'Copied the selection.' : 'Copied everything.');
        dialog.close();
      }
    });
    close.addEventListener('click', () => dialog.close());
    dialog.addEventListener('click', (event) => { if (event.target === dialog) dialog.close(); });
    dialog.addEventListener('close', () => {
      document.removeEventListener('selectionchange', paint);
      dialog.remove();
      ctx.focus();
    });
    document.body.append(dialog);
    dialog.showModal();
    // The newest line is the one somebody came to copy.
    area.scrollTop = area.scrollHeight;
    copy.focus();
  }

  function copy() {
    const term = ctx.term();
    if (!term) return;
    // What is selected on the pane is what the button means. Without a
    // selection, the sheet is where one can be made.
    if (term.hasSelection()) {
      if (copyText(term.getSelection())) {
        term.clearSelection();
        toast('Copied.');
      }
      ctx.focus();
      return;
    }
    copySheet(term);
  }

  /* --------------------------------------------------------------- paste */

  async function paste() {
    const term = ctx.term();
    if (!term) return;
    await pasteClipboard(term, ctx.send);
    ctx.focus();
  }

  /* -------------------------------------------------------------- upload */

  async function upload(files) {
    const session = ctx.current();
    const list = [...(files || [])];
    if (!session || !ctx.term() || !list.length) return;
    if (uploading) {
      toast('An upload is still running.', 'error');
      return;
    }
    const size = list.reduce((sum, f) => sum + f.size, 0);
    if (size > UPLOAD_MAX) {
      toast('That is more than 64 MB, which is the most one upload can be.', 'error');
      return;
    }
    const form = new FormData();
    for (const file of list) form.append('file', file, file.name || 'pasted.png');
    uploading = true;
    paint();
    try {
      const res = await fetch('/api/sessions/' + encodeURIComponent(session.id) + '/upload', {
        method: 'POST', body: form, credentials: 'same-origin',
      });
      let data = null;
      try { data = await res.json(); } catch { /* answered below */ }
      if (!res.ok) throw new Error((data && data.error) || 'The upload failed (' + res.status + ').');
      // The session may have changed while the file was on its way; the path
      // belongs on the prompt of the one it was uploaded to, or nowhere.
      const now = ctx.current();
      if (!now || now.id !== session.id || !ctx.term()) {
        toast('Uploaded to “' + session.title + '”.');
        return;
      }
      const paths = data.files.map((f) => quotePath(f.path)).join(' ');
      const term = ctx.term();
      // Pasted rather than typed, so that a program that asked to be told
      // about pastes - Claude Code does - sees a dropped file.
      const bracketed = !!(term.modes && term.modes.bracketedPasteMode);
      ctx.send(bracketed ? '\x1b[200~' + paths + ' \x1b[201~' : paths + ' ');
      ctx.focus();
    } catch (err) {
      toast(err instanceof TypeError ? 'The upload did not reach Socrates.' : errorMessage(err), 'error');
    } finally {
      uploading = false;
      paint();
    }
  }

  const picker = el('input', { type: 'file', multiple: true, hidden: true, 'aria-hidden': 'true' });
  picker.addEventListener('change', () => {
    const files = [...picker.files];
    picker.value = '';
    upload(files);
  });
  document.body.append(picker);

  // A file dragged onto the pane. Only files: a dragged selection of text is
  // the browser's to handle.
  const carriesFiles = (event) => !!event.dataTransfer
    && [...event.dataTransfer.types].includes('Files');
  dom.termWrap.addEventListener('dragover', (event) => {
    if (!carriesFiles(event) || !ctx.term()) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
    setClass(dom.termWrap, 'dropping', true);
  });
  dom.termWrap.addEventListener('dragleave', (event) => {
    if (!dom.termWrap.contains(event.relatedTarget)) setClass(dom.termWrap, 'dropping', false);
  });
  dom.termWrap.addEventListener('drop', (event) => {
    setClass(dom.termWrap, 'dropping', false);
    if (!carriesFiles(event) || !ctx.term()) return;
    event.preventDefault();
    upload(event.dataTransfer.files);
  });

  // An image pasted into the pane - a screenshot on the clipboard has no text
  // in it, and a terminal has nowhere else to put one. A paste into a field
  // of its own is that field's.
  document.addEventListener('paste', (event) => {
    if (!ctx.term() || !event.clipboardData) return;
    const target = event.target;
    const field = target && target.closest && target.closest('input, textarea, [contenteditable]');
    if (field && !field.classList.contains('xterm-helper-textarea')) return;
    const files = [...event.clipboardData.files];
    if (!files.length || event.clipboardData.getData('text/plain')) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    upload(files);
  }, true);

  /* ------------------------------------------------------------- painting */

  function paint() {
    const on = !!ctx.term();
    if (dom.transfer) dom.transfer.hidden = !on;
    if (dom.uploadBtn) {
      setClass(dom.uploadBtn, 'working', uploading);
      dom.uploadBtn.disabled = uploading;
    }
  }

  // Each keeps the focus where it was: a button that took it from the pane
  // would leave the next keystroke with nowhere to go.
  for (const button of [dom.copyBtn, dom.pasteBtn, dom.uploadBtn]) {
    if (button) button.addEventListener('mousedown', (event) => event.preventDefault());
  }
  if (dom.copyBtn) dom.copyBtn.addEventListener('click', copy);
  if (dom.pasteBtn) dom.pasteBtn.addEventListener('click', () => { paste().catch(() => {}); });
  if (dom.uploadBtn) dom.uploadBtn.addEventListener('click', () => picker.click());
  paint();

  return {
    /** attached repaints for whatever the pane now holds. */
    attached: paint,
  };
}
