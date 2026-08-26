//go:build !unix && !windows

package constmap

import "os"

// mmapFile always fails on platforms without memory mapping (js/wasm, plan9);
// OpenMapped then falls back to reading the file into ordinary memory.
func mmapFile(f *os.File, size int) ([]byte, error) {
	return nil, errNotMappable
}

func munmap(b []byte) error {
	return nil
}
