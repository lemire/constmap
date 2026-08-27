package constmap

import (
	"math/rand"
	"strings"
	"testing"
)

const (
	batchMapN     = 1_000_000
	batchSize     = 2000
	numBatchPools = 64 // rotate through many distinct random batches to avoid artificial cache warmth
)

// makeQueryBatches builds numBatchPools independent random samples of batchSize
// keys drawn from allKeys, so repeated benchmark iterations don't just replay
// the exact same 2000 keys (which would go cache-resident and understate the
// cost of a genuinely fresh batch against a 9MB+ table).
// The keys are cloned rather than aliased. allKeys holds its string bodies in
// index order, so copying the headers out of it would leave the bodies
// scattered and charge every lookup for a random walk over this file's own key
// text -- about 1.6 to 2.6 ns per key here, and far more with larger pools.
// That cost belongs to whatever produced the keys, not to the map. Cloning in
// draw order gives each pool the compact layout a caller holding a batch of
// keys would have. See makeQueryOrder in constmap_test.go.
func makeQueryBatches(allKeys []string, seed int64, pools int) [][]string {
	rng := rand.New(rand.NewSource(seed))
	batches := make([][]string, pools)
	for p := 0; p < pools; p++ {
		perm := rng.Perm(len(allKeys))[:batchSize]
		b := make([]string, batchSize)
		for i, j := range perm {
			b[i] = strings.Clone(allKeys[j])
		}
		batches[p] = b
	}
	return batches
}

var batchSink uint64

// The four benchmarks below pair a cold and a hot regime for each map type.
// Cold rotates through numBatchPools distinct random batches, so the touched
// cache lines are not already resident and the measurement is dominated by
// memory latency. Hot replays one batch, so the touched region goes
// cache-resident and hashing dominates instead. Batching helps in both, but
// for different reasons, and a single regime would hide one of them.

func benchBatchSetup(b *testing.B) (*ConstMap, *VerifiedConstMap, [][]string, []uint64) {
	b.Helper()
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	vm, err := NewVerified(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	return cm, vm, makeQueryBatches(keys, 1, numBatchPools), make([]uint64, batchSize)
}

func BenchmarkBatchNaive_Cold(b *testing.B) {
	cm, _, batches, out := benchBatchSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range batches[i%numBatchPools] {
			out[j] = cm.Map(k)
		}
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkBatchNaive_Hot(b *testing.B) {
	cm, _, batches, out := benchBatchSetup(b)
	q := batches[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range q {
			out[j] = cm.Map(k)
		}
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkMapMany_Cold(b *testing.B) {
	cm, _, batches, out := benchBatchSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.MapManyInto(out, batches[i%numBatchPools])
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkMapMany_Hot(b *testing.B) {
	cm, _, batches, out := benchBatchSetup(b)
	q := batches[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.MapManyInto(out, q)
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkVerifiedBatchNaive_Cold(b *testing.B) {
	_, vm, batches, out := benchBatchSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range batches[i%numBatchPools] {
			out[j] = vm.Map(k)
		}
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkVerifiedBatchNaive_Hot(b *testing.B) {
	_, vm, batches, out := benchBatchSetup(b)
	q := batches[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range q {
			out[j] = vm.Map(k)
		}
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkVerifiedMapMany_Cold(b *testing.B) {
	_, vm, batches, out := benchBatchSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm.MapManyInto(out, batches[i%numBatchPools])
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func BenchmarkVerifiedMapMany_Hot(b *testing.B) {
	_, vm, batches, out := benchBatchSetup(b)
	q := batches[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm.MapManyInto(out, q)
	}
	reportNsPerKey(b)
	batchSink = out[0]
}

func reportNsPerKey(b *testing.B) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batchSize), "ns/key")
}
