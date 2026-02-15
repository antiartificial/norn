package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/mattn/go-isatty"

	"norn/v2/cli/style"
)

const stepNameWidth = 8 // longest step name is "snapshot"

type wsEvent struct {
	Type    string            `json:"type"`
	AppID   string            `json:"appId"`
	Payload map[string]string `json:"payload"`
}

// streamSagaEvents connects to the WebSocket and prints events for the given
// saga until completion. Falls back to polling if the WS connection fails.
func streamSagaEvents(sagaID string) error {
	err := streamViaWebSocket(sagaID)
	if err == nil {
		return nil
	}
	log.Printf("ws unavailable, falling back to polling: %v", err)
	return streamViaPolling(sagaID)
}

func streamViaWebSocket(sagaID string) error {
	wsURL := client.WebSocketURL()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	tty := isatty.IsTerminal(os.Stdout.Fd())
	stepRunning := false

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		var evt wsEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			continue
		}

		if evt.Payload["sagaId"] != sagaID {
			continue
		}

		switch evt.Type {
		case "deploy.step":
			step := evt.Payload["step"]
			idx := evt.Payload["index"]
			total := evt.Payload["total"]
			status := evt.Payload["status"]

			switch status {
			case "running":
				if tty && stepRunning {
					// Clear previous running line (shouldn't happen, but be safe)
					fmt.Print("\r\033[2K")
				}
				line := formatStepLine("▶", style.StepRunning, step, "...", idx, total)
				if tty {
					fmt.Print(line)
					stepRunning = true
				} else {
					fmt.Println(line)
				}
			case "complete":
				dur := formatDuration(evt.Payload["durationMs"])
				line := formatStepLine("✓", style.StepDone, step, dur, idx, total)
				if tty && stepRunning {
					fmt.Printf("\r\033[2K%s\n", line)
				} else {
					fmt.Println(line)
				}
				stepRunning = false
			case "failed":
				dur := formatDuration(evt.Payload["durationMs"])
				line := formatStepLine("✗", style.StepFailed, step, dur, idx, total)
				if tty && stepRunning {
					fmt.Printf("\r\033[2K%s\n", line)
				} else {
					fmt.Println(line)
				}
				stepRunning = false
			}

		case "deploy.progress":
			if tty && stepRunning {
				// Print progress as sub-line below the running step
				fmt.Println() // finish current running line
				stepRunning = false
			}
			msg := evt.Payload["message"]
			fmt.Printf("    %s %s\n", style.DimText.Render("·"), msg)

		case "deploy.completed":
			fmt.Println()
			fmt.Println(style.SuccessBox.Render("complete"))
			return nil

		case "deploy.failed":
			fmt.Println()
			fmt.Printf("  %s %s\n", style.Unhealthy.Render("✗"), evt.Payload["error"])
			fmt.Println(style.ErrorBox.Render("failed"))
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	}
}

func streamViaPolling(sagaID string) error {
	seen := map[string]bool{}
	// Track step count for index/total from saga metadata
	stepCount := 0

	for i := 0; i < 150; i++ {
		events, err := client.GetSagaEvents(sagaID)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		for _, evt := range events {
			if seen[evt.ID] {
				continue
			}
			seen[evt.ID] = true

			switch evt.Action {
			case "step.start":
				stepCount++
				// Skip start events in polling — we'll show the completed line
			case "step.complete":
				step := evt.Metadata["step"]
				dur := formatDuration(evt.Metadata["durationMs"])
				line := formatStepLine("✓", style.StepDone, step, dur, "", "")
				fmt.Println(line)
			case "step.failed":
				step := evt.Metadata["step"]
				dur := formatDuration(evt.Metadata["durationMs"])
				line := formatStepLine("✗", style.StepFailed, step, dur, "", "")
				fmt.Println(line)
			case "deploy.complete":
				fmt.Println()
				fmt.Println(style.SuccessBox.Render("complete"))
				return nil
			case "deploy.failed":
				fmt.Println()
				fmt.Println(style.ErrorBox.Render("failed"))
				return nil
			default:
				// Log/progress events
				if evt.Action != "step.start" {
					fmt.Printf("    %s %s\n", style.DimText.Render("·"), evt.Message)
				}
			}
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for completion")
}

func formatStepLine(icon string, iconStyle lipgloss.Style, step, duration, idx, total string) string {
	paddedStep := fmt.Sprintf("%-*s", stepNameWidth, step)
	paddedDur := fmt.Sprintf("%6s", duration)
	counter := ""
	if idx != "" && total != "" {
		counter = fmt.Sprintf("   [%s/%s]", idx, total)
	}
	return fmt.Sprintf("  %s %s %s%s",
		iconStyle.Render(icon),
		paddedStep,
		style.DimText.Render(paddedDur),
		style.DimText.Render(counter),
	)
}

func formatDuration(msStr string) string {
	ms, err := strconv.ParseInt(msStr, 10, 64)
	if err != nil {
		return "..."
	}
	if ms >= 60000 {
		m := ms / 60000
		s := (ms % 60000) / 1000
		return fmt.Sprintf("%dm%ds", m, s)
	}
	sec := float64(ms) / 1000.0
	return fmt.Sprintf("%.1fs", sec)
}
