package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestExactJSONEqualComparesDecimalValues(t *testing.T) {
	for name, test := range map[string]struct {
		left, right interface{}
		equal       bool
	}{
		"integer above 2^53":    {json.Number("9007199254740993"), json.Number("9007199254740992"), false},
		"same large integer":    {json.Number("9007199254740993"), json.Number("9007199254740993"), true},
		"exponent vs expanded":  {json.Number("1e+21"), json.Number("1000000000000000000000"), true},
		"small exponent":        {json.Number("1e-07"), json.Number("0.0000001"), true},
		"trailing zero":         {json.Number("0.1"), json.Number("0.10"), true},
		"float64 and number":    {0.5, json.Number("0.5"), true},
		"string is not number":  {"1", json.Number("1"), false},
		"nested map":            {map[string]interface{}{"a": map[string]interface{}{"b": json.Number("2")}}, map[string]interface{}{"a": map[string]interface{}{"b": json.Number("2.0")}}, true},
		"nested map difference": {map[string]interface{}{"a": json.Number("2")}, map[string]interface{}{"a": json.Number("3")}, false},
		"extra key":             {map[string]interface{}{"a": true}, map[string]interface{}{"a": true, "b": nil}, false},
		"array order":           {[]interface{}{json.Number("1"), json.Number("2")}, []interface{}{json.Number("2"), json.Number("1")}, false},
		"null":                  {nil, nil, true},
	} {
		if got := exactJSONEqual(test.left, test.right); got != test.equal {
			t.Errorf("%s: exactJSONEqual = %t, want %t", name, got, test.equal)
		}
	}
}

func TestVerifyAcceptanceEvidenceRejectsUnknownSignedFields(t *testing.T) {
	var envelope acceptanceEnvelope
	if err := decodeStrictAcceptanceJSON([]byte(`{"schema":"x","unexpected":true}`), &envelope); err == nil {
		t.Fatal("unknown signed envelope field accepted")
	}
	if err := decodeStrictAcceptanceJSON([]byte(`{"schema":"x"} {}`), &envelope); err == nil {
		t.Fatal("trailing signed JSON accepted")
	}
}

func TestResolveBindsIntegersAboveFloatPrecision(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptance := newAcceptance(t, stores[0], "bigint-key", "operator", "bigint-app", false)
	acceptance.Operation.Payload["generation"] = json.Number("9007199254740993")
	var err error
	if acceptance.Fingerprint, err = CanonicalOperationRequestFingerprint(acceptance); err != nil {
		t.Fatal(err)
	}
	accepted, err := stores[0].Accept(context.Background(), acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Resolve(context.Background(), acceptance.Identity, acceptance.Fingerprint); err != nil {
		t.Fatalf("exact integer replay rejected: %v", err)
	}
	if _, err := dbs[0].Pool.Exec(context.Background(), `UPDATE operations SET payload=jsonb_set(payload,'{generation}','9007199254740992'::jsonb) WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	_, err = stores[0].Resolve(context.Background(), acceptance.Identity, acceptance.Fingerprint)
	if !errors.Is(err, ErrAcceptanceSignature) {
		t.Fatalf("float-equivalent integer tamper resolved: %v", err)
	}
}
