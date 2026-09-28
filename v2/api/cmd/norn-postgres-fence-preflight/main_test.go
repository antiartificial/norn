package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/database"
	"norn/v2/api/internal/pgtest"
)

func TestReadOnlyFencePreflightUsesPinnedCatalogAndExactTarget(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "source_db")
	server.CreatePasswordRole(t, "runtime_app", "fixture-runtime-password", "source_db")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, server.URL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var version int
	if err := admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 160000 {
		t.Skip("delegated fence preflight requires PostgreSQL 16 or later")
	}
	for _, statement := range []string{
		`CREATE ROLE fence_app LOGIN CREATEROLE`,
		`GRANT runtime_app TO fence_app WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`,
		`GRANT pg_signal_backend TO fence_app`,
		`GRANT pg_read_all_stats TO fence_app`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	runtimeConfig, err := pgx.ParseConfig(server.URL("source_db"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig.User, runtimeConfig.Password = "runtime_app", "fixture-runtime-password"
	runtime, err := pgx.ConnectConfig(ctx, runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	profile, logical := "mini", "app-db"
	catalog := database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{{APIVersion: database.APIVersion, ID: "source", Generation: 1,
			Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL, EngineVersion: "16",
			ProviderRef: "local:fixture", Endpoint: database.DatabaseEndpoint{Host: server.SocketDir, Port: server.Port},
			Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS:      database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled},
			Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilityRuntime}}}},
		Bindings: []database.DatabaseBinding{{APIVersion: database.APIVersion, ID: "source-app", ServiceID: "source", Database: "source_db", Role: "runtime_app", Generation: 1,
			CredentialRef: "secret:runtime", TLS: database.DatabaseTLS{Mode: database.TLSDisabled},
			PostgresFence: &database.PostgresFenceCredentials{Generation: 1, Role: "fence_app", CredentialRef: "secret:fence"}}},
		Profiles: []database.DeploymentProfile{{APIVersion: database.APIVersion, ID: profile,
			Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost,
			DatabaseBindings: map[string]string{logical: "source-app"}}},
	}
	target := database.TargetIdentity{ServiceID: "source", ServiceGeneration: 1, BindingID: "source-app", BindingGeneration: 1,
		Engine: database.EnginePostgreSQL, Database: "source_db", Role: "runtime_app"}
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"runtime": `{"password":"fixture-runtime-password"}`, "fence": `{"password":""}`} {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalogBytes, _ := json.Marshal(catalog)
	catalogPath := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalogPath, catalogBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	targetBytes, _ := json.Marshal(target)
	targetPath := filepath.Join(root, "target.json")
	if err := os.WriteFile(targetPath, targetBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(catalogBytes)
	args := []string{"--catalog-file", catalogPath, "--catalog-sha256", hex.EncodeToString(digest[:]),
		"--expected-target-file", targetPath, "--profile", profile, "--logical-resource", logical, "--secrets-dir", secrets}
	var output bytes.Buffer
	if err := run(ctx, args, &output); err != nil {
		t.Fatal(err)
	}
	var observed struct {
		CatalogSHA256 string                  `json:"catalogSha256"`
		Target        database.TargetIdentity `json:"target"`
		CanLogin      bool                    `json:"canLogin"`
		Sessions      int                     `json:"sessions"`
	}
	if err := json.Unmarshal(output.Bytes(), &observed); err != nil || observed.CatalogSHA256 != hex.EncodeToString(digest[:]) ||
		observed.Target != target || !observed.CanLogin || observed.Sessions < 1 || strings.Contains(output.String(), "fixture-runtime-password") {
		t.Fatalf("redacted preflight=%s err=%v", output.String(), err)
	}
	wrongPin := append([]string(nil), args...)
	wrongPin[3] = strings.Repeat("0", 64)
	if err := run(ctx, wrongPin, &bytes.Buffer{}); err == nil {
		t.Fatal("changed catalog digest passed read-only preflight")
	}
	stale := target
	stale.BindingGeneration++
	staleBytes, _ := json.Marshal(stale)
	if err := os.WriteFile(targetPath, staleBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, args, &bytes.Buffer{}); err == nil {
		t.Fatal("stale expected target passed read-only preflight")
	}
	var canLogin bool
	if err := admin.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = 'runtime_app'`).Scan(&canLogin); err != nil || !canLogin {
		t.Fatalf("read-only command changed runtime login state: login=%v err=%v", canLogin, err)
	}
}

func TestPrivatePreflightInputRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"outer":{"a":1,"a":2}}`, `{"a":[{"x":1,"x":2}]}`, `{"a":1} {"b":2}`} {
		var value any
		if err := decodeStrictJSON([]byte(raw), &value); err == nil {
			t.Fatalf("ambiguous JSON passed: %s", raw)
		}
	}
	root := t.TempDir()
	plain := filepath.Join(root, "plain.json")
	if err := os.WriteFile(plain, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateInput(plain); err == nil {
		t.Fatal("group/world-readable input passed")
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateInput(link); err == nil {
		t.Fatal("symlink input passed")
	}
}
