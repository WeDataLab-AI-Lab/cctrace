import { describe, expect, it } from 'vitest';

import { projectIdentityKey } from '@/lib/project-identity';
import type { Project } from '@/lib/types';
import { projectBannerLabel, projectBannerProjectHashes, projectBannerRangeQuery } from './project-banner';

const projects: Project[] = [
  {
    project_hash: 'h-main',
    project_name: 'repo',
    git_remote_url: 'https://example.com/acme/repo.git',
    repository_id: 'example.com/acme/repo',
    repository_name: 'repo',
    repo_subpath: '',
    updated_at: '2026-08-26T00:00:00Z',
    last_session_at: null,
  },
  {
    project_hash: 'h-worktree',
    project_name: 'repo-worktree',
    git_remote_url: 'https://example.com/acme/repo.git',
    repository_id: 'example.com/acme/repo',
    repository_name: 'repo',
    repo_subpath: '',
    updated_at: '2026-08-26T00:00:00Z',
    last_session_at: null,
  },
  {
    project_hash: 'h-subpath',
    project_name: 'store',
    git_remote_url: 'https://example.com/acme/repo.git',
    repository_id: 'example.com/acme/repo',
    repository_name: 'repo',
    repo_subpath: 'internal/store/',
    updated_at: '2026-08-26T00:00:00Z',
    last_session_at: null,
  },
];

describe('ProjectBanner project identity', () => {
  it('labels the root identity canonically instead of showing a worktree leaf', () => {
    expect(projectBannerLabel(projects[1]!)).toBe('repo');
  });

  it('passes every member of the repository, subpath sessions included', () => {
    const rootKey = projectIdentityKey(projects[0]!);
    const subpathKey = projectIdentityKey(projects[2]!);

    expect(subpathKey).toBe(rootKey);
    expect(projectBannerProjectHashes(projects, rootKey)).toEqual(['h-main', 'h-worktree', 'h-subpath']);
  });

  it('keeps a selected trend range in the same Sessions scope and cache identity', () => {
    const scope = projectBannerRangeQuery({
      selectedProject: projectIdentityKey(projects[0]!),
      selectedAccount: 'account@example.test',
      projectHashes: ['h-main', 'h-worktree'],
      since: '2026-08-27T00:00:00Z',
      until: '2026-08-28T00:00:00Z',
      assembled: true,
      source: 'interactive',
      agent: 'claude',
    });

    expect(scope.request).toEqual({
      loginEmail: 'account@example.test',
      projectHashes: ['h-main', 'h-worktree'],
      since: '2026-08-27T00:00:00Z',
      until: '2026-08-28T00:00:00Z',
      limit: 1000,
      assembled: true,
      source: 'interactive',
      agent: 'claude',
    });
    expect(scope.queryKey).not.toEqual(projectBannerRangeQuery({
      ...scope.request,
      selectedProject: projectIdentityKey(projects[0]!),
      selectedAccount: 'account@example.test',
      projectHashes: ['h-main'],
    }).queryKey);
  });
});
