# proglog — Build Your Own Commit Log

Ever wondered how Kafka stores millions of messages? Or how databases keep a record of every change? They use something called a **commit log** (also called a write-ahead log). This project builds one from scratch in Go.

---

## What Is a Commit Log?

Think of a commit log like a **notebook where you can only ever add pages — never erase or rearrange them**.

- You write a record → it gets a number (called an **offset**), like page 1, page 2, page 3...
- You read a record → you ask "give me page 17" and get it back instantly
- Records are never edited or deleted — they're permanent

Systems like **Apache Kafka**, **etcd**, and most databases use this exact idea at their core.

---

## The Big Picture

This project builds the log in three layers, like Russian nesting dolls:

```
Your Code
    │
    ▼
 Log          ← the top-level thing you interact with
    │
    ▼
 Segment      ← the log is split into multiple chunks
    │
    ├── Store     ← where the actual data lives (a file on disk)
    └── Index     ← a lookup table so we can find records fast
```

Each layer has one job. Let's walk through them bottom-up.

---

## Layer 1: The Store (Where Data Lives)

The **store** is just a file on disk. When you append a record, it gets written to the end of the file. Simple.

The one trick: before writing your data, we write **8 bytes that say how long your data is**. This way, when reading, we always know where one record ends and the next begins.

```
File on disk:
┌─────────────────────────────────────────────────────────────────────┐
│ [8 bytes: "42 bytes coming"] [... 42 bytes of data ...]             │
│ [8 bytes: "17 bytes coming"] [... 17 bytes of data ...]             │
│ ...                                                                  │
└─────────────────────────────────────────────────────────────────────┘
```

Writing uses a **buffer** (like a notepad) so we don't hit the disk after every single write — we batch them up, which is much faster.

---

## Layer 2: The Index (The Fast Lookup Table)

Here's a problem: if you want record #1000, how do you find it in the store file? You'd have to read from the beginning and count through 999 records. Way too slow.

The **index** solves this. It's a simple table with two columns:

```
┌──────────────────────────────────┐
│  Record #  │  Where in file?     │
├──────────────────────────────────┤
│     0      │  byte 0             │
│     1      │  byte 50            │
│     2      │  byte 91            │
│    ...     │  ...                │
└──────────────────────────────────┘
```

Each row is exactly **12 bytes** (4 for the record number + 8 for the position). This means finding any record is instant — just multiply the record number by 12, read those 12 bytes, and you have the exact byte position to jump to in the store.

The index uses a technique called **memory-mapping** — the index file is loaded into memory so reads and writes happen at RAM speed instead of disk speed.

---

## Layer 3: The Segment (One Chunk of the Log)

A **segment** bundles one store file + one index file together. It has a `baseOffset` — the number of the first record it holds.

Why split into segments? Because files can't grow forever. When a segment gets full (either the store or index hits its size limit), we create a new segment and start writing there. It's like filling up one notebook and starting a fresh one.

On disk, a segment with `baseOffset = 16` looks like this:

```
16.store   ← all the record data
16.index   ← the lookup table
```

When we open an existing segment, we check the last entry in the index to figure out where to continue writing — so the log picks up right where it left off after a restart.

---

## Layer 4: The Log (The Full Thing)

The **Log** is what your code actually talks to. It manages a list of segments and handles all the coordination:

- **Writing** → always goes to the newest (active) segment. If it's full, a new segment is created automatically.
- **Reading** → figures out which segment contains the requested offset, then delegates to that segment.
- **Concurrency** → uses a read-write lock so multiple goroutines can read at the same time, but writes happen one at a time safely.

```
segments:  [seg 0–99] [seg 100–199] [seg 200–...] ← active
                                           ▲
                                     new writes go here
```

---

## Record Format

Each record is a small protobuf message with just two fields:

```proto
message Record {
  bytes  value  = 1;   // your data (whatever you want to store)
  uint64 offset = 2;   // the record's position in the log (set by the log, not you)
}
```

You provide the `value`. The log stamps `offset` for you when you call `Append`.

---

## How Append Works (Step by Step)

```
1. You call log.Append(record)
2. The log assigns the next available offset to the record
3. The record is serialized to bytes (protobuf)
4. The bytes are written to the store file → we get back a byte position
5. The index is updated: record number → byte position
6. The offset is returned to you
```

## How Read Works (Step by Step)

```
1. You call log.Read(offset)
2. The log finds which segment contains that offset
3. The segment asks the index: "where is this record?"
4. The index returns the byte position in the store
5. The store reads the bytes at that position
6. The bytes are deserialized back into a Record
7. The Record is returned to you
```

---

## Configuration

```go
type Config struct {
    Segment struct {
        MaxStoreBytes uint64  // How big can one store file get?
        MaxIndexBytes uint64  // How big can one index file get?
        InitialOffset uint64  // What number does the very first record get?
    }
}
```

When a store or index file hits its max size, the log automatically creates a new segment. The defaults are 1024 bytes each (tiny — great for testing, bump these up for real use).

---

## Project Structure

```
.
├── api/v1/
│   └── log.proto           # What a "Record" looks like
└── internal/log/
    ├── config.go            # Configuration options
    ├── store.go             # Reads and writes raw bytes to disk
    ├── index.go             # Fast lookup table (offset → disk position)
    ├── segment.go           # One store + one index working together
    └── log.go               # The full log: manages all segments
```

---

## Dependencies

| Package                       | What it does                                               |
| ----------------------------- | ---------------------------------------------------------- |
| `google.golang.org/protobuf`  | Turns Go structs into bytes and back (for storing records) |
| `github.com/tysonmote/gommap` | Loads the index file into memory so lookups are fast       |

---

## Frequently Asked Questions

**Why not just use a database?**
Databases add a lot of complexity (transactions, query planners, etc.). A commit log is the raw, minimal building block underneath all of that. Building it yourself is the best way to understand how storage systems actually work.

**Why split into segments instead of one big file?**
One huge file is hard to manage — you can't easily delete old data or recover from partial writes. Segments let you drop old data by simply deleting old segment files.

**Why store record numbers as "relative" in the index?**
Each segment starts at a different `baseOffset`. Storing `record# - baseOffset` (relative) instead of the full record number means we can use a smaller 4-byte integer instead of 8 bytes, saving space in every single index entry.

**Why does the index get pre-grown on open then shrunk on close?**
Memory-mapping requires the file to already be at least as large as the region we want to map. We grow it upfront to the max allowed size, use it, then shrink it back to only the bytes we actually wrote before closing. This is a common mmap pattern.
