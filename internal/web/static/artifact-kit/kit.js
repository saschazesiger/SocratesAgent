// The page kit of a chat's artifacts (docs/design/ARTIFACTS.md, "The page
// kit"). It runs inside an artifact's page, a sandbox of its own origin
// beside the chat, never in the app: it renders a Markdown page, wraps
// tables so that they scroll on a phone, opens links out of the frame, and
// draws Mermaid diagrams with Mermaid from jsDelivr, loaded only when the
// page has one. Offline, a diagram stays as its source with a note.
//
// It also shows a project folder's Markdown files in the Docs view
// (docs/design/DOCS.md, body[data-docs]): headings get anchors, GitHub's
// alerts become callouts, a link to another Markdown file of the folder
// stays in the frame, one to any other file of it is handed to the app, and
// the app hears what the frame shows (its path, title and headings).

import { Marked } from './marked.esm.js';

const MERMAID = 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';

const escapeHtml = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);

// renderMarkdown draws the page's Markdown (its text in a pre, shown as it
// is when this kit cannot load) into its main; a ```mermaid block becomes
// a diagram to draw, and raw HTML stays (the frame is a sandbox).
function renderMarkdown() {
  const source = document.getElementById('artifact-markdown');
  const main = document.querySelector('main.page');
  if (!source || !main) return;
  const text = source.textContent;
  const marked = new Marked({
    gfm: true,
    renderer: {
      code({ text: body, lang }) {
        if ((lang || '').trim().toLowerCase() === 'mermaid') return '<pre class="mermaid">' + escapeHtml(body) + '</pre>\n';
        return false;
      },
    },
  });
  main.innerHTML = marked.parse(text);
}

// wrapTables lets a wide table scroll inside the page rather than widen it.
function wrapTables() {
  for (const table of document.querySelectorAll('main.page table')) {
    if (table.parentElement && table.parentElement.classList.contains('table-wrap')) continue;
    const wrap = document.createElement('div');
    wrap.className = 'table-wrap';
    table.replaceWith(wrap);
    wrap.append(table);
  }
}

// linksOut opens a link to another page in a tab of its own: in the frame
// it would take the artifact's place.
function linksOut() {
  for (const a of document.querySelectorAll('a[href]')) {
    const href = a.getAttribute('href') || '';
    if (a.hasAttribute('target') || href.startsWith('#')) continue;
    if (/^(https?:|mailto:)/i.test(href)) {
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
    }
  }
}

// slugOf is the anchor GitHub gives a heading: lower case, punctuation
// gone, spaces as dashes; a second one of the same name gets -1, -2 ...
function slugOf(text, taken) {
  const base = text.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s/g, '-') || 'section';
  let slug = base;
  for (let n = 1; taken.has(slug); n += 1) slug = base + '-' + n;
  taken.add(slug);
  return slug;
}

// anchorHeadings gives every heading of the page an id, so that a link to
// #a-heading finds it, and answers the headings of the first three levels.
function anchorHeadings() {
  const taken = new Set([...document.querySelectorAll('[id]')].map((node) => node.id));
  const heads = [];
  for (const h of document.querySelectorAll('main.page h1, main.page h2, main.page h3, main.page h4, main.page h5, main.page h6')) {
    if (!h.id) h.id = slugOf(h.textContent || '', taken);
    const level = Number(h.tagName.slice(1));
    if (level <= 3) heads.push({ id: h.id, text: (h.textContent || '').trim().slice(0, 200), level });
  }
  return heads;
}

// ALERTS are GitHub's alerts, > [!NOTE] ..., as the kit's callouts.
const ALERTS = { note: ['info', 'Note'], tip: ['ok', 'Tip'], important: ['info', 'Important'], warning: ['warn', 'Warning'], caution: ['error', 'Caution'] };

function drawAlerts() {
  for (const quote of document.querySelectorAll('main.page blockquote')) {
    const first = quote.firstElementChild;
    if (!first || first.tagName !== 'P') continue;
    const m = /^\s*\[!(note|tip|important|warning|caution)\]\s*/i.exec(first.textContent || '');
    if (!m) continue;
    const [kind, label] = ALERTS[m[1].toLowerCase()];
    // The marker is the start of the paragraph's first text.
    const walker = document.createTreeWalker(first, NodeFilter.SHOW_TEXT);
    const text = walker.nextNode();
    if (text) text.textContent = text.textContent.replace(/^\s*\[![a-z]+\]\s*/i, '');
    if (first.firstChild && first.firstChild.nodeName === 'BR') first.firstChild.remove();
    const box = document.createElement('div');
    box.className = 'callout ' + kind;
    const title = document.createElement('p');
    title.className = 'callout-title';
    title.textContent = label;
    box.append(title, ...quote.childNodes);
    if (!(first.textContent || '').trim() && !first.querySelector('img')) first.remove();
    quote.replaceWith(box);
  }
}

// toHash scrolls to the heading the address names: the page was empty when
// the browser looked for it.
function toHash() {
  const id = decodeURIComponent(location.hash.slice(1));
  if (!id) return;
  const target = document.getElementById(id);
  if (target) target.scrollIntoView();
}

const MARKDOWN = /\.(md|markdown)$/i;
const IMAGE = /\.(png|jpe?g|gif|svg|webp|avif|ico|bmp)$/i;

// docsMode is the frame of the Docs view: the page is /docs/<token>/<path>,
// and every file of the folder is under /docs/<token>/.
function docsMode(heads) {
  const path = document.body.dataset.docs || '';
  const base = (location.pathname.match(/^\/docs\/[^/]+\//) || ['/docs/'])[0];
  const tell = (message) => {
    try { window.parent.postMessage({ socratesDocs: 1, ...message }, '*'); } catch { /* not in a frame */ }
  };
  const h1 = document.querySelector('main.page h1');
  tell({ t: 'page', path, title: (h1 && h1.textContent.trim()) || path.split('/').pop(), headings: heads, hash: location.hash.slice(1) });
  document.addEventListener('click', (event) => {
    const a = event.target.closest && event.target.closest('a[href]');
    if (!a || event.defaultPrevented || a.target === '_blank') return;
    const href = a.getAttribute('href') || '';
    if (href.startsWith('#') || /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith('//')) return;
    const url = new URL(href, location.href);
    if (url.origin !== location.origin || !url.pathname.startsWith(base)) {
      event.preventDefault();
      tell({ t: 'outside', href });
      return;
    }
    let rel;
    try { rel = decodeURIComponent(url.pathname.slice(base.length)); } catch { rel = url.pathname.slice(base.length); }
    const name = rel.split('/').pop();
    // A Markdown file or a folder (its README) is shown here; a picture in
    // a tab of its own; anything else (code) is the app's to say.
    if (MARKDOWN.test(rel) || rel.endsWith('/') || rel === '' || !name.includes('.')) return;
    if (IMAGE.test(rel)) {
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      return;
    }
    event.preventDefault();
    tell({ t: 'file', path: rel });
  });
  window.addEventListener('message', (event) => {
    const data = event.data;
    if (event.source !== window.parent || !data || data.socratesDocs !== 1) return;
    if (data.t === 'scroll' && typeof data.id === 'string') {
      const target = document.getElementById(data.id);
      if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  });
  window.addEventListener('hashchange', () => tell({ t: 'hash', hash: location.hash.slice(1) }));
  // Where the page is scrolled to, so that the app can bring a page that
  // changed on disk back to the same place (?y= in its address).
  let pending = false;
  window.addEventListener('scroll', () => {
    if (pending) return;
    pending = true;
    setTimeout(() => {
      pending = false;
      tell({ t: 'scroll', y: Math.round(window.scrollY) });
    }, 200);
  }, { passive: true });
}

// keptScroll is the scroll position the address asks for (?y=, the Docs
// view reloading a page that changed), or 0.
function keptScroll() {
  const y = Number(new URLSearchParams(location.search).get('y'));
  return Number.isFinite(y) && y > 0 ? y : 0;
}

async function drawDiagrams() {
  const nodes = [...document.querySelectorAll('pre.mermaid, div.mermaid')];
  if (!nodes.length) return;
  try {
    const { default: mermaid } = await import(MERMAID);
    const css = getComputedStyle(document.documentElement);
    const v = (name) => css.getPropertyValue(name).trim();
    mermaid.initialize({
      startOnLoad: false,
      theme: 'base',
      fontFamily: v('--font'),
      themeVariables: {
        fontFamily: v('--font'),
        primaryColor: v('--accent-soft'),
        primaryBorderColor: v('--accent'),
        primaryTextColor: v('--text'),
        secondaryColor: v('--ok-soft'),
        tertiaryColor: v('--surface'),
        lineColor: v('--muted'),
        textColor: v('--text'),
        noteBkgColor: v('--warn-soft'),
        noteBorderColor: v('--warn'),
      },
    });
    await mermaid.run({ nodes, suppressErrors: true });
  } catch {
    for (const node of nodes) {
      const note = document.createElement('p');
      note.className = 'mermaid-note';
      note.textContent = 'This diagram could not be drawn here (its drawing library did not load); above is its source.';
      node.after(note);
    }
  }
}

renderMarkdown();
drawAlerts();
const heads = anchorHeadings();
wrapTables();
linksOut();
if (document.body && document.body.hasAttribute('data-docs')) docsMode(heads);
const y = keptScroll();
if (y) window.scrollTo(0, y);
else toHash();
drawDiagrams().then(() => { if (y) window.scrollTo(0, y); });
