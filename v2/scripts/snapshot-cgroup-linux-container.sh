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

# The helper bind mount retains the host runner uid. Keep the execution
# binary under a root-owned path accepted by the supervisor's path checks.
install -d -o root -g root -m 0700 /opt/norn-test-runner
install -o root -g root -m 0500 /runner /opt/norn-test-runner/norn-effect-runner

NORN_REAL_CGROUP_TEST=1 \
NORN_EFFECT_RUNNER_BINARY=/opt/norn-test-runner/norn-effect-runner \
NORN_TEST_PG_DUMP=/usr/lib/postgresql/16/bin/pg_dump \
NORN_TEST_PG_SERVICE=integration \
NORN_TEST_PSQL=/usr/lib/postgresql/16/bin/psql \
/supervisor.test -test.run '^TestLinuxCgroup(SnapshotRunner|MigrationDescendantTimeout|MigrationHelperDeath|MigrationAPIProcessExit)$' -test.v
