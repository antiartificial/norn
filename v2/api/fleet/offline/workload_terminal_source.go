package offline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const maxTerminalWorkloadRawBytes = 16 << 20

// TerminalWorkloadObservation retains the exact, bounded HTTP read transcript
// privately for the authority's durable evidence journal. Neither this
// source nor its observer is mounted on a live route.
type TerminalWorkloadObservation struct {
	WorkloadEvidence WorkloadEvidence
	EvidenceSHA256   string
	RawEvidence      []byte
}

// EffectWorkloadObservation is the independently fetched, bounded pre-effect
// transcript. Unlike terminal proof, the target node must still be present
// and empty; the source never reads from a runner receipt.
type EffectWorkloadObservation struct {
	WorkloadEvidence WorkloadEvidence
	EvidenceSHA256   string
	RawEvidence      []byte
}

type workloadRawRecorder struct {
	rows []json.RawMessage
	size int
}

type capturedWorkloadSnapshot struct{ value WorkloadSnapshot }

func (s capturedWorkloadSnapshot) Snapshot(context.Context) (WorkloadSnapshot, error) {
	return s.value, nil
}

// The caller's now can precede several authenticated network reads. Take the
// validation time from the same injected clock only after the snapshot is
// complete, and bound the elapsed read to 30 seconds.
func captureWorkloadForValidation(ctx context.Context, source NomadConsulSource, started time.Time) (WorkloadSnapshot, time.Time, error) {
	if ctx == nil || started.IsZero() || ctx.Err() != nil {
		return WorkloadSnapshot{}, time.Time{}, ErrWorkloadObservation
	}
	snapshot, err := source.Snapshot(ctx)
	if err != nil || ctx.Err() != nil {
		return WorkloadSnapshot{}, time.Time{}, ErrWorkloadObservation
	}
	clock := source.Now
	if clock == nil {
		clock = time.Now
	}
	validatedAt := clock().UTC()
	if validatedAt.Before(started) || validatedAt.Before(snapshot.ObservedAt) || validatedAt.Sub(started) > 30*time.Second {
		return WorkloadSnapshot{}, time.Time{}, ErrWorkloadObservation
	}
	return snapshot, validatedAt, nil
}

func (r *workloadRawRecorder) append(u *url.URL, h http.Header, body []byte) error {
	// Nomad may echo SecretID in token/self. Preserve only the policy fields
	// used by provePolicy; journaling the complete response would leak a live
	// bearer into the owner-only transcript.
	redacted := false
	if u.EscapedPath() == "/v1/acl/token/self" {
		var token struct {
			Type     string   `json:"Type"`
			Policies []string `json:"Policies"`
			Roles    []struct {
				ID string `json:"ID"`
			} `json:"Roles"`
		}
		if json.Unmarshal(body, &token) != nil || token.Type == "" {
			return ErrWorkloadObservation
		}
		var err error
		body, err = json.Marshal(token)
		if err != nil {
			return ErrWorkloadObservation
		}
		redacted = true
	}
	row, err := json.Marshal(struct {
		Origin      string          `json:"origin"`
		Path        string          `json:"path"`
		Query       string          `json:"query,omitempty"`
		NomadIndex  string          `json:"nomadIndex,omitempty"`
		NextToken   string          `json:"nextToken,omitempty"`
		ConsulIndex string          `json:"consulIndex,omitempty"`
		ACLFiltered string          `json:"aclFiltered,omitempty"`
		Redacted    bool            `json:"redacted,omitempty"`
		Body        json.RawMessage `json:"body"`
	}{Origin: u.Scheme + "://" + u.Host, Path: u.EscapedPath(), Query: u.RawQuery,
		NomadIndex: h.Get("X-Nomad-Index"), NextToken: h.Get("X-Nomad-NextToken"),
		ConsulIndex: h.Get("X-Consul-Index"), ACLFiltered: h.Get("X-Consul-Results-Filtered-By-ACLs"), Redacted: redacted, Body: body})
	if err != nil || len(row)+r.size > maxTerminalWorkloadRawBytes {
		return ErrWorkloadObservation
	}
	r.rows = append(r.rows, row)
	r.size += len(row)
	return nil
}

// ObserveEffectNomadConsul proves the exact caller-pinned Nomad node ID is
// present as app-03 but free of live allocations, with two healthy survivor
// placements and headroom. The caller must independently bind this ID to
// the provider droplet/private IP; this Nomad read alone cannot do so.
func ObserveEffectNomadConsul(ctx context.Context, source NomadConsulSource, now time.Time, expectedNodeID string) (EffectWorkloadObservation, error) {
	var zero EffectWorkloadObservation
	if expectedNodeID == "" {
		return zero, ErrWorkloadObservation
	}
	recorder := &workloadRawRecorder{}
	source.recorder = recorder
	snapshot, validatedAt, err := captureWorkloadForValidation(ctx, source, now)
	if err != nil {
		return zero, ErrWorkloadObservation
	}
	targetFound := false
	for _, node := range snapshot.Nodes {
		if node.Name == "app-03" {
			if targetFound || node.ID != expectedNodeID {
				return zero, ErrWorkloadObservation
			}
			targetFound = true
		}
	}
	if !targetFound {
		return zero, ErrWorkloadObservation
	}
	evidence, err := ObserveWorkload(ctx, capturedWorkloadSnapshot{snapshot}, validatedAt)
	if err != nil || len(recorder.rows) == 0 {
		return zero, ErrWorkloadObservation
	}
	raw, err := json.Marshal(struct {
		Schema       string            `json:"schema"`
		TargetNodeID string            `json:"targetNodeId"`
		ObservedAt   time.Time         `json:"observedAt"`
		SnapshotHash string            `json:"snapshotSHA256"`
		Reads        []json.RawMessage `json:"reads"`
	}{Schema: "norn.fleet-effect-workload-reads/v1", TargetNodeID: expectedNodeID, ObservedAt: evidence.ObservedAt,
		SnapshotHash: evidence.SHA256, Reads: recorder.rows})
	if err != nil || len(raw) == 0 || len(raw) > maxTerminalWorkloadRawBytes || !json.Valid(raw) {
		return zero, ErrWorkloadObservation
	}
	digest := sha256.Sum256(raw)
	return EffectWorkloadObservation{WorkloadEvidence: evidence,
		EvidenceSHA256: hex.EncodeToString(digest[:]), RawEvidence: bytes.Clone(raw)}, nil
}

// ObserveTerminalNomadConsul independently scans all Nomad namespaces twice,
// proves the dedicated ACL policy, checks surviving placement and headroom,
// and includes every Consul check. The captured raw responses must be saved
// owner-only by the caller before any terminal receipt is committed.
func ObserveTerminalNomadConsul(ctx context.Context, source NomadConsulSource, now time.Time) (TerminalWorkloadObservation, error) {
	var zero TerminalWorkloadObservation
	recorder := &workloadRawRecorder{}
	source.recorder = recorder
	snapshot, validatedAt, err := captureWorkloadForValidation(ctx, source, now)
	if err != nil {
		return zero, ErrTerminalProof
	}
	evidence, err := ObserveTerminalWorkload(ctx, capturedWorkloadSnapshot{snapshot}, validatedAt)
	if err != nil || len(recorder.rows) == 0 {
		return zero, ErrTerminalProof
	}
	raw, err := json.Marshal(struct {
		Schema       string            `json:"schema"`
		ObservedAt   time.Time         `json:"observedAt"`
		SnapshotHash string            `json:"snapshotSHA256"`
		Reads        []json.RawMessage `json:"reads"`
	}{Schema: "norn.fleet-terminal-workload-reads/v1", ObservedAt: evidence.ObservedAt,
		SnapshotHash: evidence.SHA256, Reads: recorder.rows})
	if err != nil || len(raw) > maxTerminalWorkloadRawBytes || !json.Valid(raw) {
		return zero, ErrTerminalProof
	}
	// Keep an independent digest of the raw transcript; the snapshot digest is
	// also included so a replay can bind exact read bytes to validated fields.
	digest := sha256.Sum256(raw)
	return TerminalWorkloadObservation{WorkloadEvidence: evidence,
		EvidenceSHA256: hex.EncodeToString(digest[:]), RawEvidence: bytes.Clone(raw)}, nil
}
