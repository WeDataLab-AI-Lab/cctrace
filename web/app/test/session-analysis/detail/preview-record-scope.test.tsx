// @vitest-environment jsdom
import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { main, recordState, selectSession, showList, advance } from './preview-record-test-fixture';
import { DetailPreview } from './preview';
import { NO_RECORD_ID, RECORD_FAILED_ID } from './variants';

describe('합성 세션 기록의 범위와 이동', () => {
  it('두 세션의 재생성을 엇갈려 시작해도 각각의 700ms 타이머가 완료된다', () => {
    const { unmount } = render(<DetailPreview />);
    showList();
    selectSession(RECORD_FAILED_ID);
    fireEvent.click(main().getByRole('button', { name: '다시 만들기' }));
    advance(350);
    selectSession(NO_RECORD_ID);
    fireEvent.click(main().getByRole('button', { name: '다시 만들기' }));
    advance(349);
    expect(recordState()).toBe('generating');
    advance(1);
    expect(recordState()).toBe('generating');
    selectSession(RECORD_FAILED_ID);
    expect(main().getByText(/환율 캐시 만료 처리 · 합성 기록 생성 시연/)).toBeTruthy();
    selectSession(NO_RECORD_ID);
    advance(350);
    expect(recordState()).toBeUndefined();
    expect(main().getByText(/CLI 도움말 문구 맞춤법 정리 · 합성 기록 생성 시연/)).toBeTruthy();
    unmount();
  });

  it('전체 개요 재생성은 대상 세션 기록을 실제 갱신하고 쓴 건수를 표시한다', () => {
    const { unmount } = render(<DetailPreview />);
    const summary = within(screen.getByTestId('summary-state'));
    fireEvent.click(summary.getByRole('button', { name: '개요 다시 만들기' }));
    expect(summary.getByRole('button', { name: '개요를 쓰는 중…' }).hasAttribute('disabled')).toBe(true);
    showList();
    selectSession(RECORD_FAILED_ID);
    expect(recordState()).toBe('generating');
    selectSession(NO_RECORD_ID);
    expect(recordState()).toBe('generating');
    advance(700);
    expect(main().getByText(/CLI 도움말 문구 맞춤법 정리 · 합성 기록 생성 시연/)).toBeTruthy();
    selectSession(RECORD_FAILED_ID);
    expect(main().getByText(/환율 캐시 만료 처리 · 합성 기록 생성 시연/)).toBeTruthy();
    fireEvent.change(screen.getByRole('searchbox', { name: '전체 검색' }), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
    expect(within(screen.getByTestId('summary-state')).getByRole('button', { name: '개요는 최신' }).hasAttribute('disabled')).toBe(true);
    expect(within(screen.getByTestId('summary-state')).getByRole('status').textContent).toMatch(/^마지막 범위 요청에서 [1-9][0-9]*건을 썼습니다$/);
    unmount();
  });

  // 전체 웹 병렬 실행에서 5,080ms가 걸린 렌더라 기본 5초 대신 해당 회귀에만 상한을 둔다.
  it('주 A의 전체 개요를 만든 뒤 주 B에서는 다시 생성할 수 있고 작성 건수가 보이지 않는다', () => {
    const { unmount } = render(<DetailPreview />);
    const summary = () => within(screen.getByTestId('summary-state'));
    fireEvent.click(summary().getByRole('button', { name: '개요 다시 만들기' }));
    advance(700);
    expect(summary().getByRole('button', { name: '개요는 최신' })).toBeTruthy();
    expect(summary().getByRole('status').textContent).toMatch(/^마지막 범위 요청에서 [1-9][0-9]*건을 썼습니다$/);

    fireEvent.click(screen.getByRole('button', { name: '다음 주' }));
    expect(summary().getByRole('button', { name: '개요 다시 만들기' }).hasAttribute('disabled')).toBe(false);
    expect(summary().getByRole('status').textContent).toBe('');
    fireEvent.click(screen.getByRole('button', { name: '이전 주' }));
    expect(summary().getByRole('button', { name: '개요는 최신' })).toBeTruthy();
    unmount();
  }, 15_000);

  it('다른 분류에서는 이전 개요의 최신 상태와 작성 건수가 보이지 않는다', () => {
    const { unmount } = render(<DetailPreview />);
    const summary = () => within(screen.getByTestId('summary-state'));
    fireEvent.click(summary().getByRole('button', { name: '개요 다시 만들기' }));
    advance(700);
    expect(summary().getByRole('status').textContent).toMatch(/^마지막 범위 요청에서 [1-9][0-9]*건을 썼습니다$/);

    showList();
    fireEvent.click(within(screen.getByRole('tablist', { name: '사용자 분류' })).getByRole('tab', { name: /상담 중/ }));
    fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
    expect(summary().getByRole('button', { name: '개요 다시 만들기' }).hasAttribute('disabled')).toBe(false);
    expect(summary().getByRole('status').textContent).toBe('');

    showList();
    fireEvent.click(within(screen.getByRole('tablist', { name: '사용자 분류' })).getByRole('tab', { name: /전체/ }));
    fireEvent.click(screen.getByRole('button', { name: '캘린더' }));
    expect(summary().getByRole('button', { name: '개요는 최신' })).toBeTruthy();
    expect(summary().getByRole('status').textContent).toMatch(/^마지막 범위 요청에서 [1-9][0-9]*건을 썼습니다$/);
    unmount();
  });

  it('관리자 역할에서는 주인 개요의 최신 상태와 작성 건수가 보이지 않는다', () => {
    const { unmount } = render(<DetailPreview />);
    const summary = () => within(screen.getByTestId('summary-state'));
    fireEvent.click(summary().getByRole('button', { name: '개요 다시 만들기' }));
    advance(700);
    expect(summary().getByRole('status').textContent).toMatch(/^마지막 범위 요청에서 [1-9][0-9]*건을 썼습니다$/);

    fireEvent.click(screen.getByRole('button', { name: '주인이 아닌 관리자' }));
    expect(summary().getByRole('button', { name: '개요 다시 만들기' }).hasAttribute('disabled')).toBe(true);
    expect(summary().getByRole('status').textContent).toBe('');
    unmount();
  });

  it('DST 반복 시각의 입력을 고르면 해당 턴의 대화로 이동한다', () => {
    const { unmount } = render(<DetailPreview />);
    showList();
    selectSession('dst-fall-over-the-repeat');
    expect(main().getByRole('heading', { name: '턴 상세 4건' })).toBeTruthy();
    const turns = within(main().getByRole('list', { name: '활성 경로의 턴' }));
    fireEvent.click(turns.getByRole('button', { name: /2\..*반복된 시를 가로지르는 세션/ }));
    const timeline = screen.getByRole('region', { name: '타임라인 메시지' });
    expect(timeline.querySelectorAll('[data-input-n]')).toHaveLength(4);
    expect(timeline.querySelector<HTMLElement>('[data-input-n="2"]')?.dataset.focused).toBe('true');
    unmount();
  });

});
