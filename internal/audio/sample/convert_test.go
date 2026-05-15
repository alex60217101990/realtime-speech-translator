package sample

import (
	"math"
	"testing"
)

func TestInt16ToFloat32Bounds(t *testing.T) {
	in := []int16{0, 1, -1, 16384, -16384, 32767, -32768}
	out := make([]float32, len(in))
	Int16ToFloat32(out, in)
	exp := []float32{
		0,
		1.0 / 32768,
		-1.0 / 32768,
		0.5,
		-0.5,
		32767.0 / 32768.0,
		-1.0,
	}
	for i := range in {
		if math.Abs(float64(out[i]-exp[i])) > 1e-7 {
			t.Fatalf("idx %d: in=%d got=%v want=%v", i, in[i], out[i], exp[i])
		}
	}
}

func TestInt16ToFloat32Bulk(t *testing.T) {
	// 30 samples = AVX2 path (24) + scalar tail (6).
	in := make([]int16, 30)
	for i := range in {
		in[i] = int16(i * 100)
	}
	out := make([]float32, len(in))
	Int16ToFloat32(out, in)
	for i, v := range in {
		want := float32(v) * invInt16Scale
		if out[i] != want {
			t.Fatalf("idx %d: got=%v want=%v", i, out[i], want)
		}
	}
}

func TestFloat32ToInt16Clip(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 1.0, -1.5, 0.999969482, -0.999969482}
	out := make([]int16, len(in))
	Float32ToInt16(out, in)
	exp := []int16{0, 16384, -16384, 32767, -32768, 32766, -32766}
	for i := range in {
		// Allow ±1 LSB tolerance due to rounding.
		d := int32(out[i]) - int32(exp[i])
		if d < -1 || d > 1 {
			t.Fatalf("idx %d: in=%v got=%d want=%d", i, in[i], out[i], exp[i])
		}
	}
}

func TestRoundTrip(t *testing.T) {
	in := make([]int16, 4096)
	for i := range in {
		in[i] = int16((i*31)%65536 - 32768)
	}
	mid := make([]float32, len(in))
	out := make([]int16, len(in))
	Int16ToFloat32(mid, in)
	Float32ToInt16(out, mid)
	for i := range in {
		d := int32(out[i]) - int32(in[i])
		if d < -1 || d > 1 {
			t.Fatalf("roundtrip drift at %d: %d -> %d", i, in[i], out[i])
		}
	}
}

func BenchmarkInt16ToFloat32(b *testing.B) {
	// 16000 Hz * 30ms = 480 samples.
	in := make([]int16, 480)
	out := make([]float32, 480)
	for i := range in {
		in[i] = int16(i)
	}
	b.SetBytes(int64(len(in) * 2))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Int16ToFloat32(out, in)
	}
}

func BenchmarkFloat32ToInt16(b *testing.B) {
	in := make([]float32, 480)
	out := make([]int16, 480)
	for i := range in {
		in[i] = float32(i) / 480.0
	}
	b.SetBytes(int64(len(in) * 4))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Float32ToInt16(out, in)
	}
}
