//go:build windows

package zerocopybitcask

import (
	"os"
	"reflect"
	"syscall"
	"unsafe"
)

type mmapRegion struct {
	data   []byte
	handle syscall.Handle
}

func mmapFile(path string) (*mmapRegion, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return &mmapRegion{}, nil
	}

	h, err := syscall.CreateFileMapping(syscall.Handle(file.Fd()), nil, syscall.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(info.Size()))
	if err != nil {
		_ = syscall.CloseHandle(h)
		return nil, err
	}

	header := reflect.SliceHeader{
		Data: addr,
		Len:  int(info.Size()),
		Cap:  int(info.Size()),
	}
	return &mmapRegion{
		data:   *(*[]byte)(unsafe.Pointer(&header)),
		handle: h,
	}, nil
}

func (m *mmapRegion) Close() error {
	if m == nil {
		return nil
	}
	if len(m.data) > 0 {
		addr := uintptr(unsafe.Pointer(&m.data[0]))
		if err := syscall.UnmapViewOfFile(addr); err != nil {
			return err
		}
		m.data = nil
	}
	if m.handle != 0 {
		err := syscall.CloseHandle(m.handle)
		m.handle = 0
		return err
	}
	return nil
}
