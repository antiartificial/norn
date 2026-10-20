package offline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// NomadConsulSource is a read-only, credential-scoped source for WorkloadSnapshot.
// PolicySHA256 pins a separately reviewed, dedicated Nomad policy. This adapter
// does not prove that a separate token or an unobserved policy has no access.
type NomadConsulSource struct {
	NomadURL, ConsulURL               string
	NomadToken, ConsulToken           string
	NomadServerName, ConsulServerName string
	NomadRoots, ConsulRoots           *x509.CertPool
	PolicyName, PolicySHA256          string
	Now                               func() time.Time
	recorder                          *workloadRawRecorder
}

var wildcardRead = regexp.MustCompile(`(?s)namespace\s+"\*"\s*\{[^{}]*\bpolicy\s*=\s*"read"[^{}]*\}`)
var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s NomadConsulSource) Snapshot(ctx context.Context) (WorkloadSnapshot, error) {
	if ctx == nil || ctx.Err() != nil || s.NomadToken == "" || s.ConsulToken == "" || s.PolicyName == "" || !hexDigest.MatchString(s.PolicySHA256) {
		return WorkloadSnapshot{}, ErrWorkloadObservation
	}
	nomad, err := s.client(s.NomadURL, s.NomadServerName, s.NomadRoots)
	if err != nil {
		return WorkloadSnapshot{}, err
	}
	consul, err := s.client(s.ConsulURL, s.ConsulServerName, s.ConsulRoots)
	if err != nil {
		return WorkloadSnapshot{}, err
	}
	if err = s.provePolicy(ctx, nomad); err != nil {
		return WorkloadSnapshot{}, err
	}
	first, err := s.scan(ctx, nomad, consul)
	if err != nil {
		return WorkloadSnapshot{}, err
	}
	second, err := s.scan(ctx, nomad, consul)
	if err != nil {
		return WorkloadSnapshot{}, err
	}
	if first.namespaceIndex == 0 || first.namespaceIndex != second.namespaceIndex || first.nodeIndex == 0 || first.nodeIndex != second.nodeIndex || first.consulIndex == 0 || first.consulIndex != second.consulIndex || !bytes.Equal(first.canonical, second.canonical) {
		return WorkloadSnapshot{}, ErrWorkloadObservation
	}
	for name, index := range first.allocationIndices {
		if index == 0 || index != second.allocationIndices[name] {
			return WorkloadSnapshot{}, ErrWorkloadObservation
		}
	}
	if err = s.provePolicy(ctx, nomad); err != nil {
		return WorkloadSnapshot{}, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	result := first.snapshot
	result.ObservedAt = now().UTC()
	result.FullNamespaceAccess = true
	result.ACLPolicySHA256 = s.PolicySHA256
	result.NamespaceListIndexBefore = first.namespaceIndex
	result.NamespaceListIndexAfter = second.namespaceIndex
	for i := range result.NamespaceReads {
		result.NamespaceReads[i].QueryIndexAfter = second.allocationIndices[result.NamespaceReads[i].Name]
	}
	return result, nil
}

func (s NomadConsulSource) provePolicy(ctx context.Context, nomad *http.Client) error {
	var self struct {
		Type     string
		Policies []string
		Roles    []struct{ ID string }
	}
	if _, err := s.read(ctx, nomad, s.NomadURL, "/v1/acl/token/self", nil, s.NomadToken, "X-Nomad-Token", &self); err != nil {
		return err
	}
	if self.Type != "client" || len(self.Policies) != 1 || self.Policies[0] != s.PolicyName || len(self.Roles) != 0 {
		return ErrWorkloadObservation
	}
	var policy struct{ Name, Rules string }
	if _, err := s.read(ctx, nomad, s.NomadURL, "/v1/acl/policy/"+url.PathEscape(s.PolicyName), nil, s.NomadToken, "X-Nomad-Token", &policy); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(policy.Rules))
	if policy.Name != s.PolicyName || hex.EncodeToString(digest[:]) != s.PolicySHA256 || !wildcardRead.MatchString(policy.Rules) {
		return ErrWorkloadObservation
	}
	return nil
}

func (s NomadConsulSource) client(raw, serverName string, roots *x509.CertPool) (*http.Client, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || serverName == "" || roots == nil {
		return nil, ErrWorkloadObservation
	}
	// Every query uses the injected CA and exact TLS server identity.
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName}}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (s NomadConsulSource) read(ctx context.Context, client *http.Client, base, path string, q url.Values, token, header string, dst any) (http.Header, error) {
	endpoint := base + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrWorkloadObservation
	}
	req.Header.Set(header, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read-only workload query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrWorkloadObservation
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		return nil, ErrWorkloadObservation
	}
	if rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(body))) != nil {
		return nil, ErrWorkloadObservation
	}
	if err = json.Unmarshal(body, dst); err != nil {
		return nil, ErrWorkloadObservation
	}
	if s.recorder != nil {
		if err := s.recorder.append(req.URL, resp.Header, body); err != nil {
			return nil, ErrWorkloadObservation
		}
	}
	return resp.Header, nil
}

type workloadScan struct {
	snapshot          WorkloadSnapshot
	namespaceIndex    uint64
	nodeIndex         uint64
	consulIndex       uint64
	allocationIndices map[string]uint64
	canonical         []byte
}

func (s NomadConsulSource) scan(ctx context.Context, nomad, consul *http.Client) (workloadScan, error) {
	var out workloadScan
	var namespaces []struct{ Name string }
	idx, err := s.pages(ctx, nomad, s.NomadURL, "/v1/namespaces", nil, s.NomadToken, "X-Nomad-Token", &namespaces)
	if err != nil {
		return out, err
	}
	out.namespaceIndex = idx
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i].Name < namespaces[j].Name })
	out.allocationIndices = map[string]uint64{}
	nodeIDs := map[string]bool{}
	type alloc struct {
		ID, Namespace, JobID, NodeID, ClientStatus string
		AllocatedResources                         struct {
			Tasks map[string]struct {
				Cpu    struct{ CpuShares int }
				Memory struct{ MemoryMB int }
			}
		}
	}
	for _, ns := range namespaces {
		if ns.Name == "" || ns.Name == "*" {
			return out, ErrWorkloadObservation
		}
		out.snapshot.Namespaces = append(out.snapshot.Namespaces, ns.Name)
		var allocations []alloc
		index, e := s.pages(ctx, nomad, s.NomadURL, "/v1/allocations", url.Values{"namespace": {ns.Name}, "resources": {"true"}, "per_page": {"100"}}, s.NomadToken, "X-Nomad-Token", &allocations)
		if e != nil {
			return out, e
		}
		out.allocationIndices[ns.Name] = index
		out.snapshot.NamespaceReads = append(out.snapshot.NamespaceReads, WorkloadNamespaceRead{Name: ns.Name, QueryIndexBefore: index, AllocationCount: len(allocations)})
		for _, a := range allocations {
			if a.Namespace != ns.Name || a.ID == "" || a.NodeID == "" || a.ClientStatus == "" {
				return out, ErrWorkloadObservation
			}
			cpu, mem := 0, 0
			for _, task := range a.AllocatedResources.Tasks {
				cpu += task.Cpu.CpuShares
				mem += task.Memory.MemoryMB
			}
			if len(a.AllocatedResources.Tasks) == 0 {
				return out, ErrWorkloadObservation
			}
			out.snapshot.Allocations = append(out.snapshot.Allocations, WorkloadAllocation{ID: a.ID, Namespace: a.Namespace, Job: a.JobID, Node: a.NodeID, Status: a.ClientStatus, CPU: cpu, MemoryMB: mem})
			nodeIDs[a.NodeID] = true
		}
	}
	var nodes []struct{ ID, Name, Status, SchedulingEligibility string }
	if out.nodeIndex, err = s.pages(ctx, nomad, s.NomadURL, "/v1/nodes", url.Values{"per_page": {"100"}}, s.NomadToken, "X-Nomad-Token", &nodes); err != nil {
		return out, err
	}
	idToName := map[string]string{}
	for _, n := range nodes {
		if n.ID == "" || n.Name == "" {
			return out, ErrWorkloadObservation
		}
		idToName[n.ID] = n.Name
		var info struct {
			ID, Name, Status, SchedulingEligibility string
			NodeResources                           struct {
				Cpu    struct{ CpuShares int }
				Memory struct{ MemoryMB int }
			}
			ReservedResources struct {
				Cpu    struct{ CpuShares int }
				Memory struct{ MemoryMB int }
			}
		}
		if _, err = s.read(ctx, nomad, s.NomadURL, "/v1/node/"+url.PathEscape(n.ID), nil, s.NomadToken, "X-Nomad-Token", &info); err != nil {
			return out, err
		}
		if info.ID != n.ID || info.Name != n.Name || info.NodeResources.Cpu.CpuShares <= 0 || info.NodeResources.Memory.MemoryMB <= 0 {
			return out, ErrWorkloadObservation
		}
		freeCPU := info.NodeResources.Cpu.CpuShares - info.ReservedResources.Cpu.CpuShares
		freeMem := info.NodeResources.Memory.MemoryMB - info.ReservedResources.Memory.MemoryMB
		for _, a := range out.snapshot.Allocations {
			if a.Node == n.ID && (a.Status == "running" || a.Status == "pending") {
				freeCPU -= a.CPU
				freeMem -= a.MemoryMB
			}
		}
		out.snapshot.Nodes = append(out.snapshot.Nodes, WorkloadNode{ID: n.ID, Name: n.Name, Status: info.Status, EligibleForWorkload: info.SchedulingEligibility == "eligible", FreeCPU: freeCPU, FreeMemoryMB: freeMem})
	}
	for i := range out.snapshot.Allocations {
		name := idToName[out.snapshot.Allocations[i].Node]
		if name == "" {
			return out, ErrWorkloadObservation
		}
		out.snapshot.Allocations[i].Node = name
	}
	var entries []struct {
		Node    struct{ Node string }
		Service struct {
			Service string
			Meta    map[string]string
		}
		Checks []struct{ CheckID, Name, Status string }
	}
	var consulHeader http.Header
	if consulHeader, err = s.read(ctx, consul, s.ConsulURL, "/v1/health/service/hello-norn-mysql-web", url.Values{"passing": {"false"}, "consistent": {"true"}}, s.ConsulToken, "X-Consul-Token", &entries); err != nil {
		return out, err
	}
	if consulHeader.Get("X-Consul-Results-Filtered-By-ACLs") != "" && consulHeader.Get("X-Consul-Results-Filtered-By-ACLs") != "false" {
		return out, ErrWorkloadObservation
	}
	out.consulIndex, err = strconv.ParseUint(consulHeader.Get("X-Consul-Index"), 10, 64)
	if err != nil || out.consulIndex == 0 {
		return out, ErrWorkloadObservation
	}
	for _, entry := range entries {
		meta := entry.Service.Meta
		if entry.Service.Service != "hello-norn-mysql-web" || meta["norn_job_id"] != "hello-norn-mysql" || meta["norn_namespace"] != "default" || idToName[meta["norn_node_id"]] == "" {
			return out, ErrWorkloadObservation
		}
		for _, check := range entry.Checks {
			if check.Name != "readyz" && check.Name != "healthz" {
				continue
			} // Consul also returns its node-level checks.
			out.snapshot.Services = append(out.snapshot.Services, WorkloadService{AllocationID: meta["norn_alloc_id"], Node: idToName[meta["norn_node_id"]], Namespace: meta["norn_namespace"], Name: entry.Service.Service, CheckName: check.Name, CheckID: check.CheckID, Status: check.Status})
		}
	}
	sort.Strings(out.snapshot.Namespaces)
	sort.Slice(out.snapshot.NamespaceReads, func(i, j int) bool { return out.snapshot.NamespaceReads[i].Name < out.snapshot.NamespaceReads[j].Name })
	sort.Slice(out.snapshot.Nodes, func(i, j int) bool { return out.snapshot.Nodes[i].ID < out.snapshot.Nodes[j].ID })
	sort.Slice(out.snapshot.Allocations, func(i, j int) bool { return out.snapshot.Allocations[i].ID < out.snapshot.Allocations[j].ID })
	sort.Slice(out.snapshot.Services, func(i, j int) bool { return out.snapshot.Services[i].CheckID < out.snapshot.Services[j].CheckID })
	out.canonical, err = json.Marshal(out.snapshot)
	return out, err
}

func (s NomadConsulSource) pages(ctx context.Context, client *http.Client, base, path string, q url.Values, token, header string, dst any) (uint64, error) {
	if q == nil {
		q = url.Values{}
	}
	if dst == nil {
		return 0, ErrWorkloadObservation
	}
	var combined []json.RawMessage
	seen := map[string]bool{}
	var index uint64
	for page := 0; page < 128; page++ {
		var items []json.RawMessage
		h, err := s.read(ctx, client, base, path, q, token, header, &items)
		if err != nil {
			return 0, err
		}
		var current uint64
		if _, err = fmt.Sscan(h.Get("X-Nomad-Index"), &current); err != nil || current == 0 {
			return 0, ErrWorkloadObservation
		}
		if index != 0 && index != current {
			return 0, ErrWorkloadObservation
		}
		index = current
		combined = append(combined, items...)
		next := h.Get("X-Nomad-NextToken")
		if next == "" {
			raw, _ := json.Marshal(combined)
			if err = json.Unmarshal(raw, dst); err != nil {
				return 0, ErrWorkloadObservation
			}
			return index, nil
		}
		if seen[next] {
			return 0, ErrWorkloadObservation
		}
		seen[next] = true
		q.Set("next_token", next)
	}
	return 0, errors.New("Nomad pagination exceeded safe bound")
}

var _ WorkloadSnapshotSource = NomadConsulSource{}
