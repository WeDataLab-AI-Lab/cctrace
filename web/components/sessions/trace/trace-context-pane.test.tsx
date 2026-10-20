import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it, vi } from 'vitest';
import { TraceContextPane } from './trace-context-pane';
import { toolContextRecords } from './trace-context-test-fixture';
import type { SessionRecord } from '@/lib/types';

const markup = (records: SessionRecord[], loading = false, loadError = false) => renderToStaticMarkup(createElement(TraceContextPane, {
  records, loading, loadError, onRetry: vi.fn(), onClose: vi.fn(),
}));
it('shows separate original call/result records and escaped normalized tool bodies', () => {
  const records = toolContextRecords();
  records[0].view!.tool_calls![0].input = '<script>fixture input</script>';
  const html = markup(records);
  expect(html).toContain('도구 입력');
  expect(html).toContain('도구 결과');
  expect(html).toContain('trace-fixture stdout');
  expect(html).not.toContain('결과 미확인');
  expect(html).toContain('&lt;script&gt;fixture input&lt;/script&gt;');
  expect(html).not.toContain('<script>');
  expect(html).not.toContain('raw fixture must stay hidden');
  expect(html).not.toContain('raw result must stay hidden');
  expect(html).not.toContain('사용자 입력만 보기');
});
it('never reconstructs hidden or missing views from raw and distinguishes unavailable content from loading', () => {
  const records = toolContextRecords();
  records[0].view = { kind: 'hidden', anchor_id: 'hidden', dedupe_key: '', text: 'hidden view body' };
  records[1].view = undefined;
  const html = markup(records);
  expect(html).not.toContain('hidden view body');
  expect(html).not.toContain('raw fixture must stay hidden');
  expect(html).not.toContain('raw result must stay hidden');
  expect(html).toContain('이 범위에서 표시할 원문이 제공되지 않았습니다.');
  expect(markup([], true)).toContain('선택한 원문을 읽는 중입니다.');
});
it('preserves redacted empty tool payloads, excludes inferred results, and shows observed errors', () => {
  const records = toolContextRecords();
  records[0].view!.tool_calls![0].input = undefined;
  records[1].view!.tool_results = [{ id: 'call-1', is_error: true, output: 'fixture failure' },
    { id: 'inferred', output: 'reconstructed prose', inferred: true }, { id: 'redacted' }];
  const html = markup(records);
  expect(html).toContain('도구 입력 내용이 제공되지 않았습니다.');
  expect(html).toContain('fixture failure');
  expect(html).toContain('error');
  expect(html).toContain('도구 결과 내용이 제공되지 않았습니다.');
  expect(html).not.toContain('reconstructed prose');
  expect(html).not.toContain('raw result must stay hidden');
});
it('renders only context-supplied conversation text alongside retry/close controls', () => {
  const record = { ...toolContextRecords()[0], view: { kind: 'message' as const, role: 'user' as const,
    text: 'bounded original text', anchor_id: 'context-message', dedupe_key: '' } };
  const html = markup([record], false, true);
  expect(html).toContain('bounded original text');
  expect(html).toContain('role="alert"');
  expect(html).toContain('다시 시도');
  expect(html).toContain('닫기');
});
