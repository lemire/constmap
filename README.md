# constmap

A fast, compact, immutable map from strings to `uint64` values in Go. It uses the binary fuse filter construction to store key-value pairs in a compact array where lookup requires only one hash computation, three array accesses, and two XOR operations.

The data structure is ideal when you have a known set of string keys at construction time and need fast, memory-efficient lookups afterward.

## Reference

This implementation is based on the binary fuse filter algorithm described in:

> Thomas Mueller Graf and Daniel Lemire, [Binary Fuse Filters: Fast and Smaller Than Xor Filters](https://arxiv.org/abs/2201.01174), *ACM Journal of Experimental Algorithmics*, Volume 27, 2022. DOI: [10.1145/3510449](https://doi.org/10.1145/3510449)

See also the earlier xor filter paper:

> Thomas Mueller Graf and Daniel Lemire, [Xor Filters: Faster and Smaller Than Bloom and Cuckoo Filters](https://arxiv.org/abs/1912.08258), *ACM Journal of Experimental Algorithmics*, Volume 25, 2020. DOI: [10.1145/3376122](https://doi.org/10.1145/3376122)

## Installation

```
go get github.com/lemire/constmap
```


## Usage

```go
package main

import (
	"fmt"
	"log"

	"github.com/lemire/constmap"
)

func main() {
	keys := []string{"apple", "banana", "cherry"}
	values := []uint64{100, 200, 300}

	cm, err := constmap.New(keys, values)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(cm.Map("banana")) // 200
}
```

The `keys` and `values` slices must have equal length, and keys must be unique. After construction, the `ConstMap` is immutable. Looking up a key that was not in the original set returns an undefined value.

### Verified Lookups

If you need to detect missing keys, use `VerifiedConstMap`. It stores an additional fingerprint per key and returns the sentinel `NotFound` for keys not in the original set:

```go
vm, err := constmap.NewVerified(keys, values)
if err != nil {
	log.Fatal(err)
}

fmt.Println(vm.Map("banana")) // 200
fmt.Println(vm.Map("grape"))  // constmap.NotFound (0xFFFFFFFFFFFFFFFF)
```

This doubles memory usage (~18 bytes/key instead of ~9) but lookup remains fast.

### Paired Layout

`VerifiedConstMap` keeps its values and its fingerprints in two separate arrays,
so a lookup of a present key reads three fingerprint words and then three value
words: six cache lines, in two different places. `PairedVerifiedConstMap` is the
same map with each value stored next to its fingerprint, so the three reads bring
in both at once and a lookup touches three cache lines instead of six:

```go
pm, err := constmap.NewPaired(keys, values)
if err != nil {
	log.Fatal(err)
}

fmt.Println(pm.Map("banana")) // 200
fmt.Println(pm.Map("grape"))  // constmap.NotFound
```

It has exactly the semantics, memory footprint and API of `VerifiedConstMap`
(`Map`, `MapMany`, `MapManyInto`, `SaveToFile`, `LoadPairedFromFile`), and an
existing `VerifiedConstMap` converts with `vm.Paired()`. The difference is which
keys it is fast for:

- **Present keys**: fewer cache lines, so faster. Random lookups over a million
  keys run 5% faster than `VerifiedConstMap.Map` on an Apple M4 Max and 6% on an
  Intel Xeon Gold 6548N while the map sits in the last-level cache; at ten million
  keys, where it does not, 19% on the Xeon.
- **Absent keys**: slower. `VerifiedConstMap.Map` stops after reading the
  fingerprint array, which is half the size of the whole map and so far more
  likely to be cache-resident, whereas the paired map always brings the value in
  alongside the fingerprint, so its working set for misses is twice as large.
- **Batches**: `MapMany` overlaps the memory accesses of eight keys, which
  hides some of the extra latency either way, but the paired layout still wins
  on present keys: 12% faster cold on the M4 Max and 27% on the Xeon (table
  below).

Use `PairedVerifiedConstMap` when most of the keys you look up are present, and
`VerifiedConstMap` when most are absent. Its serialized format has its own magic
bytes (`PMAP0001`) and is not interchangeable with `VerifiedConstMap`'s.

On amd64 and arm64, `MapMany` resolves each block of eight keys with a small
assembly routine that loads each 16-byte slot as one vector (SSE2 or NEON) and
XORs the three slots with two vector instructions. Build with `-tags purego` to
use the portable Go version instead.

## Batched Lookups

If you have many keys to resolve at once, `MapMany` takes a slice of strings and
returns a slice of values, where `result[i]` corresponds to `keys[i]`:

```go
values := cm.MapMany([]string{"apple", "banana", "cherry"}) // [100 200 300]
```

`VerifiedConstMap` and `PairedVerifiedConstMap` have the same method, and still
report absent keys as `NotFound`:

```go
values := vm.MapMany([]string{"banana", "grape"}) // [200 NotFound]
```

If you resolve batch after batch, `MapManyInto` writes into a buffer you own
instead of allocating a new one each time. It fills `dst[:len(keys)]`, leaves the
rest of `dst` untouched, and panics if `dst` is shorter than `keys`:

```go
dst := make([]uint64, 4096)
for _, batch := range batches {
	cm.MapManyInto(dst, batch)
	use(dst[:len(batch)])
}
```

Both are exactly equivalent to calling `Map` on each key in turn. They are faster
for two reasons.

**Overlapping the memory accesses.** A batch hashes a block of eight keys before
gathering any values. That lets the three array accesses of all eight keys be in
flight at once, instead of each key's loads waiting behind the previous key's
hashing. A lookup is memory-latency-bound whenever the map is larger than
last-level cache, so this is where most of the gain comes from on a fresh batch.

**Hashing a block in one call.** On amd64 and arm64 the eight keys are hashed by a
single assembly routine that keeps the five XXH64 prime constants in registers for
the whole block, rather than reloading them and paying a function call per key. It
is bit-for-bit identical to `xxhash.Sum64String`, and a test checks that
exhaustively for every input length it accepts. Keys of 32 bytes or more are not
covered by it; a block containing one falls back to per-key hashing, and nothing
else is affected.

### Measured

2,000-key batch against a 1,000,000-key map, ns/key, medians of
`-benchtime 3s -count 3`. *Cold* rotates through 64 distinct random batches so the
touched cache lines are not already resident; *hot* replays one batch, so the
touched region of the array stays cache-resident and hashing dominates instead.

| | | loop over `Map` | `MapMany` | |
|---|---|---|---|---|
| **Apple M4 Max** | `ConstMap` cold | 11.6 | **6.2** | 47% |
| | `ConstMap` hot | 8.8 | **4.9** | 44% |
| | `VerifiedConstMap` cold | 17.2 | **10.6** | 39% |
| | `VerifiedConstMap` hot | 9.9 | **6.6** | 34% |
| | `PairedVerifiedConstMap` cold | 14.2 | **8.2** | 42% |
| | `PairedVerifiedConstMap` hot | 8.9 | **5.4** | 39% |
| **Xeon Gold 6548N** | `ConstMap` cold | 16.4 | **13.2** | 19% |
| | `ConstMap` hot | 9.4 | **7.1** | 24% |
| | `VerifiedConstMap` cold | 20.3 | **20.0** | ~0% |
| | `VerifiedConstMap` hot | 12.1 | **8.8** | 27% |
| | `PairedVerifiedConstMap` cold | 19.6 | **14.6** | 25% |
| | `PairedVerifiedConstMap` hot | 11.6 | **7.9** | 32% |

The one place batching does not pay is `VerifiedConstMap` in the cold regime on the Xeon, where
the difference is inside the run-to-run spread. That map probes two arrays per lookup, and on a
fresh batch it is bound by memory latency the batching cannot hide; the same case on the M4 Max,
which has less memory-level parallelism to begin with, still gains 39%.

The two effects split differently by platform. Turning the batched hasher off and
leaving everything else identical costs 34% cold and 29% hot on the M4 Max, but
only 3.5% cold and 14% hot on the Xeon (measured before this benchmark revision;
the split, not the absolute figures). The assembly earns much more on arm64
because materializing a 64-bit constant there takes a multi-instruction
`MOVZ`/`MOVK` sequence, so hoisting the five primes into registers saves real work;
on x86-64 each prime is one `MOVQ` from memory, or free as a RIP-relative operand,
so there is less to hoist. In isolation the batched hasher runs at 1.9 ns/key
against cespare's 3.4 on the M4 Max, and 6.4 against 8.3 on the Xeon.

### Platform support

| | batched hasher | falls back to |
|---|---|---|
| arm64 | `hashbatch_arm64.s` | -- |
| amd64 | `hashbatch_amd64.s` | -- |
| everything else, or `-tags purego` | none | per-key `xxhash.Sum64String` |

Both assembly routines are plain scalar code with no CPU feature gate, so they run
on every arm64 and every x86-64 machine. On platforms without one, `MapMany` still
does the phase separation and is still faster than a loop; only the hashing half of
the gain is missing. Results are identical either way.

There is no SIMD variant. XXH64's short-input path is a serial
multiply-and-rotate chain per key, so the win from vectorizing would have to come
from putting several keys in several lanes -- but AVX2 has no 64x64 multiply
(`VPMULLQ` is AVX-512DQ), and even with AVX-512 the cost of marshalling eight keys
at eight different addresses and eight different lengths into lanes, then masking
through a byte-at-a-time tail, is very likely to exceed what the multiplies save.

## Serialization

A `ConstMap` can be serialized to disk and loaded back later, avoiding the cost of reconstruction. The binary format includes a FNV-1a checksum to detect corruption.

```go
// Save to file.
err := cm.SaveToFile("mymap.cmap")

// Load from file.
cm, err := constmap.LoadFromFile("mymap.cmap")
```

`WriteTo` and `ReadFrom` move the data array in 64 KiB chunks rather than one `uint64`
at a time. That matters most when the underlying writer or reader is an unbuffered
`*os.File`, as it is here: a call per word means a syscall per word. For 1,000,000 keys
(a 9.04 MB file):

| Operation      | Apple M4 Max       | Xeon Gold 6548N   |
|----------------|--------------------|-------------------|
| `SaveToFile`   | 1080 ms -> 12.5 ms | 613 ms -> 13.1 ms |
| `LoadFromFile` | 404 ms -> 9.9 ms   | 370 ms -> 12.9 ms |

There is no need to wrap the file in a `bufio.Reader` yourself; that only adds a second
copy, and measures slightly slower than handing `ReadFrom` the file directly.

`VerifiedConstMap` and `PairedVerifiedConstMap` serialize the same way, each into
its own format:

```go
// Save to file.
err := vm.SaveToFile("myverifiedmap.cmap")
err = pm.SaveToFile("mypairedmap.cmap")

// Load from file.
vm, err := constmap.LoadVerifiedFromFile("myverifiedmap.cmap")
pm, err := constmap.LoadPairedFromFile("mypairedmap.cmap")
```

The three formats carry different magic bytes, so handing a file of one kind to
another kind's reader is reported rather than silently misinterpreted.

The verified format pads its header to 32 bytes so that both `uint64` arrays begin on
a 64-bit boundary: `data` at offset 32, and `checks` at `32 + 8*len(data)`, which is a
multiple of eight because the first array is a whole number of words. A reader that
maps or otherwise aliases the file can treat either array as a `[]uint64` without a
misaligned access.

For streaming use, `WriteTo` and `ReadFrom` work with any `io.Writer` / `io.Reader`:

```go
// Write to any io.Writer.
n, err := cm.WriteTo(w)

// Read from any io.Reader.
var cm constmap.ConstMap
n, err := cm.ReadFrom(r)
```

### Interoperability with the Rust and Python implementations

The formats are shared with [rsconstmap](https://github.com/lemire/rsconstmap)
(Rust) and [fastconstmap](https://github.com/lemire/fastconstmap) (C, with Python
bindings): a map saved by any of the three loads in the other two, on a
little-endian host (which is every mainstream one: x86-64, ARM64, RISC-V, Apple
silicon). The three share the key hash (XXH64), the mixing, the seed sequence and
the layout, so for the same input this package and rsconstmap write byte-identical
files, and fastconstmap writes the same bytes apart from one header word:

- `VerifiedConstMap` (`VMAP0001`) and `PairedVerifiedConstMap` (`PMAP0001`) are
  identical across the three. fastconstmap stores its key count in the header word
  this package leaves as zero padding; every reader ignores the word.
- `ConstMap` files from fastconstmap carry the magic `CMAP0003`: this package's
  `CMAP0001` plus that 4-byte key count after the data length. `ReadFrom` accepts
  both and ignores the count.

```go
vm, err := constmap.LoadVerifiedFromFile("built-by-python.vmap") // from VerifiedConstMap.save
cm, err := constmap.LoadFromFile("built-by-rust.cmap")           // from ConstMap::save_to_file
```

Files written by fastconstmap 0.9 and earlier (`CMAP0002`, `VCMP0002`) used a
different key hash (XXH3) and cannot be loaded here; `ReadFrom` says so rather than
reporting an invalid file. fastconstmap 0.10 and later still read them, and a map
rebuilt from its keys there writes the shared format.

The interoperability is tested: `testdata/interop/` holds files written by the other
two implementations from a fixed input, which `interop_test.go` loads and checks, and
it checks that this package writes exactly the bytes rsconstmap wrote.

## Running Tests

```
go test -v
```

## Performance gains

The construction time is higher (as expected for any compact data structure), but lookups are
optimized for speed. Benchmarked on an Apple M4 Max against Go's built-in `map[string]uint64`,
with 1 million keys:

| Data Structure    | Lookup Time | Memory Usage   |
|-------------------|-------------|----------------|
| ConstMap          | 8.7 ns/op   | 9.0 bytes/key  |
| VerifiedConstMap  | 15 ns/op    | 18 bytes/key   |
| Go Map            | 46 ns/op    | 48 bytes/key   |

The speed varies depending on your system, the size of your dataset, the keys, the order of the
lookup and so forth. If it can reside in CPU cache while the map cannot, then it will be
significantly faster.

The memory usage should always be significantly better with `ConstMap` as long as you have many
thousands of keys.

### How these are measured

Lookup benchmarks query every key exactly once, in random order, from a buffer built in that
order (`makeQueryOrder`). Both halves of that are deliberate, and both change the answer a lot.

*Random order* is what exercises the map. Walking the keys in the order they were constructed is
a pattern no caller has, and it lets the hardware prefetcher hide work a real lookup must do.

*A buffer built in query order* is what keeps the measurement about the map. The key text is
allocated in index order, so merely permuting the slice would leave every string body where it
was and add a scattered, dependency-carrying load to each lookup -- you cannot hash a key before
reading its bytes. That cost is real, but it belongs to whatever produced the keys, not to the
map: it adds roughly 11 ns per lookup at a million keys and roughly 52 ns at four million,
growing with the size of the benchmark's own key array rather than with anything about the data
structure.

Querying the whole key set, rather than a small sample, is deliberate too. A small sample keeps
the key text in cache, but it also leaves most of the map untouched and cache-resident, which
flatters every implementation and hides exactly the compactness that is the point. Over 256
queries `ConstMap` beats Go's map by 1.2x; over the whole key set, by 5.3x. Same code, same map.

### Scaling

One pass over the whole key set, so every lookup is a first touch. `go test -run
TestLookupAndMemoryTable -v` with `RUN_HEAVY_MEMORY_TESTS=1`, Apple M4 Max:

| keys       | ConstMap | VerifiedConstMap | Go map | ConstMap bytes/key | Go map bytes/key |
|------------|----------|------------------|--------|--------------------|------------------|
| 10,000     | 9.4 ns   | 10.4 ns          | 14.8 ns| 10.2               | 31.4             |
| 100,000    | 8.0 ns   | 9.5 ns           | 13.6 ns| 9.5                | 26.9             |
| 1,000,000  | 12.5 ns  | 18.6 ns          | 51.1 ns| 9.0                | 47.9             |
| 10,000,000 | 34.8 ns  | 41.3 ns          | 70.1 ns| 9.0                | 36.7             |

The margin grows with the key count, which is the expected shape: at ten thousand keys everything
fits in cache and the compactness buys little, while at a million and beyond it is most of the
story.

## Benchmarks

The benchmark suite compares `ConstMap` against Go's built-in `map[string]uint64` using 1,000,000 keys. To run:

```
go test -bench=. -benchmem
```

The main benchmarks are:

- **BenchmarkConstMap** -- lookup throughput for `ConstMap.Map()`
- **BenchmarkVerifiedConstMap** -- lookup throughput for `VerifiedConstMap.Map()`
- **BenchmarkPairedConstMap** -- the same for `PairedVerifiedConstMap.Map()`
- **BenchmarkGoMap** -- lookup throughput for Go's built-in map
- **BenchmarkMapMany_Cold** / **BenchmarkMapMany_Hot** -- batched lookup, against
  **BenchmarkBatchNaive_Cold** / **BenchmarkBatchNaive_Hot** for the loop it replaces
  (and the **Verified** and **Paired** prefixed equivalents for `VerifiedConstMap`
  and `PairedVerifiedConstMap`)
- **BenchmarkHashBatchOnly** -- the batched hasher against per-key `xxhash.Sum64String`
- **BenchmarkSaveToFile** / **BenchmarkLoadFromFile** -- serialization throughput
- **BenchmarkVerifiedSaveToFile** / **BenchmarkLoadVerifiedFromFile** -- the same for `VerifiedConstMap`

For stable, reproducible results:

```
go test -bench=. -benchmem -count=5 -benchtime=3s
```

The `-count=5` flag runs each benchmark five times so you can assess variance. The `-benchtime=3s` flag gives each iteration more time to stabilize.

## Memory Usage

Run the memory comparison test to see the retained memory of each data structure with 1,000,000 keys:

```
go test -run TestMemoryUsage -v
```

The `ConstMap` stores approximately 1.125 x *n* x 8 bytes (roughly 9 bytes per key), the `VerifiedConstMap` uses twice that (~18 bytes/key), while Go's `map[string]uint64` typically uses around 50-60 bytes per key for keys of this size.

## How It Works

Given *n* key-value pairs, the algorithm:

1. Hashes each key (using [xxhash](https://github.com/cespare/xxhash)) and maps it to three positions in an array of size ~1.125*n* using overlapping segments.
2. Uses a peeling process to find an ordering where each key can be assigned to one of its three positions uniquely.
3. Walks the ordering in reverse, setting each array cell so that `array[h0] XOR array[h1] XOR array[h2] == value` for every key.

Lookup computes the same three positions and XORs the three array cells to recover the value. This gives O(1) lookup with minimal memory overhead.

Compared to xor filters which divide the array into three equal blocks (~1.23*n* overhead), binary fuse filters use overlapping segments for better locality and a lower space overhead (~1.125*n*), and they construct faster.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
