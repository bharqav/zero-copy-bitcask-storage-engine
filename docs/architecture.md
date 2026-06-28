# Zero-Copy Bitcask Storage Engine

This project is an append-only key-value store optimized for point reads. Values are read directly from memory-mapped log files; `Get` returns a slice backed by mmap rather than allocating and copying a value buffer.

## Storage Format

Every record is:

```text
| CRC32 4B | Timestamp 8B | KeySize 4B | ValueSize 4B | Key | Value |
```

The CRC covers `Timestamp + KeySize + ValueSize + Key + Value`. A `ValueSize` of `0` is a tombstone.

## Segment Structure

```mermaid
flowchart LR
  S["000000001.data"] --> R1["record: key=a value=v1"]
  S --> R2["record: key=b value=v1"]
  S --> R3["record: key=a tombstone"]
  T["000000002.data"] --> R4["record: key=c value=v1"]
  T --> R5["record: key=a value=v2"]
```

Segments are immutable after rotation. The active segment is append-only. Compaction writes a fresh generation containing only live records and then opens a newer active segment.

## Write Path

```mermaid
flowchart LR
  A["Put(key, value)"] --> B["Validate sizes"]
  B --> C["Encode record + CRC"]
  C --> D["Append to active .data file"]
  D --> E["fsync according to policy"]
  E --> F["Update sharded KeyDir"]
  F --> G["Refresh mmap registry"]
```

Fsync policies:

- `FsyncEveryWrite`: acknowledge only after the active file reaches stable storage.
- `FsyncInterval`: group fsyncs by time interval.
- `FsyncManual`: caller is responsible for `Sync`.

## Read Path

```mermaid
flowchart LR
  A["Get(key)"] --> B["Lookup KeyDir"]
  B --> C["Find FileID + ValueOffset"]
  C --> D["Slice mmap bytes"]
  D --> E["Return direct []byte view"]
```

The returned value must be treated as read-only. It points into mapped file memory.

## Recovery Path

```mermaid
flowchart TD
  A["Open database"] --> B["Sort .data files by FileID"]
  B --> C["Scan records sequentially"]
  C --> D{"CRC and size valid?"}
  D -- "yes" --> E["Apply latest state to KeyDir"]
  D -- "no / partial / torn" --> F["Truncate file at last valid offset"]
  E --> C
  F --> G["Map readable files and open active file"]
```

Recovery handles:

- Partial writes: short headers or payloads are truncated.
- Torn writes: invalid size fields or CRC mismatches are truncated.
- Tombstones: delete markers rebuild as missing keys.

## Mmap Lifecycle

The active file is append-only. After writes, the file is remapped so new values are visible through `Get`. Older mappings are retired and closed on database shutdown, which avoids invalidating slices already returned to callers.

```mermaid
stateDiagram-v2
  [*] --> ActiveAppend
  ActiveAppend --> MappedActive: refresh after append
  MappedActive --> ReadOnlyGeneration: rotate
  ReadOnlyGeneration --> CompactionInput: threshold reached
  CompactionInput --> RetiredMapping: compacted replacement exists
  RetiredMapping --> Closed: DB close
```

## Compaction

```mermaid
flowchart TD
  A["Select read-only generations"] --> B["Scan old records"]
  B --> C{"Record timestamp matches KeyDir?"}
  C -- "no" --> D["Drop stale record"]
  C -- "yes, tombstone" --> E["Drop tombstone"]
  C -- "yes, live value" --> F["Write to compacted generation"]
  F --> G["Atomically swap generation"]
  G --> H["Update KeyDir offsets"]
```

Compaction removes overwritten records and tombstones from read-only files. A background worker can call `RunCompaction`.

## Shard Distribution

```mermaid
flowchart TD
  K["key bytes"] --> H["FNV-1a hash"]
  H --> M["hash % shard_count"]
  M --> S0["Shard 0 RWMutex + map"]
  M --> S1["Shard 1 RWMutex + map"]
  M --> S2["Shard N RWMutex + map"]
  S0 --> R["RecordState only: FileID, ValueOffset, ValueSize, Timestamp"]
  S1 --> R
  S2 --> R
```

The KeyDir never stores values. It stores only disk coordinates, keeping RAM proportional to key count rather than value size.

## Observability

`DB.Stats()` exposes counters for reads, writes, deletes, fsyncs, rotations, compactions, mmap refreshes, corrupt records observed during recovery, tombstones, live key count, and retired mmap regions.

## Benchmarks

Current benchmark coverage includes:

- Random zero-copy point reads.
- Manual-fsync write throughput.
- Every-write-fsync throughput.
- p99-style read latency reporting.
- mmap slicing vs `os.File.ReadAt`.

External engine comparisons such as BadgerDB, Pebble, and RocksDB should live behind build tags so the core module remains dependency-light.
