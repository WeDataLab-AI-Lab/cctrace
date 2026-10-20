# 로컬 분석

로컬 분석은 이 머신에 저장된 Claude Code·Codex CLI 세션을 서버 없이 대시보드에서 보여 준다. cctrace가 세션 파일을 읽는 작은 API를 루프백 주소에 열고, 브라우저의 대시보드 페이지가 주소와 토큰으로 여기에 연결한다.

API 자체는 서버로 아무것도 보내지 않으며 서버 로그인도 데이터베이스도 필요 없다. [동기화 데몬](sync.md)을 대체하지는 않는다.

## 실행 방식 {#two-ways-to-run-it}

| | `cctrace local serve` | `cctrace local enable` |
|---|---|---|
| 실행 형태 | 포그라운드 명령, Ctrl-C로 종료 | 프로필 동기화 데몬의 일부 |
| 포트 | 시작할 때마다 운영체제가 선택, `--address`로 지정 가능 | `enable` 실행 시 선택, 데몬을 시작할 때마다 동일 |
| 토큰 | 시작할 때마다 새로 발급, 한 번만 출력 | `enable`마다 새로 발급, 한 번만 출력, 해시만 저장 |
| 허용 페이지 | `--origin` 값 하나 이상 | `--origin` 값 하나 |
| 읽는 폴더 | 아래 기본값 또는 지정한 폴더 | 프로필의 Claude 홈과 그 데몬이 스캔하는 Codex 홈 |
| 프로필 필요 | 아니요 | 예 |

## 한 번 실행 {#run-it-once}

```bash
cctrace local serve --origin http://127.0.0.1:8080
```

`--origin`은 필수다. 연결할 대시보드 페이지의 정확한 origin으로, 브라우저가 보여 주는 대로 스킴, 호스트, 기본값이 아닌 포트를 쓰고 경로와 끝 슬래시는 붙이지 않는다. 프록시 뒤의 대시보드라면 `https://cctrace.company.example` 같은 그 주소를 쓴다. 페이지가 둘 이상이면 `--origin`을 반복한다. 와일드카드, 경로, 쿼리 문자열, origin에 든 자격 증명은 거부된다.

출력 첫 줄은 JSON 객체다. 둘째 줄은 API가 켜졌다는 알림과 토큰을 비공개로 유지하라는 안내다.

- `endpoint`: API가 듣는 루프백 주소와 포트
- `token`: 이번 실행의 Bearer 토큰
- `profile_fingerprint`: 읽는 폴더 집합의 식별자
- `origins`: 허용된 origin

토큰은 무작위 32바이트이고 시작할 때마다 새로 만들어진다. 출력을 파일로 리디렉션하거나 토큰을 URL에 넣거나 보고서에 붙여 넣지 않는다. Ctrl-C로 API를 멈추면 토큰도 함께 무효가 된다.

| 플래그 | 기본값 | 효과 |
|---|---|---|
| `--origin <origin>` | 없음, 필수 | API 호출을 허용할 대시보드 페이지의 origin. 반복 가능 |
| `--address <ip:port>` | `127.0.0.1:0` | 듣는 주소. 리터럴 루프백 IP 주소여야 하며 `localhost` 같은 이름은 거부. 포트 0은 운영체제가 선택 |
| `--claude-dir <dir>` | 아래 참고 | 읽을 Claude 설정 디렉터리. 반복 가능 |
| `--codex-dir <dir>` | 아래 참고 | 읽을 Codex 홈. 반복 가능 |

### 읽는 폴더 {#folders-it-reads}

`--claude-dir`와 `--codex-dir`가 모두 없으면 `serve`는 다음 폴더를 읽는다.

- Claude Code: `CLAUDE_CONFIG_DIR`이 가리키는 폴더 또는 `~/.claude`, 그리고 Claude 홈을 지정한 각 프로필의 Claude 홈
- Codex CLI: `CODEX_CONFIG_DIR` 또는 `~/.codex`, 설정된 경우 `CODEX_HOME`, 그리고 Codex 홈을 지정한 각 프로필의 Codex 홈. 프로필 옵션 `options.codex_dirs`는 보지 않는다.

없는 폴더는 건너뛴다. 두 플래그 중 하나라도 주면 위의 모든 기본값이 꺼진다. 그러면 `serve`는 지정한 폴더만 읽고 프로필의 폴더는 더하지 않는다. `--claude-dir`만 주면 Codex 세션은 읽지 않고, 반대도 마찬가지다.

Claude 폴더에서는 `projects` 아래의 JSONL 세션 파일을, Codex 폴더에서는 `sessions` 아래의 rollout 파일을 읽는다([Codex CLI](../agents/codex.md)의 배치 참고). 폴더 밖을 가리키는 파일은 무시하고, `projects`나 `sessions` 디렉터리 자리의 심볼릭 링크는 따라가지 않는다.

## 데몬과 함께 실행 {#run-it-with-the-daemon}

```bash
cctrace local enable --origin https://cctrace.company.example
```

이름 있는 프로필에는 `--profile <name>`을 덧붙인다. `enable`은 `endpoint`와 `token`이 든 JSON 객체를 한 번 출력한다. origin, 127.0.0.1의 빈 포트, 토큰의 SHA-256 해시(토큰 자체가 아님)를 프로필 디렉터리에 저장한다. 기본 프로필은 `~/.cctrace/local-api-policy.json`, 이름 있는 프로필은 `~/.cctrace/profiles/<name>/local-api-policy.json`이다. 프로필 옵션 `options.local_analysis_enabled`도 켠다.

API는 프로필의 동기화 데몬과 함께 시작되므로 이미 실행 중이던 데몬은 다시 시작해야 한다(`cctrace sync --stop` 후 `cctrace sync --daemon`). 그 프로필의 Claude 홈과 데몬이 스캔하는 Codex 홈([Codex CLI](../agents/codex.md)에 목록)을 읽는다. 저장된 포트가 데몬 시작 때 이미 사용 중이면 API가 시작되지 않고 `status`가 오류를 보여 준다. `enable`은 프로필의 동기화 설정을 바꾸지 않으므로 데몬은 프로필 옵션대로 계속 업로드한다.

```bash
cctrace local status
cctrace local disable
```

`status`는 JSON 객체 하나를 출력한다: `enabled`, `running`, `origin`, `address`, `generation`, `started_at`, `ended_at`, `error`. `disable`은 저장된 토큰 해시를 지우고 옵션을 끄며, 실행 중인 데몬은 이를 알아채면 API를 멈춘다.

`enable`을 다시 실행하면 새 토큰과 새 포트가 발급된다. 이전 토큰은 무효가 되고 실행 중인 데몬은 API를 멈추므로, 데몬을 다시 시작해야 새 값으로 시작된다.

## 대시보드에서 연결 {#connect-from-the-dashboard}

이 연결을 쓰는 페이지는 둘이다.

- `/local-sessions`: 대시보드에 로그인하지 않고 쓸 수 있다.
- 세션 페이지의 **내 기기와 서버** 보기: 서버 세션을 로컬 세션 옆에 나열하므로 대시보드에 로그인해야 한다.

`--origin`에 준 origin의 페이지를 연다. **로컬 서비스 주소**에 `endpoint` 값을, **접속 토큰**에 토큰을 입력하고 **연결**을 누른다. 폼은 `http://` 뒤에 `localhost`, 127.0.0.1, `[::1]` 중 하나와 포트가 오는 주소만 받고, `localhost`는 연결하기 전에 127.0.0.1로 바꿔 쓴다. API는 자신이 듣는 정확한 주소만 받는다. `endpoint` 값을 출력된 그대로 입력한다. `endpoint`가 127.0.0.1이면 `[::1]`로 바꿔 쓴 주소는 거부된다.

**이 탭에 연결 저장**을 켜면 주소와 토큰을 그 브라우저 탭의 `sessionStorage`에 보관하므로, 새로 고쳐도 다시 붙여 넣지 않고 재연결된다. 탭을 닫으면 사라진다. 켜지 않으면 토큰은 페이지 메모리에만 있고 새로 고칠 때 다시 묻는다. **연결 끊기**는 저장된 쌍을 지운다.

세션 목록에는 검색창과 에이전트 필터(**전체**, **Claude**, **Codex**)가 있다. 목록은 각 파일의 앞과 뒤 32 KiB만 읽으므로 목록의 토큰 수, 이벤트 수, 비용은 0 대신 **미집계**로 표시된다. 세션을 선택하면 **대화**, **개별 원문**, **분석** 세 탭이 열린다. 분석 탭은 파일 전체를 읽어 기록이 뒷받침하는 값만 계산하며, 뒷받침하지 못하는 값은 비워 둔다.

**내 기기와 서버** 보기에서 세션 ID가 같은 로컬 세션과 서버 세션은 따로 나열된다. ID만으로는 둘이 같은 사람의 것인지 알 수 없으므로 페이지가 **같은 세션으로 묶기**를 제공해 직접 묶게 한다.

## API가 허용하는 것 {#what-the-api-allows}

- 루프백 주소에서만 듣는다. `serve`는 다른 `--address`를 거부하고 `enable`은 항상 127.0.0.1을 쓴다.
- 브라우저의 CORS 사전 요청을 제외한 모든 요청에 토큰이 필요하다. `Host` 헤더는 듣는 주소와 같아야 하므로 다른 이름으로 API에 닿는 요청(예: DNS 리바인딩 페이지)은 거부된다.
- 허용 목록 밖의 `Origin` 헤더가 붙은 요청은 거부된다.
- 세션 파일을 읽기만 하고 수정하지 않는다. 쓰기는 하나뿐이며, `serve`에서 페이지가 저장할 수 있는 다음 Claude Code·Codex 실행의 프로필 선택이고 `~/.cctrace/next-profile.json`에 저장된다. 데몬의 API에는 프로필 선택이 없다.
- 로컬 세션이 서버에도 있는지, 누구의 것인지는 확인하지 않는다.
- 응답에는 `no-store`가 붙는다.

HTTPS 대시보드 페이지는 루프백 주소에 닿기 전에 브라우저의 로컬 네트워크 권한이 필요할 수 있다. CLI에서 origin을 허용해도 그 권한이 주어지지 않으며 모든 브라우저가 이 연결을 지원한다는 뜻도 아니다.
