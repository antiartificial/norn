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

var rollbackCmd = &cobra.Command{
	Use:   "rollback <app>",
	Short: "Rollback to the previous deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runRollback,
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(cmd *cobra.Command, args []string) error {
	appID := args[0]
	p := tea.NewProgram(newRollbackModel(appID))
	finalModel, err := p.Run()
	if err != nil {
		return err
	}
	rm := finalModel.(rollbackModel)
	if rm.err != nil {
		return rm.err
	}
	return nil
}

// --- Messages ---

type rollbackDone struct{}
type rollbackErr struct{ err error }

// --- Model ---

type rollbackModel struct {
	appID   string
	spinner spinner.Model
	done    bool
	err     error
}

func newRollbackModel(appID string) rollbackModel {
	s := spinner.New()
	s.Spinner = spinner.Moon
	s.Style = lipgloss.NewStyle().Foreground(style.Cyan)
	return rollbackModel{
		appID:   appID,
		spinner: s,
	}
}

func (m rollbackModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		doRollback(m.appID),
	)
}

func (m rollbackModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case rollbackDone:
		m.done = true
		return m, tea.Quit

	case rollbackErr:
		m.err = msg.err
		return m, tea.Quit
	}

	return m, nil
}

func (m rollbackModel) View() string {
	if m.err != nil {
		return style.ErrorBox.Render(fmt.Sprintf("✗ Rollback failed: %s", m.err))
	}
	if m.done {
		return style.SuccessBox.Render(fmt.Sprintf("✓ Rolled back %s to previous deployment", m.appID)) + "\n"
	}
	return fmt.Sprintf("  %s Rolling back %s...\n", m.spinner.View(), style.Bold.Render(m.appID))
}

func doRollback(appID string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(200 * time.Millisecond)
		if err := client.Rollback(appID); err != nil {
			return rollbackErr{err: err}
		}
		return rollbackDone{}
	}
}
