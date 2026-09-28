# cctrace Open API Guide / Open API 사용 가이드

Read-only API for external automation over cctrace sessions, tools, plugins, skills, rules, logs, and usage.

cctrace 세션·도구·플러그인·스킬·규칙·로그·사용량의 외부 자동화용 읽기 전용 API.

## Documentation surfaces / 문서 경로

- In-app bilingual manual / 앱 내 영문·한글 매뉴얼: `/open-api`
- OpenAPI 3.1 specification / OpenAPI 3.1 명세: [`docs/api/openapi.yaml`](../api/openapi.yaml)
- Base path / 기본 경로: `/api/open/v1`
- Format / 형식: REST + JSON

## Token creation / 토큰 생성

Recommended browser flow / 권장 브라우저 흐름:

1. Dashboard `Settings` / 대시보드 `Settings`
2. `API Access Tokens`
3. `Create token` + integration-specific name / `Create token` + 연동별 이름
4. Immediate copy to a secret manager or protected environment variable / 시크릿 관리자 또는 보호된 환경 변수에 즉시 복사

Existing token values remain hidden. Create response display limited to one time.

기존 토큰 값 미표시. 생성 응답은 한 번만 노출.

Multiple-token metadata / 복수 토큰 메타데이터:

- Independent name and revocation boundary per integration / 연동별 독립 이름·폐기 경계
- Original creation timestamp / 최초 생성 시각
- Latest rotation timestamp, or `Never` / 최근 회전 시각 또는 `Never`
- Purpose: `Read API` (Settings), `Read API (CLI)` (`cctrace auth read`), `CLI ingestion` (the upload token `cctrace init` stores) / 용도: `Read API`(Settings), `Read API (CLI)`(`cctrace auth read`), `CLI ingestion`(`cctrace init` 이 저장하는 업로드 토큰)
- Mode: `Active` or `Inactive` / 모드: `Active` 또는 `Inactive`
- Expiration: `Unlimited` or a user-selected date and time / 만료: `Unlimited` 또는 사용자 지정 날짜·시각

Inactive and expired tokens fail authentication. Reactivating an expired token does not bypass its expiration; assign a future expiration or switch it to `Unlimited` as well.

비활성 또는 만료된 토큰은 인증에 실패. 만료된 토큰을 활성화해도 만료 제한은 유지되므로, 미래 만료 시각을 지정하거나 `Unlimited`로 변경해야 함.

Optional CLI flow for sync and OTEL setup / sync·OTEL 설정용 선택적 CLI 흐름:

```bash
cctrace init
cctrace status
```

`cctrace init` authentication inputs / 인증 입력:

- cctraced HTTP endpoint / cctraced HTTP 엔드포인트
- cctrace user ID / cctrace 사용자 ID
- Dashboard password / 대시보드 비밀번호

Successful CLI authentication issues or reuses the same per-user `cct_...` token and stores it as `server.auth_token`. That token is for uploading and this API refuses it.

CLI 인증 성공 시 동일한 사용자별 `cct_...` 토큰을 신규 발급하거나 기존 토큰 재사용 후 `server.auth_token`에 저장. 업로드용 토큰이라 이 API 는 거부.

CLI read token / CLI 읽기 토큰:

- Offered by `cctrace init` right after authentication, or created later with `cctrace auth read` (password re-entry) / `cctrace init` 인증 직후 제안, 또는 `cctrace auth read` 로 나중에 발급 (비밀번호 재입력)
- Stored as `server.read_token`; read commands prefer it over `server.auth_token` / `server.read_token` 에 저장, 읽기 명령은 `server.auth_token` 보다 우선 사용
- Listed in Settings as `CLI read token (<host>)`; running it again replaces the token this profile holds, and no other / Settings 에 `CLI read token (<호스트>)` 로 표시, 재실행 시 이 프로필이 가진 토큰만 교체
- Refused for administrators, whose tokens read every user's data; they create one in Settings. A CLI read token also stops working if its owner later becomes an administrator / 전체 사용자 데이터를 읽는 관리자는 거부, Settings 에서 발급. 소유자가 나중에 관리자가 되면 CLI 읽기 토큰도 사용 중단
- Scope note: ingestion refuses read tokens; the Open API and the dashboard's own read endpoints (GET/HEAD with a bearer token) both refuse upload tokens (`ingestion_token`) and administrators' CLI read tokens (`admin_read_token_forbidden`) under the same rule. Browser sessions use the login cookie and are unaffected / 범위 참고: 수집은 읽기 토큰 거부. Open API 와 대시보드 자체 조회 엔드포인트(Bearer 토큰 GET/HEAD)는 같은 규칙으로 업로드 토큰(`ingestion_token`)과 관리자 소유 CLI 읽기 토큰(`admin_read_token_forbidden`) 거부. 브라우저 세션은 로그인 쿠키 인증이라 영향 없음

Profile locations / 프로필 위치:

- Default / 기본: `~/.cctrace/profile.json`
- Named / 명명: `~/.cctrace/profiles/<name>/profile.json`
- File permission / 파일 권한: `0600`

Safe environment loading / 안전한 환경 변수 로드:

```bash
export CCTRACE_API_URL="https://cctrace.example.com"
export CCTRACE_API_TOKEN="$(
  jq -r '.server.read_token // empty' "${HOME}/.cctrace/profile.json"
)"
```

Direct issuance for automated CLI provisioning / 자동화된 CLI 프로비저닝용 직접 발급:

```http
POST /api/cli/auth
Content-Type: application/json

{
  "user_id": "alice",
  "password": "current-password"
}
```

Response field / 응답 필드: `api_token`

Password placement in URLs, shell history, or logs prohibited / URL·셸 기록·로그 내 비밀번호 삽입 금지.

Interactive user preference: Settings-based issuance / 대화형 사용자 권장: Settings 기반 발급.

## Authentication / 인증

```http
Authorization: Bearer cct_...
```

- Active per-user API token only / 활성 사용자별 API 토큰만 허용
- Dashboard JWT cookie unsupported / 대시보드 JWT 쿠키 미지원
- Global ingestion `API_KEY` rejected / 전역 수집용 `API_KEY` 거부
- User or admin role required / user 또는 admin 역할 필요
- Unlimited or user-selected expiration / 무제한 또는 사용자 지정 만료
- Active/inactive mode per token / 토큰별 활성·비활성 모드
- Self-service rotation and revocation in Settings / Settings 내 사용자 직접 회전·폐기
- Immediate invalidation of the previous token after rotation / 회전 직후 이전 토큰 즉시 무효화
- Continued validity of unselected tokens / 선택하지 않은 나머지 토큰의 활성 유지

## First request / 첫 요청

```bash
curl --fail --silent --show-error \
  -H "Authorization: Bearer ${CCTRACE_API_TOKEN}" \
  "${CCTRACE_API_URL}/api/open/v1/usage?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z"
```

## CLI client / CLI 클라이언트

The `cctrace` CLI reads this API, so it breaks before external users do when the
contract changes. Every command below reads with `server.read_token` (see
`cctrace auth read` above), or `server.auth_token` when no read token is stored.

`cctrace` CLI 가 이 API 를 읽는다. 계약이 깨지면 외부 사용자보다 우리가 먼저 안다.
아래 명령은 모두 `server.read_token`(위 `cctrace auth read`)을, 없으면 `server.auth_token` 을 사용.

```bash
cctrace auth read [--profile NAME]
cctrace usage    --since 7d [--until 7d] [--group-by user|team|model] [--json]
cctrace ls       --since 7d [--project HASH] [--limit N] [--json]
cctrace events   --session SESSION_ID [--json]
cctrace projects [--json]
cctrace tools    --since 7d [--json]
cctrace plugins  --since 7d [--agent AGENT] [--json]
cctrace skills   --since 7d [--agent AGENT] [--json]
cctrace rules    [--agent AGENT] [--status STATUS] [--query TEXT] [--limit N] [--json]
cctrace organization-insights --since 7d [--json]
cctrace insights cost    --since 7d [--limit N] [--profile NAME] [--json]
cctrace insights context --since 7d [--project HASH] [--limit N] [--profile NAME] [--json]
```

`usage --since 14d --until 7d` selects the completed seven-day window immediately before the current seven days.

`organization-insights` requires an admin token. It returns instance-wide aggregate models and tools only after at least five distinct users contributed; it is not an organization tenancy boundary.

`cctrace projects` prints the `project_hash` values that `--project` and the
`project_hash` query parameter accept.

`cctrace projects` 는 `--project` 와 `project_hash` 파라미터가 받는 해시를 출력한다.

## Endpoint summary / 엔드포인트 요약

| Method | Path | English | 한국어 |
| --- | --- | --- | --- |
| `GET` | `/api/open/v1/sessions` | Paginated session summaries | 페이지 기반 세션 요약 |
| `GET` | `/api/open/v1/sessions/{session_id}` | Sanitized session records | 정제된 세션 레코드 |
| `GET` | `/api/open/v1/tools` | Tool success and failure summaries | 도구 성공·실패 요약 |
| `GET` | `/api/open/v1/tools/{tool_name}` | Tool time series and sanitized failures | 도구 시계열·정제 실패 목록 |
| `GET` | `/api/open/v1/projects` | Projects the project_hash filter accepts | `project_hash` 필터에 넣을 수 있는 프로젝트 목록 |
| `GET` | `/api/open/v1/plugins` | Plugin invocation and token totals | 플러그인 호출·토큰 합계 |
| `GET` | `/api/open/v1/skills` | Skill outcome totals | 스킬 실행 결과 합계 |
| `GET` | `/api/open/v1/rules` | Paginated sanitized rule metadata | 페이지 기반 정제 규칙 메타데이터 |
| `GET` | `/api/open/v1/rules/{rule_id}` | Sanitized rule version metadata | 정제 규칙 버전 메타데이터 |
| `GET` | `/api/open/v1/events` | Time-ranged telemetry events | 시간 범위 텔레메트리 이벤트 |
| `GET` | `/api/open/v1/metrics` | Time-ranged telemetry metrics | 시간 범위 텔레메트리 메트릭 |
| `GET` | `/api/open/v1/usage` | Session, cost, token, work-time, and model totals; grouped with `group_by` | 세션·비용·토큰·작업 시간·모델 합계, `group_by` 로 그룹별 |
| `GET` | `/api/open/v1/organization-insights` | Admin-only privacy-preserving instance aggregate | 관리자 전용 개인정보 보호 인스턴스 집계 |

Full parameter, response-field, and example reference in `/open-api` and [`docs/api/openapi.yaml`](../api/openapi.yaml).

전체 파라미터·응답 필드·예제 레퍼런스는 `/open-api`와 [`docs/api/openapi.yaml`](../api/openapi.yaml) 참조.

## Query conventions / 쿼리 규칙

- RFC3339 `since` inclusive / RFC3339 `since` 포함
- RFC3339 `until` exclusive / RFC3339 `until` 미포함
- List default `limit=100`, `offset=0` / 목록 기본값 `limit=100`, `offset=0`
- Session-record maximum page size 1,000 / 세션 레코드 최대 페이지 크기 1,000
- Comma-separated project hashes through `project_hash` or `project_hashes` where supported / 지원 엔드포인트의 `project_hash` 또는 `project_hashes` 쉼표 구분
- Bare JSON arrays for sessions, tools, plugins, skills, events, and metrics / 세션·도구·플러그인·스킬·이벤트·메트릭 목록의 JSON 배열 응답
- Paginated `{items, total}` object for rules / 규칙 목록의 `{items, total}` 페이지 객체
- Single JSON objects for tool detail, rule detail, and usage / 도구 상세·규칙 상세·사용량의 단일 JSON 객체
- Seven-day default time range for tools, plugins, and skills / 도구·플러그인·스킬의 최근 7일 기본 시간 범위

## Data scope / 데이터 범위

Regular user / 일반 사용자:

- Token-owner scope enforcement / 토큰 소유자 범위 강제
- Requested `profile_email`, `login_email`, and `user_id` replacement / 요청 소유자 필터를 서버에서 본인 값으로 교체

Admin / 관리자:

- All visible data access / 전체 가시 데이터 접근
- Optional owner filters / 선택 소유자 필터

Sanitized response boundary / 정제 응답 경계:

- Raw prompt and command bodies excluded / 원문 프롬프트·명령 본문 제외
- Tool input and output excluded; session records expose only `tool_name` and `tool_call_id` / 도구 입력·출력 제외, 세션 레코드는 `tool_name`·`tool_call_id`만 노출

Session record kinds / 세션 레코드 종류:

- `record_type` is each agent's own spelling / `record_type`은 에이전트별 원본 값
- `kind` is how the conversation view shows the record (`message`, `tool_call`, `tool_result`, `reasoning`, `hidden`), not a tool-call type: Claude, omo and gjc calls sit inside `message` records / `kind`는 대화 뷰 표시 분류이며 도구 호출 유형이 아님, Claude·omo·gjc 호출은 `message` 레코드 안에 있음
- Count tool calls across agents by summing `tool_call_count`; hidden duplicates carry 0 / 에이전트 공통 도구 호출 수는 `tool_call_count` 합계, 숨김 중복 행은 0
- Repository paths and source files excluded / 저장소 경로·소스 파일 제외
- Emails and teams excluded, except as the grouping key of `usage?group_by=user|team` / 이메일·팀 제외 (`usage?group_by=user|team`의 그룹 키는 예외)
- Internal attributes excluded / 내부 속성 제외
- Rule bodies, comments, repository identifiers, commits, branches, and author identity excluded / 규칙 본문·댓글·저장소 식별자·커밋·브랜치·작성자 정보 제외

## Usage semantics / 사용량 계산

- `total_tokens = input_tokens + output_tokens`
- Cache-read/create tokens excluded from usage totals / 캐시 읽기·생성 토큰의 사용량 합계 제외
- `work_time_seconds`: sum of each session's first-to-last matching record duration / 세션별 첫·마지막 일치 레코드 경과시간 합계
- Additive overlapping sessions / 동시 세션 각각 합산
- Per-model token attribution with one overall session/work-time count / 모델별 토큰 배분 + 전체 세션·작업 시간 단일 계산

## Rate limit and retries / 속도 제한과 재시도

- Per-token-owner limiter: 20 requests/second / 토큰 소유자별 제한기: 초당 20회
- Burst capacity: 50 / 버스트: 50회
- Instance-wide backstop in front of authentication: 500 requests/second, burst 1,250 / 인증 앞단의 인스턴스 전체 상한: 초당 500회, 버스트 1,250회
- `429` response with `Retry-After` / `Retry-After` 포함 `429` 응답
- Retry target: `429` and transient `5xx` only / `429`와 일시적 `5xx`만 재시도
- Exponential backoff + jitter / 지수 백오프 + 지터
- Client timeout and maximum retry count / 클라이언트 타임아웃 + 최대 재시도 횟수

## Error reference / 오류 레퍼런스

| Status | Meaning / 의미 | Action / 조치 |
| --- | --- | --- |
| `400` | Invalid RFC3339 query / 잘못된 RFC3339 쿼리 | Query correction / 쿼리 수정 |
| `401` | Missing, invalid, revoked token or inactive user / 토큰 누락·오류·폐기 또는 비활성 사용자 | Settings token creation/rotation or `cctrace init` + account check / Settings 토큰 생성·회전 또는 재인증 + 계정 확인 |
| `403` | Unsupported user role / 미지원 사용자 역할 | Administrator check / 관리자 확인 |
| `429` | Per-user rate budget or instance backstop exceeded / 사용자별 요청 예산 또는 인스턴스 상한 초과 | `Retry-After` 준수 |
| `500` | Generic internal error / 일반 내부 오류 | Bounded retry + operator context / 제한 재시도 + 운영자 문의 |

## Token revocation and rotation / 토큰 폐기와 회전

Self-service flow / 사용자 직접 흐름:

1. Dashboard `Settings` / 대시보드 `Settings`
2. `API Access Tokens`
3. Target named token / 대상 이름 토큰
4. `Rotate` or `Revoke` / `Rotate` 또는 `Revoke`
5. Immediate client secret update after rotation / 회전 직후 클라이언트 시크릿 갱신

Administrator fallback / 관리자 대체 경로:

1. Admin dashboard `Users` page / 관리자 대시보드 `Users` 페이지
2. Target user actions / 대상 사용자 작업
3. `Revoke Token`

Immediate revocation effects / 즉시 폐기 범위:

- Open API requests / Open API 요청
- Per-user sync requests / 사용자별 동기화 요청
- Per-user OTEL ingestion / 사용자별 OTEL 수집

Selected-token-only invalidation for self-service actions / 사용자 직접 작업 시 선택 토큰만 무효화.

All-token invalidation for administrator revocation or account deactivation / 관리자 폐기 또는 계정 비활성화 시 전체 토큰 무효화.

Account deactivation also invalidates the token / 계정 비활성화도 토큰 무효화.

Rotated token display limited to one time / 회전된 토큰 한 번만 표시.

## Security checklist / 보안 점검

- HTTPS outside localhost / localhost 외부 HTTPS
- Secret manager or protected environment variable / 시크릿 관리자 또는 보호된 환경 변수
- Authorization-header log redaction / Authorization 헤더 로그 제거
- No token in query strings / 쿼리 문자열 토큰 금지
- No source-control commits / 소스 관리 커밋 금지
- Separate accounts for independent revocation boundaries / 독립 폐기 경계별 별도 계정

## Two read surfaces / 읽기 표면이 둘인 이유

`cctrace report` reads the internal `/api/cost/*`; `cctrace usage` and external
scripts read `/api/open/v1/usage`. Both are kept on purpose.

`cctrace report` 는 내부 `/api/cost/*` 를, `cctrace usage` 와 외부 스크립트는
`/api/open/v1/usage` 를 읽는다. 둘 다 의도적으로 유지한다.

- **Internal** — first-party, unversioned, may change with the dashboard. Takes the
  same read tokens as the Open API (`server.read_token`); the upload token
  `cctrace init` writes is refused.
- **Open** — a versioned contract for anyone building on it. Requires a token created
  under `Settings > API Access Tokens`.

- **내부** — 1st-party, 버전 없음, 대시보드와 함께 바뀔 수 있음. Open API 와 같은
  읽기 토큰(`server.read_token`) 사용, `cctrace init` 이 쓰는 업로드 토큰은 거부
- **공개** — 그 위에 무언가를 만드는 사람을 위한 버전 있는 계약. `Settings > API
  Access Tokens` 에서 발급한 토큰 필요

**Neither carries its own SQL.** Both call the same store methods (`CostByUser`,
`CostByTeam`, `CostByModel`), so they cannot report different money.
`TestCostSurfacesAgreeOnTotals` fails if that stops being true.

**둘 다 자체 SQL 을 갖지 않는다.** 같은 store 메서드를 부르므로 서로 다른 금액을
낼 수 없다. 그 성질이 깨지면 `TestCostSurfacesAgreeOnTotals` 가 실패한다.
