# Configure the server

The server is configured only through environment variables. With Docker Compose you set them in the server env file (`.env` in the `deploy/` directory), and the compose file passes a fixed subset to the `cctraced` container.

There are two kinds of keys:

- **Compose keys** shape the containers: host ports, bind addresses, image tags, the logs directory. `cctraced` never sees them.
- **Server variables** are read by `cctraced` itself. Compose passes only the ones listed in its `environment:` block.

`deploy/.env.example` is the annotated template. Run `cctraced --help` inside the container for the server's own list:

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml exec cctraced cctraced --help
```

After changing a value, recreate the container so it takes effect:

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

`CCTRACE_AI_*` variables and `CCTRACE_SECRETS_KEY` belong to an area that is under active development and are not documented here.

## Required

| Key | Default | Meaning |
|-----|---------|---------|
| `JWT_SECRET` | none | Signing key for dashboard sessions. At least 32 bytes. Compose refuses to start without it; `cctraced` exits on a shorter value with `JWT secret must be at least 32 bytes`. |
| `LOGS_DIR` | none | Host directory mounted at `/data/logs` in the `cctraced` container. Compose refuses to start without it. See [Operations](operations.md#logs). |

## Database

| Key | Default | Meaning |
|-----|---------|---------|
| `DB_PASSWORD` | `cctrace` | Password of the `cctrace` database superuser. Applied only when the database volume is first initialised. Set it before the first start. |
| `DB_APP_CREDENTIALS` | empty | `<user>:<password>` of a non-superuser role for `cctraced`. Leave it empty (see below). |
| `DB_PORT` | `5432` | Host port for TimescaleDB. Always bound to 127.0.0.1. |

Compose builds `DATABASE_URL` for `cctraced` from these values. With `DB_APP_CREDENTIALS` empty, `cctraced` connects as `cctrace` with `DB_PASSWORD`.

!!! note "`DB_APP_CREDENTIALS` in this repository"
    The compose file mounts an init directory, `initdb` next to the compose file, whose script creates the role named in `DB_APP_CREDENTIALS` on first start. That script is not part of this repository. If you set `DB_APP_CREDENTIALS` without creating the role yourself, `cctraced` cannot authenticate.

## Network

| Key | Default | Meaning |
|-----|---------|---------|
| `HTTP_BIND` | 127.0.0.1 | Host address for the dashboard and REST API port |
| `GRPC_BIND` | 0.0.0.0 | Host address for OTLP gRPC |
| `OTEL_HTTP_BIND` | 0.0.0.0 | Host address for OTLP HTTP |
| `HTTP_PORT` | `8080` | Host port for the dashboard and REST API |
| `GRPC_PORT` | `4317` | Host port for OTLP gRPC |
| `OTEL_HTTP_PORT` | `4318` | Host port for OTLP HTTP |

These are compose keys. Inside the container, `cctraced` always listens on 8080, 4317 and 4318; the compose file sets its own `HTTP_PORT`, `GRPC_PORT` and `HTTP_OTEL_PORT` to those values. Note `OTEL_HTTP_PORT` (host, compose) versus `HTTP_OTEL_PORT` (container, server).

See [Ports](../reference/ports.md) for the complete table, including the TLS overlay.

## Authentication and browser access

| Key | Default | Meaning |
|-----|---------|---------|
| `CCTRACE_SETUP_TOKEN` | empty | Token for creating the first admin. Empty: `cctraced` generates one and prints it in its log while no users exist. Invalid after the first admin is created. |
| `COOKIE_SECURE` | `0` | `1` forces the Secure flag on auth cookies. Set it when the dashboard is served over HTTPS through a proxy. Requests over TLS or with `X-Forwarded-Proto: https` get Secure cookies either way. |
| `CCTRACE_ALLOWED_ORIGINS` | empty | Comma-separated browser origins on another host or port that may call the API, as exact `scheme://host[:port]` values without paths. No wildcard. Empty disables CORS. The bundled dashboard does not need it. |
| `API_KEY` | empty | Optional shared API token, accepted in addition to per-user tokens. Empty disables the shared key; per-user tokens keep working. |

## Sync uploads

| Key | Default | Meaning |
|-----|---------|---------|
| `CCTRACE_MAX_SYNC_BODY_BYTES` | `8388608` (8 MiB) | Maximum size of one `POST /api/sync` request body. At most `268435456` (256 MiB); a larger, non-positive or non-numeric value keeps the default and logs a warning. |

The client sends session records in batches and does not split a batch by size. A batch above the limit is rejected with HTTP 413, and sync of that file stops at that point. Raise the limit if your batches exceed it. The server buffers the whole body, and decoding uses roughly five times its size in memory, per request.

## Images and containers

| Key | Default | Meaning |
|-----|---------|---------|
| `IMAGE_TAG` | `latest` | Tag of cctrace/cctraced to run. Must match a locally built image. |
| `DB_IMAGE_TAG` | `latest` | Tag of cctrace/timescaledb-pgmq to run. Must match a locally built image. |
| `CONTAINER_PREFIX` | `cctrace` | Container name prefix. Change it only to run a second stack on the same host. |

## Variables the compose file does not pass

`cctraced` reads the following variables, but the shipped `deploy/docker-compose.yml` does not list them under the `cctraced` service. Setting them in the server env file has no effect on the container. They apply when you run `cctraced` another way, or when you add them to the service's `environment:` yourself.

| Variable | Default | Meaning |
|----------|---------|---------|
| `OTEL_RETENTION_DAYS` | unset | Retention in days for OpenTelemetry events and metrics. Unset leaves the current policy (90 days from the schema). |
| `SESSION_RETENTION_DAYS` | unset | Retention in days for session records (conversation content). Unset leaves the current policy; `0` means keep forever. |
| `CCTRACE_USERID_ACCESS_CONTROL` | `true` | Per-user isolation of session data. Only the literal value `false` turns it off. |
| `CCTRACE_ALLOW_EMPTY_DASHBOARD` | empty | `1` lets `cctraced` start without embedded dashboard assets (API only). Without it, a binary built without the dashboard refuses to start. |
| `DATABASE_URL` | `postgres://cctrace:cctrace@localhost:5432/cctrace?sslmode=disable` | Database connection string. Compose always sets it (see [Database](#database)). |

Retention can be set without these variables in the dashboard: **Admin > Storage**. See [Operations](operations.md#data-retention).

For a single table of every key, see [Server environment](../reference/server-env.md).
