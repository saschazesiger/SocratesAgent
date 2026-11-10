/*
 * drawerwidth.js: the session list's width at a desk, set by dragging its
 * edge (.so-drawer-sash, so-frame.css).
 *
 * The sash is a separator between the drawer and the panel: a pointer drags
 * it, the arrow keys move it while it has the focus (Home and End to the
 * ends), and a double click or Enter puts the list back to DS's own width.
 * The width is written as --so-drawer-width on the theme element (<html>)
 * with data-so-drawer-width beside it; so-frame.css turns that into DS's
 * --ds-drawer-width above the drawer breakpoint, so the top bar's lead and
 * the snackbars beside the drawer follow it, and the modal drawer of a phone
 * keeps DS's width.
 *
 * Like the list put away (session.js SIDE_KEY), the width is a fact about
 * this screen, not about the account: it stays in localStorage.
 */

const KEY = 'socrates.sidebarWidth';
// Narrower than this and New session and the empty VS Code no longer fit
// beside each other; wider than this is a panel given away for nothing.
const MIN = 220;
const MAX = 480;
// At most this share of the window, as so-frame.css clamps it (40vw).
const SHARE = 0.4;
const STEP = 16;

const root = document.documentElement;

function limit() {
  return Math.max(MIN, Math.min(MAX, Math.floor(window.innerWidth * SHARE)));
}

function clamp(px) {
  return Math.round(Math.max(MIN, Math.min(limit(), px)));
}

// DS's own width, read where no width of ours is set: the sash starts from
// it and a reset goes back to it.
function defaultWidth() {
  const had = root.hasAttribute('data-so-drawer-width');
  if (had) root.removeAttribute('data-so-drawer-width');
  const px = parseFloat(getComputedStyle(root).getPropertyValue('--ds-drawer-width')) || 256;
  if (had) root.setAttribute('data-so-drawer-width', '');
  return px;
}

function saved() {
  try {
    const px = parseFloat(localStorage.getItem(KEY) || '');
    return Number.isFinite(px) ? px : 0;
  } catch { return 0; /* private window */ }
}

/**
 * mountDrawerWidth wires the sash. onChange is called once the list has its
 * new width and the drag (or the key) is over: the terminal is fitted then.
 */
export function mountDrawerWidth({ sash, drawer, onChange }) {
  if (!sash) return;
  let width = 0; // 0: DS's own width

  function current() { return width || defaultWidth(); }

  function apply(px) {
    width = px;
    if (px) {
      root.style.setProperty('--so-drawer-width', px + 'px');
      root.setAttribute('data-so-drawer-width', '');
    } else {
      root.style.removeProperty('--so-drawer-width');
      root.removeAttribute('data-so-drawer-width');
    }
    sash.setAttribute('aria-valuemin', String(MIN));
    sash.setAttribute('aria-valuemax', String(limit()));
    sash.setAttribute('aria-valuenow', String(Math.min(current(), limit())));
  }

  function remember() {
    try {
      if (width) localStorage.setItem(KEY, String(width));
      else localStorage.removeItem(KEY);
    } catch { /* private window */ }
  }

  function settle(px) {
    const before = width;
    apply(px);
    remember();
    if (onChange && before !== width) onChange();
  }

  const stored = saved();
  apply(stored ? Math.max(MIN, Math.min(MAX, stored)) : 0);

  // Dragging: the list's width follows the pointer from where it was taken,
  // in a frame at a time; the page selects nothing meanwhile.
  let drag = null;
  sash.addEventListener('pointerdown', (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    sash.setPointerCapture(event.pointerId);
    drag = { x: event.clientX, from: Math.min(current(), limit()), start: width, frame: 0, px: 0 };
    root.setAttribute('data-so-resizing', '');
  });
  sash.addEventListener('pointermove', (event) => {
    if (!drag) return;
    drag.px = clamp(drag.from + event.clientX - drag.x);
    if (drag.frame) return;
    drag.frame = requestAnimationFrame(() => {
      drag.frame = 0;
      apply(drag.px);
    });
  });
  const end = () => {
    if (!drag) return;
    cancelAnimationFrame(drag.frame);
    const { px, start } = drag;
    drag = null;
    root.removeAttribute('data-so-resizing');
    if (px) apply(px);
    remember();
    if (onChange && width !== start) onChange();
  };
  sash.addEventListener('pointerup', end);
  sash.addEventListener('pointercancel', end);
  sash.addEventListener('lostpointercapture', end);

  sash.addEventListener('dblclick', () => settle(0));

  sash.addEventListener('keydown', (event) => {
    const now = Math.min(current(), limit());
    let px = null;
    if (event.key === 'ArrowLeft') px = clamp(now - STEP);
    else if (event.key === 'ArrowRight') px = clamp(now + STEP);
    else if (event.key === 'Home') px = MIN;
    else if (event.key === 'End') px = limit();
    else if (event.key === 'Enter') px = 0;
    if (px === null) return;
    event.preventDefault();
    settle(px);
  });

  // A window made narrower clamps the list (so-frame.css does it at once);
  // the separator says the width it has.
  window.addEventListener('resize', () => apply(width));
  if (drawer) sash.setAttribute('aria-controls', drawer.id);
}
