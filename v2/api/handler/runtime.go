package handler

import (
	"net/http"

	"norn/v2/api/runtime"
)

func (h *Handler) RuntimeInfo(w http.ResponseWriter, r *http.Request) {
	var info *runtime.Info
	if h.rt != nil {
		info = h.rt.Info(r.Context())
	} else {
		info = &runtime.Info{
			Backend:    runtime.Docker,
			Available:  false,
			TaskDriver: "docker",
			BuildCmd:   "docker build",
		}
	}

	backends := []map[string]interface{}{
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

	writeJSON(w, map[string]interface{}{
		"active":   info,
		"backends": backends,
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
