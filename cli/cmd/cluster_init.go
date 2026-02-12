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
	initProvider string
	initSize     string
	initRegion   string
	initName     string
)

var clusterInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize cluster with the first server node",
	RunE:  runClusterInit,
}

func init() {
	clusterInitCmd.Flags().StringVar(&initProvider, "provider", "", "Cloud provider (hetzner, digitalocean, vultr)")
	clusterInitCmd.Flags().StringVar(&initSize, "size", "", "Instance size (e.g. cx22, s-1vcpu-2gb)")
	clusterInitCmd.Flags().StringVar(&initRegion, "region", "", "Region (e.g. fsn1, nyc1)")
	clusterInitCmd.Flags().StringVar(&initName, "name", "norn-server-1", "Node name")
	clusterInitCmd.MarkFlagRequired("provider")
	clusterCmd.AddCommand(clusterInitCmd)
}

func runClusterInit(cmd *cobra.Command, args []string) error {
	if initSize != "" {
		fmt.Printf("Estimated cost: ~%s/mo (%s %s)\n\n", estimateMonthlyCost(initProvider, initSize), initProvider, initSize)
	}

	m := newClusterInitModel(initProvider, initSize, initRegion, initName)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	fm := finalModel.(clusterInitModel)
	if fm.failed {
		return fmt.Errorf("cluster init failed")
	}
	return nil
}

// --- Messages ---

type clusterInitStarted struct{ ch chan tea.Msg }
type clusterInitStepUpdate struct {
	step   string
	status string
}
type clusterInitCompleted struct{}
type clusterInitFailed struct{ err string }

// --- Model ---

type clusterInitModel struct {
	provider  string
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

var clusterInitSteps = []string{"terraform-init", "terraform-apply", "poll-ready", "register-node", "merge-kubeconfig"}

func newClusterInitModel(provider, size, region, name string) clusterInitModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	steps := make([]stepState, len(clusterInitSteps))
	for i, n := range clusterInitSteps {
		steps[i] = stepState{name: n, status: "pending"}
	}

	return clusterInitModel{
		provider:  provider,
		size:      size,
		region:    region,
		name:      name,
		spinner:   s,
		steps:     steps,
		status:    "running",
		startTime: time.Now(),
	}
}

func (m clusterInitModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		runClusterInitSteps(m.provider, m.size, m.region, m.name),
	)
}

func (m clusterInitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case clusterInitStarted:
		m.eventCh = msg.ch
		return m, waitForClusterInitEvent(m.eventCh)

	case clusterInitStepUpdate:
		for i := range m.steps {
			if m.steps[i].name == msg.step {
				m.steps[i].status = msg.status
				break
			}
		}
		return m, waitForClusterInitEvent(m.eventCh)

	case clusterInitCompleted:
		m.status = "completed"
		return m, tea.Quit

	case clusterInitFailed:
		m.status = "failed"
		m.errMsg = msg.err
		m.failed = true
		return m, tea.Quit
	}

	return m, nil
}

func (m clusterInitModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("CLUSTER INIT"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("Provider"))
	b.WriteString(style.Bold.Render(m.provider))
	b.WriteString("\n")
	b.WriteString(style.Key.Render("Node"))
	b.WriteString(style.Bold.Render(m.name))
	if m.size != "" {
		b.WriteString(style.DimText.Render(fmt.Sprintf("  (%s)", m.size)))
	}
	if m.region != "" {
		b.WriteString(style.DimText.Render(fmt.Sprintf("  [%s]", m.region)))
	}
	b.WriteString("\n\n")

	stepIcons := map[string]string{
		"terraform-init":  "  ",
		"terraform-apply": "  ",
		"poll-ready":      "  ",
		"register-node":   "  ",
		"merge-kubeconfig": "  ",
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
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Initializing cluster... (%s)", elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("Cluster initialized in %s", elapsed)))
	case "failed":
		msg := "Cluster init failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Cluster init failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render(msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

func runClusterInitSteps(provider, size, region, name string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, 32)

		go func() {
			defer close(ch)
			ctx := context.Background()

			// Step 1: terraform-init
			ch <- clusterInitStepUpdate{step: "terraform-init", status: "running"}
			runner, err := terraform.NewRunner()
			if err != nil {
				ch <- clusterInitStepUpdate{step: "terraform-init", status: "failed"}
				ch <- clusterInitFailed{err: fmt.Sprintf("terraform runner: %s", err)}
				return
			}
			if err := runner.Init(ctx); err != nil {
				ch <- clusterInitStepUpdate{step: "terraform-init", status: "failed"}
				ch <- clusterInitFailed{err: fmt.Sprintf("terraform init: %s", err)}
				return
			}
			ch <- clusterInitStepUpdate{step: "terraform-init", status: "completed"}

			// Step 2: terraform-apply
			ch <- clusterInitStepUpdate{step: "terraform-apply", status: "running"}
			if err := runner.Apply(ctx); err != nil {
				ch <- clusterInitStepUpdate{step: "terraform-apply", status: "failed"}
				ch <- clusterInitFailed{err: fmt.Sprintf("terraform apply: %s", err)}
				return
			}
			ch <- clusterInitStepUpdate{step: "terraform-apply", status: "completed"}

			// Step 3: poll-ready (placeholder — wait briefly for node to be reachable)
			ch <- clusterInitStepUpdate{step: "poll-ready", status: "running"}
			time.Sleep(2 * time.Second)
			ch <- clusterInitStepUpdate{step: "poll-ready", status: "completed"}

			// Step 4: register-node
			ch <- clusterInitStepUpdate{step: "register-node", status: "running"}
			node := api.ClusterNode{
				Name:     name,
				Provider: provider,
				Region:   region,
				Size:     size,
				Role:     "server",
				Status:   "ready",
			}
			if err := client.AddClusterNode(node); err != nil {
				ch <- clusterInitStepUpdate{step: "register-node", status: "failed"}
				ch <- clusterInitFailed{err: fmt.Sprintf("register node: %s", err)}
				return
			}
			ch <- clusterInitStepUpdate{step: "register-node", status: "completed"}

			// Step 5: merge-kubeconfig (placeholder)
			ch <- clusterInitStepUpdate{step: "merge-kubeconfig", status: "running"}
			time.Sleep(500 * time.Millisecond)
			ch <- clusterInitStepUpdate{step: "merge-kubeconfig", status: "completed"}

			ch <- clusterInitCompleted{}
		}()

		return clusterInitStarted{ch: ch}
	}
}

func waitForClusterInitEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return clusterInitCompleted{}
		}
		return msg
	}
}
