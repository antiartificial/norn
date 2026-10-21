package nomad

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

type managedSecretSource struct {
	values map[string]string
	calls  int
}

func TestManagedSecretDeliveryUsesPlanNamespaceForVariableWriteAndRead(t *testing.T) {
	fake := &fakeVariables{variables: map[string]*nomadapi.Variable{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("namespace"); got != "norn-work-staging" {
			t.Errorf("Variable %s namespace = %q", r.Method, got)
		}
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	plan := ManagedJobInputRequirements{JobID: "pilot-revision", Namespace: "norn-work-staging", VariablePath: DatabaseVariablePath("pilot-revision"), RequiredKeys: []string{"PILOT_TOKEN"}}
	if err := client.StageManagedJobSecretInputs("global", plan, map[string]string{"PILOT_TOKEN": "private"}); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckManagedJobInputs(t.Context(), "global", plan, nil); err != nil {
		t.Fatal(err)
	}
}

func (s *managedSecretSource) EnvMap(string) (map[string]string, error) {
	s.calls++
	return s.values, nil
}

func TestManagedSecretAndDatabaseDeliveryShareRevisionJobWithoutOverwrite(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	plan := ManagedJobInputRequirements{JobID: "revision-job", VariablePath: DatabaseVariablePath("revision-job"), DatabaseRevision: 7,
		RequiredKeys:         []string{"API_TOKEN", DatabaseRevisionItemKey("primary", 7), DatabaseRevisionTargetKey("primary", 7)},
		RuntimeDatabaseNames: []string{"primary"}}
	secret := map[string]string{"API_TOKEN": "private-token"}
	databaseItems := map[string]string{DatabaseItemKey("primary"): "postgres://private", DatabaseTargetItemKey("primary"): `{"bindingId":"accepted"}`}
	if err := client.StageManagedJobSecretInputs("global", plan, secret); err != nil {
		t.Fatal(err)
	}
	if err := client.DeliverDatabaseVariable("global", plan.JobID, databaseItems, 7); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckManagedJobInputs(t.Context(), "global", plan, map[string]string{"primary": databaseItems[DatabaseTargetItemKey("primary")]}); err != nil {
		t.Fatal(err)
	}
	writes := fake.writes
	if err := client.StageManagedJobSecretInputs("global", plan, secret); err != nil || fake.writes != writes {
		t.Fatalf("idempotent staging wrote again: %v", err)
	}
	if err := client.StageManagedJobSecretInputs("global", plan, map[string]string{"API_TOKEN": "changed"}); !errors.Is(err, ErrDatabaseVariableConflict) || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed secret was not safely refused: %v", err)
	}
	if err := client.StageManagedJobSecretInputs("global", plan, map[string]string{"API_TOKEN": "private-token", "EXTRA": "value"}); err == nil {
		t.Fatal("extra secret accepted")
	}
	if err := client.StageManagedJobSecretInputs("global", plan, map[string]string{"API_TOKEN": "private-token", DatabaseRevisionItemKey("primary", 7): "override"}); err == nil {
		t.Fatal("reserved database key accepted")
	}
}

func TestManagedSecretSourceSelectsExactKeysAfterDatabaseStage(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	plan := ManagedJobInputRequirements{JobID: "revision-job", VariablePath: DatabaseVariablePath("revision-job"), DatabaseRevision: 7,
		RequiredKeys: []string{"API_TOKEN", DatabaseRevisionItemKey("primary", 7), DatabaseRevisionTargetKey("primary", 7)}}
	items := map[string]string{DatabaseItemKey("primary"): "postgres://private", DatabaseTargetItemKey("primary"): `{"bindingId":"accepted"}`}
	if err := client.DeliverDatabaseVariable("global", plan.JobID, items, 7); err != nil {
		t.Fatal(err)
	}
	source := &managedSecretSource{values: map[string]string{"API_TOKEN": "private-token", "UNRELATED": "must-not-stage"}}
	if err := client.StageManagedJobSecretsFromSource("orders", "global", plan, source); err != nil {
		t.Fatal(err)
	}
	variable := fake.variables[plan.VariablePath]
	if variable.Items["API_TOKEN"] != "private-token" || variable.Items["UNRELATED"] != "" ||
		variable.Items[DatabaseRevisionItemKey("primary", 7)] != "postgres://private" {
		t.Fatal("source selection changed unrelated or database material")
	}
	delete(source.values, "API_TOKEN")
	writes := fake.writes
	if err := client.StageManagedJobSecretsFromSource("orders", "global", plan, source); err == nil || fake.writes != writes {
		t.Fatalf("missing source secret wrote Nomad Variable: writes=%d err=%v", fake.writes, err)
	}
}
