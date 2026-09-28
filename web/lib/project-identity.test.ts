import { describe, expect, it } from 'vitest';

import { projectGroup } from '@/components/plugins/usage-aggregate';

import { projectIdentityKey, projectIdentityLabel } from './project-identity';

describe('projectIdentityKey', () => {
  it('merges a worktree into its main checkout: same repository_id, both empty repo_subpath', () => {
    const main = { repository_id: 'gh:org/repo', repo_subpath: '', project_name: 'repo' };
    const worktree = { repository_id: 'gh:org/repo', repo_subpath: '', project_name: 'repo-worktree' };
    expect(projectIdentityKey(main)).toBe(projectIdentityKey(worktree));
  });

  // A repository is the unit on every screen (#382, following #692): a session
  // started inside one of its folders must not split it into a second project.
  it('merges a subdirectory into its repository: same repository_id, different repo_subpath', () => {
    const root = { repository_id: 'gh:org/repo', repo_subpath: '' };
    const sub = { repository_id: 'gh:org/repo', repo_subpath: 'internal/store/' };
    expect(projectIdentityKey(root)).toBe(projectIdentityKey(sub));
  });

  it('falls back to project_name, then project_hash, when repository_id is absent', () => {
    expect(projectIdentityKey({ project_name: 'local-only' })).toBe('local-only');
    expect(projectIdentityKey({ project_hash: 'hash-1' })).toBe('hash-1');
    expect(projectIdentityKey({})).toBe('__unknown_project__');
  });
});

describe('projectIdentityKey cross-view contract', () => {
  // The same logical project must get the same identity whether it arrives as a
  // Project row (sessions page) or a PluginUsageSummary/SkillUsageSummary row
  // (usage-aggregate.ts's projectGroup) -- otherwise a project that is one row on
  // one page silently splits into several on another.
  it('matches between a Project-shaped row and a usage-summary-shaped row for the same repo + subpath', () => {
    const sessionsPageRow = { repository_id: 'gh:org/repo', repo_subpath: 'internal/store/' };
    const usagePageRow = {
      project_hash: 'h', project_name: 'store', repository_id: 'gh:org/repo', repository_name: 'repo',
      repo_subpath: 'internal/store/',
    };
    expect(projectGroup(usagePageRow).key).toBe(projectIdentityKey(sessionsPageRow));
  });

  it('merges subdirectories through projectGroup, not just projectIdentityKey directly', () => {
    const root = projectGroup({
      project_hash: 'h1', project_name: 'repo', repository_id: 'gh:org/repo', repository_name: 'repo', repo_subpath: '',
    });
    const sub = projectGroup({
      project_hash: 'h2', project_name: 'store', repository_id: 'gh:org/repo', repository_name: 'repo', repo_subpath: 'internal/store/',
    });
    expect(root.key).toBe(sub.key);
  });
});

describe('projectIdentityLabel', () => {
  it('labels by repository_name alone, not the worktree project_name or the subpath', () => {
    expect(
      projectIdentityLabel({ repository_name: 'repo', repo_subpath: '', project_name: 'repo-worktree' }),
    ).toBe('repo');
    expect(
      projectIdentityLabel({ repository_name: 'repo', repo_subpath: 'internal/store/', project_name: 'store' }),
    ).toBe('repo');
  });

  it('falls back through repository_id, project_name, project_hash when repository_name is absent', () => {
    expect(projectIdentityLabel({ repository_id: 'gh:org/repo' })).toBe('gh:org/repo');
    expect(projectIdentityLabel({ project_name: 'local-only' })).toBe('local-only');
    expect(projectIdentityLabel({ project_hash: 'hash-1' })).toBe('hash-1');
    expect(projectIdentityLabel({})).toBe('Unknown Project');
  });

  // An anonymous local repository has no repository_name and an id that is only a
  // hash, so the card read "local:18d19a223e39d079" and nobody could tell which
  // work it was. The leaf name is already in the same row -- 187 of the 192 such
  // projects in production carry one (#382).
  it('prefers the leaf name over an id that is only a hash', () => {
    expect(
      projectIdentityLabel({ repository_id: 'local:18d19a223e39d079', project_name: 'asset_survey' }),
    ).toBe('asset_survey');
  });

  // A readable id still wins: it names the repository, while project_name is the
  // worktree leaf. Labelling by the leaf there would split what the identity
  // merged, which is the contract #424 restored.
  it('keeps a readable identifier ahead of the worktree leaf', () => {
    expect(
      projectIdentityLabel({ repository_id: 'gh:org/repo', project_name: 'repo-worktree' }),
    ).toBe('gh:org/repo');
    expect(
      projectIdentityLabel({ repository_id: 'local:myproject:18d19a22', project_name: 'leaf' }),
    ).toBe('local:myproject:18d19a22');
  });

  // The five production rows that have neither still need something, and the hash
  // beats "Unknown Project" because it at least distinguishes them from each other.
  it('falls back to the opaque id when there is no name at all', () => {
    expect(projectIdentityLabel({ repository_id: 'local:18d19a223e39d079' })).toBe('local:18d19a223e39d079');
  });
});
