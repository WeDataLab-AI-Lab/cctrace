// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { calSession } from '@/app/test/session-analysis/calendar/calendar-builders';
import type { CalendarColorKey } from '@/lib/session-calendar-types';
import { ColorKeyBar } from './color-key-bar';
import { assignColors } from './colors';

afterEach(cleanup);

const SESSIONS = [
  calSession('a', { colors: { status: 'done', screens: ['대시보드'], components: ['웹'], kinds: ['구현'], profile: '메인' } }),
  calSession('b', { colors: { status: 'done', screens: ['대시보드'], components: ['웹'], kinds: ['구현'], profile: '메인' } }),
  calSession('c', { colors: { status: 'leftover', screens: ['설정'], components: [], kinds: ['조사'], profile: '서브' } }),
  calSession('d', { colors: { status: 'midway', screens: [], components: ['API'], kinds: [], profile: null } }),
];

const renderBar = (over: { colorKey?: CalendarColorKey; ownerView?: boolean; highlightKey?: string | null } = {}) => {
  const colorKey = over.colorKey ?? 'screen';
  const ownerView = over.ownerView ?? true;
  const onColorKeyChange = vi.fn();
  const onHighlight = vi.fn();
  const assignment = assignColors(SESSIONS, ownerView ? colorKey : colorKey === 'status' ? 'project' : colorKey);
  const utils = render(
    <ColorKeyBar
      colorKey={colorKey}
      onColorKeyChange={onColorKeyChange}
      ownerView={ownerView}
      assignment={assignment}
      highlightKey={over.highlightKey ?? null}
      onHighlight={onHighlight}
    />,
  );
  return { ...utils, onColorKeyChange, onHighlight, assignment };
};

const legendEntries = (): { name: string; count: string }[] =>
  within(screen.getByRole('list', { name: '범례' }))
    .getAllByRole('button')
    .map((b) => ({ name: b.querySelector('span:nth-of-type(1)')?.textContent ?? '', count: b.querySelector('span:nth-of-type(2)')?.textContent ?? '' }));

describe('색 기준 (C4)', () => {
  it('레퍼런스의 다섯이 앞에 오고 프로젝트, 에이전트, 모델이 뒤따른다', () => {
    renderBar();
    const group = screen.getByRole('group', { name: '색상 기준' });
    expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual([
      '완료 여부',
      '코드 영역',
      '대상 화면',
      '작업 유형',
      '프로필',
      '프로젝트',
      '에이전트',
      '모델',
    ]);
  });

  it('고른 기준만 aria-pressed이고, 누르면 부모에게 알린다', async () => {
    const { onColorKeyChange } = renderBar({ colorKey: 'screen' });
    expect(screen.getByRole('button', { name: '대상 화면' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: '작업 유형' }).getAttribute('aria-pressed')).toBe('false');
    await userEvent.setup().click(screen.getByRole('button', { name: '작업 유형' }));
    expect(onColorKeyChange).toHaveBeenCalledWith('kind');
  });

  it('세션 주인이 아니면 "완료 여부"를 고를 수 없고, 쓸 수 없는 이유를 밝힌다', async () => {
    const { onColorKeyChange } = renderBar({ ownerView: false, colorKey: 'status' });
    const status = screen.getByRole('button', { name: '완료 여부' }) as HTMLButtonElement;
    expect(status.disabled).toBe(true);
    expect(status.title).toContain('API가 돌려주지 않아');
    await userEvent.setup().click(status);
    expect(onColorKeyChange).not.toHaveBeenCalled();
    // 고른 기준을 쓸 수 없으면 프로젝트로 보인다는 안내.
    expect(screen.getByText('고른 기준을 쓸 수 없어 프로젝트로 보입니다.')).toBeTruthy();
    expect(screen.getByRole('button', { name: '프로젝트' }).getAttribute('aria-pressed')).toBe('true');
  });
});

describe('범례 (C5)', () => {
  it('값마다 그 주의 세션 수를 개수 순으로 보이고, 값이 없는 세션은 "(없음)" 칸이다', () => {
    renderBar({ colorKey: 'screen' });
    // 같은 개수면 이름 있는 값이 먼저이고 "(없음)"이 뒤다.
    expect(legendEntries()).toEqual([
      { name: '대시보드', count: '2' },
      { name: '설정', count: '1' },
      { name: '(없음)', count: '1' },
    ]);
  });

  it('범례의 합은 그 주의 세션 수와 같다 (기준을 바꿔도)', () => {
    const { rerender, onColorKeyChange, onHighlight } = renderBar({ colorKey: 'kind' });
    const sum = () => legendEntries().reduce((n, e) => n + Number(e.count), 0);
    expect(sum()).toBe(SESSIONS.length);
    for (const key of ['status', 'component', 'screen', 'profile'] as const) {
      rerender(
        <ColorKeyBar
          colorKey={key}
          onColorKeyChange={onColorKeyChange}
          ownerView
          assignment={assignColors(SESSIONS, key)}
          highlightKey={null}
          onHighlight={onHighlight}
        />,
      );
      expect(sum()).toBe(SESSIONS.length);
    }
  });

  it('완료 여부 범례는 고정된 이름으로 개수 순이다', () => {
    renderBar({ colorKey: 'status' });
    expect(legendEntries()).toEqual([
      { name: '완료', count: '2' },
      { name: '도중 종료', count: '1' },
      { name: '하다 만 것', count: '1' },
    ]);
  });

  it('범례 칸에 마우스를 올리거나 포커스를 두면 그 값을 강조하고, 벗어나면 해제한다', async () => {
    const { onHighlight, assignment } = renderBar({ colorKey: 'screen' });
    const first = assignment.legend[0];
    const entry = screen.getByRole('button', { name: `${first.label} ${first.sessions}세션` });
    fireEvent.pointerEnter(entry);
    expect(onHighlight).toHaveBeenLastCalledWith(first.key);
    fireEvent.pointerLeave(entry);
    expect(onHighlight).toHaveBeenLastCalledWith(null);
    fireEvent.focus(entry);
    expect(onHighlight).toHaveBeenLastCalledWith(first.key);
    fireEvent.blur(entry);
    expect(onHighlight).toHaveBeenLastCalledWith(null);
  });

  it('강조 중인 칸은 표시가 붙고, 눌림(토글) 상태로 말하지 않는다', () => {
    const { assignment } = renderBar({ colorKey: 'screen', highlightKey: null });
    cleanup();
    const first = assignment.legend[0];
    renderBar({ colorKey: 'screen', highlightKey: first.key });
    const entry = screen.getByRole('button', { name: `${first.label} ${first.sessions}세션` });
    expect(entry.hasAttribute('data-highlighted')).toBe(true);
    expect(entry.hasAttribute('aria-pressed')).toBe(false);
  });
});
