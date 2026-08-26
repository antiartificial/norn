package store

import (
	"testing"

	"norn/v2/api/model"
)

func TestPrepareOperationNormalizesDefaults(t *testing.T) {
	op := &model.Operation{ID: "op-1", Kind: "app.snapshot"}
	payload, metadata, err := prepareOperation(op)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "{}" || string(metadata) != "{}" {
		t.Fatalf("unexpected encoded operation: payload=%s metadata=%s", payload, metadata)
	}
	if op.Status != model.OperationQueued || op.MaxAttempts != 1 || op.StartedAt.IsZero() || op.NextAttemptAt.IsZero() {
		t.Fatalf("operation defaults were not normalized: %+v", op)
	}
}

func TestPrepareOperationRejectsUnencodableMetadata(t *testing.T) {
	op := &model.Operation{
		ID:       "op-1",
		Kind:     "app.snapshot",
		Metadata: map[string]interface{}{"invalid": make(chan struct{})},
	}
	if _, _, err := prepareOperation(op); err == nil {
		t.Fatal("expected invalid metadata to be rejected")
	}
}

func TestDecodeOperationFieldsRejectsCorruptReceipts(t *testing.T) {
	op := &model.Operation{ID: "op-1"}
	if err := decodeOperationFields(op, []byte(`{"ok":true}`), []byte(`{"broken"`)); err == nil {
		t.Fatal("expected corrupt metadata to be rejected")
	}
}
