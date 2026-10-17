package executor_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	. "norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return "untyped"
	}
	return ""
}

func validStart(plan string) AttemptStartRequest {
	inv := strings.Repeat("ab", 16)
	sha := strings.Repeat("c", 40)
	return AttemptStartRequest{
		SchemaVersion: AttemptSchemaVersion, RunnerAttemptID: RunnerAttemptID("424242", inv, plan),
		CommitSHA: sha, PlanSHA256: strings.Repeat("e", 64), WorkflowURL: WorkflowURL("424242", inv),
		Executor: AttemptMetadata{Lane: "executor", DropletID: "424242", MachineID: "m", BootID: "b", InvocationID: inv,
			LoopRelease: "r", CommitSHA: sha, WheelhouseSHA256: strings.Repeat("d", 64), TofuVersion: "1.9.0"},
	}
}

func TestAttemptSchemaVersionMatchesModel(t *testing.T) {
	if AttemptSchemaVersion != model.FleetRunnerAttemptSchemaVersion {
		t.Fatal("executor start schemaVersion must equal the runner-attempt schema version")
	}
}

func TestAttemptIdentityRoundTripAndMetadata(t *testing.T) {
	plan := uuid.NewString()
	r := validStart(plan)
	d, i, p, ok := ParseRunnerAttemptID(r.RunnerAttemptID)
	if !ok || d != "424242" || i != r.Executor.InvocationID || p != plan {
		t.Fatalf("round trip failed: %v %v %v %v", d, i, p, ok)
	}
	if err := r.Validate(plan); err != nil {
		t.Fatal(err)
	}
	if code(r.Validate(uuid.NewString())) != CodeInvalidFleetExecutorRequest {
		t.Fatal("attempt ID for another plan must fail")
	}
	bad := r
	bad.Executor.DropletID = "1"
	if code(bad.Validate(plan)) != CodeInvalidFleetExecutorRequest {
		t.Fatal("droplet mismatch must fail")
	}
	if PrincipalSubject("dev-1") != "managed-device:dev-1" {
		t.Fatal("principal subject must name the device")
	}
}

// TestLaneIdentifiersAreDisjoint pins item 2 of the E0 review: executor
// identifiers fail every exported GitHub-lane check, and GitHub-lane
// identifiers fail the executor validator, so neither lane's validator needs
// an exemption to admit its own lane.
func TestLaneIdentifiersAreDisjoint(t *testing.T) {
	plan := uuid.NewString()
	r := validStart(plan)

	// Executor -> GitHub-lane checks.
	if parsed, err := url.Parse(r.WorkflowURL); err != nil || parsed.Scheme == "https" {
		t.Fatal("executor workflowUrl must not be https (validOptionalFleetWorkflowURL and the acceptance check require https)")
	}
	evidence := lifecycle.StopEvidence{PredecessorID: plan, SourceDispatchRunID: "0", RunAttempt: 1, WorkflowURL: r.WorkflowURL,
		Status: "completed", Conclusion: "failure", ObservedAt: time.Now()}
	if lifecycle.ValidStopEvidence(evidence, plan, "0", time.Now()) {
		t.Fatal("GitHub stop evidence must never accept an executor workflowUrl")
	}
	if strings.HasPrefix(r.RunnerAttemptID, "github-actions:") {
		t.Fatal("executor attempt IDs must not share the GitHub prefix")
	}

	// GitHub -> executor validator.
	if _, _, _, ok := ParseRunnerAttemptID("github-actions:o/r:1:1"); ok {
		t.Fatal("GitHub attempt ID parsed as executor ID")
	}
	https := r
	https.WorkflowURL = "https://github.com/o/r/actions/runs/1"
	if https.Validate(plan) == nil {
		t.Fatal("https workflowUrl must not validate on the executor lane")
	}
	for _, field := range []string{`"dispatchNonce":"` + strings.Repeat("a", 64) + `"`, `"sourceDispatchRunId":7`, `"pilotRunId":""`} {
		body, _ := json.Marshal(r)
		body = append(body[:len(body)-1], []byte(","+field+"}")...)
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		var decoded AttemptStartRequest
		if decoder.Decode(&decoded) == nil {
			t.Fatalf("executor start body must refuse GitHub-lane field %s", field)
		}
	}
}

func TestHolderNonceIsStableHex(t *testing.T) {
	a, b := HolderNonceSHA256("p1"), HolderNonceSHA256("p1")
	if a != b || len(a) != 64 || a == HolderNonceSHA256("p2") {
		t.Fatalf("holder nonce not stable/distinct: %s", a)
	}
}

// TestGrantPhasesAreRealLifecyclePhases pins item 3: every grant phase other
// than plan, preflight and recover is a real phase of the profile's list, and every
// phase with work has a row.
func TestGrantPhasesAreRealLifecyclePhases(t *testing.T) {
	for profile, lp := range map[GrantProfile]lifecycle.Profile{
		GrantProfilePostgresLegacyNoDrain: lifecycle.PostgresLegacy,
		GrantProfileV3:                    lifecycle.V3,
	} {
		real := map[string]bool{}
		for _, phase := range lifecycle.Phases(lp, false) {
			real[phase] = true
			if _, ok := GrantTypes[profile][phase]; !ok && phase != "complete" {
				t.Errorf("%s: phase %s has no grant row", profile, phase)
			}
		}
		for phase := range GrantTypes[profile] {
			if phase != GrantPhasePlan && phase != GrantPhasePreflight && phase != GrantPhaseRecover && (!real[phase] || phase == "complete") {
				t.Errorf("%s: grant phase %s is not a working lifecycle phase", profile, phase)
			}
		}
	}
}

func TestGrantTypeTable(t *testing.T) {
	legacy, v3 := GrantProfilePostgresLegacyNoDrain, GrantProfileV3
	id := uuid.NewString()
	cases := []struct {
		profile GrantProfile
		req     CredentialGrantRequest
		code    string
	}{
		{legacy, CredentialGrantRequest{Phase: "plan", Types: []string{"do-read", "spaces-rw"}}, ""},
		{legacy, CredentialGrantRequest{Phase: "plan", Types: []string{"do-write"}}, CodeFleetCredentialTypeNotAllowed},
		{legacy, CredentialGrantRequest{Phase: "plan", AttemptID: id, Types: []string{"do-read"}}, CodeInvalidFleetExecutorRequest},
		{legacy, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredBootstrapDir, CredSSHNode}}, CodeInvalidFleetExecutorRequest},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, Types: []string{CredBootstrapDir, CredSSHNode}}, CodeFleetCredentialGrantAttemptRequired},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredBootstrapDir, CredSSHNode}}, ""},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredDOWrite}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredDORead}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredSpacesRW}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredSpacesRead}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredReleaseGitHub}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredNornAttempt}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: GrantPhasePreflight, AttemptID: id, Types: []string{CredRepoRead}}, CodeFleetCredentialTypeNotAllowed},
		{legacy, CredentialGrantRequest{Phase: "infrastructure_applied", Types: []string{"do-write"}}, CodeFleetCredentialGrantAttemptRequired},
		{legacy, CredentialGrantRequest{Phase: "infrastructure_applied", AttemptID: id, Types: []string{"do-write", "spaces-rw", "norn-attempt"}}, ""},
		{legacy, CredentialGrantRequest{Phase: "provider_applying", AttemptID: id, Types: []string{"do-write"}}, CodeInvalidFleetExecutorRequest},
		{v3, CredentialGrantRequest{Phase: "provider_applying", AttemptID: id, Types: []string{"do-write", "spaces-rw", "norn-attempt"}}, ""},
		{v3, CredentialGrantRequest{Phase: "prechange_verified", AttemptID: id, Types: []string{"do-write"}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: "infrastructure_applied", AttemptID: id, Types: []string{"do-write"}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: "nodes_configured", AttemptID: id, Types: []string{"do-read"}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: "nodes_configured", AttemptID: id, Types: []string{"ssh-node", "bootstrap-dir"}}, ""},
		{v3, CredentialGrantRequest{Phase: "complete", AttemptID: id, Types: []string{"norn-attempt"}}, CodeInvalidFleetExecutorRequest},
		{legacy, CredentialGrantRequest{Phase: "recover", AttemptID: id, Types: []string{"do-write", "ssh-node"}}, ""},
		{legacy, CredentialGrantRequest{Phase: "plan"}, CodeInvalidFleetExecutorRequest},
		{legacy, CredentialGrantRequest{Phase: "plan", Types: []string{"do-read", "repo-read"}}, ""},
		{v3, CredentialGrantRequest{Phase: "plan", Types: []string{"repo-read"}}, ""},
		{v3, CredentialGrantRequest{Phase: "recover", AttemptID: id, Types: []string{"repo-read", "norn-attempt"}}, ""},
		{v3, CredentialGrantRequest{Phase: "nodes_configured", AttemptID: id, Types: []string{"repo-read"}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: "provider_applying", AttemptID: id, Types: []string{"repo-read"}}, CodeFleetCredentialTypeNotAllowed},
		{v3, CredentialGrantRequest{Phase: "prechange_verified", AttemptID: id, Types: []string{"repo-read"}}, CodeFleetCredentialTypeNotAllowed},
		{legacy, CredentialGrantRequest{Phase: "infrastructure_applied", AttemptID: id, Types: []string{"repo-read"}}, CodeFleetCredentialTypeNotAllowed},
	}
	for i, c := range cases {
		if got := code(c.req.Validate(c.profile)); got != c.code {
			t.Errorf("case %d: code %q, want %q", i, got, c.code)
		}
	}
	n := CredentialGrantRequest{Types: []string{"b", "a", "b"}}.Normalize()
	if strings.Join(n.Types, ",") != "a,b" {
		t.Fatalf("normalize = %v", n.Types)
	}
}

func TestPreflightCompleteRequestValidation(t *testing.T) {
	id := uuid.NewString()
	good := PreflightCompleteRequest{AttemptID: id, GrantID: uuid.NewString(), EvidenceDigest: "sha256:" + strings.Repeat("a", 64)}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid non-secret receipt digest: %v", err)
	}
	for _, bad := range []PreflightCompleteRequest{
		{GrantID: good.GrantID, EvidenceDigest: good.EvidenceDigest},
		{AttemptID: good.AttemptID, EvidenceDigest: good.EvidenceDigest},
		{AttemptID: good.AttemptID, GrantID: good.GrantID, EvidenceDigest: "sha256:" + strings.Repeat("z", 64)},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted invalid preflight completion: %+v", bad)
		}
	}
}

func TestApproveModeAndPlanValidation(t *testing.T) {
	sha := strings.Repeat("a", 40)
	if (ApproveRequest{CommitSHA: sha}).Validate() != nil {
		t.Fatal("plain approve must validate")
	}
	if (ApproveRequest{CommitSHA: sha, Resume: true}).Validate() == nil {
		t.Fatal("resume without source attempt must fail")
	}
	if (ApproveRequest{CommitSHA: sha, SourceAttemptID: uuid.NewString()}).Validate() == nil {
		t.Fatal("source attempt without resume must fail")
	}
	if (ApproveRequest{CommitSHA: sha, Resume: true, SourceAttemptID: uuid.NewString()}).Validate() != nil {
		t.Fatal("resume approve must validate")
	}
	if (ExecutionModeRequest{Mode: "other"}).Validate() == nil || (ExecutionModeRequest{Mode: "controller"}).Validate() != nil {
		t.Fatal("mode validation wrong")
	}
	if (ConvergePlan{Resource: "r", Repository: "o/r", CommitSHA: sha, PathClass: PathClassExecutable, Generation: 1}).Validate() == nil {
		t.Fatal("executable plan without approval must fail")
	}
	if (ConvergePlan{Resource: "r", Repository: "o/r", CommitSHA: sha, PathClass: PathClassConfig, Generation: 1}).Validate() != nil {
		t.Fatal("config plan must validate")
	}
	if (ConvergePlan{Resource: "r", Repository: "o/r", CommitSHA: sha, PathClass: PathClassInert, RequiresApproval: true, Generation: 1}).Validate() == nil {
		t.Fatal("an inert range never records a converge plan")
	}
}

func TestStopAttestationDecision(t *testing.T) {
	now := time.Now()
	source := validStart(uuid.NewString()).Executor
	subject := PrincipalSubject("dev-1")
	sameBoot := StopAttestationRequest{SourceAttemptID: uuid.NewString(), DropletID: source.DropletID, MachineID: source.MachineID,
		BootID: source.BootID, InvocationID: strings.Repeat("0", 32), ObservedAt: now,
		PhaseUIDEmpty: true, PhaseProcessGroupReaped: true, SourceCgroupEmpty: true}
	if err := ValidStopAttestation(sameBoot, source, subject, subject, now); err != nil {
		t.Fatalf("same-boot attestation with every fact rejected: %v", err)
	}
	newBoot := sameBoot
	newBoot.BootID, newBoot.PhaseUIDEmpty, newBoot.PhaseProcessGroupReaped, newBoot.SourceCgroupEmpty = "b2", false, false, false
	if err := ValidStopAttestation(newBoot, source, subject, subject, now); err != nil {
		t.Fatalf("new-boot attestation rejected: %v", err)
	}
	for name, mutate := range map[string]func(*StopAttestationRequest, *string){
		"populated cgroup": func(r *StopAttestationRequest, _ *string) { r.SourceCgroupEmpty = false },
		"phase uid alive":  func(r *StopAttestationRequest, _ *string) { r.PhaseUIDEmpty = false },
		"clone droplet":    func(r *StopAttestationRequest, _ *string) { r.DropletID = "999" },
		"other machine":    func(r *StopAttestationRequest, _ *string) { r.MachineID = "m2" },
		"same invocation":  func(r *StopAttestationRequest, _ *string) { r.InvocationID = source.InvocationID },
		"stale":            func(r *StopAttestationRequest, _ *string) { r.ObservedAt = now.Add(-10 * time.Minute) },
		"other device":     func(_ *StopAttestationRequest, s *string) { *s = PrincipalSubject("dev-2") },
	} {
		r, attester := sameBoot, subject
		mutate(&r, &attester)
		if got := code(ValidStopAttestation(r, source, subject, attester, now)); got != CodeFleetExecutorStopAttestationInvalid {
			t.Errorf("%s: code %q", name, got)
		}
	}
	// A body that omits the same-boot facts decodes them as false: fail closed.
	body := []byte(`{"sourceAttemptId":"` + sameBoot.SourceAttemptID + `","dropletId":"424242","machineId":"m","bootId":"b","invocationId":"` + strings.Repeat("0", 32) + `","observedAt":"` + now.UTC().Format(time.RFC3339Nano) + `"}`)
	var decoded StopAttestationRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if ValidStopAttestation(decoded, source, subject, subject, now) == nil {
		t.Fatal("omitted same-boot facts must not prove a stop")
	}
}

func TestProviderEvidence(t *testing.T) {
	now := time.Now()
	e := ProviderEvidence{Provider: "digitalocean", DropletID: "9", Status: "off", ObservedAt: now.Add(-time.Minute)}
	if err := e.Validate(now); err != nil {
		t.Fatal(err)
	}
	local := e
	local.ObservedAt = e.ObservedAt.In(time.FixedZone("x", 7200))
	if e.Digest() != local.Digest() || !strings.HasPrefix(e.Digest(), "sha256:") {
		t.Fatal("digest must be canonical across time zones")
	}
	e.ObservedAt = now.Add(-2 * time.Hour)
	if code(e.Validate(now)) != CodeFleetProviderEvidenceInvalid {
		t.Fatal("stale evidence must fail")
	}
	e.ObservedAt, e.Status = now, "running"
	if e.Validate(now) == nil {
		t.Fatal("running droplet is not evidence")
	}
}

// TestConvergeExtensionKeepsCapacityPlanDigest pins that hanging an omitempty
// converge object off fleet.CapacityPlan (E4a) leaves every existing plan's
// canonical bytes and digest byte-identical. The golden digest was computed
// from the pre-executor CapacityPlan and must never change.
func TestConvergeExtensionKeepsCapacityPlanDigest(t *testing.T) {
	plan := fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: "00000000-0000-4000-8000-000000000001",
		Cluster: "staging", Pool: "workers",
		Current:  fleet.NodePool{Size: "s-2vcpu-4gb", Min: 1, Desired: 2, Max: 4},
		Proposed: fleet.NodePool{Size: "s-2vcpu-4gb", Min: 1, Desired: 3, Max: 4},
		Action:   "scale", Strategy: "rolling", SourceDigest: "sha256:" + strings.Repeat("1", 64),
		Findings: []fleet.Finding{{Severity: "info", Code: "fleet.plan.example", Field: "proposed", Message: "m"}}}
	base, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	extended, err := json.Marshal(struct {
		fleet.CapacityPlan
		Converge *ConvergePlan `json:"converge,omitempty"`
	}{CapacityPlan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base, extended) {
		t.Fatalf("omitempty converge changed canonical bytes:\n%s\n%s", base, extended)
	}
	sum := sha256.Sum256(base)
	const golden = "8261b6e37a3bdd3cbf1db505e920cbbc276eb0a13f89086703cc87aae4b5ccc2"
	if got := hex.EncodeToString(sum[:]); got != golden {
		t.Fatalf("capacity-plan canonical digest = %s, golden %s", got, golden)
	}
}

// TestRepoReadOnlyForPlanAndRecover pins ADR 0010: repo-read is carried only
// by the plan and recover grants, on both backend profiles.
func TestRepoReadOnlyForPlanAndRecover(t *testing.T) {
	if CredRepoRead != "repo-read" {
		t.Fatalf("CredRepoRead = %q", CredRepoRead)
	}
	for profile, table := range GrantTypes {
		for phase, types := range table {
			has := false
			for _, ty := range types {
				has = has || ty == CredRepoRead
			}
			if want := phase == GrantPhasePlan || phase == GrantPhaseRecover; has != want {
				t.Errorf("%s/%s: repo-read allowed = %v, want %v", profile, phase, has, want)
			}
		}
	}
	if CodeStatus[CodeFleetRepoTokenUnavailable] != 503 || CodeStatus[CodeFleetRepoTokenReplayExpired] != 409 {
		t.Fatal("repo token codes are 503 and 409")
	}
}
