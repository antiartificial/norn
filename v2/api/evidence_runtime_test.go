package main

import (
	"context"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"norn/v2/api/archive"
	"norn/v2/api/config"
	"norn/v2/api/internal/pgtest"
	"norn/v2/api/internal/s3emulator"
	"norn/v2/api/retention"
	"norn/v2/api/saga"
	"norn/v2/api/store"
)

func TestEvidenceArchiveConfigurationFailsClosed(t *testing.T) {
	hot := saga.NewPostgresStore(nil)
	if store, archiver, err := configureEvidenceArchive(&config.Config{}, nil, hot); err != nil || archiver != nil || store != saga.Store(hot) {
		t.Fatalf("unset archive = %v %v %v", store, archiver, err)
	}
	// With a control store but no archive, reads stay archive-aware (reader
	// contract 2) so history pruned elsewhere is refused, not served partially.
	if store, archiver, err := configureEvidenceArchive(&config.Config{}, &store.DB{}, hot); err != nil || archiver != nil {
		t.Fatalf("unset archive with control store = %v %v", archiver, err)
	} else if history, ok := store.(*retention.HistoryStore); !ok || history.Archive != nil {
		t.Fatalf("unset archive read store = %T", store)
	}
	dir := filepath.Join(t.TempDir(), "evidence")
	base := config.Config{EvidenceArchiveDir: dir, EvidenceArchiveMode: "shadow", EvidenceMinAge: "720h", EvidenceArchiveMaxBytes: 1 << 20}
	for name, mutate := range map[string]func(*config.Config){
		"mode":     func(c *config.Config) { c.EvidenceArchiveMode = "delete-everything" },
		"min age":  func(c *config.Config) { c.EvidenceMinAge = "soon" },
		"capacity": func(c *config.Config) { c.EvidenceArchiveMaxBytes = 0 },
		"relative": func(c *config.Config) { c.EvidenceArchiveDir = "relative/evidence" },
	} {
		cfg := base
		mutate(&cfg)
		if _, _, err := configureEvidenceArchive(&cfg, nil, hot); err == nil {
			t.Errorf("%s misconfiguration accepted", name)
		}
	}
	store, archiver, err := configureEvidenceArchive(&base, nil, hot)
	if err != nil || archiver == nil || archiver.Mode != retention.ModeShadow {
		t.Fatalf("configured archive = %v %v", archiver, err)
	}
	if _, ok := store.(*retention.HistoryStore); !ok {
		t.Fatalf("handlers would not read archived history: %T", store)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("archive root = %v, %v", info, err)
	}
}

// A process with an archive enables the durable reserve; a process without
// one leaves it enabled (dropping configuration does not lift admission
// control); only an explicit setting disables it.
func TestEvidenceReservePolicyIsDurableAndOnlyExplicitlyDisabled(t *testing.T) {
	server := pgtest.Start(t)
	server.CreateDatabase(t, "norn_control")
	db, err := store.Connect(server.URL("norn_control") + "&pool_max_conns=4")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg := &config.Config{EvidenceReserve: "enforce", EvidenceReserveMaxPending: 5, EvidenceReserveMaxPendingAge: time.Hour}
	enabled := func() bool {
		status, err := db.EvidenceReserve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return status.Enabled
	}
	if err := applyEvidenceReservePolicy(ctx, cfg, db, true); err != nil || !enabled() {
		t.Fatalf("archive-configured process = %v, enabled %v", err, enabled())
	}
	if status, err := db.EvidenceReserve(ctx); err != nil || status.MaxSignedAcceptanceBytes != 0 {
		t.Fatalf("default signed-acceptance byte gate = %+v, %v", status, err)
	}
	if err := applyEvidenceReservePolicy(ctx, cfg, db, false); err != nil || !enabled() {
		t.Fatalf("process without an archive lifted the reserve: %v", err)
	}
	cfg.EvidenceReserve = "disabled"
	if err := applyEvidenceReservePolicy(ctx, cfg, db, false); err != nil || enabled() {
		t.Fatalf("explicit disable = %v", err)
	}
	cfg.EvidenceReserve = "sometimes"
	if err := applyEvidenceReservePolicy(ctx, cfg, db, true); err == nil {
		t.Fatal("unknown reserve mode accepted")
	}
	// Norn's own control-store sessions declare the archive-aware contract.
	var name string
	if err := db.Pool.QueryRow(ctx, `SELECT current_setting('application_name')`).Scan(&name); err != nil || name != store.ReaderApplicationName("control") {
		t.Fatalf("control session application_name = %q, %v", name, err)
	}
}

// The Fleet profile is explicit (backend=object), uses dedicated credentials
// from owner-only files and a CA bundle, and refuses ambiguous or shared
// configuration. Exercised against the in-process emulator only.
func TestEvidenceArchiveFleetObjectProfileIsExplicitAndIndependent(t *testing.T) {
	emulator, server := s3emulator.Start("norn-evidence", "archive-writer")
	defer server.Close()
	_ = emulator
	dir := t.TempDir()
	private := func(name, value string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ca := private("ca.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})), 0o600)
	endpoint, _ := url.Parse(server.URL)
	hot := saga.NewPostgresStore(nil)
	base := config.Config{EvidenceArchiveBackend: "object", EvidenceArchiveMode: "shadow", EvidenceMinAge: "720h",
		EvidenceArchiveEndpoint: endpoint.Host, EvidenceArchiveBucket: "norn-evidence", EvidenceArchivePrefix: "fleet", EvidenceArchiveRegion: "us-east-1",
		EvidenceArchiveAccessKeyFile: private("access", "archive-writer\n", 0o600), EvidenceArchiveSecretKeyFile: private("secret", "archive-secret\n", 0o600),
		EvidenceArchiveCAFile: ca, S3AccessKey: "application-key"}
	reads, archiver, err := configureEvidenceArchive(&base, &store.DB{}, hot)
	if err != nil || archiver == nil {
		t.Fatalf("fleet object archive = %v", err)
	}
	if _, ok := archiver.Archive.(*archive.ObjectStore); !ok {
		t.Fatalf("archiver backend = %T", archiver.Archive)
	}
	if history, ok := reads.(*retention.HistoryStore); !ok || history.Archive != archiver.Archive {
		t.Fatalf("reads are not served from the object archive: %T", reads)
	}
	for name, mutate := range map[string]func(*config.Config){
		"shared application key":    func(c *config.Config) { c.S3AccessKey = "archive-writer" },
		"local directory as well":   func(c *config.Config) { c.EvidenceArchiveDir = filepath.Join(dir, "local") },
		"group-readable secret":     func(c *config.Config) { c.EvidenceArchiveSecretKeyFile = private("loose", "archive-secret", 0o640) },
		"missing access key file":   func(c *config.Config) { c.EvidenceArchiveAccessKeyFile = "" },
		"missing bucket":            func(c *config.Config) { c.EvidenceArchiveBucket = "" },
		"untrusted server":          func(c *config.Config) { c.EvidenceArchiveCAFile = "" },
		"object settings but local": func(c *config.Config) { c.EvidenceArchiveBackend = "local" },
		"unknown backend":           func(c *config.Config) { c.EvidenceArchiveBackend = "s3ish" },
	} {
		cfg := base
		mutate(&cfg)
		if _, _, err := configureEvidenceArchive(&cfg, &store.DB{}, hot); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
