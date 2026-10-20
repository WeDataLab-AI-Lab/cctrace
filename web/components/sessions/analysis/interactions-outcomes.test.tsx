// @vitest-environment jsdom
import { createElement } from 'react';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { A_ID, M_ID } from '@/app/test/session-analysis/fixtures';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import type { SessionAnalysisFull } from '@/lib/session-analysis-types';
import { SessionAnalysisTab } from './analysis-tab';
import { flagshipFull, passA, mount } from './analysis-interaction-test-fixture';

describe('세션 성과: 세 줄을 펼치고 접는다', () => {
  it('fixture-p4-git-witness-v2: 접힌 요약은 active_total 2가 아니라 관측 경로 한 개를 보여준다', async () => {
    const base = flagshipFull();
    const analysis: SessionAnalysisFull = {
      ...base,
      outputs: {
        ...base.outputs,
        edited_files: {
          state: 'observed', active_total: 2, abandoned_total: 0, truncated: false,
          items: [{ path: 'fixture.txt', edits: 2, subagent_edits: 0, abandoned_edits: 0 }],
        },
      },
    };
    mount(analysis);
    const outcomes = screen.getByRole('list', { name: '세션 성과' });
    const files = within(outcomes).getByRole('button', { name: /변경한 파일 1건/ });
    expect(files.getAttribute('aria-expanded')).toBe('false');
    await userEvent.click(files);
    expect(screen.getByText('fixture.txt')).toBeTruthy();
    expect(screen.getByTitle('활성 경로의 편집 횟수').textContent).toBe('2회');
  });

  it('턴 목록은 같은 경로 두 번 편집을 파일 두 개가 아니라 편집 두 회로 적는다', () => {
    const base = flagshipFull();
    const [first, ...rest] = base.turns;
    const analysis: SessionAnalysisFull = {
      ...base,
      turns: [{ ...first, edited: { count: 2, sample: ['fixture.txt', 'fixture.txt'], abandoned: 0 } }, ...rest],
    };
    mount(analysis);
    expect(screen.getByTitle('이 턴에서 편집 2회 · 경로 표본 fixture.txt, fixture.txt')).toBeTruthy();
    expect(screen.queryByTitle(/편집한 파일 2개/)).toBeNull();
  });

  it('첫 줄만 펼쳐져 있고, 커밋과 변경한 파일은 눌러서 연다', async () => {
    mount(flagshipFull());
    const outcomes = screen.getByRole('list', { name: '세션 성과' });
    const tasks = within(outcomes).getByRole('button', { name: /작업·이슈 갱신/ });
    const commits = within(outcomes).getByRole('button', { name: /커밋 10건/ });
    const files = within(outcomes).getByRole('button', { name: /변경한 파일 16건/ });
    expect(tasks.getAttribute('aria-expanded')).toBe('true');
    expect(commits.getAttribute('aria-expanded')).toBe('false');
    expect(files.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('list', { name: '커밋 목록' })).toBeNull();

    await userEvent.click(commits);
    expect(commits.getAttribute('aria-expanded')).toBe('true');
    const list = screen.getByRole('list', { name: '커밋 목록' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(10);
    expect(within(list).getByText('feat(inbox): 종류별 필터 칩과 읽음 상태 모델을 추가')).toBeTruthy();
    expect(within(list).getByText('441e4b2')).toBeTruthy();
    // 누른 버튼이 사라지지 않아 키보드 포커스를 잃지 않는다.
    expect(document.activeElement).toBe(commits);

    await userEvent.click(files);
    expect(screen.getByText('web/app/inbox/filter-chips.tsx'.split('/').pop() ?? '')).toBeTruthy();
    expect(screen.getByText('+1,184')).toBeTruthy();
    expect(screen.getByText('−301')).toBeTruthy();
    expect(screen.getByRole('link', { name: /PR 231 acme\/pulse-web 새 창에서 열기/ }).getAttribute('rel')).toBe('noopener noreferrer');

    await userEvent.click(tasks);
    expect(tasks.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('list', { name: '갱신한 task·이슈' })).toBeNull();
  });

  it('해시를 모르는 커밋은 "해시 불명" 줄로 그린다', async () => {
    const base = flagshipFull();
    if (!base.commits) throw new Error('flagship has commits');
    const analysis: SessionAnalysisFull = {
      ...base,
      commits: { ...base.commits, items: [...base.commits.items, { hash: null, short: null, subject: '임시 저장(해시를 못 찾은 커밋)', ts: base.commits.items[0].ts, tasks: [] }] },
    };
    mount(analysis);
    await userEvent.click(screen.getByRole('button', { name: /커밋 10건/ }));
    const list = screen.getByRole('list', { name: '커밋 목록' });
    expect(within(list).getByText('해시 불명')).toBeTruthy();
    expect(within(list).getByText('임시 저장(해시를 못 찾은 커밋)')).toBeTruthy();
  });

  it('상한에서 잘린 편집 파일은 "더 보기" / "접기"가 한 버튼이 글자만 바꾼다 (패스 A의 M)', async () => {
    mount(passA(M_ID));
    await userEvent.click(screen.getByRole('button', { name: /확인한 파일 100건/ }));
    const more = screen.getByRole('button', { name: '92개 더 보기' });
    expect(more.getAttribute('aria-expanded')).toBe('false');
    await userEvent.click(more);
    const less = screen.getByRole('button', { name: '접기' });
    expect(less).toBe(more);
    expect(document.activeElement).toBe(less);
  });

  it('다른 세션으로 바뀌면 펼침 상태를 새로 시작한다', async () => {
    const view = render(createElement(SessionAnalysisTab, { analysis: flagshipFull(), now: DETAIL_NOW, onOpenRequest: vi.fn() }));
    await userEvent.click(screen.getByRole('button', { name: /커밋 10건/ }));
    expect(screen.getByRole('list', { name: '커밋 목록' })).toBeTruthy();
    view.rerender(createElement(SessionAnalysisTab, { analysis: passA(A_ID), now: DETAIL_NOW, onOpenRequest: vi.fn() }));
    expect(screen.queryByRole('list', { name: '커밋 목록' })).toBeNull();
    view.rerender(createElement(SessionAnalysisTab, { analysis: flagshipFull(), now: DETAIL_NOW, onOpenRequest: vi.fn() }));
    expect(screen.getByRole('button', { name: /커밋 10건/ }).getAttribute('aria-expanded')).toBe('false');
  });
});

