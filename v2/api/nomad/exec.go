package nomad

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/gorilla/websocket"
)

// wsWriter serializes writes to a gorilla/websocket connection.
type wsWriter struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (w *wsWriter) send(msg []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteMessage(websocket.TextMessage, msg)
}

// ExecWebSocket bridges a client WebSocket connection to Nomad's exec API.
// Client → server messages: {"stdin":"data"} or {"resize":{"width":N,"height":N}}
// Server → client messages: {"stdout":"data"} or {"stderr":"data"} or {"exit":N}
func (c *Client) ExecWebSocket(allocID, task string, command []string, ws *websocket.Conn) error {
	alloc, _, err := c.api.Allocations().Info(allocID, nil)
	if err != nil {
		return fmt.Errorf("get allocation: %w", err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	termSizeCh := make(chan nomadapi.TerminalSize, 4)
	execCtx, cancel := context.WithCancel(context.Background())

	writer := &wsWriter{ws: ws}

	// Client WS → stdin pipe + resize channel
	go func() {
		defer stdinW.Close()
		defer cancel()
		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Stdin  string `json:"stdin"`
				Resize *struct {
					Width  int `json:"width"`
					Height int `json:"height"`
				} `json:"resize"`
			}
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			if msg.Stdin != "" {
				stdinW.Write([]byte(msg.Stdin))
			}
			if msg.Resize != nil {
				select {
				case termSizeCh <- nomadapi.TerminalSize{
					Width:  msg.Resize.Width,
					Height: msg.Resize.Height,
				}:
				default:
				}
			}
		}
	}()

	// stdout pipe → client WS
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdoutR.Read(buf)
			if n > 0 {
				out, _ := json.Marshal(map[string]string{"stdout": string(buf[:n])})
				writer.send(out)
			}
			if err != nil {
				return
			}
		}
	}()

	// stderr pipe → client WS
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderrR.Read(buf)
			if n > 0 {
				out, _ := json.Marshal(map[string]string{"stderr": string(buf[:n])})
				writer.send(out)
			}
			if err != nil {
				return
			}
		}
	}()

	exitCode, execErr := c.api.Allocations().Exec(
		execCtx, alloc, task, true, command,
		stdinR, stdoutW, stderrW,
		termSizeCh, nil,
	)

	// Close write ends so reader goroutines exit
	stdoutW.Close()
	stderrW.Close()

	exitMsg, _ := json.Marshal(map[string]int{"exit": exitCode})
	writer.send(exitMsg)

	if execErr != nil {
		return fmt.Errorf("exec: %w", execErr)
	}
	return nil
}

// FindRunningAlloc returns the first running allocation and its main task name for a job.
func (c *Client) FindRunningAlloc(jobID string) (allocID, taskName string, err error) {
	allocs, err := c.JobAllocations(jobID)
	if err != nil {
		return "", "", err
	}

	for _, a := range allocs {
		if a.ClientStatus == "running" {
			full, _, err := c.api.Allocations().Info(a.ID, nil)
			if err != nil {
				continue
			}
			for _, tg := range full.Job.TaskGroups {
				if len(tg.Tasks) > 0 {
					return a.ID, tg.Tasks[0].Name, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("no running allocations for job %s", jobID)
}
