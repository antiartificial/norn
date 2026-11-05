#!/usr/bin/env bash
set -euo pipefail

readonly expected_sha256='2bc04575ed02e5240780cbe1f2e09e7cd695cf2197a1cbc6ba3d0c4594c46b8c'
if [[ $# -ne 1 ]]; then
  printf 'usage: %s <output-path>\n' "$0" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_path="$1"
output_dir="$(dirname "$output_path")"
if [[ ! -d "$output_dir" ]]; then
  printf 'output directory does not exist: %s\n' "$output_dir" >&2
  exit 2
fi

temporary_path="$(/usr/bin/mktemp "${output_path}.tmp.XXXXXX")"
trap 'rm -f "$temporary_path"' EXIT

(
  cd "$repo_root"
  GOTOOLCHAIN=go1.26.10 GOWORK=off CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -buildvcs=false -ldflags='-buildid=' \
      -o "$temporary_path" v2/infra/m5/intent-verify.go
)

actual_sha256="$(/usr/bin/shasum -a 256 "$temporary_path" | /usr/bin/awk '{print $1}')"
if [[ "$actual_sha256" != "$expected_sha256" ]]; then
  printf 'M5 intent verifier build hash mismatch: %s\n' "$actual_sha256" >&2
  exit 1
fi

chmod 0700 "$temporary_path"
mv "$temporary_path" "$output_path"
trap - EXIT
