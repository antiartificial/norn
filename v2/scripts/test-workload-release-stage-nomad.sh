#!/usr/bin/env bash
# Disposable integration qualification for the V3 workload-release target
# stage boundary. It uses loopback etcd/Consul/Nomad and removes its state.
set -euo pipefail

fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/norn-workload-release-stage.XXXXXX")"
etcd_pid=""
consul_pid=""
nomad_pid=""
etcd_port=16791
etcd_peer_port=16792
consul_http_port=16591
consul_dns_port=16601
consul_serf_port=16302
consul_server_port=16303
nomad_http_port=17646
nomad_rpc_port=17647
nomad_serf_port=17648
workload_image="${NORN_TEST_DEPLOYMENT_IMAGE:-docker.io/library/alpine@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a}"

cleanup() {
  status=$?
  trap - EXIT
  if [[ -n "$nomad_pid" ]]; then kill "$nomad_pid" 2>/dev/null || true; wait "$nomad_pid" 2>/dev/null || true; fi
  if [[ -n "$consul_pid" ]]; then kill "$consul_pid" 2>/dev/null || true; wait "$consul_pid" 2>/dev/null || true; fi
  if [[ -n "$etcd_pid" ]]; then kill "$etcd_pid" 2>/dev/null || true; wait "$etcd_pid" 2>/dev/null || true; fi
  if [[ "$(uname -s)" == "Darwin" ]]; then chmod -RN "$fixture_root/nomad-data" 2>/dev/null || true; fi
  chmod -R u+rwX "$fixture_root/nomad-data" 2>/dev/null || true
  rm -rf "$fixture_root"
  exit "$status"
}
trap cleanup EXIT

for tool in docker etcd nomad consul curl go lsof rg; do command -v "$tool" >/dev/null; done
for port in "$etcd_port" "$etcd_peer_port" "$consul_http_port" "$consul_dns_port" "$consul_serf_port" "$consul_server_port" "$nomad_http_port" "$nomad_rpc_port" "$nomad_serf_port"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 || lsof -nP -iUDP:"$port" >/dev/null 2>&1; then
    echo "qualification port $port is already in use" >&2
    exit 1
  fi
done

docker image inspect "$workload_image" >/dev/null
docker_host="$(docker context inspect "$(docker context show)" --format '{{ .Endpoints.docker.Host }}')"
mkdir -p "$fixture_root/etcd-data" "$fixture_root/consul-data" "$fixture_root/nomad-data"

etcd --name norn-workload-release-stage \
  --data-dir="$fixture_root/etcd-data" \
  --listen-client-urls="http://127.0.0.1:$etcd_port" \
  --advertise-client-urls="http://127.0.0.1:$etcd_port" \
  --listen-peer-urls="http://127.0.0.1:$etcd_peer_port" \
  --initial-advertise-peer-urls="http://127.0.0.1:$etcd_peer_port" \
  --initial-cluster="norn-workload-release-stage=http://127.0.0.1:$etcd_peer_port" \
  >"$fixture_root/etcd.log" 2>&1 &
etcd_pid=$!
for attempt in $(seq 1 45); do
  if curl -fsS "http://127.0.0.1:$etcd_port/health" 2>/dev/null | rg -q '"health":"true"'; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$etcd_port/health" | rg -q '"health":"true"'

consul agent -dev -client=127.0.0.1 \
  -http-port="$consul_http_port" -dns-port="$consul_dns_port" \
  -serf-lan-port="$consul_serf_port" -server-port="$consul_server_port" \
  -data-dir="$fixture_root/consul-data" >"$fixture_root/consul.log" 2>&1 &
consul_pid=$!
for attempt in $(seq 1 45); do
  if curl -fsS "http://127.0.0.1:$consul_http_port/v1/status/leader" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$consul_http_port/v1/status/leader" >/dev/null

cat >"$fixture_root/nomad.hcl" <<HCL
data_dir = "$fixture_root/nomad-data"
bind_addr = "127.0.0.1"
datacenter = "dc1"
region = "global"
ports {
  http = $nomad_http_port
  rpc = $nomad_rpc_port
  serf = $nomad_serf_port
}
client { enabled = true }
consul { address = "127.0.0.1:$consul_http_port" }
plugin "docker" {
  config {
    endpoint = "$docker_host"
  }
}
HCL
nomad agent -dev -config="$fixture_root/nomad.hcl" >"$fixture_root/nomad.log" 2>&1 &
nomad_pid=$!
for attempt in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$nomad_http_port/v1/status/leader" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$nomad_http_port/v1/status/leader" >/dev/null
NOMAD_ADDR="http://127.0.0.1:$nomad_http_port" nomad node status >/dev/null

export NORN_TEST_ETCD_ENDPOINTS="http://127.0.0.1:$etcd_port"
export NORN_TEST_NOMAD_ADDR="http://127.0.0.1:$nomad_http_port"
export NORN_TEST_DEPLOYMENT_IMAGE="$workload_image"
export NORN_TEST_NOMAD_DOCKER=1
cd "$(dirname "${BASH_SOURCE[0]}")/../api"
go test . -run '^TestWorkloadReleaseStageRecoversLostNomadResponseAfterEtcdRestart$' -count=1 -v
