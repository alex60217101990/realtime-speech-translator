// SSE2 implementations of the int16 ↔ float32 conversion loops.
// Both routines process 8 samples per iteration and require n to be a
// multiple of 8 — the Go-side dispatcher guarantees that and handles
// any tail with the scalar fallback.
//
// SSE2 is mandatory for amd64 (GOAMD64=v1), so there is no CPUID
// gating: every host that runs this binary can execute these
// instructions.

#include "textflag.h"

// ----------------------------------------------------------------------------
// int16ToFloat32SSE2(src *int16, dst *float32, n int)
//
// Algorithm (per iteration):
//
//   X0 = unaligned load of 8 int16 from src
//   sign-extend low 4 lanes  → X0 as 4 int32
//   sign-extend high 4 lanes → X1 as 4 int32
//   convert both to float32, multiply by 1/32768
//   store 16 + 16 bytes to dst.
// ----------------------------------------------------------------------------

DATA  scaleI16Tof32<>+0(SB)/4,  $0x38000000   // 1.0 / 32768.0
DATA  scaleI16Tof32<>+4(SB)/4,  $0x38000000
DATA  scaleI16Tof32<>+8(SB)/4,  $0x38000000
DATA  scaleI16Tof32<>+12(SB)/4, $0x38000000
GLOBL scaleI16Tof32<>(SB), RODATA|NOPTR, $16

TEXT ·int16ToFloat32SSE2(SB), NOSPLIT, $0-24
	MOVQ  src+0(FP),  SI
	MOVQ  dst+8(FP),  DI
	MOVQ  n+16(FP),   CX

	MOVOU scaleI16Tof32<>(SB), X2

	XORQ  AX, AX
loop_i16:
	MOVOU (SI)(AX*2), X0           // X0 = [s0..s7] as int16
	MOVO  X0, X1

	PUNPCKLWL X0, X0                // [s0,s0,s1,s1,s2,s2,s3,s3]
	PSRAL     $16, X0               // arithmetic shift → 4× signed int32

	PUNPCKHWL X1, X1                // [s4,s4,...,s7,s7]
	PSRAL     $16, X1

	CVTPL2PS X0, X0
	CVTPL2PS X1, X1

	MULPS X2, X0
	MULPS X2, X1

	MOVOU X0, (DI)(AX*4)
	MOVOU X1, 16(DI)(AX*4)

	ADDQ $8, AX
	CMPQ AX, CX
	JL   loop_i16
	RET

// ----------------------------------------------------------------------------
// float32ToInt16SSE2(src *float32, dst *int16, n int)
//
// Algorithm (per iteration):
//
//   X0,X1 = 8 float32 from src
//   multiply by 32767 (positive max), then convert to int32 with the
//   current MXCSR rounding mode (round-nearest-even by default).
//   PACKSSDW saturates each 32→16-bit lane, so no manual clamp is
//   needed when the input strays slightly outside [-1, 1].
// ----------------------------------------------------------------------------

DATA  scalef32ToI16<>+0(SB)/4,  $0x46FFFE00  // 32767.0
DATA  scalef32ToI16<>+4(SB)/4,  $0x46FFFE00
DATA  scalef32ToI16<>+8(SB)/4,  $0x46FFFE00
DATA  scalef32ToI16<>+12(SB)/4, $0x46FFFE00
GLOBL scalef32ToI16<>(SB), RODATA|NOPTR, $16

TEXT ·float32ToInt16SSE2(SB), NOSPLIT, $0-24
	MOVQ  src+0(FP),  SI
	MOVQ  dst+8(FP),  DI
	MOVQ  n+16(FP),   CX

	MOVOU scalef32ToI16<>(SB), X2

	XORQ AX, AX
loop_f32:
	MOVOU (SI)(AX*4),  X0           // first 4 floats
	MOVOU 16(SI)(AX*4), X1          // next 4 floats

	MULPS X2, X0
	MULPS X2, X1

	CVTPS2PL X0, X0                 // 4× int32 (rounded)
	CVTPS2PL X1, X1

	PACKSSLW X1, X0                 // 8× int16, saturating

	MOVOU X0, (DI)(AX*2)

	ADDQ $8, AX
	CMPQ AX, CX
	JL   loop_f32
	RET
