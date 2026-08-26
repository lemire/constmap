#include "textflag.h"

// Registers, matching github.com/cespare/xxhash/v2's naming where this
// reuses their instruction sequences.
#define prime1 R7
#define prime2 R8
#define prime3 R9
#define prime4 R10
#define prime5 R11

#define p  R3
#define ln R4
#define h  R2
#define x1 R20
#define x2 R21
#define x3 R22
#define x4 R23

#define keysBase R12
#define outBase  R13
#define i        R14
#define nTotal   R15

#define round0(x) \
	MUL prime2, x \
	ROR $64-31, x \
	MUL prime1, x

// func hashKeysXXH64Short(keys []string, out []uint64)
//
// Precondition: every keys[i] must have len(keys[i]) < 32. If violated,
// out[i] for that key is wrong (garbage w.r.t. the true XXH64 value) but no
// out-of-bounds access occurs. Callers must verify lengths first.
//
// For inputs under 32 bytes this is bit-for-bit identical to
// github.com/cespare/xxhash/v2.Sum64String: it is that function's own
// tail-handling cascade (see xxhash_arm64.s's afterLoop through finalize),
// copied verbatim, but hoisted into a loop over many keys so the five xxHash
// prime constants are loaded into registers once per call instead of once
// per key, and the whole batch pays one call/return instead of one per key.
TEXT ·hashKeysXXH64Short(SB), NOSPLIT, $0-48
	MOVD keys_base+0(FP), keysBase
	MOVD keys_len+8(FP), nTotal
	MOVD out_base+24(FP), outBase

	CBZ nTotal, done

	LDP  ·xxhPrimesBatch+0(SB), (prime1, prime2)
	LDP  ·xxhPrimesBatch+16(SB), (prime3, prime4)
	MOVD ·xxhPrimesBatch+32(SB), prime5

	MOVD $0, i

loop:
	LSL $4, i, R16
	ADD R16, keysBase, R16
	LDP (R16), (p, ln)

	MOVD prime5, h
	ADD  ln, h

	TBZ   $4, ln, try8
	LDP.P 16(p), (x1, x2)

	round0(x1)
	ROR  $64-27, h
	EOR  x1 @> 64-27, h, h
	MADD h, prime4, prime1, h

	round0(x2)
	ROR  $64-27, h
	EOR  x2 @> 64-27, h, h
	MADD h, prime4, prime1, h

try8:
	TBZ    $3, ln, try4
	MOVD.P 8(p), x1

	round0(x1)
	ROR  $64-27, h
	EOR  x1 @> 64-27, h, h
	MADD h, prime4, prime1, h

try4:
	TBZ     $2, ln, try2
	MOVWU.P 4(p), x2

	MUL  prime1, x2
	ROR  $64-23, h
	EOR  x2 @> 64-23, h, h
	MADD h, prime3, prime2, h

try2:
	TBZ     $1, ln, try1
	MOVHU.P 2(p), x3
	AND     $255, x3, x1
	LSR     $8, x3, x2

	MUL prime5, x1
	ROR $64-11, h
	EOR x1 @> 64-11, h, h
	MUL prime1, h

	MUL prime5, x2
	ROR $64-11, h
	EOR x2 @> 64-11, h, h
	MUL prime1, h

try1:
	TBZ   $0, ln, finalize
	MOVBU (p), x4

	MUL prime5, x4
	ROR $64-11, h
	EOR x4 @> 64-11, h, h
	MUL prime1, h

finalize:
	EOR h >> 33, h
	MUL prime2, h
	EOR h >> 29, h
	MUL prime3, h
	EOR h >> 32, h

	LSL $3, i, R17
	ADD R17, outBase, R17
	MOVD h, (R17)

	ADD $1, i
	CMP i, nTotal
	BNE loop

done:
	RET
