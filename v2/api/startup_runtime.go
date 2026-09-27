package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/startup"
	"norn/v2/api/store"
)

type schemaStatusPayload struct {
	store.SchemaStatus
	Version                  string                 `json:"version"`
	StartupContract          string                 `json:"startupContract"`
	BinarySchemaContract     startup.SchemaContract `json:"binarySchemaContract"`
	ProcessID                int                    `json:"processId"`
	ProcessInstanceID        string                 `json:"processInstanceId"`
	DatabaseIdentity         string                 `json:"databaseIdentity"`
	StartupMode              string                 `json:"startupMode"`
	SchemaMode               string                 `json:"schemaMode"`
	OperationRecoveryEnabled bool                   `json:"operationRecoveryEnabled"`
	OperationWorkerEnabled   bool                   `json:"operationWorkerEnabled"`
	NomadWatcherEnabled      bool                   `json:"nomadWatcherEnabled"`
}

var (
	processInstanceIDOnce sync.Once
	processInstanceID     string
)

func currentProcessInstanceID() string {
	processInstanceIDOnce.Do(func() {
		processInstanceID = uuid.NewString()
	})
	return processInstanceID
}

func validatePassiveBind(bind string) error {
	bind = strings.TrimSpace(bind)
	if strings.EqualFold(bind, "localhost") {
		return nil
	}
	ip := net.ParseIP(bind)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s=passive requires a loopback NORN_BIND_ADDR", startup.StartupModeEnv)
	}
	return nil
}

func schemaPayload(version, databaseID string, status store.SchemaStatus, cfg startup.Config) schemaStatusPayload {
	active := cfg.StartupMode == startup.ModeActive
	return schemaStatusPayload{
		SchemaStatus:             status,
		Version:                  version,
		StartupContract:          startup.ContractName,
		BinarySchemaContract:     startup.CurrentSchemaContract(),
		ProcessID:                os.Getpid(),
		ProcessInstanceID:        currentProcessInstanceID(),
		DatabaseIdentity:         databaseID,
		StartupMode:              string(cfg.StartupMode),
		SchemaMode:               string(cfg.SchemaMode),
		OperationRecoveryEnabled: active && os.Getenv("NORN_SKIP_OPERATION_RECOVERY") != "true",
		OperationWorkerEnabled:   active && os.Getenv("NORN_SKIP_OPERATION_WORKER") != "true",
		NomadWatcherEnabled:      active && os.Getenv("NORN_SKIP_NOMAD_WATCHER") != "true",
	}
}

func databaseIdentity(databaseURL, signingKey string) string {
	if databaseURL == "" || len(signingKey) < 32 {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(signingKey))
	_, _ = mac.Write([]byte("norn.database-identity/v1\x00"))
	_, _ = mac.Write([]byte(databaseURL))
	return fmt.Sprintf("hmac-sha256:%x", mac.Sum(nil))
}

func newPassiveHandler(version, databaseID string, status store.SchemaStatus, cfg startup.Config) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"status": "passive",
			"schema": schemaPayload(version, databaseID, status, cfg),
		})
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"version": version})
	})
	mux.HandleFunc("GET /api/schema", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, schemaPayload(version, databaseID, status, cfg))
	})
	return mux
}

func newPassiveServer(bindAddr, port, version, databaseID string, status store.SchemaStatus, cfg startup.Config) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort(bindAddr, port),
		Handler:           newPassiveHandler(version, databaseID, status, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
}
