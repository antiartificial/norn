package model

import (
	"testing"
	"time"
)

func TestAttachTypedOperationReceipt(t *testing.T) {
	finished := time.Now().UTC()
	op := Operation{
		Kind: "platform.upgrade", Status: OperationSucceeded, Message: "upgrade complete",
		StartedAt: finished.Add(-time.Minute), FinishedAt: &finished,
		Payload:  map[string]interface{}{"ref": "v2.17.0", "mode": "restart"},
		Metadata: map[string]interface{}{"exitCode": float64(0), "outputTruncated": true},
	}
	op.AttachReceipt()
	if op.Receipt == nil || op.Receipt.SchemaVersion != "norn.operation-receipt/v1" || op.Receipt.Platform == nil {
		t.Fatalf("receipt = %#v", op.Receipt)
	}
	if op.Receipt.Platform.Ref != "v2.17.0" || op.Receipt.Platform.ExitCode == nil || *op.Receipt.Platform.ExitCode != 0 {
		t.Fatalf("platform receipt = %#v", op.Receipt.Platform)
	}
}
