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
 */
export function combobox(options = {}) {
  const {
    value = '',
    onChange = () => {},
    items = () => [],
    placeholder = '',
  } = options;

  const id = 'combo' + (++comboCount);
  let current = value ?? '';
  let filtered = [];
  let active = -1;
  let isOpen = false;

  const input = el('input', {
    class: 'input mono combo-input',
    type: 'text',
    value: current,
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
    input.value = current;
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
      list.append(el('div', { class: 'combo-empty', text: 'No model matches' }));
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
    input.setAttribute('aria-expanded', 'true');
    render();
    if (active >= 0) setActive(active);
  }

  function close() {
    if (!isOpen) return;
    isOpen = false;
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

  input.addEventListener('focus', () => open(''));
  input.addEventListener('input', () => {
    // The typed text is the value straight away, so a model that is not in
    // the catalogue still saves.
    current = input.value;
    onChange(current);
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
        input.value = current;
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
