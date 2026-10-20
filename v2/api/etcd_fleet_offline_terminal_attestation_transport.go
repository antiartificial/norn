package main

// These routes are deliberately not registered by the production router.
// They describe the independent management observer's narrow transport into
// the target Fleet authority, which alone owns the terminal ledger CAS.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineTerminalAttestationStore interface {
	GetOfflineEffectGrant(context.Context, string) (offline.EffectGrant, error)
	GetOfflineTerminal(context.Context, string) (offline.TerminalEvidence, error)
	GetOfflineTerminalChallenge(context.Context, string) (offline.TerminalChallengeV1, error)
	IssueOfflineTerminalChallenge(context.Context, string, string, string, string, string) (offline.TerminalChallengeV1, error)
	SubmitOfflineTerminalAttestation(context.Context, string, string, string, []byte) (offline.TerminalEvidence, error)
}

// This trust is supplied only by a future target-etcd-controller wiring path.
// It must never be sourced from the PostgreSQL management authority config.
type offlineTerminalTransportTrust struct {
	// Present only for loaded target-etcd trust. Legacy transport fixtures keep
	// these empty while production wiring must compare the exact Fleet plan.
	targetID         string
	sourceCommit     string
	runnerSubject    string
	verifierSubject  string
	observerSubject  string
	verifierDeviceID string
	observerDeviceID string
}

func (t offlineTerminalTransportTrust) valid() bool {
	return t.runnerSubject != "" && t.verifierSubject != "" && t.observerSubject != "" &&
		(t.targetID == "") == (t.sourceCommit == "") &&
		t.verifierDeviceID != "" && t.observerDeviceID != "" && t.verifierDeviceID != t.observerDeviceID &&
		t.runnerSubject != t.verifierSubject && t.runnerSubject != t.observerSubject && t.verifierSubject != t.observerSubject &&
		!strings.ContainsAny(t.runnerSubject+t.verifierSubject+t.observerSubject+t.verifierDeviceID+t.observerDeviceID, " \t\r\n")
}

func offlineTerminalPrincipal(r *http.Request, trust offlineTerminalTransportTrust, identities offlineIdentityStore, subject, scope, deviceID string) (handler.AccessPrincipal, bool) {
	principal, ok := handler.AccessPrincipalFromRequest(r)
	if !ok || !trust.valid() || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.Legacy ||
		principal.Subject != subject || principal.TokenID == "" || principal.DeviceID != deviceID || principal.CI != nil ||
		len(principal.Scopes) != 1 || principal.Scopes[0] != scope ||
		!offlineReservationDeviceActive(r.Context(), identities, principal.DeviceID) {
		return handler.AccessPrincipal{}, false
	}
	return principal, true
}

func registerEtcdFleetOfflineTerminalAttestationRoutesWithAuth(router chi.Router, verifierAuth, observerAuth func(http.Handler) http.Handler,
	identities offlineIdentityStore, store offlineTerminalAttestationStore, trust offlineTerminalTransportTrust) {
	if router == nil || verifierAuth == nil || observerAuth == nil || store == nil || !trust.valid() {
		return
	}
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}"
	challengePath := base + "/terminal-challenges"
	observerPath := base + "/terminal-observer"
	noStore := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			next.ServeHTTP(w, r)
		})
	}
	validIDs := func(r *http.Request) (string, string, bool) {
		planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
		return planID, attemptID, uuid.Validate(planID) == nil && uuid.Validate(attemptID) == nil
	}
	write := func(w http.ResponseWriter, status int, field string, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{field: value, "authorizesEffect": false})
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		status, code := http.StatusConflict, "offline_terminal_inconsistent"
		if errors.Is(err, etcdstore.ErrNotFound) {
			status, code = http.StatusNotFound, "offline_terminal_not_found"
		}
		handler.WriteControlProblem(w, r, status, code, "terminal state is absent or inconsistent")
	}
	verifier := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := offlineTerminalPrincipal(r, trust, identities, trust.verifierSubject, handler.ScopeFleetVerify, trust.verifierDeviceID); !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_verifier_invalid", "a distinct pinned active device-bound verifier is required")
			return false
		}
		return true
	}
	observer := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := offlineTerminalPrincipal(r, trust, identities, trust.observerSubject, handler.ScopeFleetObserve, trust.observerDeviceID); !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_observer_invalid", "a distinct pinned active device-bound observer is required")
			return false
		}
		return true
	}
	readGrant := func(w http.ResponseWriter, r *http.Request) (offline.EffectGrant, bool) {
		planID, attemptID, ok := validIDs(r)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_ids_invalid", "invalid plan or attempt ID")
			return offline.EffectGrant{}, false
		}
		grant, err := store.GetOfflineEffectGrant(r.Context(), planID)
		if err != nil {
			fail(w, r, err)
			return offline.EffectGrant{}, false
		}
		if grant.PlanID != planID || grant.AttemptID != attemptID || grant.Phase != "consumed" || uuid.Validate(grant.ID) != nil ||
			(trust.targetID != "" && grant.TargetID != trust.targetID) || (trust.sourceCommit != "" && grant.SourceCommit != trust.sourceCommit) {
			fail(w, r, offline.ErrTerminalAttestation)
			return offline.EffectGrant{}, false
		}
		return grant, true
	}
	router.With(noStore, observerAuth).Get(observerPath+"/grant", func(w http.ResponseWriter, r *http.Request) {
		if !observer(w, r) {
			return
		}
		if grant, ok := readGrant(w, r); ok {
			write(w, http.StatusOK, "grant", grant)
		}
	})
	router.With(noStore, observerAuth).Get(observerPath+"/evidence", func(w http.ResponseWriter, r *http.Request) {
		if !observer(w, r) {
			return
		}
		planID, attemptID, ok := validIDs(r)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_ids_invalid", "invalid plan or attempt ID")
			return
		}
		value, err := store.GetOfflineTerminal(r.Context(), planID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.PlanID != planID || value.AttemptID != attemptID {
			fail(w, r, offline.ErrTerminalAttestation)
			return
		}
		write(w, http.StatusOK, "terminalEvidence", value)
	})
	router.With(noStore, observerAuth).Get(observerPath+"/challenge", func(w http.ResponseWriter, r *http.Request) {
		if !observer(w, r) {
			return
		}
		planID, attemptID, ok := validIDs(r)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_ids_invalid", "invalid plan or attempt ID")
			return
		}
		value, err := store.GetOfflineTerminalChallenge(r.Context(), planID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.PlanID != planID || value.AttemptID != attemptID || value.ObserverSubject != trust.observerSubject || value.VerifierSubject != trust.verifierSubject ||
			(trust.targetID != "" && value.TargetID != trust.targetID) || (trust.sourceCommit != "" && value.SourceCommit != trust.sourceCommit) {
			fail(w, r, offline.ErrTerminalAttestation)
			return
		}
		write(w, http.StatusOK, "challenge", value)
	})
	router.With(noStore, verifierAuth).Get(challengePath, func(w http.ResponseWriter, r *http.Request) {
		if !verifier(w, r) {
			return
		}
		planID, attemptID, ok := validIDs(r)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_ids_invalid", "invalid plan or attempt ID")
			return
		}
		value, err := store.GetOfflineTerminalChallenge(r.Context(), planID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.PlanID != planID || value.AttemptID != attemptID || value.VerifierSubject != trust.verifierSubject || value.ObserverSubject != trust.observerSubject ||
			(trust.targetID != "" && value.TargetID != trust.targetID) || (trust.sourceCommit != "" && value.SourceCommit != trust.sourceCommit) {
			fail(w, r, offline.ErrTerminalAttestation)
			return
		}
		write(w, http.StatusOK, "challenge", value)
	})
	router.With(noStore, verifierAuth).Post(challengePath, func(w http.ResponseWriter, r *http.Request) {
		if !verifier(w, r) {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64))
		if err != nil || !bytes.Equal(bytes.TrimSpace(body), []byte("{}")) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_request_invalid", "challenge request must be an empty object")
			return
		}
		grant, ok := readGrant(w, r)
		if !ok {
			return
		}
		value, err := store.IssueOfflineTerminalChallenge(r.Context(), grant.PlanID, grant.AttemptID, grant.ID, trust.verifierSubject, trust.observerSubject)
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.PlanID != grant.PlanID || value.AttemptID != grant.AttemptID || value.GrantID != grant.ID || value.VerifierSubject != trust.verifierSubject || value.ObserverSubject != trust.observerSubject ||
			(trust.targetID != "" && value.TargetID != trust.targetID) || (trust.sourceCommit != "" && value.SourceCommit != trust.sourceCommit) {
			fail(w, r, offline.ErrTerminalAttestation)
			return
		}
		write(w, http.StatusAccepted, "challenge", value)
	})
	router.With(noStore, observerAuth).Put(challengePath+"/{challengeID}/attestation", func(w http.ResponseWriter, r *http.Request) {
		if !observer(w, r) {
			return
		}
		planID, attemptID, ok := validIDs(r)
		challengeID := chi.URLParam(r, "challengeID")
		if !ok || uuid.Validate(challengeID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_ids_invalid", "invalid plan, attempt, or challenge ID")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_attestation_invalid", "attestation body is too large or invalid")
			return
		}
		envelope, err := offline.DecodeSignedTerminalAttestationV1(body)
		if err != nil || envelope.Attestation.PlanID != planID || envelope.Attestation.AttemptID != attemptID || envelope.Attestation.ChallengeID != challengeID || envelope.Attestation.ObserverSubject != trust.observerSubject ||
			(trust.targetID != "" && envelope.Attestation.TargetID != trust.targetID) || (trust.sourceCommit != "" && envelope.Attestation.SourceCommit != trust.sourceCommit) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_terminal_attestation_invalid", "attestation body is noncanonical or mismatched")
			return
		}
		value, err := store.SubmitOfflineTerminalAttestation(r.Context(), planID, attemptID, challengeID, body)
		if err != nil {
			fail(w, r, err)
			return
		}
		if value.PlanID != planID || value.AttemptID != attemptID || value.GrantID != envelope.Attestation.GrantID {
			fail(w, r, offline.ErrTerminalAttestation)
			return
		}
		write(w, http.StatusOK, "terminalEvidence", value)
	})
}
