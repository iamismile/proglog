package log

import (
	"io"
	"os"

	"github.com/tysonmote/gommap"
)

// Each index entry layout:
//
// | offset (4 bytes) | position (8 bytes) |
// ---------------------------------------
// total = 12 bytes
//
// offset  → logical record number (0,1,2,3,...)
// position → where that record starts in store file
var (
	offWidth uint64 = 4                   // uint32 → saves space
	posWidth uint64 = 8                   // uint64 → supports large files
	entWidth        = offWidth + posWidth // 12 bytes total per index entry
)

type index struct {
	file *os.File    // underlying index file on disk
	mmap gommap.MMap // the file mapped into virtual memory — reads/writes go here, not via syscalls
	size uint64      // how many bytes are actually used
}

func newIndex(f *os.File, c Config) (*index, error) {
	idx := &index{file: f}

	// capture bytes already written before we grow the file
	fi, err := os.Stat(f.Name())
	if err != nil {
		return nil, err
	}
	idx.size = uint64(fi.Size())

	// mmap requires the file to be at least as large as the region we want to
	// map.  If the file were smaller, accessing any byte beyond the original
	// end would cause a bus error (SIGBUS).
	//
	// We pre-grow with Truncate rather than writing actual data; the OS fills
	// the extra space with zero bytes.  The real content length is tracked by
	// idx.size, and Close shrinks the file back to that length.
	if err = os.Truncate(f.Name(), int64(c.Segment.MaxIndexBytes)); err != nil {
		return nil, err
	}

	// Memory-map the file into the process's address space.
	//
	// PROT_READ | PROT_WRITE: We need to both read existing entries and
	// write new ones as records are appended.
	//
	// MAP_SHARED → changes flush back to disk (MAP_PRIVATE would never hit disk)
	if idx.mmap, err = gommap.Map(
		idx.file.Fd(),
		gommap.PROT_READ|gommap.PROT_WRITE,
		gommap.MAP_SHARED,
	); err != nil {
		return nil, err
	}

	return idx, nil
}

func (i *index) Close() error {
	//  Flush dirty mmap pages → kernel buffer (blocking) — MS_SYNC blocks until
	// the kernel confirms the write is complete, preventing data loss on crash
	if err := i.mmap.Sync(gommap.MS_SYNC); err != nil {
		return err
	}

	// Flush kernel buffer → durable storage (fsync).
	if err := i.file.Sync(); err != nil {
		return err
	}

	// Shrink the file back to only the bytes that were actually written.
	if err := i.file.Truncate(int64(i.size)); err != nil {
		return err
	}

	return i.file.Close()
}

func (i *index) Write(off uint32, pos uint64) error {
	// Guard: make sure there is room for exactly one more entry.
	// len(i.mmap) is fixed at MaxIndexBytes; i.size grows with each Write.
	if uint64(len(i.mmap)) < i.size+entWidth {
		return io.EOF
	}

	// Write the 4-byte offset field at the current cursor position.
	// enc is binary.BigEndian
	enc.PutUint32(i.mmap[i.size:i.size+offWidth], off)

	// Write the 8-byte position field immediately after the offset field.
	enc.PutUint64(i.mmap[i.size+offWidth:i.size+entWidth], pos)

	// Advance the write cursor so the next Write starts after this entry.
	i.size += uint64(entWidth)

	return nil
}

func (i *index) Read(in int64) (off uint32, pos uint64, err error) {
	// Nothing written yet — any read is out of range.
	if i.size == 0 {
		return 0, 0, io.EOF
	}

	// `off` temporarily holds the zero-based *slot number* to read.
	if in == -1 {
		// Example: 3 entries written → i.size=36 → off = (36/12)-1 = 2.
		off = uint32((i.size / entWidth) - 1)
	} else {
		off = uint32(in)
	}

	// `pos`  holds the *byte position inside the mmap* where
	//  this entry begins.
	pos = uint64(off) * entWidth
	if i.size < pos+entWidth {
		return 0, 0, io.EOF
	}

	// Deserialise the entry
	off = enc.Uint32(i.mmap[pos : pos+offWidth])
	pos = enc.Uint64(i.mmap[pos+offWidth : pos+entWidth])

	return off, pos, nil
}

func (i *index) Name() string {
	return i.file.Name()
}
