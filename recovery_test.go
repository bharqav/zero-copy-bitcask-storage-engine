package zerocopybitcask

import (
	"bytes"
	"os"
	"sync"
	"testing"
)

func TestRecoveryTruncatesPartialWrite(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Dir: dir, FsyncPolicy: FsyncManual})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("stable"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(dataFilePath(dir, 1), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{1, 2, 3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(Options{Dir: dir, FsyncPolicy: FsyncManual})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Get([]byte("stable"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Fatalf("got %q", got)
	}
	if db.Stats().CorruptRecords == 0 {
		t.Fatal("expected corrupt record counter to increment")
	}
}

func TestRecoveryTruncatesTornWrite(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Dir: dir, FsyncPolicy: FsyncManual})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("stable"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(dataFilePath(dir, 1), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	bad := EncodeRecord([]byte("broken"), []byte("value"), 99)
	bad[len(bad)-1] ^= 0xff
	if _, err := file.Write(bad); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(Options{Dir: dir, FsyncPolicy: FsyncManual})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Get([]byte("broken")); err != ErrKeyNotFound {
		t.Fatalf("broken key err = %v", err)
	}
}

func TestConcurrentStress(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Dir: dir, FsyncPolicy: FsyncManual, MaxActiveFileSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		writer := writer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := []byte{byte('a' + writer), byte(i)}
				val := []byte{byte(i), byte(writer)}
				if err := db.Put(key, val); err != nil {
					t.Errorf("put: %v", err)
					return
				}
				if _, err := db.Get(key); err != nil {
					t.Errorf("get: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func FuzzRecordCodec(f *testing.F) {
	f.Add([]byte("key"), []byte("value"), uint64(1))
	f.Add([]byte("another-key"), []byte{}, uint64(2))
	f.Fuzz(func(t *testing.T, key []byte, value []byte, ts uint64) {
		if len(key) == 0 || len(key) > 1024 || len(value) > 4096 {
			return
		}
		record := EncodeRecord(key, value, ts)
		header, ok := VerifyRecord(record)
		if !ok {
			t.Fatal("encoded record failed verification")
		}
		if header.KeySize != uint32(len(key)) || header.ValueSize != uint32(len(value)) || header.Timestamp != ts {
			t.Fatalf("header mismatch: %#v", header)
		}
	})
}
