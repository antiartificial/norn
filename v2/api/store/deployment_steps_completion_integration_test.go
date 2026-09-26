package store

import (
	"context"
	"testing"

	"norn/v2/api/internal/pgtest"
	"norn/v2/api/model"
)

func TestFinishDeploymentStepRejectsMissingStep(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_missing_deployment_step")
	db, err := Connect(server.URL("norn_missing_deployment_step"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishDeploymentStep(context.Background(), "missing-deployment", "snapshot", model.DeploymentStepComplete, 0, "", nil); err == nil {
		t.Fatal("missing deployment step was accepted as complete")
	}
}
