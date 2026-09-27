package nomad

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

const (
	DeploymentIDMeta            = "norn_deployment_id"
	SpecDigestMeta              = "norn_spec_digest"
	DatabaseBindingSchemaMeta   = "norn_database_binding_schema"
	DatabaseBindingSHA256Meta   = "norn_database_binding_sha256"
	DatabaseCatalogRevisionMeta = "norn_database_catalog_revision"
)

// DeploymentProvenance is immutable control-plane identity stamped into a
// Nomad job revision. Maintenance code can reobserve these values immediately
// before a guarded job mutation without depending on a mutable local spec.
type DeploymentProvenance struct {
	DeploymentID            string
	SpecDigest              string
	DatabaseBindingSchema   string
	DatabaseBindingSHA256   string
	DatabaseCatalogRevision string
}

func BindDeploymentProvenance(job *nomadapi.Job, provenance DeploymentProvenance) error {
	if job == nil || strings.TrimSpace(provenance.DeploymentID) == "" || strings.TrimSpace(provenance.SpecDigest) == "" ||
		strings.TrimSpace(provenance.DatabaseBindingSchema) == "" || len(provenance.DatabaseBindingSHA256) != 64 || strings.TrimSpace(provenance.DatabaseCatalogRevision) == "" {
		return fmt.Errorf("Nomad deployment provenance is incomplete")
	}
	if !strings.HasPrefix(provenance.SpecDigest, "sha256:") || len(provenance.SpecDigest) != 71 {
		return fmt.Errorf("Nomad deployment spec digest is invalid")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(provenance.SpecDigest, "sha256:")); err != nil {
		return fmt.Errorf("Nomad deployment spec digest is invalid")
	}
	if _, err := hex.DecodeString(provenance.DatabaseBindingSHA256); err != nil || strings.ToLower(provenance.DatabaseBindingSHA256) != provenance.DatabaseBindingSHA256 {
		return fmt.Errorf("Nomad database binding digest is invalid")
	}
	revision, err := strconv.ParseInt(provenance.DatabaseCatalogRevision, 10, 64)
	if err != nil || revision < 0 || strconv.FormatInt(revision, 10) != provenance.DatabaseCatalogRevision {
		return fmt.Errorf("Nomad database catalog revision is invalid")
	}
	values := map[string]string{
		DeploymentIDMeta:            provenance.DeploymentID,
		SpecDigestMeta:              provenance.SpecDigest,
		DatabaseBindingSchemaMeta:   provenance.DatabaseBindingSchema,
		DatabaseBindingSHA256Meta:   provenance.DatabaseBindingSHA256,
		DatabaseCatalogRevisionMeta: provenance.DatabaseCatalogRevision,
	}
	if job.Meta == nil {
		job.Meta = make(map[string]string, len(values))
	}
	for key, value := range values {
		if existing, exists := job.Meta[key]; exists && existing != value {
			return fmt.Errorf("Nomad deployment provenance metadata %s conflicts", key)
		}
		job.Meta[key] = value
	}
	return nil
}
