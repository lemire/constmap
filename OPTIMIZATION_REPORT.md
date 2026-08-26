# Batch Lookup Optimization Report

This documents a performance-engineering session on `constmap`, adding a
batch-lookup API (`MapBatch`, `MapBatchParallel`) optimized for looking up
~2,000 keys against a ~1,000,000-entry map on one specific laptop.

## 1. The starting prompt

> now i'd like to see if i use this repo to achieve optimal performance in
> this laptop, the use case is a map of about 1 million entries, and to
> optimize a batch look up of 2K keys. Use profiling to identify
> bottlenecks, go down to assembly level to not miss any opportunities. Use
> hardware instructions (SIMD etc) as you wish. For external libraries,
> feel free to reimplement the logic in this repo for optimal performance.
> Regularly report your progress about the optimization in every trial.

## 2. Hardware & software configuration

| | |
|---|---|
| CPU | Apple M1 Max (8 performance cores @ P-cluster, 2 efficiency cores) |
| L1 data cache | 128 KiB per P-core, 64 KiB per E-core |
| L2 cache | 12 MiB shared per 4-P-core cluster, 4 MiB shared for the E-cluster |
| Cache line size | 128 bytes (reported worst case; P-core L1 lines are 64B) |
| RAM | 64 GiB |
| OS | macOS 26.6.2 (Darwin 25.6.0, arm64) |
| Go | go1.25.3 darwin/arm64 |
| Dataset | 1,000,000 keys (`"key-%d"`), `ConstMap` data array ≈ 9 MB (1.125×n×8 bytes) |

Note: the repo's README quotes numbers from an Apple **M4 Max**; this
session ran entirely on an **M1 Max**, so absolute numbers here are not
comparable to the README's table. The 9 MB data array does not fit in L1
and is close to the L2 capacity, so a "cold" (fresh, not recently queried)
batch is genuinely memory-latency-bound on this machine.

## 3. Baseline

Two benchmark regimes were built to avoid an unrealistic result:

- **Cold**: each iteration queries a different random 2,000-key sample
  (cycling through 64 pre-generated samples), so the touched cache lines
  aren't already resident — approximates "a fresh batch of keys you haven't
  just looked up."
- **Hot**: every iteration repeats the *same* 2,000-key batch — approximates
  repeated/steady-state queries, where the touched region of the 9 MB array
  goes cache-resident after the first pass.

Naive baseline (calling the existing single-key `Map()` in a loop):

| | Cold | Hot |
|---|---|---|
| Naive `Map()` loop | 47.3–48.0 µs (≈23.9 ns/key) | 39.3–39.6 µs (≈19.7 ns/key) |

A CPU profile of the naive loop showed roughly 40% of time in hashing
(`xxhash.Sum64String` + the `mixsplit`/`murmur64` finalizer) and roughly 42%
flat in `Map` itself (the three array loads/XORs — i.e., cache-miss stalls),
confirming both hashing overhead and memory latency mattered.

## 4. Iteration log

Each trial's numbers were re-verified with `count≥3` and, once results got
subtle, with same-process A/B subtests (`b.Run`) to rule out thermal/boost
drift between separate `go test -bench` invocations — this laptop showed
real run-to-run drift, and one early "win" turned out to be exactly that
(see Trial 5).

### Trial 1 — Phase separation (kept)
Split each 256-key chunk into two passes: compute every key's three array
positions first (pure hashing, no memory stalls), *then* gather all their
values in a second, branch-light loop. This gives the CPU's out-of-order
engine a long run of mutually-independent loads instead of one blocked
behind the previous key's own hash computation.

Result: **~40% faster** than the naive loop (Cold ~28.3 µs, Hot ~23.8 µs).

### Trial 2 — Explicit ARM64 `PRFM` prefetch (reverted)
Wrote a hand-rolled ARM64 assembly stub issuing `PRFM PLDL1KEEP` for
addresses a few keys ahead of consumption. This made things *slower*
(66 µs). Cause: a non-`runtime` package can't mark assembly with
`<ABIInternal>`, so Go auto-generates an ABI0↔ABIInternal wrapper around any
plain assembly stub — every "one-instruction" prefetch call was actually two
nested calls. Reverted.

### Trial 3 — Chunk size tuning (kept, chunk=256 at this point)
Swept 32/64/128/256/512/1024/2000; all performed similarly (~27–30 µs)
except 1024 (a stack-growth anomaly — the local index arrays exceeded the
default 8 KB goroutine stack). Settled on 256 for now (later retuned, see
Trial 10).

### Trial 4 — Goroutine parallelism at 2,000 keys (limited; became `MapBatchParallel`)
Split the batch across goroutines (up to `GOMAXPROCS`). At 2,000 keys this
was *worse* than sequential — measured goroutine fan-out overhead alone
(spawn + `sync.WaitGroup`) at ~0.6–3.1 µs for 2–8 workers, before accounting
for core-wake latency, which exceeded the ~28 µs of actual work. Verified at
50,000 keys it *does* pay off (~2.2–2.9× speedup measured across the
session). Shipped as `MapBatchParallel` with a 20,000-key auto-fallback
threshold rather than discarding the idea.

### Trial 5 — Pure-Go XXH64 reimplementation (reverted; caught a false positive)
Wrote a bit-exact pure-Go XXH64 short-input path to avoid the cross-package
call into cespare's assembly. Swapping it into `Map()` appeared to cut
naive-loop time 41% (39 µs → 23 µs) — but that comparison was between two
separate `go test -bench` invocations run minutes apart. A same-process A/B
subtest showed the truth: cespare's hand-tuned assembly (~7.75 ns/key) was
actually *faster* than the pure-Go version (~9.3 ns/key). The apparent win
was thermal/boost-state drift. Reverted, and reported as a negative result
rather than a win.

### Trial 6 — Unsafe pointer gather (kept)
`h0/h1/h2` are always `< len(cm.data)` by construction (same invariant
`Map()` relies on), so the gather loop's bounds-checked slice indexing was
replaced with raw `unsafe.Add` pointer arithmetic (one explicit
length-precondition check up front, then unchecked in the hot loop).
Confirmed via `-gcflags=-B` that disabling bounds checks bought ~5%; the
`unsafe` rewrite captured that safely. Verified via disassembly that the
resulting gather loop is 3 single-instruction scaled loads + 2 XORs + 1
store per key, no branches.

Result: Cold ~27 µs, Hot ~21.5 µs (down from ~28.3/23.8 µs).

### Trial 7 — Three separate gather passes (reverted)
Tried splitting the interleaved `h0/h1/h2` gather into three separate
single-stride passes (all h0 loads, then h1, then h2), hoping for more
memory-level parallelism. Measured worse (33–35 µs) — the extra staging
arrays add real store/load traffic without buying overlap beyond what the
single interleaved loop already gets. Reverted.

### Trial 8 — "Dummy early touch" prefetch via plain loads (reverted)
Tried touching `cm.data[h0/h1/h2]` (discarding the value) as soon as each
index was computed in phase 1, reasoning that phase 1's own duration
(~1.8 µs for 256 keys) is much longer than one cache-miss latency
(~100 ns), so an early throwaway read should fully resolve before phase 2
needs the real value. First attempt used a single shared accumulator,
which serialized all the "dummy" loads into one dependency chain and made
things drastically worse (45+ µs); fixing that (independent per-iteration
slots) still made things worse (44–47 µs) — it just doubles real memory
traffic without adding overlap beyond what phase separation already
achieves. Reverted both variants.

### Trial 9 — Assembly-level check for further scalar/SIMD room
Disassembled the gather loop (`go build -gcflags=-S`) and confirmed it was
already near-optimal scalar code (see Trial 6). Checked for a SIMD lever on
the memory-gather step itself: ARM NEON has no gather-load instruction, and
this M1 doesn't implement SVE (which does), so there is no vectorization
opportunity for the "3 independent random loads per key" pattern on this
hardware.

At this point the session's summary understated one thing: it treated
cespare's xxhash call as a fixed cost. That's what the next intervention
challenged.

## 5. User intervention

> cespare's hand-tuned assembly (~7.75ns/key) beat my Go reimplementation
> (~9.3ns/key). why not see if we can beat cespare

This reframed the hashing question: not "can pure Go beat hand-tuned
assembly" (no), but "does cespare's *per-call* design leave anything on the
table when called thousands of times in a batch." It does — every call
reloads the five XXH64 prime constants from memory and pays Go's ABI0
call/return marshalling, regardless of input length.

### Trial 10 — Batched assembly XXH64 hasher (kept)

Wrote `hashKeysXXH64Short` (`hashbatch_arm64.s`): cespare's own tail-handling
instruction sequence (the part that correctly hashes any input under 32
bytes, copied verbatim — proven fast, not reinvented) hoisted into a loop
over many keys, so the five primes load into registers **once per call**
instead of once per key. Keys ≥32 bytes aren't supported by this routine and
fall back to the per-key path (checked once per chunk).

**Correctness, established before any benchmarking counted:**
- Exhaustive test against `xxhash.Sum64String` for every length 0–31 (8
  random trials each) plus 5,000 benchmark-shaped keys.
- Verified under `go vet` (`asmdecl` — frame offsets match the Go
  signature) and `go test -race`.
- Dedicated test exercising the ≥32-byte fallback path with keys of mixed
  lengths (including duplicates caught and fixed during test authoring).

**Isolated hash-only result** (same-process subtests): cespare per-key call
~8.5–9 ns/key → batched assembly ~6.25–6.4 ns/key. A real ~27–29% cut.

**Wiring it into `MapBatch` surfaced a genuine tradeoff, not noise**
(confirmed via same-process A/B, not separate invocations, learning from
Trial 5): hashing a full 256-key chunk in one call helped the hot case a lot
but *hurt* the cold case (~26.5→30.9 µs). Hypothesis: a single opaque
256-key assembly call is more of a scheduling wall than a Go loop of
per-key calls, reducing the out-of-order engine's ability to overlap one
chunk's tail hashing with the next chunk's head gather — which matters only
when gather latency (the cold case) is the bottleneck.

Swept the chunk size (which now controls both the hash-batching granularity
and the gather-chunk size) from 4 to 256 keys, re-measuring both regimes
each time. **8 keys** won outright — not a compromise, but strictly better
than the old chunk=256/per-key-hash code in *both* regimes simultaneously:

| Chunk size | Cold (Old → New) | Hot (Old → New) |
|---|---|---|
| 256 | 27.4 → 30.6 µs (worse) | 21.5 → 16.6 µs |
| 16 | 29.2 → 27.6 µs | 21.4 → 16.2 µs |
| **8** | **33.4 → 27.1 µs** | **20.4 → 15.9 µs** |
| 4 | 43.1 → 38.1 µs | 23.3 → 19.9 µs |

("Old" / "New" = per-key cespare hashing vs. batched-assembly hashing, at
that chunk size, measured in the same process.)

## 6. Final results

2,000-key batch against the 1,000,000-entry map, Apple M1 Max, `count=5`,
`benchtime=3s`:

| | Naive `Map()` loop | Final `MapBatch` | Improvement |
|---|---|---|---|
| **Cold** (fresh batch) | 47.3–48.0 µs (23.9 ns/key) | 26.9–27.7 µs (13.5 ns/key) | **~42% faster** |
| **Hot** (repeated batch) | 39.3–39.6 µs (19.7 ns/key) | 15.9–16.0 µs (8.0 ns/key) | **~59% faster** |

`MapBatchParallel` (auto-falls-back to `MapBatch` below 20,000 keys, since
goroutine fan-out overhead exceeds the work below that size) at 50,000 keys:

| | Sequential `MapBatch` | `MapBatchParallel` | Speedup |
|---|---|---|---|
| 50,000 keys | 752–784 µs | 265–272 µs | **~2.85–2.9×** |

All existing tests, `go vet`, and `go test -race` pass. No changes to the
existing public API (`New`, `Map`, `VerifiedConstMap`, serialization); the
new surface is purely additive: `(*ConstMap).MapBatch` and
`(*ConstMap).MapBatchParallel`.

## 7. What didn't make it in (and why)

- Explicit software prefetch via hand-written assembly (Trial 2) — killed
  by Go's ABI0/ABIInternal wrapper overhead for non-`runtime` packages.
- Pure-Go XXH64 reimplementation as a call-overhead workaround (Trial 5) —
  slower than cespare's assembly per call; the batched-assembly approach
  (Trial 10) is what actually worked, and it reuses cespare's instructions
  rather than replacing them.
- Three-pass split gather and "dummy early touch" prefetching (Trials 7–8)
  — both added real memory traffic without buying additional latency
  hiding beyond what phase separation (Trial 1) already achieves.
- SIMD/gather-load vectorization of the memory-access step — not available:
  ARM NEON has no gather-load instruction, and this CPU doesn't implement
  SVE.
- `VerifiedConstMap` batch path and construction-time (`New`/`NewVerified`)
  hashing were not touched in this session.

## 8. Caveats

- All numbers are specific to this Apple M1 Max laptop under the described
  Go toolchain and OS; relative gains should transfer to other Apple
  Silicon (arm64) machines but absolute timings will differ, and the
  batched-assembly hasher (`hashbatch_arm64.s`) is arm64-only — other
  architectures automatically fall back to the pre-existing per-key path
  (`hashbatch_other.go`).
- This machine showed real thermal/boost-state drift between separate
  benchmark invocations (see Trial 5). Every claim in this report that
  compares two variants was validated with a same-process A/B (`b.Run`
  subtests within one `go test -bench` invocation), not by comparing
  numbers from separate runs.
- `hashKeysXXH64Short` depends on bit-for-bit matching
  `github.com/cespare/xxhash/v2`'s algorithm and constants (pinned at
  v2.3.0 in `go.mod`). This is covered by an exhaustive correctness test
  (`TestHashKeysXXH64ShortMatchesReference`) that would fail immediately if
  that ever drifted.
