package main

// Inert target-etcd transport for signed effect evidence. Deliberately not
// registered by the production router. No source/provider/backend credential
// or proxy is held here; the target ledger owns every transition.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineSignedEffectStore interface {
	GetOfflineEffectChallenge(context.Context, string, string) (offline.EffectChallengeV1, error)
	IssueOfflineEffectChallenge(context.Context, string, string, string, string) (offline.EffectChallengeV1, error)
	ReadAcceptedOfflineEffectAttestation(context.Context, string, string, string) (etcdstore.AcceptedOfflineEffectAttestation, error)
	SubmitOfflineEffectAttestation(context.Context, string, string, string, string, []byte) (etcdstore.AcceptedOfflineEffectAttestation, error)
	GetOfflineSignedEffectGrant(context.Context, string, string, string) (offline.EffectGrant, error)
	IssueOfflineEffectGrantFromAccepted(context.Context, string, string, string, etcdstore.OfflineRunnerIdentity) (offline.EffectGrant, error)
	GetOfflineSignedConsumedGrant(context.Context, string, string, string) (offline.EffectGrant, error)
	ConsumeOfflineEffectGrantFromAccepted(context.Context, string, string, string, string, etcdstore.OfflineRunnerIdentity) (offline.EffectGrant, error)
}

func validOfflineSignedEffectTransportTrust(t offlineTerminalTransportTrust) bool {
	return t.valid() && t.runnerDeviceID != "" && t.runnerDeviceID != t.verifierDeviceID && t.runnerDeviceID != t.observerDeviceID &&
		t.targetID != "" && targetTerminalSHA.MatchString(t.sourceCommit) && !t.expiresAt.IsZero() && time.Now().UTC().Before(t.expiresAt)
}

func registerEtcdFleetOfflineSignedEffectRoutesWithAuth(router chi.Router, runnerAuth, verifierAuth, observerAuth func(http.Handler) http.Handler,
	identities offlineIdentityStore, store offlineSignedEffectStore, trust offlineTerminalTransportTrust) {
	if router == nil || runnerAuth == nil || verifierAuth == nil || observerAuth == nil || store == nil || !validOfflineSignedEffectTransportTrust(trust) {
		return
	}
	noStore := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			next.ServeHTTP(w, r)
		})
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		status, code := http.StatusConflict, "offline_effect_inconsistent"
		if errors.Is(err, etcdstore.ErrNotFound) {
			status, code = http.StatusNotFound, "offline_effect_not_found"
		}
		handler.WriteControlProblem(w, r, status, code, "signed effect state is absent or inconsistent")
	}
	invalid := func(w http.ResponseWriter, r *http.Request) {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "offline_effect_request_invalid", "request identifiers or canonical body are invalid")
	}
	write := func(w http.ResponseWriter, status int, field string, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{field: value})
	}
	ids := func(r *http.Request) (string, string, string, bool) {
		planID, attemptID, purpose := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID"), chi.URLParam(r, "purpose")
		return planID, attemptID, purpose, uuid.Validate(planID) == nil && uuid.Validate(attemptID) == nil &&
			(purpose == offline.EffectIssuePurpose || purpose == offline.EffectConsumePurpose)
	}
	pinned := func(w http.ResponseWriter, r *http.Request, subject, scope, device string) (handler.AccessPrincipal, bool) {
		principal, ok := offlineTerminalPrincipal(r, trust, identities, subject, scope, device)
		if !ok {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_effect_principal_invalid", "a pinned active device-bound principal is required")
		}
		return principal, ok
	}
	matchChallenge := func(c offline.EffectChallengeV1, planID, attemptID, purpose string) bool {
		return c.PlanID == planID && c.AttemptID == attemptID && c.Purpose == purpose && c.ObserverSubject == trust.observerSubject &&
			c.RunnerSubject == trust.runnerSubject && c.TargetID == trust.targetID && c.SourceCommit == trust.sourceCommit && uuid.Validate(c.ID) == nil
	}
	matchAccepted := func(a etcdstore.AcceptedOfflineEffectAttestation, planID, attemptID, purpose, challengeID string) bool {
		return a.PlanID == planID && a.AttemptID == attemptID && a.Purpose == purpose && a.ChallengeID == challengeID &&
			matchChallenge(a.Challenge, planID, attemptID, purpose) && a.AttestationSHA256 != ""
	}
	matchRunnerReceipt := func(a etcdstore.AcceptedOfflineEffectAttestation, principal handler.AccessPrincipal) bool {
		return a.Attempt.PrincipalSubject == principal.Subject && a.Attempt.PrincipalTokenID == principal.TokenID &&
			a.Attempt.PrincipalDeviceID == principal.DeviceID
	}
	matchGrant := func(g offline.EffectGrant, planID, attemptID string) bool {
		return g.PlanID == planID && g.AttemptID == attemptID && g.TargetID == trust.targetID && g.SourceCommit == trust.sourceCommit && uuid.Validate(g.ID) == nil
	}
	empty := func(w http.ResponseWriter, r *http.Request) bool {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64))
		if err != nil || !bytes.Equal(body, []byte("{}")) {
			invalid(w, r)
			return false
		}
		return true
	}
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}/signed-effect/{purpose}"
	router.With(noStore, verifierAuth).Get(base+"/challenge", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := pinned(w, r, trust.verifierSubject, handler.ScopeFleetVerify, trust.verifierDeviceID); !ok {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		if !ok {
			invalid(w, r)
			return
		}
		c, err := store.GetOfflineEffectChallenge(r.Context(), plan, purpose)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchChallenge(c, plan, attempt, purpose) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		write(w, http.StatusOK, "challenge", c)
	})
	router.With(noStore, verifierAuth).Post(base+"/challenge", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := pinned(w, r, trust.verifierSubject, handler.ScopeFleetVerify, trust.verifierDeviceID); !ok {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		if !ok || !empty(w, r) {
			if !ok {
				invalid(w, r)
			}
			return
		}
		c, err := store.IssueOfflineEffectChallenge(r.Context(), plan, attempt, purpose, trust.observerSubject)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchChallenge(c, plan, attempt, purpose) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		write(w, http.StatusAccepted, "challenge", c)
	})
	router.With(noStore, observerAuth).Get(base+"/observer/challenge", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := pinned(w, r, trust.observerSubject, handler.ScopeFleetObserve, trust.observerDeviceID); !ok {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		if !ok {
			invalid(w, r)
			return
		}
		c, err := store.GetOfflineEffectChallenge(r.Context(), plan, purpose)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchChallenge(c, plan, attempt, purpose) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		write(w, http.StatusOK, "challenge", c)
	})
	router.With(noStore, observerAuth).Get(base+"/observer/attestations/{challengeID}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := pinned(w, r, trust.observerSubject, handler.ScopeFleetObserve, trust.observerDeviceID); !ok {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		challengeID := chi.URLParam(r, "challengeID")
		if !ok || uuid.Validate(challengeID) != nil {
			invalid(w, r)
			return
		}
		value, err := store.ReadAcceptedOfflineEffectAttestation(r.Context(), plan, purpose, challengeID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchAccepted(value, plan, attempt, purpose, challengeID) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		write(w, http.StatusOK, "accepted", value)
	})
	router.With(noStore, observerAuth).Put(base+"/observer/attestations/{challengeID}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := pinned(w, r, trust.observerSubject, handler.ScopeFleetObserve, trust.observerDeviceID); !ok {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		challengeID := chi.URLParam(r, "challengeID")
		if !ok || uuid.Validate(challengeID) != nil {
			invalid(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
		if err != nil {
			invalid(w, r)
			return
		}
		signed, err := offline.DecodeSignedEffectAttestationV1(body)
		obs := signed.Attestation.Observation
		if err != nil || signed.Attestation.Purpose != purpose || signed.Attestation.ChallengeID != challengeID ||
			signed.Attestation.ObserverSubject != trust.observerSubject || obs.PlanID != plan || obs.AttemptID != attempt ||
			obs.TargetID != trust.targetID || obs.SourceCommit != trust.sourceCommit {
			invalid(w, r)
			return
		}
		value, err := store.SubmitOfflineEffectAttestation(r.Context(), plan, attempt, purpose, challengeID, body)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchAccepted(value, plan, attempt, purpose, challengeID) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		write(w, http.StatusOK, "accepted", value)
	})
	issueGrantPath := base + "/runner/grants/{challengeID}"
	router.With(noStore, runnerAuth).Get(issueGrantPath, func(w http.ResponseWriter, r *http.Request) {
		principal, allowed := pinned(w, r, trust.runnerSubject, handler.ScopeFleetOperate, trust.runnerDeviceID)
		if !allowed {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		challengeID := chi.URLParam(r, "challengeID")
		if !ok || purpose != offline.EffectIssuePurpose || uuid.Validate(challengeID) != nil {
			invalid(w, r)
			return
		}
		receipt, err := store.ReadAcceptedOfflineEffectAttestation(r.Context(), plan, purpose, challengeID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchAccepted(receipt, plan, attempt, purpose, challengeID) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		if !matchRunnerReceipt(receipt, principal) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_effect_principal_invalid", "the original attempt principal is required")
			return
		}
		grant, err := store.GetOfflineSignedEffectGrant(r.Context(), plan, attempt, challengeID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchGrant(grant, plan, attempt) || grant.Phase != "issued" || grant.ConsumedAt != nil {
			fail(w, r, offline.ErrEffectProofMismatch)
			return
		}
		write(w, http.StatusOK, "grant", grant)
	})
	router.With(noStore, runnerAuth).Post(issueGrantPath, func(w http.ResponseWriter, r *http.Request) {
		principal, ok := pinned(w, r, trust.runnerSubject, handler.ScopeFleetOperate, trust.runnerDeviceID)
		if !ok {
			return
		}
		plan, attempt, purpose, valid := ids(r)
		challengeID := chi.URLParam(r, "challengeID")
		if !valid || purpose != offline.EffectIssuePurpose || uuid.Validate(challengeID) != nil {
			invalid(w, r)
			return
		}
		if !empty(w, r) {
			return
		}
		identity := etcdstore.OfflineRunnerIdentity{Subject: principal.Subject, TokenID: principal.TokenID, DeviceID: principal.DeviceID}
		grant, err := store.IssueOfflineEffectGrantFromAccepted(r.Context(), plan, attempt, challengeID, identity)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchGrant(grant, plan, attempt) || grant.Phase != "issued" || grant.ConsumedAt != nil {
			fail(w, r, offline.ErrEffectProofMismatch)
			return
		}
		write(w, http.StatusAccepted, "grant", grant)
	})
	consumePath := base + "/runner/grants/{grantID}/consume/{challengeID}"
	router.With(noStore, runnerAuth).Get(consumePath, func(w http.ResponseWriter, r *http.Request) {
		principal, allowed := pinned(w, r, trust.runnerSubject, handler.ScopeFleetOperate, trust.runnerDeviceID)
		if !allowed {
			return
		}
		plan, attempt, purpose, ok := ids(r)
		grantID, challengeID := chi.URLParam(r, "grantID"), chi.URLParam(r, "challengeID")
		if !ok || purpose != offline.EffectConsumePurpose || uuid.Validate(grantID) != nil || uuid.Validate(challengeID) != nil {
			invalid(w, r)
			return
		}
		receipt, err := store.ReadAcceptedOfflineEffectAttestation(r.Context(), plan, purpose, challengeID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchAccepted(receipt, plan, attempt, purpose, challengeID) {
			fail(w, r, offline.ErrEffectAttestation)
			return
		}
		if !matchRunnerReceipt(receipt, principal) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_effect_principal_invalid", "the original attempt principal is required")
			return
		}
		grant, err := store.GetOfflineSignedConsumedGrant(r.Context(), plan, attempt, challengeID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchGrant(grant, plan, attempt) || grant.ID != grantID || grant.Phase != "consumed" {
			fail(w, r, offline.ErrEffectProofMismatch)
			return
		}
		write(w, http.StatusOK, "grant", grant)
	})
	router.With(noStore, runnerAuth).Post(consumePath, func(w http.ResponseWriter, r *http.Request) {
		principal, ok := pinned(w, r, trust.runnerSubject, handler.ScopeFleetOperate, trust.runnerDeviceID)
		if !ok {
			return
		}
		plan, attempt, purpose, valid := ids(r)
		grantID, challengeID := chi.URLParam(r, "grantID"), chi.URLParam(r, "challengeID")
		if !valid || purpose != offline.EffectConsumePurpose || uuid.Validate(grantID) != nil || uuid.Validate(challengeID) != nil {
			invalid(w, r)
			return
		}
		if !empty(w, r) {
			return
		}
		identity := etcdstore.OfflineRunnerIdentity{Subject: principal.Subject, TokenID: principal.TokenID, DeviceID: principal.DeviceID}
		grant, err := store.ConsumeOfflineEffectGrantFromAccepted(r.Context(), plan, attempt, grantID, challengeID, identity)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !matchGrant(grant, plan, attempt) || grant.ID != grantID || grant.Phase != "consumed" {
			fail(w, r, offline.ErrEffectProofMismatch)
			return
		}
		write(w, http.StatusAccepted, "grant", grant)
	})
}
