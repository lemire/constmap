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

## Serialization

A `ConstMap` can be serialized to disk and loaded back later, avoiding the cost of reconstruction. The binary format includes a FNV-1a checksum to detect corruption.

```go
// Save to file.
err := cm.SaveToFile("mymap.cmap")

// Load from file.
cm, err := constmap.LoadFromFile("mymap.cmap")
```

`WriteTo` and `ReadFrom` move the data array in 64 KiB chunks, so they are efficient
even on an unbuffered `*os.File`; wrapping the file in a `bufio.Reader` yourself is
unnecessary and slightly slower, since it adds a second copy.

For streaming use, `WriteTo` and `ReadFrom` work with any `io.Writer` / `io.Reader`:

```go
// Write to any io.Writer.
n, err := cm.WriteTo(w)

// Read from any io.Reader.
var cm constmap.ConstMap
n, err := cm.ReadFrom(r)
```

## Memory-mapped maps

`OpenMapped` reads a saved map straight out of a read-only memory mapping. Nothing
is copied: the lookup array aliases the file, the kernel faults pages in lazily as
lookups touch them, several processes mapping the same file share one copy of the
physical memory, and the bytes never enter the Go heap, so they cost the garbage
collector nothing.

```go
m, err := constmap.OpenMapped("mymap.cmap")
if err != nil {
	log.Fatal(err)
}
defer m.Close()

fmt.Println(m.Map("banana")) // 200
```

`MappedConstMap` embeds `ConstMap`, so it has the same lookup API; pass `&m.ConstMap`
to code that expects a `*ConstMap`. That pointer, and any value read through it, is
valid only until `Close`.

Unlike `LoadFromFile`, `OpenMapped` does not verify the trailing checksum, because
hashing the file would touch every page and undo the lazy loading that memory mapping
buys. It does check the magic bytes and that the file is big enough for the header it
declares. Call `m.Verify()` when integrity matters more than open latency.

How much mapping buys you depends entirely on whether you verify. With 1,000,000
keys (a 9.04 MB file, warm page cache):

| Opening a saved map     | Verifies? | Apple M4 Max | Xeon Gold 6548N |
|-------------------------|-----------|--------------|-----------------|
| `OpenMapped`            | no        | 20.0 us      | 8.63 us         |
| `OpenMapped` + `Verify` | yes       | 8.71 ms      | 10.7 ms         |
| `LoadFromFile`          | yes       | 9.61 ms      | 12.9 ms         |

| Lookups       | Apple M4 Max | Xeon Gold 6548N |
|---------------|--------------|-----------------|
| Mapped        | 10.3 ns/op   | 18.4 ns/op      |
| Heap-resident | 8.3 ns/op    | 16.8 ns/op      |

Read that table as two separate stories.

If you skip verification, `OpenMapped` does essentially no work at all -- one `mmap`
call, no matter how big the file -- and opens roughly 500x faster than reading the
file. It allocates 456 bytes; `LoadFromFile` allocates the whole 9 MB array.

If you verify, the two nearly converge, at 1.1x on the M4 and 1.2x on the Xeon, because
`Verify` walks the entire file and both paths become bound by FNV-1a at about 1 GB/s.
What is left of the gap is the copy that mapping avoids: it hashes the mapped bytes in
place, rather than decoding them into a fresh 9 MB heap allocation first. Note also
that verifying faults in every page, which is exactly the lazy loading that mapping was
supposed to buy you. `Verify` and `OpenMapped` pull in opposite directions; reach for
`Verify` when you want the integrity check, not when you want a fast open.

So: map when you want a cheap open, a small resident set, or shared pages across
processes. Read the file when you want the last nanosecond or two per lookup. Both
are fast; the old 400 ms `LoadFromFile` was not a real ceiling, just a missing buffer.

## Running Tests

```
go test -v
```

## Performance gains

The construction time is higher (as expected for any compact data structure), but lookups are optimized for speed. I ran benchmarks on my Apple M4 Max processor to compare constmap lookups against Go's built-in `map[string]uint64`. The test uses 1 million keys.

| Data Structure    | Lookup Time | Memory Usage |
|-------------------|-------------|--------------|
| ConstMap          | 7.6 ns/op   | 9 bytes/key  |
| VerifiedConstMap  | 13 ns/op    | 18 bytes/key |
| Go Map            | 23 ns/op    | 56 bytes/key |


The speed varies depending on your system and the size of your dataset. If it can reside in CPU cache while the map cannot, then it will be significantly faster. 

The memory usage should always be significantly better with `ConstMap` as long as you have many thousands of keys.

## Benchmarks

The benchmark suite compares `ConstMap` against Go's built-in `map[string]uint64` using 1,000,000 keys. To run:

```
go test -bench=. -benchmem
```

The main benchmarks are:

- **BenchmarkConstMap** -- lookup throughput for `ConstMap.Map()`
- **BenchmarkVerifiedConstMap** -- lookup throughput for `VerifiedConstMap.Map()`
- **BenchmarkGoMap** -- lookup throughput for Go's built-in map
- **BenchmarkMappedConstMap** -- lookup throughput for a memory-mapped `ConstMap`
- **BenchmarkOpenMapped** / **BenchmarkLoadFromFile** -- cost of opening a saved map

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
