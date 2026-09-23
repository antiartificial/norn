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
	CommandStored []string `json:"commandStored"`
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
	return opPut(s.sessionKey(sess.ID), storedSession{ExecSession: *sess, CommandStored: sess.Command})
}

func (s *AuthStore) putSession(ctx context.Context, sess *store.ExecSession) error {
	return s.putJSON(ctx, s.sessionKey(sess.ID), storedSession{ExecSession: *sess, CommandStored: sess.Command})
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
	s   *store.ExecSession
	rev int64
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
		out = append(out, sessionRev{s: &sess, rev: kv.ModRevision})
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
	now := time.Now()
	// Expire the device's stale pending sessions and count active/recent ones,
	// mirroring the PostgreSQL adapter's pre-insert bookkeeping.
	_ = s.ExpireExecSessions(ctx)
	sessions, err := s.scanSessions(ctx)
	if err != nil {
		return err
	}
	active, recent := 0, 0
	for _, sr := range sessions {
		if sr.s.DeviceID != session.DeviceID {
			continue
		}
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
	// Consume the matching verified challenge and insert the session atomically.
	ch, chRev, err := s.loadChallenge(ctx, session.ChallengeID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if ch.DeviceID != session.DeviceID || ch.Purpose != "exec" || ch.Resource != session.AppID ||
		ch.TokenJTI != session.TokenJTI || ch.Status != "verified" || !ch.ExpiresAt.After(now) {
		return ErrNotFound
	}
	ch.Status = "consumed"
	ch.ConsumedAt = &now
	if session.Status == "" {
		session.Status = "pending"
	}
	chOp, err := opPut(s.challengeKey(ch.ID), storedChallenge{StepUpChallenge: *ch, NonceHashStored: ch.NonceHash})
	if err != nil {
		return err
	}
	sessOp, err := s.sessionOp(session)
	if err != nil {
		return err
	}
	resp, err := s.kv.Txn(ctx).
		If(
			clientv3.Compare(clientv3.ModRevision(s.challengeKey(ch.ID)), "=", chRev),
			clientv3.Compare(clientv3.CreateRevision(s.sessionKey(session.ID)), "=", 0),
		).
		Then(chOp, sessOp).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		// The challenge was consumed or the session id already exists.
		return ErrNotFound
	}
	return nil
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
		op, err := s.sessionOp(sr.s)
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

func (s *AuthStore) ConnectExecSession(ctx context.Context, id string) error {
	for attempt := 0; attempt < 32; attempt++ {
		sess, rev, err := s.loadSession(ctx, id)
		if err != nil {
			return err
		}
		if sess.Status != "pending" || !sess.ExpiresAt.After(time.Now()) {
			return ErrNotFound
		}
		dev, _, derr := s.loadDevice(ctx, sess.DeviceID)
		if derr != nil {
			if errors.Is(derr, ErrNotFound) {
				return ErrNotFound
			}
			return derr
		}
		if dev.RevokedAt != nil {
			return ErrNotFound
		}
		now := time.Now()
		sess.Status = "running"
		sess.ConnectedAt = &now
		sess.Command = []string{}
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
	return fmt.Errorf("connect session %s: exhausted retries under contention", id)
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
		op, err := s.sessionOp(sr.s)
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
