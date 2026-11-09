package startup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/store"
)

type privateWorkloadReleaseKeyStore struct {
	ids []string
	err error
}

func (s privateWorkloadReleaseKeyStore) RequiredWorkloadReleaseInputKeys(context.Context) ([]string, error) {
	return s.ids, s.err
}

func privateWorkloadReleaseKeyRing(t *testing.T, ids ...string) *store.WorkloadReleaseInputKeyRing {
	t.Helper()
	material := make(map[string][]byte, len(ids))
	for i, id := range ids {
		material[id] = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	ring, err := store.NewWorkloadReleaseInputKeyRing(ids[0], material)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestPreflightPrivateWorkloadReleaseKeys(t *testing.T) {
	ctx := context.Background()
	readErr := errors.New("inventory unavailable")
	for _, test := range []struct {
		name  string
		store store.PrivateWorkloadReleaseKeyStore
		ring  *store.WorkloadReleaseInputKeyRing
		want  string
		is    error
	}{
		{name: "nil store", ring: privateWorkloadReleaseKeyRing(t, "current"), want: "key store is unavailable"},
		{name: "nil ring", store: privateWorkloadReleaseKeyStore{}, want: "key ring is unavailable"},
		{name: "missing historical key", store: privateWorkloadReleaseKeyStore{ids: []string{"retired"}}, ring: privateWorkloadReleaseKeyRing(t, "current"), want: `workload release input key "retired" is unavailable`},
		{name: "read error", store: privateWorkloadReleaseKeyStore{err: readErr}, ring: privateWorkloadReleaseKeyRing(t, "current"), is: readErr},
		{name: "retained keys available", store: privateWorkloadReleaseKeyStore{ids: []string{"retired", "current"}}, ring: privateWorkloadReleaseKeyRing(t, "current", "retired")},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := PreflightPrivateWorkloadReleaseKeys(ctx, test.store, test.ring)
			if test.is != nil {
				if !errors.Is(err, test.is) {
					t.Fatalf("error = %v, want wrapped %v", err, test.is)
				}
				return
			}
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v, want containing %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
