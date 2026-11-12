// A thumbnail: an image of a chat drawn small, which opens large in the
// lightbox. Its own module, as both the parts (chatparts.js) and Markdown
// (md.js) draw it.

import { html } from './ui.js';

/**
 * thumbHtml is an image of the chat drawn small - a tool's screenshot, a
 * picture the agent showed, a file the person attached - as a button that
 * opens every image of the chat in the lightbox at this one (chatview.js,
 * data-action "image").
 */
export function thumbHtml(src, name, { small = false } = {}) {
  return html`<button class="${small ? 'so-thumb so-thumb--sm ds-pressable' : 'so-thumb ds-pressable'}" type="button" data-action="image" data-src="${src}" data-name="${name}" aria-label="${'Open the image ' + name}" data-tip="${name}"><img class="so-thumb__img" src="${src}" alt="${name}" loading="lazy" decoding="async"></button>`;
}
