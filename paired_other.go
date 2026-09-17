//go:build (!amd64 && !arm64) || purego

package constmap

// gatherPaired has no assembly version in this build.
func gatherPaired(slots []uint64, h0, h1, h2 *[batchBlock]uint32, hashes *[batchBlock]uint64, out *[batchBlock]uint64) {
	gatherPairedGeneric(slots, h0, h1, h2, hashes, out)
}
