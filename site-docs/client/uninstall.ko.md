# 초기화와 제거

`cctrace reset`은 프로필 하나와 그 Claude Code 연결을 지운다. `cctrace uninstall`은 모든 프로필, `~/.cctrace` 디렉터리, 바이너리를 지운다.

두 명령 모두 서버에 이미 올라간 데이터는 지우지 않는다.

## 시작 전 확인

지울 프로필마다 동기화 데몬을 먼저 멈춘다. 두 명령 모두 실행 중인 데몬을 멈추지 않는다.

```console
$ cctrace sync --stop
$ cctrace sync --stop --profile work
```

## 초기화

```bash
cctrace reset
```

`Remove default cctrace configuration? [y/N]` 확인 후 다음을 수행한다.

1. 기본 프로필의 Claude 설정 파일(프로필이 다른 Claude 홈을 지정하지 않았다면 `~/.claude/settings.json`)에서 cctrace 항목 제거
    - `env` 아래 cctrace가 관리하는 OTEL 변수. 사용자가 직접 넣은 값이어도 제거
    - cctrace의 `SessionStart`·`SessionEnd` 훅, 그로 인해 비게 된 훅 그룹과 이벤트
    - `cctrace` 스탬프 객체
2. `~/.cctrace/profile.json` 삭제

`~/.cctrace` 아래 나머지는 유지된다: 동기화 상태 파일, 로그, 이름 있는 프로필. `reset`은 남은 이름 있는 프로필을 알려 준다. 동기화 상태가 남으므로 `cctrace init`을 다시 실행하면 수집이 멈춘 지점부터 이어진다.

| 플래그 | 효과 |
|---|---|
| `--profile <name>` | 해당 이름 있는 프로필만 초기화: 그 Claude 홈 설정 파일의 항목 제거, 프로필의 동기화 상태와 로그를 포함해 `~/.cctrace/profiles/<name>/` 삭제 |
| `--all` | 기본 프로필, 이어서 모든 이름 있는 프로필 초기화 |
| `--force` | 확인 프롬프트 생략 |

`reset`은 `~/.codex/config.toml`을 바꾸지 않는다. Codex의 메트릭 전송을 멈추려면 그 파일의 `[otel]` 섹션을 직접 삭제. [Codex CLI](../agents/codex.md) 참고.

## 제거

```bash
cctrace uninstall
```

수행할 작업을 나열하고 `Proceed? [y/N]`을 묻는다. `y`만 진행. `--force`로 프롬프트 생략. 이후 순서:

1. `cctrace reset --all --force`와 같은 동작: 모든 프로필의 Claude 홈 설정 파일에서 cctrace 항목 제거, 모든 프로필 삭제
2. 동기화 상태와 로그를 포함해 `~/.cctrace` 디렉터리 전체 삭제
3. `PATH`의 디렉터리, `/usr/local/bin`, `~/go/bin`에서 찾은 `cctrace` 바이너리 전부 삭제. 심볼릭 링크는 가리키는 파일로 해석. macOS·Linux에서 지우지 못한 파일은 `sudo rm`으로 재시도. Windows에서 사용 중인 바이너리는 이름을 바꾼 뒤 몇 초 후 삭제

지우지 못한 바이너리는 `remove manually`와 함께 표시된다.

!!! note "참고"
    `uninstall`은 `~/.cctrace`와 함께 동기화 상태도 지운다. 나중에 다시 설치하면 첫 동기화가 모든 세션 파일의 기존 내용을 과거 기록으로 보고 건너뛴다.

`reset`과 마찬가지로 `uninstall`도 `~/.codex/config.toml`의 `[otel]` 섹션은 그대로 둔다.
