package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var execProcess string

func init() {
	rootCmd.AddCommand(execCmd)
	execCmd.Flags().StringVarP(&execProcess, "process", "p", "", "Task group/process to exec into")
}

var execCmd = &cobra.Command{
	Use:   "exec <app> -- <command...>",
	Short: "Run a command in a running allocation",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		command := strings.Join(args[1:], " ")

		conn, err := client.Exec(appID, execProcess, command)
		if err != nil {
			return err
		}
		defer conn.Close()

		return streamExec(conn)
	},
}

func streamExec(conn *websocket.Conn) error {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) || err == io.EOF {
				return nil
			}
			return err
		}
		var msg struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Exit   *int   `json:"exit"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Stdout != "" {
			fmt.Fprint(os.Stdout, msg.Stdout)
		}
		if msg.Stderr != "" {
			fmt.Fprint(os.Stderr, msg.Stderr)
		}
		if msg.Exit != nil {
			if *msg.Exit != 0 {
				return fmt.Errorf("remote command exited with %d", *msg.Exit)
			}
			return nil
		}
	}
}
