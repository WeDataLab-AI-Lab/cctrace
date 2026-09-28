# 포트

서버 스택이 여는 모든 포트. `deploy/docker-compose.yml`과 선택형 `deploy/docker-compose.tls.yml` 기준.

호스트 바인드 주소와 호스트 포트는 서버 env 파일(`deploy/` 디렉터리의 `.env`)에서 지정한다. [설정](../server/configuration.md#network) 참조.

## 기본 스택 {#base-stack}

| 서비스 | 컨테이너 포트 | 호스트 바인드(기본값) | 호스트 포트(기본값) | 프로토콜 | 용도 |
|--------|---------------|----------------------|--------------------|----------|------|
| `cctraced` | 8080 | `HTTP_BIND`(127.0.0.1) | `HTTP_PORT`(8080) | HTTP | 대시보드, REST API, 클라이언트 동기화 |
| `cctraced` | 4317 | `GRPC_BIND`(0.0.0.0) | `GRPC_PORT`(4317) | OTLP over gRPC | 텔레메트리 수집 |
| `cctraced` | 4318 | `OTEL_HTTP_BIND`(0.0.0.0) | `OTEL_HTTP_PORT`(4318) | OTLP over HTTP | 텔레메트리 수집 |
| `timescaledb` | 5432 | 127.0.0.1(고정) | `DB_PORT`(5432) | PostgreSQL | 데이터베이스 |

이 포트들은 모두 TLS를 쓰지 않는다.

!!! warning "주의"
    기본값 그대로면 4317·4318의 OTLP가 모든 호스트 인터페이스에서 평문으로 열린다. 바인드 설정, 방화벽·VPN, 또는 아래 TLS 오버레이로 제한한다.

## TLS 오버레이 {#tls-overlay}

`deploy/docker-compose.tls.yml`이 추가하는 포트. Caddy가 TLS를 종료하고 compose 네트워크를 통해 `cctraced`로 전달한다.

| 서비스 | 컨테이너 포트 | 호스트 바인드(기본값) | 호스트 포트(기본값) | 전달 대상 | 용도 |
|--------|---------------|----------------------|--------------------|-----------|------|
| `caddy` | 8443 | `TLS_BIND`(0.0.0.0) | `TLS_HTTP_PORT`(8443) | `cctraced` 8080 | HTTPS 대시보드, REST API, 클라이언트 동기화 |
| `caddy` | 5317 | `TLS_BIND`(0.0.0.0) | `TLS_GRPC_PORT`(5317) | `cctraced` 4317 | TLS 적용 OTLP over gRPC |
| `caddy` | 5318 | `TLS_BIND`(0.0.0.0) | `TLS_OTEL_HTTP_PORT`(5318) | `cctraced` 4318 | TLS 적용 OTLP over HTTP |

오버레이를 쓸 때는 `HTTP_BIND`, `GRPC_BIND`, `OTEL_HTTP_BIND`를 127.0.0.1로 지정해 평문 포트가 TLS 포트 옆에서 열려 있지 않게 한다. [설치](../server/install.md#optional-https-with-caddy) 참조.
