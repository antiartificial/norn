package nomad

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func TestPeriodicForceEvaluationRequiresExactChildAndPeriodicTrigger(t *testing.T) {
	for _, test := range []struct {
		name, id, jobID, trigger string
		valid                    bool
	}{
		{"matching", "eval-1", "widget-nightly/periodic-123", "periodic-job", true},
		{"wrong-evaluation", "eval-2", "widget-nightly/periodic-123", "periodic-job", false},
		{"other-parent", "eval-1", "other-nightly/periodic-123", "periodic-job", false},
		{"lookalike-parent", "eval-1", "widget-nightly-extra/periodic-123", "periodic-job", false},
		{"ordinary-dispatch", "eval-1", "widget-nightly/periodic-123", "job-register", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestNomadClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/evaluation/eval-1" {
					t.Fatalf("request=%s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(&nomadapi.Evaluation{ID: test.id, JobID: test.jobID, TriggeredBy: test.trigger})
			}))
			child, err := client.PeriodicForceEvaluation(context.Background(), "eval-1", "widget-nightly")
			if test.valid && (err != nil || child != test.jobID) {
				t.Fatalf("valid evaluation child=%q err=%v", child, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("accepted forged evaluation child=%q", child)
			}
		})
	}
}
