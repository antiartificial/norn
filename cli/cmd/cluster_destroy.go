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

	"norn/cli/api"
	"norn/cli/style"
	"norn/cli/terraform"
)

var (
	destroyDryRun bool
	destroyYes    bool
)

var clusterDestroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Destroy all cluster nodes",
	Long:  "Tear down the entire cluster by draining, destroying, and deregistering every node.",
	RunE:  runClusterDestroy,
}

func init() {
	clusterDestroyCmd.Flags().BoolVar(&destroyDryRun, "dry-run", false, "Show what would be destroyed without making changes")
	clusterDestroyCmd.Flags().BoolVarP(&destroyYes, "yes", "y", false, "Skip confirmation prompt")
	clusterCmd.AddCommand(clusterDestroyCmd)
}

func runClusterDestroy(cmd *cobra.Command, args []string) error {
	nodes, err := client.ListClusterNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		fmt.Println(style.DimText.Render("No cluster nodes to destroy."))
		return nil
	}

	// Print node table
	fmt.Println(style.Banner.Render("CLUSTER DESTROY"))
	fmt.Printf("Nodes to destroy (%d):\n\n", len(nodes))
	for _, n := range nodes {
		fmt.Printf("  %s  %s  %s  %s  %s\n",
			style.Bold.Render(padRight(n.Name, 24)),
			padRight(n.Provider, 14),
			padRight(n.Size, 12),
			padRight(n.Role, 8),
			estimateMonthlyCost(n.Provider, n.Size)+"/mo",
		)
	}

	// Estimated savings
	var total float64
	for _, n := range nodes {
		if sizes, ok := pricingMap[n.Provider]; ok {
			if price, ok := sizes[n.Size]; ok {
				total += price
			}
		}
	}
	fmt.Printf("\nEstimated monthly savings: $%.2f/mo\n", total)

	if destroyDryRun {
		fmt.Println(style.DimText.Render("\nDry run — no changes made."))
		return nil
	}

	// Confirmation
	if !destroyYes {
		fmt.Print("\nType 'destroy' to confirm: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "destroy" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	m := newClusterDestroyModel(nodes)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	dm := finalModel.(clusterDestroyModel)
	if dm.failed {
		return fmt.Errorf("cluster destroy failed")
	}
	return nil
}

// --- Messages ---

type clusterDestroyStarted struct{ ch chan tea.Msg }
type clusterDestroyStepUpdate struct {
	step   string
	status string
}
type clusterDestroyCompleted struct{}
type clusterDestroyFailed struct{ err string }

// --- Model ---

type clusterDestroyModel struct {
	nodes     []api.ClusterNode
	spinner   spinner.Model
	steps     []stepState
	status    string // "running" | "completed" | "failed"
	errMsg    string
	failed    bool
	startTime time.Time
	eventCh   chan tea.Msg
}

func buildDestroySteps(nodes []api.ClusterNode) []string {
	var steps []string
	for _, n := range nodes {
		steps = append(steps, "drain-"+n.Name)
		steps = append(steps, "destroy-"+n.Name)
		steps = append(steps, "deregister-"+n.Name)
	}
	steps = append(steps, "cleanup")
	return steps
}

func newClusterDestroyModel(nodes []api.ClusterNode) clusterDestroyModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	stepNames := buildDestroySteps(nodes)
	steps := make([]stepState, len(stepNames))
	for i, n := range stepNames {
		steps[i] = stepState{name: n, status: "pending"}
	}

	return clusterDestroyModel{
		nodes:     nodes,
		spinner:   s,
		steps:     steps,
		status:    "running",
		startTime: time.Now(),
	}
}

func (m clusterDestroyModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		runClusterDestroySteps(m.nodes),
	)
}

func (m clusterDestroyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case clusterDestroyStarted:
		m.eventCh = msg.ch
		return m, waitForClusterDestroyEvent(m.eventCh)

	case clusterDestroyStepUpdate:
		for i := range m.steps {
			if m.steps[i].name == msg.step {
				m.steps[i].status = msg.status
				break
			}
		}
		return m, waitForClusterDestroyEvent(m.eventCh)

	case clusterDestroyCompleted:
		m.status = "completed"
		return m, tea.Quit

	case clusterDestroyFailed:
		m.status = "failed"
		m.errMsg = msg.err
		m.failed = true
		return m, tea.Quit
	}

	return m, nil
}

func (m clusterDestroyModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("CLUSTER DESTROY"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("Nodes"))
	b.WriteString(style.Bold.Render(fmt.Sprintf("%d", len(m.nodes))))
	b.WriteString("\n\n")

	for _, step := range m.steps {
		name := padRight(step.name, 30)

		switch step.status {
		case "pending":
			b.WriteString(fmt.Sprintf("  %s %s\n", style.DimText.Render(name), style.DimText.Render("waiting")))
		case "running":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", style.StepRunning.Render(name), m.spinner.View(), style.StepRunning.Render("running")))
		case "completed":
			b.WriteString(fmt.Sprintf("  %s %s\n", style.StepDone.Render(name), style.StepDone.Render("done")))
		case "failed":
			b.WriteString(fmt.Sprintf("  %s %s\n", style.StepFailed.Render(name), style.StepFailed.Render("failed")))
		}
	}

	b.WriteString("\n")

	elapsed := time.Since(m.startTime).Round(time.Second)

	switch m.status {
	case "running":
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Destroying cluster... (%s)", elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("Cluster destroyed in %s", elapsed)))
	case "failed":
		msg := "Cluster destroy failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Cluster destroy failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render(msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

func runClusterDestroySteps(nodes []api.ClusterNode) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, 32)

		go func() {
			defer close(ch)
			ctx := context.Background()

			for _, node := range nodes {
				// Drain
				ch <- clusterDestroyStepUpdate{step: "drain-" + node.Name, status: "running"}
				time.Sleep(2 * time.Second) // placeholder — would run kubectl drain
				ch <- clusterDestroyStepUpdate{step: "drain-" + node.Name, status: "completed"}

				// Destroy infra
				ch <- clusterDestroyStepUpdate{step: "destroy-" + node.Name, status: "running"}
				runner, err := terraform.NewRunner()
				if err != nil {
					ch <- clusterDestroyStepUpdate{step: "destroy-" + node.Name, status: "failed"}
					ch <- clusterDestroyFailed{err: fmt.Sprintf("terraform runner: %s", err)}
					return
				}
				if err := runner.Destroy(ctx); err != nil {
					ch <- clusterDestroyStepUpdate{step: "destroy-" + node.Name, status: "failed"}
					ch <- clusterDestroyFailed{err: fmt.Sprintf("terraform destroy %s: %s", node.Name, err)}
					return
				}
				ch <- clusterDestroyStepUpdate{step: "destroy-" + node.Name, status: "completed"}

				// Deregister from API
				ch <- clusterDestroyStepUpdate{step: "deregister-" + node.Name, status: "running"}
				if err := client.RemoveClusterNode(node.ID); err != nil {
					ch <- clusterDestroyStepUpdate{step: "deregister-" + node.Name, status: "failed"}
					ch <- clusterDestroyFailed{err: fmt.Sprintf("deregister %s: %s", node.Name, err)}
					return
				}
				ch <- clusterDestroyStepUpdate{step: "deregister-" + node.Name, status: "completed"}
			}

			// Cleanup
			ch <- clusterDestroyStepUpdate{step: "cleanup", status: "running"}
			time.Sleep(500 * time.Millisecond)
			ch <- clusterDestroyStepUpdate{step: "cleanup", status: "completed"}

			ch <- clusterDestroyCompleted{}
		}()

		return clusterDestroyStarted{ch: ch}
	}
}

func waitForClusterDestroyEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return clusterDestroyCompleted{}
		}
		return msg
	}
}
