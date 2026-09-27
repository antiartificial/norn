package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// This fixture intentionally requires explicit writer admission. During a
// migration, the old web, worker and schedule processes must all lose it.
type fixture struct {
	db       *sql.DB
	files    string
	writable bool
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: mobility-fixture serve|worker|tick|migrate")
	}
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATA_DIR") == "" {
		log.Fatal("DATABASE_URL and DATA_DIR are required")
	}
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}
	f := fixture{db: db, files: os.Getenv("DATA_DIR"), writable: os.Getenv("WRITE_ENABLED") == "true"}
	switch os.Args[1] {
	case "migrate":
		err = f.migrate(ctx)
	case "serve":
		server := &http.Server{Addr: ":8080", Handler: f.routes(), ReadHeaderTimeout: 5 * time.Second}
		err = server.ListenAndServe()
	case "worker":
		err = f.ackOne(ctx)
	case "tick":
		err = f.tick(ctx, time.Now().UTC())
	default:
		log.Fatal("unknown mode")
	}
	if err != nil {
		log.Fatal(err)
	}
}

func (f fixture) migrate(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mobility_items (
		id text PRIMARY KEY, sha256 text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
	);
	CREATE TABLE IF NOT EXISTS mobility_jobs (
		id text PRIMARY KEY REFERENCES mobility_items(id), acked_at timestamptz
	);
	CREATE TABLE IF NOT EXISTS mobility_ticks (
		minute timestamptz PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now()
	);`)
	return err
}

func (f fixture) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := f.db.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /items", f.createItem)
	mux.HandleFunc("GET /state", f.state)
	return mux
}

func (f fixture) requireWriter(w http.ResponseWriter) bool {
	if !f.writable {
		http.Error(w, "writer fenced", http.StatusLocked)
		return false
	}
	return true
}

func (f fixture) createItem(w http.ResponseWriter, r *http.Request) {
	if !f.requireWriter(w) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		http.Error(w, "item body must be 1..1048576 bytes", http.StatusBadRequest)
		return
	}
	id, err := randomID()
	if err != nil {
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
		return
	}
	digest := sha256.Sum256(body)
	if err := os.MkdirAll(f.files, 0o700); err != nil {
		http.Error(w, "file store unavailable", http.StatusServiceUnavailable)
		return
	}
	path := filepath.Join(f.files, id)
	if err := writeFile(path, body); err != nil {
		http.Error(w, "file write failed", http.StatusServiceUnavailable)
		return
	}
	tx, err := f.db.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO mobility_items(id,sha256) VALUES ($1,$2)`, id, hex.EncodeToString(digest[:]))
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO mobility_jobs(id) VALUES ($1)`, id)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	if err != nil {
		// An orphaned file is visible to inventory and must be reconciled before
		// cutover; never pretend this partial write committed an item.
		http.Error(w, "database write failed", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "sha256": hex.EncodeToString(digest[:])})
}

func (f fixture) ackOne(ctx context.Context) error {
	if !f.writable {
		return errors.New("worker fenced")
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM mobility_jobs WHERE acked_at IS NULL ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mobility_jobs SET acked_at=now() WHERE id=$1 AND acked_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (f fixture) tick(ctx context.Context, now time.Time) error {
	if !f.writable {
		return errors.New("schedule fenced")
	}
	_, err := f.db.ExecContext(ctx, `INSERT INTO mobility_ticks(minute) VALUES ($1) ON CONFLICT DO NOTHING`, now.Truncate(time.Minute))
	return err
}

func (f fixture) state(w http.ResponseWriter, r *http.Request) {
	type item struct{ ID, SHA256 string }
	rows, err := f.db.QueryContext(r.Context(), `SELECT id, sha256 FROM mobility_items ORDER BY id`)
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	defer rows.Close()
	items := make([]item, 0)
	for rows.Next() {
		var entry item
		if err := rows.Scan(&entry.ID, &entry.SHA256); err != nil {
			http.Error(w, "database read failed", http.StatusServiceUnavailable)
			return
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database read failed", http.StatusServiceUnavailable)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, "database read failed", http.StatusServiceUnavailable)
		return
	}
	var pending, acked, ticks int64
	if err := f.db.QueryRowContext(r.Context(), `SELECT count(*) FILTER (WHERE acked_at IS NULL), count(*) FILTER (WHERE acked_at IS NOT NULL) FROM mobility_jobs`).Scan(&pending, &acked); err != nil {
		http.Error(w, "job read failed", http.StatusServiceUnavailable)
		return
	}
	if err := f.db.QueryRowContext(r.Context(), `SELECT count(*) FROM mobility_ticks`).Scan(&ticks); err != nil {
		http.Error(w, "schedule read failed", http.StatusServiceUnavailable)
		return
	}
	missing, mismatched := 0, 0
	for _, entry := range items {
		body, err := os.ReadFile(filepath.Join(f.files, entry.ID))
		if err != nil {
			missing++
			continue
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			mismatched++
		}
	}
	files, err := os.ReadDir(f.files)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "file inventory failed", http.StatusServiceUnavailable)
		return
	}
	orphans := 0
	known := make(map[string]bool, len(items))
	for _, entry := range items {
		known[entry.ID] = true
	}
	for _, file := range files {
		if !file.IsDir() && !known[file.Name()] {
			orphans++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"schema": "norn.mobility-fixture/v1", "items": items, "jobsPending": pending, "jobsAcknowledged": acked, "scheduleTicks": ticks, "filesMissing": missing, "filesMismatched": mismatched, "filesOrphaned": orphans, "writerEnabled": f.writable})
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func writeFile(path string, body []byte) error {
	f, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
