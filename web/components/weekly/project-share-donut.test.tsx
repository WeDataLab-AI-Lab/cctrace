import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { Project, WeeklyInsightProject } from '@/lib/types';
import { buildProjectSlices, ProjectShareDonut } from './project-share-donut';

const registryProject = (hash: string, name: string): Project => ({
  project_hash: hash,
  project_name: name,
  git_remote_url: `https://example.com/acme/${name}.git`,
  repository_id: `example.com/acme/${name}`,
  repository_name: name,
  repo_subpath: '',
  updated_at: '2026-09-01T00:00:00Z',
  last_session_at: null,
});

const project = (hash: string, sessions: number, tokens = 0): WeeklyInsightProject => ({
  project_hash: hash,
  project_hashes: [hash],
  project_name: hash,
  session_count: sessions,
  total_tokens: tokens,
});

const manyProjects = (n: number) =>
  Array.from({ length: n }, (_, i) => project(`h${i + 1}`, n - i));
const manyRegistry = (n: number) =>
  Array.from({ length: n }, (_, i) => registryProject(`h${i + 1}`, `p${i + 1}`));

const render = (props: Partial<Parameters<typeof ProjectShareDonut>[0]> = {}) =>
  renderToStaticMarkup(
    createElement(ProjectShareDonut, {
      projects: [],
      registry: [],
      loading: false,
      registryError: false,
      ...props,
    }),
  );

describe('buildProjectSlices', () => {
  // The value is sessions. Tokens are summed differently by Claude and Codex, so a
  // project heavy in tokens must not outrank one with more sessions (#671).
  it('orders by session count, not tokens', () => {
    const view = buildProjectSlices(
      [project('h1', 2, 9_000_000), project('h2', 5, 10)],
      [registryProject('h1', 'alpha'), registryProject('h2', 'beta')],
    );

    expect(view.slices.map((s) => s.label)).toEqual(['beta', 'alpha']);
    expect(view.total).toBe(7);
  });

  it('keeps the top seven and folds the rest into an eighth "기타" slice', () => {
    const view = buildProjectSlices(manyProjects(10), manyRegistry(10));

    expect(view.slices).toHaveLength(7);
    expect(view.others?.projects.map((p) => p.label)).toEqual(['p8', 'p9', 'p10']);
    expect(view.others?.sessionCount).toBe(3 + 2 + 1);
    expect(view.total).toBe(55);
  });

  // A "기타" holding one project hides a name to save no space.
  it('shows eight projects as eight slices without a "기타" group', () => {
    const view = buildProjectSlices(manyProjects(8), manyRegistry(8));

    expect(view.slices).toHaveLength(8);
    expect(view.others).toBeNull();
  });

  it('gives slices the categorical palette in order and "기타" the eighth colour', () => {
    const view = buildProjectSlices(manyProjects(9), manyRegistry(9));

    expect(view.slices.map((s) => s.color)).toEqual([
      'var(--cat-1)', 'var(--cat-2)', 'var(--cat-3)', 'var(--cat-4)',
      'var(--cat-5)', 'var(--cat-6)', 'var(--cat-7)',
    ]);
    expect(view.others?.color).toBe('var(--cat-8)');
  });

  it('takes shares over the listed sessions', () => {
    const view = buildProjectSlices(
      [project('h1', 3), project('h2', 1)],
      [registryProject('h1', 'alpha'), registryProject('h2', 'beta')],
    );

    expect(view.slices.map((s) => s.share)).toEqual(['75%', '25%']);
  });

  it('links each row to its sessions and keeps keys unique', () => {
    const view = buildProjectSlices(
      [project('h1', 3), project('h1', 1)],
      [registryProject('h1', 'alpha')],
    );

    expect(view.slices[0].href).toBe('/sessions?project_hash=h1');
    expect(new Set(view.slices.map((s) => s.key)).size).toBe(2);
  });
});

describe('ProjectShareDonut', () => {
  it('renders the denominator caption and never a token figure', () => {
    const html = render({
      projects: [project('h1', 30, 1_234_567), project('h2', 12, 99)],
      registry: [registryProject('h1', 'alpha'), registryProject('h2', 'beta')],
    });

    expect(html).toContain('세션 42개 기준');
    expect(html).toContain('alpha');
    expect(html).toContain('href="/sessions?project_hash=h1"');
    expect(html).not.toMatch(/토큰|token/i);
  });

  // No silent cut: the folded projects stay reachable behind a toggle.
  it('offers a collapsed "기타" toggle naming how many projects it holds', () => {
    const html = render({ projects: manyProjects(10), registry: manyRegistry(10) });

    expect(html).toContain('기타 3개');
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain('p9');
  });

  it('shows a skeleton while loading instead of an empty week', () => {
    const html = render({ loading: true, projects: [] });

    expect(html).not.toContain('세션 0개');
    expect(html).not.toContain('기록된 프로젝트가 없습니다');
  });

  it('shows one line when there are no projects', () => {
    expect(render()).toContain('이 기간에 기록된 프로젝트가 없습니다.');
  });

  it('says so when project identities could not be read', () => {
    expect(render({ registryError: true, projects: manyProjects(2) })).toContain('프로젝트 정보를 불러오지 못했습니다.');
  });

  it('uses no card surface or shadow', () => {
    const html = render({ projects: manyProjects(2), registry: manyRegistry(2) });

    expect(html).not.toMatch(/(?<![:\w-])bg-surface(?![-\w])/);
    expect(html).not.toContain('shadow');
  });
});
