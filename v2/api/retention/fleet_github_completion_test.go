package retention

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/archive"
	"norn/v2/api/store"
)

func TestFleetGitHubCompletionRejectsTamperedExposedResult(t *testing.T) {
	signer, err := store.NewHMACAcceptanceSigner("fleet-github-completion-test-key-0001")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{"planId": "plan-1", "url": "https://github.example.test/pull/42"}
	canonical, err := json.Marshal(map[string]interface{}{"schema": "norn.fleet-github-completion/v1", "operationId": "op-1", "planId": "plan-1", "kind": "fleet.github.pull-request", "status": "succeeded", "result": result})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signer.Sign(context.Background(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	completion := map[string]interface{}{"canonicalBytes": base64.StdEncoding.EncodeToString(canonical), "signingAlgorithm": signature.Algorithm, "signingKeyId": signature.KeyID, "signature": signature.Value, "result": result}
	row := map[string]interface{}{"id": "op-1", "kind": "fleet.github.pull-request", "ref": "plan-1", "status": "succeeded", "payload": map[string]interface{}{"fleetGitHub": map[string]interface{}{"planId": "plan-1"}, "planId": "plan-1", "url": result["url"]}, "metadata": map[string]interface{}{"fleetGitHubCompletion": completion}}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	bundle := &archive.Bundle{Subject: archive.Subject{Kind: "operation", ID: "op-1", OperationID: "op-1", OperationKind: "fleet.github.pull-request"}, Operation: encoded}
	archiver := &Archiver{Signer: signer}
	if err := archiver.verifyFleetGitHubCompletion(context.Background(), bundle); err != nil {
		t.Fatalf("valid completion rejected: %v", err)
	}
	row["payload"].(map[string]interface{})["url"] = "https://github.example.test/pull/43"
	encoded, _ = json.Marshal(row)
	bundle.Operation = encoded
	if err := archiver.verifyFleetGitHubCompletion(context.Background(), bundle); err == nil || !strings.Contains(err.Error(), "payload differs") {
		t.Fatalf("tampered result error=%v", err)
	}
}
