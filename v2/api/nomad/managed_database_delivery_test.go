package nomad

import "testing"

func TestManagedDatabaseStageRequiresExactMaterialAndAcceptedTarget(t *testing.T) {
	client, fake := newFakeVariableClient(t)
	plan := ManagedJobInputRequirements{JobID: "revision-job", VariablePath: DatabaseVariablePath("revision-job"), DatabaseRevision: 7,
		RequiredKeys: []string{DatabaseRevisionItemKey("primary", 7), DatabaseRevisionTargetKey("primary", 7)}, RuntimeDatabaseNames: []string{"primary"}}
	items := map[string]string{DatabaseItemKey("primary"): "postgres://private", DatabaseTargetItemKey("primary"): `{"bindingId":"accepted"}`}
	want := map[string]string{"primary": items[DatabaseTargetItemKey("primary")]}
	if err := client.StageManagedJobDatabaseInputs("global", plan, map[string]string{DatabaseTargetItemKey("primary"): want["primary"]}, want); err == nil || fake.writes != 0 {
		t.Fatalf("missing URL reached Nomad: writes=%d err=%v", fake.writes, err)
	}
	if err := client.StageManagedJobDatabaseInputs("global", plan, items, map[string]string{"primary": "different"}); err == nil || fake.writes != 0 {
		t.Fatalf("different target reached Nomad: writes=%d err=%v", fake.writes, err)
	}
	if err := client.StageManagedJobDatabaseInputs("global", plan, items, want); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckManagedJobInputs(t.Context(), "global", plan, want); err != nil {
		t.Fatal(err)
	}
	writes := fake.writes
	if err := client.StageManagedJobDatabaseInputs("global", plan, items, want); err != nil || fake.writes != writes {
		t.Fatalf("idempotent stage wrote again: writes=%d err=%v", fake.writes, err)
	}
}
