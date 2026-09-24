package etcdstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
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
