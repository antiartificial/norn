package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const etcdFleetReleaseHTTPEnv = "NORN_ETCD_FLEET_RELEASE_HTTP"
const etcdFleetReleaseBodyLimit = 12 << 20

type etcdFleetReleaseRequest struct {
	SourceSHA string                 `json:"sourceSha"`
	Artifact  string                 `json:"artifact"`
	Candidate model.ReleaseCandidate `json:"candidate"`
}

func etcdFleetReleaseDeployment(cfg *config.Config, operations *etcdstore.V3OperationStore,
	verifier *pipeline.Pipeline) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if cfg == nil || operations == nil || verifier == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "release_admission_unavailable", "Fleet release admission is unavailable")
			return
		}
		app := chi.URLParam(r, "id")
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || principal.CI == nil || !etcdFleetName(app) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "release_token_binding_invalid", "a valid app-bound release identity is required")
			return
		}
		var request etcdFleetReleaseRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, etcdFleetReleaseBodyLimit))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_request", "release request must contain one bounded JSON object")
			return
		}
		candidate, err := handler.BindFleetStagingReleaseCandidate(principal, app, cfg.GitHubActionsDefaultBranch,
			request.SourceSHA, request.Artifact, cfg.ReleaseAttestationTrustMode, request.Candidate)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "release_token_binding_invalid", err.Error())
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key is required")
			return
		}
		authority, err := operations.Authority(r.Context())
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "authority_unavailable", "control authority is unavailable")
			return
		}
		identity := store.OperationRequestIdentity{Authority: authority,
			Actor: store.OperationActor{Issuer: "https://token.actions.githubusercontent.com", Subject: fmt.Sprintf("%s:%s:%s", principal.CI.RepositoryID, principal.CI.RunID, principal.CI.RunAttempt)},
			Kind:  "app.deploy", Resource: "app/" + app, Key: key}
		prior, err := operations.ResolveIdentity(r.Context(), identity)
		if err == nil {
			if !fleetReleaseReplayMatches(prior, request.SourceSHA, request.Artifact, candidate) {
				handler.WriteControlProblem(w, r, http.StatusConflict, "idempotency_conflict", "Idempotency-Key belongs to another release request")
				return
			}
			w.Header().Set("Location", "/api/v1/operations/"+prior.Operation.ID)
			writeEtcdSourceJSON(w, http.StatusOK, prior.Operation)
			return
		}
		if !errors.Is(err, store.ErrAcceptanceNotFound) {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "release_replay_unavailable", "release acceptance replay could not be verified")
			return
		}
		spec, err := fleetReleaseSpec(cfg.AppsDir, app)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "app_source_unavailable", "a unique enabled app source is required")
			return
		}
		target, _, err := operations.CurrentFleetAppTarget(r.Context(), app, "staging")
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "fleet_app_target_unavailable", "the configured Fleet app target is unavailable")
			return
		}
		var catalog store.DatabaseCatalogRevision
		if spec.NamedDatabases() {
			catalog, err = operations.ActiveDatabaseCatalog(r.Context())
			if err != nil {
				handler.WriteControlProblem(w, r, http.StatusConflict, "database_catalog_unavailable", "an active database catalog is required")
				return
			}
		}
		acceptance, err := buildEtcdFleetReleaseAcceptance(r.Context(), fleetReleaseAdmissionInputs{
			Authority: authority, Actor: identity.Actor, IdempotencyKey: key,
			Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID,
				Source: "release-control-api", Scopes: principal.Scopes},
			Spec: spec, Target: target, DatabaseProfile: cfg.DatabaseProfile, DatabaseCatalog: catalog,
			RegistryURL: cfg.RegistryURL, SourceSHA: request.SourceSHA, Artifact: request.Artifact, Candidate: candidate,
		}, verifier.VerifyReleaseArtifact)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "release_admission_failed", "Fleet release source, placement, database, or artifact verification failed")
			return
		}
		accepted, err := operations.AcceptFleetReleaseDeployment(r.Context(), acceptance)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "release_acceptance_failed", "Fleet release acceptance was not committed")
			return
		}
		w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
		status := http.StatusAccepted
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, accepted.Operation)
	}
}

func fleetReleaseSpec(appsDir, app string) (*model.InfraSpec, error) {
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return nil, err
	}
	var selected *model.InfraSpec
	for _, spec := range specs {
		if spec.App == app {
			if selected != nil {
				return nil, fmt.Errorf("duplicate enabled app source")
			}
			selected = spec
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("enabled app source is unavailable")
	}
	return selected, nil
}

func fleetReleaseReplayMatches(prior store.AcceptedOperation, sourceSHA, artifact string, candidate model.ReleaseCandidate) bool {
	if prior.Operation.Kind != "app.deploy" || prior.Deployment == nil || prior.Deployment.CommitSHA != sourceSHA || prior.Deployment.ImageTag != artifact {
		return false
	}
	encoded, err := json.Marshal(prior.Operation.Payload["candidate"])
	if err != nil {
		return false
	}
	var stored model.ReleaseCandidate
	return json.Unmarshal(encoded, &stored) == nil && reflect.DeepEqual(stored, candidate)
}

func fleetReleaseOperationReadable(cfg config.Config, principal handler.AccessPrincipal, op model.Operation) bool {
	if op.Kind != "app.deploy" || op.App == "" || op.Payload == nil {
		return false
	}
	sha, _ := op.Payload["sourceSha"].(string)
	artifact, _ := op.Payload["artifact"].(string)
	encoded, err := json.Marshal(op.Payload["candidate"])
	if err != nil {
		return false
	}
	var stored model.ReleaseCandidate
	if json.Unmarshal(encoded, &stored) != nil {
		return false
	}
	derived, err := handler.BindFleetStagingReleaseCandidate(principal, op.App, cfg.GitHubActionsDefaultBranch,
		sha, artifact, cfg.ReleaseAttestationTrustMode, stored)
	return err == nil && reflect.DeepEqual(derived, stored)
}
