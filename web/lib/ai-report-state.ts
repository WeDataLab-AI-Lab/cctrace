import { POLL_LIVE } from './query-config';
import type { AIReport, AIReportsResponse } from './types';

/** What occupies the report's place (spec §3.4). */
type ReportPanelState =
  | 'disabled'
  | 'unconfigured'
  | 'unavailable'
  | 'consent_required'
  | 'no_records'
  | 'not_generated'
  | 'running'
  | 'failed'
  | 'completed';

interface ReportPanelView {
  state: ReportPanelState;
  /** The report to draw: the current one when completed, the earlier one kept
   *  under a failure banner, otherwise null. */
  report: AIReport | null;
}

/** The single place the §3.4 priority is decided. The server returns facts only;
 *  the first matching row wins. */
const reportPanelState = (data: AIReportsResponse): ReportPanelView => {
  const none = (state: ReportPanelState): ReportPanelView => ({ state, report: null });

  // Switching reports off cancels the runs already going, so a run still
  // marked running is on its way to canceled and gets no stop button.
  if (!data.runtime.enabled) return none('disabled');
  if (!data.runtime.configured) return none('unconfigured');
  // Ahead of §3.4's table order: a run already started keeps its progress and
  // stop button through a cached runtime blip or a disclosure change mid-run.
  if (data.run?.status === 'running') return none('running');
  if (!data.runtime.available) return none('unavailable');
  if (!data.consent.granted) return none('consent_required');
  if (data.segment_count === 0) return none('no_records');
  if (data.run?.status === 'failed') return { state: 'failed', report: data.report };
  if (data.report === null) return none('not_generated');
  return { state: 'completed', report: data.report };
};

const aiReportQueryKey = (userId: number | null, weekId: string, timeZone: string) =>
  ['ai-report', userId, weekId, timeZone] as const;

/** No polling by default (spec §3.6). A run whose stream failed is polled at
 *  POLL_LIVE until it stops running; a newer run gets its own stream first. */
const aiReportPollInterval = (data: AIReportsResponse | undefined, streamFailedRunId: number | null): number | false => {
  const run = data?.run;
  if (!run || run.status !== 'running') return false;
  return run.id === streamFailedRunId ? POLL_LIVE : false;
};

export { aiReportPollInterval, aiReportQueryKey, reportPanelState };
export type { ReportPanelState, ReportPanelView };
