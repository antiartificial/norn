package handler

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ReadMetricsToken loads the dedicated, route-only scrape credential. It must
// be a regular owner-only file owned by the Norn service account.
func ReadMetricsToken(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("Norn metrics credential path is not configured")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", errors.New("Norn metrics credential must be a regular mode-0600 file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("Norn metrics credential must be owned by the service account")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("Norn metrics credential cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("Norn metrics credential changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return "", errors.New("Norn metrics credential could not be read")
	}
	token := strings.TrimSuffix(string(raw), "\n")
	if len(token) < 32 || token != strings.TrimSpace(token) || strings.ContainsAny(token, "\r\n\t") {
		return "", errors.New("Norn metrics credential is malformed")
	}
	return token, nil
}

// MetricsTokenAuth limits the static scrape credential to the metrics route;
// it does not create a general Norn API principal or grant api:read elsewhere.
func MetricsTokenAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		provided := strings.TrimPrefix(authorization, "Bearer ")
		if token == "" || !strings.HasPrefix(authorization, "Bearer ") || len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			WriteControlProblem(w, r, http.StatusUnauthorized, "metrics_unauthorized", "a valid metrics-only bearer credential is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
