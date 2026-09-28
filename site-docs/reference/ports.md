# Ports

Every port the server stack publishes, from `deploy/docker-compose.yml` and the optional `deploy/docker-compose.tls.yml`.

Host bind addresses and host ports are set in the server env file (`.env` in the `deploy/` directory). See [Configuration](../server/configuration.md#network).

## Base stack

| Service | Container port | Host bind (default) | Host port (default) | Protocol | Purpose |
|---------|----------------|---------------------|---------------------|----------|---------|
| `cctraced` | 8080 | `HTTP_BIND` (127.0.0.1) | `HTTP_PORT` (8080) | HTTP | Dashboard, REST API, client sync |
| `cctraced` | 4317 | `GRPC_BIND` (0.0.0.0) | `GRPC_PORT` (4317) | OTLP over gRPC | Telemetry ingest |
| `cctraced` | 4318 | `OTEL_HTTP_BIND` (0.0.0.0) | `OTEL_HTTP_PORT` (4318) | OTLP over HTTP | Telemetry ingest |
| `timescaledb` | 5432 | 127.0.0.1 (fixed) | `DB_PORT` (5432) | PostgreSQL | Database |

None of these ports use TLS.

!!! warning
    With the defaults, OTLP on 4317 and 4318 is reachable on every host interface in plaintext. Restrict it with the bind settings, a firewall or VPN, or the TLS overlay below.

## TLS overlay

Added by `deploy/docker-compose.tls.yml`. Caddy terminates TLS and forwards to `cctraced` over the compose network.

| Service | Container port | Host bind (default) | Host port (default) | Forwards to | Purpose |
|---------|----------------|---------------------|---------------------|-------------|---------|
| `caddy` | 8443 | `TLS_BIND` (0.0.0.0) | `TLS_HTTP_PORT` (8443) | `cctraced` 8080 | Dashboard, REST API, client sync over HTTPS |
| `caddy` | 5317 | `TLS_BIND` (0.0.0.0) | `TLS_GRPC_PORT` (5317) | `cctraced` 4317 | OTLP over gRPC with TLS |
| `caddy` | 5318 | `TLS_BIND` (0.0.0.0) | `TLS_OTEL_HTTP_PORT` (5318) | `cctraced` 4318 | OTLP over HTTP with TLS |

When you use the overlay, set `HTTP_BIND`, `GRPC_BIND` and `OTEL_HTTP_BIND` to 127.0.0.1 so the plaintext ports are not reachable beside the TLS ones. See [Install](../server/install.md#optional-https-with-caddy).
