// One place to say "the set of projects changed". A ProjectSelector can be
// mounted in a conversation or a routine row while the Projects workspace is
// open, so a project created, edited or archived there has to reach those
// selectors without remounting them and without any of them polling.
//
// Only writes publish. Nothing here fetches, so a selector reloads once per
// real change instead of on a timer.

type Listener = () => void;

const listeners = new Set<Listener>();
let revision = 0;

export function projectsRevision(): number {
  return revision;
}

export function subscribeProjects(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// Notify the current listeners after a project write. New subscriptions made
// during notification receive the next change.
export function notifyProjectsChanged(): void {
  revision += 1;
  for (const listener of [...listeners]) listener();
}

// Test-only reset so one test's listeners cannot leak into the next.
export function resetProjectEvents(): void {
  listeners.clear();
  revision = 0;
}
