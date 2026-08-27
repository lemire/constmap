//go:build (!amd64 && !arm64) || purego

package constmap

// hashManyShort has no batched-assembly fast path in this build, so
// it always reports that it didn't handle the batch; callers fall back to
// per-key hashing.
func hashManyShort(keys []string, out []uint64) bool {
	return false
}
