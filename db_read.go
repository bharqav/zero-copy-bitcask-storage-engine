package zerocopybitcask

func (db *DB) Get(key []byte) ([]byte, error) {
	state, ok := db.keydir.Get(key)
	if !ok || state.ValueSize == 0 {
		return nil, ErrKeyNotFound
	}
	value, err := db.getState(state)
	if err == nil {
		db.stats.gets.Add(1)
	}
	return value, err
}

func (db *DB) getState(state RecordState) ([]byte, error) {
	db.mapMu.RLock()
	region := db.maps[state.FileID]
	if region == nil {
		db.mapMu.RUnlock()
		return nil, ErrKeyNotFound
	}
	start := state.ValueOffset
	end := start + uint64(state.ValueSize)
	if end > uint64(len(region.data)) {
		db.mapMu.RUnlock()
		return nil, ErrCorruptRecord
	}
	value := region.data[start:end]
	db.mapMu.RUnlock()
	return value, nil
}
