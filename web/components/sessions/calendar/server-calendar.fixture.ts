/**
 * `GET /api/session-calendar` 응답을 서버(`internal/store/postgres_session_calendar.go`)가 지금 직렬화하는 모양 그대로
 * 손으로 적은 시험 자료다. 합성 캘린더 빌더를 쓰지 않는다: 태그·프로필은 null, 사실이 없는 세션의 제목 출처는 null,
 * truncated 대체 사실의 제목 출처·상태와 비용 출처는 빈 문자열로 온다.
 */

const WEEK_FROM = '2026-10-04T15:00:00Z';
const WEEK_TO = '2026-10-11T15:00:00Z';

const overviewOf = (sessionId: string, start: string, end: string) => ({
  session_id: sessionId, profile_email: 'owner@example.test', user_id: 'owner-uid', login_email: 'login@example.test',
  account_count: 1, login_email_inferred: false, model: 'claude-test', agent: 'claude', start_time: start, end_time: end,
  input_tokens: 10, output_tokens: 20, cost_usd: 0.5, event_count: 4, has_sync: true, project_hash: 'p1',
  project_name: 'cctrace', entrypoint: 'cli', has_enriched: true,
});

const untaggedColors = (status: boolean) => ({
  project: { hash: 'p1', name: 'cctrace' }, agent: 'claude', model: 'claude-test', profile: null,
  components: null, screens: null, kinds: null, ...(status ? { status: null } : {}),
});

/** 서버 응답 하나. `status`는 주인 범위에서 세션 주인일 때만 `colors.status: null` 키가 생긴다. */
const serverCalendar = ({ ownerView = true, status = true, from = WEEK_FROM, to = WEEK_TO }: { ownerView?: boolean; status?: boolean; from?: string; to?: string } = {}) => ({
  from, to, owner_view: ownerView,
  sessions: [
    {
      session_id: 'ready-owned', title: { text: '관측 제목', source: 'ai', state: 'ok' }, colors: untaggedColors(status),
      requests: 3, cost: { usd: 1.25, source: 'otel' }, facts: 'ready',
      range: { start: '2026-10-05T05:00:00Z', end: '2026-10-05T07:00:00Z' },
      segments: [{ start: '2026-10-05T05:00:00Z', end: '2026-10-05T07:00:00Z', records: 12, human_inputs: 2 }],
      inputs: { state: 'observed', truncated: false, items: [
        { ts: '2026-10-05T05:10:00Z', turn_key: 't1', preview: { text: '첫 요청', state: 'ok' } },
        { ts: '2026-10-05T06:10:00Z', turn_key: 't1', preview: { text: null, state: 'none' } },
      ] },
      overview: overviewOf('ready-owned', '2026-10-05T05:00:00Z', '2026-10-05T07:00:00Z'),
    },
    {
      session_id: 'truncated-row', title: { text: null, source: '', state: '' }, colors: untaggedColors(false),
      requests: null, cost: { usd: null, source: '' }, facts: 'truncated',
      range: { start: '2026-10-06T01:00:00Z', end: '2026-10-06T03:00:00Z' }, segments: [],
      inputs: { state: 'undeterminable', items: [], truncated: false },
      overview: overviewOf('truncated-row', '2026-10-06T01:00:00Z', '2026-10-06T03:00:00Z'),
    },
    {
      session_id: 'building-row', title: { text: null, source: null, state: 'none' }, colors: untaggedColors(false),
      requests: null, cost: { usd: null, source: 'none' }, facts: 'building',
      range: { start: '2026-10-07T01:00:00Z', end: '2026-10-07T02:00:00Z' }, segments: [],
      inputs: { state: 'undeterminable', items: [], truncated: false },
      overview: overviewOf('building-row', '2026-10-07T01:00:00Z', '2026-10-07T02:00:00Z'),
    },
    {
      session_id: 'capped-row', title: { text: '입력이 많은 세션', source: 'first_request', state: 'ok' }, colors: untaggedColors(status),
      requests: 2400, cost: { usd: 3, source: 'estimate' }, facts: 'ready',
      range: { start: '2026-10-08T00:00:00Z', end: '2026-10-08T09:00:00Z' },
      segments: [{ start: '2026-10-08T00:00:00Z', end: '2026-10-08T09:00:00Z', records: 5000, human_inputs: 2400 }],
      inputs: { state: 'observed', truncated: true, items: Array.from({ length: 2000 }, (_, i) => ({
        ts: new Date(Date.parse('2026-10-08T00:00:00Z') + i * 15_000).toISOString(), turn_key: `t${i}`,
        preview: { text: null, state: 'none' },
      })) },
      overview: overviewOf('capped-row', '2026-10-08T00:00:00Z', '2026-10-08T09:00:00Z'),
    },
  ],
});

export { WEEK_FROM, WEEK_TO, serverCalendar };
