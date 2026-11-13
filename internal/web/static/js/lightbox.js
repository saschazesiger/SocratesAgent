// The lightbox: images large, one at a time, over the whole window. A chat's
// images (what a tool returned, what the agent showed, what the person
// attached) and a test run's screenshots open in it. The viewer is Z-Dash's
// (DS.lightbox): thumbnails, the arrow keys, swipe, and zoom and pan on every
// device - a click (a double tap) zooms in there to the image's own pixels
// and back, a drag, the wheel or two fingers on a trackpad pan, a pinch or
// Ctrl with the wheel zooms freely, the buttons and + - 0 step it.

/**
 * lightbox shows `images` - { src, name, meta } each: where the image is
 * drawn from, what it is called, and a word before its name - from the one
 * at `start`; `opener` gets focus back when it closes. `noun` names them
 * ("Screenshots" for "Screenshot"). The bar links to the image on its own,
 * to open it whole in a tab of its own.
 */
export function lightbox(images, start, opener, { noun = 'Image' } = {}) {
  if (!images || !images.length) return null;
  const at = Math.max(0, Math.min(images.length - 1, Number(start) || 0));
  const shown = images.map((image) => {
    const name = image.name || String(image.src).split('/').pop();
    return { src: image.src, alt: name, caption: image.meta ? image.meta + ' · ' + name : name };
  });
  return DS.lightbox.show(shown, { index: at, opener, label: images.length > 1 ? noun + 's' : noun, original: true });
}
