package constmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"

	"github.com/cespare/xxhash/v2"
)

// PairedVerifiedConstMap is a VerifiedConstMap with a different memory layout.
// Where VerifiedConstMap keeps its values and its fingerprints in two separate
// arrays, PairedVerifiedConstMap stores each value next to its fingerprint, so
// the three positions a lookup reads each yield both words from one cache
// line: three lines touched instead of six.
//
// Whether that is a win depends on what you look up. On a present key, Map
// runs about 20% faster than VerifiedConstMap.Map on an Intel Xeon Gold 6548N
// once the map outgrows the cache (about 10% on an Apple M4 Max). On an absent
// key it is slower: VerifiedConstMap.Map reads only the fingerprint array,
// which is half the size of the whole map and so far more likely to be
// resident in cache, whereas this map always brings the value in alongside
// the fingerprint.
//
// Semantics and size are exactly those of VerifiedConstMap: the same keys and
// values produce the same seed and the same words, just zipped together. The
// serialized format is its own, and is not interchangeable with
// VerifiedConstMap's.
type PairedVerifiedConstMap struct {
	seed               uint64
	segmentLength      uint32
	segmentLengthMask  uint32
	segmentCount       uint32
	segmentCountLength uint32
	// slots holds the value for position i at slots[2*i] and its fingerprint
	// at slots[2*i+1]. Each pair is 16-byte aligned, so it never straddles a
	// cache line.
	slots []uint64
}

func (pm *PairedVerifiedConstMap) getHashFromHash(hash uint64) (uint32, uint32, uint32) {
	return (&VerifiedConstMap{
		segmentLength:      pm.segmentLength,
		segmentLengthMask:  pm.segmentLengthMask,
		segmentCountLength: pm.segmentCountLength,
	}).getHashFromHash(hash)
}

// checkPairedParameters reports whether segment parameters describe exactly
// slotCount slots, which is what keeps every position a lookup derives from
// them in range: h0 < segmentCount*segmentLength, and h1 and h2 each one
// segment further, so h2 < (segmentCount+2)*segmentLength. That needs
// segmentLength to be a power of two (h1 and h2 are formed by XORing bits
// below it), and segmentCount to be at least one (with zero, h0 is always 0
// and h2 lands in a third segment that does not exist). gatherPaired relies on
// this rather than on bounds checks, so it is enforced wherever a map's
// parameters come from outside NewVerified.
func checkPairedParameters(segmentLength, segmentCount, slotCount uint32) error {
	if slotCount == 0 {
		return nil
	}
	if segmentLength == 0 || segmentLength&(segmentLength-1) != 0 {
		return fmt.Errorf("constmap: segment length %d is not a power of two", segmentLength)
	}
	if segmentCount == 0 {
		return errors.New("constmap: segment count is zero")
	}
	if want := (uint64(segmentCount) + 2) * uint64(segmentLength); want != uint64(slotCount) {
		return fmt.Errorf("constmap: %d slots but the segment parameters describe %d", slotCount, want)
	}
	return nil
}

// NewPaired builds a PairedVerifiedConstMap from a set of string keys and their
// associated uint64 values, under the same rules as NewVerified.
func NewPaired(keys []string, values []uint64) (*PairedVerifiedConstMap, error) {
	vm, err := NewVerified(keys, values)
	if err != nil {
		return nil, err
	}
	return vm.Paired(), nil
}

// Paired returns the same map in the paired layout. The two maps share no
// memory; the result is a copy.
//
// It panics if vm's segment parameters do not describe its array length, as
// can happen for a VerifiedConstMap read from a corrupt file: such a map
// would already panic in its own Map, and the paired map's batched lookups
// trust those parameters without bounds checks.
func (vm *VerifiedConstMap) Paired() *PairedVerifiedConstMap {
	if len(vm.checks) != len(vm.data) {
		panic(fmt.Sprintf("constmap: %d data words but %d check words", len(vm.data), len(vm.checks)))
	}
	if err := checkPairedParameters(vm.segmentLength, vm.segmentCount, uint32(len(vm.data))); err != nil {
		panic(err.Error())
	}
	pm := &PairedVerifiedConstMap{
		seed:               vm.seed,
		segmentLength:      vm.segmentLength,
		segmentLengthMask:  vm.segmentLengthMask,
		segmentCount:       vm.segmentCount,
		segmentCountLength: vm.segmentCountLength,
	}
	if len(vm.data) == 0 {
		return pm
	}
	pm.slots = make([]uint64, 2*len(vm.data))
	for i, v := range vm.data {
		pm.slots[2*i] = v
		pm.slots[2*i+1] = vm.checks[i]
	}
	return pm
}

// Map returns the uint64 value associated with the given key, or NotFound if
// the key was not in the original set, exactly as VerifiedConstMap.Map does.
func (pm *PairedVerifiedConstMap) Map(key string) uint64 {
	if len(pm.slots) == 0 {
		return NotFound
	}
	hash := mixsplit(xxhash.Sum64String(key), pm.seed)
	h0, h1, h2 := pm.getHashFromHash(hash)
	s := pm.slots
	i0, i1, i2 := 2*uint64(h0), 2*uint64(h1), 2*uint64(h2)
	fp := s[i0+1] ^ s[i1+1] ^ s[i2+1]
	if fp != fingerprint(hash) {
		return NotFound
	}
	return s[i0] ^ s[i1] ^ s[i2]
}

// MapMany looks up every key in keys and returns the values in a newly
// allocated slice of the same length, where result[i] corresponds to keys[i].
// Keys that were not in the original set yield NotFound, exactly as Map does.
func (pm *PairedVerifiedConstMap) MapMany(keys []string) []uint64 {
	dst := make([]uint64, len(keys))
	pm.MapManyInto(dst, keys)
	return dst
}

// MapManyInto is MapMany writing into a caller-provided slice, so that a
// repeated batch need not allocate. It fills dst[:len(keys)] and leaves the
// rest of dst alone.
//
// It panics if dst is shorter than keys.
func (pm *PairedVerifiedConstMap) MapManyInto(dst []uint64, keys []string) {
	if len(dst) < len(keys) {
		panic("constmap: MapManyInto destination is shorter than keys")
	}
	if len(pm.slots) == 0 {
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
				hashes[j] = mixsplit(hashes[j], pm.seed)
				h0[j], h1[j], h2[j] = pm.getHashFromHash(hashes[j])
			}
		} else {
			for j := range block {
				hash := mixsplit(xxhash.Sum64String(block[j]), pm.seed)
				hashes[j] = hash
				h0[j], h1[j], h2[j] = pm.getHashFromHash(hash)
			}
		}
		gatherPaired(pm.slots, &h0, &h1, &h2, &hashes, (*[batchBlock]uint64)(dst[i:i+batchBlock]))
	}
	// Tail: fewer than batchBlock keys left.
	for ; i < len(keys); i++ {
		dst[i] = pm.Map(keys[i])
	}
}

// gatherPairedGeneric resolves one block of positions against slots: out[j]
// is the value at positions h0[j], h1[j], h2[j] if their fingerprints match
// hashes[j], and NotFound otherwise. It is the portable version of
// gatherPaired.
func gatherPairedGeneric(slots []uint64, h0, h1, h2 *[batchBlock]uint32, hashes *[batchBlock]uint64, out *[batchBlock]uint64) {
	for j := range out {
		i0, i1, i2 := 2*uint64(h0[j]), 2*uint64(h1[j]), 2*uint64(h2[j])
		fp := slots[i0+1] ^ slots[i1+1] ^ slots[i2+1]
		if fp != fingerprint(hashes[j]) {
			out[j] = NotFound
			continue
		}
		out[j] = slots[i0] ^ slots[i1] ^ slots[i2]
	}
}

// Binary format for PairedVerifiedConstMap (all little-endian):
//   [8] magic "PMAP0001"
//   [8] seed
//   [4] segmentLength
//   [4] segmentCount
//   [4] number of slots
//   [4] zero padding; fastconstmap stores its original key count here, and
//       every reader ignores the field
//   [16*slots] slots, each a value word followed by its fingerprint word
//   [8] FNV-1a 64-bit checksum of all preceding bytes
//
// This is the VerifiedConstMap format with its two arrays zipped together,
// under its own magic so that a file of either kind fed to the other's reader
// is reported rather than silently misinterpreted. The padding keeps the
// slot array on a 64-bit boundary, as in the other two formats.

var pairedMagicBytes = [8]byte{'P', 'M', 'A', 'P', '0', '0', '0', '1'}

// WriteTo serializes the PairedVerifiedConstMap to w in a portable binary
// format. A FNV-1a checksum is appended for integrity verification.
func (pm *PairedVerifiedConstMap) WriteTo(w io.Writer) (int64, error) {
	if len(pm.slots)%2 != 0 {
		return 0, fmt.Errorf("constmap: odd slot array length %d", len(pm.slots))
	}

	h := fnv.New64a()
	mw := io.MultiWriter(w, h)

	var buf [8]byte

	copy(buf[:], pairedMagicBytes[:])
	if _, err := mw.Write(buf[:]); err != nil {
		return 0, err
	}

	binary.LittleEndian.PutUint64(buf[:], pm.seed)
	if _, err := mw.Write(buf[:]); err != nil {
		return 0, err
	}

	binary.LittleEndian.PutUint32(buf[:4], pm.segmentLength)
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	binary.LittleEndian.PutUint32(buf[:4], pm.segmentCount)
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	// Number of slots, i.e. half the length of the array.
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(pm.slots)/2))
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	// Padding, so that the slot array begins on a 64-bit boundary.
	binary.LittleEndian.PutUint32(buf[:4], 0)
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	// The slot array is already in file order: value, fingerprint, value, ...
	if err := writeWords(mw, pm.slots, chunkBuffer(len(pm.slots))); err != nil {
		return 0, err
	}

	// Checksum (written to w only, not fed back into the hash).
	binary.LittleEndian.PutUint64(buf[:], h.Sum64())
	if _, err := w.Write(buf[:]); err != nil {
		return 0, err
	}

	written := int64(verifiedHeaderSize + 8*len(pm.slots) + 8)
	return written, nil
}

// ReadFrom deserializes a PairedVerifiedConstMap from r. It verifies the
// trailing checksum and returns an error if the data is corrupted.
func (pm *PairedVerifiedConstMap) ReadFrom(r io.Reader) (int64, error) {
	h := fnv.New64a()
	tr := io.TeeReader(r, h)

	var buf [8]byte

	if _, err := io.ReadFull(tr, buf[:]); err != nil {
		return 0, fmt.Errorf("constmap: reading magic: %w", err)
	}
	if buf != pairedMagicBytes {
		switch buf {
		case magicBytes:
			return 0, errors.New("constmap: this is a ConstMap file, use LoadFromFile")
		case verifiedMagicBytes:
			return 0, errors.New("constmap: this is a VerifiedConstMap file, use LoadVerifiedFromFile")
		case legacyFastMagicBytes, legacyFastVerifiedMagicBytes:
			return 0, errLegacyFastconstmap
		}
		return 0, errors.New("constmap: invalid magic bytes")
	}

	if _, err := io.ReadFull(tr, buf[:]); err != nil {
		return 0, fmt.Errorf("constmap: reading seed: %w", err)
	}
	pm.seed = binary.LittleEndian.Uint64(buf[:])

	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading segment length: %w", err)
	}
	pm.segmentLength = binary.LittleEndian.Uint32(buf[:4])
	pm.segmentLengthMask = pm.segmentLength - 1

	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading segment count: %w", err)
	}
	pm.segmentCount = binary.LittleEndian.Uint32(buf[:4])
	pm.segmentCountLength = pm.segmentCount * pm.segmentLength

	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading slot count: %w", err)
	}
	slotCount := binary.LittleEndian.Uint32(buf[:4])

	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading padding: %w", err)
	}

	// The checksum below catches accidental corruption; this catches a file
	// that is consistent but not ours, see checkPairedParameters.
	if err := checkPairedParameters(pm.segmentLength, pm.segmentCount, slotCount); err != nil {
		return 0, err
	}

	pm.slots = make([]uint64, 2*int(slotCount))
	if err := readWords(tr, pm.slots, chunkBuffer(len(pm.slots)), "slots"); err != nil {
		return 0, err
	}

	// Checksum: read from r directly (not through tee).
	expectedSum := h.Sum64()
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, fmt.Errorf("constmap: reading checksum: %w", err)
	}
	gotSum := binary.LittleEndian.Uint64(buf[:])
	if gotSum != expectedSum {
		return 0, fmt.Errorf("constmap: checksum mismatch (got %016x, expected %016x)", gotSum, expectedSum)
	}

	read := int64(verifiedHeaderSize + 8*len(pm.slots) + 8)
	return read, nil
}

// SaveToFile serializes the PairedVerifiedConstMap to a file at the given path.
func (pm *PairedVerifiedConstMap) SaveToFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := pm.WriteTo(f); err != nil {
		return err
	}
	return f.Close()
}

// LoadPairedFromFile deserializes a PairedVerifiedConstMap from a file at the
// given path.
func LoadPairedFromFile(path string) (*PairedVerifiedConstMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pm := &PairedVerifiedConstMap{}
	if _, err := pm.ReadFrom(f); err != nil {
		return nil, err
	}
	return pm, nil
}
