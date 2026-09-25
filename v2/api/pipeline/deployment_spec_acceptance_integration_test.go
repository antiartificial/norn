package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestAcceptedDeploySpecDigestBlocksChangedSpecBeforeEffects(t *testing.T) {
	p, db, request := acceptancePipelineFixture(t)
	ctx := context.Background()
	spec := &model.InfraSpec{App: "digest-bound-deploy", Deploy: true, Processes: map[string]model.Process{"web": {Command: "serve"}}}
	accepted, err := p.QueueReleaseDeployment(ctx, spec, strings.Repeat("a", 40), "registry.example/app@sha256:"+strings.Repeat("b", 64), "staging", nil, request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := db.GetDeployment(ctx, accepted.Intent.DeploymentID)
	if err != nil || queued.SpecDigest != digest || accepted.Operation.Payload["specDigest"] != digest {
		t.Fatalf("accepted spec provenance deployment=%+v payload=%+v err=%v", queued, accepted.Operation.Payload, err)
	}
	p.AppsDir = t.TempDir()
	appDir := filepath.Join(p.AppsDir, spec.App)
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	changed := []byte("name: digest-bound-deploy\ndeploy: true\nprocesses:\n  web:\n    command: changed\n")
	if err := os.WriteFile(filepath.Join(appDir, "infraspec.yaml"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	claim, err := store.NewOperationClaim(accepted.Operation.ID, "test-worker", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := p.ExecuteOperation(ctx, &accepted.Operation, claim); err == nil || result != nil || !strings.Contains(err.Error(), "accepted deployment spec differs") {
		t.Fatalf("changed spec execution result=%+v err=%v", result, err)
	}
	var steps int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM deployment_steps WHERE deployment_id=$1`, queued.ID).Scan(&steps); err != nil || steps != 0 {
		t.Fatalf("changed spec reached mutable steps=%d err=%v", steps, err)
	}
}
