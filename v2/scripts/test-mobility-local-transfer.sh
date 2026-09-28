#!/usr/bin/env bash
set -euo pipefail

# Disposable source -> target data/file rehearsal. This does not exercise
# Norn's cutover journal, provider role fence, Nomad, or traffic activation.
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
fixture_root=$repo_root/v2/infra/mobility-fixture
postgres_bin=${NORN_POSTGRES_BIN:-$(dirname "$(command -v initdb)")}
for tool in initdb pg_ctl createdb dropdb pg_dump pg_restore psql; do
  [[ -x $postgres_bin/$tool ]] || { printf 'missing PostgreSQL tool: %s\n' "$tool" >&2; exit 2; }
done
for tool in go curl python3; do command -v "$tool" >/dev/null || { printf 'missing tool: %s\n' "$tool" >&2; exit 2; }; done

scratch_base=${TMPDIR:-/tmp}
scratch=$(mktemp -d "$scratch_base/norn-mobility-transfer.XXXXXX")
chmod 700 "$scratch"
server_pid=
pg_started=false
cleanup() {
  if [[ -n $server_pid ]]; then kill "$server_pid" >/dev/null 2>&1 || true; wait "$server_pid" >/dev/null 2>&1 || true; fi
  if [[ $pg_started == true ]]; then "$postgres_bin/pg_ctl" -D "$scratch/postgres" -m immediate stop >/dev/null 2>&1 || true; fi
  SCRATCH=$scratch SCRATCH_BASE=$scratch_base python3 - <<'PY'
import os, shutil
from pathlib import Path
p = Path(os.environ['SCRATCH'])
base = Path(os.environ['SCRATCH_BASE']).resolve()
if p.parent.resolve() != base or not p.name.startswith('norn-mobility-transfer.') or p.is_symlink():
    raise SystemExit('refusing to remove unexpected scratch path')
if p.exists(): shutil.rmtree(p)
PY
}
trap cleanup EXIT

mkdir -m 700 "$scratch/socket" "$scratch/source-files" "$scratch/target-files"
"$postgres_bin/initdb" -D "$scratch/postgres" --username=mobility --auth=trust --no-sync >/dev/null
pg_port=$(python3 - <<'PY'
import socket
s=socket.socket(); s.bind(('127.0.0.1', 0)); print(s.getsockname()[1]); s.close()
PY
)
"$postgres_bin/pg_ctl" -D "$scratch/postgres" -o "-k '$scratch/socket' -h '' -p $pg_port" -l "$scratch/postgres.log" -w start >/dev/null
pg_started=true
"$postgres_bin/createdb" -h "$scratch/socket" -p "$pg_port" -U mobility source
"$postgres_bin/createdb" -h "$scratch/socket" -p "$pg_port" -U mobility target
socket_query=$(SOCKET_DIR="$scratch/socket" python3 - <<'PY'
import os, urllib.parse
print(urllib.parse.quote(os.environ['SOCKET_DIR'], safe=''))
PY
)
source_url="postgresql://mobility@/source?host=$socket_query&port=$pg_port&sslmode=disable"
target_url="postgresql://mobility@/target?host=$socket_query&port=$pg_port&sslmode=disable"
source_runtime_url="postgresql://source_runtime@/source?host=$socket_query&port=$pg_port&sslmode=disable"
(cd "$fixture_root" && CGO_ENABLED=0 go build -buildvcs=false -o "$scratch/mobility-fixture" .)
DATABASE_URL=$source_url "$scratch/mobility-fixture" migrate
"$postgres_bin/psql" -X -v ON_ERROR_STOP=1 -d "$source_url" >/dev/null <<'SQL'
CREATE ROLE source_runtime LOGIN;
GRANT CONNECT ON DATABASE source TO source_runtime;
GRANT USAGE ON SCHEMA public TO source_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO source_runtime;
SQL

http_port=$(python3 - <<'PY'
import socket
s=socket.socket(); s.bind(('127.0.0.1', 0)); print(s.getsockname()[1]); s.close()
PY
)
http_url="http://127.0.0.1:$http_port"
start_server() {
  local database_url=$1 files=$2 writable=$3
  DATABASE_URL=$database_url DATA_DIR=$files WRITE_ENABLED=$writable MOBILITY_HTTP_ADDR="127.0.0.1:$http_port" \
    "$scratch/mobility-fixture" serve > "$scratch/server.log" 2>&1 &
  server_pid=$!
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    if curl -fsS --max-time 1 "$http_url/health" >/dev/null 2>&1; then return; fi
    sleep 1
  done
  printf 'fixture server did not become healthy\n' >&2
  exit 1
}
stop_server() {
  kill "$server_pid" >/dev/null 2>&1 || true
  wait "$server_pid" >/dev/null 2>&1 || true
  server_pid=
}
create_item() {
  local body=$1 status
  status=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' -X POST --data-binary "$body" "$http_url/items")
  [[ $status == 201 ]] || { printf 'item write status=%s\n' "$status" >&2; exit 1; }
}

start_server "$source_runtime_url" "$scratch/source-files" true
create_item baseline-item
DATABASE_URL=$source_runtime_url WRITE_ENABLED=true "$scratch/mobility-fixture" worker
DATABASE_URL=$source_runtime_url WRITE_ENABLED=true "$scratch/mobility-fixture" tick
PGOPTIONS='-c default_transaction_read_only=on' "$postgres_bin/pg_dump" -Fc --no-owner --no-privileges -d "$source_url" -f "$scratch/baseline.dump"
"$postgres_bin/pg_restore" --exit-on-error --no-owner --no-privileges -d "$target_url" "$scratch/baseline.dump"
[[ $("$postgres_bin/psql" -X -At -d "$target_url" -c 'SELECT count(*) FROM mobility_items') == 1 ]] || { echo 'baseline target row count differs' >&2; exit 1; }
cp -R "$scratch/source-files/." "$scratch/target-files/"

create_item final-item
DATABASE_URL=$source_runtime_url WRITE_ENABLED=true "$scratch/mobility-fixture" worker
stop_server
start_server "$source_runtime_url" "$scratch/source-files" false
fenced_status=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' -X POST --data-binary fenced "$http_url/items")
[[ $fenced_status == 423 ]] || { printf 'source process fence status=%s\n' "$fenced_status" >&2; exit 1; }
if DATABASE_URL=$source_runtime_url WRITE_ENABLED=false "$scratch/mobility-fixture" worker >/dev/null 2>&1; then echo 'worker write gate failed' >&2; exit 1; fi
if DATABASE_URL=$source_runtime_url WRITE_ENABLED=false "$scratch/mobility-fixture" tick >/dev/null 2>&1; then echo 'schedule write gate failed' >&2; exit 1; fi
curl -fsS --max-time 5 "$http_url/state" > "$scratch/source.json"
stop_server

# Fence the exact source runtime role, terminate any remaining pooled sessions,
# and read back both properties before the final transfer. Maintenance uses a
# separate principal so the source remains available for a read-only dump.
"$postgres_bin/psql" -X -v ON_ERROR_STOP=1 -d "$source_url" >/dev/null <<'SQL'
ALTER ROLE source_runtime NOLOGIN;
SELECT pg_terminate_backend(pid) FROM pg_stat_activity
  WHERE usename = 'source_runtime' AND pid <> pg_backend_pid();
SQL
fence_readback=$("$postgres_bin/psql" -X -At -d "$source_url" -c "SELECT rolcanlogin, (SELECT count(*) FROM pg_stat_activity WHERE usename='source_runtime') FROM pg_roles WHERE rolname='source_runtime'")
[[ $fence_readback == 'f|0' ]] || { printf 'source role fence readback=%s\n' "$fence_readback" >&2; exit 1; }
if "$postgres_bin/psql" -X -At -d "$source_runtime_url" -c 'SELECT 1' >/dev/null 2>&1; then
  echo 'fenced source runtime role could still connect' >&2; exit 1
fi

PGOPTIONS='-c default_transaction_read_only=on' "$postgres_bin/pg_dump" -Fc --no-owner --no-privileges -d "$source_url" -f "$scratch/final.dump"
"$postgres_bin/dropdb" -h "$scratch/socket" -p "$pg_port" -U mobility target
"$postgres_bin/createdb" -h "$scratch/socket" -p "$pg_port" -U mobility target
"$postgres_bin/pg_restore" --exit-on-error --no-owner --no-privileges -d "$target_url" "$scratch/final.dump"
[[ $("$postgres_bin/psql" -X -At -d "$target_url" -c 'SELECT count(*) FROM mobility_items') == 2 ]] || { echo 'final target row count differs' >&2; exit 1; }
cp -R "$scratch/source-files/." "$scratch/target-files/"
start_server "$target_url" "$scratch/target-files" false
curl -fsS --max-time 5 "$http_url/state" > "$scratch/target.json"
stop_server
python3 "$fixture_root/compare_state.py" "$scratch/source.json" "$scratch/target.json"
printf 'baseline_restored=true final_transfer_replaced_target=true source_process_gate=passed source_role_fence=passed target_writer_disabled=true\n'
