package startup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPlatformUpgradeFenceRecognitionDoesNotExecuteManagedBinary(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "legacy_baseline_is_fence_binary() {")
	if start < 0 {
		t.Fatal("could not find fence recognition helper")
	}
	endMarker := "\n}\n\nlegacy_baseline_fence_host_agent()"
	end := strings.Index(text[start:], endMarker)
	if end < 0 {
		t.Fatal("could not isolate fence recognition helper")
	}
	helper := text[start : start+end+3]
	harness := filepath.Join(t.TempDir(), "fence-recognition")
	writeTestScript(t, harness, "#!/usr/bin/env bash\nset -euo pipefail\nrequire_cmd() { command -v \"$1\" >/dev/null; }\n"+helper+"\nlegacy_baseline_is_fence_binary \"$1\"\n")

	exact := filepath.Join(t.TempDir(), "exact-fence")
	writeTestScript(t, exact, "#!/usr/bin/env bash\nprintf \"norn legacy baseline fence active; candidate recovery is required\\n\" >&2\nexit 78\n")
	if out, err := exec.Command(harness, exact).CombinedOutput(); err != nil {
		t.Fatalf("exact fence bytes were rejected: %v\n%s", err, out)
	}

	marker := filepath.Join(t.TempDir(), "executed")
	hanging := filepath.Join(t.TempDir(), "source-host-agent")
	writeTestScript(t, hanging, "#!/usr/bin/env bash\n: > \"$FENCE_EXEC_MARKER\"\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, harness, hanging)
	cmd.Env = append(os.Environ(), "FENCE_EXEC_MARKER="+marker)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("non-fence host agent was accepted: %s", out)
	}
	if ctx.Err() != nil {
		t.Fatal("fence recognition executed and hung the managed host-agent binary")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fence recognition executed the managed host-agent binary: %v", err)
	}

}

func TestPlatformUpgradeQuiescenceRejectsAlreadyOrphanedManagedAgent(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "legacy_baseline_is_fence_binary()")
	if start < 0 {
		t.Fatal("fence helper is missing")
	}
	end := strings.Index(text[start:], "legacy_baseline_fence_host_agent()")
	if end < 0 {
		t.Fatal("quiescence helper boundary is missing")
	}
	helpers := text[start : start+end]
	root := t.TempDir()
	managed := filepath.Join(root, "norn-host-agent")
	nearMatch := managed + "-other"
	orphan := exec.Command("bash", "-c", `exec -a "$1" /bin/sleep 300`, "--", managed)
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	near := exec.Command("bash", "-c", `exec -a "$1" /bin/sleep 300`, "--", nearMatch)
	if err := near.Start(); err != nil {
		_ = orphan.Process.Kill()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = orphan.Process.Kill()
		_ = near.Process.Kill()
		_ = orphan.Wait()
		_ = near.Wait()
	})
	time.Sleep(100 * time.Millisecond)
	if err := exec.Command("kill", "-0", strconv.Itoa(orphan.Process.Pid)).Run(); err != nil {
		t.Fatalf("orphan fixture exited before fence replacement: %v", err)
	}
	fence := "#!/usr/bin/env bash\nprintf \"norn legacy baseline fence active; candidate recovery is required\\n\" >&2\nexit 78\n"
	if err := os.WriteFile(managed, []byte(fence), 0o755); err != nil {
		t.Fatal(err)
	}
	shimDir := filepath.Join(root, "shim")
	if err := os.Mkdir(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestScript(t, filepath.Join(shimDir, "launchctl"), "#!/usr/bin/env bash\nexit 3\n")
	harness := filepath.Join(root, "quiesce")
	writeTestScript(t, harness, "#!/usr/bin/env bash\nset -euo pipefail\nrequire_cmd() { command -v \"$1\" >/dev/null; }\nfail() { printf '%s\\n' \"$1\" >&2; exit 1; }\nhost_agent_label=com.norn.host-agent\n"+helpers+"\nif [[ \"${2:-}\" == scan ]]; then legacy_baseline_host_agent_processes \"$1\"; else legacy_baseline_quiesce_launchd_service com.norn.host-agent /unused \"$1\" /unused test-agent TERM; fi\n")
	scan := exec.Command(harness, managed, "scan")
	scan.Env = append(os.Environ(), "PATH="+shimDir+":"+os.Getenv("PATH"))
	scanOut, scanErr := scan.CombinedOutput()
	if scanErr != nil || !strings.Contains(string(scanOut), strconv.Itoa(orphan.Process.Pid)+":") {
		process, _ := exec.Command("ps", "-o", "pid=,ppid=,command=", "-p", strconv.Itoa(orphan.Process.Pid)).CombinedOutput()
		t.Fatalf("exact process enumeration missed already-orphaned agent: err=%v\nscan=%s\nps=%s", scanErr, scanOut, process)
	}
	cmd := exec.Command(harness, managed)
	cmd.Env = append(os.Environ(), "PATH="+shimDir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), strconv.Itoa(orphan.Process.Pid)+":") {
		t.Fatalf("already-orphaned exact managed agent was not rejected: err=%v\n%s", err, out)
	}
	if strings.Contains(string(out), strconv.Itoa(near.Process.Pid)+":") {
		t.Fatalf("near-match process was mistaken for the managed agent:\n%s", out)
	}
	if err := orphan.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = orphan.Wait()
	cmd = exec.Command(harness, managed)
	cmd.Env = append(os.Environ(), "PATH="+shimDir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("near-match process alone blocked quiescence: %v\n%s", err, out)
	}
}

func TestPlatformUpgradeRecoveredForwardResumeQuiescesLaunchdBeforeContinuing(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "recovered_forward_ensure_host_agent_fenced()")
	if start < 0 {
		t.Fatal("recovered-forward host-agent fence helper is missing")
	}
	end := strings.Index(text[start:], "recovered_forward_resume_or_start()")
	if end < 0 {
		t.Fatal("recovered-forward fence recovery helpers are missing")
	}
	resume := text[start : start+end]
	for _, required := range []string{
		`legacy_baseline_quiesce_launchd_service "$host_agent_label"`,
		`legacy_baseline_quiesce_launchd_service "$launch_label"`,
		`[[ -z "$listener" ]] || fail "recovered-forward API fence is installed but a listener remains"`,
	} {
		if !strings.Contains(resume, required) {
			t.Fatalf("recovered-forward resume is missing fence respawn guard %q", required)
		}
	}
}

func TestPlatformUpgradeValidatesLaunchdPlistBeforeBootstrap(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "legacy_baseline_bootstrap_launchd_service()")
	if start < 0 {
		t.Fatal("launchd bootstrap helper is missing")
	}
	end := strings.Index(text[start:], "legacy_baseline_fence_host_agent()")
	if end < 0 {
		t.Fatal("launchd bootstrap helper is missing")
	}
	helper := text[start : start+end]
	validate := strings.Index(helper, "legacy_baseline_validate_launchd_plist")
	bootstrap := strings.Index(helper, `launchctl bootstrap`)
	if validate < 0 || bootstrap < 0 || validate > bootstrap {
		t.Fatal("launchd plist is not validated before bootstrap executes it")
	}
}

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
		"norn-effect-runner", "norn-ingress-observer", "norn-ingress-publisher", "platform-release-fetch-github", "platform-release-verify-github",
		"mini-create-protected-backup", "mini-verify-protected-backup", "mini-private-copy-rehearsal", "mini-signed-shadow-launchagent-rehearsal",
		"-X main.SourceSHA=$REQUESTED_SHA",
	} {
		if !strings.Contains(string(workflow), required) {
			t.Errorf("signed release workflow is missing %q", required)
		}
	}
	upgrade, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatalf("platform upgrade script is missing: %v", err)
	}
	if !strings.Contains(string(upgrade), "-X main.SourceSHA=$sha") {
		t.Fatal("local platform-upgrade build does not embed the exact candidate source SHA")
	}
	for _, helper := range []string{
		"platform-release-artifact", "platform-release-fetch-github",
		"platform-release-manifest", "platform-release-verify-github",
		"mini-create-protected-backup", "mini-verify-protected-backup",
		"mini-private-copy-rehearsal", "mini-signed-shadow-launchagent-rehearsal",
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
printf '%s\n' '{"name":"norn.startup/v2","schemaModes":["auto","check","migrate-only"],"startupModes":["active","passive"],"passiveRoutes":["/api/health","/api/version","/api/schema"],"schemaContract":{"readerVersion":5,"writerVersion":32,"catalogMigrationVersion":51,"catalogMinimumReaderVersion":5,"catalogMinimumWriterVersion":32}}'
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
# norn.startup/v2 marker is present, but the response is not a valid contract.
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

func TestPlatformUpgradePendingLegacyTransitionRejectsOtherMutationsEarly(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "legacy-fence.json")
	state := map[string]any{
		"schema": "norn.legacy-baseline-fence/v1", "state": "candidate-preservation-pending",
		"legacyReleaseSHA": strings.Repeat("a", 40), "candidateReleaseSHA": strings.Repeat("b", 40),
		"transitionID": "test-transition",
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"upgrade", "rollback", "proxy-switch", "env-exec"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(platformUpgradePath(t), mode)
			cmd.Env = append(os.Environ(), "NORN_LEGACY_FENCE_STATE_PATH="+statePath)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "legacy baseline transition is pending") {
				t.Fatalf("%s was not refused before mutation: err=%v output=%s", mode, err, out)
			}
		})
	}
}

func TestPlatformUpgradeRecoveredBaselineAdoptionIsNarrowAndEarlyGated(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	adopt := strings.Index(text, "recovered-baseline-adopt")
	if adopt < 0 {
		t.Fatal("recovered baseline adoption lane is missing")
	}
	gate := strings.Index(text, "recovered baseline adoption receipt is required and must validate before ordinary forward mutation")
	fetch := strings.LastIndex(text, "fetch_signed_release_if_needed\nbuild_or_reuse_release")
	if gate < 0 || fetch < 0 || gate > fetch {
		t.Fatal("recovered baseline receipt is not validated before ordinary release preparation")
	}
	for _, required := range []string{
		"recovered-baseline-adopt must run from an immutable release under NORN_RELEASES_DIR",
		"recovered-baseline-adopt requires a signed immutable release",
		"recovered-baseline-adopt requires a signed current release",
		"recovered-baseline-adopt requires NORN_RELEASE_VERIFY_HOOK",
		"managed API does not match the signed current release",
		"managed host agent does not match the signed current release",
		"live API source SHA does not match --expected-current-sha",
		"API listener is not owned solely by the LaunchAgent PID",
		"preservationProven\":False",
		"norn.recovered-baseline-adoption/v2\\0",
		"recovered baseline permanently rejects legacy-baseline replay",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("recovered baseline adoption misses guard %q", required)
		}
	}
	legacyGate := text[strings.Index(text, "if [[ \"$legacy_record_state\" == \"candidate-postflight-failed\" ]]"):]
	if !strings.Contains(legacyGate, "recovered_baseline_receipt_validate || fail") || !strings.Contains(legacyGate, "legacy-baseline|legacy-baseline-finalize") {
		t.Fatal("failed legacy fence can bypass the receipt gate or replay the legacy lane")
	}
}

func TestPlatformUpgradeRecoveredBaselineReceiptGatesBeforeReleasePreparation(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	gitShimDir := filepath.Join(root, "shims")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitShimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	writeTestScript(t, filepath.Join(gitShimDir, "git"), fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-C" && "${3:-}" == "describe" ]]; then
  printf 'v3.0.0-rc.1-control\n'
  exit 0
fi
exec %q "$@"
`, gitPath))
	fence := filepath.Join(root, "fence.json")
	ledger := filepath.Join(root, "ledger.json")
	reconciliation := filepath.Join(root, "reconciliation.json")
	for path, value := range map[string]map[string]any{
		fence:          {"schema": "norn.legacy-baseline-fence/v1", "state": "candidate-postflight-failed"},
		ledger:         {"schema": "norn.m5-protected-transition/v1", "state": "candidate-recovery-required"},
		reconciliation: {"schema": "norn.m5-post-transition-reconciliation/v1"},
	} {
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(path, data, 0o600) != nil {
			t.Fatal(err)
		}
	}
	missing := exec.Command(platformUpgradePath(t), "upgrade", "HEAD")
	repo := filepath.Clean(filepath.Join(filepath.Dir(platformUpgradePath(t)), "..", ".."))
	missing.Env = append(os.Environ(), "HOME="+home, "PATH="+gitShimDir+string(os.PathListSeparator)+os.Getenv("PATH"), "NORN_LEGACY_FENCE_STATE_PATH="+fence, "NORN_PLATFORM_REPO="+repo)
	out, err := missing.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "completed recovered-forward file bindings") || strings.Contains(string(out), "building") {
		t.Fatalf("ordinary upgrade was not refused before release preparation: %v %s", err, out)
	}
	preflight := exec.Command(platformUpgradePath(t), "preflight", "HEAD")
	preflight.Env = append(os.Environ(), "PATH="+gitShimDir+string(os.PathListSeparator)+os.Getenv("PATH"), "NORN_LEGACY_FENCE_STATE_PATH="+fence, "NORN_PLATFORM_REPO="+repo)
	out, err = preflight.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "adoption receipt is required") || strings.Contains(string(out), "building") {
		t.Fatalf("missing receipt did not fail evidence preflight: %v %s", err, out)
	}
	legacy := exec.Command(platformUpgradePath(t), "legacy-baseline-finalize", "--transition-id", "x")
	legacy.Env = append(os.Environ(), "NORN_LEGACY_FENCE_STATE_PATH="+fence)
	out, err = legacy.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "permanently rejects legacy-baseline replay") {
		t.Fatalf("legacy replay escaped failed fence: %v %s", err, out)
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
		"acquire_promotion_lock",
		"legacy_baseline_require_drained",
		"legacy_baseline_require_durable_startup_mode",
		"legacy_baseline_fence_host_agent",
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
	for _, required := range []string{"backup artifact must not be empty", "backup proof size does not match the backup artifact", "backup proof digest does not match the backup artifact", "Mini operation drain clear", "legacy service fenced", "candidate postflight failed; legacy binary remains fenced"} {
		if !strings.Contains(text, required) {
			t.Fatalf("legacy baseline is missing fail-closed guard %q", required)
		}
	}
}

func TestPlatformUpgradeLegacyBaselinePreservesFenceUntilExactFinalize(t *testing.T) {
	script, err := os.ReadFile(platformUpgradePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	guard := strings.LastIndex(text, "legacy_baseline_reject_pending_transition")
	fetch := strings.LastIndex(text, "fetch_signed_release_if_needed\nbuild_or_reuse_release")
	if guard < 0 || fetch < 0 || guard > fetch {
		t.Fatal("legacy baseline does not reject an existing transition before artifact preparation")
	}
	promoteStart := strings.Index(text, "promote_legacy_baseline_release()")
	promoteEnd := strings.Index(text[promoteStart:], "legacy_baseline_pending_candidate_sha()")
	if promoteStart < 0 || promoteEnd < 0 || !strings.Contains(text[promoteStart:promoteStart+promoteEnd], "activate_release_artifacts \"$release\" true preserve-host-agent") {
		t.Fatal("legacy baseline promotion can replace the host-agent fence before preservation finalization")
	}
	restartStart := strings.Index(text, "restart_fenced_host_agent()")
	restartEnd := strings.Index(text[restartStart:], "legacy_baseline_fence_service()")
	if restartStart < 0 || restartEnd < 0 {
		t.Fatal("legacy baseline host-agent restart helper is missing")
	}
	restart := text[restartStart : restartStart+restartEnd]
	install := strings.Index(restart, "atomic_install_file \"$candidate_agent\" \"$host_agent_bin\"")
	kickstart := strings.Index(restart, "launchctl kickstart -k")
	if install < 0 || kickstart < 0 || install > kickstart || !strings.Contains(restart, "legacy_baseline_is_fence_binary") {
		t.Fatal("host-agent fence is not verified and replaced only for the exact validated kickstart")
	}
	finalizeStart := strings.Index(text, "finalize_legacy_baseline_release()")
	finalizeEnd := strings.Index(text[finalizeStart:], "atomic_install_file()")
	if finalizeStart < 0 || finalizeEnd < 0 {
		t.Fatal("legacy baseline finalize helper is missing")
	}
	finalize := text[finalizeStart : finalizeStart+finalizeEnd]
	for _, required := range []string{"legacy_baseline_pending_provenance", "NORN_LEGACY_BASELINE_LEGACY_RELEASE_SHA", "NORN_LEGACY_BASELINE_CANDIDATE_SHA"} {
		if !strings.Contains(finalize, required) {
			t.Fatalf("legacy baseline finalize loses exact provenance %q", required)
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
	agentFenced := filepath.Join(fixture.root, "legacy-agent-fenced")
	apiEnv := filepath.Join(fixture.root, "api.env.enc.json")
	if err := os.WriteFile(apiEnv, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	sopsPath := filepath.Join(fixture.shimDir, "sops")
	writeTestScript(t, sopsPath, "#!/bin/sh\nprintf '%s\\n' '{\"NORN_STARTUP_MODE\":\"active\",\"NORN_SCHEMA_MODE\":\"check\"}'\n")
	launcher := writeLauncherContract(t, fixture.root, apiEnv)
	launchPlist := writeLaunchPlist(t, fixture.root, launcher, "active", "auto")
	hostLaunchPlist := writeHostAgentPlist(t, fixture.root, filepath.Join(fixture.root, "host", "platform-upgrade"), filepath.Join(fixture.root, "host", "norn-host-agent"))
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
	managedAgent := filepath.Join(fixture.root, "host", "norn-host-agent")
	sourceAgent := filepath.Join(fixture.previousRelease, "bin", "norn-host-agent")
	agentBody := "#!/usr/bin/env bash\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
	writeTestScript(t, managedAgent, agentBody)
	writeTestScript(t, sourceAgent, agentBody)
	agentChildPIDFile := filepath.Join(fixture.root, "source-host-agent-child-pid")
	agentProcess := exec.Command("bash", "-c", `"$1" & child=$!; printf '%s\n' "$child" > "$2"; wait "$child"`, "--", managedAgent, agentChildPIDFile)
	if err := agentProcess.Start(); err != nil {
		t.Fatal(err)
	}
	var agentChildPID int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		value, readErr := os.ReadFile(agentChildPIDFile)
		if readErr == nil {
			agentChildPID, readErr = strconv.Atoi(strings.TrimSpace(string(value)))
			if readErr == nil {
				break
			}
		}
	}
	if agentChildPID == 0 {
		t.Fatal("source host-agent child did not start")
	}
	agentExited := make(chan struct{})
	go func() {
		_ = agentProcess.Wait()
		close(agentExited)
	}()
	t.Cleanup(func() {
		_ = agentProcess.Process.Kill()
		_ = exec.Command("kill", "-KILL", strconv.Itoa(agentChildPID)).Run()
		<-agentExited
	})
	restartedAgentPID := filepath.Join(fixture.root, "restarted-host-agent-pid")
	restartedAgent := exec.Command("sleep", "300")
	if err := restartedAgent.Start(); err != nil {
		t.Fatal(err)
	}
	restartedAgentExited := make(chan struct{})
	go func() {
		_ = restartedAgent.Wait()
		close(restartedAgentExited)
	}()
	t.Cleanup(func() {
		_ = restartedAgent.Process.Kill()
		<-restartedAgentExited
	})
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  print)
    if [[ "${2:-}" == *com.norn.host-agent ]]; then
      [[ ! -f "$FAKE_HOST_UNLOADED" ]] || exit 3
      if [[ -f "$FAKE_RESTARTED_AGENT_PID" ]]; then pid="$(cat "$FAKE_RESTARTED_AGENT_PID")";
      elif ! kill -0 "$FAKE_HOST_AGENT_PID" >/dev/null 2>&1; then
        if [[ "${FAKE_RESPAWN_FENCE:-0}" == 1 ]]; then pid="$FAKE_FENCE_RESPAWN_PID"; else exit 0; fi
      else pid="$FAKE_HOST_AGENT_PID"; fi
    elif ! kill -0 "$FAKE_LEGACY_PID" >/dev/null 2>&1 && [[ ! -f "$FAKE_ACTIVE_STATE" ]]; then
      if [[ "${FAKE_RESPAWN_API_FENCE:-0}" == 1 && ! -f "$FAKE_API_RESPAWN_SEEN" ]]; then
        : > "$FAKE_API_RESPAWN_SEEN"; pid="$FAKE_API_FENCE_RESPAWN_PID"
      else pid=0; fi
    else pid="$FAKE_LEGACY_PID"; fi
    if [[ "${2:-}" == *com.norn.api ]]; then [[ ! -f "$FAKE_API_UNLOADED" ]] || exit 3; printf 'service = {\n\tpath = %s\n\tpid = %s\n}\n' "$FAKE_PLIST" "$pid";
    else printf 'service = {\n\tpath = %s\n\tpid = %s\n}\n' "$FAKE_HOST_PLIST" "$pid"; fi
    exit 0 ;;
  bootout)
    if [[ "${2:-}" == *com.norn.host-agent ]]; then : > "$FAKE_HOST_UNLOADED"; /bin/kill -TERM "$FAKE_HOST_AGENT_PID";
    else : > "$FAKE_API_UNLOADED"; /bin/kill -KILL "$FAKE_LEGACY_PID"; fi
    exit 0 ;;
  bootstrap)
    plist="${3:-}"
    if [[ "$plist" == "$FAKE_HOST_PLIST" ]]; then rm -f "$FAKE_HOST_UNLOADED"; else rm -f "$FAKE_API_UNLOADED"; fi
    exit 0 ;;
  kill)
    if [[ "${3:-}" == *com.norn.host-agent ]]; then : > "$FAKE_AGENT_FENCED"; /bin/kill -TERM "$FAKE_HOST_AGENT_PID"; else : > "$FAKE_FENCED"; /bin/kill -TERM "$FAKE_LEGACY_PID"; fi
    exit 0 ;;
  kickstart)
    if [[ "${3:-}" == *com.norn.host-agent ]]; then printf '%s\n' "$FAKE_RESTART_AGENT_PID" > "$FAKE_RESTARTED_AGENT_PID";
    else version="$($NORN_BIN_DIR/norn-api --fake-version)"; printf '%s 2\n' "$version" > "$FAKE_ACTIVE_STATE"; fi
    exit 0 ;;
esac
exit 2
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "lsof"), `#!/usr/bin/env bash
set -euo pipefail
if kill -0 "$FAKE_LEGACY_PID" >/dev/null 2>&1; then printf '%s\n' "$FAKE_LEGACY_PID"; fi
`)
	cmd := fixture.command(t)
	cmd.Args = []string{platformUpgradePath(t), "legacy-baseline", "HEAD", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath}
	cmd.Env = append(cmd.Env,
		"NORN_LEGACY_FENCE_STATE_PATH="+fenceState,
		"FAKE_ACTIVE_OPERATIONS_JSON="+queuedFleetDispatchReceiptsJSON(t, 7),
		"FAKE_REQUIRE_AUTH_AFTER_PROMOTION=1",
		"FAKE_FENCED="+fenced,
		"FAKE_AGENT_FENCED="+agentFenced,
		"FAKE_PLIST="+launchPlist,
		"FAKE_HOST_PLIST="+hostLaunchPlist,
		"FAKE_HOST_UNLOADED="+filepath.Join(fixture.root, "host-unloaded"),
		"FAKE_API_UNLOADED="+filepath.Join(fixture.root, "api-unloaded"),
		"NORN_HOST_AGENT_PLIST="+hostLaunchPlist,
		"NORN_API_LAUNCH_PLIST="+launchPlist,
		"NORN_API_LAUNCH_PROGRAM="+launcher,
		"NORN_API_ENV_FILE="+apiEnv,
		"NORN_SOPS_BIN="+sopsPath,
		fmt.Sprintf("FAKE_LEGACY_PID=%d", legacyProcess.Process.Pid),
		fmt.Sprintf("FAKE_HOST_AGENT_PID=%d", agentProcess.Process.Pid),
		fmt.Sprintf("FAKE_RESTART_AGENT_PID=%d", restartedAgent.Process.Pid),
		"FAKE_RESTARTED_AGENT_PID="+restartedAgentPID,
		"FAKE_RESPAWN_FENCE=1",
		"FAKE_FENCE_RESPAWN_PID=999999",
		"FAKE_RESPAWN_API_FENCE=1",
		"FAKE_API_FENCE_RESPAWN_PID=999998",
		"FAKE_API_RESPAWN_SEEN="+filepath.Join(fixture.root, "api-fence-respawn-seen"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy baseline transition failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(fixture.migrationMarker); err != nil {
		t.Fatalf("legacy baseline did not execute migration after fencing: %v\n%s", err, out)
	}
	if err := exec.Command("kill", "-0", fmt.Sprintf("%d", legacyProcess.Process.Pid)).Run(); err == nil {
		t.Fatalf("legacy service survived direct frozen-PID termination:\n%s", out)
	}
	linked, err := os.Readlink(fixture.currentLink)
	if err != nil || linked == fixture.previousRelease {
		t.Fatalf("candidate was not promoted: link=%q err=%v\n%s", linked, err, out)
	}
	state, err := os.ReadFile(fenceState)
	if err != nil || !strings.Contains(string(state), `"state":"candidate-promoted"`) {
		t.Fatalf("legacy fence state was not retained as promoted: %v %s", err, state)
	}
	if !strings.Contains(string(out), "preserving 7 queued Fleet GitHub dispatch receipt(s)") {
		t.Fatalf("Mini transition did not report the preserved Fleet receipts:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "host-unloaded")); !os.IsNotExist(err) {
		t.Fatalf("host-agent launchd job was not bootstrapped after candidate install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "api-unloaded")); !os.IsNotExist(err) {
		t.Fatalf("API launchd job was not bootstrapped after candidate install: %v", err)
	}
	if err := exec.Command("kill", "-0", strconv.Itoa(agentChildPID)).Run(); err == nil {
		t.Fatal("source host-agent child survived launchd bootout")
	}
}

func queuedFleetDispatchReceiptsJSON(t *testing.T, count int) string {
	t.Helper()
	operations := make([]map[string]string, count)
	for i := range operations {
		operations[i] = map[string]string{
			"id": fmt.Sprintf("fleet-dispatch-%d", i+1), "kind": "fleet.github.apply-dispatch", "status": "queued",
		}
	}
	value, err := json.Marshal(map[string]any{"count": count, "operations": operations})
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestPlatformUpgradeLegacyBaselineRejectsSessionOnlyStartupModeBeforeAnyFence(t *testing.T) {
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
	digest := sha256.Sum256(artifact)
	proof, err := json.Marshal(map[string]any{"schema": "norn.legacy-control-backup/v1", "sourceReleaseSHA": legacySHA, "databaseIdentity": fixture.databaseID, "backupSHA256": fmt.Sprintf("%x", digest), "backupBytes": len(artifact), "createdAt": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(fixture.root, "legacy-backup-proof.json")
	if err := os.WriteFile(proofPath, proof, 0o600); err != nil {
		t.Fatal(err)
	}
	apiEnv := filepath.Join(fixture.root, "api.env.enc.json")
	if err := os.WriteFile(apiEnv, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	sopsPath := filepath.Join(fixture.shimDir, "sops")
	writeTestScript(t, sopsPath, "#!/bin/sh\nprintf '{}\\n'\n")
	launcher := writeLauncherContract(t, fixture.root, apiEnv)
	launchPlist := writeLaunchPlist(t, fixture.root, launcher, "active", "auto")
	apiFenced := filepath.Join(fixture.root, "api-fenced")
	agentFenced := filepath.Join(fixture.root, "agent-fenced")
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
case "${1:-}" in
  print) printf 'service = {\n\tpath = %s\n\tpid = 4242\n\tenvironment = {\n\t\tNORN_STARTUP_MODE => active\n\t\tNORN_SCHEMA_MODE => check\n\t}\n}\n' "$FAKE_PLIST"; exit 0 ;;
  kill) if [[ "${3:-}" == *host-agent ]]; then : > "$FAKE_AGENT_FENCED"; else : > "$FAKE_API_FENCED"; fi; exit 0 ;;
  setenv|unsetenv) exit 0 ;;
esac
exit 2
`)
	cmd := fixture.command(t)
	cmd.Args = []string{platformUpgradePath(t), "legacy-baseline", "HEAD", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath}
	cmd.Env = append(cmd.Env, "NORN_LEGACY_FENCE_STATE_PATH="+filepath.Join(fixture.root, "legacy-fence-state.json"), "FAKE_PLIST="+launchPlist, "FAKE_API_FENCED="+apiFenced, "FAKE_AGENT_FENCED="+agentFenced, "NORN_API_ENV_FILE="+apiEnv, "NORN_SOPS_BIN="+sopsPath)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires durable launcher configuration") {
		t.Fatalf("session-only mode was not rejected: err=%v\n%s", err, out)
	}
	for _, path := range []string{apiFenced, agentFenced, filepath.Join(fixture.root, "legacy-fence-state.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("pre-fence rejection mutated %s: %v", path, err)
		}
	}
}

func TestPlatformUpgradeLegacyBaselineFinalizeResumesAfterNormalCandidateBeforeHostAgent(t *testing.T) {
	fixture := newSchemaTransitionFixture(t, 2)
	legacySHA := strings.Repeat("a", 40)
	candidateSHA := strings.Repeat("b", 40)
	if err := os.WriteFile(filepath.Join(fixture.previousRelease, "release.env"), []byte("NORN_RELEASE_SHA="+candidateSHA+"\nNORN_RELEASE_VERSION=v2.21.0-platform\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.root, "target-api-template")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.previousRelease, "bin", "norn-api"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.binDir, "norn-api"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	// A durable host-agent fence is deliberately still in place when the API
	// has consumed its one-process activation token. The first invocation
	// models a crash after normal API verification; the retry must use the
	// persisted phase and must not attempt to release the database fence again.
	fenceState := filepath.Join(fixture.root, "legacy-fence-state.json")
	state, err := json.Marshal(map[string]any{
		"schema": "norn.legacy-baseline-fence/v1", "state": "candidate-normal-activation-pending",
		"legacyReleaseSHA": legacySHA, "candidateReleaseSHA": candidateSHA, "transitionID": "resume-finalize",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fenceState, state, 0o600); err != nil {
		t.Fatal(err)
	}
	fence := "#!/usr/bin/env bash\nprintf \"norn legacy baseline fence active; candidate recovery is required\\n\" >&2\nexit 78\n"
	if err := os.WriteFile(filepath.Join(fixture.root, "host", "norn-host-agent"), []byte(fence), 0o755); err != nil {
		t.Fatal(err)
	}
	hostLaunchPlist := writeHostAgentPlist(t, fixture.root, filepath.Join(fixture.root, "host", "platform-upgrade"), filepath.Join(fixture.root, "host", "norn-host-agent"))
	// No normal API is serving yet. This models a crash after the durable
	// activation-pending phase but before launchctl received its kickstart.
	hostAgent := exec.Command("sleep", "300")
	if err := hostAgent.Start(); err != nil {
		t.Fatal(err)
	}
	hostAgentDone := make(chan struct{})
	go func() {
		_ = hostAgent.Wait()
		close(hostAgentDone)
	}()
	t.Cleanup(func() {
		_ = hostAgent.Process.Kill()
		<-hostAgentDone
	})
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  bootstrap) : > "$FAKE_RESUME_HOST_AGENT_LOADED"; exit 0 ;;
  kickstart)
    if [[ "${*: -1}" == *com.norn.host-agent ]]; then
      : > "$FAKE_RESUME_HOST_AGENT_KICKED"
    else
      version="$($NORN_BIN_DIR/norn-api --fake-version)"
      printf '%s 2\n' "$version" > "$FAKE_ACTIVE_STATE"
    fi
    exit 0 ;;
  print)
    if [[ "${2:-}" == *com.norn.host-agent ]]; then
      [[ -f "$FAKE_RESUME_HOST_AGENT_LOADED" ]] || exit 3
      if [[ -f "$FAKE_RESUME_HOST_AGENT_KICKED" ]]; then
        printf 'service = {\n\tpath = %s\n\tpid = %s\n}\n' "$FAKE_HOST_PLIST" "$FAKE_RESUME_HOST_AGENT_PID"
      else
        printf 'service = {\n\tpath = %s\n\tpid = 99999\n}\n' "$FAKE_HOST_PLIST"
      fi
    else
      printf 'service = {\n\tpid = 4242\n}\n'
    fi
    exit 0 ;;
esac
exit 2
`)
	first := fixture.command(t)
	first.Args = []string{platformUpgradePath(t), "legacy-baseline-finalize", "--transition-id", "resume-finalize"}
	hostKickMarker := filepath.Join(fixture.root, "host-agent-kicked")
	hostLoadedMarker := filepath.Join(fixture.root, "host-agent-loaded")
	commonHostEnv := []string{"NORN_HOST_AGENT_PLIST=" + hostLaunchPlist, "FAKE_HOST_PLIST=" + hostLaunchPlist, "FAKE_RESUME_HOST_AGENT_LOADED=" + hostLoadedMarker}
	first.Env = append(first.Env,
		"NORN_LEGACY_FENCE_STATE_PATH="+fenceState,
		"NORN_M5_FINALIZE_CRASH_AFTER_NORMAL_CANDIDATE=1",
		fmt.Sprintf("FAKE_RESUME_HOST_AGENT_PID=%d", hostAgent.Process.Pid),
		"FAKE_RESUME_HOST_AGENT_KICKED="+hostKickMarker,
	)
	first.Env = append(first.Env, commonHostEnv...)
	out, err := first.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "interrupted after normal candidate verification") {
		t.Fatalf("finalize crash point was not reached: err=%v\n%s", err, out)
	}
	state, err = os.ReadFile(fenceState)
	if err != nil || !strings.Contains(string(state), `"state":"candidate-normal-active-pending-host-agent"`) {
		t.Fatalf("normal candidate phase was not persisted before host-agent restart: %v %s", err, state)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.root, "host", "norn-host-agent")); err != nil || string(got) != fence {
		t.Fatalf("host-agent fence was replaced before the simulated crash: %v %q", err, got)
	}
	wrongSource := fixture.command(t)
	wrongSource.Args = []string{platformUpgradePath(t), "legacy-baseline-finalize", "--transition-id", "resume-finalize"}
	wrongSource.Env = append(wrongSource.Env, "NORN_LEGACY_FENCE_STATE_PATH="+fenceState, "FAKE_ACTIVE_SOURCE_SHA="+strings.Repeat("c", 40), fmt.Sprintf("FAKE_RESUME_HOST_AGENT_PID=%d", hostAgent.Process.Pid), "FAKE_RESUME_HOST_AGENT_KICKED="+hostKickMarker)
	wrongSource.Env = append(wrongSource.Env, commonHostEnv...)
	out, err = wrongSource.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "active source SHA") {
		t.Fatalf("finalize accepted a normal API serving a different candidate source SHA: err=%v\n%s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.root, "host", "norn-host-agent")); err != nil || string(got) != fence {
		t.Fatalf("host-agent fence changed after source SHA mismatch: %v %q", err, got)
	}
	installCrash := fixture.command(t)
	installCrash.Args = []string{platformUpgradePath(t), "legacy-baseline-finalize", "--transition-id", "resume-finalize"}
	installCrash.Env = append(installCrash.Env, "NORN_LEGACY_FENCE_STATE_PATH="+fenceState, "NORN_M5_FINALIZE_CRASH_AFTER_HOST_AGENT_INSTALL=1", fmt.Sprintf("FAKE_RESUME_HOST_AGENT_PID=%d", hostAgent.Process.Pid), "FAKE_RESUME_HOST_AGENT_KICKED="+hostKickMarker)
	installCrash.Env = append(installCrash.Env, commonHostEnv...)
	out, err = installCrash.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "interrupted after candidate host-agent install before kickstart") {
		t.Fatalf("host-agent install crash point was not reached: err=%v\n%s", err, out)
	}
	state, err = os.ReadFile(fenceState)
	if err != nil || !strings.Contains(string(state), `"state":"candidate-normal-active-pending-host-agent-restart"`) {
		t.Fatalf("host-agent restart phase was not persisted before install: %v %s", err, state)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.root, "host", "norn-host-agent")); err != nil || string(got) != "#!/usr/bin/env bash\nexit 0\n" {
		t.Fatalf("candidate host-agent was not installed at the simulated pre-kickstart crash: %v %q", err, got)
	}
	retry := fixture.command(t)
	retry.Args = []string{platformUpgradePath(t), "legacy-baseline-finalize", "--transition-id", "resume-finalize"}
	retry.Env = append(retry.Env, "NORN_LEGACY_FENCE_STATE_PATH="+fenceState, fmt.Sprintf("FAKE_RESUME_HOST_AGENT_PID=%d", hostAgent.Process.Pid), "FAKE_RESUME_HOST_AGENT_KICKED="+hostKickMarker)
	retry.Env = append(retry.Env, commonHostEnv...)
	out, err = retry.CombinedOutput()
	if err != nil {
		t.Fatalf("finalize retry did not resume the exact normal candidate and host-agent restart: %v\n%s", err, out)
	}
	state, err = os.ReadFile(fenceState)
	if err != nil || !strings.Contains(string(state), `"state":"candidate-promoted"`) {
		t.Fatalf("finalize retry did not complete promotion: %v %s", err, state)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.root, "host", "norn-host-agent")); err != nil || string(got) != "#!/usr/bin/env bash\nexit 0\n" {
		t.Fatalf("finalize retry did not install the exact candidate host agent: %v %q", err, got)
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
	agentFenced := filepath.Join(fixture.root, "legacy-agent-fenced")
	apiEnv := filepath.Join(fixture.root, "api.env.enc.json")
	if err := os.WriteFile(apiEnv, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	sopsPath := filepath.Join(fixture.shimDir, "sops")
	writeTestScript(t, sopsPath, "#!/bin/sh\nprintf '{}\\n'\n")
	launcher := writeLauncherContract(t, fixture.root, apiEnv)
	launchPlist := writeLaunchPlist(t, fixture.root, launcher, "active", "check")
	hostLaunchPlist := writeHostAgentPlist(t, fixture.root, filepath.Join(fixture.root, "host", "platform-upgrade"), filepath.Join(fixture.root, "host", "norn-host-agent"))
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
	agentProcess := exec.Command("sleep", "300")
	if err := agentProcess.Start(); err != nil {
		t.Fatal(err)
	}
	agentExited := make(chan struct{})
	go func() { _ = agentProcess.Wait(); close(agentExited) }()
	t.Cleanup(func() { _ = agentProcess.Process.Kill(); <-agentExited })
	writeTestScript(t, filepath.Join(fixture.shimDir, "launchctl"), `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  setenv|unsetenv) exit 0 ;;
  print)
    if [[ "${2:-}" == *com.norn.host-agent ]]; then
      [[ ! -f "$FAKE_HOST_UNLOADED" ]] || exit 3
      ! kill -0 "$FAKE_HOST_AGENT_PID" >/dev/null 2>&1 && exit 3
      printf 'service = {\n\tpath = %s\n\tpid = %s\n}\n' "$FAKE_HOST_PLIST" "$FAKE_HOST_AGENT_PID"; exit 0
    fi
    [[ ! -f "$FAKE_API_UNLOADED" ]] || exit 3
    if ! kill -0 "$FAKE_LEGACY_PID" >/dev/null 2>&1; then printf 'service = {\n\tpath = %s\n\tpid = 0\n}\n' "$FAKE_PLIST"; exit 0; fi
    printf 'service = {\n\tpath = %s\n\tpid = %s\n}\n' "$FAKE_PLIST" "$FAKE_LEGACY_PID"; exit 0 ;;
  bootout)
    if [[ "${2:-}" == *com.norn.host-agent ]]; then : > "$FAKE_HOST_UNLOADED"; /bin/kill -TERM "$FAKE_HOST_AGENT_PID";
    else : > "$FAKE_API_UNLOADED"; /bin/kill -KILL "$FAKE_LEGACY_PID"; fi
    exit 0 ;;
  bootstrap)
    if [[ "${3:-}" == "$FAKE_HOST_PLIST" ]]; then rm -f "$FAKE_HOST_UNLOADED"; else rm -f "$FAKE_API_UNLOADED"; fi
    exit 0 ;;
  kill)
    if [[ "${3:-}" == *com.norn.host-agent ]]; then : > "$FAKE_AGENT_FENCED"; else : > "$FAKE_FENCED"; /bin/kill -TERM "$FAKE_LEGACY_PID"; fi
    exit 0 ;;
  kickstart) printf 'v-broken-postflight 2\n' > "$FAKE_ACTIVE_STATE"; exit 0 ;;
esac
exit 2
`)
	writeTestScript(t, filepath.Join(fixture.shimDir, "lsof"), `#!/usr/bin/env bash
set -euo pipefail
if kill -0 "$FAKE_LEGACY_PID" >/dev/null 2>&1; then printf '%s\n' "$FAKE_LEGACY_PID"; fi
`)
	cmd := fixture.command(t)
	cmd.Args = []string{platformUpgradePath(t), "legacy-baseline", "HEAD", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath}
	cmd.Env = append(cmd.Env, "NORN_LEGACY_FENCE_STATE_PATH="+fenceState, "FAKE_FENCED="+fenced, "FAKE_AGENT_FENCED="+agentFenced, "FAKE_PLIST="+launchPlist, "FAKE_HOST_PLIST="+hostLaunchPlist, "FAKE_HOST_UNLOADED="+filepath.Join(fixture.root, "host-unloaded"), "FAKE_API_UNLOADED="+filepath.Join(fixture.root, "api-unloaded"), "NORN_HOST_AGENT_PLIST="+hostLaunchPlist, "NORN_API_LAUNCH_PLIST="+launchPlist, "NORN_API_LAUNCH_PROGRAM="+launcher, "NORN_API_ENV_FILE="+apiEnv, "NORN_SOPS_BIN="+sopsPath,
		fmt.Sprintf("FAKE_LEGACY_PID=%d", legacyProcess.Process.Pid), fmt.Sprintf("FAKE_HOST_AGENT_PID=%d", agentProcess.Process.Pid))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "candidate postflight failed") {
		t.Fatalf("failed candidate postflight was not reported: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(fixture.migrationMarker); err != nil {
		t.Fatalf("postflight failed before migration: %v\n%s", err, out)
	}
	if err := exec.Command("kill", "-0", fmt.Sprintf("%d", legacyProcess.Process.Pid)).Run(); err == nil {
		t.Fatalf("legacy process survived direct frozen-PID termination:\n%s", out)
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

// A nonzero drain is the last reversible refusal: it must leave both the
// launchd binary and the schema untouched.
func TestPlatformUpgradeLegacyBaselineRejectsActiveOperationsBeforeFence(t *testing.T) {
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
	digest := sha256.Sum256(artifact)
	proofPath := filepath.Join(fixture.root, "legacy-backup-proof.json")
	proof, err := json.Marshal(map[string]any{"schema": "norn.legacy-control-backup/v1", "sourceReleaseSHA": legacySHA, "databaseIdentity": fixture.databaseID, "backupSHA256": fmt.Sprintf("%x", digest), "backupBytes": len(artifact), "createdAt": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proofPath, proof, 0o600); err != nil {
		t.Fatal(err)
	}
	fenceState, fenced := filepath.Join(fixture.root, "legacy-fence-state.json"), filepath.Join(fixture.root, "legacy-fenced")
	truncated := queuedFleetDispatchReceiptsJSON(t, 100)
	for _, test := range []struct {
		name, response, fakeCount, want string
	}{
		{name: "queued Mini operation", fakeCount: "1", want: "requires zero Mini-affecting active operations before fencing"},
		{name: "running Fleet dispatch", response: `{"count":1,"operations":[{"id":"fleet-dispatch-running","kind":"fleet.github.apply-dispatch","status":"running"}]}`, want: "requires zero Mini-affecting active operations before fencing"},
		{name: "malformed JSON", response: `{`, want: "cannot verify the legacy operation drain"},
		{name: "inconsistent count", response: `{"count":2,"operations":[]}`, want: "cannot verify the legacy operation drain"},
		{name: "duplicate IDs", response: `{"count":2,"operations":[{"id":"same","kind":"app.deploy","status":"queued"},{"id":"same","kind":"app.deploy","status":"queued"}]}`, want: "cannot verify the legacy operation drain"},
		{name: "full response page", response: truncated, want: "cannot verify the legacy operation drain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := fixture.command(t)
			cmd.Args = []string{platformUpgradePath(t), "legacy-baseline", "HEAD", "--legacy-release", legacySHA, "--backup-proof", proofPath, "--backup-artifact", artifactPath}
			cmd.Env = append(cmd.Env, "NORN_LEGACY_FENCE_STATE_PATH="+fenceState, "FAKE_FENCED="+fenced)
			if test.fakeCount != "" {
				cmd.Env = append(cmd.Env, "FAKE_ACTIVE_OPERATION_COUNT="+test.fakeCount)
			}
			if test.response != "" {
				cmd.Env = append(cmd.Env, "FAKE_ACTIVE_OPERATIONS_JSON="+test.response)
			}
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), test.want) {
				t.Fatalf("queue fixture did not stop transition: err=%v\n%s", err, out)
			}
			if _, err := os.Stat(fenced); !os.IsNotExist(err) {
				t.Fatalf("legacy was fenced before queue evidence passed: %v", err)
			}
			if _, err := os.Stat(fixture.migrationMarker); !os.IsNotExist(err) {
				t.Fatalf("migration ran before queue evidence passed: %v", err)
			}
			if linked, err := os.Readlink(fixture.currentLink); err != nil || linked != fixture.previousRelease {
				t.Fatalf("current release changed: %q %v", linked, err)
			}
		})
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
# norn.startup/v2 marker is present, but this process never responds.
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
if [[ "${NORN_STARTUP_MODE:-}" == "passive" && ( "${NORN_RECOVERED_FORWARD_MAINTENANCE:-}" == "true" || "${NORN_LEGACY_BASELINE_MAINTENANCE:-}" == "true" ) ]]; then
  echo "stale maintenance mode leaked into candidate" >&2
  exit 31
fi
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
	for _, name := range []string{"norn", "norn-host-agent", "norn-ingress-observer", "norn-ingress-publisher", "norn-effect-runner", "platform-upgrade", "platform-release-manifest", "platform-release-artifact", "platform-release-fetch-github", "platform-release-verify-github", "mini-create-protected-backup", "mini-verify-protected-backup", "mini-private-copy-rehearsal", "mini-protected-transition-rehearsal", "mini-signed-shadow-launchagent-rehearsal", "mini-recovered-forward-retarget", "mini-recovered-forward-retarget-consume", "mini-recovered-forward-transition", "mini-recovered-forward-controller-repair", "mini-recovered-forward-api-launcher", "mini-recovered-forward-verify-evidence", "mini-recovered-forward-validate-journal", "mini-recovered-forward-completed-gate", "mini-recovered-forward-generation-prepare", "mini-recovered-forward-generation-resolve", "host-runtime"} {
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
  */api/version) printf '{"version":"%s","sourceSha":"%s"}\n' "$version" "${FAKE_ACTIVE_SOURCE_SHA:-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}" ;;
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
		"NORN_RECOVERED_FORWARD_MAINTENANCE=true",
		"NORN_LEGACY_BASELINE_MAINTENANCE=true",
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

func writeLauncherContract(t *testing.T, root, envFile string) string {
	t.Helper()
	path := filepath.Join(root, "norn-api-sops-launcher")
	body := "#!/usr/bin/python3\nimport os, subprocess\nenv_file = " + strconv.Quote(envFile) + "\nsubprocess.check_output(['sops', '--decrypt', env_file])\nos.execve('/bin/false', ['/bin/false'], os.environ.copy())\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeLaunchPlist(t *testing.T, root, launcher, startupMode, schemaMode string) string {
	t.Helper()
	path := filepath.Join(root, "com.norn.api.plist")
	environment := ""
	if startupMode != "" {
		environment += "<key>NORN_STARTUP_MODE</key><string>" + startupMode + "</string>"
	}
	if schemaMode != "" {
		environment += "<key>NORN_SCHEMA_MODE</key><string>" + schemaMode + "</string>"
	}
	value := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>com.norn.api</string><key>ProgramArguments</key><array><string>` + launcher + `</string></array><key>EnvironmentVariables</key><dict>` + environment + `</dict></dict></plist>`
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeHostAgentPlist(t *testing.T, root, platformScript, hostAgent string) string {
	t.Helper()
	path := filepath.Join(root, "com.norn.host-agent.plist")
	value := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>com.norn.host-agent</string><key>ProgramArguments</key><array><string>` + platformScript + `</string><string>env-exec</string><string>` + hostAgent + `</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>`
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
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

func TestPlatformUpgradeDrainPreservesQueuedFleetDispatchAndBlocksOtherWork(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		wantError  string
		wantOutput string
	}{
		{
			name:       "queued Fleet dispatch receipt is preserved",
			response:   `{"count":1,"operations":[{"id":"fleet-dispatch-queued","kind":"fleet.github.apply-dispatch","status":"queued"}]}`,
			wantOutput: "Mini operation drain clear; preserving 1 queued Fleet GitHub dispatch receipt(s)",
		},
		{
			name:      "queued Mini operation still blocks",
			response:  `{"count":1,"operations":[{"id":"mini-deploy","kind":"app.deploy","status":"queued"}]}`,
			wantError: "1 Mini-affecting active operation(s)",
		},
		{
			name:      "running Fleet dispatch still blocks",
			response:  `{"count":1,"operations":[{"id":"fleet-dispatch-running","kind":"fleet.github.apply-dispatch","status":"running"}]}`,
			wantError: "1 Mini-affecting active operation(s)",
		},
		{
			name:      "malformed queue evidence fails closed",
			response:  `{`,
			wantError: "cannot verify the Mini operation drain",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSchemaTransitionFixture(t, 2)
			cmd := fixture.command(t)
			cmd.Env = append(cmd.Env, "FAKE_ACTIVE_OPERATIONS_JSON="+test.response)
			out, err := cmd.CombinedOutput()
			if test.wantError != "" {
				if err == nil || !strings.Contains(string(out), test.wantError) {
					t.Fatalf("upgrade did not refuse with %q: err=%v\n%s", test.wantError, err, out)
				}
				if _, statErr := os.Stat(fixture.migrationMarker); !os.IsNotExist(statErr) {
					t.Fatalf("schema migration ran after drain refusal: %v\n%s", statErr, out)
				}
				linked, linkErr := os.Readlink(fixture.currentLink)
				if linkErr != nil || linked != fixture.previousRelease {
					t.Fatalf("current release changed after drain refusal: link=%q err=%v\n%s", linked, linkErr, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Mini upgrade with only a queued Fleet receipt failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), test.wantOutput) {
				t.Fatalf("upgrade did not report preserved Fleet receipt: want %q\n%s", test.wantOutput, out)
			}
			if _, statErr := os.Stat(fixture.migrationMarker); statErr != nil {
				t.Fatalf("Mini upgrade did not migrate after ignoring the queued Fleet receipt: %v\n%s", statErr, out)
			}
		})
	}
}

func TestPlatformUpgradeDrainRejectsUnauthenticatedBeforeMutation(t *testing.T) {
	fixture := newSchemaTransitionFixture(t, 2)
	cmd := fixture.command(t)
	cmd.Env = append(cmd.Env, "FAKE_ACTIVE_OPERATIONS_JSON=not-requested", "NORN_TOKEN=")
	filtered := cmd.Env[:0]
	for _, item := range cmd.Env {
		if !strings.HasPrefix(item, "NORN_API_TOKEN=") {
			filtered = append(filtered, item)
		}
	}
	cmd.Env = filtered
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "authenticated operation-drain token is required") {
		t.Fatalf("upgrade did not refuse without authenticated drain evidence: err=%v\n%s", err, out)
	}
	if _, statErr := os.Stat(fixture.migrationMarker); !os.IsNotExist(statErr) {
		t.Fatalf("schema migration ran without authenticated drain evidence: %v\n%s", statErr, out)
	}
	linked, linkErr := os.Readlink(fixture.currentLink)
	if linkErr != nil || linked != fixture.previousRelease {
		t.Fatalf("current release changed without authenticated drain evidence: link=%q err=%v\n%s", linked, linkErr, out)
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
	writeTestScript(t, filepath.Join(fixture.previousRelease, "bin", "norn-host-agent"), "#!/usr/bin/env bash\nexit 0\n")
	writeTestScript(t, filepath.Join(fixture.binDir, "norn-api"), rollbackBinary)
	writeTestScript(t, filepath.Join(root, "host", "norn-host-agent"), "#!/usr/bin/env bash\nexit 0\n")
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
	    for helper in platform-upgrade platform-release-manifest platform-release-artifact platform-release-fetch-github platform-release-verify-github mini-create-protected-backup mini-verify-protected-backup mini-private-copy-rehearsal mini-protected-transition-rehearsal mini-signed-shadow-launchagent-rehearsal mini-recovered-forward-retarget mini-recovered-forward-retarget-consume mini-recovered-forward-transition mini-recovered-forward-controller-repair mini-recovered-forward-api-launcher mini-recovered-forward-verify-evidence mini-recovered-forward-validate-journal mini-recovered-forward-completed-gate mini-recovered-forward-generation-prepare mini-recovered-forward-generation-resolve host-runtime; do
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
    # The rollback release has its own source identity. A candidate SHA must
    # never be passed while verifying the current rollback target.
    if target.name == 'previous' and '--expected-sha' in sys.argv and option('--expected-sha').name != 'previous': sys.exit(1)
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
  if [[ "${FAKE_REQUIRE_AUTH_AFTER_PROMOTION:-}" == "1" ]]; then
    case "$url" in
      */api/health|*/api/version|*/api/schema)
        [[ "$*" == *"Authorization: Bearer "* ]] || exit 22 ;;
    esac
  fi
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
  */api/version) printf '{"version":"%s","sourceSha":"%s"}\n' "$version" "${FAKE_ACTIVE_SOURCE_SHA:-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}" ;;
  */api/schema)
    printf '{"currentMigrationVersion":%s,"minimumReaderVersion":1,"minimumWriterVersion":%s,"version":"%s","startupContract":"norn.startup/v2","binarySchemaContract":{"readerVersion":1,"writerVersion":%s,"catalogMigrationVersion":%s,"catalogMinimumReaderVersion":1,"catalogMinimumWriterVersion":%s},"processId":4242,"processInstanceId":"11111111-1111-4111-8111-111111111111","databaseIdentity":"%s","startupMode":"active","schemaMode":"check","operationRecoveryEnabled":true,"operationWorkerEnabled":true,"nomadWatcherEnabled":true}\n' "$current_migration" "$minimum_writer" "$version" "$writer" "$catalog" "$minimum_writer" "$FAKE_DATABASE_IDENTITY"
    ;;
  */api/operations/active*)
    if [[ -n "${FAKE_ACTIVE_OPERATIONS_JSON:-}" ]]; then
      printf '%s\n' "$FAKE_ACTIVE_OPERATIONS_JSON"
    else
      count="${FAKE_ACTIVE_OPERATION_COUNT:-0}"
      if [[ "$count" == "0" ]]; then operations='[]'; else operations='[{"id":"active-app-operation","kind":"app.deploy","status":"queued"}]'; fi
      printf '{"count":%s,"operations":%s}\n' "$count" "$operations"
    fi
    ;;
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
		"NORN_HOST_AGENT_DIRECT_EXEC=true",
		"NORN_HOST_CLI_BIN="+filepath.Join(f.root, "host", "norn"),
		"NORN_PLATFORM_SCRIPT_BIN="+filepath.Join(f.root, "host", "platform-upgrade"),
		"NORN_HOST_RUNTIME_BIN="+filepath.Join(f.root, "host", "host-runtime"),
		"NORN_RELEASE_MANIFEST_HELPER="+filepath.Join(f.root, "manifest-helper.py"),
		"FAKE_MANIFEST_HELPER="+filepath.Join(f.root, "manifest-helper.py"),
		"NORN_RELEASE_LOGS_DIR="+filepath.Join(f.root, "release-logs"),
		"NORN_ALLOW_LEGACY_RELEASES=true",
		"NORN_M5_TRANSITION_TEST_HOOKS=1",
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
