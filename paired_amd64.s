//go:build !purego

#include "textflag.h"

// func gatherPaired(slots []uint64, h0, h1, h2 *[8]uint32, hashes *[8]uint64, out *[8]uint64)
//
// For each j: load the three 16-byte slots at positions h0[j], h1[j], h2[j]
// as vectors, XOR them, and store the low word (the value) to out[j] if the
// high word (the fingerprint) equals hashes[j] ^ (hashes[j] >> 32), else
// store NotFound (all ones). SSE2 only, which every amd64 CPU has.
TEXT ·gatherPaired(SB), NOSPLIT, $0-64
	MOVQ slots_base+0(FP), AX
	MOVQ h0+24(FP), BX
	MOVQ h1+32(FP), CX
	MOVQ h2+40(FP), DX
	MOVQ hashes+48(FP), SI
	MOVQ out+56(FP), DI
	MOVQ $8, R8

loop:
	MOVL (BX), R9
	MOVL (CX), R10
	MOVL (DX), R11
	// A slot is 16 bytes, so position h lives at slots + 16*h.
	SHLQ $4, R9
	SHLQ $4, R10
	SHLQ $4, R11
	MOVOU (AX)(R9*1), X0
	MOVOU (AX)(R10*1), X1
	MOVOU (AX)(R11*1), X2
	PXOR  X1, X0
	PXOR  X2, X0
	MOVQ  X0, R12
	PSHUFD $0xEE, X0, X1
	MOVQ  X1, R13

	MOVQ (SI), R9
	MOVQ R9, R10
	SHRQ $32, R10
	XORQ R10, R9
	MOVQ $-1, R10
	CMPQ R13, R9
	// R12 = value if the fingerprints match, else -1 (NotFound).
	CMOVQNE R10, R12
	MOVQ R12, (DI)

	ADDQ $4, BX
	ADDQ $4, CX
	ADDQ $4, DX
	ADDQ $8, SI
	ADDQ $8, DI
	DECQ R8
	JNZ  loop
	RET
