// @vitest-environment jsdom
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { A_ID } from '@/app/test/session-analysis/fixtures';
import { flagshipFull, passA, mount } from './analysis-interaction-test-fixture';

describe('머리', () => {
  it('머리에 독립 타임라인 토글이 없다', () => {
    mount(flagshipFull());
    expect(screen.queryByRole('button', { name: '타임라인 보기' })).toBeNull();
  });

  it('"세부"를 펼치면 패스 A의 큰 지표(토큰 구성, 비용 나눔, 컨텍스트 구성, 도구, 실시간)가 보인다', async () => {
    mount(flagshipFull());
    expect(screen.queryByRole('group', { name: '세션 세부' })).toBeNull();
    const toggle = screen.getByRole('button', { name: /세부/ });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    await userEvent.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    const details = screen.getByRole('group', { name: '세션 세부' });
    const t = details.textContent ?? '';
    expect(t).toContain('총 토큰');
    expect(t).toContain('캐시 읽기');
    expect(t).toContain('턴 귀속');
    expect(t).toContain('붙지 않은 호출');
    expect(t).toContain('컨텍스트');
    expect(t).toContain('도구 호출');
    expect(t).toContain('실시간');
    expect(t).toContain('수집기 신호');
    expect(t).toContain('pulse-web');
    expect(t).toMatch(/요청\s*14\s*·\s*입력\s*15\s*·\s*턴\s*14/);
  });

  it('판정 칩은 키보드로 닿고 근거를 이름으로 갖는다', () => {
    mount(flagshipFull());
    const chip = screen.getByRole('button', { name: '백그라운드 작업: 판단 불가 — 백그라운드 작업 판단 불가' });
    chip.focus();
    expect(document.activeElement).toBe(chip);
  });
});


describe('구형 기록의 턴 경계 판단 불가', () => {
  it('pre_lineage에는 요청·입력·턴을 0으로 만들지 않고 턴 목록도 만들지 않는다', async () => {
    const analysis = passA(A_ID);
    mount({
      ...analysis,
      header: {
        ...analysis.header,
        counts: {
          ...analysis.header.counts,
          requests: null,
          inputs: null,
          turns: null,
          abandoned_turns: null,
          turn_boundaries: { state: 'undeterminable', reason: 'pre_lineage' },
        },
      },
    });
    const panel = screen.getByRole('region', { name: '턴 상세' });
    expect(within(panel).getByText('턴 목록을 만들지 않았습니다')).toBeTruthy();
    expect(panel.textContent).toContain('구형 기록에 계보 키가 없는 메시지');
    expect(within(panel).queryByRole('list', { name: '활성 경로의 턴' })).toBeNull();
    expect(within(panel).queryByText('기록된 턴이 없습니다.')).toBeNull();

    await userEvent.click(screen.getByRole('button', { name: /세부/ }));
    const counts = screen.getByRole('group', { name: '요청, 입력, 턴 수' });
    expect(within(counts).getAllByText('알 수 없음')).toHaveLength(3);
    expect(counts.textContent).not.toMatch(/요청\s*0|입력\s*0|턴\s*0/);
  });
});


describe('세션 상태의 관측 신호', () => {
  it('"관측 신호 5"를 누르면 다섯 문장이 보인다', async () => {
    mount(flagshipFull());
    expect(screen.queryByRole('list', { name: '관측 신호' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: /관측 신호 5/ }));
    const list = screen.getByRole('list', { name: '관측 신호' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(5);
    expect(list.textContent).toContain('마지막 턴 끝남');
    expect(list.textContent).toContain('백그라운드 작업 판단 불가');
  });
});

