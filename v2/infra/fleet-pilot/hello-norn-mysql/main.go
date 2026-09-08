package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
)

var version = "development"
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

type service struct {
	db         *sql.DB
	writeToken []byte
	requests   atomic.Uint64
	failures   atomic.Uint64
}

func openDatabase() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(os.Getenv("MYSQL_DSN"))
	if err != nil {
		return nil, fmt.Errorf("invalid MYSQL_DSN")
	}
	if cfg.Net != "tcp" || cfg.Addr == "" || cfg.DBName == "" {
		return nil, fmt.Errorf("MYSQL_DSN requires TCP address and database")
	}
	tlsConfig, err := mysqlTLSConfig(cfg.Addr, os.Getenv("MYSQL_CA_FILE"))
	if err != nil {
		return nil, err
	}
	if err := mysql.RegisterTLSConfig("pilot-verified", tlsConfig); err != nil {
		return nil, err
	}
	dial, err := mysqlPinnedDialer(cfg.Addr, os.Getenv("MYSQL_PINNED_IP"), (&net.Dialer{}).DialContext)
	if err != nil {
		return nil, err
	}
	mysql.RegisterDialContext("pilot-pinned", dial)
	cfg.Net = "pilot-pinned"
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

type contextDialer func(context.Context, string, string) (net.Conn, error)

func mysqlPinnedDialer(expectedAddress, pinnedIP string, dial contextDialer) (mysql.DialContextFunc, error) {
	_, port, err := mysqlDNSAddress(expectedAddress)
	if err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, fmt.Errorf("MySQL pinned dialer is unavailable")
	}
	pinned, err := netip.ParseAddr(pinnedIP)
	if err != nil || !pinned.Is4() || !pinned.IsPrivate() || pinned.String() != pinnedIP {
		return nil, fmt.Errorf("MYSQL_PINNED_IP requires a canonical RFC1918 IPv4 literal")
	}
	endpoint := netip.AddrPortFrom(pinned, uint16(port)).String()
	return func(ctx context.Context, address string) (net.Conn, error) {
		if address != expectedAddress {
			return nil, fmt.Errorf("MySQL address changed after review")
		}
		conn, err := dial(ctx, "tcp4", endpoint)
		if err != nil {
			return nil, err
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok {
			conn.Close()
			return nil, fmt.Errorf("MySQL peer differs from reviewed private endpoint")
		}
		peerIP, validPeer := netip.AddrFromSlice(peer.IP)
		if !validPeer || peerIP.Unmap() != pinned || peer.Port != int(port) {
			conn.Close()
			return nil, fmt.Errorf("MySQL peer differs from reviewed private endpoint")
		}
		return conn, nil
	}, nil
}

func mysqlDNSAddress(address string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || !validMySQLDNSHost(host) {
		return "", 0, fmt.Errorf("MYSQL_DSN requires a DNS hostname and port")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", 0, fmt.Errorf("MYSQL_DSN requires a valid TCP port")
	}
	return host, uint16(port), nil
}

func validMySQLDNSHost(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) > 1 {
		numeric := true
		for _, label := range labels {
			if label == "" {
				return false
			}
			for _, char := range label {
				if char < '0' || char > '9' {
					numeric = false
				}
			}
		}
		if numeric {
			return false
		}
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func mysqlTLSConfig(address, path string) (*tls.Config, error) {
	if path == "" {
		return nil, fmt.Errorf("MYSQL_CA_FILE is required")
	}
	pem, err := os.ReadFile(path)
	if err != nil || len(pem) == 0 || len(pem) > 1<<20 {
		return nil, fmt.Errorf("read MySQL provider CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid MySQL provider CA")
	}
	host, _, err := mysqlDNSAddress(address)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: host}, nil
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	// healthz deliberately has no dependency on MySQL. It lets Nomad restart a
	// wedged process without turning a transient managed-database issue into a
	// restart storm.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if os.Getenv("PILOT_FAIL_READINESS") == "true" || s.db == nil || s.db.PingContext(ctx) != nil {
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
	if r.Method == http.MethodPut && !s.authorizedWrite(r) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "write authorization required", http.StatusUnauthorized)
		return
	}
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

func (s *service) authorizedWrite(r *http.Request) bool {
	const bearer = "Bearer "
	provided := r.Header.Get("Authorization")
	if !strings.HasPrefix(provided, bearer) || len(s.writeToken) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(provided, bearer)), s.writeToken) == 1
}

func pilotWriteToken(value string) ([]byte, error) {
	if len(value) < 32 || len(value) > 256 {
		return nil, fmt.Errorf("PILOT_WRITE_TOKEN must be 32 to 256 bytes")
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return nil, fmt.Errorf("PILOT_WRITE_TOKEN must be printable without whitespace")
		}
	}
	return []byte(value), nil
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
	writeToken, err := pilotWriteToken(os.Getenv("PILOT_WRITE_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}
	s := &service{db: db, writeToken: writeToken}
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
