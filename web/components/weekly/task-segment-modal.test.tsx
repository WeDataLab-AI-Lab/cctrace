// @vitest-environment jsdom
import { createElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchTaskSegments } from '@/lib/api';
import type { TaskSegment } from '@/lib/types';
import { segmentTokensLabel, segmentToolLabel, TaskSegmentModal } from './task-segment-modal';

vi.mock('@/lib/api', () => ({ fetchTaskSegments: vi.fn() }));

const segment = (overrides: Partial<TaskSegment> = {}): TaskSegment => ({
  session_id: 's1',
  start_ts: '2026-09-14T09:00:00Z',
  tool_call_count: 4,
  tool_fail_count: 1,
  command_count: 0,
  input_tokens: 0,
  output_tokens: 0,
  tokens_ready: true,
  tool_evidence: true,
  tool_outcome_evidence: true,
  ...overrides,
});

describe('segmentToolLabel', () => {
  it('prints calls and failures when both were measured', () => {
    expect(segmentToolLabel(segment())).toBe('4 calls (1 failed)');
  });

  // Codex records its calls in JSONL but not their outcome, so a zero there is
  // unobserved, not "none failed".
  it('names the failures as unobserved when only the calls were measured', () => {
    const label = segmentToolLabel(segment({ tool_fail_count: 0, tool_outcome_evidence: false }));

    expect(label).toBe('4 calls · 실패 미관측');
  });

  it('says nothing was recorded when the calls were not measured', () => {
    const label = segmentToolLabel(segment({ tool_call_count: 0, tool_fail_count: 0, tool_evidence: false, tool_outcome_evidence: false }));

    expect(label).toBe('이 세션에 도구 기록 없음');
  });
});

describe('segmentTokensLabel', () => {
  it('prints the segment tokens once the server counts them by request', () => {
    expect(segmentTokensLabel(segment({ input_tokens: 1234, output_tokens: 56 }))).toBe('구간 1.2K in / 56 out');
  });

  // Until the recount reaches the stored segments, their numbers may count a
  // response once per line; the row says so rather than printing them.
  it('says the tokens are being recalculated instead of printing them while the recount runs', () => {
    const label = segmentTokensLabel(segment({ input_tokens: 1234, output_tokens: 56, tokens_ready: false }));

    expect(label).toBe('구간 토큰 재계산 중');
    expect(label).not.toContain('1.2K');
  });
});

const renderModal = (segments: TaskSegment[]) => {
  vi.mocked(fetchTaskSegments).mockResolvedValue({ segments, total: segments.length, truncated: false });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(TaskSegmentModal, {
        taskType: 'testing',
        since: '2026-09-14T00:00:00Z',
        until: '2026-09-21T00:00:00Z',
        onClose: () => {},
      }),
    ),
  );
};

afterEach(cleanup);

// While the server recounts the stored segments by request, their numbers may
// still count a response once per line: the rows say the tokens are being
// recalculated, and the rule the numbers follow is not shown without them.
describe('TaskSegmentModal tokens', () => {
  it('shows the recount, not the numbers or the counting rule, while tokens are not ready', async () => {
    renderModal([segment({ input_tokens: 1234, output_tokens: 56, tokens_ready: false })]);

    expect(await screen.findByText('구간 토큰 재계산 중')).not.toBeNull();
    expect(screen.queryByText(/1\.2K/)).toBeNull();
    expect(screen.queryByText(/API 요청마다/)).toBeNull();
  });

  it('shows the numbers and the counting rule once tokens are ready', async () => {
    renderModal([segment({ input_tokens: 1234, output_tokens: 56 })]);

    expect(await screen.findByText('구간 1.2K in / 56 out')).not.toBeNull();
    expect(screen.queryByText('구간 토큰 재계산 중')).toBeNull();
    expect(screen.getByText('구간 토큰은 API 요청마다 한 번 셉니다. Codex 는 캐시 읽기를 뺀 값입니다.')).not.toBeNull();
  });
});
