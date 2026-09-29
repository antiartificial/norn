package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	nomadapi "github.com/hashicorp/nomad/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// Run against disposable, local Nomad and etcd only. The test creates and
// purges a uniquely named Nomad job and deletes its uniquely named etcd prefix.
//
//	NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14646 NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:12379 \
//	  go test . -run TestEtcdCanaryHTTPPromotesRealNomadDeployment -count=1 -v
func TestEtcdCanaryHTTPPromotesRealNomadDeployment(t *testing.T) {
	address, endpoints := os.Getenv("NORN_TEST_NOMAD_ADDR"), os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if address == "" || endpoints == "" {
		t.Skip("set NORN_TEST_NOMAD_ADDR and NORN_TEST_ETCD_ENDPOINTS for disposable Nomad/etcd qualification")
	}
	for _, endpoint := range append([]string{address}, strings.Split(endpoints, ",")...) {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" || !loopbackHost(parsed.Hostname()) {
			t.Fatalf("disposable qualification requires loopback HTTP endpoints; refused %q", endpoint)
		}
	}
	t.Setenv("NORN_DATABASE_URL", "postgres://poisoned.invalid:1/never-open")
	ctx := context.Background()
	nomadConfig := nomadapi.DefaultConfig()
	nomadConfig.Address = address
	nomadClient, err := nomadapi.NewClient(nomadConfig)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "norn-canary-qual-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	// Register cleanup before the first mutation so a failed initial register
	// cannot leave a partial job behind.
	t.Cleanup(func() { _, _, _ = nomadClient.Jobs().Deregister(jobID, true, nil) })
	jobHCL := func(command string) string {
		return fmt.Sprintf(`job %q {
  datacenters = ["dc1"]
  type = "service"
  group "web" {
    count = 1
    update {
      max_parallel = 1
      canary = 1
      min_healthy_time = "1s"
      healthy_deadline = "30s"
      progress_deadline = "2m"
      auto_promote = false
    }
    task "sleeper" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args = ["-c", %q]
      }
      resources {
        cpu = 50
        memory = 32
      }
    }
  }
}`, jobID, command)
	}
	register := func(command string) {
		t.Helper()
		job, err := nomadClient.Jobs().ParseHCL(jobHCL(command), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := nomadClient.Jobs().Register(job, nil); err != nil {
			t.Fatal(err)
		}
	}
	register("sleep 300")
	waitNomadDeployment(t, nomadClient, jobID, func(d *nomadapi.Deployment) bool { return d.Status == "successful" })
	register("sleep 299")
	canary := waitNomadDeployment(t, nomadClient, jobID, func(d *nomadapi.Deployment) bool {
		for _, state := range d.TaskGroups {
			if len(state.PlacedCanaries) > 0 && state.HealthyAllocs >= len(state.PlacedCanaries) && !state.Promoted && d.Status == "running" {
				return true
			}
		}
		return false
	})

	etcd, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = etcd.Close() })
	prefix := "/norn-tests/canary-real-nomad/" + uuid.NewString()
	t.Cleanup(func() { _, _ = etcd.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-real-nomad-canary-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := etcdstore.NewV3OperationStoreWithPolicy(etcd, prefix, uuid.NewString(), signer, store.AcceptancePolicy{ReplayTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	identities := etcdstore.NewAuthStore(etcd, prefix)
	apps := t.TempDir()
	if err := os.MkdirAll(filepath.Join(apps, jobID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apps, jobID, "infraspec.yaml"), []byte("name: "+jobID+"\ndeploy: true\nprocesses:\n  web:\n    command: sleep 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "norn-etcd-real-nomad-canary-api-secret-000000"
	cfg := &config.Config{AppsDir: apps, NomadAddr: address, APIToken: secret}
	runtime, err := newEtcdCanaryRuntime(cfg, operations)
	if err != nil {
		t.Fatal(err)
	}
	rootID := uuid.NewString()
	issued := time.Now().UTC()
	if err := identities.RecordAccessToken(ctx, &store.AccessToken{JTI: rootID, Subject: "operator", Scopes: []string{handler.ScopeAPIWrite, handler.ScopeAPIRead}, IssuedAt: issued, ExpiresAt: issued.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.With(etcdManagedTokenAuth(cfg, identities, handler.ScopeAPIWrite)).Post("/api/v1/apps/{id}/promote", etcdCanaryPromote(cfg, operations, identities, runtime))
	api := httptest.NewServer(router)
	t.Cleanup(api.Close)
	request := func() (int, model.Operation) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, api.URL+"/api/v1/apps/"+jobID+"/promote", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+signedEtcdCanaryTestToken(t, secret, rootID))
		req.Header.Set("Idempotency-Key", "real-nomad-promotion")
		response, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var op model.Operation
		if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&op); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, op
	}
	status, accepted := request()
	if status != http.StatusAccepted || accepted.ID == "" || accepted.Payload["deploymentId"] != canary.ID {
		t.Fatalf("canary acceptance status=%d operation=%+v deployment=%s", status, accepted, canary.ID)
	}
	status, duplicate := request()
	if status != http.StatusOK || duplicate.ID != accepted.ID {
		t.Fatalf("pre-worker replay status=%d operation=%+v", status, duplicate)
	}
	workerCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go runtime.worker.Run(workerCtx)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		current, err := operations.GetOperation(ctx, accepted.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status.Terminal() {
			if current.Status != model.OperationSucceeded {
				t.Fatalf("canary promotion failed: %+v", current)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	current, err := operations.GetOperation(ctx, accepted.ID)
	if err != nil || current.Status != model.OperationSucceeded {
		t.Fatalf("worker did not finish promotion: %+v err=%v", current, err)
	}
	promoted, _, err := nomadClient.Deployments().Info(canary.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for group, state := range promoted.TaskGroups {
		if !state.Promoted {
			t.Fatalf("Nomad group %s was not promoted: %+v", group, state)
		}
	}
	status, duplicate = request()
	if status != http.StatusOK || duplicate.ID != accepted.ID {
		t.Fatalf("terminal replay status=%d operation=%+v", status, duplicate)
	}
	t.Logf("real Nomad canary deployment %s promoted by accepted operation %s", canary.ID, accepted.ID)
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func waitNomadDeployment(t *testing.T, client *nomadapi.Client, jobID string, ready func(*nomadapi.Deployment) bool) *nomadapi.Deployment {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		deployments, _, err := client.Jobs().Deployments(jobID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		var latest *nomadapi.Deployment
		for _, d := range deployments {
			if latest == nil || d.CreateIndex > latest.CreateIndex {
				latest = d
			}
		}
		if latest != nil && ready(latest) {
			return latest
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Nomad deployment for %s did not reach expected state", jobID)
	return nil
}
