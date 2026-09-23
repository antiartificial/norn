package dbbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func testResolver(t *testing.T) *Resolver {
	t.Helper()
	secrets := map[string]Secret{
		"secret/control": {Username: "norn", Password: "s3cr3t-control"},
		"secret/wp":      {Username: "wp", Password: "s3cr3t-wp"},
	}
	r := NewResolver(func(ref string) (Secret, error) {
		s, ok := secrets[ref]
		if !ok {
			return Secret{}, fmt.Errorf("no such secret %q", ref)
		}
		return s, nil
	})
	if err := r.RegisterService(DatabaseService{ID: "pg-control", Purpose: PurposeControl, Engine: EnginePostgres, Host: "db.internal", Port: 5432, TLS: true, Generation: 3}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterService(DatabaseService{ID: "mysql-wp", Purpose: PurposeApplication, Engine: EngineMySQL, Host: "wpdb.internal", Port: 3306}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBinding(DatabaseBinding{ID: "control", ServiceID: "pg-control", Database: "norn", Role: "norn", SecretRef: "secret/control", ConsistencyGroup: "control"}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBinding(DatabaseBinding{ID: "wordpress", ServiceID: "mysql-wp", Database: "wp", Role: "wp", SecretRef: "secret/wp"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveBuildsEngineSpecificDSN(t *testing.T) {
	r := testResolver(t)
	pg, err := r.Resolve("control")
	if err != nil {
		t.Fatal(err)
	}
	if pg.Engine != EnginePostgres || pg.Generation != 3 {
		t.Fatalf("unexpected resolution: %+v", pg)
	}
	if !strings.HasPrefix(pg.DSN, "postgres://norn:s3cr3t-control@db.internal:5432/norn") || !strings.Contains(pg.DSN, "sslmode=require") {
		t.Fatalf("postgres DSN wrong: %s", pg.DSN)
	}
	my, err := r.Resolve("wordpress")
	if err != nil {
		t.Fatal(err)
	}
	if my.DSN != "wp:s3cr3t-wp@tcp(wpdb.internal:3306)/wp?tls=false" {
		t.Fatalf("mysql DSN wrong: %s", my.DSN)
	}
}

func TestRedactedNeverLeaksCredentials(t *testing.T) {
	r := testResolver(t)
	red, err := r.Redacted("control")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(red)
	if strings.Contains(string(raw), "s3cr3t") || strings.Contains(string(raw), "secret/control") {
		t.Fatalf("redacted binding leaked credentials or secret ref: %s", raw)
	}
	if red.Engine != EnginePostgres || red.Database != "norn" || red.Generation != 3 {
		t.Fatalf("redacted view wrong: %+v", red)
	}
	all, err := r.RedactedAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].BindingID != "control" || all[1].BindingID != "wordpress" {
		t.Fatalf("redacted-all wrong: %+v", all)
	}
}

func TestGenerationFencing(t *testing.T) {
	r := testResolver(t)
	if _, err := r.ResolveAtGeneration("control", 3); err != nil {
		t.Fatalf("current generation should resolve: %v", err)
	}
	_, err := r.ResolveAtGeneration("control", 2)
	var stale *ErrStaleGeneration
	if !errors.As(err, &stale) {
		t.Fatalf("stale generation should be rejected, got %v", err)
	}
	if stale.Current != 3 || stale.Expected != 2 {
		t.Fatalf("stale error detail wrong: %+v", stale)
	}
}

func TestRegistrationValidation(t *testing.T) {
	r := NewResolver(func(string) (Secret, error) { return Secret{}, nil })
	if err := r.RegisterService(DatabaseService{ID: "x", Purpose: PurposeControl, Engine: "cockroach"}); err == nil {
		t.Fatal("unsupported engine should be rejected")
	}
	if err := r.RegisterService(DatabaseService{ID: "x", Purpose: "weird", Engine: EnginePostgres}); err == nil {
		t.Fatal("unsupported purpose should be rejected")
	}
	if err := r.RegisterBinding(DatabaseBinding{ID: "b", ServiceID: "missing", Database: "d", SecretRef: "s"}); err == nil {
		t.Fatal("binding to unknown service should be rejected")
	}
	_ = r.RegisterService(DatabaseService{ID: "svc", Purpose: PurposeControl, Engine: EnginePostgres})
	if err := r.RegisterBinding(DatabaseBinding{ID: "b", ServiceID: "svc", Database: "d", SecretRef: ""}); err == nil {
		t.Fatal("binding without a secret reference should be rejected")
	}
	if _, err := r.Resolve("nope"); err == nil {
		t.Fatal("resolving an unknown binding should error")
	}
}
