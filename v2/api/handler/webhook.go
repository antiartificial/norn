package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	if provider != "github" && provider != "gitea" {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	secret := h.cfg.WebhookSecret
	if secret == "" {
		writeError(w, http.StatusInternalServerError, "webhook secret not configured")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// Verify HMAC signature
	var sigHeader string
	switch provider {
	case "github":
		sigHeader = r.Header.Get("X-Hub-Signature-256")
	case "gitea":
		sigHeader = r.Header.Get("X-Gitea-Signature")
	}

	if !verifySignature(body, secret, provider, sigHeader) {
		writeError(w, http.StatusForbidden, "invalid signature")
		return
	}

	// Check event type — only handle push
	var eventHeader string
	switch provider {
	case "github":
		eventHeader = r.Header.Get("X-GitHub-Event")
	case "gitea":
		eventHeader = r.Header.Get("X-Gitea-Event")
	}

	if eventHeader != "push" {
		writeJSON(w, map[string]bool{"ignored": true})
		return
	}

	// Parse push payload
	var payload struct {
		Ref        string `json:"ref"`
		Repository struct {
			CloneURL string `json:"clone_url"`
			SSHURL   string `json:"ssh_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")

	// Discover apps and find match
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to discover apps")
		return
	}

	// Try clone_url first, then ssh_url
	spec := model.FindByRepoURL(specs, payload.Repository.CloneURL, branch)
	if spec == nil && payload.Repository.SSHURL != "" {
		spec = model.FindByRepoURL(specs, payload.Repository.SSHURL, branch)
	}

	if spec == nil {
		writeJSON(w, map[string]bool{"matched": false})
		return
	}

	log.Printf("webhook: auto-deploying %s (branch %s, provider %s)", spec.App, branch, provider)

	sagaID := h.pipeline.Run(spec, payload.Ref)

	writeJSON(w, map[string]string{
		"sagaId": sagaID,
		"app":    spec.App,
		"status": "deploying",
	})
}

func verifySignature(body []byte, secret, provider, sigHeader string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	switch provider {
	case "github":
		// GitHub sends: sha256=<hex>
		return sigHeader == "sha256="+expected
	case "gitea":
		// Gitea sends: <hex> (no prefix)
		return hmac.Equal([]byte(sigHeader), []byte(expected))
	}
	return false
}
