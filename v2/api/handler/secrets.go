package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

type SecretStatus struct {
	App                 string   `json:"app"`
	Declared            []string `json:"declared"`
	Encrypted           []string `json:"encrypted"`
	MissingEncrypted    []string `json:"missingEncrypted"`
	EncryptedUndeclared []string `json:"encryptedUndeclared"`
	PlainEnvWarnings    []string `json:"plainEnvWarnings"`
	OK                  bool     `json:"ok"`
}

func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	keys, err := h.secrets.List(id)
	if err != nil {
		// No secrets file is OK
		writeJSON(w, []string{})
		return
	}
	writeJSON(w, keys)
}

func (h *Handler) SecretsStatusAll(w http.ResponseWriter, r *http.Request) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	statuses := make([]SecretStatus, 0, len(specs))
	for _, spec := range specs {
		statuses = append(statuses, h.secretStatus(spec))
	}
	writeJSON(w, statuses)
}

func (h *Handler) SecretsStatusApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, spec := range specs {
		if spec.App == id {
			writeJSON(w, h.secretStatus(spec))
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
}

func (h *Handler) secretStatus(spec *model.InfraSpec) SecretStatus {
	declared := normalizeSecretKeys(spec.Secrets)
	encrypted, err := h.secrets.List(spec.App)
	if err != nil {
		encrypted = []string{}
	}
	encrypted = normalizeSecretKeys(encrypted)

	declaredSet := stringSet(declared)
	encryptedSet := stringSet(encrypted)

	status := SecretStatus{
		App:       spec.App,
		Declared:  declared,
		Encrypted: encrypted,
	}
	for _, key := range declared {
		if !encryptedSet[key] {
			status.MissingEncrypted = append(status.MissingEncrypted, key)
		}
	}
	for _, key := range encrypted {
		if !declaredSet[key] {
			status.EncryptedUndeclared = append(status.EncryptedUndeclared, key)
		}
	}

	validation := model.ValidateSpecWithOptions(spec, model.ValidationOptions{NetworkMode: h.cfg.NetworkMode})
	for _, finding := range validation.Findings {
		if finding.Severity == "warning" && isPlainEnvSecretFinding(finding.Message) {
			status.PlainEnvWarnings = append(status.PlainEnvWarnings, finding.Field)
		}
	}
	sort.Strings(status.PlainEnvWarnings)
	status.OK = len(status.MissingEncrypted) == 0 && len(status.EncryptedUndeclared) == 0 && len(status.PlainEnvWarnings) == 0
	return status
}

func normalizeSecretKeys(keys []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func stringSet(keys []string) map[string]bool {
	set := map[string]bool{}
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func isPlainEnvSecretFinding(message string) bool {
	return strings.Contains(message, "plain env") || strings.Contains(message, "plaintext env")
}

func (h *Handler) UpdateSecrets(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var updates map[string]string
	if err := decodeJSON(r, &updates); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.secrets.Set(id, updates); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *Handler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")
	if err := h.secrets.Delete(id, key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
