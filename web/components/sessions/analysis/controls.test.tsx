// @vitest-environment jsdom
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { PlanFilter, SessionCategoryKey, SessionSort } from '@/lib/session-analysis-types';
import { CategoryCountChips, PlanFilterToggle, SortControl } from './list-controls';
import { SessionViewerTabs, viewerPanelId, viewerTabId, type ViewerTab } from './viewer-tabs';

/**
 * 패스 A의 목록 제어(범주 칩, 정렬, 플랜 필터)와 뷰어 탭 띠. 패스 C가 상세와 카드를 다시 만들었지만 이 부품들은 그대로 쓰이므로
 * (부모 미리보기가 아직 쓴다) 동작을 그대로 지킨다.
 */

beforeAll(() => {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

afterEach(cleanup);

const noop = () => undefined;
const textOf = (html: string): string => html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ');
const pressed = (el: HTMLElement): string | null => el.getAttribute('aria-pressed');

describe('목록 제어: 정적 마크업', () => {
  it('범주 칩: 전체와 개수가 있는 범주만, 비소유 범위는 안내', () => {
    const html = renderToStaticMarkup(createElement(CategoryCountChips, { counts: { waiting: 1, running: 2, recent: 3 }, value: null, onChange: noop }));
    const text = textOf(html);
    expect(text).toContain('전체 6');
    expect(text).toContain('응답 대기 1');
    expect(text).toContain('진행 중 2');
    expect(text).toContain('최근 활동 3');
    expect(text).not.toContain('종료됨');
    expect(html).toContain('aria-pressed="true"');
    const locked = textOf(renderToStaticMarkup(createElement(CategoryCountChips, { counts: {}, value: null, onChange: noop, unavailable: true })));
    expect(locked).toContain('범주 개수는 세션 주인에게만 제공됩니다');
  });

  it('고른 범주는 개수가 0이어도 남겨 해제할 수 있다', () => {
    const text = textOf(renderToStaticMarkup(createElement(CategoryCountChips, { counts: { running: 1 }, value: 'idle', onChange: noop })));
    expect(text).toContain('쉬는 중 0');
  });

  it('정렬: 네 가지, 고른 것만 aria-pressed', () => {
    const html = renderToStaticMarkup(createElement(SortControl, { value: 'cost', onChange: noop }));
    expect(textOf(html)).toContain('시작 활동 비용 요청 수');
    expect(html.match(/aria-pressed="true"/g)).toHaveLength(1);
  });

  it('플랜 필터: 파일과 모드', () => {
    const html = renderToStaticMarkup(createElement(PlanFilterToggle, { value: 'mode', onChange: noop }));
    expect(textOf(html)).toContain('플랜 파일 플랜 모드');
    expect(html.match(/aria-pressed="true"/g)).toHaveLength(1);
  });
});

describe('목록 제어는 제어형이다', () => {
  it('정렬: 누른 값을 알리고 스스로 바꾸지 않는다', async () => {
    const onChange = vi.fn<(s: SessionSort) => void>();
    render(createElement(SortControl, { value: 'start', onChange }));
    await userEvent.click(screen.getByRole('button', { name: '비용' }));
    expect(onChange).toHaveBeenCalledWith('cost');
    expect(pressed(screen.getByRole('button', { name: '시작' }))).toBe('true');
    expect(pressed(screen.getByRole('button', { name: '비용' }))).toBe('false');
  });

  it('플랜 필터: 같은 버튼을 다시 누르면 null로 해제된다', async () => {
    const onChange = vi.fn<(p: PlanFilter | null) => void>();
    const { rerender } = render(createElement(PlanFilterToggle, { value: null, onChange }));
    await userEvent.click(screen.getByRole('button', { name: '플랜 모드' }));
    expect(onChange).toHaveBeenLastCalledWith('mode');
    rerender(createElement(PlanFilterToggle, { value: 'mode', onChange }));
    await userEvent.click(screen.getByRole('button', { name: '플랜 모드' }));
    expect(onChange).toHaveBeenLastCalledWith(null);
    await userEvent.click(screen.getByRole('button', { name: '플랜 파일' }));
    expect(onChange).toHaveBeenLastCalledWith('file');
  });

  it('범주 칩: 칩을 누르면 그 범주, 다시 누르면 전체, "전체"는 null', async () => {
    const onChange = vi.fn<(c: SessionCategoryKey | null) => void>();
    const props = { counts: { waiting: 1, idle: 2 }, onChange };
    const { rerender } = render(createElement(CategoryCountChips, { ...props, value: null }));
    await userEvent.click(screen.getByRole('button', { name: /응답 대기/ }));
    expect(onChange).toHaveBeenLastCalledWith('waiting');
    rerender(createElement(CategoryCountChips, { ...props, value: 'waiting' }));
    expect(pressed(screen.getByRole('button', { name: /응답 대기/ }))).toBe('true');
    await userEvent.click(screen.getByRole('button', { name: /응답 대기/ }));
    expect(onChange).toHaveBeenLastCalledWith(null);
    await userEvent.click(screen.getByRole('button', { name: /^전체/ }));
    expect(onChange).toHaveBeenLastCalledWith(null);
  });
});

describe('탭 띠: 분석 / 대화', () => {
  const Harness = ({ initial = 'analysis' as ViewerTab }) => {
    const [tab, setTab] = useState<ViewerTab>(initial);
    return createElement(
      'div',
      null,
      createElement(SessionViewerTabs, { value: tab, onChange: setTab }),
      createElement('div', { role: 'tabpanel', id: viewerPanelId(tab), 'aria-labelledby': viewerTabId(tab) }, tab),
    );
  };

  it('두 탭이 있고 선택된 탭만 Tab으로 닿는다(roving tabindex)', () => {
    render(createElement(Harness));
    const [analysis, conversation] = screen.getAllByRole('tab');
    expect(analysis.textContent).toBe('분석');
    expect(conversation.textContent).toBe('대화');
    expect(analysis.getAttribute('aria-selected')).toBe('true');
    expect(analysis.getAttribute('tabindex')).toBe('0');
    expect(conversation.getAttribute('tabindex')).toBe('-1');
  });

  it('탭과 패널이 서로를 가리킨다', () => {
    render(createElement(Harness));
    const tab = screen.getByRole('tab', { name: '분석' });
    expect(tab.getAttribute('aria-controls')).toBe(viewerPanelId('analysis'));
    expect(screen.getByRole('tabpanel').getAttribute('aria-labelledby')).toBe(tab.id);
  });

  it('클릭, 좌우 화살표(끝에서 돌아옴), Home, End', async () => {
    render(createElement(Harness));
    const analysis = screen.getByRole('tab', { name: '분석' });
    const conversation = screen.getByRole('tab', { name: '대화' });
    await userEvent.click(conversation);
    expect(conversation.getAttribute('aria-selected')).toBe('true');
    await userEvent.keyboard('{ArrowRight}');
    expect(analysis.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(analysis);
    await userEvent.keyboard('{ArrowLeft}');
    expect(conversation.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(conversation);
    await userEvent.keyboard('{Home}');
    expect(analysis.getAttribute('aria-selected')).toBe('true');
    await userEvent.keyboard('{End}');
    expect(conversation.getAttribute('aria-selected')).toBe('true');
  });
});
