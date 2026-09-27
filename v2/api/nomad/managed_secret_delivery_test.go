package nomad

import (
	"errors"
	"strings"
	"testing"
)

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
