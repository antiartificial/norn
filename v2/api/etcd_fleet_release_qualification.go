package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const etcdFleetQualificationHTTPEnv = "NORN_ETCD_FLEET_QUALIFICATION_HTTP"

type etcdFleetQualificationRequest struct {
	DeploymentID string `json:"deploymentId"`
}

type etcdFleetQualificationStore interface {
	store.OperationStore
	store.OperationIdentityResolver
	ResolveAcceptedOperationByID(context.Context, string) (store.AcceptedOperation, error)
	GetDeployment(context.Context, string) (*model.Deployment, error)
}

// etcdFleetReleaseQualification issues evidence only from an accepted and
// successful V3 staging release. Caller input selects a deployment ID; source,
// artifact, and candidate provenance come from the signed acceptance.
func etcdFleetReleaseQualification(cfg *config.Config, operations etcdFleetQualificationStore,
	identities store.IdentityStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if cfg == nil || cfg.EnvironmentID() != "staging" || operations == nil || identities == nil ||
			handler.ValidateReleaseQualificationSigningKey(cfg.QualificationSigningKey) != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "qualification_issuer_unavailable", "V3 staging qualification issuer is unavailable")
			return
		}
		app := chi.URLParam(r, "id")
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || !etcdFleetName(app) || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.TokenID == "" ||
			principal.CI == nil && !principal.Legacy && !principal.Allows(handler.ScopeAPIWrite) && !principal.Allows(handler.ScopeAdmin) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "qualification_identity_invalid", "a managed release qualification identity is required")
			return
		}
		if principal.CI != nil && (principal.App != app || principal.Environment != "staging" || principal.CI.Environment != "staging") {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "qualification_identity_invalid", "qualification identity is not bound to this staging app")
			return
		}
		var request etcdFleetQualificationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_qualification_request", "qualification request must contain one deploymentId")
			return
		}
		request.DeploymentID = strings.TrimSpace(request.DeploymentID)
		if uuid.Validate(request.DeploymentID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_qualification_request", "deploymentId must be a UUID")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 512 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "idempotency_key_required", "a bounded Idempotency-Key is required")
			return
		}
		identity, err := etcdFleetQualificationIdentity(r.Context(), cfg, operations, identities, principal, app, key)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "qualification_identity_unavailable", "stable qualification actor identity is unavailable")
			return
		}
		receipt, replayed, err := issueEtcdFleetReleaseQualification(r.Context(), cfg, operations, identity, principal, app, request.DeploymentID)
		if err != nil {
			status, code := http.StatusConflict, "qualification_issue_failed"
			if errors.Is(err, store.ErrAcceptanceNotFound) {
				status, code = http.StatusNotFound, "deployment_not_qualifiable"
			} else if errors.Is(err, store.ErrAcceptanceIndeterminate) {
				status, code = http.StatusServiceUnavailable, "qualification_issue_indeterminate"
			}
			handler.WriteControlProblem(w, r, status, code, "V3 staging deployment could not be qualified")
			return
		}
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
		}
		writeEtcdSourceJSON(w, status, receipt)
	}
}

func etcdFleetQualificationIdentity(ctx context.Context, cfg *config.Config, operations etcdFleetQualificationStore,
	identities store.IdentityStore, principal handler.AccessPrincipal, app, key string) (store.OperationRequestIdentity, error) {
	authority, err := operations.Authority(ctx)
	if err != nil || authority == "" || cfg.EnvironmentID() != "staging" || app == "" || key == "" {
		return store.OperationRequestIdentity{}, fmt.Errorf("V3 qualification identity is incomplete")
	}
	actor := store.OperationActor{}
	if principal.CI != nil {
		ci := principal.CI
		if ci.Provider != "github-actions" || ci.RepositoryID == "" || ci.RunID == "" || ci.RunAttempt == "" {
			return store.OperationRequestIdentity{}, fmt.Errorf("verified GitHub Actions qualification identity is incomplete")
		}
		actor = store.OperationActor{Issuer: "https://token.actions.githubusercontent.com",
			Subject: fmt.Sprintf("%s:%s:%s", ci.RepositoryID, ci.RunID, ci.RunAttempt)}
	} else {
		root, err := identities.RootAccessToken(ctx, principal.TokenID)
		if err != nil || root == "" {
			return store.OperationRequestIdentity{}, fmt.Errorf("managed token lineage is unavailable")
		}
		actor = store.OperationActor{Issuer: "norn://managed-token", Subject: root}
	}
	return store.OperationRequestIdentity{Authority: authority, Actor: actor, Kind: "release.qualification", Resource: "app/" + app, Key: key}, nil
}

func issueEtcdFleetReleaseQualification(ctx context.Context, cfg *config.Config, operations etcdFleetQualificationStore,
	identity store.OperationRequestIdentity, principal handler.AccessPrincipal, app, deploymentID string) (model.ReleaseQualification, bool, error) {
	if cfg == nil || cfg.EnvironmentID() != "staging" || operations == nil || identity.Kind != "release.qualification" ||
		identity.Resource != "app/"+app || identity.Key == "" || uuid.Validate(deploymentID) != nil {
		return model.ReleaseQualification{}, false, fmt.Errorf("V3 qualification request is incomplete")
	}
	if prior, err := operations.ResolveIdentity(ctx, identity); err == nil {
		if !acceptedQualificationMatchesDeployment(prior.Operation, app, deploymentID) {
			return model.ReleaseQualification{}, false, &store.AcceptanceConflictError{Identity: identity}
		}
		receipt, ok := qualificationFromAcceptedOperation(prior.Operation)
		if !ok {
			return model.ReleaseQualification{}, false, fmt.Errorf("accepted V3 qualification is malformed")
		}
		return receipt, true, nil
	} else if !errors.Is(err, store.ErrAcceptanceNotFound) {
		return model.ReleaseQualification{}, false, err
	}
	acceptedDeployment, err := operations.ResolveAcceptedOperationByID(ctx, deploymentID)
	if err != nil {
		return model.ReleaseQualification{}, false, err
	}
	op := acceptedDeployment.Operation
	if op.ID == "" || op.ID != deploymentID || op.Kind != "app.deploy" || op.Status != model.OperationSucceeded || op.App != app || op.Source != "release-control-api" {
		return model.ReleaseQualification{}, false, fmt.Errorf("V3 deployment acceptance is not a successful staging release")
	}
	deployment, err := operations.GetDeployment(ctx, deploymentID)
	if err != nil || deployment == nil || deployment.ID != deploymentID || deployment.App != app ||
		deployment.Status != model.StatusDeployed || deployment.Environment != "staging" || deployment.SourceKind != "release" ||
		!fleetReleaseSHA.MatchString(deployment.CommitSHA) || !model.IsContentAddressedImage(deployment.ImageTag) ||
		!fleetReleaseSpecDigest.MatchString(deployment.SpecDigest) || len(deployment.Regions) == 0 {
		return model.ReleaseQualification{}, false, fmt.Errorf("V3 deployment is not a completed immutable staging release")
	}
	for _, region := range deployment.Regions {
		if region.Status != model.StatusDeployed || region.ActiveWeight != region.DesiredWeight {
			return model.ReleaseQualification{}, false, fmt.Errorf("V3 staging deployment has an incomplete region")
		}
	}
	if op.Payload["deploymentId"] != deployment.ID || op.Payload["sourceSha"] != deployment.CommitSHA ||
		op.Payload["artifact"] != deployment.ImageTag || op.Payload["specDigest"] != deployment.SpecDigest {
		return model.ReleaseQualification{}, false, fmt.Errorf("signed V3 release provenance differs from the completed deployment")
	}
	var candidate model.ReleaseCandidate
	candidateBytes, err := json.Marshal(op.Payload["candidate"])
	if err != nil || json.Unmarshal(candidateBytes, &candidate) != nil || candidate.Provider == "" || candidate.RepositoryID == "" ||
		candidate.Attestation.MaterialSHA != deployment.CommitSHA || candidate.Attestation.SubjectDigest != releaseArtifactDigest(deployment.ImageTag) {
		return model.ReleaseQualification{}, false, fmt.Errorf("signed V3 release candidate provenance is incomplete")
	}
	if permitted, reason := handler.QualificationIntentPermitsCandidate(principal, candidate); !permitted {
		return model.ReleaseQualification{}, false, fmt.Errorf("qualification identity does not match the staged candidate: %s", reason)
	}
	issuedAt := time.Now().UTC().Truncate(time.Microsecond)
	receipt := model.ReleaseQualification{SchemaVersion: model.ReleaseQualificationSchema, ID: uuid.NewString(), App: app,
		Environment: "staging", DeploymentID: deploymentID, SourceSHA: deployment.CommitSHA, Artifact: deployment.ImageTag,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(7 * 24 * time.Hour), Candidate: candidate}
	if err := handler.SignReleaseQualification(cfg.QualificationSigningKey, &receipt); err != nil {
		return model.ReleaseQualification{}, false, err
	}
	verificationKeys, err := handler.ReleaseQualificationVerificationKeys(cfg.TrustedQualificationSigningKeys, cfg.QualificationSigningKey)
	if err != nil || model.VerifyReleaseQualificationSignature(verificationKeys, receipt) != nil {
		return model.ReleaseQualification{}, false, fmt.Errorf("V3 qualification signature failed staging self-verification")
	}
	payloadBytes, err := json.Marshal(receipt)
	if err != nil {
		return model.ReleaseQualification{}, false, err
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return model.ReleaseQualification{}, false, err
	}
	finished := issuedAt
	operation := model.Operation{ID: receipt.ID, Kind: "release.qualification", App: app, Ref: receipt.SourceSHA,
		Status: model.OperationSucceeded, Risk: "staging release evidence", Source: "release-control-api",
		Message: "V3 staging deployment qualified", Payload: payload,
		Metadata:  map[string]interface{}{"deploymentId": deploymentID, "environment": "staging"},
		StartedAt: issuedAt, FinishedAt: &finished, MaxAttempts: 1}
	acceptance := store.OperationAcceptance{Identity: identity, Operation: operation,
		Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID,
			Source: "etcd-v3-release-qualification", Scopes: append([]string(nil), principal.Scopes...)},
		Semantics: map[string]interface{}{"action": "release.qualification", "app": app, "deploymentId": deploymentID}}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return model.ReleaseQualification{}, false, err
	}
	accepted, err := operations.Accept(ctx, acceptance)
	if err == nil {
		receipt, ok := qualificationFromAcceptedOperation(accepted.Operation)
		if !ok {
			return model.ReleaseQualification{}, false, fmt.Errorf("accepted V3 qualification is malformed")
		}
		return receipt, accepted.Replayed, nil
	}
	// A concurrent exact request can sign a different issue timestamp before
	// one request wins. Re-read the identity and return only the same deployment's
	// durable first signer output.
	prior, resolveErr := operations.ResolveIdentity(ctx, identity)
	if resolveErr == nil && acceptedQualificationMatchesDeployment(prior.Operation, app, deploymentID) {
		if receipt, ok := qualificationFromAcceptedOperation(prior.Operation); ok {
			return receipt, true, nil
		}
	}
	if resolveErr != nil && !errors.Is(resolveErr, store.ErrAcceptanceNotFound) {
		return model.ReleaseQualification{}, false, &store.AcceptanceIndeterminateError{Err: errors.Join(err, resolveErr)}
	}
	return model.ReleaseQualification{}, false, err
}

func acceptedQualificationMatchesDeployment(operation model.Operation, app, deploymentID string) bool {
	return operation.Kind == "release.qualification" && operation.Status == model.OperationSucceeded && operation.App == app &&
		operation.Metadata["deploymentId"] == deploymentID
}

func qualificationFromAcceptedOperation(operation model.Operation) (model.ReleaseQualification, bool) {
	if operation.Kind != "release.qualification" || operation.Status != model.OperationSucceeded || operation.Payload == nil {
		return model.ReleaseQualification{}, false
	}
	encoded, err := json.Marshal(operation.Payload)
	var receipt model.ReleaseQualification
	if err != nil || json.Unmarshal(encoded, &receipt) != nil || receipt.SchemaVersion != model.ReleaseQualificationSchema ||
		receipt.ID != operation.ID || receipt.App != operation.App || receipt.DeploymentID != operation.Metadata["deploymentId"] ||
		receipt.Environment != "staging" {
		return model.ReleaseQualification{}, false
	}
	return receipt, true
}

func releaseArtifactDigest(artifact string) string {
	index := strings.LastIndex(artifact, "@sha256:")
	if index < 0 {
		return ""
	}
	return artifact[index+1:]
}
