package handler

import (
	"net/http"
	"strings"

	"norn/v2/api/connector"
	"norn/v2/api/runtime"
)

func (h *Handler) RuntimeInfo(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
			return
		}
	}
	var info *runtime.Info
	if h.containerRuntime != nil {
		info = h.containerRuntime.Info(r.Context())
	} else {
		info = &runtime.Info{
			Backend:    runtime.Docker,
			Available:  false,
			TaskDriver: "docker",
			BuildCmd:   "docker build",
		}
	}

	runtimes := []map[string]interface{}{
		{
			"name":      string(runtime.Docker),
			"available": dockerBinaryAvailable(),
			"current":   info.Backend == runtime.Docker,
		},
		{
			"name":      string(runtime.AppleContainer),
			"available": appleContainerBinaryAvailable(),
			"current":   info.Backend == runtime.AppleContainer,
		},
	}
	activeConnector := connector.Info{Name: connector.NomadConsul}
	if h.workloads != nil {
		activeConnector = h.workloads.Info(r.Context())
	}
	nomadConnector := connector.NewNomadConsul(h.nomad, h.consul).Info(r.Context())
	appleConnector := connector.NewApple(h.localEngine).Info(r.Context())

	writeJSON(w, map[string]interface{}{
		"active":     info,
		"backends":   runtimes,
		"connector":  activeConnector,
		"connectors": []connector.Info{nomadConnector, appleConnector},
	})
}

func dockerBinaryAvailable() bool {
	info := &runtime.Info{}
	rt := runtime.New(runtime.Docker, "")
	info = rt.Info(nil)
	return info.Available
}

func appleContainerBinaryAvailable() bool {
	rt := runtime.New(runtime.AppleContainer, "")
	info := rt.Info(nil)
	return info.Available
}
