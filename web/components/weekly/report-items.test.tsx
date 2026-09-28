import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIReportItem } from '@/lib/types';
import { formatReportTime, ReportItems, segmentHref } from './report-items';

const TZ = 'Asia/Seoul';

const item = (over: Partial<AIReportItem> = {}): AIReportItem => ({
  segment_id: '98231',
  // The model's title carries its own numbers; none of them may reach the meta row.
  title: '재개 세션 token_count 이중 집계 추적 (도구 999회)',
  reason: '같은 테스트 실패 후 후속 입력이 12회 이어짐',
  meta: {
    session_id: 'sess/1',
    start_ts: '2026-09-12T05:20:00Z',
    project_name: 'cctrace',
    agent: 'claude',
    typed_turn_count: 18,
    tool_call_count: 41,
    tool_fail_count: 3,
  },
  ...over,
});

const render = (items: AIReportItem[]) => renderToStaticMarkup(createElement(ReportItems, { items, timeZone: TZ }));

describe('ReportItems', () => {
  it('tags the block as selected by AI', () => {
    const html = render([item()]);

    expect(html).toContain('다시 볼 작업');
    expect(html).toContain('AI 선정');
  });

  // No filler and no praise when nothing stood out (spec §3.3).
  it('says one line when nothing was selected', () => {
    const html = render([]);

    expect(html).toContain('선정한 작업 없음');
    expect(html).not.toContain('<li');
  });

  it('draws the meta row from the server meta only', () => {
    const html = render([item()]);
    const meta = /data-meta="true"[^>]*>(.*?)<\/p>/.exec(html)?.[1] ?? '';

    expect(meta).toContain('cctrace');
    expect(meta).toContain('9/12 14:20');
    expect(meta).toContain('입력 기록 18');
    expect(meta).toContain('도구 41');
    expect(meta).toContain('실패 3');
    expect(meta).not.toContain('999');
  });

  // Duration, commit and token sums are not facts this row may state (spec §3.3).
  it('leaves duration, commits and tokens out of the meta row', () => {
    const html = render([item()]);

    expect(html).not.toMatch(/토큰|token 합|커밋|소요/);
  });

  it('shows the reason and links the segment in the sessions view', () => {
    const html = render([item()]);

    expect(html).toContain('선정 이유');
    expect(html).toContain('같은 테스트 실패 후 후속 입력이 12회 이어짐');
    expect(html).toContain('href="/sessions?session_id=sess%2F1&amp;from=2026-09-12T05%3A20%3A00Z"');
    expect(html).toContain('구간 열기');
  });

  // Codex calls are counted from its JSONL tool_call rows; what it never records
  // is their outcome, so its failure count of 0 would read as "none failed".
  it('prints Codex calls and says the outcome was not observed', () => {
    const html = render([item({ meta: { ...item().meta, agent: 'codex', tool_call_count: 7, tool_fail_count: 0 } })]);

    expect(html).toContain('도구 7');
    expect(html).toContain('실패 미관측');
    expect(html).not.toContain('실패 0');
  });
});

describe('segmentHref', () => {
  it('matches the task segment modal link', () => {
    expect(segmentHref(item().meta)).toBe('/sessions?session_id=sess%2F1&from=2026-09-12T05%3A20%3A00Z');
  });
});

describe('formatReportTime', () => {
  it('prints month/day and 24-hour time in the given zone', () => {
    expect(formatReportTime('2026-09-13T21:02:00Z', TZ)).toBe('9/14 06:02');
  });
});
