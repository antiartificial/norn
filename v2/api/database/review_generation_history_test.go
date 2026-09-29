package database

import "testing"

// Accepted queued work must not acquire a different provider target after
// resource removal and recreation, even across individually valid transitions.
func TestReviewRetirementCannotResetTargetGeneration(t *testing.T) {
	original := testCatalog()
	request := ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "wordpress-db"}
	accepted, err := mustResolver(t, original).Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	unmapped := cloneCatalog(original)
	delete(unmapped.Profiles[0].DatabaseBindings, "wordpress-db")
	unbound := cloneCatalog(unmapped)
	unbound.Bindings = unbound.Bindings[:4]
	removed := cloneCatalog(unbound)
	removed.Services = removed.Services[:4]
	recreated := cloneCatalog(original)
	recreated.Services[4].ProviderRef = "local:different-mysql-server"
	previous := original
	for _, next := range []Catalog{unmapped, unbound, removed, recreated} {
		if err := ValidateTransition(previous, next); err != nil {
			return // Fail-closed retirement preserves the queued-work boundary.
		}
		previous = next
	}
	request.Expected = &accepted.Target
	resolved, err := mustResolver(t, recreated).Resolve(request)
	if err == nil && resolved.ProviderRef != accepted.ProviderRef {
		t.Fatal("all retirement/recreation transitions accepted and stale Expected identity resolved to a different provider")
	}
}

func TestReviewLegacyIdentityGenerationSurvivesProfileMove(t *testing.T) {
	original := testCatalog()
	moved := cloneCatalog(original)
	moved.Profiles[0].ID = "renamed-mini"
	moved.Profiles[0].LegacyPostgres.TLS = DatabaseTLS{
		Mode: TLSVerifyFull, ServerName: "different.internal.example", CARef: "secret:legacy/ca",
	}
	if err := ValidateTransition(original, moved); err == nil {
		t.Fatal("moving the same legacy mapping ID to another profile bypassed its unchanged-generation TLS target check")
	}
}
