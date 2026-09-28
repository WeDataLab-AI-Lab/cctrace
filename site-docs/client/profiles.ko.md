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

이름 있는 프로필과 각 Claude 홈 출력. 기본 프로필은 목록에 없다. `cctrace status`는 둘 다 보여 준다.

## 프로필 삭제

```bash
cctrace profile remove work
```

확인을 받은 뒤 `~/.cctrace/profiles/` 아래 해당 프로필 디렉터리를 지운다(`--force`로 확인 생략). 프로필의 Claude 설정 파일은 건드리지 않으므로 OTEL 변수와 훅이 남는다. 이것까지 지우려면 대신 `cctrace reset --profile work` 사용. [초기화와 제거](uninstall.md) 참고.

## 명령줄에서 프로필 지정

프로필 하나를 대상으로 하는 명령은 `--profile <name>`을 받는다: `status`, `config`, `env apply`, `sync`, `kill`, `reset`. `sync`와 `kill`은 플래그가 없으면 `CCTRACE_PROFILE` 환경변수도 읽는다.

Claude 홈에 쓰인 훅은 그 홈을 `--claude-dir`로, 그리고 `--auto-profile`을 넘긴다. `--auto-profile`은 `--claude-dir` 값(플래그가 없으면 `CLAUDE_CONFIG_DIR`)과 Claude 홈이 일치하는 이름 있는 프로필을 고른다. 일치하는 프로필이 없으면 기본 프로필 사용.
