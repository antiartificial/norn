package store

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"time"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

// AcceptanceEvidence is one complete durable acceptance record as loaded from
// a control schema, whether the live store or a passively restored copy.
// Operation payload and metadata should be decoded with json.Number so that
// integers above 2^53 remain bound to the signed request.
type AcceptanceEvidence struct {
	Identity                OperationRequestIdentity
	IdentityFingerprint     RequestFingerprint
	IdentityOperationID     string
	Intent                  SignedAcceptanceIntent
	Operation               model.Operation
	Deployment              *model.Deployment
	Regions                 []model.ResolvedRegion
	FleetRunnerAttempt      *fleet.RunnerAttempt
	FleetRunnerLineageValid bool
}

// VerifyAcceptanceEvidence checks digests, signed links and the immutable
// accepted domain for one record. It is pure and does not verify the
// signature: callers verify Intent.Signature over Intent.CanonicalBytes with
// the retained key named by the record before trusting this result.
func VerifyAcceptanceEvidence(evidence AcceptanceEvidence) error {
	intent := evidence.Intent
	if evidence.IdentityFingerprint.Version != OperationRequestFingerprintVersion || !sameFingerprint(evidence.IdentityFingerprint, intent.Fingerprint) {
		return fmt.Errorf("identity and intent fingerprints differ")
	}
	if intent.Schema != OperationAcceptanceEnvelopeSchema || evidence.IdentityOperationID != intent.OperationID || intent.OperationID != evidence.Operation.ID {
		return fmt.Errorf("acceptance identity, intent and operation links differ")
	}
	requestDigest := sha256.Sum256(intent.RequestCanonicalBytes)
	if subtle.ConstantTimeCompare([]byte(intent.Fingerprint.Digest), []byte(hex.EncodeToString(requestDigest[:]))) != 1 {
		return fmt.Errorf("canonical request digest mismatch")
	}
	digest := sha256.Sum256(intent.CanonicalBytes)
	if subtle.ConstantTimeCompare([]byte(intent.CanonicalDigest), []byte(hex.EncodeToString(digest[:]))) != 1 {
		return fmt.Errorf("canonical digest mismatch")
	}
	var envelope acceptanceEnvelope
	if err := decodeStrictAcceptanceJSON(intent.CanonicalBytes, &envelope); err != nil {
		return fmt.Errorf("decode signed acceptance envelope: %w", err)
	}
	identity := evidence.Identity
	wantAcceptedAt := intent.AcceptedAt.UTC().Format(time.RFC3339Nano)
	linksOK := envelope.Schema == intent.Schema && envelope.IntentID == intent.ID && envelope.RequestIdentityID == intent.RequestIdentityID &&
		envelope.Authority == identity.Authority && envelope.Actor == identity.Actor && envelope.Kind == identity.Kind && envelope.Resource == identity.Resource && envelope.RequestKey == identity.Key &&
		sameFingerprint(envelope.Fingerprint, intent.Fingerprint) && envelope.OperationID == evidence.Operation.ID && envelope.SagaID == evidence.Operation.SagaID &&
		envelope.DeploymentID == intent.DeploymentID && envelope.AcceptedAt == wantAcceptedAt && envelope.Audit.RequestReceiptID == intent.Audit.RequestReceiptID &&
		envelope.Audit.RequestID == intent.Audit.RequestID && envelope.Audit.CredentialID == intent.Audit.CredentialID && envelope.Audit.DeviceID == intent.Audit.DeviceID &&
		envelope.Audit.Source == intent.Audit.Source && equalStrings(envelope.Audit.Scopes, intent.Audit.Scopes) &&
		envelope.SigningAlgorithm == intent.Signature.Algorithm && envelope.SigningKeyID == intent.Signature.KeyID
	if !linksOK {
		return fmt.Errorf("signed acceptance links do not match durable records")
	}
	var original requestMaterial
	if err := decodeStrictAcceptanceJSON(intent.RequestCanonicalBytes, &original); err != nil {
		return fmt.Errorf("decode canonical request: %w", err)
	}
	return verifyImmutableAcceptedDomain(identity, envelope, original, evidence)
}

// decodeStrictAcceptanceJSON rejects fields this binary cannot verify and
// preserves exact numbers. Signed bytes are only ever produced from the same
// structures, so an unknown field is unverifiable semantics, not extension.
func decodeStrictAcceptanceJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

// DecodeExactJSONObject decodes a stored JSON object with json.Number values.
func DecodeExactJSONObject(encoded []byte) (map[string]interface{}, error) {
	var out map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return out, nil
}

// exactJSONEqual compares two JSON-compatible values by decimal value rather
// than float64 or textual number form, so 9007199254740993 differs from
// 9007199254740992 while 1e+21 equals PostgreSQL's 1000000000000000000000.
func exactJSONEqual(a, b interface{}) bool {
	left, leftOK := exactJSONTree(a)
	right, rightOK := exactJSONTree(b)
	return leftOK && rightOK && exactTreeEqual(left, right)
}

func exactJSONSubset(expected, actual map[string]interface{}) bool {
	for key, value := range expected {
		if !exactJSONEqual(value, actual[key]) {
			return false
		}
	}
	return true
}

func exactJSONTree(value interface{}) (interface{}, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var tree interface{}
	if err := decoder.Decode(&tree); err != nil {
		return nil, false
	}
	return tree, true
}

func exactTreeEqual(a, b interface{}) bool {
	switch left := a.(type) {
	case map[string]interface{}:
		right, ok := b.(map[string]interface{})
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, found := right[key]
			if !found || !exactTreeEqual(value, other) {
				return false
			}
		}
		return true
	case []interface{}:
		right, ok := b.([]interface{})
		if !ok || len(left) != len(right) {
			return false
		}
		for index := range left {
			if !exactTreeEqual(left[index], right[index]) {
				return false
			}
		}
		return true
	case json.Number:
		right, ok := b.(json.Number)
		if !ok {
			return false
		}
		leftValue, leftOK := new(big.Rat).SetString(string(left))
		rightValue, rightOK := new(big.Rat).SetString(string(right))
		return leftOK && rightOK && leftValue.Cmp(rightValue) == 0
	default:
		return a == b
	}
}
