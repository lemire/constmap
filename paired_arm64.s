//go:build !purego

#include "textflag.h"

// func gatherPaired(slots []uint64, h0, h1, h2 *[8]uint32, hashes *[8]uint64, out *[8]uint64)
//
// For each j: load the three 16-byte slots at positions h0[j], h1[j], h2[j]
// as vectors, XOR them, and store the low word (the value) to out[j] if the
// high word (the fingerprint) equals hashes[j] ^ (hashes[j] >> 32), else
// store NotFound (all ones).
TEXT ·gatherPaired(SB), NOSPLIT, $0-64
	MOVD slots_base+0(FP), R0
	MOVD h0+24(FP), R1
	MOVD h1+32(FP), R2
	MOVD h2+40(FP), R3
	MOVD hashes+48(FP), R4
	MOVD out+56(FP), R5
	MOVD $8, R6

loop:
	MOVWU (R1), R7
	MOVWU (R2), R8
	MOVWU (R3), R9
	// A slot is 16 bytes, so position h lives at slots + 16*h.
	ADD R7<<4, R0, R7
	ADD R8<<4, R0, R8
	ADD R9<<4, R0, R9
	VLD1 (R7), [V0.D2]
	VLD1 (R8), [V1.D2]
	VLD1 (R9), [V2.D2]
	VEOR V1.B16, V0.B16, V0.B16
	VEOR V2.B16, V0.B16, V0.B16
	VMOV V0.D[0], R10
	VMOV V0.D[1], R11

	MOVD (R4), R12
	EOR  R12>>32, R12, R12
	CMP  R11, R12
	// R10 = value if the fingerprints match, else ^0 (NotFound).
	CSINV EQ, R10, ZR, R10
	MOVD R10, (R5)

	ADD $4, R1
	ADD $4, R2
	ADD $4, R3
	ADD $8, R4
	ADD $8, R5
	SUB $1, R6
	CBNZ R6, loop
	RET
