package log

import (
	"fmt"
	"os"
	"path"

	api "github.com/iamismile/proglog/api/v1"
	"google.golang.org/protobuf/proto"
)

// =============================================================================
// segment — the fundamental storage unit of the log
// =============================================================================
//
// A segment pairs exactly one store file with one index file under a shared
// baseOffset.  The log is composed of one or more segments; once a segment
// fills up (IsMaxed returns true), the log creates a new one.
//
// On-disk files for a segment with baseOffset 16:
//
//	16.store  — raw record bytes, each prefixed with an 8-byte length header
//	16.index  — fixed-width lookup table: relative offset → store position
//
// Offset model:
//
//	absolute offset — the global record number visible to callers (e.g. 16, 17, 18)
//	relative offset — absolute offset minus baseOffset (e.g. 0, 1, 2)
//	The index stores *relative* offsets so entry widths stay small (uint32).
type segment struct {
	store      *store // append-only file holding serialized record payloads
	index      *index // memory-mapped lookup table (relative offset → store pos)
	baseOffset uint64 // absolute offset of the very first record in this segment
	nextOffset uint64 // absolute offset that the NEXT Append will use
	config     Config // size limits, used by IsMaxed
}

// =============================================================================
// newSegment — open or create a segment
// =============================================================================

// newSegment opens (or creates) the store and index files for the segment
// whose first record has the given baseOffset, then restores nextOffset so
// that subsequent Appends continue from where the segment left off.
//
// File naming: <dir>/<baseOffset>.store  and  <dir>/<baseOffset>.index
//
// nextOffset restoration logic:
//   - If the index is empty (Read(-1) errors) → this is a brand-new segment,
//     so nextOffset = baseOffset (the first record will carry that offset).
//   - Otherwise → last relative offset stored in the index is `off`, so
//     nextOffset = baseOffset + off + 1  (one past the last written record).
func newSegment(dir string, baseOffset uint64, c Config) (*segment, error) {
	s := segment{
		baseOffset: baseOffset,
		config:     c,
	}

	// ── Open / create the store file ─────────────────────────────────────────
	// O_APPEND ensures every Write goes to the end even if another fd exists.
	storeFile, err := os.OpenFile(
		path.Join(dir, fmt.Sprintf("%d%s", baseOffset, ".store")),
		os.O_RDWR|os.O_CREATE|os.O_APPEND,
		0644,
	)
	if err != nil {
		return nil, err
	}
	if s.store, err = newStore(storeFile); err != nil {
		return nil, err
	}

	// ── Open / create the index file ─────────────────────────────────────────
	// No O_APPEND here — the index uses mmap for writes, not OS-level appends.
	indexFile, err := os.OpenFile(
		path.Join(dir, fmt.Sprintf("%d%s", baseOffset, ".index")),
		os.O_RDWR|os.O_CREATE,
		0644,
	)
	if err != nil {
		return nil, err
	}
	if s.index, err = newIndex(indexFile, c); err != nil {
		return nil, err
	}

	// ── Restore nextOffset from the last index entry ──────────────────────────
	// Read(-1) returns the last written (relative offset, store position).
	// On error the index is empty → start at baseOffset.
	if off, _, err := s.index.Read(-1); err != nil {
		// Empty index: this segment has never been written to.
		s.nextOffset = baseOffset
	} else {
		// off is relative; convert back to absolute and advance by 1.
		// Example: baseOffset=16, off=2 → nextOffset = 16+2+1 = 19
		s.nextOffset = baseOffset + uint64(off) + 1
	}

	return &s, nil
}

// =============================================================================
// Append — write one record to the segment
// =============================================================================

// Append serialises record, writes it to the store, then writes the index
// entry that maps its relative offset to its store position.
//
// Steps:
//  1. Stamp record.Offset with the next absolute offset.
//  2. Proto-marshal the record into bytes.
//  3. Append bytes to the store → get back the byte position.
//  4. Write (relativeOffset, storePosition) into the index.
//  5. Increment nextOffset.
//
// Returns the absolute offset assigned to this record.
//
// Index entry written:
//
//	relative offset = nextOffset - baseOffset   (fits in uint32)
//	store position  = pos returned by store.Append
func (s *segment) Append(record *api.Record) (offset uint64, err error) {
	// Save the offset we're about to assign before any mutation.
	curr := s.nextOffset
	record.Offset = curr // embed the offset in the proto message itself

	// Serialize the record to a byte slice for storage.
	p, err := proto.Marshal(record)
	if err != nil {
		return 0, err
	}

	// Write bytes to the store; pos is the byte offset in the store file
	// where this record's payload begins.
	_, pos, err := s.store.Append(p)
	if err != nil {
		return 0, err
	}

	// Write the index entry.
	// The index stores *relative* offsets (uint32) to keep entries compact.
	// Relative offset = absolute - base.
	// Example: nextOffset=18, baseOffset=16 → relative=2
	if err = s.index.Write(
		uint32(s.nextOffset-uint64(s.baseOffset)), // relative offset
		pos, // byte position in store
	); err != nil {
		return 0, err
	}

	s.nextOffset++ // advance cursor for the next Append

	return curr, nil // return the absolute offset assigned to this record
}

// =============================================================================
// Read — retrieve one record by absolute offset
// =============================================================================

// Read fetches the record at the given absolute offset.
//
// Steps:
//  1. Convert absolute offset → relative, look up the store position in the index.
//  2. Read the raw bytes from the store at that position.
//  3. Proto-unmarshal the bytes back into an api.Record.
//
// Example: off=18, baseOffset=16 → relative=2 → index returns storePos → Read bytes.
func (s *segment) Read(off uint64) (*api.Record, error) {
	// Translate absolute → relative offset, then look up the store position.
	_, pos, err := s.index.Read(int64(off - s.baseOffset))
	if err != nil {
		return nil, err
	}

	// Read the serialised record bytes from the store.
	p, err := s.store.Read(pos)
	if err != nil {
		return nil, err
	}

	// Deserialise back into the protobuf type.
	record := &api.Record{}
	err = proto.Unmarshal(p, record)
	if err != nil {
		return nil, err
	}

	return record, nil
}

// =============================================================================
// IsMaxed — check whether the segment is full
// =============================================================================

// IsMaxed returns true when either the store or the index has reached its
// configured size limit.  The log uses this to decide when to roll over to
// a new segment.
//
// Two independent limits because the store and index grow at different rates:
//
//	store grows by (8 + proto-message-size) bytes per record
//	index grows by exactly 12 bytes per record
func (s *segment) IsMaxed() bool {
	return s.store.size >= s.config.Segment.MaxStoreBytes ||
		s.index.size >= s.config.Segment.MaxIndexBytes
}

// =============================================================================
// Close — flush and close both files
// =============================================================================

// Close flushes and closes the index first, then the store.
// Index must be closed before store so that its mmap sync completes while
// the store file descriptor is still valid (no OS-level dependency here,
// but it mirrors the open order in newSegment for symmetry).
func (s *segment) Close() error {
	if err := s.index.Close(); err != nil {
		return err
	}
	if err := s.store.Close(); err != nil {
		return err
	}
	return nil
}

// =============================================================================
// Remove — delete the segment from disk
// =============================================================================

// Remove closes the segment and then deletes both the index and store files.
// Used when a segment's data is no longer needed (e.g. after compaction or
// truncation).
func (s *segment) Remove() error {
	if err := s.Close(); err != nil {
		return err
	}
	if err := os.Remove(s.index.Name()); err != nil {
		return err
	}
	if err := os.Remove(s.store.Name()); err != nil {
		return err
	}
	return nil
}

// nearestMultiple returns the largest multiple of k that is ≤ j.
// Used when the log needs to trim its index to a clean entry boundary
// (since each index entry is exactly `entWidth` bytes, the valid file
// size must always be a multiple of entWidth).
//
// Examples:
//
//	nearestMultiple(9,  4) → 8    (8 is the largest multiple of 4 that fits in 9)
//	nearestMultiple(12, 4) → 12   (12 is itself a multiple of 4)
//	nearestMultiple(13, 4) → 12
func nearestMultiple(j, k uint64) uint64 {
	return (j / k) * k
}
