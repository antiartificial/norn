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

func (r *workloadRawRecorder) append(u *url.URL, h http.Header, body []byte) error {
	row, err := json.Marshal(struct {
		Origin      string          `json:"origin"`
		Path        string          `json:"path"`
		Query       string          `json:"query,omitempty"`
		NomadIndex  string          `json:"nomadIndex,omitempty"`
		NextToken   string          `json:"nextToken,omitempty"`
		ConsulIndex string          `json:"consulIndex,omitempty"`
		ACLFiltered string          `json:"aclFiltered,omitempty"`
		Body        json.RawMessage `json:"body"`
	}{Origin: u.Scheme + "://" + u.Host, Path: u.EscapedPath(), Query: u.RawQuery,
		NomadIndex: h.Get("X-Nomad-Index"), NextToken: h.Get("X-Nomad-NextToken"),
		ConsulIndex: h.Get("X-Consul-Index"), ACLFiltered: h.Get("X-Consul-Results-Filtered-By-ACLs"), Body: body})
	if err != nil || len(row)+r.size > maxTerminalWorkloadRawBytes {
		return ErrWorkloadObservation
	}
	r.rows = append(r.rows, row)
	r.size += len(row)
	return nil
}

// ObserveEffectNomadConsul proves the target is present but free of live
// allocations, with two healthy survivor placements and headroom. Its raw
// transcript belongs in an owner-only effect journal before signing.
func ObserveEffectNomadConsul(ctx context.Context, source NomadConsulSource, now time.Time) (EffectWorkloadObservation, error) {
	var zero EffectWorkloadObservation
	recorder := &workloadRawRecorder{}
	source.recorder = recorder
	evidence, err := ObserveWorkload(ctx, source, now)
	if err != nil || len(recorder.rows) == 0 {
		return zero, ErrWorkloadObservation
	}
	raw, err := json.Marshal(struct {
		Schema       string            `json:"schema"`
		ObservedAt   time.Time         `json:"observedAt"`
		SnapshotHash string            `json:"snapshotSHA256"`
		Reads        []json.RawMessage `json:"reads"`
	}{Schema: "norn.fleet-effect-workload-reads/v1", ObservedAt: evidence.ObservedAt,
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
	evidence, err := ObserveTerminalWorkload(ctx, source, now)
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
