//go:build (amd64 || arm64) && !purego

package constmap

// gatherPaired resolves one block of positions against slots, exactly as
// gatherPairedGeneric does, but reads each 16-byte slot as one vector load
// (SSE2 on amd64, NEON on arm64) and XORs the three slots with two vector
// instructions, so a block costs three loads and two XORs per key instead of
// six and four. Being assembly it cannot be inlined, which is why it takes a
// whole block rather than one key: the call is paid once per batchBlock keys.
//
// It does no bounds checking. Every position must satisfy 2*h+1 < len(slots),
// which holds for positions derived from the map's own parameters; ReadFrom
// checks that a deserialized map's parameters agree with its slot count so
// that a corrupt file cannot violate it.
//
//go:noescape
func gatherPaired(slots []uint64, h0, h1, h2 *[batchBlock]uint32, hashes *[batchBlock]uint64, out *[batchBlock]uint64)
