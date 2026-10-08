# Install the server

Build the two server images from this repository, write the server env file, and start the stack with Docker Compose.

The stack has two containers: `cctraced` (OTLP ingest, sync API, dashboard) and TimescaleDB. Both images are local build tags that no registry publishes, and the compose file sets `pull_policy: never`, so the stack does not start until you have built both.

<div class="diagram">
--8<-- "server-deployment.en.svg"
</div>

## Requirements

- Docker with the Compose plugin
- A clone of this repository; run every command below from the repository root

## 1. Build the images

```console
$ docker build -t cctrace/timescaledb-pgmq:latest docker/timescaledb-pgmq/
$ docker build -f deploy/Dockerfile --build-arg UPDATE_SIGNING=optional \
    -t cctrace/cctraced:latest .
```

- The first image is TimescaleDB with the pgmq extension.
- The second builds the dashboard and the `cctraced` binary inside Docker. `UPDATE_SIGNING=optional` builds without an update-signing key; the default, `required`, fails without one.
- If you set `IMAGE_TAG` or `DB_IMAGE_TAG` in the server env file, build with exactly that tag (`cctrace/cctraced:<IMAGE_TAG>`, `cctrace/timescaledb-pgmq:<DB_IMAGE_TAG>`). Compose does not fall back to a registry.

## 2. Write the server env file

Compose reads its settings from `.env` in the `deploy/` directory (the server env file). Copy the example file, then edit the 3 keys below; every other key keeps a working default and its explanatory comment:

```console
$ cp deploy/.env.example deploy/.env
```

| Key | Why it is required |
|-----|--------------------|
| `JWT_SECRET` | No default value worth keeping. Compose refuses to start without it, and `cctraced` exits if it is shorter than 32 bytes. Generate one with `openssl rand -hex 32`. |
| `LOGS_DIR` | No default. Compose refuses to start without it. Host directory mounted at `/data/logs` in the container. Create it in advance. |
| `DB_PASSWORD` | Ships with the placeholder `change-me-strong-password`, which you should not keep. Generate one with `openssl rand -hex 20`. |

Edit the 3 keys in `deploy/.env` with your editor, or in place from the command line:

```console
$ jwt_secret=$(openssl rand -hex 32) && sed -i.bak "s|^JWT_SECRET=.*|JWT_SECRET=${jwt_secret}|" deploy/.env
$ db_password=$(openssl rand -hex 20) && sed -i.bak "s|^DB_PASSWORD=.*|DB_PASSWORD=${db_password}|" deploy/.env
$ LOGS_DIR=/absolute/path/on/the/host/for/logs && mkdir -p "$LOGS_DIR" && sed -i.bak "s|^LOGS_DIR=.*|LOGS_DIR=${LOGS_DIR}|" deploy/.env
$ rm deploy/.env.bak
```

!!! warning "Decide `DB_PASSWORD` before the first start"
    PostgreSQL applies the password only when the data volume is initialised. Changing `DB_PASSWORD` afterwards leaves the database role on the old password, and `cctraced` restarts in a loop on `password authentication failed`. Recovering means removing the database volume, which deletes all collected data.

Set `DB_APP_CREDENTIALS` before the first start as well. With `<user>:<password>` there, the first start creates a non-superuser role and `cctraced` connects as it; left empty, `cctraced` connects as the database superuser. The role is created only on the first start, and its name must not be `cctrace`. Generate the password with `openssl rand -hex 20`, as for `DB_PASSWORD`: compose writes it into a connection URL as it is, and `/`, `#`, `?` or `%` in it break that URL.

```console
$ app_password=$(openssl rand -hex 20) && sed -i.bak "s|^DB_APP_CREDENTIALS=.*|DB_APP_CREDENTIALS=cctrace_app:${app_password}|" deploy/.env
$ rm deploy/.env.bak
```

See [Configuration](configuration.md#database) for the other name rules, for what to do if the first start does not create the role, and for databases already in use.

## 3. Check what the stack exposes

Before the first start, decide which interfaces the ports bind to.

| Port | Default host bind | Purpose |
|------|-------------------|---------|
| 8080 | 127.0.0.1 | Dashboard and REST API, including client sync |
| 4317 | 0.0.0.0 (all interfaces) | OTLP over gRPC |
| 4318 | 0.0.0.0 (all interfaces) | OTLP over HTTP |
| 5432 | 127.0.0.1 (fixed) | TimescaleDB |

!!! warning "OTLP listens on all interfaces without TLS"
    Ports 4317 and 4318 accept telemetry from other machines, and `cctraced` does not terminate TLS. On a network you do not control, set `GRPC_BIND` and `OTEL_HTTP_BIND` to a loopback or private address in the server env file, restrict the ports with a firewall or VPN, or use the [HTTPS overlay](#optional-https-with-caddy).

The dashboard binds to loopback by default. For client machines to sync, they need to reach port 8080: set `HTTP_BIND` to a reachable address, or put a TLS-terminating proxy in front of it. The full port table is in [Ports](../reference/ports.md).

## 4. Start the stack

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

`up -d` returning cleanly means the containers were created, not that the server started. `cctraced` reports configuration errors in its own log.

## 5. Verify

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
$ curl -fsS http://127.0.0.1:8080/api/health
```

![Terminal showing docker compose ps with both containers healthy and a successful curl to /api/health](../assets/screenshots/01-server-compose-ps.png){ loading=lazy }

- Both containers should show `Up` with `(healthy)`. The `cctraced` health check probes `/api/version` and has a 30-second start period, so it shows `health: starting` at first.
- `/api/health` answers with `"status":"ok"` when the server can reach the database, and with HTTP 503 and `"status":"unhealthy"` when it cannot.
- If `cctraced` is `Restarting`, read its log:

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs cctraced
```

## 6. Create the first admin

On a database with no users, `cctraced` prints a one-time setup token in its log:

```text
[cctraced] initial administrator setup token: <token>
```

![Terminal showing the cctraced log line with the initial administrator setup token, redacted](../assets/screenshots/02-server-setup-token.png){ loading=lazy }

To choose the token yourself, set `CCTRACE_SETUP_TOKEN` in the server env file before the first start. The token stops working once the first admin exists. Continue with [First admin](../dashboard/first-admin.md).

## Optional: HTTPS with Caddy

`deploy/docker-compose.tls.yml` is an opt-in overlay that adds a Caddy container. Caddy terminates TLS for all three listeners and forwards to `cctraced` over the compose network, using `deploy/caddy/Caddyfile`. The base compose file is unchanged, and Caddy's image (`caddy:2-alpine`) is pulled from Docker Hub.

1. In the server env file, move the plaintext ports to loopback and name the server:

    ```text
    HTTP_BIND=127.0.0.1
    GRPC_BIND=127.0.0.1
    OTEL_HTTP_BIND=127.0.0.1
    CADDY_SITE_ADDRESS=cctrace.company.example
    ```

    `CADDY_SITE_ADDRESS` is required by the overlay. It can be a hostname or the server's IP address. Without the three bind settings, the plaintext ports stay reachable beside the TLS ones.

2. Start with both compose files:

    ```console
    $ docker compose --env-file deploy/.env \
        -f deploy/docker-compose.yml -f deploy/docker-compose.tls.yml up -d
    ```

| Channel | Plaintext (container) | TLS (host default) |
|---------|-----------------------|--------------------|
| Dashboard and REST API | 8080 | 8443 |
| OTLP gRPC | 4317 | 5317 |
| OTLP HTTP | 4318 | 5318 |

Clients then use `https://cctrace.company.example:8443` for the dashboard and sync, and port 5317 or 5318 for OTLP.

Codex sends OTLP/HTTP, so cctrace derives its metrics endpoint from the profile's OTEL endpoint by changing port 5317 to 5318, as it changes 4317 to 4318. With an internal CA, Codex also needs that CA in its configuration; see below and [Codex CLI](../agents/codex.md).

### Certificates

`CADDY_TLS_MODE` defaults to `internal`: Caddy issues certificates from its own CA, which needs no public DNS and works for a bare IP address. Clients must trust that CA. Export it with:

```console
$ docker compose --env-file deploy/.env \
    -f deploy/docker-compose.yml -f deploy/docker-compose.tls.yml \
    exec caddy cat /data/caddy/pki/authorities/local/root.crt > cctrace-ca.crt
```

Copy the file to each client machine and give it to cctrace once. `cctrace init` asks for it when an endpoint is `https://`, or set it directly:

```console
$ cctrace config set server.ca_cert_file ~/cctrace-ca.crt
```

Give it even if the CA is already in the OS keychain; whether the keychain alone is enough for Claude Code and Codex was not tested. Each client takes its CA from a different place, and cctrace writes the one file to each:

| Client | Where it reads the CA | Who sets it |
|--------|-----------------------|-------------|
| cctrace | system roots plus `server.ca_cert_file` | cctrace |
| Claude Code | `NODE_EXTRA_CA_CERTS` in its settings file, with the exporter on http/protobuf to 5318 ([why](../agents/claude-code.md#private-ca)) | cctrace |
| Codex | `tls = { ca-certificate = "..." }` in its `[otel]` section | cctrace |

A running sync daemon keeps the CA it started with; restart it after setting or clearing the CA (`cctrace sync --stop`, then start it as usual). Codex's section is rewritten when sync next starts. See [Set up the client](../client/setup.md).

The initial `curl` download of the client binary does not use this setting; add `--cacert cctrace-ca.crt` to it. See [Install the client](../client/install.md).

Set `CADDY_TLS_MODE` to an ACME account email address (for example `ops@example.com`) when the name resolves publicly and port 80 is reachable from the internet — the word `acme` itself makes Caddy refuse to start. Caddy then obtains a publicly trusted certificate, and cctrace and Claude Code need no CA configuration. Codex against a public certificate without `server.ca_cert_file` was not measured.

!!! warning "Keep the `caddy_data` volume"
    It holds the issued certificates and the internal CA. Removing it creates a new CA, and every client that trusted the old one stops connecting.

## Next steps

- [Configuration](configuration.md): every setting in the server env file
- [Operations](operations.md): logs, backup, upgrades, retention
