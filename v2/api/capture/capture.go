// Package capture bounds subprocess output in memory while the process runs.
// CombinedOutput buffers everything a process writes and only allows
// truncation after exit; a Buffer keeps the first Head and last Tail bytes,
// counts what it drops, and never grows beyond Head+Tail.
package capture

import (
	"fmt"
	"strings"
	"sync"
)

// Buffer is an io.Writer retaining a bounded head and tail of its input.
// It is safe for concurrent writers (exec may write stdout and stderr from
// separate goroutines when they are different writers).
type Buffer struct {
	mu        sync.Mutex
	headLimit int
	tailLimit int
	head      []byte
	tail      []byte // ring of tailLimit bytes
	tailStart int
	tailLen   int
	total     int64
}

// New returns a Buffer keeping at most head leading and tail trailing bytes.
func New(head, tail int) *Buffer {
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	return &Buffer{headLimit: head, tailLimit: tail, head: make([]byte, 0, head), tail: make([]byte, tail)}
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	b.total += int64(written)
	if room := b.headLimit - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if b.tailLimit == 0 || len(p) == 0 {
		return written, nil
	}
	if len(p) >= b.tailLimit {
		copy(b.tail, p[len(p)-b.tailLimit:])
		b.tailStart, b.tailLen = 0, b.tailLimit
		return written, nil
	}
	for _, c := range p {
		index := (b.tailStart + b.tailLen) % b.tailLimit
		b.tail[index] = c
		if b.tailLen < b.tailLimit {
			b.tailLen++
		} else {
			b.tailStart = (b.tailStart + 1) % b.tailLimit
		}
	}
	return written, nil
}

// Total is the number of bytes written.
func (b *Buffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Dropped is the number of bytes neither in the head nor the tail.
func (b *Buffer) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total - int64(len(b.head)) - int64(b.tailLen)
}

// Truncated reports whether any output was dropped.
func (b *Buffer) Truncated() bool { return b.Dropped() > 0 }

// Parts returns copies of the retained head and tail and the dropped count.
func (b *Buffer) Parts() (head, tail []byte, dropped int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	head = append([]byte(nil), b.head...)
	tail = make([]byte, b.tailLen)
	for i := range tail {
		tail[i] = b.tail[(b.tailStart+i)%b.tailLimit]
	}
	return head, tail, b.total - int64(len(b.head)) - int64(b.tailLen)
}

// String renders head, an explicit truncation marker and tail.
func (b *Buffer) String() string {
	head, tail, dropped := b.Parts()
	return Join(head, tail, dropped)
}

// Join renders head and tail around an explicit truncation marker.
func Join(head, tail []byte, dropped int64) string {
	var out strings.Builder
	out.Write(head)
	if dropped > 0 {
		fmt.Fprintf(&out, "\n[... %d bytes of output truncated ...]\n", dropped)
	}
	out.Write(tail)
	return out.String()
}
