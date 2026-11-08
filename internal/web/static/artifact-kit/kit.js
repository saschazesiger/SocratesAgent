// The page kit of a chat's artifacts (docs/design/ARTIFACTS.md, "The page
// kit"). It runs inside an artifact's page, a sandbox of its own origin
// beside the chat, never in the app: it renders a Markdown page, wraps
// tables so that they scroll on a phone, opens links out of the frame, and
// draws Mermaid diagrams with Mermaid from jsDelivr, loaded only when the
// page has one. Offline, a diagram stays as its source with a note.

import { Marked } from './marked.esm.js';

const MERMAID = 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';

const escapeHtml = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]);

// renderMarkdown draws the page's Markdown into its main; a ```mermaid
// block becomes a diagram to draw, and raw HTML stays (the frame is a
// sandbox).
function renderMarkdown() {
  const source = document.getElementById('artifact-markdown');
  const main = document.querySelector('main.page');
  if (!source || !main) return;
  let text = '';
  try {
    text = JSON.parse(source.textContent);
  } catch {
    return;
  }
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
wrapTables();
linksOut();
drawDiagrams();
