package offline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

type fakeLatestBackend struct {
	latest            []string
	size              int64
	raw               []byte
	err               error
	versioning        bool
	disableAfterFirst bool
	versioningCalls   int
	calls             int
}

func (f *fakeLatestBackend) VersioningEnabled(_ context.Context, bucket, key string) (bool, error) {
	if bucket != "state" || key != "staging.tfstate" {
		return false, ErrLatestBackendMismatch
	}
	if f.err != nil {
		return false, f.err
	}
	f.versioningCalls++
	if f.disableAfterFirst && f.versioningCalls > 1 {
		return false, nil
	}
	return f.versioning, nil
}

func (f *fakeLatestBackend) Latest(_ context.Context, bucket, key string) (string, int64, error) {
	if bucket != "state" || key != "staging.tfstate" {
		return "", 0, ErrLatestBackendMismatch
	}
	if f.err != nil {
		return "", 0, f.err
	}
	i := f.calls
	f.calls++
	if i >= len(f.latest) {
		return "", 0, ErrLatestBackendMismatch
	}
	return f.latest[i], f.size, nil
}

func (f *fakeLatestBackend) Version(_ context.Context, bucket, key, version string, size int64) ([]byte, error) {
	if bucket != "state" || key != "staging.tfstate" || version != f.latest[0] || size != f.size {
		return nil, ErrLatestBackendMismatch
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.raw, nil
}

func TestVerifyLatestBackendRequiresExactCurrentState(t *testing.T) {
	raw := []byte(`{"lineage":"lineage-one","serial":22,"resources":[]}`)
	sum := sha256.Sum256(raw)
	bound := Backend{Bucket: "state", Key: "staging.tfstate", VersionID: "version-22", Lineage: "lineage-one", Serial: 22, RawSHA256: hex.EncodeToString(sum[:])}
	reader := func() *fakeLatestBackend {
		return &fakeLatestBackend{latest: []string{"version-22", "version-22"}, size: int64(len(raw)), raw: raw, versioning: true}
	}
	if err := VerifyLatestBackend(context.Background(), reader(), bound); err != nil {
		t.Fatalf("exact latest: %v", err)
	}
	for name, change := range map[string]func(*fakeLatestBackend, *Backend){
		"new latest":                       func(r *fakeLatestBackend, _ *Backend) { r.latest[1] = "version-23" },
		"stale initial":                    func(r *fakeLatestBackend, _ *Backend) { r.latest[0] = "version-23" },
		"missing latest":                   func(r *fakeLatestBackend, _ *Backend) { r.err = errors.New("missing") },
		"oversize":                         func(r *fakeLatestBackend, _ *Backend) { r.size = maxEffectStateBytes + 1 },
		"short read":                       func(r *fakeLatestBackend, _ *Backend) { r.size++ },
		"changed bytes":                    func(r *fakeLatestBackend, _ *Backend) { r.raw = []byte(`{"lineage":"lineage-one","serial":23}`) },
		"wrong lineage":                    func(_ *fakeLatestBackend, b *Backend) { b.Lineage = "different" },
		"wrong serial":                     func(_ *fakeLatestBackend, b *Backend) { b.Serial = 23 },
		"suspended versioning":             func(r *fakeLatestBackend, _ *Backend) { r.versioning = false },
		"versioning suspended during read": func(r *fakeLatestBackend, _ *Backend) { r.disableAfterFirst = true },
		"null version":                     func(r *fakeLatestBackend, _ *Backend) { r.latest[0] = "null" },
		"bound null version":               func(_ *fakeLatestBackend, b *Backend) { b.VersionID = "null" },
	} {
		t.Run(name, func(t *testing.T) {
			r, b := reader(), bound
			change(r, &b)
			if err := VerifyLatestBackend(context.Background(), r, b); !errors.Is(err, ErrLatestBackendMismatch) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestVerifyLatestBackendRejectsDuplicateStateIdentity(t *testing.T) {
	raw := []byte(`{"lineage":"lineage-one","serial":22,"serial":22}`)
	sum := sha256.Sum256(raw)
	bound := Backend{Bucket: "state", Key: "staging.tfstate", VersionID: "v", Lineage: "lineage-one", Serial: 22, RawSHA256: hex.EncodeToString(sum[:])}
	r := &fakeLatestBackend{latest: []string{"v", "v"}, size: int64(len(raw)), raw: raw, versioning: true}
	if err := VerifyLatestBackend(context.Background(), r, bound); !errors.Is(err, ErrLatestBackendMismatch) {
		t.Fatalf("duplicate identity: %v", err)
	}
}
