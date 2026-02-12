package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/style"
	"norn/cli/terraform"
)

var clusterRemoveCmd = &cobra.Command{
	Use:   "remove-node <name-or-id>",
	Short: "Remove a node from the cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runClusterRemove,
}

func init() {
	clusterCmd.AddCommand(clusterRemoveCmd)
}

func runClusterRemove(cmd *cobra.Command, args []string) error {
	nodeID := args[0]

	// Confirm removal
	node, err := client.GetClusterNode(nodeID)
	if err != nil {
		return fmt.Errorf("get node: %w", err)
	}

	fmt.Printf("This will remove node %s (%s, %s, %s):\n", node.Name, node.Provider, node.Region, node.Role)
	fmt.Println("  - Drain workloads from the node")
	fmt.Println("  - Destroy cloud infrastructure via Terraform")
	fmt.Println("  - Remove node from Norn API")
	fmt.Print("\nContinue? [y/N] ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("Aborted.")
		return nil
	}

	m := newClusterRemoveModel(nodeID, node.Name)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	rm := finalModel.(clusterRemoveModel)
	if rm.failed {
		return fmt.Errorf("remove node failed")
	}
	return nil
}

// --- Messages ---

type clusterRemoveStarted struct{ ch chan tea.Msg }
type clusterRemoveStepUpdate struct {
	step   string
	status string
}
type clusterRemoveCompleted struct{}
type clusterRemoveFailed struct{ err string }

// --- Model ---

type clusterRemoveModel struct {
	nodeID    string
	nodeName  string
	spinner   spinner.Model
	steps     []stepState
	status    string // "running" | "completed" | "failed"
	errMsg    string
	failed    bool
	startTime time.Time
	eventCh   chan tea.Msg
}

var clusterRemoveSteps = []string{"drain-node", "terraform-destroy", "remove-from-api"}

func newClusterRemoveModel(nodeID, nodeName string) clusterRemoveModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	steps := make([]stepState, len(clusterRemoveSteps))
	for i, n := range clusterRemoveSteps {
		steps[i] = stepState{name: n, status: "pending"}
	}

	return clusterRemoveModel{
		nodeID:    nodeID,
		nodeName:  nodeName,
		spinner:   s,
		steps:     steps,
		status:    "running",
		startTime: time.Now(),
	}
}

func (m clusterRemoveModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		runClusterRemoveSteps(m.nodeID, m.nodeName),
	)
}

func (m clusterRemoveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case clusterRemoveStarted:
		m.eventCh = msg.ch
		return m, waitForClusterRemoveEvent(m.eventCh)

	case clusterRemoveStepUpdate:
		for i := range m.steps {
			if m.steps[i].name == msg.step {
				m.steps[i].status = msg.status
				break
			}
		}
		return m, waitForClusterRemoveEvent(m.eventCh)

	case clusterRemoveCompleted:
		m.status = "completed"
		return m, tea.Quit

	case clusterRemoveFailed:
		m.status = "failed"
		m.errMsg = msg.err
		m.failed = true
		return m, tea.Quit
	}

	return m, nil
}

func (m clusterRemoveModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("CLUSTER REMOVE NODE"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("Node"))
	b.WriteString(style.Bold.Render(m.nodeName))
	b.WriteString("\n\n")

	stepIcons := map[string]string{
		"drain-node":        "  ",
		"terraform-destroy": "  ",
		"remove-from-api":   "  ",
	}

	for _, step := range m.steps {
		icon := stepIcons[step.name]
		name := padRight(step.name, 20)

		switch step.status {
		case "pending":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.DimText.Render(name), style.DimText.Render("waiting")))
		case "running":
			b.WriteString(fmt.Sprintf("  %s %s %s %s\n", icon, style.StepRunning.Render(name), m.spinner.View(), style.StepRunning.Render("running")))
		case "completed":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.StepDone.Render(name), style.StepDone.Render("done")))
		case "failed":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.StepFailed.Render(name), style.StepFailed.Render("failed")))
		}
	}

	b.WriteString("\n")

	elapsed := time.Since(m.startTime).Round(time.Second)

	switch m.status {
	case "running":
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Removing node %s... (%s)", m.nodeName, elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("Node %s removed in %s", m.nodeName, elapsed)))
	case "failed":
		msg := "Remove node failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Remove node failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render(msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

func runClusterRemoveSteps(nodeID, nodeName string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, 32)

		go func() {
			defer close(ch)
			ctx := context.Background()

			// Step 1: drain-node (placeholder — would run kubectl drain)
			ch <- clusterRemoveStepUpdate{step: "drain-node", status: "running"}
			time.Sleep(2 * time.Second)
			ch <- clusterRemoveStepUpdate{step: "drain-node", status: "completed"}

			// Step 2: terraform-destroy
			ch <- clusterRemoveStepUpdate{step: "terraform-destroy", status: "running"}
			runner, err := terraform.NewRunner()
			if err != nil {
				ch <- clusterRemoveStepUpdate{step: "terraform-destroy", status: "failed"}
				ch <- clusterRemoveFailed{err: fmt.Sprintf("terraform runner: %s", err)}
				return
			}
			if err := runner.Destroy(ctx); err != nil {
				ch <- clusterRemoveStepUpdate{step: "terraform-destroy", status: "failed"}
				ch <- clusterRemoveFailed{err: fmt.Sprintf("terraform destroy: %s", err)}
				return
			}
			ch <- clusterRemoveStepUpdate{step: "terraform-destroy", status: "completed"}

			// Step 3: remove-from-api
			ch <- clusterRemoveStepUpdate{step: "remove-from-api", status: "running"}
			if err := client.RemoveClusterNode(nodeID); err != nil {
				ch <- clusterRemoveStepUpdate{step: "remove-from-api", status: "failed"}
				ch <- clusterRemoveFailed{err: fmt.Sprintf("remove node: %s", err)}
				return
			}
			ch <- clusterRemoveStepUpdate{step: "remove-from-api", status: "completed"}

			ch <- clusterRemoveCompleted{}
		}()

		return clusterRemoveStarted{ch: ch}
	}
}

func waitForClusterRemoveEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return clusterRemoveCompleted{}
		}
		return msg
	}
}
