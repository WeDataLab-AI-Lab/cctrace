# 개인정보

cctrace가 수집하는 것, 저장 위치, 조회 권한, 보존 기간.

!!! warning "서버는 대화 내용을 저장한다"
    세션 로그에는 사람이 입력한 내용, 에이전트의 답변, 도구 명령과 출력이 들어 있다. 기본 설정에서는 운영자가 보존 기간을 정할 때까지 계속 보관된다. 다른 사람을 연결하기 전에 보존 기간과 가림 처리를 정해 둔다.

## 수집 항목 {#what-is-collected}

| 데이터 | 보내는 쪽 | 내용 |
|---|---|---|
| OTEL 텔레메트리 | 에이전트가 직접 OTLP 포트로 | 메트릭과 이벤트: 토큰 수, 비용, 모델, 도구 활동. `cctrace init`이 `OTEL_RESOURCE_ATTRIBUTES`로 사용자 ID, 이름, 이메일, 팀 부착 |
| 세션 로그 | 동기화 데몬이 `/api/sync`로 | 디스크에 기록된 에이전트 세션 파일의 각 줄: 프롬프트, 답변, 추론 텍스트, 인자와 출력을 포함한 도구 호출. 업로드마다 프로젝트 정보(디렉터리 이름, git 원격 URL, 저장소 이름, 브랜치, 커밋) 동봉 |
| 규칙 파일 | 동기화 데몬 | 세션이 실행된 git 저장소 루트의 CLAUDE.md와 AGENTS.md |
| 사용 한도 수치 | 클라이언트 | 개발자 머신에서 읽은 요금제 사용 한도 현황 |

`cctrace init`은 `OTEL_LOG_USER_PROMPTS`를 설정하지 않으므로, 사용자가 직접 설정하지 않는 한 Claude Code 자체 텔레메트리에는 프롬프트 텍스트가 없다. 세션 로그에는 어느 경우든 대화 전체가 들어 있다.

## 저장 위치 {#where-it-is-stored}

수집 데이터는 설정한 서버와 그 TimescaleDB 데이터베이스로 간다. 그 밖의 외부 요청은 다음과 같다.

- 사용 한도를 읽으려고 클라이언트가 로컬 Claude Code 로그인으로 Anthropic 사용량 API 조회
- Codex 사용량 가격 산정을 위해 서버가 OpenAI의 공개 가격표와 변경 이력 페이지 다운로드

주간 AI 보고서 기능은 켜져 있으면 설정된 AI 공급자로 데이터를 보낸다. 이 기능은 변경이 진행 중이라 이 문서에서 다루지 않는다.

엔드포인트가 같은 머신(루프백)이 아닌 곳으로 가는 평문 `http://`이면 사설 주소라도 클라이언트는 경고만 하고 거부하지 않는다. 직접 관리하지 않는 네트워크로 클라이언트가 접속하기 전에 서버를 TLS 뒤에 둔다. [서버 설치](../server/install.md#optional-https-with-caddy) 참고.

## 조회 권한 {#who-can-see-what}

| 데이터 | `admin` | `user` |
|---|---|---|
| 세션 트랜스크립트(`/sessions`) | 모든 사용자 | 본인 |
| 규칙 파일(`/rules`) | 모든 저장소 | 본인 세션이 있는 저장소 |
| 집계: 사용량, 비용, 도구, 플러그인·스킬, 사용자별 목록 | 전체 | 전체 |
| 텔레메트리 행(`/logs`) | 전체 | 전체 |
| 사용자 관리, `/admin` | 가능 | 불가 |

- 집계 공유는 의도된 동작. 팀 전체 사용량을 한곳에서 합산하기 위함
- 텔레메트리 행의 자유 텍스트 속성(프롬프트, 도구 인자와 출력, 명령 텍스트, stdout·stderr)은 관리자 응답을 포함한 모든 대시보드 응답에서 제거. 데이터베이스에는 남아 있을 수 있음
- `user` 계정의 본인 세션 판별 기준은 cctrace 사용자 ID. 사용자 ID가 없는 계정은 세션 데이터 조회 불가(`CCTRACE_USERID_ACCESS_CONTROL` 기본값일 때). [사용자](../dashboard/users.md#how-data-is-attributed-to-an-account) 참고

### `CCTRACE_USERID_ACCESS_CONTROL` {#cctrace_userid_access_control}

| 값 | `user` 계정의 세션 매칭 기준 |
|---|---|
| 미설정 또는 `false` 이외 값(기본값) | 계정의 cctrace 사용자 ID |
| `false` | 계정의 로그인 이메일과 클라이언트가 업로드에 사용한 이메일 |

이 설정은 세션과 소유자를 매칭하는 방식만 바꾼다. 집계를 비공개로 만들지 않는다.

## 클라이언트에서 가리기 {#redact-on-the-client}

두 프로필 옵션이 세션 로그가 머신을 떠나기 전에 내용을 지운다. 둘 다 기본값은 꺼져 있다.

```bash
cctrace config set options.redact_user_prompts true
cctrace config set options.redact_tool_details true
```

| 옵션 | 값을 대체하는 필드 |
|---|---|
| `redact_user_prompts` | `content`, `text`, `thinking`, `summary`, `title` |
| `redact_tool_details` | `arguments`, `input`, `output`, `result`, `command`, `description`, `stdout`, `stderr` |

- 레코드 안 어디에 있든 해당 필드 값을 `[redacted by cctrace client]`로 대체. 파싱할 수 없는 줄은 통째로 대체
- 토큰 수, 비용, 모델, 타임스탬프는 그대로라 대시보드 수치는 변하지 않음
- 대상은 세션 로그뿐. 에이전트가 직접 보내는 OTEL 텔레메트리와 규칙 파일은 대상 아님
- 변경 이후 업로드부터 적용. 서버에 이미 있는 레코드는 그대로
- 실행 중인 동기화 데몬은 시작할 때의 옵션을 유지. `cctrace sync --stop` 후 `cctrace sync --daemon`으로 재시작

이름 있는 프로필에는 `--profile <name>`을 붙인다. `cctrace status`가 켜진 가림 처리를 표시한다.

## 데이터 삭제 {#delete-data}

| 대상 | 권한 | 방법 |
|---|---|---|
| 세션 하나 | 소유자(소유자 삭제 정책이 허용할 때) 또는 관리자 | `/sessions`의 휴지통 아이콘. [페이지](../dashboard/pages.md#delete-a-session) 참고 |
| 프로젝트의 세션과 이후 수집 | 관리자 | `/sessions`의 세션 삭제 대화상자 또는 프로젝트 선택기 |
| 한 계정 이메일로 저장된 모든 데이터 | 관리자 | `/users`의 **Clear Collected Data**. [사용자](../dashboard/users.md#manage-accounts) 참고 |

삭제한 세션은 클라이언트가 다시 올려도 거부된다.

## 보존 기간 {#retention}

| 테이블 | 내용 | 기본값 |
|---|---|---|
| `otel_events` | 텔레메트리 이벤트 | 90일 후 삭제, 30일 후 압축 |
| `otel_metrics` | 텔레메트리 메트릭 | 90일 후 삭제, 30일 후 압축 |
| `session_records` | 세션 로그(대화 내용) | 정책 없음: 운영자가 정할 때까지 보관 |

`session_records`에 정책이 없으면 서버는 시작할 때 다음을 기록한다.

```text
[cctraced] notice: session_records (conversation content) has no retention policy and is kept indefinitely, while otel_events/otel_metrics are dropped after 90 days. Set SESSION_RETENTION_DAYS (0 = keep forever) or choose an interval in Admin -> Storage.
```

보존 기간은 `/admin`의 **Storage** 탭에서 설정한다. `cctraced`는 `SESSION_RETENTION_DAYS`, `OTEL_RETENTION_DAYS`도 읽지만 기본 제공 compose 파일은 이 둘을 컨테이너에 전달하지 않는다([서버 설정](../server/configuration.md#variables-the-compose-file-does-not-pass)). 컨테이너에 전달되면 환경 변수 값이 우선하며 대시보드의 해당 설정을 잠근다. 기간을 줄이면 기존 데이터가 삭제된다. 자세한 내용: [운영](../server/operations.md#data-retention).
