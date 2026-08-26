//go:build !arm64

package constmap

// hashManyShort has no batched-assembly fast path on this architecture, so
// it always reports that it didn't handle the batch; callers fall back to
// per-key hashing.
func hashManyShort(keys []string, out []uint64) bool {
	return false
}
