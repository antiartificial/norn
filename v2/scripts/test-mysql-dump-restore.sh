#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mysql_container="norn-mysql-recovery-$RANDOM-$$"
root_password="norn-disposable-recovery-root"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/norn-mysql-recovery.XXXXXX")"
chmod 700 "$scratch"
mkdir "$scratch/mysql-data"

cleanup() {
  docker rm --force "$mysql_container" >/dev/null 2>&1 || true
  rm -r -- "$scratch"
}
if docker container inspect "$mysql_container" >/dev/null 2>&1; then
  echo "refusing to replace existing container $mysql_container" >&2
  exit 1
fi
trap cleanup EXIT

docker run --detach --name "$mysql_container" \
  --env "MYSQL_ROOT_PASSWORD=$root_password" \
  --mount "type=bind,source=$scratch/mysql-data,target=/var/lib/mysql" \
  --publish 127.0.0.1::3306 mysql:8.4 >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$mysql_container" mysqladmin ping --host 127.0.0.1 --user root --password="$root_password" --silent >/dev/null 2>&1; then
    break
  fi
  if [[ "$(docker inspect --format '{{.State.Running}}' "$mysql_container")" != "true" ]]; then
    break
  fi
  sleep 1
done
if ! docker exec "$mysql_container" mysqladmin ping --host 127.0.0.1 --user root --password="$root_password" --silent >/dev/null 2>&1; then
  docker logs --tail 80 "$mysql_container" >&2
  echo "disposable MySQL did not become ready" >&2
  exit 1
fi
host_port="$(docker port "$mysql_container" 3306/tcp)"
host_port="${host_port##*:}"
docker cp "$mysql_container:/var/lib/mysql/ca.pem" "$scratch/ca.pem"
chmod 600 "$scratch/ca.pem"
cd "$repo_root/v2/api"
NORN_TEST_MYSQL_DSN="root:${root_password}@tcp(127.0.0.1:${host_port})/mysql" \
  NORN_TEST_MYSQL_CA_PEM="$scratch/ca.pem" \
  NORN_TEST_MYSQL_CA_FILE="$scratch/ca.pem" \
  go test ./database -run '^TestMySQL(RuntimeComponentsReachDeclaredTarget|ExactTargetDumpRestore)$' -count=1 -v
