package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"norn/v2/api/fleet/offline"
)

const maxOfflineTerminalJournalBytes = 32 << 20

type offlineTerminalProviderSource interface {
	ObserveExactWorkNodeAbsent(context.Context, offline.Binding) (offlineProviderAbsenceObservation, error)
}

type offlineTerminalBackendSource interface {
	ObserveExactTerminalBackend(context.Context, offline.Binding) (offline.TerminalBackendObservation, error)
}

type offlineTerminalWorkloadSource interface {
	ObserveExactTerminalWorkload(context.Context, time.Time) (offline.TerminalWorkloadObservation, error)
}

type offlineTerminalRawJournal interface {
	AppendOfflineTerminalRaw(context.Context, []byte) error
}

type offlineTerminalRawRecord struct {
	Schema                                        string `json:"schema"`
	PlanID, AttemptID, GrantID, BindingSHA256     string
	ObservedAt                                    time.Time `json:"observedAt"`
	ProviderSHA256, BackendSHA256, WorkloadSHA256 string
	Provider, Backend, Workload                   json.RawMessage
}

type offlineTerminalFileJournal struct {
	root    string
	syncDir func(int) error
}

func (j offlineTerminalFileJournal) AppendOfflineTerminalRaw(_ context.Context, raw []byte) error {
	var record offlineTerminalRawRecord
	if len(raw) == 0 || len(raw) > maxOfflineTerminalJournalBytes || json.Unmarshal(raw, &record) != nil || record.Schema != "norn.fleet-offline-terminal-raw/v1" ||
		!offlineEvidenceDigest.MatchString(record.BindingSHA256) || uuid.Validate(record.PlanID) != nil || uuid.Validate(record.AttemptID) != nil || uuid.Validate(record.GrantID) != nil ||
		record.ObservedAt.IsZero() || len(record.Provider) == 0 || len(record.Backend) == 0 || len(record.Workload) == 0 ||
		digestBytes(record.Provider) != record.ProviderSHA256 || digestBytes(record.Backend) != record.BackendSHA256 || digestBytes(record.Workload) != record.WorkloadSHA256 {
		return offline.ErrTerminalProof
	}
	root, err := filepath.Abs(j.root)
	if err != nil || root != j.root {
		return offline.ErrTerminalProof
	}
	dirFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return offline.ErrTerminalProof
	}
	dir := os.NewFile(uintptr(dirFD), root)
	defer dir.Close()
	var stat unix.Stat_t
	if unix.Fstat(dirFD, &stat) != nil {
		return offline.ErrTerminalProof
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return offline.ErrTerminalProof
	}
	identity := sha256.Sum256([]byte(record.PlanID + "\x00" + record.AttemptID + "\x00" + record.GrantID))
	name := hex.EncodeToString(identity[:]) + ".json"
	tmpName := ".terminal-raw-" + uuid.NewString()
	tmpFD, err := unix.Openat(dirFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return offline.ErrTerminalProof
	}
	tmp := os.NewFile(uintptr(tmpFD), tmpName)
	defer tmp.Close()
	defer unix.Unlinkat(dirFD, tmpName, 0)
	if writeFull(tmp, raw) != nil || tmp.Sync() != nil || tmp.Close() != nil {
		return offline.ErrTerminalProof
	}
	if err := unix.Linkat(dirFD, tmpName, dirFD, name, 0); err != nil {
		if err != unix.EEXIST {
			return offline.ErrTerminalProof
		}
		existingFD, openErr := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return offline.ErrTerminalProof
		}
		existing := os.NewFile(uintptr(existingFD), name)
		var existingStat unix.Stat_t
		statErr := unix.Fstat(existingFD, &existingStat)
		existingRaw, readErr := io.ReadAll(io.LimitReader(existing, maxOfflineTerminalJournalBytes+1))
		closeErr := existing.Close()
		if statErr != nil || readErr != nil || closeErr != nil || existingStat.Mode&unix.S_IFMT != unix.S_IFREG || existingStat.Mode&0077 != 0 ||
			existingStat.Uid != uint32(os.Geteuid()) || len(existingRaw) > maxOfflineTerminalJournalBytes || string(existingRaw) != string(raw) {
			return offline.ErrTerminalProof
		}
	}
	syncDir := j.syncDir
	if syncDir == nil {
		syncDir = unix.Fsync
	}
	if syncDir(dirFD) != nil {
		return offline.ErrTerminalProof
	}
	return nil
}

func writeFull(w io.Writer, raw []byte) error {
	for len(raw) > 0 {
		n, err := w.Write(raw)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}

type offlineBoundTerminalBackend struct {
	reader    offline.TerminalBackendReader
	projectID string
}

func (s offlineBoundTerminalBackend) ObserveExactTerminalBackend(ctx context.Context, binding offline.Binding) (offline.TerminalBackendObservation, error) {
	return offline.ObserveTerminalBackend(ctx, s.reader, binding, s.projectID)
}

type offlineBoundTerminalWorkload struct{ source offline.NomadConsulSource }

func (s offlineBoundTerminalWorkload) ObserveExactTerminalWorkload(ctx context.Context, now time.Time) (offline.TerminalWorkloadObservation, error) {
	return offline.ObserveTerminalNomadConsul(ctx, s.source, now)
}

// offlineTerminalObservationComposer is intentionally private and unmounted.
// Only authority-owned readers and an owner-only raw journal can construct it.
type offlineTerminalObservationComposer struct {
	provider offlineTerminalProviderSource
	backend  offlineTerminalBackendSource
	workload offlineTerminalWorkloadSource
	journal  offlineTerminalRawJournal
	clock    func() time.Time
}

func (c offlineTerminalObservationComposer) ObserveOfflineTerminal(ctx context.Context, binding offline.Binding, attempt offline.Attempt, grant offline.EffectGrant) (offline.TerminalObservation, error) {
	var zero offline.TerminalObservation
	bindingRaw, bindingErr := json.Marshal(binding)
	if c.provider == nil || c.backend == nil || c.workload == nil || c.journal == nil || c.clock == nil ||
		binding.Validate() != nil || bindingErr != nil || digestBytes(bindingRaw) != grant.BindingSHA256 ||
		grant.SchemaVersion != offline.EffectGrantSchema || grant.Phase != "consumed" || grant.ConsumedAt == nil || grant.AuthorityEpoch < 1 ||
		attempt.SchemaVersion != offline.AttemptSchema || uuid.Validate(attempt.ID) != nil || uuid.Validate(grant.ID) != nil || attempt.ID != grant.AttemptID || attempt.PlanID != binding.PlanID ||
		attempt.BindingSHA256 != grant.BindingSHA256 || attempt.TargetID != binding.TargetID || attempt.FenceGeneration != binding.FenceGeneration ||
		attempt.PrincipalSubject != binding.RunnerPrincipalSubject || attempt.PrincipalTokenID == "" || attempt.PrincipalDeviceID == "" ||
		attempt.Status != "queued" || attempt.CurrentPhase != "prechange_verified" || attempt.CreatedAt.IsZero() ||
		grant.PlanID != binding.PlanID || grant.TargetID != binding.TargetID || grant.FenceGeneration != binding.FenceGeneration ||
		grant.SourceCommit != binding.SourceCommit || grant.SavedPlanSHA256 != binding.SavedPlanSHA256 || grant.EffectSHA256 != binding.EffectSHA256 ||
		grant.ProviderAccountUUID != binding.ProviderAccountUUID || grant.NodeName != binding.NodeName || grant.DropletID != binding.DropletID ||
		grant.Backend != binding.Backend || !offlineEvidenceDigest.MatchString(grant.PrechangeSHA256) || !offlineEvidenceDigest.MatchString(grant.IndependentlyObservedSHA256) ||
		grant.IssuedAt.IsZero() || grant.ExpiresAt.IsZero() || grant.ConsumedAt.Before(grant.IssuedAt) || !grant.ConsumedAt.Before(grant.ExpiresAt) {
		return zero, offline.ErrTerminalProof
	}
	provider, err := c.provider.ObserveExactWorkNodeAbsent(ctx, binding)
	if err != nil {
		return zero, offline.ErrTerminalProof
	}
	backend, err := c.backend.ObserveExactTerminalBackend(ctx, binding)
	if err != nil {
		return zero, offline.ErrTerminalProof
	}
	workload, err := c.workload.ObserveExactTerminalWorkload(ctx, c.clock().UTC())
	if err != nil {
		return zero, offline.ErrTerminalProof
	}
	observedAt := c.clock().UTC()
	workloadAt := workload.WorkloadEvidence.ObservedAt.UTC()
	if len(provider.RawEvidence) == 0 || len(backend.RawEvidence) == 0 || len(workload.RawEvidence) == 0 ||
		!offlineEvidenceDigest.MatchString(provider.EvidenceSHA256) || !offlineEvidenceDigest.MatchString(backend.EvidenceSHA256) || !offlineEvidenceDigest.MatchString(workload.EvidenceSHA256) ||
		digestBytes(provider.RawEvidence) != provider.EvidenceSHA256 || digestBytes(backend.RawEvidence) != backend.EvidenceSHA256 || digestBytes(workload.RawEvidence) != workload.EvidenceSHA256 ||
		provider.ObservedAt.IsZero() || backend.ObservedAt.IsZero() || workloadAt.IsZero() ||
		!provider.ObservedAt.After(*grant.ConsumedAt) || backend.ObservedAt.Before(provider.ObservedAt) || workloadAt.Before(backend.ObservedAt) ||
		observedAt.Before(workloadAt) || observedAt.Sub(provider.ObservedAt) > 30*time.Second ||
		!workload.WorkloadEvidence.AllNamespacesInspected || !workload.WorkloadEvidence.App03Empty || !workload.WorkloadEvidence.SurvivingPlacement ||
		!workload.WorkloadEvidence.SurvivingHeadroom || !workload.WorkloadEvidence.ServicePassing {
		return zero, offline.ErrTerminalProof
	}
	raw, err := json.Marshal(offlineTerminalRawRecord{Schema: "norn.fleet-offline-terminal-raw/v1", PlanID: binding.PlanID,
		AttemptID: attempt.ID, GrantID: grant.ID, BindingSHA256: grant.BindingSHA256, ObservedAt: observedAt,
		ProviderSHA256: provider.EvidenceSHA256, BackendSHA256: backend.EvidenceSHA256, WorkloadSHA256: workload.EvidenceSHA256,
		Provider: provider.RawEvidence, Backend: backend.RawEvidence, Workload: workload.RawEvidence})
	if err != nil || len(raw) == 0 || len(raw) > maxOfflineTerminalJournalBytes || !json.Valid(raw) {
		return zero, offline.ErrTerminalProof
	}
	digest := sha256.Sum256(raw)
	if err := c.journal.AppendOfflineTerminalRaw(ctx, raw); err != nil {
		return zero, offline.ErrTerminalProof
	}
	o := offline.TerminalObservation{PlanID: binding.PlanID, AttemptID: attempt.ID, GrantID: grant.ID,
		BindingSHA256: grant.BindingSHA256, TargetID: binding.TargetID, FenceGeneration: binding.FenceGeneration,
		AuthorityEpoch: grant.AuthorityEpoch, ObservedAt: observedAt, EvidenceSHA256: hex.EncodeToString(digest[:]),
		ProviderEvidenceSHA256: provider.EvidenceSHA256, BackendEvidenceSHA256: backend.EvidenceSHA256,
		WorkloadEvidenceSHA256: workload.EvidenceSHA256, ProviderAccountUUID: binding.ProviderAccountUUID,
		NodeName: binding.NodeName, DropletID: binding.DropletID, Backend: backend.Backend,
		ProviderNodeAbsent: true, BackendLatestExact: true, BackendNodeAbsent: true,
		AllNamespacesInspected: workload.WorkloadEvidence.AllNamespacesInspected,
		WorkloadNodeAbsent:     workload.WorkloadEvidence.App03Empty, SurvivingPlacement: workload.WorkloadEvidence.SurvivingPlacement,
		SurvivingHeadroom: workload.WorkloadEvidence.SurvivingHeadroom, ServicePassing: workload.WorkloadEvidence.ServicePassing}
	if offline.ValidateTerminalObservation(o, grant, observedAt) != nil {
		return zero, offline.ErrTerminalProof
	}
	return o, nil
}

func digestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
