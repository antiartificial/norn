package database

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// The catalog example quoted in docs/v3/database-consumer-syntax.md is this
// fixture. It must decode strictly, validate, and resolve both the proposed
// named logical resource and the explicit legacy mapping.
func TestCatalogExampleDecodesStrictlyAndResolves(t *testing.T) {
	data, err := os.ReadFile("testdata/catalog-example.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("trailing content: %v", err)
	}
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(catalog)
	if err != nil {
		t.Fatal(err)
	}
	named, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "primary"})
	if err != nil || named.Target != (TargetIdentity{ServiceID: "mini-app-pg", ServiceGeneration: 3, BindingID: "shop-primary", BindingGeneration: 2, Engine: EnginePostgreSQL, Database: "shop", Role: "shop_app"}) {
		t.Fatalf("named = %+v, %v", named.Target, err)
	}
	legacy, err := resolver.Resolve(ResolveRequest{DeploymentProfileID: "mini", Purpose: PurposeApplication, LegacyPostgres: &LegacyPostgresDeclaration{Database: "orders"}})
	if err != nil || !legacy.Legacy || legacy.Target.BindingID != "mini-legacy-pg" || legacy.Target.Database != "orders" {
		t.Fatalf("legacy = %+v, %v", legacy.Target, err)
	}
}
