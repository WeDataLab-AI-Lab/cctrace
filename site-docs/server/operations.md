# Operate the server

Where the logs are, how to back up and restore the database, how to upgrade, how to serve a server policy, and how long data is kept.

All commands run from the repository root. They use the same compose arguments as [Install](install.md):

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml <command>
```

If you use the HTTPS overlay, add `-f deploy/docker-compose.tls.yml` after the base file.

## Logs

| What | Where |
|------|-------|
| Server log (startup, migrations, setup token, warnings) | `docker compose ... logs cctraced` |
| HTTP access log for `/api/` and `/downloads/` requests | `docker compose ... logs cctraced`, and appended to `${LOGS_DIR}/access.log` on the host |
| Database log | `docker compose ... logs timescaledb` |

`LOGS_DIR` is a host directory, not a Docker volume, so the access log survives removing the container. If `cctraced` cannot open the file, it logs `access log file open failed` and writes the access log to its standard output only.

Log lines worth knowing:

| Line | Meaning |
|------|---------|
| `[cctraced] running database migrations` | Startup is applying schema migrations. |
| `[cctraced] database ready` | Migrations finished. |
| `[cctraced] initial administrator setup token: ...` | No users exist yet. See [First admin](../dashboard/first-admin.md). |
| `[cctraced] notice: session_records (conversation content) has no retention policy ...` | Nobody has chosen a retention period for conversation content. See [Data retention](#data-retention). |
| `[api] client policy report refused: ...` | The server refused a policy target report with HTTP 400. The text after the colon gives the reason, usually naming the field. See [Server policy](#server-policy). |

## Backup and restore

The database lives in the Docker volume `tsdb_data`. It holds all collected telemetry and conversation content.

!!! warning "Removing `tsdb_data` deletes all collected data"
    `docker compose down -v` removes the volumes. Use `down` without `-v` unless you intend to start over.

### Back up

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml \
    exec -T timescaledb sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump -U cctrace -d cctrace' | gzip > cctrace-backup.sql.gz
```

A database volume created by this release accepts no passwordless connection, even inside the container, so every `psql` or `pg_dump` passes the container's own `POSTGRES_PASSWORD`. A backup job you run from cron needs the same.

`pg_dump` may print a warning about circular foreign-key constraints on TimescaleDB's `continuous_agg` catalog table. The dump still completes.

### Restore

Restore into a new, empty database volume, before `cctraced` starts against it:

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml down -v
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d timescaledb
$ gunzip -c cctrace-backup.sql.gz | docker compose --env-file deploy/.env \
    -f deploy/docker-compose.yml exec -T timescaledb sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -U cctrace -d cctrace'
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

Wait for `timescaledb` to show `(healthy)` in `ps` before running the restore. The first command deletes the current database; run it only when the backup file is the data you want to keep.

The `wal_data` volume is the ingest spill buffer. It is not a backup target, but removing it can lose telemetry that was received and not yet written to the database.

## Upgrade

1. Take a backup.
2. Update your clone and rebuild both images with the tags the server env file uses:

    ```console
    $ git pull
    $ docker build -t cctrace/timescaledb-pgmq:latest docker/timescaledb-pgmq/
    $ docker build -f deploy/Dockerfile --build-arg UPDATE_SIGNING=optional \
        -t cctrace/cctraced:latest .
    ```

3. Recreate the containers and check them:

    ```console
    $ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
    $ docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
    ```

The build in step 2 is unsigned. For clients that update themselves, build as in [Security operations](security.md#build-a-signed-server-image), with a higher `VERSION` than the last release.

`cctraced` applies schema migrations at startup, before it opens its ports. There is no separate migration command. While a migration runs, the container is `Up` but reports `unhealthy` and does not answer; the log shows what it is waiting for.

To keep the previous image available, build the new one under a new tag and set `IMAGE_TAG` (and `DB_IMAGE_TAG` for a new database image) in the server env file. There are only forward migrations. To return to an older version with its data, restore the backup taken before the upgrade.

Some compose settings apply only when a container is created, for example `shm_size` on the database. After such a change in `deploy/docker-compose.yml`, recreate that container explicitly:

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --force-recreate timescaledb
```

## Server policy

A server can hand a signed policy to chosen clients, for example to move a profile to an HTTPS address. Only a client that links an update public key or a policy public key reads it. A client built from this repository without an update or policy public key links empty keys (see [Overview](../index.md)), so for it this section changes almost nothing: it never asks for a policy, and `cctrace config policy apply` stops with an error that begins "policy public key is not configured". A hand-run `cctrace config policy apply` from such a client still sends a report of that failure when its profile is logged in; see [Privacy](../reference/privacy.md#what-is-collected). How to sign a policy file is in [Security operations](security.md#sign-a-server-policy).

The server only passes the file along. It checks neither the signature nor the validity period; each client does.

Set `CCTRACE_SERVER_POLICY_FILE` in the server env file to the path of the file as the `cctraced` container sees it, then recreate the container. The shipped compose file passes the variable through but mounts no file into the container, so you have to make the file visible to it yourself. Mount the directory that holds the file, not the file itself: a mount of the single file keeps showing the old file after you replace it by renaming. With the variable empty, the server distributes no policy.

Clients ask at `/api/client-config-policy`, with their installation ID and profile name in the query and without a token. The server opens the file on every request, so a replaced file takes effect without a restart. The answers:

| Situation | Answer |
|-----------|--------|
| The variable is empty | 204, no body |
| The file lists the asking installation ID and profile as a target | 200, the content of the file |
| The file does not list them | 204 |
| The file cannot be opened, is larger than 256 KiB, or is not a well-formed policy | 503 `policy unavailable` |
| The request is not a GET | 405 |

A file that cannot be read or parsed is answered with 503 until it is fixed, so put a replacement in place in one step, for example by renaming a finished file over the old one inside the mounted directory.

A client that links a key also sends the server a report after each policy check. What a report carries and where it is stored is in [Privacy](../reference/privacy.md#what-is-collected). The server logs a report it refuses; see [Logs](#logs).

## Data retention

The schema sets these policies on a new database:

| Data | Retention | Compression |
|------|-----------|-------------|
| OpenTelemetry events and metrics | 90 days | after 30 days |
| Session records (conversation content) | none: kept indefinitely | none |

Change retention in the dashboard under **Admin > Storage**. The tab shows each table's retention and compression, its size, and the free space on the data volume.

`cctraced` also reads `OTEL_RETENTION_DAYS` and `SESSION_RETENTION_DAYS` at startup. A value set there wins over the dashboard setting, and the dashboard locks that axis. The shipped compose file does not pass these two variables to the container; see [Configuration](configuration.md#variables-the-compose-file-does-not-pass).

Retention applies to data already stored. Shortening the period deletes older records the next time the policy runs.
