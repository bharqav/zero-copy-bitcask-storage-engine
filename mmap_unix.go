//go:build !windows

package zerocopybitcask

import (
	"os"
	"syscall"
)

type mmapRegion struct {
	data []byte
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
	data, err := syscall.Mmap(int(file.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &mmapRegion{data: data}, nil
}

func (m *mmapRegion) Close() error {
	if m == nil || len(m.data) == 0 {
		return nil
	}
	err := syscall.Munmap(m.data)
	m.data = nil
	return err
}
