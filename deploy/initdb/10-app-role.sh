#!/bin/sh
# Creates the role cctraced connects as, so the application is not the database
# superuser (#26).
#
# WHEN THIS RUNS
#   Only while a database is being initialised for the first time -- the
#   entrypoint skips this directory entirely once the data volume holds a
#   cluster. An existing deployment gets the role from
#   scripts/migrate-db-app-role.sh, which also moves the ownership of objects
#   the superuser already created.
#
# WHY IT NEVER EXITS NONZERO
#   An initdb script that fails takes the whole initialisation down partway. The
#   entrypoint has already created PG_VERSION by then, so the next start skips
#   this directory and the role is never created -- and the only obvious
#   recovery is deleting the data volume. An earlier version exited 1 on an
#   empty password and produced exactly that. An unset value is reported and
#   skipped; the deployment then runs on the bootstrap role, which is the state
#   this change improves on rather than a broken one. A value that is set but
#   cannot be used is reported and skipped too, but cctraced does not fall back:
#   compose builds DATABASE_URL from DB_APP_CREDENTIALS whenever it is set, so
#   cctraced fails to authenticate until a matching role exists. The database
#   is still whole, and the role can be created on it afterwards.
set -e

if [ -z "${DB_APP_CREDENTIALS}" ]; then
	echo "[initdb] DB_APP_CREDENTIALS is unset; cctraced will connect as the bootstrap superuser."
	echo "[initdb] Set it in the server .env to create a non-superuser role — see deploy/.env.example."
	exit 0
fi

APP_USER="${DB_APP_CREDENTIALS%%:*}"
APP_PASSWORD="${DB_APP_CREDENTIALS#*:}"

if [ -z "${APP_USER}" ] || [ -z "${APP_PASSWORD}" ] || [ "${APP_USER}" = "${DB_APP_CREDENTIALS}" ]; then
	echo "[initdb] DB_APP_CREDENTIALS must be <user>:<password>; got something else. Skipping." >&2
	echo "[initdb] cctraced will fail to authenticate until this is corrected." >&2
	exit 0
fi

# Validated here rather than left to PostgreSQL. A rejected CREATE ROLE aborts
# initialisation partway, and the entrypoint skips this whole directory on the
# next start because PG_VERSION already exists -- so a typo becomes a permanent
# half-initialised database whose only obvious repair is deleting the volume.
# `pg_review:...` reached exactly that state: "role name is reserved", exit 3,
# and no role after a restart.
#
# Anything the script cannot create cleanly is reported and skipped, so the
# database still finishes initialising. cctraced fails to authenticate until a
# matching role exists -- DATABASE_URL is built from DB_APP_CREDENTIALS, not
# from the bootstrap role -- but that is repairable on a whole database, and a
# half-initialised one has no obvious repair short of deleting the volume.
case "${APP_USER}" in
pg_*)
	echo "[initdb] role name '${APP_USER}' is reserved: PostgreSQL refuses names starting with pg_. Skipping." >&2
	exit 0
	;;
esac
case "${APP_USER}" in
*[!A-Za-z0-9_]*)
	echo "[initdb] role name '${APP_USER}' has characters outside A-Za-z0-9_. Skipping." >&2
	exit 0
	;;
esac
if [ "${#APP_USER}" -gt 63 ]; then
	echo "[initdb] role name is longer than 63 bytes, which PostgreSQL truncates. Skipping." >&2
	exit 0
fi
# The bootstrap name is the commonest mistake and the worst one. CREATE ROLE
# would fail on it anyway ("already exists"), but compose then builds
# DATABASE_URL with the bootstrap superuser's name: with the bootstrap password
# that is a superuser connection nobody notices -- the state this file exists
# to remove -- and with any other password it is an authentication failure.
if [ "${APP_USER}" = "${POSTGRES_USER}" ]; then
	echo "[initdb] role name '${APP_USER}' is POSTGRES_USER, the bootstrap superuser. Skipping." >&2
	echo "[initdb] cctraced will connect as that superuser if the password matches, and fail to authenticate if not." >&2
	exit 0
fi

# The password travels as a psql variable and is quoted by :'name', so any
# password, an apostrophe included, makes valid SQL. That alone does not keep
# it out of the logs: \gexec sends the composed CREATE ROLE to the server with
# the password as a literal, and a statement that fails is written to the
# server log as STATEMENT (log_min_error_statement defaults to error) and
# printed by psql as LINE 1 when the error carries a position. Both reach
# `docker logs`, which the failure message below tells the reader to look at.
# Measured before this was added: an existing name and the reserved name
# `public` each put the password there. A statement that succeeds is logged
# too once an operator turns on log_statement or log_min_duration_statement:
# with log_statement = all, both the SELECT format(...) psql sends -- the
# variable already substituted -- and the CREATE ROLE it produces carry the
# password. Hence the settings at the top of the SQL, which switch all three
# off for this session only. They are superuser-only, and this script runs as
# the superuser, during initdb and when it is run again by hand.
# `if psql ...` rather than letting set -e stop the script: an initdb script
# that exits nonzero takes the whole initialisation down partway, and that is
# the failure mode this file is shaped to avoid.
if psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" \
	-v app_user="${APP_USER}" -v app_password="${APP_PASSWORD}" <<'SQL'
\set VERBOSITY terse
SET log_min_error_statement = panic;
SET log_statement = none;
SET log_min_duration_statement = -1;
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEROLE NOCREATEDB',
              :'app_user', :'app_password') \gexec

-- Enough to create and own the schema objects, and nothing else. On a fresh
-- database cctraced creates every table, so it is the owner and no ownership
-- transfer is needed; it installs the extensions too, which is why there is no
-- grant on the pgmq schema here.
SELECT format('GRANT ALL ON SCHEMA public TO %I', :'app_user') \gexec
SELECT format('GRANT CREATE ON DATABASE %I TO %I', current_database(), :'app_user') \gexec

-- Not optional. pg_stat_activity hides other sessions from a plain role, and
-- the boot migration's lock diagnostic reads that view: without this it reports
-- no lock holder while one holds the table (#613).
SELECT format('GRANT pg_read_all_stats TO %I', :'app_user') \gexec
SQL
then
	echo "[initdb] created ${APP_USER} (NOSUPERUSER)"
else
	echo "[initdb] could not create ${APP_USER}; see the error above. Skipping." >&2
	echo "[initdb] The database finishes initialising, but cctraced will fail to" >&2
	echo "[initdb] authenticate until a role matching DB_APP_CREDENTIALS exists." >&2
	echo "[initdb] cctraced has created nothing in this database yet, so nothing has" >&2
	echo "[initdb] to move: fix the value, recreate this container so it sees it, and" >&2
	echo "[initdb] run this script again in it with PGPASSWORD=\$POSTGRES_PASSWORD." >&2
	echo "[initdb] While no data has been collected, removing the data volume and" >&2
	echo "[initdb] starting again works too. scripts/migrate-db-app-role.sh is for a" >&2
	echo "[initdb] database cctraced has already used; it is not needed here." >&2
fi
