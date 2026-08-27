//go:build !purego

#include "textflag.h"

// Registers. Deliberately avoids R15, which the dynamic linker clobbers when
// building with -dynlink or -shared, and which the assembler rejects being
// live across a global access. Walking pointers rather than an index times
// sixteen keeps the working set to twelve registers so that constraint costs
// nothing.
#define prime1 R13
#define prime2 R14
#define prime3 R12
#define prime4 DI
#define prime5 R11

#define p   SI // pointer into the current key
#define end BX // one past the last byte of the current key, adjusted per phase
#define h   AX // running digest
#define x   DX // scratch: the key length, then the hash temporary

#define kp    R8  // walks the keys slice, sixteen bytes per string header
#define kpEnd R9  // one past the last string header
#define outp  R10 // walks the out slice

// round0 performs x = round(0, x), the same macro cespare's xxhash_amd64.s
// defines.
#define round0(x) \
	IMULQ prime2, x \
	ROLQ  $31, x    \
	IMULQ prime1, x

// func hashKeysXXH64Short(keys []string, out []uint64)
//
// Precondition: every keys[i] must have len(keys[i]) < 32. If violated, out[i]
// for that key is wrong (garbage with respect to the true XXH64 value) but no
// out-of-bounds access occurs, because the loops are still bounded by each
// key's own length. Callers must verify lengths first; hashManyShort does.
//
// For inputs under 32 bytes this is bit-for-bit identical to
// github.com/cespare/xxhash/v2.Sum64String. It is that function's own tail
// cascade -- xxhash_amd64.s from the noBlocks label through finalize -- hoisted
// into a loop over many keys, so the five prime constants are loaded into
// registers once per call instead of once per key, and the whole batch pays
// one call and return rather than one per key. Keys of 32 bytes or more would
// need the four-accumulator block loop, which is deliberately not included:
// it is the part that does not benefit from batching, and leaving it out keeps
// every prime in a register.
//
// This is plain scalar x86-64 with no feature gate. See the package
// documentation for why the multiply-and-rotate chain does not vectorise
// usefully for short keys of differing lengths.
TEXT ·hashKeysXXH64Short(SB), NOSPLIT|NOFRAME, $0-48
	MOVQ keys_base+0(FP), kp
	MOVQ keys_len+8(FP), x
	MOVQ out_base+24(FP), outp

	TESTQ x, x
	JEQ   done

	// kpEnd = kp + 16*len(keys); string headers are two words.
	SHLQ $4, x
	LEAQ (kp)(x*1), kpEnd

	MOVQ ·xxhPrimesBatch+0(SB), prime1
	MOVQ ·xxhPrimesBatch+8(SB), prime2
	MOVQ ·xxhPrimesBatch+16(SB), prime3
	MOVQ ·xxhPrimesBatch+24(SB), prime4
	MOVQ ·xxhPrimesBatch+32(SB), prime5

keyLoop:
	MOVQ 0(kp), p // string data pointer
	MOVQ 8(kp), x // string length
	ADDQ $16, kp

	// h = prime5 + len, the noBlocks entry point.
	MOVQ prime5, h
	ADDQ x, h

	// end = p + len - 8: the limit for whole eight-byte words.
	LEAQ (p)(x*1), end
	SUBQ $8, end
	CMPQ p, end
	JG   try4

loop8:
	MOVQ  (p), x
	ADDQ  $8, p
	round0(x)
	XORQ  x, h
	ROLQ  $27, h
	IMULQ prime1, h
	ADDQ  prime4, h
	CMPQ  p, end
	JLE   loop8

try4:
	ADDQ $4, end // end = p0 + len - 4
	CMPQ p, end
	JG   try1

	MOVL  (p), x // zero-extends into the full register
	ADDQ  $4, p
	IMULQ prime1, x
	XORQ  x, h
	ROLQ  $23, h
	IMULQ prime2, h
	ADDQ  prime3, h

try1:
	ADDQ $4, end // end = p0 + len
	CMPQ p, end
	JGE  finalize

loop1:
	MOVBQZX (p), x
	ADDQ    $1, p
	IMULQ   prime5, x
	XORQ    x, h
	ROLQ    $11, h
	IMULQ   prime1, h
	CMPQ    p, end
	JL      loop1

finalize:
	MOVQ  h, x
	SHRQ  $33, x
	XORQ  x, h
	IMULQ prime2, h
	MOVQ  h, x
	SHRQ  $29, x
	XORQ  x, h
	IMULQ prime3, h
	MOVQ  h, x
	SHRQ  $32, x
	XORQ  x, h

	MOVQ h, (outp)
	ADDQ $8, outp

	CMPQ kp, kpEnd
	JL   keyLoop

done:
	RET
