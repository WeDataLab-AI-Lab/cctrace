# GJC와 OMO

cctrace는 [동기화 데몬](../client/sync.md)이 세션 파일을 올리는 방식으로 GJC와 OMO 세션을 수집한다. 두 에이전트 모두 OTEL 설정과 세션 훅이 없다.

## 활성화

에이전트를 한 번 실행해 홈 디렉터리(`~/.gjc` 또는 `~/.omo`)를 만든 뒤 `cctrace init`을 실행한다. `init`은 찾은 디렉터리마다 묻는다.

```text
  gjc detected (~/.gjc found).
  Enable gjc session sync? [Y/n]
```

승낙하면 프로필의 `options.gjc_sync_enabled` 또는 `options.omo_sync_enabled`를 켠다. 에이전트 자체 설정에는 아무것도 쓰지 않는다.

`init` 이후에 켜려면:

```console
$ cctrace config set options.gjc_sync_enabled true
$ cctrace config set options.omo_sync_enabled true
```

동기화 프로세스 환경에 `CCTRACE_GJC_SYNC=true` 또는 `CCTRACE_OMO_SYNC=true`를 두어도 프로필 옵션과 관계없이 같은 효과.

## 수집 시작

옵션을 켜는 것만으로는 아무것도 시작되지 않는다. 파일은 그 프로필로 실행 중인 동기화 데몬이 수집한다.

- 같은 프로필로 Claude Code도 쓰면 `SessionStart` 훅이 띄운 데몬이 GJC·OMO도 수집
- 그렇지 않으면 `cctrace sync --daemon` 또는 `cctrace sync --watch`로 직접 띄우거나 `cctrace sync`를 주기적으로 실행

첫 동기화는 디스크에 이미 있는 내용을 건너뛰므로 옵션을 켜기 전의 세션은 올라가지 않는다.

## 읽는 파일

스캔하는 홈: GJC는 `~/.gjc`와 `options.gjc_dirs`의 각 디렉터리, OMO는 `~/.omo`와 `options.omo_dirs`의 각 디렉터리. 각 홈 아래에서 읽는 파일:

```text
GJC   agent/sessions/v2-*/*.jsonl        세션 파일
      agent/sessions/v2-*/*/*.jsonl      서브에이전트 기록
OMO   sessions/--*--/*.jsonl
      agent/sessions/--*--/*.jsonl
```

설정했지만 존재하지 않는 디렉터리는 로그에 메시지를 남기고 건너뛴다. `cctrace sync --dry-run`은 실제 동기화가 스캔할 홈과 각 홈의 세션 파일 수를 보여 준다.

동기화 진행 상태는 `~/.cctrace/gjc-sync-state.json`, `~/.cctrace/omo-sync-state.json`(이름 있는 프로필은 그 프로필 디렉터리)에 보관.
