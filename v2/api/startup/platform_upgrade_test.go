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
	"time"
)

func platformUpgradePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "platform-upgrade"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlatformReleaseLaneIncludesSignedV3Bundle(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "platform-release.yml"))
	if err != nil {
		t.Fatalf("signed release workflow is missing: %v", err)
	}
	for _, required := range []string{
		"refs/heads/master", "git merge-base --is-ancestor", "platform-release",
		"norn-effect-runner", "platform-release-fetch-github", "platform-release-verify-github",
	} {
		if !strings.Contains(string(workflow), required) {
			t.Errorf("signed release workflow is missing %q", required)
		}
	}
	for _, helper := range []string{
		"platform-release-artifact", "platform-release-fetch-github",
		"platform-release-manifest", "platform-release-verify-github",
	} {
		if _, err := os.Stat(filepath.Join(root, "v2", "scripts", helper)); err != nil {
			t.Errorf("signed release helper %s is missing: %v", helper, err)
		}
	}
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
printf '%s\n' '{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":5,"writerVersion":31,"catalogMigrationVersion":44,"catalogMinimumReaderVersion":5,"catalogMinimumWriterVersion":31}}'
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

func TestPlatformUpgradeLegacyBaselineRequiresBackupProofBeforeBuild(t *testing.T) {
	cmd := exec.Command(platformUpgradePath(t), "legacy-baseline", "--legacy-release", strings.Repeat("a", 40))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires --backup-proof") {
		t.Fatalf("legacy baseline accepted a missing backup proof: err=%v\n%s", err, out)
	}
}

func TestPlatformUpgradeLegacyBaselineRequiresBackupArtifactBeforeBuild(t *testing.T) {
	cmd := exec.Command(platformUpgradePath(t), "legacy-baseline", "--legacy-release", strings.Repeat("a", 40), "--backup-proof", "/private/proof.json")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires --backup-artifact") {
		t.Fatalf("legacy baseline accepted a missing backup artifact: err=%v\n%s", err, out)
	}
}

func TestPlatformUpgradeLegacyBaselineRejectsArtifactDigestBeforeBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"api", "cli"} {
		if err := os.Mkdir(filepath.Join(root, "v2", dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "v2", "Makefile"), []byte("fixture:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", root},
		{"-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"},
		{"-C", root, "tag", "v2.20.0-control"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("create tagged test repository: %v\n%s", err, out)
		}
	}
	legacySHA := strings.Repeat("a", 40)
	databaseURL := "postgresql://fixture@127.0.0.1:5432/norn_fixture?sslmode=disable"
	auditKey := strings.Repeat("k", 32)
	mac := hmac.New(sha256.New, []byte(auditKey))
	_, _ = mac.Write([]byte("norn.database-identity/v1\x00" + databaseURL))
	artifactPath := filepath.Join(root, "control-backup.dump")
	if err := os.WriteFile(artifactPath, []byte("private artifact whose digest differs from proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(root, "backup-proof.json")
	proof, err := json.Marshal(map[string]any{
		"schema":           "norn.legacy-control-backup/v1",
		"sourceReleaseSHA": legacySHA,
		"databaseIdentity": fmt.Sprintf("hmac-sha256:%x", mac.Sum(nil)),
		"backupSHA256":     strings.Repeat("0", 64),
		"backupBytes":      len("private artifact whose digest differs from proof\n"),
		"createdAt":        time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proofPath, proof, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(platformUpgradePath(t), "legacy-baseline", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath)
	cmd.Env = append(os.Environ(), "NORN_PLATFORM_REPO="+root, "NORN_DATABASE_URL="+databaseURL, "NORN_AUDIT_SIGNING_KEY="+auditKey)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "backup proof is invalid") {
		t.Fatalf("legacy baseline accepted a mismatched artifact digest: err=%v\n%s", err, out)
	}
	if strings.Contains(string(out), "building") {
		t.Fatalf("legacy baseline built a release before rejecting the artifact digest:\n%s", out)
	}
}

func TestPlatformUpgradeLegacyBaselineFencesBeforeMigrationAndNeverRestoresLegacy(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	fetch := strings.LastIndex(text, "fetch_signed_release_if_needed\nbuild_or_reuse_release\nprobe_startup_contract")
	if fetch < 0 {
		t.Fatal("platform upgrade no longer verifies or reuses the exact candidate before probing it")
	}
	preflight := strings.LastIndex(text[:fetch], "legacy_baseline_validate_backup_proof")
	if preflight < 0 {
		t.Fatal("legacy baseline does not validate its backup proof before fetching the signed release")
	}
	branch := text[strings.LastIndex(text, `if [[ "$mode" == "legacy-baseline" ]]`):]
	sequence := []string{
		"legacy_baseline_require_drained",
		"legacy_baseline_fence_service",
		"run_schema_migration",
		"candidate_preflight",
		"promote_legacy_baseline_release",
	}
	position := -1
	for _, step := range sequence {
		next := strings.Index(branch[position+1:], step)
		if next < 0 {
			t.Fatalf("legacy baseline sequence missing %q", step)
		}
		position += next + 1
	}
	promoteStart := strings.Index(text, "promote_legacy_baseline_release()")
	promoteEnd := strings.Index(text[promoteStart:], "validate_prospective_schema_transition()")
	if promoteStart < 0 || promoteEnd < 0 || strings.Contains(text[promoteStart:promoteStart+promoteEnd], "restore_release_artifacts") {
		t.Fatal("legacy baseline promotion can restore an unfenced legacy release")
	}
	for _, required := range []string{"backup artifact must not be empty", "backup proof size does not match the backup artifact", "backup proof digest does not match the backup artifact", "legacy operation drain clear", "legacy service fenced", "candidate postflight failed; legacy binary remains fenced"} {
		if !strings.Contains(text, required) {
			t.Fatalf("legacy baseline is missing fail-closed guard %q", required)
		}
	}
}

func TestPlatformUpgradeLegacyBaselineFencesExactLegacyBeforeMigrationAndPromotesCandidate(t *testing.T) {
	fixture := newSchemaTransitionFixture(t, 2)
	legacySHA := strings.Repeat("a", 40)
	if err := os.WriteFile(filepath.Join(fixture.previousRelease, "release.env"), []byte("NORN_RELEASE_SHA="+legacySHA+"\nNORN_RELEASE_VERSION=v2.20.0-platform\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(fixture.root, "legacy-backup-proof.json")
	artifactPath := filepath.Join(fixture.root, "legacy-backup.dump")
	artifact := []byte("private fixture backup artifact\n")
	if err := os.WriteFile(artifactPath, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactDigest := sha256.Sum256(artifact)
	proof, err := json.Marshal(map[string]any{
		"schema":           "norn.legacy-control-backup/v1",
		"sourceReleaseSHA": legacySHA,
		"databaseIdentity": fixture.databaseID,
		"backupSHA256":     fmt.Sprintf("%x", artifactDigest),
		"backupBytes":      len(artifact),
		"createdAt":        time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proofPath, proof, 0o600); err != nil {
		t.Fatal(err)
	}
	fenceState := filepath.Join(fixture.root, "legacy-fence-state.json")
	fenced := filepath.Join(fixture.root, "legacy-fenced")
	legacyProcess := exec.Command("sleep", "300")
	if err := legacyProcess.Start(); err != nil {
		t.Fatal(err)
	}
	legacyExited := make(chan struct{})
	go func() {
		_ = legacyProcess.Wait()
		close(legacyExited)
	}()
	t.Cleanup(func() {
		_ = legacyProcess.Process.Kill()
		<-legacyExited
	})
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  print) printf 'service = {\n\tpid = %s\n}\n' "$FAKE_LEGACY_PID"; exit 0 ;;
  kill) : > "$FAKE_FENCED"; /bin/kill -TERM "$FAKE_LEGACY_PID"; exit 0 ;;
  kickstart)
    version="$($NORN_BIN_DIR/norn-api --fake-version)"
    printf '%s 2\n' "$version" > "$FAKE_ACTIVE_STATE"
    exit 0 ;;
esac
exit 2
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "lsof"), `#!/usr/bin/env bash
set -euo pipefail
if [[ -f "${FAKE_FENCED:-}" ]]; then exit 0; fi
printf '%s\n' "$FAKE_LEGACY_PID"
`)
	cmd := fixture.command(t)
	cmd.Args = []string{platformUpgradePath(t), "legacy-baseline", "HEAD", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath}
	cmd.Env = append(cmd.Env,
		"NORN_LEGACY_FENCE_STATE_PATH="+fenceState,
		"FAKE_FENCED="+fenced,
		fmt.Sprintf("FAKE_LEGACY_PID=%d", legacyProcess.Process.Pid),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy baseline transition failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(fixture.migrationMarker); err != nil {
		t.Fatalf("legacy baseline did not execute migration after fencing: %v\n%s", err, out)
	}
	if _, err := os.Stat(fenced); err != nil {
		t.Fatalf("legacy service was not fenced: %v\n%s", err, out)
	}
	linked, err := os.Readlink(fixture.currentLink)
	if err != nil || linked == fixture.previousRelease {
		t.Fatalf("candidate was not promoted: link=%q err=%v\n%s", linked, err, out)
	}
	state, err := os.ReadFile(fenceState)
	if err != nil || !strings.Contains(string(state), `"state":"candidate-promoted"`) {
		t.Fatalf("legacy fence state was not retained as promoted: %v %s", err, state)
	}
}

func TestPlatformUpgradeLegacyBaselinePostflightFailureKeepsLegacyFenced(t *testing.T) {
	fixture := newSchemaTransitionFixture(t, 2)
	legacySHA := strings.Repeat("a", 40)
	if err := os.WriteFile(filepath.Join(fixture.previousRelease, "release.env"), []byte("NORN_RELEASE_SHA="+legacySHA+"\nNORN_RELEASE_VERSION=v2.20.0-platform\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("private fixture backup artifact\n")
	artifactPath := filepath.Join(fixture.root, "legacy-backup.dump")
	if err := os.WriteFile(artifactPath, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactDigest := sha256.Sum256(artifact)
	proof, err := json.Marshal(map[string]any{
		"schema": "norn.legacy-control-backup/v1", "sourceReleaseSHA": legacySHA,
		"databaseIdentity": fixture.databaseID, "backupSHA256": fmt.Sprintf("%x", artifactDigest),
		"backupBytes": len(artifact), "createdAt": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(fixture.root, "legacy-backup-proof.json")
	if err := os.WriteFile(proofPath, proof, 0o600); err != nil {
		t.Fatal(err)
	}
	fenceState := filepath.Join(fixture.root, "legacy-fence-state.json")
	fenced := filepath.Join(fixture.root, "legacy-fenced")
	legacyProcess := exec.Command("sleep", "300")
	if err := legacyProcess.Start(); err != nil {
		t.Fatal(err)
	}
	legacyExited := make(chan struct{})
	go func() {
		_ = legacyProcess.Wait()
		close(legacyExited)
	}()
	t.Cleanup(func() {
		_ = legacyProcess.Process.Kill()
		<-legacyExited
	})
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  print) printf 'service = {\n\tpid = %s\n}\n' "$FAKE_LEGACY_PID"; exit 0 ;;
  kill) : > "$FAKE_FENCED"; /bin/kill -TERM "$FAKE_LEGACY_PID"; exit 0 ;;
  kickstart) printf 'v-broken-postflight 2\n' > "$FAKE_ACTIVE_STATE"; exit 0 ;;
esac
exit 2
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "lsof"), `#!/usr/bin/env bash
set -euo pipefail
if [[ -f "${FAKE_FENCED:-}" ]]; then exit 0; fi
printf '%s\n' "$FAKE_LEGACY_PID"
`)
	cmd := fixture.command(t)
	cmd.Args = []string{platformUpgradePath(t), "legacy-baseline", "HEAD", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath}
	cmd.Env = append(cmd.Env, "NORN_LEGACY_FENCE_STATE_PATH="+fenceState, "FAKE_FENCED="+fenced,
		fmt.Sprintf("FAKE_LEGACY_PID=%d", legacyProcess.Process.Pid))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "candidate postflight failed") {
		t.Fatalf("failed candidate postflight was not reported: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(fixture.migrationMarker); err != nil {
		t.Fatalf("postflight failed before migration: %v\n%s", err, out)
	}
	if _, err := os.Stat(fenced); err != nil {
		t.Fatalf("legacy process was not fenced: %v\n%s", err, out)
	}
	state, err := os.ReadFile(fenceState)
	if err != nil || !strings.Contains(string(state), `"state":"candidate-postflight-failed"`) {
		t.Fatalf("failed postflight did not retain fence state: %v %s", err, state)
	}
	installed, err := os.ReadFile(filepath.Join(fixture.binDir, "norn-api"))
	if err != nil || !strings.Contains(string(installed), "v2.21.0-platform") || strings.Contains(string(installed), "v2.20.0-platform") {
		t.Fatalf("legacy binary was restored after migration: %v\n%s", err, installed)
	}
	linked, err := os.Readlink(fixture.currentLink)
	if err != nil || linked == fixture.previousRelease {
		t.Fatalf("legacy release link was restored after migration: %q, %v", linked, err)
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
	targetSHA := strings.Repeat("a", 40)
	target := filepath.Join(releasesDir, targetSHA)
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
  --fake-version) printf '%s\n' 'v2.21.0-platform'; exit 0 ;;
esac
[[ "${NORN_STARTUP_MODE:-}" == "passive" ]]
printf '%s %s\n' 'v2.21.0-platform' 'passive' > "$FAKE_STATE"
trap 'rm -f "$FAKE_STATE"' EXIT
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
	writeTestScript(t, filepath.Join(target, "bin", "norn-api"), apiScript)
	for _, name := range []string{"norn", "norn-host-agent", "norn-effect-runner", "platform-upgrade", "platform-release-manifest", "platform-release-artifact", "platform-release-fetch-github", "platform-release-verify-github", "host-runtime"} {
		writeTestScript(t, filepath.Join(target, "bin", name), "#!/usr/bin/env bash\nexit 0\n")
	}
	writeTestScript(t, filepath.Join(previous, "bin", "norn-api"), strings.ReplaceAll(apiScript, "v2.21.0-platform", "v2.19.0-platform"))
	writeTestScript(t, filepath.Join(previous, "bin", "norn"), "#!/usr/bin/env bash\nexit 0\n")
	if err := os.WriteFile(filepath.Join(target, "release.env"), []byte("NORN_RELEASE_VERSION=v2.21.0-platform\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "release.env"), []byte("NORN_RELEASE_VERSION=v2.19.0-platform\n"), 0o644); err != nil {
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
	cmd := exec.Command(platformUpgradePath(t), "rollback", targetSHA)
	cmd.Env = append(os.Environ(),
		"PATH="+shimDir+":"+os.Getenv("PATH"),
		"FAKE_STATE="+state,
		"NORN_PLATFORM_REPO="+repo,
		"NORN_RELEASES_DIR="+releasesDir,
		"NORN_CURRENT_LINK="+currentLink,
		"NORN_BIN_DIR="+filepath.Join(root, "bin"),
		"NORN_HOST_AGENT_BIN="+filepath.Join(root, "host", "norn-host-agent"),
		"NORN_HOST_CLI_BIN="+filepath.Join(root, "host", "norn"),
		"NORN_PLATFORM_SCRIPT_BIN="+filepath.Join(root, "host", "platform-upgrade"),
		"NORN_HOST_RUNTIME_BIN="+filepath.Join(root, "host", "host-runtime"),
		"NORN_RELEASE_LOGS_DIR="+filepath.Join(root, "release-logs"),
		"NORN_ALLOW_LEGACY_RELEASES=true",
		"NORN_DRAIN_MODE=force",
		"NORN_API_BASE=http://127.0.0.1:19999",
		"NORN_CANDIDATE_PORT=19998",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture rollback failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "passive candidate schema check passed") || !strings.Contains(string(out), "upgrade complete: v2.21.0-platform") {
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
	t.Cleanup(func() {
		_ = filepath.Walk(filepath.Join(root, "releases"), func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
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
	targetTemplate := fakeSchemaContractBinary("v2.21.0-platform", targetContract, true)
	rollbackBinary := fakeSchemaContractBinary("v2.20.0-platform", rollbackContract, false)
	targetTemplatePath := filepath.Join(root, "target-api-template")
	writeTestScript(t, targetTemplatePath, targetTemplate)
	writeTestScript(t, filepath.Join(fixture.previousRelease, "bin", "norn-api"), rollbackBinary)
	writeTestScript(t, filepath.Join(fixture.previousRelease, "bin", "norn"), "#!/usr/bin/env bash\nexit 0\n")
	writeTestScript(t, filepath.Join(fixture.binDir, "norn-api"), rollbackBinary)
	if err := os.WriteFile(filepath.Join(fixture.previousRelease, "release.env"), []byte("NORN_RELEASE_VERSION=v2.20.0-platform\n"), 0o644); err != nil {
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
  *" describe "*) printf 'v2.21.0-platform\n' ;;
  *" show -s --format=%ct "*) printf '1780000000\n' ;;
  *" show -s --format=%cI "*) printf '2026-05-26T00:00:00Z\n' ;;
  *" worktree add "*)
    worktree="${6}"
    mkdir -p "$worktree/v2/api" "$worktree/v2/cli" "$worktree/v2/scripts"
    for helper in platform-upgrade platform-release-manifest platform-release-artifact platform-release-fetch-github platform-release-verify-github host-runtime; do
      printf '#!/usr/bin/env bash\nexit 0\n' > "$worktree/v2/scripts/$helper"
      chmod +x "$worktree/v2/scripts/$helper"
    done
    cp "$FAKE_MANIFEST_HELPER" "$worktree/v2/scripts/platform-release-manifest"
    ;;
  *" worktree remove "*) ;;
  *) exit 2 ;;
esac
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "go"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == env ]]; then
  case "${2:-}" in
    GOVERSION) printf 'go1.26.1\n' ;;
    GOOS) printf 'darwin\n' ;;
    GOARCH) printf 'arm64\n' ;;
    *) exit 2 ;;
  esac
  exit 0
fi
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
	writeTestScript(t, filepath.Join(root, "manifest-helper.py"), `#!/usr/bin/env python3
import json, shutil, sys
from pathlib import Path
mode = sys.argv[1]
def option(name): return Path(sys.argv[sys.argv.index(name)+1])
if mode == 'create':
    target = option('--release')
    (target/'release.json').write_text(json.dumps({'version':'v2.21.0-platform'}))
    (target/'release.signature.json').write_text('{}')
elif mode == 'verify':
    target = option('--release')
    if not (target/'release.json').is_file(): sys.exit(1)
    print('unsigned')
elif mode == 'install':
    shutil.copytree(option('--staging'), option('--destination'))
elif mode == 'compare':
    pass
else: sys.exit(2)
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
  version='v2.20.0-platform'
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
		"NORN_HOST_CLI_BIN="+filepath.Join(f.root, "host", "norn"),
		"NORN_PLATFORM_SCRIPT_BIN="+filepath.Join(f.root, "host", "platform-upgrade"),
		"NORN_HOST_RUNTIME_BIN="+filepath.Join(f.root, "host", "host-runtime"),
		"NORN_RELEASE_MANIFEST_HELPER="+filepath.Join(f.root, "manifest-helper.py"),
		"FAKE_MANIFEST_HELPER="+filepath.Join(f.root, "manifest-helper.py"),
		"NORN_RELEASE_LOGS_DIR="+filepath.Join(f.root, "release-logs"),
		"NORN_ALLOW_LEGACY_RELEASES=true",
		"NORN_DRAIN_MODE=fail",
		"NORN_TOKEN=test-drain-token",
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
