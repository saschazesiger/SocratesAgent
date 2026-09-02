// A searchable dropdown for picking a model.
//
// The model catalogue has several hundred entries, so a plain <select> is
// unusable and a datalist gives no control over what is shown. This is a
// proper combobox: type to filter, arrow keys to move, enter to take the
// highlighted entry, escape to go back to what was there before. It follows
// the ARIA combobox pattern so it works with a keyboard and a screen reader.

import { el } from './api.js';

let openInstance = null;

// closeOpen closes whichever combobox is currently showing its list.
function closeOpen(except) {
  if (openInstance && openInstance !== except) openInstance.close();
}

document.addEventListener('click', (event) => {
  if (openInstance && !openInstance.root.contains(event.target)) closeOpen(null);
});

let comboCount = 0;

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
 * options.emptyText    what an empty list says.
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
  let active = -1;
  let isOpen = false;

  const input = el('input', {
    class: 'input mono combo-input',
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

  const list = el('div', { class: 'combo-list', id: id + '-list', role: 'listbox', hidden: true });
  const toggle = el('button', {
    class: 'combo-toggle', type: 'button', tabindex: '-1',
    'aria-label': 'Show the list',
  }, el('span', { class: 'combo-caret' }));

  const root = el('div', { class: 'combo' }, input, toggle, list);

  const instance = { root, close };

  function setValue(next, notify = true) {
    current = next ?? '';
    input.value = shown(current);
    if (notify) onChange(current);
  }

  // matches ranks entries: an id that starts with the query beats one that
  // merely contains it, so typing "claude" puts the Claude models on top.
  function matches(query) {
    const all = items() || [];
    const q = query.trim().toLowerCase();
    if (!q) return all.slice(0, 200);
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
    return scored.slice(0, 200).map((entry) => entry.item);
  }

  function render() {
    list.innerHTML = '';
    if (!filtered.length) {
      list.append(el('div', { class: 'combo-empty', text: emptyText }));
      return;
    }
    let group = null;
    filtered.forEach((item, index) => {
      if (item.group && item.group !== group) {
        group = item.group;
        list.append(el('div', { class: 'combo-group', text: group }));
      }
      const option = el('div', {
        class: 'combo-option' + (index === active ? ' active' : '') + (item.value === current ? ' chosen' : ''),
        role: 'option',
        // The value is on the element so anything outside can find the entry
        // it means without matching on the words.
        'data-value': item.value,
        id: id + '-opt-' + index,
        'aria-selected': index === active ? 'true' : 'false',
        // mousedown, not click: the input must not lose focus first.
        onmousedown: (event) => {
          event.preventDefault();
          choose(index);
        },
        onmouseenter: () => { setActive(index, false); },
      },
        el('span', { class: 'combo-label', text: item.label || item.value }),
        item.hint ? el('span', { class: 'combo-hint', text: item.hint }) : null,
      );
      list.append(option);
    });
  }

  function setActive(index, scroll = true) {
    active = index;
    const nodes = list.querySelectorAll('.combo-option');
    nodes.forEach((node, i) => {
      node.classList.toggle('active', i === index);
      node.setAttribute('aria-selected', i === index ? 'true' : 'false');
    });
    if (index >= 0 && index < nodes.length) {
      input.setAttribute('aria-activedescendant', id + '-opt-' + index);
      if (scroll) nodes[index].scrollIntoView({ block: 'nearest' });
    } else {
      input.removeAttribute('aria-activedescendant');
    }
  }

  function open(query) {
    closeOpen(instance);
    openInstance = instance;
    filtered = matches(query ?? '');
    // Start on the entry that is already chosen, so enter is a no-op.
    active = filtered.findIndex((item) => item.value === current);
    isOpen = true;
    list.hidden = false;
    // The list is as tall as the screen below the field allows, and scrolls
    // inside itself beyond that. On a phone with the keyboard up a fixed
    // height ran past the bottom of the sheet, and the options down there
    // could only be reached by scrolling the sheet under the list.
    const room = window.innerHeight - input.getBoundingClientRect().bottom - 16;
    list.style.maxHeight = Math.max(140, Math.min(300, room)) + 'px';
    input.setAttribute('aria-expanded', 'true');
    render();
    if (active >= 0) setActive(active);
  }

  function close() {
    if (!isOpen) return;
    isOpen = false;
    // A strict field shows the entry it is on, never the half-typed filter
    // that was used to find one.
    if (strict) input.value = shown(current);
    list.hidden = true;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    if (openInstance === instance) openInstance = null;
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
        if (!isOpen) return open(input.value);
        setActive(Math.min(active + 1, filtered.length - 1));
        break;
      case 'ArrowUp':
        event.preventDefault();
        if (!isOpen) return open(input.value);
        setActive(Math.max(active - 1, 0));
        break;
      case 'Home':
        if (!isOpen) return;
        event.preventDefault();
        setActive(0);
        break;
      case 'End':
        if (!isOpen) return;
        event.preventDefault();
        setActive(filtered.length - 1);
        break;
      case 'Enter':
        if (!isOpen) return;
        event.preventDefault();
        if (active >= 0) choose(active);
        else close();
        break;
      case 'Escape':
        if (!isOpen) return;
        event.preventDefault();
        event.stopPropagation();
        input.value = shown(current);
        close();
        break;
      case 'Tab':
        close();
        break;
    }
  });

  input.addEventListener('blur', () => {
    // A click on an option runs on mousedown, so closing here is safe.
    setTimeout(() => { if (openInstance === instance) close(); }, 0);
  });

  toggle.addEventListener('click', () => {
    if (isOpen) {
      close();
      return;
    }
    input.focus();
    open('');
  });

  return { node: root, setValue };
}
