// A searchable list for picking a model or a working directory, and a closed
// one for picking a profile (dropdown).
//
// The model catalogue has several hundred entries, so a plain <select> is
// unusable and a datalist gives no control over what is shown. This is the
// Dashboard-Style combobox: type to filter, arrow keys to move, enter to take
// the highlighted entry, escape to go back to what was there before. DS opens,
// places and closes the list (DS.overlay), so it is never cut off by the
// dialog or sheet it sits in and follows its field while anything moves; this
// file filters and chooses.
//
// The microphone beside a field is the caller's (keyboard.js dictated()).

import { el } from './api.js';
import { guard } from './keyboard.js';
import { DS, html, iconEl, morph } from './ui.js';

let comboCount = 0;

// The most a list shows at once; a filter finds the rest.
const LIST_MAX = 200;

/**
 * combobox builds the control.
 *
 * options.value        current value
 * options.onChange     called with the new value
 * options.items        () => [{value, label, hint, group}]
 * options.placeholder  shown while empty
 * options.strict       only a listed entry is a value; what is typed filters
 *                      the list and nothing else. A model catalogue is open -
 *                      a model the CLI has not reported still saves - but a
 *                      working directory is not: the server keeps a list of
 *                      the places a session may work, and a typed half-path is
 *                      not one of them.
 * options.display      how a value reads in the field. Without it the value is
 *                      its own label, which is right for a model id and wrong
 *                      for anything whose value is a key.
 * options.emptyText    what the list says when nothing matches.
 *
 * Returns { node, input, setValue(value, notify), value(), isOpen(), focus() }.
 */
export function combobox(options = {}) {
  const {
    value = '',
    onChange = () => {},
    items = () => [],
    placeholder = '',
    strict = false,
    display = null,
    emptyText = 'No model matches',
  } = options;

  // shown is the text the field wears for a value.
  const shown = (one) => (display ? display(one ?? '') : (one ?? ''));

  const id = 'combo' + (++comboCount);
  let current = value ?? '';
  let filtered = [];
  let total = 0;
  let active = -1;

  const input = el('input', {
    class: 'ds-input ds-combo__input',
    type: 'text',
    value: shown(current),
    placeholder,
    spellcheck: 'false',
    autocomplete: 'off',
    role: 'combobox',
    'aria-expanded': 'false',
    'aria-autocomplete': 'list',
    'aria-controls': id + '-list',
  });
  const toggle = el('button', {
    class: 'ds-combo__toggle', type: 'button', tabindex: '-1', 'aria-label': 'Show all',
  }, iconEl('chevronDown', 20));
  const list = el('ul', { class: 'ds-combo__list', id: id + '-list', role: 'listbox' });
  const status = el('p', { class: 'ds-combo__status', role: 'status' });
  const popup = el('div', { class: 'ds-combo__popup', hidden: true }, list, status);
  const node = el('div', { class: 'ds-combo' }, input, toggle, popup);
  // The keyboard switch mutes the field like every other one (keyboard.js).
  guard(input);

  // An option's id stays the same for its value across renders, so
  // aria-activedescendant and DS's keyed patching both follow the entry
  // rather than its place in the list.
  const optionIds = new Map();
  const optionId = (one) => {
    if (!optionIds.has(one)) optionIds.set(one, id + '-' + optionIds.size);
    return optionIds.get(one);
  };

  const isOpen = () => input.getAttribute('aria-expanded') === 'true';

  function setValue(next, notify = true) {
    current = next ?? '';
    input.value = shown(current);
    if (notify) onChange(current);
  }

  // matches ranks entries: an id that starts with the query beats one that
  // merely contains it, so typing "claude" puts the Claude models on top.
  function matches(query) {
    const all = items() || [];
    total = all.length;
    const q = query.trim().toLowerCase();
    if (!q) return all.slice(0, LIST_MAX);
    const words = q.split(/\s+/);
    const scored = [];
    for (const item of all) {
      const haystack = ((item.value || '') + ' ' + (item.label || '')).toLowerCase();
      if (!words.every((word) => haystack.includes(word))) continue;
      const value = (item.value || '').toLowerCase();
      let score = 2;
      if (value === q) score = 0;
      else if (value.startsWith(q)) score = 1;
      scored.push({ item, score });
    }
    scored.sort((a, b) => a.score - b.score);
    return scored.slice(0, LIST_MAX).map((entry) => entry.item);
  }

  // render draws the filtered entries. The group an entry belongs to is said
  // in its meta line ("Anthropic · 200k"): DS has no group heading in a
  // listbox, and an uppercase eyebrow is not its style.
  function render() {
    // Nothing matches: the list hides and the popup stays with its status line.
    list.hidden = !filtered.length;
    morph(list, filtered.map((item, index) => {
      const meta = [item.group, item.hint].filter(Boolean).join(' · ');
      return html`<li class="ds-combo__option" id="${optionId(item.value)}" role="option" data-value="${item.value}" data-ds-key="${item.value}" aria-selected="${item.value === current ? 'true' : 'false'}"${index === active ? html` data-active` : ''}><span class="ds-combo__name">${item.label || item.value}</span>${meta ? html`<span class="ds-combo__meta">${meta}</span>` : ''}</li>`;
    }).join(''));
    status.textContent = filtered.length
      ? filtered.length + ' of ' + total
      : emptyText;
    markActive(true);
  }

  // markActive moves the keyboard's place without drawing the list again.
  function markActive(scroll) {
    const nodes = list.querySelectorAll('.ds-combo__option');
    nodes.forEach((option, i) => option.toggleAttribute('data-active', i === active));
    if (active >= 0 && active < nodes.length) {
      input.setAttribute('aria-activedescendant', nodes[active].id);
      if (scroll) nodes[active].scrollIntoView({ block: 'nearest' });
    } else {
      input.removeAttribute('aria-activedescendant');
    }
  }

  function open(query) {
    filtered = matches(query ?? '');
    // Start on the entry that is already chosen, so enter is a no-op.
    active = filtered.findIndex((item) => item.value === current);
    render();
    if (!isOpen()) DS.overlay.open(popup, { opener: input });
  }

  function close() {
    // A strict field shows the entry it is on, never the half-typed filter
    // that was used to find one.
    if (strict) input.value = shown(current);
    input.removeAttribute('aria-activedescendant');
    if (isOpen()) DS.overlay.close(popup);
  }

  function choose(index) {
    const item = filtered[index];
    if (!item) return;
    setValue(item.value);
    close();
  }

  input.addEventListener('focus', () => {
    // In a strict field the text is a label, so typing has to replace it
    // rather than run on from the end of it.
    if (strict) input.select();
    open('');
  });
  input.addEventListener('input', () => {
    if (!strict) {
      // The typed text is the value straight away, so a model that is not in
      // the catalogue still saves.
      current = input.value;
      onChange(current);
    }
    open(input.value);
  });

  input.addEventListener('keydown', (event) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        if (!isOpen()) return open(input.value);
        active = Math.min(active + 1, filtered.length - 1);
        markActive(true);
        break;
      case 'ArrowUp':
        event.preventDefault();
        if (!isOpen()) return open(input.value);
        active = Math.max(active - 1, 0);
        markActive(true);
        break;
      case 'Home':
        if (!isOpen()) return;
        event.preventDefault();
        active = 0;
        markActive(true);
        break;
      case 'End':
        if (!isOpen()) return;
        event.preventDefault();
        active = filtered.length - 1;
        markActive(true);
        break;
      case 'Enter':
        if (!isOpen()) return;
        event.preventDefault();
        if (active >= 0) choose(active);
        else close();
        break;
      case 'Escape':
        if (!isOpen()) return;
        // The Escape is the list's: the sheet around it stays open, and the
        // field goes back to what it was before the typing.
        event.preventDefault();
        input.value = shown(current);
        close();
        break;
      case 'Tab':
        close();
        break;
    }
  });

  // A press on an option chooses on click, so the field must keep the focus
  // through the pointerdown; blur therefore only ever means "somewhere else".
  input.addEventListener('blur', () => close());

  // One listener for every option, ever: the list is patched in place, and an
  // option kept by the patch keeps the listeners it had.
  // The chevron too: a press on it that took the focus would close the list
  // on blur only for the click to open it again.
  for (const part of [list, toggle]) part.addEventListener('pointerdown', (event) => event.preventDefault());
  list.addEventListener('click', (event) => {
    const option = event.target.closest('.ds-combo__option');
    if (!option) return;
    choose(filtered.findIndex((item) => item.value === option.dataset.value));
  });
  list.addEventListener('pointermove', (event) => {
    const option = event.target.closest('.ds-combo__option');
    if (!option) return;
    const index = filtered.findIndex((item) => item.value === option.dataset.value);
    if (index !== active) {
      active = index;
      markActive(false);
    }
  });

  toggle.addEventListener('click', () => {
    if (isOpen()) {
      close();
      return;
    }
    input.focus();
    if (!isOpen()) open('');
  });

  return {
    node,
    input,
    setValue,
    value: () => current,
    isOpen,
    focus: () => input.focus(),
  };
}


/**
 * dropdown builds a closed choice that is not typed into: the DS combobox
 * with a button for its field, so the list is opened, placed and closed like
 * every other one (DS.overlay), and entries that say more than a name. It is
 * for a short list whose entries carry what the choice is made by - a profile
 * and how much of its account is left - where a row of buttons runs out of
 * width after three and a search box has nothing to search.
 *
 * options.value        current value
 * options.onChange     called with the new value when one is picked
 * options.items        () => [{value, ...}]
 * options.renderItem   (item, chosen) => html`` markup, the inside of an
 *                      entry in the open list (.ds-combo__name and
 *                      .ds-combo__meta, and whatever else it needs)
 * options.renderValue  (item) => html`` markup, what the closed field shows;
 *                      the entry's label when left out
 * options.label        the field's accessible name
 * options.id           the field's id, for a <label for>
 *
 * Returns { node, button, setValue(value, notify), value(), refresh(), isOpen(), focus() }.
 */
export function dropdown(options = {}) {
  const {
    value = '',
    onChange = () => {},
    items = () => [],
    renderItem = (item) => html`<span class="ds-combo__name">${item.label || item.value}</span>`,
    renderValue = (item) => html`${item.label || item.value}`,
    label = '',
    id: fieldId = '',
  } = options;

  const id = 'combo' + (++comboCount);
  let current = value ?? '';
  let all = [];
  let active = -1;

  const face = el('span', { class: 'so-drop__face' });
  const button = el('button', {
    class: 'ds-input ds-combo__input so-drop__field', type: 'button', id: fieldId || null,
    role: 'combobox', 'aria-haspopup': 'listbox', 'aria-expanded': 'false', 'aria-controls': id + '-list',
    'aria-label': label || null,
  }, face);
  const toggle = el('button', {
    class: 'ds-combo__toggle', type: 'button', tabindex: '-1', 'aria-label': 'Show all',
  }, iconEl('chevronDown', 20));
  const list = el('ul', { class: 'ds-combo__list', id: id + '-list', role: 'listbox', 'aria-label': label || null });
  const popup = el('div', { class: 'ds-combo__popup', hidden: true }, list);
  const node = el('div', { class: 'ds-combo so-drop' }, button, toggle, popup);

  const isOpen = () => button.getAttribute('aria-expanded') === 'true';
  const chosenItem = () => (items() || []).find((item) => item.value === current) || null;
  // An option's id is its place in the list, which is drawn whole on every
  // opening.
  const optionId = (index) => id + '-opt-' + index;

  function drawFace() {
    const item = chosenItem();
    morph(face, item ? renderValue(item) : '');
  }

  function setValue(next, notify = true) {
    current = next ?? '';
    drawFace();
    if (notify) onChange(current);
  }

  function render() {
    morph(list, all.map((item, index) => {
      const chosen = item.value === current;
      return html`<li class="ds-combo__option" id="${optionId(index)}" role="option" data-value="${item.value}" data-ds-key="${item.value}" aria-selected="${chosen ? 'true' : 'false'}"${index === active ? html` data-active` : ''}>${renderItem(item, chosen)}</li>`;
    }).join(''));
    markActive(true);
  }

  function markActive(scroll) {
    const nodes = list.querySelectorAll('.ds-combo__option');
    nodes.forEach((option, i) => option.toggleAttribute('data-active', i === active));
    if (active >= 0 && active < nodes.length) {
      button.setAttribute('aria-activedescendant', nodes[active].id);
      if (scroll) nodes[active].scrollIntoView({ block: 'nearest' });
    } else {
      button.removeAttribute('aria-activedescendant');
    }
  }

  function open() {
    all = items() || [];
    active = all.findIndex((item) => item.value === current);
    render();
    if (!isOpen()) DS.overlay.open(popup, { opener: button });
  }

  function close() {
    button.removeAttribute('aria-activedescendant');
    if (isOpen()) DS.overlay.close(popup);
  }

  function choose(index) {
    const item = all[index];
    if (!item) return;
    close();
    setValue(item.value);
  }

  button.addEventListener('click', () => {
    if (isOpen()) close();
    else open();
  });

  button.addEventListener('keydown', (event) => {
    switch (event.key) {
      case 'ArrowDown':
      case 'ArrowUp': {
        event.preventDefault();
        if (!isOpen()) return open();
        const step = event.key === 'ArrowDown' ? 1 : -1;
        active = Math.max(0, Math.min(all.length - 1, active + step));
        markActive(true);
        break;
      }
      case 'Home':
      case 'End':
        if (!isOpen()) return;
        event.preventDefault();
        active = event.key === 'Home' ? 0 : all.length - 1;
        markActive(true);
        break;
      case 'Enter':
      case ' ':
        if (!isOpen()) return;
        event.preventDefault();
        if (active >= 0) choose(active);
        else close();
        break;
      case 'Escape':
        if (!isOpen()) return;
        // The Escape is the list's: the sheet around it stays open.
        event.preventDefault();
        close();
        break;
      case 'Tab':
        close();
        break;
    }
  });

  // The field keeps the focus through a press on the list or the chevron, so
  // blur only ever means "somewhere else".
  button.addEventListener('blur', () => close());
  for (const part of [list, toggle]) part.addEventListener('pointerdown', (event) => event.preventDefault());
  toggle.addEventListener('click', () => {
    button.focus();
    if (isOpen()) close();
    else open();
  });
  list.addEventListener('click', (event) => {
    const option = event.target.closest('.ds-combo__option');
    if (!option) return;
    choose(all.findIndex((item) => item.value === option.dataset.value));
  });
  list.addEventListener('pointermove', (event) => {
    const option = event.target.closest('.ds-combo__option');
    if (!option) return;
    const index = all.findIndex((item) => item.value === option.dataset.value);
    if (index !== active) {
      active = index;
      markActive(false);
    }
  });

  drawFace();
  return {
    node,
    button,
    setValue,
    value: () => current,
    refresh: drawFace,
    isOpen,
    focus: () => button.focus(),
  };
}
