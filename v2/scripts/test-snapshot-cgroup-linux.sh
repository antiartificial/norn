#!/usr/bin/env bash
set -euo pipefail

# Run the opt-in snapshot and migration containment tests in a disposable
# privileged cgroup-v2 PostgreSQL container. Cross-compile on the host so the
# container does not install Go or consume a persistent Docker volume.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docker_arch="$(docker info --format '{{.Architecture}}')"
case "$docker_arch" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64|amd64) goarch=amd64 ;;
  *) echo "unsupported Docker architecture: $docker_arch" >&2; exit 2 ;;
esac
scratch="$(mktemp -d)"
cleanup() {
  rm -f "$scratch/supervisor.test" "$scratch/norn-effect-runner"
  rmdir "$scratch"
}
trap cleanup EXIT

(
  cd "$repo_root/v2/api"
  GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -buildvcs=false -o "$scratch/supervisor.test" ./effect/supervisor
  GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -buildvcs=false -o "$scratch/norn-effect-runner" ./cmd/norn-effect-runner
)

docker run --rm --privileged --cgroupns=private \
  --mount "type=bind,src=$repo_root,dst=/src,readonly" \
  --mount "type=bind,src=$scratch/supervisor.test,dst=/supervisor.test,readonly" \
  --mount "type=bind,src=$scratch/norn-effect-runner,dst=/runner,readonly" \
  --tmpfs /tmp:rw,exec,size=192m \
  postgres:16 bash /src/v2/scripts/snapshot-cgroup-linux-container.sh
