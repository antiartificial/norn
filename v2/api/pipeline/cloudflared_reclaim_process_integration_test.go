package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

type otherCloudflaredHostDriver struct{ localCloudflaredDriver }

func (otherCloudflaredHostDriver) Host() string { return "different-host" }

// A wrong-host process defers a signed operation, then a second OS process on
// the accepted host claims it only after the scheduled retry and completes it.
func TestCloudflaredWrongHostReclaimProcessPostgres(t *testing.T) {
	if role := os.Getenv("NORN_CLOUDFLARED_RECLAIM_ROLE"); role != "" {
		runCloudflaredReclaimChild(t, role)
		return
	}
	p, db, request := acceptancePipelineFixture(t)
	ctx := context.Background()
	var schema string
	if err := db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yml")
	if err := os.WriteFile(configPath, []byte("tunnel: disposable\ningress:\n  - hostname: demo.example.com\n    service: http://127.0.0.1:8080\n  - service: http_status:404\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	restartLog := filepath.Join(root, "restarts.log")
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\ncase \"$1\" in\n  kickstart) printf 'restart\\n' >> \"$NORN_CLOUDFLARED_RESTART_LOG\" ;;\n  print) printf 'state = running\\n' ;;\n  *) exit 1 ;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prior := cloudflared.ConfigPath()
	cloudflared.SetConfigPath(configPath)
	t.Cleanup(func() { cloudflared.SetConfigPath(prior) })
	cfg, before, err := cloudflared.ReadConfigSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil || host == "" || host == "different-host" {
		t.Fatalf("test host=%q err=%v", host, err)
	}
	mutation := cloudflared.Mutation{Action: "teardown", App: "demo", Host: host, ConfigPath: configPath, Hostnames: []string{"https://demo.example.com"}, BeforeDigest: before}
	if changed, err := mutation.Apply(cfg); err != nil || !changed {
		t.Fatalf("apply target changed=%t err=%v", changed, err)
	}
	mutation.AfterDigest, err = cloudflared.ConfigDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(bytes, &payload); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op := model.Operation{ID: uuid.NewString(), Kind: cloudflaredMutationKind, App: "demo", SagaID: uuid.NewString(), Ref: "teardown", Status: model.OperationQueued, StartedAt: now, MaxAttempts: 3, Payload: payload}
	request.Key = "wrong-host-reclaim"
	accepted, err := p.QueueOperation(ctx, op, request)
	if err != nil {
		t.Fatal(err)
	}
	child := func(role string) string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCloudflaredWrongHostReclaimProcessPostgres$")
		cmd.Env = append(os.Environ(), "NORN_CLOUDFLARED_RECLAIM_ROLE="+role, "NORN_CLOUDFLARED_RECLAIM_SCHEMA="+schema, "NORN_CLOUDFLARED_CONFIG="+configPath, "NORN_CLOUDFLARED_RESTART_LOG="+restartLog, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s process failed: %v output=%s", role, err, out)
		}
		return string(out)
	}
	wrongOutput := child("wrong")
	if !strings.Contains(wrongOutput, "wrong-host-deferred") {
		t.Fatalf("wrong-host output=%s", wrongOutput)
	}
	deferred, err := db.GetOperation(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deferred.Status != model.OperationQueued || deferred.NextAttemptAt.IsZero() || !deferred.NextAttemptAt.After(time.Now()) || deferred.Attempts != 0 {
		t.Fatalf("wrong-host defer did not preserve scheduled retry: %+v", deferred)
	}
	early, _, err := db.ClaimNextOperation(ctx, "premature-worker", time.Minute, []string{cloudflaredMutationKind})
	if err != nil || early != nil {
		t.Fatalf("scheduled retry claimed before due: op=%+v err=%v", early, err)
	}
	var effects int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_effects WHERE operation_id=$1`, accepted.Operation.ID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("wrong host reserved %d effects", effects)
	}
	if _, err := os.Stat(restartLog); !os.IsNotExist(err) {
		t.Fatalf("wrong host restarted service or stat failed: %v", err)
	}
	correctOutput := child("correct")
	if !strings.Contains(correctOutput, "correct-host-completed") {
		t.Fatalf("correct-host output=%s", correctOutput)
	}
	terminal, err := db.GetOperation(ctx, accepted.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != model.OperationSucceeded || terminal.Attempts != 1 || terminal.LockGeneration <= deferred.LockGeneration {
		t.Fatalf("correct-host terminal=%+v", terminal)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM operation_effects WHERE operation_id=$1 AND lifecycle='completed'`, accepted.Operation.ID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatalf("completed effects=%d", effects)
	}
	log, err := os.ReadFile(restartLog)
	if err != nil || string(log) != "restart\n" {
		t.Fatalf("restart log=%q err=%v", log, err)
	}
	result, err := cloudflared.ReadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Ingress) != 1 || result.Ingress[0].Hostname != "" {
		t.Fatalf("final ingress=%+v", result.Ingress)
	}
}

func runCloudflaredReclaimChild(t *testing.T, role string) {
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(os.Getenv("NORN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["search_path"] = os.Getenv("NORN_CLOUDFLARED_RECLAIM_SCHEMA")
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := &store.DB{Pool: pool}
	cloudflared.SetConfigPath(os.Getenv("NORN_CLOUDFLARED_CONFIG"))
	var driver cloudflaredDriver = localCloudflaredDriver{}
	if role == "wrong" {
		driver = otherCloudflaredHostDriver{}
	}
	effects, err := newCloudflaredEffects(db, driver)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{DB: db, CloudflaredEffects: effects}
	var claimed *model.Operation
	var claim store.OperationClaim
	deadline := time.Now().Add(12 * time.Second)
	for {
		claimed, claim, err = db.ClaimNextOperation(ctx, role+"-host-worker", time.Minute, []string{cloudflaredMutationKind})
		if err != nil {
			t.Fatal(err)
		}
		if claimed != nil {
			break
		}
		if role == "wrong" || time.Now().After(deadline) {
			t.Fatalf("%s did not claim scheduled operation", role)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if role == "wrong" {
		result, execErr := p.ExecuteOperation(ctx, claimed, claim)
		if result != nil || !effect.IsDeferred(execErr) || !strings.Contains(fmt.Sprint(execErr), "another host") {
			t.Fatalf("wrong-host result=%+v err=%v", result, execErr)
		}
		if err := db.DeferClaimedOperation(ctx, claim, "wrong host; retry on accepted host", time.Now().Add(5*time.Second), map[string]interface{}{"externalEffectRecoveryPending": true, "wrongHost": true}); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "wrong-host-deferred", claim.Generation())
		return
	}
	result, execErr := p.ExecuteOperation(ctx, claimed, claim)
	if execErr != nil || result == nil || result.Status != model.OperationSucceeded {
		t.Fatalf("correct-host result=%+v err=%v", result, execErr)
	}
	if err := db.FinishClaimedOperation(ctx, claim, result.Status, result.Message, result.Metadata); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "correct-host-completed", claim.Generation())
}
