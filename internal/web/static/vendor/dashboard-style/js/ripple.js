/**
 * Pointer ripple (components/ripple.css): a press on an action grows a
 * circle of its content colour from the point that was pressed, and fades
 * it once the press ends. Without attach() the flat state layer stays the
 * press feedback.
 *
 *   const ripples = DS.ripple.attach(document);   // once, for every action on the page
 *   ripples.detach();
 *
 * attach(root) adds .ds-ripples to the root element (<html> for document),
 * which positions and clips the hosts (ripple.css), and listens once on
 * the root: pointerdown for the pointer, Enter and Space for a centred
 * ripple on a focused host. Hosts: .ds-btn, .ds-chip (the ripple goes into
 * .ds-chip__body), .ds-segmented__option, .ds-drawer__item,
 * :is(button, a).ds-tile, .ds-card--interactive (also the <div> form,
 * whose .ds-card__action stretches over it), .ds-table__sort, the body rows
 * of .ds-table--interactive (the ripple goes into the row's first cell and
 * covers the row; Enter on .ds-table__row-link or .ds-card__action ripples
 * the row or card; a press on another link or control inside a row or
 * a <div> card is that control's own, so the host gets none), .ds-radio (a radio card, also
 * when its own input is pressed), .ds-combo__toggle, .ds-menu__item,
 * .ds-errors summary and [data-ds-ripple]; data-ds-ripple="off" on a host
 * or an ancestor opts out. Disabled, busy, done and aria-disabled hosts (a chip or radio
 * card whose input is disabled, .ds-radio--disabled), switch, checkbox and
 * radio inputs (their halo is the press), and presses other than the
 * primary button get none. Not hosts, by design: inline links
 * (.ds-link-btn, links in text), .ds-check rows, combobox options
 * (pointer-down selects and closes the list) and native field parts.
 *
 * The ripple is <span class="ds-press-ripple" aria-hidden="true"
 * data-ds-keep> with --ds-ripple-x / --ds-ripple-y (its centre, px from the
 * host's padding box) and --ds-ripple-r (the distance to the farthest
 * corner). At most one grows per host. On release it gets
 * .ds-press-ripple--out, not before --ds-dur-instant after the press, and
 * goes once its fade settled. The host carries .ds-pressed from the press
 * to the release, also at least --ds-dur-instant, so a quick click still
 * shows the whole press.
 *
 * iOS Safari applies :active only on pages with a touchstart listener:
 * attach() adds a passive one.
 *
 * Chips (D22): attach() also listens for change on .ds-chip__input in a
 * .ds-chips row, the one call that wires control motion. A toggle that
 * keeps every chip on its line plays the check slot's width from where
 * it is to its end (el.animate() on the slot with its CSS timing, so the
 * neighbours follow the reflow and a rapid toggle continues from the
 * current width); a toggle that moves a chip to another line lets the
 * slot land at once and glides the chips to their new places by flip(),
 * each chip on an opaque backing in the colour behind the row (the row's
 * data-ds-glide and --ds-chips-backdrop, for the glide), so a chip never
 * shows the one it crosses through its transparent body. The toggled
 * chip keeps pace on its neighbour's timing: a body that grew is clipped
 * back to its old width and opens with the neighbour's glide
 * (clip-path), and its label glides from where it was (translate), so
 * its edge and the next chip's never overlap; a body that shrank only
 * glides its label. It measures the end layout with the row's
 * data-ds-snap (the slots without their transition) for one forced style
 * pass. A programmatic
 * `checked` change has no event: there CSS alone runs and a wrap jumps.
 *
 * ES2022 (Safari 16.4); a module may import from an earlier module (the
 * build bundles it into dist/ds.js as DS.ripple); newer APIs only behind
 * feature detection, named under Progressive:.
 */
import { MOTION_FALLBACKS, flip, motionDuration, motionEasing, settled, travelDuration } from './motion.js';

/** The actions that get a ripple. */
export const RIPPLE_HOSTS = '.ds-btn, .ds-chip, .ds-segmented__option, .ds-drawer__item, :is(button, a).ds-tile, .ds-card--interactive, .ds-table__sort, .ds-table--interactive tbody tr:not(.ds-table__detail), .ds-radio, .ds-combo__toggle, .ds-menu__item, .ds-errors summary, [data-ds-ripple]';

/** The link that stands for its host (stretched over a row or a <div> card): Enter on it ripples the host. */
const HOST_LINK = '.ds-table__row-link, .ds-card__action';

/** Hosts whose own input is part of the host: pressing the input ripples the host. */
const OWNS_INPUT = '.ds-chip, .ds-radio';

/** Controls whose press is their own halo, not a ripple. */
const OWN_PRESS = 'input[type="checkbox"], input[type="radio"], .ds-switch';

/** Hosts a link stretches over (HOST_LINK): their other controls keep their own press. */
const STRETCHED = '.ds-card--interactive, .ds-table--interactive tbody tr';

/** What a press inside a stretched host may land on besides its link (the CSS's press exclusion, plus [tabindex]). */
const CONTROLS = 'a[href], button, input, select, textarea, label, summary, [tabindex]';

/**
 * @typedef {object} Press
 * @property {HTMLElement} span  the ripple
 * @property {number} at  when it started (performance.now())
 * @property {boolean} released
 */

/** The growing ripple of each host. @type {WeakMap<Element, Press>} */
const presses = new WeakMap();

/** The handle of each attached root. @type {WeakMap<Node, { detach: () => void }>} */
const attached = new WeakMap();

const now = () => (typeof performance !== 'undefined' ? performance.now() : Date.now());

/** The host of an event target inside `root`, or null. */
function hostOf(target, root) {
  const element = target?.nodeType === 1 ? target : target?.parentElement;
  const host = element?.closest?.(RIPPLE_HOSTS);
  if (!host || !root.contains(host)) return null;
  if (host.closest('[data-ds-ripple="off"]')) return null;
  return host;
}

/** Where the ripple is measured and placed: the chip's body, else the host itself. */
function surfaceOf(host) {
  return host.classList.contains('ds-chip') ? host.querySelector('.ds-chip__body') : host;
}

/**
 * The element the ripple goes into: a table row cannot hold a span, so
 * it goes into the row's first cell (a <td> or a <th> row header),
 * positioned against the row (it covers the row and clips the circle
 * itself, ripple.css).
 */
function holderOf(surface) {
  return surface.tagName === 'TR' ? surface.cells[0] ?? null : surface;
}

/**
 * Hosts that take no press: disabled, aria-disabled, busy, done, a chip or
 * radio card whose input is disabled. `:disabled`, not the property: it also
 * holds for an input in a disabled <fieldset> (`.ds-chips` is one).
 */
function inactive(host) {
  if (host.matches(':disabled') || host.getAttribute('aria-disabled') === 'true' || host.getAttribute('aria-busy') === 'true') return true;
  if (host.classList.contains('ds-btn--done') || host.classList.contains('ds-radio--disabled')) return true;
  if (host.matches(OWNS_INPUT)) {
    const input = host.querySelector('input');
    if (input?.matches(':disabled')) return true;
  }
  return false;
}

/**
 * Whether a press on `target` belongs to another control inside a host
 * that a link stretches over (a plain link, a field, a label in a row or
 * a <div> card): that control has its own press, so the host neither
 * ripples nor holds .ds-pressed, as its :active layer does not show.
 */
function pressesOther(host, target) {
  if (!host.matches(STRETCHED)) return false;
  const element = target?.nodeType === 1 ? target : target?.parentElement;
  const control = element?.closest?.(CONTROLS);
  return Boolean(control && control !== host && host.contains(control) && !control.matches(HOST_LINK));
}

/** Fades a ripple out and removes it once the fade settled. */
function fade(span) {
  span.classList.add('ds-press-ripple--out');
  settled(span, { event: 'transitionend', property: 'opacity' }).then(() => span.remove());
}

/**
 * Releases a press: the ripple fades out (not before --ds-dur-instant after
 * the press) and the host loses .ds-pressed, unless a newer press took over.
 */
function release(host, press) {
  if (press.released) return;
  press.released = true;
  const wait = Math.max(0, motionDuration(host, '--ds-dur-instant') - (now() - press.at));
  setTimeout(() => {
    if (presses.get(host) === press) {
      presses.delete(host);
      host.classList.remove('ds-pressed');
    }
    fade(press.span);
  }, wait);
}

/**
 * Starts a ripple on `host` at (x, y) in client coordinates, or centred
 * without them; returns the press.
 */
function start(host, x, y) {
  const surface = surfaceOf(host);
  if (!surface) return null;
  const view = surface.ownerDocument?.defaultView;
  // The defensive guard: a ripple outside a positioned host would escape it.
  if (view?.getComputedStyle(surface).position === 'static') return null;
  // One growing ripple per host: a new press fades the one still held.
  const previous = presses.get(host);
  if (previous && !previous.released) {
    previous.released = true;
    fade(previous.span);
  }
  const holder = holderOf(surface);
  if (!holder) return null;
  const rect = surface.getBoundingClientRect();
  // The padding box, in the host's own (untransformed) pixels. A table row
  // has no padding box of its own: its border box (a gliding row's
  // translate included, so the circle starts under the pointer).
  const row = surface !== holder;
  const scaleX = !row && surface.offsetWidth ? rect.width / surface.offsetWidth : 1;
  const scaleY = !row && surface.offsetHeight ? rect.height / surface.offsetHeight : 1;
  const width = row ? rect.width : surface.clientWidth;
  const height = row ? rect.height : surface.clientHeight;
  const left = x === undefined ? width / 2 : (x - rect.left) / (scaleX || 1) - (row ? 0 : surface.clientLeft);
  const top = y === undefined ? height / 2 : (y - rect.top) / (scaleY || 1) - (row ? 0 : surface.clientTop);
  const radius = Math.hypot(Math.max(left, width - left), Math.max(top, height - top));
  const span = surface.ownerDocument.createElement('span');
  span.className = 'ds-press-ripple';
  span.setAttribute('aria-hidden', 'true');
  span.setAttribute('data-ds-keep', '');
  span.style.setProperty('--ds-ripple-x', `${Math.round(left * 100) / 100}px`);
  span.style.setProperty('--ds-ripple-y', `${Math.round(top * 100) / 100}px`);
  span.style.setProperty('--ds-ripple-r', `${Math.round(radius * 100) / 100}px`);
  holder.append(span);
  host.classList.add('ds-pressed');
  /** @type {Press} */
  const press = { span, at: now(), released: false };
  presses.set(host, press);
  return press;
}

/** Ends the press of `host`, if one is growing. */
function end(host) {
  const press = presses.get(host);
  if (press) release(host, press);
}

/* ── Chips across a wrap ───────────────────────────── */

/** The running width glide of each chip's check slot. @type {WeakMap<Element, Animation>} */
const slotGlides = new WeakMap();

/** A computed time (the first of a list: '0.25s', '50ms') in ms. */
function timeOf(text) {
  const match = /^\s*(-?\d*\.?\d+)(m?s)\s*$/.exec(String(text ?? '').split(',')[0]);
  return match ? Number(match[1]) * (match[2] === 's' ? 1000 : 1) : 0;
}

/** The first easing of a computed list (cubic-bezier() holds commas of its own). */
function easingOf(text) {
  const match = /^\s*([\w-]+\([^)]*\)|[\w-]+)/.exec(String(text ?? ''));
  return match ? match[1] : 'ease';
}

/**
 * Glides the check slot of `body` from `from` to `to` px with the timing
 * the CSS gave it (a slot that is not animated jumps, as without script).
 */
function glideSlot(body, from, to, timing) {
  if (Math.abs(from - to) < 0.5 || !(timing.duration > 0) || typeof body.animate !== 'function') return;
  try {
    const animation = body.animate([{ width: `${from}px` }, { width: `${to}px` }], {
      pseudoElement: '::after',
      duration: timing.duration,
      delay: timing.delay,
      easing: timing.easing,
      fill: 'backwards',
    });
    animation.id = 'ds-chip-slot';
    slotGlides.set(body, animation);
    const done = () => {
      if (slotGlides.get(body) === animation) slotGlides.delete(body);
    };
    animation.finished.then(done, done);
  } catch {
    // No animations of pseudo-elements: the slot lands at once.
  }
}

/** The edge glides of each chip body across a wrap (clip-path and label translate). @type {WeakMap<Element, Animation[]>} */
const edgeGlides = new WeakMap();

/** The right inset of a computed `inset()` clip-path in px (0 for none). */
function insetRight(clip) {
  const match = /^inset\(([^)]*)\)/.exec(String(clip ?? '').trim());
  if (!match) return 0;
  const sides = match[1].split(/\s+round\s+/)[0].trim().split(/\s+/).map((value) => parseFloat(value) || 0);
  return sides.length > 1 ? sides[1] : sides[0] ?? 0;
}

/** The x of a computed `translate` in px (0 for none). */
function translateX(value) {
  return parseFloat(String(value ?? '').split(/\s+/)[0]) || 0;
}

/** What of a body glides along its edge: its children but the check (over the slot, it never moves) and a ripple. */
function labelsOf(body) {
  return Array.from(body.children).filter((child) => !child.matches('.ds-chip__check, .ds-press-ripple'));
}

/**
 * Where a chip body stands in an edge glide: the part of its width still
 * clipped and its label's offset, to continue from on a rapid toggle.
 */
function edgeState(body) {
  const view = body.ownerDocument.defaultView;
  const label = labelsOf(body)[0];
  return { clip: insetRight(view.getComputedStyle(body).clipPath), shift: label ? translateX(view.getComputedStyle(label).translate) : 0 };
}

/** Stops the edge glide of a body (it lands at once). */
function stopEdge(body) {
  for (const animation of edgeGlides.get(body) ?? []) animation.cancel();
  edgeGlides.delete(body);
}

/** el.animate() with the spring's cubic fallback where linear() is unknown. */
function animateEdge(element, keyframes, timing) {
  try {
    return element.animate(keyframes, timing);
  } catch {
    return element.animate(keyframes, { ...timing, easing: /** @type {string} */ (MOTION_FALLBACKS['--ds-ease-spatial']) });
  }
}

/**
 * Across a wrap the slot of a toggled chip lands at once while flip()
 * glides its neighbours: the body keeps pace on the timing of the next
 * chip on its line (else the travel of `grow` on the spring). A body that
 * grew by `grow` px is clipped back to its old width and opens with the
 * neighbour, so its edge and the neighbour's never overlap; its label
 * glides from where it was. A body that shrank cannot paint its old width:
 * its label glides back, clipped to the body. A keyboard ring (an outline)
 * stays inside the clip.
 */
function glideEdge(body, grow, shift, timing) {
  if (typeof body.animate !== 'function' || (Math.abs(grow) < 0.5 && Math.abs(shift) < 0.5)) return;
  const view = body.ownerDocument.defaultView;
  const style = view.getComputedStyle(body);
  const radius = parseFloat(style.borderTopRightRadius) || 0;
  const ring = style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) > 0
    ? Math.max(0, parseFloat(style.outlineOffset) || 0) + parseFloat(style.outlineWidth) + 1
    : 0;
  const clip = (right) => `inset(${-ring}px ${Math.round(right * 100) / 100}px ${-ring}px ${-ring}px round ${radius + ring}px)`;
  const options = { duration: timing.duration, easing: timing.easing };
  const running = [];
  try {
    running.push(animateEdge(body, [{ clipPath: clip(Math.max(0, grow)) }, { clipPath: clip(0) }], options));
    if (Math.abs(shift) >= 0.5) {
      for (const label of labelsOf(body)) running.push(animateEdge(label, [{ translate: `${Math.round(shift * 100) / 100}px 0px` }, { translate: '0px 0px' }], options));
    }
  } catch {
    // No such animations: the body lands at once.
    for (const animation of running) animation.cancel();
    return;
  }
  for (const animation of running) animation.id = 'ds-chip-edge';
  edgeGlides.set(body, running);
  const done = () => {
    if (edgeGlides.get(body) === running) edgeGlides.delete(body);
  };
  Promise.allSettled(running.map((animation) => animation.finished)).then(done);
}

/** The timing of a running ds-flip glide of `element`, or null. */
function flipTiming(element) {
  const animation = element?.getAnimations?.().find((item) => item.id === 'ds-flip' && item.playState !== 'finished' && item.playState !== 'idle');
  const timing = animation?.effect?.getTiming?.();
  return timing && Number(timing.duration) > 0 ? { duration: Number(timing.duration), easing: timing.easing || 'linear' } : null;
}

/**
 * The runtime state a DS helper owns on an element right now, which
 * DS.dom.morphHtml keeps (present or absent) until the helper lets go:
 * the record of motion.js under the same symbol of the global registry.
 * These two helpers are motion.js's own, repeated here.
 */
const OWNED = Symbol.for('ds.owned');

/** `kind`: 'attrs', 'classes' or 'props' (a style property, kebab-case). */
function own(element, kind, name) {
  if (!element || typeof element !== 'object') return;
  const record = element[OWNED] ?? (element[OWNED] = { attrs: new Map(), classes: new Map(), props: new Map() });
  record[kind].set(name, (record[kind].get(name) ?? 0) + 1);
}

function disown(element, kind, name) {
  const map = element?.[OWNED]?.[kind];
  const count = map?.get(name) ?? 0;
  if (count > 1) map.set(name, count - 1);
  else map?.delete(name);
}

/** Chip rows that carry the glide backing (owned once, however often backChips() runs). */
const backedRows = new WeakSet();

/** Whether a computed colour paints anything (alpha above 0). */
function paints(colour) {
  if (!colour || colour === 'transparent') return false;
  const inner = /\(([^)]*)\)/.exec(colour)?.[1] ?? '';
  const slash = inner.split('/');
  if (slash.length > 1) return parseFloat(slash[1]) > 0;
  const parts = inner.split(',');
  return parts.length === 4 ? parseFloat(parts[3]) > 0 : true;
}

/** The colour behind `element`: the background of the nearest ancestor (itself included) that paints one. */
function backdropOf(element) {
  const view = element.ownerDocument.defaultView;
  for (let node = element; node && node.nodeType === 1; node = node.parentElement) {
    const colour = view.getComputedStyle(node).backgroundColor;
    if (paints(colour)) return colour;
  }
  return '';
}

/**
 * While chips glide across a wrap, each chip gets an opaque backing in the
 * colour behind the row (data-ds-glide, --ds-chips-backdrop): a chip that
 * crosses another, transparent or still fading to its fill, never shows
 * the other one through it. At rest the backing matches what is behind.
 * Both are owned while they last, so a re-render of an ancestor
 * (DS.dom.morphHtml) mid-glide keeps them.
 */
function backChips(row) {
  if (!backedRows.has(row)) {
    backedRows.add(row);
    own(row, 'attrs', 'data-ds-glide');
    own(row, 'props', '--ds-chips-backdrop');
  }
  const backdrop = backdropOf(row);
  if (backdrop) row.style.setProperty('--ds-chips-backdrop', backdrop);
  else row.style.removeProperty('--ds-chips-backdrop');
  row.setAttribute('data-ds-glide', '');
}

/** Takes the backing away once no chip of `row` glides any more. */
function unbackChips(row) {
  const running = Array.from(row.children).flatMap((child) => child.getAnimations?.() ?? [])
    .filter((animation) => animation.id === 'ds-flip' && animation.playState !== 'finished' && animation.playState !== 'idle');
  if (running.length) {
    Promise.allSettled(running.map((animation) => animation.finished)).then(() => unbackChips(row));
    return;
  }
  row.removeAttribute('data-ds-glide');
  row.style.removeProperty('--ds-chips-backdrop');
  if (backedRows.delete(row)) {
    disown(row, 'attrs', 'data-ds-glide');
    disown(row, 'props', '--ds-chips-backdrop');
  }
}

/**
 * A chip of `row` was toggled (its transitions have not advanced yet):
 * the slots glide, or, when a chip changes its line, the chips do.
 */
function chipToggled(row) {
  const view = row.ownerDocument.defaultView;
  const bodies = Array.from(row.querySelectorAll('.ds-chip__body'));
  const children = Array.from(row.children);
  const tops = children.map((child) => child.getBoundingClientRect().top);
  // Where an edge glide in flight stands, then it lands (flip() measures the bodies unclipped).
  const edges = bodies.map((body) => {
    const state = edgeGlides.has(body) ? edgeState(body) : { clip: 0, shift: 0 };
    stopEdge(body);
    return state;
  });
  let wrapped = false;
  /** @type {{ body: Element, grow: number, shift: number, next: Element|null }[]} */
  let toggled = [];
  const glide = flip(row, () => {
    // Where each slot is now (the old width, or a glide in flight) and the timing of its new state.
    const slots = bodies.map((body) => {
      const style = view.getComputedStyle(body, '::after');
      return {
        body,
        from: parseFloat(style.width) || 0,
        timing: { duration: timeOf(style.transitionDuration), delay: timeOf(style.transitionDelay), easing: easingOf(style.transitionTimingFunction) },
      };
    });
    for (const body of bodies) {
      slotGlides.get(body)?.cancel();
      slotGlides.delete(body);
    }
    // The end layout: the slots at their end width.
    row.setAttribute('data-ds-snap', '');
    wrapped = children.some((child, index) => Math.abs(child.getBoundingClientRect().top - tops[index]) > 1);
    const ends = slots.map((slot) => parseFloat(view.getComputedStyle(slot.body, '::after').width) || 0);
    if (wrapped) {
      // The bodies whose slot changes, each with the next item on its line in the end layout.
      toggled = slots.map((slot, index) => {
        const grow = ends[index] - slot.from;
        const chip = slot.body.closest('.ds-chip');
        const next = chip?.parentElement === row ? chip.nextElementSibling : null;
        const same = next && Math.abs(next.getBoundingClientRect().top - chip.getBoundingClientRect().top) <= 1;
        return { body: slot.body, grow: grow + edges[index].clip, shift: edges[index].shift - grow, next: same ? next : null };
      }).filter((entry) => Math.abs(entry.grow) >= 0.5 || Math.abs(entry.shift) >= 0.5);
    }
    // Measured (a forced style pass): the slots keep their end width without a new transition.
    row.removeAttribute('data-ds-snap');
    // A chip changed its line: flip() glides every chip from its old place, each on its backing.
    if (wrapped) {
      backChips(row);
      return;
    }
    // Same lines: the slots glide from where they were (flip() then finds nothing moved).
    slots.forEach((slot, index) => glideSlot(slot.body, slot.from, ends[index], slot.timing));
  });
  if (!wrapped) return;
  // flip() has started its glides: each toggled body keeps pace with the next chip on its line.
  for (const entry of toggled) {
    const timing = flipTiming(entry.next) ?? {
      duration: travelDuration(row, Math.abs(entry.grow)),
      easing: motionEasing(row, '--ds-ease-spatial'),
    };
    glideEdge(entry.body, entry.grow, entry.shift, timing);
  }
  glide.then(() => unbackChips(row), () => unbackChips(row));
}

/** Enter activates buttons and links, Space buttons and inputs (not links, not on Enter for checkboxes). */
function activates(target, key) {
  if (key === ' ') return target.tagName !== 'A';
  if (key === 'Enter') return !target.matches?.('input[type="checkbox"], input[type="radio"]');
  return false;
}

/**
 * Adds the pointer ripple to every action inside `root` (delegated, so
 * actions rendered later have it too). Idempotent per root. An open shadow
 * root works as a root too: the listeners go on it (events keep their real
 * targets there) and .ds-ripples on the .ds-theme inside it, where ds.css
 * applies.
 * @param {Document|Element|ShadowRoot} [root]
 * @returns {{ detach: () => void }}
 */
export function attach(root = document) {
  const existing = attached.get(root);
  if (existing) return existing;
  const element = /** @type {Element} */ (root.nodeType === 9 ? /** @type {Document} */ (root).documentElement : root);
  const marked = root.nodeType === 11 ? /** @type {ShadowRoot} */ (root).querySelector('.ds-theme') ?? /** @type {ShadowRoot} */ (root).host : element;
  const doc = element.ownerDocument;
  /** The pending blur that ends a key press, per focused target. @type {WeakMap<Element, () => void>} */
  const blurs = new WeakMap();

  const onPointerDown = (/** @type {PointerEvent} */ event) => {
    if (event.button !== 0 || event.isPrimary === false) return;
    const host = hostOf(event.target, element);
    if (!host || inactive(host)) return;
    const target = /** @type {Element} */ (event.target);
    if (target.closest?.(OWN_PRESS) && !host.matches(OWNS_INPUT)) return;
    if (pressesOther(host, target)) return;
    if (!start(host, event.clientX, event.clientY)) return;
    const up = () => {
      host.removeEventListener('pointerleave', up);
      doc.removeEventListener('pointerup', up, true);
      doc.removeEventListener('pointercancel', up, true);
      end(host);
    };
    host.addEventListener('pointerleave', up);
    doc.addEventListener('pointerup', up, true);
    doc.addEventListener('pointercancel', up, true);
  };

  const onKeyDown = (/** @type {KeyboardEvent} */ event) => {
    if (event.repeat || (event.key !== 'Enter' && event.key !== ' ')) return;
    const target = /** @type {Element} */ (event.target);
    const host = hostOf(target, element);
    if (!host || inactive(host) || !activates(target, event.key)) return;
    // The host itself, the input of a chip or radio card, or the link that stands for a row or card
    // (typing in a field inside a host is no press).
    if (target !== host && !(host.matches(OWNS_INPUT) && target.tagName === 'INPUT') && !target.matches?.(HOST_LINK)) return;
    if (!start(host)) return;
    // One pending blur per target: a key press ended by keyup takes its own
    // away, so a stepper pressed again and again keeps no stale listeners.
    const stale = blurs.get(target);
    if (stale) target.removeEventListener('blur', stale);
    const leave = () => {
      blurs.delete(target);
      target.removeEventListener('blur', leave);
      end(host);
    };
    blurs.set(target, leave);
    target.addEventListener('blur', leave);
  };

  const onKeyUp = (/** @type {KeyboardEvent} */ event) => {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const target = /** @type {Element} */ (event.target);
    const pending = blurs.get(target);
    if (pending) {
      target.removeEventListener('blur', pending);
      blurs.delete(target);
    }
    const host = hostOf(target, element);
    if (host) end(host);
  };

  // A passive no-op: iOS Safari applies :active only with a touchstart listener.
  const onTouchStart = () => {};

  const onChange = (/** @type {Event} */ event) => {
    const input = /** @type {Element} */ (event.target);
    if (!input?.matches?.('.ds-chip__input')) return;
    const row = input.closest('.ds-chips');
    if (row && element.contains(row)) chipToggled(row);
  };

  marked.classList.add('ds-ripples');
  element.addEventListener('pointerdown', onPointerDown);
  element.addEventListener('keydown', onKeyDown);
  element.addEventListener('keyup', onKeyUp);
  element.addEventListener('touchstart', onTouchStart, { passive: true });
  element.addEventListener('change', onChange);

  const handle = {
    /** Removes the listeners and .ds-ripples; ripples already shown finish. */
    detach() {
      if (attached.get(root) !== handle) return;
      attached.delete(root);
      element.removeEventListener('pointerdown', onPointerDown);
      element.removeEventListener('keydown', onKeyDown);
      element.removeEventListener('keyup', onKeyUp);
      element.removeEventListener('touchstart', onTouchStart);
      element.removeEventListener('change', onChange);
      marked.classList.remove('ds-ripples');
    },
  };
  attached.set(root, handle);
  return handle;
}
