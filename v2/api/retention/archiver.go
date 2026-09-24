// Package retention moves completed evidence out of the hot control store
// into the immutable evidence archive (ADR 0001): an outbox intent created
// with the terminal transition, a sealed bundle with an explicit cutoff,
// immutable publication, read-back verification, acknowledgement of the
// exact object identity, holds, and verified pruning. Reads stay complete
// through the archive after pruning.
package retention

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"norn/v2/api/archive"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

// Mode selects whether the archiver may delete hot evidence.
type Mode string

const (
	// ModeShadow archives, verifies and compares retrieval; it never deletes.
	ModeShadow Mode = "shadow"
	// ModePrune additionally deletes hot evidence of verified bundles whose
	// holds are clear.
	ModePrune Mode = "prune"
)

// Archiver runs the evidence archive outbox.
type Archiver struct {
	DB      *store.DB
	Archive archive.Store
	// Signer, when set, verifies each bundle's original acceptance signature
	// on read-back before acknowledgement.
	Signer store.AcceptanceSigner
	Mode   Mode
	// Quiet is how long a saga must have no new events before its evidence
	// is sealed (late publication after terminalization).
	Quiet time.Duration
	// BackfillAfter is the grace before terminal operations without an
	// outbox row get one.
	BackfillAfter time.Duration
	// MinAge is the retention floor before pruning hot evidence.
	MinAge time.Duration
	// BatchSize bounds work per pass.
	BatchSize int
	// MinFreeBytes is the archive headroom below which the evidence reserve
	// reports the archive exhausted (stores with a known bound only).
	MinFreeBytes int64
}

// Report summarizes one pass.
type Report struct {
	Backfilled     int64    `json:"backfilled"`
	Supplementary  int64    `json:"supplementary"`
	Published      int      `json:"published"`
	PublishErrors  []string `json:"publishErrors,omitempty"`
	Pruned         int      `json:"prunedEvents"`
	Retired        int      `json:"retiredAcceptances"`
	Held           int      `json:"held"`
	ShadowCompared int      `json:"shadowCompared"`
	ShadowMismatch []string `json:"shadowMismatch,omitempty"`
}

func (a *Archiver) batch() int {
	if a.BatchSize <= 0 {
		return 50
	}
	return a.BatchSize
}

// RunOnce performs one bounded archive pass. Archive failures leave the
// evidence pending and hot; they are reported, never discarded.
func (a *Archiver) RunOnce(ctx context.Context) (Report, error) {
	var report Report
	var err error
	if report.Backfilled, err = a.DB.BackfillEvidenceIntents(ctx, a.BackfillAfter, a.batch()); err != nil {
		return report, fmt.Errorf("backfill evidence intents: %w", err)
	}
	if backfilled, err := a.DB.BackfillNonSagaFleetGitHubEvidenceIntents(ctx, a.BackfillAfter, a.batch()); err != nil {
		return report, fmt.Errorf("backfill non-saga Fleet GitHub evidence intents: %w", err)
	} else {
		report.Backfilled += backfilled
	}
	if report.Supplementary, err = a.DB.EnsureSupplementaryEvidenceIntents(ctx, a.Quiet); err != nil {
		return report, fmt.Errorf("supplementary evidence intents: %w", err)
	}
	var full error
	for range a.batch() {
		intent, err := a.DB.ProcessPendingEvidenceIntent(ctx, a.Quiet, a.publish)
		if errors.Is(err, store.ErrNoEvidenceWork) {
			break
		}
		if err != nil {
			if errors.Is(err, archive.ErrArchiveFull) {
				full = err
			}
			report.PublishErrors = append(report.PublishErrors, fmt.Sprintf("%s: %v", intent.ID, err))
			break // archive outage: keep evidence pending; retry next pass
		}
		report.Published++
	}
	if err := a.recordCapacity(ctx, full); err != nil {
		return report, fmt.Errorf("record archive capacity: %w", err)
	}
	verified, err := a.DB.VerifiedEvidenceIntents(ctx, a.batch())
	if err != nil {
		return report, err
	}
	for _, intent := range verified {
		// A verified operation receipt can replace an expired hot acceptance.
		if intent.SubjectKind == "operation" {
			if a.Mode != ModePrune {
				continue
			}
			// Retirement is allowed only when this process can verify the
			// original signature, not merely parse the sealed archive object.
			if a.Signer == nil {
				report.Held++
				continue
			}
			retired, holds, err := a.DB.RetireVerifiedOperationAcceptance(ctx, intent.ID, a.verifyStored)
			if err != nil {
				report.PublishErrors = append(report.PublishErrors, fmt.Sprintf("%s: %v", intent.ID, err))
				continue
			}
			if len(holds) > 0 {
				report.Held++
				continue
			}
			if retired {
				report.Retired++
			}
			continue
		}
		if intent.SubjectKind != "saga" {
			continue
		}
		if a.Mode != ModePrune {
			mismatch, err := a.compare(ctx, intent)
			report.ShadowCompared++
			if err != nil || mismatch != "" {
				report.ShadowMismatch = append(report.ShadowMismatch, fmt.Sprintf("%s: %s%v", intent.ID, mismatch, errString(err)))
			}
			continue
		}
		pruned, holds, err := a.DB.PruneVerifiedEvidence(ctx, intent.ID, a.MinAge, a.verifyStored)
		if err != nil {
			report.PublishErrors = append(report.PublishErrors, fmt.Sprintf("%s: %v", intent.ID, err))
			continue
		}
		if len(holds) > 0 {
			report.Held++
			continue
		}
		report.Pruned += pruned
	}
	return report, nil
}

// recordCapacity durably records whether the archive can take more
// evidence: a capacity refusal this pass, or (for bounded stores) headroom
// below MinFreeBytes, exhausts the evidence reserve for every process.
func (a *Archiver) recordCapacity(ctx context.Context, full error) error {
	if full != nil {
		return a.DB.RecordArchiveCapacity(ctx, true, full.Error())
	}
	if reporter, ok := a.Archive.(archive.CapacityReporter); ok {
		used, limit, err := reporter.Capacity(ctx)
		if err != nil {
			return err
		}
		if limit-used < a.MinFreeBytes {
			return a.DB.RecordArchiveCapacity(ctx, true, fmt.Sprintf("%d of %d archive bytes used; less than %d bytes of headroom remain", used, limit, a.MinFreeBytes))
		}
	}
	return a.DB.RecordArchiveCapacity(ctx, false, "")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// publish seals, publishes, reads back and verifies one bundle.
func (a *Archiver) publish(ctx context.Context, intent store.EvidenceIntent, source store.EvidenceSource) (store.EvidencePublication, error) {
	subject := archive.Subject{Kind: intent.SubjectKind, ID: intent.SubjectID, App: intent.App, OperationID: intent.OperationID, OperationKind: source.OperationKind}
	key, err := archive.ObjectKey(subject, intent.Sequence)
	if err != nil {
		return store.EvidencePublication{}, err
	}
	bundle := &archive.Bundle{Subject: subject, Sequence: intent.Sequence, Operation: source.OperationJSON, Effects: source.EffectsJSON, SealedAt: intent.CreatedAt.UTC(), Events: []saga.Event{}}
	if intent.OperationID != "" {
		bundle.Links = append(bundle.Links, archive.Link{Kind: "operation", ID: intent.OperationID})
	}
	if source.DeploymentID != "" {
		bundle.Links = append(bundle.Links, archive.Link{Kind: "deployment", ID: source.DeploymentID})
	}
	if acceptance := source.Acceptance; acceptance != nil {
		bundle.Links = append(bundle.Links, archive.Link{Kind: "acceptance-intent", ID: acceptance.IntentID})
		bundle.Acceptance = &archive.SignedAcceptance{IntentID: acceptance.IntentID, RequestIdentityID: acceptance.RequestIdentityID, RequestReceiptID: acceptance.RequestReceiptID,
			FingerprintVersion: acceptance.FingerprintVersion, FingerprintDigest: acceptance.FingerprintDigest, RequestCanonicalBytes: acceptance.RequestCanonicalBytes,
			CanonicalBytes: acceptance.CanonicalBytes, CanonicalDigest: acceptance.CanonicalDigest, SigningAlgorithm: acceptance.SigningAlgorithm,
			SigningKeyID: acceptance.SigningKeyID, Signature: acceptance.Signature}
	}
	for _, event := range source.Events {
		bundle.Events = append(bundle.Events, saga.Event(event))
	}
	encoded, err := archive.Seal(bundle)
	if err != nil {
		return store.EvidencePublication{}, err
	}
	info, err := a.Archive.PutImmutable(ctx, key, encoded)
	if errors.Is(err, archive.ErrImmutableConflict) {
		// A previous attempt published this subject/sequence and crashed
		// before acknowledging. The existing object is adopted only if it
		// proves itself against the same source evidence.
		return a.adopt(ctx, key, intent, source)
	}
	if err != nil {
		return store.EvidencePublication{}, err
	}
	return a.readBack(ctx, info, intent, source)
}

func (a *Archiver) adopt(ctx context.Context, key string, intent store.EvidenceIntent, source store.EvidenceSource) (store.EvidencePublication, error) {
	data, info, err := a.Archive.Get(ctx, key, archive.MaxBundleBytes)
	if err != nil {
		return store.EvidencePublication{}, err
	}
	bundle, err := archive.Open(data)
	if err != nil {
		return store.EvidencePublication{}, fmt.Errorf("existing archive object %s cannot be adopted: %w", key, err)
	}
	hot := map[string]saga.Event{}
	for _, event := range source.Events {
		hot[event.ID] = saga.Event(event)
	}
	if err := subjectMatches(bundle, intent, key); err != nil {
		return store.EvidencePublication{}, fmt.Errorf("existing archive object %s cannot be adopted: %w", key, err)
	}
	for _, event := range bundle.Events {
		current, ok := hot[event.ID]
		if !ok || !sameEvent(current, event) {
			return store.EvidencePublication{}, fmt.Errorf("existing archive object %s holds events that are not this subject's hot evidence", key)
		}
	}
	return a.readBack(ctx, info, intent, source)
}

// readBack re-reads the published object, requires its exact identity,
// re-opens and validates the bundle, and verifies the original acceptance
// signature bytes when a signer is configured.
func (a *Archiver) readBack(ctx context.Context, info archive.ObjectInfo, intent store.EvidenceIntent, sources ...store.EvidenceSource) (store.EvidencePublication, error) {
	if err := a.Archive.Verify(ctx, info); err != nil {
		return store.EvidencePublication{}, fmt.Errorf("read-back verification: %w", err)
	}
	data, got, err := a.Archive.Get(ctx, info.Key, archive.MaxBundleBytes)
	if err != nil {
		return store.EvidencePublication{}, fmt.Errorf("read-back: %w", err)
	}
	if got.SHA256 != info.SHA256 || got.Size != info.Size {
		return store.EvidencePublication{}, archive.ErrObjectCorrupt
	}
	bundle, err := archive.Open(data)
	if err != nil {
		return store.EvidencePublication{}, err
	}
	if err := subjectMatches(bundle, intent, info.Key); err != nil {
		return store.EvidencePublication{}, err
	}
	if intent.SubjectKind == "operation" {
		if len(sources) != 1 || !operationBundleMatchesSource(bundle, sources[0]) {
			return store.EvidencePublication{}, fmt.Errorf("operation evidence bundle differs from current signed receipt source")
		}
	}
	if err := a.verifyAcceptance(ctx, bundle); err != nil {
		return store.EvidencePublication{}, err
	}
	var cutoff *time.Time
	if bundle.Cutoff.EventCount > 0 {
		last := bundle.Cutoff.LastTimestamp
		cutoff = &last
	}
	return store.EvidencePublication{EventIDs: bundle.Cutoff.EventIDs, CutoffTimestamp: cutoff, ObjectKey: info.Key, ObjectSHA256: info.SHA256, ObjectBytes: info.Size}, nil
}

func operationBundleMatchesSource(bundle *archive.Bundle, source store.EvidenceSource) bool {
	if bundle == nil || bundle.Subject.Kind != "operation" || bundle.Subject.OperationKind != source.OperationKind || bundle.Acceptance == nil || source.Acceptance == nil ||
		!sameJSONBytes(bundle.Operation, source.OperationJSON) || !sameJSONBytes(bundle.Effects, source.EffectsJSON) {
		return false
	}
	a, b := bundle.Acceptance, source.Acceptance
	return a.IntentID == b.IntentID && a.RequestIdentityID == b.RequestIdentityID && a.RequestReceiptID == b.RequestReceiptID &&
		a.FingerprintVersion == b.FingerprintVersion && a.FingerprintDigest == b.FingerprintDigest &&
		bytes.Equal(a.RequestCanonicalBytes, b.RequestCanonicalBytes) && bytes.Equal(a.CanonicalBytes, b.CanonicalBytes) &&
		a.CanonicalDigest == b.CanonicalDigest && a.SigningAlgorithm == b.SigningAlgorithm && a.SigningKeyID == b.SigningKeyID && a.Signature == b.Signature
}

func sameJSONBytes(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// verifyAcceptance binds the archived signed acceptance to the archived
// operation (always) and verifies its signature (when a signer is
// configured). A valid signature over another operation's acceptance is
// refused: signed bytes prove nothing unless they name this operation.
func (a *Archiver) verifyAcceptance(ctx context.Context, bundle *archive.Bundle) error {
	acceptance := bundle.Acceptance
	if acceptance == nil {
		return nil
	}
	if err := store.VerifyArchivedAcceptance(store.ArchivedAcceptance{
		IntentID: acceptance.IntentID, RequestIdentityID: acceptance.RequestIdentityID, RequestReceiptID: acceptance.RequestReceiptID,
		FingerprintVersion: acceptance.FingerprintVersion, FingerprintDigest: acceptance.FingerprintDigest, RequestCanonicalBytes: acceptance.RequestCanonicalBytes,
		CanonicalBytes: acceptance.CanonicalBytes, CanonicalDigest: acceptance.CanonicalDigest, SigningAlgorithm: acceptance.SigningAlgorithm, SigningKeyID: acceptance.SigningKeyID,
		OperationRow: bundle.Operation, OperationID: bundle.Subject.OperationID, SagaID: bundleSagaID(bundle),
	}); err != nil {
		return fmt.Errorf("%w: %v", archive.ErrObjectCorrupt, err)
	}
	if a.Signer == nil {
		return nil
	}
	signature := store.AcceptanceSignature{Algorithm: acceptance.SigningAlgorithm, KeyID: acceptance.SigningKeyID, Value: acceptance.Signature}
	if err := a.Signer.Verify(ctx, signature, acceptance.CanonicalBytes); err != nil {
		return fmt.Errorf("archived acceptance signature does not verify: %w", err)
	}
	if bundle.Subject.Kind == "operation" && (bundle.Subject.OperationKind == "fleet.github.pull-request" || bundle.Subject.OperationKind == "fleet.github.apply-dispatch") && fleetGitHubAcceptanceWasReserved(acceptance.RequestCanonicalBytes) {
		if err := a.verifyFleetGitHubCompletion(ctx, bundle); err != nil {
			return err
		}
	}
	return nil
}

func fleetGitHubAcceptanceWasReserved(request []byte) bool {
	var accepted struct {
		Operation struct {
			Status string `json:"status"`
		} `json:"operation"`
	}
	return json.Unmarshal(request, &accepted) == nil && accepted.Operation.Status == "queued"
}

// verifyFleetGitHubCompletion proves that the terminal GitHub result retained
// in the operation row was signed for this reserved operation and plan.
func (a *Archiver) verifyFleetGitHubCompletion(ctx context.Context, bundle *archive.Bundle) error {
	var row struct {
		ID       string                 `json:"id"`
		Kind     string                 `json:"kind"`
		Ref      string                 `json:"ref"`
		Status   string                 `json:"status"`
		Payload  map[string]interface{} `json:"payload"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := json.Unmarshal(bundle.Operation, &row); err != nil {
		return fmt.Errorf("archived Fleet GitHub completion operation is malformed: %w", err)
	}
	completion, ok := row.Metadata["fleetGitHubCompletion"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("archived Fleet GitHub completion is missing")
	}
	canonicalText, _ := completion["canonicalBytes"].(string)
	algorithm, _ := completion["signingAlgorithm"].(string)
	keyID, _ := completion["signingKeyId"].(string)
	value, _ := completion["signature"].(string)
	canonical, err := base64.StdEncoding.DecodeString(canonicalText)
	if err != nil || len(canonical) == 0 {
		return fmt.Errorf("archived Fleet GitHub completion canonical bytes are invalid")
	}
	var signed struct {
		Schema      string          `json:"schema"`
		OperationID string          `json:"operationId"`
		PlanID      string          `json:"planId"`
		Kind        string          `json:"kind"`
		Status      string          `json:"status"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(canonical, &signed); err != nil || signed.Schema != "norn.fleet-github-completion/v1" || signed.OperationID != row.ID || signed.PlanID != row.Ref || signed.Kind != row.Kind || signed.Status != row.Status || len(signed.Result) == 0 {
		return fmt.Errorf("archived Fleet GitHub completion does not bind this terminal operation")
	}
	exposed, err := json.Marshal(completion["result"])
	if err != nil || !sameJSONBytes(exposed, signed.Result) {
		return fmt.Errorf("archived Fleet GitHub completion result differs from its signed outcome")
	}
	var resultFields map[string]interface{}
	if json.Unmarshal(signed.Result, &resultFields) != nil {
		return fmt.Errorf("archived Fleet GitHub completion result is malformed")
	}
	if len(row.Payload) != len(resultFields)+1 {
		return fmt.Errorf("archived Fleet GitHub operation payload has unsigned fields")
	}
	if _, ok := row.Payload["fleetGitHub"]; !ok {
		return fmt.Errorf("archived Fleet GitHub operation payload is missing its accepted reservation")
	}
	for key, value := range resultFields {
		if !sameJSONValue(row.Payload[key], value) {
			return fmt.Errorf("archived Fleet GitHub operation payload differs from its signed completion")
		}
	}
	if a.Signer == nil {
		return fmt.Errorf("archived Fleet GitHub completion signer is unavailable")
	}
	if err := a.Signer.Verify(ctx, store.AcceptanceSignature{Algorithm: algorithm, KeyID: keyID, Value: value}, canonical); err != nil {
		return fmt.Errorf("archived Fleet GitHub completion signature does not verify: %w", err)
	}
	return nil
}

func sameJSONValue(left, right interface{}) bool {
	a, aErr := json.Marshal(left)
	b, bErr := json.Marshal(right)
	return aErr == nil && bErr == nil && sameJSONBytes(a, b)
}

// verifyStored re-proves a verified intent's object right before pruning.
func (a *Archiver) verifyStored(ctx context.Context, intent store.EvidenceIntent) error {
	bundle, err := LoadBundle(ctx, a.Archive, intent)
	if err != nil {
		return err
	}
	if len(bundle.Cutoff.EventIDs) != len(intent.EventIDs) {
		return fmt.Errorf("archived cutoff differs from the recorded cutoff")
	}
	for index, id := range intent.EventIDs {
		if bundle.Cutoff.EventIDs[index] != id {
			return fmt.Errorf("archived cutoff differs from the recorded cutoff")
		}
	}
	return a.verifyAcceptance(ctx, bundle)
}

// compare is the shadow retrieval comparison: the archived bundle must
// reproduce exactly the hot events it claims.
func (a *Archiver) compare(ctx context.Context, intent store.EvidenceIntent) (string, error) {
	bundle, err := LoadBundle(ctx, a.Archive, intent)
	if err != nil {
		return "", err
	}
	if intent.SubjectKind != "saga" {
		return "", nil
	}
	hot, err := saga.NewPostgresStore(a.DB.Pool).ListBySaga(ctx, intent.SubjectID)
	if err != nil {
		return "", err
	}
	byID := map[string]saga.Event{}
	for _, event := range hot {
		byID[event.ID] = event
	}
	for _, event := range bundle.Events {
		current, ok := byID[event.ID]
		if !ok {
			return "archived event missing from hot store: " + event.ID, nil
		}
		if !sameEvent(current, event) {
			return "archived event differs from hot store: " + event.ID, nil
		}
	}
	return "", nil
}

// subjectMatches binds a bundle to the whole recorded subject: kind, subject
// ID, app, operation, sequence and the object key derived from them.
func subjectMatches(bundle *archive.Bundle, intent store.EvidenceIntent, key string) error {
	subject := bundle.Subject
	if subject.Kind != intent.SubjectKind || subject.ID != intent.SubjectID || subject.App != intent.App || subject.OperationID != intent.OperationID || bundle.Sequence != intent.Sequence {
		return fmt.Errorf("%w: bundle subject differs from the recorded subject", archive.ErrObjectCorrupt)
	}
	expected, err := archive.ObjectKey(subject, bundle.Sequence)
	if err != nil || expected != key {
		return fmt.Errorf("%w: bundle is stored under another key", archive.ErrObjectCorrupt)
	}
	return nil
}

func bundleSagaID(bundle *archive.Bundle) string {
	if bundle != nil && bundle.Subject.Kind == "saga" {
		return bundle.Subject.ID
	}
	return ""
}

func sameEvent(a, b saga.Event) bool {
	if len(a.Metadata) == 0 && len(b.Metadata) == 0 {
		a.Metadata, b.Metadata = nil, nil
	}
	return a.ID == b.ID && a.SagaID == b.SagaID && a.Timestamp.Equal(b.Timestamp) && a.Source == b.Source && a.App == b.App &&
		a.Category == b.Category && a.Action == b.Action && a.Message == b.Message && reflect.DeepEqual(a.Metadata, b.Metadata)
}

// LoadBundle reads a recorded bundle and requires its exact recorded
// identity before decoding it.
func LoadBundle(ctx context.Context, objects archive.Store, intent store.EvidenceIntent) (*archive.Bundle, error) {
	if intent.ObjectKey == "" {
		return nil, fmt.Errorf("evidence intent %s has no archived object", intent.ID)
	}
	data, info, err := objects.Get(ctx, intent.ObjectKey, archive.MaxBundleBytes)
	if err != nil {
		return nil, err
	}
	if info.SHA256 != intent.ObjectSHA256 || info.Size != intent.ObjectBytes {
		return nil, archive.ErrObjectCorrupt
	}
	bundle, err := archive.Open(data)
	if err != nil {
		return nil, err
	}
	if err := subjectMatches(bundle, intent, intent.ObjectKey); err != nil {
		return nil, err
	}
	return bundle, nil
}

// SignedBytes returns a bundle's original acceptance bytes, base64-encoded
// for transport; the bytes are never re-encoded.
func SignedBytes(bundle *archive.Bundle) string {
	if bundle.Acceptance == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(bundle.Acceptance.CanonicalBytes)
}

// Health is the archive's operator status.
type Health struct {
	Pending          int        `json:"pending"`
	Verified         int        `json:"verified"`
	Pruned           int        `json:"pruned"`
	OldestPendingAt  *time.Time `json:"oldestPendingAt,omitempty"`
	LastErrorIntent  string     `json:"lastErrorIntent,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
	ArchivedBytes    int64      `json:"archivedBytes"`
	HotEventsRetains int        `json:"hotEventsRetained"`
}

// ReadHealth summarizes the archive state from the index.
func ReadHealth(ctx context.Context, db *store.DB) (Health, error) {
	var health Health
	err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'pending'), count(*) FILTER (WHERE state = 'verified'), count(*) FILTER (WHERE state = 'pruned'),
		       min(created_at) FILTER (WHERE state = 'pending'), coalesce(sum(object_bytes), 0),
		       (SELECT count(*) FROM saga_events)
		FROM evidence_archive_intents`).Scan(&health.Pending, &health.Verified, &health.Pruned, &health.OldestPendingAt, &health.ArchivedBytes, &health.HotEventsRetains)
	if err != nil {
		return health, err
	}
	_ = db.Pool.QueryRow(ctx, `SELECT id, last_error FROM evidence_archive_intents WHERE last_error <> '' AND state = 'pending' ORDER BY updated_at DESC LIMIT 1`).Scan(&health.LastErrorIntent, &health.LastError)
	return health, nil
}
