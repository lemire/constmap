package constmap

import (
	"fmt"
	"math/rand"
	"testing"
)

// batchSizes covers empty, every offset within a block, exact multiples, and
// sizes with a tail, since the blocked loop and its tail are separate code.
var batchSizes = []int{
	0, 1, 2, 3, 4, 5, 6, 7,
	batchBlock, batchBlock + 1, batchBlock + 7,
	2 * batchBlock, 2*batchBlock + 3,
	1000, 4095, 4096, 4097,
}

func batchTestData(n int) ([]string, []uint64) {
	keys := make([]string, n)
	values := make([]uint64, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
		values[i] = uint64(i)*0x9E3779B97F4A7C15 + 11
	}
	return keys, values
}

// TestMapManyMatchesMap is the contract: a batch must agree with the loop it
// replaces, at every size, including the tail past the last full block.
func TestMapManyMatchesMap(t *testing.T) {
	keys, values := batchTestData(20000)
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(7))
	for _, n := range batchSizes {
		query := make([]string, n)
		for i := range query {
			query[i] = keys[rng.Intn(len(keys))]
		}

		got := cm.MapMany(query)
		if len(got) != n {
			t.Fatalf("n=%d: MapMany returned %d values", n, len(got))
		}
		for i, k := range query {
			if want := cm.Map(k); got[i] != want {
				t.Fatalf("n=%d: MapMany[%d] = %d, Map(%q) = %d", n, i, got[i], k, want)
			}
		}
	}
}

// TestMapManyValues checks the values themselves, not just agreement with Map.
func TestMapManyValues(t *testing.T) {
	keys, values := batchTestData(5000)
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	got := cm.MapMany(keys)
	for i := range keys {
		if got[i] != values[i] {
			t.Fatalf("MapMany[%d] = %d, want %d", i, got[i], values[i])
		}
	}
}

func TestVerifiedMapManyMatchesMap(t *testing.T) {
	keys, values := batchTestData(20000)
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(11))
	for _, n := range batchSizes {
		// Mix present and absent keys, so both branches of the fingerprint
		// check are exercised inside a block and in the tail.
		query := make([]string, n)
		for i := range query {
			if rng.Intn(2) == 0 {
				query[i] = keys[rng.Intn(len(keys))]
			} else {
				query[i] = fmt.Sprintf("absent-%d", rng.Int())
			}
		}

		got := vm.MapMany(query)
		if len(got) != n {
			t.Fatalf("n=%d: MapMany returned %d values", n, len(got))
		}
		for i, k := range query {
			if want := vm.Map(k); got[i] != want {
				t.Fatalf("n=%d: MapMany[%d] = %d, Map(%q) = %d", n, i, got[i], k, want)
			}
		}
	}
}

// TestVerifiedMapManyReportsMissing checks that absent keys come back as
// NotFound and present ones do not, which is the whole point of the verified
// variant.
func TestVerifiedMapManyReportsMissing(t *testing.T) {
	keys, values := batchTestData(5000)
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	got := vm.MapMany(keys)
	for i := range keys {
		if got[i] != values[i] {
			t.Fatalf("present key %d: got %d, want %d", i, got[i], values[i])
		}
	}

	absent := make([]string, 5000)
	for i := range absent {
		absent[i] = fmt.Sprintf("missing-%d", i)
	}
	got = vm.MapMany(absent)
	for i := range absent {
		if got[i] != NotFound {
			t.Fatalf("absent key %d: got %d, want NotFound", i, got[i])
		}
	}
}

func TestVerifiedMapManyEmptyMap(t *testing.T) {
	vm, err := NewVerified(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := vm.MapMany([]string{"a", "b", "c"})
	for i, v := range got {
		if v != NotFound {
			t.Errorf("empty map: result[%d] = %d, want NotFound", i, v)
		}
	}
}

func TestMapManyNoKeys(t *testing.T) {
	keys, values := batchTestData(100)
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	if got := cm.MapMany(nil); len(got) != 0 {
		t.Errorf("ConstMap.MapMany(nil) returned %d values", len(got))
	}
	if got := vm.MapMany([]string{}); len(got) != 0 {
		t.Errorf("VerifiedConstMap.MapMany(empty) returned %d values", len(got))
	}
}

// TestMapManyIntoLeavesTailAlone checks that a reused buffer longer than the
// query is written only where it should be.
func TestMapManyIntoLeavesTailAlone(t *testing.T) {
	keys, values := batchTestData(1000)
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	const sentinel = 0xABCDEF
	dst := make([]uint64, 64)
	for i := range dst {
		dst[i] = sentinel
	}
	query := keys[:10]
	cm.MapManyInto(dst, query)

	for i := range query {
		if dst[i] != values[i] {
			t.Errorf("dst[%d] = %d, want %d", i, dst[i], values[i])
		}
	}
	for i := len(query); i < len(dst); i++ {
		if dst[i] != sentinel {
			t.Errorf("dst[%d] was overwritten: %d", i, dst[i])
		}
	}
}

func TestMapManyIntoShortDestination(t *testing.T) {
	keys, values := batchTestData(100)
	cm, err := New(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"ConstMap", "VerifiedConstMap"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s.MapManyInto with a short destination did not panic", name)
				}
			}()
			dst := make([]uint64, 3)
			if name == "ConstMap" {
				cm.MapManyInto(dst, keys[:10])
			} else {
				vm.MapManyInto(dst, keys[:10])
			}
		}()
	}
}

// --- benchmarks ---

const batchQueries = 4096

var batchSink uint64

func batchBenchSetup(b *testing.B) ([]string, []uint64, []string) {
	b.Helper()
	keys, values := makeBenchData(benchN)
	rng := rand.New(rand.NewSource(1))
	query := make([]string, batchQueries)
	for i := range query {
		query[i] = keys[rng.Intn(benchN)]
	}
	return keys, values, query
}

func BenchmarkConstMapLoop(b *testing.B) {
	keys, values, query := batchBenchSetup(b)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	dst := make([]uint64, len(query))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range query {
			dst[j] = cm.Map(k)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(query)), "ns/key")
	batchSink = dst[0]
}

func BenchmarkConstMapMany(b *testing.B) {
	keys, values, query := batchBenchSetup(b)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	dst := make([]uint64, len(query))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.MapManyInto(dst, query)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(query)), "ns/key")
	batchSink = dst[0]
}

func BenchmarkVerifiedConstMapLoop(b *testing.B) {
	keys, values, query := batchBenchSetup(b)
	vm, err := NewVerified(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	dst := make([]uint64, len(query))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range query {
			dst[j] = vm.Map(k)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(query)), "ns/key")
	batchSink = dst[0]
}

func BenchmarkVerifiedConstMapMany(b *testing.B) {
	keys, values, query := batchBenchSetup(b)
	vm, err := NewVerified(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	dst := make([]uint64, len(query))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm.MapManyInto(dst, query)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(query)), "ns/key")
	batchSink = dst[0]
}
