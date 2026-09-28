---
name: install
description: >
  cctrace 최초 설치 스킬. 환경(OS/arch/shell)을 감지하고, 적절한 설치 경로를 선택하여
  소스에서 빌드 후 PATH에 등록한다. `cctrace` 명령어로 바로 실행 가능하도록 설정까지 완료.
  "설치", "install", "처음 설치", "PATH 설정" 등의 말을 하면 사용하세요.
metadata:
  author: cctrace
  version: "0.1.0"
  category: devops
  tags: [install, cctrace, path, setup]
---

# Install cctrace

소스에서 빌드하여 로컬에 설치하고, PATH 설정까지 완료합니다.

## When to Use

- cctrace를 처음 설치할 때
- PATH에 cctrace가 없어서 `command not found`가 뜰 때
- 설치 경로를 바꾸고 싶을 때

## Implementation

### Step 1: 환경 감지

```bash
OS=$(uname -s)    # Darwin or Linux
ARCH=$(uname -m)  # arm64 or x86_64
SHELL_NAME=$(basename "$SHELL")  # zsh, bash, fish

echo "OS: $OS | Arch: $ARCH | Shell: $SHELL_NAME"

# 이미 설치된 경우 감지
EXISTING=$(which cctrace 2>/dev/null || echo "")
if [ -n "$EXISTING" ]; then
  echo "Already installed: $EXISTING ($(cctrace --version 2>/dev/null || echo 'unknown version'))"
fi
```

### Step 2: 설치 경로 결정

우선순위:
1. 이미 설치된 경로 재사용 (`which cctrace`)
2. `~/.local/bin` -- sudo 불필요. **자동 갱신이 가능한 유일한 기본값**
3. `/usr/local/bin` -- 최후 수단. 아래 경고 필수

**`/usr/local/bin` 은 자동 갱신을 영구히 막는다.** 갱신은 실행 파일 옆에 임시 파일을 만드는데 그 디렉터리가 root 소유라 매번 `permission denied` 로 끝난다. 한 사용자가 이 상태로 네 번 실패하며 4개 버전 뒤처졌다 (#459).

이 경로로 떨어지면 **설치 시점에 알린다.** 사후에 알아내는 비용이 그 이슈다.

```bash
if [ -n "$EXISTING" ]; then
  INSTALL_DIR=$(dirname "$EXISTING")
elif echo "$PATH" | tr ':' '\n' | grep -q "$HOME/.local/bin"; then
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
else
  INSTALL_DIR="/usr/local/bin"
  echo "[WARN] ~/.local/bin 이 PATH 에 없어 /usr/local/bin 에 설치합니다."
  echo "       이 위치는 자동 갱신이 되지 않습니다 (매번 sudo 필요)."
  echo "       PATH 에 ~/.local/bin 을 추가한 뒤 다시 실행하는 편을 권합니다."
fi

INSTALL_PATH="$INSTALL_DIR/cctrace"
echo "Install target: $INSTALL_PATH"
```

### Step 3: Go 확인 및 빌드

Go 설치 여부를 먼저 확인합니다:

```bash
if ! command -v go &>/dev/null; then
  echo "[WARN] Go compiler not found."
fi
```

**Go가 없으면 사용자에게 질문합니다:**

> "Go 컴파일러가 설치되어 있지 않습니다. 소스 빌드 대신 서버에서 바이너리를 다운로드하여 설치할까요? (y/n)"

**y (바이너리 설치):**

```bash
# OS/Arch 감지
GOOS=$(uname -s | tr '[:upper:]' '[:lower:]')   # darwin, linux
GOARCH=$(uname -m)
[ "$GOARCH" = "x86_64" ] && GOARCH="amd64"

# 서버 주소 (기존 프로파일이 있으면 읽고, 없으면 직접 입력받는다)
ENDPOINT=$(cctrace config get server.sync_endpoint 2>/dev/null | tr -d '[:space:]')
if [ -z "$ENDPOINT" ]; then
  printf "다운로드 서버 주소를 입력하세요 (예: http://host:8080): "
  read -r ENDPOINT
  ENDPOINT=$(echo "$ENDPOINT" | tr -d '[:space:]')
fi
if [ -z "$ENDPOINT" ]; then
  echo "[ERROR] 서버 주소가 필요합니다. 설치를 중단합니다."
  exit 1
fi

BINARY_URL="$ENDPOINT/downloads/cctrace-$GOOS-$GOARCH"
echo "Downloading: $BINARY_URL"
curl -fL -o /tmp/cctrace-new "$BINARY_URL"
chmod +x /tmp/cctrace-new
echo "Download OK: $(file /tmp/cctrace-new)"
```

**n (취소):**

> "Go 설치 후 다시 시도하세요: https://go.dev/dl/"
> 스킬을 종료합니다.

**Go가 있으면 소스 빌드:**

```bash
# 맨 `go build` 를 쓰지 않습니다. 그 산출물은 main.updatePublicKeyBase64 가 비어
# self-update 가 영구히 실패합니다 -- 설치는 성공하고, 그 뒤로 서버가 새 버전을
# 아무리 서빙해도 조용히 멈춰 있습니다. 실제로 그렇게 설치된 클라이언트가 일주일
# 넘게 v0.7.18 에 머물렀습니다.
#
# `make build` 도 아닙니다. 그쪽은 lipo 로 darwin 유니버설을 만들어 macOS 전용입니다.
# build-client 는 현재 플랫폼 하나만 빌드하면서 릴리스와 같은 LDFLAGS 를 씁니다.
make build-client CLIENT_OUT=/tmp/cctrace-new
echo "Build OK: $(file /tmp/cctrace-new)"
```

**설치 전 신뢰 루트 확인 (소스 빌드·다운로드 양쪽 모두):**

교체 전에 새 바이너리가 자동 업데이트를 할 수 있는 상태인지 봅니다. 이 검사가 없으면
self-update 가 죽은 바이너리를 설치하고도 성공으로 보입니다.

```bash
# 저장소가 선언한 공개 키가 바이너리에 박혀 있는지 봅니다.
# `-a` 없이는 macOS grep 이 바이너리를 특별 취급해 매치를 보고하지 않습니다.
# `sync --dry-run` 으로는 검사할 수 없습니다 -- dry-run 은 업데이트 경로 자체를
# 건너뛰기 때문에, 즉 sync 의 `!dryRun` 조건에 가려서 항상 조용히 통과합니다.
PUB=$(make -s print-update-public-key)
if [ -z "$PUB" ]; then
  echo "[ERROR] 이 체크아웃에는 신뢰 루트가 선언되어 있지 않습니다."
  echo "        여기서 빌드한 클라이언트는 자동 업데이트를 하지 못합니다."
  exit 1
fi
if grep -aqF "$PUB" /tmp/cctrace-new; then
  echo "Trust root OK"
else
  echo "[ERROR] 신뢰 루트가 없는 바이너리입니다."
  echo "        설치하면 self-update 가 'update public key is not configured' 로 영구 실패합니다."
  exit 1
fi
```

### Step 4: 바이너리 설치

```bash
# sudo 없이 시도, 실패하면 sudo로 재시도
if cp /tmp/cctrace-new "$INSTALL_PATH" 2>/dev/null; then
  chmod +x "$INSTALL_PATH"
  echo "Installed (no sudo): $INSTALL_PATH"
else
  sudo cp /tmp/cctrace-new "$INSTALL_PATH"
  sudo chmod +x "$INSTALL_PATH"
  echo "Installed (sudo): $INSTALL_PATH"
fi
```

### Step 5: PATH 설정 (필요한 경우)

설치 디렉토리가 PATH에 없으면 쉘 설정에 추가:

```bash
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
  echo "[INFO] $INSTALL_DIR is not in PATH. Adding..."

  case "$SHELL_NAME" in
    zsh)  RC="$HOME/.zshrc" ;;
    bash) RC="$HOME/.bashrc" ;;
    fish) RC="$HOME/.config/fish/config.fish" ;;
    *)    RC="$HOME/.profile" ;;
  esac

  if [ "$SHELL_NAME" = "fish" ]; then
    echo "fish_add_path $INSTALL_DIR" >> "$RC"
  else
    echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$RC"
  fi

  echo "Added to $RC"
  export PATH="$INSTALL_DIR:$PATH"
  echo "Applied to current session."
fi
```

### Step 6: 설치 확인

`cctrace` (절대경로 아님)로 실행 가능한지 검증:

```bash
FOUND=$(which cctrace 2>/dev/null || echo "")
if [ -n "$FOUND" ]; then
  echo "[OK] cctrace found at: $FOUND"
  VER=$(cctrace --version 2>/dev/null)
  echo "[OK] Version: $VER"

  # dev 는 설치 실패가 아니라 조용한 실패입니다. 아래 주석 참조.
  case "$VER" in
    *dev*)
      echo "[WARN] 버전이 'dev' 입니다 -- ldflags 가 적용되지 않았습니다."
      echo "       이 바이너리는 자동 업데이트를 영구히 시도하지 않습니다."
      echo "       서버 배포본을 받거나, ldflags 를 넣어 다시 빌드하세요."
      ;;
  esac
else
  echo "[WARN] cctrace not found in PATH yet. Run: source $RC"
fi
```

> **`dev` 는 설치 실패처럼 보이지 않습니다.** 동기화도 수집도 정상 동작하므로 화면상
> 문제가 없고, 오직 자동 업데이트만 조용히 멈춥니다. 갱신 경로 셋이 모두
> version != "dev" 가드 뒤에 있기 때문입니다 -- 원샷 sync, 데몬 부모, watch 루프
> 세 경로가 모두 그렇습니다. 같은 ldflags 누락이 서명 검증용 공개키도 비우므로
> 업데이트를 받아도 검증에 실패합니다.
>
> 실제 사례: 한 사용자가 v0.7.6 에 머문 채 몇 달을 보냈고, "자동 업데이트가 왜 안 되냐"고
> 물어볼 때까지 아무도 몰랐습니다. 서버 쪽에서는 그냥 낡은 클라이언트로 보입니다.

## 기존 설치본 이전 (/usr/local/bin -> ~/.local/bin)

이미 `/usr/local/bin` 에 설치된 클라이언트를 사용자 소유 디렉터리로 옮기는 절차. Step 1~6 은 신규 설치용이고, 이 절은 **이미 설치된 것을 옮길 때** 씁니다.

**대상 판별** -- `command -v cctrace` 가 `/usr/local/bin/cctrace` 를 가리키는 macOS/Linux 설치본. Windows 는 해당 없음 -- 설치 위치가 사용자 홈(`AppData\Local\Programs\cctrace`)이라 디렉터리가 사용자 소유이고 갱신 권한 문제가 생기지 않습니다.

```bash
command -v cctrace          # /usr/local/bin/cctrace 이면 이전 대상
uname -s                    # Darwin 또는 Linux
```

### 이전 1. 데몬 정지

바이너리를 바꾸기 전에 돌고 있는 데몬을 전부 끝냅니다. 프로필이 여럿이어도 바이너리는 하나입니다.

```bash
cctrace sync --stop
cctrace profile list 2>/dev/null | sed -n 's/^ *\[\([^]]*\)\].*/\1/p' | while IFS= read -r P; do
  cctrace sync --stop --profile "$P"
done
pgrep -f "cctrace sync" || echo "no daemon left"
```

`cctrace profile list` 출력이 `  [claude-2]  <홈>/.claude-2` 형식(`cmd/cctrace/profile.go`)이라 프로필 이름만 뽑아야 합니다.
따옴표 없는 `$(cctrace profile list)` 는 `[claude-2]` 와 홈 경로를 각각 프로필 이름으로 넘기고,
`[` 를 포함한 토큰은 셸 글롭으로도 확장됩니다.

- 존재하지 않는 프로필 이름은 오류가 아님 -- `syncProfileDir` 가 조용히 기본 프로필로 폴백 (`cmd/cctrace/sync_control.go`)
- 그래서 `cctrace sync --stop --profile '[claude-2]'` 는 기본 프로필 데몬을 멈추고도 `Stopped sync daemon (pid ...)` 출력
- 명명 프로필 데몬은 하나도 안 멈춘 채 "멈췄다"만 보이는 상태

`sync --stop` 이 듣지 않으면 v0.7.30+ 의 `cctrace kill --profile "$P"`, 그래도 남으면 마지막 그물 `pkill -f "cctrace sync"`.

**`pgrep`·`pkill` 은 홈을 구분하지 않습니다.** 다른 사용자나 다른 홈의 데몬도 같은 이름으로
잡힙니다. `pgrep` 이 무언가를 출력해도 이전 대상이 아닐 수 있고, `pkill` 은 그것까지 죽입니다.
한 사람이 쓰는 기기가 아니면 `pkill` 대신 `cctrace kill --profile` 로 프로필을 지정하세요.

### 이전 2. PATH 에 ~/.local/bin 추가

`/usr/local/bin` 은 macOS 의 `/etc/paths` 로 이미 PATH 에 들어 있습니다. 그래서 `~/.local/bin` 을 **PATH 앞쪽에** 둡니다.

```bash
mkdir -p "$HOME/.local/bin"

case "$(basename "$SHELL")" in
  zsh)  RC="$HOME/.zshrc" ;;
  bash) RC="$HOME/.bashrc" ;;
  fish) RC="$HOME/.config/fish/config.fish" ;;
  *)    RC="$HOME/.profile" ;;
esac

# 현재 PATH 와 rc 파일을 둘 다 본다. 현재 PATH 만 보면 rc 를 아직 읽지 않은 셸에서
# 절차를 다시 돌릴 때 같은 줄이 두 번 들어간다.
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$HOME/.local/bin" \
   && ! grep -q '\.local/bin' "$RC" 2>/dev/null; then
  if [ "$(basename "$SHELL")" = "fish" ]; then
    echo 'fish_add_path $HOME/.local/bin' >> "$RC"
  else
    echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$RC"
  fi
  echo "Added to $RC"
fi
export PATH="$HOME/.local/bin:$PATH"
```

**fish 를 쓰면 위 블록을 bash 로 돌리게 됩니다.** 마지막 `export` 는 그 bash 안에서만 유효하고
지금 쓰는 fish 세션에는 적용되지 않습니다. rc 에는 `fish_add_path` 가 들어갔으므로 새 터미널부터
정상이고, 지금 세션에 바로 적용하려면 fish 에서 한 줄 실행합니다.

```fish
fish_add_path $HOME/.local/bin
```

이미 PATH 에 있어도 `/usr/local/bin` 보다 **뒤**면 옛 것이 계속 잡힙니다. 순서 확인:

```bash
echo "$PATH" | tr ':' '\n' | grep -n -e "^$HOME/.local/bin$" -e '^/usr/local/bin$'
```

`~/.local/bin` 의 번호가 더 커야 정상이 아니라, **더 작아야** 정상입니다. 뒤에 있으면 rc 파일의 해당 줄을 앞쪽으로 옮깁니다.

### 이전 3. 바이너리 복사 및 옛 것 정리

```bash
cp /usr/local/bin/cctrace "$HOME/.local/bin/cctrace"
chmod +x "$HOME/.local/bin/cctrace"
command -v cctrace          # ~/.local/bin/cctrace 여야 정상
```

여기까지로 이전은 끝납니다. 이전 2 가 `~/.local/bin` 을 PATH **앞쪽**에 뒀으므로 `cctrace` 는 새 바이너리를 실행하고, 자동 갱신도 사용자 소유 디렉터리에서 풀립니다.

옛 바이너리 제거는 **필수가 아니라 권장**입니다 -- 중복본 제거와 혼동 방지 목적.

```bash
sudo rm /usr/local/bin/cctrace     # 권장, 필수 아님
```

- 남겨 두면 PATH 순서가 다른 환경(다른 rc, cron, launchd, GUI 실행)이 옛 것을 실행
- `/usr/local/bin/cctrace` 를 절대경로로 박아 둔 스크립트·훅도 옛 것을 실행
- 옛 바이너리 자신의 자동 갱신은 계속 실패 -- 실행되면 실패 로그만 쌓임

**sudo 를 쓸 수 없는 경우** -- `sudo rm` 만 건너뛰고 나머지는 그대로 진행합니다. 이전에 sudo 가 필요한 단계는 없습니다. 확인은 `command -v cctrace` 로 하고(`which -a` 는 두 줄로 남음), `/usr/local/bin/cctrace` 제거는 관리자에게 요청해 둡니다.

`sudo rm` 이 필요한 이유는 파일 권한이 아니라 **디렉터리 권한**입니다 -- `/usr/local/bin` 이 root 소유라 그 안에 파일을 만들지도 지우지도 못합니다. 자동 갱신이 실행 파일 옆에 임시 파일을 만들다 실패하는 것과 같은 조건입니다.

### 이전 4. 셸 명령 해시 초기화

옛 경로가 셸 명령 캐시에 남아 `cctrace: command not found`(지운 경우) 또는 옛 바이너리 실행(남긴 경우)이 됩니다.

```bash
hash -r      # zsh 는 rehash
```

### 이전 5. 훅 재등록

SessionStart/SessionEnd 훅 명령에는 바이너리 **절대경로가 박혀** 있습니다(`internal/envgen/hooks.go`). 옮긴 뒤 다시 등록하지 않으면 훅은 지워진 옛 경로를 호출합니다.

```bash
"$HOME/.local/bin/cctrace" env apply --all
```

출력 첫 줄 `Binary:` 가 `~/.local/bin/cctrace` 여야 정상입니다.

### 이전 6. 확인

```bash
command -v cctrace   # ~/.local/bin/cctrace 여야 정상
which -a cctrace     # 참고 -- 옛 바이너리를 지웠으면 한 줄
cctrace --version
cctrace status
```

- `command -v cctrace` 가 여전히 `/usr/local/bin/cctrace` 이면 이전 2 의 PATH 순서가 뒤집힌 것
- `which -a cctrace` 가 **두 줄**이면 이전 3 의 `sudo rm` 을 건너뛴 것 -- 첫 줄이 `~/.local/bin` 이면 동작 자체는 정상
- `cctrace status` 의 PATHS 블록 `Binary:` 줄이 새 경로. 경로가 쓰기 불가면 그 아래 `[!] 자동 갱신 불가 -- ...` 가 함께 나옵니다. 이 줄 자체가 v0.7.33+ 출력이라 이전 전 낡은 바이너리에는 없을 수 있습니다

### 이전 7. 데몬 재시작

**이전 1 에서 멈춘 것과 같은 수만큼 되살려야 합니다.** 기본 프로필만 켜면 명명 프로필 수집이
조용히 멈춘 채로 남습니다 -- 이 이슈가 잡으려는 것과 같은 부류입니다.

```bash
cctrace sync --daemon
cctrace profile list 2>/dev/null | sed -n 's/^ *\[\([^]]*\)\].*/\1/p' | while IFS= read -r P; do
  cctrace sync --daemon --profile "$P"
done
cctrace status
```

확인은 `cctrace status` 로 합니다. `pgrep -f "cctrace sync"` 는 **하나만 살아 있어도 성공**하므로
셋 중 둘이 죽은 상태를 정상으로 보고합니다. 프로필별로 살아 있는지 보려면 각 프로필의
런타임 파일이 있는지 확인합니다.

```bash
ls ~/.cctrace/sync-runtime.json ~/.cctrace/profiles/*/sync-runtime.json 2>/dev/null
```

Claude Code 를 새로 켜면 SessionStart 훅이 각 claude-dir 에서 데몬을 되살리므로
(`internal/envgen/hooks.go`), 위 루프를 건너뛰어도 다음 세션에서 복구됩니다. 다만 그때까지의
수집은 비어 있습니다.

**설정과 프로필은 이동 대상이 아닙니다.** `~/.cctrace` 아래에 그대로 있고 바이너리 위치와 무관합니다.

## Troubleshooting

### `command not found` -- 새 터미널에서도 안 됨

Step 5에서 추가한 RC 파일을 확인:
```bash
grep cctrace ~/.zshrc ~/.bashrc ~/.profile 2>/dev/null
```

없으면 수동 추가:
```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

### Permission denied (설치 실패)

```bash
sudo cp /tmp/cctrace-new /usr/local/bin/cctrace
sudo chmod +x /usr/local/bin/cctrace
```

### Go build 실패

Go 1.22+ 필요: https://go.dev/dl/

## Notes

- 빌드된 바이너리에 ldflags 를 주지 않으면 `cctrace --version` 이 `dev` 로 나옵니다.
  **정상이 아닙니다.** 그 바이너리는 버전 비교(`version != "dev"`)에서 걸러져 자동
  업데이트를 시도조차 하지 않고, 신뢰 루트까지 비어 있으면 시도해도
  `update public key is not configured` 로 영구 실패합니다. 위 Step 의 ldflags 를
  그대로 사용하세요
- 이미 설치된 경우 `/patch` 스킬과 동일한 효과
- 데몬은 설치 후 수동으로 시작: `cctrace sync --daemon`
