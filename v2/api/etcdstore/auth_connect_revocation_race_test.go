package etcdstore

import (
	"context"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/store"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConnectExecSessionCannotRaceCredentialRevocation(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for _, tc := range []struct {
		name   string
		revoke func(context.Context, *AuthStore, string) ([]string, error)
	}{{"token", func(ctx context.Context, s *AuthStore, id string) ([]string, error) {
		return s.RevokeAccessToken(ctx, id)
	}}, {"device", func(ctx context.Context, s *AuthStore, id string) ([]string, error) {
		return s.RevokeAccessDevice(ctx, id)
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "/norn-test/connect-revoke/" + uuid.NewString()
			ctx := context.Background()
			if _, err := client.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
			s := NewAuthStore(client, prefix)
			device, token, session := provisionPendingSession(t, s)
			entered := make(chan struct{})
			release := make(chan struct{})
			s.test.beforeConnectCommit = func() { close(entered); <-release }
			result := make(chan struct {
				ok  bool
				err error
			}, 1)
			go func() {
				ok, err := s.ConnectExecSession(ctx, session, store.ExecSessionClaim{OwnerID: "race", OwnerToken: "secret", LeaseDuration: time.Minute})
				result <- struct {
					ok  bool
					err error
				}{ok, err}
			}()
			<-entered
			id := token
			if tc.name == "device" {
				id = device
			}
			ids, err := tc.revoke(ctx, s, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 1 || ids[0] != session {
				t.Fatalf("canceled ids=%v", ids)
			}
			close(release)
			got := <-result
			if got.err != nil || got.ok {
				t.Fatalf("connect after revoke ok=%v err=%v", got.ok, got.err)
			}
			stored, err := s.GetExecSession(ctx, session)
			if err != nil || stored.Status != "canceled" {
				t.Fatalf("session=%+v err=%v", stored, err)
			}
		})
	}
}
func provisionPendingSession(t *testing.T, s *AuthStore) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	device := uuid.NewString()
	token := uuid.NewString()
	if err := s.CreateAccessDevice(ctx, &store.AccessDevice{ID: device, Name: "race", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccessToken(ctx, &store.AccessToken{JTI: token, DeviceID: device, Subject: "race", Scopes: []string{"api:read"}, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	challenge := uuid.NewString()
	if err := s.CreateStepUpChallenge(ctx, &store.StepUpChallenge{ID: challenge, DeviceID: device, TokenJTI: token, Purpose: "exec", Resource: "app", NonceHash: "nonce", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyStepUpChallenge(ctx, challenge, device); err != nil {
		t.Fatal(err)
	}
	session := uuid.NewString()
	if err := s.CreateExecSession(ctx, &store.ExecSession{ID: session, DeviceID: device, TokenJTI: token, ChallengeID: challenge, AppID: "app", AllocationID: "alloc", Task: "web", Command: []string{"sh"}, CommandDigest: "digest", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return device, token, session
}
