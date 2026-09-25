package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/model"
)

// TargetSnapshotGroup is the restorable inventory of one database target,
// computed by the same location and provenance rules restore uses. Listing
// is read-only: it resolves the current target without opening a
// connection, never adopts a legacy namespace, and never shows a dump whose
// provenance does not name the current target.
type TargetSnapshotGroup struct {
	// Database is the logical name ("" for a v1 app's legacy mapping).
	Database          string           `json:"database"`
	DatabaseName      string           `json:"databaseName"`
	BindingID         string           `json:"bindingId"`
	BindingGeneration uint64           `json:"bindingGeneration"`
	Snapshots         []TargetSnapshot `json:"snapshots"`
	// Unavailable explains why the target could not be listed (for
	// example, no active catalog or a namespace owned by another mapping).
	Unavailable string `json:"unavailable,omitempty"`
}

type TargetSnapshot struct {
	Filename  string `json:"filename"`
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
	// Provenance is "sidecar" (bound at creation) or "adopted" (a pre-v3
	// dump inventoried when this mapping adopted the flat namespace).
	Provenance string `json:"provenance"`
}

// TargetSnapshots lists every snapshot-capable database of an app through
// the target-aware layer. It requires a database profile.
func (p *Pipeline) TargetSnapshots(ctx context.Context, spec *model.InfraSpec) ([]TargetSnapshotGroup, error) {
	if p == nil || p.DatabaseTargets == nil {
		return nil, &DatabaseTargetError{Reason: "no database profile is configured"}
	}
	groups := []TargetSnapshotGroup{}
	if !spec.DeclaresDatabase() {
		return groups, nil
	}
	names := []string{""}
	if spec.NamedDatabases() {
		names = names[:0]
		for _, requirement := range spec.Databases {
			if containsCapability(requirement, "snapshot") {
				names = append(names, requirement.Name)
			}
		}
		sort.Strings(names)
	}
	for _, name := range names {
		group := TargetSnapshotGroup{Database: name, Snapshots: []TargetSnapshot{}}
		location, err := p.inventoryLocation(ctx, spec, name)
		if err != nil {
			group.Unavailable = err.Error()
			groups = append(groups, group)
			continue
		}
		target := location.bound.resolved.Target
		group.DatabaseName, group.BindingID, group.BindingGeneration = target.Database, target.BindingID, target.BindingGeneration
		snapshots, err := listDataSnapshots(location)
		if err != nil {
			group.Unavailable = "snapshot inventory could not be read"
			groups = append(groups, group)
			continue
		}
		for _, snapshot := range snapshots {
			if snapshot.foreign {
				continue // another target's (or generation's) dump is never offered
			}
			provenance := "sidecar"
			if snapshot.sidecar == nil {
				provenance = "adopted"
			}
			group.Snapshots = append(group.Snapshots, TargetSnapshot{Filename: snapshot.Filename, Timestamp: snapshot.Timestamp, Size: snapshot.Size, Provenance: provenance})
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// SnapshotObjectStore is the object store snapshot export and import use;
// storage.Client satisfies it.
type SnapshotObjectStore interface {
	PutObject(ctx context.Context, bucket, key, filePath string) error
	GetObject(ctx context.Context, bucket, key, destPath string) error
}

type snapshotCreateOnlyObjectStore interface {
	SnapshotObjectStore
	PutObjectIfAbsent(ctx context.Context, bucket, key, filePath string) error
}

const (
	snapshotExportSchema   = "norn.snapshot-export/v1"
	snapshotManifestSuffix = ".manifest.json"
)

// SnapshotExportManifest travels with exported dump bytes: the complete
// target identity, catalog revision and digest restore needs. The object key
// alone is never provenance.
type SnapshotExportManifest struct {
	Schema          string                  `json:"schema"`
	App             string                  `json:"app"`
	Database        string                  `json:"database"`
	Filename        string                  `json:"filename"`
	Target          database.TargetIdentity `json:"target"`
	CatalogRevision int64                   `json:"catalogRevision"`
	SHA256          string                  `json:"sha256"`
	Size            int64                   `json:"size"`
	Provenance      string                  `json:"provenance"`
	ExportedAt      time.Time               `json:"exportedAt"`
}

// ExportTargetSnapshot exports one restorable snapshot of the current target
// (the newest when filename is empty). It opens the dump once without
// following symlinks, copies that descriptor's bytes into a private 0600
// file while hashing them, and uploads only that copy, after the copied
// bytes match the snapshot's provenance. A later replacement or symlink swap
// of the namespace file therefore cannot change what is uploaded. The
// manifest (key + ".manifest.json") is uploaded after the dump, so a
// manifest's presence means the export is complete.
func (p *Pipeline) ExportTargetSnapshot(ctx context.Context, spec *model.InfraSpec, logical, filename string, objects SnapshotObjectStore, bucket string) (SnapshotExportManifest, string, error) {
	return p.exportTargetSnapshot(ctx, spec, logical, filename, objects, bucket, "")
}

// ExportTargetSnapshotClaimed gives each accepted operation its own remote
// namespace and verifies both create-only objects before returning success.
func (p *Pipeline) ExportTargetSnapshotClaimed(ctx context.Context, spec *model.InfraSpec, logical, filename string, objects SnapshotObjectStore, bucket, operationID string) (SnapshotExportManifest, string, error) {
	if operationID == "" || strings.ContainsAny(operationID, "/.\\") {
		return SnapshotExportManifest{}, "", fmt.Errorf("snapshot export operation identity is invalid")
	}
	if _, ok := objects.(snapshotCreateOnlyObjectStore); !ok {
		return SnapshotExportManifest{}, "", fmt.Errorf("snapshot object store lacks create-only publication")
	}
	if filename == "" {
		return SnapshotExportManifest{}, "", fmt.Errorf("claimed snapshot export requires a pinned filename")
	}
	return p.exportTargetSnapshot(ctx, spec, logical, filename, objects, bucket, operationID)
}

func (p *Pipeline) exportTargetSnapshot(ctx context.Context, spec *model.InfraSpec, logical, filename string, objects SnapshotObjectStore, bucket, operationID string) (SnapshotExportManifest, string, error) {
	if p == nil || p.DatabaseTargets == nil {
		return SnapshotExportManifest{}, "", &DatabaseTargetError{Reason: "no database profile is configured"}
	}
	logical, err := snapshotDatabaseSelection(spec, logical)
	if err != nil {
		return SnapshotExportManifest{}, "", err
	}
	location, err := p.inventoryLocation(ctx, spec, logical)
	if err != nil {
		return SnapshotExportManifest{}, "", err
	}
	snapshots, err := listDataSnapshots(location)
	if err != nil {
		return SnapshotExportManifest{}, "", err
	}
	var chosen *dataSnapshot
	for index := range snapshots {
		if !snapshots[index].foreign && (filename == "" || snapshots[index].Filename == filename) {
			chosen = &snapshots[index]
			break
		}
	}
	if chosen == nil {
		return SnapshotExportManifest{}, "", &DatabaseTargetError{Reason: "no restorable snapshot of the current target matches"}
	}
	manifest := SnapshotExportManifest{Schema: snapshotExportSchema, App: spec.App, Database: logical, Filename: chosen.Filename,
		Target: location.bound.resolved.Target, Provenance: "sidecar", ExportedAt: time.Now().UTC()}
	switch {
	case chosen.sidecar != nil:
		manifest.SHA256, manifest.Size, manifest.CatalogRevision = chosen.sidecar.SHA256, chosen.sidecar.Size, chosen.sidecar.CatalogRevision
	case chosen.adopted != nil:
		manifest.SHA256, manifest.Size, manifest.Provenance = chosen.adopted.SHA256, chosen.adopted.Size, "adopted"
	default:
		return SnapshotExportManifest{}, "", &DatabaseTargetError{Reason: "snapshot has no target provenance"}
	}
	private, err := os.MkdirTemp("", "norn-export-*")
	if err != nil {
		return SnapshotExportManifest{}, "", err
	}
	defer os.RemoveAll(private)
	copyPath := filepath.Join(private, "dump")
	if err := copyVerified(filepath.Join(location.dir, chosen.Filename), copyPath, manifest.SHA256, manifest.Size); err != nil {
		return SnapshotExportManifest{}, "", fmt.Errorf("snapshot %s: %w", chosen.Filename, err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return SnapshotExportManifest{}, "", err
	}
	manifestPath := filepath.Join(private, "manifest.json")
	if err := writePrivateFile(manifestPath, encoded); err != nil {
		return SnapshotExportManifest{}, "", err
	}
	key := snapshotExportKey(spec, location.bound, chosen.Filename)
	if operationID != "" {
		key = "snapshots/" + spec.App + "/operations/" + operationID + "/" + chosen.Filename
	}
	if operationID == "" {
		if err := objects.PutObject(ctx, bucket, key, copyPath); err != nil {
			return SnapshotExportManifest{}, "", fmt.Errorf("upload snapshot: %w", err)
		}
		if err := objects.PutObject(ctx, bucket, key+snapshotManifestSuffix, manifestPath); err != nil {
			return SnapshotExportManifest{}, "", fmt.Errorf("upload snapshot manifest: %w", err)
		}
		return manifest, key, nil
	}
	if err := publishClaimedSnapshot(ctx, objects.(snapshotCreateOnlyObjectStore), bucket, key, private, copyPath, manifestPath, encoded, manifest.SHA256, manifest.Size); err != nil {
		return SnapshotExportManifest{}, "", err
	}
	return manifest, key, nil
}

func publishClaimedSnapshot(ctx context.Context, createOnly snapshotCreateOnlyObjectStore, bucket, key, private, copyPath, manifestPath string, encoded []byte, digest string, size int64) error {
	if err := createOnly.PutObjectIfAbsent(ctx, bucket, key, copyPath); err != nil {
		// A lost response can still mean the create committed. Verification below
		// decides from the remote bytes, never from the transport error alone.
		_ = err
	}
	remoteDump := filepath.Join(private, "remote-dump")
	if err := createOnly.GetObject(ctx, bucket, key, remoteDump); err != nil {
		return fmt.Errorf("verify remote snapshot: %w", err)
	}
	if err := verifyRegularFileDigest(remoteDump, digest, size); err != nil {
		return fmt.Errorf("remote snapshot differs from pinned source: %w", err)
	}
	if err := createOnly.PutObjectIfAbsent(ctx, bucket, key+snapshotManifestSuffix, manifestPath); err != nil {
		_ = err
	}
	remoteManifest := filepath.Join(private, "remote-manifest")
	if err := createOnly.GetObject(ctx, bucket, key+snapshotManifestSuffix, remoteManifest); err != nil {
		return fmt.Errorf("verify remote snapshot manifest: %w", err)
	}
	remoteManifestInfo, err := os.Lstat(remoteManifest)
	if err != nil || !remoteManifestInfo.Mode().IsRegular() || remoteManifestInfo.Size() != int64(len(encoded)) {
		return fmt.Errorf("remote snapshot manifest has unexpected size or type: %v", err)
	}
	actual, err := os.ReadFile(remoteManifest)
	if err != nil || !bytes.Equal(actual, encoded) {
		return fmt.Errorf("remote snapshot manifest differs from accepted publication: %v", err)
	}
	return nil
}

// ImportTargetSnapshot recovers an exported snapshot into the current
// target's namespace. The manifest must name this app, logical database and
// the exact current target (cross-target import needs the explicit intent of
// the syntax proposal §4, which is not implemented); the downloaded bytes
// must match its digest and size. Publication reuses the sidecar-first
// exclusive protocol, so the imported dump carries its original catalog
// revision provenance and is restorable through the ordinary durable
// restore. Re-importing identical bytes is a no-op.
func (p *Pipeline) ImportTargetSnapshot(ctx context.Context, spec *model.InfraSpec, logical string, objects SnapshotObjectStore, bucket, key string) (SnapshotExportManifest, error) {
	if p == nil || p.DatabaseTargets == nil {
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "no database profile is configured"}
	}
	logical, err := snapshotDatabaseSelection(spec, logical)
	if err != nil {
		return SnapshotExportManifest{}, err
	}
	location, err := p.inventoryLocation(ctx, spec, logical)
	if err != nil {
		return SnapshotExportManifest{}, err
	}
	private, err := os.MkdirTemp("", "norn-import-*")
	if err != nil {
		return SnapshotExportManifest{}, err
	}
	defer os.RemoveAll(private)
	manifestPath := filepath.Join(private, "manifest.json")
	if err := objects.GetObject(ctx, bucket, key+snapshotManifestSuffix, manifestPath); err != nil {
		return SnapshotExportManifest{}, fmt.Errorf("download snapshot manifest: %w", err)
	}
	manifest, err := readExportManifest(manifestPath)
	if err != nil {
		return SnapshotExportManifest{}, err
	}
	target := location.bound.resolved.Target
	switch {
	case manifest.App != spec.App || manifest.Database != logical:
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "export manifest belongs to another app or database"}
	case manifest.Target != target:
		return SnapshotExportManifest{}, fmt.Errorf("import %s: %w", manifest.Filename, errSnapshotTargetMismatch)
	case filepath.Base(manifest.Filename) != manifest.Filename || !strings.HasPrefix(manifest.Filename, target.Database+"_") || !strings.HasSuffix(manifest.Filename, ".dump") || manifest.Size <= 0:
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "export manifest names an unsafe snapshot file"}
	}
	if err := os.MkdirAll(location.dir, 0o750); err != nil {
		return SnapshotExportManifest{}, err
	}
	// Download into a fresh private directory inside the namespace (same
	// filesystem for the final link; inventory never lists directories), to
	// a path that does not exist yet: object stores create the destination
	// exclusively.
	staging, err := os.MkdirTemp(location.dir, ".norn-import-*")
	if err != nil {
		return SnapshotExportManifest{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return SnapshotExportManifest{}, err
	}
	temporaryPath := filepath.Join(staging, "dump")
	if err := objects.GetObject(ctx, bucket, key, temporaryPath); err != nil {
		return SnapshotExportManifest{}, fmt.Errorf("download snapshot: %w", err)
	}
	// Verify through one non-blocking, non-following descriptor: a FIFO or
	// symlink is refused before any read, and at most size+1 bytes are hashed.
	if err := verifyRegularFileDigest(temporaryPath, manifest.SHA256, manifest.Size); err != nil {
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "downloaded snapshot bytes do not match the export manifest"}
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return SnapshotExportManifest{}, err
	}
	publishing := location
	publishing.bound = &boundDatabase{resolved: location.bound.resolved, recorded: recordedTarget{CatalogRevision: manifest.CatalogRevision}, name: logical}
	destination := filepath.Join(location.dir, manifest.Filename)
	if info, err := os.Lstat(destination); err == nil {
		if verifyBoundDump(publishing, manifest.Filename, info.Size()) == nil {
			if existing, _ := readSidecar(publishing, manifest.Filename); existing != nil && existing.SHA256 == manifest.SHA256 {
				return manifest, nil // already imported
			}
		}
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: fmt.Sprintf("snapshot %s already exists with other content", manifest.Filename)}
	}
	if err := writeSidecarExclusive(publishing, manifest.Filename, manifest.SHA256, manifest.Size); err != nil {
		return SnapshotExportManifest{}, fmt.Errorf("record imported snapshot provenance: %w", err)
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		_ = os.Remove(destination + sidecarSuffix)
		return SnapshotExportManifest{}, fmt.Errorf("publish imported snapshot: %w", err)
	}
	return manifest, nil
}

func snapshotDatabaseSelection(spec *model.InfraSpec, logical string) (string, error) {
	if !spec.NamedDatabases() {
		if logical != "" {
			return "", &DatabaseTargetError{Reason: "a v1 app has no named databases"}
		}
		return "", nil
	}
	if logical == "" {
		selected, err := selectedDatabase(spec, map[string]interface{}{})
		if err != nil {
			return "", err
		}
		logical = selected
	}
	return logical, requireDeclared(spec, logical, "snapshot")
}

// copyVerified copies source, opened once without following symlinks, to a
// new private file while hashing the bytes actually copied, and fails unless
// they match the expected digest and size.
func copyVerified(source, destination, expectedSHA256 string, expectedSize int64) error {
	in, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open verified snapshot: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot is not a regular file")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	if syncErr := out.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	if written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fmt.Errorf("content differs from its target sidecar")
	}
	return nil
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// maxExportManifestBytes bounds manifest downloads.
const maxExportManifestBytes = 64 << 10

// openRegularNoFollow opens path without following symlinks and without
// blocking (so a FIFO cannot stall the caller) and requires a regular file.
func openRegularNoFollow(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return file, info, nil
}

// verifyRegularFileDigest requires a regular file of exactly size bytes whose
// SHA-256 is digest, reading at most size+1 bytes.
func verifyRegularFileDigest(path, digest string, size int64) error {
	file, info, err := openRegularNoFollow(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() != size {
		return fmt.Errorf("size differs")
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, size+1))
	if err != nil || read != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("content differs")
	}
	return nil
}

func readExportManifest(path string) (SnapshotExportManifest, error) {
	file, info, err := openRegularNoFollow(path)
	if err != nil {
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "export manifest is not a regular file"}
	}
	defer file.Close()
	if info.Size() > maxExportManifestBytes {
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "export manifest is too large"}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxExportManifestBytes+1))
	if err != nil || len(data) > maxExportManifestBytes {
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "export manifest could not be read within bounds"}
	}
	var manifest SnapshotExportManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if decoder.Decode(&manifest) != nil || decoder.Decode(&trailing) != io.EOF || manifest.Schema != snapshotExportSchema {
		return SnapshotExportManifest{}, &DatabaseTargetError{Reason: "export manifest is malformed"}
	}
	return manifest, nil
}

// inventoryLocation resolves the current target for a logical database
// ("" = legacy mapping) and returns its snapshot location without opening a
// session or adopting anything.
func (p *Pipeline) inventoryLocation(ctx context.Context, spec *model.InfraSpec, logical string) (snapshotLocation, error) {
	var resolved database.ResolvedBinding
	var err error
	if logical == "" {
		if spec.Infrastructure == nil || spec.Infrastructure.Postgres == nil {
			return snapshotLocation{}, &DatabaseTargetError{Reason: "app has no legacy postgres declaration"}
		}
		resolved, _, err = p.DatabaseTargets.resolve(ctx, spec, nil, nil)
	} else {
		var resolver *database.Resolver
		if resolver, _, err = p.DatabaseTargets.resolverAt(ctx); err == nil {
			resolved, err = resolver.Resolve(database.ResolveRequest{DeploymentProfileID: p.DatabaseTargets.ProfileID, Purpose: database.PurposeApplication, LogicalResourceID: logical})
		}
	}
	if err != nil {
		return snapshotLocation{}, err
	}
	bound := &boundDatabase{resolved: resolved, name: logical}
	root := p.DatabaseTargets.snapshotRoot()
	location := snapshotLocation{dir: root, database: resolved.Target.Database, bound: bound}
	if !resolved.Legacy {
		location.dir = filepath.Join(root, "targets", targetNamespace(resolved.Target))
		return location, nil
	}
	owner, err := readLegacyOwner(filepath.Join(root, legacyOwnerDirectory, location.database+".json"))
	if err != nil {
		return snapshotLocation{}, err
	}
	location.adopted = map[string]legacySnapshot{}
	if owner == nil {
		// Not adopted yet: pre-v3 dumps become restorable only when the
		// first bound operation records this mapping as the owner.
		return location, nil
	}
	if _, err := checkLegacyOwner(owner, legacySnapshotOwner{Database: location.database, ProfileID: p.DatabaseTargets.ProfileID, MappingID: resolved.Target.BindingID, ServiceID: resolved.Target.ServiceID}, nil); err != nil {
		if errors.Is(err, errLegacyNamespaceOwned) {
			return snapshotLocation{}, fmt.Errorf("snapshots for %s: %w", location.database, errLegacyNamespaceOwned)
		}
		return snapshotLocation{}, err
	}
	location.adoptedTarget = owner.Target
	for _, entry := range owner.Inventory {
		location.adopted[entry.Filename] = entry
	}
	return location, nil
}
