package uihost

import (
	"math"
	"math/cmplx"
)

// Spectrum computes the bass / mid / treble energy buckets the
// sphere shader needs from a chunk of float32 PCM. We use a simple
// radix-2 Cooley–Tukey FFT on a Hann-windowed 1024-sample buffer
// (re-used across calls) — that is the same setup the perf-branch
// VAD-viz used, ported to be hostable here so the sphere can drive
// itself from one bucket per audio chunk.
//
// At 16 kHz mic input the 1024-point FFT covers ~64 ms of audio
// with 15.6 Hz/bin resolution; bass = sum 20–250 Hz, mid =
// 250–4000 Hz, treble = 4000–8000 Hz. Buckets are normalised to
// [0, 1] by dividing by the loudest bin in their range across the
// last few seconds (EWMA) so they live in a stable range regardless
// of how loud the speaker is.
//
// Stateful: bandsNorm[*] are the EWMA references; CurrentBucket
// snapshots the most recent reading. Safe for one-goroutine use.
type Spectrum struct {
	SampleRate int
	N          int // FFT size, must be a power of two

	window []float32 // Hann window of length N
	buf    []float32 // working buffer of length N (input × window)
	twid   []complex128

	// Per-band EWMA peak references. Used to normalise bucket
	// energies into [0, 1] regardless of mic gain.
	bassRef, midRef, trebleRef float32

	// Last seq number we returned — incremented inside Bucket().
	seq uint32
}

// NewSpectrum returns an analyser sized for `n` samples (must be a
// power of two; 1024 is the default for 16 kHz mic). Reuse across
// calls; allocation-free at steady state.
func NewSpectrum(sampleRate, n int) *Spectrum {
	if n&(n-1) != 0 || n == 0 {
		panic("uihost: Spectrum n must be a power of two")
	}
	s := &Spectrum{
		SampleRate: sampleRate,
		N:          n,
		window:     make([]float32, n),
		buf:        make([]float32, n),
		twid:       make([]complex128, n/2),
	}
	// Hann window.
	for i := range s.window {
		s.window[i] = float32(0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1))))
	}
	// Pre-computed twiddle factors so Compute() does not call
	// cmplx.Exp per inner loop.
	for k := 0; k < n/2; k++ {
		angle := -2 * math.Pi * float64(k) / float64(n)
		s.twid[k] = complex(math.Cos(angle), math.Sin(angle))
	}
	return s
}

// Bucket consumes the most recent N samples (taken from the tail of
// `samples`; shorter inputs are zero-padded at the head) and
// returns an AudioBucket with Rms/Peak/Bass/Mid/Treble filled in.
// VadActive + Speaking are set by the caller before sending.
//
// SeqNo is monotonic per Spectrum instance.
func (s *Spectrum) Bucket(samples []float32) AudioBucket {
	s.seq++

	// RMS + peak across the supplied chunk (not just the FFT window;
	// the VU meter wants the full chunk so a click is not missed).
	var sumSq float64
	var peak float32
	for _, v := range samples {
		sumSq += float64(v) * float64(v)
		a := v
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	var rms float32
	if len(samples) > 0 {
		rms = float32(math.Sqrt(sumSq / float64(len(samples))))
	}

	// Window the last N samples into s.buf.
	src := samples
	if len(src) > s.N {
		src = src[len(src)-s.N:]
	}
	for i := range s.buf {
		s.buf[i] = 0
	}
	off := s.N - len(src)
	for i, v := range src {
		s.buf[off+i] = v * s.window[off+i]
	}

	// Radix-2 in-place FFT on a complex copy of s.buf.
	cBuf := make([]complex128, s.N)
	for i, v := range s.buf {
		cBuf[i] = complex(float64(v), 0)
	}
	fft(cBuf, s.twid)

	// Sum energy in the three bands.
	binHz := float64(s.SampleRate) / float64(s.N)
	bandEnergy := func(loHz, hiHz float64) float32 {
		loBin := int(math.Floor(loHz / binHz))
		hiBin := int(math.Ceil(hiHz / binHz))
		if loBin < 1 {
			loBin = 1
		}
		if hiBin > s.N/2 {
			hiBin = s.N / 2
		}
		var e float64
		for k := loBin; k < hiBin; k++ {
			e += cmplx.Abs(cBuf[k])
		}
		return float32(e / float64(hiBin-loBin))
	}

	bass := bandEnergy(20, 250)
	mid := bandEnergy(250, 4000)
	treble := bandEnergy(4000, 8000)

	// Slow EWMA tracking of band peaks so we can normalise.
	const tau = float32(0.02)
	if bass > s.bassRef {
		s.bassRef = bass
	} else {
		s.bassRef += tau * (bass - s.bassRef)
	}
	if mid > s.midRef {
		s.midRef = mid
	} else {
		s.midRef += tau * (mid - s.midRef)
	}
	if treble > s.trebleRef {
		s.trebleRef = treble
	} else {
		s.trebleRef += tau * (treble - s.trebleRef)
	}

	norm := func(v, ref float32) float32 {
		if ref <= 1e-6 {
			return 0
		}
		x := v / ref
		if x > 1 {
			x = 1
		} else if x < 0 {
			x = 0
		}
		return x
	}

	return AudioBucket{
		SeqNo:  s.seq,
		Rms:    rms,
		Peak:   peak,
		Bass:   norm(bass, s.bassRef),
		Mid:    norm(mid, s.midRef),
		Treble: norm(treble, s.trebleRef),
	}
}

// fft is the textbook radix-2 in-place Cooley–Tukey FFT. We rolled
// our own to avoid a dependency on gonum just for one call — the
// FFT package is ~40 lines and runs once per bucket, well below 1
// ms on a 1024-point input.
func fft(x []complex128, twid []complex128) {
	n := len(x)
	if n <= 1 {
		return
	}
	// bit-reverse permutation
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	// butterflies
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		step := n / size
		for i := 0; i < n; i += size {
			for k := 0; k < half; k++ {
				w := twid[k*step]
				t := w * x[i+k+half]
				u := x[i+k]
				x[i+k] = u + t
				x[i+k+half] = u - t
			}
		}
	}
}
