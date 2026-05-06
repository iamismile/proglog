package log

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	write = []byte("hello world")         // 11 bytes
	width = uint64(len(write)) + lenWidth // 19 bytes per record on disk
)

func TestStoreAppendRead(t *testing.T) {
	f, err := os.CreateTemp("", "store_append_read_test")
	require.NoError(t, err)
	defer os.Remove(f.Name()) // clean up temp file after test

	s, err := newStore(f)
	require.NoError(t, err)

	testAppend(t, s)
	testRead(t, s)
	testReadAt(t, s)

	// Re-open — proves previously written data survives a Store restart.
	s, err = newStore(f)
	require.NoError(t, err)
	testRead(t, s)
}

// Writes 3 copies of `write` and checks that each append lands at
// the expected cumulative byte position.
//
//	Record 1: pos=0,  n=19 → 0+19  = 19  = width*1
//	Record 2: pos=19, n=19 → 19+19 = 38  = width*2
//	Record 3: pos=38, n=19 → 38+19 = 57  = width*3
func testAppend(t *testing.T, s *store) {
	t.Helper()

	for i := uint64(1); i < 4; i++ {
		n, pos, err := s.Append(write)
		require.NoError(t, err)
		require.Equal(t, pos+n, width*i)
	}
}

// Reads all 3 records sequentially using Read(pos).
// pos advances by `width` (19 bytes) after each record — header + payload.
func testRead(t *testing.T, s *store) {
	t.Helper()

	var pos uint64
	for i := uint64(1); i < 4; i++ {
		read, err := s.Read(pos)
		require.NoError(t, err)
		require.Equal(t, write, read)
		pos += width // jump to the next record's start
	}
}

// testReadAt exercises the low-level ReadAt(buf, offset) interface.
// Each record on disk is stored as:
//
//	[ 8-byte big-endian length header ][ N-byte payload ]
//
// So we read in two passes per record:
//  1. Read 8 bytes → decode the payload length.
//  2. Read that many bytes → confirm it equals `write`.
func testReadAt(t *testing.T, s *store) {
	t.Helper()

	var off = int64(0)
	for i := uint64(1); i < 4; i++ {
		// Pass 1: read the 8-byte length header.
		b := make([]byte, lenWidth)
		n, err := s.ReadAt(b, off)
		require.NoError(t, err)
		require.Equal(t, lenWidth, n)
		off += int64(n)

		// Pass 2: decode length, then read the payload.
		size := enc.Uint64(b)
		b = make([]byte, size)
		n, err = s.ReadAt(b, off)
		require.NoError(t, err)
		require.Equal(t, write, b)
		require.Equal(t, int(size), n)
		off += int64(n)
	}
}

// TestStoreClose confirms that closing a Store flushes its internal bufio.Writer
// to disk, making the file larger than it was before Close was called.
//
// bufio buffers writes in memory; without Close (which calls Flush internally),
// data could be lost. This test catches that regression.
func TestStoreClose(t *testing.T) {
	f, err := os.CreateTemp("", "store_close_test")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	s, err := newStore(f)
	require.NoError(t, err)

	_, _, err = s.Append(write) // data sits in the buffer, not yet on disk
	require.NoError(t, err)

	// Snapshot file size BEFORE Close — buffer hasn't flushed yet.
	f, beforeSize, err := openFile(f.Name())
	require.NoError(t, err)

	err = s.Close() // triggers bufio.Flush + file.Close
	require.NoError(t, err)

	// File must be larger now — proves the buffer was flushed to disk.
	_, afterSize, err := openFile(f.Name())
	require.NoError(t, err)
	require.True(t, afterSize > beforeSize)
}

// openFile is a test helper that opens a file in read-write-append mode
// and returns the file handle along with its current size in bytes.
func openFile(name string) (file *os.File, size int64, err error) {
	f, err := os.OpenFile(
		name,
		os.O_RDWR|os.O_CREATE|os.O_APPEND,
		0644,
	)
	if err != nil {
		return nil, 0, err
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}

	return f, fi.Size(), nil
}
