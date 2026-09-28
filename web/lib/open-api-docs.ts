type OpenAPILocale = 'en' | 'ko';

interface LocalizedText {
  en: string;
  ko: string;
}

interface ManualSection {
  id: string;
  title: string;
}

interface ManualLocale {
  label: string;
  title: string;
  subtitle: string;
  tokenFacts: string[];
  sections: ManualSection[];
}

interface EndpointParameter {
  name: string;
  type: string;
  required?: boolean;
  description: LocalizedText;
}

interface EndpointField {
  name: string;
  type: string;
  description: LocalizedText;
}

interface OpenAPIEndpoint {
  method: 'GET';
  path: string;
  title: LocalizedText;
  description: LocalizedText;
  parameters: EndpointParameter[];
  fields: EndpointField[];
}

const OPEN_API_MANUAL: Record<OpenAPILocale, ManualLocale> = {
  en: {
    label: 'English',
    title: 'Open API Manual',
    subtitle: 'Get a token, make your first request, and integrate with the read-only cctrace API.',
    tokenFacts: ['Settings → API Access Tokens', 'cctrace auth read', 'POST /api/cli/read-token', 'server.read_token', 'Authorization: Bearer cct_...'],
    sections: [
      { id: 'getting-started', title: 'Getting started' },
      { id: 'authentication', title: 'Authentication' },
      { id: 'usage', title: 'Usage examples' },
      { id: 'reference', title: 'API reference' },
      { id: 'errors', title: 'Errors and rate limits' },
      { id: 'security', title: 'Security and data scope' },
      { id: 'troubleshooting', title: 'Troubleshooting' },
    ],
  },
  ko: {
    label: '한국어',
    title: 'Open API 매뉴얼',
    subtitle: '토큰 발급부터 첫 요청, 읽기 전용 cctrace API 연동까지 한 번에 확인.',
    tokenFacts: ['Settings → API Access Tokens', 'cctrace auth read', 'POST /api/cli/read-token', 'server.read_token', 'Authorization: Bearer cct_...'],
    sections: [
      { id: 'getting-started', title: '시작하기' },
      { id: 'authentication', title: '인증' },
      { id: 'usage', title: '사용 예시' },
      { id: 'reference', title: 'API 레퍼런스' },
      { id: 'errors', title: '오류와 속도 제한' },
      { id: 'security', title: '보안과 데이터 범위' },
      { id: 'troubleshooting', title: '문제 해결' },
    ],
  },
};

const commonTimeParameters: EndpointParameter[] = [
  {
    name: 'since',
    type: 'string (RFC3339)',
    description: { en: 'Inclusive lower time boundary.', ko: '조회 시작 시각. 해당 시각 포함.' },
  },
  {
    name: 'until',
    type: 'string (RFC3339)',
    description: { en: 'Exclusive upper time boundary.', ko: '조회 종료 시각. 해당 시각 미포함.' },
  },
];

const projectParameters: EndpointParameter[] = [
  {
    name: 'project_hash',
    type: 'string (CSV)',
    description: { en: 'One or more comma-separated project hashes.', ko: '쉼표로 구분한 하나 이상의 프로젝트 해시.' },
  },
  {
    name: 'project_hashes',
    type: 'string (CSV)',
    description: { en: 'Alias accepted alongside project_hash.', ko: 'project_hash와 함께 지원하는 별칭.' },
  },
];

const ownerParameters: EndpointParameter[] = [
  {
    name: 'profile_email',
    type: 'string',
    description: { en: 'Admin-only owner filter; ignored and replaced for regular users.', ko: '관리자 전용 소유자 필터. 일반 사용자는 본인 범위로 강제.' },
  },
  {
    name: 'login_email',
    type: 'string',
    description: { en: 'Admin-only login email filter.', ko: '관리자 전용 로그인 이메일 필터.' },
  },
  {
    name: 'user_id',
    type: 'string',
    description: { en: 'Admin-only cctrace user ID filter.', ko: '관리자 전용 cctrace 사용자 ID 필터.' },
  },
];

const pagingParameters: EndpointParameter[] = [
  {
    name: 'limit',
    type: 'integer',
    description: { en: 'Page size. Default 100; session-record pages are capped at 1,000.', ko: '페이지 크기. 기본값 100, 세션 레코드는 최대 1,000.' },
  },
  {
    name: 'offset',
    type: 'integer',
    description: { en: 'Zero-based page offset. Default 0.', ko: '0부터 시작하는 페이지 오프셋. 기본값 0.' },
  },
];

const OPEN_API_ENDPOINTS: OpenAPIEndpoint[] = [
  {
    method: 'GET',
    path: '/api/open/v1/sessions',
    title: { en: 'List sessions', ko: '세션 목록' },
    description: {
      en: 'Returns summarized sessions ordered by recent activity. Use limit and offset for pagination.',
      ko: '최근 활동 순으로 요약 세션을 반환. limit과 offset으로 페이지 이동.',
    },
    parameters: [...commonTimeParameters, ...projectParameters, ...ownerParameters, ...pagingParameters],
    fields: [
      { name: 'session_id', type: 'string', description: { en: 'Session identifier.', ko: '세션 식별자.' } },
      { name: 'user_id', type: 'string?', description: { en: 'cctrace user identifier.', ko: 'cctrace 사용자 식별자.' } },
      { name: 'model', type: 'string?', description: { en: 'Primary model label.', ko: '대표 모델 라벨.' } },
      { name: 'agent', type: 'string?', description: { en: 'Agent family such as claude or codex.', ko: 'claude, codex 등의 에이전트 계열.' } },
      { name: 'project_name', type: 'string?', description: { en: 'Project name as /projects lists it; the path-derived hash is not included.', ko: '/projects 와 같은 프로젝트 이름. 경로에서 만든 해시는 미포함.' } },
      { name: 'start_time / end_time', type: 'RFC3339', description: { en: 'Observed session boundaries.', ko: '관측된 세션 시작·종료 시각.' } },
      { name: 'input_tokens / output_tokens', type: 'int64', description: { en: 'Aggregated token totals.', ko: '집계 입력·출력 토큰.' } },
      { name: 'cost_usd', type: 'number', description: { en: 'Aggregated USD cost.', ko: '집계 USD 비용.' } },
      { name: 'event_count', type: 'int64', description: { en: 'Number of matching events.', ko: '일치 이벤트 수.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/sessions/{session_id}',
    title: { en: 'Get session records', ko: '세션 레코드 조회' },
    description: {
      en: 'Returns sanitized records for one session. Raw prompts, command bodies, paths, and internal attributes are excluded.',
      ko: '단일 세션의 정제된 레코드 반환. 원문 프롬프트, 명령 본문, 경로, 내부 속성 제외.',
    },
    parameters: [
      { name: 'session_id', type: 'string (path)', required: true, description: { en: 'Session identifier from the list endpoint.', ko: '세션 목록에서 받은 세션 식별자.' } },
      { name: 'profile_email', type: 'string', description: { en: 'Admin-only owner filter.', ko: '관리자 전용 소유자 필터.' } },
      { name: 'user_id', type: 'string', description: { en: 'Admin-only cctrace user ID filter.', ko: '관리자 전용 cctrace 사용자 ID 필터.' } },
      ...pagingParameters,
      { name: 'order', type: 'asc | desc', description: { en: 'Record order. Default desc.', ko: '레코드 정렬. 기본값 desc.' } },
    ],
    fields: [
      { name: 'ts', type: 'RFC3339', description: { en: 'Record timestamp.', ko: '레코드 시각.' } },
      { name: 'session_id', type: 'string?', description: { en: 'Session identifier.', ko: '세션 식별자.' } },
      { name: 'record_type', type: 'string', description: { en: 'Sanitized record category.', ko: '정제된 레코드 유형.' } },
      { name: 'model', type: 'string?', description: { en: 'Model label.', ko: '모델 라벨.' } },
      { name: 'input_tokens / output_tokens', type: 'integer?', description: { en: 'Token counts when present.', ko: '존재하는 경우 입력·출력 토큰.' } },
      { name: 'cache_read_tokens / cache_create_tokens', type: 'integer?', description: { en: 'Cache token counts when present.', ko: '존재하는 경우 캐시 토큰.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/tools',
    title: { en: 'List tool usage', ko: '도구 사용량 목록' },
    description: {
      en: 'Returns success and failure totals grouped by tool for a time range. The default range is the last seven days.',
      ko: '시간 범위의 도구별 성공·실패 합계 반환. 기본 범위는 최근 7일.',
    },
    parameters: [...commonTimeParameters, ...ownerParameters],
    fields: [
      { name: 'tool_name', type: 'string', description: { en: 'Observed tool name.', ko: '관측된 도구 이름.' } },
      { name: 'use_count', type: 'int64', description: { en: 'Total result count.', ko: '전체 실행 결과 수.' } },
      { name: 'success_count / fail_count', type: 'int64', description: { en: 'Successful and failed result totals.', ko: '성공·실패 결과 합계.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/tools/{tool_name}',
    title: { en: 'Get tool detail', ko: '도구 상세 조회' },
    description: {
      en: 'Returns a success/failure time series and sanitized recent failures for one tool.',
      ko: '단일 도구의 성공·실패 시계열과 정제된 최근 실패 목록 반환.',
    },
    parameters: [
      { name: 'tool_name', type: 'string (path)', required: true, description: { en: 'URL-encoded tool name.', ko: 'URL 인코딩된 도구 이름.' } },
      ...commonTimeParameters,
      ...ownerParameters,
      { name: 'granularity', type: 'minute | hour | day | week | month', description: { en: 'Time-series bucket size. Default day.', ko: '시계열 버킷 크기. 기본값 day.' } },
      { name: 'failure_limit', type: 'integer', description: { en: 'Recent failure count. Default 50.', ko: '최근 실패 항목 수. 기본값 50.' } },
    ],
    fields: [
      { name: 'tool_name', type: 'string', description: { en: 'Requested tool name.', ko: '요청한 도구 이름.' } },
      { name: 'timeseries[]', type: 'array', description: { en: 'date, success_count, and fail_count buckets.', ko: 'date, success_count, fail_count 버킷.' } },
      { name: 'failures[]', type: 'array', description: { en: 'Timestamp, session ID, model, and optional duration only.', ko: '시각, 세션 ID, 모델, 선택적 실행 시간만 포함.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/plugins',
    title: { en: 'List plugin usage', ko: '플러그인 사용량 목록' },
    description: {
      en: 'Returns slash-command invocation and token totals without owner or repository metadata.',
      ko: '소유자·저장소 메타데이터를 제외한 슬래시 명령 호출·토큰 합계 반환.',
    },
    parameters: [
      ...commonTimeParameters,
      ...ownerParameters,
      { name: 'agent', type: 'string', description: { en: 'Optional agent filter.', ko: '선택 에이전트 필터.' } },
    ],
    fields: [
      { name: 'command_name / agent', type: 'string', description: { en: 'Plugin command and agent.', ko: '플러그인 명령과 에이전트.' } },
      { name: 'invocation_count', type: 'int64', description: { en: 'Invocation total.', ko: '호출 합계.' } },
      { name: 'total_tokens / input_tokens / output_tokens', type: 'int64', description: { en: 'Attributed token totals.', ko: '귀속 토큰 합계.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/skills',
    title: { en: 'List skill usage', ko: '스킬 사용량 목록' },
    description: {
      en: 'Returns explicit and implicit skill outcome totals without owner or repository metadata.',
      ko: '소유자·저장소 메타데이터를 제외한 명시·암시 스킬 결과 합계 반환.',
    },
    parameters: [
      ...commonTimeParameters,
      ...ownerParameters,
      { name: 'agent', type: 'string', description: { en: 'Optional agent filter.', ko: '선택 에이전트 필터.' } },
    ],
    fields: [
      { name: 'skill_name / agent / invoke_type', type: 'string', description: { en: 'Skill, agent, and explicit/implicit invocation type.', ko: '스킬, 에이전트, 명시·암시 호출 유형.' } },
      { name: 'success_count / fail_count / total_count', type: 'int64', description: { en: 'Outcome totals.', ko: '실행 결과 합계.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/rules',
    title: { en: 'List rules', ko: '규칙 목록' },
    description: {
      en: 'Returns paginated rule metadata. Repository identifiers, paths, hashes, and content are excluded.',
      ko: '페이지 기반 규칙 메타데이터 반환. 저장소 식별자·경로·해시·본문 제외.',
    },
    parameters: [
      { name: 'agent', type: 'string', description: { en: 'Optional agent filter.', ko: '선택 에이전트 필터.' } },
      { name: 'status', type: 'string', description: { en: 'Rule status filter.', ko: '규칙 상태 필터.' } },
      { name: 'query', type: 'string', description: { en: 'Rule metadata search text.', ko: '규칙 메타데이터 검색어.' } },
      ...pagingParameters,
    ],
    fields: [
      { name: 'items[]', type: 'array', description: { en: 'Sanitized rule metadata and version/comment counts.', ko: '정제된 규칙 메타데이터와 버전·댓글 수.' } },
      { name: 'total', type: 'int64', description: { en: 'Total matching rules.', ko: '전체 일치 규칙 수.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/rules/{rule_id}',
    title: { en: 'Get rule detail', ko: '규칙 상세 조회' },
    description: {
      en: 'Returns sanitized rule and version metadata. Rule bodies, comments, repository data, commits, branches, and author identity are excluded.',
      ko: '정제된 규칙·버전 메타데이터 반환. 규칙 본문, 댓글, 저장소 정보, 커밋, 브랜치, 작성자 정보 제외.',
    },
    parameters: [
      { name: 'rule_id', type: 'integer (path)', required: true, description: { en: 'Rule ID from the list endpoint.', ko: '목록 엔드포인트의 규칙 ID.' } },
    ],
    fields: [
      { name: 'rule', type: 'object', description: { en: 'Sanitized rule metadata.', ko: '정제된 규칙 메타데이터.' } },
      { name: 'versions[]', type: 'array', description: { en: 'Version number, size, reason, applies-to patterns, and discovery time.', ko: '버전 번호, 크기, 변경 이유, 적용 패턴, 발견 시각.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/events',
    title: { en: 'List events', ko: '이벤트 목록' },
    description: {
      en: 'Returns sanitized telemetry events, newest first.',
      ko: '정제된 텔레메트리 이벤트를 최신 순으로 반환.',
    },
    parameters: [
      ...commonTimeParameters,
      { name: 'project_hash', type: 'string', description: { en: 'Single project hash.', ko: '단일 프로젝트 해시.' } },
      ...ownerParameters,
      { name: 'session_id', type: 'string', description: { en: 'Exact session filter.', ko: '정확한 세션 필터.' } },
      ...pagingParameters,
    ],
    fields: [
      { name: 'ts / event_name', type: 'RFC3339 / string', description: { en: 'Event time and category.', ko: '이벤트 시각과 유형.' } },
      { name: 'session_id / user_id', type: 'string?', description: { en: 'Session and user identifiers.', ko: '세션·사용자 식별자.' } },
      { name: 'model / speed', type: 'string?', description: { en: 'Model and speed tier.', ko: '모델과 속도 등급.' } },
      { name: 'cost_usd', type: 'number?', description: { en: 'Event cost in USD.', ko: '이벤트 USD 비용.' } },
      { name: 'input_tokens / output_tokens', type: 'integer?', description: { en: 'Event token counts.', ko: '이벤트 토큰 수.' } },
      { name: 'cache_read_tokens / cache_create_tokens', type: 'integer?', description: { en: 'Cache token counts.', ko: '캐시 토큰 수.' } },
      { name: 'duration_ms', type: 'integer?', description: { en: 'Observed duration in milliseconds.', ko: '관측 시간(밀리초).' } },
      { name: 'tool_name / tool_decision / tool_success', type: 'mixed?', description: { en: 'Sanitized tool outcome fields.', ko: '정제된 도구 실행 결과 필드.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/metrics',
    title: { en: 'List metrics', ko: '메트릭 목록' },
    description: {
      en: 'Returns sanitized telemetry metric points, newest first. Dimensions, owner identity, teams, and billing metadata are excluded.',
      ko: '정제된 텔레메트리 메트릭을 최신 순으로 반환. 차원, 소유자 정보, 팀, 결제 메타데이터 제외.',
    },
    parameters: [
      ...commonTimeParameters,
      ...ownerParameters,
      { name: 'metric_name', type: 'string', description: { en: 'Exact metric-name filter.', ko: '정확한 메트릭 이름 필터.' } },
      { name: 'agent', type: 'string', description: { en: 'Exact agent filter.', ko: '정확한 에이전트 필터.' } },
      { name: 'model', type: 'string', description: { en: 'Partial model-name filter.', ko: '부분 모델 이름 필터.' } },
      ...pagingParameters,
    ],
    fields: [
      { name: 'ts / metric_name', type: 'RFC3339 / string', description: { en: 'Metric time and name.', ko: '메트릭 시각과 이름.' } },
      { name: 'session_id / user_id / model / agent', type: 'string?', description: { en: 'Sanitized context identifiers.', ko: '정제된 맥락 식별자.' } },
      { name: 'value_double / value_int', type: 'number?', description: { en: 'Metric value fields when present.', ko: '존재하는 메트릭 값 필드.' } },
    ],
  },
  {
    method: 'GET',
    path: '/api/open/v1/usage',
    title: { en: 'Get usage totals', ko: '사용량 집계' },
    description: {
      en: 'Returns session, cost, token, work-time, and per-model totals for the selected scope. since and until are both required. Project filters are refused with 400 unsupported_filter because cost is aggregated over a source without project_hash. With group_by, returns one row per user, team, or model instead.',
      ko: '선택 범위의 세션·비용·토큰·작업 시간·모델별 합계 반환. since와 until 모두 필수. 비용 집계 원천에 project_hash가 없어 프로젝트 필터는 400 unsupported_filter로 거부. group_by 지정 시 사용자·팀·모델별 행 목록 반환.',
    },
    parameters: [
      ...commonTimeParameters,
      ...ownerParameters,
      {
        name: 'group_by',
        type: 'user | team | model',
        description: { en: 'Optional grouping; the user key is the profile email.', ko: '선택 그룹 기준. user의 key는 프로필 이메일.' },
      },
    ],
    fields: [
      { name: 'session_count', type: 'int64', description: { en: 'Distinct matching sessions.', ko: '일치하는 고유 세션 수.' } },
      { name: 'cost_usd', type: 'number', description: { en: 'Cost summed per request over the same window and scope.', ko: '같은 구간·범위의 요청 단위 비용 합계.' } },
      { name: 'input_tokens / output_tokens / total_tokens', type: 'int64', description: { en: 'Overall token totals.', ko: '전체 토큰 합계.' } },
      { name: 'work_time_seconds', type: 'int64', description: { en: 'Sum of each session elapsed time; overlapping sessions remain additive.', ko: '세션별 경과시간 합계. 동시 세션도 각각 합산.' } },
      { name: 'by_model[]', type: 'array', description: { en: 'Input, output, and total tokens grouped by model.', ko: '모델별 입력·출력·전체 토큰.' } },
      { name: 'items[] / total (group_by)', type: 'object', description: { en: 'Rows of key, cost_usd, input/output/total tokens, and request_count.', ko: 'key, cost_usd, 입력·출력·전체 토큰, request_count 행 목록.' } },
    ],
  },
];

export { OPEN_API_ENDPOINTS, OPEN_API_MANUAL };
export type { OpenAPIEndpoint, OpenAPILocale };
