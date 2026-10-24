// The account picker of a chat (REBUILD.md §4.3): the account the chat runs
// on, beside its model in the composer's bar, and a menu of the agent's
// accounts with how much of each window is used, when a limit renews and
// which one has the most left. Picking another while a turn runs asks
// whether to stop the turn and go on with the new account now, or to let
// the turn finish first.
//
// It plugs into the composer's account slot (composer.js useAccountPicker)
// when the chat view loads it. The slot shows only for an agent with more
// than one account: with one there is nothing to pick.

import { el, toast, errorMessage, openDialog } from './api.js';
import * as accounts from './accounts.js';
import { useAccountPicker } from './composer.js';
import { DS, html, raw, icon, morph, showOverlay, hideOverlay } from './ui.js';

// The latest chat each slot was drawn for, so a press and a new list read
// what the composer last said rather than what it said when it was wired.
const drawnFor = new Map();
let menu = null;

// currentOf is the account a chat runs on: what its journal says, else what
// its row was started with.
function currentOf(session, chat) {
  return (chat && chat.account) || (session.options && session.options.account) || '';
}

// pendingOf is the account a switch asked for is on its way to, or null.
function pendingOf(session, chat) {
  const to = chat && chat.pendingSwitch ? chat.pendingSwitch.to : '';
  return to && to !== currentOf(session, chat) ? accounts.byId(to) : null;
}

function draw(slot, { session, chat, error }) {
  if (!session) return false;
  drawnFor.set(slot, { session, chat, error });
  for (const known of drawnFor.keys()) if (!known.isConnected && known !== slot) drawnFor.delete(known);
  if (accounts.forAgent(session.harness).length < 2) return false;
  const current = accounts.byId(currentOf(session, chat));
  const pending = pendingOf(session, chat);
  const name = current ? current.label : 'Account';
  const words = pending ? name + ' → ' + pending.label : name;
  const usage = current ? accounts.usageWords(current) : '';
  const renew = current ? accounts.renewal(current) : '';
  const tip = [pending ? pending.label + ' takes over after this turn' : '', usage, renew].filter(Boolean).join(' · ') || 'Account';
  morph(slot, html`<button class="ds-btn ds-btn--quiet ds-btn--sm so-composer__account-btn" type="button" data-action="account" aria-haspopup="menu" aria-expanded="false" aria-label="${'Account: ' + words}" data-tip="${tip}"${renew ? html` data-state="limited"` : ''}>${raw(icon(renew ? 'clock' : accounts.glyph(current), 16))}<span class="so-composer__account-text so-ellipsis">${words}</span>${raw(icon('chevronDown', 16))}</button>`);
  if (!slot.dataset.wired) {
    slot.dataset.wired = '1';
    slot.addEventListener('click', (event) => {
      const button = event.target.closest('[data-action="account"]');
      if (button) openMenu(slot, button);
    });
  }
  return true;
}

// A new list - a sign-in, a switch elsewhere, fresh usage - is drawn into
// every slot on screen at once.
accounts.onChange(() => {
  for (const [slot, ctx] of drawnFor) {
    if (!slot.isConnected) {
      drawnFor.delete(slot);
      continue;
    }
    slot.hidden = !draw(slot, ctx);
  }
});

// A limit renews on the clock, not on a new reading: a slot that says when
// is drawn again as the minutes go by, and is an ordinary one once the limit
// has renewed - with the network gone as much as with it.
const RENEWAL_TICK = 15000;
setInterval(() => {
  for (const [slot, ctx] of drawnFor) {
    if (!slot.isConnected) {
      drawnFor.delete(slot);
      continue;
    }
    if (slot.querySelector('[data-state="limited"]')) slot.hidden = !draw(slot, ctx);
  }
}, RENEWAL_TICK);

// metaOf is an account's quiet line in the menu: who it is, its usage, when
// its limit renews - in how long, and by the clock - and why it is the one
// to take or cannot be taken.
function metaOf(account, best) {
  const parts = [accounts.who(account)];
  if (!accounts.usable(account)) parts.push(account.state === 'error' ? 'sign-in failed' : 'not signed in');
  const usage = accounts.usageWords(account);
  if (usage) parts.push(usage);
  const renew = accounts.renewal(account);
  if (renew) parts.push(renew + ', ' + accounts.renewalAt(account));
  if (best) parts.push('most left');
  return parts.join(' · ');
}

function openMenu(slot, button) {
  const ctx = drawnFor.get(slot);
  if (!ctx) return;
  if (menu && !menu.hidden && button.getAttribute('aria-expanded') === 'true') {
    hideOverlay(menu);
    return;
  }
  if (menu) menu.remove();
  menu = document.createElement('div');
  menu.className = 'ds-menu so-account-menu';
  menu.setAttribute('role', 'menu');
  menu.setAttribute('aria-label', 'Account');
  menu.hidden = true;
  const fill = () => {
    const { session, chat } = drawnFor.get(slot) || ctx;
    const current = currentOf(session, chat);
    const pending = pendingOf(session, chat);
    const best = accounts.recommended(session.harness);
    morph(menu, html`${accounts.forAgent(session.harness).map((a) => html`<button class="ds-menu__item" role="menuitemradio" type="button" data-account="${a.id}" aria-checked="${a.id === current ? 'true' : 'false'}"${pending && pending.id === a.id ? html` data-state="pending"` : ''}>${raw(icon('check', 20, 'ds-menu__check'))}<span class="ds-menu__label"><span class="so-account-menu__text"><span class="so-account-menu__name">${a.label}</span><span class="so-account-menu__meta">${metaOf(a, a.id === best)}</span></span></span></button>`)}<div class="ds-menu__divider" role="separator"></div><button class="ds-menu__item" role="menuitem" type="button" data-action="manage">${raw(icon('settings', 20))}<span class="ds-menu__label">Manage accounts</span></button>`);
  };
  fill();
  menu.addEventListener('click', (event) => {
    if (event.target.closest('[data-action="manage"]')) {
      location.href = '/admin#accountsCard';
      return;
    }
    const item = event.target.closest('[data-account]');
    if (!item) return;
    const now = drawnFor.get(slot) || ctx;
    hideOverlay(menu);
    choose(now, item.dataset.account, button);
  });
  showOverlay(menu, button);
  // The usage moves: the menu opens on the copy this browser has and is
  // drawn again with the server's.
  accounts.load().then(() => { if (menu && !menu.hidden) fill(); }).catch(() => {});
}

// choose switches the chat to an account: at once when it is idle, and when
// a turn runs, now (the turn stops and the new account carries it on) or
// once the turn is done, as the person says. An account that is not signed
// in is signed in first. What does not happen is said in the chat's dock
// (`error`), where the composer is, never over it.
async function choose({ session, chat, error }, id, opener) {
  const say = error || ((text) => toast(text, 'error'));
  const target = accounts.byId(id);
  if (!target) return;
  const current = currentOf(session, chat);
  const pending = pendingOf(session, chat);
  if (id === current && !pending) return;
  if (!accounts.usable(target)) {
    if (target.kind === 'api_key') {
      say('The key of ' + target.label + ' was not taken. Replace it on the dashboard.');
      return;
    }
    if (!await accounts.signIn(target, opener)) return;
    if (!accounts.usable(accounts.byId(id))) return;
  }
  let when = 'now';
  let goOn = false;
  if (chat && chat.turn && id !== current) {
    when = await askWhen(accounts.byId(current), target, opener);
    if (!when) return;
    goOn = when === 'now';
  }
  try {
    await accounts.switchChat(session.id, id, { when, goOn });
    if (when === 'after_turn') toast(target.label + ' takes over after this turn.');
  } catch (err) {
    say('The chat could not switch to ' + target.label + ': ' + errorMessage(err));
  }
}

// askWhen is the question a switch during a turn asks: "now" or
// "after_turn", or null for neither.
async function askWhen(from, to, opener) {
  const cancel = el('button', { class: 'ds-btn ds-btn--text', type: 'button', 'data-close': true, text: 'Cancel' });
  const now = el('button', { class: 'ds-btn ds-btn--tonal', type: 'button', 'data-when': 'now', text: 'Switch now' });
  const later = el('button', { class: 'ds-btn ds-btn--primary', type: 'button', 'data-when': 'after_turn', text: 'After this turn' });
  const { dialog, shown } = openDialog({
    title: 'Switch to ' + to.label + '?',
    body: 'A turn is running' + (from ? ' on ' + from.label : '') + '. Switch now stops it, and ' + to.label
      + ' carries it on; after this turn lets it finish first.',
    actions: [cancel, now, later],
    opener,
  });
  for (const button of [now, later]) {
    button.addEventListener('click', () => {
      dialog.answer(button.dataset.when);
      DS.overlay.close(dialog);
    });
  }
  const answer = await shown;
  return answer || null;
}

useAccountPicker(draw);
