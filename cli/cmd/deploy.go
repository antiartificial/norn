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

var deployCmd = &cobra.Command{
	Use:   "deploy <app> <commit-sha>",
	Short: "Deploy a commit to an app",
	Args:  cobra.ExactArgs(2),
	RunE:  runDeploy,
}

func init() {
	rootCmd.AddCommand(deployCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	appID := args[0]
	commitSHA := args[1]

	m := newDeployModel(appID, commitSHA)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	dm := finalModel.(deployModel)
	if dm.failed {
		return fmt.Errorf("deploy failed")
	}
	return nil
}

// --- Messages ---

type wsMsg struct {
	Type    string                 `json:"type"`
	AppID   string                 `json:"appId"`
	Payload map[string]interface{} `json:"payload"`
}

type stepUpdate struct {
	step   string
	status string
}

type deployCompleted struct{}
type deployFailed struct{ err string }
type deployStarted struct{ ch chan tea.Msg }
type wsError struct{ err error }

// --- Model ---

type deployModel struct {
	appID     string
	commitSHA string
	spinner   spinner.Model
	steps     []stepState
	status    string // "connecting" | "deploying" | "completed" | "failed"
	errMsg    string
	failed    bool
	startTime time.Time
	eventCh   chan tea.Msg
}

type stepState struct {
	name   string
	status string // "pending" | "running" | "completed" | "failed"
}

var pipelineSteps = []string{"build", "test", "snapshot", "migrate", "deploy"}

func newDeployModel(appID, commitSHA string) deployModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(style.Primary)

	steps := make([]stepState, len(pipelineSteps))
	for i, name := range pipelineSteps {
		steps[i] = stepState{name: name, status: "pending"}
	}

	return deployModel{
		appID:     appID,
		commitSHA: commitSHA,
		spinner:   s,
		steps:     steps,
		status:    "connecting",
		startTime: time.Now(),
	}
}

func (m deployModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		connectAndDeploy(m.appID, m.commitSHA),
	)
}

func (m deployModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case deployStarted:
		m.status = "deploying"
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

	case deployCompleted:
		m.status = "completed"
		return m, tea.Quit

	case deployFailed:
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

func (m deployModel) View() string {
	var b strings.Builder

	b.WriteString(style.Banner.Render("⚡ NORN DEPLOY"))
	b.WriteString("\n")

	b.WriteString(style.Key.Render("App"))
	b.WriteString(style.Bold.Render(m.appID))
	b.WriteString("\n")
	b.WriteString(style.Key.Render("Commit"))
	sha := m.commitSHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	b.WriteString(lipgloss.NewStyle().Foreground(style.Cyan).Render(sha))
	b.WriteString("\n\n")

	stepIcons := map[string]string{
		"build":    "🔨",
		"test":     "🧪",
		"snapshot": "📸",
		"migrate":  "🗃️",
		"deploy":   "🚀",
	}

	for _, step := range m.steps {
		icon := stepIcons[step.name]
		name := padRight(step.name, 12)

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
	case "deploying":
		b.WriteString(m.spinner.View() + style.DimText.Render(fmt.Sprintf(" Pipeline running... (%s)", elapsed)))
	case "completed":
		b.WriteString(style.SuccessBox.Render(fmt.Sprintf("✓ Deploy completed in %s", elapsed)))
	case "failed":
		msg := "Deploy failed"
		if m.errMsg != "" {
			msg = fmt.Sprintf("Deploy failed: %s", m.errMsg)
		}
		b.WriteString(style.ErrorBox.Render("✗ " + msg))
	}

	b.WriteString("\n")
	return b.String()
}

// --- Commands ---

// connectAndDeploy connects to the WebSocket first, triggers the deploy via HTTP,
// then starts a goroutine that reads WS events and sends them to a channel.
func connectAndDeploy(appID, commitSHA string) tea.Cmd {
	return func() tea.Msg {
		// Connect WebSocket first so we don't miss events
		conn, _, err := websocket.DefaultDialer.Dial(client.WebSocketURL(), nil)
		if err != nil {
			return wsError{err: fmt.Errorf("websocket connect: %w", err)}
		}

		// Trigger the deploy
		if err := client.Deploy(appID, commitSHA); err != nil {
			conn.Close()
			return wsError{err: err}
		}

		// Start reading events in the background
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
				case "deploy.step":
					step, _ := event.Payload["step"].(string)
					status, _ := event.Payload["status"].(string)
					ch <- stepUpdate{step: step, status: status}
				case "deploy.completed":
					ch <- deployCompleted{}
					return
				case "deploy.failed":
					errStr, _ := event.Payload["error"].(string)
					ch <- deployFailed{err: errStr}
					return
				}
			}
		}()

		return deployStarted{ch: ch}
	}
}

// waitForEvent reads the next event from the channel.
func waitForEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return deployCompleted{}
		}
		return msg
	}
}
