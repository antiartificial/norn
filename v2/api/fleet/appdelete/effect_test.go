package appdelete

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func requestFixture() Request {
	return Request{SchemaVersion: RequestSchema, PlanID: uuid.NewString(), AttemptID: uuid.NewString(), WriterGeneration: 7,
		WriterHolder: uuid.NewString(), Target: "app-04", DropletID: "4242", ProviderAccount: uuid.NewString(),
		AuthorizationSHA256: strings.Repeat("a", 64), WorkloadReceiptSHA256: strings.Repeat("b", 64),
		DeleteRequestSHA256: ExactDeleteRequestSHA256("4242")}
}

func TestCoPermitReturnsExactExecutorTuple(t *testing.T) {
	r := requestFixture()
	grant := GrantFromRequest(r)
	raw, _ := json.Marshal(grant)
	var got map[string]interface{}
	_ = json.Unmarshal(raw, &got)
	if len(got) != 12 || got["schemaVersion"] != GrantSchema || got["authorizesEffect"] != true ||
		got["target"] != "app-04" || got["deleteRequestSha256"] != r.DeleteRequestSHA256 {
		t.Fatalf("grant differs from executor contract: %s", raw)
	}
}

func TestDecodeRequestRejectsExtensionsAndDuplicateKeys(t *testing.T) {
	r := requestFixture()
	raw, _ := json.Marshal(r)
	extended := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"grant":true}`)...)
	if _, err := DecodeRequest(extended); err == nil {
		t.Fatal("unknown field accepted")
	}
	needle := `"planId":"` + r.PlanID + `"`
	duplicate := []byte(strings.Replace(string(raw), needle, needle+`,"planId":"`+r.PlanID+`"`, 1))
	if _, err := DecodeRequest(duplicate); err == nil {
		t.Fatal("duplicate field accepted")
	}
	caseVariant := []byte(strings.Replace(string(raw), needle, needle+`,"PlanId":"`+r.PlanID+`"`, 1))
	if _, err := DecodeRequest(caseVariant); err == nil {
		t.Fatal("case-variant field accepted")
	}
}

func TestTerminalRequestIsGenericAndStrict(t *testing.T) {
	for _, target := range []struct{ name, id string }{{"app-04", "4242"}, {"app-03", "610327215"}} {
		r := TerminalRequest{SchemaVersion: TerminalRequestSchema, PlanID: uuid.NewString(), AttemptID: uuid.NewString(), WriterGeneration: 7, WriterHolder: uuid.NewString(), Target: target.name, DropletID: target.id, ProviderAccount: uuid.NewString(), AuthorizationSHA256: strings.Repeat("a", 64), WorkloadReceiptSHA256: strings.Repeat("b", 64), DeleteRequestSHA256: ExactDeleteRequestSHA256(target.id), RefreshDispatchSHA256: strings.Repeat("c", 64), StateSHA256: strings.Repeat("d", 64), ProviderAbsenceSHA256: strings.Repeat("e", 64), WorkloadSHA256: strings.Repeat("f", 64), PublicPilotSHA256: strings.Repeat("1", 64), ObservationSHA256: strings.Repeat("2", 64)}
		raw, _ := json.Marshal(r)
		got, err := DecodeTerminalRequest(raw)
		if err != nil || got != r {
			t.Fatalf("%s decode=%+v err=%v", target.name, got, err)
		}
		extended := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"providerStatus":404}`)...)
		if _, err := DecodeTerminalRequest(extended); err == nil {
			t.Fatal("provider assertion accepted")
		}
		changed := r
		changed.Target = "app-03"
		if target.name == "app-03" {
			changed.Target = "app-04"
		}
		if changed.Validate() != nil { /* structurally valid; durable store binds tuple */
		} else if changed.Permit() == r.Permit() {
			t.Fatal("changed target did not change permit tuple")
		}
	}
}
