import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIReport } from '@/lib/types';
import { formatDuration, formatTokenCount, GenerationInfo } from './generation-info';

const report = (over: Partial<AIReport> = {}): AIReport => ({
  run_id: 11,
  generated_at: '2026-09-13T21:02:00Z',
  tz: 'Asia/Seoul',
  runtime: 'codex-app-server',
  model: 'gpt-5.3-codex',
  usage: { reported: true, input_tokens: 184000, output_tokens: 6000 },
  duration_ms: 161000,
  summary: 's',
  items: [],
  process: { tool_calls: [], segments_read: 0 },
  ...over,
});

const render = (r: AIReport, inProgress = false) =>
  renderToStaticMarkup(createElement(GenerationInfo, { report: r, inProgress, timeZone: 'Asia/Seoul' }));

describe('GenerationInfo', () => {
  it('states time, runtime, model, tokens and duration', () => {
    const html = render(report());

    expect(html).toContain('생성 9/14 06:02');
    expect(html).toContain('Codex app-server');
    expect(html).toContain('gpt-5.3-codex');
    expect(html).toContain('입력 184K / 출력 6K');
    expect(html).toContain('2분 41초');
  });

  // 재실행 버튼은 요약 카드 헤더로 옮겼다. 여기는 생성 사실만 적는 자리다.
  it('carries no action button', () => {
    const html = render(report());

    expect(html).not.toContain('재분석');
    expect(html).not.toContain('다시 생성');
    expect(html).not.toContain('<button');
  });

  // A runtime that reports nothing must not print as a run that spent nothing.
  it('says tokens were not reported instead of printing zero', () => {
    const html = render(report({ usage: { reported: false, input_tokens: null, output_tokens: null } }));

    expect(html).toContain('토큰 미보고');
    expect(html).not.toContain('입력 0');
  });

  // No configured model and none reported: say so rather than leave a gap.
  it('names the default model when the run recorded none', () => {
    expect(render(report({ model: '' }))).toContain('기본 모델');
  });

  it('marks a report on a week still running', () => {
    expect(render(report(), true)).toContain('기간 진행 중 · 9/14 06:02 까지');
    expect(render(report())).not.toContain('기간 진행 중');
  });
});

describe('formatTokenCount / formatDuration', () => {
  it('abbreviates tokens', () => {
    expect(formatTokenCount(950)).toBe('950');
    expect(formatTokenCount(6000)).toBe('6K');
    expect(formatTokenCount(2_100_000)).toBe('2.1M');
  });

  it('writes seconds and minutes', () => {
    expect(formatDuration(41_000)).toBe('41초');
    expect(formatDuration(161_000)).toBe('2분 41초');
  });
});
