interface ProjectIdentityInput {
  project_hash?: string;
  project_name?: string;
  repository_id?: string;
  repository_name?: string;
  repo_subpath?: string;
}

// Stable identity for "the same project" across machines/users and the
// leading-dash project_hash inconsistency between the claude and codex syncers.
// Keys on repository_id alone: the repository is the unit on every screen, the
// same key the weekly and organization aggregates use (#692, #382). Worktrees and
// sessions started inside one of the repository's folders land in one project.
// repo_subpath used to sit in the key (#257), but it answers "how far is cwd from
// the repo root", not "is this an independent package" -- a session started in
// `staging/` or a scratch directory like `.omo/evidence/` split the repository.
// Checkouts with no git remote fall back to project_name, then project_hash --
// repository_id there is a hash of cwd (see gitctx.anonymousLocalID), so those stay
// scattered by design rather than leak local paths into a shared identity.
const projectIdentityKey = (row: ProjectIdentityInput): string => {
  const repositoryID = row.repository_id?.trim() ?? '';
  const projectName = row.project_name?.trim() ?? '';
  const projectHash = row.project_hash?.trim() ?? '';
  if (repositoryID) return repositoryID;
  return projectName || projectHash || '__unknown_project__';
};

// Human-readable label for a project identity. Shared so every view labels the
// same identity identically. Labels by repository_name -- the worktree/workspace
// leaf name (project_name) and the subpath are auxiliary, not the label; callers
// that want the leaf (e.g. as a badge) read row.project_name separately.
const projectIdentityLabel = (row: ProjectIdentityInput): string => {
  const repositoryName = row.repository_name?.trim();
  if (repositoryName) return repositoryName;
  // An anonymous local repository gets no repository_name (gitctx returns empty),
  // and its repository_id is nothing but a hash -- so the card read
  // "local:18d19a223e39d079" and nobody could tell which work it was. The leaf
  // name is already in the same row: 187 of the 192 such projects in production
  // carry one (#382).
  //
  // Only an opaque id steps aside. A readable one like "gh:org/repo" still wins,
  // because it names the repository and project_name is the worktree leaf --
  // labelling by the leaf there would split what the identity merged (#424).
  // A local identity is kept separate per project_hash by design, so using its
  // leaf name merges and splits nothing.
  const repositoryID = row.repository_id?.trim();
  const identifier = repositoryID && !isOpaqueLocalID(repositoryID) ? repositoryID : '';
  return (
    identifier ||
    row.project_name?.trim() ||
    repositoryID ||
    row.project_hash?.trim() ||
    'Unknown Project'
  );
};

// `local:<hex>` is the shape gitctx produces for a repository with no remote and
// no readable name. `local:<name>:<hex>` carries one and is left alone.
const OPAQUE_LOCAL_ID = /^local:[0-9a-f]+$/;
const isOpaqueLocalID = (id: string): boolean => OPAQUE_LOCAL_ID.test(id);

export { projectIdentityKey, projectIdentityLabel };
export type { ProjectIdentityInput };
