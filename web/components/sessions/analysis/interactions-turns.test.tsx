// @vitest-environment jsdom
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { A_ID } from '@/app/test/session-analysis/fixtures';
import type { AnalysisTurn } from '@/lib/session-analysis-types';
import { flagshipFull, passA, mount } from './analysis-interaction-test-fixture';

describe('턴 줄과 작업 항목 줄은 선택한 턴을 부모에 전달한다', () => {
  it('턴 줄을 누르면 onOpenRequest(turn)이 그 턴으로 불린다', async () => {
    const onOpenRequest = vi.fn<(turn: AnalysisTurn) => void>();
    mount(flagshipFull(), { onOpenRequest });
    await userEvent.click(screen.getByRole('button', { name: /필터와 목록 보기 전환은 툴바 맨 왼쪽에 두세요/ }));
    expect(onOpenRequest).toHaveBeenCalledTimes(1);
    expect(onOpenRequest.mock.calls[0][0]).toMatchObject({ key: 't02', request_no: 2 });
  });

  it('작업 항목 줄을 누르면 그 줄을 연 턴으로 불린다', async () => {
    const onOpenRequest = vi.fn<(turn: AnalysisTurn) => void>();
    mount(flagshipFull(), { onOpenRequest });
    const list = screen.getByRole('list', { name: '작업 항목' });
    await userEvent.click(within(list).getByRole('button', { name: /안 읽음 점을 숫자 배지로 바꿔 달라/ }));
    expect(onOpenRequest.mock.calls[0][0]).toMatchObject({ key: 't05' });
    // 여러 줄이 한 턴을 가리켜도 같은 턴이다.
    await userEvent.click(within(list).getByRole('button', { name: /알림 센터의 필터 위치와 읽음 상태를 어디에 저장할지/ }));
    expect(onOpenRequest.mock.calls[1][0]).toMatchObject({ key: 't01' });
  });

  it('턴을 모르는 작업 항목 줄은 눌리지 않는다', () => {
    mount(flagshipFull());
    const list = screen.getByRole('list', { name: '작업 항목' });
    expect(within(list).queryByRole('button', { name: /그 밖에는 일단 괜찮다/ })).toBeNull();
    expect(within(list).getByText(/그 밖에는 일단 괜찮다/)).toBeTruthy();
  });

  it('턴 줄은 키보드로 닿고 Enter로 누른다', async () => {
    const onOpenRequest = vi.fn<(turn: AnalysisTurn) => void>();
    mount(flagshipFull(), { onOpenRequest });
    const row = screen.getByRole('button', { name: /접근성: 칩은 스크린리더가 읽을 수 있게 해주고/ });
    row.focus();
    expect(document.activeElement).toBe(row);
    await userEvent.keyboard('{Enter}');
    expect(onOpenRequest).toHaveBeenCalledWith(expect.objectContaining({ key: 't13' }));
  });

  it('요청이 아닌 턴과 버려진 분기의 턴도 눌린다 (패스 A의 A)', async () => {
    const onOpenRequest = vi.fn<(turn: AnalysisTurn) => void>();
    mount(passA(A_ID), { onOpenRequest });
    await userEvent.click(screen.getByRole('button', { name: /백그라운드 작업 완료: 통합 테스트 스위트/ }));
    await userEvent.click(screen.getByRole('button', { name: /이벤트 소싱으로 바꿔서 구현부터 시작해줘/ }));
    expect(onOpenRequest.mock.calls.map(([t]) => t.key)).toEqual(['ta08', 'ta05']);
    expect(onOpenRequest.mock.calls[1][0].branch).toBe('abandoned');
  });

  it('버려진 분기는 접고 펼 수 있다', async () => {
    mount(passA(A_ID));
    const toggle = screen.getByRole('button', { name: /버려진 분기 3턴/ });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    await userEvent.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('button', { name: /이벤트 소싱으로 바꿔서 구현부터 시작해줘/ })).toBeNull();
  });

});
