#!/usr/bin/env bash
set -euo pipefail

fixture_root="$(mktemp -d /tmp/norn-wp-restore.XXXXXX)"
fixture_id="${fixture_root##*.}"
mysql_container="norn-wp-restore-mysql-${fixture_id}"
postgres_container="norn-wp-restore-postgres-${fixture_id}"
mysql_root_password="disposable-norn-root"
postgres_password="disposable-norn-postgres"
mysql_port=13346
postgres_port=15436
nomad_port=14646
consul_port=18500
host_ip="$(ipconfig getifaddr en0)"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
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
  docker rm -fv "$mysql_container" "$postgres_container" >/dev/null 2>&1 || true
  python3 - "$fixture_root/volumes-before" <<'PY'
import json, pathlib, subprocess, sys
baseline=pathlib.Path(sys.argv[1])
if baseline.is_file():
    before=set(baseline.read_text().splitlines())
    after=set(subprocess.run(['docker','volume','ls','-q'],capture_output=True,text=True).stdout.splitlines())
    ids=subprocess.run(['docker','ps','-aq'],capture_output=True,text=True).stdout.splitlines()
    attached=set()
    if ids:
        result=subprocess.run(['docker','inspect',*ids],capture_output=True,text=True)
        if result.returncode==0:
            for container in json.loads(result.stdout):
                attached.update(mount.get('Name') for mount in container.get('Mounts',[]) if mount.get('Type')=='volume')
    for name in after-before:
        if name in attached:
            continue
        result=subprocess.run(['docker','volume','inspect',name],capture_output=True,text=True)
        if result.returncode==0 and 'com.docker.volume.anonymous' in (json.loads(result.stdout)[0].get('Labels') or {}):
            subprocess.run(['docker','volume','rm',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
PY
  chmod -RN "$fixture_root/nomad-data" 2>/dev/null || true
  python3 - "$fixture_root" <<'PY'
import shutil, sys
shutil.rmtree(sys.argv[1], ignore_errors=True)
PY
  exit "$status"
}
trap cleanup EXIT

[[ -n "$host_ip" ]] || { echo 'a reachable en0 address is required' >&2; exit 1; }
if [[ -n "$(docker ps -aq)" ]]; then
  echo 'run this disposable qualification in an empty Docker context' >&2
  exit 1
fi
for port in "$mysql_port" "$postgres_port" "$nomad_port" 14647 14648 "$consul_port"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "qualification port $port is in use" >&2
    exit 1
  fi
done
docker volume ls -q > "$fixture_root/volumes-before"

mkdir -p "$fixture_root/certs" "$fixture_root/mysql-data" "$fixture_root/postgres-data" "$fixture_root/nomad-data" "$fixture_root/wp-content" "$fixture_root/consul-data"
chmod 755 "$fixture_root" "$fixture_root/certs"
openssl genrsa -out "$fixture_root/certs/ca.key" 2048 >/dev/null 2>&1
openssl req -x509 -new -nodes -key "$fixture_root/certs/ca.key" -sha256 -days 1 -subj '/CN=Norn disposable CA' -out "$fixture_root/certs/ca.pem" >/dev/null 2>&1
openssl genrsa -out "$fixture_root/certs/server.key" 2048 >/dev/null 2>&1
openssl req -new -key "$fixture_root/certs/server.key" -subj "/CN=$host_ip" -out "$fixture_root/certs/server.csr" >/dev/null 2>&1
printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\n' "$host_ip" > "$fixture_root/certs/server.ext"
openssl x509 -req -in "$fixture_root/certs/server.csr" -CA "$fixture_root/certs/ca.pem" -CAkey "$fixture_root/certs/ca.key" -CAcreateserial -out "$fixture_root/certs/server.pem" -days 1 -sha256 -extfile "$fixture_root/certs/server.ext" >/dev/null 2>&1
openssl genrsa -out "$fixture_root/certs/wrong-ca.key" 2048 >/dev/null 2>&1
openssl req -x509 -new -nodes -key "$fixture_root/certs/wrong-ca.key" -sha256 -days 1 -subj '/CN=Norn unrelated CA' -out "$fixture_root/certs/wrong-ca.pem" >/dev/null 2>&1
chmod 644 "$fixture_root/certs"/*.pem "$fixture_root/certs/server.key"

docker run -d --name "$mysql_container" --mount "type=bind,src=$fixture_root/mysql-data,dst=/var/lib/mysql" --mount "type=bind,src=$fixture_root/certs,dst=/certs,readonly" -e "MYSQL_ROOT_PASSWORD=$mysql_root_password" -p "$mysql_port:3306" mysql:8.4 --ssl-ca=/certs/ca.pem --ssl-cert=/certs/server.pem --ssl-key=/certs/server.key --require-secure-transport=ON > /dev/null
docker run -d --name "$postgres_container" --tmpfs /var/lib/postgresql/data:rw,size=512m -e "POSTGRES_PASSWORD=$postgres_password" -p "127.0.0.1:$postgres_port:5432" postgres:16 > /dev/null
for attempt in $(seq 1 90); do
  if docker exec "$mysql_container" mysql -h localhost -u root -p"$mysql_root_password" -Nse 'SELECT 1' >/dev/null 2>&1 && docker exec "$postgres_container" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  if [[ "$(docker inspect "$postgres_container" --format '{{.State.Running}}')" != true ]]; then docker logs "$postgres_container" 2>&1 | tail -30; exit 1; fi
  if [[ "$(docker inspect "$mysql_container" --format '{{.State.Running}}')" != true ]]; then docker logs "$mysql_container" 2>&1 | tail -30; exit 1; fi
  sleep 1
done
docker exec "$mysql_container" mysql -h localhost -u root -p"$mysql_root_password" -Nse 'SELECT 1' >/dev/null
docker exec "$postgres_container" pg_isready -U postgres >/dev/null
cat > "$fixture_root/mysql-fixture.sql" <<'SQL'
CREATE DATABASE wordpress;
CREATE DATABASE wp_restore_target;
CREATE USER 'wordpress'@'%' IDENTIFIED BY 'disposable-wordpress';
CREATE USER 'wp_snapshot'@'%' IDENTIFIED BY 'disposable-snapshot';
CREATE USER 'wp_fence'@'%' IDENTIFIED BY 'disposable-fence';
CREATE USER 'wp_restore_runtime'@'%' IDENTIFIED BY 'disposable-restore-runtime';
CREATE USER 'wp_restore'@'%' IDENTIFIED BY 'disposable-restore';
GRANT ALL PRIVILEGES ON wordpress.* TO 'wordpress'@'%';
GRANT SELECT, SHOW VIEW, TRIGGER, EVENT, LOCK TABLES ON wordpress.* TO 'wp_snapshot'@'%';
GRANT CREATE USER, PROCESS, CONNECTION_ADMIN ON *.* TO 'wp_fence'@'%';
GRANT SELECT ON mysql.user TO 'wp_fence'@'%';
GRANT ALL PRIVILEGES ON wp_restore_target.* TO 'wp_restore_runtime'@'%';
GRANT ALL PRIVILEGES ON wp_restore_target.* TO 'wp_restore'@'%';
CREATE TABLE wordpress.source_rehearsal (value VARCHAR(64) NOT NULL);
INSERT INTO wordpress.source_rehearsal VALUES ('source-rehearsal');
SQL
docker exec -i "$mysql_container" mysql -u root -p"$mysql_root_password" < "$fixture_root/mysql-fixture.sql" >/dev/null

cat > "$fixture_root/nomad.hcl" <<EOF
data_dir = "$fixture_root/nomad-data"
bind_addr = "127.0.0.1"
ports { http = $nomad_port rpc = 14647 serf = 14648 }
client {
  enabled = true
  host_volume "wp-content" { path = "$fixture_root/wp-content" read_only = false }
}
consul { address = "127.0.0.1:$consul_port" }
EOF
consul agent -dev -client=127.0.0.1 -http-port="$consul_port" -dns-port=18600 -serf-lan-port=18301 -server-port=18300 -data-dir="$fixture_root/consul-data" > "$fixture_root/consul.log" 2>&1 &
consul_pid=$!
for attempt in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$consul_port/v1/status/leader" >/dev/null 2>&1; then break; fi
  sleep 1
done
nomad agent -dev -config="$fixture_root/nomad.hcl" > "$fixture_root/nomad.log" 2>&1 &
nomad_pid=$!
for attempt in $(seq 1 45); do
  if NOMAD_ADDR="http://127.0.0.1:$nomad_port" nomad node status >/dev/null 2>&1; then break; fi
  sleep 1
done
NOMAD_ADDR="http://127.0.0.1:$nomad_port" nomad node status > "$fixture_root/nodes.txt"

export NORN_TEST_NOMAD_ADDR="http://127.0.0.1:$nomad_port"
export NORN_TEST_NOMAD_DOCKER=1
export NORN_TEST_DATABASE_URL="postgres://postgres:$postgres_password@127.0.0.1:$postgres_port/postgres?sslmode=disable"
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_HOST="$host_ip"
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_SERVER_NAME="$host_ip"
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_PORT="$mysql_port"
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_USER=wordpress
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_PASSWORD=disposable-wordpress
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_DATABASE=wordpress
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_ENGINE_VERSION=8.4
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_CA_PEM_B64="$(base64 < "$fixture_root/certs/ca.pem" | tr -d '\n')"
export NORN_TEST_WORDPRESS_DEPLOY_MYSQL_WRONG_CA_PEM_B64="$(base64 < "$fixture_root/certs/wrong-ca.pem" | tr -d '\n')"
export NORN_TEST_WORDPRESS_DEPLOY_CONTENT_VOLUME=wp-content
export NORN_TEST_WORDPRESS_DEPLOY_SOURCE_QUIESCE=1
export NORN_TEST_WORDPRESS_DEPLOY_SNAPSHOT_PASSWORD=disposable-snapshot
export NORN_TEST_WORDPRESS_DEPLOY_FENCE_PASSWORD=disposable-fence
export NORN_TEST_WORDPRESS_DEPLOY_RESTORE=1
export NORN_TEST_WORDPRESS_DEPLOY_RESTORE_DATABASE=wp_restore_target
export NORN_TEST_WORDPRESS_DEPLOY_RESTORE_USER=wp_restore_runtime
export NORN_TEST_WORDPRESS_DEPLOY_RESTORE_PASSWORD=disposable-restore-runtime
export NORN_TEST_WORDPRESS_DEPLOY_RESTORE_ROLE_PASSWORD=disposable-restore

cd "$repo_root/v2/api"
if [[ "${NORN_TEST_WORDPRESS_SOURCE_CRASH_AFTER_TRANSFER:-}" == "1" || "${NORN_TEST_WORDPRESS_SOURCE_CRASH_BEFORE_COMMIT:-}" == "1" || "${NORN_TEST_WORDPRESS_SOURCE_CRASH_AFTER_STAGE:-}" == "1" || "${NORN_TEST_WORDPRESS_SOURCE_CRASH_BEFORE_PUBLISH:-}" == "1" || "${NORN_TEST_WORDPRESS_SOURCE_CRASH_AFTER_PUBLISH:-}" == "1" ]]; then
  go build -buildvcs=false -tags norn_test_crash_hooks -o "$fixture_root/norn-mysql-maintenance" ./cmd/norn-mysql-maintenance
else
  go build -buildvcs=false -o "$fixture_root/norn-mysql-maintenance" ./cmd/norn-mysql-maintenance
fi
export NORN_TEST_WORDPRESS_MAINTENANCE_CLI="$fixture_root/norn-mysql-maintenance"
go test ./worker -run '^TestClaimedWordPressVerifiedTLSDeployInNomad$' -count=1 -v
restored_marker="$(docker exec "$mysql_container" mysql -u root -p"$mysql_root_password" -Nse "SELECT COUNT(*) FROM wp_restore_target.source_rehearsal WHERE value='source-rehearsal'" 2>/dev/null)"
restored_options="$(docker exec "$mysql_container" mysql -u root -p"$mysql_root_password" -Nse 'SELECT COUNT(*) FROM wp_restore_target.wp_options' 2>/dev/null)"
restored_users="$(docker exec "$mysql_container" mysql -u root -p"$mysql_root_password" -Nse 'SELECT COUNT(*) FROM wp_restore_target.wp_users' 2>/dev/null)"
[[ "$restored_marker" == 1 && "$restored_options" -gt 0 && "$restored_users" -gt 0 ]]
echo "restored target contains source marker, populated wp_options, and installed wp_users"
