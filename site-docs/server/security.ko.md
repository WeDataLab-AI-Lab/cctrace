# 보안 운영

클라이언트가 내 서버에서 스스로 업데이트하게 하는 방법, 서버 정책에 서명하는 방법, 그 둘을 떠받치는 키를 보관하는 방법.

[설치](install.md)대로 `UPDATE_SIGNING=optional`에 키 없이 빌드한 서버는 이 내용을 하나도 쓰지 않는다. 그 서버의 클라이언트는 빈 공개키를 링크하므로 자동 업데이트를 거부하고 서버 정책도 적용하지 않는다. 이 쪽의 모든 절차는 직접 만든 키 쌍에서 시작한다.

## 두 신뢰 루트 {#two-trust-roots}

| 키 쌍 | 서명 대상 | 클라이언트가 받아들이는 용도 | 개인키를 쓰는 곳 |
|---|---|---|---|
| 업데이트 키 | `/downloads`의 각 클라이언트 바이너리 옆 매니페스트 | 업데이트와 서버 정책 | 이미지 빌드(BuildKit secret) |
| 정책 키 | 서버 정책 | 서버 정책만 | 정책 서명 도구(정책에 서명할 때마다) |

클라이언트는 빌드할 때 링크된 공개키만 믿고, 서버가 건네는 키는 믿지 않는다. 업데이트 매니페스트는 업데이트 공개키로 검사한다. 정책은 정책 공개키와 업데이트 공개키를 합친 목록으로 검사한다. 두 빌드 값은 모두 쉼표로 구분한 목록을 받으며, 목록의 어느 키로 만든 서명이든 통과한다.

키 쌍은 둘을 따로 만든다. 정책은 클라이언트가 쓰는 모든 서버 주소와 클라이언트가 신뢰하는 인증 기관을 바꾸고, 업데이트는 바이너리 자체를 바꾼다. 쌍을 나누면 정책 서명에 꺼내 쓰는 키로는 클라이언트가 설치할 바이너리에 서명할 수 없다. 이미지 빌드는 업데이트 공개키와 같은 정책 공개키를 거부한다. 업데이트 키가 정책에도 계속 신뢰되는 것은 정책 개인키를 잃었을 때도 정책에 서명할 수 있게 하기 위해서다.

## 키 쌍 만들기 {#generate-a-key-pair}

두 쌍 모두 표준 base64로 쓴 raw Ed25519 키다.

- 공개키: 32바이트, 44자 한 줄
- 개인키: 64바이트 파일. 32바이트 seed 뒤에 32바이트 공개키

OpenSSL은 키를 DER 또는 PEM 컨테이너로 쓰는데, 빌드도 서명 도구도 이 형식을 그대로 받지 않는다. 아래 명령은 DER 파일에서 raw 바이트를 꺼낸다. OpenSSL 1.1.1 이상이 필요하다. macOS에 들어 있는 LibreSSL은 `Algorithm ed25519 not found`로 답한다.

키는 저장소 밖 디렉터리에 두어 어떤 빌드 컨텍스트에도 들어가지 않게 한다. 이 쪽의 예시는 `KEYS=../cctrace-keys`(클론 옆 디렉터리)를 쓰고 저장소 루트에서 실행한다.

```console
$ KEYS=../cctrace-keys
$ mkdir -p "$KEYS" && chmod 700 "$KEYS"
$ umask 077
$ openssl genpkey -algorithm ed25519 -outform DER -out "$KEYS/update.der"
$ openssl pkey -inform DER -in "$KEYS/update.der" -pubout -outform DER -out "$KEYS/update.pub.der"
$ tail -c 32 "$KEYS/update.pub.der" | openssl base64 -A > "$KEYS/update-public-key.txt"
$ cat <(tail -c 32 "$KEYS/update.der") <(tail -c 32 "$KEYS/update.pub.der") \
    | openssl base64 -A > "$KEYS/update-private-key"
$ rm "$KEYS/update.der" "$KEYS/update.pub.der"
```

DER 개인키의 끝 32바이트가 seed이고, DER 공개키의 끝 32바이트가 공개키다. 정책 키 쌍은 같은 명령에서 `update`를 `policy`로 바꿔 만든다.

쓰기 전에 형식을 확인한다.

```console
$ openssl base64 -d -A < "$KEYS/update-public-key.txt" | wc -c
32
$ openssl base64 -d -A < "$KEYS/update-private-key" | wc -c
64
$ openssl base64 -d -A < "$KEYS/update-private-key" | tail -c 32 | openssl base64 -A \
    | cmp - "$KEYS/update-public-key.txt" && echo "pair ok"
pair ok
```

세 번째 명령은 개인키 파일이 클라이언트에 넣을 공개키로 끝나는지 확인한다. seed 32바이트만 있는 파일이나 PEM 파일은 64바이트로 풀리지 않으며 서명 도구도 거부한다.

## 클라이언트에 키 넣기 {#build-the-keys-into-clients}

클라이언트는 Go 링커 플래그 `-X main.updatePublicKeyBase64=...`와 `-X main.policyPublicKeyBase64=...`로 키를 링크하며, 아래 빌드가 이 플래그를 대신 넣는다. 클라이언트를 만드는 빌드는 두 종류다.

| 빌드 | 키를 넘기는 곳 |
|---|---|
| 서버 이미지(`/downloads`에서 배포하는 바이너리) | `--build-arg UPDATE_PUBLIC_KEY=...`와 `--build-arg POLICY_PUBLIC_KEY=...`. [다음 절](#build-a-signed-server-image) 참고 |
| `make build-client`, `build-linux`, `build-windows`, `build`, `install` | Make 변수 `UPDATE_PUBLIC_KEY`와 `POLICY_PUBLIC_KEY` |

make에는 명령줄로 넘기거나, 기본 엔드포인트를 담는 local-defaults.env 파일에 적는다([클라이언트 설치](../client/install.md) 참고).

```console
$ make build-client UPDATE_PUBLIC_KEY="$(cat "$KEYS/update-public-key.txt")" \
    POLICY_PUBLIC_KEY="$(cat "$KEYS/policy-public-key.txt")"
```

`make print-update-public-key`와 `make print-policy-public-key`는 make가 링크할 값을 출력한다. 명령줄과 그 파일 어디에도 값이 없으면 빈 줄을 출력하고, 그때 빌드한 클라이언트는 빈 키를 링크한다. 이미지 빌드는 local-defaults.env를 읽지 않는다.

## 서명된 서버 이미지 빌드 {#build-a-signed-server-image}

이미지 빌드는 배포하는 클라이언트 바이너리마다 매니페스트에 서명한다. 기본값인 `UPDATE_SIGNING=required`에서는 입력 세 가지가 필요하며 하나라도 빠지면 멈춘다. 빌드 인자로 넘기는 두 공개키와 BuildKit secret `update_signing_key`로 넘기는 업데이트 개인키다.

```console
$ docker build -f deploy/Dockerfile \
    --build-arg VERSION=v1.0.0 \
    --build-arg UPDATE_PUBLIC_KEY="$(cat "$KEYS/update-public-key.txt")" \
    --build-arg POLICY_PUBLIC_KEY="$(cat "$KEYS/policy-public-key.txt")" \
    --secret id=update_signing_key,src="$KEYS/update-private-key" \
    -t cctrace/cctraced:latest .
```

- `VERSION`은 서버가 알리는 버전이자 이미지 안 모든 클라이언트 바이너리에 찍히는 버전이다. 클라이언트는 v1.2.3 형식의 더 높은 버전으로만 업데이트한다([아래](#when-a-client-updates-itself) 참고). 지정하지 않으면 둘 다 `dev`가 되어 어떤 클라이언트도 업데이트하지 않는다.
- `--secret`에는 BuildKit이 필요하다. Docker Engine 23.0부터 `docker build`의 기본 빌더다. secret은 서명 단계에만 마운트되며 빌드 컨텍스트, 레이어, 이미지 어디에도 들어가지 않는다. 개인키는 이 방법으로만 넘긴다. 빌드 인자로 넘기지 않고, 빌드가 복사하는 빌드 컨텍스트인 저장소 아래에 두지 않는다.
- secret이 없으면 서명 단계가 자체 메시지 없이 종료 코드 1로 실패한다.
- 서명 단계는 개인키가 `UPDATE_PUBLIC_KEY`의 키 중 하나와 짝인지 확인하고, 아니면 `signing private key is not among the declared update public keys`로 멈춘다.
- 이 모드에서는 `POLICY_PUBLIC_KEY`도 필수다. 각 항목은 표준 base64로 쓴 32바이트 키여야 하며 `UPDATE_PUBLIC_KEY`의 항목과 같으면 안 된다.
- 배포 바이너리의 기본 엔드포인트도 같은 방식으로 `DEFAULT_SYNC_ENDPOINT`, `DEFAULT_OTEL_ENDPOINT`에 넘긴다.

결과를 확인한다. 이미지에 바이너리 다섯 개와 각각의 매니페스트가 있고 secret은 없어야 한다.

```console
$ docker run --rm --entrypoint sh cctrace/cctraced:latest -c \
    'ls -1 /downloads; test ! -e /run/secrets/update_signing_key && echo "no secret"'
cctrace-darwin-amd64
cctrace-darwin-amd64.manifest.json
cctrace-darwin-arm64
cctrace-darwin-arm64.manifest.json
cctrace-linux-amd64
cctrace-linux-amd64.manifest.json
cctrace-linux-arm64
cctrace-linux-arm64.manifest.json
cctrace-windows-amd64.exe
cctrace-windows-amd64.exe.manifest.json
no secret
```

매니페스트는 파일 이름과 함께 그 파일의 SHA-256, 버전, 서명 두 개를 담는다. 하나는 파일 이름과 체크섬에 대한 서명이고, 다른 하나는 버전까지 더한 서명이다.

`/downloads`는 `cctraced` 이미지 안의 디렉터리다. compose 파일은 그 위에 볼륨을 마운트하지 않으므로, 바이너리와 매니페스트는 [업그레이드](operations.md#upgrade)처럼 이미지를 다시 빌드하고 컨테이너를 다시 만들 때만 바뀐다. 릴리스 하나는 더 높은 `VERSION`으로 한 서명 빌드 하나다.

## 클라이언트가 스스로 업데이트하는 조건 {#when-a-client-updates-itself}

클라이언트는 다음을 모두 만족할 때만 자기 바이너리를 바꾼다.

- 업데이트 공개키를 링크했다. 서명된 이미지에서 내려받았거나 `UPDATE_PUBLIC_KEY`를 넘겨 make로 빌드한 경우다.
- 자기 버전이 v1.2.3 형식이고 서버가 더 높은 버전을 알린다. v1.2.3-rc1처럼 세 번째 숫자 뒤에 붙은 꼬리는 비교에서 무시한다. `dev` 빌드는 업데이트하지 않는다. 태그가 없는 클론에서 make로 빌드해 버전이 커밋 해시뿐인 클라이언트도 마찬가지다. make에 `VERSION=v1.2.3`을 넘긴다.
- 동기화 엔드포인트가 HTTPS이거나, `localhost` 또는 루프백·사설(RFC 1918, RFC 4193)·링크 로컬 대역 IP 주소로 가는 평문 HTTP다. 호스트 이름으로 가는 평문 HTTP는 거부한다. 그 이름이 어디로 풀리는지 클라이언트가 판단할 수 없기 때문이다. 이 규칙 밖의 주소로 가는 리디렉션도 거부한다.
- 매니페스트가 업데이트 공개키 중 하나로 검증되고, 두 서명이 모두 통과하고, 매니페스트의 버전이 서버가 알린 버전과 같고, 내려받은 파일이 매니페스트의 체크섬과 맞는다.
- 클라이언트를 실행하는 사용자가 바이너리가 있는 디렉터리에 쓸 수 있다. 업데이트는 새 파일을 기존 파일 옆에 쓰므로, root 소유의 `/usr/local/bin`처럼 root만 쓸 수 있는 디렉터리에 있는 바이너리는 스스로 업데이트하지 못한다.

일회성 `cctrace sync`는 업로드 전에 확인한다. 동기화 데몬은 시작할 때와 실행 중 약 5분마다 확인한다. 실패한 시도는 프로필 로그(기본 프로필은 `~/.cctrace/sync.log`)에 `update:` 줄을 남기며 수집은 현재 바이너리로 계속된다. 실패 뒤 데몬은 같은 버전에 대한 다음 시도까지 15분, 30분, 그다음부터 1시간씩 기다린다. 직접 실행한 `cctrace sync`는 바로 시도한다. 각 클라이언트가 마지막으로 알린 버전은 대시보드의 **Admin > Clients**에서 볼 수 있다.

클라이언트가 알아볼 수 없는 공인 대역 주소의 사설망이라면, 클라이언트 환경에 `CCTRACE_UPDATE_ALLOW_INSECURE_ENDPOINT=1`을 지정해 전송 규칙을 건너뛸 수 있다. 서명과 체크섬 검사는 그대로 적용된다.

## 개인키 보관 {#store-the-private-keys}

- `/downloads`를 배포하는 서버에 개인키를 두지 않는다. 서명은 그 서버가 장악당해도 클라이언트가 위조 바이너리를 거부하게 하려고 있다. 같은 호스트에 키를 두면 공격자가 바이너리와 서명을 함께 얻는다.
- 개인키 파일은 권한 0600으로 저장소와 모든 빌드 컨텍스트 밖에 둔다. 이미지 빌드는 정책 개인키를 쓰지 않으므로 정책에 서명할 때만 꺼낸다.
- 개인키마다 오프라인 사본을 둔다. 예를 들어 잠긴 장소의 이동식 매체에 두고, 업데이트 키 사본과 정책 키 사본은 따로 보관한다. 사본을 만든 뒤 원본과 비교하고 공개키와 짝인지 확인한다.

```console
$ cmp "$KEYS/update-private-key" "<copy>"
$ openssl base64 -d -A < "<copy>" | tail -c 32 | openssl base64 -A \
    | cmp - "$KEYS/update-public-key.txt" && echo "pair ok"
```

## 업데이트 키 교체 {#rotate-the-update-key}

클라이언트는 업데이트 공개키 중 어느 하나로 검증되는 매니페스트를 받아들인다. 클라이언트가 두 키를 함께 믿는 구간을 거쳐 교체한다.

1. 새 키 쌍 생성
2. `UPDATE_PUBLIC_KEY="<old>,<new>"`로 릴리스. 서명은 여전히 기존 개인키로
3. 모든 클라이언트가 그 릴리스로 올라올 때까지 대기(**Admin > Clients**)
4. 새 개인키로 서명해 릴리스. 목록에는 두 키를 그대로 둠
5. 이후 릴리스에서 목록에 새 키만 남김

단계마다 릴리스이므로 `VERSION`도 매번 올린다. 4단계 시점까지 2단계 릴리스로 업데이트하지 않은 클라이언트는 기존 키만 믿는다. 이 클라이언트는 새 릴리스마다 `update manifest signature verification failed`로 거부하고 지금 바이너리로 계속 동작하며, 한 번은 손으로 다시 설치해야 한다.

## 정책 키 교체 {#rotate-the-policy-key}

같은 겹침 구간을 `POLICY_PUBLIC_KEY`로 거친다.

1. 새 정책 키 쌍 생성
2. `POLICY_PUBLIC_KEY="<old>,<new>"`로 릴리스
3. 모든 클라이언트가 그 릴리스로 올라온 뒤 새 정책은 새 개인키로 서명
4. 이후 릴리스에서 목록에 새 키만 남김

2단계 릴리스에 이르지 않은 클라이언트는 새 키로 서명한 정책을 `untrusted policy signature`로 거부한다.

## 키를 잃었을 때 {#if-a-key-is-lost}

- **사본 없이 업데이트 개인키를 잃은 경우.** 클라이언트가 믿는 키로 새 릴리스에 서명할 수 없다. 새 키 쌍을 만들어 새 키만으로 릴리스하고, 모든 클라이언트를 한 번 손으로 다시 설치한다. [클라이언트 설치](../client/install.md)처럼 `/downloads`에서 바이너리를 내려받으면 된다. 그 뒤로는 자동 업데이트가 다시 동작한다.
- **정책 개인키를 잃은 경우.** 클라이언트는 정책에도 업데이트 키를 믿으므로 다음 정책은 업데이트 개인키로 서명한다. 잃은 키 대신 새 정책 공개키를 넣어 릴리스하고, 클라이언트가 그 릴리스로 올라온 뒤에는 다시 새 정책 키로 서명한다.
- **복사되었을 수 있는 개인키**는 잃은 것으로 본다. 클라이언트는 그 공개키를 목록에서 뺀 릴리스를 실행하기 전까지 그 키가 서명한 것을 계속 받아들인다.

## 서버 정책 서명 {#sign-a-server-policy}

정책은 프로필을 같은 서버 호스트의 새 주소로 옮긴다. 평문에서 HTTPS로, 다른 포트로, 새 인증 기관으로 옮길 수 있고 다른 호스트로는 옮기지 못한다. 서버는 정책이 지목한 클라이언트에 서명된 파일을 건네고, 서명·유효 기간·정책 번호는 각 클라이언트가 직접 검사한다. 서버가 파일을 배포하는 방법은 [운영](operations.md#server-policy)에, 정책을 적용할 때 클라이언트가 바꾸는 것은 [동기화 데몬](../client/sync.md#server-policy)에 있다.

### 필드 {#fields}

| 필드 | 값 |
|---|---|
| `schema` | `1` |
| `sequence` | 정책 번호. 0보다 큼. [정책 번호와 유효 기간](#policy-numbers-and-validity) 참고 |
| `server_host` | 정책 안 모든 주소의 호스트. 소문자, scheme과 포트 제외 |
| `not_before`, `expires_at` | 유효 기간. RFC 3339 형식(예: `2026-11-02T00:00:00Z`) |
| `targets` | `{"installation_id": ..., "profile": ...}` 배열. 기본 프로필은 `profile`이 빈 문자열 |
| `all_targets` | `true`면 이 서버에 묻는 모든 설치와 프로필이 대상이며 `targets`는 비워야 함. 그 밖에는 생략 |
| `from` | 정책이 옮겨 가기 전의 주소 묶음 배열. 각각 `{"sync": ..., "otel": ..., "protocol": ...}` |
| `to` | 옮겨 갈 주소 묶음 |
| `ca_pem` | PEM 형식 CA 인증서 하나 이상을 담은 문자열 하나 |
| `ca_sha256` | `ca_pem` 바이트의 SHA-256, 소문자 hex |

서명 도구와 모든 클라이언트가 검사하는 것:

- 모든 주소는 `server_host`와 명시적 포트를 쓴다. 경로는 비어 있거나 `/`다. 사용자 정보, 쿼리, 프래그먼트는 거부한다.
- 각 `protocol`은 grpc 또는 http/protobuf다. `to`의 두 주소는 `https://`로 시작하고 프로토콜은 http/protobuf다.
- `ca_pem`의 모든 인증서는 CA 인증서다.
- 알 수 없는 필드가 없고, 서명한 파일이 256 KiB를 넘지 않는다.

값을 구하는 곳:

- `installation_id`와 `profile`: 클라이언트에서 `cctrace config policy status`가 둘 다 출력한다. 키를 링크한 클라이언트는 로그인된 프로필의 정책 확인마다 이 값과 현재 주소를 서버에 보고도 한다. 보고는 `client_policy_reports` 테이블에 쌓이며 대시보드 화면이나 API는 없다([개인정보](../reference/privacy.md) 참고).
- `from`: 묶음의 세 값이 클라이언트에서 `cctrace config get server.sync_endpoint`, `cctrace config get server.endpoint`, `cctrace config get server.protocol`이 출력하는 값과 문자열로 같을 때만 그 프로필에 맞는다.
- `to`: 클라이언트는 아무것도 바꾸기 전에 새 `sync` 주소의 `/api/health`와 `/api/version`을 요청하고, 새 `otel` 주소의 `/v1/logs`와 `/v1/metrics`에 프로필의 업로드 토큰을 붙인 빈 요청을 보낸다. 이때 시스템 루트와 `ca_pem`을 신뢰한다. 200이 아닌 응답이 하나라도 오면 정책을 멈춘다. 그래서 새 `otel` 주소는 OTLP/HTTP 주소여야 하고, 정책을 게시하기 전에 TLS 리스너가 떠 있어야 한다. [HTTPS 오버레이](install.md#optional-https-with-caddy)라면 새 `sync` 주소는 8443 포트, 새 `otel` 주소는 5318 포트다.
- `ca_pem`: 서버 인증서를 발급한 CA. 예를 들어 [인증서](install.md#certificates)에서 내보낸 CA 파일이다.

### 작성과 서명 {#write-and-sign}

jq로 정책 파일을 만들면 PEM의 줄바꿈이 이스케이프된다. 아래 예시는 cctrace.example.com 서버에서 설치 하나의 기본 프로필을 평문 포트에서 HTTPS 오버레이로 옮기며, 서버의 내부 CA를 cctrace-ca.crt로 내보내 두었다고 가정한다.

```console
$ jq -n --rawfile ca cctrace-ca.crt \
    --arg sum "$(openssl dgst -sha256 -r cctrace-ca.crt | cut -d' ' -f1)" '{
  schema: 1,
  sequence: 1,
  server_host: "cctrace.example.com",
  not_before: "2026-11-02T00:00:00Z",
  expires_at: "2026-11-16T00:00:00Z",
  targets: [{installation_id: "<installation ID>", profile: ""}],
  from: [{sync: "http://cctrace.example.com:8080", otel: "http://cctrace.example.com:4317", protocol: "grpc"}],
  to: {sync: "https://cctrace.example.com:8443", otel: "https://cctrace.example.com:5318", protocol: "http/protobuf"},
  ca_pem: $ca,
  ca_sha256: $sum
}' > policy.json
```

정책 개인키로 서명한다. `-public-key`에는 클라이언트가 정책에 신뢰하는 목록을 넘긴다.

```console
$ go run ./scripts/sign-server-policy.go \
    -private-key "$KEYS/policy-private-key" \
    -public-key "$(cat "$KEYS/policy-public-key.txt"),$(cat "$KEYS/update-public-key.txt")" \
    -out server-policy.json \
    policy.json
```

- 도구는 클라이언트와 같은 필드 검사를 하고 첫 문제에서 멈춘다. 예: `sign policy: destination requires HTTPS and http/protobuf`
- 서명 키가 `-public-key`에 있는지만 확인하며, 클라이언트가 그 목록으로 빌드되었는지는 확인하지 않는다.
- 서명한 파일에는 정책과 서명만 있고 비밀은 없다. 도구는 파일을 권한 0600으로 쓰므로, 제자리에 두기 전에 `chmod 644` 등으로 서버가 읽을 수 있게 한다.

서명한 파일은 서버로 복사해 [운영](operations.md#server-policy)에 적힌 대로 한 번에 제자리에 둔다. 마운트한 디렉터리에 임시 이름으로 복사한 뒤 이름 바꾸기로 기존 파일 위에 덮어쓴다.

### 정책 번호와 유효 기간 {#policy-numbers-and-validity}

클라이언트는 프로필마다 마지막으로 적용한 정책 번호를 기억하고, 받은 정책을 그 번호와 비교한다.

| 받은 번호 | 결과 |
|---|---|
| 더 작음 | 거부: `policy sequence <n> is older than applied sequence <m>` |
| 같고, 내용이 같고, 프로필이 아직 `to`에 있음 | 이미 적용됨. 아무 일도 없음 |
| 같지만 내용이 다르거나 프로필이 더는 `to`에 없음 | 거부: `applied policy changed; require a higher policy sequence or explicit manual recovery` |
| 더 큼 | `targets`와 `from`으로 검사한 뒤 적용 |

- 서버는 날짜를 검사하지 않는다. 클라이언트는 `not_before` 전이나 `expires_at`부터는 정책을 `policy outside validity interval`로 거부한다. 이를 실패한 확인으로 기록하고, 파일이 그대로 있는 동안 확인할 때마다(실행 중인 데몬은 약 5분마다) 같은 실패를 반복한다.
- 정책을 연장하거나 내용을 바꾸려면 더 높은 번호로 다시 서명한다. 같은 번호에 날짜만 바꿔도 내용이 바뀐 것이므로, 그 정책을 이미 적용한 클라이언트는 거부한다.
- 더 높은 번호는 이전 정책을 적용한 프로필에도 간다. `targets`가 그 프로필을 지목하거나 `all_targets`가 켜져 있을 때다. 그 프로필의 현재 주소는 이전 정책의 `to`이므로 그 묶음을 평문 묶음과 함께 `from`에 넣는다. 넣지 않으면 `current endpoints are not authorized by policy`로 실패한다. 넣으면 새 정책을 적용하며, 주소는 그대로이고 새 `ca_pem`을 새 CA 파일에 쓴다.
- `all_targets`면 이 서버에 묻는 모든 프로필이 정책을 받으며, 현재 묶음이 `from`에 없는 프로필은 확인할 때마다 `current endpoints are not authorized by policy`로 실패한다.
- 정책 배포를 멈추려면 `CCTRACE_SERVER_POLICY_FILE`을 비우고 컨테이너를 다시 만든다. 그러면 서버는 204로 답한다. 파일을 지우면 503으로 답한다. 배포를 멈춰도 되돌아가는 프로필은 없다. 정책을 적용한 프로필은 새 주소를 유지한다.

## TLS {#tls}

정책은 이미 돌고 있는 HTTPS로만 클라이언트를 옮길 수 있다. 먼저 [선택: Caddy로 HTTPS 적용](install.md#optional-https-with-caddy)과 [인증서](install.md#certificates)대로 구성한다.

- 내부 CA는 `caddy_data` 볼륨에 있다. 볼륨을 지우면 새 CA가 만들어지고, 이전 CA만 믿는 클라이언트는 모두 연결이 끊긴다. 정책으로 옮긴 클라이언트도 그 정책의 `ca_pem`을 믿으므로 마찬가지다.
- 오버레이의 첫 단계는 평문 포트를 루프백에 바인드한다. 이 바인드 설정을 빼면 평문 포트가 TLS 포트 옆에서 계속 열려 있다. 정책으로 옮길 때는 이 상태가 필요하다. 평문 주소를 쓰는 클라이언트는 평문 주소에서 정책을 받아 오고, 정책을 적용할 때까지 그쪽으로 계속 보낸다. 대가는 평문 포트로 오가는 모든 것이 암호화되지 않은 채 네트워크를 지난다는 점이다. 업로드 토큰, 대시보드 비밀번호와 세션 토큰, 동기화가 올리는 대화 내용이 여기에 든다. 모든 클라이언트가 옮겨 가면 평문 포트를 루프백에 바인드한다.
- 정책으로 옮긴 클라이언트는 그 정책 `from`의 평문 동기화 주소를 기억한다. HTTPS 주소에 연결할 수 없거나 인증서를 믿을 수 없으면 그 평문 주소에 인증 정보 없이 정책을 묻는다. 평문 포트를 닫으면 CA 교체를 놓친 클라이언트가 돌아올 이 길도 사라진다.
