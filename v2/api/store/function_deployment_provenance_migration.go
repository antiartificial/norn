package store

// A deployment writer must record the spec used to produce its image before
// function invocation can bind that image to a mutable on-disk spec.
const FunctionDeploymentProvenanceWriterVersion int64 = 19

func functionDeploymentProvenanceMigration() SchemaMigration {
	return SchemaMigration{
		Version:              22,
		Name:                 "function-deployment-spec-provenance",
		SQL:                  `ALTER TABLE deployments ADD COLUMN spec_digest TEXT NOT NULL DEFAULT '';`,
		MinimumReaderVersion: FunctionInvocationArchiveReaderVersion,
		MinimumWriterVersion: FunctionDeploymentProvenanceWriterVersion,
	}
}
