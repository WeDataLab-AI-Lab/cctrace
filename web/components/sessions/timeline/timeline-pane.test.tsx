// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ComponentProps } from 'react';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { TimelineMessage } from '@/lib/session-analysis-types';
import { CONVERSATION, NOW, assistantMsg, userMsg } from './test-data';
import { FOCUS_FLASH_MS, SCROLL_PAD, SETTLE_MS, TimelinePane } from './timeline-pane';

type PaneProps = ComponentProps<typeof TimelinePane>;

const originalOffsetTop = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetTop');

beforeAll(() => {
  // jsdom에는 레이아웃이 없어 offsetTop이 늘 0이다. 부모 안의 순서 x 100px로 대신해 스크롤 위치를 셀 수 있게 한다.
  Object.defineProperty(HTMLElement.prototype, 'offsetTop', {
    configurable: true,
    get(this: HTMLElement) {
      const parent = this.parentElement;
      return parent ? Array.prototype.indexOf.call(parent.children, this) * 100 : 0;
    },
  });
});

afterAll(() => {
  if (originalOffsetTop) Object.defineProperty(HTMLElement.prototype, 'offsetTop', originalOffsetTop);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const BASE: PaneProps = {
  shortId: 'ad704e10',
  title: '알림 센터에 필터와 읽음 처리 넣기',
  messages: CONVERSATION,
  now: NOW,
  onClose: () => undefined,
};

const renderPane = (props: Partial<PaneProps> = {}) => render(<TimelinePane {...BASE} {...props} />);

const scroller = (): HTMLElement => screen.getByRole('region', { name: '타임라인 메시지' });
const message = (id: string): HTMLElement => {
  const found = scroller().querySelector<HTMLElement>(`[data-message-id="${id}"]`);
  if (!found) throw new Error(`message ${id} not rendered`);
  return found;
};
const hasMessage = (id: string): boolean => scroller().querySelector(`[data-message-id="${id}"]`) !== null;
const currentId = (): string | null => scroller().querySelector('[aria-current="location"]')?.getAttribute('data-message-id') ?? null;
const prevButton = (): HTMLButtonElement => screen.getByRole('button', { name: '이전 입력' });
const nextButton = (): HTMLButtonElement => screen.getByRole('button', { name: '다음 입력' });
const userOnlyBox = (): HTMLInputElement => screen.getByRole('checkbox', { name: '사용자 입력만 보기' });
const hideBox = (): HTMLInputElement => screen.getByRole('checkbox', { name: '중간 출력 숨기기' });
const inputTotalText = (): string => document.querySelector('[data-input-total]')?.textContent ?? '';
const metaOf = (id: string, index: number): string => message(id).querySelectorAll('p')[index]?.textContent ?? '';

describe('머리와 띠', () => {
  it('"타임라인" 제목, 짧은 ID와 제목, "입력 N건"', () => {
    renderPane();
    const pane = screen.getByRole('complementary', { name: '타임라인' });
    expect(within(pane).getByRole('heading', { name: '타임라인' })).toBeTruthy();
    expect(pane.textContent).toContain('ad704e10');
    expect(pane.textContent).toContain('알림 센터에 필터와 읽음 처리 넣기');
    expect(pane.textContent).toContain('입력 4건');
  });

  it('선택한 시간대는 사용자 입력 시각에 적용된다', () => {
    const input = userMsg(1, 1, { ts: '2026-12-08T05:20:00Z' });
    renderPane({ messages: [input], timeZone: 'America/New_York' });
    expect(metaOf(input.id, 0)).toContain('2026/12/08 00:20:00 GMT-05:00');
  });

  it('닫기를 누르면 onClose', async () => {
    const onClose = vi.fn();
    renderPane({ onClose });
    await userEvent.click(screen.getByRole('button', { name: '닫기' }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('제목이 없으면 "제목 없음"', () => {
    renderPane({ title: null });
    expect(screen.getByText('제목 없음')).toBeTruthy();
  });

  it('메시지가 없으면 안내를 쓰고 이동 버튼이 모두 꺼진다', () => {
    renderPane({ messages: [] });
    expect(screen.getByText('표시할 메시지가 없습니다.')).toBeTruthy();
    expect(inputTotalText()).toBe('입력 0건');
    expect(prevButton().disabled).toBe(true);
    expect(nextButton().disabled).toBe(true);
  });

  it('API가 입력 수를 관측하지 못하면 0 대신 판단 불가와 분석 출처 상태를 보인다', () => {
    renderPane({ inputCount: null, freshness: { state: 'unavailable', detail: '세션 분석 사실이 아직 없습니다.',
      matchedInputs: 0, expectedInputs: null, computedAt: null, rulesVersion: null } });
    expect(inputTotalText()).toBe('입력 판단 불가');
    expect(screen.getByRole('status').textContent).toContain('세션 분석 사실이 아직 없습니다.');
    expect(document.querySelector('[data-freshness="unavailable"]')).not.toBeNull();
  });

  it('사용자 입력이 하나도 없는 세션은 토글을 끄라고 안내한다', () => {
    renderPane({ messages: [assistantMsg('a', 't1')] });
    expect(screen.getByText(/직접 입력으로 확인된 메시지가 없습니다/)).toBeTruthy();
    expect(prevButton().disabled).toBe(true);
    expect(nextButton().disabled).toBe(true);
  });
});

describe('두 토글', () => {
  it('처음: 사용자 입력만 보기 켜짐, 중간 출력 숨기기 꺼짐이고 쓸 수 없다', () => {
    renderPane();
    expect(userOnlyBox().checked).toBe(true);
    expect(hideBox().checked).toBe(false);
    expect(hideBox().disabled).toBe(true);
    // 사용자 입력만 남는다.
    for (const id of ['u1', 'u2', 'u3']) expect(hasMessage(id), id).toBe(true);
    expect(hasMessage('u4')).toBe(false);
    for (const id of ['a1', 'a2', 'a4', 'a5', 'a6', 's1', 'a7']) expect(hasMessage(id), id).toBe(false);
    expect(screen.queryByText('구현 방향을 정리했습니다.')).toBeNull();
  });

  it('사용자 입력만 보기를 끄면 Claude의 출력, 질문 카드, 플랜, system 줄이 보인다', async () => {
    renderPane();
    await userEvent.click(userOnlyBox());
    expect(userOnlyBox().checked).toBe(false);
    expect(hideBox().disabled).toBe(false);
    expect(screen.getByText('구현 방향을 정리했습니다.')).toBeTruthy();
    expect(screen.getByText('파일을 먼저 읽어 보겠습니다.')).toBeTruthy();
    expect(screen.getByText('통합 테스트가 모두 통과했습니다.')).toBeTruthy();
    expect(screen.getByText('컨텍스트를 압축했습니다')).toBeTruthy();
    expect(within(message('a5')).getByText('답 없음')).toBeTruthy();
    expect(within(message('a6')).getByRole('group', { name: '플랜' })).toBeTruthy();
  });

  it('중간 출력 숨기기는 intermediate인 출력만 가린다', async () => {
    renderPane();
    await userEvent.click(userOnlyBox());
    await userEvent.click(hideBox());
    expect(hideBox().checked).toBe(true);
    expect(hasMessage('a1')).toBe(false);
    expect(hasMessage('a4')).toBe(false);
    for (const id of ['u1', 'a2', 'u2', 'u3', 'a5', 'a6', 's1', 'u4', 'a7']) expect(hasMessage(id), id).toBe(true);
  });

  it('사용자 입력만 보기를 다시 켜도 중간 출력 숨기기의 값은 남는다', async () => {
    renderPane();
    await userEvent.click(userOnlyBox());
    await userEvent.click(hideBox());
    await userEvent.click(userOnlyBox());
    expect(hideBox().disabled).toBe(true);
    expect(hideBox().checked).toBe(true);
    expect(hasMessage('a2')).toBe(false);
    await userEvent.click(userOnlyBox());
    expect(hasMessage('a2')).toBe(true);
    expect(hasMessage('a1')).toBe(false);
  });

  it('직접 입력 필터는 알림을 숨기지만 전체 입력 수는 유지한다', async () => {
    renderPane();
    const inputsShown = () => scroller().querySelectorAll('[data-input-n]').length;
    expect(inputsShown()).toBe(3);
    expect(inputTotalText()).toBe('입력 4건');
    await userEvent.click(userOnlyBox());
    expect(inputsShown()).toBe(4);
    await userEvent.click(hideBox());
    expect(inputsShown()).toBe(4);
    expect(inputTotalText()).toBe('입력 4건');
  });
});

describe('메시지 모양', () => {
  it('사용자 입력: 머리 줄은 "시각 (상대) · n/N · 나", 말풍선은 줄바꿈을 지키고 이미지 칩이 붙는다', () => {
    renderPane();
    expect(metaOf('u1', 0)).toBe('2026/10/03 11:56:22 (5시간 전) · 1/4 · 나');
    const bubble = message('u1').querySelector('[data-bubble]');
    expect(bubble?.className).toContain('whitespace-pre-wrap');
    expect(bubble?.textContent).toBe('알림 센터에 필터를 넣어 주세요.\n종류별로 걸 수 있으면 좋겠어요');
    expect(bubble?.className).toContain('bg-brand-soft');
    expect(within(message('u1')).getByText('이미지 2장')).toBeTruthy();
    expect(within(message('u2')).queryByText(/이미지/)).toBeNull();
  });

  it('말풍선과 머리 줄은 오른쪽에 붙는다', () => {
    renderPane();
    expect(message('u1').querySelector('[data-bubble]')?.className).toContain('self-end');
    expect(message('u1').querySelector('p')?.className).toContain('self-end');
  });

  it('질문에 답한 입력: "Claude · 질문 시각" 줄, 사용자 머리 줄, 카드 순서다', () => {
    renderPane();
    const answer = message('u2');
    expect(metaOf('u2', 0)).toBe('Claude · 2026/10/03 11:57:38 (5시간 전)');
    expect(metaOf('u2', 1)).toBe('2026/10/03 11:58:05 (5시간 전) · 2/4 · 질문에 답함 · 나');
    const card = within(answer).getByRole('group', { name: '질문: 표시 위치' });
    const second = answer.querySelectorAll('p')[1];
    expect(second.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // 답 입력은 말풍선이 없고 카드가 전부다.
    expect(answer.querySelector('[data-bubble]')).toBeNull();
    expect(card.querySelector('li[data-chosen="true"]')?.textContent).toContain('헤더 드롭다운');
  });

  it('사람이 아닌 입력(알림)은 출처만 적고 직접 입력 필터에서는 숨긴다', async () => {
    renderPane();
    expect(hasMessage('u4')).toBe(false);
    await userEvent.click(userOnlyBox());
    expect(metaOf('u4', 0)).toBe('2026/10/03 12:40:00 (4시간 전) · 4/4 · 백그라운드 알림');
    const bubble = message('u4').querySelector('[data-bubble]');
    expect(bubble?.className).not.toContain('bg-brand-soft');
    expect(bubble?.className).toContain('border-border');
    expect(metaOf('u3', 0)).toContain('작업 중 입력 · 나');
  });

  it('assistant: 왼쪽 "Claude · 시각" 줄과 본문, 중간 출력은 조용한 모양이다', async () => {
    renderPane();
    await userEvent.click(userOnlyBox());
    expect(metaOf('a2', 0)).toBe('Claude · 2026/10/03 11:57:30 (5시간 전)');
    expect(message('a2').querySelector('p')?.className).toContain('self-start');
    expect(message('a1').getAttribute('data-intermediate')).toBe('true');
    expect(message('a2').getAttribute('data-intermediate')).toBeNull();
    const quiet = screen.getByText('파일을 먼저 읽어 보겠습니다.').closest('div[class*="border-l-2"]');
    expect(quiet).not.toBeNull();
    expect(screen.getByText('구현 방향을 정리했습니다.').closest('div[class*="border-l-2"]')).toBeNull();
  });

  it('본문이 없는 입력은 이유를 적고, 이미지만 있는 입력은 이미지 칩만 둔다', () => {
    renderPane({ messages: [userMsg(1, 2, { text: undefined }), userMsg(2, 2, { text: undefined, images: 1 })] });
    expect(within(message('u1')).getByText(/본문을 볼 수 없습니다/)).toBeTruthy();
    expect(message('u2').querySelector('[data-bubble]')).toBeNull();
    expect(within(message('u2')).getByText('이미지 1장')).toBeTruthy();
  });

  it('메시지 바깥 요소가 턴 키와 입력 번호를 단다', () => {
    renderPane();
    expect(message('u3').dataset.turnKey).toBe('t3');
    expect(message('u3').dataset.inputN).toBe('3');
    expect(message('u2').dataset.turnKey).toBe('t1');
  });
});

describe('이전·다음 입력', () => {
  it('처음에는 첫 입력이 현재라 이전이 꺼져 있다', () => {
    renderPane();
    expect(currentId()).toBe('u1');
    expect(prevButton().disabled).toBe(true);
    expect(nextButton().disabled).toBe(false);
  });

  it('다음을 누르면 그 입력으로 스크롤하고 현재 입력이 옮겨 가며, 끝에서 다음이 꺼진다', async () => {
    renderPane();
    const user = userEvent.setup();
    await user.click(nextButton());
    expect(scroller().scrollTop).toBe(100 - SCROLL_PAD);
    expect(currentId()).toBe('u2');
    expect(prevButton().disabled).toBe(false);
    await user.click(nextButton());
    expect(scroller().scrollTop).toBe(200 - SCROLL_PAD);
    expect(currentId()).toBe('u3');
    expect(nextButton().disabled).toBe(true);
    expect(prevButton().disabled).toBe(false);
  });

  it('이전을 누르면 거꾸로 간다', async () => {
    renderPane();
    const user = userEvent.setup();
    await user.click(nextButton());
    await user.click(nextButton());
    await user.click(prevButton());
    expect(currentId()).toBe('u2');
    expect(scroller().scrollTop).toBe(100 - SCROLL_PAD);
    await user.click(prevButton());
    expect(currentId()).toBe('u1');
    expect(scroller().scrollTop).toBe(0);
    expect(prevButton().disabled).toBe(true);
  });

  it('번호가 빠진 입력(1, 2, 4)은 건너뛴다', async () => {
    renderPane({ messages: [userMsg(1, 4), userMsg(2, 4), userMsg(4, 4)] });
    const user = userEvent.setup();
    await user.click(nextButton());
    await user.click(nextButton());
    expect(currentId()).toBe('u4');
    expect(scroller().scrollTop).toBe(200 - SCROLL_PAD);
    expect(nextButton().disabled).toBe(true);
  });

  it('직접 입력 필터를 끄면 외부 입력도 이전·다음 이동에 포함한다', async () => {
    renderPane();
    const user = userEvent.setup();
    await user.click(userOnlyBox());
    await user.click(nextButton());
    expect(currentId()).toBe('u2');
    // 모든 메시지가 보이면 u2는 DOM의 네 번째(offsetTop 300)다.
    expect(scroller().scrollTop).toBe(300 - SCROLL_PAD);
  });

  it('현재 입력의 번호만 굵게 보인다', async () => {
    renderPane();
    await userEvent.click(nextButton());
    const bold = (id: string) => message(id).querySelector('p span.font-semibold')?.textContent ?? null;
    expect(bold('u2')).toBe('2/4');
    expect(bold('u1')).toBeNull();
    expect(scroller().querySelectorAll('[aria-current="location"]')).toHaveLength(1);
  });
});

describe('스크롤을 따라가는 현재 입력', () => {
  it('기준선(맨 위 + 24px) 위에 있는 마지막 표시 입력이 현재 입력이다', () => {
    renderPane();
    scroller().scrollTop = 250;
    fireEvent.scroll(scroller());
    expect(currentId()).toBe('u3');
    expect(nextButton().disabled).toBe(true);
    scroller().scrollTop = 280;
    fireEvent.scroll(scroller());
    expect(currentId()).toBe('u3');
    expect(nextButton().disabled).toBe(true);
    scroller().scrollTop = 0;
    fireEvent.scroll(scroller());
    expect(currentId()).toBe('u1');
    expect(prevButton().disabled).toBe(true);
  });

  it('이동 버튼을 누른 직후의 스크롤 이벤트는 현재 입력을 흔들지 않고, 잠금이 풀린 뒤에는 따라간다', () => {
    const clock = vi.spyOn(Date, 'now').mockReturnValue(10_000);
    renderPane();
    fireEvent.click(nextButton());
    expect(currentId()).toBe('u2');
    // 부드러운 이동의 출발점 근처에서 이벤트가 온다.
    scroller().scrollTop = 0;
    clock.mockReturnValue(10_000 + SETTLE_MS - 50);
    fireEvent.scroll(scroller());
    expect(currentId()).toBe('u2');
    clock.mockReturnValue(10_000 + SETTLE_MS + 50);
    fireEvent.scroll(scroller());
    expect(currentId()).toBe('u1');
  });
});

describe('focus: 상세에서 누른 턴', () => {
  it('그 턴의 첫 입력으로 스크롤하고 현재 입력을 옮기며 잠깐 표시한다', () => {
    vi.useFakeTimers();
    const { rerender } = renderPane();
    expect(scroller().scrollTop).toBe(0);
    rerender(<TimelinePane {...BASE} focus={{ turnKey: 't3', nonce: 1 }} />);
    // t3의 첫 메시지는 입력 3(u3)이다. 기본 거름의 DOM 순서는 u1, u2, u3, u4라 세 번째다.
    expect(scroller().scrollTop).toBe(200 - SCROLL_PAD);
    expect(message('u3').dataset.focused).toBe('true');
    expect(currentId()).toBe('u3');
    act(() => {
      vi.advanceTimersByTime(FOCUS_FLASH_MS + 10);
    });
    expect(message('u3').dataset.focused).toBeUndefined();
  });

  it('같은 턴을 다시 눌러도(nonce가 바뀌면) 다시 옮긴다', () => {
    const { rerender } = renderPane({ focus: { turnKey: 't3', nonce: 1 } });
    expect(scroller().scrollTop).toBe(200 - SCROLL_PAD);
    scroller().scrollTop = 0;
    rerender(<TimelinePane {...BASE} focus={{ turnKey: 't3', nonce: 1 }} />);
    expect(scroller().scrollTop).toBe(0);
    rerender(<TimelinePane {...BASE} focus={{ turnKey: 't3', nonce: 2 }} />);
    expect(scroller().scrollTop).toBe(200 - SCROLL_PAD);
  });

  it('다른 턴으로 바꾸면 이전 표시는 거두고 숨겨진 출처는 중립적으로 드러낸다', () => {
    const { rerender } = renderPane({ focus: { turnKey: 't3', nonce: 1 } });
    expect(message('u3').dataset.focused).toBe('true');
    rerender(<TimelinePane {...BASE} focus={{ turnKey: 't4', nonce: 2 }} />);
    expect(message('u3').dataset.focused).toBeUndefined();
    expect(userOnlyBox().checked).toBe(true);
    expect(screen.getByRole('status').textContent).toContain('현재 필터에서 숨겨져');
    expect(message('u4').dataset.focused).toBe('true');
    expect(currentId()).toBe('u4');
  });

  it('처음 그릴 때 이미 focus가 있으면 그 턴에서 시작한다', () => {
    renderPane({ focus: { turnKey: 't4', nonce: 1 } });
    expect(userOnlyBox().checked).toBe(true);
    expect(scroller().scrollTop).toBe(300 - SCROLL_PAD);
    expect(currentId()).toBe('u4');
  });

  it('질문에 답한 입력은 요청과 같은 턴이라 그 턴의 첫 입력(요청)으로 간다', () => {
    renderPane({ focus: { turnKey: 't1', nonce: 1 } });
    expect(scroller().scrollTop).toBe(0);
    expect(message('u1').dataset.focused).toBe('true');
    expect(currentId()).toBe('u1');
  });

  it('없는 턴이면 아무 데도 가지 않는다', () => {
    renderPane({ focus: { turnKey: 'none', nonce: 1 } });
    expect(scroller().scrollTop).toBe(0);
    expect(scroller().querySelector('[data-focused]')).toBeNull();
  });

  it('거름에 가려진 턴을 정확히 누르면 출처를 중립적으로 드러내고 이동한다', async () => {
    const hiddenTurn: TimelineMessage = assistantMsg('a9', 't9', { text: '입력 없이 Claude만 말한 턴' });
    renderPane({ messages: [...CONVERSATION, hiddenTurn], focus: { turnKey: 't9', nonce: 1 } });
    expect(userOnlyBox().checked).toBe(true);
    expect(screen.getByRole('status').textContent).toContain('이 항목만 표시합니다');
    expect(scroller().scrollTop).toBe(200 - SCROLL_PAD);
    expect(message('a9').dataset.focused).toBe('true');
  });
});
