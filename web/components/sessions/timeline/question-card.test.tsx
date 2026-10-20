// @vitest-environment jsdom
import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { AnalysisQuestion } from '@/lib/session-analysis-types';
import { QuestionCard, REDACTED_NOTE, WITHHELD_NOTE } from './question-card';
import { option, questionItem, questionOf } from './test-data';

afterEach(cleanup);

const renderCard = (question: AnalysisQuestion): HTMLElement => {
  render(<QuestionCard question={question} />);
  return screen.getByRole('group', { name: /^질문/ });
};

/** 선택지 카드(자유 응답 포함)를 모두 모은다. */
const cards = (root: HTMLElement): HTMLElement[] => Array.from(root.querySelectorAll<HTMLElement>('li[data-chosen]'));
const chosen = (root: HTMLElement): HTMLElement[] => cards(root).filter((c) => c.dataset.chosen === 'true');
const labelsOf = (els: readonly HTMLElement[]): string[] => els.map((el) => el.querySelector('p')?.textContent?.replace('선택한 답: ', '') ?? '');

describe('단일 선택', () => {
  it('머리글, "1개 선택", 질문 문장, 선택지와 설명, 끝의 "자유 응답"', () => {
    const root = renderCard(questionOf());
    expect(within(root).getByRole('heading', { name: '질문: 표시 위치' })).toBeTruthy();
    expect(root.textContent).toContain('1개 선택');
    expect(root.textContent).not.toContain('여러 개 선택');
    expect(root.textContent).toContain('알림 센터를 어디에 둘까요?');
    expect(root.textContent).toContain('독립 페이지에 대한 설명');
    const all = cards(root);
    expect(all).toHaveLength(3);
    expect(all[all.length - 1].textContent).toContain('자유 응답');
  });

  it('고른 선택지만 또렷하게 표시한다(data-chosen)', () => {
    const root = renderCard(questionOf());
    const picked = chosen(root);
    expect(picked).toHaveLength(1);
    expect(labelsOf(picked)).toEqual(['헤더 드롭다운 (Recommended)']);
    // 다른 선택지와 자유 응답은 고르지 않은 상태다.
    expect(cards(root).filter((c) => c.dataset.chosen === 'false')).toHaveLength(2);
    // 테두리는 1px 테두리 + 1px 링으로 2px처럼 보인다.
    expect(picked[0].className).toContain('border-brand');
    expect(picked[0].className).toContain('ring-brand');
  });

  it('스크린리더가 고른 답을 알 수 있다', () => {
    const root = renderCard(questionOf());
    const sr = chosen(root)[0].querySelector('.sr-only');
    expect(sr?.textContent).toBe('선택한 답: ');
  });
});

describe('(Recommended)', () => {
  it('추천 선택지에만 붙는다', () => {
    const root = renderCard(questionOf());
    const texts = cards(root).map((c) => c.querySelector('p')?.textContent ?? '');
    expect(texts.filter((t) => t.includes('(Recommended)'))).toHaveLength(1);
    expect(texts.find((t) => t.includes('독립 페이지'))).not.toContain('(Recommended)');
  });

  it('답의 라벨 끝에 "(Recommended)"가 글자로 붙어 있어도 같은 선택지로 본다', () => {
    const root = renderCard(
      questionOf({ items: [questionItem({ answer: { labels: ['헤더 드롭다운 (Recommended)'], free_text: null } })] }),
    );
    expect(chosen(root)).toHaveLength(1);
    expect(root.textContent).not.toContain('선택지에 없는 답');
  });
});

describe('여러 개 선택', () => {
  const multi = questionOf({
    items: [
      questionItem({
        header: '필터 종류',
        question: '넣을 필터를 모두 고르세요',
        multi_select: true,
        options: [option('안 읽음'), option('멘션'), option('시스템 알림')],
        answer: { labels: ['안 읽음', '멘션'], free_text: null },
      }),
    ],
  });

  it('"여러 개 선택"이라 쓰고 고른 것이 둘이다', () => {
    const root = renderCard(multi);
    expect(root.textContent).toContain('여러 개 선택');
    expect(root.textContent).not.toContain('1개 선택');
    expect(labelsOf(chosen(root))).toEqual(['안 읽음', '멘션']);
  });

  it('체크 상자 기호를 쓴다(점 기호가 아니다)', () => {
    const root = renderCard(multi);
    expect(root.querySelectorAll('svg.lucide-square-check').length).toBe(2);
    expect(root.querySelectorAll('svg.lucide-square').length).toBe(2);
  });
});

describe('자유 응답과 선택지에 없는 답', () => {
  it('직접 쓴 답이 있으면 "자유 응답" 카드가 고른 것이고 글이 보인다', () => {
    const root = renderCard(
      questionOf({ items: [questionItem({ answer: { labels: [], free_text: '둘 다 말고 사이드바에 두세요' } })] }),
    );
    const picked = chosen(root);
    expect(picked).toHaveLength(1);
    expect(picked[0].textContent).toContain('자유 응답');
    expect(picked[0].textContent).toContain('둘 다 말고 사이드바에 두세요');
  });

  it('직접 쓴 답이 공백뿐이면 고른 것으로 치지 않는다', () => {
    const root = renderCard(questionOf({ items: [questionItem({ answer: { labels: ['독립 페이지'], free_text: '   ' } })] }));
    expect(labelsOf(chosen(root))).toEqual(['독립 페이지']);
  });

  it('선택지에 없는 라벨은 버리지 않고 고른 카드로 덧붙인다', () => {
    const root = renderCard(
      questionOf({ items: [questionItem({ answer: { labels: ['사라진 선택지'], free_text: null } })] }),
    );
    const picked = chosen(root);
    expect(labelsOf(picked)).toEqual(['사라진 선택지']);
    expect(picked[0].textContent).toContain('선택지에 없는 답입니다.');
  });
});

describe('답하지 않은 질문과 거절된 질문', () => {
  it('답 없음: 아무것도 고르지 않고 칩이 있다', () => {
    const root = renderCard(questionOf({ status: 'unanswered', items: [questionItem({ answer: null })] }));
    expect(chosen(root)).toHaveLength(0);
    expect(root.textContent).toContain('답 없음');
    expect(root.dataset.questionStatus).toBe('unanswered');
  });

  it('거절됨: 아무것도 고르지 않고 칩이 있다', () => {
    const root = renderCard(questionOf({ status: 'rejected', items: [questionItem({ answer: null })] }));
    expect(chosen(root)).toHaveLength(0);
    expect(root.textContent).toContain('거절됨');
    expect(root.textContent).not.toContain('답 없음');
  });

  it('상태가 답했음이 아니면 항목에 답이 딸려 와도 고른 것으로 그리지 않는다', () => {
    const root = renderCard(questionOf({ status: 'unanswered' }));
    expect(chosen(root)).toHaveLength(0);
  });

  it('답했으면 상태 칩이 없다', () => {
    const root = renderCard(questionOf());
    expect(root.textContent).not.toContain('답 없음');
    expect(root.textContent).not.toContain('거절됨');
  });
});

describe('질문 항목이 여럿', () => {
  const two = questionOf({
    items: [
      questionItem(),
      questionItem({
        header: '저장 방식',
        question: '읽음 상태를 어디에 저장할까요?',
        options: [option('서버'), option('브라우저', { recommended: true })],
        answer: { labels: ['서버'], free_text: null },
      }),
    ],
  });

  it('한 상자 안에서 항목마다 머리글과 고른 답이 따로다', () => {
    const root = renderCard(two);
    expect(within(root).getAllByRole('heading')).toHaveLength(2);
    expect(within(root).getByRole('heading', { name: '질문: 저장 방식' })).toBeTruthy();
    expect(labelsOf(chosen(root))).toEqual(['헤더 드롭다운 (Recommended)', '서버']);
    expect(root.getAttribute('aria-label')).toBe('질문: 표시 위치 / 저장 방식');
  });

  it('상태 칩은 상자 맨 위에 한 번만 붙는다', () => {
    const root = renderCard({ ...two, status: 'unanswered', items: two.items.map((i) => ({ ...i, answer: null })) });
    expect(root.textContent?.match(/답 없음/g)).toHaveLength(1);
  });
});

describe('가려졌거나 보류된 질문', () => {
  it('비식별화: 선택지 대신 안내를 쓴다', () => {
    const root = renderCard(questionOf({ state: 'redacted', items: [] }));
    expect(root.textContent).toContain(REDACTED_NOTE);
    expect(cards(root)).toHaveLength(0);
  });

  it('보류됨: 질문이 없었다는 뜻이 아니라고 적는다', () => {
    const root = renderCard(questionOf({ state: 'withheld', items: [] }));
    expect(root.textContent).toContain(WITHHELD_NOTE);
    expect(root.textContent).toContain('질문이 없었다는 뜻이 아닙니다');
    expect(cards(root)).toHaveLength(0);
  });

  it('가려진 질문의 상태 칩도 보인다', () => {
    const root = renderCard(questionOf({ state: 'redacted', status: 'unanswered', items: [] }));
    expect(root.textContent).toContain('답 없음');
  });

  it('항목이 하나도 없는 관측 질문도 빈 상자가 아니라 안내를 쓴다', () => {
    const root = renderCard(questionOf({ items: [] }));
    expect(root.textContent).toContain('질문 항목이 기록되지 않았습니다.');
  });
});
