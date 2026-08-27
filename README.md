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

## Batched Lookups

If you have many keys to look up at once, `MapMany` takes a slice of strings and
returns a slice of values, where `result[i]` corresponds to `keys[i]`:

```go
values := cm.MapMany([]string{"apple", "banana", "cherry"}) // [100 200 300]
```

`VerifiedConstMap` has the same method, and still reports absent keys as `NotFound`:

```go
values := vm.MapMany([]string{"banana", "grape"}) // [200 NotFound]
```

Use `MapManyInto(dst, keys)` if you are looking up batch after batch and would
rather reuse a buffer than allocate one each time. It fills `dst[:len(keys)]` and
panics if `dst` is shorter than `keys`.

A batch is faster than the loop it replaces, because it hashes a block of keys
before gathering any values, which lets the array accesses of the whole block be in
flight at once instead of each key's loads waiting behind the previous key's
hashing. With 1,000,000 keys:

| Lookup             | Apple M4 Max          | Xeon Gold 6548N        |
|--------------------|-----------------------|------------------------|
| `ConstMap`         | 9.7 -> 8.0 ns/key     | 9.7 -> 8.9 ns/key      |
| `VerifiedConstMap` | 11.3 -> 10.0 ns/key   | 18.2 -> 13.9 ns/key    |

The verified map gains more on the Xeon because it touches two arrays per lookup,
so there is more memory latency to hide. How much you gain depends on the machine
and on how much of the map fits in cache: a map small enough to sit in a large
last-level cache has little latency left to hide, and can come out roughly even.

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

`VerifiedConstMap` serializes the same way, into its own format:

```go
// Save to file.
err := vm.SaveToFile("myverifiedmap.cmap")

// Load from file.
vm, err := constmap.LoadVerifiedFromFile("myverifiedmap.cmap")
```

The two formats carry different magic bytes, so handing a file of one kind to the
other kind's reader is reported rather than silently misinterpreted.

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


The speed varies depending on your system, the size of your dataset, the keys, the order of the lookup and so forth. If it can reside in CPU cache while the map cannot, then it will be significantly faster. 

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
- **BenchmarkConstMapMany** / **BenchmarkVerifiedConstMapMany** -- batched lookup, against
  **BenchmarkConstMapLoop** / **BenchmarkVerifiedConstMapLoop** for the loop they replace
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
