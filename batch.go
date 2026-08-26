package constmap

import (
	"runtime"
	"sync"
	"unsafe"

	"github.com/cespare/xxhash/v2"
)

// batchChunk is the chunk size MapBatch uses to (a) separate hash
// computation from the memory gather within a chunk and (b) batch calls
// into hashManyShort. It trades two effects against each other:
//
//   - Bigger chunks amortize hashManyShort's fixed per-call cost (loading
//     the five xxHash primes into registers) over more keys.
//   - Smaller chunks interleave hashing and gathering more finely across
//     chunk boundaries, which matters for cache-cold batches: the CPU's
//     out-of-order window can start a later chunk's independent gather
//     loads while an earlier chunk is still stalled on a cache miss, and a
//     single large opaque asm call for a whole chunk's hashing acts as more
//     of a scheduling wall against that overlap than several small ones.
//
// 8 was chosen empirically on an Apple M1 Max as a sweet spot: it beat both
// larger chunks (256, which amortizes hashing best but hurt cold-batch
// latency-hiding) and smaller ones (4, too little amortization) on both a
// cache-cold and a cache-hot 2,000-key batch against a 1M-entry map. Index
// buffers are stack arrays sized to this constant, so it also bounds
// per-call stack use.
const batchChunk = 8

// MapBatch resolves many keys at once. For each chunk of keys it first
// computes all three array positions — hashing the whole chunk in one call
// to hashManyShort where possible (see hashbatch_arm64.s), which loads the
// xxHash prime constants into registers once per chunk instead of once per
// key — then gathers cm.data[h0]^cm.data[h1]^cm.data[h2] for the whole chunk
// in a tight loop with no hashing in between. Because those gather loads are
// mutually independent, separating them from the hash computation lets the
// CPU keep many outstanding L2/DRAM requests in flight at once instead of
// serializing hash-then-load-then-load-then-load on every single key.
//
// out must have length >= len(keys); out[i] receives the value for keys[i].
// As with Map, looking up a key that was not in the original set returns an
// undefined value.
func (cm *ConstMap) MapBatch(keys []string, out []uint64) {
	mapBatchRange(cm, keys, out)
}

// mapBatchRange runs the chunked, phase-separated lookup over keys[0:len(keys)]
// into out[0:len(keys)]. Factored out so MapBatchParallel can run it
// concurrently over disjoint slices.
func mapBatchRange(cm *ConstMap, keys []string, out []uint64) {
	n := len(keys)
	if n == 0 {
		return
	}
	if len(out) < n {
		panic("constmap: out is shorter than keys")
	}

	// h0/h1/h2 (from getHashFromHash) are always < len(cm.data) by
	// construction, the same invariant Map relies on. The gather loop
	// below exploits that to use raw pointer arithmetic instead of
	// bounds-checked slice indexing, which matters here: each removed
	// compare+branch is instruction-issue pressure competing with the
	// loads themselves for the CPU's out-of-order window, and this loop
	// is executed 3 times per key.
	seed := cm.seed
	dataBase := unsafe.Pointer(unsafe.SliceData(cm.data))
	outBase := unsafe.Pointer(unsafe.SliceData(out))
	var h0s, h1s, h2s [batchChunk]uint32
	var hashes [batchChunk]uint64

	for start := 0; start < n; start += batchChunk {
		end := start + batchChunk
		if end > n {
			end = n
		}
		m := end - start
		chunkKeys := keys[start:end]

		// hashManyShort hashes the whole chunk with the five xxHash
		// primes loaded into registers once instead of once per key
		// (see hashbatch_arm64.s), but only covers keys under 32
		// bytes; it reports false without touching hashes if any key
		// in the chunk is longer, and we fall back to per-key hashing
		// for that chunk.
		if hashManyShort(chunkKeys, hashes[:m]) {
			for i := 0; i < m; i++ {
				hash := mixsplit(hashes[i], seed)
				h0, h1, h2 := cm.getHashFromHash(hash)
				h0s[i], h1s[i], h2s[i] = h0, h1, h2
			}
		} else {
			for i := 0; i < m; i++ {
				hash := mixsplit(xxhash.Sum64String(chunkKeys[i]), seed)
				h0, h1, h2 := cm.getHashFromHash(hash)
				h0s[i], h1s[i], h2s[i] = h0, h1, h2
			}
		}
		for i := 0; i < m; i++ {
			v0 := *(*uint64)(unsafe.Add(dataBase, uintptr(h0s[i])*8))
			v1 := *(*uint64)(unsafe.Add(dataBase, uintptr(h1s[i])*8))
			v2 := *(*uint64)(unsafe.Add(dataBase, uintptr(h2s[i])*8))
			*(*uint64)(unsafe.Add(outBase, uintptr(start+i)*8)) = v0 ^ v1 ^ v2
		}
	}
}

// maxBatchWorkers caps how many goroutines MapBatchParallel will use,
// regardless of GOMAXPROCS. On Apple M1/M2/M4 Max, cores past the 8
// performance cores are efficiency cores that are much slower and share
// less memory bandwidth per core; capping avoids handing them an equal
// share of the work.
var maxBatchWorkers = runtime.GOMAXPROCS(0)

// parallelBatchThreshold is the minimum batch size at which MapBatchParallel
// bothers sharding across goroutines. Measured on an Apple M1 Max: fanning
// out goroutines costs on the order of several microseconds (scheduling,
// waking parked P-cores, sync.WaitGroup), while MapBatch alone resolves a
// 2,000-key batch in the tens of microseconds. Below this threshold that
// fixed cost isn't recovered by the parallel speedup, so MapBatchParallel
// just calls MapBatch directly.
const parallelBatchThreshold = 20_000

// MapBatchParallel resolves many keys at once like MapBatch, but for large
// batches shards the work across multiple goroutines (up to maxBatchWorkers)
// so independent keys' hashing and memory gathers run on separate cores
// concurrently. For batches smaller than parallelBatchThreshold it falls
// back to the sequential MapBatch, since goroutine fan-out/fan-in overhead
// would outweigh the benefit.
//
// out must have length >= len(keys).
func (cm *ConstMap) MapBatchParallel(keys []string, out []uint64) {
	n := len(keys)
	if n == 0 {
		return
	}
	if n < parallelBatchThreshold {
		mapBatchRange(cm, keys, out)
		return
	}

	workers := maxBatchWorkers
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		mapBatchRange(cm, keys, out)
		return
	}

	share := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < n; start += share {
		end := start + share
		if end > n {
			end = n
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			mapBatchRange(cm, keys[start:end], out[start:end])
		}(start, end)
	}
	wg.Wait()
}
