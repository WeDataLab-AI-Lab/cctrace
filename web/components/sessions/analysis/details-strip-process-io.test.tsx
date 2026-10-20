// @vitest-environment jsdom
import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { AnalysisLive, LiveInstance, LiveProcessMetrics } from '@/lib/session-analysis-types';
import { TooltipProvider } from '@/components/ui/tooltip';
import { DetailsStrip } from './details-strip';

const FIXED_NOW = Date.parse('2026-10-09T08:00:00Z');

afterEach(cleanup);

const instance = (process: LiveProcessMetrics, heartbeatAt: string): LiveInstance => ({
  status: 'busy', waiting_kind: '', heartbeat_at: heartbeatAt, name: '', stale: false, process,
});

describe('실시간 프로세스 입출력', () => {
  it('PID별 PTY 합산 출력과 pipes 분리 수치를 출처·범위·신선도와 함께 보여준다', () => {
    const ptyProcess: LiveProcessMetrics = {
      pid: 11, input_lines: 1, output_lines: 2, input_bytes: 4, output_bytes: 20,
      io_source: 'process_stream', io_scope: 'instance', io_mode: 'pty',
      io_as_of: '2026-10-09T07:59:50Z', io_epoch: 'epoch-pty',
      io_freshness: 'fresh', io_partial: false, io_failure_code: null,
      stdout_bytes: null, stdout_lines: null, stderr_bytes: null, stderr_lines: null,
    };
    const pipeProcess: LiveProcessMetrics = {
      pid: 12, input_lines: 2, output_lines: 3, input_bytes: 7, output_bytes: 13,
      io_source: 'process_stream', io_scope: 'instance', io_mode: 'pipes',
      io_as_of: '2026-10-09T07:58:50Z', io_epoch: 'epoch-pipes',
      io_freshness: 'partial', io_partial: true, io_failure_code: 'upload_failed',
      stdout_bytes: 9, stdout_lines: 2, stderr_bytes: 4, stderr_lines: 1,
    };
    const live: AnalysisLive = {
      instances: [instance(ptyProcess, '2026-10-09T07:59:55Z'), instance(pipeProcess, '2026-10-09T07:59:56Z')],
      collector: { heartbeat_at: null, last_stored_at: null },
    };
    render(<TooltipProvider><DetailsStrip id="details" header={undefined} ownerView now={FIXED_NOW} live={live} /></TooltipProvider>);

    const groups = screen.getAllByRole('group', { name: '프로세스 상세' });
    expect(groups).toHaveLength(2);
    const pty = groups.find((group) => within(group).queryByText('11') !== null);
    const pipes = groups.find((group) => within(group).queryByText('12') !== null);
    expect(pty).toBeTruthy();
    expect(pipes).toBeTruthy();
    expect(within(pty!).getByText('실행 인스턴스')).toBeTruthy();
    expect(within(pty!).getByText('PTY 합산 출력')).toBeTruthy();
    expect(within(pty!).queryByText('stdout 바이트')).toBeNull();
    expect(within(pipes!).getByText('stdout 바이트')).toBeTruthy();
    expect(within(pipes!).getByText('stderr 바이트')).toBeTruthy();
    expect(within(pipes!).getByText(/부분 측정 · 업로드 실패/)).toBeTruthy();
    expect(within(pipes!).queryByText('세션 합계')).toBeNull();
  });

  it('저장 transcript 합계와 실제 프로세스 수치를 출처별로 나누고 PID를 섞지 않는다', () => {
    const transcript: LiveProcessMetrics = {
      pid: null, input_lines: 3, output_lines: 8, input_bytes: 200, output_bytes: 1024,
      io_source: 'transcript', io_scope: 'session', io_freshness: 'fresh', io_partial: false,
      io_as_of: '2026-10-09T07:59:00Z',
    };
    const process: LiveProcessMetrics = {
      pid: 42, input_lines: 1, output_lines: 2, input_bytes: 12, output_bytes: 45,
      io_source: 'process_stream', io_scope: 'instance', io_mode: 'pty',
      io_as_of: '2026-10-09T07:59:50Z', io_epoch: 'epoch-process',
      io_freshness: 'fresh', io_partial: false,
    };
    const live: AnalysisLive = {
      instances: [instance(process, '2026-10-09T07:59:55Z')],
      transcript_io: transcript,
      collector: { heartbeat_at: null, last_stored_at: null },
    };
    render(<TooltipProvider><DetailsStrip id="details" header={undefined} ownerView now={FIXED_NOW} live={live} /></TooltipProvider>);
    const groups = screen.getAllByRole('group', { name: '프로세스 상세' });
    expect(groups).toHaveLength(2);
    const transcriptGroup = groups.find((group) => within(group).queryByText('대화 기록량') !== null);
    const processGroup = groups.find((group) => within(group).queryByText('42') !== null);
    expect(transcriptGroup).toBeTruthy();
    expect(processGroup).toBeTruthy();
    expect(within(transcriptGroup!).getByText('세션 합계')).toBeTruthy();
    expect(within(transcriptGroup!).getByText('저장 기록 기준')).toBeTruthy();
    expect(within(transcriptGroup!).getByRole('button', { name: /대화 기록량은 특정 프로세스 PID에 귀속되지 않습니다/ })).toBeTruthy();
    expect(within(processGroup!).getByText('42')).toBeTruthy();
    expect(within(processGroup!).queryByText('대화 기록량')).toBeNull();
  });

  it('가려진 transcript 방향은 알 수 없음과 부분 범위 이유를 표시한다', () => {
    const transcript: LiveProcessMetrics = {
      pid: null, input_lines: null, output_lines: 1, input_bytes: null, output_bytes: 6,
      io_source: 'transcript', io_scope: 'session', io_freshness: 'partial', io_partial: true,
      io_as_of: '2026-10-09T07:59:00Z', io_failure_code: null,
    };
    const live: AnalysisLive = {
      instances: [], transcript_io: transcript,
      collector: { heartbeat_at: null, last_stored_at: null },
    };
    render(<TooltipProvider><DetailsStrip id="details" header={undefined} ownerView now={FIXED_NOW} live={live} /></TooltipProvider>);
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getAllByText('알 수 없음').length).toBeGreaterThan(0);
    expect(within(group).getByText('6 B')).toBeTruthy();
    expect(within(group).getByText('부분 측정').closest('[title]')?.getAttribute('title')).toContain('가려지거나');
  });

  it('프로세스 스트림만 관측한 상태를 진행 중으로 표시하지 않는다', () => {
    const process: LiveProcessMetrics = {
      pid: 91, input_lines: 0, output_lines: 0, input_bytes: 0, output_bytes: 0,
      io_source: 'process_stream', io_scope: 'instance', io_mode: 'pty',
      io_as_of: '2026-10-09T07:59:50Z', io_epoch: 'epoch-unknown', io_freshness: 'fresh', io_partial: false,
    };
    const live: AnalysisLive = {
      instances: [{ ...instance(process, '2026-10-09T07:59:55Z'), status: 'unknown', status_source: 'process_io_unknown' }],
      collector: { heartbeat_at: null, last_stored_at: null },
    };
    render(<TooltipProvider><DetailsStrip id="details" header={undefined} ownerView now={FIXED_NOW} live={live} /></TooltipProvider>);
    const status = screen.getByText('판단 불가');
    expect(status.closest('[title]')?.getAttribute('title')).toContain('실제 프로세스 입출력은 수집하지만');
    expect(screen.queryByText('진행 중')).toBeNull();
  });

});
