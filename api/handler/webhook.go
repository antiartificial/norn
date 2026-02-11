package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"norn/api/hub"
	"norn/api/model"
)

type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
	} `json:"repository"`
}

func (h *Handler) WebhookPush(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Detect provider from headers
	var provider string
	var signature string
	if r.Header.Get("X-Gitea-Event") == "push" {
		provider = "gitea"
		signature = r.Header.Get("X-Gitea-Signature")
	} else if r.Header.Get("X-GitHub-Event") == "push" {
		provider = "github"
		// X-Hub-Signature-256 format: sha256=<hex>
		sig := r.Header.Get("X-Hub-Signature-256")
		if strings.HasPrefix(sig, "sha256=") {
			signature = sig[7:]
		}
	} else {
		http.Error(w, "unsupported webhook event", http.StatusBadRequest)
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	// Extract branch from ref (refs/heads/main → main)
	branch := ""
	if strings.HasPrefix(payload.Ref, "refs/heads/") {
		branch = payload.Ref[len("refs/heads/"):]
	}
	if branch == "" {
		http.Error(w, "not a branch push", http.StatusBadRequest)
		return
	}

	// Find matching app
	specs, err := h.discoverApps()
	if err != nil {
		http.Error(w, "failed to discover apps", http.StatusInternalServerError)
		return
	}

	var matched *model.InfraSpec
	for _, spec := range specs {
		if spec.Repo == nil {
			continue
		}
		if !spec.Repo.AutoDeploy {
			continue
		}
		if spec.Repo.Branch != branch {
			continue
		}
		if matchesRepoURL(spec.Repo.URL, payload.Repository.CloneURL, payload.Repository.SSHURL) {
			matched = spec
			break
		}
	}

	if matched == nil {
		http.Error(w, "no matching app found", http.StatusNotFound)
		return
	}

	// Verify HMAC signature
	if matched.Repo.WebhookSecret != "" {
		if signature == "" || !verifySignature(body, matched.Repo.WebhookSecret, signature) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}
	}

	_ = provider // provider detected above, used for logging context

	// Trigger deploy
	deploy := &model.Deployment{
		ID:        uuid.NewString(),
		App:       matched.App,
		CommitSHA: payload.After,
		ImageTag:  fmt.Sprintf("%s:%s", matched.App, payload.After[:12]),
		Status:    model.StatusQueued,
		StartedAt: time.Now(),
	}

	if err := h.db.InsertDeployment(r.Context(), deploy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "deploy.webhook", AppID: matched.App, Payload: map[string]string{
		"commitSha": payload.After,
		"branch":    branch,
		"provider":  provider,
	}})
	h.ws.Broadcast(hub.Event{Type: "deploy.queued", AppID: matched.App, Payload: deploy})

	go h.runPipeline(deploy, matched)

	writeJSON(w, map[string]string{
		"status": "deploying",
		"app":    matched.App,
		"commit": payload.After,
	})
}

func verifySignature(payload []byte, secret, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func matchesRepoURL(specURL, cloneURL, sshURL string) bool {
	return normalizeURL(specURL) == normalizeURL(cloneURL) ||
		normalizeURL(specURL) == normalizeURL(sshURL)
}

func normalizeURL(u string) string {
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimRight(u, "/")
	return strings.ToLower(u)
}
