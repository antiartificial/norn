package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/internal/integrationtest"
)

// TestFleetResourceRoutesRegistered fails if production stops registering any
// resource route on the PG authority-only router or the etcd runtime (Q6).
func TestFleetResourceRoutesRegistered(t *testing.T) {
	requireRoutesRegistered(t, "../main.go", pgFleetResourceRoutes, func(route [3]string) string {
		return fmt.Sprintf("r.%s(%q, fleetResources.%s)", route[0], route[1], route[2])
	})
	requireRoutesRegistered(t, "../etcd_fleet_resources.go", etcdFleetResourceRoutes, func(route [4]string) string {
		return fmt.Sprintf("router.With(%s).%s(%q, fleetResources.%s)", route[3], route[0], route[1], route[2])
	})
	requireRoutesRegistered(t, "../etcd_fleet_runtime.go", []string{"registerEtcdFleetResourceRoutes(router, cfg, identities, operations, fleetGitHub, liveness)"}, func(line string) string { return line })
}

// TestFleetResourceRoutesPostgres drives the real PG route mount
// (Handler.FleetResourceRoutes) against the real fleet_resources tables: a
// registered target, an environment-bound CI observation, an operator-
// declared desired revision with the expectedRevision CAS, a real reconcile,
// and the single read that explains the record.
func TestFleetResourceRoutesPostgres(t *testing.T) {
	db := integrationtest.PG(t)
	p := newPGLifecycleHarness(t, db)
	p.h.cfg.GitHubActionsFleetAllowedRepository = pgConformanceRepository + "@1@2"
	// The lane (staging/nyc3) comes from the configured Fleet root, and its
	// workflows run in the "staging" GitHub environment.
	fleetConfig := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(fleetConfig, []byte(`apiVersion: norn.dev/fleet/v1
kind: Cluster
metadata: {environment: staging}
cluster: {name: staging-nyc3, provider: digitalocean, region: nyc3}
nodePools:
  app:
    size: s-4vcpu-8gb
    min: 2
    desired: 3
    max: 8
    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p.h.cfg.FleetConfig, p.h.cfg.FleetGitHubEnvironment = fleetConfig, "staging"
	reconciler := &controller.Reconciler{Store: db}
	p.h.SetFleetControllerLiveness(reconciler)
	mountFleetTargetRoutes(p.router, p.h.FleetTargetRoutes())
	mountFleetResourceRoutes(p.router, p.h.FleetResourceRoutes())
	ctx := context.Background()

	target := fleetTargetRegisterResp(p.router, lifecycle.TargetIdentity{Provider: "digitalocean", ProviderAccount: "acct", StateBackend: "s3://bucket/state"}, []string{"cluster:res-routes", "environment:staging/nyc3"})
	if target.HTTPStatus != http.StatusCreated || target.TargetID == "" {
		t.Fatalf("register target: %+v", target)
	}
	observer := observerPrincipal()
	observer.CI.Repository, observer.CI.RepositoryID, observer.CI.RepositoryOwnerID = pgConformanceRepository, "1", "2"

	if rec := fleetTargetDo(p.router, http.MethodPost, resourcePath("prod"), platformPrincipal(), "", map[string]string{"targetId": "tgt_" + strings.Repeat("0", 64)}); rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered target: %d %s", rec.Code, rec.Body)
	}
	if rec := fleetTargetDo(p.router, http.MethodPost, resourcePath("prod"), platformPrincipal(), "", map[string]string{"targetId": target.TargetID}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := fleetTargetDo(p.router, http.MethodPost, resourcePath("prod"), platformPrincipal(), "", map[string]string{"targetId": target.TargetID}); rec.Code != http.StatusOK {
		t.Fatalf("idempotent create: %d %s", rec.Code, rec.Body)
	}
	fresh := decodeResource(t, fleetTargetDo(p.router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil))
	if fresh.Revision != 0 || fresh.Desired.Generation != 0 || fresh.Controller.State != "unknown" || fresh.ApprovalPolicy.Basis != "none" {
		t.Fatalf("fresh resource = %+v", fresh)
	}

	// Observation: only the observe intent, bound to the target by alias.
	intentApply := *observer.CI
	intentApply.Intent = "apply"
	if rec := appendObservation(p.router, &AccessPrincipal{Subject: "ci", Scopes: []string{ScopeFleetOperate}, CI: &intentApply}, "prod", okObservation(time.Now())); rec.Code != http.StatusForbidden {
		t.Fatalf("apply intent observation: %d %s", rec.Code, rec.Body)
	}
	rec := appendObservation(p.router, observer, "prod", okObservation(time.Now().Add(-time.Minute)))
	var appended FleetObservationAppended
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &appended) != nil || appended.Result != "applied" {
		t.Fatalf("observation: %d %s", rec.Code, rec.Body)
	}
	if rec := appendObservation(p.router, observer, "prod", okObservation(time.Now().Add(-time.Hour))); !strings.Contains(rec.Body.String(), `"result":"superseded"`) {
		t.Fatalf("older observation: %d %s", rec.Code, rec.Body)
	}
	if rec := appendObservation(p.router, observer, "prod", okObservation(time.Now().Add(10*time.Minute))); rec.Code != http.StatusBadRequest {
		t.Fatalf("future observation: %d %s", rec.Code, rec.Body)
	}

	// Desired (no GitHub App on this handler: operator-declared) with CAS.
	sha := strings.Repeat("d", 40)
	desired := func(expected int64) int {
		return fleetTargetDo(p.router, http.MethodPost, resourcePath("prod")+"/desired", writerPrincipal(), "", map[string]interface{}{"commitSha": sha, "expectedRevision": expected}).Code
	}
	if code := desired(0); code != http.StatusOK {
		t.Fatalf("desired: %d", code)
	}
	if code := desired(0); code != http.StatusConflict {
		t.Fatalf("stale desired: %d", code)
	}
	if code := desired(1); code != http.StatusOK {
		t.Fatalf("desired at revision 1: %d", code)
	}

	// Reconcile for real, run one rescan, and read the whole record.
	if _, err := db.ReconcileFleetResource(ctx, "prod", controller.DeriveStatus, nil); err != nil {
		t.Fatal(err)
	}
	reconciler.Step(ctx)
	view := decodeResource(t, fleetTargetDo(p.router, http.MethodGet, resourcePath("prod"), readerPrincipal(), "", nil))
	runtimeReady := ""
	for _, c := range view.Status.Conditions {
		if c.Type == controller.ConditionRuntimeReady {
			runtimeReady = c.Status
		}
	}
	if view.TargetID != target.TargetID || view.Desired.Generation != 2 || view.Desired.CommitSHA != sha || view.Desired.Verification != controller.VerificationOperatorDeclared ||
		runtimeReady != controller.StatusTrue || view.Observed[controller.SourceRuntime].Sequence != 1 || view.Status.EvaluatedAt == nil ||
		view.Status.NextAction != controller.NextActionRefreshObservations || view.Blocker == nil || view.Blocker.Reason != controller.ReasonDesiredNotApplied ||
		view.Controller.State != "running" || view.Controller.LastRescanAt == nil || view.ApprovalPolicy.ProtectedBranch || view.ApprovalPolicy.AuthorizedDispatch || view.ApprovalPolicy.EnvironmentReviewers != "not_enforced" {
		t.Fatalf("record = %+v", view)
	}
	var list FleetResourceList
	if rec := fleetTargetDo(p.router, http.MethodGet, "/api/v1/fleet/resources", readerPrincipal(), "", nil); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].Name != "prod" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if got, err := db.ListFleetObservations(ctx, "prod", 10); err != nil || len(got) != 2 {
		t.Fatalf("stored observations = %d %v", len(got), err)
	}
	if _, err := db.GetFleetResource(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
}
