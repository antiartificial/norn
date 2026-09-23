package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
