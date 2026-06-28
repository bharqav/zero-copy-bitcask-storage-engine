package zerocopybitcask

import (
	"errors"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	HeaderSize      = 20
	CRCOffset       = 0
	TimestampOffset = 4
	KeySizeOffset   = 12
	ValueSizeOffset = 16

	DefaultMaxKeySize    = 64 << 10
	DefaultMaxValueSize  = 1 << 20
	DefaultMaxActiveSize = 1 << 30
	DefaultKeyDirShards  = 256
)

var (
	ErrKeyNotFound    = errors.New("key not found")
	ErrKeyTooLarge    = errors.New("key exceeds maximum size")
	ErrValueTooLarge  = errors.New("value exceeds maximum size")
	ErrEmptyKey       = errors.New("key must not be empty")
	ErrCorruptRecord  = errors.New("corrupt record")
	ErrDatabaseClosed = errors.New("database is closed")
)

type RecordState struct {
	FileID      uint32
	ValueSize   uint32
	ValueOffset uint64
	Timestamp   uint64
}

type CompactionStats struct {
	InputFiles        int
	LiveRecords       uint64
	StaleRecords      uint64
	TombstonesDropped uint64
	BytesRewritten    uint64
	OutputFileID      uint32
}

type FsyncPolicy int

const (
	FsyncEveryWrite FsyncPolicy = iota
	FsyncManual
	FsyncInterval
)

type keyDirShard struct {
	mu sync.RWMutex
	m  map[string]RecordState
}

type KeyDir struct {
	shards []keyDirShard
}

func NewKeyDir(shardCount int) *KeyDir {
	if shardCount <= 0 {
		shardCount = DefaultKeyDirShards
	}
	kd := &KeyDir{shards: make([]keyDirShard, shardCount)}
	for i := range kd.shards {
		kd.shards[i].m = make(map[string]RecordState)
	}
	return kd
}

func (k *KeyDir) Get(key []byte) (RecordState, bool) {
	shard := k.shard(key)
	shard.mu.RLock()
	state, ok := shard.m[bytesToString(key)]
	shard.mu.RUnlock()
	return state, ok
}

func (k *KeyDir) Set(key []byte, state RecordState) {
	shard := k.shard(key)
	shard.mu.Lock()
	shard.m[string(key)] = state
	shard.mu.Unlock()
}

func (k *KeyDir) Delete(key []byte) {
	shard := k.shard(key)
	shard.mu.Lock()
	delete(shard.m, bytesToString(key))
	shard.mu.Unlock()
}

func (k *KeyDir) Snapshot() map[string]RecordState {
	out := make(map[string]RecordState)
	for i := range k.shards {
		shard := &k.shards[i]
		shard.mu.RLock()
		for key, state := range shard.m {
			out[key] = state
		}
		shard.mu.RUnlock()
	}
	return out
}

func (k *KeyDir) shard(key []byte) *keyDirShard {
	h := fnv.New32a()
	_, _ = h.Write(key)
	return &k.shards[int(h.Sum32())%len(k.shards)]
}

func bytesToString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

type Options struct {
	Dir                      string
	SyncWrites               bool
	FsyncPolicy              FsyncPolicy
	FsyncInterval            time.Duration
	MaxKeySize               uint32
	MaxValueSize             uint32
	MaxActiveFileSize        int64
	KeyDirShards             int
	CompactionThresholdBytes int64
}

func (o Options) withDefaults() Options {
	if o.MaxKeySize == 0 {
		o.MaxKeySize = DefaultMaxKeySize
	}
	if o.MaxValueSize == 0 {
		o.MaxValueSize = DefaultMaxValueSize
	}
	if o.MaxActiveFileSize == 0 {
		o.MaxActiveFileSize = DefaultMaxActiveSize
	}
	if o.KeyDirShards == 0 {
		o.KeyDirShards = DefaultKeyDirShards
	}
	if o.SyncWrites {
		o.FsyncPolicy = FsyncEveryWrite
	}
	if o.FsyncPolicy == FsyncInterval && o.FsyncInterval <= 0 {
		o.FsyncInterval = time.Second
	}
	return o
}

type Stats struct {
	Gets           uint64
	Puts           uint64
	Deletes        uint64
	BytesWritten   uint64
	Fsyncs         uint64
	Rotations      uint64
	Compactions    uint64
	MmapRefreshes  uint64
	CorruptRecords uint64
	Tombstones     uint64
	KeyCount       uint64
	RetiredMmaps   uint64
	ObsoleteFiles  uint64
}

type dbStats struct {
	gets           atomic.Uint64
	puts           atomic.Uint64
	deletes        atomic.Uint64
	bytesWritten   atomic.Uint64
	fsyncs         atomic.Uint64
	rotations      atomic.Uint64
	compactions    atomic.Uint64
	mmapRefreshes  atomic.Uint64
	corruptRecords atomic.Uint64
	tombstones     atomic.Uint64
	staleRecords   atomic.Uint64
}
