package constmap

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

// saveTemp builds a map over keys/values and writes it to a fresh file.
func saveTemp(t *testing.T, name string, keys []string, values []uint64) (*ConstMap, string) {
	t.Helper()
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := cm.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	return cm, path
}

// writeV1 writes cm in the legacy unpadded "CMAP0001" layout.
func writeV1(t *testing.T, path string, cm *ConstMap) {
	t.Helper()
	var body bytes.Buffer
	h := fnv.New64a()
	mw := io.MultiWriter(&body, h)

	var buf [8]byte
	copy(buf[:], "CMAP0001")
	mustWrite(t, mw, buf[:])
	binary.LittleEndian.PutUint64(buf[:], cm.seed)
	mustWrite(t, mw, buf[:])
	binary.LittleEndian.PutUint32(buf[:4], cm.segmentLength)
	mustWrite(t, mw, buf[:4])
	binary.LittleEndian.PutUint32(buf[:4], cm.segmentCount)
	mustWrite(t, mw, buf[:4])
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(cm.data)))
	mustWrite(t, mw, buf[:4])
	for _, v := range cm.data {
		binary.LittleEndian.PutUint64(buf[:], v)
		mustWrite(t, mw, buf[:])
	}
	binary.LittleEndian.PutUint64(buf[:], h.Sum64())
	mustWrite(t, &body, buf[:])

	if err := os.WriteFile(path, body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, w io.Writer, b []byte) {
	t.Helper()
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
}

func TestMappedRoundTrip(t *testing.T) {
	keys := []string{"apple", "banana", "cherry", "date", "elderberry"}
	values := []uint64{100, 200, 300, 400, 500}
	_, path := saveTemp(t, "fruit.cmap", keys, values)

	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if !m.Mapped() {
		t.Error("expected a real memory mapping on this platform")
	}
	for i, k := range keys {
		if got := m.Map(k); got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
	if err := m.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestMappedZeroCopy checks that the data array really aliases the mapping
// rather than a copy of it.
func TestMappedZeroCopy(t *testing.T) {
	keys := []string{"one", "two", "three"}
	values := []uint64{1, 2, 3}
	_, path := saveTemp(t, "alias.cmap", keys, values)

	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if len(m.data) == 0 {
		t.Fatal("no data")
	}
	base := uintptr(unsafe.Pointer(&m.mapping[0]))
	first := uintptr(unsafe.Pointer(&m.data[0]))
	if first != base+headerSize {
		t.Errorf("data starts at mapping+%d, want mapping+%d", first-base, headerSize)
	}
	if first%8 != 0 {
		t.Errorf("data is not eight-byte aligned: %#x", first)
	}
	end := first + uintptr(8*len(m.data))
	if end+8 > base+uintptr(len(m.mapping)) {
		t.Error("data extends past the mapping")
	}
}

func TestMappedLarge(t *testing.T) {
	n := 100000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i * 7)
	}
	_, path := saveTemp(t, "large.cmap", keys, values)

	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	for i, k := range keys {
		if got := m.Map(k); got != values[i] {
			t.Fatalf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
	if err := m.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestMappedMatchesLoad checks that mapping and loading the same file agree.
func TestMappedMatchesLoad(t *testing.T) {
	n := 20000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("k%d", i)
		values[i] = uint64(i)*0x9E3779B97F4A7C15 + 3
	}
	_, path := saveTemp(t, "agree.cmap", keys, values)

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if m.seed != loaded.seed || m.segmentLength != loaded.segmentLength ||
		m.segmentCount != loaded.segmentCount || m.segmentCountLength != loaded.segmentCountLength {
		t.Error("mapped header differs from loaded header")
	}
	if len(m.data) != len(loaded.data) {
		t.Fatalf("mapped %d words, loaded %d", len(m.data), len(loaded.data))
	}
	for i := range m.data {
		if m.data[i] != loaded.data[i] {
			t.Fatalf("data[%d]: mapped %d, loaded %d", i, m.data[i], loaded.data[i])
		}
	}
	for i, k := range keys {
		if got := m.Map(k); got != values[i] {
			t.Fatalf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestMappedEmpty(t *testing.T) {
	_, path := saveTemp(t, "empty.cmap", nil, nil)

	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if len(m.data) != 0 {
		t.Errorf("expected empty data, got %d words", len(m.data))
	}
	if err := m.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestMappedV1Fallback checks that a legacy unpadded file still opens, by way
// of the copy fallback.
func TestMappedV1Fallback(t *testing.T) {
	keys := []string{"alpha", "beta", "gamma", "delta"}
	values := []uint64{11, 22, 33, 44}
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.cmap")
	writeV1(t, path, cm)

	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if m.Mapped() {
		t.Error("a CMAP0001 file should not be aliased in place")
	}
	for i, k := range keys {
		if got := m.Map(k); got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
	if err := m.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestMappedCorrupted(t *testing.T) {
	keys := []string{"apple", "banana", "cherry"}
	values := []uint64{10, 20, 30}
	_, path := saveTemp(t, "corrupt.cmap", keys, values)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// OpenMapped does not checksum, so it succeeds; Verify must not.
	m, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.Verify(); err == nil {
		t.Error("expected a checksum error for corrupted data, got nil")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(); err == nil {
		t.Error("expected an error from Verify after Close, got nil")
	}
}

func TestMappedBadMagic(t *testing.T) {
	keys := []string{"a", "b", "c"}
	values := []uint64{1, 2, 3}
	_, path := saveTemp(t, "magic.cmap", keys, values)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copy(raw[:8], "NOPE0000")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenMapped(path); err == nil {
		t.Error("expected an error for bad magic bytes, got nil")
	} else if !strings.Contains(err.Error(), "magic") {
		t.Errorf("expected a magic-bytes error, got %v", err)
	}
}

func TestMappedTruncated(t *testing.T) {
	keys := make([]string, 1000)
	values := make([]uint64, 1000)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i)
	}
	_, path := saveTemp(t, "short.cmap", keys, values)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the header but drop most of the data.
	if err := os.WriteFile(path, raw[:headerSize+64], 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenMapped(path); err == nil {
		t.Error("expected an error for a truncated file, got nil")
	}
}

func TestMappedTooShort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.cmap")
	if err := os.WriteFile(path, []byte("CMAP0002"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMapped(path); err == nil {
		t.Error("expected an error for an eight-byte file, got nil")
	}
}

func TestMappedMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.cmap")
	if _, err := OpenMapped(path); !os.IsNotExist(err) {
		t.Errorf("expected a not-exist error, got %v", err)
	}
}

// TestMappedShared checks that two mappings of one file coexist.
func TestMappedShared(t *testing.T) {
	keys := []string{"x", "y", "z"}
	values := []uint64{7, 8, 9}
	_, path := saveTemp(t, "shared.cmap", keys, values)

	a, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenMapped(path)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		if got := b.Map(k); got != values[i] {
			t.Errorf("b.Map(%q) = %d, want %d", k, got, values[i])
		}
	}
	// Closing one mapping must leave the other usable.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		if got := a.Map(k); got != values[i] {
			t.Errorf("a.Map(%q) after b.Close = %d, want %d", k, got, values[i])
		}
	}
}

// mappedSink keeps benchmark lookups from being optimized away.
var mappedSink uint64

func benchMappedFile(b *testing.B, n int) (string, []string) {
	b.Helper()
	keys, values := makeBenchData(n)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "bench.cmap")
	if err := cm.SaveToFile(path); err != nil {
		b.Fatal(err)
	}
	return path, keys
}

func BenchmarkMappedConstMap(b *testing.B) {
	path, keys := benchMappedFile(b, benchN)
	m, err := OpenMapped(path)
	if err != nil {
		b.Fatal(err)
	}
	defer m.Close()

	b.ResetTimer()
	var sum uint64
	for i := 0; i < b.N; i++ {
		sum += m.Map(keys[i%len(keys)])
	}
	mappedSink = sum
}

func BenchmarkOpenMapped(b *testing.B) {
	path, _ := benchMappedFile(b, benchN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m, err := OpenMapped(path)
		if err != nil {
			b.Fatal(err)
		}
		if err := m.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkOpenMappedVerify is the apples-to-apples counterpart to
// BenchmarkLoadFromFile: both end with a map whose checksum has been checked.
func BenchmarkOpenMappedVerify(b *testing.B) {
	path, _ := benchMappedFile(b, benchN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m, err := OpenMapped(path)
		if err != nil {
			b.Fatal(err)
		}
		if err := m.Verify(); err != nil {
			b.Fatal(err)
		}
		mappedSink = m.data[0]
		if err := m.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadFromFileBuffered guards the claim that callers need not buffer
// the file themselves: now that ReadFrom moves the data array in chunks, a
// bufio.Reader only adds a second copy. It should not beat BenchmarkLoadFromFile.
func BenchmarkLoadFromFileBuffered(b *testing.B) {
	path, _ := benchMappedFile(b, benchN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		var cm ConstMap
		if _, err := cm.ReadFrom(bufio.NewReaderSize(f, 1<<20)); err != nil {
			b.Fatal(err)
		}
		f.Close()
		mappedSink = cm.data[0]
	}
}

func BenchmarkLoadFromFile(b *testing.B) {
	path, _ := benchMappedFile(b, benchN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm, err := LoadFromFile(path)
		if err != nil {
			b.Fatal(err)
		}
		mappedSink = cm.data[0]
	}
}
