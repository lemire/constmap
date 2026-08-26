package constmap

import (
	"testing"

	"github.com/cespare/xxhash/v2"
)

// Controlled back-to-back comparison, run as subtests in one process to
// avoid thermal/boost-state drift between separate `go test -bench`
// invocations (see the per-key hashKey attempt earlier: that comparison run
// across separate invocations gave a false "41% win" that a same-process
// A/B correctly showed was noise).
func BenchmarkHashBatchOnly(b *testing.B) {
	keys, _ := makeBenchData(batchMapN)
	batches := makeQueryBatches(keys, 1, numBatchPools)
	out := make([]uint64, batchSize)

	b.Run("PerKeyCall", func(b *testing.B) {
		var sink uint64
		for i := 0; i < b.N; i++ {
			q := batches[i%numBatchPools]
			for j, k := range q {
				out[j] = xxhash.Sum64String(k)
			}
			sink += out[0]
		}
		batchSink = sink
	})

	b.Run("BatchedAsm", func(b *testing.B) {
		var sink uint64
		for i := 0; i < b.N; i++ {
			q := batches[i%numBatchPools]
			hashKeysXXH64Short(q, out)
			sink += out[0]
		}
		batchSink = sink
	})
}
