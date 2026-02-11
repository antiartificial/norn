package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"norn/cli/style"
)

var teardownCmd = &cobra.Command{
	Use:   "teardown <app>",
	Short: "Remove infrastructure for an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runTeardown,
}

func init() {
	rootCmd.AddCommand(teardownCmd)
}

func runTeardown(cmd *cobra.Command, args []string) error {
	appID := args[0]

	fmt.Printf("This will tear down all infrastructure for %s:\n", appID)
	fmt.Println("  - Remove DNS route")
	fmt.Println("  - Remove cloudflared ingress rule")
	fmt.Println("  - Restart cloudflared")
	fmt.Println("  - Delete K8s service")
	fmt.Println("  - Delete K8s deployment")
	fmt.Print("\nContinue? [y/N] ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("Aborted.")
		return nil
	}

	m := newTeardownModel(appID)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	tm := finalModel.(teardownModel)
	if tm.failed {
		return fmt.Errorf("teardown failed")
	}
	return nil
}

// --- Messages ---

type tdCompleted struct{}
type tdFailed struct{ err string }
type tdStarted struct{ ch chan tea.Msg }

// --- Model ---

type teardownModel struct {
	appID     string
	spinner   spinner.Model
	steps     []stepState
	status    string // "connecting" | "tearing_down" | "completed" | "failed"
	errMsg    string
	failed    bool
	startTime time.Time
	eventCh   chan tea.Msg
}

var tdSteps = []string{"remove-dns-route", "unpatch-cloudflared", "restart-cloudflared", "delete-service", "delete-deployment"}

func newTeardownModel(appID string) teardownModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	steps := make([]stepState, len(tdSteps))
	for i, name := range tdSteps {
		steps[i] = stepState{name: name, status: "pending"}
	}

	return teardownModel{
		appID:     appID,
		spinner:   s,
		steps:     steps,
		status:    "connecting",
		startTime: time.Now(),
	}
}

func (m teardownModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		connectAndTeardown(m.appID),
	)
}

func (m teardownModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tdStarted:
		m.status = "tearing_down"
		m.eventCh = msg.ch
		return m, waitForEvent(m.eventCh)

	case stepUpdate:
		for i := range m.steps {
			if m.steps[i].name == msg.step {
				m.steps[i].status = msg.status
				break
			}
		}
		return m, waitForEvent(m.eventCh)

	case tdCompleted:
		m.status = "completed"
		return m, tea.Quit

	case tdFailed:
		m.status = "failed"
		m.errMsg = msg.err
		m.failed = true
		return m, tea.Quit

	case wsError:
		m.status = "failed"
		m.errMsg = msg.err.Error()
		m.failed = true
		return m, tea.Quit
	}

	return m, nil
}

func (m teardownModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("🗑️  NORN TEARDOWN"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("App"))
	b.WriteString(style.Bold.Render(m.appID))
	b.WriteString("\n\n")

	stepIcons := map[string]string{
		"remove-dns-route":    "🌐",
		"unpatch-cloudflared": "🔗",
		"restart-cloudflared": "🔄",
		"delete-service":      "🔗",
		"delete-deployment":   "📦",
	}

	for _, step := range m.steps {
		icon := stepIcons[step.name]
		name := padRight(step.name, 22)

		switch step.status {
		case "pending":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.DimText.Render(name), style.DimText.Render("waiting")))
		case "running":
			b.WriteString(fmt.Sprintf("  %s %s %s %s\n", icon, style.StepRunning.Render(name), m.spinner.View(), style.StepRunning.Render("running")))
		case "completed":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.StepDone.Render(name), style.StepDone.Render("✓ done")))
		case "skipped":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.DimText.Render(name), style.DimText.Render("↷ skipped")))
		case "failed":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.StepFailed.Render(name), style.StepFailed.Render("✗ failed")))
		}
	}

	b.WriteString("\n")

	elapsed := time.Since(m.startTime).Round(time.Second)

	switch m.status {
	case "connecting":
		b.WriteString(m.spinner.View() + style.DimText.Render(" Connecting to API..."))
	case "tearing_down":
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Tearing down infrastructure... (%s)", elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("✓ Teardown completed in %s", elapsed)))
	case "failed":
		msg := "Teardown failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Teardown failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render("✗ " + msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

func connectAndTeardown(appID string) tea.Cmd {
	return func() tea.Msg {
		conn, _, err := websocket.DefaultDialer.Dial(client.WebSocketURL(), nil)
		if err != nil {
			return wsError{err: fmt.Errorf("websocket connect: %w", err)}
		}

		if err := client.Teardown(appID); err != nil {
			conn.Close()
			return wsError{err: err}
		}

		ch := make(chan tea.Msg, 32)
		go func() {
			defer conn.Close()
			defer close(ch)

			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					ch <- wsError{err: fmt.Errorf("websocket read: %w", err)}
					return
				}

				var event wsMsg
				if err := json.Unmarshal(message, &event); err != nil {
					continue
				}
				if event.AppID != appID {
					continue
				}

				switch event.Type {
				case "teardown.step":
					step, _ := event.Payload["step"].(string)
					status, _ := event.Payload["status"].(string)
					ch <- stepUpdate{step: step, status: status}
				case "teardown.completed":
					ch <- tdCompleted{}
					return
				case "teardown.failed":
					errStr, _ := event.Payload["error"].(string)
					ch <- tdFailed{err: errStr}
					return
				}
			}
		}()

		return tdStarted{ch: ch}
	}
}
