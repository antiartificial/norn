package nomad

import (
	"context"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestCheckManagedJobInputsRequiresDeliveredKeysAndAcceptedTarget(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	plan := ManagedJobInputRequirements{JobID: "revision-job", VariablePath: DatabaseVariablePath("revision-job"), DatabaseRevision: 7,
		RequiredKeys:         []string{"API_TOKEN", DatabaseRevisionItemKey("primary", 7), DatabaseRevisionTargetKey("primary", 7)},
		RuntimeDatabaseNames: []string{"primary"}}
	want := map[string]string{"primary": `{"provider":"accepted"}`}
	check := func() error { return client.CheckManagedJobInputs(context.Background(), "global", plan, want) }
	if err := check(); err == nil {
		t.Fatal("missing private variable accepted")
	}
	fake.variables[plan.VariablePath] = &nomadapi.Variable{Path: plan.VariablePath, Items: map[string]string{
		"API_TOKEN": "secret", DatabaseRevisionItemKey("primary", 7): "postgres://secret",
		DatabaseRevisionTargetKey("primary", 7): `{"provider":"wrong"}`,
	}}
	if err := check(); err == nil {
		t.Fatal("wrong database target accepted")
	}
	fake.variables[plan.VariablePath].Items[DatabaseRevisionTargetKey("primary", 7)] = want["primary"]
	if err := check(); err != nil {
		t.Fatal(err)
	}
	delete(fake.variables[plan.VariablePath].Items, "API_TOKEN")
	if err := check(); err == nil {
		t.Fatal("missing secret accepted")
	}
}
