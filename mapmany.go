package constmap

import "github.com/cespare/xxhash/v2"

// batchBlock is how many keys the batched lookups hash before gathering any
// values. Hashing a whole block first lets the array accesses of every key in
// the block be in flight at once, instead of each key's loads waiting behind
// the hashing of the one before it. That memory-level parallelism is the only
// reason a batch beats a loop over Map.
//
// Eight was the best or tied-best of 4, 8, 16, 32 and 64 on an Apple M4 Max
// and an Intel Xeon Gold 6548N, for maps that fit in last-level cache and for
// maps several times larger than it. It is also the granularity at which a
// block is handed to hashManyShort, so it bounds how much work one long key
// costs: a key of 32 bytes or more drops its whole block back to per-key
// hashing, and no more than that.
const batchBlock = 8

// MapMany looks up every key in keys and returns the values in a newly
// allocated slice of the same length, where result[i] corresponds to keys[i].
//
// It is equivalent to calling Map on each key in turn, and carries the same
// caveat: a key that was not in the original set yields an undefined value.
// Use VerifiedConstMap.MapMany if you need missing keys reported.
//
// Batching is faster than the equivalent loop because it overlaps the memory
// accesses of several keys. With a million keys that was 18% on an Apple M4
// Max and 8% on an Intel Xeon Gold 6548N. How much it wins depends on how
// much memory latency there is to hide, so it varies with the machine and
// with how much of the map fits in cache; on a map small enough to sit in a
// large last-level cache it can come out roughly even.
func (cm *ConstMap) MapMany(keys []string) []uint64 {
	dst := make([]uint64, len(keys))
	cm.MapManyInto(dst, keys)
	return dst
}

// MapManyInto is MapMany writing into a caller-provided slice, so that a
// repeated batch need not allocate. It fills dst[:len(keys)] and leaves the
// rest of dst alone.
//
// It panics if dst is shorter than keys.
func (cm *ConstMap) MapManyInto(dst []uint64, keys []string) {
	if len(dst) < len(keys) {
		panic("constmap: MapManyInto destination is shorter than keys")
	}

	var h0, h1, h2 [batchBlock]uint32
	var hashes [batchBlock]uint64

	i := 0
	for ; i+batchBlock <= len(keys); i += batchBlock {
		block := keys[i : i+batchBlock]
		if hashManyShort(block, hashes[:]) {
			for j := range block {
				h0[j], h1[j], h2[j] = cm.getHashFromHash(mixsplit(hashes[j], cm.seed))
			}
		} else {
			for j := range block {
				hash := mixsplit(xxhash.Sum64String(block[j]), cm.seed)
				h0[j], h1[j], h2[j] = cm.getHashFromHash(hash)
			}
		}
		out := dst[i : i+batchBlock]
		for j := range out {
			out[j] = cm.data[h0[j]] ^ cm.data[h1[j]] ^ cm.data[h2[j]]
		}
	}
	// Tail: fewer than batchBlock keys left.
	for ; i < len(keys); i++ {
		dst[i] = cm.Map(keys[i])
	}
}

// MapMany looks up every key in keys and returns the values in a newly
// allocated slice of the same length, where result[i] corresponds to keys[i].
// Keys that were not in the original set yield NotFound, exactly as Map does.
//
// Each lookup touches two arrays rather than one, so there is more memory
// latency for batching to hide: with a million keys it was 24% faster than
// the equivalent loop on an Intel Xeon Gold 6548N, and 11% on an Apple M4
// Max.
func (vm *VerifiedConstMap) MapMany(keys []string) []uint64 {
	dst := make([]uint64, len(keys))
	vm.MapManyInto(dst, keys)
	return dst
}

// MapManyInto is MapMany writing into a caller-provided slice, so that a
// repeated batch need not allocate. It fills dst[:len(keys)] and leaves the
// rest of dst alone.
//
// It panics if dst is shorter than keys.
func (vm *VerifiedConstMap) MapManyInto(dst []uint64, keys []string) {
	if len(dst) < len(keys) {
		panic("constmap: MapManyInto destination is shorter than keys")
	}
	if len(vm.data) == 0 {
		for i := range keys {
			dst[i] = NotFound
		}
		return
	}

	var h0, h1, h2 [batchBlock]uint32
	var hashes [batchBlock]uint64

	i := 0
	for ; i+batchBlock <= len(keys); i += batchBlock {
		block := keys[i : i+batchBlock]
		if hashManyShort(block, hashes[:]) {
			for j := range block {
				hashes[j] = mixsplit(hashes[j], vm.seed)
				h0[j], h1[j], h2[j] = vm.getHashFromHash(hashes[j])
			}
		} else {
			for j := range block {
				hash := mixsplit(xxhash.Sum64String(block[j]), vm.seed)
				hashes[j] = hash
				h0[j], h1[j], h2[j] = vm.getHashFromHash(hash)
			}
		}
		out := dst[i : i+batchBlock]
		for j := range out {
			fp := vm.checks[h0[j]] ^ vm.checks[h1[j]] ^ vm.checks[h2[j]]
			if fp != fingerprint(hashes[j]) {
				out[j] = NotFound
				continue
			}
			out[j] = vm.data[h0[j]] ^ vm.data[h1[j]] ^ vm.data[h2[j]]
		}
	}
	// Tail: fewer than batchBlock keys left.
	for ; i < len(keys); i++ {
		dst[i] = vm.Map(keys[i])
	}
}
