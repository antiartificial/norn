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
	rootCmd.AddCommand(tuneCmd)
	tuneCmd.AddCommand(tuneRecommendCmd)
	tuneCmd.AddCommand(tuneStatusCmd)
}

var tuneCmd = &cobra.Command{
	Use:   "tune",
	Short: "Show advisory resource tuning recommendations",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTuneRecommendations()
	},
}

var tuneRecommendCmd = &cobra.Command{
	Use:   "recommend",
	Short: "Recommend CPU, memory, and scale changes from live signals",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTuneRecommendations()
	},
}

var tuneStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current tuning signal status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTuneRecommendations()
	},
}

func runTuneRecommendations() error {
	recommendations, err := client.TuningRecommendations()
	if err != nil {
		return fmt.Errorf("failed to fetch tuning recommendations: %w", err)
	}
	if len(recommendations) == 0 {
		fmt.Println("  no running allocations with tuning signals")
		return nil
	}

	fmt.Println(style.Title.Render("resource tuning recommendations"))
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  "+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("PROCESS")+"\t"+
		style.TableHeader.Render("MODE")+"\t"+
		style.TableHeader.Render("CURRENT")+"\t"+
		style.TableHeader.Render("RECOMMENDED")+"\t"+
		style.TableHeader.Render("SIGNALS")+"\t"+
		style.TableHeader.Render("CONF"))

	for _, rec := range recommendations {
		current := formatTuningState(rec.Current)
		recommended := formatTuningState(rec.Recommended)
		if current == recommended {
			recommended = style.DimText.Render("keep")
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			rec.App,
			rec.Process,
			rec.Mode,
			current,
			recommended,
			formatObserved(rec.Observed),
			formatConfidence(rec.Confidence),
		)
	}
	w.Flush()

	fmt.Println()
	for _, rec := range recommendations {
		if len(rec.Actions) == 1 && rec.Actions[0] == "keep" {
			continue
		}
		fmt.Printf("  %s/%s: %s\n", rec.App, rec.Process, strings.Join(rec.Actions, ", "))
		for _, reason := range rec.Reasons {
			fmt.Printf("    %s\n", style.DimText.Render(reason))
		}
		for _, signal := range rec.Signals {
			if !signal.Available && signal.Reason != "" {
				fmt.Printf("    %s\n", style.DimText.Render(signal.Name+": "+signal.Reason))
			}
		}
	}

	return nil
}

func formatTuningState(state api.TuningResourceState) string {
	return fmt.Sprintf("cpu %d, mem %d, scale %d", state.CPU, state.Memory, state.Scale)
}

func formatObserved(observed api.TuningObserved) string {
	peak := observed.PeakMemoryMB
	if peak == 0 {
		peak = observed.UsedMemoryMB
	}
	return fmt.Sprintf("mem %d/%d MB, cpu %.1f%%", observed.UsedMemoryMB, peak, observed.CPUPercent)
}

func formatConfidence(confidence string) string {
	switch confidence {
	case "high":
		return style.Healthy.Render(confidence)
	case "low":
		return style.Warning.Render(confidence)
	default:
		return confidence
	}
}
