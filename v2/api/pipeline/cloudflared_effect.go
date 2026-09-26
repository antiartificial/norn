package pipeline

import (
	"bytes"
	"context"
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

	"norn/v2/api/cloudflared"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const cloudflaredMutationKind = "app.cloudflared-mutate"

// CloudflaredEffects is intentionally host-bound. A worker on another host
// must defer the claim rather than editing its own local tunnel configuration.
type CloudflaredEffects struct {
	store    *store.PGEffectStore
	executor *effect.Executor
	driver   cloudflaredDriver
}

type cloudflaredDriver interface {
	Read(context.Context) (*cloudflared.Config, string, error)
	Apply(context.Context, *cloudflared.Config) error
	Restart(context.Context) error
	ReadReceipt(string) ([]byte, error)
	WriteReceipt(string, []byte) error
	Host() string
	Path() string
}

type localCloudflaredDriver struct{}

func (localCloudflaredDriver) Read(ctx context.Context) (*cloudflared.Config, string, error) {
	return cloudflared.ReadConfigSnapshot(ctx)
}
func (localCloudflaredDriver) Apply(ctx context.Context, cfg *cloudflared.Config) error {
	return cloudflared.ApplyConfig(ctx, cfg)
}
func (localCloudflaredDriver) Restart(ctx context.Context) error { return cloudflared.Restart(ctx) }
func (localCloudflaredDriver) Host() string                      { host, _ := os.Hostname(); return host }
func (localCloudflaredDriver) Path() string                      { return cloudflared.ConfigPath() }
func cloudflaredReceiptPath(id string) string {
	return filepath.Join(filepath.Dir(cloudflared.ConfigPath()), ".norn-cloudflared-receipts", id+".json")
}
func (localCloudflaredDriver) ReadReceipt(id string) ([]byte, error) {
	if err := secureCloudflaredReceiptDir(filepath.Dir(cloudflaredReceiptPath(id)), false); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(cloudflaredReceiptPath(id), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 1 || info.Size() > 4096 {
		return nil, fmt.Errorf("cloudflared receipt is not a small owner-only regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, fmt.Errorf("cloudflared receipt owner or link count differs")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return nil, fmt.Errorf("cloudflared receipt is unreadable or too large")
	}
	return data, nil
}
func (localCloudflaredDriver) WriteReceipt(id string, data []byte) error {
	path := cloudflaredReceiptPath(id)
	if err := secureCloudflaredReceiptDir(filepath.Dir(path), true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, readErr := (localCloudflaredDriver{}).ReadReceipt(id)
		if readErr != nil || !bytes.Equal(existing, data) {
			return fmt.Errorf("existing cloudflared receipt differs from this execution")
		}
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func secureCloudflaredReceiptDir(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("cloudflared receipt directory is not private")
	}
	return nil
}

func NewCloudflaredEffects(db *store.DB) (*CloudflaredEffects, error) {
	return newCloudflaredEffects(db, localCloudflaredDriver{})
}
func newCloudflaredEffects(db *store.DB, driver cloudflaredDriver) (*CloudflaredEffects, error) {
	if driver == nil || driver.Host() == "" || driver.Path() == "" {
		return nil, fmt.Errorf("cloudflared host identity is unavailable")
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	supervisor := &cloudflaredSupervisor{driver: driver}
	return &CloudflaredEffects{store: effectStore, driver: driver, executor: &effect.Executor{Store: effectStore, Supervisor: supervisor, Verifier: cloudflaredVerifier{}}}, nil
}
func (p *Pipeline) CloudflaredMutationAvailable() bool {
	return p != nil && p.CloudflaredEffects != nil && p.CloudflaredEffects.executor != nil
}

func (p *Pipeline) executeCloudflaredMutation(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CloudflaredMutationAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable cloudflared mutation is unavailable"}
	}
	var mutation cloudflared.Mutation
	data, err := json.Marshal(op.Payload)
	if err == nil {
		err = json.Unmarshal(data, &mutation)
	}
	if err != nil || mutation.App != op.App || mutation.BeforeDigest == "" || mutation.AfterDigest == "" {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "invalid accepted cloudflared mutation"}
	}
	resource := "host/" + mutation.Host + "/cloudflared"
	if mutation.Host != p.CloudflaredEffects.driver.Host() || mutation.ConfigPath != p.CloudflaredEffects.driver.Path() {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "cloudflared mutation belongs to another host or config path"})
	}
	authority, err := p.CloudflaredEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "control authority unavailable", Cause: err})
	}
	reservation := effect.Reservation{Authority: authority, Resource: resource,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          cloudflaredMutationKind, Supervisor: "cloudflared-host", LaunchPayload: data}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	sum := sha256.Sum256([]byte(authority + "\x00" + op.ID + "\x00" + reservation.InputDigest))
	reservation.SupervisorExecutionID = "cloudflared-" + hex.EncodeToString(sum[:16])
	result, err := p.CloudflaredEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	if errors.Is(err, effect.ErrResourceBlocked) {
		blocking, found, lookupErr := p.CloudflaredEffects.store.UnresolvedForResource(ctx, authority, resource)
		if lookupErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "blocking ingress effect lookup failed", Cause: lookupErr})
		}
		if found {
			_, _ = p.CloudflaredEffects.executor.Recover(ctx, blocking)
		}
	}
	if err != nil {
		if effect.IsDeferred(err) || errors.Is(err, effect.ErrResourceBlocked) {
			return deferredResult(claim, err)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cloudflared effect: " + err.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cloudflared mutation failed"}
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("cloudflared %s completed for %s", mutation.Action, mutation.App), Metadata: map[string]interface{}{"effectId": result.EffectID, "effectReused": result.Reused, "configDigest": mutation.AfterDigest}}
}

type cloudflaredSupervisor struct{ driver cloudflaredDriver }

func cloudflaredMutation(r effect.Reservation) (cloudflared.Mutation, error) {
	var m cloudflared.Mutation
	if err := json.Unmarshal(r.LaunchPayload, &m); err != nil {
		return m, err
	}
	if m.Host == "" || m.ConfigPath == "" || m.BeforeDigest == "" || m.AfterDigest == "" || m.BeforeDigest == m.AfterDigest {
		return m, fmt.Errorf("invalid cloudflared mutation descriptor")
	}
	return m, nil
}
func (s *cloudflaredSupervisor) Prepare(ctx context.Context, r effect.Reservation) error {
	m, err := cloudflaredMutation(r)
	if err != nil {
		return err
	}
	if s.driver.Host() != m.Host || s.driver.Path() != m.ConfigPath {
		return fmt.Errorf("cloudflared host changed")
	}
	if _, err := s.driver.ReadReceipt(r.SupervisorExecutionID); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, digest, err := s.driver.Read(ctx)
	if err != nil {
		return err
	}
	if digest != m.BeforeDigest {
		return fmt.Errorf("cloudflared config changed before launch or an earlier launch has ambiguous restart outcome")
	}
	return nil
}
func (s *cloudflaredSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	m, err := cloudflaredMutation(r)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	cfg, before, err := s.driver.Read(ctx)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	if before != m.BeforeDigest {
		return effect.ExecutionIdentity{}, fmt.Errorf("cloudflared config no longer matches accepted state")
	}
	if _, err := m.Apply(cfg); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	after, err := cloudflared.ConfigDigest(cfg)
	if err != nil || after != m.AfterDigest {
		return effect.ExecutionIdentity{}, fmt.Errorf("cloudflared target digest mismatch")
	}
	if err := s.driver.Apply(ctx, cfg); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	if err := s.driver.Restart(ctx); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	output := []byte(fmt.Sprintf(`{"executionId":%q,"host":%q,"configDigest":%q}`, r.SupervisorExecutionID, m.Host, m.AfterDigest))
	if err := s.driver.WriteReceipt(r.SupervisorExecutionID, output); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: m.Host}, nil
}
func (s *cloudflaredSupervisor) Query(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	m, err := cloudflaredMutation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	if s.driver.Host() != m.Host || s.driver.Path() != m.ConfigPath {
		return effect.Observation{}, fmt.Errorf("cloudflared host changed")
	}
	id.Supervisor, id.SupervisorExecutionID, id.RuntimeInstanceID = r.Supervisor, r.SupervisorExecutionID, m.Host
	output, err := s.driver.ReadReceipt(r.SupervisorExecutionID)
	if errors.Is(err, os.ErrNotExist) {
		return effect.Observation{Identity: id, Phase: effect.SupervisorUnknown}, nil
	}
	if err != nil {
		return effect.Observation{}, err
	}
	var receipt struct {
		ExecutionID  string `json:"executionId"`
		Host         string `json:"host"`
		ConfigDigest string `json:"configDigest"`
	}
	if err := json.Unmarshal(output, &receipt); err != nil || receipt.ExecutionID != r.SupervisorExecutionID || receipt.Host != m.Host || receipt.ConfigDigest != m.AfterDigest {
		return effect.Observation{}, fmt.Errorf("cloudflared receipt does not match accepted effect")
	}
	return effect.Observation{Identity: id, Phase: effect.SupervisorSucceeded, Output: output, Evidence: effect.RawEvidence{Source: "cloudflared.local-receipt", Reference: r.SupervisorExecutionID, Payload: output}}, nil
}
func (s *cloudflaredSupervisor) Revoke(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, id)
}
func (s *cloudflaredSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity, _ string) ([]byte, error) {
	obs, err := s.Query(ctx, r, id)
	return obs.Output, err
}

type cloudflaredVerifier struct{}

func (cloudflaredVerifier) Verify(_ context.Context, record effect.Record, obs effect.Observation) (effect.Verification, error) {
	if obs.Phase != effect.SupervisorSucceeded || obs.Identity.SupervisorExecutionID != record.Reservation.SupervisorExecutionID || strings.TrimSpace(obs.Identity.RuntimeInstanceID) == "" {
		return effect.Verification{}, fmt.Errorf("cloudflared restart has no verified local receipt")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: record.Reservation.InputDigest, ResultDigest: effect.DigestInput(obs.Output), ResultReference: obs.Evidence.Reference, SupervisorExecutionID: obs.Identity.SupervisorExecutionID, RuntimeInstanceID: obs.Identity.RuntimeInstanceID, EvidenceSource: obs.Evidence.Source, EvidenceReference: obs.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
