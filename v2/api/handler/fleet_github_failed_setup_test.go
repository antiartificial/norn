package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/githubapp"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// This uses a redacted capture of run 39104495159. The run-name nonce is
// synthetic; every job, step, runner, and artifact identity is the observed
// GitHub REST shape. Only the protected Norn binding carries its nonce hash.
func TestReconcileAbandonedFailedRunnerSetupSignsTerminalReceipt(t *testing.T) {
	const planID = "dab170e6-5677-4187-abc7-c0bf195775a6"
	const runID int64 = 39104495159
	const headSHA = "602a3867eb590341218efb93295d7afe018a842a"
	ctx := context.Background()
	p := newPGFenceHarness(t)
	reg := p.RegisterTarget(lifecycle.TargetIdentity{Provider: "digitalocean", ProviderAccount: "failed-setup", StateBackend: "s3://failed-setup/state"}, []string{"cluster:failed-setup"})
	if reg.HTTPStatus != http.StatusCreated {
		t.Fatalf("register: %+v", reg)
	}
	now := time.Now().UTC()
	plan := fleet.CapacityPlan{SchemaVersion: "norn.fleet-capacity-plan/v1", ID: planID, Cluster: "failed-setup", Pool: "app", Current: fleet.NodePool{Desired: 3}, Proposed: fleet.NodePool{Desired: 3}, Action: "scale", SourceDigest: "sha256:" + strings.Repeat("1", 64)}
	p.signCapacityPlan(&plan)
	rawPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rawPlan, &payload); err != nil {
		t.Fatal(err)
	}
	if err := p.db.InsertCompletedOperation(ctx, &model.Operation{ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded, Payload: payload, StartedAt: now, UpdatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	p.planEnv[planID], p.planCluster[planID] = "staging/nyc3", "failed-setup"

	nonce := strings.Repeat("a", 64)
	nonceSum := sha256.Sum256([]byte(nonce))
	nonceHash := hex.EncodeToString(nonceSum[:])
	if _, err := p.db.CreateFleetGitHubDispatch(ctx, store.FleetGitHubDispatch{PlanID: planID, PlanRunID: 91, PlanSHA256: strings.Repeat("c", 64), ApprovedHeadSHA: headSHA, FleetEnvironment: "staging/nyc3", DispatchNonceSHA256: nonceHash, DispatchState: "prepared"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.MarkFleetGitHubDispatchSubmitting(ctx, planID, nonceHash); err != nil {
		t.Fatal(err)
	}
	const receiptID = "failed-setup-reconcile-acceptance"
	if err := p.db.ReserveMutationAudit(ctx, &store.MutationAuditEvent{ID: receiptID, RequestID: receiptID + "-request", PrincipalSubject: "operator", Method: http.MethodPost, Path: "/api/v1/fleet/plans/{planID}/github/dispatch", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	reservationRequest := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/dispatch", nil)
	reservationRequest = withOperationAcceptanceRequestContext(reservationRequest, operationAcceptanceRequestContext{ReceiptID: receiptID, RequestID: receiptID + "-request", Actor: verifiedOperationActor{Issuer: "https://access.example.test", Subject: "operator", CredentialID: "token", DeviceID: "device", Source: string(AccessPrincipalSourceManagedToken), Scopes: []string{ScopeAPIWrite}}})
	principal := AccessPrincipal{Subject: "operator", TokenID: "token", DeviceID: "device", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeAPIWrite}}
	reserved, err := p.h.reserveFleetGitHubOperation(reservationRequest, principal, planID, "fleet.github.apply-dispatch", map[string]interface{}{"planId": planID})
	if err != nil || reserved.Status != model.OperationQueued {
		t.Fatalf("reservation=%+v err=%v", reserved, err)
	}
	p.AgeHolder(fleettest.PlanRef{ID: planID}, lifecycle.AbandonMinimumAge+time.Hour)
	if abandoned := p.AbandonPlan(fleettest.PlanRef{ID: planID}); abandoned.HTTPStatus != http.StatusOK && abandoned.HTTPStatus != http.StatusCreated {
		t.Fatalf("abandon: %+v", abandoned)
	}

	configPath := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(configPath, []byte("apiVersion: norn.dev/fleet/v1\nkind: Cluster\nmetadata: {environment: staging}\ncluster: {name: failed-setup, provider: digitalocean, region: nyc3}\nnodePools:\n  app:\n    size: s-4vcpu-8gb\n    min: 2\n    desired: 3\n    max: 8\n    replacement: {strategy: blueGreen, requireCapacityHeadroom: true, requireReadiness: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.h.cfg.FleetConfig = configPath
	fixtures := map[string][]byte{}
	for _, name := range []string{"run", "jobs", "artifacts"} {
		fixtures[name], err = os.ReadFile(filepath.Join("testdata", "failed_setup_39104495159_"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"slug":"norn-work-staging-fleet"}`)
		case "/app/installations/1/access_tokens":
			fmt.Fprint(w, `{"token":"installation-token","expires_at":"2027-01-15T09:00:00Z"}`)
		case "/repos/antiartificial/norn-fleet/actions/runs/39104495159":
			_, _ = w.Write(fixtures["run"])
		case "/repos/antiartificial/norn-fleet/actions/runs/39104495159/jobs":
			_, _ = w.Write(fixtures["jobs"])
		case "/repos/antiartificial/norn-fleet/actions/runs/39104495159/artifacts":
			_, _ = w.Write(fixtures["artifacts"])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	p.h.fleetGitHub, err = githubapp.New(githubapp.Config{AppID: "9999", InstallationID: 1, PrivateKeyFile: keyPath, Repository: "antiartificial/norn-fleet", DefaultBranch: "main", Environment: "staging", ConfigPath: "environments/staging/nyc3/cluster.yaml", PlanWorkflow: "plan.yml", ApplyWorkflow: "apply.yml", APIBaseURL: gh.URL}, gh.Client())
	if err != nil {
		t.Fatal(err)
	}
	p.h.fleetGitHubConfigError = nil

	post := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(fleetGitHubReconcileRequest{Kind: "apply-dispatch", ExactRunID: runID, ExactRunAttempt: 1})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/plans/"+planID+"/github/reconcile", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = WithAccessPrincipal(req, &principal)
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec
	}
	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("reconcile=%d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Operation model.Operation `json:"operation"`
		Outcome   string          `json:"outcome"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != "verified-failed-runner-setup-before-execution" || response.Operation.Status != model.OperationCanceled || response.Operation.Receipt == nil {
		t.Fatalf("terminal response=%+v", response)
	}
	if completion, ok := response.Operation.Metadata["fleetGitHubCompletion"].(map[string]interface{}); !ok || completion["signature"] == "" || completion["schema"] != "norn.fleet-github-completion/v1" {
		t.Fatalf("unsigned completion=%+v", response.Operation.Metadata)
	}
	terminal, err := p.db.GetOperation(ctx, reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal.AttachReceipt()
	if terminal.Status != model.OperationCanceled || terminal.Receipt == nil {
		t.Fatalf("queued reservation did not become signed terminal operation: status=%s receipt=%t", terminal.Status, terminal.Receipt != nil)
	}
	attempts, err := p.db.ListFleetRunnerAttempts(ctx, planID, 1)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("failed setup created a runner attempt=%+v err=%v", attempts, err)
	}
	stored, err := p.db.GetFleetGitHubDispatch(ctx, planID)
	if err != nil || stored.DispatchState != "submitting" || stored.RunID != 0 {
		t.Fatalf("dispatch rebound=%+v err=%v", stored, err)
	}
	if again := post(); again.Code != http.StatusOK || !bytes.Contains(again.Body.Bytes(), []byte("verified-failed-runner-setup-before-execution")) {
		t.Fatalf("terminal replay=%d %s", again.Code, again.Body.String())
	}
}
