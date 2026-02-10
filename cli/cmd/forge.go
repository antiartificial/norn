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

var forgeCmd = &cobra.Command{
	Use:   "forge <app>",
	Short: "Provision infrastructure for an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runForge,
}

func init() {
	rootCmd.AddCommand(forgeCmd)
}

func runForge(cmd *cobra.Command, args []string) error {
	appID := args[0]

	m := newForgeModel(appID)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	fm := finalModel.(forgeModel)
	if fm.failed {
		return fmt.Errorf("forge failed")
	}
	return nil
}

// --- Messages ---

type forgeCompleted struct{}
type forgeFailed struct{ err string }
type forgeStarted struct{ ch chan tea.Msg }

// --- Model ---

type forgeModel struct {
	appID     string
	spinner   spinner.Model
	steps     []stepState
	status    string // "connecting" | "forging" | "completed" | "failed"
	errMsg    string
	failed    bool
	startTime time.Time
	eventCh   chan tea.Msg
}

var forgeSteps = []string{"create-deployment", "create-service", "patch-cloudflared", "create-dns-route", "restart-cloudflared"}

func newForgeModel(appID string) forgeModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	steps := make([]stepState, len(forgeSteps))
	for i, name := range forgeSteps {
		steps[i] = stepState{name: name, status: "pending"}
	}

	return forgeModel{
		appID:     appID,
		spinner:   s,
		steps:     steps,
		status:    "connecting",
		startTime: time.Now(),
	}
}

func (m forgeModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		connectAndForge(m.appID),
	)
}

func (m forgeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case forgeStarted:
		m.status = "forging"
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

	case forgeCompleted:
		m.status = "completed"
		return m, tea.Quit

	case forgeFailed:
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

func (m forgeModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("⚒️  NORN FORGE"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("App"))
	b.WriteString(style.Bold.Render(m.appID))
	b.WriteString("\n\n")

	stepIcons := map[string]string{
		"create-deployment":  "📦",
		"create-service":     "🔗",
		"patch-cloudflared":  "☁️",
		"create-dns-route":   "🌐",
		"restart-cloudflared": "🔄",
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
		case "failed":
			b.WriteString(fmt.Sprintf("  %s %s %s\n", icon, style.StepFailed.Render(name), style.StepFailed.Render("✗ failed")))
		}
	}

	b.WriteString("\n")

	elapsed := time.Since(m.startTime).Round(time.Second)

	switch m.status {
	case "connecting":
		b.WriteString(m.spinner.View() + style.DimText.Render(" Connecting to API..."))
	case "forging":
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Forging infrastructure... (%s)", elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("✓ Forge completed in %s", elapsed)))
	case "failed":
		msg := "Forge failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Forge failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render("✗ " + msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

func connectAndForge(appID string) tea.Cmd {
	return func() tea.Msg {
		conn, _, err := websocket.DefaultDialer.Dial(client.WebSocketURL(), nil)
		if err != nil {
			return wsError{err: fmt.Errorf("websocket connect: %w", err)}
		}

		if err := client.Forge(appID); err != nil {
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
				case "forge.step":
					step, _ := event.Payload["step"].(string)
					status, _ := event.Payload["status"].(string)
					ch <- stepUpdate{step: step, status: status}
				case "forge.completed":
					ch <- forgeCompleted{}
					return
				case "forge.failed":
					errStr, _ := event.Payload["error"].(string)
					ch <- forgeFailed{err: errStr}
					return
				}
			}
		}()

		return forgeStarted{ch: ch}
	}
}
