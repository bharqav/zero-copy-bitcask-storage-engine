package zerocopybitcask

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (db *DB) recover() error {
	ids, err := dataFileIDs(db.opts.Dir)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		ids = []uint32{1}
		file, err := os.OpenFile(dataFilePath(db.opts.Dir, 1), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		size, err := db.recoverFile(id)
		if err != nil {
			return err
		}
		db.activeFileID = id
		db.activeSize = size
	}

	activePath := dataFilePath(db.opts.Dir, db.activeFileID)
	active, err := os.OpenFile(activePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	db.activeFile = active

	for _, id := range ids {
		if err := db.refreshMapLocked(id); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) recoverFile(fileID uint32) (int64, error) {
	path := dataFilePath(db.opts.Dir, fileID)
	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var offset int64
	headerBuf := make([]byte, HeaderSize)
	for {
		_, err := io.ReadFull(file, headerBuf)
		if errors.Is(err, io.EOF) {
			return offset, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			db.stats.corruptRecords.Add(1)
			return offset, file.Truncate(offset)
		}
		if err != nil {
			return offset, err
		}

		header, err := DecodeHeader(headerBuf)
		if err != nil {
			db.stats.corruptRecords.Add(1)
			return offset, file.Truncate(offset)
		}
		if header.KeySize == 0 || header.KeySize > db.opts.MaxKeySize || header.ValueSize > db.opts.MaxValueSize {
			db.stats.corruptRecords.Add(1)
			return offset, file.Truncate(offset)
		}

		payloadSize := int64(header.KeySize) + int64(header.ValueSize)
		record := make([]byte, HeaderSize+payloadSize)
		copy(record, headerBuf)
		if _, err := io.ReadFull(file, record[HeaderSize:]); err != nil {
			db.stats.corruptRecords.Add(1)
			return offset, file.Truncate(offset)
		}
		verified, ok := VerifyRecord(record)
		if !ok {
			db.stats.corruptRecords.Add(1)
			return offset, file.Truncate(offset)
		}

		keyStart := HeaderSize
		keyEnd := keyStart + int(verified.KeySize)
		key := record[keyStart:keyEnd]
		valueOffset := uint64(offset) + HeaderSize + uint64(verified.KeySize)
		next := RecordState{
			FileID:      fileID,
			ValueSize:   verified.ValueSize,
			ValueOffset: valueOffset,
			Timestamp:   verified.Timestamp,
		}
		if current, ok := db.keydir.Get(key); !ok || next.Timestamp >= current.Timestamp {
			db.keydir.Set(key, next)
		}
		if verified.ValueSize == 0 {
			db.stats.tombstones.Add(1)
		}
		offset += int64(RecordSize(verified))
	}
}

func dataFileIDs(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []uint32
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".data") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".data")
		n, err := strconv.ParseUint(stem, 10, 32)
		if err != nil {
			continue
		}
		ids = append(ids, uint32(n))
	}
	return ids, nil
}

func compactedFilePath(dir string, fileID uint32) string {
	return filepath.Join(dir, strconv.FormatUint(uint64(fileID), 10)+".compact")
}
