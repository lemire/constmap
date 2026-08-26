// Package constmap provides a static map from strings to uint64 values
// using the binary fuse filter construction. Given a set of (key, value) pairs,
// it builds a compact array such that for any key in the set,
// array[h0(key)] XOR array[h1(key)] XOR array[h2(key)] == value.
//
// Lookup is extremely fast: one xxhash call plus three array accesses and two XORs.
// The data structure is immutable after construction.
package constmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/bits"
	"os"

	"github.com/cespare/xxhash/v2"
)

// ConstMap is an immutable map from strings to uint64 values.
type ConstMap struct {
	seed               uint64
	segmentLength      uint32
	segmentLengthMask  uint32
	segmentCount       uint32
	segmentCountLength uint32
	data               []uint64
}

func murmur64(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

func mixsplit(key uint64, seed uint64) uint64 {
	return murmur64(key + seed)
}

func splitmix64(seed *uint64) uint64 {
	*seed += 0x9E3779B97F4A7C15
	z := *seed
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (cm *ConstMap) getHashFromHash(hash uint64) (uint32, uint32, uint32) {
	hi, _ := bits.Mul64(hash, uint64(cm.segmentCountLength))
	h0 := uint32(hi)
	h1 := h0 + cm.segmentLength
	h2 := h1 + cm.segmentLength
	h1 ^= uint32(hash>>18) & cm.segmentLengthMask
	h2 ^= uint32(hash) & cm.segmentLengthMask
	return h0, h1, h2
}

func calculateSegmentLength(size uint32) uint32 {
	if size == 0 {
		return 4
	}
	return uint32(1) << int(math.Floor(math.Log(float64(size))/math.Log(3.33)+2.25))
}

func calculateSizeFactor(size uint32) float64 {
	return math.Max(1.125, 0.875+0.25*math.Log(1000000)/math.Log(float64(size)))
}

func (cm *ConstMap) initializeParameters(size uint32) {
	cm.segmentLength = calculateSegmentLength(size)
	if cm.segmentLength > 262144 {
		cm.segmentLength = 262144
	}
	cm.segmentLengthMask = cm.segmentLength - 1
	capacity := uint32(0)
	if size > 1 {
		sizeFactor := calculateSizeFactor(size)
		capacity = uint32(math.Round(float64(size) * sizeFactor))
	}
	totalSegmentCount := (capacity + cm.segmentLength - 1) / cm.segmentLength
	if totalSegmentCount < 3 {
		totalSegmentCount = 3
	}
	cm.segmentCount = totalSegmentCount - 2
	cm.segmentCountLength = cm.segmentCount * cm.segmentLength
	cm.data = make([]uint64, totalSegmentCount*cm.segmentLength)
}

const maxIterations = 100

// New builds a ConstMap from a set of string keys and their associated uint64 values.
// The two slices must have equal length. Keys must be unique.
func New(keys []string, values []uint64) (*ConstMap, error) {
	if len(keys) != len(values) {
		return nil, errors.New("constmap: keys and values must have equal length")
	}
	n := len(keys)
	if n == 0 {
		return &ConstMap{}, nil
	}

	// Hash all keys with xxhash.
	hashed := make([]uint64, n)
	for i, k := range keys {
		hashed[i] = xxhash.Sum64String(k)
	}

	// Build a map from mixed hash -> value (populated per seed attempt).
	hashToValue := make(map[uint64]uint64, n)

	size := uint32(n)
	cm := &ConstMap{}
	cm.initializeParameters(size)
	capacity := uint32(len(cm.data))

	rngcounter := uint64(1)
	cm.seed = splitmix64(&rngcounter)

	alone := make([]uint32, capacity)
	t2count := make([]uint8, capacity)
	t2hash := make([]uint64, capacity)
	reverseH := make([]uint8, size)
	reverseOrder := make([]uint64, size+1)
	reverseOrder[size] = 1

	for iterations := 0; ; iterations++ {
		if iterations > maxIterations {
			return nil, errors.New("constmap: failed to construct map, possible duplicate keys")
		}

		if size > 4 && size < 1_000_000 {
			switch iterations % 4 {
			case 2:
				cm.segmentLength /= 2
				cm.segmentLengthMask = cm.segmentLength - 1
				cm.segmentCount = cm.segmentCount*2 + 2
				cm.segmentCountLength = cm.segmentCount * cm.segmentLength
			case 3:
				cm.segmentLength *= 2
				cm.segmentLengthMask = cm.segmentLength - 1
				cm.segmentCount = cm.segmentCount/2 - 1
				cm.segmentCountLength = cm.segmentCount * cm.segmentLength
			}
		}

		blockBits := 1
		for (1 << blockBits) < cm.segmentCount {
			blockBits++
		}
		startPos := make([]uint32, 1<<blockBits)
		for i := range startPos {
			startPos[i] = uint32((uint64(i) * uint64(size)) >> blockBits)
		}
		for _, key := range hashed {
			hash := mixsplit(key, cm.seed)
			segmentIndex := hash >> (64 - blockBits)
			for reverseOrder[startPos[segmentIndex]] != 0 {
				segmentIndex++
				segmentIndex &= (1 << blockBits) - 1
			}
			reverseOrder[startPos[segmentIndex]] = hash
			startPos[segmentIndex]++
		}

		hasError := false
		for i := uint32(0); i < size; i++ {
			hash := reverseOrder[i]
			index1, index2, index3 := cm.getHashFromHash(hash)
			t2count[index1] += 4
			t2hash[index1] ^= hash
			t2count[index2] += 4
			t2count[index2] ^= 1
			t2hash[index2] ^= hash
			t2count[index3] += 4
			t2count[index3] ^= 2
			t2hash[index3] ^= hash

			if t2hash[index1]&t2hash[index2]&t2hash[index3] == 0 {
				if ((t2hash[index1] == 0) && (t2count[index1] == 8)) ||
					((t2hash[index2] == 0) && (t2count[index2] == 8)) ||
					((t2hash[index3] == 0) && (t2count[index3] == 8)) {
					// duplicate hash detected, not supported
					return nil, errors.New("constmap: duplicate key hash detected")
				}
			}
			if t2count[index1] < 4 || t2count[index2] < 4 || t2count[index3] < 4 {
				hasError = true
			}
		}
		if hasError {
			for i := uint32(0); i < size; i++ {
				reverseOrder[i] = 0
			}
			for i := uint32(0); i < capacity; i++ {
				t2count[i] = 0
				t2hash[i] = 0
			}
			cm.seed = splitmix64(&rngcounter)
			continue
		}

		// Peeling: find singletons and peel them off.
		Qsize := 0
		for i := uint32(0); i < capacity; i++ {
			alone[Qsize] = i
			if (t2count[i] >> 2) == 1 {
				Qsize++
			}
		}

		stacksize := uint32(0)
		segLen := cm.segmentLength
		segLenToMinusSegLenX2 := segLen ^ (-(2 * segLen))
		for Qsize > 0 {
			Qsize--
			index := alone[Qsize]
			if (t2count[index] >> 2) == 1 {
				hash := t2hash[index]
				found := t2count[index] & 3
				reverseH[stacksize] = found
				reverseOrder[stacksize] = hash
				stacksize++

				h01 := uint32(hash>>18) & cm.segmentLengthMask
				h02 := uint32(hash) & cm.segmentLengthMask

				is0 := -uint32((found - 1) >> 7)
				is1 := -uint32(found & 1)
				is2 := -uint32(found >> 1)

				otherIndex1 := index + (segLen ^ (segLenToMinusSegLenX2 & is2))
				otherIndex2 := index - (segLen ^ (segLenToMinusSegLenX2 & is0))

				otherIndex1 ^= (h01 &^ is2) ^ (h02 &^ is0)
				otherIndex2 ^= (h01 &^ is0) ^ (h02 &^ is1)

				f1 := uint8(is0&1 | is1&2)
				f2 := uint8(is0&2 | is2&1)

				alone[Qsize] = otherIndex1
				if (t2count[otherIndex1] >> 2) == 2 {
					Qsize++
				}
				t2count[otherIndex1] -= 4
				t2count[otherIndex1] ^= f1
				t2hash[otherIndex1] ^= hash

				alone[Qsize] = otherIndex2
				if (t2count[otherIndex2] >> 2) == 2 {
					Qsize++
				}
				t2count[otherIndex2] -= 4
				t2count[otherIndex2] ^= f2
				t2hash[otherIndex2] ^= hash
			}
		}

		if stacksize == size {
			// Build hash -> value mapping for this seed.
			for k := range hashToValue {
				delete(hashToValue, k)
			}
			for i := 0; i < n; i++ {
				hashToValue[mixsplit(hashed[i], cm.seed)] = values[i]
			}
			break
		}

		for i := uint32(0); i < size; i++ {
			reverseOrder[i] = 0
		}
		for i := uint32(0); i < capacity; i++ {
			t2count[i] = 0
			t2hash[i] = 0
		}
		cm.seed = splitmix64(&rngcounter)
	}

	// Assignment phase: walk the stack in reverse, assigning values.
	var h012 [5]uint32
	for i := int(size - 1); i >= 0; i-- {
		hash := reverseOrder[i]
		val := hashToValue[hash]
		index1, index2, index3 := cm.getHashFromHash(hash)
		found := reverseH[i]
		h012[0] = index1
		h012[1] = index2
		h012[2] = index3
		h012[3] = h012[0]
		h012[4] = h012[1]
		cm.data[h012[found]] = val ^ cm.data[h012[found+1]] ^ cm.data[h012[found+2]]
	}

	return cm, nil
}

// Map returns the uint64 value associated with the given key.
// The key must have been present in the original set passed to New.
// If the key was not in the original set, the return value is undefined.
func (cm *ConstMap) Map(key string) uint64 {
	hash := mixsplit(xxhash.Sum64String(key), cm.seed)
	h0, h1, h2 := cm.getHashFromHash(hash)
	return cm.data[h0] ^ cm.data[h1] ^ cm.data[h2]
}

// NotFound is the sentinel value returned by VerifiedConstMap.Map when
// the key was not in the original set.
const NotFound = ^uint64(0) // 0xFFFFFFFFFFFFFFFF

// fingerprint derives a verification hash from the mixed hash.
func fingerprint(hash uint64) uint64 {
	return hash ^ (hash >> 32)
}

// VerifiedConstMap is like ConstMap but additionally stores a verification
// fingerprint for each key. On lookup, the fingerprint is checked; if it
// does not match, NotFound is returned. This detects (with high probability)
// lookups of keys that were not in the original set, at the cost of doubling
// memory usage (~18 bytes/key instead of ~9).
type VerifiedConstMap struct {
	seed               uint64
	segmentLength      uint32
	segmentLengthMask  uint32
	segmentCount       uint32
	segmentCountLength uint32
	data               []uint64
	checks             []uint64
}

func (vm *VerifiedConstMap) getHashFromHash(hash uint64) (uint32, uint32, uint32) {
	hi, _ := bits.Mul64(hash, uint64(vm.segmentCountLength))
	h0 := uint32(hi)
	h1 := h0 + vm.segmentLength
	h2 := h1 + vm.segmentLength
	h1 ^= uint32(hash>>18) & vm.segmentLengthMask
	h2 ^= uint32(hash) & vm.segmentLengthMask
	return h0, h1, h2
}

// NewVerified builds a VerifiedConstMap from a set of string keys and their
// associated uint64 values. The two slices must have equal length. Keys must
// be unique. Values equal to NotFound (0xFFFFFFFFFFFFFFFF) are allowed but
// cannot be distinguished from a missing key on lookup.
func NewVerified(keys []string, values []uint64) (*VerifiedConstMap, error) {
	if len(keys) != len(values) {
		return nil, errors.New("constmap: keys and values must have equal length")
	}
	n := len(keys)
	if n == 0 {
		return &VerifiedConstMap{}, nil
	}

	// Hash all keys with xxhash.
	hashed := make([]uint64, n)
	for i, k := range keys {
		hashed[i] = xxhash.Sum64String(k)
	}

	hashToValue := make(map[uint64]uint64, n)

	size := uint32(n)
	vm := &VerifiedConstMap{}

	// Initialize parameters (same logic as ConstMap).
	vm.segmentLength = calculateSegmentLength(size)
	if vm.segmentLength > 262144 {
		vm.segmentLength = 262144
	}
	vm.segmentLengthMask = vm.segmentLength - 1
	cap0 := uint32(0)
	if size > 1 {
		sizeFactor := calculateSizeFactor(size)
		cap0 = uint32(math.Round(float64(size) * sizeFactor))
	}
	totalSegmentCount := (cap0 + vm.segmentLength - 1) / vm.segmentLength
	if totalSegmentCount < 3 {
		totalSegmentCount = 3
	}
	vm.segmentCount = totalSegmentCount - 2
	vm.segmentCountLength = vm.segmentCount * vm.segmentLength
	arrayLen := totalSegmentCount * vm.segmentLength
	vm.data = make([]uint64, arrayLen)
	vm.checks = make([]uint64, arrayLen)

	capacity := arrayLen

	rngcounter := uint64(1)
	vm.seed = splitmix64(&rngcounter)

	alone := make([]uint32, capacity)
	t2count := make([]uint8, capacity)
	t2hash := make([]uint64, capacity)
	reverseH := make([]uint8, size)
	reverseOrder := make([]uint64, size+1)
	reverseOrder[size] = 1

	for iterations := 0; ; iterations++ {
		if iterations > maxIterations {
			return nil, errors.New("constmap: failed to construct map, possible duplicate keys")
		}

		if size > 4 && size < 1_000_000 {
			switch iterations % 4 {
			case 2:
				vm.segmentLength /= 2
				vm.segmentLengthMask = vm.segmentLength - 1
				vm.segmentCount = vm.segmentCount*2 + 2
				vm.segmentCountLength = vm.segmentCount * vm.segmentLength
			case 3:
				vm.segmentLength *= 2
				vm.segmentLengthMask = vm.segmentLength - 1
				vm.segmentCount = vm.segmentCount/2 - 1
				vm.segmentCountLength = vm.segmentCount * vm.segmentLength
			}
		}

		blockBits := 1
		for (1 << blockBits) < vm.segmentCount {
			blockBits++
		}
		startPos := make([]uint32, 1<<blockBits)
		for i := range startPos {
			startPos[i] = uint32((uint64(i) * uint64(size)) >> blockBits)
		}
		for _, key := range hashed {
			hash := mixsplit(key, vm.seed)
			segmentIndex := hash >> (64 - blockBits)
			for reverseOrder[startPos[segmentIndex]] != 0 {
				segmentIndex++
				segmentIndex &= (1 << blockBits) - 1
			}
			reverseOrder[startPos[segmentIndex]] = hash
			startPos[segmentIndex]++
		}

		hasError := false
		for i := uint32(0); i < size; i++ {
			hash := reverseOrder[i]
			index1, index2, index3 := vm.getHashFromHash(hash)
			t2count[index1] += 4
			t2hash[index1] ^= hash
			t2count[index2] += 4
			t2count[index2] ^= 1
			t2hash[index2] ^= hash
			t2count[index3] += 4
			t2count[index3] ^= 2
			t2hash[index3] ^= hash

			if t2hash[index1]&t2hash[index2]&t2hash[index3] == 0 {
				if ((t2hash[index1] == 0) && (t2count[index1] == 8)) ||
					((t2hash[index2] == 0) && (t2count[index2] == 8)) ||
					((t2hash[index3] == 0) && (t2count[index3] == 8)) {
					return nil, errors.New("constmap: duplicate key hash detected")
				}
			}
			if t2count[index1] < 4 || t2count[index2] < 4 || t2count[index3] < 4 {
				hasError = true
			}
		}
		if hasError {
			for i := uint32(0); i < size; i++ {
				reverseOrder[i] = 0
			}
			for i := uint32(0); i < capacity; i++ {
				t2count[i] = 0
				t2hash[i] = 0
			}
			vm.seed = splitmix64(&rngcounter)
			continue
		}

		Qsize := 0
		for i := uint32(0); i < capacity; i++ {
			alone[Qsize] = i
			if (t2count[i] >> 2) == 1 {
				Qsize++
			}
		}

		stacksize := uint32(0)
		segLen := vm.segmentLength
		segLenToMinusSegLenX2 := segLen ^ (-(2 * segLen))
		for Qsize > 0 {
			Qsize--
			index := alone[Qsize]
			if (t2count[index] >> 2) == 1 {
				hash := t2hash[index]
				found := t2count[index] & 3
				reverseH[stacksize] = found
				reverseOrder[stacksize] = hash
				stacksize++

				h01 := uint32(hash>>18) & vm.segmentLengthMask
				h02 := uint32(hash) & vm.segmentLengthMask

				is0 := -uint32((found - 1) >> 7)
				is1 := -uint32(found & 1)
				is2 := -uint32(found >> 1)

				otherIndex1 := index + (segLen ^ (segLenToMinusSegLenX2 & is2))
				otherIndex2 := index - (segLen ^ (segLenToMinusSegLenX2 & is0))

				otherIndex1 ^= (h01 &^ is2) ^ (h02 &^ is0)
				otherIndex2 ^= (h01 &^ is0) ^ (h02 &^ is1)

				f1 := uint8(is0&1 | is1&2)
				f2 := uint8(is0&2 | is2&1)

				alone[Qsize] = otherIndex1
				if (t2count[otherIndex1] >> 2) == 2 {
					Qsize++
				}
				t2count[otherIndex1] -= 4
				t2count[otherIndex1] ^= f1
				t2hash[otherIndex1] ^= hash

				alone[Qsize] = otherIndex2
				if (t2count[otherIndex2] >> 2) == 2 {
					Qsize++
				}
				t2count[otherIndex2] -= 4
				t2count[otherIndex2] ^= f2
				t2hash[otherIndex2] ^= hash
			}
		}

		if stacksize == size {
			for k := range hashToValue {
				delete(hashToValue, k)
			}
			for i := 0; i < n; i++ {
				hashToValue[mixsplit(hashed[i], vm.seed)] = values[i]
			}
			break
		}

		for i := uint32(0); i < size; i++ {
			reverseOrder[i] = 0
		}
		for i := uint32(0); i < capacity; i++ {
			t2count[i] = 0
			t2hash[i] = 0
		}
		vm.seed = splitmix64(&rngcounter)
	}

	// Assignment phase: walk the stack in reverse, assigning values and fingerprints.
	var h012 [5]uint32
	for i := int(size - 1); i >= 0; i-- {
		hash := reverseOrder[i]
		val := hashToValue[hash]
		fp := fingerprint(hash)
		index1, index2, index3 := vm.getHashFromHash(hash)
		found := reverseH[i]
		h012[0] = index1
		h012[1] = index2
		h012[2] = index3
		h012[3] = h012[0]
		h012[4] = h012[1]
		vm.data[h012[found]] = val ^ vm.data[h012[found+1]] ^ vm.data[h012[found+2]]
		vm.checks[h012[found]] = fp ^ vm.checks[h012[found+1]] ^ vm.checks[h012[found+2]]
	}

	return vm, nil
}

// Map returns the uint64 value associated with the given key.
// If the key was not in the original set, NotFound is returned.
// The false positive probability is approximately 2^{-64}.
func (vm *VerifiedConstMap) Map(key string) uint64 {
	if len(vm.data) == 0 {
		return NotFound
	}
	hash := mixsplit(xxhash.Sum64String(key), vm.seed)
	h0, h1, h2 := vm.getHashFromHash(hash)
	fp := vm.checks[h0] ^ vm.checks[h1] ^ vm.checks[h2]
	if fp != fingerprint(hash) {
		return NotFound
	}
	return vm.data[h0] ^ vm.data[h1] ^ vm.data[h2]
}

// Binary format (all little-endian):
//   [8] magic "CMAP0001"
//   [8] seed
//   [4] segmentLength
//   [4] segmentCount
//   [4] len(data)
//   [8*len(data)] data
//   [8] FNV-1a 64-bit checksum of all preceding bytes

var magicBytes = [8]byte{'C', 'M', 'A', 'P', '0', '0', '0', '1'}

// ioChunkWords is how many uint64s WriteTo and ReadFrom move per call to the
// underlying writer or reader. Moving one word at a time costs a call per
// word -- and, when the reader is a bare *os.File as it is in LoadFromFile, a
// syscall per word, which dominates everything else. 8192 words is 64 KiB.
const ioChunkWords = 8192

// WriteTo serializes the ConstMap to w in a portable binary format.
// A FNV-1a checksum is appended for integrity verification.
func (cm *ConstMap) WriteTo(w io.Writer) (int64, error) {
	h := fnv.New64a()
	mw := io.MultiWriter(w, h)

	var buf [8]byte

	// Magic.
	copy(buf[:], magicBytes[:])
	if _, err := mw.Write(buf[:]); err != nil {
		return 0, err
	}

	// Seed.
	binary.LittleEndian.PutUint64(buf[:], cm.seed)
	if _, err := mw.Write(buf[:]); err != nil {
		return 0, err
	}

	// SegmentLength.
	binary.LittleEndian.PutUint32(buf[:4], cm.segmentLength)
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	// SegmentCount.
	binary.LittleEndian.PutUint32(buf[:4], cm.segmentCount)
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	// Data length.
	dataLen := uint32(len(cm.data))
	binary.LittleEndian.PutUint32(buf[:4], dataLen)
	if _, err := mw.Write(buf[:4]); err != nil {
		return 0, err
	}

	// Data, encoded and written a chunk at a time.
	if len(cm.data) > 0 {
		chunk := make([]byte, min(len(cm.data), ioChunkWords)*8)
		for i := 0; i < len(cm.data); {
			n := min(len(cm.data)-i, ioChunkWords)
			b := chunk[:n*8]
			for j, v := range cm.data[i : i+n] {
				binary.LittleEndian.PutUint64(b[j*8:], v)
			}
			if _, err := mw.Write(b); err != nil {
				return 0, err
			}
			i += n
		}
	}

	// Checksum (written to w only, not fed back into the hash).
	binary.LittleEndian.PutUint64(buf[:], h.Sum64())
	if _, err := w.Write(buf[:]); err != nil {
		return 0, err
	}

	written := int64(8 + 8 + 4 + 4 + 4 + 8*len(cm.data) + 8)
	return written, nil
}

// ReadFrom deserializes a ConstMap from r. It verifies the trailing checksum
// and returns an error if the data is corrupted.
func (cm *ConstMap) ReadFrom(r io.Reader) (int64, error) {
	h := fnv.New64a()
	tr := io.TeeReader(r, h)

	var buf [8]byte

	// Magic.
	if _, err := io.ReadFull(tr, buf[:]); err != nil {
		return 0, fmt.Errorf("constmap: reading magic: %w", err)
	}
	if buf != magicBytes {
		return 0, errors.New("constmap: invalid magic bytes")
	}

	// Seed.
	if _, err := io.ReadFull(tr, buf[:]); err != nil {
		return 0, fmt.Errorf("constmap: reading seed: %w", err)
	}
	cm.seed = binary.LittleEndian.Uint64(buf[:])

	// SegmentLength.
	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading segment length: %w", err)
	}
	cm.segmentLength = binary.LittleEndian.Uint32(buf[:4])
	cm.segmentLengthMask = cm.segmentLength - 1

	// SegmentCount.
	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading segment count: %w", err)
	}
	cm.segmentCount = binary.LittleEndian.Uint32(buf[:4])
	cm.segmentCountLength = cm.segmentCount * cm.segmentLength

	// Data length.
	if _, err := io.ReadFull(tr, buf[:4]); err != nil {
		return 0, fmt.Errorf("constmap: reading data length: %w", err)
	}
	dataLen := binary.LittleEndian.Uint32(buf[:4])

	// Data, read a chunk at a time and decoded in place.
	cm.data = make([]uint64, dataLen)
	if len(cm.data) > 0 {
		chunk := make([]byte, min(len(cm.data), ioChunkWords)*8)
		for i := 0; i < len(cm.data); {
			n := min(len(cm.data)-i, ioChunkWords)
			b := chunk[:n*8]
			if _, err := io.ReadFull(tr, b); err != nil {
				return 0, fmt.Errorf("constmap: reading data[%d:%d]: %w", i, i+n, err)
			}
			for j := range cm.data[i : i+n] {
				cm.data[i+j] = binary.LittleEndian.Uint64(b[j*8:])
			}
			i += n
		}
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

	read := int64(8 + 8 + 4 + 4 + 4 + 8*int(dataLen) + 8)
	return read, nil
}

// SaveToFile serializes the ConstMap to a file at the given path.
func (cm *ConstMap) SaveToFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := cm.WriteTo(f); err != nil {
		return err
	}
	return f.Close()
}

// LoadFromFile deserializes a ConstMap from a file at the given path.
func LoadFromFile(path string) (*ConstMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cm := &ConstMap{}
	if _, err := cm.ReadFrom(f); err != nil {
		return nil, err
	}
	return cm, nil
}
