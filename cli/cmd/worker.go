package cmd

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/style"
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Manage distributed workers",
}

var workerListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List connected workers",
	Aliases: []string{"ls"},
	RunE: func(cmd *cobra.Command, args []string) error {
		var workers []workerInfo
		if err := client.GetJSON("/api/workers", &workers); err != nil {
			return err
		}

		if len(workers) == 0 {
			fmt.Println("\n  No workers connected.\n")
			return nil
		}

		fmt.Println()
		for _, w := range workers {
			statusStyle := lipgloss.NewStyle().Bold(true)
			if w.Status == "connected" {
				statusStyle = statusStyle.Foreground(style.Green)
			} else if w.Status == "draining" {
				statusStyle = statusStyle.Foreground(style.Yellow)
			} else {
				statusStyle = statusStyle.Foreground(style.Red)
			}

			fmt.Printf("  %s %s  %s\n",
				style.Bold.Render(w.ID),
				statusStyle.Render(w.Status),
				style.DimText.Render(fmt.Sprintf("v%s", w.Version)),
			)
			fmt.Printf("    %s %s  %s %d/%d  %s %.0f%%\n",
				style.Key.Render("Caps"),
				style.Val.Render(fmt.Sprintf("%v", w.Capabilities)),
				style.Key.Render("Tasks"),
				w.TasksActive, w.MaxConcurrent,
				style.Key.Render("CPU"),
				w.CPULoad,
			)
			if w.PublicURL != "" {
				fmt.Printf("    %s %s\n", style.Key.Render("URL"), style.Val.Render(w.PublicURL))
			}
			if !w.LastHeartbeat.IsZero() {
				ago := time.Since(w.LastHeartbeat).Round(time.Second)
				fmt.Printf("    %s %s ago\n", style.Key.Render("Last heartbeat"), style.DimText.Render(ago.String()))
			}
			fmt.Println()
		}

		return nil
	},
}

var workerStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Show detailed worker status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var w workerInfo
		if err := client.GetJSON("/api/workers/"+args[0], &w); err != nil {
			return err
		}

		statusStyle := lipgloss.NewStyle().Bold(true)
		if w.Status == "connected" {
			statusStyle = statusStyle.Foreground(style.Green)
		} else {
			statusStyle = statusStyle.Foreground(style.Yellow)
		}

		fmt.Println()
		fmt.Printf("  %s %s\n", style.Key.Render("Worker"), style.Bold.Render(w.ID))
		fmt.Printf("  %s %s\n", style.Key.Render("Status"), statusStyle.Render(w.Status))
		fmt.Printf("  %s %s\n", style.Key.Render("Version"), style.Val.Render(w.Version))
		fmt.Printf("  %s %v\n", style.Key.Render("Capabilities"), w.Capabilities)
		fmt.Printf("  %s %d/%d\n", style.Key.Render("Tasks"), w.TasksActive, w.MaxConcurrent)
		fmt.Printf("  %s %.1f%%\n", style.Key.Render("CPU"), w.CPULoad)
		fmt.Printf("  %s %.1f%%\n", style.Key.Render("Memory"), w.MemoryUsed)
		if w.PublicURL != "" {
			fmt.Printf("  %s %s\n", style.Key.Render("URL"), style.Val.Render(w.PublicURL))
		}
		fmt.Println()

		return nil
	},
}

var workerDrainCmd = &cobra.Command{
	Use:   "drain <id>",
	Short: "Drain a worker (stop new tasks, wait for in-flight)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.PostJSON("/api/workers/"+args[0]+"/drain", "{}"); err != nil {
			return err
		}
		fmt.Printf("\n  Worker %s set to draining.\n\n", args[0])
		return nil
	},
}

var workerRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Deregister a worker",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.Delete("/api/workers/" + args[0]); err != nil {
			return err
		}
		fmt.Printf("\n  Worker %s removed.\n\n", args[0])
		return nil
	},
}

type workerInfo struct {
	ID            string    `json:"id"`
	Capabilities  []string  `json:"capabilities"`
	Version       string    `json:"version"`
	PublicURL     string    `json:"publicUrl"`
	MaxConcurrent int       `json:"maxConcurrent"`
	TasksActive   int       `json:"tasksActive"`
	QueueDepth    int       `json:"queueDepth"`
	CPULoad       float64   `json:"cpuLoad"`
	MemoryUsed    float64   `json:"memoryUsed"`
	Status        string    `json:"status"`
	ConnectedAt   time.Time `json:"connectedAt"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}

func init() {
	workerCmd.AddCommand(workerListCmd)
	workerCmd.AddCommand(workerStatusCmd)
	workerCmd.AddCommand(workerDrainCmd)
	workerCmd.AddCommand(workerRemoveCmd)
	rootCmd.AddCommand(workerCmd)
}
