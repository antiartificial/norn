// Package cutover defines the fail-closed phase contract for application
// database replacement. Store adapters must commit each returned revision with
// a compare-and-swap; this package alone grants no runtime authority.
package cutover

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"norn/v2/api/database"
)

type Phase string

const (
	PhasePrepare   Phase = "prepare"
	PhaseQuiesce   Phase = "quiesce"
	PhaseFinalSync Phase = "final-sync"
	PhaseActivate  Phase = "activate"
	PhaseVerify    Phase = "verify"
	PhaseAccept    Phase = "accept"
)

// Intent is immutable after creation. It deliberately contains no credentials.
type Intent struct {
	SchemaVersion         string                  `json:"schemaVersion"`
	OperationID           string                  `json:"operationId"`
	App                   string                  `json:"app"`
	LogicalDatabase       string                  `json:"logicalDatabase"`
	CandidateRelease      string                  `json:"candidateRelease"`
	Source                database.TargetIdentity `json:"source"`
	Target                database.TargetIdentity `json:"target"`
	AuthorityGeneration   uint64                  `json:"authorityGeneration"`
	WriterInventorySHA256 string                  `json:"writerInventorySha256"`
}

// Journal is a value to be stored outside both application databases. A
// receipt is an immutable digest of separately retained evidence, not a claim
// that this package observed the corresponding external effect.
type Journal struct {
	Intent   Intent           `json:"intent"`
	Phase    Phase            `json:"phase"`
	Revision uint64           `json:"revision"`
	Receipts map[Phase]string `json:"receipts"`
}

var ErrTransition = errors.New("database cutover transition rejected")

// IntentSHA256 is the canonical binding stored in a signed cutover operation.
// It covers every immutable source, target, release and writer-inventory field.
func IntentSHA256(intent Intent) (string, error) {
	if _, err := New(intent); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func New(intent Intent) (Journal, error) {
	if intent.SchemaVersion != "norn.database-cutover/v1" || !validName(intent.OperationID) || !validName(intent.App) || !validName(intent.LogicalDatabase) || !validName(intent.CandidateRelease) || intent.AuthorityGeneration == 0 || !validDigest(intent.WriterInventorySHA256) || !validTarget(intent.Source) || !validTarget(intent.Target) || intent.Source == intent.Target || intent.Source.Engine != intent.Target.Engine {
		return Journal{}, fmt.Errorf("%w: invalid immutable intent", ErrTransition)
	}
	return Journal{Intent: intent, Phase: PhasePrepare, Revision: 1, Receipts: map[Phase]string{}}, nil
}

// Advance validates one phase edge and returns a fresh value. The caller must
// atomically compare expectedRevision and the immutable intent in its store.
// A lost response can be reconciled by reading back the stored phase/receipt.
func (j Journal) Advance(expectedRevision uint64, next Phase, evidenceSHA256 string) (Journal, error) {
	if expectedRevision == 0 || j.Revision != expectedRevision || !validDigest(evidenceSHA256) || !validPhase(j.Phase) || !validPhase(next) || j.Revision == ^uint64(0) {
		return Journal{}, fmt.Errorf("%w: stale revision or invalid evidence", ErrTransition)
	}
	order := []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync, PhaseActivate, PhaseVerify, PhaseAccept}
	var index int
	for index < len(order) && order[index] != j.Phase {
		index++
	}
	if index+1 >= len(order) || order[index+1] != next || len(j.Receipts) != index {
		return Journal{}, fmt.Errorf("%w: phase order or prior receipts", ErrTransition)
	}
	for prior := 0; prior < index; prior++ {
		if !validDigest(j.Receipts[order[prior+1]]) {
			return Journal{}, fmt.Errorf("%w: missing prior receipt", ErrTransition)
		}
	}
	out := j
	out.Revision++
	out.Phase = next
	out.Receipts = make(map[Phase]string, len(j.Receipts)+1)
	for phase, digest := range j.Receipts {
		out.Receipts[phase] = digest
	}
	out.Receipts[next] = evidenceSHA256
	return out, nil
}

// Validate reconstructs the complete receipt chain from the immutable intent.
// Store readers call it before returning a persisted journal or advancing it.
func (j Journal) Validate() error {
	current, err := New(j.Intent)
	if err != nil {
		return err
	}
	order := []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync, PhaseActivate, PhaseVerify, PhaseAccept}
	for _, next := range order[1:] {
		if current.Phase == j.Phase {
			break
		}
		current, err = current.Advance(current.Revision, next, j.Receipts[next])
		if err != nil {
			return err
		}
	}
	if current.Phase != j.Phase || current.Revision != j.Revision || len(current.Receipts) != len(j.Receipts) {
		return fmt.Errorf("%w: stored phase, revision or receipts differ", ErrTransition)
	}
	for phase, digest := range j.Receipts {
		if current.Receipts[phase] != digest {
			return fmt.Errorf("%w: stored receipt differs", ErrTransition)
		}
	}
	return nil
}

func validPhase(p Phase) bool {
	return p == PhasePrepare || p == PhaseQuiesce || p == PhaseFinalSync || p == PhaseActivate || p == PhaseVerify || p == PhaseAccept
}
func validName(s string) bool {
	return s != "" && len(s) <= 256 && s == strings.TrimSpace(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}
func validTarget(t database.TargetIdentity) bool {
	return validName(t.ServiceID) && validName(t.BindingID) && validName(t.Database) && validName(t.Role) && t.ServiceGeneration > 0 && t.BindingGeneration > 0 && (t.Engine == database.EnginePostgreSQL || t.Engine == database.EngineMySQL)
}
