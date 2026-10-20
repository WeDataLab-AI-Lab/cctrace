# 프로필

한 머신에서 Claude Code를 여러 계정으로 쓸 때 Claude 설정 디렉터리마다 프로필을 하나씩 두는 방법.

## 프로필과 계정의 대응

Claude Code는 로그인마다 설정 디렉터리를 따로 둔다. 기본은 `~/.claude`, 다른 디렉터리는 `CLAUDE_CONFIG_DIR`로 지정한다. cctrace도 같은 단위로 나눈다.

| 프로필 | 저장 위치 | Claude 홈 |
|---|---|---|
| 기본 | `~/.cctrace/profile.json` | `~/.claude` |
| 이름 있는 프로필, 예: `work` | `~/.cctrace/profiles/work/profile.json` | 프로필에 지정한 디렉터리, 예: `~/.claude-work` |

프로필마다 서버 엔드포인트, cctrace 사용자 정보, 업로드 토큰, 옵션을 따로 갖는다. OTEL 변수와 훅은 자기 Claude 홈의 설정 파일에 쓴다. 동기화 데몬과 그 잠금·로그·동기화 상태도 프로필별. [동기화 데몬](sync.md) 참고.

프로필이 어느 cctrace 사용자로 보고되는지는 만든 방법에 따라 다르다.

- `cctrace init --profile <name>`: 별도로 인증하므로 기본 프로필과 다른 cctrace 사용자 가능
- `cctrace profile add`, `init`이 감지한 디렉터리: 기본 프로필의 사용자·서버·옵션을 복사하므로 같은 cctrace 사용자

프로필 이름 규칙: 영문자나 숫자로 시작, 영문자·숫자·`-`·`_`만 허용, 최대 63자. `default`는 예약어.

## init 중 자동 감지

기본 프로필로 `cctrace init`을 마칠 때, 홈 디렉터리에서 이름이 `.claude-`로 시작하고 projects 디렉터리나 설정 파일(settings.json)을 가진 디렉터리를 찾는다. `.claude-mem`과 다른 프로필이 이미 쓰는 디렉터리는 제외.

```text
  Detected 2 additional Claude home(s):
    1. /Users/alice/.claude-work
    2. /Users/alice/.claude-personal

  Set up sync for which? (all/none/1,2,...) [all]:
```

선택한 디렉터리마다 앞의 점을 뺀 디렉터리 이름(`claude-work`)으로 이름 있는 프로필을 만들고 기본 프로필을 복사한 뒤 그 디렉터리의 설정 파일을 쓴다.

## 프로필 추가

```console
$ cctrace profile add claude-4 --home ~/.claude-4
```

`profile add`는 기본 프로필을 복사해 `--home`에 준 디렉터리를 가리키게 하고 그 디렉터리의 설정 파일에 OTEL 변수와 훅을 쓴다. `--home` 필수, 기본 프로필이 먼저 있어야 한다.

이미 있는 이름, 다른 이름 있는 프로필이 쓰는 Claude 홈은 거부한다. 디렉터리가 아직 없으면 경고만 하고 프로필은 만든다.

별도 로그인으로 프로필을 만들려면 이름을 주고 init을 실행한다.

```console
$ cctrace init --profile work
```

전체 설정 절차에 `Claude config directory (Enter for default ~/.claude)` 프롬프트 하나가 추가된다. 입력한 디렉터리를 다른 프로필이 이미 쓰면 경고한다.

## 프로필 목록

```bash
cctrace profile list
```

기본 프로필(설정된 뒤)과 이름 있는 프로필을 각 Claude 홈과 함께 출력. `cctrace status`도 둘 다 보여 준다.

## 프로필 삭제

```bash
cctrace profile remove work
```

확인을 받은 뒤 `~/.cctrace/profiles/` 아래 해당 프로필 디렉터리를 지운다(`--force`로 확인 생략). 프로필의 Claude 설정 파일은 건드리지 않으므로 OTEL 변수와 훅이 남는다. 이것까지 지우려면 대신 `cctrace reset --profile work` 사용. [초기화와 제거](uninstall.md) 참고.

삭제한 프로필이 다른 프로필과 함께 쓰는 출처를 맡고 있었다면, 다른 프로필이 `claim`할 때까지 그 출처는 어느 프로필도 수집하지 않는다. [프로필이 함께 쓰는 수집 폴더](#source-folders-that-profiles-share) 참고.

## 명령줄에서 프로필 지정

프로필 하나를 대상으로 하는 명령은 `--profile <name>`을 받는다: `status`, `config`(`config policy` 포함), `env apply`, `sync`, `kill`, `reset`, `local enable`, `local status`, `local disable`. `sync`, `kill`, `config policy`는 플래그가 없으면 `CCTRACE_PROFILE` 환경변수도 읽는다.

Claude 홈에 쓰인 훅은 그 홈을 `--claude-dir`로, 그리고 `--auto-profile`을 넘긴다. `--auto-profile`은 `--claude-dir` 값(플래그가 없으면 `CLAUDE_CONFIG_DIR`)과 Claude 홈이 일치하는 이름 있는 프로필을 고른다. 일치하는 프로필이 없으면 기본 프로필 사용.

## 프로필이 함께 쓰는 수집 폴더 { #source-folders-that-profiles-share }

두 프로필이 같은 수집 폴더를 읽게 되는 경우가 있다. 흔한 경우는 기본 Codex 홈이다. Codex 수집을 켠 프로필은 모두 이 폴더가 있으면 스캔한다. 프로필끼리 업로드 목적지가 같고(동기화 엔드포인트, cctrace 사용자, 이메일이 모두 같음) 수집기가 읽는 폴더가 같거나 한쪽이 다른 쪽 안에 있으면 데몬은 그 폴더를 공유 출처로 본다. 두 프로필이 모두 올리면 그 안의 세션이 두 번 집계되므로, 데몬은 한 프로필만 수집하게 한다.

- **한 프로필만 수집.** 그 프로필이 맡은 프로필(holder)이다. 데몬이 출처를 처음 맡긴 프로필이 이후 다른 프로필이 어떻게 바뀌어도 계속 맡는다. 처음 시작할 때는 기본 프로필이 먼저, 이어 이름 있는 프로필이 이름순이다. 중지되었거나 꺼진 프로필은 새 출처를 맡지 못하고, 이미 맡은 출처는 유지한다.
- **다른 프로필은 그 출처만 잃음.** 나머지는 그대로 업로드한다: Claude 세션, 공유하지 않는 모든 폴더. 프로필 전체가 멈추지 않는다. 단, 프로필의 모든 출처가 공유 출처이면 수집할 것이 없으며 `cctrace status`가 이를 알린다.
- **맡은 프로필의 필터 적용.** 공유 출처는 맡은 프로필의 필터(저장소 허용 목록, 프롬프트·도구 가림, 제외 계정)가 허용하는 만큼 전송한다. 막힌 프로필은 이 출처에서 아무것도 보내지 않으므로 자기 필터가 전송 범위를 넓히는 일은 없다.
- **막혀 있던 기간을 되짚어 읽지 않음.** 저장된 읽기 위치가 전혀 없이 시작하는 프로필은 디스크에 이미 있는 세션을 건너뛰고 새 세션부터 수집한다. 저장된 읽기 위치가 이미 있는 프로필(예: 그 출처를 예전에 읽던 프로필)은 거기서 이어 읽으므로 막혀 있던 기간의 파일도 보낼 수 있고, 그중 일부는 다른 프로필이 이미 보낸 것이다. 서버가 중복을 걸러 낸다. 출처가 다른 프로필로 넘어가면 새 담당은 데몬이 읽을 수 있는 한 이전 담당의 읽기 위치에서 이어 가고, 읽을 수 없으면 디스크에 이미 있는 세션을 건너뛰고 새 세션부터 수집한다.
- **담당 변경은 사용자가 바꿀 때만,** 예외가 하나 있다. 중지되었거나 꺼진 담당 프로필도 출처를 계속 맡는다. 삭제한 프로필의 출처는 어느 프로필도 읽지 않는다. 삭제가 필터가 다른 프로필로 출처를 넘기지 않게 하기 위해서다. 담당 프로필이 자기 설정에서 출처를 빼면(예: 그 에이전트의 수집을 끔) 출처를 놓고, 필터가 같거나 더 엄격한 다음 프로필이 자동으로 이어받는다. 해당하는 프로필이 없으면 `cctrace profile claim`을 실행할 때까지 아무도 수집하지 않는다. 폴더를 다른 폴더로 가는 링크로 바꾸는 식으로 디스크가 바뀌어 서로 다른 프로필이 맡던 두 출처가 한 출처가 되면 먼저 맡은 쪽이 계속 맡고, 다른 쪽 프로필은 그 출처 읽기를 마친 뒤 그 출처에서 막힌다.

겹친 폴더가 같은 폴더이면 손실이 없다. 담당 프로필이 그 안의 모든 파일을 읽는다. 한쪽이 다른 쪽 안에 있으면 데몬은 그래도 한 프로필만 붙이지만, 수집기가 폴더의 정해진 깊이만 읽기 때문에 일부 파일은 어느 프로필에도 수집되지 않을 수 있다. `cctrace status`는 이런 행에 경고를 붙인다.

### 막힌 출처 확인

`cctrace status`는 프로필이 어떤 출처에서 막혀 있을 때만 `수집 경로` 줄을 출력한다. 출력은 한국어다.

```text
    수집 경로:     [!] 이 프로필에서 수집하지 않는 경로 2개 -- 같은 서버·사용자로 수집하는 다른 프로필이 맡고 있습니다
                   codex  기본 폴더        3fa91c2d  맡은 프로필: default (수집 중)
                   codex  [명시 Codex 홈]  9b01d7e4  맡은 프로필: personal (중지됨)
                   나머지 경로는 정상 수집합니다. 맡길 프로필을 바꾸려면: cctrace profile claim <프로필> --source <id>
```

행은 출처 종류, 폴더를 얻은 곳, id, 맡은 프로필과 그 상태 순이다. 폴더는 경로로 출력하지 않는다.

| 열 | 값 |
|---|---|
| 폴더 | `홈 폴더`(Claude 홈), `기본 폴더`(기본 Codex 홈), `CODEX_HOME`, `설정 폴더`(프로필 설정에 적힌 폴더), `[명시 Codex 홈]`(프로필의 명시 Codex 홈) |
| 맡은 프로필 상태 | `수집 중`, `중지됨`, `꺼짐`, `막힘`, `이전 중`(출처를 넘겨주는 중) |
| 맡은 프로필 없음 | `(삭제됨)`, `(주인 없음)`(아무도 맡지 않음), `(알 수 없음)` |

폴더가 중첩이면 행 끝에 `-- 일부 파일은 어느 프로필에도 수집되지 않을 수 있음`이 붙는다. 프로필의 모든 출처가 공유 출처이면 첫 줄이 `[!] 이 프로필의 모든 경로가 다른 프로필과 겹쳐 수집이 멈춰 있습니다 (duplicate_route)`로 바뀐다. 프로필이 폴더를 막힌 기간이 지난 뒤에야 읽기 시작했으면 마지막에 `겹침으로 막혀 있던 기간의 세션은 수집되지 않았습니다 (수집을 시작한 시점부터 수집)`가 붙는다. 모든 프로필의 같은 내용은 `cctrace sync status`와 데몬 로그에서 한 번에 볼 수 있다. [동기화 데몬](sync.md#sources-shared-between-profiles) 참고.

### 공유 출처를 다른 프로필에 맡기기

```console
$ cctrace profile claim work --source 3fa91c2d
$ cctrace profile claim work --all --force
```

`profile claim <profile>`은 그 프로필이 막혀 있는 출처를 그 프로필로 옮긴다. 맡고 있던 프로필이 먼저 읽기를 놓고, 이어 `<profile>`이 이전 담당의 읽기 위치에서 이어 읽는다. 로컬 데몬이 실행 중이어야 한다(`cctrace sync --daemon`). 실행 중이 아니면 명령이 이를 알리고 멈춘다.

| 플래그 | 뜻 |
|---|---|
| `--source <id\|folder>` | 맡을 출처: `cctrace status`의 id 또는 그 폴더(같은 폴더면 표기가 달라도 됨). 반복 가능 |
| `--all` | 프로필이 막혀 있는 출처 전부 |
| `--force` | 확인 생략 |

`--source`도 `--all`도 없으면 프로필이 막혀 있는 출처를 나열하고 멈춘다. 출처마다 계획을 출력하고 `Proceed? [y/N]`로 묻는다. 터미널이 없으면 `--force` 없이는 거부한다. 출력은 영어다.

```text
Claim codex source 3fa91c2d (default folder) for profile "work"
  current holder : default (active)
  cursor         : continue from "default"
  filters        : repository allowlist: default 2 entries, "work" none (collects every repository)
                   prompt redaction: default off, "work" off
                   tool redaction: default off, "work" off
                   excluded accounts: default 0, "work" 0
                   note: "work" would send more than "default" does
  effect         : "default" stops reading this source; "work" starts after "default" has released it
Proceed? [y/N]
```

확인하기 전에 `filters` 줄을 읽는다. 두 프로필 필터의 항목 수와 켜짐 여부만 비교하고 값은 보이지 않는다. 저장소 접두어가 경로이기 때문이다. 출처를 받는 프로필이 맡고 있던 프로필보다 더 많이 보내면 `note` 줄이 나온다. 이전 담당의 읽기 위치를 읽을 수 없으면 `cursor`가 `holder state unavailable; existing sessions will be skipped, new ones collected`로 나온다. 맡은 프로필이 설정에서 출처를 빼서 아무도 맡지 않는 출처는 마지막으로 읽은 프로필의 읽기 위치를 따르며, `cursor`가 그 프로필의 `continue from`으로 나온다.

데몬이 claim을 받으면 명령이 `Claim 3fa91c2d accepted: "default" lets go of it first, then "work" starts reading it.`을 출력한다. 이전은 백그라운드에서 끝난다. 데몬이 거부하면(예: 그 출처의 이전이 아직 진행 중) 아무것도 바뀌지 않고 메시지가 할 일을 알려 준다. 맡은 프로필이 삭제되었거나 아무도 맡지 않은 출처는 바로 넘어간다. routes 파일을 쓸 수 없는 동안(데몬이 `[routes] invalid`를 기록함)에는 첫 claim이 데몬에게 파일을 다시 만들게 하며, `--all`이나 `--source`를 여럿 줘도 이름을 준 출처를 모두 맡는다. 다음 출처를 맡기 전에 명령이 데몬이 다시 만든 파일을 게시하기를 최대 10초 기다린다.

## 서버 정책 보류 { #server-policy-deferral }

서버 운영자는 클라이언트를 새 서버 주소로 옮기는 서명된 정책을 게시할 수 있다. 업데이트 공개키 또는 정책 공개키를 링크해 빌드한 클라이언트만 정책을 적용한다([동기화 데몬](sync.md#server-policy) 참고). 이 저장소에서 업데이트 공개키도 정책 공개키도 넘기지 않고 빌드한 클라이언트에는 아래 내용이 적용되지 않는다.

정책은 두 프로필이 함께 쓸 수 있는 파일을 다시 쓴다: 프로필의 Claude 홈에 있는 Claude 설정 파일, 기본 Codex 홈, 설정된 경우 `CODEX_HOME`이 가리키는 디렉터리, `options.codex_dirs`의 각 디렉터리에 있는 Codex 설정 파일. 다른 프로필이 같은 파일을 쓰면 클라이언트는 정책을 적용하지 않고 보류한다. 다만 Codex 설정 파일은 그 파일을 쓰는 프로필이 모두 같은 서버로 함께 옮기거나 기본 Codex 홈의 파일이면 보류하지 않는다(아래 참고). 실행은 `policy deferred`로 끝나는 오류로 종료되고, 프로필은 기존 주소로 수집을 계속하며, 파일이 더 이상 공유되지 않을 때까지 이후 확인도 같은 결과로 끝난다. 사유는 `cctrace config policy status`로 확인.

클라이언트는 다른 프로필을 모두 확인한다. 기본 프로필, 이름 있는 프로필의 이름순으로 보며 첫 충돌에서 멈추지 않는다. 사유는 충돌마다 프로필 이름과 파일 종류를 이름순으로, 같은 프로필이면 Claude를 Codex보다 먼저 나열한다.

```text
shared settings with profiles "home" (claude), "work" (codex); policy deferred
```

| 항목 | 원인 |
|---|---|
| `"<name>" (claude)` | 다른 프로필의 Claude 홈이 같음. 다른 프로필이 기본 프로필이면 `<name>`은 빈 값 |
| `"<name>" (codex)` | 두 프로필이 기본 Codex 홈이 아닌 공통 Codex 디렉터리(`CODEX_HOME`이 가리키는 곳이나 `options.codex_dirs`에 적은 곳)를 스캔하고 어느 쪽도 Codex 동기화를 끄지 않음. 이 프로필과 함께 옮기지 않는 프로필만 나열. 이전 클라이언트 버전은 기본 Codex 홈만 공유하는 프로필도 나열하는데, 이 폴더는 존재하면 모든 프로필이 스캔 |

사유는 500자까지다. 충돌한 프로필이 너무 많아 모두 적지 못하면 뒤쪽 항목은 이름 대신 개수로 센다: `... and 31 more; policy deferred`. 이 숫자는 프로필 수가 아니라 항목 수라서, 두 가지 모두 겹치는 프로필은 둘로 센다. 이전 클라이언트 버전은 첫 충돌에서 멈추고 하나만 `shared Claude settings with profile "<name>"; policy deferred` 또는 `shared Codex settings with profile "<name>"; policy deferred`로 보고한다.

Codex 설정 파일을 함께 쓰는 프로필은 이미 정책의 새 주소를 쓰고 있거나, 정책이 지금 쓰는 주소 그대로 그 프로필을 대상으로 삼고 그 프로필의 동기화 데몬이 정책으로 다시 시작할 수 있는 상태로 돌고 있으면 함께 옮기는 것으로 보고 사유에 나열하지 않는다. 다시 시작할 수 있는 상태란 `--local`이나 `--endpoint` 없이 시작했고 자기 Claude 홈을 읽는 것이다. 같은 서버의 다른 표기, 다른 포트, 다른 프로토콜은 정책이 지정한 주소가 아니다. Claude 홈을 공유하면 다른 프로필이 함께 옮기더라도 항상 보류한다. 이전 클라이언트 버전은 공유하는 Codex 설정 파일마다 보류한다.

함께 옮기는 경우 클라이언트는 공유 Codex 설정 파일을 정책과 함께가 아니라 정책을 반영한 뒤에 쓰고, 끝날 때까지 정책 상태에 그 쓰기를 남긴다: 파일 경로, 옛 `[otel]` 절과 새 `[otel]` 절의 다이제스트, 옛 OTEL 주소, 옛 인증서 경로. 기록에는 토큰이 없다. 이후 실행은 서버에 정책을 묻기 전에 이 쓰기를 처리한다. 기본 Codex 홈 밖에 있는 파일의 `[otel]` 절이 함께 옮기는 어느 프로필이 쓰는 것과도 다르면 `shared Codex settings edited; policy deferred`로 보류하고, 그 프로필들 중 하나의 절로 돌아올 때까지 이어진다. 쓰기가 실패하면 실행은 `shared settings write pending`으로 끝나고 다음 실행이 다시 시도한다. 다른 프로필이 정책을 적용하기 전에는 그 프로필의 세션이 옛 절을 되돌려 쓸 수 있고, 그 프로필이 적용하면 파일은 새 서버를 쓴다.

기본 Codex 홈의 Codex 설정 파일은 예외다. Codex 동기화를 켠 프로필은 동기화가 돌 때마다 자기 `[otel]` 절을 이 파일에 쓰고 마지막에 쓴 프로필이 이기므로, 정책이 무엇을 하든 이 파일을 다른 프로필에 맞춰 붙잡아 둘 수 없다. 그래서 이 파일만 다른 프로필과 공유하는 프로필은 상대가 함께 옮기든 아니든 정책을 보류하지 않는다. 프로필은 옮기고, 파일은 지금 들어 있는 내용에 따라 처리한다.

| 파일의 내용 | 클라이언트의 처리 |
|---|---|
| 이 프로필의 옛 절 | 위와 같이 정책을 반영한 뒤 새 절을 씀 |
| 이 프로필의 새 절, 또는 함께 옮기는 프로필의 절 | 바꾸지 않음 |
| 옮기지 않는 프로필의 절, 손으로 고친 절, 그 밖의 내용 | 파일은 그대로 두고 프로필은 옮김 |

절의 토큰은 바뀌지 않는다: 정책은 서버를 옮길 뿐 자격은 그대로 둔다. 파일을 그대로 두어도 정책은 적용된 것으로 치고 실행은 확인 결과 `ok`로 끝난다. `cctrace config policy status`와 서버로 보내는 보고의 사유는 `default Codex config kept: <path>`다. 이 프로필의 sync가 다음에 시작해 새 주소를 담은 자기 절을 쓰기 전까지, 그 파일은 지금 있는 절이 가리키는 곳으로 그 절의 토큰을 달고 Codex 지표를 계속 보낸다. 여러 프로필이 기본 Codex 홈을 쓰면 Codex 지표는 정책 이전과 마찬가지로 마지막에 쓴 프로필의 서버로 가며, 정책은 이를 바꾸지 않는다. 이전 클라이언트 버전은 이 파일도 다른 파일처럼 보류한다.

프로필에 명시한 Codex 홈(`codex_home`)은 그 프로필만 쓰는 곳이라 공유 파일이 아니며 정책을 보류시키지 않는다. 프로필의 Codex 동기화가 켜져 있으면 클라이언트는 이 홈의 `[otel]` 절을 정책의 나머지 변경과 같은 트랜잭션에서 옮긴다. 절이 cctrace가 프로필에 맞춰 생성하는 내용일 때만 옮기며, 프로필이 지금 쓰는 주소의 절이거나 정책이 같은 호스트에서 옮겨 오는 주소의 절이고, 프로필의 인증서, 정책 이전에 사용자가 지정한 인증서, 또는 인증서 없음을 쓰는 경우다. 다른 점은 토큰 하나다. 절이 담고 있는 토큰은 그대로 두며, 이 기기의 어느 프로필 것도 아닌 토큰일 수 있다. 파일의 나머지도 그대로 둔다. 이 홈이 기본 Codex 홈, `CODEX_HOME`이 가리키는 디렉터리, `options.codex_dirs`의 디렉터리이기도 하면 위에서 설명한 그 디렉터리의 규칙을 따른다.

| 명시 홈 파일의 내용 | 클라이언트의 처리 |
|---|---|
| 위에서 설명한 이 프로필의 절 | 정책과 함께 새 절을 씀 |
| 이미 정책의 새 주소인 절, 자기 정책 실행이 이 홈에 닿는 다른 프로필(이 폴더를 스캔하거나 Codex 동기화를 켜고 명시 홈으로 가진 프로필)의 토큰을 단 절, 파일 없음 | 바꾸지 않고 아무 말도 하지 않음. 다른 프로필이 자기 절을 옮김 |
| `[otel]` 절 없음, 다른 곳으로 보내거나 손으로 고친 절, 옛 형식의 절이나 여러 줄 루트 `otel` 키, 읽을 수 없는 파일, 심볼릭 링크인 Codex 설정 파일, 정책 실행이 이 홈에 닿지 않는 프로필의 토큰을 단 절, 클라이언트가 잠금을 잡을 수 없는 홈 | 파일은 그대로 두고 프로필은 옮김 |

파일을 그대로 두어도 정책은 적용된 것으로 치고 실행은 확인 결과 `ok`로 끝난다. `cctrace config policy status`와 서버로 보내는 보고의 사유는 `explicit Codex home kept`이고, 기본 Codex 홈의 메모도 있으면 `; `로 이어 붙인다. 정책 실행이 기록하거나 보고하는 사유에 든 명시 Codex 홈의 경로는 `[explicit Codex home]`로 바뀐다. 다른 프로세스가 쥔 잠금은 다른 파일의 잠금처럼 기다리고, 기다림이 끝나면 실행이 끝난다. 파일 쓰기가 실패하면 트랜잭션 전체가 되돌려져 정책이 적용되지 않으며 다음 확인이 다시 시도한다.

Claude 설정을 분리하려면 두 프로필 중 하나에 별도 Claude 홈을 준다.

```console
$ cctrace profile edit work --home ~/.claude-work
$ cctrace env apply --profile work
```

`profile edit`는 프로필의 폴더만 바꾸고, `env apply`는 새 홈에 OTEL 변수와 훅을 쓴다. 다른 방법으로, 프로필을 지운 뒤 `--home`을 주어 다시 추가한다. 프로필을 지우면 그 프로필만의 로그인이 있었더라도 함께 사라진다. 새 프로필은 기본 프로필의 사용자·서버·옵션을 복사하고 동기화 상태는 빈 채로 시작하므로 디스크에 이미 있는 내용은 건너뛴다.

기본 Codex 홈이 아닌 디렉터리의 Codex 설정을 분리하려면 하나만 남기고 나머지 프로필의 Codex 동기화를 끈다. 끈 프로필은 Codex 수집을 멈춘다.

```console
$ cctrace config set options.codex_sync_enabled false --profile work
```

다음 두 사유도 파일의 소유자가 바꿀 때까지 보류가 이어진다: 클라이언트가 이전 정책 때 쓴 인증서 파일이 기록과 달라졌거나 `server.ca_cert_file`이 더 이상 그 파일을 가리키지 않는 경우(`managed CA was edited; policy deferred` 또는 `managed CA was changed; policy deferred`), 다른 프로필이 쓰지 않는 Codex 설정 파일의 `[otel]` 절이 cctrace가 프로필에 맞춰 생성하는 내용과 다른 경우(`unmanaged Codex OTEL settings differ from the profile; policy deferred`). 프로필의 Codex 동기화를 끄면 두 번째 사유는 더 이상 적용되지 않는다.
