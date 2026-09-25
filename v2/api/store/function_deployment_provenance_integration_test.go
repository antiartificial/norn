package store

import (
	"context"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestFunctionDeploymentBindingRequiresProvenSuccessfulEnvironmentRevision(t *testing.T) {
	pool := schemaMigrationTestPools(t, 1)[0]
	db := &DB{Pool: pool}
	migrator, err := NewControlSchemaMigrator(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := migrator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	add := func(id, environment string, status model.DeployStatus, digest string, at time.Time) {
		t.Helper()
		d := &model.Deployment{ID: id, App: "function-fixture", Environment: environment, SagaID: id, ImageTag: "registry.example/function@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: status, StartedAt: at}
		if err := db.InsertDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		if digest != "" {
			d.SpecDigest = digest
			if err := db.UpdateDeploymentResult(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now().UTC()
	add("legacy", "staging", model.StatusDeployed, "", now)
	if _, _, err := db.FunctionDeploymentBinding(ctx, "function-fixture", "staging"); err == nil {
		t.Fatal("legacy deployment without provenance admitted")
	}
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	add("proven", "staging", model.StatusDeployed, digest, now.Add(time.Second))
	add("failed", "staging", model.StatusFailed, "", now.Add(2*time.Second))
	add("other-environment", "production", model.StatusDeployed, "", now.Add(4*time.Second))
	image, gotDigest, err := db.FunctionDeploymentBinding(ctx, "function-fixture", "staging")
	if err != nil || image == "" || gotDigest != digest {
		t.Fatalf("active provenance image=%q digest=%q err=%v", image, gotDigest, err)
	}
	add("queued", "staging", model.StatusQueued, "", now.Add(3*time.Second))
	if _, _, err := db.FunctionDeploymentBinding(ctx, "function-fixture", "staging"); err == nil {
		t.Fatal("newer active attempt admitted")
	}
	if err := db.UpdateDeployment(ctx, "queued", model.StatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.FunctionDeploymentBinding(ctx, "function-fixture", "staging"); err != nil {
		t.Fatalf("failed attempt displaced active deployment: %v", err)
	}
	add("unproven-rollback", "staging", model.StatusDeployed, "", now.Add(5*time.Second))
	if _, _, err := db.FunctionDeploymentBinding(ctx, "function-fixture", "staging"); err == nil {
		t.Fatal("unproven rollback admitted")
	}
}
