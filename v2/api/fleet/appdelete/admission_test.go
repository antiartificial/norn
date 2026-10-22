package appdelete

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestApp04AdmissionExactRequest(t *testing.T) {
	r := Request{SchemaVersion: RequestSchema, PlanID: uuid.NewString(), AttemptID: uuid.NewString(),
		WriterGeneration: 1, WriterHolder: uuid.NewString(), Target: "app-04", DropletID: "4242",
		ProviderAccount: uuid.NewString(), AuthorizationSHA256: strings.Repeat("a", 64),
		WorkloadReceiptSHA256: strings.Repeat("b", 64), DeleteRequestSHA256: ExactDeleteRequestSHA256("4242")}
	a := App04Admission{SchemaVersion: App04AdmissionSchema, PlanID: r.PlanID, AttemptID: r.AttemptID,
		PlanSHA256: strings.Repeat("c", 64), SourceCommit: strings.Repeat("d", 40), TargetID: "work-staging",
		TargetFenceGeneration: 3, AuthorityEpoch: 1, Target: r.Target, DropletID: r.DropletID,
		ProviderAccount: r.ProviderAccount, SavedPlanSHA256: strings.Repeat("e", 64),
		WriterGeneration: r.WriterGeneration, WriterHolder: r.WriterHolder,
		AuthorizationSHA256: r.AuthorizationSHA256, WorkloadReceiptSHA256: r.WorkloadReceiptSHA256,
		DeleteRequestSHA256: r.DeleteRequestSHA256}
	if err := a.Validate(); err != nil || !a.MatchesRequest(r) {
		t.Fatalf("valid admission: %v", err)
	}
	changed := a
	changed.Target = "app-03"
	if changed.Validate() == nil {
		t.Fatal("app03 admitted through app04 contract")
	}
	changed = a
	changed.SavedPlanSHA256 = ""
	if changed.Validate() == nil {
		t.Fatal("missing saved plan admitted")
	}
	wrong := r
	wrong.WriterGeneration++
	if a.MatchesRequest(wrong) {
		t.Fatal("changed PG generation matched")
	}
}
