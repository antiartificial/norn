package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
)

var version = "development"
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

type service struct {
	db       *sql.DB
	requests atomic.Uint64
	failures atomic.Uint64
}

func openDatabase() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(os.Getenv("MYSQL_DSN"))
	if err != nil {
		return nil, fmt.Errorf("invalid MYSQL_DSN")
	}
	if cfg.Net != "tcp" || cfg.Addr == "" || cfg.DBName == "" {
		return nil, fmt.Errorf("MYSQL_DSN requires TCP address and database")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	if err := appendMySQLCAs(roots, os.Getenv("MYSQL_CA_FILE"), os.Getenv("MYSQL_CA_PEM")); err != nil {
		return nil, err
	}
	if err := mysql.RegisterTLSConfig("pilot-verified", &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}); err != nil {
		return nil, err
	}
	cfg.TLSConfig = "pilot-verified"
	cfg.AllowAllFiles = false
	cfg.MultiStatements = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open MySQL failed")
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func appendMySQLCAs(roots *x509.CertPool, path, inline string) error {
	if path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read MySQL CA file")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return fmt.Errorf("invalid MySQL CA")
		}
	}
	if inline != "" && !roots.AppendCertsFromPEM([]byte(inline)) {
		return fmt.Errorf("invalid MySQL CA")
	}
	return nil
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if os.Getenv("PILOT_FAIL_READINESS") == "true" || s.db.PingContext(ctx) != nil {
			http.Error(w, "not ready", 503)
			return
		}
		rows, err := s.db.QueryContext(ctx, "SELECT id FROM pilot_records LIMIT 0")
		if err != nil {
			http.Error(w, "schema not ready", 503)
			return
		}
		rows.Close()
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"version": version, "allocation": os.Getenv("NOMAD_ALLOC_ID"), "region": os.Getenv("NOMAD_REGION")})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE pilot_requests_total counter\npilot_requests_total %d\n# TYPE pilot_failures_total counter\npilot_failures_total %d\n", s.requests.Load(), s.failures.Load())
	})
	mux.HandleFunc("PUT /records/{id}", s.record)
	mux.HandleFunc("GET /records/{id}", s.record)
	return mux
}

func (s *service) record(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		http.Error(w, "invalid id", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if r.Method == http.MethodPut {
		// The ID is the immutable payload; retrying a request cannot duplicate it.
		if _, err := s.db.ExecContext(ctx, "INSERT INTO pilot_records (id) VALUES (?) ON DUPLICATE KEY UPDATE id=id", id); err != nil {
			s.failures.Add(1)
			http.Error(w, "database unavailable", 503)
			return
		}
	}
	var stored string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM pilot_records WHERE id=?", id).Scan(&stored)
	if err == sql.ErrNoRows {
		http.Error(w, "not found", 404)
		return
	}
	if err != nil {
		s.failures.Add(1)
		http.Error(w, "database unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"id": stored, "version": version, "allocation": os.Getenv("NOMAD_ALLOC_ID")})
}

func main() {
	db, err := openDatabase()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if len(os.Args) == 2 && os.Args[1] == "migrate" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS pilot_records (id VARCHAR(96) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))"); err != nil {
			log.Fatal("migration failed")
		}
		return
	}
	s := &service{db: db}
	server := &http.Server{Addr: ":8080", Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		drain, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		server.Shutdown(drain)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
