package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/api"
	"norn/cli/style"
)

var statusCmd = &cobra.Command{
	Use:     "status [app]",
	Short:   "Show status of all apps or a specific app",
	Aliases: []string{"s", "ls"},
	Args:    cobra.MaximumNArgs(1),
	RunE:    runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return showAppDetail(args[0])
	}
	return showAllApps()
}

func showAllApps() error {
	apps, err := client.ListApps()
	if err != nil {
		return fmt.Errorf("failed to fetch apps: %w", err)
	}

	if len(apps) == 0 {
		fmt.Println(style.DimText.Render("No apps discovered. Add an infraspec.yaml to a project directory."))
		return nil
	}

	fmt.Println(style.Banner.Render("⚡ NORN") + style.Subtitle.Render(fmt.Sprintf("  %d app(s)", len(apps))))
	fmt.Println()

	// Table header
	header := fmt.Sprintf(
		"  %-2s  %-20s %-12s %-8s %-10s %-24s %s",
		"", "APP", "ROLE", "READY", "SHA", "HOSTS", "SERVICES",
	)
	fmt.Println(style.TableHeader.Render(header))

	for _, app := range apps {
		printAppRow(app)
	}
	fmt.Println()

	return nil
}

func printAppRow(app api.AppStatus) {
	dot := style.StatusDot(app.Healthy)

	name := style.Bold.Render(padRight(app.Spec.App, 20))

	role := style.RoleBadge.Render(padRight(app.Spec.Role, 12))

	ready := padRight(app.Ready, 8)
	if app.Healthy {
		ready = style.Healthy.Render(ready)
	} else {
		ready = style.Unhealthy.Render(ready)
	}

	sha := style.DimText.Render(padRight("—", 10))
	if app.CommitSHA != "" {
		sha = lipgloss.NewStyle().Foreground(style.Cyan).Render(padRight(app.CommitSHA[:min(7, len(app.CommitSHA))], 10))
	}

	hosts := buildHostsStr(app)
	services := buildServicesStr(app)

	fmt.Printf("  %s  %s %s %s %s %-24s %s\n",
		dot, name, role, ready, sha, hosts, services)
}

func showAppDetail(appID string) error {
	app, err := client.GetApp(appID)
	if err != nil {
		return fmt.Errorf("failed to fetch app: %w", err)
	}

	cardStyle := style.CardHealthy
	if !app.Healthy {
		cardStyle = style.CardUnhealthy
	}

	var b strings.Builder

	// Title line
	b.WriteString(style.Bold.Render(app.Spec.App))
	b.WriteString("  ")
	b.WriteString(style.RoleBadge.Render(app.Spec.Role))
	if app.Healthy {
		b.WriteString("  " + style.Healthy.Render("● healthy"))
	} else {
		b.WriteString("  " + style.Unhealthy.Render("● unhealthy"))
	}
	b.WriteString("\n\n")

	// Key-value details
	kvLine := func(k, v string) {
		b.WriteString(style.Key.Render(k))
		b.WriteString(style.Val.Render(v))
		b.WriteString("\n")
	}

	kvLine("Ready", app.Ready)
	if app.CommitSHA != "" {
		kvLine("Commit", app.CommitSHA[:min(7, len(app.CommitSHA))])
	}
	if app.DeployedAt != "" {
		kvLine("Deployed", app.DeployedAt)
	}
	if app.Spec.Hosts != nil {
		if app.Spec.Hosts.External != "" {
			kvLine("External", app.Spec.Hosts.External)
		}
		if app.Spec.Hosts.Internal != "" {
			kvLine("Internal", app.Spec.Hosts.Internal)
		}
	}

	// Services
	services := []string{}
	if app.Spec.Services != nil {
		if app.Spec.Services.Postgres != nil {
			services = append(services, "pg:"+app.Spec.Services.Postgres.Database)
		}
		if app.Spec.Services.KV != nil {
			services = append(services, "kv:"+app.Spec.Services.KV.Namespace)
		}
		if app.Spec.Services.Events != nil {
			services = append(services, "events:"+strings.Join(app.Spec.Services.Events.Topics, ","))
		}
	}
	if len(services) > 0 {
		kvLine("Services", strings.Join(services, "  "))
	}
	if len(app.Spec.Secrets) > 0 {
		kvLine("Secrets", fmt.Sprintf("%d key(s)", len(app.Spec.Secrets)))
	}

	// Pods
	if len(app.Pods) > 0 {
		b.WriteString("\n")
		b.WriteString(style.TableHeader.Render("  Pods"))
		b.WriteString("\n")
		for _, pod := range app.Pods {
			dot := style.DotHealthy
			if !pod.Ready {
				dot = style.DotUnhealthy
			}
			b.WriteString(fmt.Sprintf("  %s %s  %s\n", dot, pod.Name, style.DimText.Render(pod.Status)))
		}
	}

	// Recent deployments
	if len(app.Deployments) > 0 {
		b.WriteString("\n")
		b.WriteString(style.TableHeader.Render("  Recent Deployments"))
		b.WriteString("\n")
		limit := min(5, len(app.Deployments))
		for _, d := range app.Deployments[:limit] {
			statusStyle := style.DimText
			switch d.Status {
			case "deployed":
				statusStyle = style.StepDone
			case "failed":
				statusStyle = style.StepFailed
			case "building", "testing", "migrating", "deploying":
				statusStyle = style.StepRunning
			}
			sha := d.CommitSHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n",
				statusStyle.Render(padRight(d.Status, 12)),
				lipgloss.NewStyle().Foreground(style.Cyan).Render(sha),
				style.DimText.Render(d.CreatedAt),
			))
		}
	}

	fmt.Println(cardStyle.Render(b.String()))
	return nil
}

func buildHostsStr(app api.AppStatus) string {
	parts := []string{}
	if app.Spec.Hosts != nil {
		if app.Spec.Hosts.External != "" {
			parts = append(parts, "🌐 "+app.Spec.Hosts.External)
		}
		if app.Spec.Hosts.Internal != "" {
			parts = append(parts, app.Spec.Hosts.Internal)
		}
	}
	if len(parts) == 0 {
		return style.DimText.Render("—")
	}
	return strings.Join(parts, " ")
}

func buildServicesStr(app api.AppStatus) string {
	parts := []string{}
	if app.Spec.Services != nil {
		if app.Spec.Services.Postgres != nil {
			parts = append(parts, style.ServiceBadge.Render("PG"))
		}
		if app.Spec.Services.KV != nil {
			parts = append(parts, style.ServiceBadge.Render("KV"))
		}
		if app.Spec.Services.Events != nil {
			parts = append(parts, style.ServiceBadge.Render("EV"))
		}
	}
	if len(app.Spec.Secrets) > 0 {
		parts = append(parts, style.DimText.Render(fmt.Sprintf("🔑%d", len(app.Spec.Secrets))))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
