// Package logcollect collects application task output from Nomad into a
// bounded, labelled local spool, separately from evidence (ADR 0001):
// diagnostics may be dropped under pressure — visibly, with durable
// counters — while audited evidence may not. Every record keeps its source
// identity: app, job, node, allocation, task group, task and stream.
package logcollect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Labels identify one collected stream.
type Labels struct {
	App       string `json:"app"`
	JobID     string `json:"jobId"`
	NodeID    string `json:"nodeId"`
	NodeName  string `json:"nodeName"`
	AllocID   string `json:"allocId"`
	TaskGroup string `json:"taskGroup"`
	Task      string `json:"task"`
	Stream    string `json:"stream"`
}

// Limits bound the spool. A stream never exceeds StreamBytes plus one
// segment; the spool never exceeds TotalBytes plus the segment being
// written. Oldest segments are dropped first and counted.
type Limits struct {
	TotalBytes   int64
	StreamBytes  int64
	SegmentBytes int64
}

// Record is one spooled chunk: the Nomad log file index and offset it came
// from, or a gap marker describing output that could not be collected.
type Record struct {
	Time   time.Time `json:"t"`
	File   int       `json:"i"`
	Offset int64     `json:"o"`
	Data   []byte    `json:"d,omitempty"`
	Gap    string    `json:"gap,omitempty"`
}

// Position is how far a stream has been collected.
type Position struct {
	Known    bool  `json:"known"`
	File     int   `json:"file"`
	Offset   int64 `json:"offset"`
	Complete bool  `json:"complete"`
}

func (p Position) after(file int, offset int64) bool {
	return p.File > file || (p.File == file && p.Offset >= offset)
}

// Counters are the spool's durable diagnostic-loss counters.
type Counters struct {
	DroppedBytes    int64 `json:"droppedBytes"`
	DroppedSegments int64 `json:"droppedSegments"`
	Gaps            int64 `json:"gaps"`
}

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type segment struct {
	seq  int
	size int64
}

type spoolStream struct {
	dir      string
	labels   Labels
	segments []segment // ascending seq; the last is current
	position Position
}

func (s *spoolStream) bytes() int64 {
	var total int64
	for _, seg := range s.segments {
		total += seg.size
	}
	return total
}

// Spool is the bounded on-disk collection store, confined to its root.
type Spool struct {
	root     *os.Root
	limits   Limits
	lock     *os.File
	mu       sync.Mutex
	streams  map[string]*spoolStream
	total    int64
	counters Counters
}

// OpenSpool opens (creating) an owner-only spool root and recovers its
// streams, sizes and counters.
func OpenSpool(dir string, limits Limits) (*Spool, error) {
	if !filepath.IsAbs(dir) || limits.TotalBytes <= 0 || limits.StreamBytes <= 0 || limits.SegmentBytes <= 0 || limits.SegmentBytes > limits.StreamBytes || limits.StreamBytes > limits.TotalBytes {
		return nil, fmt.Errorf("log spool needs an absolute directory and segment <= stream <= total limits")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("log spool root must be an owner-only directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("log spool is in use by another collector: %w", err)
	}
	spool := &Spool{root: root, limits: limits, lock: lock, streams: map[string]*spoolStream{}}
	if data, err := readRootFile(root, "counters.json", 4096); err == nil {
		_ = json.Unmarshal(data, &spool.counters)
	}
	if err := spool.recover(); err != nil {
		spool.Close()
		return nil, err
	}
	return spool, nil
}

// Close releases the spool.
func (s *Spool) Close() error {
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	_ = s.lock.Close()
	return s.root.Close()
}

// Counters returns the durable loss counters.
func (s *Spool) Counters() Counters {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counters
}

// TotalBytes is the spooled size.
func (s *Spool) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func streamDir(labels Labels) (string, error) {
	for _, value := range []string{labels.App, labels.AllocID, labels.Task} {
		if !labelPattern.MatchString(value) {
			return "", fmt.Errorf("log label %q is not a safe identifier", value)
		}
	}
	if labels.Stream != "stdout" && labels.Stream != "stderr" {
		return "", fmt.Errorf("log stream must be stdout or stderr")
	}
	return path.Join("streams", labels.App, labels.AllocID, labels.Task+"."+labels.Stream), nil
}

func segmentName(dir string, seq int) string {
	return path.Join(dir, fmt.Sprintf("seg-%010d.jsonl", seq))
}

// recover walks the spool: labels, segment sizes, torn tails and positions.
func (s *Spool) recover() error {
	apps, err := fs.ReadDir(s.root.FS(), "streams")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, app := range apps {
		allocs, err := fs.ReadDir(s.root.FS(), path.Join("streams", app.Name()))
		if err != nil {
			return err
		}
		for _, alloc := range allocs {
			entries, err := fs.ReadDir(s.root.FS(), path.Join("streams", app.Name(), alloc.Name()))
			if err != nil {
				return err
			}
			for _, entry := range entries {
				dir := path.Join("streams", app.Name(), alloc.Name(), entry.Name())
				stream, err := s.loadStream(dir)
				if err != nil {
					return fmt.Errorf("recover log stream %s: %w", dir, err)
				}
				s.streams[dir] = stream
				s.total += stream.bytes()
			}
		}
	}
	return nil
}

func (s *Spool) loadStream(dir string) (*spoolStream, error) {
	stream := &spoolStream{dir: dir}
	data, err := readRootFile(s.root, path.Join(dir, "labels.json"), 4096)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &stream.labels); err != nil {
		return nil, err
	}
	if data, err := readRootFile(s.root, path.Join(dir, "position.json"), 4096); err == nil {
		_ = json.Unmarshal(data, &stream.position)
	}
	entries, err := fs.ReadDir(s.root.FS(), dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		seq, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".jsonl"))
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		stream.segments = append(stream.segments, segment{seq: seq, size: info.Size()})
	}
	sort.Slice(stream.segments, func(i, j int) bool { return stream.segments[i].seq < stream.segments[j].seq })
	if len(stream.segments) > 0 {
		last := &stream.segments[len(stream.segments)-1]
		position, size, err := s.recoverTail(segmentName(dir, last.seq))
		if err != nil {
			return nil, err
		}
		last.size = size
		if position.Known && (!stream.position.Known || !stream.position.after(position.File, position.Offset)) {
			complete := stream.position.Complete
			stream.position = position
			stream.position.Complete = complete
		}
	}
	return stream, nil
}

// recoverTail truncates a torn final line (crash mid-append) and returns
// the position after the last intact record.
func (s *Spool) recoverTail(name string) (Position, int64, error) {
	data, err := readRootFile(s.root, name, s.limits.SegmentBytes*2+1<<20)
	if err != nil {
		return Position{}, 0, err
	}
	good := int64(0)
	var position Position
	for offset := 0; offset < len(data); {
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			break
		}
		var record Record
		if json.Unmarshal(data[offset:offset+end], &record) != nil {
			break
		}
		position = Position{Known: true, File: record.File, Offset: record.Offset + int64(len(record.Data))}
		offset += end + 1
		good = int64(offset)
	}
	if good < int64(len(data)) {
		file, err := s.root.OpenFile(name, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return Position{}, 0, err
		}
		err = file.Truncate(good)
		if err == nil {
			err = file.Sync()
		}
		file.Close()
		if err != nil {
			return Position{}, 0, err
		}
	}
	return position, good, nil
}

// Stream opens (or resumes) a labelled stream and returns its position.
func (s *Spool) Stream(labels Labels) (Position, error) {
	dir, err := streamDir(labels)
	if err != nil {
		return Position{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stream, ok := s.streams[dir]; ok {
		if stream.labels != labels {
			return Position{}, fmt.Errorf("log stream %s exists with other labels", dir)
		}
		return stream.position, nil
	}
	if err := s.root.MkdirAll(dir, 0o700); err != nil {
		return Position{}, err
	}
	encoded, _ := json.Marshal(labels)
	if err := s.writeAtomic(path.Join(dir, "labels.json"), encoded); err != nil {
		return Position{}, err
	}
	s.streams[dir] = &spoolStream{dir: dir, labels: labels}
	return Position{}, nil
}

// Append adds records to a stream, rolling segments and enforcing the
// stream and total bounds (dropping and counting the oldest segments). The
// segment is synced before the call returns; the recorded position is the
// end of the last record.
func (s *Spool) Append(labels Labels, records ...Record) error {
	dir, err := streamDir(labels)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stream, ok := s.streams[dir]
	if !ok {
		return fmt.Errorf("log stream %s is not open", dir)
	}
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		if len(stream.segments) == 0 || (stream.segments[len(stream.segments)-1].size > 0 && stream.segments[len(stream.segments)-1].size+int64(len(line)) > s.limits.SegmentBytes) {
			if err := s.roll(stream); err != nil {
				return err
			}
		}
		current := &stream.segments[len(stream.segments)-1]
		file, err := s.root.OpenFile(segmentName(dir, current.seq), os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return err
		}
		_, err = file.Write(line)
		if err == nil {
			err = file.Sync()
		}
		file.Close()
		if err != nil {
			return err
		}
		current.size += int64(len(line))
		s.total += int64(len(line))
		stream.position.Known, stream.position.File, stream.position.Offset = true, record.File, record.Offset+int64(len(record.Data))
		if record.Gap != "" {
			s.counters.Gaps++
			if err := s.saveCounters(); err != nil {
				return err
			}
		}
		if err := s.enforce(stream); err != nil {
			return err
		}
	}
	return nil
}

// roll starts a new segment, persisting the position first so it survives
// the stream's segments being dropped.
func (s *Spool) roll(stream *spoolStream) error {
	if len(stream.segments) > 0 {
		if err := s.savePosition(stream); err != nil {
			return err
		}
	}
	next := 1
	if len(stream.segments) > 0 {
		next = stream.segments[len(stream.segments)-1].seq + 1
	}
	stream.segments = append(stream.segments, segment{seq: next})
	return nil
}

func (s *Spool) savePosition(stream *spoolStream) error {
	encoded, _ := json.Marshal(stream.position)
	return s.writeAtomic(path.Join(stream.dir, "position.json"), encoded)
}

// MarkComplete records that a stream has ended (terminal allocation).
func (s *Spool) MarkComplete(labels Labels) error {
	dir, err := streamDir(labels)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stream, ok := s.streams[dir]
	if !ok {
		return fmt.Errorf("log stream %s is not open", dir)
	}
	stream.position.Complete = true
	return s.savePosition(stream)
}

// enforce drops oldest segments: first beyond the stream bound (never the
// segment being written), then beyond the total bound across all streams
// (oldest closed segment first; a current segment of another stream only
// when no closed segment remains).
func (s *Spool) enforce(writing *spoolStream) error {
	for writing.bytes() > s.limits.StreamBytes && len(writing.segments) > 1 {
		if err := s.drop(writing); err != nil {
			return err
		}
	}
	for s.total > s.limits.TotalBytes {
		victim := s.oldestSegment(writing, false)
		if victim == nil {
			victim = s.oldestSegment(writing, true)
		}
		if victim == nil {
			return nil
		}
		if err := s.drop(victim); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) oldestSegment(writing *spoolStream, allowCurrent bool) *spoolStream {
	var victim *spoolStream
	var oldest time.Time
	for _, stream := range s.streams {
		closed := len(stream.segments) > 1 || (allowCurrent && stream != writing && len(stream.segments) == 1)
		if !closed {
			continue
		}
		info, err := s.root.Stat(segmentName(stream.dir, stream.segments[0].seq))
		if err != nil {
			continue
		}
		if victim == nil || info.ModTime().Before(oldest) {
			victim, oldest = stream, info.ModTime()
		}
	}
	return victim
}

func (s *Spool) drop(stream *spoolStream) error {
	oldest := stream.segments[0]
	if len(stream.segments) == 1 {
		// Persist the position before the only segment disappears.
		if err := s.savePosition(stream); err != nil {
			return err
		}
	}
	if err := s.root.Remove(segmentName(stream.dir, oldest.seq)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	stream.segments = stream.segments[1:]
	s.total -= oldest.size
	s.counters.DroppedBytes += oldest.size
	s.counters.DroppedSegments++
	return s.saveCounters()
}

func (s *Spool) saveCounters() error {
	encoded, _ := json.Marshal(s.counters)
	return s.writeAtomic("counters.json", encoded)
}

func (s *Spool) writeAtomic(name string, data []byte) error {
	temporary := name + ".tmp"
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		return err
	}
	if err := s.root.Rename(temporary, name); err != nil {
		return err
	}
	directory, err := s.root.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readRootFile(root *os.Root, name string, limit int64) ([]byte, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("spool file %s is not a bounded regular file", name)
	}
	data := make([]byte, info.Size())
	_, err = io.ReadFull(file, data)
	return data, err
}

// Query selects collected records of one app.
type Query struct {
	App    string
	Alloc  string
	Task   string
	Stream string
	Since  time.Time
	// Limit bounds returned records; ScanBytes bounds bytes read.
	Limit     int
	ScanBytes int64
}

// Entry is a returned record with its labels.
type Entry struct {
	Labels
	Record
}

// Result is a bounded query answer.
type Result struct {
	Entries   []Entry  `json:"entries"`
	Truncated bool     `json:"truncated"`
	Counters  Counters `json:"counters"`
}

// Read returns an app's records oldest first, merged across its streams,
// bounded by Limit records and ScanBytes read. Dropped diagnostics are
// reported through the counters and gap records, never hidden.
func (s *Spool) Read(query Query) (Result, error) {
	if !labelPattern.MatchString(query.App) {
		return Result{}, fmt.Errorf("invalid app")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Result{Counters: s.counters}
	var scanned int64
	for _, stream := range s.streams {
		labels := stream.labels
		if labels.App != query.App || (query.Alloc != "" && labels.AllocID != query.Alloc) || (query.Task != "" && labels.Task != query.Task) || (query.Stream != "" && labels.Stream != query.Stream) {
			continue
		}
		for _, seg := range stream.segments {
			if scanned+seg.size > query.ScanBytes {
				result.Truncated = true
				break
			}
			data, err := readRootFile(s.root, segmentName(stream.dir, seg.seq), s.limits.SegmentBytes*2+1<<20)
			if err != nil {
				return Result{}, err
			}
			scanned += int64(len(data))
			scanner := bufio.NewScanner(bytes.NewReader(data))
			scanner.Buffer(make([]byte, 64<<10), len(data)+1)
			for scanner.Scan() {
				var record Record
				if json.Unmarshal(scanner.Bytes(), &record) != nil {
					continue
				}
				if !query.Since.IsZero() && record.Time.Before(query.Since) {
					continue
				}
				result.Entries = append(result.Entries, Entry{Labels: labels, Record: record})
			}
		}
	}
	sort.SliceStable(result.Entries, func(i, j int) bool { return result.Entries[i].Time.Before(result.Entries[j].Time) })
	if query.Limit > 0 && len(result.Entries) > query.Limit {
		result.Entries = result.Entries[:query.Limit]
		result.Truncated = true
	}
	return result, nil
}
