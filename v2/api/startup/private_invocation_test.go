package startup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"norn/v2/api/etcdstore"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

var errRequiredPrivateInvocationKeys = errors.New("required keys read failed")

type privateInvocationPreflightStore struct {
	ids []string
	err error
}

var _ store.PrivateInvocationStore = (*privateInvocationPreflightStore)(nil)
var _ store.PrivateInvocationStore = (*store.PGOperationStore)(nil)
var _ store.PrivateInvocationStore = (*etcdstore.V3OperationStore)(nil)

func (s *privateInvocationPreflightStore) AcceptPrivateInvocation(context.Context, store.OperationAcceptance, store.PrivateInvocationInput, *store.PrivateInvocationKeyRing) (store.AcceptedOperation, error) {
	panic("unexpected private invocation acceptance during preflight")
}

func (s *privateInvocationPreflightStore) OpenPrivateInvocation(context.Context, model.Operation, *store.PrivateInvocationKeyRing) (store.PrivateInvocationInput, error) {
	panic("unexpected private invocation open during preflight")
}

func (s *privateInvocationPreflightStore) RequiredPrivateInvocationKeys(context.Context) ([]string, error) {
	return s.ids, s.err
}

func preflightKeyRing(t *testing.T, ids ...string) *store.PrivateInvocationKeyRing {
	t.Helper()
	keys := make(map[string][]byte, len(ids))
	for _, id := range ids {
		keys[id] = bytes.Repeat([]byte{id[0]}, 32)
	}
	ring, err := store.NewPrivateInvocationKeyRing(ids[0], keys)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestPreflightPrivateInvocationKeys(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		store store.PrivateInvocationStore
		ring  *store.PrivateInvocationKeyRing
		want  error
		match string
	}{
		{
			name:  "missing key",
			store: &privateInvocationPreflightStore{ids: []string{"retired"}},
			ring:  preflightKeyRing(t, "current"),
			match: `private invocation key "retired" is unavailable`,
		},
		{
			name:  "nil store",
			ring:  preflightKeyRing(t, "current"),
			match: "private invocation store is unavailable",
		},
		{
			name:  "nil ring",
			store: &privateInvocationPreflightStore{},
			match: "private invocation key ring is unavailable",
		},
		{
			name:  "read error",
			store: &privateInvocationPreflightStore{err: errRequiredPrivateInvocationKeys},
			ring:  preflightKeyRing(t, "current"),
			want:  errRequiredPrivateInvocationKeys,
		},
		{
			name:  "success",
			store: &privateInvocationPreflightStore{ids: []string{"retired", "current"}},
			ring:  preflightKeyRing(t, "current", "retired"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PreflightPrivateInvocationKeys(ctx, tt.store, tt.ring)
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("error = %v, want wrapped %v", err, tt.want)
				}
				return
			}
			if tt.match != "" {
				if err == nil || !strings.Contains(err.Error(), tt.match) {
					t.Fatalf("error = %v, want containing %q", err, tt.match)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
