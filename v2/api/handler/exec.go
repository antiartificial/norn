package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var execUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // Rely on outer auth (CF Access cookies pass through)
	},
}

func (h *Handler) ExecAlloc(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not available")
		return
	}

	containerName := r.URL.Query().Get("allocId") // legacy param name
	processName := r.URL.Query().Get("process")
	command := r.URL.Query().Get("command")
	if command == "" {
		command = "/bin/sh"
	}
	cmd := []string(nil)
	if rawArgv := r.URL.Query().Get("argv"); rawArgv != "" {
		if err := json.Unmarshal([]byte(rawArgv), &cmd); err != nil {
			writeError(w, http.StatusBadRequest, "invalid argv")
			return
		}
	}

	if containerName == "" {
		var err error
		containerName, err = h.engine.FindRunningInstance(id, processName)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
	}

	ws, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("exec websocket upgrade: %v", err)
		return
	}
	defer ws.Close()

	if len(cmd) == 0 {
		cmd = strings.Fields(command)
		if len(cmd) == 0 {
			cmd = []string{"/bin/sh"}
		}
	}

	if err := h.engine.ExecWebSocket(containerName, cmd, ws); err != nil {
		log.Printf("exec error for %s/%s: %v", id, containerName, err)
	}
}
