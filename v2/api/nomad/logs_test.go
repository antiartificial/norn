package nomad

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

// followingLogServer serves one allocation whose log streams emit a frame
// and then stay open (a quiet followed task) until the request is cancelled.
type followingLogServer struct {
	open, closed atomic.Int32
	// flood keeps sending frames until the request is cancelled.
	flood bool
}

func (s *followingLogServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/job/shop/allocations":
		_ = json.NewEncoder(w).Encode([]*nomadapi.AllocationListStub{{ID: "alloc-1", ClientStatus: "running", NodeID: "node-1"}})
	case r.URL.Path == "/v1/allocation/alloc-1":
		// The job lists another group first; the allocation belongs to "web".
		worker, web := "worker", "web"
		_ = json.NewEncoder(w).Encode(&nomadapi.Allocation{ID: "alloc-1", NodeID: "node-1", TaskGroup: "web",
			Job: &nomadapi.Job{TaskGroups: []*nomadapi.TaskGroup{{Name: &worker, Tasks: []*nomadapi.Task{{Name: "wrong-task"}}}, {Name: &web, Tasks: []*nomadapi.Task{{Name: "web"}}}}}})
	case r.URL.Path == "/v1/client/fs/logs/alloc-1":
		if r.URL.Query().Get("task") != "web" {
			http.Error(w, "wrong task", http.StatusBadRequest)
			return
		}
		s.open.Add(1)
		defer s.closed.Add(1)
		_ = json.NewEncoder(w).Encode(&nomadapi.StreamFrame{Data: []byte(r.URL.Query().Get("type") + " line\n")})
		w.(http.Flusher).Flush()
		for s.flood && r.Context().Err() == nil {
			if err := json.NewEncoder(w).Encode(&nomadapi.StreamFrame{Data: []byte(strings.Repeat("x", 16<<10))}); err != nil {
				break
			}
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	default:
		http.NotFound(w, r)
	}
}

// A client that stops reading mid-stream leaves the pipe writer blocked;
// cancelling the request must still end both upstream Nomad requests.
func TestStreamLogsCancelsUpstreamWhileWriterIsBlocked(t *testing.T) {
	fake := &followingLogServer{flood: true}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := client.StreamLogs(ctx, "shop", true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// Never read: wait until both upstream streams are open, then cancel.
	deadline := time.Now().Add(5 * time.Second)
	for fake.open.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // the writer is now blocked on the pipe
	cancel()
	for fake.closed.Load() < 2 && time.Now().Before(deadline.Add(5*time.Second)) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.closed.Load() != 2 {
		t.Fatalf("upstream requests open=%d closed=%d after cancelling a non-reading client", fake.open.Load(), fake.closed.Load())
	}
	// The blocked writer was released rather than left waiting forever: the
	// read side is closed, so no stale frame is handed over after cancellation.
	if n, err := reader.Read(make([]byte, 64)); err == nil {
		t.Fatalf("read %d bytes from a cancelled stream; the pipe writer is still blocked", n)
	}
}

func TestStreamLogsCancelsUpstreamWhenClientLeaves(t *testing.T) {
	for name, end := range map[string]func(io.ReadCloser, context.CancelFunc){
		"reader closed":    func(reader io.ReadCloser, _ context.CancelFunc) { _ = reader.Close() },
		"context canceled": func(_ io.ReadCloser, cancel context.CancelFunc) { cancel() },
	} {
		t.Run(name, func(t *testing.T) {
			fake := &followingLogServer{}
			server := httptest.NewServer(fake)
			defer server.Close()
			client, err := NewClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader, err := client.StreamLogs(ctx, "shop", true)
			if err != nil {
				t.Fatal(err)
			}
			seen := ""
			buf := make([]byte, 64)
			for !strings.Contains(seen, "stdout line") || !strings.Contains(seen, "stderr line") {
				n, err := reader.Read(buf)
				if err != nil {
					t.Fatalf("read before end: %v (seen %q)", err, seen)
				}
				seen += string(buf[:n])
			}
			end(reader, cancel)
			deadline := time.Now().Add(5 * time.Second)
			for fake.closed.Load() < 2 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if fake.open.Load() != 2 || fake.closed.Load() != 2 {
				t.Fatalf("upstream log requests open=%d closed=%d; following did not stop", fake.open.Load(), fake.closed.Load())
			}
			if name == "context canceled" {
				if _, err := io.ReadAll(reader); err == nil {
					t.Fatal("cancelled stream ended as a clean EOF")
				}
			}
		})
	}
}
