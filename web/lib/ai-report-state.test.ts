import { describe, expect, it } from 'vitest';
import { aiReportPollInterval, aiReportQueryKey, reportPanelState } from './ai-report-state';
import { POLL_LIVE } from './query-config';
import type { AIReport, AIReportRun, AIReportsResponse } from './types';

const report = (over: Partial<AIReport> = {}): AIReport => ({
  run_id: 11,
  generated_at: '2026-09-13T21:02:00Z',
  tz: 'Asia/Seoul',
  runtime: 'codex-app-server',
  model: 'gpt-5.3-codex',
  usage: { reported: true, input_tokens: 184000, output_tokens: 6000 },
  duration_ms: 161000,
  summary: '이번 주는 토큰 집계 수정에 구간이 몰렸다.',
  items: [],
  process: { tool_calls: [], segments_read: 0 },
  ...over,
});

const run = (over: Partial<AIReportRun> = {}): AIReportRun => ({
  id: 12,
  status: 'completed',
  started_at: '2026-09-13T21:00:00Z',
  finished_at: '2026-09-13T21:02:41Z',
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
  // The built-in default, off: these tests are about which state wins, not
  // about schedules.
  schedule: { enabled: false, enabled_source: 'default', weekday: 1, hour: 6, minute: 0, when_source: 'default', tz: 'Asia/Seoul', next_run: null },
  run: null,
  report: null,
  ...over,
});

describe('reportPanelState', () => {
  // Spec §3.4: the first matching row wins, one state in the report's place.
  it('is disabled while AI reports are switched off, above everything else', () => {
    const data = response({
      runtime: { enabled: false, key: '', configured: false, available: false, reason: null },
      consent: { granted: false, runtime_key: '', disclosure_version: '' },
      report: report(),
    });

    expect(reportPanelState(data)).toEqual({ state: 'disabled', report: null });
  });

  it('is disabled even while a run is still marked running, since switching off cancels it', () => {
    const data = response({ runtime: { enabled: false, key: 'codex-app-server', configured: true, available: true, reason: null }, run: run({ status: 'running' }) });

    expect(reportPanelState(data).state).toBe('disabled');
  });

  it('is unconfigured when no runtime is set, above everything else', () => {
    const data = response({
      runtime: { enabled: true, key: '', configured: false, available: false, reason: null },
      consent: { granted: false, runtime_key: '', disclosure_version: '' },
      segment_count: 0,
      report: report(),
    });

    expect(reportPanelState(data)).toEqual({ state: 'unconfigured', report: null });
  });

  it('is unavailable when the runtime is set but cannot be reached', () => {
    const data = response({
      runtime: { enabled: true, key: 'codex-app-server', configured: true, available: false, reason: '인증 만료' },
      consent: { granted: false, runtime_key: 'k', disclosure_version: 'v' },
    });

    expect(reportPanelState(data).state).toBe('unavailable');
  });

  it('asks for consent before checking the week', () => {
    const data = response({ consent: { granted: false, runtime_key: 'k', disclosure_version: 'v' }, segment_count: 0 });

    expect(reportPanelState(data).state).toBe('consent_required');
  });

  it('has nothing to read in a week without segments', () => {
    expect(reportPanelState(response({ segment_count: 0 })).state).toBe('no_records');
  });

  it('is not generated when the week has neither a report nor a run', () => {
    expect(reportPanelState(response())).toEqual({ state: 'not_generated', report: null });
  });

  // A stopped run keeps nothing (spec §3.4 중지) -- without an earlier report the
  // week is simply not generated, not failed.
  it('treats a canceled run without a report as not generated', () => {
    expect(reportPanelState(response({ run: run({ status: 'canceled' }) })).state).toBe('not_generated');
  });

  // A run already started keeps its progress and stop button even if the cached
  // runtime status flips or the disclosure version changes mid-run.
  it('stays running over a runtime blip or a changed disclosure', () => {
    const running = run({ status: 'running', finished_at: null });

    expect(reportPanelState(response({ run: running, runtime: { enabled: true, key: 'k', configured: true, available: false, reason: null } })).state).toBe('running');
    expect(reportPanelState(response({ run: running, consent: { granted: false, runtime_key: 'k', disclosure_version: 'v2' } })).state).toBe('running');
  });

  it('is running while the latest run runs, even over an earlier report', () => {
    const data = response({ run: run({ status: 'running', finished_at: null }), report: report() });

    expect(reportPanelState(data)).toEqual({ state: 'running', report: null });
  });

  it('is failed with the earlier report kept when there is one', () => {
    const previous = report();
    const data = response({ run: run({ status: 'failed', error: { code: 'time_limit', message: '시간 초과' } }), report: previous });

    expect(reportPanelState(data)).toEqual({ state: 'failed', report: previous });
  });

  it('is failed with no report to fall back on', () => {
    const data = response({ run: run({ status: 'failed', error: { code: 'invalid_output', message: '' } }) });

    expect(reportPanelState(data)).toEqual({ state: 'failed', report: null });
  });

  it('is completed when a report exists', () => {
    const current = report();

    expect(reportPanelState(response({ run: run(), report: current }))).toEqual({ state: 'completed', report: current });
  });

  // Stopping a regeneration keeps the report already on screen.
  it('stays completed when a regeneration was canceled', () => {
    expect(reportPanelState(response({ run: run({ status: 'canceled' }), report: report() })).state).toBe('completed');
  });
});

describe('aiReportPollInterval', () => {
  // Spec §3.6: no polling by default; the stream carries progress. Polling is
  // the fallback for a run whose stream could not be read, and only while it runs.
  it('never polls while the stream is in charge', () => {
    expect(aiReportPollInterval(response({ run: run({ status: 'running' }) }), null)).toBe(false);
  });

  it('polls a running run whose stream failed', () => {
    expect(aiReportPollInterval(response({ run: run({ id: 12, status: 'running' }) }), 12)).toBe(POLL_LIVE);
  });

  it('stops once that run is no longer running', () => {
    expect(aiReportPollInterval(response({ run: run({ id: 12, status: 'completed' }) }), 12)).toBe(false);
    expect(aiReportPollInterval(undefined, 12)).toBe(false);
  });

  it('does not poll a newer run for an older stream failure', () => {
    expect(aiReportPollInterval(response({ run: run({ id: 13, status: 'running' }) }), 12)).toBe(false);
  });
});

describe('aiReportQueryKey', () => {
  it('scopes the report by user, week and time zone', () => {
    expect(aiReportQueryKey(7, '2026-W37', 'Asia/Seoul')).toEqual(['ai-report', 7, '2026-W37', 'Asia/Seoul']);
  });
});
