package zerocopybitcask

import (
	"io"
	"os"
	"sort"
	"time"
)

func (db *DB) RunCompaction(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = db.CompactIfNeeded()
		case <-stop:
			return
		}
	}
}

func (db *DB) StartCompactor(interval time.Duration) func() {
	stop := make(chan struct{})
	go db.RunCompaction(interval, stop)
	return func() { close(stop) }
}

func (db *DB) CompactIfNeeded() error {
	readonly, err := db.readonlyFileIDs()
	if err != nil {
		return err
	}
	if len(readonly) == 0 {
		return nil
	}
	if db.opts.CompactionThresholdBytes <= 0 {
		_, err := db.Compact()
		return err
	}
	size, err := db.totalFileSize(readonly)
	if err != nil {
		return err
	}
	if size < db.opts.CompactionThresholdBytes {
		return nil
	}
	_, err = db.Compact()
	return err
}

func (db *DB) Compact() (CompactionStats, error) {
	db.compactMu.Lock()
	defer db.compactMu.Unlock()

	readonly, err := db.readonlyFileIDs()
	if err != nil {
		return CompactionStats{}, err
	}
	if len(readonly) == 0 {
		return CompactionStats{}, nil
	}

	db.writeMu.Lock()
	if db.closed {
		db.writeMu.Unlock()
		return CompactionStats{}, ErrDatabaseClosed
	}
	targetID := db.activeFileID + 1
	db.writeMu.Unlock()

	tmpPath := compactedFilePath(db.opts.Dir, targetID)
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return CompactionStats{}, err
	}

	updates := make(map[string]RecordState)
	result := CompactionStats{InputFiles: len(readonly), OutputFileID: targetID}
	var writeOffset uint64
	for _, id := range readonly {
		if err := db.copyLiveRecords(id, out, &writeOffset, updates, targetID, &result); err != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return CompactionStats{}, err
		}
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return CompactionStats{}, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return CompactionStats{}, err
	}

	finalPath := dataFilePath(db.opts.Dir, targetID)
	db.writeMu.Lock()
	defer db.writeMu.Unlock()
	if db.closed {
		_ = os.Remove(tmpPath)
		return CompactionStats{}, ErrDatabaseClosed
	}
	if db.activeFileID >= targetID {
		_ = os.Remove(tmpPath)
		return CompactionStats{}, nil
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return CompactionStats{}, err
	}
	db.activeFileID = targetID
	db.activeSize = 0
	if db.activeFile != nil {
		if err := db.activeFile.Close(); err != nil {
			return CompactionStats{}, err
		}
	}
	active, err := os.OpenFile(dataFilePath(db.opts.Dir, db.activeFileID+1), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return CompactionStats{}, err
	}
	db.activeFileID++
	db.activeFile = active

	if err := db.refreshMapLocked(targetID); err != nil {
		return CompactionStats{}, err
	}

	for key, state := range updates {
		current, ok := db.keydir.Get([]byte(key))
		if ok && current.Timestamp == state.Timestamp {
			db.keydir.Set([]byte(key), state)
		}
	}
	db.mapMu.Lock()
	for _, id := range readonly {
		if region := db.maps[id]; region != nil {
			db.retiredMaps = append(db.retiredMaps, region)
			delete(db.maps, id)
		}
		db.obsoleteFiles = append(db.obsoleteFiles, id)
	}
	db.mapMu.Unlock()
	db.stats.compactions.Add(1)
	db.stats.staleRecords.Add(result.StaleRecords)
	return result, nil
}

func (db *DB) copyLiveRecords(fileID uint32, out *os.File, writeOffset *uint64, updates map[string]RecordState, targetID uint32, result *CompactionStats) error {
	in, err := os.Open(dataFilePath(db.opts.Dir, fileID))
	if err != nil {
		return err
	}
	defer in.Close()

	headerBuf := make([]byte, HeaderSize)
	for {
		_, err := io.ReadFull(in, headerBuf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
		header, err := DecodeHeader(headerBuf)
		if err != nil {
			return err
		}
		payloadSize := int(header.KeySize + header.ValueSize)
		record := make([]byte, HeaderSize+payloadSize)
		copy(record, headerBuf)
		if _, err := io.ReadFull(in, record[HeaderSize:]); err != nil {
			return err
		}
		verified, ok := VerifyRecord(record)
		if !ok {
			return ErrCorruptRecord
		}

		keyStart := HeaderSize
		keyEnd := keyStart + int(verified.KeySize)
		key := record[keyStart:keyEnd]
		current, ok := db.keydir.Get(key)
		if !ok || current.Timestamp != verified.Timestamp || current.FileID != fileID || verified.ValueSize == 0 {
			if verified.ValueSize == 0 {
				result.TombstonesDropped++
			} else {
				result.StaleRecords++
			}
			continue
		}

		n, err := out.Write(record)
		if err != nil {
			return err
		}
		if n != len(record) {
			return io.ErrShortWrite
		}
		keyCopy := string(key)
		updates[keyCopy] = RecordState{
			FileID:      targetID,
			ValueSize:   verified.ValueSize,
			ValueOffset: *writeOffset + HeaderSize + uint64(verified.KeySize),
			Timestamp:   verified.Timestamp,
		}
		*writeOffset += uint64(len(record))
		result.LiveRecords++
		result.BytesRewritten += uint64(len(record))
	}
}

func (db *DB) readonlyFileIDs() ([]uint32, error) {
	db.writeMu.Lock()
	if db.closed {
		db.writeMu.Unlock()
		return nil, ErrDatabaseClosed
	}
	activeID := db.activeFileID
	db.writeMu.Unlock()

	ids, err := dataFileIDs(db.opts.Dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var readonly []uint32
	for _, id := range ids {
		if id < activeID {
			readonly = append(readonly, id)
		}
	}
	return readonly, nil
}

func (db *DB) totalFileSize(ids []uint32) (int64, error) {
	var total int64
	for _, id := range ids {
		info, err := os.Stat(dataFilePath(db.opts.Dir, id))
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}
