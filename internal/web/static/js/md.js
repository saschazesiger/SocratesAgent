// Markdown, as the chat view draws what an agent writes.
//
// The text is the agent's and nothing on the page may trust it: marked turns
// it into HTML, and DOMPurify takes out everything that could run, fetch or
// dress itself up as the page's own - scripts and handlers, images (a remote
// image is a request the person never made), forms, and every class, id and
// style, so that only the project's ds-/so- classes are ever in the page.
// What comes back is markup for html`` and morph(); nothing here touches the
// document.
//
// A fenced code block at the top level is drawn by this module itself rather
// than by marked: it gets its language, a copy button, and a scroll of its
// own, so a long line scrolls inside the block and never the page.

import { Marked } from '../vendor/markdown/marked.esm.js';
import DOMPurify from '../vendor/markdown/purify.es.js';
import { html, raw, icon } from './ui.js';

const marked = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    // An image is a link to it: the page fetches nothing the person did not
    // ask for.
    image({ href, text }) {
      return html`<a href="${href || ''}">${text || href || ''}</a>`.toString();
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

/** sanitize makes a piece of HTML from marked safe for the page. */
function sanitize(markup) {
  return DOMPurify.sanitize(markup, PURIFY);
}

/** codeBlock is one fenced block, drawn by the page. */
export function codeBlock(code, lang = '') {
  const language = String(lang || '').split(/\s/)[0].slice(0, 24);
  return html`<div class="so-code"><div class="so-code__bar"><span class="so-code__lang">${language || 'text'}</span><button class="ds-btn ds-btn--quiet ds-btn--icon ds-btn--sm so-code__copy" type="button" data-action="copy-code" aria-label="Copy code" data-tip="Copy code">${raw(icon('copy', 18))}</button></div><pre class="so-code__pre"><code>${code}</code></pre></div>`;
}

// A long chat renders the same texts again and again (every re-render of a
// message that did not change, every part the morph keeps), so the markup of
// the last few hundred texts is kept.
const cache = new Map();
const CACHE_SIZE = 400;

/** markdown is a text as safe markup, ready for html``. */
export function markdown(text) {
  const source = String(text || '');
  const hit = cache.get(source);
  if (hit) {
    cache.delete(source);
    cache.set(source, hit);
    return hit;
  }
  let out;
  try {
    const tokens = marked.lexer(source);
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
    out = html`<p>${source}</p>`;
  }
  cache.set(source, out);
  if (cache.size > CACHE_SIZE) cache.delete(cache.keys().next().value);
  return out;
}
