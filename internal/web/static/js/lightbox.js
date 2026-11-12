// The lightbox: images large, one at a time, in a full dialog over the page.
// A chat's images (what a tool returned, what the agent showed, what the
// person attached) and a test run's screenshots open in it.

import { el } from './api.js';
import { html, raw, icon, morph, showOverlay, hideOverlay } from './ui.js';

let seq = 0;

/**
 * lightbox shows `images` - { src, name, meta } each: where the image is
 * drawn from, what it is called, and a word before its name - from the one
 * at `start`: ← and → (the buttons and the keys) go through them, Escape
 * closes, and the image opens whole in a tab of its own. `noun` names them
 * in the title ("Screenshot 2 of 5").
 */
export function lightbox(images, start, opener, { noun = 'Image' } = {}) {
  if (!images || !images.length) return;
  let at = Math.max(0, Math.min(images.length - 1, Number(start) || 0));
  const titleId = 'lightboxTitle' + (++seq);
  const title = el('h2', { class: 'ds-dialog__title', id: titleId });
  const close = el('button', { class: 'ds-btn ds-btn--quiet ds-btn--icon', type: 'button', 'data-ds-close': true, 'aria-label': 'Close', 'data-tip': 'Close' });
  morph(close, html`${raw(icon('close', 20))}`);
  const figure = el('figure', { class: 'so-lightbox__figure' });
  const whole = el('a', { class: 'ds-btn ds-btn--text', target: '_blank', rel: 'noopener' });
  morph(whole, html`${raw(icon('external', 18))}<span>Open</span>`);
  const prev = el('button', { class: 'ds-btn', type: 'button', 'data-go': '-1' });
  morph(prev, html`${raw(icon('arrowLeft', 18))}<span>Previous</span>`);
  const next = el('button', { class: 'ds-btn', type: 'button', 'data-go': '1' });
  morph(next, html`<span>Next</span>${raw(icon('arrowRight', 18))}`);
  const many = images.length > 1;
  const dialog = el('dialog', { class: 'ds-dialog ds-dialog--full so-lightbox', 'aria-labelledby': titleId },
    el('div', { class: 'ds-dialog__backdrop', 'data-ds-close': true }),
    el('div', { class: 'ds-dialog__box' },
      el('div', { class: 'ds-dialog__head' }, close, title),
      el('div', { class: 'ds-dialog__body so-lightbox__body' }, figure),
      el('div', { class: 'ds-dialog__actions so-lightbox__actions' }, whole, ...(many ? [prev, next] : []))));

  const draw = () => {
    const image = images[at];
    const name = image.name || String(image.src).split('/').pop();
    title.textContent = many ? noun + ' ' + (at + 1) + ' of ' + images.length : noun;
    whole.setAttribute('href', image.src);
    morph(figure, html`<img class="so-lightbox__img" src="${image.src}" alt="${name}"><figcaption class="so-lightbox__caption">${image.meta ? html`<span class="ds-meta">${image.meta + ' · '}</span>` : ''}${name}</figcaption>`);
    for (const [b, off] of [[prev, at === 0], [next, at === images.length - 1]]) {
      if (off) b.setAttribute('aria-disabled', 'true'); else b.removeAttribute('aria-disabled');
    }
  };
  const go = (step) => {
    const to = at + step;
    if (to < 0 || to >= images.length) return;
    at = to;
    draw();
  };
  dialog.addEventListener('click', (event) => {
    const b = event.target.closest('[data-go]');
    if (b && b.getAttribute('aria-disabled') !== 'true') go(Number(b.getAttribute('data-go')));
  });
  dialog.addEventListener('keydown', (event) => {
    if (event.key === 'ArrowLeft') { event.preventDefault(); go(-1); }
    if (event.key === 'ArrowRight') { event.preventDefault(); go(1); }
  });
  dialog.addEventListener('close', () => hideOverlay(dialog, { remove: true }), { once: true });
  draw();
  showOverlay(dialog, opener);
}
