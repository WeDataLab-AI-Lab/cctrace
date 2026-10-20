# 동기화 데몬

동기화 데몬은 디스크의 세션 로그를 서버의 sync 엔드포인트로 올린다. OTEL 텔레메트리는 데몬을 거치지 않고 에이전트가 직접 보낸다.

## 시작 방식

세션 로그 동기화를 켜면 `cctrace init`이 Claude Code 훅 두 개를 설치한다([Claude Code](../agents/claude-code.md) 참고).

| 훅 | 실행 명령 | 효과 |
|---|---|---|
| `SessionStart` | `cctrace sync --daemon ... --auto-profile --interval 1s` | 프로필의 백그라운드 감시 프로세스 시작, 1초 주기 폴링. 이미 실행 중이면 새 프로세스는 종료 |
| `SessionEnd` | `cctrace sync --daemon --once ... --auto-profile` | 백그라운드에서 한 번 더 동기화. 비동기 훅이며 실행 중인 데몬은 멈추지 않음(열려 있는 다른 세션용) |

데몬 하나가 프로필에 켜진 모든 에이전트를 수집한다: Claude Code, 그리고 옵션이 켜진 Codex CLI·GJC·OMO. Codex·GJC·OMO는 자체 훅이 없으므로 이들만 쓴다면 데몬을 직접 띄운다.

```bash
cctrace sync --daemon
```

데몬은 pid, 로그 경로, `Stop: cctrace sync --stop`을 출력한다.

## 수동 동기화

```bash
cctrace sync                 # 한 번 동기화 후 종료
cctrace sync --watch         # 포그라운드 유지, 30초마다 동기화
cctrace sync --dry-run       # 전송 없이 파일별 신규 레코드 수 표시
```

프로필·디렉터리·모드 플래그 없는 `cctrace sync`는 기본 프로필, 이어서 각 이름 있는 프로필을 한 번씩 동기화한다.

## 플래그

| 플래그 | 기본값 | 효과 |
|---|---|---|
| `--watch` | 꺼짐 | 포그라운드에서 계속 폴링 |
| `--interval <duration>` | `30s` | 감시 모드 폴링 주기, Go duration 형식(`30s`, `2m`). 설치된 훅은 `1s` |
| `--daemon` | 꺼짐 | 백그라운드 감시 프로세스 시작 후 반환 |
| `--once` | 꺼짐 | `--daemon`과 함께: 백그라운드에서 한 번 동기화 후 종료. `SessionEnd` 훅이 사용 |
| `--stop` | 꺼짐 | 실행 중인 데몬에 현재 동기화를 마치고 종료하도록 요청. 최대 35초 대기 |
| `--dry-run` | 꺼짐 | 전송할 내용과 스캔할 Codex·GJC·OMO 홈 표시. 전송 없음 |
| `--claude-dir <dir>` | 프로필의 Claude 홈, 없으면 `~/.claude` | 세션 로그를 읽을 Claude 설정 디렉터리 |
| `--profile <name>` | `CCTRACE_PROFILE`, 없으면 기본 프로필 | 사용할 이름 있는 프로필 |
| `--auto-profile` | 꺼짐 | `--claude-dir`(없으면 `CLAUDE_CONFIG_DIR`)과 Claude 홈이 일치하는 이름 있는 프로필 선택 |
| `--profile-email <email>` | `CCTRACE_PROFILE_EMAIL`, 없으면 프로필 이메일 | 업로드 레코드에 붙는 이메일 |
| `--endpoint <url>` | 프로필의 sync 엔드포인트 | 대신 보낼 HTTP 엔드포인트. 예: `http://localhost:8080` |
| `--local` | 꺼짐 | 개발용: localhost 서버로 전송. `CCTRACE_LOCAL_SYNC_ENDPOINT`·`CCTRACE_LOCAL_OTEL_ENDPOINT` 또는 `HTTP_PORT`·`GRPC_PORT`를 환경변수나 현재 디렉터리 기준 가장 가까운 상위 .env 파일에서 읽음. 없으면 8080·4317 포트 |

업로드 엔드포인트 우선순위: `--endpoint`, 프로필의 sync 엔드포인트, 프로필의 OTEL 엔드포인트 순.

훅은 숨김 플래그 `--log-to-file`도 넘긴다. 프로세스 자체 출력을 프로필 로그로 보내는 플래그다. 수동 실행에는 필요 없다.

## 업로드 대상

- Claude 홈의 `projects` 디렉터리 아래 Claude Code 세션 로그
- 프로필에서 켠 경우 Codex CLI·GJC·OMO 세션 파일. [Codex CLI](../agents/codex.md), [GJC와 OMO](../agents/gjc-omo.md) 참고

에이전트별 첫 동기화는 디스크에 이미 있는 내용을 건너뛴다. 기존 파일은 현재 끝까지 읽은 것으로 표시하고 그 뒤에 쓰인 내용만 올린다. 따라서 설치 이전 기록은 올라가지 않는다. 예외는 데몬을 띄운 `SessionStart` 훅의 Claude Code 세션으로, 처음부터 읽는다. 이렇게 건너뛴 파일 수는 `cctrace status`에 표시.

데몬은 시작할 때마다 Claude 설정 파일을 현재 바이너리가 쓸 내용과 비교해 다르면 다시 쓴다.

업로드가 실패하면 간격을 늘려 재시도한다(최대 약 5분 간격, 서버가 요청하면 더 길게). 서버에 닿지 못해 30분 동안 전송이 실패했고 `SessionStart` 훅이 설치되어 있으면, 데몬은 잠금을 풀고 종료해 다음 세션이 새 데몬을 띄우게 한다. 읽기 위치는 보존되므로 유실 없음.

## 프로필이 함께 쓰는 출처 { #sources-shared-between-profiles }

업로드 목적지가 같은 두 프로필이 수집 폴더를 함께 쓰면 데몬은 한 프로필(맡은 프로필)만 겹친 출처를 수집하게 하고 다른 프로필은 그 출처에서만 막는다. 각 프로필의 나머지는 계속 수집한다. 어느 프로필이 맡는지, 어떻게 옮기는지, 폴더가 중첩이면 무슨 뜻인지는 [프로필](profiles.md#source-folders-that-profiles-share)에 있다. 출처는 데몬이 판단한 시점부터 막힌다. 저장된 읽기 위치가 전혀 없이 시작하는 프로필은 위의 첫 동기화 건너뛰기처럼 그 기간의 세션을 건너뛴다. 저장된 읽기 위치가 이미 있는 프로필은 거기서 이어 읽으므로 그 기간의 파일도 보낼 수 있고, 서버는 이미 받은 것을 걸러 낸다.

일회성 `cctrace sync`, `SessionEnd` 실행, 재보강도 같은 판단을 따른다. 실행 중인 데몬이 그 프로필에 읽게 할 출처만 읽고, 빼 둔 출처는 stderr에, 백그라운드 실행이면 로그에 알린다.

```text
profile=work [routes] kind=codex source=3fa91c2d was not read: another profile holds it
```

폴더는 표기나 출처가 아니라 어느 폴더인지로 판단한다. 플래그나 환경 변수로 받은 폴더도 데몬의 판단이 업로드 목적지가 같은 다른 프로필에 준 출처와 겹치면 읽지 않는다. 출처 id를 알 수 없으면(예: routes 파일을 쓸 수 없는 동안) 줄이 `source=-`로 나온다. 프로필이 방금 넘겨받은 출처도 이런 실행은 읽지 않는다(`it was just handed to this profile, and the daemon reads it first`). 데몬의 작업이 이전 담당의 읽기 위치에서 먼저 읽는다.

### `cctrace sync status`

`cctrace sync status`는 실행 중인 데몬이 마지막으로 게시한 내용을 표로 출력한다. `--json`은 표 대신 원본 스냅샷을 출력한다. 구역은 행이 있을 때만 나온다.

```text
daemon   pid 4121   version 1.2.3   started 2026-10-15T09:12:03Z   observed 3s ago   complete yes

PROFILE   STATE    SENT  LAST PASS             NEXT RETRY  CODE
default   healthy  1204  2026-10-15T09:31:10Z  -           -
work      healthy   311  2026-10-15T09:31:08Z  -           -
personal  blocked     0  -                     -           duplicate_route

SHARED SOURCES (collected by another profile, not by the one on the left)
PROFILE   KIND    SOURCE    LABEL    REASON  HELD BY  HOLDER STATE
work      codex   3fa91c2d  default  same    default  active
personal  claude  77aa10c2  home     same    default  active
```

| 열 | 뜻 |
|---|---|
| `CODE` | 프로필의 모든 출처가 공유 출처여서 수집할 것이 없으면 `duplicate_route` |
| `SOURCE` | 출처 id. `cctrace profile claim --source`가 받는 값 |
| `LABEL` | 폴더를 얻은 곳: `home`, `default`, `env`, `configured`, `explicit` |
| `REASON` | 같은 폴더면 `same`, 폴더가 서로 안에 있으면 `nested`(일부 파일은 어느 프로필에도 수집되지 않을 수 있음) |
| `HELD BY`, `HOLDER STATE` | 맡은 프로필과 그 상태: `active`, `stopped`, `disabled`, `blocked`, `transferring`, `removed`, `unowned`, `unknown` |

세 번째 구역 `DOWNGRADE`는 데몬의 이전 빌드가 표현하지 못하는 출처를 나열하고, 행마다 해소 명령 `cctrace profile claim <profile> --source <id>`를 붙인다. 그런 빌드로 되돌릴 때만 의미가 있다.

### 데몬 로그

데몬은 판단이 바뀔 때 `[routes]` 줄을 한 번 쓴다. 주기마다 쓰지 않는다. 줄에는 이름, 개수, id만 있고 폴더, 주소, 토큰은 없다. `excluded` 같은 상태 줄은 그 상태가 끝났다가 다시 생길 때만 다시 쓴다. 로그는 영어다.

| 줄 | 뜻 |
|---|---|
| `[routes] reserved profile=default kind=codex source=3fa91c2d label=default destination=1a2b3c4d` | 프로필이 출처를 맡음. `destination`은 해시의 앞 8자 |
| `[routes] excluded profile=work kind=codex source=3fa91c2d label=default reason=same holder=default holder_state=active` | 프로필이 출처에서 막힘 |
| `[routes] fully-shared profile=work sources=1 holders=default` | 프로필의 모든 출처가 막힘 |
| `[routes] releasing source=3fa91c2d holder=default to=work cause=claim` | 맡은 프로필이 출처를 놓는 중. `to`는 이어받을 프로필(없으면 `to=none`), `cause`는 `claim`, `source_removed`, `collision` 중 하나 |
| `[routes] releasing source=3fa91c2d holder=work to=none cause=collision` | 따로 맡던 두 출처가 한 출처임이 드러남(예: 폴더를 다른 폴더로 가는 링크로 바꿈). 나중에 정해진 담당을 놓는 중이며, 그 담당 프로필이 읽기를 멈출 때까지 어느 프로필도 이 출처를 읽지 않음 |
| `[routes] released source=3fa91c2d holder=default cause=source_removed successor=none` | 맡은 프로필이 출처를 놓았고 이어받은 프로필이 없음 |
| `[routes] removed source=3fa91c2d holder=work cause=collision` | 나중에 정해진 담당의 프로필이 읽기를 멈춰 그 담당을 지움. 이제 먼저 정해진 담당의 프로필이 이 출처를 읽음 |
| `[routes] handoff source=3fa91c2d from=default to=work` | 출처의 담당이 바뀜 |
| `[routes] orphan source=3fa91c2d former_holder=default` | 맡은 프로필이 삭제됨. claim할 때까지 아무도 읽지 않음 |
| `[routes] invalid reason=unreadable` | routes 파일을 쓸 수 없음. `cctrace profile claim`이 다시 만들 때까지 겹친 출처는 아무도 읽지 않음 |
| `[routes] write-failed rev=8 reason=io` | routes 파일에 쓰지 못해 새 판단을 적용하지 않고 다시 시도함. 실패가 이어지는 동안 한 번만 씀. `reason`은 `io`, 또는 파일을 거절한 검사의 단어 |

## 서버 정책 { #server-policy }

서버 운영자는 클라이언트를 새 서버 주소와 새 인증 기관으로 옮기는 서명된 정책을 하나 게시할 수 있다. 클라이언트는 바이너리에 링크된 업데이트 공개키 또는 정책 공개키로 정책을 검증하므로 그 키로 빌드한 클라이언트만 정책을 적용한다. 이 저장소에서 업데이트 공개키도 정책 공개키도 넘기지 않고 빌드한 클라이언트에는 공개키가 없다([클라이언트 업그레이드](#upgrading-the-client) 참고). 스스로 정책 확인을 시작하지 않으며, 직접 실행한 `cctrace config policy apply`는 `policy public key is not configured; rebuild with main.policyPublicKeyBase64 or main.updatePublicKeyBase64`로 실패한다. 자기 공개키로 빌드한 클라이언트는 키를 `UPDATE_PUBLIC_KEY` 또는 `POLICY_PUBLIC_KEY` Make 변수나 Docker 빌드 인자로 넘겨 링크하며, 짝이 되는 개인키로 서명한 정책을 적용한다.

공개키가 있는 클라이언트는 `cctrace sync`에서 세 시점에 백그라운드로 정책 확인(`cctrace config policy apply`)을 시작한다.

- 감시 프로세스 시작 때
- 실행 중 약 5분마다
- 일회성 `cctrace sync` 종료 때

`--dry-run`, `--local`, `--endpoint`를 준 실행은 정책 확인을 시작하지 않는다.

확인은 설치 식별자와 프로필 이름을 보내 서버에 정책을 묻는다. 이 요청에는 업로드 토큰이 없다. 설치 식별자는 `cctrace`가 첫 사용 때 `~/.cctrace/installation-id`에 만드는 무작위 UUID이며 머신의 모든 프로필이 공유한다. 서버에 이 클라이언트용 정책이 없으면 아무것도 바뀌지 않는다. 있으면 적용하기 전에 정책의 새 주소를 시험하며, OTLP 주소로 가는 시험 요청에는 프로필의 업로드 토큰이 담긴다.

정책이 적용되면 클라이언트는 정책의 인증 기관을 프로필 디렉터리에 쓰고, 프로필의 서버 주소, Claude 홈의 Claude 설정 파일, 프로필의 Codex 동기화를 꺼 두지 않았다면 기본 Codex 홈, 설정된 경우 `CODEX_HOME`이 가리키는 디렉터리, `options.codex_dirs`의 각 디렉터리에 있는 Codex 설정 파일을 다시 쓴다. 프로필에 명시한 `codex_home`은 프로필의 Codex 동기화가 켜져 있으면 따로 처리한다. 그 홈의 `[otel]` 절이 cctrace가 프로필에 맞춰 생성한 절일 때만 절의 토큰은 그대로 두고 옮기며, 아니면 정책을 보류하지 않고 파일을 그대로 둔다. [서버 정책 보류](profiles.md#server-policy-deferral) 참고. 동기화 데몬이 실행 중이면 멈췄다가 실행 때와 같은 설정으로 다시 시작한다. `--local`이나 `--endpoint`로 시작한 데몬은 이 방식으로 옮길 수 없으며, 이때 `apply`는 아무것도 바꾸지 않고 오류로 끝난다. 실행 중인 Claude Code와 Codex 세션은 다음에 시작할 때부터 새 설정을 쓴다.

다른 프로필도 쓰는 파일을 바꿔야 하는 정책은 적용되지 않고 보류된다. 다만 Codex 설정 파일은 그 파일을 쓰는 프로필이 모두 같은 서버로 함께 옮기면 보류하지 않는다. [서버 정책 보류](profiles.md#server-policy-deferral) 참고. `cctrace config policy apply`를 실행할 때마다 프로필이 로그인되어 있으면 클라이언트가 결과를 서버에 보고한다. [개인정보](../reference/privacy.md) 참고.

## 로그와 상태 파일

프로필 디렉터리마다 아래 파일을 둔다. 기본 프로필은 `~/.cctrace/`, 이름 있는 프로필은 `~/.cctrace/profiles/<name>/`.

| 파일 | 내용 |
|---|---|
| `~/.cctrace/sync.log` | 데몬 로그. 10 MB에서 회전, 이전 파일 5개 보관 |
| `~/.cctrace/sync-crash.log` | 백그라운드 프로세스의 원시 출력(패닉 등). 1 MB 초과 시 비움 |
| `~/.cctrace/sync-state.json` | Claude Code 세션 파일별 읽은 위치 |
| `~/.cctrace/codex-sync-state.json`, `~/.cctrace/gjc-sync-state.json`, `~/.cctrace/omo-sync-state.json` | Codex·GJC·OMO의 같은 정보 |
| `~/.cctrace/server-policy-state.json` | 서버 정책 상태: 마지막으로 적용한 정책 번호, 최근 상태와 사유, 최근 결과 32건, 아직 남은 공유 Codex 설정 쓰기(경로, 옛 `[otel]` 절과 새 `[otel]` 절의 다이제스트, 옛 OTEL 주소와 인증서 경로; 토큰 없음) |
| `~/.cctrace/sync.lock`, `~/.cctrace/sync.pid`, `~/.cctrace/sync-runtime.json` | 단일 실행 잠금, 실행 중인 데몬의 pid와 버전 |

상태 파일은 지우지 않는다. 동기화 상태 파일이 없으면 첫 동기화로 간주되어, 다음 동기화가 모든 파일의 기존 내용을 올리지 않고 건너뛴다. 서버 정책 상태 파일이 없으면 어떤 정책을 적용했는지 잊어, 같은 정책이 다시 적용되지 않고 실패할 수 있다. 아직 남은 공유 Codex 설정 쓰기도 함께 사라진다.

## 데몬 중지

```bash
cctrace sync --stop
```

`--stop`은 데몬에 종료를 요청하고 기다린다. 실행 중인 데몬이 없으면 `no running daemon found`로 실패. 다음 Claude Code 세션이 훅으로 새 데몬을 띄운다.

데몬이 응답하지 않으면 강제 종료한다.

```bash
cctrace kill
```

`kill`은 프로필의 동기화 잠금을 쥔 프로세스를 종료하고 잠금이 풀리기를 최대 5초 기다린 뒤 실행 정보 파일을 정리한다. 잠금을 쥔 데몬이 없으면 `No running sync daemon`을 출력하고 성공 처리. `--profile`을 받고 `CCTRACE_PROFILE`을 읽는다.

두 명령 모두 프로필 하나가 대상. 이름 있는 프로필은 `--profile <name>` 추가.

## 클라이언트 업그레이드 { #upgrading-the-client }

이 저장소에서 업데이트 공개키 없이 빌드한 클라이언트는 스스로 교체되지 않는다. 스스로 교체되는 클라이언트는 [보안 운영](../server/security.md#when-a-client-updates-itself) 참고. [서버 설치](../server/install.md) 절차대로 빌드한 서버는 버전을 `dev`로 알린다. 이때 클라이언트는 갱신 확인을 건너뛴다. 서버가 더 새 릴리스 버전을 알리면 업데이트 공개키가 비어 있어 시도가 실패하고 그 이유가 `~/.cctrace/sync.log`의 `update:` 줄에 남는다. 예: `update: update public key is not configured`. 어느 경우든 수집은 현재 바이너리로 계속된다.

업그레이드 절차:

1. 클라이언트를 다시 빌드해([설치](install.md) 참고) 설치된 바이너리를 덮어쓰기
2. 새 바이너리 경로가 다르면 그 바이너리로 `cctrace env apply`를 실행해 훅 경로 갱신
3. 새 코드로 돌도록 데몬 재시작: `cctrace sync --stop` 후 `cctrace sync --daemon` 또는 새 Claude Code 세션 시작

이전 버전 데몬이 아직 돌고 있으면 `cctrace status`의 `수집 상태` 아래에 두 버전을 적은 안내가 표시된다.

## 프로필 소속·전송 제외·수집 실행

다중 홈 프로필 개편은 구현·검증 중입니다. 프로필 소속은 구성 묶음이며 전송 허용 여부나 수집 실행 상태와 별개입니다. 전송이 허용된 홈도 해당 프로필의 수집이 중지돼 있으면 전송하지 않습니다. 사용량 조회는 이 세 상태와 독립적으로 유지됩니다.

제외 계정은 목록에 계속 표시하고 마지막에 배치하며, 카드 전체의 회색·옅은 붉은 강조와 상태 문구로 구분합니다. `Exclude session uploads` 양방향 변경은 확인 후에만 저장하며, 프로필 이동·수집 시작·사용량 조회 변경을 일으키지 않습니다.

로컬 세션 전송 제외로 서버의 강제 제외를 해제할 수 없습니다. provider가 직접 보내는 OTEL과 세션 파일 업로드는 서로 다른 경로이므로, 로그 제외를 전체 텔레메트리 차단으로 해석하지 않아야 합니다.
