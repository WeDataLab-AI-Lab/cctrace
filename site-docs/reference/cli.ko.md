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

## 프로필

| 명령 | 플래그 | 동작 |
|---|---|---|
| `cctrace profile add <name>` | `--home <dir>` (필수) | 기본 프로필을 다른 Claude 홈용 이름 있는 프로필로 복사하고 그 홈에 연결 |
| `cctrace profile list` | | 이름 있는 프로필과 Claude 홈 목록 |
| `cctrace profile remove <name>` | `--force` | 이름 있는 프로필 디렉터리 삭제. Claude 설정 항목은 남음, 함께 지우려면 `cctrace reset --profile` |

[프로필](../client/profiles.md) 참고.

## 동기화

| 명령 | 동작 |
|---|---|
| `cctrace sync` | 기본 프로필과 모든 이름 있는 프로필을 한 번씩 동기화 |
| `cctrace sync --daemon` | 프로필 하나의 백그라운드 데몬 시작 |
| `cctrace sync --stop` | 데몬 정상 종료 |
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

## 제거

| 명령 | 플래그 | 동작 |
|---|---|---|
| `cctrace reset` | `--profile <name>`, `--all`, `--force` | 프로필과 그 Claude 설정 항목 제거. 기본 프로필의 동기화 상태는 유지 |
| `cctrace uninstall` | `--force` | 모든 프로필 초기화, `~/.cctrace` 삭제, `PATH`의 `cctrace` 바이너리 삭제 |

[초기화와 제거](../client/uninstall.md) 참고.

## 클라이언트가 읽는 환경변수

| 변수 | 사용 명령 | 효과 |
|---|---|---|
| `CCTRACE_PROFILE` | `sync`, `kill` | `--profile`이 없을 때의 이름 있는 프로필 |
| `CCTRACE_PROFILE_EMAIL` | `sync` | `--profile-email`이 없을 때 레코드에 붙는 이메일 |
| `CLAUDE_CONFIG_DIR` | `sync --auto-profile` | `--claude-dir`이 없을 때 대조할 Claude 홈 |
| `CODEX_CONFIG_DIR` | `init`, `sync` | `~/.codex` 대신 쓸 Codex 홈 |
| `CODEX_HOME` | `sync` | 추가로 스캔할 Codex 홈 |
| `CCTRACE_CODEX_SYNC`, `CCTRACE_GJC_SYNC`, `CCTRACE_OMO_SYNC` | `sync` | `true`면 프로필 옵션과 관계없이 해당 에이전트 수집 |

## 기타 명령

조회·분석 명령은 존재하지만 변경이 진행 중이라 이 문서에서 다루지 않음: `report`, `usage`, `ls`, `events`, `projects`, `sessions`, `tools`, `plugins`, `skills`, `rules`, `organization-insights`, `insights`, `backfill-quota`, `auth read`.
