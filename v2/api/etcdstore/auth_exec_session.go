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

// This file completes the etcd auth aggregate with the exec-session boundary:
// step-up challenges, session creation gated on a verified challenge with a
// per-device active cap, connect/finish/expire, and the credential-bound
// cancellation that identity revocation composes into its atomic transaction.

// --- stored wrappers for json:"-" fields ---

type storedChallenge struct {
	store.StepUpChallenge
	NonceHashStored string `json:"nonceHashStored"`
}

type storedSession struct {
	store.ExecSession
	CommandStored    []string   `json:"commandStored"`
	OwnerIDStored    string     `json:"ownerId,omitempty"`
	OwnerTokenStored string     `json:"ownerToken,omitempty"`
	OwnerLeaseUntil  *time.Time `json:"ownerLeaseUntil,omitempty"`
}

func (s *AuthStore) putChallenge(ctx context.Context, c *store.StepUpChallenge) error {
	return s.putJSON(ctx, s.challengeKey(c.ID), storedChallenge{StepUpChallenge: *c, NonceHashStored: c.NonceHash})
}

func challengeFrom(sc storedChallenge) store.StepUpChallenge {
	c := sc.StepUpChallenge
	c.NonceHash = sc.NonceHashStored
	return c
}

func (s *AuthStore) sessionOp(sess *store.ExecSession) (clientv3.Op, error) {
	return s.sessionOpOwned(sess, "", "", nil)
}

func (s *AuthStore) putSession(ctx context.Context, sess *store.ExecSession) error {
	return s.putJSON(ctx, s.sessionKey(sess.ID), storedSession{ExecSession: *sess, CommandStored: sess.Command})
}

func (s *AuthStore) sessionOpOwned(sess *store.ExecSession, ownerID, ownerToken string, leaseUntil *time.Time) (clientv3.Op, error) {
	return opPut(s.sessionKey(sess.ID), storedSession{ExecSession: *sess, CommandStored: sess.Command, OwnerIDStored: ownerID, OwnerTokenStored: ownerToken, OwnerLeaseUntil: leaseUntil})
}

func sessionFrom(ss storedSession) store.ExecSession {
	sess := ss.ExecSession
	sess.Command = ss.CommandStored
	return sess
}

// --- step-up challenges ---

func (s *AuthStore) loadChallenge(ctx context.Context, id string) (*store.StepUpChallenge, int64, error) {
	resp, err := s.kv.Get(ctx, s.challengeKey(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var sc storedChallenge
	if err := json.Unmarshal(resp.Kvs[0].Value, &sc); err != nil {
		return nil, 0, err
	}
	c := challengeFrom(sc)
	return &c, resp.Kvs[0].ModRevision, nil
}

func (s *AuthStore) CreateStepUpChallenge(ctx context.Context, challenge *store.StepUpChallenge) error {
	// Rate limit mirrors PostgreSQL: <20 challenges per device in 10 minutes.
	resp, err := s.kv.Get(ctx, s.challengePrefix(), clientv3.WithPrefix())
	if err != nil {
		return err
	}
	since := time.Now().Add(-10 * time.Minute)
	recent := 0
	for _, kv := range resp.Kvs {
		var sc storedChallenge
		if err := json.Unmarshal(kv.Value, &sc); err != nil {
			return err
		}
		if sc.DeviceID == challenge.DeviceID && sc.CreatedAt.After(since) {
			recent++
		}
	}
	if recent >= 20 {
		return store.ErrRateLimited
	}
	if challenge.Status == "" {
		challenge.Status = "pending"
	}
	return s.putChallenge(ctx, challenge)
}

func (s *AuthStore) GetStepUpChallenge(ctx context.Context, id string) (*store.StepUpChallenge, error) {
	c, _, err := s.loadChallenge(ctx, id)
	return c, err
}

func (s *AuthStore) VerifyStepUpChallenge(ctx context.Context, id, deviceID string) error {
	for attempt := 0; attempt < 32; attempt++ {
		c, rev, err := s.loadChallenge(ctx, id)
		if err != nil {
			return err
		}
		if c.DeviceID != deviceID || c.Status != "pending" || !c.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		c.Status = "verified"
		now := time.Now()
		c.VerifiedAt = &now
		op, err := opPut(s.challengeKey(id), storedChallenge{StepUpChallenge: *c, NonceHashStored: c.NonceHash})
		if err != nil {
			return err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.challengeKey(id)), "=", rev)).
			Then(op).Commit()
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return fmt.Errorf("verify challenge %s: exhausted retries under contention", id)
}

// --- exec sessions ---

type sessionRev struct {
	s                   *store.ExecSession
	rev                 int64
	ownerID, ownerToken string
	leaseUntil          *time.Time
}

func (s *AuthStore) scanSessions(ctx context.Context) ([]sessionRev, error) {
	resp, err := s.kv.Get(ctx, s.sessionPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]sessionRev, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var ss storedSession
		if err := json.Unmarshal(kv.Value, &ss); err != nil {
			return nil, err
		}
		sess := sessionFrom(ss)
		out = append(out, sessionRev{s: &sess, rev: kv.ModRevision, ownerID: ss.OwnerIDStored, ownerToken: ss.OwnerTokenStored, leaseUntil: ss.OwnerLeaseUntil})
	}
	return out, nil
}

func (s *AuthStore) loadSession(ctx context.Context, id string) (*store.ExecSession, int64, error) {
	resp, err := s.kv.Get(ctx, s.sessionKey(id))
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, 0, ErrNotFound
	}
	var ss storedSession
	if err := json.Unmarshal(resp.Kvs[0].Value, &ss); err != nil {
		return nil, 0, err
	}
	sess := sessionFrom(ss)
	return &sess, resp.Kvs[0].ModRevision, nil
}

func (s *AuthStore) CreateExecSession(ctx context.Context, session *store.ExecSession) error {
	if session.Status == "" {
		session.Status = "pending"
	}
	for attempt := 0; attempt < 32; attempt++ {
		now := time.Now()
		// Expire stale sessions before counting. The admission gate below makes
		// competing creators serialize, while the per-session compares keep an
		// observed finish or cancellation from being counted stale.
		_ = s.ExpireExecSessions(ctx)
		sessions, err := s.scanSessions(ctx)
		if err != nil {
			return err
		}
		active, recent := 0, 0
		cmps := make([]clientv3.Cmp, 0, len(sessions)+5)
		for _, sr := range sessions {
			if sr.s.DeviceID != session.DeviceID {
				continue
			}
			cmps = append(cmps, clientv3.Compare(clientv3.ModRevision(s.sessionKey(sr.s.ID)), "=", sr.rev))
			if sr.s.Status == "pending" || sr.s.Status == "running" {
				active++
			}
			if sr.s.CreatedAt.After(now.Add(-time.Hour)) {
				recent++
			}
		}
		if active >= 3 {
			return store.ErrTooManyActiveExecSessions
		}
		if recent >= 30 {
			return store.ErrRateLimited
		}

		// The device and token revisions are part of the same transaction as
		// session creation. A revoke that wins after these reads makes the CAS
		// fail, after which the next iteration rejects the revoked credential.
		dev, devRev, err := s.loadDevice(ctx, session.DeviceID)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		token, tokenRev, err := s.loadToken(ctx, session.TokenJTI)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if dev.RevokedAt != nil || token.RevokedAt != nil || !token.ExpiresAt.After(now) || token.DeviceID != session.DeviceID {
			return ErrNotFound
		}

		ch, chRev, err := s.loadChallenge(ctx, session.ChallengeID)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ch.DeviceID != session.DeviceID || ch.Purpose != "exec" || ch.Resource != session.AppID ||
			ch.TokenJTI != session.TokenJTI || ch.Status != "verified" || !ch.ExpiresAt.After(now) {
			return ErrNotFound
		}
		ch.Status = "consumed"
		ch.ConsumedAt = &now
		chOp, err := opPut(s.challengeKey(ch.ID), storedChallenge{StepUpChallenge: *ch, NonceHashStored: ch.NonceHash})
		if err != nil {
			return err
		}
		sessOp, err := s.sessionOp(session)
		if err != nil {
			return err
		}

		gate := s.sessionGateKey(session.DeviceID)
		gateResp, err := s.kv.Get(ctx, gate)
		if err != nil {
			return err
		}
		if len(gateResp.Kvs) == 0 {
			cmps = append(cmps, clientv3.Compare(clientv3.CreateRevision(gate), "=", 0))
		} else {
			cmps = append(cmps, clientv3.Compare(clientv3.ModRevision(gate), "=", gateResp.Kvs[0].ModRevision))
		}
		cmps = append(cmps,
			clientv3.Compare(clientv3.ModRevision(s.deviceKey(dev.ID)), "=", devRev),
			clientv3.Compare(clientv3.ModRevision(s.tokenKey(token.JTI)), "=", tokenRev),
			clientv3.Compare(clientv3.ModRevision(s.challengeKey(ch.ID)), "=", chRev),
			clientv3.Compare(clientv3.CreateRevision(s.sessionKey(session.ID)), "=", 0),
		)
		if s.test.beforeCreateCommit != nil {
			s.test.beforeCreateCommit()
		}
		resp, err := s.kv.Txn(ctx).If(cmps...).Then(chOp, sessOp, clientv3.OpPut(gate, now.UTC().Format(time.RFC3339Nano))).Commit()
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return fmt.Errorf("create exec session %s: exhausted retries under contention", session.ID)
}

func (s *AuthStore) GetExecSession(ctx context.Context, id string) (*store.ExecSession, error) {
	_ = s.ExpireExecSessions(ctx)
	sess, _, err := s.loadSession(ctx, id)
	return sess, err
}

func (s *AuthStore) ExpireExecSessions(ctx context.Context) error {
	sessions, err := s.scanSessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, sr := range sessions {
		if sr.s.Status != "pending" || sr.s.ExpiresAt.After(now) {
			continue
		}
		sr.s.Status = "expired"
		sr.s.FinishedAt = &now
		sr.s.ErrorCode = "exec_session_expired"
		op, err := s.sessionOpOwned(sr.s, sr.ownerID, sr.ownerToken, sr.leaseUntil)
		if err != nil {
			return err
		}
		// CAS so a concurrent connect/cancel is not clobbered; a lost race means
		// the session already moved off pending, which is fine.
		_, _ = s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.sessionKey(sr.s.ID)), "=", sr.rev)).
			Then(op).Commit()
	}
	return nil
}

func (s *AuthStore) ConnectExecSession(ctx context.Context, id string, claim store.ExecSessionClaim) (bool, error) {
	if claim.OwnerID == "" || claim.OwnerToken == "" || claim.LeaseDuration <= 0 {
		return false, errors.New("exec session claim is incomplete")
	}
	for attempt := 0; attempt < 32; attempt++ {
		resp, err := s.kv.Get(ctx, s.sessionKey(id))
		if err != nil {
			return false, err
		}
		if len(resp.Kvs) == 0 {
			return false, nil
		}
		var stored storedSession
		if err := json.Unmarshal(resp.Kvs[0].Value, &stored); err != nil {
			return false, err
		}
		sess := sessionFrom(stored)
		if sess.Status != "pending" || !sess.ExpiresAt.After(time.Now()) {
			return false, nil
		}
		dev, devRev, err := s.loadDevice(ctx, sess.DeviceID)
		if err != nil {
			return false, err
		}
		token, tokenRev, err := s.loadToken(ctx, sess.TokenJTI)
		if err != nil {
			return false, err
		}
		if dev.RevokedAt != nil || token.RevokedAt != nil || !token.ExpiresAt.After(time.Now()) || token.DeviceID != sess.DeviceID {
			return false, nil
		}
		now := time.Now()
		until := now.Add(claim.LeaseDuration)
		sess.Status = "running"
		sess.ConnectedAt = &now
		sess.Command = []string{}
		op, err := s.sessionOpOwned(&sess, claim.OwnerID, claim.OwnerToken, &until)
		if err != nil {
			return false, err
		}
		if s.test.beforeConnectCommit != nil {
			s.test.beforeConnectCommit()
		}
		txn, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.sessionKey(id)), "=", resp.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.ModRevision(s.deviceKey(dev.ID)), "=", devRev),
			clientv3.Compare(clientv3.ModRevision(s.tokenKey(token.JTI)), "=", tokenRev),
		).Then(op).Commit()
		if err != nil {
			return false, err
		}
		if txn.Succeeded {
			return true, nil
		}
	}
	return false, fmt.Errorf("connect session %s: exhausted retries under contention", id)
}

func (s *AuthStore) RenewExecSession(ctx context.Context, id, ownerID, ownerToken string, lease time.Duration) (bool, error) {
	if ownerID == "" || ownerToken == "" || lease <= 0 {
		return false, errors.New("exec session lease renewal is incomplete")
	}
	resp, err := s.kv.Get(ctx, s.sessionKey(id))
	if err != nil {
		return false, err
	}
	if len(resp.Kvs) == 0 {
		return false, nil
	}
	var stored storedSession
	if err = json.Unmarshal(resp.Kvs[0].Value, &stored); err != nil {
		return false, err
	}
	sess := sessionFrom(stored)
	if sess.Status != "running" || stored.OwnerIDStored != ownerID || stored.OwnerTokenStored != ownerToken || stored.OwnerLeaseUntil == nil || !stored.OwnerLeaseUntil.After(time.Now()) || !sess.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	until := time.Now().Add(lease)
	op, err := s.sessionOpOwned(&sess, ownerID, ownerToken, &until)
	if err != nil {
		return false, err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.sessionKey(id)), "=", resp.Kvs[0].ModRevision)).Then(op).Commit()
	return txn.Succeeded, err
}

func (s *AuthStore) FinishOwnedExecSession(ctx context.Context, id, ownerID, ownerToken, status string, exitCode *int, errorCode string) (bool, error) {
	resp, err := s.kv.Get(ctx, s.sessionKey(id))
	if err != nil {
		return false, err
	}
	if len(resp.Kvs) == 0 {
		return false, nil
	}
	var stored storedSession
	if err = json.Unmarshal(resp.Kvs[0].Value, &stored); err != nil {
		return false, err
	}
	sess := sessionFrom(stored)
	if sess.Status != "running" || stored.OwnerIDStored != ownerID || stored.OwnerTokenStored != ownerToken || stored.OwnerLeaseUntil == nil || !stored.OwnerLeaseUntil.After(time.Now()) || !sess.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	now := time.Now()
	sess.Status = status
	sess.FinishedAt = &now
	sess.ExitCode = exitCode
	sess.ErrorCode = errorCode
	op, err := s.sessionOpOwned(&sess, ownerID, ownerToken, stored.OwnerLeaseUntil)
	if err != nil {
		return false, err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.sessionKey(id)), "=", resp.Kvs[0].ModRevision)).Then(op).Commit()
	return txn.Succeeded, err
}

// RecoverExpiredExecSessions fails only sessions whose private owner lease expired.
func (s *AuthStore) RecoverExpiredExecSessions(ctx context.Context) error {
	sessions, err := s.scanSessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, sr := range sessions {
		if sr.s.Status != "running" {
			continue
		}
		if sr.leaseUntil == nil || sr.ownerID == "" || sr.ownerToken == "" || sr.leaseUntil.After(now) {
			continue
		}
		sr.s.Status = "failed"
		sr.s.FinishedAt = &now
		sr.s.ErrorCode = "server_restarted"
		op, err := s.sessionOpOwned(sr.s, sr.ownerID, sr.ownerToken, sr.leaseUntil)
		if err != nil {
			return err
		}
		// CAS so a concurrent finish/cancel is not clobbered; a lost race means
		// the session already moved off running, which is fine.
		_, _ = s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.sessionKey(sr.s.ID)), "=", sr.rev)).
			Then(op).Commit()
	}
	return nil
}

func (s *AuthStore) ReconcileExecSessions(ctx context.Context) error {
	if err := s.ExpireExecSessions(ctx); err != nil {
		return err
	}
	return s.RecoverExpiredExecSessions(ctx)
}

func (s *AuthStore) FinishExecSession(ctx context.Context, id, status string, exitCode *int, errorCode string) error {
	for attempt := 0; attempt < 32; attempt++ {
		sess, rev, err := s.loadSession(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if sess.Status != "pending" && sess.Status != "running" {
			return nil
		}
		now := time.Now()
		sess.Status = status
		sess.FinishedAt = &now
		sess.ExitCode = exitCode
		sess.ErrorCode = errorCode
		op, err := s.sessionOp(sess)
		if err != nil {
			return err
		}
		resp, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.sessionKey(id)), "=", rev)).
			Then(op).Commit()
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return fmt.Errorf("finish session %s: exhausted retries under contention", id)
}

// ExecSessionAuthorized reports whether the session is still active and its
// bound device is not revoked — the execution-boundary fence re-checked at use,
// independent of the session's status cascade. Mirrors the PostgreSQL adapter.
func (s *AuthStore) ExecSessionAuthorized(ctx context.Context, sessionID string) (bool, error) {
	sess, _, err := s.loadSession(ctx, sessionID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if sess.Status != "pending" && sess.Status != "running" {
		return false, nil
	}
	dev, _, derr := s.loadDevice(ctx, sess.DeviceID)
	if errors.Is(derr, ErrNotFound) {
		return false, nil
	}
	if derr != nil {
		return false, derr
	}
	if dev.RevokedAt != nil {
		return false, nil
	}
	return true, nil
}

func (s *AuthStore) ListExecSessions(ctx context.Context, deviceID string, all bool) ([]store.ExecSession, error) {
	_ = s.ExpireExecSessions(ctx)
	sessions, err := s.scanSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.ExecSession{}
	for _, sr := range sessions {
		if !all && sr.s.DeviceID != deviceID {
			continue
		}
		out = append(out, *sr.s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

func sessionFieldValue(sess *store.ExecSession, field string) (string, bool) {
	switch field {
	case "token_jti":
		return sess.TokenJTI, true
	case "device_id":
		return sess.DeviceID, true
	default:
		return "", false
	}
}

// cancelSessionPieces returns the compare/op pairs and cancelled ids for every
// pending/running session bound to a credential. The identity revocation methods
// compose these into their single atomic transaction so revoke-plus-cancel
// commits (or fails) as one unit — the ADR 0007 invariant.
func (s *AuthStore) cancelSessionPieces(ctx context.Context, field, value, code string) ([]clientv3.Cmp, []clientv3.Op, []string, error) {
	sessions, err := s.scanSessions(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	var cmps []clientv3.Cmp
	var ops []clientv3.Op
	ids := []string{}
	for _, sr := range sessions {
		fv, ok := sessionFieldValue(sr.s, field)
		if !ok {
			return nil, nil, nil, fmt.Errorf("unsupported exec session cancel column %q", field)
		}
		if fv != value || (sr.s.Status != "pending" && sr.s.Status != "running") {
			continue
		}
		sr.s.Status = "canceled"
		sr.s.FinishedAt = &now
		sr.s.ErrorCode = code
		cmps = append(cmps, clientv3.Compare(clientv3.ModRevision(s.sessionKey(sr.s.ID)), "=", sr.rev))
		op, err := s.sessionOpOwned(sr.s, sr.ownerID, sr.ownerToken, sr.leaseUntil)
		if err != nil {
			return nil, nil, nil, err
		}
		ops = append(ops, op)
		ids = append(ids, sr.s.ID)
	}
	return cmps, ops, ids, nil
}

// CancelActiveExecSessions cancels the pending/running sessions bound to a
// credential (column "token_jti" or "device_id") and returns their ids. This is
// the standalone entry point; identity revocation cancels within its own atomic
// transaction via cancelSessionPieces so revocation stays all-or-nothing.
func (s *AuthStore) CancelActiveExecSessions(ctx context.Context, column, value, errorCode string) ([]string, error) {
	switch column {
	case "token_jti", "device_id":
	default:
		return nil, fmt.Errorf("unsupported exec session cancel column %q", column)
	}
	for attempt := 0; attempt < 32; attempt++ {
		cmps, ops, ids, err := s.cancelSessionPieces(ctx, column, value, errorCode)
		if err != nil {
			return nil, err
		}
		if len(ops) == 0 {
			return ids, nil
		}
		resp, err := s.kv.Txn(ctx).If(cmps...).Then(ops...).Commit()
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return ids, nil
		}
	}
	return nil, fmt.Errorf("cancel active exec sessions: exhausted retries under contention")
}
