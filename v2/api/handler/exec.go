package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"norn/v2/api/hub"
)

func (h *Handler) ExecAlloc(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	allocID := r.URL.Query().Get("allocId")
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

	var taskName string

	if allocID == "" {
		aID, tName, err := h.nomad.FindRunningAlloc(id, processName)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		allocID = aID
		taskName = tName
	} else if processName != "" {
		taskName = processName
	} else {
		_, tName, err := h.nomad.FindRunningAlloc(id, "")
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		taskName = tName
	}

	allowedOrigins := map[string]bool{
		"http://localhost:5173": true,
		"http://localhost:3000": true,
	}
	if h.cfg != nil {
		for _, origin := range strings.Split(h.cfg.AllowedOrigins, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				allowedOrigins[origin] = true
			}
		}
	}
	execUpgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(req *http.Request) bool {
			return hub.OriginAllowed(req, allowedOrigins)
		},
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

	if err := h.nomad.ExecWebSocket(allocID, taskName, cmd, ws); err != nil {
		log.Printf("exec error for %s/%s: %v", id, allocID, err)
	}
}
