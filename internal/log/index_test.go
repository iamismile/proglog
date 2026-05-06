package log

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIndex validates the full lifecycle of an index:
//
//  1. Empty index rejects reads.
//  2. Written entries can be read back by offset.
//  3. Out-of-range read returns io.EOF.
//  4. After Close + re-open, the index rebuilds state from disk
//     and the last entry is still readable via Read(-1).
func TestIndex(t *testing.T) {
	f, err := os.CreateTemp(os.TempDir(), "index_test")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	// Allow up to 1024 bytes — enough for many entries (each is 12 bytes).
	c := Config{}
	c.Segment.MaxIndexBytes = 1024 // 1MB

	idx, err := newIndex(f, c)
	require.NoError(t, err)

	// Empty index must reject any read
	// Read(-1) means "give me the last entry". With nothing written, it must
	// return an error (io.EOF) instead of garbage data.
	_, _, err = idx.Read(-1)
	require.Error(t, err)
	require.Equal(t, f.Name(), idx.Name())

	// Entry layout on disk (each 12 bytes):
	//   {Off:0, Pos:0}  → bytes  0–11
	//   {Off:1, Pos:10} → bytes 12–23
	entries := []struct {
		Off uint32
		Pos uint64
	}{
		{Off: 0, Pos: 0},
		{Off: 1, Pos: 10},
	}

	for _, want := range entries {
		err = idx.Write(want.Off, want.Pos)
		require.NoError(t, err)

		// Read back by the same offset and confirm the stored position matches.
		_, pos, err := idx.Read(int64(want.Off))
		require.NoError(t, err)
		require.Equal(t, want.Pos, pos)
	}

	// Out-of-range read must return io.EOF
	// len(entries) == 2, so offset 2 does not exist yet.
	// The segment uses this signal to know the index is full.
	_, _, err = idx.Read(int64(len(entries)))
	require.Equal(t, io.EOF, err)

	// Close flushes the mmap, fsyncs, and trims the file to the written size.
	_ = idx.Close()

	// Re-open and rebuild state from disk
	// newIndex reads the file's existing byte count into idx.size, so the
	// in-memory cursor is restored without replaying any entries explicitly.
	f, _ = os.OpenFile(f.Name(), os.O_RDWR, 0600)
	idx, err = newIndex(f, c)
	require.NoError(t, err)

	// Read(-1) returns the last entry. After restart it must still be
	// {Off:1, Pos:10} — proving data survived Close and re-open.
	off, pos, err := idx.Read(-1)
	require.NoError(t, err)
	require.Equal(t, uint32(1), off)
	require.Equal(t, entries[1].Pos, pos)
}
