package main

import (
	"fmt"
	"os"

	"norn/v2/api/model"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: catalog-publisher-validate INFRASPEC")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read InfraSpec: %v\n", err)
		os.Exit(1)
	}
	spec, err := model.ParseInfraSpecDocument(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "strict InfraSpec parse: %v\n", err)
		os.Exit(1)
	}
	result := model.ValidateSpecWithOptions(spec, model.ValidationOptions{NetworkMode: "public"})
	if !result.Valid {
		for _, finding := range result.Findings {
			if finding.Severity == "error" {
				fmt.Fprintf(os.Stderr, "%s: %s\n", finding.Field, finding.Message)
			}
		}
		os.Exit(1)
	}
}
