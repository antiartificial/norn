package observerclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"norn/v2/api/fleet/offline"
)

type effectBackendFixture struct {
	versioning     bool
	latest         []string
	raw            []byte
	lock           []error
	latestN        int
	lockN          int
	versionN       int
	missingVersion bool
}

func (f *effectBackendFixture) VersioningEnabled(context.Context, string, string) (bool, error) {
	return f.versioning, nil
}

func (f *effectBackendFixture) Latest(_ context.Context, _, _ string) (string, int64, error) {
	if f.latestN >= len(f.latest) {
		return "", 0, errors.New("missing latest")
	}
	v := f.latest[f.latestN]
	f.latestN++
	return v, int64(len(f.raw)), nil
}

func (f *effectBackendFixture) Version(_ context.Context, _, _, version string, size int64) ([]byte, error) {
	f.versionN++
	if f.missingVersion || version != "version-22" || size != int64(len(f.raw)) {
		return nil, errors.New("missing version")
	}
	return append([]byte(nil), f.raw...), nil
}

func (f *effectBackendFixture) ForeignLockAbsent(context.Context, string, string) error {
	if f.lockN >= len(f.lock) {
		return errors.New("ambiguous lock result")
	}
	err := f.lock[f.lockN]
	f.lockN++
	return err
}

func backendReadFixture() (EffectBackendReader, offline.EffectChallengeV1, offline.Binding, *effectBackendFixture) {
	raw := []byte(`{"lineage":"lineage-one","serial":22,"resources":[]}`)
	sum := sha256.Sum256(raw)
	backend := offline.Backend{Bucket: "work-state", Key: "fleet.tfstate", VersionID: "version-22", Lineage: "lineage-one", Serial: 22, RawSHA256: hex.EncodeToString(sum[:])}
	binding := offline.Binding{PlanID: "plan", TargetID: "target", ProviderAccountUUID: "work-account", SourceCommit: "commit",
		SavedPlanSHA256: "plan-hash", EffectSHA256: "effect-hash", FenceGeneration: 7, NodeName: "app-03", DropletID: "610327215", Backend: backend}
	challenge := offline.EffectChallengeV1{ID: "challenge", PlanID: binding.PlanID, TargetID: binding.TargetID,
		ProviderAccountUUID: binding.ProviderAccountUUID, SourceCommit: binding.SourceCommit,
		SavedPlanSHA256: binding.SavedPlanSHA256, EffectSHA256: binding.EffectSHA256,
		FenceGeneration: binding.FenceGeneration, NodeName: binding.NodeName, DropletID: binding.DropletID, CurrentBackend: backend}
	fixture := &effectBackendFixture{versioning: true, latest: []string{"version-22", "version-22"}, raw: raw, lock: []error{nil, nil}}
	return EffectBackendReader{Reader: fixture, ExpectedAccount: "work-account", Bucket: "work-state", Key: "fleet.tfstate"}, challenge, binding, fixture
}

func TestEffectBackendReaderCapturesRawCurrentVersion(t *testing.T) {
	r, c, b, fixture := backendReadFixture()
	raw, err := r.Read(context.Background(), c, b)
	if err != nil {
		t.Fatal(err)
	}
	var transcript struct {
		Schema              string          `json:"schema"`
		ChallengeID         string          `json:"challengeId"`
		ProviderAccountUUID string          `json:"providerAccountUUID"`
		Backend             offline.Backend `json:"backend"`
		RawState            []byte          `json:"rawState"`
	}
	if err := json.Unmarshal(raw, &transcript); err != nil || transcript.Schema != "norn.fleet-effect-backend-transcript/v1" ||
		transcript.ChallengeID != c.ID || transcript.ProviderAccountUUID != "work-account" ||
		transcript.Backend != b.Backend || string(transcript.RawState) != string(fixture.raw) {
		t.Fatalf("unexpected transcript: %s (%v)", raw, err)
	}
	if fixture.latestN != 2 || fixture.lockN != 2 || fixture.versionN != 1 {
		t.Fatalf("missing independent reads: latest=%d lock=%d version=%d", fixture.latestN, fixture.lockN, fixture.versionN)
	}
}

func TestEffectBackendReaderFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*EffectBackendReader, *offline.EffectChallengeV1, *offline.Binding, *effectBackendFixture){
		"wrong account": func(r *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *effectBackendFixture) {
			r.ExpectedAccount = "personal"
		},
		"wrong bucket": func(r *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *effectBackendFixture) {
			r.Bucket = "other"
		},
		"changed challenge": func(_ *EffectBackendReader, c *offline.EffectChallengeV1, _ *offline.Binding, _ *effectBackendFixture) {
			c.CurrentBackend.Serial = 23
		},
		"stale latest": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.latest[0] = "version-21"
		},
		"concurrent state": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.latest[1] = "version-23"
		},
		"missing version": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.missingVersion = true
		},
		"changed bytes": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.raw = []byte(`{"lineage":"lineage-one","serial":23}`)
		},
		"suspended versioning": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.versioning = false
		},
		"pre lock": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.lock[0] = errors.New("locked")
		},
		"post lock": func(_ *EffectBackendReader, _ *offline.EffectChallengeV1, _ *offline.Binding, f *effectBackendFixture) {
			f.lock[1] = errors.New("locked")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, c, b, f := backendReadFixture()
			mutate(&r, &c, &b, f)
			if raw, err := r.Read(context.Background(), c, b); !errors.Is(err, ErrEffectBackendRead) || raw != nil {
				t.Fatalf("read=%d bytes err=%v", len(raw), err)
			}
		})
	}
}
