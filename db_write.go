package zerocopybitcask

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DB struct {
	opts Options

	keydir *KeyDir

	writeMu   sync.Mutex
	compactMu sync.Mutex
	mapMu     sync.RWMutex

	activeFile    *os.File
	activeFileID  uint32
	activeSize    int64
	maps          map[uint32]*mmapRegion
	retiredMaps   []*mmapRegion
	obsoleteFiles []uint32
	lastSync      time.Time
	stats         dbStats

	closed bool
}

func Open(opts Options) (*DB, error) {
	opts = opts.withDefaults()
	if opts.Dir == "" {
		return nil, fmt.Errorf("dir is required")
	}
	if err := os.MkdirAll(opts.Dir, 0755); err != nil {
		return nil, err
	}

	db := &DB{
		opts:   opts,
		keydir: NewKeyDir(opts.KeyDirShards),
		maps:   make(map[uint32]*mmapRegion),
	}
	if err := db.recover(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Put(key, value []byte) error {
	if err := db.validate(key, value); err != nil {
		return err
	}
	return db.putAt(key, value, uint64(time.Now().UnixNano()))
}

func (db *DB) Delete(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	if uint32(len(key)) > db.opts.MaxKeySize {
		return ErrKeyTooLarge
	}
	return db.putAt(key, nil, uint64(time.Now().UnixNano()))
}

func (db *DB) Sync() error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	if db.closed {
		return ErrDatabaseClosed
	}
	return db.syncLocked()
}

func (db *DB) Close() error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	db.closed = true
	var first error
	if db.activeFile != nil {
		if err := db.activeFile.Close(); err != nil {
			first = err
		}
		db.activeFile = nil
	}

	db.mapMu.Lock()
	for id, region := range db.maps {
		if err := region.Close(); err != nil && first == nil {
			first = err
		}
		delete(db.maps, id)
	}
	for _, region := range db.retiredMaps {
		if err := region.Close(); err != nil && first == nil {
			first = err
		}
	}
	db.retiredMaps = nil
	for _, fileID := range db.obsoleteFiles {
		if err := os.Remove(dataFilePath(db.opts.Dir, fileID)); err != nil && first == nil && !os.IsNotExist(err) {
			first = err
		}
	}
	db.obsoleteFiles = nil
	db.mapMu.Unlock()
	return first
}

func (db *DB) putAt(key, value []byte, timestamp uint64) error {
	record := EncodeRecord(key, value, timestamp)

	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	if db.closed {
		return ErrDatabaseClosed
	}
	if db.activeSize > 0 && db.activeSize+int64(len(record)) > db.opts.MaxActiveFileSize {
		if err := db.rotateActiveFileLocked(); err != nil {
			return err
		}
	}

	recordOffset := db.activeSize
	n, err := db.activeFile.Write(record)
	if err != nil {
		return err
	}
	if n != len(record) {
		return fmt.Errorf("short write: %d of %d", n, len(record))
	}
	db.activeSize += int64(n)
	db.stats.bytesWritten.Add(uint64(n))
	if db.shouldSyncLocked() {
		if err := db.syncLocked(); err != nil {
			return err
		}
	}

	valueOffset := uint64(recordOffset) + HeaderSize + uint64(len(key))
	db.keydir.Set(key, RecordState{
		FileID:      db.activeFileID,
		ValueSize:   uint32(len(value)),
		ValueOffset: valueOffset,
		Timestamp:   timestamp,
	})
	if len(value) == 0 {
		db.stats.deletes.Add(1)
		db.stats.tombstones.Add(1)
	} else {
		db.stats.puts.Add(1)
	}
	return db.refreshMapLocked(db.activeFileID)
}

func (db *DB) validate(key, value []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	if uint32(len(key)) > db.opts.MaxKeySize {
		return ErrKeyTooLarge
	}
	if uint32(len(value)) > db.opts.MaxValueSize {
		return ErrValueTooLarge
	}
	return nil
}

func (db *DB) rotateActiveFileLocked() error {
	if db.activeFile != nil {
		if err := db.syncLocked(); err != nil {
			return err
		}
		if err := db.activeFile.Close(); err != nil {
			return err
		}
	}
	db.activeFileID++
	db.activeSize = 0
	file, err := os.OpenFile(dataFilePath(db.opts.Dir, db.activeFileID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	db.activeFile = file
	db.stats.rotations.Add(1)
	return db.refreshMapLocked(db.activeFileID)
}

func (db *DB) refreshMapLocked(fileID uint32) error {
	path := dataFilePath(db.opts.Dir, fileID)
	region, err := mmapFile(path)
	if err != nil {
		return err
	}
	db.mapMu.Lock()
	old := db.maps[fileID]
	db.maps[fileID] = region
	if old != nil {
		db.retiredMaps = append(db.retiredMaps, old)
	}
	db.mapMu.Unlock()
	db.stats.mmapRefreshes.Add(1)
	return nil
}

func (db *DB) shouldSyncLocked() bool {
	switch db.opts.FsyncPolicy {
	case FsyncManual:
		return false
	case FsyncInterval:
		return db.lastSync.IsZero() || time.Since(db.lastSync) >= db.opts.FsyncInterval
	default:
		return true
	}
}

func (db *DB) syncLocked() error {
	if err := db.activeFile.Sync(); err != nil {
		return err
	}
	db.lastSync = time.Now()
	db.stats.fsyncs.Add(1)
	return nil
}

func (db *DB) Stats() Stats {
	snapshot := db.keydir.Snapshot()
	db.mapMu.RLock()
	retired := len(db.retiredMaps)
	obsolete := len(db.obsoleteFiles)
	db.mapMu.RUnlock()
	return Stats{
		Gets:           db.stats.gets.Load(),
		Puts:           db.stats.puts.Load(),
		Deletes:        db.stats.deletes.Load(),
		BytesWritten:   db.stats.bytesWritten.Load(),
		Fsyncs:         db.stats.fsyncs.Load(),
		Rotations:      db.stats.rotations.Load(),
		Compactions:    db.stats.compactions.Load(),
		MmapRefreshes:  db.stats.mmapRefreshes.Load(),
		CorruptRecords: db.stats.corruptRecords.Load(),
		Tombstones:     db.stats.tombstones.Load(),
		KeyCount:       uint64(len(snapshot)),
		RetiredMmaps:   uint64(retired),
		ObsoleteFiles:  uint64(obsolete),
	}
}

func dataFilePath(dir string, fileID uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%09d.data", fileID))
}
