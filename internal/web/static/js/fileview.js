// Any file beside a chat (docs/design/PAGES.md, "Any file"): what a chat's
// agent made or found and wants to show - a video it cut, a recording, a
// picture, a PDF, an HTML report, a CSV, a log - in the column beside the
// chat (docside.js) with a player or a viewer for its kind. Markdown is the
// Docs viewer's (docside.js, in its frame); everything else is drawn here.
//
// A file comes from the chat's pages folder (the Docs routes, /docs/<token>/)
// or from anywhere on the machine the agent named it (the chat's file route,
// /api/chats/<id>/file?path=, which serves only what a text of the agent's
// names). Either way the kinds are the server's (internal/docs FileKind).

import { html, raw, icon, morph } from './ui.js';
import { lightbox } from './lightbox.js';

// The most text read for the text view; a longer file says so and offers
// the whole of it.
const TEXT_MAX = 1 << 20;
// The most rows of a CSV drawn as a table.
const TABLE_ROWS = 500;

// The kinds by extension, as internal/docs has them (FileKind): which links
// a chat opens beside itself, and the glyph of a file in a list. The server
// has the last word (a file whose name says nothing is looked at there).
const VIDEO = /\.(mp4|m4v|webm|mov|ogv|mkv|avi|mpe?g|3gp)$/i;
const AUDIO = /\.(mp3|wav|ogg|oga|opus|m4a|aac|flac|weba|aiff?|midi?)$/i;
const IMAGE = /\.(png|jpe?g|gif|svg|webp|avif|ico|bmp)$/i;
const PDF = /\.pdf$/i;
const HTML = /\.(html?|xhtml)$/i;
const MARKDOWN = /\.(md|markdown)$/i;
const TEXT = /\.(txt|text|log|out|err|csv|tsv|json|jsonl|ndjson|geojson|ya?ml|toml|xml|ini|cfg|conf|properties|srt|vtt|sql|diff|patch|rst|adoc|tex|bib|org|sh|bash|zsh|fish|ps1|bat|py|rb|pl|php|lua|r|js|mjs|cjs|ts|tsx|jsx|go|mod|sum|rs|java|kts?|scala|swift|c|h|cc|cpp|hpp|cs|m|dart|exs?|erl|hs|clj|css|scss|sass|less|vue|svelte|astro|graphql|gql|proto|tf|hcl|nix|gradle|cmake|mk|dockerfile|lock|mmd|dot|gv|ics|vcf|http)$/i;

/** fileKind is how a file is shown, by its name: markdown, image, video, audio, pdf, html, text or file. */
export function fileKind(path) {
  const name = String(path || '').split(/[?#]/)[0].split('/').pop();
  if (MARKDOWN.test(name)) return 'markdown';
  if (IMAGE.test(name)) return 'image';
  if (VIDEO.test(name)) return 'video';
  if (AUDIO.test(name)) return 'audio';
  if (PDF.test(name)) return 'pdf';
  if (HTML.test(name)) return 'html';
  if (TEXT.test(name)) return 'text';
  return 'file';
}

/** shownKind is whether a file named only as a `code` path counts as a link: something to look at or listen to. */
export const shownKind = (kind) => kind === 'video' || kind === 'audio' || kind === 'image' || kind === 'pdf' || kind === 'html';

/** kindGlyph is the DS glyph of a file of a kind. */
export function kindGlyph(kind, path = '') {
  switch (kind) {
    case 'video': return 'video';
    case 'audio': return 'audioLines';
    case 'image': return 'fileImage';
    case 'pdf': return 'fileText';
    case 'html': return 'globe';
    case 'markdown': return 'fileText';
    case 'text':
      if (/\.(csv|tsv)$/i.test(path)) return 'fileSpreadsheet';
      if (/\.(json|jsonl|ndjson|geojson)$/i.test(path)) return 'fileJson';
      return 'fileCode';
    default:
      return /\.(zip|tar|gz|tgz|bz2|xz|7z|rar|zst)$/i.test(path) ? 'fileArchive' : 'file';
  }
}

/** kindWord is a kind as the viewer names it. */
export function kindWord(kind) {
  return { video: 'Video', audio: 'Audio', image: 'Picture', pdf: 'PDF', html: 'Web page', text: 'Text', markdown: 'Page' }[kind] || 'File';
}

/** sizeText is a file's size for a person: 812 B, 4.2 MB. */
export function sizeText(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return n + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return (v >= 10 ? Math.round(v) : Math.round(v * 10) / 10) + ' ' + units[i];
}

/** withVersion is an address with `v=<mtime>`, so that a file changed on disk is asked for again. */
export function withVersion(src, mtime) {
  if (!mtime) return src;
  return src + (src.includes('?') ? '&' : '?') + 'v=' + encodeURIComponent(mtime);
}

// parseDelimited is a CSV or TSV text as rows of cells, quotes as RFC 4180
// has them, at most `max` rows.
function parseDelimited(text, sep, max) {
  const rows = [];
  let row = [];
  let cell = '';
  let quoted = false;
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') { cell += '"'; i += 1; } else quoted = false;
      } else cell += c;
      continue;
    }
    if (c === '"' && cell === '') quoted = true;
    else if (c === sep) { row.push(cell); cell = ''; }
    else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i += 1;
      row.push(cell);
      cell = '';
      rows.push(row);
      row = [];
      if (rows.length >= max) return { rows, more: i < text.length - 1 };
    } else cell += c;
  }
  if (cell !== '' || row.length) { row.push(cell); rows.push(row); }
  return { rows, more: false };
}

/**
 * mountFileView draws files into `box`. It answers
 *   show(file)    file: {src, download, name, kind, size, mtime}: `src` the
 *                 file's address, `download` the address that saves it
 *   reload(file)  the same file changed on disk: drawn again, a player where
 *                 it was
 *   clear()       nothing shown (a player stops)
 */
export function mountFileView(box) {
  let shown = null;
  let gen = 0;

  function emptyHtml(file, title, text, tone = '') {
    return html`<div class="ds-empty ds-empty--section so-fileview__empty ${tone}">
      <span class="ds-empty__disc" aria-hidden="true">${raw(icon(kindGlyph(file.kind, file.name), 24))}</span>
      <p class="ds-empty__title">${title}</p>
      <p class="ds-empty__text">${text}</p>
      <div class="ds-empty__actions">
        <a class="ds-btn ds-btn--tonal" href="${file.download}" download="${file.name}">${raw(icon('download', 18))}<span>Download</span></a>
        <a class="ds-btn ds-btn--quiet" href="${file.src}" target="_blank" rel="noopener noreferrer">${raw(icon('external', 18))}<span>Open in a new tab</span></a>
      </div>
    </div>`;
  }

  function cantPlay(file) {
    morph(box, emptyHtml(file, 'This browser cannot ' + (file.kind === 'video' ? 'play this video' : file.kind === 'audio' ? 'play this recording' : 'show this file'),
      file.name + ' · ' + sizeText(file.size) + '. Download it to open it with an app of this device.', 'ds-empty--error'));
  }

  function mediaHtml(file, src) {
    switch (file.kind) {
      case 'video':
        return html`<div class="so-fileview so-fileview--video"><video class="so-fileview__video" controls playsinline preload="metadata" src="${src}" aria-label="${file.name}"></video></div>`;
      case 'audio':
        return html`<div class="so-fileview so-fileview--audio"><div class="ds-empty ds-empty--section">
          <span class="ds-empty__disc" aria-hidden="true">${raw(icon('audioLines', 24))}</span>
          <p class="ds-empty__title">${file.name}</p>
          <p class="ds-empty__text">${sizeText(file.size)}</p>
        </div><audio class="so-fileview__audio" controls preload="metadata" src="${src}" aria-label="${file.name}"></audio></div>`;
      case 'image':
        return html`<div class="so-fileview so-fileview--image"><button class="so-fileview__picture ds-pressable" type="button" data-fileview="zoom" aria-label="${'Show ' + file.name + ' large'}"><img class="so-fileview__img" src="${src}" alt="${file.name}"></button></div>`;
      case 'pdf':
        return html`<div class="so-fileview so-fileview--frame"><iframe class="so-fileview__frame" src="${src}" title="${file.name}"></iframe></div>`;
      case 'html':
        return html`<div class="so-fileview so-fileview--frame"><iframe class="so-fileview__frame so-fileview__frame--page" src="${src}" title="${file.name}" referrerpolicy="no-referrer" sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-forms"></iframe></div>`;
      default:
        return null;
    }
  }

  async function drawText(file, src, my) {
    morph(box, html`<div class="so-fileview so-fileview--text" aria-busy="true"></div>`);
    let text = '';
    let cut = false;
    try {
      const res = await fetch(src, { credentials: 'same-origin', headers: { Range: 'bytes=0-' + (TEXT_MAX - 1) } });
      if (!res.ok) throw new Error(String(res.status));
      const buf = await res.arrayBuffer();
      text = new TextDecoder('utf-8').decode(buf);
      cut = res.status === 206 && Number(file.size) > TEXT_MAX;
    } catch {
      if (my === gen) morph(box, emptyHtml(file, 'The file could not be read', 'It may have gone, or the connection dropped.', 'ds-empty--error'));
      return;
    }
    if (my !== gen) return;
    const note = cut ? html`<p class="so-fileview__note">${'The first ' + sizeText(TEXT_MAX) + ' of ' + sizeText(file.size) + '.'} <a href="${file.download}" download="${file.name}">Download the whole file</a></p>` : '';
    const sep = /\.tsv$/i.test(file.name) ? '\t' : /\.csv$/i.test(file.name) ? ',' : '';
    if (sep) {
      const { rows, more } = parseDelimited(text, sep, TABLE_ROWS + 1);
      if (rows.length > 1) {
        const [head, ...body] = rows;
        const shownRows = body.slice(0, TABLE_ROWS);
        const extra = more || body.length > TABLE_ROWS
          ? html`<p class="so-fileview__note">${'The first ' + TABLE_ROWS + ' rows.'} <a href="${file.download}" download="${file.name}">Download the whole file</a></p>` : '';
        morph(box, html`<div class="so-fileview so-fileview--text">${note}${extra}<div class="ds-table-wrap so-fileview__table"><table class="ds-table ds-table--bordered ds-sm"><thead><tr>${head.map((c) => html`<th>${c}</th>`)}</tr></thead><tbody>${shownRows.map((r) => html`<tr>${r.map((c) => html`<td>${c}</td>`)}</tr>`)}</tbody></table></div></div>`);
        return;
      }
    }
    if (!cut && /\.(json|geojson)$/i.test(file.name)) {
      try { text = JSON.stringify(JSON.parse(text), null, 2); } catch { /* shown as it is */ }
    }
    morph(box, html`<div class="so-fileview so-fileview--text">${note}<pre class="ds-code-block so-fileview__pre" tabindex="0" aria-label="${file.name}"><code>${text}</code></pre></div>`);
  }

  function draw(file, { keep = null } = {}) {
    const my = ++gen;
    const src = withVersion(file.src, file.mtime);
    if (file.kind === 'text' || file.kind === 'markdown') {
      drawText(file, src, my);
      return;
    }
    const markup = mediaHtml(file, src);
    if (!markup) {
      morph(box, emptyHtml(file, file.name, kindWord(file.kind) + ' · ' + sizeText(file.size) + '. Socrates cannot show this kind of file: download it or open it in a new tab.'));
      return;
    }
    morph(box, markup);
    const media = box.querySelector('video, audio');
    if (media) {
      media.addEventListener('error', () => { if (my === gen && shown === file) cantPlay(file); }, { once: true });
      if (keep) {
        media.addEventListener('loadedmetadata', () => {
          if (keep.time) media.currentTime = Math.min(keep.time, media.duration || keep.time);
          if (keep.playing) media.play().catch(() => {});
        }, { once: true });
      }
    }
  }

  box.addEventListener('click', (event) => {
    const zoom = event.target.closest('[data-fileview="zoom"]');
    if (zoom && shown) lightbox([{ src: withVersion(shown.src, shown.mtime), name: shown.name }], 0, zoom);
  });

  return {
    show(file) {
      shown = file;
      draw(file);
    },
    reload(file) {
      const media = box.querySelector('video, audio');
      const keep = media ? { time: media.currentTime, playing: !media.paused } : null;
      shown = file;
      draw(file, { keep });
    },
    clear() {
      gen += 1;
      shown = null;
      const media = box.querySelector('video, audio');
      if (media) media.pause();
      box.replaceChildren();
    },
  };
}
