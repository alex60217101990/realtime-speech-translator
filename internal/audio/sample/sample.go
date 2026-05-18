// Package sample converts between the two PCM formats the
// realtime-speech-translator audio path actually touches:
//
//   - int16 little-endian — the format every host capture driver
//     hands out and the format malgo gives the capture callback.
//   - float32 in [-1, 1] — the format sherpa-onnx wants on the way in
//     (recognizer + VAD) and gives back on the way out (Piper TTS).
//
// The conversion is one of the hottest loops in the application: at
// 16 kHz mono mic capture we process 16 000 samples/s; at 22 050 Hz
// TTS playback another 22 050 samples/s. Doing the naive
// `float32(s) / 32768` in Go costs more cycles than every other
// per-sample step combined.
//
// This package exposes two entry points and ships a SIMD
// implementation (SSE2 on amd64; portable scalar elsewhere). The two
// functions are fully interchangeable with the portable scalar
// versions — same length contract, identical rounding modulo IEEE-754
// floating-point error in the last bit on the int→float path.
package sample

// Int16ToFloat32 converts S16LE samples into normalized float32 in
// [-1, 1] (-32768 maps to -1, 32767 maps to ~0.99997). The dst slice
// must have at least len(src) capacity; only len(src) entries are
// written.
func Int16ToFloat32(src []int16, dst []float32) {
	if len(dst) < len(src) {
		panic("sample: Int16ToFloat32 dst shorter than src")
	}
	int16ToFloat32(src, dst)
}

// Float32ToInt16 converts normalized float32 audio in [-1, 1] back to
// S16LE, clamping samples that drift outside the unit interval
// (Piper occasionally overshoots by 1–2 % on plosives) so we never
// wrap around to the opposite sign.
func Float32ToInt16(src []float32, dst []int16) {
	if len(dst) < len(src) {
		panic("sample: Float32ToInt16 dst shorter than src")
	}
	float32ToInt16(src, dst)
}

// int16ToFloat32Scalar is the portable reference implementation. It
// is exported only via the build-tagged dispatcher below; tests
// compare the SIMD output against it sample-by-sample.
func int16ToFloat32Scalar(src []int16, dst []float32) {
	const scale = 1.0 / 32768.0
	// Loop unrolled by 4 — the Go compiler can't autovectorise int16
	// loads as of 1.26, but unrolling removes a branch per sample and
	// lets the scheduler issue independent FP MULs.
	n := len(src)
	i := 0
	for ; i+4 <= n; i += 4 {
		dst[i] = float32(src[i]) * scale
		dst[i+1] = float32(src[i+1]) * scale
		dst[i+2] = float32(src[i+2]) * scale
		dst[i+3] = float32(src[i+3]) * scale
	}
	for ; i < n; i++ {
		dst[i] = float32(src[i]) * scale
	}
}

func float32ToInt16Scalar(src []float32, dst []int16) {
	n := len(src)
	i := 0
	for ; i+4 <= n; i += 4 {
		dst[i] = clampToI16(src[i])
		dst[i+1] = clampToI16(src[i+1])
		dst[i+2] = clampToI16(src[i+2])
		dst[i+3] = clampToI16(src[i+3])
	}
	for ; i < n; i++ {
		dst[i] = clampToI16(src[i])
	}
}

// clampToI16 saturates a float sample to the int16 range. We use
// 32767 (not 32768) as the positive limit to match the convention
// used by every wav editor on the planet and to keep the symmetric
// rounding tidy.
func clampToI16(f float32) int16 {
	v := f * 32767.0
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
