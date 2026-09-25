package startup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func platformUpgradePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "platform-upgrade"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "norn-api")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlatformUpgradeStartupContractProbeUsesSanitizedEnvironment(t *testing.T) {
	binary := writeExecutable(t, `#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "--norn-startup-contract" ]]
[[ "${NORN_DATABASE_URL:-}" == *"127.0.0.1:1"* ]]
[[ -z "${NORN_API_TOKEN:-}" ]]
printf '%s\n' '{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":3,"writerVersion":18,"catalogMigrationVersion":21,"catalogMinimumReaderVersion":3,"catalogMinimumWriterVersion":18}}'
`)
	cmd := exec.Command(platformUpgradePath(t), "startup-contract", binary)
	cmd.Env = append(os.Environ(), "NORN_API_TOKEN=must-not-reach-probe")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	var contract Contract
	if err := json.Unmarshal(out, &contract); err != nil {
		t.Fatalf("invalid probe JSON: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(contract, CurrentContract()) {
		t.Fatalf("unexpected probe output: %s", out)
	}
}

func TestPlatformUpgradeRejectsUnverifiedStartupContract(t *testing.T) {
	binary := writeExecutable(t, `#!/usr/bin/env bash
printf '%s\n' '{"name":"legacy"}'
`)
	cmd := exec.Command(platformUpgradePath(t), "startup-contract", binary)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unsupported binary was accepted: %s", out)
	}
	if !strings.Contains(string(out), "refusing to execute it against the control database") {
		t.Fatalf("missing fail-closed guidance: %s", out)
	}
}

func TestPlatformUpgradeRejectsExactContractWithNonzeroExit(t *testing.T) {
	binary := writeExecutable(t, `#!/usr/bin/env bash
printf '%s\n' '{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":1,"writerVersion":3,"catalogMigrationVersion":3,"catalogMinimumReaderVersion":1,"catalogMinimumWriterVersion":3}}'
exit 7
`)
	cmd := exec.Command(platformUpgradePath(t), "startup-contract", binary)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("nonzero capability probe was accepted: %s", out)
	}
}

func TestPlatformUpgradeBoundsContractProbeThatIgnoresTerm(t *testing.T) {
	binary := writeExecutable(t, `#!/usr/bin/env bash
trap '' TERM
while :; do sleep 1; done
`)
	cmd := exec.Command(platformUpgradePath(t), "startup-contract", binary)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("hanging capability probe was accepted: %s", out)
	}
	if !strings.Contains(string(out), "bounded") {
		t.Fatalf("missing bounded-probe failure: %s", out)
	}
}

func TestPlatformUpgradeProxyUpgradeFailsClosed(t *testing.T) {
	cmd := exec.Command(platformUpgradePath(t), "upgrade", "HEAD")
	cmd.Env = append(os.Environ(), "NORN_PLATFORM_UPGRADE_MODE=proxy")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("proxy upgrade unexpectedly proceeded: %s", out)
	}
	if !strings.Contains(string(out), "proxy upgrade handoff is not yet supported") {
		t.Fatalf("missing proxy handoff guidance: %s", out)
	}
}

func TestPlatformUpgradeRollbackExecutesPassiveCheckThenFreshActiveRestart(t *testing.T) {
	root := t.TempDir()
	shimDir := filepath.Join(root, "shims")
	releasesDir := filepath.Join(root, "releases")
	target := filepath.Join(releasesDir, "HEAD")
	previous := filepath.Join(releasesDir, "previous")
	for _, dir := range []string{shimDir, filepath.Join(target, "bin"), filepath.Join(target, "logs"), filepath.Join(previous, "bin"), filepath.Join(root, "bin"), filepath.Join(root, "host")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(root, "runtime-state")
	contract := `{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":1,"writerVersion":3,"catalogMigrationVersion":3,"catalogMinimumReaderVersion":1,"catalogMinimumWriterVersion":3}}`
	apiScript := `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  --norn-startup-contract) printf '%s\n' '` + contract + `'; exit 0 ;;
  --fake-version) printf '%s\n' 'v-target-test'; exit 0 ;;
esac
[[ "${NORN_STARTUP_MODE:-}" == "passive" ]]
printf '%s %s\n' 'v-target-test' 'passive' > "$FAKE_STATE"
trap 'rm -f "$FAKE_STATE"' EXIT
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
	writeTestScript(t, filepath.Join(target, "bin", "norn-api"), apiScript)
	for _, name := range []string{"norn", "norn-host-agent", "platform-upgrade", "host-runtime"} {
		writeTestScript(t, filepath.Join(target, "bin", name), "#!/usr/bin/env bash\nexit 0\n")
	}
	writeTestScript(t, filepath.Join(previous, "bin", "norn-api"), strings.ReplaceAll(apiScript, "v-target-test", "v-previous-test"))
	if err := os.WriteFile(filepath.Join(target, "release.env"), []byte("NORN_RELEASE_VERSION=v-target-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "release.env"), []byte("NORN_RELEASE_VERSION=v-previous-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	currentLink := filepath.Join(root, "current")
	if err := os.Symlink(previous, currentLink); err != nil {
		t.Fatal(err)
	}
	writeTestScript(t, filepath.Join(shimDir, "curl"), `#!/usr/bin/env bash
set -euo pipefail
[[ -f "$FAKE_STATE" ]] || exit 7
read -r version mode < "$FAKE_STATE"
url="${!#}"
case "$url" in
  */api/health) printf '{"status":"%s"}\n' "$mode" ;;
  */api/version) printf '{"version":"%s"}\n' "$version" ;;
  */api/schema)
    if [[ "$mode" == "passive" ]]; then
      printf '{"currentMigrationVersion":1,"startupMode":"passive","schemaMode":"check","operationRecoveryEnabled":false,"operationWorkerEnabled":false,"nomadWatcherEnabled":false}\n'
    else
      printf '{"currentMigrationVersion":1,"startupMode":"active","schemaMode":"check","operationRecoveryEnabled":true,"operationWorkerEnabled":true,"nomadWatcherEnabled":true}\n'
    fi ;;
  *) exit 22 ;;
esac
`)
	writeTestScript(t, filepath.Join(shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  kickstart)
    version="$($NORN_BIN_DIR/norn-api --fake-version)"
    printf '%s active\n' "$version" > "$FAKE_STATE"
    exit 0 ;;
esac
exit 2
`)
	repo := filepath.Clean(filepath.Join(filepath.Dir(platformUpgradePath(t)), "..", ".."))
	cmd := exec.Command(platformUpgradePath(t), "rollback", "HEAD")
	cmd.Env = append(os.Environ(),
		"PATH="+shimDir+":"+os.Getenv("PATH"),
		"FAKE_STATE="+state,
		"NORN_PLATFORM_REPO="+repo,
		"NORN_RELEASES_DIR="+releasesDir,
		"NORN_CURRENT_LINK="+currentLink,
		"NORN_BIN_DIR="+filepath.Join(root, "bin"),
		"NORN_HOST_AGENT_BIN="+filepath.Join(root, "host", "norn-host-agent"),
		"NORN_PLATFORM_SCRIPT_BIN="+filepath.Join(root, "host", "platform-upgrade"),
		"NORN_HOST_RUNTIME_BIN="+filepath.Join(root, "host", "host-runtime"),
		"NORN_DRAIN_MODE=force",
		"NORN_API_BASE=http://127.0.0.1:19999",
		"NORN_CANDIDATE_PORT=19998",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture rollback failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "passive candidate schema check passed") || !strings.Contains(string(out), "upgrade complete: v-target-test") {
		t.Fatalf("rollback did not exercise passive then active sequence:\n%s", out)
	}
	linked, err := os.Readlink(currentLink)
	if err != nil || linked != target {
		t.Fatalf("current link = %q, %v; want %q", linked, err, target)
	}
}

func writeTestScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformUpgradeOrdersMigrationBeforePassiveCheckAndRestart(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	upgradeBranch := text[strings.LastIndex(text, `if [[ "$mode" == "upgrade" ]]`):]
	sequence := []string{"validate_prospective_schema_transition", "drain_check", "validate_current_rollback_target", "run_schema_migration", "candidate_preflight", "stop_candidate", "promote_release"}
	position := -1
	for _, step := range sequence {
		next := strings.Index(upgradeBranch[position+1:], step)
		if next < 0 {
			t.Fatalf("upgrade sequence missing %q", step)
		}
		position += next + 1
	}
	if strings.Count(text, "NORN_STARTUP_MODE=passive") < 2 ||
		strings.Count(text, "NORN_SCHEMA_MODE=check") < 2 ||
		!strings.Contains(text, "launchctl setenv NORN_STARTUP_MODE active") ||
		!strings.Contains(text, "launchctl setenv NORN_SCHEMA_MODE check") {
		t.Fatal("candidate and active restart modes are not explicit")
	}
}

func schemaProbeContract(reader, writer, catalog, minimumReader, minimumWriter int) string {
	return fmt.Sprintf(`{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":%d,"writerVersion":%d,"catalogMigrationVersion":%d,"catalogMinimumReaderVersion":%d,"catalogMinimumWriterVersion":%d}}`, reader, writer, catalog, minimumReader, minimumWriter)
}

func TestPlatformUpgradeRejectsServingWriterIncompatibilityBeforeMigration(t *testing.T) {
	fixture := newSchemaTransitionFixture(t, 1)
	out, err := fixture.command(t).CombinedOutput()
	if err == nil {
		t.Fatalf("incompatible serving writer was accepted:\n%s", out)
	}
	if !strings.Contains(string(out), "actual serving API") &&
		!strings.Contains(string(out), "active binary cannot survive") &&
		!strings.Contains(string(out), "cannot prove every writer") {
		t.Fatalf("missing serving-process compatibility failure:\n%s", out)
	}
	if _, err := os.Stat(fixture.migrationMarker); !os.IsNotExist(err) {
		t.Fatalf("schema migration marker exists after pre-migration rejection: %v", err)
	}
	linked, err := os.Readlink(fixture.currentLink)
	if err != nil || linked != fixture.previousRelease {
		t.Fatalf("current release changed to %q, %v; want %q", linked, err, fixture.previousRelease)
	}
}

func TestPlatformUpgradeCompatibleServingWriterExecutesMigrationAndPromotion(t *testing.T) {
	fixture := newSchemaTransitionFixture(t, 2)
	out, err := fixture.command(t).CombinedOutput()
	if err != nil {
		t.Fatalf("compatible transition failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(fixture.migrationMarker); err != nil {
		t.Fatalf("compatible transition did not execute migrate-only: %v\n%s", err, out)
	}
	linked, err := os.Readlink(fixture.currentLink)
	if err != nil || linked == fixture.previousRelease {
		t.Fatalf("compatible transition was not promoted: link=%q err=%v\n%s", linked, err, out)
	}
	if !strings.Contains(string(out), "does not prove admission quiescence") {
		t.Fatalf("bounded compatibility caveat was not explicit:\n%s", out)
	}
	runner, err := os.Stat(filepath.Join(fixture.binDir, "norn-effect-runner"))
	if err != nil || runner.Mode().Perm() != 0o755 {
		t.Fatalf("effect runner was not installed beside norn-api: %v %v\n%s", runner, err, out)
	}
	staged, _ := filepath.Glob(filepath.Join(fixture.binDir, ".norn-effect-runner.*"))
	if len(staged) != 0 {
		t.Fatalf("staged effect runner left behind: %v", staged)
	}
}

func TestPlatformUpgradeRejectsUnboundServingIdentityBeforeMigration(t *testing.T) {
	t.Run("launchd pid mismatch", func(t *testing.T) {
		fixture := newSchemaTransitionFixture(t, 2)
		writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), "#!/usr/bin/env bash\n[[ \"${1:-}\" == print ]] && printf 'pid = 9999\\n'\n")
		out, err := fixture.command(t).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "not the launchd-supervised") {
			t.Fatalf("unbound serving PID was not rejected: err=%v\n%s", err, out)
		}
		if _, err := os.Stat(fixture.migrationMarker); !os.IsNotExist(err) {
			t.Fatalf("migration ran after PID mismatch: %v", err)
		}
	})

	t.Run("database environment mismatch", func(t *testing.T) {
		fixture := newSchemaTransitionFixture(t, 2)
		fixture.databaseID = "hmac-sha256:" + strings.Repeat("0", 64)
		out, err := fixture.command(t).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "identity/schema contract is unverifiable") {
			t.Fatalf("database identity mismatch was not rejected: err=%v\n%s", err, out)
		}
		if _, err := os.Stat(fixture.migrationMarker); !os.IsNotExist(err) {
			t.Fatalf("migration ran after database identity mismatch: %v", err)
		}
	})
}

type schemaTransitionFixture struct {
	root            string
	repo            string
	shimDir         string
	releasesDir     string
	previousRelease string
	currentLink     string
	binDir          string
	migrationMarker string
	activeState     string
	candidateState  string
	liveWriter      int
	databaseURL     string
	auditKey        string
	databaseID      string
}

func newSchemaTransitionFixture(t *testing.T, liveWriter int) schemaTransitionFixture {
	t.Helper()
	root := t.TempDir()
	fixture := schemaTransitionFixture{
		root: root, repo: filepath.Join(root, "repo"), shimDir: filepath.Join(root, "shims"),
		releasesDir: filepath.Join(root, "releases"), previousRelease: filepath.Join(root, "releases", "previous"),
		currentLink: filepath.Join(root, "current"), binDir: filepath.Join(root, "bin"),
		migrationMarker: filepath.Join(root, "schema-ledger-mutated"), activeState: filepath.Join(root, "active-state"),
		candidateState: filepath.Join(root, "candidate-state"), liveWriter: liveWriter,
		databaseURL: "postgresql://fixture@127.0.0.1:5432/norn_fixture?sslmode=disable",
		auditKey:    strings.Repeat("k", 32),
	}
	mac := hmac.New(sha256.New, []byte(fixture.auditKey))
	_, _ = mac.Write([]byte("norn.database-identity/v1\x00" + fixture.databaseURL))
	fixture.databaseID = fmt.Sprintf("hmac-sha256:%x", mac.Sum(nil))
	for _, dir := range []string{
		fixture.shimDir, filepath.Join(fixture.repo, "v2", "api"), filepath.Join(fixture.repo, "v2", "cli"),
		filepath.Join(fixture.previousRelease, "bin"), fixture.binDir, filepath.Join(root, "host"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targetContract := schemaProbeContract(1, 2, 2, 1, 2)
	rollbackContract := schemaProbeContract(1, 2, 1, 1, 1)
	targetTemplate := fakeSchemaContractBinary("v-target-test", targetContract, true)
	rollbackBinary := fakeSchemaContractBinary("v-current-test", rollbackContract, false)
	targetTemplatePath := filepath.Join(root, "target-api-template")
	writeTestScript(t, targetTemplatePath, targetTemplate)
	writeTestScript(t, filepath.Join(fixture.previousRelease, "bin", "norn-api"), rollbackBinary)
	writeTestScript(t, filepath.Join(fixture.binDir, "norn-api"), rollbackBinary)
	if err := os.WriteFile(filepath.Join(fixture.previousRelease, "release.env"), []byte("NORN_RELEASE_VERSION=v-current-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.previousRelease, fixture.currentLink); err != nil {
		t.Fatal(err)
	}

	writeTestScript(t, filepath.Join(fixture.shimDir, "git"), `#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" rev-parse --is-inside-work-tree "*) printf 'true\n' ;;
  *" rev-parse --verify "*) printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' ;;
  *" describe "*) printf 'v-target-test\n' ;;
  *" worktree add "*)
    worktree="${6}"
    mkdir -p "$worktree/v2/api" "$worktree/v2/cli" "$worktree/v2/scripts"
    printf '#!/usr/bin/env bash\nexit 0\n' > "$worktree/v2/scripts/platform-upgrade"
    printf '#!/usr/bin/env bash\nexit 0\n' > "$worktree/v2/scripts/host-runtime"
    chmod +x "$worktree/v2/scripts/platform-upgrade" "$worktree/v2/scripts/host-runtime"
    ;;
  *" worktree remove "*) ;;
  *) exit 2 ;;
esac
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "go"), `#!/usr/bin/env bash
set -euo pipefail
out=""
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" == "-o" ]]; then out="$2"; shift 2; continue; fi
  shift
done
[[ -n "$out" ]]
mkdir -p "$(dirname "$out")"
if [[ "$(basename "$out")" == "norn-api" ]]; then
  cp "$FAKE_TARGET_API_TEMPLATE" "$out"
else
  printf '#!/usr/bin/env bash\nexit 0\n' > "$out"
  chmod +x "$out"
fi
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "curl"), `#!/usr/bin/env bash
set -euo pipefail
url="${!#}"
if [[ "$url" == *":19998/"* ]]; then
  [[ -f "$FAKE_CANDIDATE_STATE" ]] || exit 7
  read -r version writer < "$FAKE_CANDIDATE_STATE"
  migration=1
  [[ -f "$FAKE_MIGRATION_MARKER" ]] && migration=2
  case "$url" in
    */api/health) printf '{"status":"passive"}\n' ;;
    */api/version) printf '{"version":"%s"}\n' "$version" ;;
    */api/schema) printf '{"currentMigrationVersion":%s,"startupMode":"passive","schemaMode":"check","operationRecoveryEnabled":false,"operationWorkerEnabled":false,"nomadWatcherEnabled":false}\n' "$migration" ;;
    *) exit 22 ;;
  esac
  exit 0
fi
if [[ -f "$FAKE_ACTIVE_STATE" ]]; then
  read -r version writer < "$FAKE_ACTIVE_STATE"
  catalog=2
  minimum_writer=2
  current_migration=2
else
  version='v-current-test'
  writer="$FAKE_LIVE_WRITER"
  catalog=1
  minimum_writer="$FAKE_LIVE_WRITER"
  current_migration="$FAKE_LIVE_WRITER"
fi
case "$url" in
  */api/health) printf '{"status":"ok"}\n' ;;
  */api/version) printf '{"version":"%s"}\n' "$version" ;;
  */api/schema)
    printf '{"currentMigrationVersion":%s,"minimumReaderVersion":1,"minimumWriterVersion":%s,"version":"%s","startupContract":"norn.startup/v2","binarySchemaContract":{"readerVersion":1,"writerVersion":%s,"catalogMigrationVersion":%s,"catalogMinimumReaderVersion":1,"catalogMinimumWriterVersion":%s},"processId":4242,"processInstanceId":"11111111-1111-4111-8111-111111111111","databaseIdentity":"%s","startupMode":"active","schemaMode":"check","operationRecoveryEnabled":true,"operationWorkerEnabled":true,"nomadWatcherEnabled":true}\n' "$current_migration" "$minimum_writer" "$version" "$writer" "$catalog" "$minimum_writer" "$FAKE_DATABASE_IDENTITY"
    ;;
  */api/operations/active*) printf '{"count":0}\n' ;;
  *) exit 22 ;;
esac
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  print) printf 'service = {\n\tpid = 4242\n}\n'; exit 0 ;;
  kickstart)
    version="$($NORN_BIN_DIR/norn-api --fake-version)"
    printf '%s 2\n' "$version" > "$FAKE_ACTIVE_STATE"
    exit 0
    ;;
esac
exit 2
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "lsof"), "#!/usr/bin/env bash\nprintf '4242\\n'\n")
	return fixture
}

func fakeSchemaContractBinary(version, contract string, migrates bool) string {
	migrate := "exit 0"
	if migrates {
		migrate = `: > "$FAKE_MIGRATION_MARKER"; exit 0`
	}
	return `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  --norn-startup-contract) printf '%s\n' '` + contract + `'; exit 0 ;;
  --fake-version) printf '%s\n' '` + version + `'; exit 0 ;;
esac
if [[ "${NORN_SCHEMA_MODE:-}" == "migrate-only" ]]; then ` + migrate + `; fi
[[ "${NORN_STARTUP_MODE:-}" == "passive" ]]
printf '%s 2\n' '` + version + `' > "$FAKE_CANDIDATE_STATE"
trap 'rm -f "$FAKE_CANDIDATE_STATE"' EXIT
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
}

func (f schemaTransitionFixture) command(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(platformUpgradePath(t), "upgrade", "HEAD")
	cmd.Env = append(os.Environ(),
		"PATH="+f.shimDir+":"+os.Getenv("PATH"),
		"NORN_PLATFORM_REPO="+f.repo,
		"NORN_RELEASES_DIR="+f.releasesDir,
		"NORN_CURRENT_LINK="+f.currentLink,
		"NORN_BIN_DIR="+f.binDir,
		"NORN_HOST_AGENT_BIN="+filepath.Join(f.root, "host", "norn-host-agent"),
		"NORN_PLATFORM_SCRIPT_BIN="+filepath.Join(f.root, "host", "platform-upgrade"),
		"NORN_HOST_RUNTIME_BIN="+filepath.Join(f.root, "host", "host-runtime"),
		"NORN_DRAIN_MODE=fail",
		"NORN_API_BASE=http://127.0.0.1:19999",
		"NORN_CANDIDATE_PORT=19998",
		"FAKE_TARGET_API_TEMPLATE="+filepath.Join(f.root, "target-api-template"),
		"FAKE_MIGRATION_MARKER="+f.migrationMarker,
		"FAKE_ACTIVE_STATE="+f.activeState,
		"FAKE_CANDIDATE_STATE="+f.candidateState,
		"FAKE_DATABASE_IDENTITY="+f.databaseID,
		fmt.Sprintf("FAKE_LIVE_WRITER=%d", f.liveWriter),
		"NORN_DATABASE_URL="+f.databaseURL,
		"NORN_AUDIT_SIGNING_KEY="+f.auditKey,
	)
	return cmd
}
