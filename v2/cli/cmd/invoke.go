package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var (
	invokeProcess string
	invokeBody    string
)

func init() {
	invokeCmd.Flags().StringVar(&invokeProcess, "process", "", "function process name")
	invokeCmd.Flags().StringVar(&invokeBody, "body", "", "request body (JSON string or @file)")
	rootCmd.AddCommand(invokeCmd)
}

var invokeCmd = &cobra.Command{
	Use:   "invoke <app>",
	Short: "Invoke a function",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		// Handle @file syntax for body
		body := invokeBody
		if strings.HasPrefix(body, "@") {
			data, err := os.ReadFile(body[1:])
			if err != nil {
				return fmt.Errorf("read body file: %w", err)
			}
			body = string(data)
		}

		fmt.Printf("%s invoking %s", style.DotWarning, appID)
		if invokeProcess != "" {
			fmt.Printf(" (process: %s)", invokeProcess)
		}
		fmt.Println()

		exec, err := client.InvokeFunction(appID, invokeProcess, body)
		if err != nil {
			return fmt.Errorf("invoke failed: %w", err)
		}

		fmt.Printf("  id: %s\n", style.DimText.Render(exec.ID))
		fmt.Println()

		// Try to wait for completion via WebSocket
		err = waitForFunctionComplete(exec.ID)
		if err != nil {
			// Fall back to polling
			return pollFunctionComplete(appID, exec.ID)
		}
		return nil
	},
}

func waitForFunctionComplete(execID string) error {
	wsURL := client.WebSocketURL()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Minute))

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var evt wsEvent
		if err := json.Unmarshal(msg, &evt); err != nil {
			continue
		}

		if evt.Type != "function.completed" || evt.Payload["execId"] != execID {
			continue
		}

		status := evt.Payload["status"]
		exitCode := evt.Payload["exitCode"]

		if status == "complete" && exitCode == "0" {
			fmt.Println(style.SuccessBox.Render(fmt.Sprintf("complete (exit %s)", exitCode)))
		} else {
			fmt.Println(style.ErrorBox.Render(fmt.Sprintf("%s (exit %s)", status, exitCode)))
		}
		return nil
	}
}

func pollFunctionComplete(appID, execID string) error {
	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)

		execs, err := client.FunctionHistory(appID)
		if err != nil {
			continue
		}

		for _, e := range execs {
			if e.ID == execID && e.Status != "running" {
				if e.Status == "complete" && e.ExitCode == 0 {
					fmt.Println(style.SuccessBox.Render(fmt.Sprintf("complete (exit %d, %dms)", e.ExitCode, e.DurationMs)))
				} else {
					fmt.Println(style.ErrorBox.Render(fmt.Sprintf("%s (exit %d, %dms)", e.Status, e.ExitCode, e.DurationMs)))
				}
				return nil
			}
		}
	}

	return fmt.Errorf("timeout waiting for function completion")
}
