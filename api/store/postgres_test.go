package store

import (
	"context"
	"os"
	"testing"
	"time"

	"norn/api/model"
)

func getTestDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("NORN_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://norn:norn@localhost:5432/norn_db?sslmode=disable"
	}
	db, err := Connect(url)
	if err != nil {
		t.Skipf("skipping DB test (cannot connect): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConnect(t *testing.T) {
	db := getTestDB(t)
	ctx := context.Background()

	row := db.QueryRow(ctx, "SELECT 1")
	var one int
	if err := row.Scan(&one); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if one != 1 {
		t.Errorf("got %d, want 1", one)
	}
}

func TestMigrate(t *testing.T) {
	db := getTestDB(t)
	// Should be idempotent — safe to run multiple times
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}
}

func TestDeploymentCRUD(t *testing.T) {
	db := getTestDB(t)
	ctx := context.Background()

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Insert
	deploy := &model.Deployment{
		ID:        "test-" + time.Now().Format("20060102150405.000"),
		App:       "test-app",
		CommitSHA: "abc123def456",
		ImageTag:  "test-app:abc123def456",
		Status:    model.StatusQueued,
		Steps:     []model.StepLog{},
		StartedAt: time.Now(),
	}

	if err := db.InsertDeployment(ctx, deploy); err != nil {
		t.Fatalf("InsertDeployment: %v", err)
	}

	// List
	deploys, err := db.ListDeployments(ctx, "test-app", 10)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(deploys) == 0 {
		t.Fatal("expected at least one deployment")
	}

	found := false
	for _, d := range deploys {
		if d.ID == deploy.ID {
			found = true
			if d.Status != model.StatusQueued {
				t.Errorf("Status = %q, want queued", d.Status)
			}
			if d.CommitSHA != "abc123def456" {
				t.Errorf("CommitSHA = %q", d.CommitSHA)
			}
		}
	}
	if !found {
		t.Error("inserted deployment not found in list")
	}

	// Update
	steps := []model.StepLog{{Step: "build", Status: model.StatusDeployed, DurationMs: 500}}
	if err := db.UpdateDeployment(ctx, deploy.ID, model.StatusDeployed, steps, ""); err != nil {
		t.Fatalf("UpdateDeployment: %v", err)
	}

	deploys, _ = db.ListDeployments(ctx, "test-app", 10)
	for _, d := range deploys {
		if d.ID == deploy.ID {
			if d.Status != model.StatusDeployed {
				t.Errorf("Status after update = %q, want deployed", d.Status)
			}
			if d.FinishedAt == nil {
				t.Error("FinishedAt should be set for terminal status")
			}
			if len(d.Steps) != 1 {
				t.Errorf("Steps = %d, want 1", len(d.Steps))
			}
		}
	}

	// Cleanup
	db.pool.Exec(ctx, "DELETE FROM deployments WHERE id = $1", deploy.ID)
}

func TestRecoverInFlightDeployments(t *testing.T) {
	db := getTestDB(t)
	ctx := context.Background()

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	id := "recover-test-" + time.Now().Format("20060102150405.000")
	deploy := &model.Deployment{
		ID:        id,
		App:       "test-app",
		CommitSHA: "abc123",
		ImageTag:  "test-app:abc123",
		Status:    model.StatusBuilding,
		Steps:     []model.StepLog{},
		StartedAt: time.Now(),
	}

	if err := db.InsertDeployment(ctx, deploy); err != nil {
		t.Fatalf("InsertDeployment: %v", err)
	}

	if err := db.RecoverInFlightDeployments(ctx); err != nil {
		t.Fatalf("RecoverInFlightDeployments: %v", err)
	}

	deploys, _ := db.ListDeployments(ctx, "test-app", 100)
	for _, d := range deploys {
		if d.ID == id {
			if d.Status != model.StatusFailed {
				t.Errorf("Status = %q, want failed after recovery", d.Status)
			}
		}
	}

	// Cleanup
	db.pool.Exec(ctx, "DELETE FROM deployments WHERE id = $1", id)
}

func TestClusterNodeCRUD(t *testing.T) {
	db := getTestDB(t)
	ctx := context.Background()

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	id := "cluster-test-" + time.Now().Format("20060102150405.000")
	node := &model.ClusterNode{
		ID:        id,
		Name:      "test-node-crud",
		Provider:  "hetzner",
		Region:    "fsn1",
		Size:      "cx22",
		Role:      "server",
		Status:    "provisioning",
		CreatedAt: time.Now(),
	}

	// Insert
	if err := db.InsertClusterNode(ctx, node); err != nil {
		t.Fatalf("InsertClusterNode: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		db.pool.Exec(ctx, "DELETE FROM cluster_nodes WHERE id = $1", id)
	})

	// List
	nodes, err := db.ListClusterNodes(ctx)
	if err != nil {
		t.Fatalf("ListClusterNodes: %v", err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == id {
			found = true
			if n.Name != "test-node-crud" {
				t.Errorf("Name = %q", n.Name)
			}
		}
	}
	if !found {
		t.Error("inserted node not found in list")
	}

	// Get
	got, err := db.GetClusterNode(ctx, id)
	if err != nil {
		t.Fatalf("GetClusterNode: %v", err)
	}
	if got == nil {
		t.Fatal("GetClusterNode returned nil")
	}
	if got.Provider != "hetzner" {
		t.Errorf("Provider = %q", got.Provider)
	}
	if got.Status != "provisioning" {
		t.Errorf("Status = %q", got.Status)
	}

	// UpdateStatus
	if err := db.UpdateClusterNodeStatus(ctx, id, "ready", ""); err != nil {
		t.Fatalf("UpdateClusterNodeStatus: %v", err)
	}
	got, _ = db.GetClusterNode(ctx, id)
	if got.Status != "ready" {
		t.Errorf("Status after update = %q, want ready", got.Status)
	}

	// Delete
	if err := db.DeleteClusterNode(ctx, id); err != nil {
		t.Fatalf("DeleteClusterNode: %v", err)
	}
	got, err = db.GetClusterNode(ctx, id)
	if err != nil {
		t.Fatalf("GetClusterNode after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestConnect_BadURL(t *testing.T) {
	_, err := Connect("postgres://nobody:nope@localhost:59999/nonexistent?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Error("expected error for bad connection")
	}
}
