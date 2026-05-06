package log

import (
	"bufio"
	"encoding/binary"
	"os"
	"sync"
)

// "When we write numbers, write them in Big Endian format" (most significant byte first).
// Think of it as agreeing on a standard way to write numbers
// so any computer can read them back the same way.
var enc = binary.BigEndian

// Every record starts with an 8-byte header that stores the length of the actual data.
const lenWidth = 8

type store struct {
	*os.File               // The actual file on disk
	mu       sync.Mutex    // A lock to prevent simultaneous writes
	buf      *bufio.Writer // A buffer for efficient writing
	size     uint64        // Current total size of the file
}

func newStore(f *os.File) (*store, error) {
	fi, err := os.Stat(f.Name())
	if err != nil {
		return nil, err
	}

	// When opening an existing store, check how full it already is (fi.Size())
	// so we know where to start writing new entries.
	size := uint64(fi.Size())

	return &store{
		File: f,
		size: size,
		buf:  bufio.NewWriter(f),
	}, nil
}

func (s *store) Append(p []byte) (n uint64, pos uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remember WHERE this entry starts
	pos = s.size

	// Step 1: Write the header (8 bytes saying how long the data is)
	if err := binary.Write(s.buf, enc, uint64(len(p))); err != nil {
		return 0, 0, err
	}

	// Step 2: Write the actual data
	w, err := s.buf.Write(p)
	if err != nil {
		return 0, 0, err
	}

	// Step 3: Update our "pages used" counter
	w += lenWidth
	s.size += uint64(w)

	return uint64(w), pos, nil
}

func (s *store) Read(pos uint64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// First, make sure everything in the buffer is copied to the store
	if err := s.buf.Flush(); err != nil {
		return nil, err
	}

	// Read the 8-byte header to know how long the data is
	size := make([]byte, lenWidth)
	if _, err := s.File.ReadAt(size, int64(pos)); err != nil {
		return nil, err
	}

	// Read that many bytes of actual data
	b := make([]byte, enc.Uint64(size))
	if _, err := s.File.ReadAt(b, int64(pos+lenWidth)); err != nil {
		return nil, err
	}

	return b, nil
}

func (s *store) ReadAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Make sure everything is on disk
	if err := s.buf.Flush(); err != nil {
		return 0, err
	}

	// Read raw bytes at any offset
	return s.File.ReadAt(p, off)
}

func (s *store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Make sure everything is on disk
	err := s.buf.Flush()
	if err != nil {
		return err
	}

	return s.File.Close()
}
