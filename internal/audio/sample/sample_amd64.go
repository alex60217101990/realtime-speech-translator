//go:build amd64

package sample

// Dispatcher for the amd64 path. Both routines hand the bulk of work
// to SSE2 assembly (8 samples per iteration) and clean up the tail
// with the scalar implementation.
//
// SSE2 is part of the x86-64 baseline (GOAMD64=v1), so no CPUID
// detection is required — these functions are always safe to call on
// any amd64 host.

func int16ToFloat32(src []int16, dst []float32) {
	n := len(src)
	if n == 0 {
		return
	}
	// Process len(src) rounded down to 8 with SSE2; tail with scalar.
	vec := n &^ 7
	if vec > 0 {
		int16ToFloat32SSE2(&src[0], &dst[0], vec)
	}
	if vec < n {
		int16ToFloat32Scalar(src[vec:], dst[vec:])
	}
}

func float32ToInt16(src []float32, dst []int16) {
	n := len(src)
	if n == 0 {
		return
	}
	vec := n &^ 7
	if vec > 0 {
		float32ToInt16SSE2(&src[0], &dst[0], vec)
	}
	if vec < n {
		float32ToInt16Scalar(src[vec:], dst[vec:])
	}
}

// int16ToFloat32SSE2 converts n (must be a multiple of 8) int16
// samples to normalized float32. Implementation in sample_amd64.s.
//
//go:noescape
func int16ToFloat32SSE2(src *int16, dst *float32, n int)

// float32ToInt16SSE2 converts n (must be a multiple of 8) float32
// samples to S16, saturating to the int16 range. Implementation in
// sample_amd64.s.
//
//go:noescape
func float32ToInt16SSE2(src *float32, dst *int16, n int)
