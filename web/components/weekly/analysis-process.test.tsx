import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIToolCall } from '@/lib/types';
import { AnalysisProcess, summarizeArgs, ToolCallList } from './analysis-process';

const calls: AIToolCall[] = [
  { seq: 1, tool: 'query_segments', args: { order_by: 'tool_fail_count', limit: 20 }, status: 'ok', result_rows: 20, duration_ms: 41 },
  { seq: 2, tool: 'read_segment', args: { segment_id: '98231' }, status: 'timeout', result_rows: null, duration_ms: 15000 },
  { seq: 3, tool: 'compare_week', args: {}, status: 'running' },
];

const noop = () => {};

const render = (open: boolean) =>
  renderToStaticMarkup(createElement(AnalysisProcess, { toolCalls: calls, segmentsRead: 6, open, onToggle: noop }));

describe('AnalysisProcess', () => {
  it('is a disclosure with counts in its header', () => {
    const html = render(false);

    expect(html).toContain('분석 과정');
    expect(html).toContain('도구 호출 3회');
    expect(html).toContain('읽은 구간 6개');
    expect(html).toContain('aria-expanded="false"');
  });

  // Collapsed by default; the open state belongs to the parent so a poll or a
  // stream event that re-renders the list does not fold it back (spec §3.5).
  it('draws the list only when the parent says it is open', () => {
    expect(render(false)).not.toContain('query_segments');

    const open = render(true);
    expect(open).toContain('aria-expanded="true"');
    expect(open).toContain('query_segments');
  });
});

describe('ToolCallList', () => {
  const html = renderToStaticMarkup(createElement(ToolCallList, { toolCalls: calls }));

  it('lists tool, argument summary, rows and duration in seq order', () => {
    expect(html.indexOf('query_segments')).toBeLessThan(html.indexOf('read_segment'));
    expect(html).toContain('order_by=tool_fail_count, limit=20');
    expect(html).toContain('결과 20행');
    expect(html).toContain('41ms');
    expect(html).toContain('15.0초');
  });

  it('names a call that did not finish instead of printing zero rows', () => {
    expect(html).toContain('시간 초과');
    expect(html).toContain('실행 중');
    expect(html).not.toContain('결과 0행');
  });
});

describe('summarizeArgs', () => {
  it('writes arguments as key=value and says when there are none', () => {
    expect(summarizeArgs({ had_compact: true, agent: 'claude' })).toBe('had_compact=true, agent=claude');
    expect(summarizeArgs({})).toBe('인자 없음');
  });
});
