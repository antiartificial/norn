package capture

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestBufferKeepsHeadAndTailAndCountsDropped(t *testing.T) {
	b := New(4, 3)
	for _, chunk := range []string{"ab", "cdef", "ghij", "k"} {
		if n, err := b.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write %q = %d, %v", chunk, n, err)
		}
	}
	if b.Total() != 11 || b.Dropped() != 4 || !b.Truncated() {
		t.Fatalf("total=%d dropped=%d", b.Total(), b.Dropped())
	}
	if got := b.String(); got != "abcd\n[... 4 bytes of output truncated ...]\nijk" {
		t.Fatalf("string = %q", got)
	}
	small := New(10, 10)
	_, _ = small.Write([]byte("short"))
	if small.Truncated() || small.String() != "short" {
		t.Fatalf("untruncated = %q", small.String())
	}
}

// A process emitting far more output than the bound runs to completion with
// its exit status intact, while memory held stays bounded.
func TestBufferBoundsHighVolumeSubprocessOutput(t *testing.T) {
	const volume = 64 << 20
	b := New(16<<10, 16<<10)
	cmd := exec.Command("sh", "-c", "head -c 67108864 /dev/zero | tr '\\000' 'x'; echo END >&2; exit 3")
	cmd.Stdout, cmd.Stderr = b, b
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := cmd.Run()
	runtime.ReadMemStats(&after)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("exit = %v", err)
	}
	if b.Total() < volume || !b.Truncated() || len(b.String()) > 33<<10 || !strings.HasSuffix(b.String(), "END\n") {
		t.Fatalf("total=%d len=%d suffix=%q", b.Total(), len(b.String()), b.String()[len(b.String())-8:])
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 24<<20 {
		t.Fatalf("capturing %d bytes allocated %d bytes", volume, grew)
	}
}
