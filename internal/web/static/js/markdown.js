// A deliberately small Markdown renderer. No dependencies, no CDN, enough for
// the answers an agent writes: headings, lists, code, quotes, tables, links.

// Sentinel used to park code spans while inline rules run.
const MARK = String.fromCharCode(1);

function esc(text) {
  return String(text ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function unesc(text) {
  return text.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
}

function safeUrl(url) {
  const value = String(url || '').trim();
  if (/^(https?:\/\/|mailto:|\/|#)/i.test(value)) return value.replace(/"/g, '%22');
  return null;
}

function inline(text) {
  const codes = [];
  let out = text.replace(/`([^`]+)`/g, (_, code) => {
    codes.push(code);
    return MARK + (codes.length - 1) + MARK;
  });

  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  out = out.replace(/(^|[\s(])\*([^*\n]+)\*/g, '$1<em>$2</em>');
  out = out.replace(/(^|[\s(])_([^_\n]+)_(?=$|[\s.,;:!?)])/g, '$1<em>$2</em>');
  out = out.replace(/~~([^~]+)~~/g, '<del>$1</del>');
  out = out.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (match, label, href) => {
    const url = safeUrl(href);
    return url ? '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + label + '</a>' : match;
  });
  out = out.replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g,
    (_, lead, url) => lead + '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + url + '</a>');

  return out.replace(new RegExp(MARK + '(\\d+)' + MARK, 'g'),
    (_, index) => '<code>' + codes[Number(index)] + '</code>');
}

export function renderMarkdown(source) {
  const lines = esc(String(source ?? '')).split('\n');
  const out = [];
  const paragraph = [];
  let i = 0;

  const flush = () => {
    if (paragraph.length) out.push('<p>' + inline(paragraph.join('<br>')) + '</p>');
    paragraph.length = 0;
  };

  while (i < lines.length) {
    const line = lines[i];

    const fence = line.match(/^\s*(```|~~~)/);
    if (fence) {
      flush();
      const marker = fence[1];
      const body = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(marker)) body.push(lines[i++]);
      i++;
      out.push('<pre><code>' + body.join('\n') + '</code></pre>');
      continue;
    }

    if (!line.trim()) {
      flush();
      i++;
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      flush();
      const level = Math.min(heading[1].length, 3);
      out.push('<h' + level + '>' + inline(heading[2].trim()) + '</h' + level + '>');
      i++;
      continue;
    }

    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      flush();
      out.push('<hr>');
      i++;
      continue;
    }

    if (/^\s*\|.*\|\s*$/.test(line) && i + 1 < lines.length && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1])) {
      flush();
      const cells = (row) => row.trim().replace(/^\||\|$/g, '').split('|').map((c) => inline(c.trim()));
      const head = cells(line);
      i += 2;
      const rows = [];
      while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) rows.push(cells(lines[i++]));
      out.push('<table><thead><tr>' + head.map((c) => '<th>' + c + '</th>').join('') +
        '</tr></thead><tbody>' +
        rows.map((r) => '<tr>' + r.map((c) => '<td>' + c + '</td>').join('') + '</tr>').join('') +
        '</tbody></table>');
      continue;
    }

    if (/^\s*&gt;\s?/.test(line)) {
      flush();
      const quote = [];
      while (i < lines.length && /^\s*&gt;\s?/.test(lines[i])) quote.push(lines[i++].replace(/^\s*&gt;\s?/, ''));
      out.push('<blockquote>' + renderMarkdown(unesc(quote.join('\n'))) + '</blockquote>');
      continue;
    }

    const bullet = /^\s*[-*+]\s+(.*)$/;
    const ordered = /^\s*\d+[.)]\s+(.*)$/;
    if (bullet.test(line) || ordered.test(line)) {
      flush();
      const isBullet = bullet.test(line);
      const pattern = isBullet ? bullet : ordered;
      const items = [];
      while (i < lines.length) {
        const match = lines[i].match(pattern);
        if (match) {
          items.push(match[1]);
          i++;
        } else if (items.length && /^\s{2,}\S/.test(lines[i])) {
          items[items.length - 1] += '<br>' + lines[i].trim();
          i++;
        } else break;
      }
      const tag = isBullet ? 'ul' : 'ol';
      out.push('<' + tag + '>' + items.map((item) => '<li>' + inline(item) + '</li>').join('') + '</' + tag + '>');
      continue;
    }

    paragraph.push(line.trim());
    i++;
  }
  flush();
  return out.join('\n');
}
