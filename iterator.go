package zerocopybitcask

import (
	"bytes"
	"sort"
)

type IteratorOptions struct {
	Prefix []byte
	Start  []byte
	End    []byte
}

type Iterator struct {
	db      *DB
	entries []iteratorEntry
	index   int
}

type iteratorEntry struct {
	key   string
	state RecordState
}

func (db *DB) NewIterator(opts IteratorOptions) *Iterator {
	snapshot := db.keydir.Snapshot()
	entries := make([]iteratorEntry, 0, len(snapshot))
	for key, state := range snapshot {
		keyBytes := []byte(key)
		if state.ValueSize == 0 {
			continue
		}
		if len(opts.Prefix) > 0 && !bytes.HasPrefix(keyBytes, opts.Prefix) {
			continue
		}
		if len(opts.Start) > 0 && bytes.Compare(keyBytes, opts.Start) < 0 {
			continue
		}
		if len(opts.End) > 0 && bytes.Compare(keyBytes, opts.End) >= 0 {
			continue
		}
		entries = append(entries, iteratorEntry{key: key, state: state})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})
	return &Iterator{
		db:      db,
		entries: entries,
		index:   -1,
	}
}

func (db *DB) Iterator() *Iterator {
	return db.NewIterator(IteratorOptions{})
}

func (db *DB) PrefixIterator(prefix []byte) *Iterator {
	return db.NewIterator(IteratorOptions{Prefix: prefix})
}

func (db *DB) PrefixScan(prefix []byte) *Iterator {
	return db.NewIterator(IteratorOptions{Prefix: prefix})
}

func (db *DB) RangeScan(start, end []byte) *Iterator {
	return db.NewIterator(IteratorOptions{Start: start, End: end})
}

func (it *Iterator) Next() bool {
	if it == nil {
		return false
	}
	it.index++
	return it.index < len(it.entries)
}

func (it *Iterator) Key() []byte {
	if it == nil || it.index < 0 || it.index >= len(it.entries) {
		return nil
	}
	return []byte(it.entries[it.index].key)
}

func (it *Iterator) Value() ([]byte, error) {
	if it == nil || it.index < 0 || it.index >= len(it.entries) {
		return nil, ErrKeyNotFound
	}
	return it.db.getState(it.entries[it.index].state)
}

func (it *Iterator) Close() error {
	return nil
}
