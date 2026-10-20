// @vitest-environment jsdom
import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { A_ID } from '@/app/test/session-analysis/fixtures';
import { text, turn } from '@/app/test/session-analysis/builders';
import type { AnalysisTurn } from '@/lib/session-analysis-types';
import { passA, mount } from './analysis-interaction-test-fixture';

describe('턴 목록의 명시적 페이지', () => {
  it('긴 세션은 100행만 먼저 그리고 "더 보기"로 늘린다(활성과 버려진 분기 각각)', () => {
    const many = (prefix: string, n: number, branch: 'active' | 'abandoned'): AnalysisTurn[] =>
      Array.from({ length: n }, (_, i) =>
        turn({
          key: `${prefix}${i + 1}`,
          no: i + 1,
          at: 900 - i,
          branch,
          request_no: branch === 'active' ? i + 1 : null,
          preview: text(`${prefix} 요청 ${i + 1}`),
        }),
      );
    const analysis = { ...passA(A_ID), turns: many('a', 130, 'active'), abandoned: many('x', 110, 'abandoned') };
    mount(analysis);
    const active = () => screen.getByLabelText('활성 경로의 턴').children.length;
    const abandoned = () => screen.getByLabelText('버려진 분기의 턴').children.length;
    expect(active()).toBe(100);
    expect(abandoned()).toBe(100);
    fireEvent.click(screen.getByRole('button', { name: '30개 더 보기' }));
    expect(active()).toBe(130);
    expect(screen.queryByRole('button', { name: '30개 더 보기' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '버려진 분기 10개 더 보기' }));
    expect(abandoned()).toBe(110);
  });
});
