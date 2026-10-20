# CLI

이 가이드가 다루는 `cctrace` 클라이언트 명령. 모든 플래그는 `cctrace --help`, `cctrace <command> --help`로 확인.

## 설정

| 명령 | 플래그 | 동작 |
|---|---|---|
| `cctrace init` | `--profile <name>` | 서버 인증, 프로필과 에이전트 설정 기록. [연결](../client/setup.md) 참고 |
| `cctrace status` | `--profile <name>` | 사용자 정보, 엔드포인트, 수집 상태, 경로, 옵션, 사용량 표시. 종료 코드: 프로필 없음 1, OTEL 엔드포인트 도달 불가 2 |
| `cctrace env apply` | `--profile <name>`, `--all` | 저장된 프로필과 현재 바이너리 경로로 Claude 설정 파일의 OTEL 변수와 훅 재기록 |
| `cctrace config list` | `--profile <name>` | 프로필 설정 목록, 토큰은 가림. `cctrace config`만 실행해도 동일 |
| `cctrace config get <key>` | `--profile <name>` | 설정 하나 출력 |
| `cctrace config set <key> <value>` | `--profile <name>` | 설정 하나 변경 후 Claude 설정 파일 재기록. 키 목록은 [연결](../client/setup.md#view-and-change-settings) 참고 |
| `cctrace config policy status` | `--profile <name>` | 설치 식별자, 프로필 이름, 프로필의 서버 정책 상태를 JSON으로 출력. 서버에 접속하지 않음 |
| `cctrace config policy check` | `--profile <name>` | 서버의 서명된 정책을 받아 검증하고 새 주소 접속을 시험한 뒤, 정책과 적용 시 바뀔 파일을 출력(적용할 정책이 없거나 이미 적용했으면 `status`와 같은 상태를 출력). 설정과 정책 상태는 쓰지 않음. 업데이트 공개키 또는 정책 공개키 필요 |
| `cctrace config policy apply` | `--profile <name>` | 서버의 서명된 정책을 받아 검증·적용한 뒤, 프로필이 로그인되어 있으면 결과를 서버에 보고. 업데이트 공개키 또는 정책 공개키 필요 |

`config policy`는 [동기화 데몬](../client/sync.md#server-policy)에서 설명하는 서명된 서버 정책을 다룬다. `check`와 `apply`는 클라이언트에 링크된 업데이트 공개키 또는 정책 공개키로 정책을 검증한다. 이 저장소에서 업데이트 공개키도 정책 공개키도 넘기지 않고 빌드한 클라이언트에는 공개키가 없어 둘 다 `policy public key is not configured; rebuild with main.policyPublicKeyBase64 or main.updatePublicKeyBase64`로 끝나고, `status`는 그대로 동작한다. 프로필이 로그인되어 있으면 `apply`는 이 실패도 서버에 보고한다. 설치 식별자는 `~/.cctrace/installation-id`에 저장되는 무작위 UUID이며, `status`·`check`·`apply` 중 먼저 실행한 명령이 만든다. 보고에 담기는 내용은 [개인정보](privacy.md)에 정리.

## 프로필

| 명령 | 플래그 | 동작 |
|---|---|---|
| `cctrace profile add <name>` | `--home <dir>` (필수) | 기본 프로필을 다른 Claude 홈용 이름 있는 프로필로 복사하고 그 홈에 연결 |
| `cctrace profile list` | | 이름 있는 프로필과 Claude 홈 목록 |
| `cctrace profile edit <name>` | `--home <dir>`, `--codex-home <dir>` | 이름 있는 프로필의 Claude 홈이나 Codex 홈 변경. 설정 파일은 다시 쓰지 않음. 새 Claude 홈에 OTEL 변수와 훅을 쓰려면 이어서 `cctrace env apply --profile <name>` 실행 |
| `cctrace profile remove <name>` | `--force` | 이름 있는 프로필 디렉터리 삭제. Claude 설정 항목은 남음, 함께 지우려면 `cctrace reset --profile` |
| `cctrace profile claim <name>` | `--source <id\|folder>`(반복 가능), `--all`, `--force` | 다른 프로필과 함께 쓰는 출처를 이 프로필에 맡김. 맡고 있던 프로필이 먼저 놓음. 로컬 데몬 실행 필요, `--force`가 없으면 확인을 받음 |

[프로필](../client/profiles.md) 참고.

## 동기화

| 명령 | 동작 |
|---|---|
| `cctrace sync` | 기본 프로필과 모든 이름 있는 프로필을 한 번씩 동기화 |
| `cctrace sync --daemon` | 프로필 하나의 백그라운드 데몬 시작 |
| `cctrace sync --stop` | 데몬 정상 종료 |
| `cctrace sync status` | 실행 중인 데몬이 마지막으로 게시한 프로필과 막힌 출처를 표로 출력. `--json`은 원본 스냅샷 출력 |
| `cctrace kill` | 프로필의 동기화 잠금을 쥔 데몬 강제 종료. `--profile` 지원 |

| 플래그 | 기본값 | 의미 |
|---|---|---|
| `--watch` | 꺼짐 | 포그라운드에서 계속 폴링 |
| `--interval <duration>` | `30s` | 감시 모드 폴링 주기 |
| `--daemon` | 꺼짐 | 백그라운드 실행 |
| `--once` | 꺼짐 | `--daemon`과 함께: 한 번 동기화 후 종료 |
| `--stop` | 꺼짐 | 실행 중인 데몬 중지 |
| `--dry-run` | 꺼짐 | 전송 없이 보낼 내용 표시 |
| `--claude-dir <dir>` | 프로필의 Claude 홈 | 읽을 Claude 설정 디렉터리 |
| `--profile <name>` | `CCTRACE_PROFILE` | 이름 있는 프로필 |
| `--auto-profile` | 꺼짐 | `--claude-dir` 또는 `CLAUDE_CONFIG_DIR`과 Claude 홈이 일치하는 프로필 선택 |
| `--profile-email <email>` | `CCTRACE_PROFILE_EMAIL` | 레코드에 붙는 이메일 대체 |
| `--endpoint <url>` | 프로필의 sync 엔드포인트 | 업로드 엔드포인트 대체 |
| `--local` | 꺼짐 | 개발용: localhost 서버로 전송 |

자세한 내용은 [동기화 데몬](../client/sync.md) 참고.

## 로컬 분석 { #local-analysis }

이 머신의 세션 로그를 브라우저의 대시보드로 제공하는 명령. 제공 자체는 아무것도 업로드하지 않으며, `local enable`을 써도 동기화 데몬은 프로필 옵션대로 계속 업로드함. [로컬 분석](../client/local.md) 참고.

| 명령 | 플래그 | 동작 |
|---|---|---|
| `cctrace local serve` | `--origin` (필수), `--address`, `--claude-dir`, `--codex-dir` | 루프백 주소에서 로컬 세션 API를 포그라운드로 중지할 때까지 제공. 엔드포인트와 새 무작위 토큰을 JSON으로 출력. 토큰은 저장하지 않음 |
| `cctrace local enable` | `--origin` (필수), `--profile <name>` | 프로필의 동기화 데몬이 해당 origin에 한해 고정 루프백 포트로 로컬 세션 API를 제공하도록 허용. 엔드포인트와 토큰을 한 번만 출력하고 토큰은 해시만 보관. 다시 실행하면 새 토큰 발급과 새 포트 선택 |
| `cctrace local status` | `--profile <name>` | 로컬 분석의 활성·실행 여부와 origin, 주소를 JSON으로 출력 |
| `cctrace local disable` | `--profile <name>` | 토큰을 폐기하고 프로필의 로컬 분석 끄기. 제공 중인 데몬은 제공 중단 |

| `local serve` 플래그 | 기본값 | 의미 |
|---|---|---|
| `--origin <origin>` | 필수 | API 호출을 허용할 웹 origin. 여러 번 지정 가능 |
| `--address <ip:port>` | `127.0.0.1:0` | 수신할 루프백 IP 주소와 포트. 포트 0은 빈 포트 자동 선택 |
| `--claude-dir <dir>` | 아래 참고 | 읽을 Claude 설정 디렉터리. 여러 번 지정 가능 |
| `--codex-dir <dir>` | 아래 참고 | 읽을 Codex 홈. 여러 번 지정 가능 |

`--claude-dir`과 `--codex-dir`을 모두 주지 않으면 `local serve`는 기본 Claude 홈, 기본 Codex 홈과 `CODEX_HOME`, 프로필마다 설정된 Claude·Codex 홈을 읽는다. 둘 중 하나라도 주면 준 디렉터리만 읽는다.

## 제거

| 명령 | 플래그 | 동작 |
|---|---|---|
| `cctrace reset` | `--profile <name>`, `--all`, `--force` | 프로필과 그 Claude 설정 항목 제거. 기본 프로필의 동기화 상태는 유지 |
| `cctrace uninstall` | `--force` | 모든 프로필 초기화, `~/.cctrace` 삭제, `PATH`의 `cctrace` 바이너리 삭제 |

[초기화와 제거](../client/uninstall.md) 참고.

## 클라이언트가 읽는 환경변수

| 변수 | 사용 명령 | 효과 |
|---|---|---|
| `CCTRACE_PROFILE` | `sync`, `kill`, `config policy` | `--profile`이 없을 때의 이름 있는 프로필 |
| `CCTRACE_PROFILE_EMAIL` | `sync` | `--profile-email`이 없을 때 레코드에 붙는 이메일 |
| `CLAUDE_CONFIG_DIR` | `sync --auto-profile` | `--claude-dir`이 없을 때 대조할 Claude 홈 |
| `CODEX_CONFIG_DIR` | `init`, `sync` | `~/.codex` 대신 쓸 Codex 홈 |
| `CODEX_HOME` | `sync` | 추가로 스캔할 Codex 홈 |
| `CCTRACE_CODEX_SYNC`, `CCTRACE_GJC_SYNC`, `CCTRACE_OMO_SYNC` | `sync` | `true`면 프로필 옵션과 관계없이 해당 에이전트 수집 |

## 기타 명령

조회·분석 명령은 존재하지만 변경이 진행 중이라 이 문서에서 다루지 않음: `report`, `usage`, `ls`, `events`, `projects`, `sessions`, `tools`, `plugins`, `skills`, `rules`, `organization-insights`, `insights`, `backfill-quota`, `auth read`.
