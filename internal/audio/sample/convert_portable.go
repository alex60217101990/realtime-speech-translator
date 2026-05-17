//go:build !goexperiment.simd || !amd64

package sample

// Int16ToFloat32 normalizes signed 16-bit PCM samples into the float32
// range [-1.0, 1.0). dst must have len(src) elements; the function panics
// otherwise.
//
// Portable scalar implementation; selected when SIMD is not available.
func Int16ToFloat32(dst []float32, src []int16) {
	if len(dst) < len(src) {
		panicShortDst()
	}
	for i, v := range src {
		dst[i] = float32(v) * invInt16Scale
	}
}

// Float32ToInt16 converts a float32 sample stream back to signed 16-bit
// PCM with saturating clip. dst must have len(src) elements.
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

