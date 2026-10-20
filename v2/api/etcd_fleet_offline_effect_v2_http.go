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

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

// OfflineEffectEvidenceSourceV2 is the server-owned effect-time trust
// boundary. A runner can trigger a read but cannot submit either observation.
// Production wiring is intentionally absent until the independent verifier
// and all-writer PostgreSQL fence are qualified together.
type OfflineEffectEvidenceSourceV2 interface {
	ObserveOfflineEffect(context.Context, string, string) (offline.EffectObservationV2, error)
	ObserveOfflineTerminal(context.Context, string, string, string) (offline.TerminalObservationV2, error)
}

type offlineEffectV2Store interface {
	IssueOfflineEffectGrantV2(context.Context, string, string, etcdstore.OfflineRunnerProof, offline.EffectObservationV2, time.Time) (offline.EffectGrantV2, error)
	CommitOfflineDeleteV2(context.Context, string, string, string, etcdstore.OfflineRunnerProof, string, string, time.Time) (offline.EffectGrantV2, error)
	DispatchOfflineDeleteV2(context.Context, string, string, string, etcdstore.OfflineRunnerProof, offline.EffectObservationV2, time.Time) (offline.DeleteDispatchV2, error)
	RecordOfflineTerminalV2(context.Context, string, string, string, etcdstore.OfflineRunnerProof, offline.TerminalObservationV2, time.Time) (offline.TerminalEvidenceV2, error)
	RecoverOfflineEffectGrantV2(context.Context, string, string, etcdstore.OfflineRunnerProof, time.Time) (offline.EffectGrantV2, error)
}

func registerEtcdFleetOfflineEffectV2Routes(router chi.Router, auth func(http.Handler) http.Handler,
	store offlineEffectV2Store, identities offlineEffectIdentityStore, source OfflineEffectEvidenceSourceV2) {
	base := "/api/v1/fleet/offline/attempts/{planID}/{attemptID}"
	router.With(offlineEffectNoStore, auth).Post(base+"/effect-grants/v2", etcdOfflineEffectV2Issue(store, identities, source))
	router.With(offlineEffectNoStore, auth).Get(base+"/effect-grants/v2", etcdOfflineEffectV2Read(store, identities, "grant"))
	router.With(offlineEffectNoStore, auth).Post(base+"/effect-grants/{grantID}/delete-commitment", etcdOfflineEffectV2Commit(store, identities))
	router.With(offlineEffectNoStore, auth).Post(base+"/effect-grants/{grantID}/dispatch", etcdOfflineEffectV2Dispatch(store, identities, source))
	router.With(offlineEffectNoStore, auth).Post(base+"/effect-grants/{grantID}/terminal", etcdOfflineEffectV2Terminal(store, identities, source))
	router.With(offlineEffectNoStore, auth).Get(base+"/delete-commitment", etcdOfflineEffectV2Read(store, identities, "commitment"))
	router.With(offlineEffectNoStore, auth).Get(base+"/delete-dispatch-reservation", etcdOfflineEffectV2Read(store, identities, "dispatch"))
	router.With(offlineEffectNoStore, auth).Get(base+"/terminal-evidence", etcdOfflineEffectV2Read(store, identities, "terminal"))
}

func offlineEffectV2Unavailable(w http.ResponseWriter, r *http.Request, store offlineEffectV2Store, source OfflineEffectEvidenceSourceV2, needsSource bool) bool {
	if store == nil || (needsSource && source == nil) {
		handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_effect_v2_unavailable", "offline effect v2 requires a qualified independent evidence source and all-writer fence")
		return true
	}
	return false
}

func refreshOfflineEffectProof(w http.ResponseWriter, r *http.Request, identities offlineEffectIdentityStore, prior etcdstore.OfflineRunnerProof) (etcdstore.OfflineRunnerProof, bool) {
	proof, ok := offlineEffectGrantProof(w, r, identities)
	if !ok {
		return etcdstore.OfflineRunnerProof{}, false
	}
	if prior.Subject != "" && (proof.Subject != prior.Subject || proof.TokenID != prior.TokenID || proof.DeviceID != prior.DeviceID) {
		handler.WriteControlProblem(w, r, http.StatusPreconditionFailed, "offline_runner_changed", "offline runner identity changed during independent observation")
		return etcdstore.OfflineRunnerProof{}, false
	}
	return proof, true
}

func writeOfflineEffectV2(w http.ResponseWriter, status int, field string, value any, authorizes bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{field: value, "authorizesEffect": authorizes})
}

func offlineEffectV2Error(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusConflict, "offline_effect_v2_conflict"
	if errors.Is(err, etcdstore.ErrNotFound) {
		status, code = http.StatusNotFound, "offline_effect_v2_not_found"
	}
	if !errors.Is(err, offline.ErrEffectProofV2) && !errors.Is(err, offline.ErrDeleteCommitmentV2) &&
		!errors.Is(err, offline.ErrTerminalProofV2) && !errors.Is(err, etcdstore.ErrOfflineEffectV2Conflict) && !errors.Is(err, etcdstore.ErrNotFound) {
		status, code = http.StatusServiceUnavailable, "offline_effect_v2_uncertain"
	}
	handler.WriteControlProblem(w, r, status, code, "offline effect v2 transition failed; use nonauthorizing GET readback")
}

func etcdOfflineEffectV2Issue(store offlineEffectV2Store, identities offlineEffectIdentityStore, source OfflineEffectEvidenceSourceV2) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, _, ok := offlineEffectGrantPath(w, r, false)
		if !ok || !offlineEffectEmptyRequest(w, r) || offlineEffectV2Unavailable(w, r, store, source, true) {
			return
		}
		prior, ok := refreshOfflineEffectProof(w, r, identities, etcdstore.OfflineRunnerProof{})
		if !ok {
			return
		}
		observation, err := source.ObserveOfflineEffect(r.Context(), planID, attemptID)
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		proof, ok := refreshOfflineEffectProof(w, r, identities, prior)
		if !ok {
			return
		}
		grant, err := store.IssueOfflineEffectGrantV2(r.Context(), planID, attemptID, proof, observation, time.Now().UTC())
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		writeOfflineEffectV2(w, http.StatusCreated, "grant", grant, false)
	}
}

type offlineDeleteCommitmentRequestV2 struct {
	CommitmentSHA256    string `json:"commitmentSHA256"`
	DeleteRequestSHA256 string `json:"deleteRequestSHA256"`
}

func decodeOfflineEffectV2Body(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || offlineReservationUniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_effect_v2_request", "offline effect v2 request is not exact")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_effect_v2_request", "offline effect v2 request is not exact")
		return false
	}
	return true
}

func etcdOfflineEffectV2Commit(store offlineEffectV2Store, identities offlineEffectIdentityStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, grantID, ok := offlineEffectGrantPath(w, r, true)
		if !ok {
			return
		}
		var request offlineDeleteCommitmentRequestV2
		if !decodeOfflineEffectV2Body(w, r, &request) || offlineEffectV2Unavailable(w, r, store, nil, false) {
			return
		}
		proof, ok := refreshOfflineEffectProof(w, r, identities, etcdstore.OfflineRunnerProof{})
		if !ok {
			return
		}
		grant, err := store.CommitOfflineDeleteV2(r.Context(), planID, attemptID, grantID, proof, request.CommitmentSHA256, request.DeleteRequestSHA256, time.Now().UTC())
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		writeOfflineEffectV2(w, http.StatusCreated, "commitment", grant.Commitment, false)
	}
}

func etcdOfflineEffectV2Dispatch(store offlineEffectV2Store, identities offlineEffectIdentityStore, source OfflineEffectEvidenceSourceV2) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, grantID, ok := offlineEffectGrantPath(w, r, true)
		if !ok || !offlineEffectEmptyRequest(w, r) || offlineEffectV2Unavailable(w, r, store, source, true) {
			return
		}
		prior, ok := refreshOfflineEffectProof(w, r, identities, etcdstore.OfflineRunnerProof{})
		if !ok {
			return
		}
		observation, err := source.ObserveOfflineEffect(r.Context(), planID, attemptID)
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		proof, ok := refreshOfflineEffectProof(w, r, identities, prior)
		if !ok {
			return
		}
		dispatch, err := store.DispatchOfflineDeleteV2(r.Context(), planID, attemptID, grantID, proof, observation, time.Now().UTC())
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		// This response is the only authorizing envelope. The durable record and
		// every GET remain nonauthorizing, so a lost response cannot be replayed.
		writeOfflineEffectV2(w, http.StatusCreated, "reservation", dispatch, true)
	}
}

func etcdOfflineEffectV2Terminal(store offlineEffectV2Store, identities offlineEffectIdentityStore, source OfflineEffectEvidenceSourceV2) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, grantID, ok := offlineEffectGrantPath(w, r, true)
		if !ok || !offlineEffectEmptyRequest(w, r) || offlineEffectV2Unavailable(w, r, store, source, true) {
			return
		}
		prior, ok := refreshOfflineEffectProof(w, r, identities, etcdstore.OfflineRunnerProof{})
		if !ok {
			return
		}
		observation, err := source.ObserveOfflineTerminal(r.Context(), planID, attemptID, grantID)
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		proof, ok := refreshOfflineEffectProof(w, r, identities, prior)
		if !ok {
			return
		}
		terminal, err := store.RecordOfflineTerminalV2(r.Context(), planID, attemptID, grantID, proof, observation, time.Now().UTC())
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		writeOfflineEffectV2(w, http.StatusCreated, "terminalEvidence", terminal, false)
	}
}

func etcdOfflineEffectV2Read(store offlineEffectV2Store, identities offlineEffectIdentityStore, field string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, attemptID, _, ok := offlineEffectGrantPath(w, r, false)
		if !ok || offlineEffectV2Unavailable(w, r, store, nil, false) {
			return
		}
		proof, ok := refreshOfflineEffectProof(w, r, identities, etcdstore.OfflineRunnerProof{})
		if !ok {
			return
		}
		grant, err := store.RecoverOfflineEffectGrantV2(r.Context(), planID, attemptID, proof, time.Now().UTC())
		if err != nil {
			offlineEffectV2Error(w, r, err)
			return
		}
		var value any = grant
		switch field {
		case "commitment":
			if grant.Commitment == nil {
				handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_effect_v2_not_found", "offline effect v2 evidence not found")
				return
			}
			value = grant.Commitment
		case "dispatch":
			if grant.Dispatch == nil {
				handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_effect_v2_not_found", "offline effect v2 evidence not found")
				return
			}
			value = grant.Dispatch
		case "terminal":
			if grant.Terminal == nil {
				handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_effect_v2_not_found", "offline effect v2 evidence not found")
				return
			}
			value = grant.Terminal
		}
		name := field
		if field == "dispatch" {
			name = "reservation"
		}
		if field == "terminal" {
			name = "terminalEvidence"
		}
		writeOfflineEffectV2(w, http.StatusOK, name, value, false)
	}
}
