package zerocopybitcask

import (
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func TestEncodeRecordLayout(t *testing.T) {
	key := []byte("alpha")
	value := []byte("bravo")
	ts := uint64(123456789)

	buf := EncodeRecord(key, value, ts)

	if len(buf) != HeaderSize+len(key)+len(value) {
		t.Fatalf("length = %d", len(buf))
	}
	if got := binary.BigEndian.Uint64(buf[TimestampOffset:]); got != ts {
		t.Fatalf("timestamp = %d", got)
	}
	if got := binary.BigEndian.Uint32(buf[KeySizeOffset:]); got != uint32(len(key)) {
		t.Fatalf("key size = %d", got)
	}
	if got := binary.BigEndian.Uint32(buf[ValueSizeOffset:]); got != uint32(len(value)) {
		t.Fatalf("value size = %d", got)
	}
	if got := string(buf[HeaderSize : HeaderSize+len(key)]); got != string(key) {
		t.Fatalf("key payload = %q", got)
	}
	if got := string(buf[HeaderSize+len(key):]); got != string(value) {
		t.Fatalf("value payload = %q", got)
	}
	wantCRC := crc32.ChecksumIEEE(buf[TimestampOffset:])
	if got := binary.BigEndian.Uint32(buf[CRCOffset:]); got != wantCRC {
		t.Fatalf("crc = %d, want %d", got, wantCRC)
	}
}

func TestVerifyRecordDetectsCorruption(t *testing.T) {
	buf := EncodeRecord([]byte("key"), []byte("value"), 99)
	if _, ok := VerifyRecord(buf); !ok {
		t.Fatal("valid record failed verification")
	}

	buf[len(buf)-1] ^= 0xff
	if _, ok := VerifyRecord(buf); ok {
		t.Fatal("corrupt record verified")
	}
}

func TestTombstoneRecord(t *testing.T) {
	buf := EncodeRecord([]byte("gone"), nil, 7)
	header, ok := VerifyRecord(buf)
	if !ok {
		t.Fatal("tombstone failed verification")
	}
	if header.ValueSize != 0 {
		t.Fatalf("value size = %d", header.ValueSize)
	}
}
