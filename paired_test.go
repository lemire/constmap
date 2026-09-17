package constmap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

func TestPairedBasic(t *testing.T) {
	keys := []string{"apple", "banana", "cherry", "date", "elderberry"}
	values := []uint64{100, 200, 300, 400, 500}

	pm, err := NewPaired(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for i, k := range keys {
		if got := pm.Map(k); got != values[i] {
			t.Errorf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
	for _, k := range []string{"grape", "kiwi", "mango", "pear", "plum"} {
		if got := pm.Map(k); got != NotFound {
			t.Errorf("Map(%q) = %d, want NotFound", k, got)
		}
	}
}

func TestPairedEmpty(t *testing.T) {
	pm, err := NewPaired(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := pm.Map("anything"); got != NotFound {
		t.Errorf("empty map: Map(\"anything\") = %d, want NotFound", got)
	}
	if got := pm.MapMany([]string{"a", "b", "c"}); len(got) != 3 || got[0] != NotFound || got[2] != NotFound {
		t.Errorf("empty map: MapMany = %v, want all NotFound", got)
	}
}

// TestPairedMatchesVerified checks the defining property of the paired map:
// it is the verified map's two arrays zipped together, so the two agree on
// every key, present or absent, and use the same number of words.
func TestPairedMatchesVerified(t *testing.T) {
	n := 100000
	keys := make([]string, n)
	values := make([]uint64, n)
	rng := rand.New(rand.NewSource(3))
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = rng.Uint64()
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	pm := vm.Paired()

	if len(pm.slots) != 2*len(vm.data) {
		t.Fatalf("paired map has %d words, verified has %d", len(pm.slots), 2*len(vm.data))
	}
	for i := range vm.data {
		if pm.slots[2*i] != vm.data[i] || pm.slots[2*i+1] != vm.checks[i] {
			t.Fatalf("slot %d = (%x, %x), want (%x, %x)", i, pm.slots[2*i], pm.slots[2*i+1], vm.data[i], vm.checks[i])
		}
	}
	for i, k := range keys {
		if got := pm.Map(k); got != values[i] {
			t.Fatalf("Map(%q) = %d, want %d", k, got, values[i])
		}
	}
	for i := 0; i < 20000; i++ {
		k := fmt.Sprintf("absent-%d", i)
		if got, want := pm.Map(k), vm.Map(k); got != want {
			t.Fatalf("Map(%q) = %d, VerifiedConstMap.Map = %d", k, got, want)
		}
	}
}

func TestPairedMapManyMatchesMap(t *testing.T) {
	keys, values := batchTestData(20000)
	pm, err := NewPaired(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(11))
	for _, n := range batchSizes {
		// Mix present and absent keys, so both outcomes of the fingerprint
		// check are exercised inside a block and in the tail.
		query := make([]string, n)
		for i := range query {
			if rng.Intn(2) == 0 {
				query[i] = keys[rng.Intn(len(keys))]
			} else {
				query[i] = fmt.Sprintf("absent-%d", rng.Int())
			}
		}

		got := pm.MapMany(query)
		if len(got) != n {
			t.Fatalf("n=%d: MapMany returned %d values", n, len(got))
		}
		for i, k := range query {
			if want := pm.Map(k); got[i] != want {
				t.Fatalf("n=%d: MapMany[%d] = %d, Map(%q) = %d", n, i, got[i], k, want)
			}
		}
	}
}

// TestPairedMapManyLongKeys forces the per-key hashing path by making every
// key at least 32 bytes, which the batched hasher declines.
func TestPairedMapManyLongKeys(t *testing.T) {
	n := 5000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("a-rather-long-key-that-exceeds-thirty-two-bytes-%d", i)
		values[i] = uint64(i)
	}
	pm, err := NewPaired(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	query := append(append([]string(nil), keys...), "a-rather-long-key-that-exceeds-thirty-two-bytes-absent")
	got := pm.MapMany(query)
	for i := range keys {
		if got[i] != values[i] {
			t.Fatalf("MapMany[%d] = %d, want %d", i, got[i], values[i])
		}
	}
	if got[n] != NotFound {
		t.Fatalf("MapMany[absent] = %d, want NotFound", got[n])
	}
}

// TestGatherPairedMatchesGeneric compares the block gather in use (assembly
// on amd64 and arm64 unless built with purego) against its portable version
// on random positions, both matching and mismatching fingerprints.
func TestGatherPairedMatchesGeneric(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	slots := make([]uint64, 2*4096)
	for i := range slots {
		slots[i] = rng.Uint64()
	}
	for iter := 0; iter < 2000; iter++ {
		var h0, h1, h2 [batchBlock]uint32
		var hashes [batchBlock]uint64
		for j := range h0 {
			h0[j] = uint32(rng.Intn(4096))
			h1[j] = uint32(rng.Intn(4096))
			h2[j] = uint32(rng.Intn(4096))
			if rng.Intn(2) == 0 {
				// Make the fingerprint match: pick a hash whose fingerprint
				// equals the XOR of the three check words. fingerprint(h) =
				// h ^ h>>32, whose low 32 bits are the low bits of h XORed
				// with the high, so solve for the low half.
				fp := slots[2*h0[j]+1] ^ slots[2*h1[j]+1] ^ slots[2*h2[j]+1]
				hi := fp >> 32
				lo := (fp & 0xFFFFFFFF) ^ hi
				hashes[j] = hi<<32 | lo
			} else {
				hashes[j] = rng.Uint64()
			}
		}
		var got, want [batchBlock]uint64
		gatherPaired(slots, &h0, &h1, &h2, &hashes, &got)
		gatherPairedGeneric(slots, &h0, &h1, &h2, &hashes, &want)
		if got != want {
			t.Fatalf("iteration %d: gatherPaired = %v, generic = %v", iter, got, want)
		}
	}
}

func TestPairedRoundTripLookups(t *testing.T) {
	n := 50000
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i) * 7
	}
	pm, err := NewPaired(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "paired.cmap")
	if err := pm.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPairedFromFile(path)
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

func TestPairedSerializeEmpty(t *testing.T) {
	pm, err := NewPaired(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := pm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	var got PairedVerifiedConstMap
	if _, err := got.ReadFrom(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if len(got.slots) != 0 {
		t.Errorf("expected an empty slot array, got %d words", len(got.slots))
	}
	if v := got.Map("anything"); v != NotFound {
		t.Errorf("Map on an empty map = %d, want NotFound", v)
	}
}

// TestPairedSerializeSameSizeAsVerified checks that the paired file is the
// verified file's size: same header, same number of words, same trailer.
func TestPairedSerializeSameSizeAsVerified(t *testing.T) {
	keys, values := batchTestData(1000)
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	var vbuf, pbuf bytes.Buffer
	vn, err := vm.WriteTo(&vbuf)
	if err != nil {
		t.Fatal(err)
	}
	pn, err := vm.Paired().WriteTo(&pbuf)
	if err != nil {
		t.Fatal(err)
	}
	if vn != pn || vbuf.Len() != pbuf.Len() || int(pn) != pbuf.Len() {
		t.Fatalf("verified wrote %d bytes (%d reported), paired wrote %d (%d reported)", vbuf.Len(), vn, pbuf.Len(), pn)
	}
}

// TestPairedSerializeCorrupted checks that a well-formed file with a flipped
// byte is rejected by the checksum, not by anything earlier: the map's
// parameters are consistent, so the reader gets as far as comparing sums.
func TestPairedSerializeCorrupted(t *testing.T) {
	pm := syntheticPaired(14)

	var buf bytes.Buffer
	if _, err := pm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	var intact PairedVerifiedConstMap
	if _, err := intact.ReadFrom(bytes.NewReader(raw)); err != nil {
		t.Fatalf("the intact file must read back: %v", err)
	}

	// Flip a value byte, a fingerprint byte, and a byte of the checksum
	// itself; all three must be reported as checksum mismatches.
	for _, at := range []int{verifiedHeaderSize + 40, verifiedHeaderSize + 48, len(raw) - 3} {
		corrupt := append([]byte(nil), raw...)
		corrupt[at] ^= 0xff
		var got PairedVerifiedConstMap
		_, err := got.ReadFrom(bytes.NewReader(corrupt))
		if err == nil {
			t.Errorf("flipping byte %d: expected a checksum error, got nil", at)
		} else if !strings.Contains(err.Error(), "checksum") {
			t.Errorf("flipping byte %d: expected a checksum error, got %v", at, err)
		}
	}
}

// TestPairedReadFromRejectsInconsistentParameters checks that a file whose
// segment parameters do not describe its slot count is refused, since the
// assembly gather trusts those parameters to keep every position in range.
func TestPairedReadFromRejectsInconsistentParameters(t *testing.T) {
	pm := syntheticPaired(14)

	var buf bytes.Buffer
	if _, err := pm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	// Each case rewrites the header and then the checksum, so that only the
	// consistency check can catch it.
	cases := []struct {
		name          string
		segmentLength uint32
		segmentCount  uint32
		want          string
	}{
		{"doubled segment count", 64, 28, "segment parameters"},
		{"segment length not a power of two", 3, 14, "power of two"},
		// 16 segments of 64 describe the same 1024 slots, but h0 is then
		// always 0 and h2 lands in a third segment past the end.
		{"zero segment count", 512, 0, "segment count"},
	}
	for _, c := range cases {
		bad := append([]byte(nil), raw...)
		binary.LittleEndian.PutUint32(bad[16:20], c.segmentLength)
		binary.LittleEndian.PutUint32(bad[20:24], c.segmentCount)
		rewriteChecksum(bad)
		var got PairedVerifiedConstMap
		_, err := got.ReadFrom(bytes.NewReader(bad))
		if err == nil {
			t.Errorf("%s: expected an error, got nil", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: expected an error mentioning %q, got %v", c.name, c.want, err)
		}
	}
}

// TestPairedRejectsInconsistentVerified checks that converting a
// VerifiedConstMap whose parameters do not describe its arrays, as one read
// from a corrupt but checksum-valid file would be, panics rather than
// producing a map whose batched lookups read past the slots.
func TestPairedRejectsInconsistentVerified(t *testing.T) {
	vm := syntheticVerified(1000) // parameters describe 384 words, not 1000
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected Paired() to panic on inconsistent parameters")
		}
	}()
	vm.Paired()
}

func TestPairedWriteToShortWriter(t *testing.T) {
	pm := syntheticPaired(ioChunkWords/64 - 2) // ioChunkWords slots, two chunks of words
	limits := []int{
		0,
		8,
		verifiedHeaderSize,
		verifiedHeaderSize + 8*ioChunkWords - 1,
		verifiedHeaderSize + 8*len(pm.slots) + 4, // inside the checksum
	}
	for _, limit := range limits {
		if _, err := pm.WriteTo(&failWriter{remaining: limit}); err == nil {
			t.Errorf("writer failing after %d bytes: expected an error, got nil", limit)
		}
	}
}

// TestPairedFormatIsDistinct checks that the paired reader refuses the other
// two formats, and that the other two readers refuse a paired file.
func TestPairedFormatIsDistinct(t *testing.T) {
	keys := []string{"apple", "banana", "cherry"}
	values := []uint64{1, 2, 3}

	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	pm := vm.Paired()

	var plain, verified, paired bytes.Buffer
	if _, err := cm.WriteTo(&plain); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.WriteTo(&verified); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.WriteTo(&paired); err != nil {
		t.Fatal(err)
	}

	var gotPaired PairedVerifiedConstMap
	if _, err := gotPaired.ReadFrom(bytes.NewReader(plain.Bytes())); err == nil {
		t.Error("paired reader accepted a ConstMap file")
	}
	if _, err := gotPaired.ReadFrom(bytes.NewReader(verified.Bytes())); err == nil {
		t.Error("paired reader accepted a VerifiedConstMap file")
	}
	var gotPlain ConstMap
	if _, err := gotPlain.ReadFrom(bytes.NewReader(paired.Bytes())); err == nil {
		t.Error("ConstMap reader accepted a paired file")
	}
	var gotVerified VerifiedConstMap
	if _, err := gotVerified.ReadFrom(bytes.NewReader(paired.Bytes())); err == nil {
		t.Error("VerifiedConstMap reader accepted a paired file")
	}
}

// syntheticPaired builds a paired map whose parameters are consistent with
// its array, (segmentCount+2)*64 slots of pseudo-random words, without the
// cost of a real construction. Unlike syntheticVerified, whose parameters
// always describe 384 slots whatever its length, the result passes
// checkPairedParameters, so tests of what happens after that check can use
// it.
func syntheticPaired(segmentCount uint32) *PairedVerifiedConstMap {
	pm := &PairedVerifiedConstMap{
		seed:               0x0123456789abcdef,
		segmentLength:      64,
		segmentLengthMask:  63,
		segmentCount:       segmentCount,
		segmentCountLength: 64 * segmentCount,
		slots:              make([]uint64, 2*64*int(segmentCount+2)),
	}
	for i := range pm.slots {
		pm.slots[i] = uint64(i)*0x9E3779B97F4A7C15 ^ 0xdeadbeef
	}
	return pm
}

// rewriteChecksum recomputes the trailing FNV-1a checksum of a serialized
// map in place, so a test can alter the header and still get past the
// checksum to the check it is targeting.
func rewriteChecksum(raw []byte) {
	h := fnv.New64a()
	h.Write(raw[:len(raw)-8])
	binary.LittleEndian.PutUint64(raw[len(raw)-8:], h.Sum64())
}

func BenchmarkPairedConstMap(b *testing.B) {
	keys, values := makeBenchData(benchN)
	pm, err := NewPaired(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	queries := makeQueryOrder(keys, 1)

	b.ResetTimer()
	var s uint64
	for i := 0; i < b.N; i++ {
		s += pm.Map(queries[i%benchN])
	}
	benchSink = s
}

// The paired batch benchmarks mirror the Verified ones in batch_bench_test.go.

func benchPairedSetup(b *testing.B) (*PairedVerifiedConstMap, [][]string, []uint64) {
	b.Helper()
	keys, values := makeBenchData(batchMapN)
	pm, err := NewPaired(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	return pm, makeQueryBatches(keys, 1, numBatchPools), make([]uint64, batchSize)
}

func BenchmarkPairedBatchNaive_Cold(b *testing.B) {
	pm, batches, out := benchPairedSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range batches[i%numBatchPools] {
			out[j] = pm.Map(k)
		}
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkPairedBatchNaive_Hot(b *testing.B) {
	pm, batches, out := benchPairedSetup(b)
	q := batches[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range q {
			out[j] = pm.Map(k)
		}
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkPairedMapMany_Cold(b *testing.B) {
	pm, batches, out := benchPairedSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pm.MapManyInto(out, batches[i%numBatchPools])
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkPairedMapMany_Hot(b *testing.B) {
	pm, batches, out := benchPairedSetup(b)
	q := batches[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pm.MapManyInto(out, q)
	}
	reportNsPerKey(b)
	batchSink = out[0]
}
