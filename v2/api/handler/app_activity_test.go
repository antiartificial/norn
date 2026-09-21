package handler

import (
	"testing"

	"norn/v2/api/model"
)

func TestAppActivityEventRecordsActorAndInfoSeverity(t *testing.T) {
	ev := appActivityEvent("demo", "app.restarted", "App restarted", "demo was restarted", "alice", nil)
	if ev.App != "demo" || ev.Type != "app.restarted" {
		t.Fatalf("unexpected app/type: %+v", ev)
	}
	if ev.Severity != model.BeaconInfo {
		t.Fatalf("expected info severity, got %q", ev.Severity)
	}
	if ev.Metadata["actor"] != "alice" {
		t.Fatalf("actor not recorded in metadata: %v", ev.Metadata)
	}
}

func TestAppActivityEventDedupeKeysAreUniquePerCall(t *testing.T) {
	a := appActivityEvent("demo", "app.restarted", "", "", "", nil)
	b := appActivityEvent("demo", "app.restarted", "", "", "", nil)
	if a.DedupeKey == b.DedupeKey {
		t.Fatalf("dedupe keys must be unique so repeated actions are not collapsed within the hour window: %q", a.DedupeKey)
	}
}

func TestSecretKeyNamesReturnsSortedKeysNeverValues(t *testing.T) {
	keys := secretKeyNames(map[string]string{"DB_URL": "postgres://secret", "API_KEY": "topsecret"})
	if len(keys) != 2 || keys[0] != "API_KEY" || keys[1] != "DB_URL" {
		t.Fatalf("expected sorted key names, got %v", keys)
	}
	for _, k := range keys {
		if k == "postgres://secret" || k == "topsecret" {
			t.Fatalf("secret value leaked into key names: %v", keys)
		}
	}
}
