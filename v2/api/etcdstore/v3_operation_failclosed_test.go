package etcdstore

import (
	"context"
	"testing"
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
