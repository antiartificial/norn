#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mysql_container="norn-mysql-recovery-$RANDOM-$$"
root_password="norn-disposable-recovery-root"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/norn-mysql-recovery.XXXXXX")"
chmod 700 "$scratch"
if docker container inspect "$mysql_container" >/dev/null 2>&1; then
  rm -r -- "$scratch"
  echo "refusing to replace existing container $mysql_container" >&2
  exit 1
fi
cleanup() {
  docker rm --force "$mysql_container" >/dev/null 2>&1 || true
  rm -r -- "$scratch"
}
trap cleanup EXIT
mkdir "$scratch/mysql-data"
mkdir "$scratch/tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$scratch/tls/ca-key.pem" -out "$scratch/tls/ca.pem" \
  -subj '/CN=Norn disposable MySQL CA' >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
  -keyout "$scratch/tls/server-key.pem" -out "$scratch/tls/server.csr" \
  -subj '/CN=127.0.0.1' >/dev/null 2>&1
cat >"$scratch/tls/server.ext" <<'EOF'
subjectAltName=IP:127.0.0.1
extendedKeyUsage=serverAuth
EOF
openssl x509 -req -in "$scratch/tls/server.csr" \
  -CA "$scratch/tls/ca.pem" -CAkey "$scratch/tls/ca-key.pem" -CAcreateserial \
  -out "$scratch/tls/server-cert.pem" -days 1 \
  -extfile "$scratch/tls/server.ext" >/dev/null 2>&1
chmod 755 "$scratch/tls"
chmod 644 "$scratch/tls/ca.pem" "$scratch/tls/server-cert.pem" "$scratch/tls/server-key.pem"

docker run --detach --name "$mysql_container" \
  --env "MYSQL_ROOT_PASSWORD=$root_password" \
  --mount "type=bind,source=$scratch/mysql-data,target=/var/lib/mysql" \
  --mount "type=bind,source=$scratch/tls,target=/norn-tls,readonly" \
  --publish 127.0.0.1::3306 mysql:8.4 \
  --ssl-ca=/norn-tls/ca.pem --ssl-cert=/norn-tls/server-cert.pem \
  --ssl-key=/norn-tls/server-key.pem >/dev/null

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
cd "$repo_root/v2/api"
NORN_TEST_MYSQL_DSN="root:${root_password}@tcp(127.0.0.1:${host_port})/mysql" \
  NORN_TEST_MYSQL_CA_PEM="$scratch/tls/ca.pem" \
  NORN_TEST_MYSQL_CA_FILE="$scratch/tls/ca.pem" \
  NORN_TEST_MYSQL_SERVER_NAME="127.0.0.1" \
  go test ./database -run '^TestMySQL(RuntimeComponentsReachDeclaredTarget|ExactTargetDumpRestore)$' -count=1 -v
