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

type SecretMigrationPlan struct {
	GeneratedAt string                `json:"generatedAt"`
	App         string                `json:"app,omitempty"`
	Items       []SecretMigrationItem `json:"items"`
	Count       int                   `json:"count"`
}

type SecretMigrationItem struct {
	App       string `json:"app"`
	Field     string `json:"field"`
	Key       string `json:"key"`
	Declared  bool   `json:"declared"`
	Encrypted bool   `json:"encrypted"`
	Action    string `json:"action"`
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

func (h *Handler) SecretsMigrationPlan(w http.ResponseWriter, r *http.Request) {
	appFilter := strings.TrimSpace(r.URL.Query().Get("app"))
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	plan := SecretMigrationPlan{
		GeneratedAt: timeNowUTC(),
		App:         appFilter,
		Items:       []SecretMigrationItem{},
	}
	found := appFilter == ""
	for _, spec := range specs {
		if appFilter != "" && spec.App != appFilter {
			continue
		}
		found = true
		plan.Items = append(plan.Items, h.secretMigrationItems(spec)...)
	}
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", appFilter))
		return
	}
	plan.Count = len(plan.Items)
	writeJSON(w, plan)
}

func (h *Handler) secretStatus(spec *model.InfraSpec) SecretStatus {
	declared := normalizeSecretKeys(spec.Secrets)
	encrypted := []string{}
	if h.secrets != nil {
		if keys, err := h.secrets.List(spec.App); err == nil {
			encrypted = keys
		}
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

func (h *Handler) secretMigrationItems(spec *model.InfraSpec) []SecretMigrationItem {
	status := h.secretStatus(spec)
	declared := stringSet(status.Declared)
	encrypted := stringSet(status.Encrypted)
	var items []SecretMigrationItem
	addEnvItems := func(field string, env map[string]string) {
		for key, value := range env {
			if !plainEnvLooksSecretLike(key, value) {
				continue
			}
			normalized := strings.ToUpper(strings.TrimSpace(key))
			item := SecretMigrationItem{
				App:       spec.App,
				Field:     field + "." + key,
				Key:       normalized,
				Declared:  declared[normalized],
				Encrypted: encrypted[normalized],
				Action:    "move plaintext env to secrets.enc.yaml, declare the key, then remove plaintext env",
			}
			switch {
			case item.Declared && item.Encrypted:
				item.Action = "remove plaintext env; encrypted value is already declared and present"
			case item.Declared:
				item.Action = "add encrypted value, then remove plaintext env"
			case item.Encrypted:
				item.Action = "declare existing encrypted key, then remove plaintext env"
			}
			items = append(items, item)
		}
	}
	addEnvItems("env", spec.Env)
	for process, proc := range spec.Processes {
		addEnvItems("processes."+process+".env", proc.Env)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].App == items[j].App {
			return items[i].Field < items[j].Field
		}
		return items[i].App < items[j].App
	})
	return items
}

func plainEnvLooksSecretLike(key, value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	secretMarkers := []string{
		"API_KEY",
		"AUTH_TOKEN",
		"CLIENT_SECRET",
		"CREDENTIAL",
		"DATABASE_URL",
		"DB_PASSWORD",
		"DSN",
		"PASSWORD",
		"PRIVATE_KEY",
		"SECRET",
		"TOKEN",
	}
	for _, marker := range secretMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	lowerValue := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lowerValue, "postgres://") ||
		strings.HasPrefix(lowerValue, "mysql://") ||
		strings.HasPrefix(lowerValue, "mongodb://") ||
		strings.HasPrefix(lowerValue, "redis://")
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
