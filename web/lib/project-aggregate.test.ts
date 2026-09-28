import { describe, expect, it } from 'vitest';

import { deepLinkSelection } from './project-selection';
import {
  aggregateProjectHashes,
  projectAggregateHref,
  projectAggregateKey,
  projectAggregateLabel,
  type ProjectAggregateRow,
} from './project-aggregate';
import type { OrganizationInsightProject, Project, WeeklyInsightProject } from './types';

const makeProject = (overrides: Partial<Project>): Project => ({
  project_hash: 'h-default',
  project_name: 'default',
  git_remote_url: 'https://example.com/acme/repo.git',
  repository_id: 'example.com/acme/repo',
  repository_name: 'repo',
  repo_subpath: '',
  updated_at: '2026-08-26T00:00:00Z',
  last_session_at: null,
  ...overrides,
});

const registry: Project[] = [
  makeProject({ project_hash: 'h-worktree', project_name: 'repo-worktree' }),
  makeProject({ project_hash: 'h-main', project_name: 'repo' }),
  makeProject({
    project_hash: 'h-subpath',
    project_name: 'store',
    repo_subpath: 'internal/store/',
  }),
];

describe('project aggregate web contract', () => {
  it('preserves explicit member hashes in weekly and organization response types', () => {
    const weekly: WeeklyInsightProject = {
      project_hash: 'h-worktree',
      project_hashes: ['h-main', 'h-worktree'],
      project_name: 'repo-worktree',
      session_count: 2,
      total_tokens: 300,
    };
    const organization: OrganizationInsightProject = {
      project_hash: 'h-main',
      project_hashes: ['h-main', 'h-worktree'],
      contributor_count: 2,
      session_count: 2,
      total_tokens: 300,
    };

    expect(weekly.project_hashes).toEqual(['h-main', 'h-worktree']);
    expect(organization.project_hashes).toEqual(['h-main', 'h-worktree']);
  });

  it('resolves root and subpath rows to the same repository key and label', () => {
    const root: ProjectAggregateRow = {
      project_hash: 'h-worktree',
      project_hashes: ['h-main', 'h-worktree'],
    };
    const subpath: ProjectAggregateRow = {
      project_hash: 'h-subpath',
      project_hashes: ['h-subpath'],
    };

    expect(aggregateProjectHashes(root)).toEqual(['h-main', 'h-worktree']);
    expect(projectAggregateKey(root, registry)).toBe('example.com/acme/repo');
    expect(projectAggregateLabel(root, registry)).toBe('repo');
    expect(projectAggregateLabel(root, registry)).not.toContain('repo-worktree');
    expect(projectAggregateLabel(root, registry)).not.toContain('h-worktree');

    expect(projectAggregateKey(subpath, registry)).toBe('example.com/acme/repo');
    expect(projectAggregateLabel(subpath, registry)).toBe('repo');
  });

  it('uses the representative hash for deep-link resolution that carries every repository member', () => {
    const root: ProjectAggregateRow = {
      project_hash: 'h-worktree',
      project_hashes: ['h-main', 'h-worktree'],
    };

    const href = projectAggregateHref(root);
    expect(href).toBe('/sessions?project_hash=h-worktree');

    const linkedHash = new URL(href ?? '', 'http://localhost').searchParams.get('project_hash');
    expect(linkedHash).toBe('h-worktree');
    const selectedHashes = deepLinkSelection(linkedHash ?? '', registry)?.hashes ?? [];
    expect(selectedHashes).toHaveLength(3);
    expect(selectedHashes).toEqual(expect.arrayContaining(['h-main', 'h-worktree', 'h-subpath']));
  });

  it('uses the first member when an aggregate has no representative hash', () => {
    const withoutRepresentative: ProjectAggregateRow = {
      project_hashes: ['h-main', 'h-worktree'],
    };

    expect(aggregateProjectHashes(withoutRepresentative)).toEqual(['h-main', 'h-worktree']);
    expect(projectAggregateHref(withoutRepresentative)).toBe('/sessions?project_hash=h-main');
    expect(projectAggregateKey(withoutRepresentative, registry)).toBe('example.com/acme/repo');
    expect(projectAggregateLabel(withoutRepresentative, registry)).toBe('repo');
  });

  it('falls back to a non-empty representative and never emits an unfiltered link', () => {
    const legacy: ProjectAggregateRow = { project_hash: ' h-main ', project_hashes: [] };
    const empty: ProjectAggregateRow = { project_hash: '', project_hashes: [] };

    expect(aggregateProjectHashes(legacy)).toEqual(['h-main']);
    expect(projectAggregateHref(legacy)).toBe('/sessions?project_hash=h-main');
    expect(aggregateProjectHashes(empty)).toEqual([]);
    expect(projectAggregateHref(empty)).toBeNull();
    expect(projectAggregateLabel(empty, registry)).toBe('Unknown Project');
  });

  it('does not expose a hash when the identity registry has no matching member', () => {
    const root: ProjectAggregateRow = {
      project_hash: 'h-worktree',
      project_hashes: ['h-main', 'h-worktree'],
    };

    expect(projectAggregateLabel(root, [])).toBe('Unknown Project');
  });
});
