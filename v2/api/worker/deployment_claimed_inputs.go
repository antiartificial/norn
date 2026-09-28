package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type ClaimedDeploymentVerifier interface {
	VerifyClaimedDeployment(context.Context, model.Operation) (store.AcceptedOperation, error)
}

type VerifiedManagedDeployment struct {
	Accepted        store.AcceptedOperation
	CatalogRevision int64
	ProfileID       string
	RuntimeTargets  map[string]database.TargetIdentity
}

// VerifyClaimedManagedDeployment gets its target identities only from the
// signed accepted operation. A caller may pass the result to private Nomad
// preparation after re-resolving and probing those targets at this revision.
func VerifyClaimedManagedDeployment(ctx context.Context, verifier ClaimedDeploymentVerifier, claimed model.Operation, spec *model.InfraSpec) (VerifiedManagedDeployment, error) {
	if verifier == nil || spec == nil || claimed.ID == "" || claimed.Kind != "app.deploy" || claimed.App != spec.App {
		return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment claim or source is invalid")
	}
	accepted, err := verifier.VerifyClaimedDeployment(ctx, claimed)
	if err != nil {
		return VerifiedManagedDeployment{}, err
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || accepted.Deployment == nil || accepted.Deployment.App != spec.App || accepted.Deployment.SpecDigest != digest ||
		accepted.Operation.ID != claimed.ID || accepted.Operation.App != claimed.App || accepted.Operation.Kind != claimed.Kind {
		return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment source differs from signed acceptance")
	}
	runtime := map[string]bool{}
	for _, requirement := range spec.Databases {
		if requirement.Runtime != nil {
			if requirement.Name == "" || runtime[requirement.Name] {
				return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment runtime database names are ambiguous")
			}
			runtime[requirement.Name] = true
		}
	}
	result := VerifiedManagedDeployment{Accepted: accepted, RuntimeTargets: map[string]database.TargetIdentity{}}
	if len(runtime) == 0 {
		return result, nil
	}
	raw, ok := accepted.Operation.Payload["databaseTargets"].(string)
	if !ok {
		return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment has no signed database targets")
	}
	var signed struct {
		Schema          string `json:"schema"`
		ProfileID       string `json:"profileId"`
		CatalogRevision int64  `json:"catalogRevision"`
		Targets         []struct {
			Name   string                  `json:"name"`
			Target database.TargetIdentity `json:"target"`
		} `json:"targets"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if decoder.Decode(&signed) != nil || decoder.Decode(&trailing) != io.EOF || signed.Schema != "norn.database-targets/v1" || signed.CatalogRevision < 1 || signed.ProfileID == "" {
		return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment signed database set is invalid")
	}
	seen := map[string]bool{}
	for _, entry := range signed.Targets {
		if entry.Name == "" || seen[entry.Name] {
			return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment signed database names are ambiguous")
		}
		seen[entry.Name] = true
		if runtime[entry.Name] {
			result.RuntimeTargets[entry.Name] = entry.Target
		}
	}
	if len(result.RuntimeTargets) != len(runtime) {
		return VerifiedManagedDeployment{}, fmt.Errorf("managed deployment signed runtime targets are incomplete")
	}
	result.CatalogRevision = signed.CatalogRevision
	result.ProfileID = signed.ProfileID
	return result, nil
}
