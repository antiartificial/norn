#!/usr/bin/env bash
# Disposable M1/M4 rehearsal: kill a real etcd-backed app.deploy worker after
# Nomad accepts its service job, then prove lease-expiry recovery fails closed.
# It only binds loopback ports and removes every fixture resource on exit.
set -euo pipefail

fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/norn-etcd-deploy-crash.XXXXXX")"
fixture_id="${fixture_root##*.}"
etcd_container="norn-etcd-deploy-crash-etcd-${fixture_id}"
etcd_image="quay.io/coreos/etcd@sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6"
workload_image="docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
nomad_pid=""
consul_pid=""
etcd_port=12379
nomad_port=14649
consul_port=18501

cleanup() {
  status=$?
  trap - EXIT
  if [[ -n "$nomad_pid" ]]; then kill "$nomad_pid" 2>/dev/null || true; wait "$nomad_pid" 2>/dev/null || true; fi
  if [[ -n "$consul_pid" ]]; then kill "$consul_pid" 2>/dev/null || true; wait "$consul_pid" 2>/dev/null || true; fi
  docker rm -fv "$etcd_container" >/dev/null 2>&1 || true
  chmod -RN "$fixture_root/nomad-data" 2>/dev/null || true
  rm -rf "$fixture_root"
  exit "$status"
}
trap cleanup EXIT

for tool in docker nomad consul curl go; do command -v "$tool" >/dev/null; done
for port in "$etcd_port" "$nomad_port" 14650 14651 "$consul_port" 18601 18302 18303; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "qualification port $port is in use" >&2
    exit 1
  fi
done

docker image inspect "$etcd_image" >/dev/null
docker image inspect "$workload_image" >/dev/null

mkdir -p "$fixture_root/nomad-data" "$fixture_root/consul-data"
docker run -d --name "$etcd_container" -p "127.0.0.1:$etcd_port:2379" "$etcd_image" \
  /usr/local/bin/etcd --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls "http://127.0.0.1:$etcd_port" >/dev/null
for attempt in $(seq 1 45); do
  if curl -fsS "http://127.0.0.1:$etcd_port/health" | grep -q '"health":"true"'; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$etcd_port/health" | grep -q '"health":"true"'

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
  if curl -fsS "http://127.0.0.1:$consul_port/v1/status/leader" >/dev/null; then break; fi
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

export NORN_TEST_ETCD_ENDPOINTS="http://127.0.0.1:$etcd_port"
export NORN_TEST_NOMAD_ADDR="http://127.0.0.1:$nomad_port"
export NORN_TEST_DEPLOYMENT_IMAGE="$workload_image"
cd "$(dirname "${BASH_SOURCE[0]}")/../api"
go test ./etcdstore -run '^TestV3DeploymentWorkerProcessCrashFailsClosedThroughEtcdAndDisposableNomad$' -count=1 -v
