package main

// The durability pilot is intentionally small but models a production-safe
// pattern: a PostgreSQL transactional outbox provides at-least-once delivery;
// the worker records a message key before its side effect, making duplicate
// Redpanda deliveries harmless; Valkey is a disposable read-through cache and
// is never the source of truth.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

var version = "dev"

type config struct {
	databaseURL string
	valkeyAddr  string
	brokers     []string
	topic       string
}

type metrics struct {
	requests         atomic.Uint64
	outboxPublished  atomic.Uint64
	workerCompleted  atomic.Uint64
	cacheErrors      atomic.Uint64
	dependencyErrors atomic.Uint64
}

type pilot struct {
	db       *sql.DB
	cache    *redis.Client
	producer *kgo.Client
	config   config
	metrics  metrics
}

type jobInput struct {
	Work string `json:"work"`
}

type jobStatus struct {
	Key       string `json:"key"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	Completed bool   `json:"completed"`
}

func main() {
	if configured := os.Getenv("APP_VERSION"); configured != "" {
		version = configured
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	p, err := newPilot(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer p.close()

	mode := "api"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "api":
		p.serve()
	case "worker":
		if err := p.work(context.Background()); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("usage: durability-pilot [api|worker], got %q", mode)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		databaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		valkeyAddr:  strings.TrimSpace(os.Getenv("VALKEY_ADDR")),
		topic:       strings.TrimSpace(os.Getenv("PILOT_TOPIC")),
	}
	if brokers := strings.TrimSpace(os.Getenv("KAFKA_BROKERS")); brokers != "" {
		cfg.brokers = strings.Split(brokers, ",")
	}
	if cfg.topic == "" {
		cfg.topic = "norn-durability-work"
	}
	if cfg.databaseURL == "" || cfg.valkeyAddr == "" || len(cfg.brokers) == 0 {
		return config{}, errors.New("DATABASE_URL, VALKEY_ADDR, and KAFKA_BROKERS are required")
	}
	return cfg, nil
}

func newPilot(ctx context.Context, cfg config) (*pilot, error) {
	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	cache := redis.NewClient(&redis.Options{Addr: cfg.valkeyAddr, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	producer, err := kgo.NewClient(kgo.SeedBrokers(cfg.brokers...), kgo.DefaultProduceTopic(cfg.topic), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		_ = cache.Close()
		_ = db.Close()
		return nil, fmt.Errorf("configure queue client: %w", err)
	}
	p := &pilot{
		db:       db,
		cache:    cache,
		producer: producer,
		config:   cfg,
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.db.PingContext(ready); err != nil {
		p.close()
		return nil, fmt.Errorf("database ping: %w", err)
	}
	if err := p.migrate(ready); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *pilot) close() {
	if p.producer != nil {
		p.producer.Close()
	}
	if p.cache != nil {
		_ = p.cache.Close()
	}
	if p.db != nil {
		_ = p.db.Close()
	}
}

func (p *pilot) migrate(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS durability_jobs (
  job_key text PRIMARY KEY,
  payload jsonb NOT NULL,
  status text NOT NULL DEFAULT 'queued',
  attempts integer NOT NULL DEFAULT 0,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS durability_outbox (
  job_key text PRIMARY KEY REFERENCES durability_jobs(job_key) ON DELETE CASCADE,
  payload jsonb NOT NULL,
  published_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS durability_processed_messages (
  job_key text PRIMARY KEY,
  processed_at timestamptz NOT NULL DEFAULT now()
);`)
	if err != nil {
		return fmt.Errorf("migrate durability schema: %w", err)
	}
	return nil
}

func (p *pilot) serve() {
	go p.publishLoop(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", p.live)
	mux.HandleFunc("GET /readyz", p.ready)
	mux.HandleFunc("POST /v1/jobs", p.submit)
	mux.HandleFunc("GET /v1/jobs/{key}", p.status)
	mux.HandleFunc("GET /metrics", p.prometheus)
	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Printf("norn durability pilot %s listening on :8080", version)
	log.Fatal(server.ListenAndServe())
}

func (p *pilot) live(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
}

func (p *pilot) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := p.db.PingContext(ctx); err != nil {
		p.metrics.dependencyErrors.Add(1)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := p.cache.Ping(ctx).Err(); err != nil {
		p.metrics.dependencyErrors.Add(1)
		http.Error(w, "cache unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := p.producer.Ping(ctx); err != nil {
		p.metrics.dependencyErrors.Add(1)
		http.Error(w, "queue unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{\"status\":\"ready\"}\n"))
}

func (p *pilot) submit(w http.ResponseWriter, r *http.Request) {
	p.metrics.requests.Add(1)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		http.Error(w, "Idempotency-Key must be 8-128 characters", http.StatusBadRequest)
		return
	}
	var input jobInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.Work) > 2048 {
		http.Error(w, "expected a bounded {work} JSON body", http.StatusBadRequest)
		return
	}
	payload, _ := json.Marshal(input)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO durability_jobs(job_key, payload) VALUES ($1, $2::jsonb) ON CONFLICT (job_key) DO NOTHING`, key, payload); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO durability_outbox(job_key, payload) VALUES ($1, $2::jsonb) ON CONFLICT (job_key) DO NOTHING`, key, payload)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		http.Error(w, "durable enqueue failed", http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema": "norn.durability-pilot/v1", "key": key, "status": "queued"})
}

func (p *pilot) status(w http.ResponseWriter, r *http.Request) {
	p.metrics.requests.Add(1)
	key := r.PathValue("key")
	if len(key) < 8 || len(key) > 128 {
		http.Error(w, "job key must be 8-128 characters", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if cached, err := p.cache.Get(ctx, cacheKey(key)).Bytes(); err == nil {
		w.Header().Set("X-Cache", "hit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cached)
		return
	} else if err != redis.Nil {
		p.metrics.cacheErrors.Add(1)
	}
	var result jobStatus
	var completedAt sql.NullTime
	err := p.db.QueryRowContext(ctx, `SELECT job_key, status, attempts, completed_at FROM durability_jobs WHERE job_key = $1`, key).Scan(&result.Key, &result.Status, &result.Attempts, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	result.Completed = completedAt.Valid
	body, _ := json.Marshal(result)
	if err := p.cache.Set(ctx, cacheKey(key), body, 30*time.Second).Err(); err != nil {
		p.metrics.cacheErrors.Add(1)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(body, '\n'))
}

func (p *pilot) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.publishOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *pilot) publishOnce(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `SELECT job_key, payload FROM durability_outbox WHERE published_at IS NULL ORDER BY created_at LIMIT 32`)
	if err != nil {
		p.metrics.dependencyErrors.Add(1)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var payload []byte
		if rows.Scan(&key, &payload) != nil {
			return
		}
		produceCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := p.producer.ProduceSync(produceCtx, &kgo.Record{Key: []byte(key), Value: payload}).FirstErr()
		cancel()
		if err != nil {
			p.metrics.dependencyErrors.Add(1)
			return
		}
		if _, err := p.db.ExecContext(ctx, `UPDATE durability_outbox SET published_at = now() WHERE job_key = $1 AND published_at IS NULL`, key); err == nil {
			p.metrics.outboxPublished.Add(1)
		}
	}
}

func (p *pilot) work(ctx context.Context) error {
	consumer, err := kgo.NewClient(kgo.SeedBrokers(p.config.brokers...), kgo.ConsumerGroup("norn-durability-worker"), kgo.ConsumeTopics(p.config.topic), kgo.DisableAutoCommit())
	if err != nil {
		return err
	}
	defer consumer.Close()
	for {
		fetches := consumer.PollFetches(ctx)
		if err := fetches.Err(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("queue fetch: %v", err)
		}
		for _, record := range fetches.Records() {
			if err := p.process(ctx, string(record.Key)); err != nil {
				log.Printf("process %q: %v", record.Key, err)
				// Do not process or commit a later offset from this fetch. Doing so
				// could advance the consumer group past the failed record.
				break
			}
			if err := consumer.CommitRecords(ctx, record); err != nil {
				log.Printf("commit %q: %v", record.Key, err)
				break
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (p *pilot) process(ctx context.Context, key string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var inserted string
	err = tx.QueryRowContext(ctx, `INSERT INTO durability_processed_messages(job_key) VALUES ($1) ON CONFLICT DO NOTHING RETURNING job_key`, key).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE durability_jobs SET status = 'completed', attempts = attempts + 1, completed_at = now() WHERE job_key = $1`, key); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	p.metrics.workerCompleted.Add(1)
	_ = p.cache.Del(ctx, cacheKey(key)).Err()
	return nil
}

func (p *pilot) prometheus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# TYPE norn_durability_requests_total counter\nnorn_durability_requests_total %d\n", p.metrics.requests.Load())
	fmt.Fprintf(w, "# TYPE norn_durability_outbox_published_total counter\nnorn_durability_outbox_published_total %d\n", p.metrics.outboxPublished.Load())
	fmt.Fprintf(w, "# TYPE norn_durability_worker_completed_total counter\nnorn_durability_worker_completed_total %d\n", p.metrics.workerCompleted.Load())
	fmt.Fprintf(w, "# TYPE norn_durability_cache_errors_total counter\nnorn_durability_cache_errors_total %d\n", p.metrics.cacheErrors.Load())
	fmt.Fprintf(w, "# TYPE norn_durability_dependency_errors_total counter\nnorn_durability_dependency_errors_total %d\n", p.metrics.dependencyErrors.Load())
}

func cacheKey(key string) string { return "norn:durability:job:" + key }
