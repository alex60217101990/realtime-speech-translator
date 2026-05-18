package sample

import (
	"math/rand"
	"testing"
)

// TestInt16ToFloat32_MatchesScalar covers the SIMD path against the
// scalar reference on a range of lengths that exercise both the
// vector body and the scalar tail handler.
func TestInt16ToFloat32_MatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	for _, n := range []int{0, 1, 4, 7, 8, 9, 15, 16, 17, 31, 32, 100, 4099} {
		n := n
		src := make([]int16, n)
		for i := range src {
			src[i] = int16(rng.Int31n(65536) - 32768)
		}
		gotVec := make([]float32, n)
		gotScalar := make([]float32, n)

		Int16ToFloat32(src, gotVec)
		int16ToFloat32Scalar(src, gotScalar)

		for i := range src {
			if gotVec[i] != gotScalar[i] {
				t.Fatalf("n=%d index=%d: vec=%v scalar=%v src=%v", n, i, gotVec[i], gotScalar[i], src[i])
			}
		}
	}
}

// TestFloat32ToInt16_MatchesScalarWithinOne tolerates a ±1 LSB
// difference because the SSE2 path uses MXCSR round-to-nearest-even
// while the scalar fallback truncates.
func TestFloat32ToInt16_MatchesScalarWithinOne(t *testing.T) {
	rng := rand.New(rand.NewSource(2))

	for _, n := range []int{0, 1, 4, 7, 8, 9, 15, 16, 17, 31, 100, 4099} {
		n := n
		src := make([]float32, n)
		for i := range src {
			src[i] = rng.Float32()*2 - 1 // [-1, 1]
		}
		gotVec := make([]int16, n)
		gotScalar := make([]int16, n)

		Float32ToInt16(src, gotVec)
		float32ToInt16Scalar(src, gotScalar)

		for i := range src {
			diff := int(gotVec[i]) - int(gotScalar[i])
			if diff < -1 || diff > 1 {
				t.Fatalf("n=%d index=%d: vec=%v scalar=%v src=%v (diff=%d)",
					n, i, gotVec[i], gotScalar[i], src[i], diff)
			}
		}
	}
}

// TestFloat32ToInt16_Saturates verifies the inputs outside [-1, 1]
// don't wrap around: clipping must produce the int16 saturation
// limits, not a sign-flipped artefact.
func TestFloat32ToInt16_Saturates(t *testing.T) {
	src := []float32{
		// Padded to a multiple of 8 so both the SIMD body and the
		// tail handler see the limit cases.
		2.0, -2.0, 1.5, -1.5, 1.0001, -1.0001, 0.5, -0.5,
		3.0, -3.0, 10.0, -10.0, 1.0, -1.0, 0, 0,
	}
	dst := make([]int16, len(src))
	Float32ToInt16(src, dst)

	checks := map[int]int16{0: 32767, 1: -32768, 2: 32767, 3: -32768, 4: 32767, 5: -32768}
	for i, want := range checks {
		if dst[i] != want {
			t.Errorf("index=%d: got %d, want %d (saturation)", i, dst[i], want)
		}
	}
}

// Benchmarks compare the SIMD body against the scalar reference at a
// realistic batch size (one second of 16 kHz mono mic audio).

func BenchmarkInt16ToFloat32_SIMD(b *testing.B) {
	src := make([]int16, 16000)
	dst := make([]float32, 16000)
	for i := range src {
		src[i] = int16((i * 1023) % 32768)
	}
	b.SetBytes(int64(len(src) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Int16ToFloat32(src, dst)
	}
}

func BenchmarkInt16ToFloat32_Scalar(b *testing.B) {
	src := make([]int16, 16000)
	dst := make([]float32, 16000)
	for i := range src {
		src[i] = int16((i * 1023) % 32768)
	}
	b.SetBytes(int64(len(src) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		int16ToFloat32Scalar(src, dst)
	}
}

func BenchmarkFloat32ToInt16_SIMD(b *testing.B) {
	src := make([]float32, 22050)
	dst := make([]int16, 22050)
	for i := range src {
		src[i] = float32(i%32768)/32768.0 - 0.5
	}
	b.SetBytes(int64(len(src) * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Float32ToInt16(src, dst)
	}
}

func BenchmarkFloat32ToInt16_Scalar(b *testing.B) {
	src := make([]float32, 22050)
	dst := make([]int16, 22050)
	for i := range src {
		src[i] = float32(i%32768)/32768.0 - 0.5
	}
	b.SetBytes(int64(len(src) * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		float32ToInt16Scalar(src, dst)
	}
}
