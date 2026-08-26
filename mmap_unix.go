//go:build unix

package constmap

import (
	"os"
	"syscall"
)

// mmapFile maps the first size bytes of f read-only and shared.
func mmapFile(f *os.File, size int) ([]byte, error) {
	return syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
}

func munmap(b []byte) error {
	return syscall.Munmap(b)
}
