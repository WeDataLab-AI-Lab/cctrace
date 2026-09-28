# Claude Code

cctrace는 Claude Code를 두 경로로 수집한다. Claude Code가 서버로 직접 보내는 OTEL 텔레메트리와 [동기화 데몬](../client/sync.md)이 올리는 세션 로그다. 둘 다 Claude Code 설정 파일에서 구성된다.

## 설정 기록 위치

`cctrace init`, `cctrace env apply`, `cctrace profile add`, `cctrace config set`은 프로필의 Claude 홈에 있는 설정 파일에 쓴다. 기본 프로필은 `~/.claude/settings.json`, 이름 있는 프로필은 그 프로필이 가리키는 디렉터리. [프로필](../client/profiles.md) 참고.

cctrace가 관리하지 않는 키는 모두 유지된다. 쓰기 전에 스냅숏을 떠서 자기 키 외의 값이 바뀌면 쓰기를 거부하고, 유효한 JSON이 아닌 설정 파일도 덮어쓰지 않는다. 파일은 원자적으로 교체되며 권한은 0600.

## OTEL 환경변수

`env` 아래 설정되는 변수:

| 변수 | 값 |
|---|---|
| `CLAUDE_CODE_ENABLE_TELEMETRY` | `1` |
| `OTEL_METRICS_EXPORTER` | `otlp` |
| `OTEL_LOGS_EXPORTER` | `otlp` |
| `OTEL_METRICS_INCLUDE_ACCOUNT_UUID` | `1` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | 프로필의 프로토콜, 기본 `grpc` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 프로필의 OTEL 엔드포인트 |
| `OTEL_EXPORTER_OTLP_HEADERS` | `Authorization=Bearer <upload token>` |
| `OTEL_METRIC_EXPORT_INTERVAL` | 기본 `60000` (`options.metrics_export_interval`) |
| `OTEL_LOGS_EXPORT_INTERVAL` | 기본 `5000` (`options.logs_export_interval`) |
| `OTEL_BSP_MAX_QUEUE_SIZE` | `4096` |
| `OTEL_BSP_SCHEDULE_DELAY` | `5000` |
| `OTEL_BSP_MAX_EXPORT_BATCH_SIZE` | `512` |
| `OTEL_BSP_EXPORT_TIMEOUT` | `30000` |
| `OTEL_RESOURCE_ATTRIBUTES` | 프로필의 사용자 ID·이름·이메일·팀, URL 이스케이프(아래 예시) |

```text
OTEL_RESOURCE_ATTRIBUTES=user.id=alice,user.name=Alice%20Doe,user.profile.email=alice@example.com,user.team=platform
```

프로필 값이 빈 속성은 생략된다.

이 키들은 cctrace 소유. 사용자가 직접 넣은 값이 있어도 `init`이 덮어쓰고 `reset`이 지운다.

## 세션 훅

세션 로그 동기화(`options.sync_enabled`)가 켜져 있으면 `hooks` 아래 훅 두 개가 `matcher`가 빈 그룹에 추가된다.

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/cctrace sync --daemon --log-to-file --claude-dir /Users/alice/.claude --auto-profile --interval 1s"
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/cctrace sync --daemon --once --log-to-file --claude-dir /Users/alice/.claude --auto-profile",
            "async": true,
            "timeout": 30
          }
        ]
      }
    ]
  }
}
```

- 명령은 훅을 쓴 바이너리의 절대 경로로 시작. 임시 디렉터리의 바이너리는 쓰지 않고 `PATH`에서 찾은 `cctrace`로 대체. 바이너리를 옮긴 뒤에는 `cctrace env apply` 실행
- `--claude-dir`은 프로필의 Claude 홈, `--auto-profile`은 일치하는 프로필 선택. 플래그는 [동기화 데몬](../client/sync.md) 참고
- `SessionStart`는 프로필의 데몬 시작. `SessionEnd`는 30초 제한의 비동기 동기화를 한 번 더 실행하고 데몬은 유지
- 기존 훅은 유지. cctrace 훅이 이미 있으면 새로 추가하지 않고 그 자리에서 갱신

나중에 `options.sync_enabled`를 꺼도 훅은 지워지지 않는다. 이때 훅은 실행되지만 동기화 없이 종료한다. 훅 제거는 `cctrace reset`.

## 자가 복구 스탬프

cctrace는 생성한 내용의 해시를 담은 최상위 `cctrace` 객체도 쓴다. 동기화 데몬은 시작할 때마다 해시를 다시 계산하고, 업그레이드된 바이너리가 다른 변수나 훅을 쓸 상황이면 설정 파일을 다시 쓴다. `reset`이 이 객체를 지운다.

## 변경 반영 시점

Claude Code는 세션 시작 시점에 환경변수와 훅을 읽는다. `init`, `env apply`, `config set` 뒤에는 새 Claude Code 세션 시작. 이미 실행 중이던 세션은 텔레메트리를 보내지 않는다. 그 세션 로그는 데몬이 처음 발견한 지점부터만 올라간다.
