//go:build !amd64

package sample

// Non-amd64 fallback: dispatch straight to the scalar loops.

func int16ToFloat32(src []int16, dst []float32) { int16ToFloat32Scalar(src, dst) }
func float32ToInt16(src []float32, dst []int16) { float32ToInt16Scalar(src, dst) }
