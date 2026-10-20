package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineDeleteRuntimeResolver interface {
	ResolveOfflineDeleteRuntime(context.Context, string, string, string) (supervisor.OfflineDeleteRuntimeIdentity, error)
}

type offlineDeleteTransportAuthority interface {
	Commit(context.Context, string, string, etcdstore.OfflineRunnerIdentity) (offline.DeleteCommitment, []byte, error)
	RecordTerminal(context.Context, string, string) (offline.TerminalEvidence, error)
}

type offlineDeleteDurableStore interface {
	GetOfflineDeleteCommitment(context.Context, string) (offline.DeleteCommitment, []byte, error)
	GetOfflineAttempt(context.Context, string) (offline.Attempt, error)
	GetOfflineEffectGrant(context.Context, string) (offline.EffectGrant, error)
	CommitOfflineDelete(context.Context, string, string, string, etcdstore.OfflineDeleteRuntimeIdentity) (offline.DeleteCommitment, []byte, error)
	GetOfflineTerminal(context.Context, string) (offline.TerminalEvidence, error)
	RecordOfflineTerminal(context.Context, string, string, string, time.Time, etcdstore.OfflineTerminalObserver) (offline.TerminalEvidence, error)
}

type serverOwnedOfflineDeleteAuthority struct {
	store    offlineDeleteDurableStore
	runtime  offlineDeleteRuntimeResolver
	observer etcdstore.OfflineTerminalObserver
	clock    func() time.Time
}

func (a *serverOwnedOfflineDeleteAuthority) Commit(ctx context.Context, planID, attemptID string, runner etcdstore.OfflineRunnerIdentity) (offline.DeleteCommitment, []byte, error) {
	if a == nil || a.store == nil || a.runtime == nil {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	if prior, raw, err := a.store.GetOfflineDeleteCommitment(ctx, planID); err == nil {
		if prior.AttemptID != attemptID || prior.RunnerPrincipalSubject != runner.Subject || prior.RunnerTokenID != runner.TokenID || prior.RunnerDeviceID != runner.DeviceID {
			return offline.DeleteCommitment{}, nil, etcdstore.ErrOfflineDeleteCommitmentConflict
		}
		return prior, raw, nil
	} else if !errors.Is(err, etcdstore.ErrNotFound) {
		return offline.DeleteCommitment{}, nil, err
	}
	attempt, err := a.store.GetOfflineAttempt(ctx, planID)
	if err != nil || attempt.ID != attemptID || attempt.PrincipalSubject != runner.Subject || attempt.PrincipalTokenID != runner.TokenID || attempt.PrincipalDeviceID != runner.DeviceID {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	grant, err := a.store.GetOfflineEffectGrant(ctx, planID)
	if err != nil || grant.AttemptID != attemptID || grant.Phase != "consumed" {
		return offline.DeleteCommitment{}, nil, offline.ErrDeleteCommitment
	}
	identity, err := a.runtime.ResolveOfflineDeleteRuntime(ctx, planID, attemptID, grant.ID)
	if err != nil {
		return offline.DeleteCommitment{}, nil, err
	}
	value, raw, err := a.store.CommitOfflineDelete(ctx, planID, attemptID, grant.ID, etcdstore.OfflineDeleteRuntimeIdentity{
		SupervisorRootID: identity.SupervisorRootID, SupervisorExecutionID: identity.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID,
	})
	if errors.Is(err, etcdstore.ErrOfflineDeleteCommitmentConflict) {
		prior, recovered, recoverErr := a.store.GetOfflineDeleteCommitment(ctx, planID)
		if recoverErr == nil && prior.AttemptID == attemptID && prior.RunnerPrincipalSubject == runner.Subject && prior.RunnerTokenID == runner.TokenID && prior.RunnerDeviceID == runner.DeviceID && prior.SupervisorRootID == identity.SupervisorRootID && prior.SupervisorExecutionID == identity.SupervisorExecutionID && prior.RuntimeInstanceID == identity.RuntimeInstanceID {
			return prior, recovered, nil
		}
	}
	return value, raw, err
}

func (a *serverOwnedOfflineDeleteAuthority) RecordTerminal(ctx context.Context, planID, attemptID string) (offline.TerminalEvidence, error) {
	if a == nil || a.store == nil || a.observer == nil || a.clock == nil {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	if prior, err := a.store.GetOfflineTerminal(ctx, planID); err == nil {
		if prior.AttemptID != attemptID {
			return offline.TerminalEvidence{}, etcdstore.ErrOfflineTerminalConflict
		}
		return prior, nil
	} else if !errors.Is(err, etcdstore.ErrNotFound) {
		return offline.TerminalEvidence{}, err
	}
	grant, err := a.store.GetOfflineEffectGrant(ctx, planID)
	if err != nil || grant.AttemptID != attemptID || grant.Phase != "consumed" {
		return offline.TerminalEvidence{}, offline.ErrTerminalProof
	}
	value, err := a.store.RecordOfflineTerminal(ctx, planID, attemptID, grant.ID, a.clock().UTC(), a.observer)
	if errors.Is(err, etcdstore.ErrOfflineTerminalConflict) {
		prior, recoverErr := a.store.GetOfflineTerminal(ctx, planID)
		if recoverErr == nil && prior.AttemptID == attemptID && prior.GrantID == grant.ID {
			return prior, nil
		}
	}
	return value, err
}

func registerEtcdFleetOfflineDeleteMutationRoutes(router chi.Router, runnerAuth, verifierAuth func(http.Handler) http.Handler, identities offlineIdentityStore, trust *offlineFleetTrust, authority offlineDeleteTransportAuthority) {
	if authority == nil || trust == nil {
		return
	}
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}"
	empty := func(w http.ResponseWriter, r *http.Request) bool {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64))
		return err == nil && bytes.Equal(bytes.TrimSpace(raw), []byte("{}"))
	}
	write := func(w http.ResponseWriter, field string, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		writeOfflineJSON(w, map[string]any{field: value, "authorizesEffect": false})
	}
	router.With(runnerAuth).Post(base+"/delete-commitment", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := offlineRunnerPrincipal(r, trust, identities)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_runner_invalid", "a pinned active device-bound runner is required")
			return
		}
		if !empty(w, r) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_commitment_request_invalid", "commitment request must be an empty object")
			return
		}
		value, _, err := authority.Commit(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), etcdstore.OfflineRunnerIdentity{Subject: principal.Subject, TokenID: principal.TokenID, DeviceID: principal.DeviceID})
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_commitment_refused", "commitment is unavailable or inconsistent")
			return
		}
		write(w, "commitment", value)
	})
	router.With(verifierAuth).Post(base+"/terminal-evidence", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := offlineVerifierPrincipal(r, trust, identities); !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_verifier_invalid", "a distinct pinned active device-bound verifier is required")
			return
		}
		if !empty(w, r) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_request_invalid", "terminal request must be an empty object")
			return
		}
		value, err := authority.RecordTerminal(r.Context(), chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"))
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_terminal_refused", "terminal evidence is unavailable or inconsistent")
			return
		}
		write(w, "terminalEvidence", value)
	})
}

func writeOfflineJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
