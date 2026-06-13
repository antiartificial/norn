package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/model"
)

func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	if provider != "github" && provider != "gitea" {
		writeError(w, http.StatusBadRequest, "unsupported provider")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// Check event type and record the delivery before validation decisions.
	var sigHeader string
	var eventHeader string
	var deliveryID string
	switch provider {
	case "github":
		sigHeader = r.Header.Get("X-Hub-Signature-256")
		eventHeader = r.Header.Get("X-GitHub-Event")
		deliveryID = r.Header.Get("X-GitHub-Delivery")
	case "gitea":
		sigHeader = r.Header.Get("X-Gitea-Signature")
		eventHeader = r.Header.Get("X-Gitea-Event")
		deliveryID = r.Header.Get("X-Gitea-Delivery")
	}

	delivery := &model.WebhookDelivery{
		ID:         uuid.New().String(),
		Provider:   provider,
		Event:      eventHeader,
		DeliveryID: deliveryID,
		Status:     "received",
		RemoteAddr: r.RemoteAddr,
		UserAgent:  r.UserAgent(),
		ReceivedAt: time.Now(),
		Metadata: map[string]interface{}{
			"contentLength": len(body),
		},
	}
	if h.db != nil {
		if err := h.db.InsertWebhookDelivery(r.Context(), delivery); err != nil {
			log.Printf("webhook: insert delivery: %v", err)
		}
	}

	secret := h.cfg.WebhookSecret
	if secret == "" {
		h.finishWebhookDelivery(r, delivery, "failed", "webhook secret not configured")
		writeError(w, http.StatusInternalServerError, "webhook secret not configured")
		return
	}

	// Verify HMAC signature
	if !verifySignature(body, secret, provider, sigHeader) {
		h.finishWebhookDelivery(r, delivery, "failed", "invalid signature")
		writeError(w, http.StatusForbidden, "invalid signature")
		return
	}

	if eventHeader != "push" {
		h.finishWebhookDelivery(r, delivery, "ignored", "unsupported event: "+eventHeader)
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
		h.finishWebhookDelivery(r, delivery, "failed", "invalid payload")
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	delivery.Ref = payload.Ref
	delivery.Branch = branch
	delivery.Repository = payload.Repository.CloneURL
	delivery.Payload = map[string]interface{}{
		"ref":       payload.Ref,
		"clone_url": payload.Repository.CloneURL,
		"ssh_url":   payload.Repository.SSHURL,
		"branch":    branch,
	}

	// Discover apps and find match
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		h.finishWebhookDelivery(r, delivery, "failed", "failed to discover apps")
		writeError(w, http.StatusInternalServerError, "failed to discover apps")
		return
	}

	// Try clone_url first, then ssh_url
	spec := model.FindByRepoURL(specs, payload.Repository.CloneURL, branch)
	if spec == nil && payload.Repository.SSHURL != "" {
		spec = model.FindByRepoURL(specs, payload.Repository.SSHURL, branch)
	}

	if spec == nil {
		h.finishWebhookDelivery(r, delivery, "ignored", "no matching app")
		writeJSON(w, map[string]bool{"matched": false})
		return
	}

	log.Printf("webhook: auto-deploying %s (branch %s, provider %s)", spec.App, branch, provider)

	sagaID := h.pipeline.Run(spec, payload.Ref)
	delivery.App = spec.App
	delivery.SagaID = sagaID
	h.finishWebhookDelivery(r, delivery, "deploying", "matched app "+spec.App)

	writeJSON(w, map[string]string{
		"sagaId": sagaID,
		"app":    spec.App,
		"status": "queued",
	})
}

func (h *Handler) ReplayWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = "deploy"
	}
	if req.Mode != "deploy" && req.Mode != "preflight" {
		writeError(w, http.StatusBadRequest, "mode must be deploy or preflight")
		return
	}

	delivery, err := h.db.GetWebhookDelivery(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if delivery == nil {
		writeError(w, http.StatusNotFound, "webhook delivery not found")
		return
	}

	spec, err := h.specForWebhookDelivery(delivery)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ref := delivery.Ref
	if ref == "" {
		ref = mapString(delivery.Payload, "ref")
	}
	if ref == "" {
		ref = "HEAD"
	}

	var sagaID string
	if req.Mode == "preflight" {
		sagaID = h.pipeline.Preflight(spec, ref)
	} else {
		sagaID = h.pipeline.Run(spec, ref)
	}

	delivery.App = spec.App
	delivery.Ref = ref
	if delivery.Branch == "" {
		delivery.Branch = strings.TrimPrefix(ref, "refs/heads/")
	}
	delivery.SagaID = sagaID
	delivery.Status = "replayed"
	delivery.Reason = "replayed as " + req.Mode
	if delivery.Metadata == nil {
		delivery.Metadata = map[string]interface{}{}
	}
	delivery.Metadata["replayMode"] = req.Mode
	delivery.Metadata["replayedAt"] = time.Now().UTC().Format(time.RFC3339)
	if err := h.db.UpdateWebhookDelivery(r.Context(), delivery); err != nil {
		log.Printf("webhook: replay update delivery %s: %v", delivery.ID, err)
	}

	writeJSON(w, map[string]string{
		"sagaId": sagaID,
		"app":    spec.App,
		"mode":   req.Mode,
		"status": "queued",
	})
}

func (h *Handler) specForWebhookDelivery(delivery *model.WebhookDelivery) (*model.InfraSpec, error) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		return nil, err
	}
	if delivery.App != "" {
		for _, spec := range specs {
			if spec.App == delivery.App {
				return spec, nil
			}
		}
	}
	branch := delivery.Branch
	if branch == "" && delivery.Ref != "" {
		branch = strings.TrimPrefix(delivery.Ref, "refs/heads/")
	}
	cloneURL := delivery.Repository
	if cloneURL == "" {
		cloneURL = mapString(delivery.Payload, "clone_url")
	}
	sshURL := mapString(delivery.Payload, "ssh_url")
	if cloneURL != "" {
		if spec := model.FindByRepoURL(specs, cloneURL, branch); spec != nil {
			return spec, nil
		}
	}
	if sshURL != "" {
		if spec := model.FindByRepoURL(specs, sshURL, branch); spec != nil {
			return spec, nil
		}
	}
	return nil, fmt.Errorf("no matching app for webhook delivery")
}

func mapString(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func (h *Handler) finishWebhookDelivery(r *http.Request, delivery *model.WebhookDelivery, status, reason string) {
	if h.db == nil || delivery == nil {
		return
	}
	delivery.Status = status
	delivery.Reason = reason
	if err := h.db.UpdateWebhookDelivery(r.Context(), delivery); err != nil {
		log.Printf("webhook: update delivery %s: %v", delivery.ID, err)
	}
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
