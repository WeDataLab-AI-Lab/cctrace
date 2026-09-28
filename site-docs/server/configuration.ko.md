# 서버 설정

서버 설정은 환경변수로만 한다. Docker Compose에서는 서버 env 파일(`deploy/` 디렉터리의 `.env`)에 값을 넣고 compose 파일이 그중 정해진 일부를 `cctraced` 컨테이너에 전달한다.

키는 두 종류다.

- **Compose 키**: 호스트 포트, 바인드 주소, 이미지 태그, 로그 디렉터리 등 컨테이너 구성. `cctraced`에는 전달되지 않음
- **서버 변수**: `cctraced`가 직접 읽는 값. Compose는 `environment:` 블록에 적힌 것만 전달

주석이 달린 템플릿은 `deploy/.env.example`이다. 서버가 읽는 변수 목록은 컨테이너 안에서 `cctraced --help`로 확인한다.

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec cctraced cctraced --help
```

값을 바꾼 뒤에는 컨테이너를 다시 만들어야 적용된다.

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

`CCTRACE_AI_*` 변수와 `CCTRACE_SECRETS_KEY`는 변경이 진행 중이라 이 문서에서 다루지 않음.

## 필수 {#required}

| 키 | 기본값 | 의미 |
|----|--------|------|
| `JWT_SECRET` | 없음 | 대시보드 세션 서명 키. 32바이트 이상. 없으면 Compose가 시작 거부, 짧으면 `cctraced`가 `JWT secret must be at least 32 bytes`로 종료 |
| `LOGS_DIR` | 없음 | `cctraced` 컨테이너의 `/data/logs`에 마운트되는 호스트 디렉터리. 없으면 Compose가 시작 거부. [운영](operations.md#logs) 참조 |

## 데이터베이스 {#database}

| 키 | 기본값 | 의미 |
|----|--------|------|
| `DB_PASSWORD` | `cctrace` | `cctrace` DB 슈퍼유저 비밀번호. DB 볼륨 최초 초기화 때만 적용. 첫 시작 전에 지정 |
| `DB_APP_CREDENTIALS` | 비어 있음 | `cctraced`용 비슈퍼유저 역할의 `<user>:<password>`. 비워 둘 것(아래 참조) |
| `DB_PORT` | `5432` | TimescaleDB 호스트 포트. 항상 127.0.0.1에 바인드 |

Compose는 이 값들로 `cctraced`의 `DATABASE_URL`을 만든다. `DB_APP_CREDENTIALS`가 비어 있으면 `cctraced`는 `cctrace` 계정과 `DB_PASSWORD`로 접속한다.

!!! note "이 저장소에서의 `DB_APP_CREDENTIALS`"
    compose 파일은 compose 파일 옆의 `initdb` 디렉터리를 초기화 스크립트 위치로 마운트한다. 그 스크립트가 첫 시작 때 `DB_APP_CREDENTIALS`의 역할을 만든다. 이 스크립트는 이 저장소에 포함되어 있지 않다. 역할을 직접 만들지 않고 `DB_APP_CREDENTIALS`를 지정하면 `cctraced`가 인증에 실패한다.

## 네트워크 {#network}

| 키 | 기본값 | 의미 |
|----|--------|------|
| `HTTP_BIND` | 127.0.0.1 | 대시보드·REST API 포트의 호스트 주소 |
| `GRPC_BIND` | 0.0.0.0 | OTLP gRPC 호스트 주소 |
| `OTEL_HTTP_BIND` | 0.0.0.0 | OTLP HTTP 호스트 주소 |
| `HTTP_PORT` | `8080` | 대시보드·REST API 호스트 포트 |
| `GRPC_PORT` | `4317` | OTLP gRPC 호스트 포트 |
| `OTEL_HTTP_PORT` | `4318` | OTLP HTTP 호스트 포트 |

모두 Compose 키다. 컨테이너 안의 `cctraced`는 항상 8080, 4317, 4318에서 수신한다. compose 파일이 컨테이너용 `HTTP_PORT`, `GRPC_PORT`, `HTTP_OTEL_PORT`를 이 값으로 고정한다. `OTEL_HTTP_PORT`(호스트, Compose)와 `HTTP_OTEL_PORT`(컨테이너, 서버)는 이름이 비슷하지만 다른 키다.

TLS 오버레이를 포함한 전체 표는 [포트](../reference/ports.md) 참조.

## 인증과 브라우저 접근 {#authentication-and-browser-access}

| 키 | 기본값 | 의미 |
|----|--------|------|
| `CCTRACE_SETUP_TOKEN` | 비어 있음 | 첫 관리자 생성용 토큰. 비어 있으면 사용자가 없는 동안 `cctraced`가 생성해 로그에 출력. 첫 관리자 생성 후 무효 |
| `COOKIE_SECURE` | `0` | `1`이면 인증 쿠키에 Secure 플래그 강제. 프록시를 거쳐 HTTPS로 대시보드를 제공할 때 지정. TLS 요청이나 `X-Forwarded-Proto: https` 요청은 이 값과 무관하게 Secure 쿠키 |
| `CCTRACE_ALLOWED_ORIGINS` | 비어 있음 | API 호출을 허용할 다른 호스트·포트의 브라우저 origin 목록(쉼표 구분). 경로 없는 정확한 `scheme://host[:port]` 값. 와일드카드 불가. 비어 있으면 CORS 비활성. 번들 대시보드에는 불필요 |
| `API_KEY` | 비어 있음 | 사용자별 토큰과 함께 허용되는 공유 API 토큰(선택). 비어 있으면 공유 키 비활성, 사용자별 토큰은 계속 동작 |

## 동기화 업로드 {#sync-uploads}

| 키 | 기본값 | 의미 |
|----|--------|------|
| `CCTRACE_MAX_SYNC_BODY_BYTES` | `8388608`(8 MiB) | `POST /api/sync` 요청 본문 최대 크기. 최대 `268435456`(256 MiB). 이보다 크거나, 0 이하이거나, 숫자가 아니면 기본값 유지와 경고 로그 |

클라이언트는 세션 레코드를 배치로 보내며 크기 기준으로 배치를 나누지 않는다. 한도를 넘는 배치는 HTTP 413으로 거부되고 해당 파일의 동기화는 그 지점에서 멈춘다. 배치가 한도를 넘는다면 한도를 올린다. 서버는 요청 본문 전체를 버퍼링한다. 디코딩에는 요청당 본문 크기의 약 다섯 배 메모리를 쓴다.

## 이미지와 컨테이너 {#images-and-containers}

| 키 | 기본값 | 의미 |
|----|--------|------|
| `IMAGE_TAG` | `latest` | 실행할 cctrace/cctraced 태그. 로컬 빌드 이미지와 일치 필요 |
| `DB_IMAGE_TAG` | `latest` | 실행할 cctrace/timescaledb-pgmq 태그. 로컬 빌드 이미지와 일치 필요 |
| `CONTAINER_PREFIX` | `cctrace` | 컨테이너 이름 접두사. 같은 호스트에서 두 번째 스택을 띄울 때만 변경 |

## compose 파일이 전달하지 않는 변수 {#variables-the-compose-file-does-not-pass}

아래 변수는 `cctraced`가 읽지만 배포된 `deploy/docker-compose.yml`의 `cctraced` 서비스에는 적혀 있지 않다. 서버 env 파일에 넣어도 컨테이너에는 영향이 없다. `cctraced`를 다른 방식으로 실행하거나 서비스의 `environment:`에 직접 추가할 때 적용된다.

| 변수 | 기본값 | 의미 |
|------|--------|------|
| `OTEL_RETENTION_DAYS` | 미설정 | OpenTelemetry 이벤트·메트릭 보존 일수. 미설정이면 현재 정책 유지(스키마 기본 90일) |
| `SESSION_RETENTION_DAYS` | 미설정 | 세션 레코드(대화 내용) 보존 일수. 미설정이면 현재 정책 유지, `0`은 영구 보존 |
| `CCTRACE_USERID_ACCESS_CONTROL` | `true` | 세션 데이터의 사용자별 격리. 정확히 `false`일 때만 해제 |
| `CCTRACE_ALLOW_EMPTY_DASHBOARD` | 비어 있음 | `1`이면 내장 대시보드 없이 시작(API 전용). 없으면 대시보드 없이 빌드한 바이너리는 시작 거부 |
| `DATABASE_URL` | `postgres://cctrace:cctrace@localhost:5432/cctrace?sslmode=disable` | DB 연결 문자열. Compose가 항상 지정([데이터베이스](#database) 참조) |

보존 기간은 이 변수 없이 대시보드의 **Admin > Storage**에서 지정할 수 있다. [운영](operations.md#data-retention) 참조.

모든 키를 한 표로 보려면 [서버 환경변수](../reference/server-env.md) 참조.
