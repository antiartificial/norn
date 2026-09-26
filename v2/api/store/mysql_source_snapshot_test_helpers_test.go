package store

import (
	"strconv"
	"strings"
)

func validSourceSnapshotJobIdentity(app string, catalogRevision int64, modifyIndex string, allocationIDs ...string) MySQLSourceSnapshotJobIdentity {
	return MySQLSourceSnapshotJobIdentity{
		App: app, DeploymentID: "deployment-1", SpecDigest: "sha256:" + strings.Repeat("a", 64), Region: "west", NomadRegion: "global",
		JobID: app, JobVersion: "1", JobModifyIndex: modifyIndex, AllocationIDs: allocationIDs,
		DatabaseBindingSchema: mysqlDeployedDatabaseTargetsSchema, DatabaseBindingSHA256: strings.Repeat("b", 64), DatabaseCatalogRevision: strconv.FormatInt(catalogRevision, 10),
	}
}
