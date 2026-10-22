package logcollect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path"
)

// Cursor is how far a stream has been shipped: everything before (File,
// Offset) is done, and when Gap is set the gap record at exactly that
// position is done too (it precedes the data record that starts there).
type Cursor struct {
	Known  bool  `json:"known"`
	File   int   `json:"file"`
	Offset int64 `json:"offset"`
	Gap    bool  `json:"gap,omitempty"`
}

// Past is the cursor after record: the end of its data, or its own position
// for a gap record.
func (r Record) Past() Cursor {
	if r.Gap != "" {
		return Cursor{Known: true, File: r.File, Offset: r.Offset, Gap: true}
	}
	return Cursor{Known: true, File: r.File, Offset: r.Offset + int64(len(r.Data))}
}

func (c Cursor) before(r Record) bool {
	if !c.Known || r.File != c.File {
		return !c.Known || r.File > c.File
	}
	if r.Offset != c.Offset {
		return r.Offset > c.Offset
	}
	return r.Gap == "" || !c.Gap
}

// StreamState is one stream's identity, collected position and shipped cursor.
type StreamState struct {
	Labels   Labels
	Position Position
	Shipped  Cursor
}

// Idle reports that everything collected has been shipped.
func (s StreamState) Idle() bool {
	return !s.Position.Known || (s.Shipped.Known && s.Shipped.File == s.Position.File && s.Shipped.Offset == s.Position.Offset)
}

// StreamStates lists every stream with its shipped cursor.
func (s *Spool) StreamStates() []StreamState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]StreamState, 0, len(s.streams))
	for _, stream := range s.streams {
		states = append(states, StreamState{Labels: stream.labels, Position: stream.position, Shipped: s.shippedCursor(stream)})
	}
	return states
}

func (s *Spool) shippedCursor(stream *spoolStream) Cursor {
	if !stream.shippedLoaded {
		stream.shippedLoaded = true
		if data, err := readRootFile(s.root, path.Join(stream.dir, "shipped.json"), 4096); err == nil {
			_ = json.Unmarshal(data, &stream.shipped)
		}
	}
	return stream.shipped
}

// SetShipped durably records a stream's shipped cursor (owner-only, atomic).
func (s *Spool) SetShipped(labels Labels, cursor Cursor) error {
	dir, err := streamDir(labels)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stream, ok := s.streams[dir]
	if !ok {
		return errStreamNotOpen(dir)
	}
	encoded, _ := json.Marshal(cursor)
	if err := s.writeAtomic(path.Join(dir, "shipped.json"), encoded); err != nil {
		return err
	}
	stream.shipped, stream.shippedLoaded = cursor, true
	return nil
}

// ReadAfter returns a stream's records after cursor in order, stopping once
// the data read reaches maxBytes (at least one record is returned when any
// is pending). Segments dropped by the spool are simply absent.
func (s *Spool) ReadAfter(labels Labels, cursor Cursor, maxBytes int) ([]Record, error) {
	dir, err := streamDir(labels)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stream, ok := s.streams[dir]
	if !ok {
		return nil, errStreamNotOpen(dir)
	}
	var records []Record
	var size int
	for _, seg := range stream.segments {
		data, err := readRootFile(s.root, segmentName(dir, seg.seq), s.limits.SegmentBytes*2+1<<20)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64<<10), len(data)+1)
		for scanner.Scan() {
			var record Record
			if json.Unmarshal(scanner.Bytes(), &record) != nil || !cursor.before(record) {
				continue
			}
			if len(records) > 0 && size+len(record.Data)+len(record.Gap) > maxBytes {
				return records, nil
			}
			records = append(records, record)
			size += len(record.Data) + len(record.Gap)
		}
	}
	return records, nil
}
