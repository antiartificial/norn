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
	pages := 0
	nomad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Nomad-Token") != "nomad-read" {
			t.Errorf("unexpected Nomad request %s %s", r.Method, r.URL)
			http.Error(w, "denied", 403)
			return
		}
		w.Header().Set("X-Nomad-Index", "10")
		switch r.URL.Path {
		case "/v1/acl/token/self":
			fmt.Fprint(w, `{"Type":"client","Policies":[{"Name":"workload-read"}]}`)
		case "/v1/acl/policy/workload-read":
			fmt.Fprintf(w, `{"Name":"workload-read","Rules":%q}`, rules)
		case "/v1/namespaces":
			if r.URL.Query().Get("next_token") == "" {
				w.Header().Set("X-Nomad-NextToken", "next")
				fmt.Fprint(w, `[{"Name":"default"}]`)
			} else {
				pages++
				fmt.Fprint(w, `[{"Name":"system"}]`)
			}
		case "/v1/allocations":
			if r.URL.Query().Get("namespace") == "system" {
				fmt.Fprint(w, `[]`)
				return
			}
			if r.URL.Query().Get("namespace") != "default" {
				http.Error(w, "bad namespace", 400)
				return
			}
			if r.URL.Query().Get("next_token") == "" {
				w.Header().Set("X-Nomad-NextToken", "next")
				fmt.Fprint(w, `[{"ID":"alloc-1","Namespace":"default","JobID":"hello-norn-mysql","NodeID":"node-1","ClientStatus":"running","AllocatedResources":{"Tasks":{"web":{"Cpu":{"CpuShares":200},"Memory":{"MemoryMB":128}}}}}]`)
			} else {
				fmt.Fprint(w, `[{"ID":"alloc-2","Namespace":"default","JobID":"hello-norn-mysql","NodeID":"node-2","ClientStatus":"running","AllocatedResources":{"Tasks":{"web":{"Cpu":{"CpuShares":200},"Memory":{"MemoryMB":128}}}}}]`)
			}
		case "/v1/nodes":
			fmt.Fprint(w, `[{"ID":"node-1","Name":"app-01"},{"ID":"node-2","Name":"app-02"},{"ID":"node-3","Name":"app-03"}]`)
		case "/v1/node/node-1", "/v1/node/node-2", "/v1/node/node-3":
			id := strings.TrimPrefix(r.URL.Path, "/v1/node/")
			num := strings.TrimPrefix(id, "node-")
			fmt.Fprintf(w, `{"ID":%q,"Name":%q,"Status":"ready","SchedulingEligibility":"eligible","NodeResources":{"Cpu":{"CpuShares":1000},"Memory":{"MemoryMB":1024}},"ReservedResources":{"Cpu":{"CpuShares":100},"Memory":{"MemoryMB":100}}}`, id, "app-0"+num)
		default:
			http.Error(w, "unknown", 404)
		}
	}))
	defer nomad.Close()
	consul := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Consul-Token") != "consul-read" || r.URL.Path != "/v1/health/service/hello-norn-mysql-web" || r.URL.Query().Get("passing") != "false" {
			t.Errorf("unexpected Consul request %s", r.URL)
			http.Error(w, "denied", 403)
			return
		}
		status := "passing"
		if mismatch {
			status = "critical"
		}
		fmt.Fprintf(w, `[{"Service":{"Service":"hello-norn-mysql-web","Meta":{"norn_alloc_id":"alloc-1","norn_node_id":"node-1","norn_job_id":"hello-norn-mysql","norn_namespace":"default"}},"Checks":[{"CheckID":"a-ready","Name":"readyz","Status":"passing"},{"CheckID":"a-health","Name":"healthz","Status":"passing"}]},{"Service":{"Service":"hello-norn-mysql-web","Meta":{"norn_alloc_id":"alloc-2","norn_node_id":"node-2","norn_job_id":"hello-norn-mysql","norn_namespace":"default"}},"Checks":[{"CheckID":"b-ready","Name":"readyz","Status":%q},{"CheckID":"b-health","Name":"healthz","Status":"passing"}]}]`, status)
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
	mismatch = true
	if _, err = ObserveWorkload(context.Background(), source, now); err == nil {
		t.Fatal("failing Consul check accepted")
	}
	source.PolicySHA256 = strings.Repeat("0", 64)
	if _, err = source.Snapshot(context.Background()); err == nil {
		t.Fatal("unreviewed policy accepted")
	}
}
