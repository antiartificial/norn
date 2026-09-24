#!/usr/bin/env bash
set -euo pipefail

# Runs inside postgres:16. The host wrapper mounts the checkout read-only.
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends ca-certificates curl >/dev/null
arch="$(dpkg --print-architecture)"
case "$arch" in
  amd64) goarch=amd64 ;;
  arm64) goarch=arm64 ;;
  *) echo "unsupported container architecture: $arch" >&2; exit 2 ;;
esac
curl -fsSLo /tmp/go.tgz "https://go.dev/dl/go1.26.6.linux-${goarch}.tar.gz"
tar -C /usr/local -xzf /tmp/go.tgz
export PATH=/usr/local/go/bin:$PATH
export GOCACHE=/tmp/go-cache
export GOMODCACHE=/tmp/go-mod-cache

rm -rf /tmp/norn-snapshot-pg
install -d -o postgres -g postgres -m 0700 /tmp/norn-snapshot-pg
runuser -u postgres -- initdb -D /tmp/norn-snapshot-pg/data --auth-local=trust --auth-host=trust --no-locale >/dev/null
runuser -u postgres -- pg_ctl -D /tmp/norn-snapshot-pg/data -o '-k /tmp/norn-snapshot-pg -p 55432' -w start >/dev/null
cleanup() {
  runuser -u postgres -- pg_ctl -D /tmp/norn-snapshot-pg/data -m immediate stop >/dev/null 2>&1 || true
}
trap cleanup EXIT

cd /src/v2/api
go build -o /usr/local/bin/norn-effect-runner ./cmd/norn-effect-runner
NORN_REAL_CGROUP_TEST=1 NORN_EFFECT_RUNNER_BINARY=/usr/local/bin/norn-effect-runner NORN_TEST_PG_DUMP=/usr/lib/postgresql/16/bin/pg_dump NORN_TEST_PG_SERVICE=integration go test ./effect/supervisor -run '^TestLinuxCgroupSnapshotRunner$' -count=1 -v
