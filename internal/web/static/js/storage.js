// The storage banner: one line under the top bar when the bucket is not
// taking what this page does.
//
// The bucket is the store of record. Everything still works while it cannot
// be reached - the database on this machine is a full working copy and the
// changes queue up - but a person who closes a laptop believing a prompt is
// safe somewhere else deserves to know that it is not yet. And when another
// Socrates has taken the bucket over, this one is no longer the one that
// counts: what is done here stays here. Both are said in words, with how long
// and how much, and nothing at all is said while the bucket is fine.
//
// It reads the short "storage" object of GET /api/state, which a signed-in
// page is given for exactly this - no request of its own for the common case.
// The full status, and the buttons to test and sync, are on the admin page's
// Storage card, which the banner links to.

import { api, el, onWake } from './api.js';

// How often the state is read while everything is fine, and while it is not.
// A problem is checked more often so the banner leaves soon after it is over.
const CALM_EVERY = 30000;
const TROUBLE_EVERY = 6000;

const two = (n) => String(n).padStart(2, '0');

// since is a moment the way the banner says it: the time when it was today,
// the date and the time when it was not.
function since(at) {
  if (!at) return '';
  const d = new Date(at);
  const clock = two(d.getHours()) + ':' + two(d.getMinutes());
  if (d.toDateString() === new Date().toDateString()) return clock;
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) + ' ' + clock;
}

function changes(n) {
  if (!n) return 'Nothing is waiting to be backed up.';
  return n === 1 ? '1 change is waiting to be backed up.' : n + ' changes are waiting to be backed up.';
}

/**
 * bannerText is what the banner says for a storage summary, or null for none.
 * Exported for the page's own use and to keep the wording in one place.
 */
export function bannerText(storage) {
  if (!storage || !storage.configured) return null;
  if (storage.fenced || storage.state === 'fenced') {
    const host = storage.lease && storage.lease.host ? ' (' + storage.lease.host + ')' : '';
    return {
      kind: 'fenced',
      text: 'Another Socrates' + host + ' took over this bucket. '
        + 'Changes made here are no longer backed up.',
    };
  }
  if (storage.state === 'offline') {
    const when = since(storage.error_since);
    return {
      kind: 'offline',
      text: 'Storage unreachable' + (when ? ' since ' + when : '') + '. ' + changes(storage.pending),
      title: storage.last_error || '',
    };
  }
  return null;
}

/** mountStorageBanner fills `host` and keeps it current. */
export function mountStorageBanner(host) {
  if (!host) return;
  const text = el('span', { class: 'storage-banner-text' });
  const link = el('a', { class: 'storage-banner-link', href: '/admin#storageCard', text: 'Details' });
  host.replaceChildren(el('span', { class: 'storage-banner-dot', 'aria-hidden': 'true' }), text, link);

  let timer = null;
  let trouble = false;

  const draw = (storage) => {
    const said = bannerText(storage);
    trouble = !!said;
    host.hidden = !said;
    if (!said) return;
    host.dataset.kind = said.kind;
    if (text.textContent !== said.text) text.textContent = said.text;
    host.title = said.title || '';
  };

  const read = async () => {
    clearTimeout(timer);
    try {
      const state = await api('/api/state', { attempts: 1, timeout: 8000 });
      draw(state && state.storage);
    } catch {
      // No answer is the connection bar's business, not this one's: what the
      // banner says about the bucket is left as it was.
    }
    timer = setTimeout(tick, trouble ? TROUBLE_EVERY : CALM_EVERY);
  };
  const tick = () => {
    if (document.visibilityState === 'visible') read();
    else timer = setTimeout(tick, trouble ? TROUBLE_EVERY : CALM_EVERY);
  };

  onWake(() => read());
  read();
}
