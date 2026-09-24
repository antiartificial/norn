package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
)

func TestManagerLaunchSnapshotUsesPrivateMaterialAndStableDescriptor(t *testing.T) {
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := SnapshotLaunchMaterial{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), ServiceName: "demo", ServiceFile: []byte("[demo]\nhost=localhost\nuser=demo\n"), Password: "rotated-secret", Subject: "app:demo/db:main@generation:1", Timeout: time.Minute}
	payload, err := manager.BuildSnapshotDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), material.Password) || strings.Contains(string(payload), "localhost") {
		t.Fatalf("descriptor leaked private material: %s", payload)
	}
	r := effect.Reservation{Authority: "authority", Resource: "app/demo/app.snapshot", OperationClaim: effect.OperationClaim{OperationID: "snapshot-op", OwnerID: "worker", Generation: 1}, Stage: SnapshotStage, Supervisor: "snapshot-runner", LaunchPayload: payload}
	r.InputDigest, err = effect.ComputeInputDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	r.SupervisorExecutionID = "snapshot-execution"
	if err := manager.Prepare(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchSnapshot(context.Background(), r, material)
	if err != nil || identity.RuntimeInstanceID == "" {
		t.Fatalf("launch = %+v, %v", identity, err)
	}
	rotated := material
	rotated.Password = "new-secret"
	if _, err := manager.LaunchSnapshot(context.Background(), r, rotated); err != nil {
		t.Fatalf("rotated credential could not recover launch identity: %v", err)
	}
	backend.mu.Lock()
	starts := backend.starts
	backend.mu.Unlock()
	if starts != 1 {
		t.Fatalf("snapshot launched %d times", starts)
	}
}
