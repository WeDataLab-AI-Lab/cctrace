import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { AIReport, AIReportRun, AIReportsResponse } from '@/lib/types';
import { AIReportRequestError } from '@/lib/api';
import { actionErrorLabel, elapsedLabel, opensConsent, ReportPanelBody, runFailureReason, startErrorLabel } from './report-panel';
import type { ReportPanelBodyProps } from './report-panel';

const report = (over: Partial<AIReport> = {}): AIReport => ({
  run_id: 11,
  generated_at: '2026-09-13T21:02:00Z',
  tz: 'Asia/Seoul',
  runtime: 'codex-app-server',
  model: 'gpt-5.3-codex',
  usage: { reported: true, input_tokens: 184000, output_tokens: 6000 },
  duration_ms: 161000,
  summary: '이전 리포트 요약 문장',
  items: [],
  process: { tool_calls: [], segments_read: 2 },
  ...over,
});

const run = (over: Partial<AIReportRun> = {}): AIReportRun => ({
  id: 12,
  status: 'completed',
  started_at: '2026-09-13T21:00:00Z',
  finished_at: null,
  tool_calls: [],
  ...over,
});

const response = (over: Partial<AIReportsResponse> = {}): AIReportsResponse => ({
  week: '2026-W37',
  tz: 'Asia/Seoul',
  since: '2026-09-06T15:00:00Z',
  until: '2026-09-13T15:00:00Z',
  in_progress: false,
  runtime: { enabled: true, key: 'codex-app-server', configured: true, available: true, reason: null },
  consent: { granted: true, runtime_key: 'codex-app-server:chatgpt', disclosure_version: '2026-09-14' },
  segment_count: 58,
  // The built-in default, off: the state tests are about runs, not schedules.
  schedule: { enabled: false, enabled_source: 'default', weekday: 1, hour: 6, minute: 0, when_source: 'default', tz: 'Asia/Seoul', next_run: null },
  run: null,
  report: null,
  ...over,
});

const noop = () => {};

const render = (over: Partial<ReportPanelBodyProps> = {}) =>
  renderToStaticMarkup(
    createElement(ReportPanelBody, {
      data: response(),
      readState: 'read',
      timeZone: 'Asia/Seoul',
      processOpen: false,
      onToggleProcess: noop,
      onStart: noop,
      onRegenerate: noop,
      onCancel: noop,
      onOpenSettings: noop,
      pending: false,
      actionError: null,
      ...over,
    }),
  );

describe('ReportPanelBody', () => {
  it('draws a skeleton, not a state, before the read completes', () => {
    const html = render({ data: undefined, readState: 'loading' });

    expect(html).toContain('불러오는 중');
    expect(html).not.toContain('분석 시작');
    expect(html).not.toContain('설정되지 않았습니다');
  });

  it('says the report state could not be read', () => {
    expect(render({ data: undefined, readState: 'failed' })).toContain('리포트 상태를 불러오지 못했습니다');
  });

  it('says reports are switched off and who can turn them on, without a start button', () => {
    const html = render({ data: response({ runtime: { enabled: false, key: 'codex-app-server', configured: true, available: true, reason: null } }) });

    expect(html).toContain('AI 리포트가 꺼져 있습니다. 관리자에게 요청하세요');
    expect(html).toContain('data-state="disabled"');
    expect(html).not.toContain('분석 시작');
  });

  it('explains the page and who can enable it when unconfigured', () => {
    const html = render({ data: response({ runtime: { enabled: true, key: '', configured: false, available: false, reason: null } }) });

    expect(html).toContain('AI 런타임이 설정되지 않았습니다. 관리자에게 요청하세요');
    expect(html).toContain('data-state="unconfigured"');
  });

  // Runtime reasons are process output; the screen points to the admin page instead.
  it('says the runtime is unavailable without printing its raw reason', () => {
    const html = render({ data: response({ runtime: { enabled: true, key: 'codex-app-server', configured: true, available: false, reason: 'error -32601: /srv/secret' } }) });

    expect(html).toContain('AI 런타임에 연결할 수 없습니다');
    expect(html).toContain('관리자');
    expect(html).not.toContain('/srv/secret');
    expect(html).not.toContain('분석 시작');
  });

  it('offers analysis before consent, which opens the consent dialog', () => {
    const html = render({ data: response({ consent: { granted: false, runtime_key: 'k', disclosure_version: 'v' } }) });

    expect(html).toContain('data-state="consent_required"');
    expect(html).toContain('분석 시작');
  });

  it('disables the button and says why in a week without segments', () => {
    const html = render({ data: response({ segment_count: 0 }) });

    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>분석 시작/);
    expect(html).toContain('작업 구간이 없어');
  });

  it('shows the expected size before the first report', () => {
    const html = render();

    expect(html).toContain('분석 시작');
    expect(html).toContain('구간 58개 중 일부를 읽음');
  });

  it('shows live tool calls and a stop button while running', () => {
    const calls = [{ seq: 1, tool: 'query_segments', args: { limit: 20 }, status: 'running' as const }];
    const html = render({ data: response({ run: run({ status: 'running', tool_calls: calls }), report: report() }) });

    expect(html).toContain('query_segments');
    expect(html).toContain('중지');
    expect(html).not.toContain('이전 리포트 요약 문장');
  });

  // A run takes minutes. Without a sign of motion the card reads as a hung
  // screen, and the first thing a user does to a hung screen is reload it --
  // which abandons the stream and starts the wait over.
  it('keeps the wait visibly alive while running', () => {
    const html = render({ data: response({ run: run({ status: 'running' }) }) });

    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('cctrace-bar');
  });
});

describe('elapsedLabel', () => {
  // Same words as the finished report's duration, so the number a user watched
  // climb is the number they are left with.
  it('counts from the run start in the report duration format', () => {
    expect(elapsedLabel('2026-09-13T21:00:00Z', Date.parse('2026-09-13T21:00:41Z'))).toBe('41초');
    expect(elapsedLabel('2026-09-13T21:00:00Z', Date.parse('2026-09-13T21:02:41Z'))).toBe('2분 41초');
  });

  // The browser clock is not the server's. A few seconds of skew must not print
  // a run that has not started yet.
  it('never counts backwards when the clocks disagree', () => {
    expect(elapsedLabel('2026-09-13T21:00:00Z', Date.parse('2026-09-13T20:59:50Z'))).toBe('0초');
  });

  it('says nothing when the run has no start time', () => {
    expect(elapsedLabel('', Date.parse('2026-09-13T21:00:41Z'))).toBe('');
  });
});

describe('ReportPanelBody states', () => {

  it('keeps the earlier report under a failure banner', () => {
    const html = render({
      data: response({ run: run({ status: 'failed', error: { code: 'time_limit', message: '시간 제한을 넘었습니다' } }), report: report() }),
    });

    expect(html).toContain('마지막 생성이 실패했습니다');
    expect(html).toContain('시간 제한을 넘었습니다');
    expect(html).not.toContain('/srv/secret');
    expect(html).toContain('이전 리포트 요약 문장');
  });

  it('gives the reason and a retry when a failure left no report', () => {
    const html = render({ data: response({ run: run({ status: 'failed', error: { code: 'invalid_output', message: 'AI 응답 형식이 올바르지 않습니다' } }) }) });

    expect(html).toContain('AI 응답 형식이 올바르지 않습니다');
    expect(html).toContain('다시 시도');
  });

  it('says the model returned nothing when the report came back empty', () => {
    const html = render({ data: response({ run: run({ status: 'failed', error: { code: 'empty_report', message: '모델이 빈 리포트를 반환했습니다' } }) }) });

    expect(html).toContain('모델이 빈 리포트를 반환했습니다');
    expect(html).toContain('다시 시도');
  });

  it('draws summary, items, process and generation info when completed', () => {
    const html = render({ data: response({ run: run(), report: report() }) });

    expect(html).toContain('AI 작성');
    expect(html).toContain('선정한 작업 없음');
    expect(html).toContain('분석 과정');
    expect(html).toContain('재분석');
    expect(html).toContain('data-state="completed"');
  });

  // 재실행은 리포트를 다시 만드는 주요 동작인데, 생성 시각·모델·토큰이 나열된
  // 회색 푸터 오른쪽 끝에 작은 버튼으로 있었다. 사람이 그 자리를 보지 않아
  // "버튼이 없다" 는 말이 나왔다. 라벨만 검사하면 푸터에 그대로 두고도 통과하므로
  // 요약 카드 헤더 안에 있는지까지 본다.
  it('puts the re-analyse action in the summary header, not the footer', () => {
    const html = render({ data: response({ run: run(), report: report() }) });

    const header = html.slice(html.indexOf('요약'), html.indexOf('</header>', html.indexOf('요약')));
    expect(header).toContain('재분석');

    const footer = html.slice(html.indexOf('<footer'));
    expect(footer).not.toContain('재분석');
  });

  it('shows a refused action', () => {
    expect(render({ actionError: '이미 생성 중인 리포트가 있습니다' })).toContain('이미 생성 중인 리포트가 있습니다');
  });
});

describe('startErrorLabel', () => {
  it('turns server codes into a reason', () => {
    expect(startErrorLabel('already_running')).toContain('이미 생성 중');
    expect(startErrorLabel('no_records')).toContain('작업 구간');
    expect(startErrorLabel('runtime_account_changing')).toBe('AI 연결 계정을 바꾸는 중이라 리포트를 생성할 수 없습니다');
    expect(startErrorLabel('http_500')).toContain('http_500');
  });

  // A dropped connection is not a refusal.
  it('does not call a network failure a refused request', () => {
    expect(actionErrorLabel(new TypeError('Failed to fetch'), 'start')).toBe('서버에 연결하지 못했습니다');
  });

  it('says a stop failed in its own words', () => {
    expect(actionErrorLabel(new AIReportRequestError(409, 'not_running'), 'cancel')).toBe('중지하지 못했습니다 (not_running)');
    expect(actionErrorLabel(new AIReportRequestError(409, 'already_running'), 'start')).toBe('이미 생성 중인 리포트가 있습니다');
    expect(actionErrorLabel(null, 'start')).toBeNull();
  });
});

describe('runFailureReason', () => {
  it('names a known code and shows an unknown one as a code', () => {
    // The server answers with the reader's message; the screen shows what it
    // was given rather than keeping a second map that drifts from it.
    expect(runFailureReason(run({ status: 'failed', error: { code: 'tool_call_limit', message: '도구 호출 한도를 넘었습니다' } }))).toBe('도구 호출 한도를 넘었습니다');
    expect(runFailureReason(run({ status: 'failed', error: { code: 'weird', message: '' } }))).toBe('오류 코드 weird');
    expect(runFailureReason(null)).toBe('원인 미확인');
  });
});

describe('opensConsent', () => {
  // The screen's consent state can be stale: a 403 still leads to the dialog.
  it('opens the consent dialog for a consent_required refusal only', () => {
    expect(opensConsent(new AIReportRequestError(403, 'consent_required'))).toBe(true);
    expect(opensConsent(new AIReportRequestError(409, 'already_running'))).toBe(false);
    expect(opensConsent(new TypeError('Failed to fetch'))).toBe(false);
  });
});
