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

## 로그와 상태 파일

프로필 디렉터리마다 아래 파일을 둔다. 기본 프로필은 `~/.cctrace/`, 이름 있는 프로필은 `~/.cctrace/profiles/<name>/`.

| 파일 | 내용 |
|---|---|
| `~/.cctrace/sync.log` | 데몬 로그. 10 MB에서 회전, 이전 파일 5개 보관 |
| `~/.cctrace/sync-crash.log` | 백그라운드 프로세스의 원시 출력(패닉 등). 1 MB 초과 시 비움 |
| `~/.cctrace/sync-state.json` | Claude Code 세션 파일별 읽은 위치 |
| `~/.cctrace/codex-sync-state.json`, `~/.cctrace/gjc-sync-state.json`, `~/.cctrace/omo-sync-state.json` | Codex·GJC·OMO의 같은 정보 |
| `~/.cctrace/sync.lock`, `~/.cctrace/sync.pid`, `~/.cctrace/sync-runtime.json` | 단일 실행 잠금, 실행 중인 데몬의 pid와 버전 |

상태 파일은 지우지 않는다. 상태 파일이 없으면 첫 동기화로 간주되어, 다음 동기화가 모든 파일의 기존 내용을 올리지 않고 건너뛴다.

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

이 저장소에서 빌드한 클라이언트는 스스로 교체되지 않는다. [서버 설치](../server/install.md) 절차대로 빌드한 서버는 버전을 `dev`로 알린다. 이때 클라이언트는 갱신 확인을 건너뛴다. 서버가 더 새 릴리스 버전을 알리면 업데이트 공개키가 비어 있어 시도가 실패하고 그 이유가 `~/.cctrace/sync.log`의 `update:` 줄에 남는다. 예: `update: update public key is not configured`. 어느 경우든 수집은 현재 바이너리로 계속된다.

업그레이드 절차:

1. 클라이언트를 다시 빌드해([설치](install.md) 참고) 설치된 바이너리를 덮어쓰기
2. 새 바이너리 경로가 다르면 그 바이너리로 `cctrace env apply`를 실행해 훅 경로 갱신
3. 새 코드로 돌도록 데몬 재시작: `cctrace sync --stop` 후 `cctrace sync --daemon` 또는 새 Claude Code 세션 시작

이전 버전 데몬이 아직 돌고 있으면 `cctrace status`의 `수집 상태` 아래에 두 버전을 적은 안내가 표시된다.
