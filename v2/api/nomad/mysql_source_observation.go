package nomad

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

var ErrMySQLSourceJobObservation = errors.New("exact MySQL source job observation is unavailable")

type MySQLSourceJobObservationRequest struct {
	App                     string
	DeploymentID            string
	SpecDigest              string
	Region                  string
	NomadRegion             string
	DatabaseBindingSchema   string
	DatabaseBindingSHA256   string
	DatabaseCatalogRevision string
}

type MySQLSourceJobObservation struct {
	App                     string
	DeploymentID            string
	SpecDigest              string
	Region                  string
	NomadRegion             string
	JobID                   string
	JobVersion              string
	JobModifyIndex          string
	AllocationIDs           []string
	DatabaseBindingSchema   string
	DatabaseBindingSHA256   string
	DatabaseCatalogRevision string
}

// ObserveMySQLSourceJob derives the exact running Nomad revision from server
// state and verifies that its immutable metadata matches signed deployment
// provenance. Callers must sign the returned identity before any mutation.
func (c *Client) ObserveMySQLSourceJob(ctx context.Context, request MySQLSourceJobObservationRequest) (MySQLSourceJobObservation, error) {
	if c == nil || c.api == nil || !validMySQLSourceObservationRequest(request) {
		return MySQLSourceJobObservation{}, ErrMySQLSourceJobObservation
	}
	job, _, err := c.api.Jobs().Info(request.App, &nomadapi.QueryOptions{Region: request.NomadRegion})
	if err != nil {
		return MySQLSourceJobObservation{}, err
	}
	if job == nil || job.ID == nil || *job.ID != request.App || job.Region == nil || *job.Region != request.NomadRegion || job.Stop == nil || *job.Stop || job.Version == nil || job.JobModifyIndex == nil || *job.JobModifyIndex == 0 || !exactMySQLSourceJobMeta(job.Meta, request) {
		return MySQLSourceJobObservation{}, ErrMySQLSourceJobObservation
	}
	allocations, _, err := c.api.Jobs().Allocations(request.App, true, &nomadapi.QueryOptions{Region: request.NomadRegion})
	if err != nil {
		return MySQLSourceJobObservation{}, err
	}
	ids, ok := exactLiveMySQLSourceAllocations(request.App, *job.Version, allocations)
	if !ok || len(ids) == 0 {
		return MySQLSourceJobObservation{}, ErrMySQLSourceJobObservation
	}
	return MySQLSourceJobObservation{
		App: request.App, DeploymentID: request.DeploymentID, SpecDigest: request.SpecDigest, Region: request.Region,
		NomadRegion: request.NomadRegion, JobID: request.App, JobVersion: strconv.FormatUint(*job.Version, 10),
		JobModifyIndex: strconv.FormatUint(*job.JobModifyIndex, 10), AllocationIDs: ids,
		DatabaseBindingSchema: request.DatabaseBindingSchema, DatabaseBindingSHA256: request.DatabaseBindingSHA256,
		DatabaseCatalogRevision: request.DatabaseCatalogRevision,
	}, nil
}

func validMySQLSourceObservationRequest(request MySQLSourceJobObservationRequest) bool {
	return strings.TrimSpace(request.App) != "" && strings.TrimSpace(request.DeploymentID) != "" &&
		strings.TrimSpace(request.SpecDigest) != "" && strings.TrimSpace(request.Region) != "" && strings.TrimSpace(request.NomadRegion) != "" &&
		strings.TrimSpace(request.DatabaseBindingSchema) != "" && strings.TrimSpace(request.DatabaseBindingSHA256) != "" &&
		strings.TrimSpace(request.DatabaseCatalogRevision) != ""
}

func exactMySQLSourceJobMeta(meta map[string]string, request MySQLSourceJobObservationRequest) bool {
	return meta[DeploymentIDMeta] == request.DeploymentID && meta[SpecDigestMeta] == request.SpecDigest &&
		meta[DatabaseBindingSchemaMeta] == request.DatabaseBindingSchema && meta[DatabaseBindingSHA256Meta] == request.DatabaseBindingSHA256 &&
		meta[DatabaseCatalogRevisionMeta] == request.DatabaseCatalogRevision
}

func exactLiveMySQLSourceAllocations(jobID string, version uint64, allocations []*nomadapi.AllocationListStub) ([]string, bool) {
	ids := make([]string, 0, len(allocations))
	seen := make(map[string]bool, len(allocations))
	for _, allocation := range allocations {
		if allocation == nil || allocation.ID == "" || allocation.JobID != jobID {
			return nil, false
		}
		live := allocation.DesiredStatus == nomadapi.AllocDesiredStatusRun &&
			(allocation.ClientStatus == nomadapi.AllocClientStatusPending || allocation.ClientStatus == nomadapi.AllocClientStatusRunning)
		if !live {
			continue
		}
		if allocation.JobVersion != version || seen[allocation.ID] {
			return nil, false
		}
		seen[allocation.ID] = true
		ids = append(ids, allocation.ID)
	}
	sort.Strings(ids)
	return ids, true
}
