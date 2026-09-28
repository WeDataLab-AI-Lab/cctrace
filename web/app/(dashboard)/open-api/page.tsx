'use client';

import { useState } from 'react';
import { AlertTriangle, BookOpen, CheckCircle2, KeyRound, LockKeyhole, Terminal } from 'lucide-react';
import { OPEN_API_ENDPOINTS, OPEN_API_MANUAL } from '@/lib/open-api-docs';
import type { OpenAPIEndpoint, OpenAPILocale } from '@/lib/open-api-docs';
import { cn } from '@/lib/utils';

const codeSamples = {
  init: `cctrace init
# Enter the cctrace server URL, your cctrace user ID, and password.
# Answer Y when asked to create a read token for analysis commands.

cctrace auth read
# Later, or on another machine: re-enter your password to create one.`,
  environment: `export CCTRACE_API_URL="https://cctrace.example.com"
export CCTRACE_API_TOKEN="$(
  jq -r '.server.read_token // empty' "\${HOME}/.cctrace/profile.json"
)"`,
  firstRequest: `curl --fail --silent --show-error \\
  -H "Authorization: Bearer \${CCTRACE_API_TOKEN}" \\
  "\${CCTRACE_API_URL}/api/open/v1/usage?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z"`,
  directAuth: `read -r -p "cctrace user ID: " CCTRACE_USER_ID
read -r -s -p "Password: " CCTRACE_PASSWORD; printf '\\n'; export CCTRACE_PASSWORD

curl --fail --silent --show-error \\
  -H 'Content-Type: application/json' \\
  --data "$(jq -n \\
    --arg user_id "\${CCTRACE_USER_ID}" \\
    --arg device "$(hostname)" \\
    '{user_id: $user_id, password: env.CCTRACE_PASSWORD, device: $device}')" \\
  "\${CCTRACE_API_URL}/api/cli/read-token" | jq

unset CCTRACE_PASSWORD`,
  sessions: `curl --fail --silent --show-error \\
  -H "Authorization: Bearer \${CCTRACE_API_TOKEN}" \\
  "\${CCTRACE_API_URL}/api/open/v1/sessions?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z&limit=100&offset=0"`,
  sessionDetail: `SESSION_ID='replace-with-session-id'
curl --fail --silent --show-error \\
  -H "Authorization: Bearer \${CCTRACE_API_TOKEN}" \\
  "\${CCTRACE_API_URL}/api/open/v1/sessions/\${SESSION_ID}?order=asc&limit=1000"`,
  events: `curl --fail --silent --show-error \\
  -H "Authorization: Bearer \${CCTRACE_API_TOKEN}" \\
  "\${CCTRACE_API_URL}/api/open/v1/events?since=2026-08-01T00:00:00Z&until=2026-08-02T00:00:00Z&session_id=replace-with-session-id"`,
  python: `import os
import requests

base_url = os.environ["CCTRACE_API_URL"].rstrip("/")
token = os.environ["CCTRACE_API_TOKEN"]
response = requests.get(
    f"{base_url}/api/open/v1/usage",
    headers={"Authorization": f"Bearer {token}"},
    params={"since": "2026-08-01T00:00:00Z"},
    timeout=30,
)
response.raise_for_status()
print(response.json())`,
  javascript: `const baseUrl = process.env.CCTRACE_API_URL.replace(/\\/$/, '');
const response = await fetch(baseUrl + '/api/open/v1/usage', {
  headers: {
    Authorization: 'Bearer ' + process.env.CCTRACE_API_TOKEN,
  },
});

if (!response.ok) {
  throw new Error('cctrace API returned ' + response.status);
}
console.log(await response.json());`,
  usageResponse: `{
  "session_count": 3,
  "input_tokens": 12000,
  "output_tokens": 3000,
  "total_tokens": 15000,
  "work_time_seconds": 4200,
  "by_model": [
    {
      "model": "claude-sonnet-4-20250514",
      "input_tokens": 12000,
      "output_tokens": 3000,
      "total_tokens": 15000
    }
  ]
}`,
};

const copy = {
  en: {
    version: 'REST · JSON · v1 · read-only',
    baseUrl: 'Base URL',
    baseUrlBody: 'Use the same HTTP(S) origin as the cctrace dashboard. Do not append /api to the base URL.',
    prerequisites: 'Before you begin',
    prerequisiteItems: [
      'An active cctrace dashboard account with the user or admin role',
      'Your cctrace user ID and password (not necessarily your email address)',
      'Network access to the cctraced HTTP endpoint',
      'A dashboard account for Settings-based token management; curl and jq for the examples',
    ],
    tokenTitle: 'Create your access token',
    tokenLead: 'Open Settings → API Access Tokens to create a named cct_ token for each integration. Copy each secret immediately: existing token values are never displayed again.',
    steps: [
      ['1', 'Prepare the account', 'An administrator creates your dashboard account and assigns a cctrace user ID. If you were given a temporary password, change it in Settings first — token creation and rotation are refused until you do.'],
      ['2', 'Create a named token in Settings', 'Select API Access Tokens → Create token and enter the client or integration name. Copy the one-time value into a secret manager or protected environment variable.'],
      ['3', 'Send a first request', 'Use Authorization: Bearer cct_… over HTTPS. Never place the token in a URL, log, or source file.'],
      ['4', 'Optional CLI setup', 'Run cctrace init for sync and OTEL setup; it stores an upload token as server.auth_token, which this API refuses. Accept its offer of a read token, or run cctrace auth read later; the read token is stored as server.read_token. Administrators create read tokens here in Settings instead.'],
    ],
    profileNote: 'Default profile: ~/.cctrace/profile.json. Named profile: ~/.cctrace/profiles/<name>/profile.json. Use the matching path when exporting the token.',
    directTitle: 'Direct issuance (advanced)',
    directBody: 'POST /api/cli/read-token accepts user_id, password, and a device name and returns a read token (pass the token you hold as replace to revoke it; administrators are refused). POST /api/cli/auth returns the upload token, which this API refuses. Interactive users should prefer Settings or cctrace auth read so credentials are not placed in scripts, command history, logs, or URLs.',
    authTitle: 'Send the token',
    authBody: 'Every /api/open/v1 request requires this header:',
    authWarnings: [
      'The dashboard JWT cookie is not an Open API credential.',
      'The global ingestion API_KEY is deliberately rejected by the Open API.',
      'A token belongs to one active dashboard user and inherits that user’s role and data scope.',
      'Each user may keep multiple named tokens and revoke one integration without interrupting the others.',
      'Choose Unlimited or a custom expiration when creating a token. Inactive or expired tokens are rejected until reactivated or assigned a valid future expiration.',
    ],
    firstRequestTitle: 'First request',
    firstRequestBody: 'Request usage for a closed RFC3339 time window. A 200 response with JSON confirms the base URL, token, and scope are valid.',
    examplesTitle: 'Common requests',
    pythonTitle: 'Python',
    javascriptTitle: 'JavaScript',
    referenceLead: 'All endpoints return application/json. Most list endpoints return a bare JSON array; rules return a paginated object and usage returns one aggregate object. Optional fields are omitted when unavailable.',
    parameters: 'Parameters',
    responseFields: 'Response fields',
    required: 'required',
    optional: 'optional',
    noParameters: 'No parameters.',
    usageSemanticsTitle: 'Usage calculation semantics',
    usageSemantics: [
      'total_tokens equals input_tokens plus output_tokens; cache-read/create tokens are not included in the usage total.',
      'work_time_seconds sums the elapsed time between the first and last matching record of each session.',
      'Overlapping sessions contribute independently; work time is not deduplicated wall-clock time.',
      'Multi-model session tokens are assigned to their observed models; session_count and work time are counted once overall.',
      'since is inclusive and until is exclusive. Use the previous page’s last boundary to build non-overlapping windows.',
    ],
    errorsTitle: 'HTTP status reference',
    errors: [
      ['200', 'Success', 'JSON response returned. An empty array or zero totals are valid results.'],
      ['400', 'Bad request', 'A since or until value is not RFC3339. Correct the query before retrying.'],
      ['401', 'Unauthorized', 'Bearer header missing, token invalid, revoked, inactive, or expired; or account inactive. Check the token status and expiration in Settings.'],
      ['403', 'Forbidden', 'The token owner does not have a supported user or admin role. Contact an administrator.'],
      ['429', 'Rate limited', 'Your per-user Open API budget, or the instance-wide backstop, was exceeded. Wait for Retry-After, then retry with exponential backoff and jitter.'],
      ['500', 'Internal error', 'The server returns a generic message. Retry transient failures and give the server operator a timestamp/request context.'],
    ],
    rateTitle: 'Rate limit and retries',
    rateBody: 'Each token owner gets 20 requests/second with a burst capacity of 50; an instance-wide backstop of 500 requests/second (burst 1,250) sits in front of authentication. Honor Retry-After. Retry only 429 and transient 5xx responses; do not blindly retry 400, 401, or 403.',
    paginationTitle: 'Pagination guidance',
    paginationBody: 'Start with offset=0 and increment by the returned page length until the array is shorter than limit. Keep all filters and time boundaries unchanged during a pagination run. Session-detail ordering is deterministic for tied timestamps.',
    securityTitle: 'Role-based data scope',
    securityItems: [
      'Regular user: the server ignores requested profile_email, login_email, and user_id values and forces the token owner’s scope.',
      'Admin: may query all visible data or narrow it with owner filters.',
      'Session, tools, plugins, skills, rules, events, and metrics are sanitized. Raw prompt or rule bodies, command contents, repository paths, emails, teams, source files, and internal attributes are not returned.',
      'This API and the dashboard’s read endpoints refuse upload tokens under the same rule, and ingestion refuses read tokens. A read token still reads with its owner’s role, so treat every token as a secret.',
    ],
    rotationTitle: 'Revoke and rotate',
    rotationBody: 'Use Settings → API Access Tokens to activate, deactivate, change expiration, rotate, or revoke one named token. Expiration may be Unlimited or a user-selected date and time. Only the selected token is changed; other tokens remain unaffected. The list shows status, expiration, creation time, latest rotation time, and whether the token is a Read API, Read API (CLI), or CLI ingestion token. A rotated replacement is shown once. Administrators can revoke all tokens for a user from Users, and disabling the account invalidates every token.',
    practicesTitle: 'Operational practices',
    practices: [
      'Store tokens in a secret manager or protected environment variable; never commit them.',
      'Use HTTPS outside localhost and avoid tokens in query strings.',
      'Set client timeouts and cap retry attempts.',
      'Use the smallest time range and page size needed.',
      'Redact Authorization headers from application, proxy, and CI logs.',
      'Use separate user accounts/tokens when independent revocation or audit boundaries are required.',
    ],
    troubleshooting: [
      ['cctrace init says invalid credentials', 'Confirm the cctrace user ID, not email, and the current password. Ask an admin whether the account is active.'],
      ['cctrace status shows no token', 'Run cctrace init for the same profile and server. Named profiles require --profile <name>.'],
      ['401 after the token worked', 'The token may have been rotated/revoked or the user disabled. Check Settings and update the client with a newly created token; contact an admin if authentication still fails.'],
      ['200 with no data', 'Check UTC/RFC3339 boundaries, project hash, and the token owner’s data scope. Empty results are not authentication failures.'],
      ['429 responses', 'Serialize or throttle requests, honor Retry-After, then retry with jitter. The budget is per user, so parallel scripts under one account share it.'],
      ['Unexpectedly missing fields', 'Optional and sensitive fields are omitted by design. Use the dashboard for richer authorized views.'],
    ],
  },
  ko: {
    version: 'REST · JSON · v1 · 읽기 전용',
    baseUrl: '기본 URL',
    baseUrlBody: 'cctrace 대시보드와 동일한 HTTP(S) origin 사용. 기본 URL 뒤에 /api를 붙이지 않음.',
    prerequisites: '준비 사항',
    prerequisiteItems: [
      'user 또는 admin 역할의 활성 cctrace 대시보드 계정',
      'cctrace 사용자 ID와 비밀번호(이메일 주소와 다를 수 있음)',
      'cctraced HTTP 엔드포인트 네트워크 접근',
      'Settings 기반 토큰 관리용 대시보드 계정, 예제 실행용 curl과 jq',
    ],
    tokenTitle: '액세스 토큰 생성',
    tokenLead: 'Settings → API Access Tokens에서 연동별 이름을 가진 cct_ 토큰 생성. 기존 토큰 값은 다시 표시되지 않으므로 각 발급 직후 복사 필수.',
    steps: [
      ['1', '계정 준비', '관리자가 대시보드 계정과 cctrace 사용자 ID를 생성. 임시 비밀번호를 받았다면 Settings에서 먼저 변경 — 변경 전에는 토큰 생성과 회전이 거부된다.'],
      ['2', 'Settings에서 이름 있는 토큰 생성', 'API Access Tokens → Create token 선택 후 클라이언트 또는 연동 이름 입력. 한 번만 표시되는 값을 시크릿 관리자 또는 보호된 환경 변수에 저장.'],
      ['3', '첫 요청 전송', 'HTTPS에서 Authorization: Bearer cct_… 사용. URL, 로그, 소스 파일에 토큰 삽입 금지.'],
      ['4', '선택적 CLI 설정', 'sync와 OTEL 설정용 cctrace init 실행. 이때 저장되는 server.auth_token 은 업로드용이라 이 API 가 거부. init 의 읽기 토큰 발급 제안을 수락하거나 나중에 cctrace auth read 실행, server.read_token 에 저장. 관리자는 Settings 에서 발급.'],
    ],
    profileNote: '기본 프로필: ~/.cctrace/profile.json. 명명 프로필: ~/.cctrace/profiles/<name>/profile.json. 토큰 로드 시 해당 프로필 경로 사용.',
    directTitle: '직접 발급(고급)',
    directBody: 'POST /api/cli/read-token 에 user_id, password, 기기 이름을 보내면 읽기 토큰 반환 (보유 토큰을 replace 로 보내면 그 토큰만 폐기, 관리자 거부). POST /api/cli/auth 가 반환하는 업로드 토큰은 이 API 가 거부. 대화형 사용자는 자격 증명이 스크립트, 명령 기록, 로그, URL에 남지 않도록 Settings 또는 cctrace auth read 사용 권장.',
    authTitle: '토큰 전송',
    authBody: '모든 /api/open/v1 요청에 다음 헤더 필요:',
    authWarnings: [
      '대시보드 JWT 쿠키는 Open API 자격 증명이 아님.',
      '수집용 전역 API_KEY는 Open API에서 의도적으로 거부.',
      '토큰은 한 활성 대시보드 사용자에게 귀속되며 해당 역할과 데이터 범위 상속.',
      '사용자별 복수 이름 토큰 유지 가능. 다른 연동 중단 없이 개별 토큰 폐기 가능.',
      '토큰 생성 시 무제한 또는 사용자 지정 만료 시각 선택 가능. 비활성 또는 만료된 토큰은 재활성화하거나 유효한 미래 만료 시각을 지정할 때까지 거부.',
    ],
    firstRequestTitle: '첫 요청',
    firstRequestBody: '닫힌 RFC3339 시간 범위의 사용량 요청. JSON과 함께 200 응답 시 기본 URL, 토큰, 접근 범위 정상.',
    examplesTitle: '주요 요청',
    pythonTitle: 'Python',
    javascriptTitle: 'JavaScript',
    referenceLead: '모든 엔드포인트의 응답 형식은 application/json. 대부분의 목록은 JSON 배열, 규칙은 페이지 객체, 사용량은 단일 집계 객체. 값이 없는 선택 필드는 응답에서 생략.',
    parameters: '파라미터',
    responseFields: '응답 필드',
    required: '필수',
    optional: '선택',
    noParameters: '파라미터 없음.',
    usageSemanticsTitle: '사용량 계산 규칙',
    usageSemantics: [
      'total_tokens = input_tokens + output_tokens. 캐시 읽기·생성 토큰은 사용량 합계에서 제외.',
      'work_time_seconds는 세션별 첫 일치 레코드와 마지막 일치 레코드 사이 경과시간의 합.',
      '동시 실행 세션도 각각 합산. 중복 제거된 실제 경과시간이 아님.',
      '다중 모델 세션의 토큰은 관측 모델별로 배분. session_count와 작업 시간은 전체 합계에서 한 번만 계산.',
      'since는 포함, until은 미포함. 겹치지 않는 구간 생성 시 이전 구간의 종료 경계를 다음 구간에 재사용.',
    ],
    errorsTitle: 'HTTP 상태 코드',
    errors: [
      ['200', '성공', 'JSON 응답 반환. 빈 배열이나 0 합계도 정상 결과.'],
      ['400', '잘못된 요청', 'since 또는 until이 RFC3339 형식이 아님. 쿼리 수정 후 재요청.'],
      ['401', '인증 실패', 'Bearer 헤더 누락, 잘못되거나 폐기·비활성·만료된 토큰, 또는 비활성 계정. Settings에서 토큰 상태와 만료 시각 확인.'],
      ['403', '권한 없음', '토큰 소유자 역할이 user/admin이 아님. 관리자 문의.'],
      ['429', '속도 제한', '사용자별 Open API 요청 예산 또는 인스턴스 전체 상한 초과. Retry-After만큼 대기 후 지수 백오프와 지터 적용.'],
      ['500', '내부 오류', '일반화된 오류 메시지 반환. 일시 오류 재시도 후 운영자에게 시각과 요청 맥락 전달.'],
    ],
    rateTitle: '속도 제한과 재시도',
    rateBody: '토큰 소유자별 초당 20회, 버스트 50회. 인증 앞단에 인스턴스 전체 상한 초당 500회, 버스트 1,250회. Retry-After 준수. 429와 일시적 5xx만 재시도하고 400, 401, 403의 무조건 재시도 금지.',
    paginationTitle: '페이지 이동',
    paginationBody: 'offset=0에서 시작해 반환 배열 길이만큼 증가. 배열 길이가 limit보다 작으면 종료. 한 페이지 순회 동안 필터와 시간 경계 고정. 세션 상세는 동일 시각 레코드에도 결정론적 정렬 적용.',
    securityTitle: '역할 기반 데이터 범위',
    securityItems: [
      '일반 사용자: 요청의 profile_email, login_email, user_id를 무시하고 토큰 소유자 범위로 강제.',
      '관리자: 전체 가시 데이터 조회 또는 소유자 필터로 범위 축소 가능.',
      '세션, 도구, 플러그인, 스킬, 규칙, 이벤트, 메트릭 데이터 정제. 원문 프롬프트·규칙 본문, 명령 내용, 저장소 경로, 이메일, 팀, 소스 파일, 내부 속성 미반환.',
      '이 API 와 대시보드 조회 엔드포인트는 같은 규칙으로 업로드 토큰 거부, 수집은 읽기 토큰 거부. 읽기 토큰은 소유자 역할로 조회하므로 모든 토큰을 비밀로 취급.',
    ],
    rotationTitle: '폐기와 회전',
    rotationBody: 'Settings → API Access Tokens에서 이름 있는 토큰 하나를 활성·비활성, 만료 시각 변경, 회전 또는 폐기. 만료는 무제한 또는 사용자가 선택한 날짜와 시각으로 설정 가능. 선택한 토큰만 변경하며 나머지 토큰에는 영향 없음. 목록에 상태, 만료, 생성 시각, 최근 회전 시각, Read API, Read API (CLI), CLI ingestion 용도 표시. 회전된 신규 토큰은 한 번만 표시. 관리자는 Users에서 사용자 전체 토큰 폐기 가능. 계정 비활성화 시 모든 토큰 무효화.',
    practicesTitle: '운영 권장 사항',
    practices: [
      '토큰을 시크릿 관리자 또는 보호된 환경 변수에 저장. 저장소 커밋 금지.',
      'localhost 외부에서 HTTPS 사용. 쿼리 문자열에 토큰 삽입 금지.',
      '클라이언트 타임아웃과 최대 재시도 횟수 설정.',
      '필요한 최소 시간 범위와 페이지 크기 사용.',
      '애플리케이션·프록시·CI 로그에서 Authorization 헤더 제거.',
      '독립 폐기나 감사 경계 필요 시 별도 사용자 계정과 토큰 사용.',
    ],
    troubleshooting: [
      ['cctrace init에서 invalid credentials', '이메일이 아닌 cctrace 사용자 ID와 현재 비밀번호 확인. 관리자에게 계정 활성 상태 문의.'],
      ['cctrace status에 토큰 없음', '동일 프로필과 서버 대상으로 cctrace init 실행. 명명 프로필은 --profile <name> 필요.'],
      ['정상 사용 중 401 발생', '토큰 회전·폐기 또는 사용자 비활성 가능. Settings 확인 후 새 토큰으로 클라이언트 갱신. 계속 실패하면 관리자 문의.'],
      ['200이지만 데이터 없음', 'UTC/RFC3339 경계, 프로젝트 해시, 토큰 소유자의 데이터 범위 확인. 빈 결과는 인증 실패가 아님.'],
      ['429 응답', '요청 직렬화 또는 제한, Retry-After 준수, 지터 포함 재시도. 예산이 사용자 단위라 한 계정의 병렬 스크립트끼리 공유.'],
      ['필드 일부 누락', '선택 필드와 민감 필드는 의도적으로 생략. 더 풍부한 권한 기반 조회는 대시보드 사용.'],
    ],
  },
};

interface LanguageButtonProps {
  locale: OpenAPILocale;
  active: boolean;
  label: string;
  onSelect: (locale: OpenAPILocale) => void;
}

const LanguageButton = ({ locale, active, label, onSelect }: LanguageButtonProps) => {
  const handleClick = () => onSelect(locale);
  return (
    <button
      type="button"
      onClick={handleClick}
      className={cn(
        'rounded-md px-3 py-1.5 text-xs font-medium transition-colors',
        active ? 'bg-brand text-brand-ink' : 'text-ink-2 hover:bg-surface-sunk',
      )}
    >
      {label}
    </button>
  );
};

interface CodeBlockProps {
  children: string;
}

const CodeBlock = ({ children }: CodeBlockProps) => (
  <pre className="overflow-x-auto rounded-lg border border-border bg-surface-sunk p-4 font-mono text-[12px] leading-5 text-ink">
    <code>{children}</code>
  </pre>
);

interface EndpointCardProps {
  endpoint: OpenAPIEndpoint;
  locale: OpenAPILocale;
}

const EndpointCard = ({ endpoint, locale }: EndpointCardProps) => {
  const text = copy[locale];
  return (
    <details className="rounded-lg border border-border bg-surface p-5 shadow-[var(--sh-xs)]">
      <summary className="cursor-pointer text-ink">
        <span className="ml-2 inline-flex flex-wrap items-center gap-2 align-middle">
          <span className="rounded bg-success-soft px-2 py-1 font-mono text-[11px] font-semibold text-success-strong">
            {endpoint.method}
          </span>
          <code className="break-all font-mono text-[13px] font-medium text-ink">{endpoint.path}</code>
          <span className="text-[13px] text-ink-2">— {endpoint.title[locale]}</span>
        </span>
      </summary>

      <div className="mt-4 border-t border-border-subtle pt-4">
        <p className="text-[13px] leading-5 text-ink-2">{endpoint.description[locale]}</p>

        <h4 className="mb-2 mt-5 text-[12px] font-semibold uppercase tracking-[0.04em] text-ink-3">{text.parameters}</h4>
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="w-full min-w-[620px] text-left text-[12px]">
            <thead className="bg-surface-2 text-ink-3">
              <tr>
                <th className="px-3 py-2 font-medium">Name</th>
                <th className="px-3 py-2 font-medium">Type</th>
                <th className="px-3 py-2 font-medium">Required</th>
                <th className="px-3 py-2 font-medium">Description</th>
              </tr>
            </thead>
            <tbody>
              {endpoint.parameters.map((parameter) => (
                <tr key={parameter.name} className="border-t border-border-subtle">
                  <td className="px-3 py-2 font-mono text-ink">{parameter.name}</td>
                  <td className="px-3 py-2 font-mono text-ink-2">{parameter.type}</td>
                  <td className="px-3 py-2 text-ink-2">{parameter.required ? text.required : text.optional}</td>
                  <td className="px-3 py-2 text-ink-2">{parameter.description[locale]}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <h4 className="mb-2 mt-5 text-[12px] font-semibold uppercase tracking-[0.04em] text-ink-3">{text.responseFields}</h4>
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="w-full min-w-[620px] text-left text-[12px]">
            <thead className="bg-surface-2 text-ink-3">
              <tr>
                <th className="px-3 py-2 font-medium">Field</th>
                <th className="px-3 py-2 font-medium">Type</th>
                <th className="px-3 py-2 font-medium">Description</th>
              </tr>
            </thead>
            <tbody>
              {endpoint.fields.map((field) => (
                <tr key={field.name} className="border-t border-border-subtle">
                  <td className="px-3 py-2 font-mono text-ink">{field.name}</td>
                  <td className="px-3 py-2 font-mono text-ink-2">{field.type}</td>
                  <td className="px-3 py-2 text-ink-2">{field.description[locale]}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </details>
  );
};

export default function OpenAPIPage() {
  const [locale, setLocale] = useState<OpenAPILocale>('en');
  const manual = OPEN_API_MANUAL[locale];
  const text = copy[locale];
  const handleLanguageSelect = (nextLocale: OpenAPILocale) => setLocale(nextLocale);

  return (
    <main className="mx-auto max-w-[1120px] space-y-8 pb-16">
      <section className="rounded-lg border border-border bg-surface p-6 shadow-[var(--sh-sm)] md:p-8">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="max-w-3xl">
            <div className="mb-3 flex items-center gap-2 text-brand">
              <BookOpen size={18} />
              <span className="text-[11px] font-semibold uppercase tracking-[0.06em]">{text.version}</span>
            </div>
            <h2 className="text-[26px] font-semibold tracking-[-0.02em] text-ink">{manual.title}</h2>
            <p className="mt-2 text-[14px] leading-6 text-ink-2">{manual.subtitle}</p>
          </div>
          <div className="flex rounded-lg border border-border bg-surface-2 p-1">
            {(Object.keys(OPEN_API_MANUAL) as OpenAPILocale[]).map((option) => (
              <LanguageButton
                key={option}
                locale={option}
                label={OPEN_API_MANUAL[option].label}
                active={locale === option}
                onSelect={handleLanguageSelect}
              />
            ))}
          </div>
        </div>
      </section>

      <div className="min-w-0 space-y-10">
          <section id="getting-started" className="scroll-mt-4 space-y-5">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[0].title}</h2>
            <div className="rounded-lg border border-border bg-surface p-5">
              <h3 className="text-[14px] font-semibold text-ink">{text.baseUrl}</h3>
              <p className="mt-1 text-[13px] leading-5 text-ink-2">{text.baseUrlBody}</p>
              <code className="mt-3 block rounded-md bg-surface-sunk px-3 py-2 font-mono text-[12px] text-ink">https://cctrace.example.com</code>
            </div>
            <div>
              <h3 className="mb-3 text-[15px] font-semibold text-ink">{text.prerequisites}</h3>
              <ul className="space-y-2">
                {text.prerequisiteItems.map((item) => (
                  <li key={item} className="flex gap-2 text-[13px] leading-5 text-ink-2">
                    <CheckCircle2 size={15} className="mt-0.5 shrink-0 text-success" />
                    {item}
                  </li>
                ))}
              </ul>
            </div>
            <div>
              <h3 className="text-[17px] font-semibold text-ink">{text.tokenTitle}</h3>
              <p className="mt-1 text-[13px] leading-5 text-ink-2">{text.tokenLead}</p>
              <div className="mt-3 flex flex-wrap gap-2">
                {manual.tokenFacts.map((fact) => (
                  <code key={fact} className="rounded-md bg-surface-sunk px-2 py-1 font-mono text-[11px] text-ink-2">{fact}</code>
                ))}
              </div>
              <div className="mt-4 grid gap-3 sm:grid-cols-2">
                {text.steps.map(([number, title, body]) => (
                  <article key={number} className="rounded-lg border border-border bg-surface p-4">
                    <div className="mb-2 flex items-center gap-2">
                      <span className="flex h-6 w-6 items-center justify-center rounded-full bg-brand-soft text-[11px] font-semibold text-brand">{number}</span>
                      <h4 className="text-[13px] font-semibold text-ink">{title}</h4>
                    </div>
                    <p className="text-[12px] leading-5 text-ink-2">{body}</p>
                  </article>
                ))}
              </div>
            </div>
            <CodeBlock>{codeSamples.init}</CodeBlock>
            <div className="rounded-lg border border-brand/25 bg-brand-soft p-4 text-[12px] leading-5 text-ink-2">{text.profileNote}</div>
            <CodeBlock>{codeSamples.environment}</CodeBlock>
          </section>

          <section id="authentication" className="scroll-mt-4 space-y-5">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[1].title}</h2>
            <div className="rounded-lg border border-border bg-surface p-5">
              <div className="mb-2 flex items-center gap-2">
                <KeyRound size={17} className="text-brand" />
                <h3 className="text-[15px] font-semibold text-ink">{text.authTitle}</h3>
              </div>
              <p className="text-[13px] text-ink-2">{text.authBody}</p>
              <code className="mt-3 block rounded-md bg-surface-sunk px-3 py-2 font-mono text-[12px] text-ink">Authorization: Bearer cct_...</code>
              <ul className="mt-4 space-y-2">
                {text.authWarnings.map((warning) => (
                  <li key={warning} className="flex gap-2 text-[12px] leading-5 text-ink-2">
                    <LockKeyhole size={14} className="mt-0.5 shrink-0 text-ink-3" />
                    {warning}
                  </li>
                ))}
              </ul>
            </div>
            <div>
              <h3 className="text-[15px] font-semibold text-ink">{text.directTitle}</h3>
              <p className="mb-3 mt-1 text-[13px] leading-5 text-ink-2">{text.directBody}</p>
              <CodeBlock>{codeSamples.directAuth}</CodeBlock>
            </div>
          </section>

          <section id="usage" className="scroll-mt-4 space-y-5">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[2].title}</h2>
            <div>
              <div className="mb-2 flex items-center gap-2">
                <Terminal size={17} className="text-brand" />
                <h3 className="text-[15px] font-semibold text-ink">{text.firstRequestTitle}</h3>
              </div>
              <p className="mb-3 text-[13px] leading-5 text-ink-2">{text.firstRequestBody}</p>
              <CodeBlock>{codeSamples.firstRequest}</CodeBlock>
            </div>
            <h3 className="text-[15px] font-semibold text-ink">{text.examplesTitle}</h3>
            <CodeBlock>{codeSamples.sessions}</CodeBlock>
            <CodeBlock>{codeSamples.sessionDetail}</CodeBlock>
            <CodeBlock>{codeSamples.events}</CodeBlock>
            <div className="grid gap-5 xl:grid-cols-2">
              <div className="min-w-0 space-y-2">
                <h3 className="text-[14px] font-semibold text-ink">{text.pythonTitle}</h3>
                <CodeBlock>{codeSamples.python}</CodeBlock>
              </div>
              <div className="min-w-0 space-y-2">
                <h3 className="text-[14px] font-semibold text-ink">{text.javascriptTitle}</h3>
                <CodeBlock>{codeSamples.javascript}</CodeBlock>
              </div>
            </div>
          </section>

          <section id="reference" className="scroll-mt-4 space-y-5">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[3].title}</h2>
            <p className="text-[13px] leading-5 text-ink-2">{text.referenceLead}</p>
            {OPEN_API_ENDPOINTS.map((endpoint) => (
              <EndpointCard key={endpoint.path} endpoint={endpoint} locale={locale} />
            ))}
            <div className="space-y-3">
              <h3 className="text-[15px] font-semibold text-ink">{text.usageSemanticsTitle}</h3>
              <ul className="list-disc space-y-2 pl-5 text-[13px] leading-5 text-ink-2">
                {text.usageSemantics.map((item) => <li key={item}>{item}</li>)}
              </ul>
              <CodeBlock>{codeSamples.usageResponse}</CodeBlock>
            </div>
          </section>

          <section id="errors" className="scroll-mt-4 space-y-5">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[4].title}</h2>
            <h3 className="text-[15px] font-semibold text-ink">{text.errorsTitle}</h3>
            <div className="overflow-x-auto rounded-lg border border-border bg-surface">
              <table className="w-full min-w-[680px] text-left text-[12px]">
                <tbody>
                  {text.errors.map(([status, title, body]) => (
                    <tr key={status} className="border-t border-border-subtle first:border-t-0">
                      <td className="px-4 py-3 font-mono font-semibold text-ink">{status}</td>
                      <td className="px-4 py-3 font-medium text-ink">{title}</td>
                      <td className="px-4 py-3 leading-5 text-ink-2">{body}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="rounded-lg border border-warning/35 bg-warning-soft p-4">
              <div className="flex items-center gap-2 text-warning-strong">
                <AlertTriangle size={16} />
                <h3 className="text-[13px] font-semibold">{text.rateTitle}</h3>
              </div>
              <p className="mt-1 text-[12px] leading-5 text-warning-strong">{text.rateBody}</p>
            </div>
            <div>
              <h3 className="text-[15px] font-semibold text-ink">{text.paginationTitle}</h3>
              <p className="mt-1 text-[13px] leading-5 text-ink-2">{text.paginationBody}</p>
            </div>
          </section>

          <section id="security" className="scroll-mt-4 space-y-5">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[5].title}</h2>
            <h3 className="text-[15px] font-semibold text-ink">{text.securityTitle}</h3>
            <ul className="space-y-2">
              {text.securityItems.map((item) => (
                <li key={item} className="flex gap-2 text-[13px] leading-5 text-ink-2">
                  <ShieldIcon />
                  {item}
                </li>
              ))}
            </ul>
            <div className="rounded-lg border border-border bg-surface p-5">
              <h3 className="text-[15px] font-semibold text-ink">{text.rotationTitle}</h3>
              <p className="mt-1 text-[13px] leading-5 text-ink-2">{text.rotationBody}</p>
            </div>
            <div>
              <h3 className="text-[15px] font-semibold text-ink">{text.practicesTitle}</h3>
              <ul className="mt-2 list-disc space-y-2 pl-5 text-[13px] leading-5 text-ink-2">
                {text.practices.map((item) => <li key={item}>{item}</li>)}
              </ul>
            </div>
          </section>

          <section id="troubleshooting" className="scroll-mt-4 space-y-4">
            <h2 className="text-[20px] font-semibold tracking-[-0.02em] text-ink">{manual.sections[6].title}</h2>
            {text.troubleshooting.map(([problem, resolution]) => (
              <details key={problem} className="rounded-lg border border-border bg-surface p-4">
                <summary className="cursor-pointer text-[13px] font-medium text-ink">{problem}</summary>
                <p className="mt-2 text-[12px] leading-5 text-ink-2">{resolution}</p>
              </details>
            ))}
          </section>
      </div>
    </main>
  );
}

const ShieldIcon = () => <LockKeyhole size={15} className="mt-0.5 shrink-0 text-brand" />;
