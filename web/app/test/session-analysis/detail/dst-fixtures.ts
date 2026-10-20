import type { AnalysisTurn } from '@/lib/session-analysis-types';
import { DST_DEMO } from '../calendar/dst-demo';
import { DETAIL_NOW, finalize, inputsOf, NOT_COLLECTED_COMMITS, NO_RECORD, ready, S, type DetailFixture } from './builders';

const minutesAgo = (iso: string): number => (DETAIL_NOW - Date.parse(iso)) / 60_000;

/** The calendar owns the absolute instants; never reconstruct them from a displayed local clock. */
const turnsFor = (session: (typeof DST_DEMO)[number]): AnalysisTurn[] => session.inputs.items.map((input, index) => {
  const next = session.inputs.items[index + 1];
  const segment = session.segments.find((item) => input.ts >= item.start && input.ts <= item.end);
  const latest = Math.min(
    Date.parse(segment?.end ?? input.ts),
    next ? Date.parse(next.ts) - 1 : Number.POSITIVE_INFINITY,
    Date.parse(input.ts) + 60_000,
  );
  const start = Date.parse(input.ts);
  const end = new Date(Math.max(start, latest)).toISOString();
  return {
    key: input.turn_key,
    no: index + 1,
    request_no: index + 1,
    branch: 'active',
    anchor_ts: input.ts,
    started_at: input.ts,
    ended_at: end,
    kind: 'typed',
    is_request: true,
    extra_inputs: 0,
    status: 'undeterminable',
    status_reason: 'pre_collect',
    preview: input.preview,
    images: 0,
    mode: '',
    denied: false,
    login_email: '',
    api_requests: 0,
    tokens: { input: 0, output: 0, cache_read: 0, cache_create: 0 },
    cost: { usd: null, source: 'none', reason: null },
    context: { tokens: null, window: null, percent: null, source: 'unknown' },
    tools: { calls: 0, subagents: 0 },
    question_headers: [],
    edited: { count: 0, sample: [], abandoned: 0 },
    prs: [],
    last_assistant_message_id: `msg_${input.turn_key}`,
    opener_copied: false,
    commits: [],
    tasks: [],
    work_ids: [],
  };
});

const fixtureFor = (session: (typeof DST_DEMO)[number]): DetailFixture => {
  const turns = turnsFor(session);
  const inputs = inputsOf(turns);
  const profile = 'dst-demo@example.com';
  const base = ready({
    session_id: session.session_id,
    agent: session.colors.agent,
    model: session.colors.model ?? '',
    branch: 'dst-demo',
    project: session.colors.project ?? { hash: '-demo-web-console', name: 'web-console' },
    login_emails: [profile],
    title: session.title,
    name: null,
    category: { key: 'inactive', as_of: session.range.end },
    turns,
    inputs,
    started_ago: minutesAgo(session.range.start),
    last_activity_ago: minutesAgo(session.range.end),
    cost_source: 'none',
    unattached_usd: null,
    subagents_running: null,
    active_seconds: null,
    tags: { change_area: { value: null, state: 'observed' }, task_type: null, rules_files: [] },
    plan_flags: { has_plan_file: false, used_plan_mode: false },
    signals: {
      turn: S.turnUnknown('pre_collect'),
      question: S.questionUnknown('pre_collect'),
      background: S.backgroundUnknown('pre_collect'),
      uncommitted: S.uncommittedUnknown('no_git'),
      todo: S.todoUnknown('no_list'),
    },
    outputs: { edited_files: { state: 'observed', active_total: 0, abandoned_total: 0, truncated: false, items: [] }, lines: null, prs: { items: [], truncated: false } },
    recaps: { state: 'pre_collect', items: [], absent_reason: 'pre_collect' },
  });
  return finalize({
    base,
    meta: { session_id: session.session_id, user_id: 'dst-demo', profile_email: profile, login_email: profile },
    label: `뉴욕 서머타임 전환 합성 세션: ${session.title.text}`,
    covers: ['전환일의 절대 UTC 입력 시각', '세션 기록 없음', '커밋 수집 여부 모름', '완료 판정 불가'],
    identities: { user_ids: ['dst-demo'], profile_emails: [profile], login_emails: [profile], mixed: false },
    alive: false,
    record: NO_RECORD,
    commits: NOT_COLLECTED_COMMITS,
    tasks: { items: [] },
    classification: 'unsorted',
  });
};

/** Seven calendar DST demonstrations with a navigable detail and matching conversation. */
export const DST_DETAIL_SESSIONS: DetailFixture[] = DST_DEMO.map(fixtureFor);
