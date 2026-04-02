package constmap

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBasic(t *testing.T) {
	keys := []string{"apple", "banana", "cherry", "date", "elderberry"}
	values := []uint64{100, 200, 300, 400, 500}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := cm.Map(k)
		if got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestEmpty(t *testing.T) {
	cm, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cm == nil {
		t.Fatal("expected non-nil ConstMap for empty input")
	}
}

func TestLarge(t *testing.T) {
	n := 100000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i * 7)
	}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := cm.Map(k)
		if got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestMismatchedLengths(t *testing.T) {
	_, err := New([]string{"a"}, []uint64{1, 2})
	if err == nil {
		t.Error("expected error for mismatched lengths")
	}
}

const benchN = 1_000_000

func makeBenchData(n int) ([]string, []uint64) {
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i)
	}
	return keys, values
}

func BenchmarkNew(b *testing.B) {
	for _, n := range []int{1 << 18, 1 << 20, 1 << 24} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			keys, values := makeBenchData(n)
			for b.Loop() {
				if _, err := New(keys, values); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNewVerified(b *testing.B) {
	for _, n := range []int{1 << 18, 1 << 20, 1 << 24} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			keys, values := makeBenchData(n)
			for b.Loop() {
				if _, err := NewVerified(keys, values); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkConstMap(b *testing.B) {
	keys, values := makeBenchData(benchN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.Map(keys[i%benchN])
	}
}

func BenchmarkVerifiedConstMap(b *testing.B) {
	keys, values := makeBenchData(benchN)
	vm, err := NewVerified(keys, values)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm.Map(keys[i%benchN])
	}
}

func BenchmarkGoMap(b *testing.B) {
	keys, values := makeBenchData(benchN)
	m := make(map[string]uint64, benchN)
	for i, k := range keys {
		m[k] = values[i]
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m[keys[i%benchN]]
	}
}

func TestMemoryUsage(t *testing.T) {
	n := benchN
	keys, values := makeBenchData(n)

	// Measure Go map memory via TotalAlloc (monotonic, never decreases).
	var mBefore, mAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mBefore)
	goMap := make(map[string]uint64, n)
	for i, k := range keys {
		goMap[k] = values[i]
	}
	runtime.ReadMemStats(&mAfter)
	goMapBytes := mAfter.TotalAlloc - mBefore.TotalAlloc
	runtime.KeepAlive(goMap)

	// Measure ConstMap retained size directly: it's just the data slice.
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	constMapBytes := uint64(cap(cm.data)) * 8 // []uint64, 8 bytes per element
	runtime.KeepAlive(cm)

	t.Logf("n = %d", n)
	t.Logf("ConstMap: %d bytes (%.1f bytes/key)", constMapBytes, float64(constMapBytes)/float64(n))
	t.Logf("Go map:   %d bytes (%.1f bytes/key)", goMapBytes, float64(goMapBytes)/float64(n))
	t.Logf("Ratio:    Go map uses %.1fx more memory than ConstMap", float64(goMapBytes)/float64(constMapBytes))
}

func TestRandomValues(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	n := 50000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("random-key-%d-%d", i, rng.Int63())
		values[i] = rng.Uint64()
	}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := cm.Map(k)
		if got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestSerializeDeserialize(t *testing.T) {
	keys := []string{"apple", "banana", "cherry", "date", "elderberry"}
	values := []uint64{100, 200, 300, 400, 500}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	var cm2 ConstMap
	if _, err := cm2.ReadFrom(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := cm2.Map(k)
		if got != values[i] {
			t.Errorf("after deserialize: Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestSerializeLarge(t *testing.T) {
	n := 100000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i * 7)
	}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	var cm2 ConstMap
	if _, err := cm2.ReadFrom(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := cm2.Map(k)
		if got != values[i] {
			t.Errorf("after deserialize: Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestSerializeCorrupted(t *testing.T) {
	keys := []string{"apple", "banana", "cherry"}
	values := []uint64{10, 20, 30}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the middle of the data.
	data := buf.Bytes()
	data[len(data)/2] ^= 0xff

	var cm2 ConstMap
	if _, err := cm2.ReadFrom(bytes.NewReader(data)); err == nil {
		t.Error("expected error for corrupted data, got nil")
	}
}

func TestSaveLoadFile(t *testing.T) {
	keys := []string{"one", "two", "three"}
	values := []uint64{1, 2, 3}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "test.cmap")
	if err := cm.SaveToFile(path); err != nil {
		t.Fatal(err)
	}

	cm2, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := cm2.Map(k)
		if got != values[i] {
			t.Errorf("after load: Map(%q) = %d, want %d", k, got, values[i])
		}
	}

	// Verify the file exists and has nonzero size.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Error("saved file is empty")
	}
}

func TestSerializeEmpty(t *testing.T) {
	cm, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	var cm2 ConstMap
	if _, err := cm2.ReadFrom(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}

	if len(cm2.data) != 0 {
		t.Errorf("expected empty data, got %d elements", len(cm2.data))
	}
}

func TestVerifiedBasic(t *testing.T) {
	keys := []string{"apple", "banana", "cherry", "date", "elderberry"}
	values := []uint64{100, 200, 300, 400, 500}

	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := vm.Map(k)
		if got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
}

func TestVerifiedMissing(t *testing.T) {
	keys := []string{"apple", "banana", "cherry"}
	values := []uint64{100, 200, 300}

	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	// Keys not in the set should return NotFound.
	missing := []string{"grape", "kiwi", "mango", "pear", "plum"}
	for _, k := range missing {
		got := vm.Map(k)
		if got != NotFound {
			t.Errorf("Map(%q) = %d, want NotFound", k, got)
		}
	}
}

func TestVerifiedLarge(t *testing.T) {
	n := 100000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := 0; i < n; i++ {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i * 7)
	}

	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		got := vm.Map(k)
		if got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}

	// Check that missing keys return NotFound.
	for i := 0; i < 10000; i++ {
		k := fmt.Sprintf("missing-%d", i)
		got := vm.Map(k)
		if got != NotFound {
			t.Errorf("Map(%q) = %d, want NotFound", k, got)
		}
	}
}

func TestVerifiedEmpty(t *testing.T) {
	vm, err := NewVerified(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := vm.Map("anything")
	if got != NotFound {
		t.Errorf("empty map: Map(\"anything\") = %d, want NotFound", got)
	}
}

func TestVerifiedMemoryUsage(t *testing.T) {
	n := benchN
	keys, values := makeBenchData(n)

	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	verifiedBytes := uint64(cap(vm.data)+cap(vm.checks)) * 8
	runtime.KeepAlive(vm)

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	constMapBytes := uint64(cap(cm.data)) * 8
	runtime.KeepAlive(cm)

	t.Logf("n = %d", n)
	t.Logf("ConstMap:         %d bytes (%.1f bytes/key)", constMapBytes, float64(constMapBytes)/float64(n))
	t.Logf("VerifiedConstMap: %d bytes (%.1f bytes/key)", verifiedBytes, float64(verifiedBytes)/float64(n))
	t.Logf("Ratio:            VerifiedConstMap is %.1fx the size of ConstMap", float64(verifiedBytes)/float64(constMapBytes))
}
