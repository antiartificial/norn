package database

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPostgresFenceCredentialsAreCatalogBound(t *testing.T) {
	base := testCatalog()
	base.Bindings[0].PostgresFence = &PostgresFenceCredentials{
		Generation: 1, Role: "shop_fence", CredentialRef: "secret:apps/shop/fence",
	}
	if err := ValidateCatalog(base); err != nil {
		t.Fatalf("valid PostgreSQL fence binding rejected: %v", err)
	}
	resolved, err := mustResolver(t, base).Resolve(ResolveRequest{
		DeploymentProfileID: "mini", Purpose: PurposeApplication, LogicalResourceID: "shop-db",
	})
	if err != nil || resolved.PostgresFence == nil || *resolved.PostgresFence != *base.Bindings[0].PostgresFence {
		t.Fatalf("fence identity was not resolved: %#v, %v", resolved.PostgresFence, err)
	}
	base.Bindings[0].PostgresFence.Role = "mutated"
	if resolved.PostgresFence.Role != "shop_fence" {
		t.Fatal("resolved fence identity aliases mutable catalog input")
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "shop_fence") || strings.Contains(string(encoded), "secret:apps/shop/fence") {
		t.Fatal("private fence identity leaked into binding inspection")
	}

	base.Bindings[0].PostgresFence.Role = "shop_fence"
	for name, mutate := range map[string]func(*Catalog){
		"zero generation": func(c *Catalog) { c.Bindings[0].PostgresFence.Generation = 0 },
		"runtime role":    func(c *Catalog) { c.Bindings[0].PostgresFence.Role = "shop_app" },
		"runtime credential": func(c *Catalog) {
			c.Bindings[0].PostgresFence.CredentialRef = c.Bindings[0].CredentialRef
		},
		"invalid role":      func(c *Catalog) { c.Bindings[0].PostgresFence.Role = "bad role" },
		"invalid reference": func(c *Catalog) { c.Bindings[0].PostgresFence.CredentialRef = "password" },
		"control binding": func(c *Catalog) {
			c.Bindings[2].PostgresFence = c.Bindings[0].PostgresFence
			c.Bindings[0].PostgresFence = nil
		},
		"mysql binding": func(c *Catalog) {
			c.Bindings[4].PostgresFence = c.Bindings[0].PostgresFence
			c.Bindings[0].PostgresFence = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneCatalog(base)
			mutate(&candidate)
			if err := ValidateCatalog(candidate); err == nil {
				t.Fatal("unsafe PostgreSQL fence identity accepted")
			}
		})
	}

	rotated := cloneCatalog(base)
	rotated.Bindings[0].PostgresFence.Role = "shop_fence_next"
	if err := ValidateTransition(base, rotated); err == nil {
		t.Fatal("fence identity changed without a binding generation bump")
	}
	rotated.Bindings[0].Generation++
	if err := ValidateTransition(base, rotated); err != nil {
		t.Fatalf("versioned fence identity change rejected: %v", err)
	}
}
