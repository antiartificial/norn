#!/usr/bin/env bash
set -euo pipefail

# Starts a disposable single-member etcd for the production-profile process
# proof. The client credential is distinct from the bootstrap administrator;
# after auth is enabled it can read and write only the generated test prefix.
fixture_parent="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
fixture_root="$(mktemp -d "${fixture_parent%/}/norn-etcd-tls-rbac.XXXXXX")"
container_name="norn-etcd-tls-rbac-${RANDOM}${RANDOM}"
endpoint="https://127.0.0.1:12379"
prefix="/norn-test/production-tls"
root_password="fixture-root-${RANDOM}${RANDOM}${RANDOM}"
user_password="fixture-user-${RANDOM}${RANDOM}${RANDOM}"

chmod 0700 "$fixture_root"

cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup ERR

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -sha256 \
  -subj '/CN=norn-etcd-fixture-ca' -keyout "$fixture_root/ca.key" -out "$fixture_root/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=127.0.0.1' \
  -keyout "$fixture_root/server.key" -out "$fixture_root/server.csr" >/dev/null 2>&1
printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nextendedKeyUsage=serverAuth\n' > "$fixture_root/server.ext"
openssl x509 -req -days 1 -sha256 -CA "$fixture_root/ca.crt" -CAkey "$fixture_root/ca.key" -CAcreateserial \
  -in "$fixture_root/server.csr" -out "$fixture_root/server.crt" -extfile "$fixture_root/server.ext" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=norn-test-client' \
  -keyout "$fixture_root/client.key" -out "$fixture_root/client.csr" >/dev/null 2>&1
printf 'extendedKeyUsage=clientAuth\n' > "$fixture_root/client.ext"
openssl x509 -req -days 1 -sha256 -CA "$fixture_root/ca.crt" -CAkey "$fixture_root/ca.key" -CAcreateserial \
  -in "$fixture_root/client.csr" -out "$fixture_root/client.crt" -extfile "$fixture_root/client.ext" >/dev/null 2>&1
chmod 0600 "$fixture_root"/*.key

docker run --detach --name "$container_name" --publish 127.0.0.1:12379:2379 --volume "$fixture_root:/tls:ro" \
  quay.io/coreos/etcd:v3.5.15 \
  /usr/local/bin/etcd --name fixture --data-dir /var/lib/etcd \
  --listen-client-urls https://0.0.0.0:2379 --advertise-client-urls "$endpoint" \
  --trusted-ca-file /tls/ca.crt --cert-file /tls/server.crt --key-file /tls/server.key --client-cert-auth >/dev/null

export ETCDCTL_API=3
etcdctl=(docker exec "$container_name" etcdctl --endpoints https://127.0.0.1:2379 --cacert /tls/ca.crt --cert /tls/client.crt --key /tls/client.key)
for attempt in {1..30}; do
  if "${etcdctl[@]}" endpoint health >/dev/null 2>&1; then
    break
  fi
  [[ "$attempt" == 30 ]] && { docker logs "$container_name" >&2; exit 1; }
  sleep 1
done

"${etcdctl[@]}" user add "root:${root_password}" >/dev/null
"${etcdctl[@]}" role add root >/dev/null
"${etcdctl[@]}" user grant-role root root >/dev/null
"${etcdctl[@]}" role add norn-test >/dev/null
"${etcdctl[@]}" role grant-permission --prefix=true norn-test readwrite "${prefix}/" >/dev/null
"${etcdctl[@]}" user add "norn-test:${user_password}" >/dev/null
"${etcdctl[@]}" user grant-role norn-test norn-test >/dev/null
"${etcdctl[@]}" auth enable >/dev/null

if [[ -n "${GITHUB_ENV:-}" ]]; then
  printf 'NORN_TEST_ETCD_TLS_ENDPOINTS=%s\n' "$endpoint" >> "$GITHUB_ENV"
  printf 'NORN_TEST_ETCD_TLS_PREFIX=%s\n' "$prefix" >> "$GITHUB_ENV"
  printf 'NORN_TEST_ETCD_TLS_CA_FILE=%s\n' "$fixture_root/ca.crt" >> "$GITHUB_ENV"
  printf 'NORN_TEST_ETCD_TLS_CERT_FILE=%s\n' "$fixture_root/client.crt" >> "$GITHUB_ENV"
  printf 'NORN_TEST_ETCD_TLS_KEY_FILE=%s\n' "$fixture_root/client.key" >> "$GITHUB_ENV"
  printf 'NORN_TEST_ETCD_TLS_USERNAME=norn-test\n' >> "$GITHUB_ENV"
  printf 'NORN_TEST_ETCD_TLS_PASSWORD=%s\n' "$user_password" >> "$GITHUB_ENV"
fi
printf '::add-mask::%s\n' "$root_password"
printf '::add-mask::%s\n' "$user_password"
printf 'TLS etcd fixture ready: client is restricted to %s/\n' "$prefix"
