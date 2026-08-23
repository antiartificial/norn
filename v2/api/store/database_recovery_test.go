package store

import "testing"

func TestDatabaseRecoveryStatusRequiresArchiveAndReplicaWAL(t *testing.T) {
	status := DatabaseRecoveryStatus{ArchiveMode: "on", ArchiveCommand: "wal-g wal-push %p", WALLevel: "replica"}
	if !status.PITREnabled() {
		t.Fatal("valid archive configuration was not recognized")
	}
	status.ArchiveCommand = "(disabled)"
	if status.PITREnabled() {
		t.Fatal("disabled archive command was accepted")
	}
	status.ArchiveLibrary = "custom_archive"
	if !status.PITREnabled() {
		t.Fatal("archive library was not recognized")
	}
}
