#!/usr/bin/env bash
set -euo pipefail

# A synthetic ownership rehearsal only. Never point this at an existing cluster.
postgres_bin=${NORN_TEST_POSTGRES_BIN:-/opt/homebrew/bin}
for tool in initdb pg_ctl psql; do
  [[ -x $postgres_bin/$tool ]] || { printf 'missing %s\n' "$postgres_bin/$tool" >&2; exit 2; }
done

scratch=$(mktemp -d "${TMPDIR:-/tmp}/norn-mini-role-split.XXXXXXXX")
cleanup() {
  "$postgres_bin/pg_ctl" -D "$scratch/data" -m immediate stop >/dev/null 2>&1 || true
  rm -rf -- "$scratch"
}
trap cleanup EXIT
mkdir "$scratch/socket"
"$postgres_bin/initdb" -D "$scratch/data" --auth-local=trust --auth-host=reject >/dev/null
"$postgres_bin/pg_ctl" -D "$scratch/data" \
  -o "-k $scratch/socket -p 56433 -c listen_addresses=''" \
  -l "$scratch/postgres.log" start >/dev/null

admin=$(id -un)
sql() { "$postgres_bin/psql" -X -v ON_ERROR_STOP=1 -At -h "$scratch/socket" -p 56433 "$@"; }
sql -U "$admin" -d postgres -c 'CREATE ROLE norn LOGIN' >/dev/null
sql -U "$admin" -d postgres -c 'CREATE ROLE mailindexer_app LOGIN' >/dev/null
sql -U "$admin" -d postgres -c 'CREATE DATABASE norn_v2 OWNER norn' >/dev/null
sql -U "$admin" -d postgres -c 'CREATE DATABASE mailindexer OWNER norn' >/dev/null
sql -U norn -d mailindexer -c 'CREATE TABLE public.events (id serial PRIMARY KEY, note text NOT NULL)' >/dev/null
sql -U norn -d mailindexer -c "INSERT INTO public.events (note) VALUES ('before')" >/dev/null

# Explicitly scope every mutation to the disposable application database.
# REASSIGN OWNED is intentionally absent because norn also owns norn_v2.
sql -U "$admin" -d postgres -c 'ALTER DATABASE mailindexer OWNER TO mailindexer_app' >/dev/null
sql -U "$admin" -d mailindexer -c 'ALTER TABLE public.events OWNER TO mailindexer_app' >/dev/null
sql -U "$admin" -d mailindexer -c 'ALTER SEQUENCE public.events_id_seq OWNER TO mailindexer_app' >/dev/null
sql -U "$admin" -d postgres -c 'REVOKE ALL ON DATABASE mailindexer FROM PUBLIC' >/dev/null
sql -U "$admin" -d postgres -c 'GRANT CONNECT ON DATABASE mailindexer TO mailindexer_app' >/dev/null

owners=$(sql -U "$admin" -d postgres -F '|' -c \
  "SELECT datname, pg_get_userbyid(datdba) FROM pg_database WHERE datname IN ('norn_v2','mailindexer') ORDER BY datname")
[[ $owners == $'mailindexer|mailindexer_app\nnorn_v2|norn' ]] || {
  printf 'database ownership changed unexpectedly\n' >&2; exit 1;
}
objects=$(sql -U "$admin" -d mailindexer -F '|' -c \
  "SELECT relname, pg_get_userbyid(relowner) FROM pg_class WHERE oid IN ('public.events'::regclass, 'public.events_id_seq'::regclass) ORDER BY relname")
[[ $objects == $'events|mailindexer_app\nevents_id_seq|mailindexer_app' ]] || {
  printf 'application object ownership changed unexpectedly\n' >&2; exit 1;
}
sql -U mailindexer_app -d mailindexer -c "INSERT INTO public.events (note) VALUES ('after')" >/dev/null
sql -U mailindexer_app -d mailindexer -c 'CREATE TABLE public.migration_probe (id integer)' >/dev/null
[[ $(sql -U mailindexer_app -d mailindexer -c 'SELECT count(*) FROM public.events') == 2 ]] || {
  printf 'application data did not survive the role split\n' >&2; exit 1;
}
if sql -U norn -d mailindexer -c 'SELECT 1' >/dev/null 2>&1; then
  printf 'old control role can still connect to the application database\n' >&2; exit 1
fi
sql -U norn -d norn_v2 -c 'SELECT 1' >/dev/null

# Rehearse a deliberate pre-adoption rollback after the new role has written.
# Both application rows and the migration-created table must remain available.
sql -U "$admin" -d postgres -c 'ALTER DATABASE mailindexer OWNER TO norn' >/dev/null
sql -U "$admin" -d mailindexer -c 'ALTER TABLE public.events OWNER TO norn' >/dev/null
sql -U "$admin" -d mailindexer -c 'ALTER SEQUENCE public.events_id_seq OWNER TO norn' >/dev/null
sql -U "$admin" -d mailindexer -c 'ALTER TABLE public.migration_probe OWNER TO norn' >/dev/null
sql -U "$admin" -d postgres -c 'REVOKE ALL ON DATABASE mailindexer FROM mailindexer_app' >/dev/null
sql -U "$admin" -d postgres -c 'GRANT CONNECT ON DATABASE mailindexer TO norn' >/dev/null
sql -U norn -d mailindexer -c "INSERT INTO public.events (note) VALUES ('rollback')" >/dev/null
[[ $(sql -U norn -d mailindexer -c 'SELECT count(*) FROM public.events') == 3 ]] || {
  printf 'application data did not survive role rollback\n' >&2; exit 1;
}
if sql -U mailindexer_app -d mailindexer -c 'SELECT 1' >/dev/null 2>&1; then
  printf 'replacement role can still connect after rollback\n' >&2; exit 1
fi
owners=$(sql -U "$admin" -d postgres -F '|' -c \
  "SELECT datname, pg_get_userbyid(datdba) FROM pg_database WHERE datname IN ('norn_v2','mailindexer') ORDER BY datname")
[[ $owners == $'mailindexer|norn\nnorn_v2|norn' ]] || {
  printf 'database ownership did not return to the initial state\n' >&2; exit 1;
}
objects=$(sql -U "$admin" -d mailindexer -F '|' -c \
  "SELECT relname, pg_get_userbyid(relowner) FROM pg_class WHERE oid IN ('public.events'::regclass, 'public.events_id_seq'::regclass, 'public.migration_probe'::regclass) ORDER BY relname")
[[ $objects == $'events|norn\nevents_id_seq|norn\nmigration_probe|norn' ]] || {
  printf 'application object ownership did not return to the initial state\n' >&2; exit 1;
}
printf 'PASS: synthetic app role split and rollback preserve data and control ownership\n'
