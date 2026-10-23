// The backup notice: one line under the top bar once the backup needs a
// person - no backup has succeeded for a day, backup.json cannot be read, or
// a database has stood in with an old copy for a day (backup.Status.Warning).
//
// The data directory on this machine is the only store; the bucket holds a
// backup of it and nothing the page does waits on it. A backup that keeps
// failing therefore changes nothing anybody would notice - until the machine
// is lost. So the page says it, in words, with a way to the Backup card, and
// says nothing at all while backups work or none are configured.
//
// It reads the "backup" object of GET /api/state, which a signed-in page is
// given for exactly this.

import { api, onWake } from './api.js';
import { html, raw, icon, morph } from './ui.js';

// How often the state is read. The warning is about a day, so a look every
// few minutes is plenty.
const EVERY = 5 * 60 * 1000;

/** mountBackupNotice fills `host` and keeps it current. */
export function mountBackupNotice(host) {
  if (!host) return;
  let timer = null;

  const draw = (backup) => {
    const warning = backup && backup.warning;
    host.hidden = !warning;
    if (!warning) return;
    morph(host, html`<div class="ds-alert ds-alert--warning">${raw(icon('alert', 20))}<span class="ds-alert__text so-banner__text">${warning}</span><a class="ds-btn ds-btn--text ds-btn--sm" href="/admin#backupCard">Backup</a></div>`);
  };

  const read = async () => {
    clearTimeout(timer);
    try {
      const state = await api('/api/state', { attempts: 1, timeout: 8000 });
      draw(state && state.backup);
    } catch {
      // No answer is the connection bar's business, not this one's.
    }
    timer = setTimeout(read, EVERY);
  };

  onWake(() => read());
  read();
}
