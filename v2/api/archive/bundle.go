package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"

	"norn/v2/api/saga"
)

// BundleSchema versions the immutable evidence bundle.
const BundleSchema = "norn.evidence-bundle/v1"

// MaxBundleBytes bounds a single bundle (writes and reads).
const MaxBundleBytes = 16 << 20

// Bundle is one immutable unit of archived evidence for a subject (today: a
// saga and the operation that produced it).
type Bundle struct {
	Schema   string  `json:"schema"`
	Subject  Subject `json:"subject"`
	Sequence int     `json:"sequence"`
	// Cutoff is the explicit final evidence cutoff: exactly these events are
	// in this bundle. Events outside it stay hot (or go to a later sequence).
	Cutoff Cutoff `json:"cutoff"`
	// Links are dependency links to other evidence (operation, deployment,
	// acceptance intent).
	Links []Link `json:"links"`
	// Operation is the operation row exactly as stored (payload and metadata
	// included, not the API read projection).
	Operation json.RawMessage `json:"operation,omitempty"`
	// Effects is the terminal operation's durable external-effect outcome and
	// output references. It is retained with the signed acceptance rather than
	// reconstructed from a later projection.
	Effects json.RawMessage `json:"effects,omitempty"`
	// Acceptance carries the original signed acceptance bytes, never
	// reconstructed from reformatted JSON.
	Acceptance   *SignedAcceptance `json:"acceptance,omitempty"`
	Events       []saga.Event      `json:"events"`
	EventsSHA256 string            `json:"eventsSha256"`
	EventsBytes  int64             `json:"eventsBytes"`
	SealedAt     time.Time         `json:"sealedAt"`
}

type Subject struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	App           string `json:"app"`
	OperationID   string `json:"operationId,omitempty"`
	OperationKind string `json:"operationKind,omitempty"`
}

type Cutoff struct {
	EventIDs      []string  `json:"eventIds"`
	EventCount    int       `json:"eventCount"`
	LastTimestamp time.Time `json:"lastTimestamp"`
}

type Link struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// SignedAcceptance is the operation's acceptance evidence as persisted.
type SignedAcceptance struct {
	IntentID                string `json:"intentId"`
	RequestCanonicalBytes   []byte `json:"requestCanonicalBytes"`
	CanonicalBytes          []byte `json:"canonicalBytes"`
	CanonicalDigest         string `json:"canonicalDigest"`
	SigningAlgorithm        string `json:"signingAlgorithm"`
	SigningKeyID            string `json:"signingKeyId"`
	Signature               string `json:"signature"`
	RequestReceiptID        string `json:"requestReceiptId,omitempty"`
	RequestIdentityID       string `json:"requestIdentityId,omitempty"`
	FingerprintDigest       string `json:"fingerprintDigest,omitempty"`
	FingerprintVersion      string `json:"fingerprintVersion,omitempty"`
	AcceptedCanonicalSHA256 string `json:"acceptedCanonicalSha256"`
}

var subjectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ObjectKey is the immutable key for a subject's bundle sequence.
func ObjectKey(subject Subject, sequence int) (string, error) {
	app := subject.App
	if app == "" {
		app = "none"
	}
	if subject.Kind != "saga" || !subjectIDPattern.MatchString(subject.ID) || !subjectIDPattern.MatchString(app) || sequence < 1 {
		return "", fmt.Errorf("bundle subject cannot form an archive key")
	}
	return fmt.Sprintf("evidence/saga/%s/%s/%06d.json", app, subject.ID, sequence), nil
}

// Seal completes a bundle's checksums and cutoff from its events, and
// encodes it. The encoding is the stored bytes; they are never re-encoded.
func Seal(bundle *Bundle) ([]byte, error) {
	bundle.Schema = BundleSchema
	ids := make([]string, 0, len(bundle.Events))
	var last time.Time
	for _, event := range bundle.Events {
		if event.SagaID != bundle.Subject.ID {
			return nil, fmt.Errorf("event %s belongs to another saga", event.ID)
		}
		ids = append(ids, event.ID)
		if event.Timestamp.After(last) {
			last = event.Timestamp
		}
	}
	events, err := json.Marshal(bundle.Events)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(events)
	bundle.EventsSHA256, bundle.EventsBytes = hex.EncodeToString(sum[:]), int64(len(events))
	bundle.Cutoff = Cutoff{EventIDs: ids, EventCount: len(ids), LastTimestamp: last}
	if bundle.Acceptance != nil {
		canonical := sha256.Sum256(bundle.Acceptance.CanonicalBytes)
		bundle.Acceptance.AcceptedCanonicalSHA256 = hex.EncodeToString(canonical[:])
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxBundleBytes {
		return nil, fmt.Errorf("evidence bundle exceeds %d bytes", MaxBundleBytes)
	}
	return encoded, nil
}

// Open strictly decodes stored bundle bytes and verifies their internal
// consistency: schema, cutoff, event checksum, subject and signed-byte
// checksum. It does not verify the acceptance signature (that needs the
// control-plane signer; see VerifyAcceptance in callers).
func Open(data []byte) (*Bundle, error) {
	if len(data) > MaxBundleBytes {
		return nil, ErrObjectTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle Bundle
	var trailing json.RawMessage
	if decoder.Decode(&bundle) != nil || decoder.Decode(&trailing) != io.EOF || bundle.Schema != BundleSchema {
		return nil, fmt.Errorf("%w: malformed bundle", ErrObjectCorrupt)
	}
	events, err := json.Marshal(bundle.Events)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(events)
	if hex.EncodeToString(sum[:]) != bundle.EventsSHA256 || int64(len(events)) != bundle.EventsBytes || len(bundle.Events) != bundle.Cutoff.EventCount || len(bundle.Cutoff.EventIDs) != bundle.Cutoff.EventCount {
		return nil, fmt.Errorf("%w: event checksum or cutoff mismatch", ErrObjectCorrupt)
	}
	for index, event := range bundle.Events {
		if event.ID != bundle.Cutoff.EventIDs[index] || event.SagaID != bundle.Subject.ID {
			return nil, fmt.Errorf("%w: event outside the cutoff", ErrObjectCorrupt)
		}
	}
	if bundle.Acceptance != nil {
		canonical := sha256.Sum256(bundle.Acceptance.CanonicalBytes)
		if hex.EncodeToString(canonical[:]) != bundle.Acceptance.AcceptedCanonicalSHA256 {
			return nil, fmt.Errorf("%w: signed acceptance bytes changed", ErrObjectCorrupt)
		}
	}
	if _, err := ObjectKey(bundle.Subject, bundle.Sequence); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrObjectCorrupt, err)
	}
	return &bundle, nil
}
