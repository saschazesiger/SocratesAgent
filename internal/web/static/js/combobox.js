// A searchable list for picking a model or a working directory.
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
  list.addEventListener('pointerdown', (event) => event.preventDefault());
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
 * dropdown builds a closed choice that is not typed into: a field that shows
 * what is chosen, and a list under it whose entries can be as rich as they
 * need to be. It is for a short list whose entries say more than a name - a
 * profile and how much of its account is left - where a row of buttons runs
 * out of width after three and a search box has nothing to search.
 *
 * options.value        current value
 * options.onChange     called with the new value when one is picked
 * options.items        () => [{value, ...}]
 * options.renderItem   (item, chosen) => Node, an entry in the open list
 * options.renderValue  (item) => Node, what the closed field shows; the
 *                      entry itself when left out
 * options.label        the field's accessible name
 *
 * It goes once the profile is chosen from radio cards; until then its list
 * opens in place under the field rather than being pinned over the page.
 */
export function dropdown(options = {}) {
  const {
    value = '',
    onChange = () => {},
    items = () => [],
    renderItem = (item) => el('span', { class: 'combo-label', text: item.label || item.value }),
    renderValue = null,
    label = '',
  } = options;

  const id = 'combo' + (++comboCount);
  let current = value ?? '';
  let all = [];
  let active = -1;
  let isOpen = false;

  const face = el('span', { class: 'drop-face' });
  const button = el('button', {
    class: 'ds-input drop-button', type: 'button',
    'aria-haspopup': 'listbox', 'aria-expanded': 'false', 'aria-controls': id + '-list',
    'aria-label': label || null,
  }, face, el('span', { class: 'combo-caret' }));
  const list = el('div', { class: 'combo-list drop-list', id: id + '-list', role: 'listbox', hidden: true });
  const root = el('div', { class: 'combo drop' }, button, list);
  function chosenItem() { return (items() || []).find((item) => item.value === current) || null; }

  function drawFace() {
    face.replaceChildren();
    const item = chosenItem();
    if (item) face.append(renderValue ? renderValue(item) : renderItem(item, true));
  }

  function setValue(next, notify = true) {
    current = next ?? '';
    drawFace();
    if (notify) onChange(current);
  }

  function render() {
    list.replaceChildren();
    all.forEach((item, index) => {
      const chosen = item.value === current;
      list.append(el('div', {
        class: 'combo-option drop-option' + (index === active ? ' active' : '') + (chosen ? ' chosen' : ''),
        role: 'option',
        'data-value': item.value,
        id: id + '-opt-' + index,
        'aria-selected': chosen ? 'true' : 'false',
        // mousedown, not click: the button must not lose focus first.
        onmousedown: (event) => {
          event.preventDefault();
          choose(index);
        },
        onmouseenter: () => { setActive(index, false); },
      }, renderItem(item, chosen)));
    });
  }

  function setActive(index, scroll = true) {
    active = index;
    const nodes = list.querySelectorAll('.combo-option');
    nodes.forEach((node, i) => node.classList.toggle('active', i === index));
    if (index >= 0 && index < nodes.length) {
      button.setAttribute('aria-activedescendant', id + '-opt-' + index);
      if (scroll) nodes[index].scrollIntoView({ block: 'nearest' });
    } else {
      button.removeAttribute('aria-activedescendant');
    }
  }

  function open() {
    all = items() || [];
    active = all.findIndex((item) => item.value === current);
    isOpen = true;
    list.hidden = false;
    button.setAttribute('aria-expanded', 'true');
    render();
    if (active >= 0) setActive(active);
  }

  function close() {
    if (!isOpen) return;
    isOpen = false;
    list.hidden = true;
    button.setAttribute('aria-expanded', 'false');
    button.removeAttribute('aria-activedescendant');
  }

  function choose(index) {
    const item = all[index];
    if (!item) return;
    close();
    setValue(item.value);
  }

  button.addEventListener('click', () => {
    if (isOpen) close();
    else open();
  });

  button.addEventListener('keydown', (event) => {
    switch (event.key) {
      case 'ArrowDown':
      case 'ArrowUp': {
        event.preventDefault();
        if (!isOpen) return open();
        const step = event.key === 'ArrowDown' ? 1 : -1;
        setActive(Math.max(0, Math.min(all.length - 1, active + step)));
        break;
      }
      case 'Home':
      case 'End':
        if (!isOpen) return;
        event.preventDefault();
        setActive(event.key === 'Home' ? 0 : all.length - 1);
        break;
      case 'Enter':
      case ' ':
        if (!isOpen) return;
        event.preventDefault();
        if (active >= 0) choose(active);
        else close();
        break;
      case 'Escape':
        if (!isOpen) return;
        // The sheet around it must not take the escape as a no.
        event.preventDefault();
        event.stopPropagation();
        close();
        break;
      case 'Tab':
        close();
        break;
    }
  });

  button.addEventListener('blur', () => {
    setTimeout(close, 0);
  });

  drawFace();
  return { node: root, setValue, refresh: drawFace };
}
