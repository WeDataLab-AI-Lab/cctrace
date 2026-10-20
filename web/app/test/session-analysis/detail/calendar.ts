import type { CalendarSession, CalendarStatus } from '@/lib/session-calendar-types';
import { isFullAnalysis } from '@/components/sessions/analysis/guards';
import type { DetailFixture } from './fixtures';

/** 상세와 캘린더가 같은 합성 세션·턴·태그·판정을 읽는다. 입력 없는 시간에 새 활동을 지어내지 않는다. */
export const calendarOfDetail = (fixture: DetailFixture): CalendarSession | null => {
  const analysis = fixture.analysis;
  if (!analysis || !isFullAnalysis(analysis)) return null;
  const header = analysis.header;
  const record = analysis.record;
  const tags = record && (record.state === 'ready' || record.state === 'stale') ? record.tags : null;
  const judgement = analysis.judgement;
  let status: CalendarStatus | null = null;
  if (judgement?.status === 'running' || judgement?.status === 'interrupted') status = 'running';
  else if (judgement?.bucket === 'ended_midway') status = 'midway';
  else if (judgement?.bucket) status = judgement.bucket;
  const start = header.started_at ?? fixture.row.start_time;
  const end = header.last_activity_at ?? fixture.row.end_time;
  return {
    session_id: fixture.id,
    title: header.title,
    colors: {
      project: header.project,
      agent: header.agent,
      model: header.model || null,
      profile: fixture.row.profile_email || null,
      components: tags?.components ?? [],
      screens: tags?.screens ?? [],
      kinds: tags?.kinds ?? [],
      status,
    },
    requests: header.counts.requests,
    cost: { usd: header.cost.usd, source: header.cost.source },
    facts: analysis.state,
    range: { start, end },
    segments: analysis.turns.filter((turn) => turn.branch === 'active').map((turn) => ({
      start: turn.started_at,
      end: turn.ended_at ?? turn.started_at,
      records: turn.api_requests + analysis.inputs.filter((input) => input.turn_key === turn.key).length,
      human_inputs: analysis.inputs.filter((input) => input.turn_key === turn.key && (input.kind === 'typed' || input.kind === 'queued' || input.kind === 'unknown')).length,
    })),
    inputs: {
      state: header.counts.inputs === null ? 'undeterminable' : 'observed',
      items: analysis.inputs.map((input) => ({
        ts: input.ts,
        turn_key: input.turn_key,
        preview: analysis.turns.find((turn) => turn.key === input.turn_key)?.preview ?? { text: null, state: 'none' },
      })),
      truncated: analysis.truncated?.inputs ?? false,
    },
  };
};
