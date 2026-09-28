import { projectIdentityKey, projectIdentityLabel, type ProjectIdentityInput } from './project-identity';

/**
 * The chosen project, carrying the hashes it stood for at the moment it was chosen.
 *
 * The hashes belong to the selection instead of being re-derived from the project
 * registry because that registry is scoped by source/agent/account/mode: the moment the
 * chosen project falls out of that scope (or while the scoped query is in flight)
 * re-deriving yields [], an empty hash list is dropped from the request URL, and the
 * server reads that as "no project filter" -- the list would silently WIDEN to every
 * project while the picker still claimed one was selected.
 */
interface ProjectSelection {
  key: string;
  name: string;
  hashes: string[];
}

/**
 * The project_hash values the session list must be filtered to.
 *
 * The live registry wins while it still holds the project, so a worktree hash added
 * after the selection is picked up; the hashes carried on the selection are the fallback
 * for exactly the case that must never resolve to "no filter" -- the project is no
 * longer in the current scope. Returning [] for a live selection would widen the list to
 * everything, so an empty live match falls back rather than being taken at face value.
 */
const selectionProjectHashes = (
  selection: ProjectSelection | null,
  projects: ProjectIdentityInput[] | undefined,
): string[] => {
  if (!selection) return [];
  const live = (projects ?? [])
    .filter((p) => projectIdentityKey(p) === selection.key)
    .map((p) => p.project_hash ?? '');
  return live.length > 0 ? live : selection.hashes;
};

/**
 * Resolve a ?project_hash=... deep link against the registry it was matched in.
 *
 * The caller applies this once, so a miss is permanent -- which is why the registry
 * handed in here must be the unscoped one. Every hash sharing the matched project's
 * identity is carried along, so the selection survives a later scope change on its own.
 */
const deepLinkSelection = (
  projectHash: string,
  projects: ProjectIdentityInput[] | undefined,
): ProjectSelection | null => {
  const match = (projects ?? []).find((p) => p.project_hash === projectHash);
  if (!match) return null;
  const key = projectIdentityKey(match);
  return {
    key,
    name: projectIdentityLabel(match),
    hashes: (projects ?? [])
      .filter((p) => projectIdentityKey(p) === key)
      .map((p) => p.project_hash ?? ''),
  };
};

export { selectionProjectHashes, deepLinkSelection };
export type { ProjectSelection };
