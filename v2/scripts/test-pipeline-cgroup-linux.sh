#!/usr/bin/env bash
set -euo pipefail

# Verify the external PostgreSQL fixture in a disposable privileged Linux
# container. The root-owned test process can then use the real cgroup backend.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
docker_arch="$(docker info --format '{{.Architecture}}')"
case "$docker_arch" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64|amd64) goarch=amd64 ;;
  *) echo "unsupported Docker architecture: $docker_arch" >&2; exit 2 ;;
esac
scratch="$(mktemp -d)"
cleanup() {
  rm -f "$scratch/pipeline.test" "$scratch/norn-effect-runner"
  rmdir "$scratch"
}
trap cleanup EXIT

(
  cd "$repo_root/v2/api"
  GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -buildvcs=false -o "$scratch/pipeline.test" ./pipeline
  GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -buildvcs=false -o "$scratch/norn-effect-runner" ./cmd/norn-effect-runner
)

for test_name in TestRecoveredDeploymentMigrationReusesAcceptedPreMigrationSnapshots TestDeployMigrationRecoversAfterLiteralProcessExit TestDeployMigrationWaitsForOriginalTransactionAfterAPIExit TestDeployMigrationRecoversAfterSuccessorAPIExit TestDeployMigrationReplayVerifiesOriginalRemoteSnapshot TestDeployMigrationReplayRejectsChangedRemoteSnapshot; do
  docker run --rm --privileged --cgroupns=private \
    --env "NORN_PIPELINE_TEST_RUN=$test_name" \
    --mount "type=bind,src=$repo_root,dst=/src,readonly" \
    --mount "type=bind,src=$scratch/pipeline.test,dst=/pipeline.test,readonly" \
    --mount "type=bind,src=$scratch/norn-effect-runner,dst=/runner,readonly" \
    --tmpfs /tmp:rw,exec,size=256m \
    postgres:16 bash /src/v2/scripts/pipeline-cgroup-linux-container.sh
done
