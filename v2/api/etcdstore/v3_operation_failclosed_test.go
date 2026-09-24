package etcdstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestIncompleteV3RecoveryAndAppLockFailClosed(t *testing.T) {
	s := &V3OperationStore{}
	if err := s.RecoverExpiredOperations(context.Background()); err == nil {
		t.Fatal("incomplete operation recovery must block worker startup")
	}
	lock, acquired, err := s.AcquireAppOperationLock(context.Background(), "example")
	if lock != nil {
		lock.Release()
	}
	if err == nil || acquired {
		t.Fatalf("incomplete app lock acquired=%v err=%v", acquired, err)
	}
}

func TestLegacyAppOperationLockFailsClosedWithoutLeaseClient(t *testing.T) {
	lock, acquired, err := (&OperationStore{}).AcquireAppOperationLock(context.Background(), "example")
	if lock != nil || acquired || err == nil {
		t.Fatalf("legacy app lock lock=%v acquired=%v err=%v", lock, acquired, err)
	}
}

type blackholeAppLockLease struct {
	deadline time.Time
}

func (b *blackholeAppLockLease) KeepAliveOnce(ctx context.Context, _ clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error) {
	b.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAppLockKeepAliveDeadlineBoundsBlackhole(t *testing.T) {
	blackhole := &blackholeAppLockLease{}
	start := time.Now()
	_, err := boundedAppLockKeepAlive(context.Background(), blackhole, 1, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blackhole keepalive err=%v", err)
	}
	if blackhole.deadline.IsZero() || !blackhole.deadline.Before(start.Add(100*time.Millisecond)) {
		t.Fatalf("keepalive deadline=%v is not shorter than remaining TTL", blackhole.deadline)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("blackhole keepalive took %v, want less than remaining TTL", elapsed)
	}
}

func TestV3OperationNormalizationMatchesSharedAuditAndRequestBounds(t *testing.T) {
	authority := uuid.NewString()
	s := &V3OperationStore{authority: authority}
	newAcceptance := func() store.OperationAcceptance {
		a := store.OperationAcceptance{
			Identity:  store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "issuer", Subject: "subject"}, Kind: "app.preflight", Resource: "app/example", Key: uuid.NewString()},
			Operation: model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: "example"},
			Audit:     store.AcceptanceAuditContext{Source: "test", Scopes: []string{" write ", "read", "write"}},
		}
		var err error
		a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newAcceptance()
	normalized, err := s.normalize(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(normalized.Audit.Scopes, ","); got != "read,write" {
		t.Fatalf("scopes=%q", got)
	}

	tooManyScopes := newAcceptance()
	tooManyScopes.Audit.Scopes = make([]string, 257)
	for index := range tooManyScopes.Audit.Scopes {
		tooManyScopes.Audit.Scopes[index] = uuid.NewString()
	}
	if _, err := s.normalize(tooManyScopes); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("too many scopes err=%v", err)
	}

	overLimit := newAcceptance()
	overLimit.Semantics = map[string]interface{}{"blob": strings.Repeat("x", 1024*1024)}
	overLimit.Fingerprint, err = store.CanonicalOperationRequestFingerprint(overLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.normalize(overLimit); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("oversized canonical request err=%v", err)
	}
}

func TestIncompleteV3AggregateAdmissionRefusesBeforeWriting(t *testing.T) {
	s := &V3OperationStore{authority: "authority"}
	input := store.OperationAcceptance{
		Identity:   store.OperationRequestIdentity{Authority: "authority"},
		Deployment: &model.Deployment{ID: "deployment"},
	}
	if _, err := s.Accept(context.Background(), input); err == nil {
		t.Fatal("incomplete etcd deployment admission must be refused")
	}
}
