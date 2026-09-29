package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCronPauseAndResumeSendIdempotencyKeyAndDecodeAcceptedOperation(t *testing.T) {
	for _, tc := range []struct{ action, kind string }{{"pause", "app.cron-pause"}, {"resume", "app.cron-resume"}} {
		t.Run(tc.action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/apps/demo/cron/"+tc.action || r.Header.Get("Idempotency-Key") != "retry-1" {
					t.Fatalf("method=%s path=%s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"id":"operation-1","kind":"` + tc.kind + `","status":"queued"}`))
			}))
			defer server.Close()
			client := &Client{BaseURL: server.URL, HTTPClient: server.Client()}
			var op *Operation
			var err error
			if tc.action == "pause" {
				op, err = client.CronPause("demo", "nightly", "retry-1")
			} else {
				op, err = client.CronResume("demo", "nightly", "retry-1")
			}
			if err != nil || op == nil || op.ID != "operation-1" || op.Kind != tc.kind {
				t.Fatalf("operation=%+v err=%v", op, err)
			}
		})
	}
}

func TestCronPauseAndResumeRejectMissingRetryKey(t *testing.T) {
	client := &Client{BaseURL: "http://unused"}
	if _, err := client.CronPause("demo", "nightly", ""); err == nil {
		t.Fatal("pause accepted no key")
	}
	if _, err := client.CronResume("demo", "nightly", ""); err == nil {
		t.Fatal("resume accepted no key")
	}
}
