#!/usr/bin/env bash
# Exercise the complete secretless platform bundle path on the checked-out commit.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
sha="$(git -C "$repo" rev-parse HEAD)"
source_epoch="$(git -C "$repo" show -s --format=%ct "$sha")"
created_at="$(git -C "$repo" show -s --format=%cI "$sha")"
described="$(git -C "$repo" describe --tags --exclude='platform-*' "$sha")"
if [[ "$described" =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)-(control|platform)(-[0-9]+-g[0-9a-f]{7,40})?$ ]]; then
  version="${BASH_REMATCH[1]}-platform${BASH_REMATCH[3]}"
elif [[ "$described" =~ ^(v[0-9]+\.[0-9]+\.[0-9]+)(-[0-9]+-g[0-9a-f]{7,40})?$ ]]; then
  version="${BASH_REMATCH[1]}-platform${BASH_REMATCH[2]}"
else
  echo "unsupported platform version description: $described" >&2
  exit 1
fi

scratch="$(mktemp -d "${TMPDIR:-/tmp}/norn-release-bundle-test.XXXXXX")"
cleanup() {
  chmod -R u+w "$scratch" 2>/dev/null || true
  rm -r "$scratch"
}
trap cleanup EXIT
release="$scratch/release"
bundle="$scratch/bundle"
mkdir -p "$release/bin" "$release/ui"

build_go() {
  local module="$1" output="$2" package="$3" ldflags="$4"
  (
    cd "$repo/$module"
    env CGO_ENABLED=0 GOOS="$(go env GOOS)" GOARCH="$(go env GOARCH)" SOURCE_DATE_EPOCH="$source_epoch" \
      GOFLAGS= GOWORK=off GOENV=off GOEXPERIMENT= GOAMD64=v1 GOARM64=v8.0 \
      go build -trimpath -buildvcs=false -mod=readonly -ldflags "$ldflags" -o "$output" "$package"
  )
}

mkdir -p "$scratch/ui-source"
tar -C "$repo/v2/ui" --exclude=node_modules --exclude=dist -cf - . |
  tar -C "$scratch/ui-source" -xf -
(
  cd "$scratch/ui-source"
  SOURCE_DATE_EPOCH="$source_epoch" pnpm install --frozen-lockfile
  SOURCE_DATE_EPOCH="$source_epoch" pnpm build
)
cp -R "$scratch/ui-source/dist/." "$release/ui/"
build_go v2/api "$release/bin/norn-api" . "-buildid= -X main.Version=$version"
build_go v2/api "$release/bin/norn-host-agent" ./cmd/norn-host-agent "-buildid="
build_go v2/api "$release/bin/norn-ingress-observer" ./cmd/norn-ingress-observer "-buildid="
build_go v2/api "$release/bin/norn-ingress-publisher" ./cmd/norn-ingress-publisher "-buildid="
build_go v2/api "$release/bin/norn-effect-runner" ./cmd/norn-effect-runner "-buildid="
build_go v2/cli "$release/bin/norn" . "-buildid= -X norn/v2/cli/cmd.Version=$version"
for helper in host-runtime platform-release-artifact platform-release-fetch-github platform-release-manifest platform-release-verify-github platform-upgrade; do
  install -m 0755 "$repo/v2/scripts/$helper" "$release/bin/$helper"
done
cat > "$release/release.env" <<EOF
NORN_RELEASE_SHA=$sha
NORN_RELEASE_VERSION=$version
NORN_RELEASE_CREATED_AT=$created_at
NORN_UI_DIR=ui
EOF

python3 "$repo/v2/scripts/platform-release-manifest" create \
  --release "$release" --source "$repo" --sha "$sha" --version "$version" \
  --created-at "$created_at" --os "$(go env GOOS)" --arch "$(go env GOARCH)" \
  --go-version "$(go env GOVERSION)" \
  --node-version "$(node --version)" --node-required v24.19.0 \
  --pnpm-version "$(pnpm --version)" --pnpm-required 10.32.1 \
  --go-flag=-trimpath --go-flag=-buildvcs=false --go-flag=-mod=readonly \
  --go-ldflag=-buildid= "--go-ldflag=-X main.Version=$version" \
  "--go-ldflag=-X norn/v2/cli/cmd.Version=$version" \
  --go-environment=GOFLAGS= --go-environment=GOWORK=off --go-environment=GOENV=off \
  --go-environment=GOEXPERIMENT= --go-environment=GOAMD64=v1 --go-environment=GOARM64=v8.0 \
  --cgo-enabled 0 --source-date-epoch "$source_epoch"
chmod -R a-w "$release"
[[ "$(python3 "$repo/v2/scripts/platform-release-manifest" verify --release "$release" --expected-sha "$sha")" == unsigned ]]

SOURCE_DATE_EPOCH="$source_epoch" python3 "$repo/v2/scripts/platform-release-artifact" package \
  --release-dir "$release" --output-dir "$bundle" --commit "$sha" \
  --os "$(go env GOOS)" --arch "$(go env GOARCH)" \
  --repository antiartificial/norn --source-date-epoch "$source_epoch"
openssl_bin="$(command -v openssl)"
if [[ -x /opt/homebrew/opt/openssl@3/bin/openssl ]]; then
  openssl_bin=/opt/homebrew/opt/openssl@3/bin/openssl
fi
"$openssl_bin" genpkey -algorithm ED25519 -out "$scratch/test-private.pem"
"$openssl_bin" pkey -in "$scratch/test-private.pem" -pubout -out "$scratch/test-public.pem"
NORN_RELEASE_SIGNING_KEY_FILE="$scratch/test-private.pem" \
  python3 "$repo/v2/scripts/platform-release-artifact" sign --bundle-dir "$bundle"
python3 "$repo/v2/scripts/platform-release-artifact" verify --bundle-dir "$bundle" --public-key "$scratch/test-public.pem" --quiet
python3 "$repo/v2/scripts/platform-release-artifact" import --bundle-dir "$bundle" \
  --releases-dir "$scratch/imported" --public-key "$scratch/test-public.pem"
[[ "$(python3 "$repo/v2/scripts/platform-release-manifest" verify --release "$scratch/imported/$sha" --expected-sha "$sha")" == signed ]]
NORN_RELEASE_PUBLIC_KEY="$scratch/test-public.pem" \
  "$repo/v2/scripts/platform-release-verify-github" "$scratch/imported/$sha" "$scratch/imported/$sha/release.json"
echo "verified secretless platform bundle and ephemeral signature for $sha"
