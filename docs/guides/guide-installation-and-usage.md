# Installation and Usage Guide

Covers Claude Code Trace from installation through the full administrator and end-user flows.

---

## 1. Server installation (administrator)

### Prerequisites

- Docker + Docker Compose
- `openssl` (generates the secrets in step 4 below)
- Ports: 4317 (gRPC), 4318 (HTTP/OTLP), 5432 (PostgreSQL, localhost only), 8080 (dashboard/API)

### Install

Build the two images locally, then start them with `deploy/docker-compose.yml`.

Both image tags are for local builds only and are not published to a registry.
Compose uses `pull_policy: never` for both services: without locally built
images, startup fails instead of attempting a pull. If you change `IMAGE_TAG`
in `deploy/.env`, the server `docker build -t cctrace/cctraced:<IMAGE_TAG>` tag
must match exactly; the DB build tag stays `cctrace/timescaledb-pgmq:latest`.

> **This section is only for the administrator standing the server up.** End users do not need repository access — the server serves per-platform binaries at `/downloads`, and that is all they need (§3-1). Where repository access is restricted, an administrator can build once on a machine that has it, move the images to the server, and distribute only the binaries to users over an internal channel.

```bash
# 1) Clone the repository
#    Replace <repository-url> with the repository address you use.
#    It has to be reachable from the machine running this command --
#    on a segmented corporate network, an address that opens elsewhere
#    may not open here.
git clone <repository-url> cctrace
cd cctrace

# 2) Build the DB image (TimescaleDB + pgmq extension)
docker build -t cctrace/timescaledb-pgmq:latest docker/timescaledb-pgmq/

# 3) Build the server image
#    UPDATE_SIGNING=optional: build without an update signing key
#    (the default, required, is for the release pipeline only)
docker build -f deploy/Dockerfile --build-arg UPDATE_SIGNING=optional \
  -t cctrace/cctraced:latest .

# 4) Copy the example env file, then set the three required keys.
#    Copying keeps every other key and its comment; writing the file from
#    scratch drops them. `sed -i.bak` works with both BSD (macOS) and GNU sed.
cp deploy/.env.example deploy/.env
jwt_secret=$(openssl rand -hex 32) && sed -i.bak "s|^JWT_SECRET=.*|JWT_SECRET=${jwt_secret}|" deploy/.env
db_password=$(openssl rand -hex 20) && sed -i.bak "s|^DB_PASSWORD=.*|DB_PASSWORD=${db_password}|" deploy/.env
LOGS_DIR=/absolute/path/on/the/host/for/logs && mkdir -p "$LOGS_DIR" && sed -i.bak "s|^LOGS_DIR=.*|LOGS_DIR=${LOGS_DIR}|" deploy/.env
rm deploy/.env.bak

# 5) Start
docker compose --env-file deploy/.env \
  -f deploy/docker-compose.yml up -d

# 6) Verify -- `up -d` means the containers were created, not that the server came up
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
curl -fsS http://localhost:8080/api/health && echo
```

In step 6, check that `docker compose ps` shows `127.0.0.1:8080->8080/tcp`
for the default HTTP publish address. This checks the publish configuration;
it does not prove that access from an external machine is blocked.

**Do not skip step 6.** `up -d` prints `Started` and exits 0 even when the
configuration is wrong. A short `JWT_SECRET` is rejected precisely
(`JWT secret must be at least 32 bytes, got 8`), but that rejection lives inside
the container log while a restart loop spins quietly outside it. If `STATUS` in
`ps` reads `Restarting`, or health does not answer, read
`docker compose logs cctraced`.

`LOGS_DIR` is the **host** directory cctraced's access log accumulates in, mounted at `/data/logs` inside the container. The point is for logs to outlive the container, so it is a real path rather than a volume. Docker creates it if missing, but then it is owned by root, so creating it in advance is better. cctraced rotates the file itself.

Compose refuses to start without `LOGS_DIR`, and the server process validates `JWT_SECRET` at startup and refuses a missing or short value (see §6).

> **Decide `DB_PASSWORD` before the first start.** Postgres only applies the
> password when the volume is initialised, so changing `deploy/.env` after the
> stack has run leaves the role's password at the old value. cctraced then enters
> a restart loop with `password authentication failed for user "cctrace" (SQLSTATE 28P01)`.
> If you are already there, follow the recovery procedure in the §8 **Server startup** table.

Services that come up:

| Service | Host port | Description |
|---------|-----------|-------------|
| TimescaleDB | 127.0.0.1:5432 | Time-series DB (user: `cctrace`, password: value of `DB_PASSWORD` — `cctrace` if unset, localhost only) |
| cctraced (gRPC) | 4317 | OTEL gRPC ingest |
| cctraced (HTTP) | 4318 | OTEL HTTP/protobuf ingest |
| cctraced (REST) | 8080 | REST API + web dashboard |

First access is `http://127.0.0.1:8080` on the server host, or the HTTPS reverse proxy URL; the administrator account creation flow is §2 below.

To change ports, edit `HTTP_PORT`, `GRPC_PORT`, `OTEL_HTTP_PORT`, and `DB_PORT` in `deploy/.env` (copied from `deploy/.env.example` above).

> The internal operations procedure for shipping images to a remote server and managing signing keys is out of scope here and lives in a separate deployment document.

### HTTPS

**cctraced does not terminate TLS.** Compose publishes plaintext HTTP on `127.0.0.1:8080` by default.
Production HTTPS comes from a reverse proxy in front of it (ADR-9).

#### The bundled Caddy overlay

`deploy/docker-compose.tls.yml` adds a Caddy service that terminates all three
listeners, including OTLP over gRPC. It is opt-in: the base compose is unchanged,
so an existing install keeps its ports until you ask for this one.

```bash
# In deploy/.env — the app's own ports become loopback-only, and Caddy reaches
# it over the compose network instead.
HTTP_BIND=127.0.0.1
GRPC_BIND=127.0.0.1
OTEL_HTTP_BIND=127.0.0.1
CADDY_SITE_ADDRESS=cctrace.example.com   # a hostname, or the server's IP

docker compose --env-file deploy/.env \
  -f deploy/docker-compose.yml -f deploy/docker-compose.tls.yml up -d
```

| Channel | Plaintext | TLS |
|---------|-----------|-----|
| Dashboard / REST | 8080 | **8443** |
| OTLP gRPC | 4317 | **5317** |
| OTLP HTTP | 4318 | **5318** |

The TLS ports keep their numbers plus 1000 so a client's endpoint changes scheme
and port together — a wrong scheme then fails to connect instead of quietly
reaching the plaintext listener next door.

`CADDY_TLS_MODE` defaults to `internal`: Caddy issues from its own CA, which needs
no public DNS and works for a bare IP address. Set it to an ACME account email
address (for example `ops@example.com`) when the name resolves publicly and port
80 is reachable, and Caddy obtains a real certificate instead — the word `acme`
itself makes Caddy refuse to start. Then cctrace and Claude Code need no CA
file. Codex against a public certificate without `server.ca_cert_file` was not
measured.

#### Trusting the internal CA

Export it once:

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.tls.yml \
  exec caddy cat /data/caddy/pki/authorities/local/root.crt > cctrace-ca.crt
```

Then give that file to each client machine once:

```bash
cctrace config set server.ca_cert_file ~/cctrace-ca.crt
```

`cctrace init` asks for the same file when either endpoint is `https://`, before
it logs in. The path is stored absolute, and a file that is missing or holds no
PEM certificate is refused. `config set server.ca_cert_file ""` clears it, and
so does answering `none` at the `init` prompt.

Specify it even when the CA is already in the OS keychain: whether a CA
installed only there is enough for Claude Code or Codex was not measured. Each
client takes its CA from a different place, and cctrace writes the one file to
each:

| Client | Where it reads the CA | Who sets it |
|--------|-----------------------|-------------|
| cctrace (sync, read commands, `status`, update download) | system roots plus `server.ca_cert_file` | cctrace |
| Claude Code | `NODE_EXTRA_CA_CERTS` in the profile's Claude Code settings file, with the exporter on http/protobuf | cctrace (`init`, `config set`) |
| Codex | `tls = { ca-certificate = "..." }` in its `[otel]` block | cctrace (`init`, then every `cctrace sync`) |

**Claude Code goes over http/protobuf, not `grpc`.** Measured on 2026-10-06
with Claude Code 2.1.291 on macOS, against a local TLS receiver signed by a
private CA and against the dev stack behind Caddy `tls internal`:

| Protocol | CA given through | Result |
|----------|------------------|--------|
| `grpc` | `OTEL_EXPORTER_OTLP_CERTIFICATE` (settings or process env) | TLS handshake aborted: CA not trusted |
| `grpc` | `NODE_EXTRA_CA_CERTS` (settings or process env) | same failure |
| http/protobuf | `OTEL_EXPORTER_OTLP_CERTIFICATE` (settings env) | same failure |
| http/protobuf | `NODE_EXTRA_CA_CERTS` (process env) | `/v1/metrics` and `/v1/logs` arrived |
| http/protobuf | `NODE_EXTRA_CA_CERTS` (settings env) | arrived; through Caddy on 5318 to cctraced |

Whether `grpc` trusts a CA installed in the OS keychain was not measured. So
with a CA set and an `https://` OTEL endpoint, cctrace writes Claude Code's
`OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, the endpoint with its gRPC port
moved to the OTLP/HTTP one (4317 to 4318, 14317 to 14318, 5317 to 5318) and
`NODE_EXTRA_CA_CERTS`. server.protocol stays as stored; `init` and
`config set` print one line saying what Claude Code will use instead. Without a
CA, or on plain `http://`, nothing changes.

`NODE_EXTRA_CA_CERTS` is often already set for a company proxy, so cctrace
changes or removes only a value it wrote itself (recorded in the settings
file's `cctrace` object). A value you set is never removed; if it differs from
`server.ca_cert_file`, `init` and `config set` keep it and print both paths.
Put both CAs in one PEM file and use it for both, or remove yours to hand the
variable over.

A running sync daemon keeps the CA it started with. After setting or clearing
`server.ca_cert_file`, restart it: `cctrace sync --stop`, then start it again as
usual (the next Claude Code session starts it, or `cctrace sync --daemon`).
Codex's `[otel]` block is rewritten with the new CA when sync next starts.

> **Codex needs the CA in its own config.** Measured on Codex 0.160.0 with a
> local HTTPS receiver and a test CA: with `tls.ca-certificate` set the metrics
> arrived (147,889 bytes, the same as over plaintext); without it nothing arrived
> and Codex logged no error. The earlier note that Codex does not send to
> `https://` at all (#644) came from a 0.153.4 measurement that gave the CA through
> `SSL_CERT_FILE`; 0.153.4 was not re-measured. A #644 comment found that its
> binary's `otlp-http` options have a `tls` field, but applying the table and
> sending over https was measured only on 0.160.0. When cctrace writes an
> `https://` Codex endpoint with no CA set it prints
> `[!] Codex metrics endpoint ... server.ca_cert_file is not set`.

Losing the `caddy_data` volume means a new CA, and every client that trusted the
old one stops connecting.

#### Or terminate it yourself


```nginx
server {
    listen 443 ssl;
    server_name cctrace.example.com;
    ssl_certificate     /etc/letsencrypt/live/cctrace.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/cctrace.example.com/privkey.pem;

    # The server caps OTLP/HTTP bodies at 16 MiB. A smaller proxy limit truncates
    # large batches at the proxy, where the cause never reaches the server log.
    client_max_body_size 16m;

    location / { proxy_pass http://127.0.0.1:8080; proxy_set_header Host $http_host; }
}
```

Set `HTTP_BIND=127.0.0.1` in `deploy/.env` for a proxy running on the host.
To restrict OTLP too, set `GRPC_BIND=127.0.0.1` and `OTEL_HTTP_BIND=127.0.0.1`;
their defaults remain 0.0.0.0 for collection from other machines. Protect remote
OTLP with TLS termination and firewall/VPN restrictions. A proxy in another
container needs a reachable container-network upstream, not its own loopback.

`git pull` preserves the untracked `deploy/.env`, so these values need no
reapplication after upgrades. Recreate cctraced after changing publish values:
`docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --no-deps --force-recreate cctraced`.

The client **refuses a non-local plaintext HTTP update endpoint**
(`must use HTTPS`). Loopback is the only development exception.

### Browser origin policy

CORS is disabled by default. The bundled dashboard uses the application's host
and port and needs no CORS configuration. Dashboard writes and browser login,
setup, and refresh requests require `X-Requested-With: XMLHttpRequest` plus an
allowed `Origin`, or an allowed `Referer` when `Origin` is absent. Missing both
headers returns 403 with `missing Origin/Referer headers` in the response and log.

The reverse proxy must preserve the original `Host`, including a non-default
port, and forward `Origin` and `Referer`. CSRF checks compare host:port against
`Host`, ignoring the scheme so TLS termination works without extra configuration.
`X-Forwarded-Host`, `X-Forwarded-Proto`, and `Forwarded` are not trusted.

For a dashboard on a different host or port (including local frontend development
with `NEXT_PUBLIC_API_URL`), set `CCTRACE_ALLOWED_ORIGINS` in the server environment:

```dotenv
CCTRACE_ALLOWED_ORIGINS=https://dashboard.example.com,http://localhost:3000
```

Entries use exact `scheme://host[:port]` values, without a path or trailing slash;
commas separate entries and surrounding whitespace is ignored. Only listed origins
receive credentialed CORS headers. Wildcard `*`, opaque `null`, and empty entries
grant no access. The same list allows dashboard CSRF checks for those origins.
This setting is for different hosts or ports, not TLS termination. It does not
override browser cookie restrictions. Restart or recreate cctraced after changing
it. Bearer-authenticated CLI sync, OTLP ingestion, and open API access need no
Origin, Referer, or X-Requested-With headers.

### Backup and restore

The database lives in a Docker named volume (`tsdb_data`), and **conversation content accumulates there** (see Data retention in §7).

```bash
# Backup -- compressed dump
docker compose --env-file deploy/.env -f deploy/docker-compose.yml \
  exec -T timescaledb sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump -U cctrace -d cctrace' | gzip > cctrace-$(date +%F).sql.gz

# Restore -- into an empty database
gunzip -c cctrace-2026-08-18.sql.gz | \
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml \
  exec -T timescaledb sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -U cctrace -d cctrace "$@"' psql
```

The `wal_data` volume is the ingest buffer holding records still waiting to be sent. It is not a backup target, but **deleting it loses whatever has not been sent yet.**

### Upgrades

**Breaking change: port 8080 now defaults to loopback.** Remote clients syncing
to `http://<server-address>:8080` lose access if `HTTP_BIND` is unset. Before upgrading,
configure an HTTPS proxy and update client URLs, or explicitly set
`HTTP_BIND=0.0.0.0` (or a specific host interface) in `deploy/.env` to preserve
direct access with appropriate network restrictions. Apply the publish change
with `docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --no-deps --force-recreate cctraced`;
this recreates only cctraced and leaves the DB container and volumes intact.

```bash
git pull
docker build -t cctrace/timescaledb-pgmq:latest docker/timescaledb-pgmq/
docker build -f deploy/Dockerfile --build-arg UPDATE_SIGNING=optional \
  -t cctrace/cctraced:latest .
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
```

- **cctraced runs schema migrations at startup.** There is no separate command
- On upgrade, enable Origin forwarding if the reverse proxy removes it; dashboard writes without both Origin and Referer now return 403. Preserve Host including the port. If the dashboard is served on a separate host or port, explicitly add its origin to `CCTRACE_ALLOWED_ORIGINS` before upgrading; otherwise its write requests will be rejected.
- Take the backup above first — migrations have no rollback command
- When the compose file itself changed (for example `shm_size`), the container **must be recreated**. Such values apply only at creation time, so they are not picked up while `up -d` keeps the existing container: `docker compose ... up -d --force-recreate timescaledb`

### Environment variables

Set in the `.env` file:

| Variable | Default | Description |
|----------|---------|-------------|
| `CCTRACE_SETUP_TOKEN` | (generated) | First-admin token; generated once in server logs at startup if unset and no users exist; invalid after first admin creation |
| `JWT_SECRET` | (none) | Dashboard JWT signing key (at least 32 bytes, **required**) |
| `COOKIE_SECURE` | `0` | Set to `1` behind HTTPS proxies to force Secure access and refresh cookies, even without `X-Forwarded-Proto`. Leave `0` for plain HTTP development; TLS and forwarded HTTPS still enable Secure automatically. |
| `API_KEY` | (none) | Global API token (optional; a shared key as an alternative to per-user tokens) |
| `GRPC_PORT` | `4317` | gRPC ingest port |
| `HTTP_OTEL_PORT` | `4318` | HTTP/OTLP ingest port **inside the cctraced container** |
| `OTEL_HTTP_PORT` | `4318` | **Host** HTTP/OTLP port opened by Compose (note: one character apart from the name above) |
| `HTTP_PORT` | `8080` | REST API + dashboard port |
| `HTTP_BIND` | 127.0.0.1 | Compose host publish address for REST/dashboard; not a Go environment variable |
| `GRPC_BIND` | 0.0.0.0 | Compose host publish address for remote OTLP/gRPC ingest |
| `OTEL_HTTP_BIND` | 0.0.0.0 | Compose host publish address for remote OTLP/HTTP ingest |
| `CCTRACE_ALLOWED_ORIGINS` | (empty) | Comma-separated exact browser origins on other hosts or ports; empty disables CORS, no wildcard |
| `CCTRACE_MAX_SYNC_BODY_BYTES` | `8388608` | Ceiling in bytes for one `POST /api/sync` body, at most `268435456`. Raise it when the deployment's own batches exceed 8 MiB; unusable or larger values keep the default. Budget about five times the ceiling in memory per in-flight oversized request |
| `DB_PASSWORD` | `cctrace` | Database password. Do not leave it at the default |
| `DATABASE_URL` | `postgres://cctrace:cctrace@localhost:5432/cctrace` | Database connection string |
| `CCTRACE_USERID_ACCESS_CONTROL` | `true` | Filters session ownership by user_id; aggregates stay shared |
| `OTEL_RETENTION_DAYS` | (unset) | Retention in days for `otel_events` and `otel_metrics`. Unset keeps the 90-day migration default |
| `SESSION_RETENTION_DAYS` | (unset) | Retention in days for `session_records`. **Unset means indefinite** (see §7). `0` states permanent retention explicitly |

`cctraced --help` describes server runtime variables. Compose-only variables
(such as the host publish addresses) are defined in `deploy/docker-compose.yml`
and `deploy/.env.example`.

---

## 2. Administrator flow

### 2-1. First-time setup (create the admin account)

Before setup, run `docker compose logs cctraced` from the compose directory to read the generated token, or set `CCTRACE_SETUP_TOKEN` in `.env` before starting the server. Environment-supplied tokens are never logged. Generated tokens change on restart until setup completes.

Open `http://127.0.0.1:8080` in a browser on the server host, or use the HTTPS proxy URL:

1. You are redirected to `/setup` automatically
2. Enter the setup token, email, name, and a password (8 characters or more)
3. The account is created, you are logged in, and the dashboard opens

> This is available **exactly once**. The setup token is invalid after the first administrator is created. Later visits go to the login page.

The account created at `/setup` has no team and no `cctrace_user_id`, so it cannot pass `cctrace init`: the CLI signs in with `cctrace_user_id`, and init stops with `validation failed` when the team is empty. To send data from your own machine as this administrator, open **Users** → **Management**, select **Edit** in the account's row menu, and fill in **Team** and **cctrace User ID** first.

### 2-2. Creating user accounts

Dashboard → **Users** → **Management** → **Add User**:

```
Required:
  - Email: the user's email
  - Name: display name
  - Team: the team they belong to
  - Role: admin or user
  - cctrace_user_id: the short identifier used during CLI setup (e.g. alice)

Optional:
  - (the password is generated -- a temporary password is issued)
```

Example response:
```json
{
  "id": 2,
  "email": "alice@example.com",
  "name": "Alice Doe",
  "team": "engineering",
  "role": "user",
  "cctrace_user_id": "alice",
  "is_active": true,
  "has_api_token": false,
  "temp_password": "aB3cDe4Fg5Hi"
}
```

> Important: the user needs `cctrace_user_id` when they run `cctrace init`, so keep a record of it.

#### Bootstrapping over the API (without a browser)

The two procedures above describe the dashboard UI. On a headless server or from an automation script the same things can be done over the API, but **state-changing requests require the `X-Requested-With: XMLHttpRequest` header.** Without it the server returns `403 {"error":"CSRF validation failed"}` (GET, HEAD, and OPTIONS are exempt).

```bash
BASE=https://cctrace.example.com
XHR='X-Requested-With: XMLHttpRequest'

# 1) Set CCTRACE_SETUP_TOKEN to the configured token or the current server-log token.
# Create the admin account (open exactly once)
curl -fsS -X POST "$BASE/api/auth/setup" -H "X-Setup-Token: $CCTRACE_SETUP_TOKEN" -H "$XHR" -H 'Content-Type: application/json' \
  -d '{"email":"admin@ex.com","name":"Admin","password":"<password>"}'

# 2) Log in -- receive the cookie used by later requests
curl -fsS -X POST "$BASE/api/auth/login" -H "$XHR" -H 'Content-Type: application/json' \
  -c cookies.txt -d '{"email":"admin@ex.com","password":"<password>"}'

# 3) Create a user -- hand the temp_password from the response to them
curl -fsS -X POST "$BASE/api/admin/users" -H "$XHR" -H 'Content-Type: application/json' \
  -b cookies.txt -d '{"email":"alice@ex.com","name":"Alice","team":"engineering","role":"user","cctrace_user_id":"alice"}'
```

Read APIs require this cookie too. The per-user token (`cct_...`) issued by `cctrace init` is for OTEL ingest and sync only and cannot authenticate dashboard API calls.

### 2-3. Managing users

| Task | How |
|------|-----|
| Edit details | **Management** tab → row menu → **Edit** → change name, team, role, or cctrace User ID |
| Reset password | **Reset Password** → issues a new temporary password |
| Revoke API token | **Revoke Token** → deletes the user's API token |
| Deactivate | **Management** tab → row menu → **Deactivate** → login is blocked |

### 2-4. The administrator dashboard

| Page | Contents | Access |
|------|----------|--------|
| **Overview** | Total cost/token summary, headline tool usage | admin only |
| **Sessions** | Every user's sessions (private content masked) | admin only |
| **Cost** | Cost analysis by user, team, and model | admin only |
| **Tools** | Calls per tool, success/failure rate, duration | admin only |
| **Users** | Account management (create/edit/reset/revoke) | admin only |
| **Admin** | The three tabs below (Clients / Storage / Excluded Accounts) | admin only |
| **Logs** | Raw OTEL event browser | admin only |

Since v0.7.8 these are consolidated under a single **Admin** entry in the sidebar. It is **adminOnly**, so the menu itself is invisible to regular users (role=user). The former top-level Client Versions and Storage entries are gone and live as tabs inside the Admin page:

| Tab | Contents |
|-----|----------|
| **Clients** | Summary and list of accounts lagging the server version. An account that has never reported a version shows as `unreported (< v0.6.1)` and sorts to the top (clients before v0.6.1 sent no version header at all, so that is a confirmed upper bound) |
| **Storage** | Storage and WAL status (the same content as the former Storage page) |
| **Excluded Accounts** | Add or remove account exclusions. Events and cost for an excluded account are hidden from every dashboard aggregate. Excluding an address also hides the billing accounts it was seen with in an observed quota reading — Codex rows carry the billing id, not the address — and the row lists them ("Also hides …"). A billing account shared with an address that is not excluded (a team plan) is not hidden this way; exclude it by its billing id instead. Once drawn, a link holds until the address is un-excluded. Codex sessions, cost and metrics are excluded by billing account only, never by the address itself: the address a Codex row carries is the person's cctrace dashboard login, not the account that was billed, so excluding it does not hide that person's Codex usage on billing accounts that are not excluded — exclude the billing account, or an address linked to it, instead. Exclusion also stops storage: the server discards synced records and OTEL telemetry from an excluded account at ingest (counted as `skipped`), so data sent while excluded is not recovered by un-excluding. Billing accounts a user excluded from their own Settings (§7) are marked **Self-registered**; the user can undo those themselves, and removing one here overrides them |

The old `/versions` and `/storage` paths are not deleted; they redirect to `/admin`.

**Known exception — Unpriced Models (#441).** The Admin → **Unpriced Models** tab
is the one screen on this page that reads all usage, not the exclusion-filtered
view every other aggregate above uses: a missing price is a property of the
*model*, not of the account that happened to trigger it. Since #719 and #738 an
excluded account's new data is discarded at ingest, so this applies only to
usage already stored — sent before the account was excluded, or before #719
shipped: filtering that out could let a model only that usage touched go
permanently unpriced with no signal left to price it from. The screen exposes only (agent,
model) aggregate rows — no session, account, or user identity — and, like the
rest of this page, is admin-only.

---

## 3. End-user flow

### 3-1. Installing the CLI

**Download from the server (recommended, no Go required).** cctraced serves the per-platform binaries cross-compiled during the image build at `/downloads`. The user's machine needs no Go toolchain, and the administrator needs no separate distribution channel.

```bash
# <server-address> is the REST address of the cctraced started in §1 (8080 by default)
GOOS=$(uname -s | tr 'A-Z' 'a-z')
GOARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fL -o cctrace "https://cctrace.example.com/downloads/cctrace-${GOOS}-${GOARCH}"
chmod +x cctrace && sudo mv cctrace /usr/local/bin/
```

If the server uses a private CA (the Caddy `tls internal` default above), `curl`
does not trust it yet: add `--cacert cctrace-ca.crt` to the download.

Windows is `/downloads/cctrace-windows-amd64.exe`. For the combinations provided, see the five binaries in the §9 checklist.

**Building from source.**

```bash
# build for the current platform
make build-client
# -> produces dist/cctrace
sudo cp dist/cctrace /usr/local/bin/cctrace

# or go install
go install ./cmd/cctrace

# add to PATH
export PATH=$PATH:$(go env GOPATH)/bin
```

`make build` is not the client build: it also builds `cctraced`, needs the dashboard build output, and makes a macOS universal binary with `lipo`, so it fails outside macOS. `make build-linux` and `make build-windows` cross-compile into `dist/`.

When installing an administrator-provided binary, use trusted HTTPS or a separate managed channel. Do not download over external plaintext HTTP.

A `make build-client` artifact (like every client target in the Makefile) gets the update signing public key injected as its trust root **when the repository carries one**, giving it the same signature verification a release has. A public distribution does not carry that key, so this applies only where an administrator supplies it. Running `go build` or `go install` directly injects nothing, so those builds return `update public key is not configured`.

> **Self-update does not work in a distribution without the public key.** Without it, `make build` warns `UPDATE_PUBLIC_KEY is empty` (`make build-client` builds without a warning), the server image has to be built with `UPDATE_SIGNING=optional`, and that build generates no signing manifests under `/downloads`. The client never skips manifest verification, so updating is **structurally impossible**. The self-update description below applies to signed release builds. Elsewhere, upgrades happen by the administrator distributing a new binary.

Self-update:

- `cctrace sync` checks `/api/version` at startup
- On finding a new version, the per-platform signing manifest is verified first
- The embedded Ed25519 public key verifies the filename and SHA-256 signature
- The downloaded binary's SHA-256 is verified again
- Only a temporary file that passed verification replaces the running binary
- On any failure the existing binary is kept

An external update endpoint must be HTTPS. `localhost` and loopback HTTP are the only development exception.

### 3-2. Profile setup (`cctrace init`)

> **PATH install prompt:** when `cctrace` is not on PATH, `cctrace init` asks at the end whether to copy the running binary to `/usr/local/bin` (Windows: `%LOCALAPPDATA%\Programs\cctrace`), default `y`, retrying with `sudo` on macOS/Linux if the plain copy fails. Answering `n` prints manual PATH instructions instead. The copy happens after `~/.claude/settings.json` is written, so the sync hooks keep the full path of the binary `init` ran from — the copy does not change them.

> **Precondition**: an administrator must have created your account in the dashboard. Before running `cctrace init`, log in to `http://<server-address>:8080` with the administrator-issued temporary password and change it in Settings. Use the changed password for CLI authentication.

```bash
cctrace init
```

Or a named profile (multi-profile):
```bash
cctrace init --profile work
```

#### The setup flow

Interactive prompts (the CLI still labels the password prompt `Temporary password`; enter your **changed password**, not the administrator-issued temporary one):

> Since v0.7.9, release binaries distributed by an administrator have the sync and OTEL endpoint defaults linked in at build time, so **pressing enter is enough**. When a bracketed default like `[http://localhost:8080]` appears in the example below, just press enter. A build made without `deploy/local-defaults.env` (for example `go install`, or a build from public source) has empty defaults and needs the values typed in as before. A bare `host:port` without a scheme (`http://`/`https://`) gets `http://` prepended automatically.

```
Claude Code Trace Setup
========================

  Sync endpoint [http://localhost:8080]: http://localhost:8080
  OTEL endpoint [http://localhost:4317]: http://localhost:4317
  User ID (company e-mail id, e.g. 'user123'): alice
  Temporary password: <your changed password>

  [OK] Authenticated: Alice Doe <alice@example.com>

  Name: Alice Doe          (filled in from the server, not editable)
  Email: alice@example.com  (filled in from the server, not editable)
  Team: engineering          (filled in from the server; init stops with `validation failed` when the account has none)

  Enable session log sync? (y/n): y

  [OK] Profile saved to /Users/alice/.cctrace/profile.json
  [OK] OTEL environment applied to /Users/alice/.claude/settings.json
  Create a read token for analysis commands (ls, usage, insights)? [Y/n] [Y]:
  [OK] Read token "CLI read token (alice-laptop)" created

  Next steps:
  1. Restart Claude Code to activate telemetry.
  2. Session log sync starts on its own from the next Claude Code session.
     The hook is already written to settings.json; nothing is syncing yet.
     To sync now without waiting, run 'cctrace sync'
     (or 'cctrace sync --daemon' to leave one running).
     Sessions from before this install are not backfilled.

  Run 'cctrace status' to verify your configuration.

  Detected 2 additional Claude home(s):
    1. /Users/alice/.claude-work
    2. /Users/alice/.claude-personal

  Set up sync for which? (all/none/1,2,...) [all]: all
  [OK] Profile "claude-work" → /Users/alice/.claude-work/settings.json
  [OK] Profile "claude-personal" → /Users/alice/.claude-personal/settings.json
```

> **Read token question.** The token `init` stores in `auth_token` uploads telemetry, and the read-only Open API refuses it. Answering `Y` uses the password you just entered to create a separate read token for `ls`, `usage`, `insights`, and the other analysis commands, and stores it as `read_token` (§3-9b). Answering `n` skips it; `cctrace auth read` creates one later. Administrator accounts are refused here and create read tokens in Settings instead.

> The `Claude config directory (Enter for default ~/.claude):` prompt appears only when initialising a named profile with `--profile` (the default profile is always fixed to `~/.claude`).

Files created:

```
~/.cctrace/
├── profile.json           # profile settings (default profile)
├── profiles/work/         # named profile (when --profile is used)
│   ├── profile.json
│   └── sync-state.json
├── sync.log               # daemon log (rotates above 10MB)
├── sync.log.1 … sync.log.5  # rotated backups (up to 5, 50MB total cap)
├── sync-crash.log         # the daemon's raw stdout/stderr (panics etc., 1MB cap)
└── sync.pid               # daemon PID file
```

The daemon log rotates within its size cap, so old content is discarded. An identical message is written at most once a minute and the occurrences in between are summarised as `... (previous message repeated N times in between)`, which keeps a retry loop from filling the log. On daemon shutdown, occurrences not yet summarised are recorded as `... (repeated N times, not shown): <message>`.

#### Profile structure

```json
{
  "version": 1,
  "user": {
    "id": "alice",
    "name": "Alice Doe",
    "email": "alice@example.com",
    "team": "engineering"
  },
  "server": {
    "endpoint": "http://localhost:4317",
    "sync_endpoint": "http://localhost:8080",
    "protocol": "grpc",
    "auth_token": "cct_...",
    "read_token": "cct_..."
  },
  "options": {
    "sync_enabled": true,
    "metrics_export_interval": 60000,
    "logs_export_interval": 5000
  },
  "claude_config_dir": "/Users/alice/.claude"
}
```

#### When a profile already exists (re-running `cctrace init`)

You can pick just what you need instead of redoing the whole setup. **The Codex entry appears only when `~/.codex` exists.**

```
  Profile already exists: /home/alice/.cctrace/profile.json
    1) Re-apply hooks and OTEL env for this binary (keep existing settings)
    2) Full re-setup (re-enter endpoint, password)
    3) Cancel
  Choose [1/2/3]:
```

| Choice | What it does |
|--------|--------------|
| Re-apply hooks and OTEL env | Same as `cctrace env apply` — re-registers hooks and environment for the current binary path |
| Full re-setup | Starts over from endpoint and password. The existing profile is backed up and restored automatically on failure |
| Patch Codex integration only | Shown only when Codex is detected. Updates just the `[otel]` block in `~/.codex/config.toml` (§3-5b) |

Enter selects the first item on the list.

### 3-3. Applying the environment

After `cctrace init` completes, the `env` section of `~/.claude/settings.json` gets these entries automatically:

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
    "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer cct_...",
    "OTEL_METRIC_EXPORT_INTERVAL": "60000",
    "OTEL_LOGS_EXPORT_INTERVAL": "5000",
    "OTEL_RESOURCE_ATTRIBUTES": "user.id=alice,user.name=Alice%20Doe,user.profile.email=alice@example.com,user.team=engineering"
  }
}
```

> Claude Code reads environment variables from `~/.claude/settings.json` automatically, so nothing else is needed.

> **The settings.json write pipeline:** `cctrace init` does not overwrite the existing file immediately. It captures a snapshot, validates what it is about to apply, and only then writes — so existing settings are not lost.

### 3-4. Restart Claude Code

```bash
claude
```

With the OTEL environment in place, Claude Code sends telemetry to the server automatically (roughly every 60 seconds).

**There are two reasons a restart is required.** Environment variables are read when Claude Code starts, and the SessionStart hook registered by `cctrace init` fires **only when a session starts**. Neither applies to the session `cctrace init` ran inside — the sync daemon does not come up, and that session's content is not collected. Everything works from the next session on.

For the leading part of an already-running session not being collected, see §8 Troubleshooting.

### 3-5. First dashboard login (before CLI setup)

Browser: the HTTPS proxy URL (for example `https://cctrace.example.com`),
or `http://127.0.0.1:8080` on the server host

Complete this step **before §3-2 (`cctrace init`)**.

1. Log in with the **email** the administrator created and the **temporary password**
2. You are **redirected to the settings page** (a password change is required)
3. Change the temporary password to a new one
4. Run `cctrace init` and authenticate with the changed password

### 3-5b. Codex CLI users

cctrace collects sessions from both Claude Code and **Codex CLI**. The Codex side differs in where it is configured and in what gets collected, so read this separately.

**Order matters.** `cctrace init` detects Codex only if `~/.codex` exists when it runs.

```
install Codex CLI  ->  run codex once (creates ~/.codex)  ->  cctrace init
```

When detected, setup asks:

```
  Codex CLI detected (~/.codex found).
  Enable Codex session sync? [Y/n]
  [OK] Codex OTEL configured (~/.codex/config.toml)
```

**If you installed Codex after finishing `cctrace init`, run it again.** With a profile already present, `cctrace init` offers a choice that attaches Codex without a full re-setup.

```
  Profile already exists: /home/alice/.cctrace/profile.json
    1) Patch Codex integration only (keep existing settings)
    2) Re-apply hooks and OTEL env for this binary (keep existing settings)
    3) Full re-setup (re-enter endpoint, password)
    4) Cancel
  Choose [1/2/3/4]: 1
```

Choice 1 leaves the existing endpoint and token alone and sets only the `[otel]` block in `~/.codex/config.toml` and `options.codex_sync_enabled` in the profile.

> When Codex is not installed, choice 1 **does not appear at all** and the numbering shifts up by one (§3-2).

**What gets configured.** An OTLP HTTP exporter is written to `~/.codex/config.toml`. The HTTP port is derived from the profile's gRPC endpoint, so nothing extra is asked.

```toml
[otel]
metrics_exporter = { otlp-http = { endpoint = "http://<server-address>:4318/v1/metrics", protocol = "binary", headers = { Authorization = "Bearer cct_..." } } }
```

**Collection differs from Claude.**

| | Claude Code | Codex CLI |
|---|---|---|
| Session log path | `~/.claude/projects/*/*.jsonl` | `~/.codex/sessions/**/*.jsonl` |
| Sync trigger | SessionStart hook starts the daemon | No hook — `cctrace sync` (manually or with `--watch`) |
| OTEL | metrics + logs | **metrics only** |

The absence of a hook matters. **For Codex, turning the integration on is itself the first sync**, so rollout files that already existed at that moment are not collected. Seeing `Synced 0 records` and an empty dashboard right after enabling it is expected; collection starts with the next new session. `cctrace status` reports how much was skipped (see §8).

The related config keys are visible through `cctrace config` — `options.codex_sync_enabled` and `options.codex_dirs` (the list of Codex homes to scan).

**Extra Codex homes** (`CODEX_HOME`, `options.codex_dirs`) get their `[otel]` block kept current as well, under narrower rules than `~/.codex`:

- Only a home whose `[otel]` already sends to this profile's server (same host and port) is rewritten. A home with no `[otel]` never gets one; a home sending to another collector is left alone with a notice
- `cctrace sync` keeps the Bearer token a home already has, so named profiles on the same server do not overwrite each other. A token that differs from the profile's is reported on every sync
- `cctrace init` (including the Codex-only patch) replaces that token with the profile's. Run it after a token reissue
- A home whose Codex config is a symlink, or has a multi-line root `otel.*` key, is left untouched with a notice

### 3-5c. GJC / OMO users

Alongside Claude Code and Codex CLI, cctrace also collects **GJC** and **OMO** sessions. Both differ from Codex in where they are configured, so read this separately.

**Order matters.** `cctrace init` detects GJC and OMO only if `~/.gjc` / `~/.omo` exist when it runs.

```
install GJC  ->  run gjc once (creates ~/.gjc)  ->  cctrace init
or
install OMO  ->  run omo once (creates ~/.omo)  ->  cctrace init
```

When detected, setup asks:

```
  gjc detected (~/.gjc found).
  Enable gjc session sync? [Y/n]
  [OK] gjc sync enabled.
  [!] gjc has no session-start hook, so collection does not start by
      itself. Run 'cctrace sync --watch' (or a periodic 'cctrace sync').
  [!] gjc's token log lives inside each project directory
      (<cwd>/.gjc/_session-*/token-logs/); deleting a project directory
      loses that part of its history.

  omo detected (~/.omo found).
  Enable omo session sync? [Y/n]
  [OK] omo sync enabled.
  [!] omo has no session-start hook, so collection does not start by
      itself. Run 'cctrace sync --watch' (or a periodic 'cctrace sync').
```

**If you installed GJC or OMO after finishing `cctrace init`, there are two ways forward.** Option 1: run `cctrace init` again and choose full re-setup, which detects GJC and OMO. Option 2: enable them directly with `cctrace config set options.gjc_sync_enabled true` or `cctrace config set options.omo_sync_enabled true`.

**What gets configured.** Unlike Codex, GJC and OMO leave no OTEL block in a config file. All that is written is the sync enable flag in the profile.

```json
{
  "options": {
    "gjc_sync_enabled": true,
    "gjc_dirs": ["/Users/alice/.gjc"],
    "omo_sync_enabled": true
  }
}
```

**Collection differs from both Claude and Codex.**

| | Claude Code | Codex CLI | GJC / OMO |
|---|---|---|---|
| Session log path | `~/.claude/projects/*/*.jsonl` | `~/.codex/sessions/**/*.jsonl` | gjc: `~/.gjc/agent/sessions/v2-*/*.jsonl` + subagent transcripts `~/.gjc/agent/sessions/v2-*/*/*.jsonl` / omo: `~/.omo/sessions/--*--/*.jsonl` |
| Sync trigger | SessionStart hook starts the daemon | No hook — `cctrace sync` (manually or with `--watch`) | No hook — `cctrace sync` (manually or with `--watch`) |
| OTEL | metrics + logs | metrics only | none (no OTLP exporter) |

The absence of a hook matters. **For GJC and OMO, turning the integration on is itself the first sync**, so session files that already existed at that moment are not collected. Seeing `Synced 0 records` right after enabling is expected; collection starts with the next new session. `cctrace status` reports how much was skipped (see §8).

**Five silent failures worth knowing about:**

1. **There is no automatic start path.** The recommended route is to run `cctrace sync --watch` (or a periodic `cctrace sync`). With no hook, "the integration is on" and "collection is running" are different states. Enable it and walk away and nothing is collected.

2. **Sessions that existed before you enabled the integration are not collected.** `Synced 0 records` right afterwards is expected, and `cctrace status` reports how much was skipped (see §8).

3. **A session that ended without a response never reaches disk at all.** Both GJC and OMO keep a session in memory and create no file until the first response arrives. That is upstream behaviour, not a cctrace limitation.

4. **GJC's project-local token log is not collected.** GJC also writes a token log inside the project directory (`<cwd>/.gjc/_session-*/token-logs/`), but cctrace reads only the session JSONL and subagent transcripts under `~/.gjc/agent/sessions/`. The `init` warning about deleting a project directory refers to that token log, which cctrace does not upload.

5. **Only part of OMO's cost is attributed to OMO.** When OMO spawns the `claude` binary (provider `claude-sdk-oauth`), Claude Code writes its own session log, which cctrace already collects as `agent=claude`. To avoid double counting, that share is excluded on the OMO side. So an OMO session looking cheaper than it was is expected — that share sits under claude. Shares that went out through other providers such as `openai-codex` are attributed to OMO. The totals are correct and nothing is lost: the Claude-driven part is simply counted as `agent=claude`, and summing every agent gives the real total.

Settings can be read and changed with `cctrace config` — `cctrace config get options.gjc_sync_enabled`, `cctrace config set options.gjc_sync_enabled true`, `cctrace config get options.gjc_dirs` (the list of GJC homes to scan), `cctrace config get options.omo_sync_enabled`, `cctrace config get options.omo_dirs` (additional OMO homes to scan beyond `~/.omo`), and so on.

### 3-6. Session log sync

Uploads Claude Code session logs (`~/.claude/projects/*/*.jsonl`) to the server. Codex uses a different layout, `~/.codex/sessions/**/*.jsonl`.

#### Option A: hook-based (recommended — configured automatically)

After `cctrace init` completes, the `hooks` section of `~/.claude/settings.json` is configured automatically:

```json
{
  "hooks": {
    "SessionStart": "cctrace sync --daemon --claude-dir ~/.claude --auto-profile --interval 1s",
    "SessionEnd": "cctrace sync --daemon --once --claude-dir ~/.claude --auto-profile"
  }
}
```

> **Note:** the hook format actually written to `~/.claude/settings.json` follows Claude's matcher/hooks array structure. The example above is conceptual; `cctrace init` writes the correct form into the real file.

- SessionStart: starts the daemon and auto-selects the profile (1-second sync interval)
- SessionEnd: keeps the daemon and runs one final asynchronous sync (`async: true`, no `--stop`)

#### Option B: manual daemon mode

```bash
cctrace sync --daemon
# or a named profile
cctrace sync --daemon --profile work
```

Stop the daemon:
```bash
cctrace sync --stop
```

If it does not exit -- an unresponsive daemon on Windows blocks its own update,
because a running binary cannot be replaced -- force it:
```bash
cctrace kill
```

`kill` terminates the process rather than asking it to finish. It acts only while
the sync lock is held, so it cannot signal a pid that a crashed daemon left
behind and that the OS has since handed to something else.

#### The --once flag

Sync once in the background:
```bash
cctrace sync --daemon --once
```

#### Manual sync

```bash
cctrace sync                    # sync once
cctrace sync --watch            # keep syncing every 30s (default interval: 30s)
cctrace sync --watch --interval 60s  # every 60s
cctrace sync --dry-run          # check without sending anything
cctrace sync --profile work     # use a named profile
```

### 3-7. Clearing local setup (`cctrace reset`)

**`reset` removes configuration.** It deletes the profile (`~/.cctrace/profile.json`) and strips the OTEL variables and sync hooks out of `~/.claude/settings.json`. After running it, collection resumes only once you run `cctrace init` again. The sync state file recording how far collection got is **kept**. Deleting it does not re-upload anything: the next sync treats every existing session file as history that predates the install and skips it, collecting only what is written afterwards.

```bash
cctrace reset                   # remove the default profile's setup (profile + hooks + OTEL vars)
cctrace reset --all             # remove the default profile and every named profile
cctrace reset --force           # run without the confirmation prompt
cctrace reset --profile work    # remove only a named profile
```

To remove the binary as well, use `cctrace uninstall`.

- `--all`: resets the default profile and every named profile
- `--force`: runs immediately without an interactive confirmation
- `--profile`: resets only the named profile given

### 3-8. Checking status (`cctrace status`)

Prints the current profile and sync status.

```bash
cctrace status                  # status of the default profile
cctrace status --profile work   # status of a named profile
```

Output sections:

```
  USER
    Name:          Alice Doe
    Email:         alice@ex.com
    User ID:       alice
    Team:          engineering

  SERVER
    OTEL:          http://localhost:4317 (grpc)
    OTEL status:   [OK] connected
    Sync:          http://localhost:8080
    서버 도달:     [OK] 이 프로세스에서는 서버에 닿습니다
    수집 상태:     [OK] 전송 실패 기록이 없습니다
    Auth token:    cct_abcd1234...

  PATHS
    Claude home:   /home/alice/.claude
    Settings:      /home/alice/.claude/settings.json (applied)
    Profile dir:   /home/alice/.cctrace

  OPTIONS
    Sync enabled:  yes

  QUOTA
    5h:            [####----------------] 3.0%   resets Aug 13 21:00
    7d:            [#######-------------] 35.0%  resets Aug 18 09:00
```

The two Korean-labelled lines report different subjects, and the distinction is the point. `서버 도달` ("server reachable") is whether **this `cctrace status` process** reached the endpoint; `수집 상태` ("collection state") is whether **the daemon** is getting records through. They used to be one line, `Sync status: [OK] healthy`, and that line stayed true for two days while a daemon on the same machine failed every single send (#712).

`수집 상태` reports `[!] ...` once sending has been failing for 15 minutes, carrying the last successful send, the failure count and the last error verbatim. A server refusal (413/429) says so and says a restart will not help. After 30 minutes of a transport-class stall the daemon releases its lock and exits, so the next session's SessionStart hook starts a fresh process; offsets are untouched, so that process backfills everything.

`수집 상태` is sometimes followed by `[!] N files skipped at first sync (... not collected)`. For what it means and what to do, see the §8 troubleshooting table.

**Records judged by repository (only with `options.collect_repository_prefixes`).** With a repository allowlist set, a Claude Code record is sent only when the repository of the working directory it was written in is on the list. A session that wanders into another repository leaves those records behind while the rest goes out grouped by repository identity, each group under its own identity (so for a session that crosses repositories, the project's row carries the identity of the last group sent). The session's position moves forward when a whole group has been accepted; if the server fails part-way through a large group, that group is sent again from its start. When git cannot say which repository a directory holds, the affected records are **held** rather than guessed at: nothing of them is sent, the session's position stops in front of them, and they are retried every 30 seconds. `수집 상태` is then followed by `[!] N sessions holding unsent records until their repository is confirmed (<reason> xN); retried every 30s, dropped after 24h of failing`. The reasons are `git-uncertain` (git timed out, a worktree's `.git` file points nowhere, permission denied, dubious ownership), `repository-lost-grace` (a directory that was a repository reports none, as a volume not mounted yet after wake does; believed after 15 minutes), `cwd-unknown` (lines that carry no working directory, after content that was skipped and names none: the part of a file that predates the first sync, or a line too large to read — and, in a new file, also the lines in front of such a line when none before it names a working directory) and `transition-pending` (the directory holds a different repository than before and not every session file could be listed to mark what was unsent, for example because a project directory cannot be read). **A hold that has been seen failing for 24 hours is a loss**: that directory is treated as outside the allowlist, its held records are dropped, the rest of the session continues, and `[!] N sessions dropped records held over 24h, not collected (...)` stays in the status from then on. Time during which the daemon was not running does not count towards the 24 hours.

When a working directory comes to hold a different repository (a path reused for another checkout, a worktree replaced), the records of that directory still unsent at the moment sync notices are sent only if both the old and the new repository are on the list: nothing says which of the two a given line was written in. After a replacement where either side is excluded, those lines are dropped — typically the first prompt of the new session, or everything written while the daemon was stopped. Four limits remain. A directory sync has never looked at is judged by what it holds now. A replacement that is reversed between two looks goes unnoticed. A session file that is renamed is read again from its start under the new path, as it always was, and does not inherit what was recorded for the old path. And what each directory held is kept in the sync state file, only while an allowlist is set, and an older cctrace does not preserve it: after a downgrade and upgrade, or after turning the allowlist on, sync starts again from "never looked". `cctrace sync reenrich` re-reads each session file from its start, so under an allowlist it skips a file from which any record was kept back or is being held, one with a line whose working directory is unknown, excluded or cannot be confirmed now, and one still holding lines from before a replacement. That protection knows only what this version recorded: a file whose records were kept back by an earlier cctrace, and content skipped at first sync, carry no such record. This describes Claude Code session files; Codex sessions are collected separately, with their own state. Without an allowlist none of it applies and nothing is held. What does change there is that a record carries the git lookup of the pass that sends it, whole — repository identity (id, remote, name, subpath), commit and branch — instead of one cached for up to an hour, so a directory that now holds a different repository is sent under it at once. The cached identity still stands in when git cannot answer, or when a directory that was a repository reports none — as before, only while that cached entry is itself a certain answer and less than an hour old.

**Keeping a personal billing account out.** If you use a personal Claude or Codex subscription on the same machine, list it with `cctrace config set options.exclude_accounts <entries>` — comma-separated, each an account id or `provider:account_id` (`anthropic`, `openai`); an empty value clears the list. Session records of those accounts are never sent: `cctrace sync` consumes them and moves on, so they are not retried and nothing behind them is held up. Login addresses are refused, because session records carry the billing account id and no address. Independently, sync asks the server whether the accounts a pass saw are excluded there (only those accounts; the server's list is not downloaded) and skips them the same way. An answer is reused for 5 minutes, so an exclusion lifted on the server applies within that time; an older server without the route excludes nothing, and is asked again after an hour. Either way `수집 상태` is followed by `[!] records not uploaded for ...`, naming each account and whether the exclusion is `options.exclude_accounts` or `excluded on the server`. This option does not cover OTEL: Claude Code and Codex send telemetry to the server directly, so an account also has to be excluded on the server (Admin → Excluded Accounts) for its OTEL to be discarded. Like `options.collect_repository_prefixes`, it applies to records collected from now on; what is already on the server stays until an administrator excludes the account there.

`QUOTA` is the Claude account's usage, read from `~/.claude/.credentials.json`. **On a machine that only uses Codex, or that is not logged into Claude Code, `(unavailable: read oauth token: ...)` is expected** — it has no bearing on collection or sync.

### 3-9. Using the dashboard

After logging in, a user can reach:

| Page | Contents |
|------|----------|
| **Overview** | Org-wide cost/token summary, including the subscription burn chart |
| **Sessions** | Your own sessions (filtered by user_id) |
| **Cost** | Org-wide cost analysis, broken down by user, team and model |
| **Tools** | Org-wide tool usage statistics |
| **Projects** | The projects you worked in |

> **Usage and cost are shared, on purpose.** A team that pays one bill needs to
> see where it went, so aggregates are not filtered by the signed-in account.
> What stays private is the content: prompts, tool arguments and command output
> are removed from every response, and session lists are filtered by user_id.

> **Users** and **Logs** are admin-only.

#### Interactive vs Headless

The default filter on the Sessions screen is **Interactive**: only sessions a person drove are shown, and automated ones are left out of the list.

| Class | What it is |
|---|---|
| **Interactive** | A session containing a turn typed by a person |
| **Headless** | Non-interactive runs like `claude -p`, one-shots injected by an SDK, sessions started by an agent |

The test differs per agent. For Claude it is the entry point plus **whether a human-typed turn exists**; for Codex it is the originator that started the session. GJC and OMO follow the same approach.

If a session is missing, switch the filter to **All**. In particular, a session whose first turn was not collected can be classified as Headless even though a person drove it — see §8 for the cause and the fix.

Other behaviour in the session list and detail:

- The session detail header shows one line, `cctrace {version} · Claude Code {version}` (since v0.7.8). For sessions collected before v0.7.8 the `cctrace` version is not blank but shows the confirmed bound, rendered by the dashboard as `v0.7.8 미만` (Korean for "below v0.7.8"). The `Claude Code` version is often known even for older sessions; `—` when it is not
- Sessions that were opened and closed with no content are excluded from the list automatically
- New sessions arriving while you browse do not push the list around; a "N new sessions" badge appears at the top and applies when clicked
- Every Claude, Codex, GJC, and OMO session carries an agent badge
- The instruction (`agent_task`) of Codex, GJC, and OMO subagent sessions is collected and shown as session content too
- Clicking the logo at the top left of the sidebar clears the query filters

---

### 3-9b. Analysing usage from the CLI and coding agents

The dashboard shows totals. Two questions it does not answer directly — *why did my cost change?* and *am I wasting context?* — have their own commands, and a skill lets Claude Code, Codex, or Gemini CLI run them and explain the result.

#### Read token

Every analysis command reads the read-only Open API, which refuses the upload token in `auth_token`. They use `server.read_token` instead. `cctrace report` and `cctrace sessions` read the dashboard's own endpoints, which apply the same rule and need the same read token.

| Situation | What to do |
|---|---|
| New setup | Answer `Y` to the read token question in `cctrace init` (§3-2) |
| Already set up, or skipped it | `cctrace auth read` — asks for your dashboard password and stores the token |
| Named profile | `cctrace auth read --profile <name>` |
| Administrator account | Refused from the CLI. Create a token in Settings → API Access Tokens, then `cctrace config set server.read_token <token>` |

- The token appears in Settings → API Access Tokens as `CLI read token (<hostname>)`, labelled **Read API (CLI)**, and can be revoked there
- Running `cctrace auth read` again replaces the token this profile holds and no other
- Profiles created for additional Claude homes start with a copy of the default profile's read token. When one of them replaces it, every local profile still holding the old token for the same user and server is updated too, and the command lists them
- If the account later becomes an administrator, the CLI read token stops working (`admin_read_token_forbidden`)
- Without a read token, analysis commands print `this profile has no read token, and its upload token cannot read the API; run 'cctrace auth read' to create one`

#### Why did my cost change? (`cctrace insights cost`)

```bash
cctrace insights cost                 # last 7 days vs the 7 days before
cctrace insights cost --since 30d     # last 30 days vs the 30 days before
cctrace insights cost --limit 10      # list up to 10 costly sessions
```

| Field | Meaning |
|---|---|
| `delta_cost_usd` | Cost change between the two windows |
| `volume_effect_usd` / `intensity_effect_usd` | The change split into *more sessions* and *each session costing more*; the two add up to the delta |
| `dominant_effect` | `volume`, `intensity`, or `undetermined` when the earlier window has no sessions |
| `top_driver` | The model or project whose cost moved most |
| `by_model` / `by_project` | Per-model cost (aggregated per request) and per-project cost in both windows |
| `top_sessions` | Costly sessions in the recent window, for `cctrace events --session <id>` |
| `caveats` | Limits to report with the numbers, such as `boundary_sessions` or `model_totals_differ` |

#### Am I wasting context? (`cctrace insights context`)

```bash
cctrace insights context                        # last 7 days
cctrace insights context --project <hash>       # one project; hashes from `cctrace projects`
```

| Field | Meaning |
|---|---|
| `overall.hit_rate` / `by_model` | Share of context tokens served from the prompt cache |
| `rebuilds` | Requests that rewrote the cache after the session sat idle at least `min_gap_seconds` (300), with the typical gap. A sign of resuming a session after its cache expired |
| `bloated` | Share of cost spent on requests carrying 100k context tokens or more |
| `bloated_sessions` | Sessions with such requests, by size |

Both commands print indented JSON (`--json` for compact output). The 20k/100k-token and 5-minute thresholds are fixed estimates: read the results as signals, not measured waste.

With an administrator token, when the token's role cannot be confirmed, or when rows from more than one user are mixed, session lists and the project breakdown are withheld and a caveat (`admin_scope`, `role_unknown`, or `multiple_users`) explains why.

#### Letting an agent do it (`usage-insights` skill)

The skill package in `packages/usage-insights` runs the commands above (and `usage`, `ls`, `tools`, `skills`, …) and turns the output into observations, interpretations, and small experiments. It needs the `cctrace` CLI on PATH and a read token.

```bash
claude --plugin-dir ./packages/usage-insights                                          # Claude Code
cp -R ./packages/usage-insights "${CODEX_HOME:-$HOME/.codex}/skills/usage-insights"   # Codex
gemini extensions install ./packages/usage-insights                                   # Gemini CLI
```

Then ask in plain words, for example *"Why did my cost go up this week?"* or *"Am I wasting context in this project?"*. In Claude Code the skill is also `/usage-insights:usage-insights`.

The skill never reads transcripts, never lists session IDs in its report, and never asks for your password or a token — if the read token is missing it tells you to run `cctrace auth read` yourself.

### 3-10. The other commands

Besides `init`, `status`, `sync`, and `reset` there are nineteen more. `cctrace --help` lists them too.

| Command | Purpose |
|---------|---------|
| `cctrace config` | View or modify profile settings — `get <key>` / `set <key> <value>` / `list` |
| `cctrace env apply` | Re-register Claude Code hooks and the OTEL environment for the current binary path. Settings are kept |
| `cctrace profile` | Manage named profiles — `add <name>` / `list` / `remove <name>` (§4) |
| `cctrace kill` | Force-terminate the running sync daemon. Prefer `sync --stop`, which shuts it down cleanly |
| `cctrace uninstall` | Remove all cctrace configuration and the binary itself |
| `cctrace backfill-quota` | Reconstruct Codex rate-limit history from session logs already on this machine |
| `cctrace report` | Query the server for a cost and tool-usage report |
| `cctrace sessions` | Query the server for a list of session records |
| `cctrace auth read` | Create a read token for the commands below (re-enter your password); stored as `server.read_token`. Not available to administrators |
| `cctrace ls` | List recent sessions with cost |
| `cctrace events` | Show the events of one session |
| `cctrace usage` | Show token and cost usage |
| `cctrace projects` | List the projects available as a filter |
| `cctrace tools` | List tool usage |
| `cctrace plugins` | List plugin usage |
| `cctrace skills` | List skill usage |
| `cctrace rules` | List project-rule usage |
| `cctrace organization-insights` | List organization-wide usage insights |
| `cctrace insights` | Summarize why cost changed (`cost`) and how context is used (`context`) |

The ten from `ls` to `insights` read the server's read-only Open API, so they
answer only for data the account may see. They use `server.read_token`, which
`cctrace auth read` creates; the upload token `init` stores in `server.auth_token`
is refused by that API. Every one of them accepts `--profile <name>` (falling
back to `CCTRACE_PROFILE` when the flag is left off) to read a named profile's
token, so `cctrace auth read --profile work` and, say, `cctrace ls --profile work`
read the same profile. `report` and `sessions` read the dashboard's internal
endpoints with the same `server.read_token`; those endpoints refuse the upload
token too. `config`, `env`, `profile`, `kill`, `uninstall`, and
`backfill-quota` are local and work offline.

**`cctrace env apply`** is the cure for hooks pointing at an old path after the
binary was moved or replaced. Unlike re-running `init`, it does not ask for the
endpoint or password again.

```bash
cctrace env apply                    # default profile
cctrace env apply --profile <name>   # one named profile
cctrace env apply --all              # default plus every named profile
```

**`cctrace report`** — the default window is 168 hours (7 days).

```bash
cctrace report --since 24h
cctrace report --json
```

**`cctrace sessions`** — 20 records by default.

```bash
cctrace sessions --limit 50
cctrace sessions --user <profile-email>
cctrace sessions --session <session-id>
cctrace sessions --json
```

Both query the server, so a profile and a read token (`server.read_token`, from
`cctrace auth read`) must already be configured.

---

## 4. Multiple profiles

You can keep separate settings for several environments (work/personal, different servers).

### Default profile vs named profiles

| Kind | Path | Setup | Sync |
|------|------|-------|------|
| **Default** | `~/.cctrace/profile.json` | `cctrace init` | `cctrace sync` |
| **Named** | `~/.cctrace/profiles/{name}/profile.json` | `cctrace init --profile {name}` | `cctrace sync --profile {name}` |

### Automatic .claude-* discovery

After the default profile is set up, cctrace searches `$HOME` for `.claude-*` directories and offers to create named profiles:

```
Detected 2 additional Claude home(s):
  - ~/.claude-work
  - ~/.claude-personal
Set up sync for these? (y/n): y
```

Accepting creates a named profile for each directory automatically.

### Example: two profiles

```bash
# 1. Work account (default)
cctrace init
# -> ~/.cctrace/profile.json

# 2. Personal account
cctrace init --profile personal
# -> ~/.cctrace/profiles/personal/profile.json

# sync with the work profile
cctrace sync

# sync with the personal profile
cctrace sync --profile personal

# start the daemon for the personal profile
cctrace sync --daemon --profile personal
```

### Switching Claude Code environments

Each profile can have its own `claude_config_dir`:

```bash
# Profile A (default): ~/.claude (or a path you specify)
cctrace init
# -> OTEL_EXPORTER_OTLP_ENDPOINT=http://server-a:4317

# Profile B: ~/.claude-personal
cctrace init --profile personal
# specify the Claude config directory during setup
#   Claude config directory: ~/.claude-personal
# -> the environment is applied to ~/.claude-personal/settings.json
```

From then on, you choose which `~/.claude` directory to use when starting Claude Code.

---

## 5. Data flow

### OTEL ingest + session log sync

```
Claude Code (local machine)
    │
    ├── OTEL (gRPC/protobuf) ──→ cctraced (ports 4317/4318)
    │                             ├── Ring Buffer (10K, in memory)
    │                             ├── WAL Spiller (disk, fsync)
    │                             └── PGMQ ──→ Worker ──→ TimescaleDB
    │                                            (otel_events, otel_metrics tables)
    │
    └── ~/.claude/projects/*/*.jsonl (local session logs; Codex uses ~/.codex/sessions/**, GJC uses ~/.gjc/agent/sessions/v2-*/**, OMO uses ~/.omo/sessions/--*--/**)
            │
            └── cctrace sync (hook/daemon)
                    │
                    └── POST /api/sync ──→ TimescaleDB (session_records)
```

WAL recovery on restart:

- Only records successfully sent to the queue are removed from the WAL
- The first failed record and everything after it are preserved
- A crash tail without a terminating newline is preserved
- Log and metric WALs replay independently

Worker error classification:

- Messages that fail JSON deserialisation are quarantined immediately
- SQLSTATE class 22 and 23 failures bisect the batch down to the single bad row
- Transient failures such as connection errors and timeouts stay on PGMQ retry
- The INSERT and the archiving of good messages share one transaction

### Access control flow (user_id based)

```
dashboard: log in with email + temporary password
  ↓
Settings: change the temporary password
  ↓
cctrace init
  ↓
enter User ID (alice) + changed password
  ↓
POST /api/cli/auth
  ↓
server: verify cctrace_user_id -> issue API token
  ↓
store the API token in the profile (cct_...)
  ↓
on sync: send the Authorization: Bearer cct_... header
  ↓
server: resolve user_id from the token -> store session records
  ↓
dashboard: show only records where CctraceUserID == "alice"
```

---

## 6. Authentication

Three independent mechanisms:

| Mechanism | Used for | Trigger | Stored in |
|-----------|----------|---------|-----------|
| **Global API_KEY** | OTEL/sync requests (any client) | `API_KEY` environment variable (optional) | Server environment |
| **Per-user token** | OTEL/sync requests (individual user) | `cctrace init` → `/api/cli/auth` | Profile `auth_token` (`cct_` prefix) |
| **CLI read token** | Open API reads (`ls`, `usage`, `insights`, …) and dashboard reads (`report`, `sessions`) | `cctrace init` offer or `cctrace auth read` → `/api/cli/read-token` | Profile `read_token` (`cct_` prefix) |
| **JWT (cookie)** | Web dashboard login | `/api/auth/login` or `/api/auth/setup` | HttpOnly cookie (1h access + 7d refresh) |

`API_KEY` is optional. A production server always wires up the per-user token validator, so OTEL/sync authentication is active even without it.

Settings → API Access Tokens issues **read-only** tokens for `/api/open/v1/*`.
These tokens are rejected immediately on sync and OTLP ingestion, including tokens
issued before this change: HTTP 403 (`read_only_token`) or gRPC PermissionDenied.
Existing CLI-issued tokens continue to work; no database migration or CLI token
rotation is required.

If a Settings token was manually configured for collection, run `cctrace init`
and choose **Full re-setup (re-enter endpoint, password)** if a profile already
exists, then authenticate with the account credentials to obtain a CLI ingestion token.
Use that token for manually configured collectors too, replacing their bearer
credential. Restart the affected CLI/collector process so it loads the updated
configuration. Rotating the token in Settings keeps it read-only and does not
restore collection access. Keep the Settings token for read API integrations.

### Authentication precedence

**OTEL/sync endpoints:**

```
Authorization header check:
  1. matches the global API_KEY?              -> allow
  2. matches a CLI-issued per-user token (valid, active, unexpired)? -> allow
  3. valid Settings-issued read token?          -> 403 Forbidden (read_only_token)
  4. neither                                   -> 401 Unauthorized
```

**Dashboard (REST API):**

```
JWT cookie check:
  1. valid access token? -> allow + inject user info
  2. expired?            -> refresh with the refresh token
  3. neither             -> 401 Unauthorized -> redirect to /login
```

### Configuration combinations

| Scenario | API_KEY set | JWT_SECRET set | Result |
|----------|-------------|----------------|--------|
| Standard operation | no | 32+ bytes | OTEL/sync protected by per-user tokens + dashboard protected by JWT |
| With a shared key | yes | 32+ bytes | OTEL/sync protected by API_KEY or per-user tokens + dashboard protected by JWT |
| Missing JWT_SECRET | either | no | Server refuses to start |
| JWT_SECRET too short | either | under 32 bytes | Server refuses to start |
| JWT_SECRET is the published example | either | the value `deploy/.env.example` shipped | Server refuses to start |

HTTP OTLP and gRPC OTLP share one Bearer authentication policy. `/v1/logs` and `/v1/metrics` HTTP request bodies are capped at 16 MiB. Trace export does not hide that storage is unsupported: it returns HTTP 501 or gRPC `Unimplemented`.

---

## 7. Privacy

### Protecting user data

- **Users**: can mark their own sessions and projects private (dashboard → **Settings**)
- **Administrators**: private session content is masked (metadata only)
- **Isolation**: with `CCTRACE_USERID_ACCESS_CONTROL=true` (the default), *session and
  event ownership* is filtered by user_id. It does not make cost and usage
  aggregates private — those are shared across the organisation by design, so
  one bill can be accounted for. See the note under "Using the dashboard"
- **Content**: free-text attributes (prompts, tool arguments, command output,
  stdout/stderr) are stripped from API responses regardless of who asks, because
  the detail routes are not scoped to the session owner

### Excluding a personal billing account (Settings)

If you use a personal Claude or Codex subscription on the same machine as your
work account, Settings → **Billing Accounts in Your Data** lists every billing
account seen in your own session records (and, when the server scopes users by
profile rather than user id, your quota readings), and lets you exclude one
yourself — no administrator needed. Excluding asks for confirmation first.

- **Exclude** hides that account's sessions, conversation text, usage and cost
  from every dashboard view, and the server stops storing session logs synced
  from it: records sent while it is excluded are discarded at ingest and are not
  recovered later
- **Include again** brings back what was stored before the exclusion. Only an
  exclusion you registered yourself can be undone here
- An account an administrator excluded shows as **Excluded by an admin** and is
  read-only. An account that also bills someone else (a team plan) can only be
  excluded by an administrator, because excluding it would hide everyone billed
  to it. It shows as **Shared** when its quota readings name another login
  address or another profile; one that only another user's session records carry
  is refused when you press Exclude
- Only accounts seen in your own data are listed or accepted, so you cannot
  exclude someone else's account. In Admin → Excluded Accounts your entry is
  marked **Self-registered**
- Exclude and include requests are limited per user (5 at once, then one a
  minute), since each one rebuilds the dashboard's aggregates

This works on the server. Claude Code and Codex send OTEL telemetry to the
server directly, not through `cctrace`; it is hidden from the dashboard like the
rest of the account's data.

### When cctrace_user_id is unset

If a dashboard user (role=user) has no `cctrace_user_id`:
- Session queries return empty results (fail-closed filtering) — a sentinel value
  is substituted that matches no row, rather than the filter being skipped
- No *personal* data is reachable until an administrator assigns a user_id. Shared
  aggregates (cost, usage, quota) remain visible, as they are for every user

### Data retention

**The default differs per table, and the table holding conversation content is the one kept forever.**

| Table | Contents | Default retention | Default compression |
|-------|----------|-------------------|---------------------|
| `otel_events` | Telemetry events | 90 days | 30 days |
| `otel_metrics` | Telemetry metrics | 90 days | 30 days |
| `session_records` | **Conversation content** (prompts and responses) | **Indefinite (no policy)** | none |

The 90-day policy is applied by migrations to `otel_events` and `otel_metrics` only. `session_records` gets no policy, so **nothing is deleted until an operator decides.**

Why 90 days is not the default here — applying a policy is retroactive, so a single upgrade would delete existing conversation content. That a code deploy does not delete data is a design invariant of `ReconcileRetention`. Instead, the unset state is announced in the startup log:

```
[cctraced] notice: session_records (conversation content) has no retention policy
and is kept indefinitely, while otel_events/otel_metrics are dropped after 90 days.
Set SESSION_RETENTION_DAYS (0 = keep forever) or choose an interval in Admin -> Storage.
```

**Two ways to set it** — the environment wins; without it, the admin-screen setting applies.

- Environment: `SESSION_RETENTION_DAYS=90` (takes effect on restart). An axis pinned by the environment cannot be edited from the admin screen
- Admin screen: **Admin → Storage → Retention**. The value is stored in the database and survives a restart

What the values mean — `0` = keep forever (policy removed), `N > 0` = drop chunks older than N days, negative = ignored with a warning.

Deletion runs asynchronously in a TimescaleDB background job. Shortening retention **deletes existing data retroactively**, so the admin screen previews how many rows would become drop-eligible and requires a typed confirmation before applying.

---

## 8. Troubleshooting

### Server startup

| Symptom | Cause | Fix |
|---------|-------|-----|
| `up -d` succeeded but the dashboard does not open | A misconfiguration has cctraced in a restart loop. Compose only reports as far as container creation, so it looks successful | Check for `Restarting` with `docker compose ps`, then read `docker compose logs cctraced` |
| Log shows `JWT secret must be at least 32 bytes, got N` | `JWT_SECRET` is too short | Replace with a random string of 32+ bytes and restart |
| Log shows `JWT_SECRET is required` | `JWT_SECRET` is unset | Add it to `.env` and restart |
| Log shows `JWT_SECRET is a published example value` | `JWT_SECRET` is the value `deploy/.env.example` shipped, which anyone can sign dashboard tokens with | Replace it with `openssl rand -hex 32` and restart. Every dashboard user signs in again, and if `CCTRACE_SECRETS_KEY` is empty (the default), API keys registered in Admin > AI must be registered again |
| Log shows `password authentication failed for user "cctrace"` (SQLSTATE 28P01) | `DB_PASSWORD` was changed after the first start. Postgres keeps the password from the initial volume setup | See **Recovering the DB password** below |
| The DB container is `unhealthy` and the health log says `password authentication failed` | Same cause | Same fix |
| Startup refused with a `LOGS_DIR` error | A required value is unset | Set an absolute host path in `.env` |

**Recovering the DB password** — to keep the data, align the role's password with the new value. pg_hba has no `trust` rule, so psql inside the container needs a password too (#26); here the one that still works is the **old** value.

Neither password goes on a command line, where `ps` and shell history would keep it. The old one is typed at a hidden prompt and handed to the container through the environment; the new one is already there as the container's `POSTGRES_PASSWORD`, and psql reads it with `\getenv`.

```bash
C="docker compose --env-file deploy/.env -f deploy/docker-compose.yml"
read -rs -p "old DB password: " PGPASSWORD; echo; export PGPASSWORD
$C exec -T -e PGPASSWORD timescaledb psql -U cctrace -d cctrace -v ON_ERROR_STOP=1 <<'SQL'
\getenv new_password POSTGRES_PASSWORD
ALTER USER cctrace WITH PASSWORD :'new_password';
SQL
unset PGPASSWORD
$C restart cctraced
```

If the old value is lost, open the unix socket for one command and close it again. The `local` rules become `trust` only between the first and last line.

```bash
C="docker compose --env-file deploy/.env -f deploy/docker-compose.yml"
$C exec -u postgres timescaledb sh -c 'sed -i "s/^\(local.*\)scram-sha-256$/\1trust/" "$PGDATA/pg_hba.conf" && pg_ctl reload -D "$PGDATA" && sleep 1'
$C exec -T timescaledb psql -U cctrace -d cctrace -v ON_ERROR_STOP=1 <<'SQL'  # deliberately passwordless: temporary trust
\getenv new_password POSTGRES_PASSWORD
ALTER USER cctrace WITH PASSWORD :'new_password';
SQL
$C exec -u postgres timescaledb sh -c 'sed -i "s/^\(local.*\)trust$/\1scram-sha-256/" "$PGDATA/pg_hba.conf" && pg_ctl reload -D "$PGDATA" && sleep 1'
$C restart cctraced
```

Only when the data is expendable, reinitialise the volume with `docker compose down -v` — **every collected telemetry record and all conversation content is destroyed.**

The DB container's healthcheck runs `select 1` **with authentication**. A mismatched password makes it `unhealthy` and cctraced does not start at all — which removes the state that used to read as "the DB is fine, only the app is broken".

### Setup

| Symptom | Cause | Fix |
|---------|-------|-----|
| "No profile found" | Setup was never run | Run `cctrace init` |
| `cctrace status` shows `[!] N files skipped at first sync` | Session files that already existed at the first sync are not collected up to that point (deliberate, so history from before installation is not backfilled). Running `cctrace init` inside a session puts that session in this category | Expected; no action needed. **Everything is collected from the next session on.** What already passed cannot be recovered |
| The beginning of the first session is missing from the dashboard / the session is absent from the Interactive list | Same cause. When the first human-typed turn is lost, the session is classified as automated and drops out of the default filter | Switch the session list filter to All. Later sessions classify correctly |
| "Server unreachable" | Wrong server address or a closed port | Check `curl http://localhost:8080/api/health` |
| "Invalid credentials" | Wrong user_id or password | Confirm the user_id with an administrator. Enter the password you set on the dashboard, not the temporary one (§3-2) |
| "Warning: profile X also uses this Claude directory" | Several profiles share one .claude directory | Point each profile at a different Claude home |
| "Claude config directory conflict" | A named profile uses the same directory as another profile | Choose a different Claude home during setup |

### OTEL ingest

| Symptom | Cause | Fix |
|---------|-------|-----|
| "Database connection failed" | The DB is not running, or a connection error | Check `docker-compose ps` and `docker-compose logs timescaledb` |
| No telemetry collected | The OTEL environment is missing | Check `echo $OTEL_EXPORTER_OTLP_ENDPOINT` and restart Claude Code |
| "Ring buffer full" in the log | Ingest is faster than delivery | Check server load and adjust the export interval |
| "WAL recovery ... paused" in the log | Queue delivery failed during WAL replay, or an incomplete tail | Recover the DB/PGMQ, restart cctraced, and do not delete the WAL volume |

### Session sync

| Symptom | Cause | Fix |
|---------|-------|-----|
| "Sync not enabled in profile" | Sync was declined during setup | Run `cctrace init` again and choose sync_enabled: true |
| The hook does not run | An error in `~/.claude/settings.json` | Validate the JSON and check the SessionStart/SessionEnd hooks |
| "Daemon failed to start" | Port conflict or a permission error | Run `cctrace sync --stop` and check the log (`~/.cctrace/sync.log`) |
| `cctrace sync --stop` times out, or the binary cannot be replaced because it is in use | An unresponsive daemon | `cctrace kill` -- terminates the process instead of asking it to exit |
| The daemon exits leaving no trace | A runtime error such as a panic | Check `~/.cctrace/sync-crash.log` (the daemon's raw stdout/stderr) |
| The same error is summarised as `(previous message repeated N times in between)` | The same message repeating | Expected — use the count to judge whether a retry loop is running; the original text is on the line above |
| The hook uses an old command / points at an old binary path | Upgraded from an older setup, or the binary was moved | `cctrace env apply` — re-registers the hooks and OTEL environment for the current binary path, keeping settings (§3-10) |
| `update public key is not configured` | A development build with no public key injected | Reinstall the signed release binary provided by an administrator |
| `manifest signature verification failed` | A tampered manifest, or a distribution key mismatch | Stop updating and ask an administrator to audit the artifacts |
| `checksum mismatch` | A tampered binary or an incomplete download | Keep the existing binary; check the network and retry |
| `must use HTTPS` | An external plaintext HTTP endpoint | Reconfigure the profile with an HTTPS server address |

### Dashboard

| Symptom | Cause | Fix |
|---------|-------|-----|
| Login fails (temporary password) | The temporary password expired or is wrong | Have an administrator run Reset Password |
| Redirected to "must_change_password" | First login (using the temporary password) | Set a new password on the settings page |
| "Unauthorized (401)" | The JWT expired | Refresh the page (the refresh token renews it automatically) |
| "Empty dashboard" | `cctrace_user_id` is unset | Have an administrator assign a user_id on the Users page |

### API tokens

Reset Password changes only the password and requires a password change at the
next login. It does **not** revoke or rotate existing API tokens. CLI authentication
with a temporary password is rejected until the password is changed on the dashboard.

For a suspected token compromise:

1. In Settings → API tokens, **Rotate** the affected token (the old secret becomes
   invalid immediately), or **Revoke** it to delete it. Disabling a token also
   blocks authentication, but re-enabling it restores the same secret.
2. To invalidate every token for a user, an administrator can select **Revoke Token**
   in Users. This deletes both the CLI token and all named tokens for that user.
3. For CLI collection, after revocation and any required password change, run
   `cctrace init` to obtain a replacement token. If a profile already exists,
   select **Full re-setup (re-enter endpoint, password)**; a settings-only update
   keeps the existing token. For named integrations, create a
   replacement in Settings → API tokens, or copy the newly rotated secret.
4. Update each affected client with the replacement secret. Settings displays a
   newly created or rotated secret only once; store it before closing the message.

Password reset alone is not token compromise containment. Revocation and rotation
reject the old credential on subsequent authentication checks; already accepted
requests are not cancelled. Restore access only after securing the account and clients.


| Symptom | Cause | Fix |
|---------|-------|-----|
| OTEL/sync 403 Forbidden (`read_only_token`), gRPC PermissionDenied | A Settings-issued read-only token was used for collection | Run `cctrace init` to obtain a CLI ingestion token |
| `this profile has no read token, and its upload token cannot read the API` | The profile has no `read_token` | Run `cctrace auth read` (§3-9b) |
| `administrators cannot create a read token from the CLI` from `cctrace auth read`, or `admin_read_token_forbidden` from a read command | The account is an administrator, or became one after the CLI read token was issued | Create a token in Settings → API Access Tokens and set it with `cctrace config set server.read_token <token>` |
| OTEL/sync 401 Unauthorized | The token was revoked, disabled, or expired | Check token status in Settings → API tokens, rotate or replace it, then re-run `cctrace init` and update the client |
| Sync fails with `cct_...` | The token was revoked | Run `cctrace init` and select **Full re-setup (re-enter endpoint, password)** if a profile already exists |
| Sync stops advancing, server logs 413 on `/api/sync` | A 200-record batch, or one record on its own, exceeds `CCTRACE_MAX_SYNC_BODY_BYTES` | The client does not split by size and does not advance past a rejected batch, so sync stays stuck there. Raise the ceiling above the largest batch this deployment produces and restart `cctraced` |
| Sync stops advancing with a transport error and no HTTP status | The batch could not be uploaded within the 30 s read deadline | The ceiling is not the constraint here and raising it changes nothing. A batch is deliverable at roughly its size / 30 s sustained -- about 2.2 MB/s at 64 MiB. Either the link has to carry that, or the oversized records have to stop being produced |

### Multiple profiles

| Symptom | Cause | Fix |
|---------|-------|-----|
| "profile not found" | The profile was never created | Run `cctrace init --profile {name}` |
| The wrong profile syncs | The `--profile` flag is missing | Always use `cctrace sync --profile {name}` |

---

## 9. Security checklist

- [ ] First administrator created using the server-log or `CCTRACE_SETUP_TOKEN` token; token invalid after successful setup

- [ ] `JWT_SECRET` of 48 bytes or more (the server's hard floor is 32 bytes; 48 is the recommendation)
- [ ] Decide whether to set `API_KEY` (only when running a shared key alongside)
- [ ] The OTLP exporter sends the `Authorization: Bearer cct_...` header
- [ ] The temporary-password distribution path is secure (a separate channel)
- [ ] Profile file permissions: `~/.cctrace/profile.json` = 0600
- [ ] HTTPS in production — cctraced does not terminate TLS; use the reverse proxy configuration in §1 **HTTPS**
- [ ] Verify the host publish addresses with `docker compose ps` (HTTP defaults to `127.0.0.1:8080`)
- [ ] Restrict remote OTLP ports 4317/4318 with firewall/VPN rules; default publish is 0.0.0.0
- [ ] The HTTP OTLP 16 MiB limit and the proxy's limit agree
- [ ] The update public key and the BuildKit signing secret are stored separately
- [ ] The five per-platform binaries are present under `/downloads`
- [ ] The five signing manifests are present — **they exist only in a build made with `UPDATE_SIGNING=required`.** An `optional` build has zero manifests and self-update does not work (§3-1)
- [ ] Periodic password rotation is enforced (organisational policy)
- [ ] Inactive users are cleaned up regularly
- [ ] API tokens are revoked (on departure or a role change)
