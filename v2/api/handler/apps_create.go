package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"norn/v2/api/model"
)

type CreateAppRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Port int    `json:"port,omitempty"`
}

type AppDeploymentRequest struct {
	Enabled bool `json:"enabled"`
}

type AppMutationReceipt struct {
	App     string           `json:"app"`
	Created bool             `json:"created,omitempty"`
	Spec    *model.InfraSpec `json:"spec"`
}

// CreateApp creates a non-deployable draft. Enabling deployment is a separate
// explicit mutation, preventing a partial template from entering recovery.
func (h *Handler) CreateApp(w http.ResponseWriter, r *http.Request) {
	var req CreateAppRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if !validAppIDRe.MatchString(req.Name) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_name", "name must use lowercase letters, numbers, and hyphens")
		return
	}
	if req.Kind == "" {
		req.Kind = "endpoint"
	}
	if req.Kind != "endpoint" && req.Kind != "worker" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_app_kind", "kind must be endpoint or worker")
		return
	}
	if req.Port == 0 {
		req.Port = 8080
	}
	if req.Port < 1 || req.Port > 65535 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_port", "port must be between 1 and 65535")
		return
	}

	spec := safeAppTemplate(req)
	if result := model.ValidateSpec(spec); !result.Valid {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_infraspec", "generated InfraSpec did not validate")
		return
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_create_failed", "failed to serialize InfraSpec")
		return
	}
	appDir := filepath.Join(h.cfg.AppsDir, req.Name)
	if err := os.Mkdir(appDir, 0750); err != nil {
		if os.IsExist(err) {
			WriteControlProblem(w, r, http.StatusConflict, "app_exists", fmt.Sprintf("app %s already exists", req.Name))
		} else {
			WriteControlProblem(w, r, http.StatusInternalServerError, "app_create_failed", "failed to create app directory")
		}
		return
	}
	path := filepath.Join(appDir, "infraspec.yaml")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err == nil {
		_, err = file.Write(data)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		_ = os.Remove(path)
		_ = os.Remove(appDir)
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_create_failed", "failed to write InfraSpec")
		return
	}
	w.Header().Set("Location", "/api/v1/apps/"+req.Name)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, AppMutationReceipt{App: req.Name, Created: true, Spec: spec})
}

func safeAppTemplate(req CreateAppRequest) *model.InfraSpec {
	processes := map[string]model.Process{}
	if req.Kind == "worker" {
		processes["worker"] = model.Process{Command: "./worker", Scaling: &model.Scaling{Min: 1, Max: 3}, Resources: &model.Resources{CPU: 100, Memory: 128}}
	} else {
		processes["web"] = model.Process{Port: req.Port, Health: &model.HealthSpec{Path: "/readyz", Interval: "10s", Timeout: "5s"}, Scaling: &model.Scaling{Min: 1, Max: 3}, Resources: &model.Resources{CPU: 100, Memory: 128}}
	}
	return &model.InfraSpec{App: req.Name, Deploy: false, Processes: processes}
}

func (h *Handler) UpdateAppDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req AppDeploymentRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	path := filepath.Join(h.cfg.AppsDir, id, "infraspec.yaml")
	if info, statErr := os.Lstat(filepath.Dir(path)); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", fmt.Sprintf("app %s not found", id))
		return
	}
	if info, statErr := os.Lstat(path); statErr != nil || info.Mode()&os.ModeSymlink != 0 {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", fmt.Sprintf("app %s not found", id))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", fmt.Sprintf("app %s not found", id))
		return
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		WriteControlProblem(w, r, http.StatusConflict, "invalid_infraspec", "InfraSpec cannot be updated")
		return
	}
	if req.Enabled {
		spec, loadErr := model.LoadInfraSpec(path)
		if loadErr != nil || !model.ValidateSpecWithOptions(spec, model.ValidationOptions{NetworkMode: h.cfg.NetworkMode, StrictSecrets: h.cfg.StrictSecrets}).Valid {
			WriteControlProblem(w, r, http.StatusConflict, "invalid_infraspec", "InfraSpec must pass validation before deployment can be enabled")
			return
		}
	}
	setYAMLBoolean(doc.Content[0], "deploy", req.Enabled)
	updated, err := yaml.Marshal(&doc)
	if err != nil || writeFileAtomic(path, updated, 0640) != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_update_failed", "failed to update InfraSpec")
		return
	}
	spec, err := model.LoadInfraSpec(path)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "app_update_failed", "updated InfraSpec could not be read")
		return
	}
	writeJSON(w, AppMutationReceipt{App: id, Spec: spec})
}

func setYAMLBoolean(root *yaml.Node, key string, value bool) {
	if root.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", value)}
			return
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", value)})
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".infraspec-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(mode); err == nil {
		_, err = temp.Write(data)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
