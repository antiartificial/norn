package nomad

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	nomadapi "github.com/hashicorp/nomad/api"
)

// wsWriter serializes writes to a gorilla/websocket connection.
type wsWriter struct {
	mu sync.Mutex
	ws *websocket.Conn
}

type execProtocolWriter struct {
	mu       sync.Mutex
	ws       *websocket.Conn
	sequence int64
}

func (w *execProtocolWriter) send(frame map[string]interface{}) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sequence++
	frame["sequence"] = w.sequence
	frame["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	_ = w.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.ws.WriteJSON(frame)
}

func (w *wsWriter) send(msg []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
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

	_ = stdinR.Close()
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

// ExecSessionWebSocket implements norn.exec/v1. Client frames are input,
// resize, and close-input; server frames are ready, stdout, stderr, error, and
// exit. Sequence numbers are independently monotonic in each direction.
func (c *Client) ExecSessionWebSocket(allocID, task string, command []string, terminal bool, columns, rows int, expiresAt time.Time, ws *websocket.Conn) (int, error) {
	alloc, _, err := c.api.Allocations().Info(allocID, nil)
	if err != nil {
		return -1, fmt.Errorf("get allocation: %w", err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	termSizeCh := make(chan nomadapi.TerminalSize, 4)
	execCtx, cancel := context.WithDeadline(context.Background(), expiresAt)
	defer cancel()
	writer := &execProtocolWriter{ws: ws}
	if columns > 0 && rows > 0 {
		termSizeCh <- nomadapi.TerminalSize{Width: columns, Height: rows}
	}
	if err := writer.send(map[string]interface{}{
		"frame": "ready", "protocol": "norn.exec/v1", "terminal": terminal,
		"columns": columns, "rows": rows,
	}); err != nil {
		return -1, err
	}

	go func() {
		defer stdinW.Close()
		defer cancel()
		var lastSequence int64
		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var frame struct {
				Frame    string `json:"frame"`
				Sequence int64  `json:"sequence"`
				Data     string `json:"data"`
				Encoding string `json:"encoding"`
				Columns  int    `json:"columns"`
				Rows     int    `json:"rows"`
			}
			if json.Unmarshal(raw, &frame) != nil || frame.Sequence <= lastSequence {
				_ = writer.send(map[string]interface{}{"frame": "error", "code": "invalid_exec_frame", "detail": "frame is invalid or sequence is not monotonic"})
				return
			}
			lastSequence = frame.Sequence
			switch frame.Frame {
			case "input":
				input := []byte(frame.Data)
				if frame.Encoding == "base64" {
					decoded, decodeErr := base64.StdEncoding.DecodeString(frame.Data)
					if decodeErr != nil {
						_ = writer.send(map[string]interface{}{"frame": "error", "code": "invalid_exec_encoding", "detail": "input data is not valid base64"})
						return
					}
					input = decoded
				} else if frame.Encoding != "" && frame.Encoding != "utf8" {
					_ = writer.send(map[string]interface{}{"frame": "error", "code": "invalid_exec_encoding", "detail": "input encoding must be utf8 or base64"})
					return
				}
				if _, err := stdinW.Write(input); err != nil {
					return
				}
			case "resize":
				if frame.Columns < 1 || frame.Columns > 1000 || frame.Rows < 1 || frame.Rows > 1000 {
					_ = writer.send(map[string]interface{}{"frame": "error", "code": "invalid_terminal_size", "detail": "columns and rows must be between 1 and 1000"})
					return
				}
				select {
				case termSizeCh <- nomadapi.TerminalSize{Width: frame.Columns, Height: frame.Rows}:
				default:
				}
			case "close-input":
				return
			default:
				_ = writer.send(map[string]interface{}{"frame": "error", "code": "unsupported_exec_frame", "detail": "unsupported client frame"})
				return
			}
		}
	}()

	var outputWG sync.WaitGroup
	stream := func(frameName string, reader *io.PipeReader) {
		defer outputWG.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				data, encoding := string(buf[:n]), "utf8"
				if !utf8.Valid(buf[:n]) {
					data, encoding = base64.StdEncoding.EncodeToString(buf[:n]), "base64"
				}
				if err := writer.send(map[string]interface{}{"frame": frameName, "data": data, "encoding": encoding}); err != nil {
					cancel()
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	outputWG.Add(2)
	go stream("stdout", stdoutR)
	go stream("stderr", stderrR)

	exitCode, execErr := c.api.Allocations().Exec(
		execCtx, alloc, task, terminal, command,
		stdinR, stdoutW, stderrW, termSizeCh, nil,
	)
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	outputWG.Wait()
	exitFrame := map[string]interface{}{"frame": "exit", "exitCode": exitCode, "reason": "exited"}
	if execErr != nil {
		exitFrame["reason"] = "transport-error"
		if execCtx.Err() == context.DeadlineExceeded {
			exitFrame["reason"] = "expired"
		}
	}
	_ = writer.send(exitFrame)
	if execErr != nil {
		return exitCode, fmt.Errorf("exec: %w", execErr)
	}
	return exitCode, nil
}

// FindRunningAlloc returns the first running allocation and its main task name for a job.
// When taskGroup is non-empty, only allocations from that task group are considered.
func (c *Client) FindRunningAlloc(jobID, taskGroup string) (allocID, taskName string, err error) {
	allocs, err := c.JobAllocations(jobID)
	if err != nil {
		return "", "", err
	}

	for _, a := range allocs {
		if taskGroup != "" && a.TaskGroup != taskGroup {
			continue
		}
		if a.ClientStatus == "running" {
			full, _, err := c.api.Allocations().Info(a.ID, nil)
			if err != nil {
				continue
			}
			for _, tg := range full.Job.TaskGroups {
				if tg == nil || tg.Name == nil || *tg.Name != a.TaskGroup {
					continue
				}
				if len(tg.Tasks) > 0 {
					return a.ID, tg.Tasks[0].Name, nil
				}
			}
		}
	}

	if taskGroup != "" {
		return "", "", fmt.Errorf("no running allocations for job %s task group %s", jobID, taskGroup)
	}
	return "", "", fmt.Errorf("no running allocations for job %s", jobID)
}

// ResolveExecTarget ensures an explicitly selected allocation belongs to the
// app named by the authorization resource. This prevents an allocation ID from
// escaping the app-bound step-up grant.
func (c *Client) ResolveExecTarget(jobID, allocID, taskGroup string) (string, string, error) {
	if allocID == "" {
		return c.FindRunningAlloc(jobID, taskGroup)
	}
	alloc, _, err := c.api.Allocations().Info(allocID, nil)
	if err != nil {
		return "", "", fmt.Errorf("get allocation: %w", err)
	}
	return execTargetFromAllocation(jobID, taskGroup, alloc)
}

func execTargetFromAllocation(jobID, taskGroup string, alloc *nomadapi.Allocation) (string, string, error) {
	if alloc == nil {
		return "", "", fmt.Errorf("allocation was not found")
	}
	if alloc.JobID != jobID {
		return "", "", fmt.Errorf("allocation does not belong to app %s", jobID)
	}
	if alloc.ClientStatus != "running" {
		return "", "", fmt.Errorf("allocation is not running")
	}
	if taskGroup != "" && alloc.TaskGroup != taskGroup {
		return "", "", fmt.Errorf("allocation is not for process %s", taskGroup)
	}
	if alloc.Job != nil {
		for _, group := range alloc.Job.TaskGroups {
			if group != nil && group.Name != nil && *group.Name == alloc.TaskGroup && len(group.Tasks) > 0 {
				return alloc.ID, group.Tasks[0].Name, nil
			}
		}
	}
	return "", "", fmt.Errorf("allocation task could not be resolved")
}
