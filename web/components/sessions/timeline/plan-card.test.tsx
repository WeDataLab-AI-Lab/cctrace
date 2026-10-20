// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type { AnalysisPlan, PlanDecision } from '@/lib/session-analysis-types';
import { PlanCard } from './plan-card';
import { planOf } from './test-data';

afterEach(cleanup);

const renderPlan = (plan: AnalysisPlan): HTMLElement => {
  render(<PlanCard plan={plan} />);
  return screen.getByRole('group', { name: '플랜' });
};

const LONG_BODY = Array.from({ length: 14 }, (_, i) => `${i + 1}. 긴 플랜의 단계 ${i + 1}`).join('\n');

describe('결정', () => {
  const cases: [PlanDecision, string][] = [
    ['approved', '승인'],
    ['rejected', '반려'],
    ['interrupted', '중단'],
    ['pending', '대기 중'],
  ];

  for (const [decision, label] of cases) {
    it(`${decision}: "${label}" 칩과 data 표시`, () => {
      const root = renderPlan(planOf({ decision }));
      expect(root.textContent).toContain(label);
      expect(root.dataset.planDecision).toBe(decision);
    });
  }

  it('결정마다 칩의 설명(title)이 다르다', () => {
    const titles = cases.map(([decision]) => {
      const root = renderPlan(planOf({ decision }));
      const title = root.querySelector('[title]')?.getAttribute('title') ?? '';
      cleanup();
      return title;
    });
    expect(new Set(titles).size).toBe(4);
  });

  it('반려는 위험 색, 대기 중은 점선이다', () => {
    const rejected = renderPlan(planOf({ decision: 'rejected' }));
    expect(rejected.querySelector('[title]')?.className).toContain('danger');
    cleanup();
    const pending = renderPlan(planOf({ decision: 'pending' }));
    expect(pending.querySelector('[title]')?.className).toContain('border-dashed');
  });
});

describe('머리', () => {
  it('플랜 파일 경로를 보이고, 없으면 없다고 적는다', () => {
    expect(renderPlan(planOf()).textContent).toContain('/plans/inbox-filters.md');
    cleanup();
    expect(renderPlan(planOf({ file_path: null })).textContent).toContain('플랜 파일 경로 없음');
  });

  it('시각은 시:분으로 보인다', () => {
    expect(renderPlan(planOf()).querySelector('time')?.textContent).toBe('12:30');
  });

  it('읽을 수 없는 시각은 그리지 않는다', () => {
    expect(renderPlan(planOf({ ts: 'not-a-time' })).querySelector('time')).toBeNull();
  });
});

describe('본문', () => {
  it('짧은 본문은 접지 않는다', () => {
    const root = renderPlan(planOf());
    expect(root.textContent).toContain('필터 칩을 만든다');
    expect(screen.queryByRole('button', { name: '전체 보기' })).toBeNull();
  });

  it('긴 본문은 접어 두고 "전체 보기"로 펴며, 접힌 동안은 inert다', async () => {
    renderPlan(planOf({ body: LONG_BODY }));
    const toggle = screen.getByRole('button', { name: '전체 보기' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    const body = document.getElementById(toggle.getAttribute('aria-controls') ?? '');
    expect(body?.hasAttribute('inert')).toBe(true);
    await userEvent.click(toggle);
    const opened = screen.getByRole('button', { name: '접기' });
    expect(opened).toBe(toggle);
    expect(opened.getAttribute('aria-expanded')).toBe('true');
    expect(body?.hasAttribute('inert')).toBe(false);
    expect(document.activeElement).toBe(opened);
    await userEvent.click(opened);
    expect(screen.getByRole('button', { name: '전체 보기' })).toBe(toggle);
  });

  it('비식별화·보류·빈 본문은 이유를 적는다', () => {
    expect(renderPlan(planOf({ state: 'redacted', body: undefined })).textContent).toContain('비식별화로 플랜 본문이 가려졌습니다.');
    cleanup();
    expect(renderPlan(planOf({ state: 'withheld', body: undefined })).textContent).toContain('보류됨');
    cleanup();
    expect(renderPlan(planOf({ body: '   ' })).textContent).toContain('플랜 본문이 기록되지 않았습니다.');
  });
});
