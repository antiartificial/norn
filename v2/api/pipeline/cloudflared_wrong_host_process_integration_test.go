package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/cloudflared"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// A separate process claims an accepted host-A operation with a host-B driver.
// It must defer before reserving an effect or editing any local configuration.
func TestCloudflaredWrongHostWorkerProcessPostgres(t *testing.T) {
	if os.Getenv("NORN_CLOUDFLARED_WRONG_HOST_CHILD") == "1" {
		runCloudflaredWrongHostChild(t)
		return
	}
	p, db, request := acceptancePipelineFixture(t)
	_ = p
	var schema string
	if err := db.Pool.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCloudflaredDriver{config: cloudflared.Config{Ingress: []cloudflared.IngressRule{{Service: "http_status:404"}}}}
	reservation := cloudflaredTestReservation(t, fake)
	var payload map[string]interface{}
	if err := json.Unmarshal(reservation.LaunchPayload, &payload); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: cloudflaredMutationKind, App: "demo", SagaID: uuid.NewString(), Ref: "enable", Status: model.OperationQueued, StartedAt: now, MaxAttempts: 3, Payload: payload}
	request.Key = "wrong-host-process"
	accepted, err := p.QueueOperation(context.Background(), op, request)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCloudflaredWrongHostWorkerProcessPostgres$")
	cmd.Env = append(os.Environ(), "NORN_CLOUDFLARED_WRONG_HOST_CHILD=1", "NORN_CLOUDFLARED_WRONG_HOST_SCHEMA="+schema)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrong-host process: %v output=%s", err, output)
	}
	var effects int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_effects WHERE operation_id=$1`, accepted.Operation.ID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("wrong-host process reserved %d effects", effects)
	}
	stored, err := db.GetOperation(context.Background(), accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == model.OperationSucceeded || stored.Status == model.OperationFailed {
		t.Fatalf("wrong-host worker terminalized operation: %+v", stored)
	}
}

func runCloudflaredWrongHostChild(t *testing.T) {
	config, err := pgxpool.ParseConfig(os.Getenv("NORN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["search_path"] = os.Getenv("NORN_CLOUDFLARED_WRONG_HOST_SCHEMA")
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.DB{Pool: pool}
	fake := &fakeCloudflaredDriver{host: "mini-b", config: cloudflared.Config{Ingress: []cloudflared.IngressRule{{Service: "http_status:404"}}}}
	effects, err := newCloudflaredEffects(db, fake)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{DB: db, CloudflaredEffects: effects}
	claimed, claim, err := db.ClaimNextOperation(context.Background(), "wrong-host-process", time.Minute, []string{cloudflaredMutationKind})
	if err != nil || claimed == nil {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	result, err := p.ExecuteOperation(context.Background(), claimed, claim)
	if result != nil || !effect.IsDeferred(err) || !strings.Contains(fmt.Sprint(err), "another host") || fake.applyCalls != 0 || fake.restartCalls != 0 {
		t.Fatalf("result=%+v err=%v writes=%d restarts=%d", result, err, fake.applyCalls, fake.restartCalls)
	}
}
