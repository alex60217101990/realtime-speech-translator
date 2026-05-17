//go:build goexperiment.simd && amd64

package sample

import "simd/archsimd"

// hasAVX2 caches the runtime AVX2 capability so the hot path avoids a
// repeated method call. Initialized at package load.
var hasAVX2 = archsimd.X86.AVX2()

// Int16ToFloat32 normalizes signed 16-bit PCM samples into [-1.0, 1.0).
//
// Uses an AVX2 path processing 8 samples per iteration on supported CPUs:
//
//	Int16x8 -> Int32x8 (ExtendToInt32) -> Float32x8 (ConvertToFloat32) ->
//	    Mul(1/32768) -> StoreSlice
//
// Falls back to scalar for the tail and on CPUs without AVX2.
func Int16ToFloat32(dst []float32, src []int16) {
	if len(dst) < len(src) {
		panicShortDst()
	}
	n := len(src)
	i := 0
	if hasAVX2 {
		scale := archsimd.BroadcastFloat32x8(invInt16Scale)
		for ; i+8 <= n; i += 8 {
			v := archsimd.LoadInt16x8Slice(src[i:])
			f := v.ExtendToInt32().ConvertToFloat32().Mul(scale)
			f.StoreSlice(dst[i:])
		}
	}
	for ; i < n; i++ {
		dst[i] = float32(src[i]) * invInt16Scale
	}
}

// Float32ToInt16 converts float32 samples to int16 with saturating clip.
//
// Scalar implementation for now: the AVX2 path requires saturating pack
// (PACKSSDW) which the current simd/archsimd API exposes only for AVX-512
// MaskedConvert variants. A correct, branch-free AVX2 implementation is
// possible but is deferred until a hot profile justifies the maintenance
// cost — Float32ToInt16 runs only on TTS playback frames (~22 kHz, mono),
// so it is far cheaper than Int16ToFloat32 in the capture hot path.
func Float32ToInt16(dst []int16, src []float32) {
	if len(dst) < len(src) {
		panicShortDst()
	}
	for i, v := range src {
		s := v * 32768.0
		switch {
		case s > 32767.0:
			dst[i] = 32767
		case s < -32768.0:
			dst[i] = -32768
		default:
			dst[i] = int16(s)
		}
	}
}
