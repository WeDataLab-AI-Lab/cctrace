# Codex CLI

cctrace는 Codex CLI를 Codex가 서버로 보내는 OTEL 메트릭과 [동기화 데몬](../client/sync.md)이 올리는 세션 파일로 수집한다.

## 활성화

Codex를 한 번 실행해 `~/.codex`를 만든 뒤 `cctrace init`을 실행한다. `init`이 그 디렉터리를 찾으면 다음을 묻는다.

```text
  Codex CLI detected (~/.codex found).
  Enable Codex session sync? [Y/n]
```

승낙하면 프로필의 `options.codex_sync_enabled`를 켜고 `~/.codex/config.toml`에 `[otel]` 섹션을 쓴다.

`init` 이후에 Codex를 설치했다면 `cctrace init`을 다시 실행해 `Patch Codex integration only`를 고른다. 기존 엔드포인트와 토큰을 유지하고, 옵션을 켜고, 비밀번호를 묻지 않고 `[otel]` 섹션을 쓴다.

`init`은 `CODEX_CONFIG_DIR`가 있으면 그 경로를, 없으면 `~/.codex`를 Codex 홈으로 본다.

!!! note "Codex 자동 활성화 (직접 끈 경우 제외)"
    동기화는 실행될 때마다 Codex 홈을 확인한다. Codex 홈이 있고, 프로필에 OTEL 엔드포인트와 업로드 토큰이 있고, `options.codex_sync_enabled`가 꺼져 있으면 옵션을 켜고 `[otel]` 섹션을 쓴다. Codex 지원 이전에 만든 프로필을 위한 동작이다.

    `init`에서 거절하거나 `cctrace config set options.codex_sync_enabled false`를 실행하면 프로필에 `options.codex_sync_declined`로 기록되고, 동기화는 어떤 Codex 홈도 건드리지 않는다. `cctrace config set options.codex_sync_enabled true`, `init`에서 승낙, `Patch Codex integration only` 선택 중 하나로 기록이 풀린다.

## OTEL 설정

cctrace가 쓰는 섹션 예시:

```toml
[otel]
metrics_exporter = { otlp-http = { endpoint = "http://cctrace.company.example:4318/v1/metrics", protocol = "binary", headers = { Authorization = "Bearer <upload token>", X-Cctrace-Codex-Account = "<account id>" } } }
```

- 엔드포인트는 프로필의 OTEL 엔드포인트에서 유도: 포트 4317은 4318로(TLS 오버레이의 5317은 5318로), 경로는 `/v1/metrics`로. Codex는 gRPC가 아닌 OTLP/HTTP로 전송
- 메트릭 익스포터만 설정. Codex용 OTEL 로그 전송은 설정하지 않음
- `X-Cctrace-Codex-Account`는 `<Codex 홈>/auth.json`에 있는 Codex 결제 계정 id. 서버가 Codex 메트릭에 결제 계정 제외를 적용하는 근거. 계정을 모르면 생략, 계정이 바뀌면 동기화가 다시 씀. Codex를 다시 시작해야 새 섹션을 읽음
- `server.ca_cert_file`이 있으면 익스포터에 `tls = { ca-certificate = "<파일>" }`이 붙음. 사설 CA 인증서를 쓰는 `https://` 엔드포인트에 Codex가 보내려면 이 값이 필요하며, 없으면 메트릭을 보내지 않고 오류도 내지 않음. `init`·Codex 패치·동기화가 CA 없이 `https://` 엔드포인트를 쓸 때 `[!]` 경고 출력. 공인 인증서 엔드포인트에 CA 없이 Codex가 보내는지는 측정하지 않음
- 섹션 전체를 cctrace가 관리하며 헤더가 더 붙을 수 있음. 기존 `[otel]` 섹션은 인라인·테이블 형식 모두 교체, 파일의 나머지는 유지. 파일은 원자적으로 교체되며 권한은 0600
- 동기화할 때마다 현재 프로필 기준으로 만들 섹션과 비교해 다르면 다시 씀. 예: `cctrace config set`으로 OTEL 엔드포인트나 `server.ca_cert_file`을 바꾼 뒤. 프로필에 CA가 없으면 직접 넣은 `tls` 테이블은 지워짐

## 세션 파일

동기화 데몬은 각 Codex 홈 아래에서 다음 패턴에 맞는 세션 파일을 올린다.

```text
sessions/rollout-*.jsonl
sessions/YYYY/MM/DD/rollout-*.jsonl
```

스캔하는 Codex 홈(순서대로, 중복 제거):

1. `CODEX_CONFIG_DIR` 또는 `~/.codex`
2. 설정된 경우 `CODEX_HOME`(기본 홈 대신이 아니라 추가)
3. `options.codex_dirs`의 각 디렉터리

동기화는 추가 홈(2·3)의 `[otel]` 섹션도 현재 형식과 그 홈의 계정 헤더로 다시 쓴다. 단, Codex 동기화가 켜져 있고 그 홈의 `[otel]` 엔드포인트가 이미 이 cctrace 서버(같은 호스트·포트)를 가리킬 때만이다. `[otel]` 섹션이 없는 홈은 건드리지 않고, 다른 서버를 가리키는 홈은 로그에 이름을 남기고 그대로 두며, `<Codex 홈>/config.toml`이 심볼릭 링크면 메시지를 남기고 건너뛴다. 동기화는 추가 홈에 이미 있는 Bearer 토큰을 유지하고, 프로필 토큰과 다르면 `cctrace init`을 안내하는 한 줄을 출력한다. `cctrace init`과 Codex 패치는 그 토큰을 프로필 토큰으로 바꾼다.

`options.codex_dirs`에 있지만 존재하지 않는 디렉터리는 로그에 메시지를 남기고 건너뛴다. `cctrace sync --dry-run`은 실제 동기화가 스캔할 홈과 각 홈의 세션 파일 수를 보여 준다.

동기화 프로세스 환경에 `CCTRACE_CODEX_SYNC=true`를 두면 프로필 옵션과 관계없이 Codex 수집이 켜진다.

## 수집 시작

Codex에는 세션 훅이 없다. 파일은 그 프로필로 실행 중인 동기화 데몬이 수집한다.

- 같은 프로필로 Claude Code도 쓰면 Claude Code의 `SessionStart` 훅이 띄운 데몬이 Codex도 수집
- Codex만 쓰면 `cctrace sync --daemon`으로 데몬을 직접 띄우거나 `cctrace sync`를 주기적으로 실행

다른 에이전트와 마찬가지로 첫 동기화는 이미 디스크에 있는 내용을 건너뛴다. Codex를 켠 직후 `Synced 0 records`는 정상이다. 그 뒤에 쓰인 내용부터 수집된다.

## 설정 제거

`cctrace reset`과 `cctrace uninstall`은 `~/.codex/config.toml`을 바꾸지 않는다. Codex의 메트릭 전송을 멈추려면 `[otel]` 섹션을 직접 삭제.
