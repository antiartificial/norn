package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"

	"norn/v2/cli/style"
)

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

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		var evt wsEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			continue
		}

		// Filter to our saga
		if evt.Payload["sagaId"] != sagaID {
			continue
		}

		icon, s := eventStyle(evt)
		fmt.Printf("  %s %s\n", s.Render(icon), eventMessage(evt))

		if evt.Type == "deploy.completed" {
			fmt.Println()
			fmt.Println(style.SuccessBox.Render("complete"))
			return nil
		}
		if evt.Type == "deploy.failed" {
			fmt.Println()
			fmt.Println(style.ErrorBox.Render("failed"))
			return nil
		}

		// Reset deadline on each message
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	}
}

func streamViaPolling(sagaID string) error {
	seen := map[string]bool{}
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

			icon := "·"
			s := style.DimText
			switch evt.Action {
			case "step.start":
				icon = "▶"
				s = style.StepRunning
			case "step.complete":
				icon = "✓"
				s = style.StepDone
			case "step.failed":
				icon = "✗"
				s = style.StepFailed
			case "deploy.complete":
				icon = "✓"
				s = style.Healthy
			case "deploy.failed":
				icon = "✗"
				s = style.Unhealthy
			}
			fmt.Printf("  %s %s\n", s.Render(icon), evt.Message)

			if evt.Action == "deploy.complete" || evt.Action == "deploy.failed" {
				fmt.Println()
				if evt.Action == "deploy.complete" {
					fmt.Println(style.SuccessBox.Render("complete"))
				} else {
					fmt.Println(style.ErrorBox.Render("failed"))
				}
				return nil
			}
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for completion")
}

func eventStyle(evt wsEvent) (string, lipgloss.Style) {
	switch evt.Type {
	case "deploy.step":
		switch evt.Payload["status"] {
		case "running":
			return "▶", style.StepRunning
		case "complete":
			return "✓", style.StepDone
		case "failed":
			return "✗", style.StepFailed
		}
	case "deploy.progress":
		return "·", style.DimText
	case "deploy.completed":
		return "✓", style.Healthy
	case "deploy.failed":
		return "✗", style.Unhealthy
	}
	return "·", style.DimText
}

func eventMessage(evt wsEvent) string {
	switch evt.Type {
	case "deploy.step":
		return fmt.Sprintf("%s %s", evt.Payload["step"], evt.Payload["status"])
	case "deploy.progress":
		if msg, ok := evt.Payload["message"]; ok {
			return msg
		}
		return "progress"
	case "deploy.completed":
		return fmt.Sprintf("deployed %s", evt.Payload["imageTag"])
	case "deploy.failed":
		return evt.Payload["error"]
	}
	return evt.Type
}
