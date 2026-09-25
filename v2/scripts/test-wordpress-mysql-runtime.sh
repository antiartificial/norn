#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mysql_container="norn-wordpress-mysql-runtime"
mysql_image="mysql:8.4"
wordpress_image="wordpress:6.8.2-php8.3-apache"
root_password="norn-wordpress-qualification-root"

cleanup() {
  docker rm --force "$mysql_container" >/dev/null 2>&1 || true
}
if docker container inspect "$mysql_container" >/dev/null 2>&1; then
  echo "refusing to replace existing container $mysql_container" >&2
  exit 1
fi
trap cleanup EXIT

docker run --detach --name "$mysql_container" \
  --env "MYSQL_ROOT_PASSWORD=$root_password" \
  --publish 127.0.0.1::3306 \
  "$mysql_image" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$mysql_container" mysqladmin ping --host 127.0.0.1 --user root --password="$root_password" --silent >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$mysql_container" mysqladmin ping --host 127.0.0.1 --user root --password="$root_password" --silent >/dev/null

host_port="$(docker port "$mysql_container" 3306/tcp)"
host_port="${host_port##*:}"
cd "$repo_root/v2/api"
NORN_TEST_MYSQL_DSN="root:${root_password}@tcp(127.0.0.1:${host_port})/mysql" \
NORN_TEST_WORDPRESS_MYSQL_ENDPOINT="host.docker.internal:${host_port}" \
NORN_TEST_WORDPRESS_IMAGE="$wordpress_image" \
go test ./database -run '^TestWordPressImageUsesMySQLRuntimeComponents$' -count=1 -v
