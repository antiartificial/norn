package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type terminalProviderStub struct {
	o   offlineProviderAbsenceObservation
	err error
}

func (s terminalProviderStub) ObserveExactWorkNodeAbsent(context.Context, offline.Binding) (offlineProviderAbsenceObservation, error) {
	return s.o, s.err
}

type terminalBackendStub struct {
	o   offline.TerminalBackendObservation
	err error
}

func (s terminalBackendStub) ObserveExactTerminalBackend(context.Context, offline.Binding) (offline.TerminalBackendObservation, error) {
	return s.o, s.err
}

type terminalWorkloadStub struct {
	o   offline.TerminalWorkloadObservation
	err error
}

func (s terminalWorkloadStub) ObserveExactTerminalWorkload(context.Context, time.Time) (offline.TerminalWorkloadObservation, error) {
	return s.o, s.err
}

type terminalJournalStub struct {
	raw []byte
	err error
}

func (s *terminalJournalStub) AppendOfflineTerminalRaw(_ context.Context, raw []byte) error {
	s.raw = append([]byte(nil), raw...)
	return s.err
}

func terminalComposerFixture(t *testing.T) (offline.Binding, offline.Attempt, offline.EffectGrant, time.Time, offlineTerminalObservationComposer, *terminalJournalStub) {
	t.Helper()
	_, binding, attempt, grant, _ := consumedOfflineGrant(t)
	now := grant.ConsumedAt.Add(10 * time.Second).UTC()
	post := grant.Backend
	post.Serial++
	post.VersionID = "terminal-version"
	post.RawSHA256 = strings.Repeat("9", 64)
	journal := &terminalJournalStub{}
	providerRaw, backendRaw, workloadRaw := []byte(`{"provider":true}`), []byte(`{"backend":true}`), []byte(`{"workload":true}`)
	composer := offlineTerminalObservationComposer{
		provider: terminalProviderStub{o: offlineProviderAbsenceObservation{ObservedAt: now.Add(-3 * time.Second), EvidenceSHA256: digestBytes(providerRaw), RawEvidence: providerRaw}},
		backend:  terminalBackendStub{o: offline.TerminalBackendObservation{Backend: post, ObservedAt: now.Add(-2 * time.Second), EvidenceSHA256: digestBytes(backendRaw), RawEvidence: backendRaw}},
		workload: terminalWorkloadStub{o: offline.TerminalWorkloadObservation{WorkloadEvidence: offline.WorkloadEvidence{ObservedAt: now.Add(-time.Second), AllNamespacesInspected: true, App03Empty: true, SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}, EvidenceSHA256: digestBytes(workloadRaw), RawEvidence: workloadRaw}},
		journal:  journal, clock: func() time.Time { return now },
	}
	return binding, attempt, grant, now, composer, journal
}

func TestOfflineTerminalComposerBindsSourcesAndJournalsBeforeReturn(t *testing.T) {
	binding, attempt, grant, now, composer, journal := terminalComposerFixture(t)
	got, err := composer.ObserveOfflineTerminal(context.Background(), binding, attempt, grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := offline.ValidateTerminalObservation(got, grant, now); err != nil || len(journal.raw) == 0 ||
		got.ProviderEvidenceSHA256 != digestBytes([]byte(`{"provider":true}`)) || got.BackendEvidenceSHA256 != digestBytes([]byte(`{"backend":true}`)) ||
		got.WorkloadEvidenceSHA256 != digestBytes([]byte(`{"workload":true}`)) {
		t.Fatalf("bad composed observation: %+v journal=%d err=%v", got, len(journal.raw), err)
	}
}

func TestOfflineTerminalComposerRejectsFailureMismatchPartialAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*offline.Binding, *offline.Attempt, *offline.EffectGrant, *offlineTerminalObservationComposer, *terminalJournalStub)
	}{
		{"attempt mismatch", func(_ *offline.Binding, a *offline.Attempt, _ *offline.EffectGrant, _ *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			a.ID = "wrong"
		}},
		{"provider failure", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, c *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			s := c.provider.(terminalProviderStub)
			s.err = errors.New("read")
			c.provider = s
		}},
		{"partial backend", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, c *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			s := c.backend.(terminalBackendStub)
			s.o.RawEvidence = nil
			c.backend = s
		}},
		{"forged provider digest", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, c *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			s := c.provider.(terminalProviderStub)
			s.o.EvidenceSHA256 = strings.Repeat("f", 64)
			c.provider = s
		}},
		{"workload failure flag", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, c *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			s := c.workload.(terminalWorkloadStub)
			s.o.WorkloadEvidence.ServicePassing = false
			c.workload = s
		}},
		{"out of order", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, c *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			p := c.provider.(terminalProviderStub)
			b := c.backend.(terminalBackendStub)
			p.o.ObservedAt = b.o.ObservedAt.Add(time.Second)
			c.provider = p
		}},
		{"stale span", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, c *offlineTerminalObservationComposer, _ *terminalJournalStub) {
			p := c.provider.(terminalProviderStub)
			p.o.ObservedAt = c.clock().Add(-31 * time.Second)
			c.provider = p
		}},
		{"journal failure", func(_ *offline.Binding, _ *offline.Attempt, _ *offline.EffectGrant, _ *offlineTerminalObservationComposer, j *terminalJournalStub) {
			j.err = errors.New("disk")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, attempt, grant, _, composer, journal := terminalComposerFixture(t)
			tc.mutate(&binding, &attempt, &grant, &composer, journal)
			if _, err := composer.ObserveOfflineTerminal(context.Background(), binding, attempt, grant); !errors.Is(err, offline.ErrTerminalProof) {
				t.Fatalf("unsafe composition accepted: %v", err)
			}
		})
	}
}

func TestOfflineTerminalFileJournalIsOwnerOnlyAndIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	journal := offlineTerminalFileJournal{root: root}
	planID, attemptID, grantID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	provider, backend, workload := json.RawMessage(`{"provider":true}`), json.RawMessage(`{"backend":true}`), json.RawMessage(`{"workload":true}`)
	record, err := json.Marshal(offlineTerminalRawRecord{Schema: "norn.fleet-offline-terminal-raw/v1", PlanID: planID, AttemptID: attemptID,
		GrantID: grantID, BindingSHA256: strings.Repeat("a", 64), ObservedAt: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		ProviderSHA256: digestBytes(provider), BackendSHA256: digestBytes(backend), WorkloadSHA256: digestBytes(workload),
		Provider: provider, Backend: backend, Workload: workload})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendOfflineTerminalRaw(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendOfflineTerminalRaw(context.Background(), record); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	path := filepath.Join(root, digestBytes([]byte(planID+"\x00"+attemptID+"\x00"+grantID))+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal mode: %v %v", info, err)
	}
	changedRecord := offlineTerminalRawRecord{Schema: "norn.fleet-offline-terminal-raw/v1", PlanID: planID, AttemptID: attemptID,
		GrantID: grantID, BindingSHA256: strings.Repeat("a", 64), ObservedAt: time.Date(2026, 10, 19, 12, 0, 1, 0, time.UTC),
		ProviderSHA256: digestBytes(provider), BackendSHA256: digestBytes(backend), WorkloadSHA256: digestBytes(workload),
		Provider: provider, Backend: backend, Workload: workload}
	changed, _ := json.Marshal(changedRecord)
	if err := journal.AppendOfflineTerminalRaw(context.Background(), changed); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("changed retry accepted: %v", err)
	}
	traversal := []byte(strings.Replace(string(record), planID, "../escape", 1))
	if err := journal.AppendOfflineTerminalRaw(context.Background(), traversal); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("traversal identity accepted: %v", err)
	}
	link := filepath.Join(t.TempDir(), "journal-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := (offlineTerminalFileJournal{root: link}).AppendOfflineTerminalRaw(context.Background(), record); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("symlink journal root accepted: %v", err)
	}
}

func TestOfflineTerminalFileJournalRetrySyncsExistingLink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	planID, attemptID, grantID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	provider, backend, workload := json.RawMessage(`{"provider":true}`), json.RawMessage(`{"backend":true}`), json.RawMessage(`{"workload":true}`)
	record, _ := json.Marshal(offlineTerminalRawRecord{Schema: "norn.fleet-offline-terminal-raw/v1", PlanID: planID, AttemptID: attemptID,
		GrantID: grantID, BindingSHA256: strings.Repeat("a", 64), ObservedAt: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		ProviderSHA256: digestBytes(provider), BackendSHA256: digestBytes(backend), WorkloadSHA256: digestBytes(workload), Provider: provider, Backend: backend, Workload: workload})
	calls := 0
	journal := offlineTerminalFileJournal{root: root, syncDir: func(int) error {
		calls++
		if calls == 1 {
			return errors.New("interrupted directory sync")
		}
		return nil
	}}
	if err := journal.AppendOfflineTerminalRaw(context.Background(), record); !errors.Is(err, offline.ErrTerminalProof) {
		t.Fatalf("first sync failure accepted: %v", err)
	}
	if err := journal.AppendOfflineTerminalRaw(context.Background(), record); err != nil || calls != 2 {
		t.Fatalf("retry did not resync existing link: calls=%d err=%v", calls, err)
	}
}
