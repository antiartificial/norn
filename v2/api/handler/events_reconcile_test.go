package handler

import "testing"

func TestCronParentJobID(t *testing.T) {
	tests := []struct {
		name    string
		app     string
		process string
		jobID   string
		want    string
	}{
		{
			name:  "child job",
			jobID: "field-harbor-field-harbor-sync-pm/periodic-1781658600",
			want:  "field-harbor-field-harbor-sync-pm",
		},
		{
			name:  "parent job",
			jobID: "field-harbor-field-harbor-sync-pm",
			want:  "field-harbor-field-harbor-sync-pm",
		},
		{
			name:    "app and process",
			app:     "field-harbor",
			process: "field-harbor-sync-pm",
			want:    "field-harbor-field-harbor-sync-pm",
		},
		{
			name: "missing evidence",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronParentJobID(tt.app, tt.process, tt.jobID); got != tt.want {
				t.Fatalf("cronParentJobID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetadataString(t *testing.T) {
	metadata := map[string]interface{}{
		"process": "field-harbor-sync-pm",
		"attempt": 2,
	}
	if got := metadataString(metadata, "process"); got != "field-harbor-sync-pm" {
		t.Fatalf("metadataString(process) = %q", got)
	}
	if got := metadataString(metadata, "attempt"); got != "2" {
		t.Fatalf("metadataString(attempt) = %q", got)
	}
	if got := metadataString(metadata, "missing"); got != "" {
		t.Fatalf("metadataString(missing) = %q", got)
	}
}
