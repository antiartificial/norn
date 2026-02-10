package cmd

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/style"
)

var restartCmd = &cobra.Command{
	Use:   "restart <app>",
	Short: "Rolling restart of all pods",
	Args:  cobra.ExactArgs(1),
	RunE:  runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	appID := args[0]
	p := tea.NewProgram(newRestartModel(appID))
	finalModel, err := p.Run()
	if err != nil {
		return err
	}
	rm := finalModel.(restartModel)
	if rm.err != nil {
		return rm.err
	}
	return nil
}

// --- Messages ---

type restartDone struct{}
type restartErr struct{ err error }

// --- Model ---

type restartModel struct {
	appID   string
	spinner spinner.Model
	done    bool
	err     error
}

func newRestartModel(appID string) restartModel {
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(style.Yellow)
	return restartModel{
		appID:   appID,
		spinner: s,
	}
}

func (m restartModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		doRestart(m.appID),
	)
}

func (m restartModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case restartDone:
		m.done = true
		return m, tea.Quit

	case restartErr:
		m.err = msg.err
		return m, tea.Quit
	}

	return m, nil
}

func (m restartModel) View() string {
	if m.err != nil {
		return style.ErrorBox.Render(fmt.Sprintf("✗ Restart failed: %s", m.err))
	}
	if m.done {
		return style.SuccessBox.Render(fmt.Sprintf("✓ Rolling restart triggered for %s", m.appID)) + "\n"
	}
	return fmt.Sprintf("  %s Restarting %s...\n", m.spinner.View(), style.Bold.Render(m.appID))
}

func doRestart(appID string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(200 * time.Millisecond) // brief delay so spinner is visible
		if err := client.Restart(appID); err != nil {
			return restartErr{err: err}
		}
		return restartDone{}
	}
}
