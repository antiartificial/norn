package etcdstore

import (
	"context"
	"errors"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/store"
	"os"
	"strings"
	"sync"
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

func TestCreateExecSessionCannotRaceCredentialRevocation(t *testing.T) {
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
			prefix := "/norn-test/create-revoke/" + uuid.NewString()
			ctx := context.Background()
			if _, err := client.Delete(ctx, prefix, clientv3.WithPrefix()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
			s := NewAuthStore(client, prefix)
			device, token, challenge := provisionVerifiedExecChallenge(t, s)
			session := execSession(device, token, challenge)
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			s.test.beforeCreateCommit = func() { once.Do(func() { close(entered); <-release }) }
			result := make(chan error, 1)
			go func() { result <- s.CreateExecSession(ctx, session) }()
			<-entered
			id := token
			if tc.name == "device" {
				id = device
			}
			ids, err := tc.revoke(ctx, s, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 0 {
				t.Fatalf("revocation unexpectedly canceled sessions=%v", ids)
			}
			close(release)
			if err := <-result; !errors.Is(err, ErrNotFound) {
				t.Fatalf("create after revoke err=%v, want ErrNotFound", err)
			}
			if _, err := s.GetExecSession(ctx, session.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("session persisted after revoke: %v", err)
			}
		})
	}
}

func TestCreateExecSessionPerDeviceCapIsAtomic(t *testing.T) {
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/create-cap/" + uuid.NewString()
	ctx := context.Background()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	s := NewAuthStore(client, prefix)
	device, token, _ := provisionVerifiedExecChallenge(t, s)
	sessions := make([]*store.ExecSession, 4)
	for i := range sessions {
		_, _, challenge := provisionVerifiedExecChallengeForDevice(t, s, device, token)
		sessions[i] = execSession(device, token, challenge)
	}
	start := make(chan struct{})
	results := make(chan error, len(sessions))
	for _, session := range sessions {
		go func(session *store.ExecSession) {
			<-start
			results <- s.CreateExecSession(ctx, session)
		}(session)
	}
	close(start)
	successes, capped := 0, 0
	for range sessions {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrTooManyActiveExecSessions):
			capped++
		default:
			t.Fatalf("create err=%v", err)
		}
	}
	if successes != 3 || capped != 1 {
		t.Fatalf("successes=%d capped=%d, want 3 and 1", successes, capped)
	}
}

func provisionPendingSession(t *testing.T, s *AuthStore) (string, string, string) {
	t.Helper()
	device, token, challenge := provisionVerifiedExecChallenge(t, s)
	session := execSession(device, token, challenge)
	if err := s.CreateExecSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	return device, token, session.ID
}

func provisionVerifiedExecChallenge(t *testing.T, s *AuthStore) (string, string, string) {
	t.Helper()
	device := uuid.NewString()
	token := uuid.NewString()
	ctx := context.Background()
	if err := s.CreateAccessDevice(ctx, &store.AccessDevice{ID: device, Name: "race", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccessToken(ctx, &store.AccessToken{JTI: token, DeviceID: device, Subject: "race", Scopes: []string{"api:read"}, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return provisionVerifiedExecChallengeForDevice(t, s, device, token)
}

func provisionVerifiedExecChallengeForDevice(t *testing.T, s *AuthStore, device, token string) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	challenge := uuid.NewString()
	if err := s.CreateStepUpChallenge(ctx, &store.StepUpChallenge{ID: challenge, DeviceID: device, TokenJTI: token, Purpose: "exec", Resource: "app", NonceHash: "nonce", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyStepUpChallenge(ctx, challenge, device); err != nil {
		t.Fatal(err)
	}
	return device, token, challenge
}

func execSession(device, token, challenge string) *store.ExecSession {
	return &store.ExecSession{ID: uuid.NewString(), DeviceID: device, TokenJTI: token, ChallengeID: challenge, AppID: "app", AllocationID: "alloc", Task: "web", Command: []string{"sh"}, CommandDigest: "digest", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
}
