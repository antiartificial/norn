package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var version = "dev"

type server struct {
	db           *sql.DB
	requests     atomic.Uint64
	databaseErrs atomic.Uint64
	cacheMu      sync.Mutex
	cachedHits   int64
	cacheUntil   time.Time
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if configured := os.Getenv("APP_VERSION"); configured != "" {
		version = configured
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("database ping: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ha_toy_hits (
		id bigserial PRIMARY KEY, app_version text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		log.Fatalf("database migration: %v", err)
	}

	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.hit)
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/metrics", s.metrics)
	log.Printf("norn HA toy app %s listening on :8080", version)
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func (s *server) hit(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ha_toy_hits(app_version) VALUES ($1)`, version); err != nil {
		s.databaseErrs.Add(1)
		http.Error(w, "database write failed", http.StatusServiceUnavailable)
		return
	}
	hits, cacheHit, err := s.totalHits(ctx)
	if err != nil {
		s.databaseErrs.Add(1)
		http.Error(w, "database read failed", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema": "norn.ha-toy/v1", "version": version, "hits": hits,
		"cache_hit": cacheHit, "host": os.Getenv("NOMAD_ALLOC_ID"),
	})
}

func (s *server) totalHits(ctx context.Context) (int64, bool, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if time.Now().Before(s.cacheUntil) {
		return s.cachedHits, true, nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ha_toy_hits`).Scan(&s.cachedHits); err != nil {
		return 0, false, err
	}
	s.cacheUntil = time.Now().Add(5 * time.Second)
	return s.cachedHits, false, nil
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func (s *server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE norn_ha_toy_requests_total counter\nnorn_ha_toy_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# TYPE norn_ha_toy_database_errors_total counter\nnorn_ha_toy_database_errors_total %d\n", s.databaseErrs.Load())
}
