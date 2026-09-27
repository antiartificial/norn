#!/usr/bin/env bash
set -euo pipefail

root=/tmp/norn-pipeline-pg
install -d -o postgres -g postgres -m 0700 "$root"
for name in control primary analytics; do
  dir="$root/$name"
  port=55432
  case "$name" in
    primary) port=55433 ;;
    analytics) port=55434 ;;
  esac
  install -d -o postgres -g postgres -m 0700 "$dir"
  gosu postgres /usr/lib/postgresql/16/bin/initdb -D "$dir/data" --auth-local=trust --auth-host=trust --no-locale >/dev/null
  gosu postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$dir/data" -o "-k $dir -p $port" -w start >/dev/null
done
cleanup() {
  for name in analytics primary control; do
    gosu postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$root/$name/data" -m immediate stop >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT
gosu postgres createdb -h "$root/control" -p 55432 -U postgres norn_pipeline

NORN_TEST_DATABASE_URL="postgres://postgres@/norn_pipeline?host=$root/control&port=55432" \
NORN_PIPELINE_EXTERNAL_PG_ROOT="$root" \
NORN_REAL_CGROUP_TEST=1 \
NORN_EFFECT_RUNNER_BINARY=/runner \
/pipeline.test -test.run "^${NORN_PIPELINE_TEST_RUN}$" -test.timeout 45s -test.v
