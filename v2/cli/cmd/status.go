package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all apps",
	RunE: func(cmd *cobra.Command, args []string) error {
		apps, err := client.ListApps()
		if err != nil {
			return fmt.Errorf("failed to fetch apps: %w", err)
		}

		if len(apps) == 0 {
			fmt.Println(style.DimText.Render("no apps discovered"))
			return nil
		}
		deployments, err := client.ListDeployments("")
		if err != nil {
			return fmt.Errorf("failed to fetch deployments: %w", err)
		}
		latestByApp := map[string]apiDeployment{}
		for _, deployment := range deployments {
			if _, ok := latestByApp[deployment.App]; !ok {
				latestByApp[deployment.App] = apiDeployment{
					imageTag:    deployment.ImageTag,
					commitSHA:   deployment.CommitSHA,
					sourceKind:  deployment.SourceKind,
					sourceDirty: deployment.SourceDirty,
				}
			}
		}

		fmt.Println(style.Title.Render("norn v2 status"))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
			style.TableHeader.Render("STATUS")+"\t"+
			style.TableHeader.Render("ALLOCS")+"\t"+
			style.TableHeader.Render("IMAGE")+"\t"+
			style.TableHeader.Render("COMMIT")+"\t"+
			style.TableHeader.Render("SOURCE")+"\t"+
			style.TableHeader.Render("PROCESSES"))

		for _, app := range apps {
			name := app.Spec.App
			dot := style.NomadStatusDot(app.NomadStatus)

			allocStr := "0"
			allocationSummary := summarizedAllocations(app)
			if allocationSummary.Total > 0 {
				allocStr = fmt.Sprintf("%d live", allocationSummary.Running)
				if allocationSummary.Retained > 0 {
					allocStr += fmt.Sprintf(" (%d retained)", allocationSummary.Retained)
				}
			}

			var procs []string
			for pName := range app.Spec.Processes {
				procs = append(procs, pName)
			}
			deployment := latestByApp[name]
			image := deployment.imageTag
			if image == "" {
				image = "-"
			}
			commit := shortValue(deployment.commitSHA, 12)
			if commit == "" {
				commit = "-"
			}
			source := deployment.sourceKind
			if source == "" {
				source = "-"
			}
			if deployment.sourceDirty {
				source += "*"
			}

			fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				dot,
				style.Bold.Render(name),
				app.NomadStatus,
				allocStr,
				image,
				commit,
				source,
				strings.Join(procs, ", "),
			)
		}
		w.Flush()
		return nil
	},
}

type apiDeployment struct {
	imageTag    string
	commitSHA   string
	sourceKind  string
	sourceDirty bool
}

func summarizedAllocations(app api.AppStatus) api.AllocationSummary {
	if app.AllocationSummary.Total > 0 {
		return app.AllocationSummary
	}
	var s api.AllocationSummary
	for _, a := range app.Allocations {
		s.Total++
		if a.Status == "running" {
			s.Running++
		}
		switch a.Status {
		case "complete", "failed", "lost":
			s.Retained++
		default:
			s.Active++
		}
	}
	return s
}
