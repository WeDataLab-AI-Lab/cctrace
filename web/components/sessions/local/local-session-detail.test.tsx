// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import type { SessionRecord } from '@/lib/types';
import type { LocalSessionAnalysis } from '@/lib/local-analysis-types';
import { LocalSessionDetail, type LocalSessionDetailProps } from './local-session-detail';

const record = (over: Partial<SessionRecord> = {}): SessionRecord => ({
  session_id: 'native-session', project_hash: 'work', profile_email: '', model: 'claude',
  ts: '2026-10-09T10:00:00Z', record_type: 'user',
  view: { kind: 'message', role: 'user', text: '실제 입력', anchor_id: 'user-1', dedupe_key: 'user-1' },
  raw: { message: '실제 입력' }, ...over,
});
const analysis = (over: Partial<LocalSessionAnalysis> = {}): LocalSessionAnalysis => ({
  revision: 'r1', rules_version: 3, state: 'observed',
  session: { input_tokens: 0, output_tokens: 12, active_seconds: 0, turns: 1, inputs: 1, cost_usd: 0,
    turn_boundaries: { state: 'observed' } },
  turns: [{ turn_key: 'user-1', ordinal: 1, status: 'ended', started_at: '2026-10-09T10:00:00Z',
    preview: '실제 입력', preview_state: 'ok', input_tokens: 0, output_tokens: 12 }], ...over,
});
const props = (over: Partial<LocalSessionDetailProps> = {}): LocalSessionDetailProps => ({
  localId: 'opaque', title: '내 로컬 세션', revision: 'r1', records: [[record()]], analysis: analysis(), ...over,
});

afterEach(() => cleanup());

describe('LocalSessionDetail', () => {
  it('renders only normalized views for Claude and Codex, and pairs tool results', () => {
    render(<LocalSessionDetail {...props({ records: [[
      record({ raw: { message: 'raw only secret', token: 'hidden' } }),
      record({ ts: '2026-10-09T10:01:00Z', record_type: 'assistant', view: {
        kind: 'message', role: 'assistant', text: '**처리 중** [외부](https://example.org/path)', anchor_id: 'a1', dedupe_key: 'a1',
        tool_calls: [{ id: 'call', name: 'Read', input: '{"file":"a"}' }],
      } }),
      record({ ts: '2026-10-09T10:02:00Z', record_type: 'tool_result', model: 'codex',
        view: { kind: 'tool_result', anchor_id: 't1', dedupe_key: 't1', tool_results: [{ id: 'call', output: '파일 읽음' }] } }),
      record({ ts: '2026-10-09T10:03:00Z', record_type: 'tool_call', model: 'codex',
        view: { kind: 'tool_call', role: 'assistant', anchor_id: 'c1', dedupe_key: 'c1', tool_calls: [{ name: 'exec_command', input: 'pwd' }] } }),
      record({ ts: '2026-10-09T10:04:00Z', raw: { text: 'raw fallback forbidden' }, view: undefined }),
    ]] })} />);
    expect(screen.getByText('실제 입력')).toBeTruthy();
    expect(screen.getByText('처리 중')).toBeTruthy();
    expect(screen.getByRole('link', { name: '외부' }).getAttribute('href')).toBe('https://example.org/path');
    expect(screen.getAllByText('파일 읽음')).toHaveLength(1);
    expect(screen.getByText('exec_command')).toBeTruthy();
    expect(screen.queryByText('raw only secret')).toBeNull();
    expect(screen.queryByText('raw fallback forbidden')).toBeNull();
  });

  it('renders daemon-provided raw, only source basenames, and no raw for hidden or missing views', () => {
    render(<LocalSessionDetail {...props({ records: [[
      record({ source_file: '/Users/example/projects/session.jsonl', raw: { safe: '<script>alert(1)</script>', malformed: null } }),
      record({ ts: '2026-10-09T10:01:00Z', raw: { excluded: 'not shown' },
        view: { kind: 'hidden', anchor_id: 'hidden', dedupe_key: 'hidden' } }),
      record({ ts: '2026-10-09T10:02:00Z', raw: { sensitive: 'synthetic-hidden' }, view: undefined }),
      record({ ts: '2026-10-09T10:03:00Z', raw: undefined, view: undefined }),
    ]] })} />);
    fireEvent.click(screen.getByRole('tab', { name: '개별 원문' }));
    expect(screen.getByText('session.jsonl')).toBeTruthy();
    expect(screen.queryByText(/Users\/example/)).toBeNull();
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeTruthy();
    expect(screen.getAllByText('원문 미제공')).toHaveLength(3);
    expect(screen.queryByText(/not shown/)).toBeNull();
    expect(screen.queryByText(/synthetic-hidden/)).toBeNull();
    expect(document.querySelector('script')).toBeNull();
  });

  it('keeps the selected tab during growing pages and revision changes, without displaying stale records or analysis', () => {
    const view = render(<LocalSessionDetail {...props()} />);
    fireEvent.click(screen.getByRole('tab', { name: '분석' }));
    expect(screen.getByText('0', { selector: 'dd' })).toBeTruthy();
    expect(screen.getByText('$0.0000')).toBeTruthy();
    view.rerender(<LocalSessionDetail {...props({ revision: 'r2', records: [[record()], [record({ ts: '2026-10-09T11:00:00Z' })]] })} />);
    expect(screen.getByRole('tab', { name: '분석' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('원본이 변경되었습니다. 최신 분석을 기다리는 중입니다.')).toBeTruthy();
    fireEvent.click(screen.getByRole('tab', { name: '대화' }));
    view.rerender(<LocalSessionDetail {...props({ revision: 'r2', recordsRevision: 'r1', records: [[record()]] })} />);
    expect(screen.getByText('원본이 변경되었습니다. 최신 기록을 기다리는 중입니다.')).toBeTruthy();
    expect(screen.queryByText('실제 입력')).toBeNull();
  });

  it('keeps withheld, redacted, and absent previews distinct without showing withheld text', () => {
    const turns: LocalSessionAnalysis['turns'] = [
      { turn_key: 'withheld', ordinal: 1, status: 'ended', started_at: '2026-10-09T10:00:00Z',
        preview: 'do not disclose', preview_state: 'withheld', input_tokens: null, output_tokens: 0 },
      { turn_key: 'redacted', ordinal: 2, status: 'ended', started_at: '2026-10-09T10:01:00Z',
        preview: null, preview_state: 'redacted', input_tokens: 0, output_tokens: 0 },
      { turn_key: 'absent', ordinal: 3, status: 'ended', started_at: '2026-10-09T10:02:00Z',
        preview: null, preview_state: 'none', input_tokens: 0, output_tokens: 0 },
    ];
    render(<LocalSessionDetail {...props({ analysis: analysis({ turns }) })} />);
    fireEvent.click(screen.getByRole('tab', { name: '분석' }));
    expect(screen.getByText('입력 원문 미제공')).toBeTruthy();
    expect(screen.getByText('입력 내용이 가려졌습니다.')).toBeTruthy();
    expect(screen.getByText('입력 미관측')).toBeTruthy();
    expect(screen.queryByText('do not disclose')).toBeNull();
  });

  it('distinguishes unknown and zero, missing observations, and loading, error and paging', () => {
    const onLoadMore = vi.fn();
    const view = render(<LocalSessionDetail {...props({ records: [], recordsLoading: true, hasMore: true, onLoadMore })} />);
    expect(screen.getByText('로컬 기록을 불러오는 중입니다.')).toBeTruthy();
    view.rerender(<LocalSessionDetail {...props({ records: [], hasMore: true, onLoadMore })} />);
    expect(screen.getByText('관측된 기록이 없습니다.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '다음 기록 불러오기' }));
    expect(onLoadMore).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole('tab', { name: '분석' }));
    view.rerender(<LocalSessionDetail {...props({ records: [], analysis: analysis({ state: 'undeterminable',
      session: { input_tokens: null, output_tokens: 0, active_seconds: null, turns: null, inputs: null, cost_usd: null,
        turn_boundaries: { state: 'undeterminable' } }, turns: [] }) })} />);
    expect(screen.getAllByText('확인할 수 없음').length).toBeGreaterThan(0);
    expect(screen.getByText('0', { selector: 'dd' })).toBeTruthy();
    expect(screen.getByText(/턴 경계를 판단할 수 없어/)).toBeTruthy();
    view.rerender(<LocalSessionDetail {...props({ analysisError: true })} />);
    expect(screen.getByRole('alert').textContent).toContain('로컬 분석을 읽지 못했습니다');
  });
});
