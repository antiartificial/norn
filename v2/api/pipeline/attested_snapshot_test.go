package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/effect"
)

func testAttestedSnapshotLocation(t *testing.T) snapshotLocation {
	t.Helper()
	target := database.TargetIdentity{ServiceID: "postgres-main", ServiceGeneration: 1, BindingID: "app-db", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "demo", Role: "demo"}
	return snapshotLocation{dir: t.TempDir(), database: "demo", bound: &boundDatabase{resolved: database.ResolvedBinding{Target: target}, recorded: recordedTarget{CatalogRevision: 7, Target: target}}}
}

func testAttestedArtifact(operationID string, data []byte, fence func() error) AttestedSnapshotArtifact {
	digest := sha256.Sum256(data)
	return AttestedSnapshotArtifact{OperationID: operationID, ClaimGeneration: 1, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data)), Copy: func(w io.Writer) error { _, err := w.Write(data); return err }, Fence: func(publish func() error) error {
		if err := fence(); err != nil {
			return err
		}
		return publish()
	}}
}

func TestPublishAttestedSnapshotReconcilesCrashOrphans(t *testing.T) {
	location := testAttestedSnapshotLocation(t)
	const operationID = "snapshot-operation"
	staging := filepath.Join(location.dir, ".attested-snapshot-"+operationID+"-crashed")
	if err := os.WriteFile(staging, []byte("untrusted partial bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("attested snapshot bytes")
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	published, err := PublishAttestedSnapshot(location, at, testAttestedArtifact(operationID, data, func() error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crashed staging file remains: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(location.dir, published.Filename))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("published bytes = %q, %v", got, err)
	}
	if err := verifyBoundDump(location, published.Filename, int64(len(data))); err != nil {
		t.Fatalf("published provenance = %v", err)
	}
}

func TestPublishAttestedSnapshotCompletesMatchingSidecarOrphan(t *testing.T) {
	location := testAttestedSnapshotLocation(t)
	data := []byte("attested snapshot bytes")
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	artifact := testAttestedArtifact("snapshot-operation", data, func() error { return nil })
	filename := "demo_effect-snapshot-operation_20260924T120000.dump"
	if err := writeSidecarExclusive(location, filename, artifact.SHA256, artifact.Size); err != nil {
		t.Fatal(err)
	}
	published, err := PublishAttestedSnapshot(location, at, artifact)
	if err != nil || published.Filename != filename {
		t.Fatalf("orphan sidecar recovery = %+v, %v", published, err)
	}
	if err := verifyBoundDump(location, filename, int64(len(data))); err != nil {
		t.Fatalf("recovered provenance = %v", err)
	}
}

func TestPublishAttestedSnapshotFencesReplay(t *testing.T) {
	location := testAttestedSnapshotLocation(t)
	data := []byte("attested snapshot bytes")
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	artifact := testAttestedArtifact("snapshot-operation", data, func() error { return nil })
	if _, err := PublishAttestedSnapshot(location, at, artifact); err != nil {
		t.Fatal(err)
	}
	artifact.Fence = func(func() error) error { return errors.New("claim expired") }
	if _, err := PublishAttestedSnapshot(location, at, artifact); err == nil || !strings.Contains(err.Error(), "before replay") {
		t.Fatalf("stale replay = %v", err)
	}
}

// A process may link the dump and die before it records the durable receipt.
// On replay Link returns EEXIST; the verified matching pair is proof enough to
// record that missing receipt rather than leaving terminalization impossible.
func TestPublishAttestedSnapshotRecordsReceiptWhenLinkFindsMatchingPair(t *testing.T) {
	location := testAttestedSnapshotLocation(t)
	data := []byte("attested snapshot bytes")
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	artifact := testAttestedArtifact("snapshot-operation", data, func() error { return nil })
	receipts := 0
	artifact.Receipt = func(string) error { receipts++; return nil }
	original := linkAttestedSnapshot
	linkAttestedSnapshot = func(oldname, newname string) error {
		if err := original(oldname, newname); err != nil {
			return err
		}
		return fs.ErrExist // emulate a crash/uncertain Link acknowledgement.
	}
	t.Cleanup(func() { linkAttestedSnapshot = original })
	if _, err := PublishAttestedSnapshot(location, at, artifact); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("publication receipts = %d, want 1", receipts)
	}
}

func TestPublishAttestedSnapshotDefersWhenReceiptPersistenceIsUnavailable(t *testing.T) {
	location := testAttestedSnapshotLocation(t)
	artifact := testAttestedArtifact("snapshot-operation", []byte("attested snapshot bytes"), func() error { return nil })
	artifact.Receipt = func(string) error { return errors.New("postgres unavailable") }
	_, err := PublishAttestedSnapshot(location, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), artifact)
	if !effect.IsDeferred(err) {
		t.Fatalf("receipt persistence error = %v, want deferred", err)
	}
}
