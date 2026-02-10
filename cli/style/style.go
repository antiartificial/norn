package style

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	Primary   = lipgloss.Color("#7C3AED")
	Green     = lipgloss.Color("#10B981")
	Red       = lipgloss.Color("#EF4444")
	Yellow    = lipgloss.Color("#F59E0B")
	Cyan      = lipgloss.Color("#06B6D4")
	Dim       = lipgloss.Color("#6B7280")
	White     = lipgloss.Color("#F9FAFB")
	DarkBg    = lipgloss.Color("#1F2937")

	// Text styles
	Title = lipgloss.NewStyle().
		Bold(true).
		Foreground(Primary).
		MarginBottom(1)

	Subtitle = lipgloss.NewStyle().
		Foreground(Dim).
		Italic(true)

	Bold = lipgloss.NewStyle().Bold(true).Foreground(White)

	Healthy = lipgloss.NewStyle().Foreground(Green).Bold(true)
	Unhealthy = lipgloss.NewStyle().Foreground(Red).Bold(true)
	Warning = lipgloss.NewStyle().Foreground(Yellow)

	DimText = lipgloss.NewStyle().Foreground(Dim)

	// Status indicators
	DotHealthy   = Healthy.Render("●")
	DotUnhealthy = Unhealthy.Render("●")
	DotWarning   = Warning.Render("●")
	DotDim       = DimText.Render("●")

	// Badges
	Badge = lipgloss.NewStyle().
		Padding(0, 1).
		Bold(true)

	RoleBadge = Badge.Foreground(Cyan)

	ServiceBadge = lipgloss.NewStyle().
		Foreground(Primary).
		Bold(true)

	// Borders
	CardStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#374151")).
		Padding(1, 2).
		MarginBottom(1)

	CardHealthy = CardStyle.BorderForeground(Green)
	CardUnhealthy = CardStyle.BorderForeground(Red)

	// Header / banner
	Banner = lipgloss.NewStyle().
		Bold(true).
		Foreground(Primary).
		MarginBottom(1)

	// Step indicators
	StepPending  = DimText
	StepRunning  = lipgloss.NewStyle().Foreground(Yellow).Bold(true)
	StepDone     = lipgloss.NewStyle().Foreground(Green)
	StepFailed   = lipgloss.NewStyle().Foreground(Red).Bold(true)

	// Table
	TableHeader = lipgloss.NewStyle().
		Bold(true).
		Foreground(Primary).
		BorderBottom(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(Dim).
		PaddingRight(2)

	TableCell = lipgloss.NewStyle().PaddingRight(2)

	// Error box
	ErrorBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Red).
		Foreground(Red).
		Padding(0, 1).
		MarginTop(1)

	// Success box
	SuccessBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Green).
		Foreground(Green).
		Padding(0, 1).
		MarginTop(1)

	// Key-value
	Key = lipgloss.NewStyle().Foreground(Dim).Width(14)
	Val = lipgloss.NewStyle().Foreground(White)
)

func StatusDot(healthy bool) string {
	if healthy {
		return DotHealthy
	}
	return DotUnhealthy
}

func ServiceDot(status string) string {
	switch status {
	case "up":
		return DotHealthy
	case "down":
		return DotUnhealthy
	default:
		return DotDim
	}
}
