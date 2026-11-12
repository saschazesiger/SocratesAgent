// Markdown, as the chat view draws what an agent writes.
//
// The text is the agent's and nothing on the page may trust it: marked turns
// it into HTML, and DOMPurify takes out everything that could run, fetch or
// dress itself up as the page's own - scripts and handlers, images (a remote
// image is a request the person never made), forms, and every class, id and
// style, so that only the project's DS and so- classes are ever in the page.
// What comes back is markup for html`` and morph(); nothing here touches the
// document.
//
// A fenced code block at the top level is drawn by this module itself rather
// than by marked: it gets its language, a copy button, and a scroll of its
// own, so a long line scrolls inside the block and never the page.

import { Marked } from '../vendor/markdown/marked.esm.js';
import DOMPurify from '../vendor/markdown/purify.es.js';
import { html, raw, icon } from './ui.js';
import { thumbHtml } from './thumb.js';

// A path to a Markdown file, nothing else in the span (`docs/X.md`,
// `./README.md#setup`, `/abs/path/NOTES.md`): no spaces, no placeholders.
const MD_PATH = /^(?:\.{0,2}\/)?[\w@+-][\w@+.\/-]*\.(?:md|markdown)(?:#[\w-]*)?$/i;

// An image of a file on this machine, by its path: absolute, relative to
// the chat's folder, or a file:// URL - never a URL of another scheme, and
// only a picture by its name. The server draws it only when a text of the
// agent's names it (server handleChatShownImage).
const LOCAL_IMAGE = /^(?:file:\/\/\/|\/|\.{0,2}\/?[\w@+-])(?!.*:\/\/)[^\s?#]*\.(?:png|jpe?g|gif|webp|avif|bmp)$/i;

// While a text is turned into markup: how its images are drawn, and the
// thumbnails it drew so far, each standing in the markup as SLOT and its
// number until DOMPurify is done with it. SLOT is new on every load, so no
// text can write one.
let drawing = null;
let slots = [];
const SLOT = 'soimg' + Math.random().toString(36).slice(2, 10) + '_';
const SLOTS = new RegExp(SLOT + '(\\d+)_', 'g');

const marked = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    // An image is a link to it: the page fetches nothing the person did not
    // ask for. One of a file on this machine that the chat can draw
    // (`images`, the agent showing a screenshot it took or a chart it made)
    // is drawn small in place, as a slot filled in once the text is clean.
    image({ href, text }) {
      const src = drawing && LOCAL_IMAGE.test(href || '') ? drawing.src(href) : '';
      if (src) {
        slots.push(thumbHtml(src, text || String(href).split('/').pop()));
        return SLOT + (slots.length - 1) + '_';
      }
      return html`<a href="${href || ''}">${text || href || ''}</a>`.toString();
    },
    // A Markdown file named as a `code` path is a link to it, which the chat
    // opens beside itself when the file is in its folder (session.js
    // docLink): agents name files that way out of habit, however they are
    // told to link them.
    codespan({ text, inLink }) {
      if (inLink || !MD_PATH.test(text)) return false;
      return html`<a href="${text}"><code>${text}</code></a>`.toString();
    },
    // A task list's box is a mark, not a form field: nothing in an agent's
    // text is something to fill in.
    checkbox({ checked }) {
      return checked
        ? '<span role="img" aria-label="Done">☑</span> '
        : '<span role="img" aria-label="To do">☐</span> ';
    },
  },
});

const PURIFY = {
  FORBID_TAGS: ['img', 'style', 'form', 'input', 'button', 'textarea', 'select', 'option', 'iframe', 'object',
    'embed', 'video', 'audio', 'picture', 'source', 'svg', 'math', 'link', 'meta', 'base'],
  FORBID_ATTR: ['class', 'id', 'style', 'srcset', 'src', 'action', 'formaction'],
  ALLOW_DATA_ATTR: false,
  ADD_ATTR: ['target'],
};

// Every link opens beside the app, never in place of it, and tells the other
// side nothing about where it came from.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A' && node.hasAttribute('href')) {
    node.setAttribute('target', '_blank');
    node.setAttribute('rel', 'noopener noreferrer');
  }
});

/**
 * sanitize makes a piece of HTML from marked safe for the page, and then
 * puts the thumbnails the page drew itself where their slots are.
 */
function sanitize(markup) {
  const clean = DOMPurify.sanitize(markup, PURIFY);
  return slots.length ? clean.replace(SLOTS, (all, n) => (slots[n] ? String(slots[n]) : '')) : clean;
}

/**
 * codeBlock is one fenced block, drawn by the page. `extra` is markup for its
 * bar after Copy - a tool call's "i", which beside the block would cost it a
 * column of its own.
 */
export function codeBlock(code, lang = '', extra = '') {
  const language = String(lang || '').split(/\s/)[0].slice(0, 24);
  return html`<div class="so-code"><div class="so-code__bar"><span class="so-code__lang">${language || 'text'}</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-sm so-code__copy" type="button" data-action="copy-code" aria-label="Copy code" data-tip="Copy code">${raw(icon('copy', 18))}</button>${extra}</div><pre class="so-code__pre"><code>${code}</code></pre></div>`;
}

// A long chat renders the same texts again and again (every re-render of a
// message that did not change, every part the morph keeps), so the markup of
// the last few hundred texts is kept - within a budget of characters, so
// that a handful of very long answers cannot fill a phone's memory.
const cache = new Map();
const CACHE_SIZE = 400;
const CACHE_CHARS = 2 << 20;
let cachedChars = 0;

function remember(source, out) {
  cache.set(source, out);
  cachedChars += source.length + out.markup.length;
  while (cache.size > CACHE_SIZE || (cachedChars > CACHE_CHARS && cache.size > 1)) {
    const [oldest, markup] = cache.entries().next().value;
    cache.delete(oldest);
    cachedChars -= oldest.length + markup.markup.length;
  }
}

/**
 * chatImages is how a chat draws the images of files on this machine that
 * an agent's text shows (markdown's `images`): from the chat's image route,
 * which serves one only when a text of the agent's names it (server
 * handleChatShownImage) - an answer's, or a question's (ask_user). One per
 * chat, so that the cache keeps what it drew.
 */
export function chatImages(id) {
  if (!id) return null;
  let one = drawers.get(id);
  if (!one) {
    one = { key: id, src: (href) => '/api/chats/' + encodeURIComponent(id) + '/image?path=' + encodeURIComponent(decodePath(href)) };
    drawers.set(id, one);
  }
  return one;
}
const drawers = new Map();

// decodePath is a Markdown image's path as the agent wrote it: marked
// percent-encodes what a URL may not hold (a space), which the text names
// plain.
function decodePath(href) {
  try { return decodeURI(String(href || '')); } catch { return String(href || ''); }
}

/**
 * markdown is a text as safe markup, ready for html``. `keep: false` is for
 * a text that is still being written: each of its states is drawn once and
 * never again, so none is kept. `images` draws the images of files on this
 * machine the text shows: { key, src(path) } - the key of the chat they are
 * drawn for, and where one is drawn from ('' to keep it a link).
 */
export function markdown(text, { keep = true, images = null } = {}) {
  const source = (images ? images.key + '\n' : '') + String(text || '');
  const hit = cache.get(source);
  if (hit) {
    cache.delete(source);
    cache.set(source, hit);
    return hit;
  }
  let out;
  drawing = images;
  slots = [];
  try {
    const tokens = marked.lexer(String(text || ''));
    // A `code` path that is already a link's text stays plain code: a link
    // in a link is no link at all.
    marked.walkTokens(tokens, (token) => {
      if (token.type === 'link') marked.walkTokens(token.tokens || [], (inner) => { inner.inLink = true; });
    });
    const blocks = [];
    let run = [];
    const flush = () => {
      if (!run.length) return;
      const markupRun = Object.assign(run, { links: tokens.links });
      blocks.push(raw(sanitize(marked.parser(markupRun))));
      run = [];
    };
    for (const token of tokens) {
      if (token.type === 'code') {
        flush();
        blocks.push(codeBlock(token.text, token.lang));
      } else {
        run.push(token);
      }
    }
    flush();
    out = html`${blocks}`;
  } catch {
    out = html`<p>${String(text || '')}</p>`;
  } finally {
    drawing = null;
    slots = [];
  }
  if (keep) remember(source, out);
  return out;
}
