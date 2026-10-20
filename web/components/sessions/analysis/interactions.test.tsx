// @vitest-environment jsdom
import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { DETAIL_NOW } from '@/app/test/session-analysis/detail/builders';
import { processing, recordGenerating } from '@/app/test/session-analysis/detail/variants';
import type { SessionAnalysisFull } from '@/lib/session-analysis-types';
import { TooltipProvider } from '@/components/ui/tooltip';
import { DetailsStrip } from './details-strip';
import { isFullAnalysis } from './guards';
import { flagshipFull } from './analysis-interaction-test-fixture';

describe('실시간 프로세스 상세', () => {
  const show = (live: NonNullable<SessionAnalysisFull['live']>, ownerView = true) =>
    render(<TooltipProvider><DetailsStrip id="details" header={flagshipFull().header} now={DETAIL_NOW} ownerView={ownerView} live={live} /></TooltipProvider>);
  const liveOf = (fixture: ReturnType<typeof processing>) => {
    const analysis = fixture.analysis;
    if (!analysis || !isFullAnalysis(analysis) || !analysis.live) throw new Error('variant must have live data');
    return analysis.live;
  };

  it('출처가 미상인 입출력량은 0 대신 판단 불가로 표시하고 PID만 보여준다', () => {
    const live = flagshipFull().live;
    if (!live) throw new Error('flagship must have live data');
    show(live);
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getByText('18,427')).toBeTruthy();
    expect(within(group).getAllByText('알 수 없음')).toHaveLength(5);
    expect(within(group).getAllByRole('button', { name: /실제 스트림인지 대화 기록량인지 판단할 수 없습니다/ })).toHaveLength(5);
  });

  it('입출력 출처가 transcript이면 실제 스트림으로 표시하지 않는다', () => {
    const base = flagshipFull().live;
    if (!base) throw new Error('flagship must have live data');
    show({ ...base, instances: [{ ...base.instances[0], process: {
      pid: 42, input_lines: 3, output_lines: 8, input_bytes: 200, output_bytes: 1024, io_source: 'transcript',
    } }] });
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getByText('대화 기록량')).toBeTruthy();
    expect(within(group).getByText('1.0 KB')).toBeTruthy();
    expect(within(group).queryByText('실제 스트림')).toBeNull();
  });

  it('실제 스트림의 관측값 0은 알 수 없음과 구별한다', () => {
    const base = flagshipFull().live;
    if (!base) throw new Error('flagship must have live data');
    show({ ...base, instances: [{ ...base.instances[0], process: {
      pid: 42, input_lines: 0, output_lines: 0, input_bytes: 0, output_bytes: 0, io_source: 'process_stream',
    } }] });
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getByText('실제 스트림')).toBeTruthy();
    expect(within(group).getAllByText('0')).toHaveLength(2);
    expect(within(group).getAllByText('0 B')).toHaveLength(2);
    expect(within(group).queryByText('알 수 없음')).toBeNull();
  });

  it('합성 process_stream 변형은 실제 스트림 출처와 줄·바이트 단위를 표시한다', () => {
    show(liveOf(processing()));
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getByText('28,734')).toBeTruthy();
    expect(within(group).getByText('1,298')).toBeTruthy();
    expect(within(group).getByText('2.0 KB')).toBeTruthy();
    expect(within(group).getByText('2.5 MB')).toBeTruthy();
    expect(within(group).getByText('실제 스트림')).toBeTruthy();
  });

  it('합성 transcript 변형은 대화 기록량이며 PID가 없으면 모름으로 표시한다', () => {
    show(liveOf(recordGenerating()));
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getByText('대화 기록량')).toBeTruthy();
    expect(within(group).getByText('12.0 KB')).toBeTruthy();
    expect(within(group).getByText('90.0 KB')).toBeTruthy();
    expect(within(group).getAllByText('알 수 없음')).toHaveLength(1);
    expect(within(group).getByRole('button', { name: /대화 기록량은 특정 프로세스 PID에 귀속되지 않습니다/ })).toBeTruthy();
  });

  it('수집하지 않은 프로세스 값은 모두 알 수 없음으로 표시한다', () => {
    const base = flagshipFull().live;
    if (!base) throw new Error('flagship must have live data');
    show({ ...base, instances: [{ ...base.instances[0], process: undefined }] });
    const group = screen.getByRole('group', { name: '프로세스 상세' });
    expect(within(group).getAllByText('알 수 없음')).toHaveLength(6);
  });

  it('관리자는 주인 전용 프로세스와 수집기 상세를 보지 못한다', () => {
    const live = flagshipFull().live;
    if (!live) throw new Error('flagship must have live data');
    show(live, false);
    expect(screen.queryByRole('group', { name: '프로세스 상세' })).toBeNull();
    expect(screen.queryByText('수집기 신호')).toBeNull();
  });
});
