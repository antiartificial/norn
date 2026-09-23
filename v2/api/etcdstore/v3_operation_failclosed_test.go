package etcdstore

import (
	"context"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestIncompleteV3RecoveryAndAppLockFailClosed(t *testing.T) {
	s := &V3OperationStore{}
	if err := s.RecoverExpiredOperations(context.Background()); err == nil {
		t.Fatal("incomplete operation recovery must block worker startup")
	}
	release, acquired, err := s.AcquireAppOperationLock(context.Background(), "example")
	if release != nil {
		release()
	}
	if err == nil || acquired {
		t.Fatalf("incomplete app lock acquired=%v err=%v", acquired, err)
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
