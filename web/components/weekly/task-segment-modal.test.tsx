import { describe, expect, it } from 'vitest';
import type { TaskSegment } from '@/lib/types';
import { segmentToolLabel } from './task-segment-modal';

const segment = (overrides: Partial<TaskSegment> = {}): TaskSegment => ({
  session_id: 's1',
  start_ts: '2026-09-14T09:00:00Z',
  tool_call_count: 4,
  tool_fail_count: 1,
  command_count: 0,
  input_tokens: 0,
  output_tokens: 0,
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
