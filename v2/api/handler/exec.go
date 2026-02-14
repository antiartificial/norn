package handler

import (
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
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	allocID := r.URL.Query().Get("allocId")
	command := r.URL.Query().Get("command")
	if command == "" {
		command = "/bin/sh"
	}

	var taskName string

	if allocID == "" {
		aID, tName, err := h.nomad.FindRunningAlloc(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		allocID = aID
		taskName = tName
	} else {
		_, tName, err := h.nomad.FindRunningAlloc(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		taskName = tName
	}

	ws, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("exec websocket upgrade: %v", err)
		return
	}
	defer ws.Close()

	cmd := strings.Fields(command)
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}

	if err := h.nomad.ExecWebSocket(allocID, taskName, cmd, ws); err != nil {
		log.Printf("exec error for %s/%s: %v", id, allocID, err)
	}
}
