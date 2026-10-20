package offline

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNomadConsulSourceStableScopedHTTPS(t *testing.T) {
	const rules = `namespace "*" { policy = "read" }`
	digest := sha256.Sum256([]byte(rules))
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	mismatch := false
	filtered := false
	targetGone := false
	targetRunning := false
	indexDrift := false
	namespaceReads := 0
	ghostAllocation := false
	nodeCheckCritical := false
	targetAddress := "10.43.0.13"
	targetNetworkIP := "10.43.0.13"
	pages := 0
	nomad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Nomad-Token") != "nomad-read" {
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
			http.Error(w, "denied", 403)
			return
		}
		if indexDrift && r.URL.Path == "/v1/namespaces" && namespaceReads%2 == 1 {
			w.Header().Set("X-Nomad-Index", "11")
		} else {
			w.Header().Set("X-Nomad-Index", "10")
		}
		switch r.URL.Path {
		case "/v1/acl/token/self":
			fmt.Fprint(w, `{"Type":"client","Policies":["workload-read"],"SecretID":"server-echoed-live-secret"}`)
		case "/v1/acl/policy/workload-read":
			fmt.Fprintf(w, `{"Name":"workload-read","Rules":%q}`, rules)
		case "/v1/namespaces":
			namespaceReads++
			fmt.Fprint(w, `[{"Name":"default"},{"Name":"system"}]`)
		case "/v1/allocations":
			if r.URL.Query().Get("namespace") == "system" {
				if targetRunning {
					fmt.Fprint(w, `[{"ID":"active-app-03","Namespace":"system","JobID":"other","NodeID":"node-3","ClientStatus":"running","AllocatedResources":{"Tasks":{"web":{"Cpu":{"CpuShares":100},"Memory":{"MemoryMB":64}}}}}]`)
				} else if ghostAllocation {
					fmt.Fprint(w, `[{"ID":"old-app-03","Namespace":"system","JobID":"former","NodeID":"node-3","ClientStatus":"complete","AllocatedResources":{"Tasks":{"web":{"Cpu":{"CpuShares":100},"Memory":{"MemoryMB":64}}}}}]`)
				} else {
					fmt.Fprint(w, `[]`)
				}
				return
			}
			if r.URL.Query().Get("namespace") != "default" || r.URL.Query().Get("resources") != "true" || r.URL.Query().Get("per_page") != "100" {
				http.Error(w, "bad namespace", 400)
				return
			}
			if r.URL.Query().Get("next_token") == "" {
				w.Header().Set("X-Nomad-NextToken", "next")
				fmt.Fprint(w, `[{"ID":"alloc-1","Namespace":"default","JobID":"hello-norn-mysql","NodeID":"node-1","ClientStatus":"running","AllocatedResources":{"Tasks":{"web":{"Cpu":{"CpuShares":200},"Memory":{"MemoryMB":128}}}}}]`)
			} else {
				pages++
				fmt.Fprint(w, `[{"ID":"alloc-2","Namespace":"default","JobID":"hello-norn-mysql","NodeID":"node-2","ClientStatus":"running","AllocatedResources":{"Tasks":{"web":{"Cpu":{"CpuShares":200},"Memory":{"MemoryMB":128}}}}}]`)
			}
		case "/v1/nodes":
			if r.URL.Query().Get("per_page") != "100" {
				http.Error(w, "missing pagination", 400)
				return
			}
			if targetGone {
				fmt.Fprint(w, `[{"ID":"node-1","Name":"app-01"},{"ID":"node-2","Name":"app-02"}]`)
			} else {
				fmt.Fprint(w, `[{"ID":"node-1","Name":"app-01"},{"ID":"node-2","Name":"app-02"},{"ID":"node-3","Name":"app-03"}]`)
			}
		case "/v1/node/node-1", "/v1/node/node-2", "/v1/node/node-3":
			id := strings.TrimPrefix(r.URL.Path, "/v1/node/")
			num := strings.TrimPrefix(id, "node-")
			address, networkIP := fmt.Sprintf("10.43.0.1%s", num), fmt.Sprintf("10.43.0.1%s", num)
			if id == "node-3" {
				address, networkIP = targetAddress, targetNetworkIP
			}
			fmt.Fprintf(w, `{"ID":%q,"Name":%q,"Address":%q,"Status":"ready","SchedulingEligibility":"eligible","NodeResources":{"Cpu":{"CpuShares":1000},"Memory":{"MemoryMB":1024},"Networks":[{"IP":%q}]},"ReservedResources":{"Cpu":{"CpuShares":100},"Memory":{"MemoryMB":100}}}`, id, "app-0"+num, address, networkIP)
		default:
			http.Error(w, "unknown", 404)
		}
	}))
	defer nomad.Close()
	consul := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Consul-Token") != "consul-read" || r.URL.Path != "/v1/health/service/hello-norn-mysql-web" || r.URL.Query().Get("passing") != "false" || r.URL.Query().Get("consistent") != "true" {
			t.Errorf("unexpected Consul request %s", r.URL)
			http.Error(w, "denied", 403)
			return
		}
		w.Header().Set("X-Consul-Index", "23")
		if filtered {
			w.Header().Set("X-Consul-Results-Filtered-By-ACLs", "true")
		}
		status := "passing"
		if mismatch {
			status = "critical"
		}
		nodeStatus := "passing"
		if nodeCheckCritical {
			nodeStatus = "critical"
		}
		fmt.Fprintf(w, `[{"Service":{"Service":"hello-norn-mysql-web","Meta":{"norn_alloc_id":"alloc-1","norn_node_id":"node-1","norn_job_id":"hello-norn-mysql","norn_namespace":"default"}},"Checks":[{"CheckID":"node-check","Name":"serfHealth","Status":%q},{"CheckID":"a-ready","Name":"readyz","Status":"passing"},{"CheckID":"a-health","Name":"healthz","Status":"passing"}]},{"Service":{"Service":"hello-norn-mysql-web","Meta":{"norn_alloc_id":"alloc-2","norn_node_id":"node-2","norn_job_id":"hello-norn-mysql","norn_namespace":"default"}},"Checks":[{"CheckID":"b-ready","Name":"readyz","Status":%q},{"CheckID":"b-health","Name":"healthz","Status":"passing"}]}]`, nodeStatus, status)
	}))
	defer consul.Close()
	roots := func(s *httptest.Server) *x509.CertPool { p := x509.NewCertPool(); p.AddCert(s.Certificate()); return p }
	source := NomadConsulSource{NomadURL: nomad.URL, ConsulURL: consul.URL, NomadToken: "nomad-read", ConsulToken: "consul-read", NomadServerName: "example.com", ConsulServerName: "example.com", NomadRoots: roots(nomad), ConsulRoots: roots(consul), PolicyName: "workload-read", PolicySHA256: hex.EncodeToString(digest[:]), Now: func() time.Time { return now }}
	// A mismatched TLS name fails before evidence, even with the right CA.
	source.NomadServerName = "wrong.invalid"
	if _, err := source.Snapshot(context.Background()); err == nil {
		t.Fatal("wrong TLS identity accepted")
	}
	source.NomadServerName = "example.com"
	evidence, err := ObserveWorkload(context.Background(), source, now)
	if err != nil || !evidence.AllNamespacesInspected || pages != 2 {
		t.Fatalf("valid paginated observation: %+v %v pages=%d", evidence, err, pages)
	}
	effect, err := ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13")
	if err != nil || !effect.WorkloadEvidence.App03Empty || effect.EvidenceSHA256 == effect.WorkloadEvidence.SHA256 || len(effect.RawEvidence) == 0 || len(effect.RawEvidence) > maxTerminalWorkloadRawBytes {
		t.Fatalf("valid effect observation: %+v %v", effect, err)
	}
	if !strings.Contains(string(effect.RawEvidence), `"schema":"norn.fleet-effect-workload-reads/v2"`) ||
		!strings.Contains(string(effect.RawEvidence), `"targetNodeId":"node-3"`) ||
		!strings.Contains(string(effect.RawEvidence), `"targetPrivateIp":"10.43.0.13"`) ||
		!strings.Contains(string(effect.RawEvidence), `"/v1/allocations"`) ||
		!strings.Contains(string(effect.RawEvidence), `"/v1/acl/token/self"`) ||
		strings.Contains(string(effect.RawEvidence), "nomad-read") || strings.Contains(string(effect.RawEvidence), "consul-read") ||
		strings.Contains(string(effect.RawEvidence), "server-echoed-live-secret") ||
		!strings.Contains(string(effect.RawEvidence), `"redacted":true`) {
		t.Fatal("effect transcript missing independent reads or exposes bearer")
	}
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-wrong", "10.43.0.13"); err == nil {
		t.Fatal("wrong pinned Nomad target ID accepted")
	}
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "", "10.43.0.13"); err == nil {
		t.Fatal("missing pinned Nomad target ID accepted")
	}
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", ""); err == nil {
		t.Fatal("missing pinned provider private IP accepted")
	}
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.99"); err == nil {
		t.Fatal("wrong pinned provider private IP accepted")
	}
	targetAddress = ""
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("missing Nomad target address accepted")
	}
	targetAddress = "10.43.0.13"
	targetNetworkIP = "10.43.0.99"
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("ambiguous Nomad target network addresses accepted")
	}
	targetNetworkIP = "10.43.0.13"
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now.Add(31*time.Second), "node-3", "10.43.0.13"); err == nil {
		t.Fatal("stale effect observation accepted")
	}
	targetRunning = true
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("live app-03 allocation accepted for effect")
	}
	targetRunning = false
	indexDrift = true
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("namespace index drift accepted for effect")
	}
	indexDrift = false
	mismatch = true
	if _, err = ObserveWorkload(context.Background(), source, now); err == nil {
		t.Fatal("failing Consul check accepted")
	}
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("failing Consul check accepted for effect")
	}
	filtered = true
	mismatch = false
	if _, err = source.Snapshot(context.Background()); err == nil {
		t.Fatal("ACL-filtered Consul response accepted")
	}
	source.PolicySHA256 = strings.Repeat("0", 64)
	if _, err = source.Snapshot(context.Background()); err == nil {
		t.Fatal("unreviewed policy accepted")
	}
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("unreviewed policy accepted for effect")
	}
	source.PolicySHA256 = hex.EncodeToString(digest[:])
	filtered = false
	if _, err = ObserveTerminalNomadConsul(context.Background(), source, now); err == nil {
		t.Fatal("terminal proof accepted a still-present app-03 node")
	}
	targetGone = true
	if _, err = ObserveEffectNomadConsul(context.Background(), source, now, "node-3", "10.43.0.13"); err == nil {
		t.Fatal("effect proof accepted missing app-03 node")
	}
	terminal, err := ObserveTerminalNomadConsul(context.Background(), source, now)
	if err != nil || !terminal.WorkloadEvidence.App03Empty || len(terminal.RawEvidence) == 0 || terminal.EvidenceSHA256 == terminal.WorkloadEvidence.SHA256 {
		t.Fatalf("terminal workload observation: %+v %v", terminal, err)
	}
	if !strings.Contains(string(terminal.RawEvidence), `"/v1/allocations"`) || strings.Contains(string(terminal.RawEvidence), "nomad-read") {
		t.Fatal("raw evidence lacks allocation reads or leaks bearer token")
	}
	if strings.Contains(string(terminal.RawEvidence), "server-echoed-live-secret") {
		t.Fatal("terminal transcript leaked Nomad bearer")
	}
	ghostAllocation = true
	if _, err = ObserveTerminalNomadConsul(context.Background(), source, now); err == nil {
		t.Fatal("historical allocation on removed node accepted in another namespace")
	}
	ghostAllocation = false
	nodeCheckCritical = true
	if _, err = ObserveTerminalNomadConsul(context.Background(), source, now); err == nil {
		t.Fatal("failing Consul node health accepted")
	}
	nodeCheckCritical = false
	targetGone = false
	source.Now = time.Now
	if _, err = ObserveEffectNomadConsul(context.Background(), source, time.Now(), "node-3", "10.43.0.13"); err != nil {
		t.Fatalf("effect observation with real advancing clock: %v", err)
	}
	targetGone = true
	if _, err = ObserveTerminalNomadConsul(context.Background(), source, time.Now()); err != nil {
		t.Fatalf("terminal observation with real advancing clock: %v", err)
	}
}
