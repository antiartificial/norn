#!/usr/bin/env bash
set -euo pipefail

publisher=$(cd "$(dirname "$0")" && pwd)/publish-work-staging-app-catalog
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
(cd "$(dirname "$0")/../api" && go build -buildvcs=false -o "$temporary/catalog-publisher-validate" ./cmd/catalog-publisher-validate)
export NORN_CATALOG_VALIDATOR="$temporary/catalog-publisher-validate"

run_publisher() {
  local source=$1 output=$2 github_output=$3
  GITHUB_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  GITHUB_RUN_ID=123 GITHUB_RUN_ATTEMPT=2 GITHUB_OUTPUT="$github_output" \
    "$publisher" "$source" "$output"
}

disabled="$temporary/disabled.yaml"
sed 's/deploy: true/deploy: false/' \
  "$(dirname "$0")/../infra/fleet-staging/hello-norn-mysql/infraspec.yaml" > "$disabled"
run_publisher "$disabled" "$temporary/disabled-output" "$temporary/disabled.github-output"
grep -qx 'publish=false' "$temporary/disabled.github-output"
test ! -e "$temporary/disabled-output/catalog.tar.gz"

enabled="$temporary/enabled.yaml"
sed -e 's/deploy: false/deploy: true/' \
  -e 's#https://work-staging-hello-norn-mysql.invalid#https://reviewed.staging.acme.dev#' \
  "$disabled" > "$enabled"
run_publisher "$enabled" "$temporary/first" "$temporary/first.github-output"
run_publisher "$enabled" "$temporary/second" "$temporary/second.github-output"
cmp "$temporary/first/catalog.tar.gz" "$temporary/second/catalog.tar.gz"
grep -qx 'publish=true' "$temporary/first.github-output"
tar -tzf "$temporary/first/catalog.tar.gz" > "$temporary/members"
printf '%s\n' 'hello-norn-mysql/' 'hello-norn-mysql/infraspec.yaml' 'norn-catalog-provenance.json' > "$temporary/expected-members"
cmp "$temporary/expected-members" "$temporary/members"
tar -xOzf "$temporary/first/catalog.tar.gz" norn-catalog-provenance.json | python3 -c '
import json, sys
p = json.load(sys.stdin)
assert p["sourceSHA"] == "a" * 40
assert p["publisherRunID"] == "123"
assert p["publisherRunAttempt"] == "2"
assert p["publisherWorkflow"] == ".github/workflows/norn-app-catalog.yml"
'

placeholder="$temporary/placeholder.yaml"
sed 's/deploy: false/deploy: true/' "$disabled" > "$placeholder"
if run_publisher "$placeholder" "$temporary/placeholder-output" "$temporary/placeholder.github-output"; then
  echo "enabled placeholder hostname was accepted" >&2
  exit 1
fi

for bad_host in 'work.invalid.' localhost. 127.0.0.1 intranet; do
  bad="$temporary/bad-host-${bad_host//[^a-zA-Z0-9]/_}.yaml"
  sed -e 's/deploy: false/deploy: true/' \
    -e "s#https://work-staging-hello-norn-mysql.invalid#https://$bad_host#" "$disabled" > "$bad"
  if run_publisher "$bad" "$temporary/bad-host-output" "$temporary/bad-host.github-output"; then
    echo "placeholder hostname $bad_host was accepted" >&2
    exit 1
  fi
done

unsafe_ingress="$temporary/unsafe-ingress.yaml"
sed -e 's/deploy: false/deploy: true/' \
  -e 's/restrictedIngress:/restrictedIngres:/' \
  -e 's#https://work-staging-hello-norn-mysql.invalid#https://reviewed.staging.acme.dev#' \
  "$disabled" > "$unsafe_ingress"
if run_publisher "$unsafe_ingress" "$temporary/unsafe-output" "$temporary/unsafe.github-output"; then
  echo "misspelled restricted ingress was accepted" >&2
  exit 1
fi

wrong_namespace="$temporary/wrong-namespace.yaml"
sed -e 's/deploy: false/deploy: true/' \
  -e 's/norn-work-staging/other/' \
  -e 's#https://work-staging-hello-norn-mysql.invalid#https://reviewed.staging.acme.dev#' \
  "$disabled" > "$wrong_namespace"
if run_publisher "$wrong_namespace" "$temporary/wrong-output" "$temporary/wrong.github-output"; then
  echo "wrong namespace was accepted" >&2
  exit 1
fi

echo "publisher tests passed"
