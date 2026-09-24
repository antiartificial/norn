package supervisor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
)

const evidenceSource = "norn-effect-supervisor/v1"

type BackendExecution struct {
	SupervisorExecutionID string
	RuntimeInstanceID     string
	StateDirectory        string
}

type BackendState struct {
	Phase             effect.SupervisorPhase
	ExitCode          *int
	Output            []byte
	ContainmentProven bool
	EvidenceReference string
	TimedOut          bool
	OutputDiscarded   int64
}

// Backend must make Start recoverable by the execution and runtime identities
// before it can return. A Linux implementation uses a cgroup identity; a
// backend that cannot prove all descendants exited must leave
// ContainmentProven false.
type Backend interface {
	Start(context.Context, BackendExecution, effect.LaunchMaterial) error
	Observe(context.Context, BackendExecution) (BackendState, error)
	Revoke(context.Context, BackendExecution) (BackendState, error)
	RetrieveResult(context.Context, BackendExecution, string) ([]byte, error)
}

// SnapshotBackend carries private snapshot material only over the runner pipe.
// Its descriptor remains the durable, secret-free reservation payload.
type SnapshotBackend interface {
	StartSnapshot(context.Context, BackendExecution, SnapshotDescriptor, SnapshotLaunchMaterial) error
	ObserveSnapshot(context.Context, BackendExecution, SnapshotDescriptor) (BackendState, error)
	QuerySnapshot(context.Context, BackendExecution, SnapshotDescriptor) (SnapshotManifest, error)
	CopySnapshotArtifact(context.Context, BackendExecution, SnapshotDescriptor, io.Writer) (SnapshotManifest, error)
}

type Manager struct {
	root                   string
	rootID                 string
	key                    []byte
	backend                Backend
	snapshotArtifactBudget int64
}

// RootID identifies the local supervisor namespace. It is safe to use for
// selecting durable effect records, but it is not an execution credential.
func (m *Manager) RootID() string {
	if m == nil {
		return ""
	}
	return m.rootID
}

// SetSnapshotArtifactBudget configures the total admission ceiling for private
// snapshot dumps. A zero value disables snapshot admission.
func (m *Manager) SetSnapshotArtifactBudget(bytes int64) error {
	if m == nil || bytes < MaxSnapshotArtifactBytes {
		return fmt.Errorf("snapshot artifact budget must be at least %d bytes", MaxSnapshotArtifactBytes)
	}
	m.snapshotArtifactBudget = bytes
	return nil
}

// registryEntry is the root-level record of an execution namespace. The
// runtime identity is recorded here before the backend starts, so losing or
// replacing the per-execution journal can never make a launched execution
// look registered-but-never-launched.
type registryEntry struct {
	InputDigest           string `json:"inputDigest"`
	MaterialMAC           string `json:"materialMac"`
	Supervisor            string `json:"supervisor"`
	SupervisorExecutionID string `json:"supervisorExecutionId"`
	RuntimeInstanceID     string `json:"runtimeInstanceId,omitempty"`
}

func (e registryEntry) sameBinding(other registryEntry) bool {
	return e.InputDigest == other.InputDigest && e.MaterialMAC == other.MaterialMAC &&
		e.Supervisor == other.Supervisor && e.SupervisorExecutionID == other.SupervisorExecutionID
}

const launchIntentName = "launch-intent"

type registry struct {
	Protocol string                   `json:"protocol"`
	RootID   string                   `json:"rootId"`
	Entries  map[string]registryEntry `json:"entries"`
}

type signedRegistry struct {
	Registry registry `json:"registry"`
	MAC      string   `json:"mac"`
}

func NewManager(root string, signingKey []byte, backend Backend) (*Manager, error) {
	if strings.TrimSpace(root) == "" || backend == nil {
		return nil, fmt.Errorf("effect supervisor root and backend are required")
	}
	if len(signingKey) < 32 {
		return nil, fmt.Errorf("effect supervisor signing key must be at least 32 bytes")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create effect supervisor root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure effect supervisor root: %w", err)
	}
	manager := &Manager{root: root, key: append([]byte(nil), signingKey...), backend: backend}
	rootID, err := manager.loadOrCreateRegistry()
	if err != nil {
		return nil, err
	}
	manager.rootID = rootID
	return manager, nil
}

func (m *Manager) Prepare(_ context.Context, reservation effect.Reservation) error {
	descriptor, err := m.verifyAnyDescriptor(reservation.LaunchPayload)
	if err != nil {
		return err
	}
	return m.withExecutionLock(reservation.SupervisorExecutionID, func(directory string) error {
		if _, err := os.Stat(filepath.Join(directory, "tombstone")); err == nil {
			return fmt.Errorf("supervisor execution id is durably revoked")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		binding := registryEntry{
			InputDigest: reservation.InputDigest, MaterialMAC: descriptor.MaterialMAC,
			Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID,
		}
		registered, err := m.registryEntry(reservation.SupervisorExecutionID)
		if err != nil {
			return err
		}
		record, journalErr := m.readJournal(directory)
		if registered != nil {
			if !registered.sameBinding(binding) {
				return fmt.Errorf("supervisor execution id is registered for different work")
			}
			if journalErr != nil {
				return fmt.Errorf("registered effect supervisor history is missing or corrupt: %w", journalErr)
			}
			if record.InputDigest != reservation.InputDigest || record.MaterialMAC != descriptor.MaterialMAC ||
				record.Supervisor != reservation.Supervisor || record.SupervisorExecutionID != reservation.SupervisorExecutionID {
				return fmt.Errorf("supervisor execution id is bound to different work")
			}
			return nil
		}
		if journalErr == nil {
			if record.InputDigest != reservation.InputDigest || record.MaterialMAC != descriptor.MaterialMAC ||
				record.Supervisor != reservation.Supervisor || record.SupervisorExecutionID != reservation.SupervisorExecutionID {
				return fmt.Errorf("unregistered supervisor journal is bound to different work")
			}
			return m.registerExecution(binding)
		}
		if !errors.Is(journalErr, os.ErrNotExist) {
			return journalErr
		}
		// Unregistered with no journal is a fresh namespace only if nothing else
		// was ever written for it. Leftover launch intent, runner status or
		// output means history was lost (for example a rolled-back registry),
		// and re-registering would later read as never launched.
		if err := requireFreshExecutionDirectory(directory); err != nil {
			return err
		}
		if err := m.writeJournal(directory, journal{
			Protocol: ProtocolV1, InputDigest: reservation.InputDigest, MaterialMAC: descriptor.MaterialMAC,
			Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID,
			Phase: "registered", CreatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		return m.registerExecution(binding)
	})
}

func (m *Manager) verifyAnyDescriptor(payload json.RawMessage) (Descriptor, error) {
	var header struct{ Protocol, Stage string }
	if err := json.Unmarshal(payload, &header); err != nil {
		return Descriptor{}, err
	}
	if header.Protocol == SnapshotProtocolV1 && header.Stage == SnapshotStage {
		s, err := m.verifySnapshotDescriptor(payload, nil)
		if err != nil {
			return Descriptor{}, err
		}
		return Descriptor{Protocol: s.Protocol, SupervisorRootID: s.SupervisorRootID, Stage: s.Stage, MaterialMAC: s.MaterialMAC}, nil
	}
	return m.verifyDescriptor(payload, nil)
}

type journal struct {
	Protocol              string    `json:"protocol"`
	InputDigest           string    `json:"inputDigest"`
	MaterialMAC           string    `json:"materialMac"`
	Supervisor            string    `json:"supervisor"`
	SupervisorExecutionID string    `json:"supervisorExecutionId"`
	RuntimeInstanceID     string    `json:"runtimeInstanceId"`
	Phase                 string    `json:"phase"`
	CreatedAt             time.Time `json:"createdAt"`
}

type signedJournal struct {
	Record journal `json:"record"`
	MAC    string  `json:"mac"`
}

type evidenceAssertion struct {
	Protocol              string                 `json:"protocol"`
	InputDigest           string                 `json:"inputDigest"`
	SupervisorExecutionID string                 `json:"supervisorExecutionId"`
	RuntimeInstanceID     string                 `json:"runtimeInstanceId"`
	Phase                 effect.SupervisorPhase `json:"phase"`
	ExitCode              *int                   `json:"exitCode,omitempty"`
	ResultDigest          string                 `json:"resultDigest,omitempty"`
	ResultReference       string                 `json:"resultReference,omitempty"`
	ContainmentProven     bool                   `json:"containmentProven"`
	TimedOut              bool                   `json:"timedOut,omitempty"`
	OutputDiscarded       int64                  `json:"outputDiscarded,omitempty"`
	EvidenceReference     string                 `json:"evidenceReference"`
	ObservedAt            time.Time              `json:"observedAt"`
}

type signedEvidence struct {
	Assertion evidenceAssertion `json:"assertion"`
	MAC       string            `json:"mac"`
}

func (m *Manager) Launch(ctx context.Context, reservation effect.Reservation, material effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	_, err := m.verifyDescriptor(reservation.LaunchPayload, &material)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	var identity effect.ExecutionIdentity
	err = m.withExecutionLock(reservation.SupervisorExecutionID, func(directory string) error {
		if _, err := os.Stat(filepath.Join(directory, "tombstone")); err == nil {
			return fmt.Errorf("supervisor execution id is durably revoked")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		record, err := m.readBoundJournal(directory, reservation, effect.ExecutionIdentity{
			Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID,
		})
		if err == nil && record.RuntimeInstanceID != "" {
			identity = journalIdentity(record)
			return nil
		}
		if err != nil {
			return err
		}
		record.RuntimeInstanceID = uuid.NewString()
		record.Phase = "prepared"
		// Durable launch evidence precedes any backend start, in three places:
		// the root registry, a launch-intent marker and the journal.
		if err := m.recordLaunchRuntime(record); err != nil {
			return err
		}
		encoded, _ := json.Marshal(record)
		if err := writeDurableJSON(directory, launchIntentName, map[string]string{"runtimeInstanceId": record.RuntimeInstanceID, "mac": m.mac(encoded)}); err != nil {
			return err
		}
		if err := m.writeJournal(directory, record); err != nil {
			return err
		}
		execution := backendExecution(record, directory)
		if err := m.backend.Start(ctx, execution, material); err != nil {
			return fmt.Errorf("start supervised effect: %w", err)
		}
		record.Phase = "launched"
		if err := m.writeJournal(directory, record); err != nil {
			return fmt.Errorf("record supervised launch: %w", err)
		}
		identity = journalIdentity(record)
		return nil
	})
	return identity, err
}

// LaunchSnapshot mirrors Launch but never converts SnapshotLaunchMaterial into
// generic LaunchMaterial, preventing its service file/password from entering
// the normal supervisor journal or effect reservation.
func (m *Manager) LaunchSnapshot(ctx context.Context, reservation effect.Reservation, material SnapshotLaunchMaterial) (effect.ExecutionIdentity, error) {
	descriptor, err := m.verifySnapshotDescriptor(reservation.LaunchPayload, &material)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	backend, ok := m.backend.(SnapshotBackend)
	if !ok {
		return effect.ExecutionIdentity{}, fmt.Errorf("snapshot supervisor backend is unavailable")
	}
	var identity effect.ExecutionIdentity
	err = m.withExecutionLock(reservation.SupervisorExecutionID, func(directory string) error {
		record, err := m.readBoundJournal(directory, reservation, effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID})
		if err != nil {
			return err
		}
		if record.RuntimeInstanceID != "" {
			identity = journalIdentity(record)
			return nil
		}
		if err := m.admitSnapshotArtifact(reservation.SupervisorExecutionID); err != nil {
			return err
		}
		record.RuntimeInstanceID, record.Phase = uuid.NewString(), "prepared"
		if err := m.recordLaunchRuntime(record); err != nil {
			return err
		}
		encoded, _ := json.Marshal(record)
		if err := writeDurableJSON(directory, launchIntentName, map[string]string{"runtimeInstanceId": record.RuntimeInstanceID, "mac": m.mac(encoded)}); err != nil {
			return err
		}
		if err := m.writeJournal(directory, record); err != nil {
			return err
		}
		if err := backend.StartSnapshot(ctx, backendExecution(record, directory), descriptor, material); err != nil {
			return fmt.Errorf("start supervised snapshot: %w", err)
		}
		record.Phase = "launched"
		if err := m.writeJournal(directory, record); err != nil {
			return err
		}
		identity = journalIdentity(record)
		return nil
	})
	return identity, err
}

// DiscardSnapshotArtifact removes only the already-published private dump.
// Its caller must have a durable terminal operation record; without it the
// dump remains available for effect replay.
func (m *Manager) DiscardSnapshotArtifact(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) error {
	d, err := m.verifySnapshotDescriptor(reservation.LaunchPayload, nil)
	if err != nil {
		return err
	}
	b, ok := m.backend.(SnapshotBackend)
	if !ok {
		return fmt.Errorf("snapshot supervisor backend is unavailable")
	}
	return m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		r, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		private, err := snapshotArtifactDirectory(directory)
		if errors.Is(err, os.ErrNotExist) {
			return removeSnapshotAdmission(directory)
		}
		if err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(private, "archive.dump")); errors.Is(err, os.ErrNotExist) {
			return removeSnapshotAdmission(directory)
		} else if err != nil {
			return err
		}
		// This proves a signed successful terminal status and containment before
		// deleting the one private archive. It is intentionally idempotent.
		if _, err := b.QuerySnapshot(ctx, backendExecution(r, directory), d); err != nil {
			var integrity *SnapshotArtifactIntegrityError
			if errors.As(err, &integrity) {
				if cleanupErr := removeDisposableSnapshotArtifact(private); cleanupErr != nil {
					return cleanupErr
				}
				if admissionErr := removeSnapshotAdmission(directory); admissionErr != nil {
					return admissionErr
				}
				return &PublishedSnapshotCorruptionError{ExecutionID: identity.SupervisorExecutionID, Cause: err}
			}
			return err
		}
		if err := os.Remove(filepath.Join(private, "archive.dump")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncDirectory(private); err != nil {
			return err
		}
		return removeSnapshotAdmission(directory)
	})
}

// PublishedSnapshotCorruptionError reports that a disposable private artifact
// was removed after its public snapshot and successful operation were
// already durable. Callers may report this without preventing startup.
type PublishedSnapshotCorruptionError struct {
	ExecutionID string
	Cause       error
}

func (e *PublishedSnapshotCorruptionError) Error() string {
	return fmt.Sprintf("published snapshot corrupt private artifact %s was removed: %v", e.ExecutionID, e.Cause)
}

func (e *PublishedSnapshotCorruptionError) Unwrap() error { return e.Cause }

func removeDisposableSnapshotArtifact(private string) error {
	for _, name := range []string{"archive.dump", "service.conf", "passfile"} {
		if err := os.Remove(filepath.Join(private, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDirectory(private)
}

func removeSnapshotAdmission(directory string) error {
	if err := os.Remove(filepath.Join(directory, "snapshot-admission")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(directory)
}

func snapshotArtifactDirectory(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var private string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".snapshot-") {
			if private != "" {
				return "", fmt.Errorf("snapshot artifact directory is ambiguous")
			}
			private = filepath.Join(directory, entry.Name())
		}
	}
	if private == "" {
		return "", os.ErrNotExist
	}
	return private, nil
}

func (m *Manager) admitSnapshotArtifact(executionID string) error {
	if m.snapshotArtifactBudget < MaxSnapshotArtifactBytes {
		return fmt.Errorf("snapshot artifact admission budget is not configured")
	}
	return m.withRegistryLock(func(*registry) error {
		digest := sha256.Sum256([]byte(executionID))
		currentDirectory := hex.EncodeToString(digest[:])
		var used int64
		entries, err := os.ReadDir(m.root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			directory := filepath.Join(m.root, entry.Name())
			admission := filepath.Join(directory, "snapshot-admission")
			if info, err := os.Lstat(admission); err == nil {
				if !info.Mode().IsRegular() || info.Size() > 4<<10 || used > m.snapshotArtifactBudget-MaxSnapshotArtifactBytes {
					return fmt.Errorf("snapshot artifact admission accounting is invalid")
				}
				if entry.Name() == currentDirectory {
					continue
				}
				used += MaxSnapshotArtifactBytes
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			private, err := snapshotArtifactDirectory(directory)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			info, err := os.Lstat(filepath.Join(private, "archive.dump"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
				return fmt.Errorf("snapshot artifact accounting is invalid")
			}
			if info.Size() > MaxSnapshotArtifactBytes || used > m.snapshotArtifactBudget-info.Size() {
				return fmt.Errorf("snapshot artifact budget is exhausted")
			}
			used += info.Size()
		}
		if used > m.snapshotArtifactBudget-MaxSnapshotArtifactBytes {
			return fmt.Errorf("snapshot artifact budget cannot admit another bounded dump")
		}
		directory := filepath.Join(m.root, currentDirectory)
		if err := writeDurableJSON(directory, "snapshot-admission", map[string]int64{"reservedBytes": MaxSnapshotArtifactBytes}); err != nil {
			return fmt.Errorf("record snapshot artifact admission: %w", err)
		}
		return nil
	})
}

func (m *Manager) QuerySnapshot(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (SnapshotManifest, error) {
	d, err := m.verifySnapshotDescriptor(reservation.LaunchPayload, nil)
	if err != nil {
		return SnapshotManifest{}, err
	}
	b, ok := m.backend.(SnapshotBackend)
	if !ok {
		return SnapshotManifest{}, fmt.Errorf("snapshot supervisor backend is unavailable")
	}
	var out SnapshotManifest
	err = m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		r, e := m.readBoundJournal(directory, reservation, identity)
		if e != nil {
			return e
		}
		out, e = b.QuerySnapshot(ctx, backendExecution(r, directory), d)
		return e
	})
	return out, err
}

// ObserveSnapshot returns signed generic effect evidence for the snapshot
// protocol. It never accepts runner-local state without the backend's
// containment proof, which lets effect.Executor recover the same durable
// reservation after an expired operation claim.
func (m *Manager) ObserveSnapshot(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	descriptor, err := m.verifySnapshotDescriptor(reservation.LaunchPayload, nil)
	if err != nil {
		return effect.Observation{}, err
	}
	backend, ok := m.backend.(SnapshotBackend)
	if !ok {
		return effect.Observation{}, fmt.Errorf("snapshot supervisor backend is unavailable")
	}
	var observation effect.Observation
	err = m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		if record.RuntimeInstanceID == "" {
			observation, err = m.observation(record, BackendState{Phase: effect.SupervisorNotFound, ContainmentProven: true, EvidenceReference: "registered-not-launched/" + reservation.SupervisorExecutionID})
			return err
		}
		state, err := backend.ObserveSnapshot(ctx, backendExecution(record, directory), descriptor)
		if err != nil {
			return err
		}
		observation, err = m.observation(record, state)
		return err
	})
	return observation, err
}

// RetrieveSnapshotResult re-observes the exact execution and returns its
// signed-manifest output. Artifact bytes remain behind CopySnapshotArtifact.
func (m *Manager) RetrieveSnapshotResult(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity, _ string) ([]byte, error) {
	observation, err := m.ObserveSnapshot(ctx, reservation, identity)
	if err != nil {
		return nil, err
	}
	if observation.Phase != effect.SupervisorSucceeded && observation.Phase != effect.SupervisorFailed {
		return nil, fmt.Errorf("snapshot execution has no terminal result")
	}
	return observation.Output, nil
}

func (m *Manager) CopySnapshotArtifact(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity, destination io.Writer) (SnapshotManifest, error) {
	d, err := m.verifySnapshotDescriptor(reservation.LaunchPayload, nil)
	if err != nil {
		return SnapshotManifest{}, err
	}
	b, ok := m.backend.(SnapshotBackend)
	if !ok {
		return SnapshotManifest{}, fmt.Errorf("snapshot supervisor backend is unavailable")
	}
	var out SnapshotManifest
	err = m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		r, e := m.readBoundJournal(directory, reservation, identity)
		if e != nil {
			return e
		}
		out, e = b.CopySnapshotArtifact(ctx, backendExecution(r, directory), d, destination)
		return e
	})
	return out, err
}

func (m *Manager) Query(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return m.observe(ctx, reservation, identity)
}

func (m *Manager) Revoke(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	var observation effect.Observation
	err := m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		if err := m.writeTombstone(directory, record); err != nil {
			return err
		}
		if record.RuntimeInstanceID == "" {
			observation, err = m.observation(record, BackendState{
				Phase: effect.SupervisorNotFound, ContainmentProven: true,
				EvidenceReference: "tombstone/" + record.SupervisorExecutionID,
			})
			return err
		}
		state, err := m.backend.Revoke(ctx, backendExecution(record, directory))
		if err != nil {
			return err
		}
		observation, err = m.observation(record, state)
		return err
	})
	return observation, err
}

func (m *Manager) RetrieveResult(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity, reference string) ([]byte, error) {
	var output []byte
	err := m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		output, err = m.backend.RetrieveResult(ctx, backendExecution(record, directory), reference)
		return err
	})
	return output, err
}

func (m *Manager) observe(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	var observation effect.Observation
	err := m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		if record.RuntimeInstanceID == "" {
			observation, err = m.observation(record, BackendState{
				Phase: effect.SupervisorNotFound, ContainmentProven: true,
				EvidenceReference: "registered-not-launched/" + reservation.SupervisorExecutionID,
			})
			return err
		}
		state, err := m.backend.Observe(ctx, backendExecution(record, directory))
		if err != nil {
			return err
		}
		observation, err = m.observation(record, state)
		return err
	})
	return observation, err
}

func (m *Manager) observation(record journal, state BackendState) (effect.Observation, error) {
	phase := state.Phase
	if isTerminal(phase) && !state.ContainmentProven {
		phase = effect.SupervisorUnknown
	}
	resultDigest, resultReference := "", ""
	if phase == effect.SupervisorSucceeded || phase == effect.SupervisorFailed {
		resultDigest = effect.DigestInput(state.Output)
		resultReference = "result/" + record.SupervisorExecutionID
	}
	runtimeInstanceID := record.RuntimeInstanceID
	if phase == effect.SupervisorNotFound {
		runtimeInstanceID = ""
	}
	assertion := evidenceAssertion{
		Protocol: ProtocolV1, InputDigest: record.InputDigest, SupervisorExecutionID: record.SupervisorExecutionID,
		RuntimeInstanceID: runtimeInstanceID, Phase: phase, ExitCode: state.ExitCode,
		ResultDigest: resultDigest, ResultReference: resultReference, ContainmentProven: state.ContainmentProven,
		TimedOut: state.TimedOut, OutputDiscarded: state.OutputDiscarded,
		EvidenceReference: state.EvidenceReference, ObservedAt: time.Now().UTC(),
	}
	payload, err := m.signJSON(assertion)
	if err != nil {
		return effect.Observation{}, err
	}
	return effect.Observation{
		Identity: effect.ExecutionIdentity{Supervisor: record.Supervisor, SupervisorExecutionID: record.SupervisorExecutionID, RuntimeInstanceID: runtimeInstanceID},
		Phase:    phase, ExitCode: state.ExitCode, Output: append([]byte(nil), state.Output...),
		Evidence: effect.RawEvidence{Source: evidenceSource, Reference: state.EvidenceReference, Payload: payload},
	}, nil
}

func (m *Manager) readBoundJournal(directory string, reservation effect.Reservation, identity effect.ExecutionIdentity) (journal, error) {
	if _, err := m.verifyAnyDescriptor(reservation.LaunchPayload); err != nil {
		return journal{}, err
	}
	registered, err := m.registryEntry(reservation.SupervisorExecutionID)
	if err != nil {
		return journal{}, err
	}
	if registered == nil || registered.InputDigest != reservation.InputDigest ||
		registered.Supervisor != reservation.Supervisor || registered.SupervisorExecutionID != reservation.SupervisorExecutionID {
		return journal{}, fmt.Errorf("effect supervisor execution is not durably registered")
	}
	record, err := m.readJournal(directory)
	if err != nil {
		return journal{}, err
	}
	if registered.RuntimeInstanceID != "" && registered.RuntimeInstanceID != record.RuntimeInstanceID {
		return journal{}, fmt.Errorf("supervisor journal does not match the registered launch")
	}
	if registered.RuntimeInstanceID == "" && record.RuntimeInstanceID != "" {
		return journal{}, fmt.Errorf("supervisor journal names a launch the registry never recorded")
	}
	if reservation.InputDigest != record.InputDigest || reservation.Supervisor != record.Supervisor ||
		reservation.SupervisorExecutionID != record.SupervisorExecutionID || identity.Supervisor != record.Supervisor || identity.SupervisorExecutionID != record.SupervisorExecutionID ||
		(identity.RuntimeInstanceID != "" && identity.RuntimeInstanceID != record.RuntimeInstanceID) {
		return journal{}, fmt.Errorf("supervisor identity does not match durable journal")
	}
	return record, nil
}

func (m *Manager) loadOrCreateRegistry() (string, error) {
	lock, err := os.OpenFile(filepath.Join(m.root, ".root-identity.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state, err := m.readRegistry()
	if err == nil {
		return state.RootID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != ".root-identity.lock" {
			return "", fmt.Errorf("effect supervisor registry is missing from a non-empty state root")
		}
	}
	state = registry{Protocol: ProtocolV1, RootID: uuid.NewString(), Entries: map[string]registryEntry{}}
	if err := m.writeRegistry(state); err != nil {
		return "", err
	}
	return state.RootID, nil
}

func (m *Manager) withRegistryLock(fn func(*registry) error) error {
	lock, err := os.OpenFile(filepath.Join(m.root, ".root-identity.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state, err := m.readRegistry()
	if err != nil {
		return err
	}
	return fn(&state)
}

func (m *Manager) registryEntry(executionID string) (*registryEntry, error) {
	var result *registryEntry
	err := m.withRegistryLock(func(state *registry) error {
		entry, ok := state.Entries[executionID]
		if ok {
			copy := entry
			result = &copy
		}
		return nil
	})
	return result, err
}

func (m *Manager) registerExecution(entry registryEntry) error {
	return m.withRegistryLock(func(state *registry) error {
		if current, exists := state.Entries[entry.SupervisorExecutionID]; exists {
			if !current.sameBinding(entry) {
				return fmt.Errorf("supervisor execution id is registered for different work")
			}
			return nil
		}
		state.Entries[entry.SupervisorExecutionID] = entry
		return m.writeRegistry(*state)
	})
}

// recordLaunchRuntime binds a runtime identity to a registered namespace. A
// namespace launches at most once; a different runtime is refused.
func (m *Manager) recordLaunchRuntime(record journal) error {
	return m.withRegistryLock(func(state *registry) error {
		entry, exists := state.Entries[record.SupervisorExecutionID]
		if !exists || entry.InputDigest != record.InputDigest || entry.MaterialMAC != record.MaterialMAC || entry.Supervisor != record.Supervisor {
			return fmt.Errorf("effect supervisor execution is not durably registered")
		}
		if entry.RuntimeInstanceID != "" {
			return fmt.Errorf("effect supervisor execution already has a recorded launch")
		}
		entry.RuntimeInstanceID = record.RuntimeInstanceID
		state.Entries[record.SupervisorExecutionID] = entry
		return m.writeRegistry(*state)
	})
}

func requireFreshExecutionDirectory(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "lock" && !strings.HasPrefix(entry.Name(), ".journal-") {
			return fmt.Errorf("effect supervisor execution history exists without a registration")
		}
	}
	return nil
}

func (m *Manager) readRegistry() (registry, error) {
	data, err := readBoundedRegular(filepath.Join(m.root, "registry.json"), 64<<20)
	if err != nil {
		return registry{}, err
	}
	var envelope signedRegistry
	if err := json.Unmarshal(data, &envelope); err != nil {
		return registry{}, err
	}
	encoded, _ := json.Marshal(envelope.Registry)
	if !hmac.Equal([]byte(envelope.MAC), []byte(m.mac(encoded))) || envelope.Registry.Protocol != ProtocolV1 ||
		envelope.Registry.RootID == "" || envelope.Registry.Entries == nil {
		return registry{}, fmt.Errorf("effect supervisor registry authentication failed")
	}
	return envelope.Registry, nil
}

func (m *Manager) writeRegistry(state registry) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeDurableJSON(m.root, "registry.json", signedRegistry{Registry: state, MAC: m.mac(encoded)})
}

func (m *Manager) withExecutionLock(executionID string, fn func(string) error) error {
	if strings.TrimSpace(executionID) == "" {
		return fmt.Errorf("supervisor execution id is required")
	}
	digest := sha256.Sum256([]byte(executionID))
	directory := filepath.Join(m.root, hex.EncodeToString(digest[:]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn(directory)
}

func (m *Manager) writeJournal(directory string, record journal) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	envelope := signedJournal{Record: record, MAC: m.mac(encoded)}
	return writeDurableJSON(directory, "journal.json", envelope)
}

func (m *Manager) readJournal(directory string) (journal, error) {
	data, err := readBoundedRegular(filepath.Join(directory, "journal.json"), maxRunnerStatusBytes)
	if err != nil {
		return journal{}, err
	}
	var envelope signedJournal
	if err := json.Unmarshal(data, &envelope); err != nil {
		return journal{}, err
	}
	encoded, _ := json.Marshal(envelope.Record)
	if !hmac.Equal([]byte(envelope.MAC), []byte(m.mac(encoded))) {
		return journal{}, fmt.Errorf("effect supervisor journal authentication failed")
	}
	return envelope.Record, nil
}

func (m *Manager) writeTombstone(directory string, record journal) error {
	path := filepath.Join(directory, "tombstone")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(record)
	if _, err := file.Write([]byte(m.mac(encoded))); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func (m *Manager) signJSON(assertion evidenceAssertion) (json.RawMessage, error) {
	encoded, err := json.Marshal(assertion)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(signedEvidence{Assertion: assertion, MAC: m.mac(encoded)})
	return json.RawMessage(payload), err
}

func (m *Manager) mac(value []byte) string {
	mac := hmac.New(sha256.New, m.key)
	mac.Write(value)
	return hex.EncodeToString(mac.Sum(nil))
}

func writeDurableJSON(directory, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".journal-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, name)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func journalIdentity(record journal) effect.ExecutionIdentity {
	return effect.ExecutionIdentity{Supervisor: record.Supervisor, SupervisorExecutionID: record.SupervisorExecutionID, RuntimeInstanceID: record.RuntimeInstanceID}
}

func backendExecution(record journal, directory string) BackendExecution {
	return BackendExecution{SupervisorExecutionID: record.SupervisorExecutionID, RuntimeInstanceID: record.RuntimeInstanceID, StateDirectory: directory}
}

func isTerminal(phase effect.SupervisorPhase) bool {
	return phase == effect.SupervisorSucceeded || phase == effect.SupervisorFailed || phase == effect.SupervisorStopped || phase == effect.SupervisorNotFound
}

var _ effect.Supervisor = (*Manager)(nil)
