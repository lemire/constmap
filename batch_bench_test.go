package constmap

import (
	"math/rand"
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
func makeQueryBatches(allKeys []string, seed int64, pools int) [][]string {
	rng := rand.New(rand.NewSource(seed))
	batches := make([][]string, pools)
	for p := 0; p < pools; p++ {
		perm := rng.Perm(len(allKeys))[:batchSize]
		b := make([]string, batchSize)
		for i, j := range perm {
			b[i] = allKeys[j]
		}
		batches[p] = b
	}
	return batches
}

var batchSink uint64

// BenchmarkBatchNaive_Cold measures batch lookup where each b.N iteration
// queries a different random 2000-key batch, cycling through numBatchPools
// distinct batches. This approximates the real workload: 2000 keys you
// haven't just looked up, against a 1M-entry table that doesn't fit L1/L2.
func BenchmarkBatchNaive_Cold(b *testing.B) {
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	batches := makeQueryBatches(keys, 1, numBatchPools)
	out := make([]uint64, batchSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := batches[i%numBatchPools]
		for j, k := range q {
			out[j] = cm.Map(k)
		}
	}
	var s uint64
	for _, v := range out {
		s += v
	}
	batchSink = s
	b.ReportMetric(float64(batchSize), "keys/batch")
}

// BenchmarkBatchNaive_Hot repeats the SAME 2000-key batch every iteration.
// After the first pass the touched cache lines (~2000*3*cachelinesize) are
// L2-resident, so this measures steady-state/repeated-query performance
// rather than a fresh batch against cold memory.
func BenchmarkBatchNaive_Hot(b *testing.B) {
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	batches := makeQueryBatches(keys, 1, 1)
	q := batches[0]
	out := make([]uint64, batchSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range q {
			out[j] = cm.Map(k)
		}
	}
	var s uint64
	for _, v := range out {
		s += v
	}
	batchSink = s
	b.ReportMetric(float64(batchSize), "keys/batch")
}

func BenchmarkMapBatch_Cold(b *testing.B) {
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	batches := makeQueryBatches(keys, 1, numBatchPools)
	out := make([]uint64, batchSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := batches[i%numBatchPools]
		cm.MapBatch(q, out)
	}
	var s uint64
	for _, v := range out {
		s += v
	}
	batchSink = s
	b.ReportMetric(float64(batchSize), "keys/batch")
}

func BenchmarkMapBatch_Hot(b *testing.B) {
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	batches := makeQueryBatches(keys, 1, 1)
	q := batches[0]
	out := make([]uint64, batchSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.MapBatch(q, out)
	}
	var s uint64
	for _, v := range out {
		s += v
	}
	batchSink = s
	b.ReportMetric(float64(batchSize), "keys/batch")
}

func BenchmarkMapBatchParallel_Cold(b *testing.B) {
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	batches := makeQueryBatches(keys, 1, numBatchPools)
	out := make([]uint64, batchSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := batches[i%numBatchPools]
		cm.MapBatchParallel(q, out)
	}
	var s uint64
	for _, v := range out {
		s += v
	}
	batchSink = s
	b.ReportMetric(float64(batchSize), "keys/batch")
}

func BenchmarkMapBatchParallel_Hot(b *testing.B) {
	keys, values := makeBenchData(batchMapN)
	cm, err := New(keys, values)
	if err != nil {
		b.Fatal(err)
	}
	batches := makeQueryBatches(keys, 1, 1)
	q := batches[0]
	out := make([]uint64, batchSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.MapBatchParallel(q, out)
	}
	var s uint64
	for _, v := range out {
		s += v
	}
	batchSink = s
	b.ReportMetric(float64(batchSize), "keys/batch")
}
