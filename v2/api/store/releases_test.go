package store

import (
	"strings"
	"testing"
)

func TestLatestSuccessfulPromotedDeploymentQueryRequiresSameLaneAndQualification(t *testing.T) {
	query := latestSuccessfulPromotedDeploymentQuery()
	for _, required := range []string{
		"d.environment=$2",
		"d.id != $3",
		"o.kind='app.deploy' AND o.status='succeeded'",
		"o.payload->>'deploymentId'=d.id",
		"o.metadata ? 'promotionQualification'",
		"o.metadata->'promotionQualification'->>'schemaVersion'='norn.release-qualification/v2'",
		"COALESCE(o.metadata->'promotionQualification'->>'signature', '') <> ''",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("production rollback target query lacks %q:\n%s", required, query)
		}
	}
}

func TestLatestSuccessfulPromotedDeploymentQueryExcludesLegacyDeployments(t *testing.T) {
	query := latestSuccessfulPromotedDeploymentQuery()
	if !strings.Contains(query, "AND EXISTS (") {
		t.Fatalf("query must require a promotion operation rather than selecting every deployed row:\n%s", query)
	}
	if strings.Contains(query, "WHERE d.app=$1 AND d.status='deployed' ORDER BY") {
		t.Fatalf("query regressed to an unqualified latest-deployment lookup:\n%s", query)
	}
}
