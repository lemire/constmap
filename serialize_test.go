package constmap

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"testing/iotest"
)

// chunkTestSizes straddles the ioChunkWords boundary, where the chunked
// encode/decode loops in WriteTo and ReadFrom are easiest to get wrong.
var chunkTestSizes = []int{
	0, 1, 2,
	ioChunkWords - 1, ioChunkWords, ioChunkWords + 1,
	2*ioChunkWords - 1, 2 * ioChunkWords, 2*ioChunkWords + 1,
	3*ioChunkWords + 7,
}

// synthetic builds a ConstMap with a data array of exactly n words. The
// contents are not a valid filter, but serialization does not care and this
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
			if want := int64(headerSize + 8*n + 8); written != want {
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

// TestReadFromTruncated checks that a file cut short inside the data array is
// reported rather than silently accepted.
func TestReadFromTruncated(t *testing.T) {
	cm := synthetic(ioChunkWords + 100)

	var buf bytes.Buffer
	if _, err := cm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	for _, cut := range []int{headerSize, headerSize + 8, headerSize + 8*ioChunkWords, len(raw) - 9, len(raw) - 1} {
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
	for _, limit := range []int{0, 8, headerSize, headerSize + 8*ioChunkWords - 1} {
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
