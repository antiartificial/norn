package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type workloadReleaseSnapshotOpener struct {
	operationID string
	ciphertext  []byte
	err         error
	calls       int
}

func (o *workloadReleaseSnapshotOpener) OpenPrivateWorkloadRelease(_ context.Context, operation model.Operation, _ *store.WorkloadReleaseInputKeyRing) ([]byte, error) {
	o.calls++
	if o.err != nil {
		return nil, o.err
	}
	if operation.ID != o.operationID {
		return nil, errors.New("wrong operation")
	}
	return bytes.Clone(o.ciphertext), nil
}

func TestPrepareWorkloadReleaseTargetInputsUsesAcceptedSnapshotAndSelectsPlanKeys(t *testing.T) {
	plan, spec := workloadReleaseTargetPlanFixture(t)
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: "operation-release-1", Kind: "app.workload-release", App: plan.Intent.App,
		Payload: map[string]interface{}{"workloadReleasePlan": string(encodedPlan)}}
	keys, err := store.NewWorkloadReleaseInputKeyRing("release-key", map[string][]byte{"release-key": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	opener := &workloadReleaseSnapshotOpener{operationID: operation.ID, ciphertext: []byte("accepted-ciphertext")}
	decryptCalls := 0
	prepared, err := prepareWorkloadReleaseTargetInputs(context.Background(), operation, plan, spec, opener, keys, func(ciphertext []byte) (map[string]string, error) {
		decryptCalls++
		if string(ciphertext) != "accepted-ciphertext" {
			t.Fatal("decryptor did not receive the accepted encrypted snapshot")
		}
		return map[string]string{"API_TOKEN": "accepted-secret", "UNRELATED": "must-not-stage"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opener.calls != 1 || decryptCalls != 1 || prepared.JobPlan.Request.JobID != plan.Intent.TargetJobID ||
		len(prepared.Secrets) != 1 || prepared.Secrets["API_TOKEN"] != "accepted-secret" {
		t.Fatalf("prepared=%+v openerCalls=%d decryptCalls=%d", prepared, opener.calls, decryptCalls)
	}
}

func TestPrepareWorkloadReleaseTargetInputsRejectsMissingSecretAndPlanDrift(t *testing.T) {
	plan, spec := workloadReleaseTargetPlanFixture(t)
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: "operation-release-1", Kind: "app.workload-release", App: plan.Intent.App,
		Payload: map[string]interface{}{"workloadReleasePlan": string(encodedPlan)}}
	keys, err := store.NewWorkloadReleaseInputKeyRing("release-key", map[string][]byte{"release-key": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	opener := &workloadReleaseSnapshotOpener{operationID: operation.ID, ciphertext: []byte("accepted-ciphertext")}
	if _, err := prepareWorkloadReleaseTargetInputs(context.Background(), operation, plan, spec, opener, keys, func([]byte) (map[string]string, error) {
		return map[string]string{"UNRELATED": "value"}, nil
	}); err == nil {
		t.Fatal("snapshot without a required target key was accepted")
	}
	changedOperation := operation
	changedOperation.Payload = map[string]interface{}{"workloadReleasePlan": "{}"}
	if _, err := prepareWorkloadReleaseTargetInputs(context.Background(), changedOperation, plan, spec, opener, keys, func([]byte) (map[string]string, error) {
		t.Fatal("plan drift reached decryption")
		return nil, nil
	}); err == nil {
		t.Fatal("operation plan drift was accepted")
	}
}

var _ WorkloadReleasePrivateInputOpener = (*workloadReleaseSnapshotOpener)(nil)
