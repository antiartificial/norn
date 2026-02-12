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
	addProvider string
	addRole     string
	addSize     string
	addRegion   string
	addName     string
)

var clusterAddCmd = &cobra.Command{
	Use:   "add-node",
	Short: "Add a server or agent node to the cluster",
	RunE:  runClusterAdd,
}

func init() {
	clusterAddCmd.Flags().StringVar(&addProvider, "provider", "", "Cloud provider (hetzner, digitalocean, vultr)")
	clusterAddCmd.Flags().StringVar(&addRole, "role", "agent", "Node role (server, agent)")
	clusterAddCmd.Flags().StringVar(&addSize, "size", "", "Instance size (e.g. cx22, s-1vcpu-2gb)")
	clusterAddCmd.Flags().StringVar(&addRegion, "region", "", "Region (e.g. fsn1, nyc1)")
	clusterAddCmd.Flags().StringVar(&addName, "name", "", "Node name")
	clusterAddCmd.MarkFlagRequired("provider")
	clusterAddCmd.MarkFlagRequired("name")
	clusterCmd.AddCommand(clusterAddCmd)
}

func runClusterAdd(cmd *cobra.Command, args []string) error {
	if addSize != "" {
		fmt.Printf("Estimated cost: ~%s/mo (%s %s)\n\n", estimateMonthlyCost(addProvider, addSize), addProvider, addSize)
	}

	m := newClusterAddModel(addProvider, addRole, addSize, addRegion, addName)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	am := finalModel.(clusterAddModel)
	if am.failed {
		return fmt.Errorf("add node failed")
	}
	return nil
}

// --- Messages ---

type clusterAddStarted struct{ ch chan tea.Msg }
type clusterAddStepUpdate struct {
	step   string
	status string
}
type clusterAddCompleted struct{}
type clusterAddFailed struct{ err string }

// --- Model ---

type clusterAddModel struct {
	provider  string
	role      string
	size      string
	region    string
	name      string
	spinner   spinner.Model
	steps     []stepState
	status    string // "running" | "completed" | "failed"
	errMsg    string
	failed    bool
	startTime time.Time
	eventCh   chan tea.Msg
}

var clusterAddSteps = []string{"terraform-apply", "poll-ready", "register-node"}

func newClusterAddModel(provider, role, size, region, name string) clusterAddModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	steps := make([]stepState, len(clusterAddSteps))
	for i, n := range clusterAddSteps {
		steps[i] = stepState{name: n, status: "pending"}
	}

	return clusterAddModel{
		provider:  provider,
		role:      role,
		size:      size,
		region:    region,
		name:      name,
		spinner:   s,
		steps:     steps,
		status:    "running",
		startTime: time.Now(),
	}
}

func (m clusterAddModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		runClusterAddSteps(m.provider, m.role, m.size, m.region, m.name),
	)
}

func (m clusterAddModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case clusterAddStarted:
		m.eventCh = msg.ch
		return m, waitForClusterAddEvent(m.eventCh)

	case clusterAddStepUpdate:
		for i := range m.steps {
			if m.steps[i].name == msg.step {
				m.steps[i].status = msg.status
				break
			}
		}
		return m, waitForClusterAddEvent(m.eventCh)

	case clusterAddCompleted:
		m.status = "completed"
		return m, tea.Quit

	case clusterAddFailed:
		m.status = "failed"
		m.errMsg = msg.err
		m.failed = true
		return m, tea.Quit
	}

	return m, nil
}

func (m clusterAddModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("CLUSTER ADD NODE"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("Provider"))
	b.WriteString(style.Bold.Render(m.provider))
	b.WriteString("\n")
	b.WriteString(style.Key.Render("Node"))
	b.WriteString(style.Bold.Render(m.name))
	b.WriteString(style.DimText.Render(fmt.Sprintf("  (%s)", m.role)))
	if m.size != "" {
		b.WriteString(style.DimText.Render(fmt.Sprintf("  %s", m.size)))
	}
	if m.region != "" {
		b.WriteString(style.DimText.Render(fmt.Sprintf("  [%s]", m.region)))
	}
	b.WriteString("\n\n")

	stepIcons := map[string]string{
		"terraform-apply": "  ",
		"poll-ready":      "  ",
		"register-node":   "  ",
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
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Adding %s node... (%s)", m.role, elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("Node %s added in %s", m.name, elapsed)))
	case "failed":
		msg := "Add node failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Add node failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render(msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

func runClusterAddSteps(provider, role, size, region, name string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, 32)

		go func() {
			defer close(ch)
			ctx := context.Background()

			// Step 1: terraform-apply
			ch <- clusterAddStepUpdate{step: "terraform-apply", status: "running"}
			runner, err := terraform.NewRunner()
			if err != nil {
				ch <- clusterAddStepUpdate{step: "terraform-apply", status: "failed"}
				ch <- clusterAddFailed{err: fmt.Sprintf("terraform runner: %s", err)}
				return
			}
			if err := runner.Apply(ctx); err != nil {
				ch <- clusterAddStepUpdate{step: "terraform-apply", status: "failed"}
				ch <- clusterAddFailed{err: fmt.Sprintf("terraform apply: %s", err)}
				return
			}
			ch <- clusterAddStepUpdate{step: "terraform-apply", status: "completed"}

			// Step 2: poll-ready (placeholder)
			ch <- clusterAddStepUpdate{step: "poll-ready", status: "running"}
			time.Sleep(2 * time.Second)
			ch <- clusterAddStepUpdate{step: "poll-ready", status: "completed"}

			// Step 3: register-node
			ch <- clusterAddStepUpdate{step: "register-node", status: "running"}
			node := api.ClusterNode{
				Name:     name,
				Provider: provider,
				Region:   region,
				Size:     size,
				Role:     role,
				Status:   "ready",
			}
			if err := client.AddClusterNode(node); err != nil {
				ch <- clusterAddStepUpdate{step: "register-node", status: "failed"}
				ch <- clusterAddFailed{err: fmt.Sprintf("register node: %s", err)}
				return
			}
			ch <- clusterAddStepUpdate{step: "register-node", status: "completed"}

			ch <- clusterAddCompleted{}
		}()

		return clusterAddStarted{ch: ch}
	}
}

func waitForClusterAddEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return clusterAddCompleted{}
		}
		return msg
	}
}
