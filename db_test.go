package zerocopybitcask

import (
	"bytes"
	"testing"
)

func TestPutGetDeleteRecovery(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Dir: dir, SyncWrites: true, MaxActiveFileSize: 128})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Put([]byte("a"), []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("a"), []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("second")) {
		t.Fatalf("got %q", got)
	}
	if err := db.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get([]byte("a")); err != ErrKeyNotFound {
		t.Fatalf("delete get err = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(Options{Dir: dir, SyncWrites: true, MaxActiveFileSize: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Get([]byte("a")); err != ErrKeyNotFound {
		t.Fatalf("recovered deleted key err = %v", err)
	}
}

func TestIteratorPrefixAndRange(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Dir: dir, FsyncPolicy: FsyncManual})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, kv := range []struct {
		key string
		val string
	}{
		{"user:001", "a"},
		{"user:002", "b"},
		{"user:003", "c"},
		{"order:001", "x"},
	} {
		if err := db.Put([]byte(kv.key), []byte(kv.val)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Delete([]byte("user:002")); err != nil {
		t.Fatal(err)
	}

	it := db.NewIterator(IteratorOptions{
		Prefix: []byte("user:"),
		Start:  []byte("user:001"),
		End:    []byte("user:004"),
	})
	defer it.Close()

	var keys []string
	for it.Next() {
		value, err := it.Value()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(it.Key())+"="+string(value))
	}
	want := []string{"user:001=a", "user:003=c"}
	if !sameStrings(keys, want) {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestCompactionKeepsLiveRecords(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Dir: dir, SyncWrites: true, MaxActiveFileSize: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Put([]byte("a"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("b"), []byte("live")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("a"), []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get([]byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("live")) {
		t.Fatalf("b = %q", got)
	}
	got, err = db.Get([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("new")) {
		t.Fatalf("a = %q", got)
	}
}

func TestCompactIfNeededHonorsThreshold(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{
		Dir:                      dir,
		FsyncPolicy:              FsyncManual,
		MaxActiveFileSize:        64,
		CompactionThresholdBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := 0; i < 10; i++ {
		if err := db.Put([]byte{byte('k'), byte(i)}, []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompactIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if got := db.Stats().Compactions; got == 0 {
		t.Fatal("expected threshold compaction to run")
	}
}
