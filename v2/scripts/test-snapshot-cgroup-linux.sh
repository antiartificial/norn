#!/usr/bin/env bash
set -euo pipefail

# Requires Docker Desktop or Docker Engine with a privileged cgroup-v2 Linux
# container. It creates no Norn resources and removes the container on exit.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
exec docker run --rm --privileged --cgroupns=private --mount "type=bind,src=${repo_root},dst=/src,readonly" postgres:16 bash /src/v2/scripts/snapshot-cgroup-linux-container.sh
