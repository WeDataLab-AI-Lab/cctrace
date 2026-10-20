// @vitest-environment jsdom
import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { main, recordState, selectSession, showList, advance } from './preview-record-test-fixture';
import { DetailPreview } from './preview';
import { NO_RECORD_ID, RECORD_FAILED_ID, RECORD_STALE_ID } from './variants';

describe('합성 세션 기록 다시 만들기', () => {
  it('실패·없음·오래됨 모두 만드는 중을 거쳐 700ms에 최신 기록으로 바뀐다', () => {
    for (const [id, state] of [
      [RECORD_FAILED_ID, 'failed'],
      [NO_RECORD_ID, 'none'],
      [RECORD_STALE_ID, 'stale'],
    ] as const) {
      const { unmount } = render(<DetailPreview />);
      showList();
      selectSession(id);
      if (state === 'stale') {
        expect(main().getByText(/이 기록은 .*대화만 반영합니다/)).toBeTruthy();
      } else {
        expect(recordState()).toBe(state);
      }
      fireEvent.click(main().getByRole('button', { name: '다시 만들기' }));
      expect(recordState()).toBe('generating');
      expect(main().queryByRole('button', { name: '다시 만들기' })).toBeNull();
      advance(699);
      expect(recordState()).toBe('generating');
      advance(1);
      expect(recordState()).toBeUndefined();
      expect(main().getByRole('heading', { name: '세션 개요' })).toBeTruthy();
      expect(main().getByText(/합성 기록 생성 시연|월말 마감 리포트를 CSV로 내보내는 기능을 넣었다/)).toBeTruthy();
      unmount();
    }
  }, 15_000);

  it('세션 기록 버튼은 캘린더 전체 개요 생성과 독립이고 관리자에게는 보이지 않는다', () => {
    const { unmount } = render(<DetailPreview />);
    const summary = within(screen.getByTestId('summary-state'));
    expect(summary.getByRole('button', { name: '개요 다시 만들기' }).hasAttribute('disabled')).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: '목록' }));
    selectSession(RECORD_FAILED_ID);
    fireEvent.click(main().getByRole('button', { name: '다시 만들기' }));
    expect(summary.getByRole('button', { name: '개요 다시 만들기' }).hasAttribute('disabled')).toBe(false);
    advance(700);
    expect(summary.getByRole('button', { name: '개요 다시 만들기' }).hasAttribute('disabled')).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: '주인이 아닌 관리자' }));
    expect(main().queryByRole('button', { name: '다시 만들기' })).toBeNull();
    unmount();
  });

  it('생성 도중 다른 세션으로 이동해도 시작한 세션의 기록만 갱신한다', () => {
    const { unmount } = render(<DetailPreview />);
    showList();
    selectSession(RECORD_FAILED_ID);
    fireEvent.click(main().getByRole('button', { name: '다시 만들기' }));
    selectSession(NO_RECORD_ID);
    expect(recordState()).toBe('none');
    advance(700);
    expect(recordState()).toBe('none');
    selectSession(RECORD_FAILED_ID);
    expect(recordState()).toBeUndefined();
    expect(main().getByText(/환율 캐시 만료 처리 · 합성 기록 생성 시연/)).toBeTruthy();
    unmount();
  });

  it('화면을 닫을 때 대기 중인 기록 생성 타이머를 정리한다', () => {
    const { unmount } = render(<DetailPreview />);
    showList();
    selectSession(RECORD_FAILED_ID);
    fireEvent.click(main().getByRole('button', { name: '다시 만들기' }));
    expect(recordState()).toBe('generating');
    const pending = vi.getTimerCount();
    expect(pending).toBeGreaterThan(0);
    unmount();
    expect(vi.getTimerCount()).toBeLessThan(pending);
  });
});
