import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { SessionRecord } from '@/lib/session-analysis-types';
import { WorkPanel } from './work-panel';

const record: SessionRecord = {
  state: 'ready', generated_at: '2026-10-08T12:00:00Z', covers_until: '2026-10-08T11:00:00Z',
  headline: '작업 제안', bullets: [], review: [], tags: { kinds: [], components: [], screens: [], related: [] },
  work: [
    { id: 'w1', kind: 'request', done: false, text: '필터를 추가한다', commits: [], tasks: [] },
    { id: 'w2', kind: 'followup', done: true, text: '검색을 검증한다', commits: [], tasks: [] },
    { id: 'w3', kind: 'finishing', done: null, text: '문서를 갱신한다', commits: [], tasks: [] },
  ],
};

const renderPanel = (value: SessionRecord): string => renderToStaticMarkup(createElement(WorkPanel, {
  id: 'work', record: value, turns: [], onOpenRequest: () => undefined,
}));

describe('작업 상황의 모델 제안 표시(P1.5)', () => {
  it('항목별 제안을 보존하지만 완료 상태와 미완 건수로 확정하지 않는다', () => {
    const html = renderPanel(record);
    const text = html.replace(/<[^>]+>/g, '');
    expect(text).toContain('3건');
    expect(text).toContain('판정 검증 대기');
    expect(text).toContain('모델 제안: 미완');
    expect(text).toContain('모델 제안: 완료');
    expect(text).toContain('work[].done 모델 제안');
    expect(text).toContain('unvalidated_model');
    expect(text).toContain('P1.5 사람 평가 검증 기준');
    expect(text).not.toContain('미완 1');
    expect((html.match(/data-state="unknown"/g) ?? []).length).toBe(3);
  });
});
