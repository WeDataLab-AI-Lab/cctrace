import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import {
  organizationProjectView,
  OrganizationProjectList,
  OrganizationProjectRow,
  PROJECT_REGISTRY_ERROR_MESSAGE,
} from './organization-insights-tab';
import type { OrganizationInsightProject, Project } from '@/lib/types';

const projectRegistry: Project[] = [
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

const rootOrganizationProject: OrganizationInsightProject = {
  project_hash: 'h-main',
  project_hashes: ['h-main', 'h-worktree'],
  contributor_count: 2,
  session_count: 2,
  total_tokens: 300,
};

describe('organization project rendering', () => {
  it('renders the canonical root label without changing privacy aggregate counts', () => {
    const markup = renderToStaticMarkup(createElement(OrganizationProjectRow, {
      project: rootOrganizationProject,
      registry: projectRegistry,
    }));

    expect(markup).toContain('>repo<');
    expect(markup).not.toContain('>repo-worktree<');
    expect(markup).not.toContain('>h-main<');
    expect(markup).toContain('300 tokens');
    expect(markup).not.toContain('<a');
    expect(organizationProjectView(rootOrganizationProject, projectRegistry)).toEqual({
      key: 'example.com/acme/repo',
      label: 'repo',
      contributor_count: 2,
      session_count: 2,
      total_tokens: 300,
    });
  });

  it('labels a subpath member by its repository, not the subpath or the leaf', () => {
    const subpath: OrganizationInsightProject = {
      project_hash: 'h-subpath',
      project_hashes: ['h-subpath'],
      contributor_count: 2,
      session_count: 1,
      total_tokens: 50,
    };

    const markup = renderToStaticMarkup(createElement(OrganizationProjectRow, {
      project: subpath,
      registry: projectRegistry,
    }));

    expect(markup).toContain('>repo<');
    expect(markup).not.toContain('>store<');
    expect(organizationProjectView(subpath, projectRegistry).key).toBe('example.com/acme/repo');
  });

  it('uses Unknown Project and no navigation when the registry cannot identify a row', () => {
    const unknown: OrganizationInsightProject = {
      project_hash: 'h-worktree',
      project_hashes: ['h-main', 'h-worktree'],
      contributor_count: 2,
      session_count: 2,
      total_tokens: 300,
    };

    const markup = renderToStaticMarkup(createElement(OrganizationProjectRow, {
      project: unknown,
      registry: [projectRegistry[2]],
    }));

    expect(markup).toContain('Unknown Project');
    expect(markup).not.toContain('>h-worktree<');
    expect(markup).not.toContain('>repo-worktree<');
    expect(markup).not.toContain('<a');
    expect(organizationProjectView(unknown, [projectRegistry[2]]).key).toBe('aggregate:h-main,h-worktree');
  });

  it('surfaces a registry failure instead of presenting it as an unidentified project', () => {
    const markup = renderToStaticMarkup(createElement(OrganizationProjectList, {
      projects: [rootOrganizationProject],
      registry: [],
      loading: false,
      registryError: true,
    }));

    expect(markup).toContain(PROJECT_REGISTRY_ERROR_MESSAGE);
    expect(markup).not.toContain('Unknown Project');
    expect(markup).not.toContain('>h-main<');
  });
});
