#!/usr/bin/env bash
set -euo pipefail

# Local, provider-free M4 replica qualification. This starts one disposable
# Nomad server, three app-pool clients, and one disposable PostgreSQL server.

for tool in nomad initdb pg_ctl createdb go python3 docker; do
  command -v "$tool" >/dev/null || { printf 'missing tool: %s\n' "$tool" >&2; exit 2; }
done

m4_image=${NORN_TEST_M4_IMAGE:-docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662}
docker pull "$m4_image" >/dev/null

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
scratch_base=${NORN_TEST_TMPDIR:-/tmp}
scratch=$(mktemp -d "$scratch_base/norn-m4-capacity.XXXXXX")
chmod 700 "$scratch"
nomad_pids=()
pg_started=false
cleanup() {

	status=$?
	if (( status != 0 )); then
		for log in "$scratch"/*.log; do
			[[ -f $log ]] || continue
			printf '%s\n' "--- $(basename "$log")" >&2
			tail -40 "$log" >&2 || true
		done
	fi
	for pid in "${nomad_pids[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
	for pid in "${nomad_pids[@]}"; do wait "$pid" >/dev/null 2>&1 || true; done
	# Nomad can retain stopped Docker containers after purging the job. Remove
	# only containers whose mounts are rooted in this harness's private scratch
	# directory so their allocation directories can be deleted deterministically.
	while IFS= read -r container_id; do
		[[ -n $container_id ]] || continue
		if docker inspect --format '{{range .Mounts}}{{println .Source}}{{end}}' "$container_id" 2>/dev/null | grep -Fqx "$scratch" || \
		   docker inspect --format '{{range .Mounts}}{{println .Source}}{{end}}' "$container_id" 2>/dev/null | grep -Fq "$scratch/"; then
			docker container rm --force "$container_id" >/dev/null 2>&1 || true
		fi
	done < <(docker ps --all --quiet --filter label=com.hashicorp.nomad.alloc_id)
	if [[ $pg_started == true ]]; then pg_ctl -D "$scratch/postgres" -m immediate -w stop >/dev/null 2>&1 || true; fi
	chmod -R u+rwX "$scratch" >/dev/null 2>&1 || true
  SCRATCH="$scratch" SCRATCH_BASE="$scratch_base" python3 - <<'PY'
import os, shutil, time
from pathlib import Path
p=Path(os.environ['SCRATCH']); base=Path(os.environ['SCRATCH_BASE']).resolve()
if p.parent.resolve()!=base or not p.name.startswith('norn-m4-capacity.') or p.is_symlink():
    raise SystemExit('refusing to remove unexpected scratch path')
if p.exists():
    for attempt in range(5):
        try:
            shutil.rmtree(p)
            break
        except PermissionError:
            if attempt == 4: raise
            time.sleep(.2)
PY
	return "$status"
}
trap cleanup EXIT

read -r server_http server_rpc server_serf c1_http c1_rpc c1_serf c2_http c2_rpc c2_serf c3_http c3_rpc c3_serf pg_port < <(python3 - <<'PY'
import socket
s=[]
for _ in range(13):
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); s.append((sock,sock.getsockname()[1]))
print(*(port for _,port in s))
for sock,_ in s: sock.close()
PY
)

mkdir -p "$scratch/server" "$scratch/client-1" "$scratch/client-2" "$scratch/client-3" "$scratch/pg-socket"
cat >"$scratch/server.hcl" <<EOF
data_dir = "$scratch/server"
bind_addr = "127.0.0.1"
server {
  enabled = true
  bootstrap_expect = 1
}
client { enabled = false }
ports {
  http = $server_http
  rpc = $server_rpc
  serf = $server_serf
}
advertise {
  http = "127.0.0.1:$server_http"
  rpc = "127.0.0.1:$server_rpc"
  serf = "127.0.0.1:$server_serf"
}
EOF
nomad agent -config="$scratch/server.hcl" >"$scratch/server.log" 2>&1 & nomad_pids+=("$!")

for index in 1 2 3; do
  eval "http_port=\$c${index}_http rpc_port=\$c${index}_rpc serf_port=\$c${index}_serf"
  cat >"$scratch/client-$index.hcl" <<EOF
data_dir = "$scratch/client-$index"
bind_addr = "127.0.0.1"
name = "m4-app-$index"
server { enabled = false }
client {
  enabled = true
  node_pool = "app"
  servers = ["127.0.0.1:$server_rpc"]
  options = { "driver.raw_exec.enable" = "1" }
}
ports {
  http = $http_port
  rpc = $rpc_port
  serf = $serf_port
}
advertise {
  http = "127.0.0.1:$http_port"
  rpc = "127.0.0.1:$rpc_port"
  serf = "127.0.0.1:$serf_port"
}
EOF
  nomad agent -config="$scratch/client-$index.hcl" >"$scratch/client-$index.log" 2>&1 & nomad_pids+=("$!")
done

nomad_addr="http://127.0.0.1:$server_http"
for _ in {1..60}; do
  if NOMAD_ADDR="$nomad_addr" nomad node status -json 2>/dev/null | python3 -c 'import json,sys; n=json.load(sys.stdin); raise SystemExit(0 if len([x for x in n if x.get("Status")=="ready" and x.get("SchedulingEligibility")=="eligible" and x.get("NodePool")=="app"])==3 else 1)' 2>/dev/null; then
    break
  fi
  sleep 1
done
NOMAD_ADDR="$nomad_addr" nomad node status -json | python3 -c 'import json,sys; n=json.load(sys.stdin); assert len([x for x in n if x.get("Status")=="ready" and x.get("SchedulingEligibility")=="eligible" and x.get("NodePool")=="app"])==3, n'

initdb -D "$scratch/postgres" --username=norn_m4 --auth=trust --no-sync >/dev/null
pg_ctl -D "$scratch/postgres" -o "-k '$scratch/pg-socket' -h '' -p $pg_port" -l "$scratch/postgres.log" -w start >/dev/null
pg_started=true
createdb -h "$scratch/pg-socket" -p "$pg_port" -U norn_m4 norn_m4
socket_query=$(SOCKET_DIR="$scratch/pg-socket" python3 - <<'PY'
import os, urllib.parse
print(urllib.parse.quote(os.environ['SOCKET_DIR'], safe=''))
PY
)

cd "$repo_root/v2/api"
NORN_TEST_M4_NOMAD_ADDR="$nomad_addr" \
NORN_TEST_M4_IMAGE="$m4_image" \
NORN_TEST_DATABASE_URL="postgresql://norn_m4@/norn_m4?host=$socket_query&port=$pg_port&sslmode=disable" \
go test ./pipeline -run '^TestM4ReplicaIntentAcrossThreeDisposableNomadClients$' -count=1 -v
