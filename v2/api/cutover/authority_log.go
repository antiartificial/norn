package cutover

// This file implements the read side of ADR 0006's external migration
// authority log.  It intentionally does not update a Journal or invoke an
// activation adapter: an archive proof is a recovery/admission input, never a
// substitute for an execution-boundary fence.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/archive"
)

const (
	AuthorityManifestSchema = "norn.database-migration-authority/v1"
	authorityLogPrefix      = "database-migration-authority/v1"
	maxAuthorityManifest    = int64(1 << 20)
	maxAuthorityEvidence    = int64(16 << 20)
)

var ErrAuthorityLogInvalid = errors.New("database migration authority log is invalid")

// AuthorityEvidence names immutable bytes that were published before the
// manifest which relies on them.  The verifier reads and hashes those bytes;
// a digest alone does not assert that an external effect occurred.
type AuthorityEvidence struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

type AuthoritySignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyId"`
	Value     string `json:"value"`
}

// AuthorityManifest is deliberately secret-free. Tuple is immutable across a
// log and binds every phase to the same source/target, release, catalog and
// writer inventory selected when the migration began.
type AuthorityManifest struct {
	SchemaVersion          string              `json:"schemaVersion"`
	LogID                  string              `json:"logId"`
	Sequence               uint64              `json:"sequence"`
	Phase                  Phase               `json:"phase"`
	Tuple                  Intent              `json:"tuple"`
	SourceControlStoreID   string              `json:"sourceControlStoreId"`
	SourceAuthorityEpoch   uint64              `json:"sourceAuthorityEpoch"`
	TargetControlStoreID   string              `json:"targetControlStoreId"`
	TargetAuthorityEpoch   uint64              `json:"targetAuthorityEpoch"`
	ConsistencyGroupSHA256 string              `json:"consistencyGroupSha256"`
	PriorManifestSHA256    string              `json:"priorManifestSha256,omitempty"`
	Evidence               []AuthorityEvidence `json:"evidence"`
	RecoveryPolicy         string              `json:"recoveryPolicy"`
	SignedAt               time.Time           `json:"signedAt"`
	ExpiresAt              time.Time           `json:"expiresAt"`
	Signature              AuthoritySignature  `json:"signature"`
}

// AuthorityTrustKey permits a verifier to retain old public keys during key
// rotation. A revoked key fails closed even for historical reads; ValidFrom
// and ValidUntil constrain the time claimed by a signed manifest.
type AuthorityTrustKey struct {
	KeyID      string
	PublicKey  ed25519.PublicKey
	ValidFrom  time.Time
	ValidUntil time.Time
	RevokedAt  *time.Time
}

// AuthorityEvidenceVerifier is only used for a prospective check after the
// immutable historical chain has been accepted. Implementations perform the
// fresh typed readback needed for a changing external fact.
type AuthorityEvidenceVerifier interface {
	VerifyAuthorityEvidence(context.Context, AuthorityManifest, AuthorityEvidence, []byte) error
}

type AuthorityLogVerifier struct {
	Archive          archive.Reader
	TrustRoots       []AuthorityTrustKey
	EvidenceVerifier AuthorityEvidenceVerifier
	Now              func() time.Time
}

// VerifiedAuthorityChain is immutable recovery information. It never permits
// an effect: archive List is not a linearizable read of a log head.
type VerifiedAuthorityChain struct {
	manifests       []AuthorityManifest
	manifestDigests []string
	marker          *struct{}
}

func (c *VerifiedAuthorityChain) Valid() bool { return c != nil && c.marker != nil }
func (c *VerifiedAuthorityChain) LastManifest() (AuthorityManifest, string, bool) {
	if !c.Valid() || len(c.manifests) == 0 {
		return AuthorityManifest{}, "", false
	}
	i := len(c.manifests) - 1
	return cloneAuthorityManifest(c.manifests[i]), c.manifestDigests[i], true
}
func (c *VerifiedAuthorityChain) Manifests() []AuthorityManifest {
	if !c.Valid() {
		return nil
	}
	out := make([]AuthorityManifest, len(c.manifests))
	for i := range c.manifests {
		out[i] = cloneAuthorityManifest(c.manifests[i])
	}
	return out
}

// AuthorityManifestKey is the only accepted location for an authority
// manifest. Digest is SHA-256 of the exact stored JSON bytes, including its
// signature. The zero-padded sequence makes lexical archive listing stable.
func AuthorityManifestKey(logID string, sequence uint64, digest string) (string, error) {
	if !validAuthorityName(logID) || sequence == 0 || !validDigest(digest) {
		return "", ErrAuthorityLogInvalid
	}
	return fmt.Sprintf("%s/%s/%020d/%s.json", authorityLogPrefix, logID, sequence, digest), nil
}

// VerifyHistorical validates retained authority after expiry so recovery can
// find the final durable boundary. It never grants a new effect permit.
func (v AuthorityLogVerifier) VerifyHistorical(ctx context.Context, logID string) (*VerifiedAuthorityChain, error) {
	return v.verifyHistorical(ctx, logID)
}

// VerifyProspective requires a current, typed readback of every retained
// evidence item and an unexpired final manifest. Its result is still not an
// effect permit; the execution adapter must independently fence and observe
// the exact generation it will change.
func (v AuthorityLogVerifier) VerifyProspective(ctx context.Context, logID string) (*VerifiedAuthorityChain, error) {
	if v.EvidenceVerifier == nil {
		return nil, ErrAuthorityLogInvalid
	}
	chain, err := v.verifyHistorical(ctx, logID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	last, _, ok := chain.LastManifest()
	if !ok || !now.Before(last.ExpiresAt) {
		return nil, ErrAuthorityLogInvalid
	}
	if !v.trustKeyValidNow(last.Signature.KeyID, now) {
		return nil, ErrAuthorityLogInvalid
	}
	for _, manifest := range chain.manifests {
		for _, evidence := range manifest.Evidence {
			data, info, err := v.Archive.Get(ctx, evidence.Key, maxAuthorityEvidence)
			if err != nil || info.Key != evidence.Key || info.SHA256 != evidence.SHA256 || info.Size != int64(len(data)) || digestBytes(data) != evidence.SHA256 || v.Archive.Verify(ctx, info) != nil || v.EvidenceVerifier.VerifyAuthorityEvidence(ctx, manifest, evidence, data) != nil {
				return nil, ErrAuthorityLogInvalid
			}
		}
	}
	return chain, nil
}

func (v AuthorityLogVerifier) verifyHistorical(ctx context.Context, logID string) (*VerifiedAuthorityChain, error) {
	if v.Archive == nil || !validAuthorityName(logID) {
		return nil, ErrAuthorityLogInvalid
	}
	if !validAuthorityTrustRoots(v.TrustRoots) {
		return nil, ErrAuthorityLogInvalid
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	keys, err := v.Archive.List(ctx, authorityLogPrefix+"/"+logID+"/")
	// Recovery must work at every durable checkpoint, including the initial
	// Prepare and Quiesce manifests. An empty archive has no authority
	// boundary; longer chains are checked for contiguity below.
	if err != nil || len(keys) == 0 {
		return nil, ErrAuthorityLogInvalid
	}
	type item struct {
		key      string
		sequence uint64
		digest   string
	}
	items := make([]item, 0, len(keys))
	for _, key := range keys {
		sequence, digest, ok := parseAuthorityManifestKey(logID, key)
		if !ok {
			return nil, ErrAuthorityLogInvalid
		}
		items = append(items, item{key, sequence, digest})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].sequence < items[j].sequence })
	manifests := make([]AuthorityManifest, 0, len(items))
	digests := make([]string, 0, len(items))
	seenEvidence := map[string]bool{}
	var tuple Intent
	for i, item := range items {
		if item.sequence != uint64(i+1) {
			return nil, ErrAuthorityLogInvalid
		}
		raw, info, err := v.Archive.Get(ctx, item.key, maxAuthorityManifest)
		if err != nil || info.Key != item.key || info.SHA256 != item.digest || info.Size != int64(len(raw)) || digestBytes(raw) != item.digest || v.Archive.Verify(ctx, info) != nil {
			return nil, ErrAuthorityLogInvalid
		}
		manifest, err := decodeAuthorityManifest(raw)
		if err != nil || manifest.LogID != logID || manifest.Sequence != item.sequence || manifest.SchemaVersion != AuthorityManifestSchema {
			return nil, ErrAuthorityLogInvalid
		}
		if err := v.verifyManifest(ctx, manifest, now, i, manifests, digests, tuple, seenEvidence); err != nil {
			return nil, err
		}
		if i == 0 {
			tuple = manifest.Tuple
		}
		manifests = append(manifests, cloneAuthorityManifest(manifest))
		digests = append(digests, item.digest)
	}
	return &VerifiedAuthorityChain{manifests: manifests, manifestDigests: digests, marker: &struct{}{}}, nil
}

func (v AuthorityLogVerifier) verifyManifest(ctx context.Context, m AuthorityManifest, now time.Time, index int, prior []AuthorityManifest, priorDigests []string, tuple Intent, seenEvidence map[string]bool) error {
	if !validAuthorityManifest(m) || (index == 0 && (m.Phase != PhasePrepare || m.PriorManifestSHA256 != "")) || (index > 0 && (m.PriorManifestSHA256 != priorDigests[index-1] || m.Phase != nextAuthorityPhase(prior[index-1].Phase))) {
		return ErrAuthorityLogInvalid
	}
	if now.Before(m.SignedAt) {
		return ErrAuthorityLogInvalid
	}
	if index > 0 && (!sameAuthorityTuple(tuple, m.Tuple) || !sameAuthorityControlTuple(prior[0], m) || !prior[index-1].SignedAt.Before(m.SignedAt)) {
		return ErrAuthorityLogInvalid
	}
	trust, ok := v.trustKey(m.Signature.KeyID, m.SignedAt)
	if !ok {
		return ErrAuthorityLogInvalid
	}
	canonical, err := CanonicalAuthorityManifest(m)
	if err != nil {
		return ErrAuthorityLogInvalid
	}
	sig, err := hex.DecodeString(m.Signature.Value)
	if err != nil || !ed25519.Verify(trust.PublicKey, canonical, sig) {
		return ErrAuthorityLogInvalid
	}
	for _, evidence := range m.Evidence {
		idIdentity := "id\x00" + evidence.ID
		keyIdentity := "key\x00" + evidence.Key
		if seenEvidence[idIdentity] || seenEvidence[keyIdentity] {
			return ErrAuthorityLogInvalid
		}
		seenEvidence[idIdentity], seenEvidence[keyIdentity] = true, true
		data, info, err := v.Archive.Get(ctx, evidence.Key, maxAuthorityEvidence)
		if err != nil || info.Key != evidence.Key || info.SHA256 != evidence.SHA256 || info.Size != int64(len(data)) || digestBytes(data) != evidence.SHA256 || v.Archive.Verify(ctx, info) != nil {
			return ErrAuthorityLogInvalid
		}
	}
	return nil
}

func (v AuthorityLogVerifier) trustKey(keyID string, signedAt time.Time) (AuthorityTrustKey, bool) {
	for _, key := range v.TrustRoots {
		if key.KeyID != keyID || len(key.PublicKey) != ed25519.PublicKeySize || key.RevokedAt != nil || (!key.ValidFrom.IsZero() && signedAt.Before(key.ValidFrom)) || (!key.ValidUntil.IsZero() && !signedAt.Before(key.ValidUntil)) {
			continue
		}
		return key, true
	}
	return AuthorityTrustKey{}, false
}

// trustKeyValidNow intentionally differs from trustKey: prospective work
// cannot revive a rotated-out key by presenting a backdated SignedAt. This is
// not used for historical recovery, which verifies the key window at signing.
func (v AuthorityLogVerifier) trustKeyValidNow(keyID string, now time.Time) bool {
	for _, key := range v.TrustRoots {
		if key.KeyID == keyID && key.RevokedAt == nil && (key.ValidFrom.IsZero() || !now.Before(key.ValidFrom)) && (key.ValidUntil.IsZero() || now.Before(key.ValidUntil)) {
			return true
		}
	}
	return false
}

func validAuthorityTrustRoots(keys []AuthorityTrustKey) bool {
	if len(keys) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if !validAuthorityName(key.KeyID) || len(key.PublicKey) != ed25519.PublicKeySize || seen[key.KeyID] || (!key.ValidFrom.IsZero() && !validUTCTime(key.ValidFrom)) || (!key.ValidUntil.IsZero() && !validUTCTime(key.ValidUntil)) || (!key.ValidFrom.IsZero() && !key.ValidUntil.IsZero() && !key.ValidFrom.Before(key.ValidUntil)) || (key.RevokedAt != nil && !validUTCTime(*key.RevokedAt)) {
			return false
		}
		seen[key.KeyID] = true
	}
	return true
}

func CanonicalAuthorityManifest(m AuthorityManifest) ([]byte, error) {
	m.Signature = AuthoritySignature{}
	return json.Marshal(m)
}

func decodeAuthorityManifest(raw []byte) (AuthorityManifest, error) {
	var m AuthorityManifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var trailing json.RawMessage
	if d.Decode(&m) != nil || d.Decode(&trailing) != io.EOF {
		return AuthorityManifest{}, ErrAuthorityLogInvalid
	}
	return m, nil
}

func validAuthorityManifest(m AuthorityManifest) bool {
	if !validAuthorityName(m.LogID) || m.Sequence == 0 || !validPhase(m.Phase) || m.RecoveryPolicy == "" || !validUTCTime(m.SignedAt) || !validUTCTime(m.ExpiresAt) || !m.SignedAt.Before(m.ExpiresAt) || m.Signature.Algorithm != "ed25519" || !validAuthorityName(m.Signature.KeyID) || len(m.Evidence) == 0 {
		return false
	}
	if _, err := New(m.Tuple); err != nil || !validCandidateReleaseDigest(m.Tuple.CandidateRelease) || !validAuthorityName(m.SourceControlStoreID) || !validAuthorityName(m.TargetControlStoreID) || m.SourceAuthorityEpoch == 0 || m.TargetAuthorityEpoch == 0 || !validDigest(m.ConsistencyGroupSHA256) {
		return false
	}
	seen := map[string]bool{}
	lastID := ""
	for _, e := range m.Evidence {
		if !validAuthorityName(e.ID) || !validAuthorityName(e.Kind) || !archive.ValidKey(e.Key) || !validDigest(e.SHA256) || seen[e.ID] || (lastID != "" && e.ID <= lastID) {
			return false
		}
		seen[e.ID] = true
		lastID = e.ID
	}
	return true
}
func validUTCTime(t time.Time) bool { return !t.IsZero() && t.Location() == time.UTC }
func validAuthorityName(s string) bool {
	return validName(s) && len(s) <= 128 && !strings.Contains(s, "/")
}
func nextAuthorityPhase(p Phase) Phase {
	order := []Phase{PhasePrepare, PhaseQuiesce, PhaseFinalSync, PhaseActivate, PhaseVerify, PhaseAccept}
	for i := range order {
		if order[i] == p && i+1 < len(order) {
			return order[i+1]
		}
	}
	return ""
}
func sameAuthorityTuple(a, b Intent) bool {
	x, e1 := IntentSHA256(a)
	y, e2 := IntentSHA256(b)
	return e1 == nil && e2 == nil && x == y
}
func sameAuthorityControlTuple(a, b AuthorityManifest) bool {
	return a.SourceControlStoreID == b.SourceControlStoreID && a.SourceAuthorityEpoch == b.SourceAuthorityEpoch && a.TargetControlStoreID == b.TargetControlStoreID && a.TargetAuthorityEpoch == b.TargetAuthorityEpoch && a.ConsistencyGroupSHA256 == b.ConsistencyGroupSHA256
}
func validCandidateReleaseDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validDigest(strings.TrimPrefix(value, "sha256:"))
}

func parseAuthorityManifestKey(logID, key string) (uint64, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 5 || strings.Join(parts[:2], "/") != authorityLogPrefix || parts[2] != logID || len(parts[3]) != 20 || !strings.HasSuffix(parts[4], ".json") {
		return 0, "", false
	}
	sequence, err := strconv.ParseUint(parts[3], 10, 64)
	digest := strings.TrimSuffix(parts[4], ".json")
	return sequence, digest, err == nil && sequence > 0 && fmt.Sprintf("%020d", sequence) == parts[3] && validDigest(digest) && path.Clean(key) == key
}

func cloneAuthorityManifest(m AuthorityManifest) AuthorityManifest {
	m.Evidence = append([]AuthorityEvidence(nil), m.Evidence...)
	return m
}
