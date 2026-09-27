package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
)

type reviewMaterialSource string

func (s reviewMaterialSource) Resolve(context.Context, string) ([]byte, error) {
	return []byte(s), nil
}

// Fixture adapted when the endpoint moved from the credential secret into
// catalog identity (Endpoint); the secret now carries only the password.
// The review assertions below are unchanged.
func reviewMaterialBinding() ResolvedBinding {
	return ResolvedBinding{
		Target:  TargetIdentity{ServiceID: "review-pg", ServiceGeneration: 1, BindingID: "review-app", BindingGeneration: 1, Engine: EnginePostgreSQL, Database: "review", Role: "review"},
		Purpose: PurposeApplication, CredentialRef: "secret:review/app", TLS: DatabaseTLS{Mode: TLSDisabled},
		Endpoint: DatabaseEndpoint{Host: "127.0.0.1", Port: 5432},
	}
}

func TestReviewMaterialIgnoresAmbientService(t *testing.T) {
	t.Setenv("PGSERVICE", "ambient-must-not-be-read")
	t.Setenv("PGSERVICEFILE", t.TempDir()+"/missing-service-file")
	session, err := OpenSession(context.Background(), reviewMaterialBinding(), reviewMaterialSource(`{"password":"review-only"}`))
	if err != nil {
		t.Fatalf("explicit target construction consulted ambient service settings: %v", err)
	}
	defer session.Close()
}

func TestReviewMaterialRejectsTrailingJSONDelimiter(t *testing.T) {
	session, err := OpenSession(context.Background(), reviewMaterialBinding(), reviewMaterialSource(`{"password":"review-only"}]`))
	if session != nil {
		defer session.Close()
	}
	if err == nil {
		t.Fatal("connection secret with trailing JSON delimiter was accepted")
	}
}

func TestReviewMaterialDoesNotInheritPassword(t *testing.T) {
	t.Setenv("PGPASSWORD", "review-ambient-control-password")
	session, err := OpenSession(context.Background(), reviewMaterialBinding(), reviewMaterialSource(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.config.Password != "" {
		t.Fatal("application adapter inherited an ambient password")
	}
}

func TestReviewMaterialFormattingIsRedacted(t *testing.T) {
	const canary = "review-private-session-password"
	session, err := OpenSession(context.Background(), reviewMaterialBinding(), reviewMaterialSource(`{"password":"`+canary+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(verb, session), canary) {
			t.Fatalf("session formatting with %s exposes private password", verb)
		}
	}
}

type snapshotDescriptorBackend struct{}

func (snapshotDescriptorBackend) Start(context.Context, supervisor.BackendExecution, effect.LaunchMaterial) error {
	return nil
}
func (snapshotDescriptorBackend) Observe(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{}, nil
}
func (snapshotDescriptorBackend) Revoke(context.Context, supervisor.BackendExecution) (supervisor.BackendState, error) {
	return supervisor.BackendState{}, nil
}
func (snapshotDescriptorBackend) RetrieveResult(context.Context, supervisor.BackendExecution, string) ([]byte, error) {
	return nil, nil
}

func TestSnapshotLaunchMaterialStaysPrivateAndRotatesWithSession(t *testing.T) {
	manager, err := supervisor.NewManager(t.TempDir(), []byte("0123456789abcdef0123456789abcdef"), snapshotDescriptorBackend{})
	if err != nil {
		t.Fatal(err)
	}
	options := SnapshotLaunchOptions{PGDumpPath: "/usr/bin/pg_dump", PGDumpSHA256: strings.Repeat("a", 64), Subject: "app:review/db:main@generation:1", Timeout: time.Minute}
	descriptor := func(password string) []byte {
		t.Helper()
		session, err := OpenSession(context.Background(), reviewMaterialBinding(), reviewMaterialSource(`{"password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		var payload []byte
		err = session.WithSnapshotLaunchMaterial(options, func(material supervisor.SnapshotLaunchMaterial) error {
			if material.Password != password || !strings.Contains(string(material.ServiceFile), "host=127.0.0.1") {
				t.Fatalf("private launch material was incomplete")
			}
			if strings.Contains(string(material.ServiceFile), password) || strings.Contains(string(material.ServiceFile), "passfile=") {
				t.Fatal("runner service file retained session-only credentials")
			}
			payload, err = manager.BuildSnapshotDescriptor(material)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), password) || strings.Contains(string(payload), "127.0.0.1") {
			t.Fatal("durable descriptor persisted private session material")
		}
		if err := json.Unmarshal(payload, &map[string]any{}); err != nil {
			t.Fatalf("descriptor is not JSON: %v", err)
		}
		return payload
	}
	first := descriptor("first-rotating-secret")
	second := descriptor("second-rotating-secret")
	if string(first) != string(second) {
		t.Fatal("credential rotation changed the durable descriptor")
	}
}

func TestSnapshotLaunchMaterialRejectsSessionTLSPaths(t *testing.T) {
	// TLS paths name session-private files. The snapshot runner has no protocol
	// for carrying them, so the bridge must refuse rather than reuse the path.
	if _, err := snapshotServiceFile([]byte("[norn_target]\nhost=127.0.0.1\nport=5432\ndbname=review\nuser=review\nsslmode=verify-ca\nsslrootcert=/private/ca.pem\n")); err == nil {
		t.Fatal("snapshot service accepted node-local TLS paths")
	}
}
