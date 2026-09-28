# 서버 환경변수

이 가이드가 다루는 서버 설정을 한 표로 정리. 자세한 설명과 주의 사항은 [설정](../server/configuration.md) 참조.

"지정 위치" 구분:

- **env 파일**: 서버 env 파일(`deploy/` 디렉터리의 `.env`)에 지정, `deploy/docker-compose.yml`이 사용
- **env 파일, TLS**: `deploy/docker-compose.tls.yml`을 쓸 때만 사용
- **compose(고정)**: compose 파일이 직접 지정. 변경 대상 아님
- **전달 안 됨**: `cctraced`가 읽지만 배포된 compose 파일이 컨테이너에 전달하지 않음

| 이름 | 지정 위치 | 기본값 | 의미 |
|------|-----------|--------|------|
| `JWT_SECRET` | env 파일 | 없음(필수) | 대시보드 세션 서명 키, 32바이트 이상 |
| `LOGS_DIR` | env 파일 | 없음(필수) | 접근 로그용 호스트 디렉터리, `/data/logs`에 마운트 |
| `DB_PASSWORD` | env 파일 | `cctrace` | DB 슈퍼유저 비밀번호, 최초 초기화 때만 적용 |
| `DB_APP_CREDENTIALS` | env 파일 | 비어 있음 | 비슈퍼유저 역할의 `<user>:<password>`. 비워 둘 것 |
| `DB_PORT` | env 파일 | `5432` | TimescaleDB 호스트 포트, 127.0.0.1에 바인드 |
| `HTTP_BIND` | env 파일 | 127.0.0.1 | 대시보드·REST API 호스트 주소 |
| `GRPC_BIND` | env 파일 | 0.0.0.0 | OTLP gRPC 호스트 주소 |
| `OTEL_HTTP_BIND` | env 파일 | 0.0.0.0 | OTLP HTTP 호스트 주소 |
| `HTTP_PORT` | env 파일 | `8080` | 대시보드·REST API 호스트 포트 |
| `GRPC_PORT` | env 파일 | `4317` | OTLP gRPC 호스트 포트 |
| `OTEL_HTTP_PORT` | env 파일 | `4318` | OTLP HTTP 호스트 포트 |
| `IMAGE_TAG` | env 파일 | `latest` | cctrace/cctraced 로컬 태그 |
| `DB_IMAGE_TAG` | env 파일 | `latest` | cctrace/timescaledb-pgmq 로컬 태그 |
| `CONTAINER_PREFIX` | env 파일 | `cctrace` | 컨테이너 이름 접두사 |
| `CCTRACE_SETUP_TOKEN` | env 파일 | 비어 있음(생성 후 로그 출력) | 첫 관리자 토큰, 첫 관리자 생성 후 무효 |
| `COOKIE_SECURE` | env 파일 | `0` | `1`이면 Secure 인증 쿠키 강제 |
| `CCTRACE_ALLOWED_ORIGINS` | env 파일 | 비어 있음(CORS 비활성) | 쉼표로 구분한 정확한 브라우저 origin 목록 |
| `API_KEY` | env 파일 | 비어 있음 | 선택형 공유 API 토큰 |
| `CCTRACE_MAX_SYNC_BODY_BYTES` | env 파일 | `8388608` | `POST /api/sync` 본문 최대 바이트, 최대 `268435456` |
| `CADDY_SITE_ADDRESS` | env 파일, TLS | 없음(오버레이 필수) | 클라이언트가 쓰는 호스트 이름 또는 IP 주소 |
| `CADDY_TLS_MODE` | env 파일, TLS | `internal` | `internal`(Caddy 자체 CA) 또는 `acme` |
| `TLS_BIND` | env 파일, TLS | 0.0.0.0 | TLS 포트 호스트 주소 |
| `TLS_HTTP_PORT` | env 파일, TLS | `8443` | HTTPS 호스트 포트 |
| `TLS_GRPC_PORT` | env 파일, TLS | `5317` | TLS 적용 OTLP gRPC 호스트 포트 |
| `TLS_OTEL_HTTP_PORT` | env 파일, TLS | `5318` | TLS 적용 OTLP HTTP 호스트 포트 |
| `DATABASE_URL` | compose(고정) | `DB_APP_CREDENTIALS` / `DB_PASSWORD`로 구성 | DB 연결 문자열 |
| `HTTP_PORT`(컨테이너) | compose(고정) | `8080` | `cctraced` HTTP 수신 포트 |
| `GRPC_PORT`(컨테이너) | compose(고정) | `4317` | `cctraced` OTLP gRPC 수신 포트 |
| `HTTP_OTEL_PORT` | compose(고정) | `4318` | `cctraced` OTLP HTTP 수신 포트 |
| `WAL_DIR` | compose(고정) | `/data/buffer-wal` | 수집 큐 임시 버퍼 디렉터리, `wal_data` 볼륨 |
| `OTEL_RETENTION_DAYS` | 전달 안 됨 | 미설정 | OpenTelemetry 이벤트·메트릭 보존 일수 |
| `SESSION_RETENTION_DAYS` | 전달 안 됨 | 미설정 | 세션 레코드 보존 일수, `0`은 영구 보존 |
| `CCTRACE_USERID_ACCESS_CONTROL` | 전달 안 됨 | `true` | 사용자별 데이터 격리, `false`일 때만 해제 |
| `CCTRACE_ALLOW_EMPTY_DASHBOARD` | 전달 안 됨 | 비어 있음 | `1`이면 내장 대시보드 없이 시작 |

`CCTRACE_AI_*` 변수와 `CCTRACE_SECRETS_KEY`는 변경이 진행 중이라 이 문서에서 다루지 않음.
