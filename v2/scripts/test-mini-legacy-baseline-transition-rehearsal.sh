#!/usr/bin/env bash
set -euo pipefail

# Opt-in, no-provider M5 contract rehearsal against a caller-owned disposable
# PostgreSQL database. It invokes the real platform-upgrade script through
# hermetic fixtures that simulate launchd, API responses, and the candidate.
[[ ${NORN_M5_LEGACY_BASELINE_REHEARSAL:-} == 1 ]] || { echo 'set NORN_M5_LEGACY_BASELINE_REHEARSAL=1' >&2; exit 2; }
[[ -n ${NORN_TEST_DATABASE_URL:-} ]] || { echo 'set NORN_TEST_DATABASE_URL to a disposable PostgreSQL database' >&2; exit 2; }
[[ ${NORN_M5_LEGACY_BASELINE_DB_OWNED:-} == 1 ]] || { echo 'set NORN_M5_LEGACY_BASELINE_DB_OWNED=1 only for a disposable database you own' >&2; exit 2; }
NORN_TEST_DATABASE_URL="$NORN_TEST_DATABASE_URL" python3 - <<'PY'
import os, urllib.parse
p=urllib.parse.urlsplit(os.environ['NORN_TEST_DATABASE_URL'])
if p.scheme not in {'postgres', 'postgresql'} or p.hostname not in {'127.0.0.1', '::1', 'localhost'} or not p.path.lstrip('/').startswith('norn_m5_'):
    raise SystemExit('NORN_TEST_DATABASE_URL must be loopback and name a norn_m5_* disposable database')
PY
receipt=${NORN_M5_LEGACY_BASELINE_RECEIPT:-}
[[ $receipt == /* && ! -e $receipt ]] || { echo 'NORN_M5_LEGACY_BASELINE_RECEIPT must be a new absolute path' >&2; exit 2; }
for tool in go python3 git; do command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 2; }; done
root=$(cd "$(dirname "$0")/../.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/norn-m5-transition.XXXXXX")
store_passed=false startup_passed=false stage=setup
cleanup() { status=$?; rm -rf -- "$scratch"; if [[ -n ${receipt:-} ]]; then ROOT="$root" RECEIPT="$receipt" STATUS="$status" SCRATCH="$scratch" STORE_PASSED="$store_passed" STARTUP_PASSED="$startup_passed" STAGE="$stage" python3 - <<'PY'
import datetime,hashlib,json,os,subprocess,tempfile,urllib.parse
p=urllib.parse.urlsplit(os.environ['NORN_TEST_DATABASE_URL']); safe={'scheme':p.scheme,'host':p.hostname,'port':p.port,'database':p.path.lstrip('/')}
root=os.environ['ROOT']; files=['v2/scripts/test-mini-legacy-baseline-transition-rehearsal.sh','v2/scripts/platform-upgrade','v2/api/startup/platform_upgrade_test.go','v2/api/store/mini_synthetic_upgrade_integration_test.go']
hashes={path:hashlib.sha256(open(os.path.join(root,path),'rb').read()).hexdigest() for path in files}
passed=[]
if os.environ['STORE_PASSED']=='true': passed.append('store/TestSyntheticMiniControlUpgradeAndReaderBoundary')
if os.environ['STARTUP_PASSED']=='true': passed.append('startup/TestPlatformUpgradeLegacyBaseline{FencesExactLegacyBeforeMigrationAndPromotesCandidate,PostflightFailureKeepsLegacyFenced,RejectsActiveOperationsBeforeFence}')
value={'schema':'norn.m5-legacy-baseline-rehearsal/v1','completedAt':datetime.datetime.now(datetime.timezone.utc).isoformat(),'exitCode':int(os.environ['STATUS']),'failureStage':None if os.environ['STATUS']=='0' else os.environ['STAGE'],'source':{'commit':subprocess.check_output(['git','-C',root,'rev-parse','HEAD'],text=True).strip(),'dirty':bool(subprocess.check_output(['git','-C',root,'status','--porcelain'],text=True).strip()),'filesSHA256':hashes},'database':safe,'passedTests':passed,'simulation':{'launchd':True,'apiResponses':True,'candidateRelease':'unsigned fixture'},'cleanup':{'scratchRemoved':not os.path.exists(os.environ['SCRATCH'])}}
fd,tmp=tempfile.mkstemp(prefix='.norn-m5-receipt.',dir=os.path.dirname(os.environ['RECEIPT'])); os.fchmod(fd,0o600)
with os.fdopen(fd,'w',encoding='utf-8') as f: json.dump(value,f,sort_keys=True);f.write('\n')
os.replace(tmp,os.environ['RECEIPT'])
PY
fi; exit "$status"; }
trap cleanup EXIT INT TERM HUP
pushd "$root/v2/api" >/dev/null
stage=store
go test ./store -run '^TestSyntheticMiniControlUpgradeAndReaderBoundary$' -count=1 -v
store_passed=true
stage=startup
go test ./startup -run '^TestPlatformUpgradeLegacyBaseline(FencesExactLegacyBeforeMigrationAndPromotesCandidate|PostflightFailureKeepsLegacyFenced|RejectsActiveOperationsBeforeFence)$' -count=1 -v
startup_passed=true
stage=complete
popd >/dev/null
echo "M5 rehearsal receipt: $receipt"
