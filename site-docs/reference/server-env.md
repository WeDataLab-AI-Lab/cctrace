# Server environment

Every server setting covered by this guide, in one table. Details and caveats are in [Configuration](../server/configuration.md).

"Where set" means:

- **env file**: set in the server env file (`.env` in the `deploy/` directory) and used by `deploy/docker-compose.yml`.
- **env file, TLS**: read only when `deploy/docker-compose.tls.yml` is used.
- **compose (fixed)**: set by the compose file itself; not meant to be changed.
- **not passed**: read by `cctraced`, but the shipped compose file does not pass it to the container.

| Name | Where set | Default | Meaning |
|------|-----------|---------|---------|
| `JWT_SECRET` | env file | none (required) | Dashboard session signing key, at least 32 bytes |
| `LOGS_DIR` | env file | none (required) | Host directory mounted at `/data/logs` for the access log |
| `DB_PASSWORD` | env file | `cctrace` | Database superuser password, applied at first initialisation only |
| `DB_APP_CREDENTIALS` | env file | empty | `<user>:<password>` of a non-superuser role; leave empty |
| `DB_PORT` | env file | `5432` | Host port for TimescaleDB, bound to 127.0.0.1 |
| `HTTP_BIND` | env file | 127.0.0.1 | Host address for the dashboard and REST API |
| `GRPC_BIND` | env file | 0.0.0.0 | Host address for OTLP gRPC |
| `OTEL_HTTP_BIND` | env file | 0.0.0.0 | Host address for OTLP HTTP |
| `HTTP_PORT` | env file | `8080` | Host port for the dashboard and REST API |
| `GRPC_PORT` | env file | `4317` | Host port for OTLP gRPC |
| `OTEL_HTTP_PORT` | env file | `4318` | Host port for OTLP HTTP |
| `IMAGE_TAG` | env file | `latest` | Local tag of cctrace/cctraced |
| `DB_IMAGE_TAG` | env file | `latest` | Local tag of cctrace/timescaledb-pgmq |
| `CONTAINER_PREFIX` | env file | `cctrace` | Container name prefix |
| `CCTRACE_SETUP_TOKEN` | env file | empty (generated and logged) | First-admin token; invalid after the first admin exists |
| `COOKIE_SECURE` | env file | `0` | `1` forces Secure auth cookies |
| `CCTRACE_ALLOWED_ORIGINS` | env file | empty (CORS off) | Comma-separated exact browser origins |
| `API_KEY` | env file | empty | Optional shared API token |
| `CCTRACE_MAX_SYNC_BODY_BYTES` | env file | `8388608` | Maximum `POST /api/sync` body in bytes, up to `268435456` |
| `CADDY_SITE_ADDRESS` | env file, TLS | none (required by the overlay) | Hostname or IP address clients use |
| `CADDY_TLS_MODE` | env file, TLS | `internal` | `internal` (Caddy's own CA) or `acme` |
| `TLS_BIND` | env file, TLS | 0.0.0.0 | Host address for the TLS ports |
| `TLS_HTTP_PORT` | env file, TLS | `8443` | Host port for HTTPS |
| `TLS_GRPC_PORT` | env file, TLS | `5317` | Host port for OTLP gRPC with TLS |
| `TLS_OTEL_HTTP_PORT` | env file, TLS | `5318` | Host port for OTLP HTTP with TLS |
| `DATABASE_URL` | compose (fixed) | built from `DB_APP_CREDENTIALS` / `DB_PASSWORD` | Database connection string |
| `HTTP_PORT` (container) | compose (fixed) | `8080` | Port `cctraced` listens on for HTTP |
| `GRPC_PORT` (container) | compose (fixed) | `4317` | Port `cctraced` listens on for OTLP gRPC |
| `HTTP_OTEL_PORT` | compose (fixed) | `4318` | Port `cctraced` listens on for OTLP HTTP |
| `WAL_DIR` | compose (fixed) | `/data/buffer-wal` | Ingest spill directory, on the `wal_data` volume |
| `OTEL_RETENTION_DAYS` | not passed | unset | Retention days for OpenTelemetry events and metrics |
| `SESSION_RETENTION_DAYS` | not passed | unset | Retention days for session records; `0` keeps forever |
| `CCTRACE_USERID_ACCESS_CONTROL` | not passed | `true` | Per-user data isolation; only `false` disables it |
| `CCTRACE_ALLOW_EMPTY_DASHBOARD` | not passed | empty | `1` starts without embedded dashboard assets |

`CCTRACE_AI_*` variables and `CCTRACE_SECRETS_KEY` belong to an area that is under active development and are not documented here.
