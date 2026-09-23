package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// AuthStore is an etcd-backed store.AuthStore — the whole auth aggregate
// (identity/revocation + exec sessions) on one etcd cluster. It exists because
// ADR 0007 (Accepted) established that identity and exec-sessions are one
// aggregate whose revoke-plus-cancel must be atomic on a single backend. On
// PostgreSQL that atomicity is one transaction; here it is one multi-key
// compare-and-swap transaction over the credential record and the affected
// session records, guarded on their ModRevisions so a concurrent modification
// forces a retry rather than a partial revocation.
//
// Records live under prefix/auth/<kind>/<id>. Fields the model tags json:"-"
// (device public key, enrollment hashes, challenge nonce hash, session command)
// are preserved through explicit stored* wrappers, the same technique the
// mutation-audit adapter uses, because JSON marshaling would otherwise drop them.
type AuthStore struct {
	kv     clientv3.KV
	prefix string
}

// NewAuthStore returns an etcd auth store rooted at prefix.
func NewAuthStore(kv clientv3.KV, prefix string) *AuthStore {
	return &AuthStore{kv: kv, prefix: prefix}
}

var _ store.AuthStore = (*AuthStore)(nil)

func (s *AuthStore) deviceKey(id string) string    { return s.prefix + "/auth/device/" + id }
func (s *AuthStore) devicePrefix() string           { return s.prefix + "/auth/device/" }
func (s *AuthStore) tokenKey(jti string) string     { return s.prefix + "/auth/token/" + jti }
func (s *AuthStore) tokenPrefix() string            { return s.prefix + "/auth/token/" }
func (s *AuthStore) enrollKey(id string) string     { return s.prefix + "/auth/enroll/" + id }
func (s *AuthStore) enrollPrefix() string           { return s.prefix + "/auth/enroll/" }
func (s *AuthStore) oidcKey(issuer, jti string) string {
	return s.prefix + "/auth/oidc/" + issuer + "\x00" + jti
}
func (s *AuthStore) grantKey(id string) string      { return s.prefix + "/auth/grant/" + id }
func (s *AuthStore) grantPrefix() string            { return s.prefix + "/auth/grant/" }
func (s *AuthStore) challengeKey(id string) string  { return s.prefix + "/auth/challenge/" + id }
func (s *AuthStore) challengePrefix() string        { return s.prefix + "/auth/challenge/" }
func (s *AuthStore) sessionKey(id string) string    { return s.prefix + "/auth/session/" + id }
func (s *AuthStore) sessionPrefix() string          { return s.prefix + "/auth/session/" }

// --- stored wrappers: persist json:"-" fields the model hides ---

type storedDevice struct {
	store.AccessDevice
	PublicKeyStored string `json:"publicKeyStored"`
}

func (s *AuthStore) putDevice(ctx context.Context, d *store.AccessDevice) error {
	return s.putJSON(ctx, s.deviceKey(d.ID), storedDevice{AccessDevice: *d, PublicKeyStored: d.PublicKey})
}

func deviceFrom(sd storedDevice) store.AccessDevice {
	d := sd.AccessDevice
	d.PublicKey = sd.PublicKeyStored
	d.Tokens = []store.AccessToken{}
	return d
}

type storedEnrollment struct {
	store.AccessEnrollment
	CodeHashStored         string `json:"codeHashStored"`
	VerifierHashStored     string `json:"verifierHashStored"`
	PublicKeyStored        string `json:"publicKeyStored"`
	SourceHashStored       string `json:"sourceHashStored"`
	VerifierAttemptsStored int    `json:"verifierAttemptsStored"`
}

func (s *AuthStore) putEnrollment(ctx context.Context, e *store.AccessEnrollment) error {
	return s.putJSON(ctx, s.enrollKey(e.ID), storedEnrollment{
		AccessEnrollment:       *e,
		CodeHashStored:         e.CodeHash,
		VerifierHashStored:     e.VerifierHash,
		PublicKeyStored:        e.PublicKey,
		SourceHashStored:       e.SourceHash,
		VerifierAttemptsStored: e.VerifierAttempts,
	})
}

func enrollmentFrom(se storedEnrollment) store.AccessEnrollment {
	e := se.AccessEnrollment
	e.CodeHash = se.CodeHashStored
	e.VerifierHash = se.VerifierHashStored
	e.PublicKey = se.PublicKeyStored
	e.SourceHash = se.SourceHashStored
	e.VerifierAttempts = se.VerifierAttemptsStored
	return e
}

// --- generic JSON KV helpers ---

func (s *AuthStore) putJSON(ctx context.Context, key string, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.kv.Put(ctx, key, string(raw))
	return err
}

func opPut(key string, value interface{}) (clientv3.Op, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return clientv3.Op{}, err
	}
	return clientv3.OpPut(key, string(raw)), nil
}

// --- devices ---

func (s *AuthStore) CreateAccessDevice(ctx context.Context, device *store.AccessDevice) error {
	// INSERT semantics: fail if the device key already exists.
	sd := storedDevice{AccessDevice: *device, PublicKeyStored: device.PublicKey}
	raw, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	resp, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(s.deviceKey(device.ID)), "=", 0)).
		Then(clientv3.OpPut(s.deviceKey(device.ID), string(raw))).
		Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return fmt.Errorf("etcdstore: device %s already exists", device.ID)
	}
	return nil
}

func (s *AuthStore) loadDevice(ctx context.Context, id string) (*store.AccessDevice, int64, error) {
	resp, err := s.kv.Get(ctx, s.deviceKey(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var sd storedDevice
	if err := json.Unmarshal(resp.Kvs[0].Value, &sd); err != nil {
		return nil, 0, err
	}
	d := deviceFrom(sd)
	return &d, resp.Kvs[0].ModRevision, nil
}

func (s *AuthStore) ActiveAccessDevice(ctx context.Context, id string) (*store.AccessDevice, error) {
	d, _, err := s.loadDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.RevokedAt != nil {
		return nil, ErrNotFound
	}
	return d, nil
}

func (s *AuthStore) TouchAccessDevice(ctx context.Context, id string) error {
	d, rev, err := s.loadDevice(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if d.RevokedAt != nil {
		return nil
	}
	now := time.Now()
	if d.LastSeenAt != nil && d.LastSeenAt.After(now.Add(-5*time.Minute)) {
		return nil
	}
	d.LastSeenAt = &now
	op, err := opPut(s.deviceKey(id), storedDevice{AccessDevice: *d, PublicKeyStored: d.PublicKey})
	if err != nil {
		return err
	}
	_, err = s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(s.deviceKey(id)), "=", rev)).
		Then(op).Commit()
	return err
}

func (s *AuthStore) ListAccessDevices(ctx context.Context) ([]store.AccessDevice, error) {
	resp, err := s.kv.Get(ctx, s.devicePrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	tokens, err := s.scanTokens(ctx)
	if err != nil {
		return nil, err
	}
	byDevice := map[string][]store.AccessToken{}
	for _, t := range tokens {
		if t.DeviceID != "" {
			byDevice[t.DeviceID] = append(byDevice[t.DeviceID], *t)
		}
	}
	out := []store.AccessDevice{}
	for _, kv := range resp.Kvs {
		var sd storedDevice
		if err := json.Unmarshal(kv.Value, &sd); err != nil {
			return nil, err
		}
		d := deviceFrom(sd)
		d.Tokens = byDevice[d.ID]
		if d.Tokens == nil {
			d.Tokens = []store.AccessToken{}
		}
		sort.Slice(d.Tokens, func(i, j int) bool { return d.Tokens[i].IssuedAt.After(d.Tokens[j].IssuedAt) })
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// --- tokens ---

type tokenRev struct {
	t   *store.AccessToken
	rev int64
}

func (s *AuthStore) loadToken(ctx context.Context, jti string) (*store.AccessToken, int64, error) {
	resp, err := s.kv.Get(ctx, s.tokenKey(jti))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var t store.AccessToken
	if err := json.Unmarshal(resp.Kvs[0].Value, &t); err != nil {
		return nil, 0, err
	}
	return &t, resp.Kvs[0].ModRevision, nil
}

func (s *AuthStore) scanTokens(ctx context.Context) ([]*store.AccessToken, error) {
	resp, err := s.kv.Get(ctx, s.tokenPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]*store.AccessToken, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var t store.AccessToken
		if err := json.Unmarshal(kv.Value, &t); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, nil
}

func (s *AuthStore) tokensForDevice(ctx context.Context, deviceID string) ([]tokenRev, error) {
	resp, err := s.kv.Get(ctx, s.tokenPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var out []tokenRev
	for _, kv := range resp.Kvs {
		var t store.AccessToken
		if err := json.Unmarshal(kv.Value, &t); err != nil {
			return nil, err
		}
		if t.DeviceID == deviceID {
			tt := t
			out = append(out, tokenRev{t: &tt, rev: kv.ModRevision})
		}
	}
	return out, nil
}

func (s *AuthStore) RecordAccessToken(ctx context.Context, token *store.AccessToken) error {
	return s.putJSON(ctx, s.tokenKey(token.JTI), token)
}

func (s *AuthStore) AccessTokenActive(ctx context.Context, jti string) (bool, error) {
	t, _, err := s.loadToken(ctx, jti)
	if err != nil {
		// Mirrors the PostgreSQL adapter, which scans no row into a bool and
		// returns an error; callers fail closed on a missing token.
		return false, err
	}
	if t.RevokedAt != nil || !t.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	if t.DeviceID != "" {
		d, _, derr := s.loadDevice(ctx, t.DeviceID)
		if derr != nil && !errors.Is(derr, ErrNotFound) {
			return false, derr
		}
		if d != nil && d.RevokedAt != nil {
			return false, nil
		}
	}
	return true, nil
}

// --- OIDC single-use assertions ---

func (s *AuthStore) ConsumeGitHubActionsAssertion(ctx context.Context, issuer, jti string, expiresAt time.Time) error {
	if issuer == "" || jti == "" || expiresAt.IsZero() {
		return errors.New("assertion identity and expiry are required")
	}
	s.pruneExpiredAssertions(ctx)
	key := s.oidcKey(issuer, jti)
	resp, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, expiresAt.UTC().Format(time.RFC3339Nano))).
		Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return store.ErrGitHubActionsAssertionConsumed
	}
	return nil
}

func (s *AuthStore) pruneExpiredAssertions(ctx context.Context) {
	resp, err := s.kv.Get(ctx, s.prefix+"/auth/oidc/", clientv3.WithPrefix())
	if err != nil {
		return
	}
	now := time.Now()
	for _, kv := range resp.Kvs {
		if exp, perr := time.Parse(time.RFC3339Nano, string(kv.Value)); perr == nil && !exp.After(now) {
			_, _ = s.kv.Delete(ctx, string(kv.Key))
		}
	}
}

// --- IP access grants ---

func (s *AuthStore) CreateAccessGrant(ctx context.Context, g *store.AccessGrant) error {
	if g.ID == "" {
		return errors.New("access grant id is required")
	}
	return s.putJSON(ctx, s.grantKey(g.ID), g)
}

func (s *AuthStore) DeleteAccessGrant(ctx context.Context, id string) error {
	_, err := s.kv.Delete(ctx, s.grantKey(id))
	return err
}

func (s *AuthStore) scanGrants(ctx context.Context) ([]store.AccessGrant, error) {
	resp, err := s.kv.Get(ctx, s.grantPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]store.AccessGrant, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var g store.AccessGrant
		if err := json.Unmarshal(kv.Value, &g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func (s *AuthStore) ListAccessGrants(ctx context.Context) ([]store.AccessGrant, error) {
	all, err := s.scanGrants(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := []store.AccessGrant{}
	for _, g := range all {
		if g.ExpiresAt.After(now) {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *AuthStore) MatchAccessGrant(ctx context.Context, ip string) (bool, error) {
	all, err := s.scanGrants(ctx)
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, g := range all {
		if g.IP == ip && g.ExpiresAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func (s *AuthStore) CleanExpiredGrants(ctx context.Context) error {
	all, err := s.scanGrantsWithRev(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, gr := range all {
		if !gr.g.ExpiresAt.After(now) {
			_, _ = s.kv.Delete(ctx, s.grantKey(gr.g.ID))
		}
	}
	return nil
}

type grantRev struct {
	g   store.AccessGrant
	rev int64
}

func (s *AuthStore) scanGrantsWithRev(ctx context.Context) ([]grantRev, error) {
	resp, err := s.kv.Get(ctx, s.grantPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]grantRev, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var g store.AccessGrant
		if err := json.Unmarshal(kv.Value, &g); err != nil {
			return nil, err
		}
		out = append(out, grantRev{g: g, rev: kv.ModRevision})
	}
	return out, nil
}

// --- enrollments ---

func (s *AuthStore) loadEnrollment(ctx context.Context, id string) (*store.AccessEnrollment, int64, error) {
	resp, err := s.kv.Get(ctx, s.enrollKey(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var se storedEnrollment
	if err := json.Unmarshal(resp.Kvs[0].Value, &se); err != nil {
		return nil, 0, err
	}
	e := enrollmentFrom(se)
	return &e, resp.Kvs[0].ModRevision, nil
}

func (s *AuthStore) scanEnrollments(ctx context.Context) ([]store.AccessEnrollment, error) {
	resp, err := s.kv.Get(ctx, s.enrollPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]store.AccessEnrollment, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var se storedEnrollment
		if err := json.Unmarshal(kv.Value, &se); err != nil {
			return nil, err
		}
		out = append(out, enrollmentFrom(se))
	}
	return out, nil
}

func (s *AuthStore) CreateAccessEnrollment(ctx context.Context, enrollment *store.AccessEnrollment) error {
	// Rate limits mirror the PostgreSQL adapter: <100 enrollments globally and
	// <10 per source hash in the last 10 minutes.
	all, err := s.scanEnrollments(ctx)
	if err != nil {
		return err
	}
	since := time.Now().Add(-10 * time.Minute)
	recentGlobal, recentSource := 0, 0
	for _, e := range all {
		if e.CreatedAt.After(since) {
			recentGlobal++
			if e.SourceHash == enrollment.SourceHash {
				recentSource++
			}
		}
	}
	if recentGlobal >= 100 || recentSource >= 10 {
		return store.ErrRateLimited
	}
	if enrollment.Status == "" {
		enrollment.Status = "pending"
	}
	return s.putEnrollment(ctx, enrollment)
}

func (s *AuthStore) GetAccessEnrollment(ctx context.Context, id string) (*store.AccessEnrollment, error) {
	e, _, err := s.loadEnrollment(ctx, id)
	return e, err
}

func (s *AuthStore) GetAccessEnrollmentByCodeHash(ctx context.Context, codeHash string) (*store.AccessEnrollment, error) {
	all, err := s.scanEnrollments(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].CodeHash == codeHash {
			return &all[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *AuthStore) ListAccessEnrollments(ctx context.Context, status string) ([]store.AccessEnrollment, error) {
	all, err := s.scanEnrollments(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.AccessEnrollment{}
	for _, e := range all {
		if status != "" && e.Status != status {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// updateEnrollment applies apply under optimistic retry, guarding on the
// enrollment's ModRevision.
func (s *AuthStore) updateEnrollment(ctx context.Context, id string, apply func(e *store.AccessEnrollment) error) (*store.AccessEnrollment, error) {
	for attempt := 0; attempt < 32; attempt++ {
		e, rev, err := s.loadEnrollment(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := apply(e); err != nil {
			return nil, err
		}
		op, err := opPut(s.enrollKey(id), storedEnrollment{
			AccessEnrollment:       *e,
			CodeHashStored:         e.CodeHash,
			VerifierHashStored:     e.VerifierHash,
			PublicKeyStored:        e.PublicKey,
			SourceHashStored:       e.SourceHash,
			VerifierAttemptsStored: e.VerifierAttempts,
		})
		if err != nil {
			return nil, err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.enrollKey(id)), "=", rev)).
			Then(op).Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return e, nil
		}
	}
	return nil, fmt.Errorf("update enrollment %s: exhausted retries under contention", id)
}

func (s *AuthStore) ApproveAccessEnrollment(ctx context.Context, id, deviceID string, scopes []string) (*store.AccessEnrollment, error) {
	return s.updateEnrollment(ctx, id, func(e *store.AccessEnrollment) error {
		if e.Status != "pending" || !e.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		e.Status = "approved"
		e.ApprovedScopes = scopes
		e.DeviceID = deviceID
		now := time.Now()
		e.ApprovedAt = &now
		return nil
	})
}

func (s *AuthStore) ApproveAccessEnrollmentWithDevice(ctx context.Context, id string, device *store.AccessDevice, scopes []string) (*store.AccessEnrollment, error) {
	// Approve the enrollment and create the device in one transaction, guarded on
	// the enrollment's revision and the device key's absence.
	for attempt := 0; attempt < 32; attempt++ {
		e, rev, err := s.loadEnrollment(ctx, id)
		if err != nil {
			return nil, err
		}
		if e.Status != "pending" || !e.ExpiresAt.After(time.Now()) {
			return nil, ErrNotFound
		}
		e.Status = "approved"
		e.ApprovedScopes = scopes
		e.DeviceID = device.ID
		now := time.Now()
		e.ApprovedAt = &now
		enrollOp, err := opPut(s.enrollKey(id), storedEnrollment{
			AccessEnrollment: *e, CodeHashStored: e.CodeHash, VerifierHashStored: e.VerifierHash,
			PublicKeyStored: e.PublicKey, SourceHashStored: e.SourceHash, VerifierAttemptsStored: e.VerifierAttempts,
		})
		if err != nil {
			return nil, err
		}
		deviceOp, err := opPut(s.deviceKey(device.ID), storedDevice{AccessDevice: *device, PublicKeyStored: device.PublicKey})
		if err != nil {
			return nil, err
		}
		resp, err := s.kv.Txn(ctx).
			If(
				clientv3.Compare(clientv3.ModRevision(s.enrollKey(id)), "=", rev),
				clientv3.Compare(clientv3.CreateRevision(s.deviceKey(device.ID)), "=", 0),
			).
			Then(enrollOp, deviceOp).Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return e, nil
		}
	}
	return nil, fmt.Errorf("approve enrollment %s with device: exhausted retries under contention", id)
}

func (s *AuthStore) ExchangeAccessEnrollment(ctx context.Context, id string) (*store.AccessEnrollment, error) {
	return s.updateEnrollment(ctx, id, func(e *store.AccessEnrollment) error {
		if e.Status != "approved" || !e.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		e.Status = "exchanged"
		now := time.Now()
		e.ExchangedAt = &now
		return nil
	})
}

func (s *AuthStore) ExchangeAccessEnrollmentWithToken(ctx context.Context, id string, token *store.AccessToken) error {
	for attempt := 0; attempt < 32; attempt++ {
		e, rev, err := s.loadEnrollment(ctx, id)
		if err != nil {
			return err
		}
		if e.Status != "approved" || !e.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		e.Status = "exchanged"
		now := time.Now()
		e.ExchangedAt = &now
		enrollOp, err := opPut(s.enrollKey(id), storedEnrollment{
			AccessEnrollment: *e, CodeHashStored: e.CodeHash, VerifierHashStored: e.VerifierHash,
			PublicKeyStored: e.PublicKey, SourceHashStored: e.SourceHash, VerifierAttemptsStored: e.VerifierAttempts,
		})
		if err != nil {
			return err
		}
		tokenOp, err := opPut(s.tokenKey(token.JTI), token)
		if err != nil {
			return err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.enrollKey(id)), "=", rev)).
			Then(enrollOp, tokenOp).Commit()
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return fmt.Errorf("exchange enrollment %s with token: exhausted retries under contention", id)
}

func (s *AuthStore) RecordAccessEnrollmentFailure(ctx context.Context, id string) error {
	_, err := s.updateEnrollment(ctx, id, func(e *store.AccessEnrollment) error {
		if e.Status != "approved" || !e.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		e.VerifierAttempts++
		if e.VerifierAttempts >= 8 {
			e.Status = "locked"
		}
		return nil
	})
	if err != nil {
		return err
	}
	e, _, lerr := s.loadEnrollment(ctx, id)
	if lerr != nil {
		return lerr
	}
	if e.Status == "locked" {
		return store.ErrEnrollmentLocked
	}
	return nil
}

// --- atomic revocation: revoke a credential AND cancel its sessions in one txn ---

// RotateAccessToken records the replacement and revokes the token that
// authorized rotation, cancelling that token's exec sessions — atomically.
func (s *AuthStore) RotateAccessToken(ctx context.Context, previousJTI string, token *store.AccessToken) ([]string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		prev, prevRev, err := s.loadToken(ctx, previousJTI)
		if err != nil {
			return nil, err
		}
		if prev.RevokedAt != nil || !prev.ExpiresAt.After(time.Now()) {
			return nil, ErrNotFound
		}
		now := time.Now()
		prev.RevokedAt = &now
		cmps := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.tokenKey(previousJTI)), "=", prevRev)}
		prevOp, err := opPut(s.tokenKey(previousJTI), prev)
		if err != nil {
			return nil, err
		}
		newOp, err := opPut(s.tokenKey(token.JTI), token)
		if err != nil {
			return nil, err
		}
		ops := []clientv3.Op{prevOp, newOp}
		sessCmps, sessOps, ids, err := s.cancelSessionPieces(ctx, "token_jti", previousJTI, "token_rotated")
		if err != nil {
			return nil, err
		}
		cmps = append(cmps, sessCmps...)
		ops = append(ops, sessOps...)
		resp, err := s.kv.Txn(ctx).If(cmps...).Then(ops...).Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return ids, nil
		}
	}
	return nil, fmt.Errorf("rotate token %s: exhausted retries under contention", previousJTI)
}

// RevokeAccessToken revokes the token and cancels its exec sessions atomically.
func (s *AuthStore) RevokeAccessToken(ctx context.Context, jti string) ([]string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		tok, rev, err := s.loadToken(ctx, jti)
		if err != nil {
			return nil, err
		}
		if tok.RevokedAt == nil {
			now := time.Now()
			tok.RevokedAt = &now
		}
		cmps := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.tokenKey(jti)), "=", rev)}
		tokOp, err := opPut(s.tokenKey(jti), tok)
		if err != nil {
			return nil, err
		}
		ops := []clientv3.Op{tokOp}
		sessCmps, sessOps, ids, err := s.cancelSessionPieces(ctx, "token_jti", jti, "token_revoked")
		if err != nil {
			return nil, err
		}
		cmps = append(cmps, sessCmps...)
		ops = append(ops, sessOps...)
		resp, err := s.kv.Txn(ctx).If(cmps...).Then(ops...).Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return ids, nil
		}
	}
	return nil, fmt.Errorf("revoke token %s: exhausted retries under contention", jti)
}

// RevokeAccessDevice revokes the device, all its tokens, and cancels the exec
// sessions they authorized — all in one atomic multi-key transaction.
func (s *AuthStore) RevokeAccessDevice(ctx context.Context, id string) ([]string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		dev, devRev, err := s.loadDevice(ctx, id)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		if dev.RevokedAt == nil {
			dev.RevokedAt = &now
		}
		cmps := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.deviceKey(id)), "=", devRev)}
		devOp, err := opPut(s.deviceKey(id), storedDevice{AccessDevice: *dev, PublicKeyStored: dev.PublicKey})
		if err != nil {
			return nil, err
		}
		ops := []clientv3.Op{devOp}
		tokens, err := s.tokensForDevice(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, tr := range tokens {
			if tr.t.RevokedAt == nil {
				tr.t.RevokedAt = &now
			}
			cmps = append(cmps, clientv3.Compare(clientv3.ModRevision(s.tokenKey(tr.t.JTI)), "=", tr.rev))
			tokOp, err := opPut(s.tokenKey(tr.t.JTI), tr.t)
			if err != nil {
				return nil, err
			}
			ops = append(ops, tokOp)
		}
		sessCmps, sessOps, ids, err := s.cancelSessionPieces(ctx, "device_id", id, "device_revoked")
		if err != nil {
			return nil, err
		}
		cmps = append(cmps, sessCmps...)
		ops = append(ops, sessOps...)
		resp, err := s.kv.Txn(ctx).If(cmps...).Then(ops...).Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return ids, nil
		}
	}
	return nil, fmt.Errorf("revoke device %s: exhausted retries under contention", id)
}
