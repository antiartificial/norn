package etcdstore

import (
	"context"
	"errors"
	"testing"

	"norn/v2/api/fleet/offline"
)

type offlineOriginFixture struct {
	want string
	err  error
	seen int
}

func (f *offlineOriginFixture) VerifyOfflineEffectSource(_ context.Context, sha string) error {
	f.seen++
	if sha != f.want {
		return offline.ErrEffectProofMismatch
	}
	return f.err
}

func TestOriginCheckedReobserverRequiresIndependentExactSource(t *testing.T) {
	b := offline.Binding{SourceCommit: "abcdef"}
	claim := offline.EffectObservation{TrustedOrigin: true}
	fixture := &offlineOriginFixture{want: b.SourceCommit}
	observer := originCheckedEffectReobserver{remaining: offlineTestReobserver{observation: claim}, origin: fixture}
	got, err := observer.reobserveEffect(context.Background(), b, offline.Attempt{})
	if err != nil || !got.TrustedOrigin || fixture.seen != 1 {
		t.Fatalf("exact source rejected: %+v %v", got, err)
	}
	fixture.err = errors.New("GitHub unavailable")
	if got, err := observer.reobserveEffect(context.Background(), b, offline.Attempt{}); !errors.Is(err, offline.ErrEffectProofMissing) || got.TrustedOrigin {
		t.Fatalf("unavailable source accepted: %+v %v", got, err)
	}
	fixture.err = nil
	fixture.want = "other"
	if got, err := observer.reobserveEffect(context.Background(), b, offline.Attempt{}); !errors.Is(err, offline.ErrEffectProofMissing) || got.TrustedOrigin {
		t.Fatalf("different source accepted: %+v %v", got, err)
	}
	observer.origin = nil
	if _, err := observer.reobserveEffect(context.Background(), b, offline.Attempt{}); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("missing independent source verifier accepted: %v", err)
	}
}
