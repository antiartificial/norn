package cmd

import (
	"bytes"
	"strings"
	"testing"

	"norn/v2/cli/api"
)

func TestFleetReplaceRequiresSize(t *testing.T) {
	flag := fleetReplaceCmd.Flags().Lookup("size")
	if flag == nil {
		t.Fatal("fleet replace size flag is missing")
	}
	oldValue, oldChanged := flag.Value.String(), flag.Changed
	t.Cleanup(func() {
		_ = flag.Value.Set(oldValue)
		flag.Changed = oldChanged
	})

	_ = flag.Value.Set("")
	flag.Changed = false
	if err := fleetReplaceCmd.ValidateRequiredFlags(); err == nil {
		t.Fatal("fleet replace accepted a missing --size flag")
	}
	if err := flag.Value.Set("s-8vcpu-16gb"); err != nil {
		t.Fatal(err)
	}
	flag.Changed = true
	if err := fleetReplaceCmd.ValidateRequiredFlags(); err != nil {
		t.Fatalf("fleet replace rejected --size: %v", err)
	}
}

func TestPrintFleetValidationUsesConfiguredWriter(t *testing.T) {
	var output bytes.Buffer
	printFleetValidation(&output, &api.FleetValidationReport{
		Name:  "production-nyc3",
		Valid: false,
		Findings: []api.ValidationFinding{{
			Severity: "error", Code: "fleet.node-pools.required", Message: "at least one node pool is required",
		}},
	})
	for _, want := range []string{"production-nyc3", "fleet.node-pools.required", "at least one node pool is required"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("validation output %q does not contain %q", output.String(), want)
		}
	}
}
