#!/usr/bin/env bash
set -euo pipefail

fixture_root="$(mktemp -d /tmp/norn-function-crash.XXXXXX)"
fixture_id="${fixture_root##*.}"
postgres_container="norn-function-crash-postgres-${fixture_id}"
postgres_password="disposable-norn-postgres"
postgres_port=15437
nomad_port=14649
consul_port=18501
nomad_pid=""
consul_pid=""

cleanup() {
  status=$?
  trap - EXIT
  if [[ -n "$nomad_pid" ]]; then kill "$nomad_pid" 2>/dev/null || true; wait "$nomad_pid" 2>/dev/null || true; fi
  if [[ -n "$consul_pid" ]]; then kill "$consul_pid" 2>/dev/null || true; wait "$consul_pid" 2>/dev/null || true; fi
  python3 - "$fixture_root" <<'PY'
import json, subprocess, sys
root=sys.argv[1]
ids=subprocess.run(['docker','ps','-aq'],capture_output=True,text=True).stdout.splitlines()
if ids:
    result=subprocess.run(['docker','inspect',*ids],capture_output=True,text=True)
    if result.returncode==0:
        for item in json.loads(result.stdout):
            if any((mount.get('Source') or '').startswith(root+'/') for mount in item.get('Mounts',[])):
                subprocess.run(['docker','rm','-fv',item['Id']],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
PY
  docker rm -fv "$postgres_container" >/dev/null 2>&1 || true
  chmod -RN "$fixture_root/nomad-data" 2>/dev/null || true
  python3 - "$fixture_root" <<'PY'
import shutil, sys
shutil.rmtree(sys.argv[1], ignore_errors=True)
PY
  exit "$status"
}
trap cleanup EXIT

if [[ -n "$(docker ps -aq)" ]]; then
  echo 'run this disposable qualification in an empty Docker context' >&2
  exit 1
fi
for port in "$postgres_port" "$nomad_port" 14650 14651 "$consul_port" 18601 18302 18303; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "qualification port $port is in use" >&2
    exit 1
  fi
done
image="$(docker image inspect busybox:1.36 --format '{{index .RepoDigests 0}}')"
if [[ ! "$image" =~ ^busybox@sha256:[0-9a-f]{64}$ ]]; then
  echo 'a locally available content-addressed busybox:1.36 image is required' >&2
  exit 1
fi

mkdir -p "$fixture_root/nomad-data" "$fixture_root/consul-data"
docker run -d --name "$postgres_container" --tmpfs /var/lib/postgresql/data:rw,size=512m \
  -e "POSTGRES_PASSWORD=$postgres_password" -p "127.0.0.1:$postgres_port:5432" postgres:16 >/dev/null
for attempt in $(seq 1 60); do
  if docker exec "$postgres_container" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  if [[ "$(docker inspect "$postgres_container" --format '{{.State.Running}}')" != true ]]; then
    docker logs "$postgres_container" >&2
    exit 1
  fi
  sleep 1
done
docker exec "$postgres_container" pg_isready -U postgres >/dev/null

cat > "$fixture_root/nomad.hcl" <<EOF
data_dir = "$fixture_root/nomad-data"
bind_addr = "127.0.0.1"
ports { http = $nomad_port rpc = 14650 serf = 14651 }
client { enabled = true }
consul { address = "127.0.0.1:$consul_port" }
EOF
consul agent -dev -client=127.0.0.1 -http-port="$consul_port" -dns-port=18601 \
  -serf-lan-port=18302 -server-port=18303 -data-dir="$fixture_root/consul-data" \
  > "$fixture_root/consul.log" 2>&1 &
consul_pid=$!
for attempt in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$consul_port/v1/status/leader" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$consul_port/v1/status/leader" >/dev/null
nomad agent -dev -config="$fixture_root/nomad.hcl" > "$fixture_root/nomad.log" 2>&1 &
nomad_pid=$!
for attempt in $(seq 1 45); do
  if NOMAD_ADDR="http://127.0.0.1:$nomad_port" nomad node status >/dev/null 2>&1; then break; fi
  sleep 1
done
NOMAD_ADDR="http://127.0.0.1:$nomad_port" nomad node status >/dev/null

export NORN_TEST_NOMAD_ADDR="http://127.0.0.1:$nomad_port"
export NORN_TEST_NOMAD_DOCKER=1
export NORN_TEST_FUNCTION_IMAGE="$image"
export NORN_TEST_DATABASE_URL="postgres://postgres:$postgres_password@127.0.0.1:$postgres_port/postgres?sslmode=disable"
cd "$(dirname "${BASH_SOURCE[0]}")/../api"
go test . -run '^TestClaimedFunctionV3WorkerProcessCrashNomadPostgres$' -count=1 -v
