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
	CatalogRevision       int64                   `json:"catalogRevision"`
	CatalogDigest         string                  `json:"catalogDigest"`
	SourceProfileID       string                  `json:"sourceProfileId"`
	TargetProfileID       string                  `json:"targetProfileId"`
	Source                database.TargetIdentity `json:"source"`
	Target                database.TargetIdentity `json:"target"`
	AuthorityGeneration   uint64                  `json:"authorityGeneration"`
	WriterInventorySHA256 string                  `json:"writerInventorySha256"`
}

// Journal is a value to be stored outside both application databases. A
// receipt is an immutable digest of separately retained evidence, not a claim
// that this package observed the corresponding external effect.
type Journal struct {
	Intent             Intent                           `json:"intent"`
	Phase              Phase                            `json:"phase"`
	Revision           uint64                           `json:"revision"`
	Receipts           map[Phase]string                 `json:"receipts"`
	EvidenceReferences map[Phase]PhaseEvidenceReference `json:"evidenceReferences"`
}

// PhaseEvidenceReference binds a retained external-evidence digest to one
// journal edge. It is a reference contract, not verification of the effect.
type PhaseEvidenceReference struct {
	SchemaVersion      string `json:"schemaVersion"`
	IntentSHA256       string `json:"intentSha256"`
	FromRevision       uint64 `json:"fromRevision"`
	NextPhase          Phase  `json:"nextPhase"`
	EvidenceSHA256     string `json:"evidenceSha256"`
	PriorReceiptSHA256 string `json:"priorReceiptSha256,omitempty"`
}

func NewPhaseEvidenceReference(j Journal, next Phase, evidenceSHA256 string) (PhaseEvidenceReference, error) {
	if err := j.Validate(); err != nil || !validDigest(evidenceSHA256) {
		return PhaseEvidenceReference{}, fmt.Errorf("%w: invalid journal or evidence digest", ErrTransition)
	}
	intentDigest, err := IntentSHA256(j.Intent)
	if err != nil {
		return PhaseEvidenceReference{}, err
	}
	return PhaseEvidenceReference{
		SchemaVersion: "norn.database-cutover-phase-evidence/v1", IntentSHA256: intentDigest,
		FromRevision: j.Revision, NextPhase: next, EvidenceSHA256: evidenceSHA256,
		PriorReceiptSHA256: j.Receipts[j.Phase],
	}, nil
}

var ErrTransition = errors.New("database cutover transition rejected")

// ErrExternalProofRequired prevents claimed consumer phases from being
// recorded until a coordinator verifies retained external effects and owns
// the generation-bound consumer switch. A reference hash alone is not proof.
var ErrExternalProofRequired = errors.New("database cutover external proof and consumer switch required")

// RequireVerifiedConsumerPhase is called by claimed store adapters after
// validating the journal edge but before persisting it. Storage-only fixtures
// may still exercise the full state machine without granting runtime authority.
func RequireVerifiedConsumerPhase(next Phase) error {
	if next == PhaseActivate || next == PhaseVerify || next == PhaseAccept {
		return ErrExternalProofRequired
	}
	return nil
}

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
	if intent.SchemaVersion != "norn.database-cutover/v2" || !validName(intent.OperationID) || !validName(intent.App) || !validName(intent.LogicalDatabase) || !validName(intent.CandidateRelease) || intent.CatalogRevision <= 0 || !validCatalogDigest(intent.CatalogDigest) || !validName(intent.SourceProfileID) || !validName(intent.TargetProfileID) || intent.SourceProfileID == intent.TargetProfileID || intent.AuthorityGeneration == 0 || !validDigest(intent.WriterInventorySHA256) || !validTarget(intent.Source) || !validTarget(intent.Target) || intent.Source == intent.Target || intent.Source.Engine != intent.Target.Engine {
		return Journal{}, fmt.Errorf("%w: invalid immutable intent", ErrTransition)
	}
	return Journal{Intent: intent, Phase: PhasePrepare, Revision: 1, Receipts: map[Phase]string{}, EvidenceReferences: map[Phase]PhaseEvidenceReference{}}, nil
}

// VerifyCatalog requires the current control catalog to resolve the same
// logical database in distinct source and passive target profiles. It does
// not verify provider connectivity, writer inventory or transfer receipts.
func VerifyCatalog(intent Intent, revision int64, digest string, catalog database.Catalog) error {
	if _, err := New(intent); err != nil {
		return err
	}
	if revision != intent.CatalogRevision || digest != intent.CatalogDigest {
		return fmt.Errorf("%w: active catalog revision or digest changed", ErrTransition)
	}
	resolver, err := database.NewResolver(catalog)
	if err != nil {
		return fmt.Errorf("%w: invalid active catalog", ErrTransition)
	}
	for _, item := range []struct {
		profile      string
		target       database.TargetIdentity
		capabilities []database.Capability
	}{
		{intent.SourceProfileID, intent.Source, []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot}},
		{intent.TargetProfileID, intent.Target, []database.Capability{database.CapabilityRuntime, database.CapabilityRestore}},
	} {
		resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: item.profile, Purpose: database.PurposeApplication, LogicalResourceID: intent.LogicalDatabase, Expected: &item.target, RequiredCapabilities: item.capabilities})
		if err != nil || resolved.Target != item.target {
			return fmt.Errorf("%w: source or target profile no longer resolves the signed database", ErrTransition)
		}
	}
	return nil
}

// Advance validates one phase edge and returns a fresh value. The caller must
// atomically compare expectedRevision and the immutable intent in its store.
// A lost response can be reconciled by reading back the stored phase/receipt.
func (j Journal) Advance(expectedRevision uint64, next Phase, evidenceSHA256 string) (Journal, error) {
	return j.advance(expectedRevision, next, evidenceSHA256, false)
}

func (j Journal) advance(expectedRevision uint64, next Phase, evidenceSHA256 string, referenced bool) (Journal, error) {
	if !referenced && len(j.EvidenceReferences) > 0 {
		return Journal{}, fmt.Errorf("%w: referenced journal requires a phase reference", ErrTransition)
	}
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
	out.EvidenceReferences = make(map[Phase]PhaseEvidenceReference, len(j.EvidenceReferences))
	for phase, ref := range j.EvidenceReferences {
		out.EvidenceReferences[phase] = ref
	}
	return out, nil
}

// AdvanceWithEvidence hashes the complete reference into the receipt chain.
// Callers must separately verify and retain the referenced effect evidence.
func (j Journal) AdvanceWithEvidence(ref PhaseEvidenceReference) (Journal, error) {
	if len(j.EvidenceReferences) != len(j.Receipts) {
		return Journal{}, fmt.Errorf("%w: phase reference chain is incomplete", ErrTransition)
	}
	intentDigest, err := IntentSHA256(j.Intent)
	if err != nil {
		return Journal{}, err
	}
	if ref.SchemaVersion != "norn.database-cutover-phase-evidence/v1" ||
		ref.IntentSHA256 != intentDigest || ref.FromRevision != j.Revision ||
		!validDigest(ref.EvidenceSHA256) || ref.PriorReceiptSHA256 != j.Receipts[j.Phase] {
		return Journal{}, fmt.Errorf("%w: phase evidence reference differs from journal", ErrTransition)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		return Journal{}, err
	}
	digest := sha256.Sum256(encoded)
	out, err := j.advance(ref.FromRevision, ref.NextPhase, hex.EncodeToString(digest[:]), true)
	if err != nil {
		return Journal{}, err
	}
	out.EvidenceReferences[ref.NextPhase] = ref
	return out, nil
}

// Validate reconstructs the complete receipt chain from the immutable intent.
// Store readers call it before returning a persisted journal or advancing it.
func (j Journal) Validate() error {
	// A claimed journal cannot be extended by an older digest-only writer.
	// Raw receipts remain supported only for the private storage fixtures that
	// never began a referenced chain.
	if len(j.EvidenceReferences) > 0 && len(j.EvidenceReferences) != len(j.Receipts) {
		return fmt.Errorf("%w: referenced receipt chain is incomplete", ErrTransition)
	}
	current, err := New(j.Intent)
	if err != nil {
		return err
	}
	order := []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync, PhaseActivate, PhaseVerify, PhaseAccept}
	for _, next := range order[1:] {
		if current.Phase == j.Phase {
			break
		}
		if ref, ok := j.EvidenceReferences[next]; ok {
			current, err = current.AdvanceWithEvidence(ref)
		} else {
			current, err = current.Advance(current.Revision, next, j.Receipts[next])
		}
		if err != nil {
			return err
		}
	}
	if current.Phase != j.Phase || current.Revision != j.Revision || len(current.Receipts) != len(j.Receipts) || len(current.EvidenceReferences) != len(j.EvidenceReferences) {
		return fmt.Errorf("%w: stored phase, revision or receipts differ", ErrTransition)
	}
	for phase, digest := range j.Receipts {
		if current.Receipts[phase] != digest {
			return fmt.Errorf("%w: stored receipt differs", ErrTransition)
		}
	}
	for phase, ref := range j.EvidenceReferences {
		if current.EvidenceReferences[phase] != ref {
			return fmt.Errorf("%w: stored evidence reference differs", ErrTransition)
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
func validCatalogDigest(s string) bool {
	return validDigest(s) || (strings.HasPrefix(s, "sha256:") && validDigest(strings.TrimPrefix(s, "sha256:")))
}
func validTarget(t database.TargetIdentity) bool {
	return validName(t.ServiceID) && validName(t.BindingID) && validName(t.Database) && validName(t.Role) && t.ServiceGeneration > 0 && t.BindingGeneration > 0 && (t.Engine == database.EnginePostgreSQL || t.Engine == database.EngineMySQL)
}
