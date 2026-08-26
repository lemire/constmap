package constmap

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

// serializedHeaderSize is the number of bytes preceding the data array:
// magic, seed, segment length, segment count, data length.
const serializedHeaderSize = 8 + 8 + 4 + 4 + 4

// chunkTestSizes straddles the ioChunkWords boundary, where the chunked
// encode and decode loops in WriteTo and ReadFrom are easiest to get wrong.
var chunkTestSizes = []int{
	0, 1, 2,
	ioChunkWords - 1, ioChunkWords, ioChunkWords + 1,
	2*ioChunkWords - 1, 2 * ioChunkWords, 2*ioChunkWords + 1,
	3*ioChunkWords + 7,
}

// synthetic builds a ConstMap with a data array of exactly n words. The
// contents are not a valid filter, but serialization does not care, and this
// is the only way to pin the array to a chosen length.
func synthetic(n int) *ConstMap {
	cm := &ConstMap{
		seed:               0x0123456789abcdef,
		segmentLength:      64,
		segmentLengthMask:  63,
		segmentCount:       4,
		segmentCountLength: 256,
		data:               make([]uint64, n),
	}
	for i := range cm.data {
		cm.data[i] = uint64(i)*0x9E3779B97F4A7C15 ^ 0xdeadbeef
	}
	return cm
}

func assertSameMap(t *testing.T, got, want *ConstMap) {
	t.Helper()
	if got.seed != want.seed || got.segmentLength != want.segmentLength ||
		got.segmentCount != want.segmentCount ||
		got.segmentLengthMask != want.segmentLengthMask ||
		got.segmentCountLength != want.segmentCountLength {
		t.Errorf("header mismatch: got %+v, want %+v", *got, *want)
	}
	if len(got.data) != len(want.data) {
		t.Fatalf("got %d words, want %d", len(got.data), len(want.data))
	}
	for i := range want.data {
		if got.data[i] != want.data[i] {
			t.Fatalf("data[%d] = %d, want %d", i, got.data[i], want.data[i])
		}
	}
}

// TestSerializeChunkBoundaries checks the chunked loops at and around the
// chunk size, including the empty case.
func TestSerializeChunkBoundaries(t *testing.T) {
	for _, n := range chunkTestSizes {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			cm := synthetic(n)

			var buf bytes.Buffer
			written, err := cm.WriteTo(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if want := int64(serializedHeaderSize + 8*n + 8); written != want {
				t.Errorf("WriteTo reported %d bytes, want %d", written, want)
			}
			if int64(buf.Len()) != written {
				t.Errorf("WriteTo reported %d bytes but wrote %d", written, buf.Len())
			}

			var got ConstMap
			read, err := got.ReadFrom(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if read != written {
				t.Errorf("ReadFrom consumed %d bytes, WriteTo produced %d", read, written)
			}
			assertSameMap(t, &got, cm)
		})
	}
}

// TestReadFromShortReads checks that the chunked reader copes with a reader
// that dribbles out one byte at a time, so chunk boundaries never line up
// with read boundaries.
func TestReadFromShortReads(t *testing.T) {
	cm := synthetic(2*ioChunkWords + 13)

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	var got ConstMap
	if _, err := got.ReadFrom(iotest.OneByteReader(bytes.NewReader(buf.Bytes()))); err != nil {
		t.Fatal(err)
	}
	assertSameMap(t, &got, cm)
}

// TestReadFromTruncated checks that a stream cut short inside the data array
// is reported rather than silently accepted.
func TestReadFromTruncated(t *testing.T) {
	cm := synthetic(ioChunkWords + 100)

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	cuts := []int{
		serializedHeaderSize,
		serializedHeaderSize + 8,
		serializedHeaderSize + 8*ioChunkWords,
		len(raw) - 9,
		len(raw) - 1,
	}
	for _, cut := range cuts {
		var got ConstMap
		if _, err := got.ReadFrom(bytes.NewReader(raw[:cut])); err == nil {
			t.Errorf("truncating to %d bytes: expected an error, got nil", cut)
		}
	}
}

// TestWriteToShortWriter checks that a writer failing part way through,
// including in the middle of the data array, surfaces its error rather than
// having it dropped.
func TestWriteToShortWriter(t *testing.T) {
	cm := synthetic(2 * ioChunkWords)
	limits := []int{0, 8, serializedHeaderSize, serializedHeaderSize + 8*ioChunkWords - 1}
	for _, limit := range limits {
		if _, err := cm.WriteTo(&failWriter{remaining: limit}); err == nil {
			t.Errorf("writer failing after %d bytes: expected an error, got nil", limit)
		}
	}
}

// failWriter accepts remaining bytes and then fails.
type failWriter struct{ remaining int }

func (w *failWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, errors.New("failWriter: out of space")
	}
	w.remaining -= len(p)
	return len(p), nil
}

// --- VerifiedConstMap ---

// syntheticVerified builds a VerifiedConstMap with arrays of exactly n words.
func syntheticVerified(n int) *VerifiedConstMap {
	vm := &VerifiedConstMap{
		seed:               0x0123456789abcdef,
		segmentLength:      64,
		segmentLengthMask:  63,
		segmentCount:       4,
		segmentCountLength: 256,
		data:               make([]uint64, n),
		checks:             make([]uint64, n),
	}
	for i := range vm.data {
		vm.data[i] = uint64(i)*0x9E3779B97F4A7C15 ^ 0xdeadbeef
		vm.checks[i] = uint64(i)*0xBF58476D1CE4E5B9 ^ 0xfeedface
	}
	return vm
}

func assertSameVerified(t *testing.T, got, want *VerifiedConstMap) {
	t.Helper()
	if got.seed != want.seed || got.segmentLength != want.segmentLength ||
		got.segmentCount != want.segmentCount ||
		got.segmentLengthMask != want.segmentLengthMask ||
		got.segmentCountLength != want.segmentCountLength {
		t.Error("header mismatch")
	}
	if len(got.data) != len(want.data) || len(got.checks) != len(want.checks) {
		t.Fatalf("got %d/%d words, want %d/%d",
			len(got.data), len(got.checks), len(want.data), len(want.checks))
	}
	for i := range want.data {
		if got.data[i] != want.data[i] {
			t.Fatalf("data[%d] = %d, want %d", i, got.data[i], want.data[i])
		}
		if got.checks[i] != want.checks[i] {
			t.Fatalf("checks[%d] = %d, want %d", i, got.checks[i], want.checks[i])
		}
	}
}

// TestVerifiedSerializeAlignment is the point of the padded header: both
// uint64 arrays must begin on a 64-bit boundary, whatever the map's size, so
// a reader aliasing the bytes never performs a misaligned access.
func TestVerifiedSerializeAlignment(t *testing.T) {
	if verifiedHeaderSize%8 != 0 {
		t.Fatalf("header is %d bytes, not a multiple of 8", verifiedHeaderSize)
	}
	for _, n := range chunkTestSizes {
		vm := syntheticVerified(n)

		var buf bytes.Buffer
		written, err := vm.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(verifiedHeaderSize + 16*n + 8); written != want {
			t.Errorf("n=%d: WriteTo reported %d bytes, want %d", n, written, want)
		}
		if int64(buf.Len()) != written {
			t.Errorf("n=%d: reported %d bytes but wrote %d", n, written, buf.Len())
		}

		dataOffset := verifiedHeaderSize
		checksOffset := dataOffset + 8*n
		if dataOffset%8 != 0 {
			t.Errorf("n=%d: data starts at offset %d, not 64-bit aligned", n, dataOffset)
		}
		if checksOffset%8 != 0 {
			t.Errorf("n=%d: checks start at offset %d, not 64-bit aligned", n, checksOffset)
		}

		// The padding word itself must be zero, so it stays available for a
		// future format revision.
		if pad := binary.LittleEndian.Uint32(buf.Bytes()[28:32]); pad != 0 {
			t.Errorf("n=%d: padding is %#x, want 0", n, pad)
		}
	}
}

// TestVerifiedSerializeChunkBoundaries exercises both arrays across the chunk
// size, since checks is written and read immediately after data.
func TestVerifiedSerializeChunkBoundaries(t *testing.T) {
	for _, n := range chunkTestSizes {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			vm := syntheticVerified(n)

			var buf bytes.Buffer
			written, err := vm.WriteTo(&buf)
			if err != nil {
				t.Fatal(err)
			}

			var got VerifiedConstMap
			read, err := got.ReadFrom(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if read != written {
				t.Errorf("ReadFrom consumed %d bytes, WriteTo produced %d", read, written)
			}
			assertSameVerified(t, &got, vm)
		})
	}
}

// TestVerifiedRoundTripLookups checks that lookups still behave after a round
// trip, including that absent keys still report NotFound.
func TestVerifiedRoundTripLookups(t *testing.T) {
	n := 50000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i) * 7
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "verified.cmap")
	if err := vm.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadVerifiedFromFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		if v := got.Map(k); v != values[i] {
			t.Fatalf("after load: Map(%q) = %d, want %d", k, v, values[i])
		}
	}
	for i := 0; i < 10000; i++ {
		absent := fmt.Sprintf("absent-%d", i)
		if v := got.Map(absent); v != NotFound {
			t.Fatalf("after load: Map(%q) = %d, want NotFound", absent, v)
		}
	}
}

func TestVerifiedSerializeEmpty(t *testing.T) {
	vm, err := NewVerified(nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := vm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	var got VerifiedConstMap
	if _, err := got.ReadFrom(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if len(got.data) != 0 || len(got.checks) != 0 {
		t.Errorf("expected empty arrays, got %d/%d", len(got.data), len(got.checks))
	}
	if v := got.Map("anything"); v != NotFound {
		t.Errorf("Map on an empty map = %d, want NotFound", v)
	}
}

func TestVerifiedReadFromShortReads(t *testing.T) {
	vm := syntheticVerified(ioChunkWords + 13)

	var buf bytes.Buffer
	if _, err := vm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	var got VerifiedConstMap
	if _, err := got.ReadFrom(iotest.OneByteReader(bytes.NewReader(buf.Bytes()))); err != nil {
		t.Fatal(err)
	}
	assertSameVerified(t, &got, vm)
}

func TestVerifiedReadFromTruncated(t *testing.T) {
	vm := syntheticVerified(ioChunkWords + 100)

	var buf bytes.Buffer
	if _, err := vm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	n := len(vm.data)
	cuts := []int{
		verifiedHeaderSize - 1,       // inside the header
		verifiedHeaderSize,           // no arrays at all
		verifiedHeaderSize + 8,       // inside data
		verifiedHeaderSize + 8*n,     // data complete, no checks
		verifiedHeaderSize + 8*n + 8, // inside checks
		len(raw) - 9,                 // checks complete, no checksum
		len(raw) - 1,                 // truncated checksum
	}
	for _, cut := range cuts {
		var got VerifiedConstMap
		if _, err := got.ReadFrom(bytes.NewReader(raw[:cut])); err == nil {
			t.Errorf("truncating to %d bytes: expected an error, got nil", cut)
		}
	}
}

func TestVerifiedSerializeCorrupted(t *testing.T) {
	vm := syntheticVerified(1000)

	var buf bytes.Buffer
	if _, err := vm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	// Flip a byte in data, then one in checks; both must be caught.
	for _, at := range []int{verifiedHeaderSize + 40, verifiedHeaderSize + 8*len(vm.data) + 40} {
		corrupt := append([]byte(nil), raw...)
		corrupt[at] ^= 0xff
		var got VerifiedConstMap
		if _, err := got.ReadFrom(bytes.NewReader(corrupt)); err == nil {
			t.Errorf("flipping byte %d: expected a checksum error, got nil", at)
		}
	}
}

func TestVerifiedWriteToShortWriter(t *testing.T) {
	vm := syntheticVerified(2 * ioChunkWords)
	limits := []int{
		0,
		8,
		verifiedHeaderSize,
		verifiedHeaderSize + 8*ioChunkWords - 1,
		verifiedHeaderSize + 8*len(vm.data) + 8, // inside checks
	}
	for _, limit := range limits {
		if _, err := vm.WriteTo(&failWriter{remaining: limit}); err == nil {
			t.Errorf("writer failing after %d bytes: expected an error, got nil", limit)
		}
	}
}

// TestVerifiedMismatchedArrays checks that a hand-built map with unequal
// arrays is refused rather than writing a file that cannot be read back.
func TestVerifiedMismatchedArrays(t *testing.T) {
	vm := syntheticVerified(10)
	vm.checks = vm.checks[:5]
	if _, err := vm.WriteTo(&bytes.Buffer{}); err == nil {
		t.Error("expected an error for mismatched arrays, got nil")
	}
}

// TestSerializeFormatsAreDistinct checks that handing a file of one kind to
// the other kind's reader is reported rather than misinterpreted.
func TestSerializeFormatsAreDistinct(t *testing.T) {
	dir := t.TempDir()
	keys := []string{"apple", "banana", "cherry"}
	values := []uint64{1, 2, 3}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	plainPath := filepath.Join(dir, "plain.cmap")
	if err := cm.SaveToFile(plainPath); err != nil {
		t.Fatal(err)
	}

	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	verifiedPath := filepath.Join(dir, "verified.cmap")
	if err := vm.SaveToFile(verifiedPath); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadVerifiedFromFile(plainPath); err == nil {
		t.Error("a ConstMap file loaded as a VerifiedConstMap, it must not")
	} else {
		t.Logf("plain as verified: %v", err)
	}
	if _, err := LoadFromFile(verifiedPath); err == nil {
		t.Error("a VerifiedConstMap file loaded as a ConstMap, it must not")
	} else {
		t.Logf("verified as plain: %v", err)
	}
}

// loadSink keeps benchmark work from being optimized away.
var loadSink uint64

// benchSavedFile writes an n-key map to a temporary file.
func benchSavedFile(b *testing.B, n int) string {
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
	return path
}

func BenchmarkLoadFromFile(b *testing.B) {
	path := benchSavedFile(b, benchN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm, err := LoadFromFile(path)
		if err != nil {
			b.Fatal(err)
		}
		loadSink = cm.data[0]
	}
}

// BenchmarkLoadFromFileBuffered guards the claim that callers need not buffer
// the file themselves: now that ReadFrom moves the data array in chunks, a
// bufio.Reader only adds a second copy. It should not beat BenchmarkLoadFromFile.
func BenchmarkLoadFromFileBuffered(b *testing.B) {
	path := benchSavedFile(b, benchN)
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
		loadSink = cm.data[0]
	}
}

func BenchmarkSaveToFile(b *testing.B) {
	keys, values := makeBenchData(benchN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cm.SaveToFile(filepath.Join(dir, "bench.cmap")); err != nil {
			b.Fatal(err)
		}
	}
}

func benchSavedVerifiedFile(b *testing.B, n int) string {
	b.Helper()
	keys, values := makeBenchData(n)
	vm, err := NewVerified(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "bench.vcmap")
	if err := vm.SaveToFile(path); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkLoadVerifiedFromFile(b *testing.B) {
	path := benchSavedVerifiedFile(b, benchN)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm, err := LoadVerifiedFromFile(path)
		if err != nil {
			b.Fatal(err)
		}
		loadSink = vm.data[0]
	}
}

func BenchmarkVerifiedSaveToFile(b *testing.B) {
	keys, values := makeBenchData(benchN)
	vm, err := NewVerified(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := vm.SaveToFile(filepath.Join(dir, "bench.vcmap")); err != nil {
			b.Fatal(err)
		}
	}
}
