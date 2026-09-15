// Which project a session belongs to, and the folders the sidebar draws.
//
// The list used to be read the way a phone's call list is read - today's
// sessions, yesterday's, and history under them. That is the right shape for
// six sessions and the wrong one for sixty: after a week of work the thing
// being looked for is not "the one from Tuesday", it is "the one in the
// browser project", and a list sorted by day scatters a project's sessions
// across five headers where none of them can be seen together.
//
// So the sidebar is grouped by the directory a session works in. Every
// session that Socrates gave a fresh directory of its own - a "dynamic"
// working directory, which is a generated name under the workspace root and
// not a project anybody would recognise - goes into one folder at the bottom
// instead, because a folder per throwaway directory is the scattering again
// with worse names.
//
// The grouping is done here, away from the drawing, for the reason the day
// buckets were: the only hard part is naming the folders, and a name is worth
// being able to test on its own.

/**
 * MORE_KEY is the folder every session with a directory of Socrates' own
 * making lands in, and MORE_LABEL is what it reads.
 *
 * The key cannot collide with a directory because it is not a path: no
 * absolute path starts with a `#`.
 */
export const MORE_KEY = '#more';
export const MORE_LABEL = 'More Chats';

/**
 * momentOf is the last time a session's status changed, as the list
 * understands it - which is the moment the list orders by.
 *
 * `active_at` is written by the server when the session did something: it
 * started working, finished, began needing an answer, or its program came up
 * or went down. It is deliberately not `updated_at`, which every attach,
 * rename and resize bumps: a row that jumped to the top of the list for
 * being opened and read was the complaint that made this column exist.
 * `updated_at` is the fallback for a row from a server that did not know the
 * column yet, and `created_at` for a row that has never been written to.
 */
export function momentOf(session) {
  if (!session) return 0;
  return Number(session.active_at) || Number(session.updated_at) || Number(session.created_at) || 0;
}

/**
 * movesRow says whether a committed change of activity from `prev` to `next`
 * is a status change in the sense the list orders by: the agent started
 * working, finished, or began needing an answer.
 *
 * It is the same rule the server applies when it writes `active_at`, and it
 * is applied here too so a row moves the moment the frame arrives rather
 * than at the next fetch of the list. A change into idle from anything but
 * busy is not on the list on purpose: it is what every running session
 * reports when the detector comes up after a restart.
 */
export function movesRow(prev, next) {
  if (!next || prev === next) return false;
  return next === 'busy' || prev === 'busy' || next === 'waiting';
}

/**
 * dirOf is a working directory with the spelling differences taken out of it,
 * so that `/root/browser/` and `/root/browser` are one folder and not two.
 *
 * The root directory is the one path that is nothing but separators, and it
 * keeps its slash: an empty string is how "this session has no directory" is
 * said a line below, and `/` is a real place.
 */
export function dirOf(path) {
  const dir = String(path || '').trim().replace(/\/+$/, '');
  if (dir) return dir;
  return String(path || '').trim() ? '/' : '';
}

/**
 * folderKeyOf says which folder a session belongs in.
 *
 * A dynamic directory is one Socrates made up for a session that was not
 * given a place to work, so it names no project and goes to the bottom with
 * the others. A row from a server too old to have said how its directory was
 * chosen is judged by whether it has one at all, which is the honest reading:
 * the field has been written since the first release, and a row without one
 * has nothing to be grouped by either way.
 */
export function folderKeyOf(session) {
  if (!session) return MORE_KEY;
  if (String(session.workdir_mode || '') === 'dynamic') return MORE_KEY;
  return dirOf(session.workdir) || MORE_KEY;
}

// The parts of a path that are not separators.
function segmentsOf(path) {
  return String(path || '').split('/').filter(Boolean);
}

/**
 * nameFolders writes the label of every project folder: the directory's own
 * name, and as much of its parentage as it takes to tell it from the others.
 *
 * The name rather than the path because the sidebar is read, not typed into,
 * and `/srv/data/projects/browser` is four words of machine in front of the
 * one word that identifies it - the same rule that keeps the working
 * directory of a session behind the "i". The whole path is on the folder's
 * tooltip, which is where the technical spelling of anything on this page
 * lives.
 *
 * Two projects can of course be called the same thing in different places,
 * and then the name alone is a lie rather than a shorthand, so the clashing
 * folders take a parent each until they differ - and a folder with no more
 * parents to take falls back to its whole path, which is unique by
 * construction.
 */
function nameFolders(folders) {
  const dirs = folders.filter((folder) => folder.key !== MORE_KEY);
  for (const folder of dirs) folder.label = segmentsOf(folder.path).slice(-1)[0] || folder.path || '/';
  for (let depth = 2; depth <= 12; depth += 1) {
    const byLabel = new Map();
    for (const folder of dirs) {
      const same = byLabel.get(folder.label);
      if (same) same.push(folder); else byLabel.set(folder.label, [folder]);
    }
    const clashing = [...byLabel.values()].filter((same) => same.length > 1).flat();
    if (!clashing.length) break;
    let widened = false;
    for (const folder of clashing) {
      const segments = segmentsOf(folder.path);
      const wider = segments.length >= depth ? segments.slice(-depth).join('/') : folder.path || '/';
      if (wider !== folder.label) { folder.label = wider; widened = true; }
    }
    if (!widened) break;
  }
  for (const folder of folders) if (folder.key === MORE_KEY) folder.label = MORE_LABEL;
}

/**
 * foldersOf is the sidebar, worked out: one folder per project directory and
 * one for everything Socrates housed itself, each holding its sessions newest
 * first.
 *
 * The folders are ordered by the session that moved most recently in each, so
 * the project being worked on is at the top and stays there while it is being
 * worked on - the same rule the rows themselves have always had, one level
 * up. "More Chats" is last whatever is in it: it is where a session goes for
 * having no project, so it cannot be allowed to sit above the projects.
 *
 * Ties keep the order the server gave, because Array.sort is stable and the
 * server lists the sessions newest first.
 */
export function foldersOf(sessions) {
  const byKey = new Map();
  for (const session of sessions || []) {
    if (!session) continue;
    const key = folderKeyOf(session);
    const folder = byKey.get(key);
    if (folder) folder.sessions.push(session);
    else byKey.set(key, { key, path: key === MORE_KEY ? '' : key, label: '', at: 0, sessions: [session] });
  }
  const folders = [...byKey.values()];
  for (const folder of folders) {
    folder.sessions.sort((a, b) => momentOf(b) - momentOf(a));
    folder.at = folder.sessions.length ? momentOf(folder.sessions[0]) : 0;
  }
  nameFolders(folders);
  folders.sort((a, b) => {
    if ((a.key === MORE_KEY) !== (b.key === MORE_KEY)) return a.key === MORE_KEY ? 1 : -1;
    return b.at - a.at;
  });
  return folders;
}
