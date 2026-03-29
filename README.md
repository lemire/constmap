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
go get github.com/fastfilter/constmap
```


## Usage

```go
package main

import (
	"fmt"
	"log"

	"github.com/fastfilter/constmap"
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

## Serialization

A `ConstMap` can be serialized to disk and loaded back later, avoiding the cost of reconstruction. The binary format includes a FNV-1a checksum to detect corruption.

```go
// Save to file.
err := cm.SaveToFile("mymap.cmap")

// Load from file.
cm, err := constmap.LoadFromFile("mymap.cmap")
```

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

## Benchmarks

The benchmark suite compares `ConstMap` against Go's built-in `map[string]uint64` using 1,000,000 keys. To run:

```
go test -bench=. -benchmem
```

There are three benchmarks:

- **BenchmarkConstMap** -- lookup throughput for `ConstMap.Map()`
- **BenchmarkGoMap** -- lookup throughput for Go's built-in map
- **BenchmarkConstMapNew** -- construction time for `ConstMap`

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

The `ConstMap` stores approximately 1.125 x *n* x 8 bytes (roughly 9 bytes per key), while Go's `map[string]uint64` typically uses around 50-60 bytes per key for keys of this size.

## How It Works

Given *n* key-value pairs, the algorithm:

1. Hashes each key (using [xxhash](https://github.com/cespare/xxhash)) and maps it to three positions in an array of size ~1.125*n* using overlapping segments.
2. Uses a peeling process to find an ordering where each key can be assigned to one of its three positions uniquely.
3. Walks the ordering in reverse, setting each array cell so that `array[h0] XOR array[h1] XOR array[h2] == value` for every key.

Lookup computes the same three positions and XORs the three array cells to recover the value. This gives O(1) lookup with minimal memory overhead.

Compared to xor filters which divide the array into three equal blocks (~1.23*n* overhead), binary fuse filters use overlapping segments for better locality and a lower space overhead (~1.125*n*), and they construct faster.
