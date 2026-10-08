# 설치

`cctrace` 클라이언트를 소스에서 빌드해 `PATH`에 두는 절차. 세션을 보고할 개발자 머신마다 하나씩 필요하다.

## 준비물

- Go 1.25 이상 (`go.mod`)
- `make`
- 실행 중인 서버. [서버 설치](../server/install.md) 참고

클라이언트에는 Node.js와 Docker가 필요 없다. 둘은 서버와 대시보드 빌드용이다.

## 클라이언트 빌드

`make build-client`는 현재 플랫폼용 클라이언트 하나를 `dist/`에 만든다. `make build`와 달리 대시보드 빌드가 필요 없고 macOS 밖(Linux)에서도 동작한다.

```bash
make build-client
```

다른 플랫폼용은 `make build-linux`(linux/amd64), `make build-windows`(windows/amd64). 결과물 위치는 똑같이 `dist/`.

!!! note "자동 갱신 없음"
    이 저장소에서 빌드한 클라이언트는 업데이트 공개키가 비어 있어 자동 갱신을 거부한다. 새 버전은 직접 다시 빌드해 교체한다. [클라이언트 업그레이드](sync.md#upgrading-the-client) 참고.

??? note "팀 배포용 기본 엔드포인트"
    `cctrace init`은 서버의 sync 엔드포인트와 OTEL 엔드포인트를 묻는다. 나눠 줄 바이너리에 이 프롬프트의 기본값을 넣으려면 `deploy/local-defaults.env.example`을 같은 디렉터리에 local-defaults.env 라는 이름으로 복사하고 `DEFAULT_SYNC_ENDPOINT`, `DEFAULT_OTEL_ENDPOINT`를 채운 뒤 다시 빌드한다. 이 파일이 없으면 프롬프트가 빈 값으로 시작하고 사용자가 두 주소를 직접 입력한다. 이 파일은 `make`만 읽는다. 서버 이미지가 제공하는 바이너리(아래)에는 대신 `docker build`에 `--build-arg DEFAULT_SYNC_ENDPOINT=...`, `--build-arg DEFAULT_OTEL_ENDPOINT=...`를 넘긴다.

## 내 서버에서 내려받기 {#download-from-your-own-server}

[서버 설치](../server/install.md)에서 빌드한 서버 이미지는 클라이언트도 함께 크로스 컴파일해 서버에서 제공한다. 서버에 닿는 머신이라면 빌드 대신 내려받을 수 있다.

| 플랫폼 | 서버 경로 |
|---|---|
| macOS, Apple silicon | `/downloads/cctrace-darwin-arm64` |
| macOS, Intel | `/downloads/cctrace-darwin-amd64` |
| Linux, x86-64 | `/downloads/cctrace-linux-amd64` |
| Linux, ARM64 | `/downloads/cctrace-linux-arm64` |
| Windows, x86-64 | `/downloads/cctrace-windows-amd64.exe` |

이 경로는 `cctrace init`에서 sync 엔드포인트로 쓰는 주소(포트 포함)에서 제공된다. 아래 `<sync endpoint>`를 그 주소로, `<platform>`을 위 표에서 자신의 플랫폼에 맞는 파일명 접미사(예: `linux-amd64`)로 바꾼다.

```console
$ curl -fL -o cctrace "<sync endpoint>/downloads/cctrace-<platform>"
$ chmod +x cctrace
```

내려받기는 HTTPS 또는 신뢰할 수 있는 네트워크에서만. 서버 인증서가 사설 CA([서버 설치](../server/install.md#certificates)의 Caddy `tls internal` 기본값)에서 나왔다면 `curl`은 아직 그 CA를 신뢰하지 않으므로 `--cacert cctrace-ca.crt`를 붙인다.

서버 이미지를 `--build-arg VERSION=...` 없이 빌드했다면 이 바이너리의 버전은 `dev`. `cctrace --version`은 `dev`를 출력하고 `cctrace status`는 개발 빌드로 표시한다. 이 저장소에서 빌드한 다른 클라이언트와 마찬가지로 스스로 갱신하지 않는다.

## PATH 등록

`cctrace init` 실행 전에 바이너리를 계속 둘 위치로 옮겨 둔다. `init`이 쓰는 Claude Code 훅은 실행한 바이너리의 절대 경로를 그대로 기록하므로, `dist/`나 내려받은 디렉터리의 바이너리로 실행하면 훅이 그 경로를 계속 가리킨다. 나중에 바이너리를 옮겼다면 새 위치에서 `cctrace env apply`로 훅을 다시 쓴다.

빌드했다면 바이너리는 `dist/cctrace`, 내려받았다면 `curl`을 실행한 디렉터리의 `cctrace`다.

```console
$ sudo cp dist/cctrace /usr/local/bin/cctrace   # make build-client로 빌드한 경우
$ sudo cp ./cctrace /usr/local/bin/cctrace      # 서버에서 내려받은 경우
$ cctrace --version
cctrace version dev
```

이 단계를 건너뛰면 `init`이 대신 제안한다. `init` 종료 시점에 `cctrace`가 `PATH`에 없으면 다음을 묻는다.

```text
  [!] 'cctrace' is not in $PATH. Install to /usr/local/bin? (y/n) [y]:
```

- **macOS, Linux:** 실행 중인 바이너리를 `/usr/local/bin/cctrace`로 복사. 권한 부족으로 실패하면 `sudo`로 재시도
- **Windows:** 홈 디렉터리 아래 `AppData\Local\Programs\cctrace`로 복사하고 그 디렉터리를 사용자 `Path`에 추가. 이후 새 터미널 필요

거절하거나 복사가 실패하면 `init`이 수동 방법을 출력한다. `go install ./cmd/cctrace` 후 `~/go/bin`을 `PATH`에 추가하거나, 이미 `PATH`에 있는 디렉터리로 바이너리를 복사하는 방법이다.

이 복사는 `init`이 훅을 쓴 뒤에 일어나므로 훅에는 `init`을 실행한 바이너리 경로가 남는다. 설치된 사본으로 `cctrace env apply`를 한 번 실행해 둔다.

## 다음 단계

[클라이언트를 서버에 연결](setup.md).
