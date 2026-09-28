# Operate the server

Where the logs are, how to back up and restore the database, how to upgrade, and how long data is kept.

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

`cctraced` applies schema migrations at startup, before it opens its ports. There is no separate migration command. While a migration runs, the container is `Up` but reports `unhealthy` and does not answer; the log shows what it is waiting for.

To keep the previous image available, build the new one under a new tag and set `IMAGE_TAG` (and `DB_IMAGE_TAG` for a new database image) in the server env file. There are only forward migrations. To return to an older version with its data, restore the backup taken before the upgrade.

Some compose settings apply only when a container is created, for example `shm_size` on the database. After such a change in `deploy/docker-compose.yml`, recreate that container explicitly:

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --force-recreate timescaledb
```

## Data retention

The schema sets these policies on a new database:

| Data | Retention | Compression |
|------|-----------|-------------|
| OpenTelemetry events and metrics | 90 days | after 30 days |
| Session records (conversation content) | none: kept indefinitely | none |

Change retention in the dashboard under **Admin > Storage**. The tab shows each table's retention and compression, its size, and the free space on the data volume.

`cctraced` also reads `OTEL_RETENTION_DAYS` and `SESSION_RETENTION_DAYS` at startup. A value set there wins over the dashboard setting, and the dashboard locks that axis. The shipped compose file does not pass these two variables to the container; see [Configuration](configuration.md#variables-the-compose-file-does-not-pass).

Retention applies to data already stored. Shortening the period deletes older records the next time the policy runs.
