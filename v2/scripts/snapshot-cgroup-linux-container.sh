#!/usr/bin/env bash
set -euo pipefail

# The host wrapper supplies Linux test and runner binaries. PostgreSQL data
# lives in the disposable container's tmpfs; no package installation or
# persistent Docker volume is needed.
install -d -o postgres -g postgres -m 0700 /tmp/norn-snapshot-pg
gosu postgres /usr/lib/postgresql/16/bin/initdb -D /tmp/norn-snapshot-pg/data --auth-local=trust --auth-host=trust --no-locale >/dev/null
gosu postgres /usr/lib/postgresql/16/bin/pg_ctl -D /tmp/norn-snapshot-pg/data -o '-k /tmp/norn-snapshot-pg -p 55432' -w start >/dev/null
cleanup() {
  gosu postgres /usr/lib/postgresql/16/bin/pg_ctl -D /tmp/norn-snapshot-pg/data -m immediate stop >/dev/null 2>&1 || true
}
trap cleanup EXIT

NORN_REAL_CGROUP_TEST=1 \
NORN_EFFECT_RUNNER_BINARY=/runner \
NORN_TEST_PG_DUMP=/usr/lib/postgresql/16/bin/pg_dump \
NORN_TEST_PG_SERVICE=integration \
NORN_TEST_PSQL=/usr/lib/postgresql/16/bin/psql \
/supervisor.test -test.run '^TestLinuxCgroup(SnapshotRunner|MigrationDescendantTimeout|MigrationHelperDeath)$' -test.v
