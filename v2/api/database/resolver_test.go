package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const secretCanary = "NORN_DB_SECRET_CANARY_7f3a"

func testCatalog() Catalog {
	pg := func(id, provider string, purpose ServicePurpose) DatabaseService {
		return DatabaseService{
			APIVersion: APIVersion, ID: id, Generation: 3, Purpose: purpose, Engine: EnginePostgreSQL, EngineVersion: "16.4",
			ProviderRef: provider, Endpoint: DatabaseEndpoint{Host: "/var/run/" + id, Port: 5432}, Topology: DatabaseTopology{Mode: TopologyLocalShared, AvailabilityClass: AvailabilitySingleHost},
			TLS:      DatabaseTLSPolicy{MinimumMode: TLSDisabled},
			Recovery: RecoveryPolicy{Capabilities: []Capability{CapabilityRuntime, CapabilityMigration, CapabilitySnapshot, CapabilityRestore, CapabilityHealth}},
		}
	}
	managed := DatabaseService{
		APIVersion: APIVersion, ID: "fleet-pg", Generation: 7, Purpose: PurposeApplication, Engine: EnginePostgreSQL, EngineVersion: "16",
		ProviderRef: "do:databases/fleet-pg", Endpoint: DatabaseEndpoint{Host: "db.internal.example", Port: 25060}, Topology: DatabaseTopology{Mode: TopologyManaged, AvailabilityClass: AvailabilityManaged},
		TLS:      DatabaseTLSPolicy{MinimumMode: TLSVerifyFull, RequireClientCertificate: true},
		Recovery: RecoveryPolicy{Capabilities: []Capability{CapabilityRuntime, CapabilityRestore, CapabilityManagedReadiness, CapabilityPITR}},
	}
	mysql := DatabaseService{
		APIVersion: APIVersion, ID: "wp-mysql", Generation: 1, Purpose: PurposeApplication, Engine: EngineMySQL, EngineVersion: "8.0",
		ProviderRef: "local:mini-mysql", Endpoint: DatabaseEndpoint{Host: "127.0.0.1", Port: 3306}, Topology: DatabaseTopology{Mode: TopologyLocalShared, AvailabilityClass: AvailabilitySingleHost},
		TLS:      DatabaseTLSPolicy{MinimumMode: TLSDisabled},
		Recovery: RecoveryPolicy{Capabilities: []Capability{CapabilityRuntime, CapabilitySnapshot, CapabilityRestore}},
	}
	return Catalog{
		APIVersion: APIVersion,
		Services: []DatabaseService{
			pg("mini-app-pg", "local:mini-postgres", PurposeApplication),
			pg("mini-control-pg", "local:mini-postgres", PurposeControl),
			pg("other-pg", "local:other-postgres", PurposeApplication),
			managed, mysql,
		},
		Bindings: []DatabaseBinding{
			{APIVersion: APIVersion, ID: "shop-primary", ServiceID: "mini-app-pg", Database: "shop", Role: "shop_app", Generation: 2, CredentialRef: "secret:apps/shop/primary", TLS: DatabaseTLS{Mode: TLSDisabled}},
			{APIVersion: APIVersion, ID: "shop-reporting", ServiceID: "other-pg", Database: "shop", Role: "shop_app", Generation: 5, CredentialRef: "secret:apps/shop/reporting", TLS: DatabaseTLS{Mode: TLSDisabled}},
			{APIVersion: APIVersion, ID: "norn-control", ServiceID: "mini-control-pg", Database: "norn_v2", Role: "norn", Generation: 1, CredentialRef: "secret:norn/control", TLS: DatabaseTLS{Mode: TLSDisabled}},
			{APIVersion: APIVersion, ID: "fleet-app", ServiceID: "fleet-pg", Database: "app", Role: "app", Generation: 4, CredentialRef: "secret:fleet/app",
				TLS: DatabaseTLS{Mode: TLSVerifyFull, ServerName: "db.internal.example", CARef: "secret:fleet/ca", ClientCertRef: "secret:fleet/client-cert", ClientKeyRef: "secret:fleet/client-key"}},
			{APIVersion: APIVersion, ID: "wordpress", ServiceID: "wp-mysql", Database: "wordpress", Role: "wp", Generation: 1, CredentialRef: "secret:apps/wp", TLS: DatabaseTLS{Mode: TLSDisabled}},
		},
		Profiles: []DeploymentProfile{
			{
				APIVersion: APIVersion, ID: "mini", Topology: DeploymentTopologyLocal, AvailabilityClass: AvailabilitySingleHost,
				DatabaseBindings: map[string]string{"shop-db": "shop-primary", "shop-reporting-db": "shop-reporting", "control": "norn-control", "wordpress-db": "wordpress"},
				LegacyPostgres:   &LegacyPostgresDefault{MappingID: "mini-legacy-pg", ServiceID: "mini-app-pg", Role: "legacy_apps", Generation: 1, CredentialRef: "secret:apps/legacy", TLS: DatabaseTLS{Mode: TLSDisabled}},
			},
			{
				APIVersion: APIVersion, ID: "fleet", Topology: DeploymentTopologyFleet, AvailabilityClass: AvailabilityMultiZone,
				DatabaseBindings: map[string]string{"app-db": "fleet-app"},
			},
		},
	}
}

func mustResolver(t *testing.T, catalog Catalog) *Resolver {
	t.Helper()
	resolver, err := NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func requireCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var resolverErr *ResolverError
	if !errors.As(err, &resolverErr) {
		t.Fatalf("error = %v, want resolver error %s", err, code)
	}
	if resolverErr.Code != code {
		t.Fatalf("error code = %s (%v), want %s", resolverErr.Code, err, code)
	}
}

func TestSameDatabaseNameOnTwoServicesResolvesDistinctTargets(t *testing.T) {
	resolver := mustResolver(t, testCatalog())
	primary, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-db"})
	if err != nil {
		t.Fatal(err)
	}
	reporting, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-reporting-db"})
	if err != nil {
		t.Fatal(err)
	}
	if primary.Target.Database != reporting.Target.Database || primary.Target.Role != reporting.Target.Role {
		t.Fatal("fixture must share database and role names")
	}
	if primary.Target.ServiceID != "mini-app-pg" || reporting.Target.ServiceID != "other-pg" || primary.Target == reporting.Target || primary.ProviderRef == reporting.ProviderRef {
		t.Fatalf("same-named databases collapsed: %s / %s", primary, reporting)
	}
	// Every consumer asking for one logical resource must get the same target,
	// and one service's identity can never authorize the other.
	for _, capability := range []Capability{CapabilityRuntime, CapabilityMigration, CapabilitySnapshot, CapabilityHealth} {
		again, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-db", RequiredCapabilities: []Capability{capability}, Expected: &primary.Target})
		if err != nil || again.Target != primary.Target {
			t.Fatalf("%s consumer resolved %v, %v", capability, again.Target, err)
		}
	}
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-reporting-db", Expected: &primary.Target, RequiredCapabilities: []Capability{CapabilityRestore}})
	requireCode(t, err, CodeStaleTarget)
}

func TestMiniExplicitLegacyResolution(t *testing.T) {
	resolver := mustResolver(t, testCatalog())
	legacy, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "blog"}})
	if err != nil {
		t.Fatal(err)
	}
	want := TargetIdentity{ServiceID: "mini-app-pg", ServiceGeneration: 3, BindingID: "mini-legacy-pg", BindingGeneration: 1, Engine: EnginePostgreSQL, Database: "blog", Role: "legacy_apps"}
	if legacy.Target != want || !legacy.Legacy || legacy.LogicalResourceID != "" || legacy.CredentialRef != "secret:apps/legacy" {
		t.Fatalf("legacy target = %#v", legacy.Target)
	}
	if !legacy.Inspection().Legacy {
		t.Fatal("legacy resolution is not visible in inspection")
	}

	// A profile without an explicit legacy default never guesses one.
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "blog"}})
	requireCode(t, err, CodeResolutionNotFound)
	// A legacy database that is also a named binding on the legacy service is ambiguous.
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "shop"}})
	requireCode(t, err, CodeAmbiguousLegacy)
	// Named plus legacy in one request is ambiguous; missing named never falls back.
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-db", LegacyPostgres: &LegacyPostgresDeclaration{Database: "blog"}})
	requireCode(t, err, CodeAmbiguousLegacy)
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "blog-db"})
	requireCode(t, err, CodeResolutionNotFound)
	// The shared physical server's control database is never an app default.
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "norn_v2"}})
	requireCode(t, err, CodePurposeMismatch)
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "../etc"}})
	requireCode(t, err, CodeInvalidRequest)
}

func TestExpectedIdentityRejectsEveryStaleField(t *testing.T) {
	resolver := mustResolver(t, testCatalog())
	current, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if err != nil {
		t.Fatal(err)
	}
	stale := map[string]func(*TargetIdentity){
		"service":            func(value *TargetIdentity) { value.ServiceID = "other-pg" },
		"service generation": func(value *TargetIdentity) { value.ServiceGeneration-- },
		"binding":            func(value *TargetIdentity) { value.BindingID = "shop-primary" },
		"binding generation": func(value *TargetIdentity) { value.BindingGeneration-- },
		"future generation":  func(value *TargetIdentity) { value.BindingGeneration++ },
		"engine":             func(value *TargetIdentity) { value.Engine = EngineMySQL },
		"database":           func(value *TargetIdentity) { value.Database = "app2" },
		"role":               func(value *TargetIdentity) { value.Role = "admin" },
	}
	for name, mutate := range stale {
		t.Run(name, func(t *testing.T) {
			expected := current.Target
			mutate(&expected)
			_, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db", Expected: &expected})
			requireCode(t, err, CodeStaleTarget)
		})
	}
	if _, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db", Expected: &current.Target, RequiredCapabilities: []Capability{CapabilityRestore}}); err != nil {
		t.Fatalf("exact expected identity rejected: %v", err)
	}
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db", RequiredCapabilities: []Capability{CapabilityRestore}})
	requireCode(t, err, CodeInvalidRequest)
}

func TestApplicationAndControlResolutionAreSeparate(t *testing.T) {
	resolver := mustResolver(t, testCatalog())
	_, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "control"})
	requireCode(t, err, CodePurposeMismatch)
	control, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeControl, LogicalResourceID: "control"})
	if err != nil || control.Purpose != PurposeControl {
		t.Fatalf("control resolution = %v, %v", control, err)
	}
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeControl, LogicalResourceID: "shop-db"})
	requireCode(t, err, CodePurposeMismatch)
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", LogicalResourceID: "shop-db"})
	requireCode(t, err, CodeUnsupportedPurpose)

	for name, mutate := range map[string]func(*Catalog){
		"shared credential": func(c *Catalog) { c.Bindings[0].CredentialRef = "secret:norn/control" },
		"shared role":       func(c *Catalog) { c.Bindings[0].Role = "norn" },
		"shared database":   func(c *Catalog) { c.Bindings[0].Database = "norn_v2" },
		"legacy credential": func(c *Catalog) { c.Profiles[0].LegacyPostgres.CredentialRef = "secret:norn/control" },
		"legacy role":       func(c *Catalog) { c.Profiles[0].LegacyPostgres.Role = "norn" },
		"legacy control":    func(c *Catalog) { c.Profiles[0].LegacyPostgres.ServiceID = "mini-control-pg" },
	} {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalog()
			mutate(&catalog)
			requireCode(t, ValidateCatalog(catalog), CodePurposeMismatch)
		})
	}
	// Separate providers may reuse database and role names.
	catalog := testCatalog()
	catalog.Bindings[1].Role = "norn"
	catalog.Bindings[1].Database = "norn_v2"
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("separate provider rejected: %v", err)
	}
}

func TestEngineCapabilityAndPurposeSupport(t *testing.T) {
	resolver := mustResolver(t, testCatalog())
	if _, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "wordpress-db", RequiredCapabilities: []Capability{CapabilitySnapshot}}); err != nil {
		t.Fatalf("mysql snapshot capability rejected: %v", err)
	}
	_, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "wordpress-db", RequiredCapabilities: []Capability{CapabilityPITR}})
	requireCode(t, err, CodeUnsupportedCapability)
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "wordpress-db", RequiredCapabilities: []Capability{CapabilityMigration}})
	requireCode(t, err, CodeUnsupportedCapability) // supported by adapter, not declared by service
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-db", RequiredCapabilities: []Capability{"logical-replication"}})
	requireCode(t, err, CodeUnsupportedCapability)

	for name, test := range map[string]struct {
		mutate func(*Catalog)
		code   ErrorCode
	}{
		"cockroach":           {func(c *Catalog) { c.Services[0].Engine = "cockroachdb" }, CodeUnsupportedEngine},
		"postgres alias":      {func(c *Catalog) { c.Services[0].Engine = "postgres" }, CodeUnsupportedEngine},
		"mariadb":             {func(c *Catalog) { c.Services[4].Engine = "mariadb" }, CodeUnsupportedEngine},
		"mysql pitr":          {func(c *Catalog) { c.Services[4].Recovery.Capabilities = []Capability{CapabilityPITR} }, CodeUnsupportedCapability},
		"mysql managed":       {func(c *Catalog) { c.Services[4].Recovery.Capabilities = []Capability{CapabilityManagedReadiness} }, CodeUnsupportedCapability},
		"unknown capability":  {func(c *Catalog) { c.Services[0].Recovery.Capabilities = []Capability{"backup"} }, CodeUnsupportedCapability},
		"local managed ready": {func(c *Catalog) { c.Services[0].Recovery.Capabilities = []Capability{CapabilityManagedReadiness} }, CodeUnsupportedCapability},
		"duplicate capability": {func(c *Catalog) {
			c.Services[0].Recovery.Capabilities = []Capability{CapabilityRuntime, CapabilityRuntime}
		}, CodeInvalidCatalog},
		"unknown purpose":       {func(c *Catalog) { c.Services[0].Purpose = "analytics" }, CodeUnsupportedPurpose},
		"empty purpose":         {func(c *Catalog) { c.Services[0].Purpose = "" }, CodeUnsupportedPurpose},
		"mysql control":         {func(c *Catalog) { c.Services[4].Purpose = PurposeControl }, CodeUnsupportedEngine},
		"legacy mysql":          {func(c *Catalog) { c.Profiles[0].LegacyPostgres.ServiceID = "wp-mysql" }, CodeUnsupportedEngine},
		"mysql dotted database": {func(c *Catalog) { c.Bindings[4].Database = "word.press" }, CodeInvalidCatalog},
		"local multi-zone":      {func(c *Catalog) { c.Services[0].Topology.AvailabilityClass = AvailabilityMultiZone }, CodeInvalidCatalog},
	} {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalog()
			test.mutate(&catalog)
			requireCode(t, ValidateCatalog(catalog), test.code)
		})
	}
}

func TestCatalogRejectsMissingReferencesAndDefaults(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*Catalog)
		code   ErrorCode
	}{
		"binding service":      {func(c *Catalog) { c.Bindings[0].ServiceID = "absent" }, CodeResolutionNotFound},
		"profile binding":      {func(c *Catalog) { c.Profiles[1].DatabaseBindings["app-db"] = "absent" }, CodeResolutionNotFound},
		"legacy service":       {func(c *Catalog) { c.Profiles[0].LegacyPostgres.ServiceID = "absent" }, CodeResolutionNotFound},
		"credential ref":       {func(c *Catalog) { c.Bindings[0].CredentialRef = "" }, CodeInvalidCatalog},
		"legacy credential":    {func(c *Catalog) { c.Profiles[0].LegacyPostgres.CredentialRef = "" }, CodeInvalidCatalog},
		"legacy generation":    {func(c *Catalog) { c.Profiles[0].LegacyPostgres.Generation = 0 }, CodeInvalidCatalog},
		"legacy mapping id":    {func(c *Catalog) { c.Profiles[0].LegacyPostgres.MappingID = "shop-primary" }, CodeInvalidCatalog},
		"binding generation":   {func(c *Catalog) { c.Bindings[0].Generation = 0 }, CodeInvalidCatalog},
		"service generation":   {func(c *Catalog) { c.Services[0].Generation = 0 }, CodeInvalidCatalog},
		"provider ref":         {func(c *Catalog) { c.Services[0].ProviderRef = "" }, CodeInvalidCatalog},
		"duplicate service":    {func(c *Catalog) { c.Services[1].ID = c.Services[0].ID }, CodeInvalidCatalog},
		"duplicate binding":    {func(c *Catalog) { c.Bindings[1].ID = c.Bindings[0].ID }, CodeInvalidCatalog},
		"duplicate target":     {func(c *Catalog) { c.Bindings[1].ServiceID = c.Bindings[0].ServiceID }, CodeInvalidCatalog},
		"duplicate profile":    {func(c *Catalog) { c.Profiles[1].ID = c.Profiles[0].ID }, CodeInvalidCatalog},
		"api version":          {func(c *Catalog) { c.Bindings[0].APIVersion = "norn.database/v2" }, CodeInvalidCatalog},
		"security profile":     {func(c *Catalog) { c.Profiles[0].Topology = "production" }, CodeInvalidCatalog},
		"local ha claim":       {func(c *Catalog) { c.Profiles[0].AvailabilityClass = AvailabilityMultiZone }, CodeInvalidCatalog},
		"fleet single host":    {func(c *Catalog) { c.Profiles[1].AvailabilityClass = AvailabilitySingleHost }, CodeInvalidCatalog},
		"invalid logical name": {func(c *Catalog) { c.Profiles[1].DatabaseBindings["App DB"] = "fleet-app" }, CodeInvalidCatalog},
	} {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalog()
			test.mutate(&catalog)
			requireCode(t, ValidateCatalog(catalog), test.code)
		})
	}
	resolver := mustResolver(t, testCatalog())
	_, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "absent", Purpose: PurposeApplication, LogicalResourceID: "shop-db"})
	requireCode(t, err, CodeResolutionNotFound)
	_, err = resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication})
	requireCode(t, err, CodeInvalidRequest)
}

func TestTLSPolicyVersusClientVerification(t *testing.T) {
	resolver := mustResolver(t, testCatalog())
	managed, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if err != nil {
		t.Fatal(err)
	}
	inspection := managed.Inspection().TLSRequirements
	if !inspection.ClientCertificateRequired || !inspection.ClientCertificateConfigured || !inspection.ServerNameRequired || inspection.MinimumMode != TLSVerifyFull {
		t.Fatalf("managed TLS inspection = %#v", inspection)
	}

	// A binding may carry client certificate material the service does not
	// require; inspection reports policy, not the presence of material.
	catalog := testCatalog()
	catalog.Services[3].TLS.RequireClientCertificate = false
	optional, err := mustResolver(t, catalog).Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if err != nil {
		t.Fatal(err)
	}
	if got := optional.Inspection().TLSRequirements; got.ClientCertificateRequired || !got.ClientCertificateConfigured {
		t.Fatalf("optional client certificate inspection = %#v", got)
	}

	for name, mutate := range map[string]func(*Catalog){
		"weaker than policy":       func(c *Catalog) { c.Bindings[3].TLS.Mode = TLSVerifyCA; c.Bindings[3].TLS.ServerName = "" },
		"disabled under policy":    func(c *Catalog) { c.Bindings[3].TLS = DatabaseTLS{Mode: TLSDisabled} },
		"missing ca":               func(c *Catalog) { c.Bindings[3].TLS.CARef = "" },
		"missing server name":      func(c *Catalog) { c.Bindings[3].TLS.ServerName = "" },
		"missing required cert":    func(c *Catalog) { c.Bindings[3].TLS.ClientCertRef, c.Bindings[3].TLS.ClientKeyRef = "", "" },
		"cert without key":         func(c *Catalog) { c.Bindings[3].TLS.ClientKeyRef = "" },
		"disabled carrying refs":   func(c *Catalog) { c.Bindings[0].TLS.CARef = "secret:ca" },
		"unknown mode":             func(c *Catalog) { c.Bindings[0].TLS.Mode = "require" },
		"client cert disabled min": func(c *Catalog) { c.Services[0].TLS.RequireClientCertificate = true },
		"verify-ca server name":    func(c *Catalog) { c.Services[0].TLS.MinimumMode = TLSVerifyCA },
	} {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalog()
			mutate(&catalog)
			requireCode(t, ValidateCatalog(catalog), CodeInvalidCatalog)
		})
	}
}

func TestInspectionAndErrorsRedactSecretCanaries(t *testing.T) {
	catalog := testCatalog()
	catalog.Bindings[3].CredentialRef = "secret:" + secretCanary + "/password"
	catalog.Bindings[3].TLS.CARef = "secret:" + secretCanary + "/ca"
	catalog.Bindings[3].TLS.ClientCertRef = "secret:" + secretCanary + "/cert"
	catalog.Bindings[3].TLS.ClientKeyRef = "secret:" + secretCanary + "/key"
	resolved, err := mustResolver(t, catalog).Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if err != nil {
		t.Fatal(err)
	}
	for name, rendered := range map[string]func() (string, error){
		"inspection json": func() (string, error) {
			encoded, err := json.Marshal(resolved.Inspection())
			return string(encoded), err
		},
		"resolved json": func() (string, error) { encoded, err := json.Marshal(resolved); return string(encoded), err },
		"resolved ptr":  func() (string, error) { encoded, err := json.Marshal(&resolved); return string(encoded), err },
		"fmt %v":        func() (string, error) { return fmt.Sprintf("%v", resolved), nil },
		"fmt %+v":       func() (string, error) { return fmt.Sprintf("%+v", resolved), nil },
		"fmt %#v":       func() (string, error) { return fmt.Sprintf("%#v", resolved), nil },
	} {
		text, err := rendered()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, secretCanary) {
			t.Fatalf("%s leaked secret reference: %s", name, text)
		}
	}
	encoded, _ := json.Marshal(resolved)
	if !strings.Contains(string(encoded), `"credentialPresent":true`) || !strings.Contains(string(encoded), `"bindingId":"fleet-app"`) {
		t.Fatalf("redacted projection lost public identity: %s", encoded)
	}

	dsn := "postgres://admin:" + secretCanary + "@db.example/app"
	for name, mutate := range map[string]func(*Catalog){
		"dsn credential ref": func(c *Catalog) { c.Bindings[0].CredentialRef = dsn },
		"dsn provider ref":   func(c *Catalog) { c.Services[0].ProviderRef = dsn },
		"dsn ca ref":         func(c *Catalog) { c.Bindings[3].TLS.CARef = dsn },
		"dsn binding id":     func(c *Catalog) { c.Bindings[0].ID = dsn },
		"dsn service id":     func(c *Catalog) { c.Services[0].ID = dsn },
		"dsn database":       func(c *Catalog) { c.Bindings[0].Database = dsn },
		"dsn role":           func(c *Catalog) { c.Bindings[0].Role = dsn },
		"dsn server name":    func(c *Catalog) { c.Bindings[3].TLS.ServerName = dsn },
		"dsn engine":         func(c *Catalog) { c.Services[0].Engine = Engine(dsn) },
		"dsn logical":        func(c *Catalog) { c.Profiles[1].DatabaseBindings[dsn] = "fleet-app" },
		"dsn legacy role":    func(c *Catalog) { c.Profiles[0].LegacyPostgres.Role = dsn },
	} {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalog()
			mutate(&catalog)
			err := ValidateCatalog(catalog)
			if err == nil {
				t.Fatal("DSN-shaped value accepted")
			}
			if strings.Contains(err.Error(), secretCanary) || strings.Contains(fmt.Sprintf("%#v", err), secretCanary) {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
	resolver := mustResolver(t, testCatalog())
	for _, request := range []ResolveRequest{
		{DeploymentProfileID: dsn, Purpose: PurposeApplication, LogicalResourceID: "shop-db"},
		{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: dsn},
		{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: dsn}},
		{DeploymentProfileID: "mini", Purpose: ServicePurpose(dsn), LogicalResourceID: "shop-db"},
		{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-db", RequiredCapabilities: []Capability{Capability(dsn)}},
	} {
		_, err := resolver.Resolve(request)
		if err == nil || strings.Contains(err.Error(), secretCanary) {
			t.Fatalf("resolve error leaked or accepted secret: %v", err)
		}
	}
}

func TestResolverIsIsolatedFromCallerCatalogMutation(t *testing.T) {
	catalog := testCatalog()
	resolver := mustResolver(t, catalog)
	catalog.Profiles[1].DatabaseBindings["app-db"] = "shop-primary"
	catalog.Services[3].Recovery.Capabilities[0] = CapabilityPITR
	catalog.Profiles[0].LegacyPostgres.Role = "changed"
	resolved, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if err != nil || resolved.Target.BindingID != "fleet-app" || resolved.Capabilities[0] != CapabilityRuntime {
		t.Fatalf("resolver observed caller mutation: %v %v", resolved, err)
	}
	resolved.Capabilities[0] = CapabilityPITR
	again, _ := resolver.Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if again.Capabilities[0] != CapabilityRuntime {
		t.Fatal("resolver returned shared capability storage")
	}
	legacy, _ := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "blog"}})
	if legacy.Target.Role != "legacy_apps" {
		t.Fatal("resolver observed caller legacy mutation")
	}
}

func TestValidateTransitionGenerations(t *testing.T) {
	type change struct {
		mutate func(*Catalog)
		code   ErrorCode // empty means the transition is accepted
	}
	bumpService := func(c *Catalog) { c.Services[3].Generation++ }
	bumpBinding := func(c *Catalog) { c.Bindings[3].Generation++ }
	for name, test := range map[string]change{
		"no change": {func(*Catalog) {}, ""},
		// Credential-only rotation keeps target identity.
		"rotate credential": {func(c *Catalog) { c.Bindings[3].CredentialRef = "secret:fleet/app-v2" }, ""},
		"rotate client certificate": {func(c *Catalog) {
			c.Bindings[3].TLS.ClientCertRef, c.Bindings[3].TLS.ClientKeyRef = "secret:fleet/cert-v2", "secret:fleet/key-v2"
		}, ""},
		"rotate ca":                {func(c *Catalog) { c.Bindings[3].TLS.CARef = "secret:fleet/ca-v2" }, ""},
		"rotate legacy credential": {func(c *Catalog) { c.Profiles[0].LegacyPostgres.CredentialRef = "secret:apps/legacy-v2" }, ""},
		"provider without bump":    {func(c *Catalog) { c.Services[3].ProviderRef = "do:databases/fleet-pg-2" }, CodeUnsafeTransition},
		"provider with bump":       {func(c *Catalog) { c.Services[3].ProviderRef = "do:databases/fleet-pg-2"; bumpService(c) }, ""},
		"endpoint without bump":    {func(c *Catalog) { c.Services[3].Endpoint.Host = "db2.internal.example" }, CodeUnsafeTransition},
		"endpoint port no bump":    {func(c *Catalog) { c.Services[3].Endpoint.Port = 25061 }, CodeUnsafeTransition},
		"endpoint with bump":       {func(c *Catalog) { c.Services[3].Endpoint.Port = 25061; bumpService(c) }, ""},
		"version without bump":     {func(c *Catalog) { c.Services[3].EngineVersion = "17" }, CodeUnsafeTransition},
		"version with bump":        {func(c *Catalog) { c.Services[3].EngineVersion = "17"; bumpService(c) }, ""},
		"topology without bump":    {func(c *Catalog) { c.Services[3].Topology.AvailabilityClass = AvailabilityMultiZone }, CodeUnsafeTransition},
		"tls policy without bump":  {func(c *Catalog) { c.Services[3].TLS.RequireClientCertificate = false }, CodeUnsafeTransition},
		"tls policy with bump":     {func(c *Catalog) { c.Services[3].TLS.RequireClientCertificate = false; bumpService(c) }, ""},
		"capability without bump":  {func(c *Catalog) { c.Services[3].Recovery.Capabilities = c.Services[3].Recovery.Capabilities[:1] }, CodeUnsafeTransition},
		"capability reorder":       {func(c *Catalog) { caps := c.Services[3].Recovery.Capabilities; caps[0], caps[1] = caps[1], caps[0] }, ""},
		"engine without bump": {func(c *Catalog) {
			c.Services[2].Engine = EngineMySQL
			c.Bindings[1].Database = "shop"
			c.Bindings[1].Role = "shop"
		}, CodeUnsafeTransition},
		"engine with bump": {func(c *Catalog) {
			c.Services[2].Engine = EngineMySQL
			c.Services[2].Generation++
			c.Bindings[1].Role = "shop"
			c.Bindings[1].Generation++
		}, ""},
		"service generation down": {func(c *Catalog) { c.Services[3].Generation-- }, CodeUnsafeTransition},
		"purpose change":          {func(c *Catalog) { c.Services[2].Purpose = PurposeControl; bumpService(c) }, CodeUnsafeTransition},
		"binding service no bump": {func(c *Catalog) {
			c.Bindings[0].ServiceID = "fleet-pg"
			c.Bindings[0].TLS = c.Bindings[3].TLS
			c.Bindings[0].Database = "shop2"
		}, CodeUnsafeTransition},
		"binding database no bump": {func(c *Catalog) { c.Bindings[3].Database = "app2" }, CodeUnsafeTransition},
		"binding database bump":    {func(c *Catalog) { c.Bindings[3].Database = "app2"; bumpBinding(c) }, ""},
		"binding role no bump":     {func(c *Catalog) { c.Bindings[3].Role = "app2" }, CodeUnsafeTransition},
		"binding server name":      {func(c *Catalog) { c.Bindings[3].TLS.ServerName = "db2.internal.example" }, CodeUnsafeTransition},
		"binding server name bump": {func(c *Catalog) { c.Bindings[3].TLS.ServerName = "db2.internal.example"; bumpBinding(c) }, ""},
		"binding generation down":  {func(c *Catalog) { c.Bindings[3].Generation-- }, CodeUnsafeTransition},
		"legacy role no bump":      {func(c *Catalog) { c.Profiles[0].LegacyPostgres.Role = "legacy2" }, CodeUnsafeTransition},
		"legacy role bump": {func(c *Catalog) {
			c.Profiles[0].LegacyPostgres.Role = "legacy2"
			c.Profiles[0].LegacyPostgres.Generation++
		}, ""},
		"legacy service no bump": {func(c *Catalog) { c.Profiles[0].LegacyPostgres.ServiceID = "other-pg" }, CodeUnsafeTransition},
		"legacy mapping renamed": {func(c *Catalog) {
			c.Profiles[0].LegacyPostgres.MappingID = "legacy-2"
			c.Profiles[0].LegacyPostgres.Generation++
		}, CodeUnsafeTransition},
		"logical re-pointed":         {func(c *Catalog) { c.Profiles[0].DatabaseBindings["shop-db"] = "shop-reporting" }, CodeUnsafeTransition},
		"referenced binding removed": {func(c *Catalog) { c.Bindings = c.Bindings[:4]; delete(c.Profiles[0].DatabaseBindings, "wordpress-db") }, CodeUnsafeTransition},
		"referenced service removed": {func(c *Catalog) {
			c.Services = c.Services[:4]
			c.Bindings = c.Bindings[:4]
			delete(c.Profiles[0].DatabaseBindings, "wordpress-db")
		}, CodeUnsafeTransition},
		"mapping removed first": {func(c *Catalog) { delete(c.Profiles[0].DatabaseBindings, "wordpress-db") }, ""},
		"invalid next catalog":  {func(c *Catalog) { c.Bindings[0].CredentialRef = "" }, CodeInvalidCatalog},
	} {
		t.Run(name, func(t *testing.T) {
			next := cloneCatalog(testCatalog())
			test.mutate(&next)
			err := ValidateTransition(testCatalog(), next)
			if test.code == "" {
				if err != nil {
					t.Fatalf("transition rejected: %v", err)
				}
				return
			}
			requireCode(t, err, test.code)
		})
	}

	// Two-phase removal: once nothing references the binding/service, removal
	// is safe if the removed ID is recorded as a permanent tombstone.
	unreferenced := testCatalog()
	delete(unreferenced.Profiles[0].DatabaseBindings, "wordpress-db")
	removed := cloneCatalog(unreferenced)
	removed.Bindings = removed.Bindings[:4]
	requireCode(t, ValidateTransition(unreferenced, removed), CodeUnsafeTransition)
	removed.Retired = []RetiredResource{{Kind: RetiredBinding, ID: "wordpress"}}
	if err := ValidateTransition(unreferenced, removed); err != nil {
		t.Fatalf("unreferenced binding removal rejected: %v", err)
	}
	serviceRemoved := cloneCatalog(removed)
	serviceRemoved.Services = serviceRemoved.Services[:4]
	requireCode(t, ValidateTransition(removed, serviceRemoved), CodeUnsafeTransition)
	serviceRemoved.Retired = append(serviceRemoved.Retired, RetiredResource{Kind: RetiredService, ID: "wp-mysql"})
	if err := ValidateTransition(removed, serviceRemoved); err != nil {
		t.Fatalf("unreferenced service removal rejected: %v", err)
	}
	forgotten := cloneCatalog(serviceRemoved)
	forgotten.Retired = forgotten.Retired[1:]
	requireCode(t, ValidateTransition(serviceRemoved, forgotten), CodeUnsafeTransition)

	// Retired IDs can never come back, so their generation sequence cannot
	// restart under a different provider.
	recreated := cloneCatalog(serviceRemoved)
	recreated.Services = append(recreated.Services, testCatalog().Services[4])
	recreated.Services[4].ProviderRef = "local:different-mysql-server"
	requireCode(t, ValidateCatalog(recreated), CodeInvalidCatalog)
	rebound := cloneCatalog(removed)
	rebound.Bindings = append(rebound.Bindings, testCatalog().Bindings[4])
	requireCode(t, ValidateCatalog(rebound), CodeInvalidCatalog)
	reusedAsLegacy := cloneCatalog(removed)
	reusedAsLegacy.Profiles[0].LegacyPostgres.MappingID = "wordpress"
	requireCode(t, ValidateCatalog(reusedAsLegacy), CodeInvalidCatalog)

	// Removing a legacy default retires its mapping ID as a binding identity.
	withoutLegacy := testCatalog()
	withoutLegacy.Profiles[0].LegacyPostgres = nil
	requireCode(t, ValidateTransition(testCatalog(), withoutLegacy), CodeUnsafeTransition)
	withoutLegacy.Retired = []RetiredResource{{Kind: RetiredBinding, ID: "mini-legacy-pg"}}
	if err := ValidateTransition(testCatalog(), withoutLegacy); err != nil {
		t.Fatalf("retired legacy default rejected: %v", err)
	}
	for name, retired := range map[string][]RetiredResource{
		"unknown kind": {{Kind: "profile", ID: "mini"}},
		"invalid id":   {{Kind: RetiredService, ID: "postgres://x:" + secretCanary + "@y"}},
		"duplicate":    {{Kind: RetiredService, ID: "gone"}, {Kind: RetiredService, ID: "gone"}},
	} {
		catalog := testCatalog()
		catalog.Retired = retired
		err := ValidateCatalog(catalog)
		requireCode(t, err, CodeInvalidCatalog)
		if strings.Contains(err.Error(), secretCanary) {
			t.Fatalf("%s: retired error leaked value: %v", name, err)
		}
	}
}

func TestLegacyMappingIdentityIsGlobalAcrossProfiles(t *testing.T) {
	shared := testCatalog()
	legacy := *shared.Profiles[0].LegacyPostgres
	shared.Profiles[1].LegacyPostgres = &legacy
	if err := ValidateCatalog(shared); err != nil {
		t.Fatalf("identical shared legacy mapping rejected: %v", err)
	}
	conflicting := cloneCatalog(shared)
	conflicting.Profiles[1].LegacyPostgres.Role = "other_legacy"
	requireCode(t, ValidateCatalog(conflicting), CodeInvalidCatalog)

	// Moving a mapping to a renamed profile keeps its identity rules.
	moved := testCatalog()
	moved.Profiles[0].ID = "renamed-mini"
	moved.Profiles[0].LegacyPostgres.Role = "legacy_moved"
	requireCode(t, ValidateTransition(testCatalog(), moved), CodeUnsafeTransition)
	moved.Profiles[0].LegacyPostgres.Generation++
	if err := ValidateTransition(testCatalog(), moved); err != nil {
		t.Fatalf("bumped moved legacy mapping rejected: %v", err)
	}
	downgraded := cloneCatalog(moved)
	downgraded.Profiles[0].LegacyPostgres.Generation = 1
	downgraded.Profiles[0].LegacyPostgres.Role = "legacy_apps"
	requireCode(t, ValidateTransition(moved, downgraded), CodeUnsafeTransition)
}

func TestTransitionBumpFencesQueuedWork(t *testing.T) {
	previous := testCatalog()
	accepted, err := mustResolver(t, previous).Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db"})
	if err != nil {
		t.Fatal(err)
	}
	rotated := cloneCatalog(previous)
	rotated.Bindings[3].CredentialRef = "secret:fleet/app-v2"
	if err := ValidateTransition(previous, rotated); err != nil {
		t.Fatal(err)
	}
	afterRotation, err := mustResolver(t, rotated).Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db", Expected: &accepted.Target})
	if err != nil || afterRotation.CredentialRef != "secret:fleet/app-v2" {
		t.Fatalf("credential rotation broke queued identity: %v", err)
	}
	cutover := cloneCatalog(rotated)
	cutover.Services[3].EngineVersion = "17"
	cutover.Services[3].Generation++
	if err := ValidateTransition(rotated, cutover); err != nil {
		t.Fatal(err)
	}
	_, err = mustResolver(t, cutover).Resolve(ResolveRequest{DeploymentProfileID: "fleet", Purpose: PurposeApplication, LogicalResourceID: "app-db", Expected: &accepted.Target})
	requireCode(t, err, CodeStaleTarget)
}
