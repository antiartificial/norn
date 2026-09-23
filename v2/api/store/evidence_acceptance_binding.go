package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"norn/v2/api/model"
)

// ArchivedAcceptance is the signed acceptance carried by an evidence bundle
// together with the operation row it claims to have admitted.
type ArchivedAcceptance struct {
	IntentID              string
	RequestIdentityID     string
	RequestReceiptID      string
	FingerprintVersion    string
	FingerprintDigest     string
	RequestCanonicalBytes []byte
	CanonicalBytes        []byte
	CanonicalDigest       string
	SigningAlgorithm      string
	SigningKeyID          string
	// OperationRow is the archived raw operations row (row_to_json).
	OperationRow json.RawMessage
	// OperationID and SagaID are the archive subject's recorded identity.
	OperationID string
	SagaID      string
}

// VerifyArchivedAcceptance binds archived signed acceptance content to the
// archived operation, not merely to some signed bytes: both digests, the
// strictly decoded signed envelope (intent, request identity, operation,
// saga, receipt and signing key) and the canonical request (kind, app, ref,
// risk, source, attempts and semantic payload) must describe this subject's
// operation row. Signature validity is checked separately by the caller.
func VerifyArchivedAcceptance(a ArchivedAcceptance) error {
	requestDigest := sha256.Sum256(a.RequestCanonicalBytes)
	if subtle.ConstantTimeCompare([]byte(a.FingerprintDigest), []byte(hex.EncodeToString(requestDigest[:]))) != 1 {
		return fmt.Errorf("archived canonical request digest mismatch")
	}
	digest := sha256.Sum256(a.CanonicalBytes)
	if subtle.ConstantTimeCompare([]byte(a.CanonicalDigest), []byte(hex.EncodeToString(digest[:]))) != 1 {
		return fmt.Errorf("archived canonical acceptance digest mismatch")
	}
	var envelope acceptanceEnvelope
	if err := decodeStrictAcceptanceJSON(a.CanonicalBytes, &envelope); err != nil {
		return fmt.Errorf("decode archived acceptance envelope: %w", err)
	}
	var original requestMaterial
	if err := decodeStrictAcceptanceJSON(a.RequestCanonicalBytes, &original); err != nil {
		return fmt.Errorf("decode archived canonical request: %w", err)
	}
	row, err := DecodeExactJSONObject(a.OperationRow)
	if err != nil {
		return fmt.Errorf("decode archived operation row: %w", err)
	}
	text := func(key string) string { value, _ := row[key].(string); return value }
	op := model.Operation{ID: text("id"), Kind: text("kind"), App: text("app"), SagaID: text("saga_id"), Ref: text("ref"), Risk: text("risk"), Source: text("source")}
	if number, ok := row["max_attempts"].(json.Number); ok {
		value, err := number.Int64()
		if err != nil {
			return fmt.Errorf("archived operation attempts are malformed")
		}
		op.MaxAttempts = int(value)
	}
	op.Payload, _ = row["payload"].(map[string]interface{})
	if op.ID == "" || op.ID != a.OperationID || op.SagaID != a.SagaID {
		return fmt.Errorf("archived operation row is not the archive subject's operation")
	}
	linked := envelope.Schema == OperationAcceptanceEnvelopeSchema && envelope.IntentID == a.IntentID && envelope.RequestIdentityID == a.RequestIdentityID &&
		envelope.OperationID == op.ID && envelope.SagaID == op.SagaID && envelope.Audit.RequestReceiptID == a.RequestReceiptID &&
		envelope.Fingerprint.Version == a.FingerprintVersion && envelope.Fingerprint.Digest == a.FingerprintDigest &&
		envelope.SigningAlgorithm == a.SigningAlgorithm && envelope.SigningKeyID == a.SigningKeyID
	if !linked {
		return fmt.Errorf("archived signed acceptance does not name this operation, saga, intent and signing key")
	}
	if original.Schema != OperationRequestFingerprintVersion || original.Authority != envelope.Authority || original.Kind != envelope.Kind || original.Resource != envelope.Resource {
		return fmt.Errorf("archived canonical request identity differs from the signed envelope")
	}
	if original.Operation.Kind != op.Kind || original.Operation.App != op.App || original.Operation.Ref != op.Ref || original.Operation.Risk != op.Risk ||
		original.Operation.Source != op.Source || original.Operation.MaxAttempts != op.MaxAttempts {
		return fmt.Errorf("archived operation differs from the accepted request")
	}
	accepted := OperationAcceptance{Operation: op}
	if envelope.DeploymentID != "" {
		accepted.Deployment = &model.Deployment{ID: envelope.DeploymentID}
	}
	if envelope.FleetRunnerAttempt != nil {
		accepted.FleetRunnerAttempt = &FleetRunnerAttemptAdmission{AttemptID: envelope.FleetRunnerAttempt.ID}
	}
	if !exactJSONEqual(original.Operation.Payload, semanticOperationMap(op.Payload, accepted)) {
		return fmt.Errorf("archived operation payload differs from the accepted request")
	}
	return nil
}
