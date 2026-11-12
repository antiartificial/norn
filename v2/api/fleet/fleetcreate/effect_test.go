package fleetcreate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validIntent() Intent {
	return Intent{SchemaVersion: IntentSchema, IntentID: uuid.NewString(), PlanID: uuid.NewString(),
		Cluster: "staging-fresh", CommitSHA: strings.Repeat("a", 40), PlanSHA256: strings.Repeat("b", 64),
		ProviderAccountSHA256: strings.Repeat("c", 64), StateBackendSHA256: strings.Repeat("d", 64),
		StateLineageSHA256: strings.Repeat("e", 64), StateSerial: 23,
		TopologySHA256: strings.Repeat("f", 64), SSHInputsSHA256: strings.Repeat("1", 64)}
}

func validAdmission(i Intent) Admission {
	digest, _ := i.Digest()
	return Admission{SchemaVersion: AdmissionSchema, IntentID: i.IntentID, PlanID: i.PlanID, Cluster: i.Cluster, AttemptID: uuid.NewString(),
		IntentSHA256: digest, RemainderSHA256: strings.Repeat("2", 64),
		ProviderReadbackSHA256: strings.Repeat("3", 64), StateReadbackSHA256: strings.Repeat("4", 64),
		StateLineageSHA256: i.StateLineageSHA256, StateSerial: i.StateSerial, ExpectedNodeCount: 9, ObservedNodeCount: 8}
}

func validDispatch(i Intent) DispatchBinding {
	digest, _ := i.Digest()
	return DispatchBinding{SchemaVersion: DispatchSchema, IntentID: i.IntentID, PlanID: i.PlanID,
		Cluster: i.Cluster, IntentSHA256: digest, Repository: "antiartificial/norn-fleet",
		RepositoryID: "101", RepositoryOwnerID: "202", WorkflowPath: ".github/workflows/fresh-fleet-create.yml",
		WorkflowSHA: i.CommitSHA, Environment: "work-staging",
		RunID: "42", RunAttempt: "1", CommitSHA: i.CommitSHA,
		DispatchReceiptSHA256: strings.Repeat("9", 64)}
}

func TestAttemptStartBindsIntentAndDispatchReceipt(t *testing.T) {
	i := validIntent()
	digest, _ := i.Digest()
	dispatch := validDispatch(i)
	r := AttemptStartRequest{SchemaVersion: StartSchema, IntentID: i.IntentID, PlanID: i.PlanID,
		Cluster: i.Cluster, AttemptID: uuid.NewString(), IntentSHA256: digest,
		RunnerAttemptID: dispatch.RunnerAttemptID(), DispatchReceiptSHA256: dispatch.DispatchReceiptSHA256}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if !dispatch.MatchesIntent(i) || !dispatch.MatchesStart(r) {
		t.Fatal("dispatch did not bind exact intent and runner")
	}
	a := AttemptFromStart(r)
	if !a.MatchesStart(r) || a.Status != "admitted" || a.Generation != 1 {
		t.Fatalf("attempt does not bind exact initial request: %+v", a)
	}
	changed := r
	changed.DispatchReceiptSHA256 = strings.Repeat("b", 64)
	if a.MatchesStart(changed) {
		t.Fatal("attempt accepted a substituted dispatch receipt")
	}
	changedDispatch := dispatch
	changedDispatch.RunAttempt = "2"
	if changedDispatch.MatchesStart(r) {
		t.Fatal("dispatch accepted a different workflow attempt")
	}
	changedDispatch = dispatch
	changedDispatch.CommitSHA = strings.Repeat("f", 40)
	if changedDispatch.MatchesIntent(i) {
		t.Fatal("dispatch accepted a different intent commit")
	}
}

func TestIntentBindsReviewedCreateInputs(t *testing.T) {
	i := validIntent()
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	digest, err := i.Digest()
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	changed := i
	changed.ProviderAccountSHA256 = strings.Repeat("9", 64)
	changedDigest, _ := changed.Digest()
	if changedDigest == digest {
		t.Fatal("provider account change did not change intent digest")
	}
	changed = i
	changed.StateSerial = 0
	if changed.Validate() == nil {
		t.Fatal("zero state serial was accepted")
	}
}

func TestAdmissionAndGrantBindSameIntentAndExactRemainder(t *testing.T) {
	i := validIntent()
	a := validAdmission(i)
	if err := a.Validate(); err != nil || !a.MatchesIntent(i) {
		t.Fatalf("valid admission mismatch: err=%v", err)
	}
	r := Request{SchemaVersion: RequestSchema, IntentID: a.IntentID, PlanID: a.PlanID, AttemptID: a.AttemptID,
		IntentSHA256: a.IntentSHA256, AdmissionSHA256: a.Digest(), RemainderSHA256: a.RemainderSHA256}
	if err := r.Validate(); err != nil || !r.MatchesAdmission(a) {
		t.Fatalf("valid request mismatch: err=%v", err)
	}
	g := GrantFromRequest(r)
	if !g.MatchesRequest(r) {
		t.Fatal("grant did not preserve exact request")
	}
	g.RemainderSHA256 = strings.Repeat("9", 64)
	if g.MatchesRequest(r) {
		t.Fatal("grant accepted substituted remainder")
	}
	a.StateLineageSHA256 = strings.Repeat("8", 64)
	if a.MatchesIntent(i) {
		t.Fatal("admission accepted substituted state lineage")
	}
	a = validAdmission(i)
	a.ObservedNodeCount = a.ExpectedNodeCount
	if a.Validate() == nil {
		t.Fatal("completed inventory must not authorize a create remainder")
	}
}

func TestDecodeRequestRejectsDuplicateUnknownAndTrailingJSON(t *testing.T) {
	r := Request{SchemaVersion: RequestSchema, IntentID: uuid.NewString(), PlanID: uuid.NewString(), AttemptID: uuid.NewString(),
		IntentSHA256: strings.Repeat("a", 64), AdmissionSHA256: strings.Repeat("b", 64), RemainderSHA256: strings.Repeat("c", 64)}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodeRequest(encoded); err != nil || decoded != r {
		t.Fatalf("valid request decoded=%+v err=%v", decoded, err)
	}
	valid := string(encoded)
	duplicate := strings.Replace(valid, `"schemaVersion":`, `"schemaVersion":"duplicate","schemaVersion":`, 1)
	unknown := strings.TrimSuffix(valid, "}") + `,"providerToken":"secret"}`
	trailing := valid + ` {}`
	for name, raw := range map[string]string{"duplicate": duplicate, "unknown": unknown, "trailing": trailing} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRequest([]byte(raw)); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}
