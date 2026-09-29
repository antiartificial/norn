package logcollect

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

// fakeLogs models Nomad task logs: numbered rotating files per task stream,
// of which only the newest are retained, streamed from the oldest retained
// file with file names and offsets, followed until the allocation ends.
type fakeLogs struct {
	mu      sync.Mutex
	allocs  map[string][]nomad.LogAllocation
	files   map[string]map[int][]byte
	changed chan struct{}
	follows map[string]int
}

func newFakeLogs() *fakeLogs {
	return &fakeLogs{allocs: map[string][]nomad.LogAllocation{}, files: map[string]map[int][]byte{}, changed: make(chan struct{}), follows: map[string]int{}}
}

func streamKey(alloc, task, stream string) string { return alloc + "/" + task + "/" + stream }

func (f *fakeLogs) notify() {
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeLogs) write(alloc, task, stream, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := streamKey(alloc, task, stream)
	if f.files[key] == nil {
		f.files[key] = map[int][]byte{0: nil}
	}
	newest := newestIndex(f.files[key])
	f.files[key][newest] = append(f.files[key][newest], text...)
	f.notify()
}

// rotate starts a new file; keep bounds retained files (older ones vanish).
func (f *fakeLogs) rotate(alloc, task, stream string, keep int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := streamKey(alloc, task, stream)
	newest := newestIndex(f.files[key]) + 1
	f.files[key][newest] = nil
	for index := range f.files[key] {
		if index <= newest-keep {
			delete(f.files[key], index)
		}
	}
	f.notify()
}

func newestIndex(files map[int][]byte) int {
	newest := 0
	for index := range files {
		newest = max(newest, index)
	}
	return newest
}

func (f *fakeLogs) Allocations(_ context.Context, jobID string) ([]nomad.LogAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nomad.LogAllocation(nil), f.allocs[jobID]...), nil
}

func (f *fakeLogs) Follow(ctx context.Context, allocation nomad.LogAllocation, task, stream string) (<-chan *nomadapi.StreamFrame, <-chan error) {
	frames, errs := make(chan *nomadapi.StreamFrame), make(chan error, 1)
	key := streamKey(allocation.ID, task, stream)
	f.mu.Lock()
	f.follows[key]++
	f.mu.Unlock()
	go func() {
		sent := map[int]int64{}
		for {
			f.mu.Lock()
			files := f.files[key]
			indexes := []int{}
			for index := range files {
				indexes = append(indexes, index)
			}
			var pending []*nomadapi.StreamFrame
			sortInts(indexes)
			for _, index := range indexes {
				data := files[index]
				for offset := sent[index]; offset < int64(len(data)); offset += 4 {
					end := min(offset+4, int64(len(data)))
					pending = append(pending, &nomadapi.StreamFrame{File: fmt.Sprintf("alloc/logs/%s.%s.%d", task, stream, index), Offset: offset, Data: bytes.Clone(data[offset:end])})
				}
				sent[index] = int64(len(data))
			}
			changed := f.changed
			terminal := allocation.Terminal()
			f.mu.Unlock()
			for _, frame := range pending {
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
			if terminal {
				close(frames)
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frames, errs
}

func sortInts(values []int) {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func testLimits() Limits {
	return Limits{TotalBytes: 1 << 20, StreamBytes: 256 << 10, SegmentBytes: 16 << 10}
}

func openTestSpool(t *testing.T, dir string, limits Limits) *Spool {
	t.Helper()
	spool, err := OpenSpool(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	return spool
}

func streamText(t *testing.T, spool *Spool, alloc, task, stream string) (string, []string) {
	t.Helper()
	result, err := spool.Read(Query{App: "shop", Alloc: alloc, Task: task, Stream: stream, ScanBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var gaps []string
	for _, entry := range result.Entries {
		text.Write(entry.Data)
		if entry.Gap != "" {
			gaps = append(gaps, entry.Gap)
		}
	}
	return text.String(), gaps
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollectorLabelsResumesWithoutDuplicatesAndRecordsGaps(t *testing.T) {
	source := newFakeLogs()
	source.allocs["shop-web"] = []nomad.LogAllocation{
		{ID: "alloc-1", JobID: "shop-web", NodeID: "node-1", NodeName: "mini-a", TaskGroup: "web", ClientStatus: "running", Tasks: []string{"web"}},
		{ID: "alloc-2", JobID: "shop-web", NodeID: "node-2", NodeName: "mini-b", TaskGroup: "worker", ClientStatus: "complete", Tasks: []string{"sidecar", "worker"}},
	}
	source.write("alloc-1", "web", "stdout", "line1\nline2\n")
	source.write("alloc-1", "web", "stderr", "err1\n")
	source.write("alloc-2", "worker", "stdout", "w0\n")
	source.rotate("alloc-2", "worker", "stdout", 5)
	source.write("alloc-2", "worker", "stdout", "w1\n")
	source.write("alloc-2", "sidecar", "stdout", "s\n")
	dir := filepath.Join(t.TempDir(), "spool")
	spool := openTestSpool(t, dir, testLimits())
	jobs := func() []Job { return []Job{{App: "shop", JobID: "shop-web"}} }
	ctx, cancel := context.WithCancel(context.Background())
	collector := &Collector{Source: source, Spool: spool, Jobs: jobs}
	if err := collector.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first collection", func() bool {
		stdout, _ := streamText(t, spool, "alloc-1", "web", "stdout")
		worker, _ := streamText(t, spool, "alloc-2", "worker", "stdout")
		stderr, _ := streamText(t, spool, "alloc-1", "web", "stderr")
		return stdout == "line1\nline2\n" && worker == "w0\nw1\n" && stderr == "err1\n"
	})
	// Every record keeps its node, allocation, group, task and stream.
	result, err := spool.Read(Query{App: "shop", ScanBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range result.Entries {
		want := map[string]string{"alloc-1": "mini-a/web", "alloc-2": "mini-b/worker"}[entry.AllocID]
		if entry.NodeName+"/"+entry.TaskGroup != want || entry.JobID != "shop-web" || (entry.Stream != "stdout" && entry.Stream != "stderr") {
			t.Fatalf("mislabelled entry %+v", entry.Labels)
		}
	}
	// The terminal allocation's streams complete and are never re-followed.
	waitFor(t, "terminal streams to complete", func() bool {
		position, _ := spool.Stream(Labels{App: "shop", JobID: "shop-web", NodeID: "node-2", NodeName: "mini-b", AllocID: "alloc-2", TaskGroup: "worker", Task: "worker", Stream: "stdout"})
		return position.Complete
	})

	// Collector restart: output written while it was down is collected once;
	// output rotated away while it was down is recorded as a gap.
	cancel()
	collector.Wait()
	spool.Close()
	source.write("alloc-1", "web", "stdout", "line3\n")
	source.write("alloc-1", "web", "stderr", "err2-lost\n")
	source.rotate("alloc-1", "web", "stderr", 1)
	source.write("alloc-1", "web", "stderr", "err3\n")
	spool = openTestSpool(t, dir, testLimits())
	defer spool.Close()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	restarted := &Collector{Source: source, Spool: spool, Jobs: jobs}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed collection", func() bool {
		stdout, _ := streamText(t, spool, "alloc-1", "web", "stdout")
		stderr, _ := streamText(t, spool, "alloc-1", "web", "stderr")
		return strings.HasSuffix(stdout, "line3\n") && strings.HasSuffix(stderr, "err3\n")
	})
	if stdout, gaps := streamText(t, spool, "alloc-1", "web", "stdout"); stdout != "line1\nline2\nline3\n" || len(gaps) != 0 {
		t.Fatalf("resumed stdout = %q gaps %v (duplicated or skipped)", stdout, gaps)
	}
	stderr, gaps := streamText(t, spool, "alloc-1", "web", "stderr")
	if stderr != "err1\nerr3\n" || len(gaps) != 1 || !strings.Contains(gaps[0], "rotated away") || spool.Counters().Gaps != 1 {
		t.Fatalf("stderr across rotation = %q gaps %v counters %+v", stderr, gaps, spool.Counters())
	}
	source.mu.Lock()
	refollowed := source.follows[streamKey("alloc-2", "worker", "stdout")]
	source.mu.Unlock()
	if refollowed != 1 {
		t.Fatalf("completed stream followed %d times", refollowed)
	}

	// Allocation turnover: a replaced allocation's follower stops, the new
	// one is collected under its own labels, and the old output remains.
	source.mu.Lock()
	source.allocs["shop-web"] = []nomad.LogAllocation{{ID: "alloc-3", JobID: "shop-web", NodeID: "node-2", NodeName: "mini-b", TaskGroup: "web", ClientStatus: "running", Tasks: []string{"web"}}}
	source.mu.Unlock()
	source.write("alloc-3", "web", "stdout", "new\n")
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replacement allocation", func() bool {
		text, _ := streamText(t, spool, "alloc-3", "web", "stdout")
		return text == "new\n"
	})
	restarted.mu.Lock()
	_, stillFollowed := restarted.followers["alloc-1/web/stdout"]
	restarted.mu.Unlock()
	if stillFollowed {
		t.Fatal("follower of a replaced allocation kept running")
	}
	if text, _ := streamText(t, spool, "alloc-1", "web", "stdout"); text != "line1\nline2\nline3\n" {
		t.Fatalf("replaced allocation's output = %q", text)
	}
}

// The spool stays within its bounds by dropping the oldest segments, counts
// every dropped byte durably, and recovers a torn tail after a crash.
func TestSpoolIsBoundedCountsLossAndRecoversTornTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	limits := Limits{TotalBytes: 8 << 10, StreamBytes: 4 << 10, SegmentBytes: 1 << 10}
	spool := openTestSpool(t, dir, limits)
	streams := []Labels{}
	for _, alloc := range []string{"a1", "a2", "a3"} {
		labels := Labels{App: "shop", JobID: "shop", NodeID: "n", NodeName: "n", AllocID: alloc, TaskGroup: "web", Task: "web", Stream: "stdout"}
		if _, err := spool.Stream(labels); err != nil {
			t.Fatal(err)
		}
		streams = append(streams, labels)
	}
	offset := map[string]int64{}
	for round := range 200 {
		labels := streams[round%3]
		data := []byte(strings.Repeat(string(rune('a'+round%26)), 100))
		if err := spool.Append(labels, Record{Time: time.Now().UTC(), File: 0, Offset: offset[labels.AllocID], Data: data}); err != nil {
			t.Fatal(err)
		}
		offset[labels.AllocID] += int64(len(data))
		if total := spool.TotalBytes(); total > limits.TotalBytes+limits.SegmentBytes {
			t.Fatalf("spool holds %d bytes, bound is %d", total, limits.TotalBytes+limits.SegmentBytes)
		}
	}
	counters := spool.Counters()
	if counters.DroppedBytes == 0 || counters.DroppedSegments == 0 {
		t.Fatalf("no loss counted: %+v", counters)
	}
	// The newest output of every stream survives.
	for _, labels := range streams {
		result, err := spool.Read(Query{App: "shop", Alloc: labels.AllocID, ScanBytes: 1 << 20})
		if err != nil || len(result.Entries) == 0 {
			t.Fatalf("stream %s = %+v, %v", labels.AllocID, result, err)
		}
		last := result.Entries[len(result.Entries)-1]
		if last.Offset+int64(len(last.Data)) != offset[labels.AllocID] {
			t.Fatalf("stream %s lost its newest output", labels.AllocID)
		}
	}
	// A torn final line (crash mid-append) is truncated on recovery, and the
	// counters survive the restart.
	spool.Close()
	segments, _ := filepath.Glob(filepath.Join(dir, "streams", "shop", "a1", "web.stdout", "seg-*.jsonl"))
	newest := segments[len(segments)-1]
	file, err := os.OpenFile(newest, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"t":"2026-09-22T00:00:00Z","i":0,"o":999999,"d":"dG9ybg`)
	file.Close()
	spool = openTestSpool(t, dir, limits)
	defer spool.Close()
	if got := spool.Counters(); got != counters {
		t.Fatalf("counters after restart = %+v, want %+v", got, counters)
	}
	position, err := spool.Stream(streams[0])
	if err != nil || !position.Known || position.Offset != offset["a1"] {
		t.Fatalf("recovered position = %+v, %v (want offset %d)", position, err, offset["a1"])
	}
	// A second collector cannot open the same spool.
	if _, err := OpenSpool(dir, limits); err == nil {
		t.Fatal("two collectors opened one spool")
	}
}
