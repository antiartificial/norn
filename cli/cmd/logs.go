package cmd

import (
	"bufio"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/style"
)

var logsCmd = &cobra.Command{
	Use:   "logs <app>",
	Short: "Stream live pod logs",
	Args:  cobra.ExactArgs(1),
	RunE:  runLogs,
}

func init() {
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	appID := args[0]
	p := tea.NewProgram(newLogsModel(appID), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- Messages ---

type logLine struct {
	line string
}

type logStreamDone struct{}
type logStreamError struct {
	err error
}

// --- Model ---

type logsModel struct {
	appID    string
	viewport viewport.Model
	lines    []string
	ready    bool
	err      error
}

func newLogsModel(appID string) logsModel {
	return logsModel{
		appID: appID,
		lines: []string{},
	}
}

func (m logsModel) Init() tea.Cmd {
	return streamLogs(m.appID)
}

func (m logsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		headerHeight := 3
		m.viewport = viewport.New(msg.Width, msg.Height-headerHeight)
		m.viewport.SetContent(strings.Join(m.lines, "\n"))
		m.ready = true
		return m, nil

	case logLine:
		m.lines = append(m.lines, msg.line)
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			m.viewport.GotoBottom()
		}
		return m, waitForLogLine(m.appID)

	case logStreamDone:
		m.lines = append(m.lines, style.DimText.Render("--- stream ended ---"))
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			m.viewport.GotoBottom()
		}
		return m, nil

	case logStreamError:
		m.err = msg.err
		return m, nil
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m logsModel) View() string {
	if m.err != nil {
		return style.ErrorBox.Render(fmt.Sprintf("Error: %s", m.err))
	}

	header := lipgloss.JoinHorizontal(lipgloss.Top,
		style.Banner.Render("⚡ LOGS"),
		"  ",
		style.Bold.Render(m.appID),
		"  ",
		style.DimText.Render("q to quit • ↑↓ to scroll"),
	)

	if !m.ready {
		return header + "\n\n" + style.DimText.Render("Connecting...")
	}

	return header + "\n" + m.viewport.View()
}

// We use a channel-based approach: first call starts the stream,
// subsequent calls read one line at a time.

var logScanners = map[string]*bufio.Scanner{}

func streamLogs(appID string) tea.Cmd {
	return func() tea.Msg {
		body, err := client.StreamLogs(appID)
		if err != nil {
			return logStreamError{err: err}
		}
		scanner := bufio.NewScanner(body)
		logScanners[appID] = scanner

		if scanner.Scan() {
			return logLine{line: scanner.Text()}
		}
		return logStreamDone{}
	}
}

func waitForLogLine(appID string) tea.Cmd {
	return func() tea.Msg {
		scanner, ok := logScanners[appID]
		if !ok {
			return logStreamDone{}
		}
		if scanner.Scan() {
			return logLine{line: scanner.Text()}
		}
		return logStreamDone{}
	}
}
