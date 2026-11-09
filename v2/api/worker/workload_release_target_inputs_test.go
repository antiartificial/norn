package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type workloadReleaseSnapshotOpener struct {
	operationID string
	ciphertext  []byte
	binding     *store.WorkloadReleaseInputBinding
	envelope    *store.WorkloadReleaseInputEnvelope
	err         error
	calls       int
}

func (o *workloadReleaseSnapshotOpener) OpenPrivateWorkloadRelease(_ context.Context, operation model.Operation, keys *store.WorkloadReleaseInputKeyRing) ([]byte, error) {
	o.calls++
	if o.err != nil {
		return nil, o.err
	}
	if operation.ID != o.operationID {
		return nil, errors.New("wrong operation")
	}
	if o.binding != nil && o.envelope != nil {
		return keys.Open(*o.binding, *o.envelope)
	}
	return bytes.Clone(o.ciphertext), nil
}

func TestPrepareWorkloadReleaseTargetInputsUsesPinnedSourceAndAcceptedSnapshot(t *testing.T) {
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
	pinned, err := store.EncodeWorkloadReleasePinnedMaterial(spec, plan.Intent.TargetSpecDigest, []byte("accepted-ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	binding := store.WorkloadReleaseInputBinding{Authority: "authority", Operation: operation.ID, App: operation.App, PlanDigest: planDigest}
	envelope, err := keys.Seal(binding, pinned)
	if err != nil {
		t.Fatal(err)
	}
	clear(pinned)
	opener := &workloadReleaseSnapshotOpener{operationID: operation.ID, binding: &binding, envelope: &envelope}
	// The worker must rebuild from the accepted private spec, not a current
	// checkout that may be missing or have moved since admission.
	spec = nil
	decryptCalls := 0
	prepared, err := prepareWorkloadReleaseTargetInputs(context.Background(), operation, plan, opener, keys, func(ciphertext []byte) (map[string]string, error) {
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
	pinned, err := store.EncodeWorkloadReleasePinnedMaterial(spec, plan.Intent.TargetSpecDigest, []byte("accepted-ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	opener := &workloadReleaseSnapshotOpener{operationID: operation.ID, ciphertext: pinned}
	if _, err := prepareWorkloadReleaseTargetInputs(context.Background(), operation, plan, opener, keys, func([]byte) (map[string]string, error) {
		return map[string]string{"UNRELATED": "value"}, nil
	}); err == nil {
		t.Fatal("snapshot without a required target key was accepted")
	}
	changedOperation := operation
	changedOperation.Payload = map[string]interface{}{"workloadReleasePlan": "{}"}
	if _, err := prepareWorkloadReleaseTargetInputs(context.Background(), changedOperation, plan, opener, keys, func([]byte) (map[string]string, error) {
		t.Fatal("plan drift reached decryption")
		return nil, nil
	}); err == nil {
		t.Fatal("operation plan drift was accepted")
	}
}

func TestPrepareWorkloadReleaseTargetInputsRejectsPinnedSourceDigestDriftBeforeDecrypt(t *testing.T) {
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
	pinned, err := store.EncodeWorkloadReleasePinnedMaterial(spec, plan.Intent.TargetSpecDigest, []byte("accepted-ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	opener := &workloadReleaseSnapshotOpener{operationID: operation.ID, ciphertext: pinned}
	changedPlan := plan
	changedPlan.Intent.TargetSpecDigest = "sha256:" + strings.Repeat("1", 64)
	changedEncoded, err := json.Marshal(changedPlan)
	if err != nil {
		t.Fatal(err)
	}
	operation.Payload["workloadReleasePlan"] = string(changedEncoded)
	if _, err := prepareWorkloadReleaseTargetInputs(context.Background(), operation, changedPlan, opener, keys, func([]byte) (map[string]string, error) {
		t.Fatal("source digest mismatch reached SOPS decryption")
		return nil, nil
	}); err == nil {
		t.Fatal("pinned source with a different accepted digest was used")
	}
}

var _ WorkloadReleasePrivateInputOpener = (*workloadReleaseSnapshotOpener)(nil)
