package log

import (
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	api "github.com/iamismile/proglog/api/v1"
)

type Log struct {
	// RWMutex allows multiple readers OR one writer at a time.
	// Reads can happen in parallel unless a write is in progress.
	mu sync.RWMutex

	Dir    string // Directory where segment files are stored
	Config Config // Configuration for segment size

	activeSegment *segment   // The segment currently being written to
	segments      []*segment // All segments (ordered by baseOffset)
}

// NewLog creates a log with defaults and loads existing segments from disk.
func NewLog(dir string, c Config) (*Log, error) {
	// Set default max sizes if not provided
	if c.Segment.MaxStoreBytes == 0 {
		c.Segment.MaxStoreBytes = 1024
	}
	if c.Segment.MaxIndexBytes == 0 {
		c.Segment.MaxIndexBytes = 1024
	}

	l := &Log{
		Dir:    dir,
		Config: c,
	}

	// Setup loads existing segments or creates a new one
	return l, l.setup()
}

func (l *Log) setup() error {
	// Ensure the directory exists
	// if exists do nothing
	if err := os.MkdirAll(l.Dir, 0755); err != nil {
		return err
	}

	// Read all files in the directory
	files, err := os.ReadDir(l.Dir)
	if err != nil {
		return err
	}

	// Extract base offsets from filenames
	var baseOffsets []uint64
	for _, file := range files {
		offStr := strings.TrimSuffix(file.Name(), path.Ext(file.Name()))
		off, err := strconv.ParseUint(offStr, 10, 0)
		if err != nil {
			continue // skip files that are not valid segment files
		}
		baseOffsets = append(baseOffsets, off)
	}

	// Sort offsets in ascending order
	slices.Sort(baseOffsets)

	// Create segments from offsets
	// Each segment has 2 files (.store and .index),
	// so offsets will appear twice → skip duplicates
	for i := 0; i < len(baseOffsets); i++ {
		if i > 0 && baseOffsets[i] == baseOffsets[i-1] {
			continue
		}

		if err := l.newSegment(baseOffsets[i]); err != nil {
			return err
		}
	}

	// If no segments exist, create the first one
	if len(l.segments) == 0 {
		baseOffset := l.Config.Segment.InitialOffset
		err = l.newSegment(baseOffset)
		if err != nil {
			return err
		}
	}

	// The last segment is always the active (write) segment
	l.activeSegment = l.segments[len(l.segments)-1]

	return nil
}

// newSegment creates a new segment starting at a given offset
// and appends it to the log's segment list.
func (l *Log) newSegment(off uint64) error {
	s, err := newSegment(l.Dir, off, l.Config)
	if err != nil {
		return err
	}
	l.segments = append(l.segments, s)
	return nil
}

// Append adds a new record to the log.
// It writes to the active segment and creates a new segment if needed.
func (l *Log) Append(record *api.Record) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Append record to active segment
	off, err := l.activeSegment.Append(record)
	if err != nil {
		return 0, err
	}

	// If segment reached its max size → rotate (create new segment)
	if l.activeSegment.IsMaxed() {
		err = l.newSegment(off + 1)
		if err != nil {
			return 0, err
		}

		// Update active segment to the newly created one
		l.activeSegment = l.segments[len(l.segments)-1]
	}

	return off, nil
}

func (l *Log) Read(off uint64) (*api.Record, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// We need to find which segment contains the given offset.
	// Each segment has a range:
	// [baseOffset, nextOffset]
	var s *segment
	for _, segment := range l.segments {
		// Check if the offset falls inside this segment's range
		if segment.baseOffset <= off && off < segment.nextOffset {
			s = segment
			break
		}
	}

	// If no segment found OR offset is beyond the segment's range,
	// it means the requested offset is invalid / out of bounds.
	if s == nil || s.nextOffset <= off {
		return nil, api.ErrOffsetOutOfRange{Offset: off}
	}

	return s.Read(off)
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, segment := range l.segments {
		if err := segment.Close(); err != nil {
			return err
		}
	}

	return nil
}

func (l *Log) Remove() error {
	if err := l.Close(); err != nil {
		return err
	}

	return os.RemoveAll(l.Dir)
}

func (l *Log) Reset() error {
	if err := l.Remove(); err != nil {
		return err
	}

	return l.setup()
}

func (l *Log) LowestOffset() (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.segments[0].baseOffset, nil
}

func (l *Log) HighestOffset() (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	off := l.segments[len(l.segments)-1].nextOffset

	// If no records have been written yet,
	// nextOffset could be 0 → meaning no valid offsets exist.
	if off == 0 {
		return 0, nil
	}

	return off - 1, nil
}

// Truncate removes old segments that are no longer needed.
//
// It deletes all segments where the highest offset in the segment
// is strictly less than the given `lowest` offset.
//
// In other words, after calling Truncate(lowest), the log will only
// contain records with offsets >= lowest.
//
// This helps free disk space by removing older data that has already
// been processed or is no longer required.
func (l *Log) Truncate(lowest uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var segments []*segment
	for _, s := range l.segments {
		// We remove the segment only if
		// that highest offset is equal or less than lowest
		if s.nextOffset <= lowest+1 {
			if err := s.Remove(); err != nil {
				return err
			}
			continue
		}

		segments = append(segments, s)
	}

	l.segments = segments
	return nil
}

// Reader returns a single io.Reader that can read ALL segments sequentially
// as if they were one continuous file.
//
// Problem:
// Our log is split into multiple segments (files).
// But sometimes (like replication, backup, streaming),
// we want to read everything as ONE continuous stream.
//
// Solution:
// 1. Create a reader for each segment
// 2. Combine them using io.MultiReader
//
// io.MultiReader works like this:
// It reads from the first reader until it's done,
// then moves to the second, then third... and so on.
//
// So the final result behaves like:
// [segment1][segment2][segment3] → one long stream
func (l *Log) Reader() io.Reader {
	// Lock for reading - allows multiple readers, but blocks writers
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Create a reader for each segment
	readers := make([]io.Reader, len(l.segments))

	for i, segment := range l.segments {
		// Wrap each segment's store (the raw data file) in an originReader.
		// off: 0 means "start reading from the very beginning of this file"
		readers[i] = &originReader{
			store: segment.store,
			off:   0,
		}
	}

	// io.MultiReader chains all the readers together.
	// When the first segment is fully read, it automatically moves to the next,
	// and so on — like joining multiple files end-to-end.
	return io.MultiReader(readers...)
}

// originReader is a custom reader that reads from a store
// starting from a specific offset and keeps track of progress.
//
// Think of it like:
// "Read from this file, and remember where you stopped"
type originReader struct {
	*store       // The actual data file (embedded so we inherit its methods)
	off    int64 // Our current reading position (like a cursor in the file)
}

// Read implements io.Reader interface.
//
// What happens here:
// 1. Read data from store starting at current offset (off)
// 2. Move offset forward by number of bytes read
// 3. Return data
//
// This allows sequential reading (like a normal file read)
func (o *originReader) Read(p []byte) (int, error) {
	// Read from the current offset
	n, err := o.ReadAt(p, o.off)

	// Move offset forward (like a cursor)
	o.off += int64(n)

	return n, err
}
