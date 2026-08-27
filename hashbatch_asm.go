//go:build (amd64 || arm64) && !purego

package constmap

// xxhPrimesBatch holds the XXH64 prime constants in a contiguous array so the
// assembly can load all five in a few instructions: two LDPs plus a MOVD on
// arm64, five MOVQs on amd64. Values match github.com/cespare/xxhash/v2's own (unexported, so not
// safe to reference directly from our assembly) prime table; declared here
// so hashKeysXXH64Short has no dependency on that package's internal layout.
var xxhPrimesBatch = [5]uint64{
	11400714785074694791,
	14029467366897019727,
	1609587929392839161,
	9650029242287828579,
	2870177450012600261,
}

// hashKeysXXH64Short computes the XXH64 (zero seed) digest of each keys[i]
// into out[i]. Every key must be shorter than 32 bytes; behavior is
// undefined (a wrong hash, not a crash) for any key that isn't — callers
// must check lengths first. See hashbatch_arm64.s and hashbatch_amd64.s for why this exists: it
// amortizes the fixed per-call cost (register setup, loading the five xxHash
// primes) across a whole batch instead of paying it once per key the way
// calling github.com/cespare/xxhash/v2.Sum64String per key does.
//
// out must have length >= len(keys).
//
//go:noescape
func hashKeysXXH64Short(keys []string, out []uint64)

// hashManyShort tries to hash every key in keys into out using the batched
// assembly path. It returns false (leaving out untouched) if any key is 32
// bytes or longer, since hashKeysXXH64Short's fast tail cascade only covers
// inputs under 32 bytes; callers should fall back to per-key hashing in
// that case.
func hashManyShort(keys []string, out []uint64) bool {
	for _, k := range keys {
		if len(k) >= 32 {
			return false
		}
	}
	hashKeysXXH64Short(keys, out)
	return true
}
