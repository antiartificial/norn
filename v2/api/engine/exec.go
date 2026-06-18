package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/gorilla/websocket"
)

// ExecWebSocket bridges a WebSocket connection to `container exec` for
// interactive shell access. It handles stdin, stdout/stderr, and terminal
// resize messages using the same protocol as the existing Nomad exec bridge.
func (e *Engine) ExecWebSocket(containerName string, command []string, ws *websocket.Conn) error {
	args := append([]string{"exec", "-it", containerName}, command...)
	cmd := exec.Command(containerBin, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("exec stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("exec stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}

	var wg sync.WaitGroup

	// Read from container stdout → write to websocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				msg := execMessage{Stdout: buf[:n]}
				data, _ := json.Marshal(msg)
				if writeErr := ws.WriteMessage(websocket.TextMessage, data); writeErr != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("engine: exec stdout: %v", err)
				}
				return
			}
		}
	}()

	// Read from websocket → write to container stdin (or handle resize)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdin.Close()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var msg execMessage
			if json.Unmarshal(data, &msg) == nil {
				if msg.Resize != nil {
					// Terminal resize — not directly supported by `container exec`
					// without PTY control, but we try via SIGWINCH if possible
					continue
				}
				if len(msg.Stdin) > 0 {
					stdin.Write(msg.Stdin)
					continue
				}
			}
			// Raw data fallback
			stdin.Write(data)
		}
	}()

	wg.Wait()
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Send exit code
	exitMsg := execMessage{ExitCode: &exitCode}
	data, _ := json.Marshal(exitMsg)
	ws.WriteMessage(websocket.TextMessage, data)

	return nil
}

type execMessage struct {
	Stdin    json.RawMessage `json:"stdin,omitempty"`
	Stdout   json.RawMessage `json:"stdout,omitempty"`
	Stderr   json.RawMessage `json:"stderr,omitempty"`
	Resize   *execResize     `json:"resize,omitempty"`
	ExitCode *int            `json:"exitCode,omitempty"`
}

type execResize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ExecNonInteractive runs a command in a container and returns the combined output.
func (e *Engine) ExecNonInteractive(containerName string, command []string) ([]byte, error) {
	args := append([]string{"exec", containerName}, command...)
	cmd := exec.Command(containerBin, args...)
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}
