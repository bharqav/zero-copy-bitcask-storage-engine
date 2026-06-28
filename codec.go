package zerocopybitcask

import (
	"encoding/binary"
	"hash/crc32"
)

type Header struct {
	CRC       uint32
	Timestamp uint64
	KeySize   uint32
	ValueSize uint32
}

func EncodeRecord(key, value []byte, timestamp uint64) []byte {
	size := HeaderSize + len(key) + len(value)
	buf := make([]byte, size)
	binary.BigEndian.PutUint64(buf[TimestampOffset:], timestamp)
	binary.BigEndian.PutUint32(buf[KeySizeOffset:], uint32(len(key)))
	binary.BigEndian.PutUint32(buf[ValueSizeOffset:], uint32(len(value)))
	copy(buf[HeaderSize:], key)
	copy(buf[HeaderSize+len(key):], value)
	binary.BigEndian.PutUint32(buf[CRCOffset:], crc32.ChecksumIEEE(buf[TimestampOffset:]))
	return buf
}

func DecodeHeader(buf []byte) (Header, error) {
	if len(buf) < HeaderSize {
		return Header{}, ErrCorruptRecord
	}
	return Header{
		CRC:       binary.BigEndian.Uint32(buf[CRCOffset:]),
		Timestamp: binary.BigEndian.Uint64(buf[TimestampOffset:]),
		KeySize:   binary.BigEndian.Uint32(buf[KeySizeOffset:]),
		ValueSize: binary.BigEndian.Uint32(buf[ValueSizeOffset:]),
	}, nil
}

func VerifyRecord(buf []byte) (Header, bool) {
	header, err := DecodeHeader(buf)
	if err != nil {
		return Header{}, false
	}
	total := HeaderSize + int(header.KeySize) + int(header.ValueSize)
	if total < HeaderSize || len(buf) < total {
		return Header{}, false
	}
	return header, crc32.ChecksumIEEE(buf[TimestampOffset:total]) == header.CRC
}

func RecordSize(header Header) uint64 {
	return uint64(HeaderSize) + uint64(header.KeySize) + uint64(header.ValueSize)
}
