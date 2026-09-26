package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

const (
	nomadNoDatabaseBindingSchema = "norn.database-targets/none/v1"
)

// bindDeploymentJobProvenance stamps the control-plane identities that a
// later destructive maintenance operation must match to this exact Nomad job
// revision. The database digest covers the exact string retained in the signed
// operation payload rather than a reconstruction from mutable catalog state.
func bindDeploymentJobProvenance(job *nomadapi.Job, deploymentID, specDigest string, payload map[string]interface{}) error {
	if job == nil || deploymentID == "" || specDigest == "" {
		return fmt.Errorf("Nomad deployment provenance is incomplete")
	}
	schema := nomadNoDatabaseBindingSchema
	material := schema
	revision := int64(0)

	named, err := recordedTargetSetFromPayload(payload)
	if err != nil {
		return err
	}
	legacy, err := recordedTargetFromPayload(payload)
	if err != nil {
		return err
	}
	if named != nil && legacy != nil {
		return fmt.Errorf("Nomad deployment provenance has conflicting database bindings")
	}
	if named != nil {
		encoded, ok := payload[databaseTargetsPayloadKey].(string)
		if !ok || named.CatalogRevision <= 0 {
			return fmt.Errorf("Nomad deployment provenance has an invalid named database binding")
		}
		schema, material, revision = recordedTargetSetSchema, encoded, named.CatalogRevision
	} else if legacy != nil {
		encoded, ok := payload[databaseTargetPayloadKey].(string)
		if !ok || legacy.CatalogRevision <= 0 {
			return fmt.Errorf("Nomad deployment provenance has an invalid legacy database binding")
		}
		schema, material, revision = recordedTargetSchema, encoded, legacy.CatalogRevision
	}

	digest := sha256.Sum256([]byte(schema + "\x00" + material))
	return nomad.BindDeploymentProvenance(job, nomad.DeploymentProvenance{
		DeploymentID: deploymentID, SpecDigest: specDigest, DatabaseBindingSchema: schema,
		DatabaseBindingSHA256: hex.EncodeToString(digest[:]), DatabaseCatalogRevision: strconv.FormatInt(revision, 10),
	})
}
