package etcdstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

func enrollmentAdmissionHarness(t *testing.T) *AuthStore {
	t.Helper()
	endpoints := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/norn-test/enrollment-admission/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()); _ = client.Close() })
	return NewAuthStore(client, prefix)
}

func TestConcurrentEtcdEnrollmentAdmissionEnforcesSourceCap(t *testing.T) {
	auth := enrollmentAdmissionHarness(t)
	const requests = 48
	var wg sync.WaitGroup
	results := make(chan error, requests)
	for index := 0; index < requests; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			results <- auth.CreateAccessEnrollment(ctx, &store.AccessEnrollment{ID: uuid.NewString(), SourceHash: "same-source", CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(10 * time.Minute), Status: "pending"})
		}(index)
	}
	wg.Wait()
	close(results)
	accepted, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, store.ErrRateLimited):
			limited++
		default:
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	if accepted != 10 || limited != requests-10 {
		t.Fatalf("accepted=%d limited=%d, want 10/%d", accepted, limited, requests-10)
	}
	rows, err := auth.scanEnrollments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 {
		t.Fatalf("durable enrollments=%d want 10", len(rows))
	}
	for _, row := range rows {
		if row.CreatedAt.Before(time.Now().Add(-time.Minute)) {
			t.Fatalf("caller timestamp bypassed window: %s", row.CreatedAt)
		}
	}
}

func TestConcurrentEtcdEnrollmentAdmissionEnforcesGlobalCap(t *testing.T) {
	auth := enrollmentAdmissionHarness(t)
	const requests = 112
	var wg sync.WaitGroup
	results := make(chan error, requests)
	for index := 0; index < requests; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			results <- auth.CreateAccessEnrollment(ctx, &store.AccessEnrollment{ID: uuid.NewString(), SourceHash: fmt.Sprintf("source-%03d", index), ExpiresAt: time.Now().Add(10 * time.Minute)})
		}(index)
	}
	wg.Wait()
	close(results)
	accepted, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, store.ErrRateLimited):
			limited++
		default:
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	if accepted != 100 || limited != requests-100 {
		t.Fatalf("accepted=%d limited=%d, want 100/%d", accepted, limited, requests-100)
	}
	rows, err := auth.scanEnrollments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 100 {
		t.Fatalf("durable enrollments=%d want 100", len(rows))
	}
}

func TestEtcdEnrollmentAdmissionGlobalWindowAndRetirement(t *testing.T) {
	auth := enrollmentAdmissionHarness(t)
	ctx := context.Background()
	old := &store.AccessEnrollment{ID: uuid.NewString(), SourceHash: "old", CreatedAt: time.Now().Add(-31 * 24 * time.Hour), ExpiresAt: time.Now().Add(-31*24*time.Hour + 10*time.Minute), Status: "expired"}
	windowOld := &store.AccessEnrollment{ID: uuid.NewString(), SourceHash: "window-old", CreatedAt: time.Now().Add(-11 * time.Minute), ExpiresAt: time.Now().Add(-time.Minute), Status: "expired"}
	for _, row := range []*store.AccessEnrollment{old, windowOld} {
		if err := auth.putEnrollment(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		row := &store.AccessEnrollment{ID: uuid.NewString(), SourceHash: fmt.Sprintf("source-%02d", i/10), ExpiresAt: time.Now().Add(10 * time.Minute)}
		if err := auth.CreateAccessEnrollment(ctx, row); err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
	}
	if err := auth.CreateAccessEnrollment(ctx, &store.AccessEnrollment{ID: uuid.NewString(), SourceHash: "fresh-source"}); !errors.Is(err, store.ErrRateLimited) {
		t.Fatalf("global cap not enforced: %v", err)
	}
	if _, err := auth.GetAccessEnrollment(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("30-day retired enrollment remains: %v", err)
	}
	if _, err := auth.GetAccessEnrollment(ctx, windowOld.ID); err != nil {
		t.Fatalf("11-minute enrollment retired too early: %v", err)
	}
}
