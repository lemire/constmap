package constmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"unsafe"
)

// errNotMappable signals that a file cannot be aliased in place, and that the
// caller should fall back to reading it into ordinary memory. It is not
// returned to users.
var errNotMappable = errors.New("constmap: file cannot be memory mapped")

// nativeLittleEndian reports whether this machine stores integers in the same
// byte order as the serialized format. Zero-copy access requires it.
var nativeLittleEndian = func() bool {
	x := uint16(1)
	return *(*byte)(unsafe.Pointer(&x)) == 1
}()

// MappedConstMap is a ConstMap whose data array lives in a memory-mapped file
// rather than on the Go heap. Opening one costs a single mmap call no matter
// how large the map is: the kernel faults pages in lazily as lookups touch
// them, nothing is copied, and several processes mapping the same file share
// one copy of the physical memory. The bytes also stay out of the Go heap, so
// they cost the garbage collector nothing.
//
// The embedded ConstMap carries the entire lookup API:
//
//	m, err := constmap.OpenMapped("fruit.cmap")
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer m.Close()
//	fmt.Println(m.Map("banana"))
//
// Pass &m.ConstMap to code that expects a *ConstMap. That pointer, and every
// value read through it, is valid only until Close.
type MappedConstMap struct {
	ConstMap

	// mapping is the whole file. It is nil when OpenMapped had to fall back
	// to reading the file into ordinary memory.
	mapping []byte
	closed  bool
}

// OpenMapped opens the file at path and returns a ConstMap that reads its data
// directly out of a read-only memory mapping, without copying it.
//
// Unlike LoadFromFile, OpenMapped does not verify the trailing checksum:
// hashing the file would touch every page and defeat the lazy, pay-per-lookup
// loading that memory mapping buys. It does check the magic bytes and that the
// file is large enough for the header it declares. Call Verify when integrity
// matters more than open latency.
//
// Files written by older versions of this package ("CMAP0001"), big-endian
// machines, and platforms without mmap all fall back transparently to reading
// the file into ordinary memory; the returned value behaves identically, and
// Close is then a no-op.
//
// The caller must Close the result. Using a MappedConstMap after Close, or
// modifying the underlying file while it is open, is undefined behavior.
func OpenMapped(path string) (*MappedConstMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size < headerSize+8 {
		return nil, fmt.Errorf("constmap: %s is %d bytes, too short to be a constmap file", path, size)
	}
	if size != int64(int(size)) {
		return nil, fmt.Errorf("constmap: %s is %d bytes, too large to map on this platform", path, size)
	}

	mapping, err := mmapFile(f, int(size))
	if err != nil {
		if errors.Is(err, errNotMappable) {
			return openCopied(path)
		}
		return nil, fmt.Errorf("constmap: mapping %s: %w", path, err)
	}

	m := &MappedConstMap{mapping: mapping}
	if err := m.parse(); err != nil {
		if uerr := munmap(mapping); uerr != nil {
			return nil, uerr
		}
		if errors.Is(err, errNotMappable) {
			return openCopied(path)
		}
		return nil, fmt.Errorf("constmap: %s: %w", path, err)
	}
	return m, nil
}

// openCopied is the fallback for files that cannot be aliased in place. The
// checksum is verified along the way, as it is for any other plain read.
func openCopied(path string) (*MappedConstMap, error) {
	cm, err := LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	return &MappedConstMap{ConstMap: *cm}, nil
}

// parse points the embedded ConstMap at the mapped bytes.
func (m *MappedConstMap) parse() error {
	b := m.mapping

	var magic [8]byte
	copy(magic[:], b[:8])
	switch magic {
	case magicBytes:
	case magicBytesV1:
		// Unpadded header: the data array is not eight-byte aligned.
		return errNotMappable
	default:
		return errors.New("invalid magic bytes")
	}
	if !nativeLittleEndian {
		return errNotMappable
	}

	m.seed = binary.LittleEndian.Uint64(b[8:16])
	m.segmentLength = binary.LittleEndian.Uint32(b[16:20])
	m.segmentLengthMask = m.segmentLength - 1
	m.segmentCount = binary.LittleEndian.Uint32(b[20:24])
	m.segmentCountLength = m.segmentCount * m.segmentLength
	dataLen := binary.LittleEndian.Uint32(b[24:28])

	want := int64(headerSize) + 8*int64(dataLen) + 8
	if int64(len(b)) < want {
		return fmt.Errorf("file is %d bytes but the header describes %d", len(b), want)
	}
	if dataLen == 0 {
		m.data = nil
		return nil
	}

	p := unsafe.Pointer(&b[headerSize])
	if uintptr(p)%8 != 0 {
		// A page-aligned mapping plus a 32-byte header should make this
		// impossible, but aliasing misaligned uint64s would be unsafe.
		return errNotMappable
	}
	m.data = unsafe.Slice((*uint64)(p), dataLen)
	return nil
}

// Mapped reports whether the map really is backed by a memory mapping, as
// opposed to the copy that OpenMapped falls back to for older files and
// platforms without mmap.
func (m *MappedConstMap) Mapped() bool {
	return m.mapping != nil
}

// Verify recomputes the file's FNV-1a checksum and reports whether the bytes
// are intact. It reads the whole map, so it costs about as much as
// LoadFromFile would have; OpenMapped skips it by design.
func (m *MappedConstMap) Verify() error {
	if m.closed {
		return errors.New("constmap: Verify on a closed map")
	}
	if m.mapping == nil {
		// The fallback path went through ReadFrom, which already checked.
		return nil
	}
	end := headerSize + 8*len(m.data)
	h := fnv.New64a()
	if _, err := h.Write(m.mapping[:end]); err != nil {
		return err
	}
	want := h.Sum64()
	got := binary.LittleEndian.Uint64(m.mapping[end : end+8])
	if got != want {
		return fmt.Errorf("constmap: checksum mismatch (got %016x, expected %016x)", got, want)
	}
	return nil
}

// Close releases the mapping. Every lookup value obtained from the map, and
// any *ConstMap taken from it, becomes invalid; touching one afterwards may
// crash the process. Close is idempotent.
func (m *MappedConstMap) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	m.data = nil
	mapping := m.mapping
	m.mapping = nil
	if mapping == nil {
		return nil
	}
	return munmap(mapping)
}
