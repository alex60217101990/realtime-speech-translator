// Package dsp implements the small set of streaming-friendly audio
// preprocessing blocks the recognizer chain needs:
//
//   - Biquad IIR filter (RBJ Audio EQ Cookbook coefficients) with a
//     factory for high-pass, low-pass and band-pass responses. Used
//     to build a telephony-shaped band-pass that matches the
//     spectral envelope T-one was trained on (300–3400 Hz).
//   - 1-tap pre-emphasis y[n] = x[n] − α·x[n−1] (α ≈ 0.97). The
//     classic step every ASR feature extractor used before
//     log-Mel was standard; on cheap mics it boosts unvoiced
//     consonants the CTC head would otherwise miss.
//   - Streaming AGC (automatic gain control) that targets a fixed
//     RMS and rate-limits the gain change so we do not pump up
//     room tone into the signal during pauses.
//
// All blocks are stateful and process in place. They are wired into
// the STT hot-path so the audio handed to AcceptWaveform looks as
// close as possible to the model's training distribution.
package dsp

import "math"

// ----------------------------------------------------------------------------
// Biquad
// ----------------------------------------------------------------------------

// Biquad is a transposed-direct-form-II 2-pole/2-zero IIR section.
// y[n] = b0·x[n] + b1·x[n−1] + b2·x[n−2] − a1·y[n−1] − a2·y[n−2]
type Biquad struct {
	b0, b1, b2 float32
	a1, a2     float32
	z1, z2     float32
}

// ProcessInPlace runs the filter over samples in place.
func (b *Biquad) ProcessInPlace(samples []float32) {
	z1, z2 := b.z1, b.z2
	b0, b1, b2 := b.b0, b.b1, b.b2
	a1, a2 := b.a1, b.a2
	for i, x := range samples {
		y := b0*x + z1
		z1 = b1*x - a1*y + z2
		z2 = b2*x - a2*y
		samples[i] = y
	}
	b.z1, b.z2 = z1, z2
}

// HighPass returns a Butterworth Q=0.707 high-pass at fc.
func HighPass(sampleRate int, fc float32) Biquad {
	omega := 2 * math.Pi * float64(fc) / float64(sampleRate)
	cosO := math.Cos(omega)
	alpha := math.Sin(omega) / (2 * 0.707)

	b0 := (1 + cosO) / 2
	b1 := -(1 + cosO)
	b2 := (1 + cosO) / 2
	a0 := 1 + alpha
	a1 := -2 * cosO
	a2 := 1 - alpha

	return Biquad{
		b0: float32(b0 / a0), b1: float32(b1 / a0), b2: float32(b2 / a0),
		a1: float32(a1 / a0), a2: float32(a2 / a0),
	}
}

// LowPass returns a Butterworth Q=0.707 low-pass at fc.
func LowPass(sampleRate int, fc float32) Biquad {
	omega := 2 * math.Pi * float64(fc) / float64(sampleRate)
	cosO := math.Cos(omega)
	alpha := math.Sin(omega) / (2 * 0.707)

	b0 := (1 - cosO) / 2
	b1 := 1 - cosO
	b2 := (1 - cosO) / 2
	a0 := 1 + alpha
	a1 := -2 * cosO
	a2 := 1 - alpha

	return Biquad{
		b0: float32(b0 / a0), b1: float32(b1 / a0), b2: float32(b2 / a0),
		a1: float32(a1 / a0), a2: float32(a2 / a0),
	}
}

// ----------------------------------------------------------------------------
// Telephony band-pass (HP 300 Hz → LP 3400 Hz Butterworth pair)
// ----------------------------------------------------------------------------

// Bandpass is a fixed-shape telephony band-pass: two cascaded
// Butterworth biquads (HP 300 Hz, LP 3400 Hz). 12 dB / octave roll-
// off on each end; enough to push the input into the spectral
// envelope T-one trained on (G.711 narrow-band) without making the
// signal sound mangled to the ASR's mel filterbank.
type Bandpass struct {
	hp Biquad
	lp Biquad
}

// NewTelephonyBandpass returns a 300–3400 Hz band-pass at the
// given sample rate.
func NewTelephonyBandpass(sampleRate int) *Bandpass {
	return &Bandpass{
		hp: HighPass(sampleRate, 300),
		lp: LowPass(sampleRate, 3400),
	}
}

// ProcessInPlace applies HP then LP in place.
func (b *Bandpass) ProcessInPlace(samples []float32) {
	b.hp.ProcessInPlace(samples)
	b.lp.ProcessInPlace(samples)
}

// ----------------------------------------------------------------------------
// Pre-emphasis
// ----------------------------------------------------------------------------

// PreEmphasis is the classic 1-tap FIR y[n] = x[n] − α·x[n−1].
// Stateful: prev carries x[n−1] across chunks.
type PreEmphasis struct {
	Alpha float32 // typical 0.95–0.97
	prev  float32
}

// ProcessInPlace applies the filter in place.
func (p *PreEmphasis) ProcessInPlace(samples []float32) {
	if len(samples) == 0 {
		return
	}
	prev := p.prev
	a := p.Alpha
	for i, x := range samples {
		samples[i] = x - a*prev
		prev = x
	}
	p.prev = prev
}

// ----------------------------------------------------------------------------
// AGC (streaming, rolling-RMS targeting)
// ----------------------------------------------------------------------------

// AGC nudges the signal toward TargetRMS using a slow EWMA of the
// recent RMS as the gain reference. Rate-limited so we do not pump
// noise during pauses.
//
// Reasonable defaults for 16 kHz mic input feeding T-one:
//
//	TargetRMS = 0.10   (~-20 dBFS, telephony-typical level)
//	MaxGain   = 8      (don't amplify floor noise)
//	MinRMS    = 0.005  (below this we treat as silence; no gain)
//	Tau       = 0.05   (EWMA factor; 0.05 gives ~20-chunk window)
type AGC struct {
	TargetRMS float32
	MaxGain   float32
	MinRMS    float32
	Tau       float32

	// MaxStep caps fractional gain change per ProcessInPlace call
	// (0.20 default ≈ ±2 dB/chunk). Lower values give a slower,
	// more stable AGC — useful when the downstream model is
	// sensitive to abrupt level changes (T-one raw-PCM CTC).
	MaxStep float32

	emaRMS float32 // smoothed input RMS
	gain   float32 // smoothed gain applied last chunk
}

// CurrentGain returns the most recent applied gain (for UI / status).
func (a *AGC) CurrentGain() float32 { return a.gain }

// ProcessInPlace updates the gain estimate from the chunk's RMS and
// applies the (rate-limited) gain in place.
func (a *AGC) ProcessInPlace(samples []float32) {
	if len(samples) == 0 {
		return
	}
	if a.TargetRMS == 0 {
		a.TargetRMS = 0.10
	}
	if a.MaxGain == 0 {
		a.MaxGain = 8
	}
	if a.MinRMS == 0 {
		a.MinRMS = 0.005
	}
	if a.Tau == 0 {
		a.Tau = 0.05
	}
	if a.gain == 0 {
		a.gain = 1
	}

	// Chunk RMS.
	var sumSq float64
	for _, x := range samples {
		sumSq += float64(x) * float64(x)
	}
	chunkRMS := float32(math.Sqrt(sumSq / float64(len(samples))))

	// EWMA of input RMS.
	a.emaRMS = a.emaRMS + a.Tau*(chunkRMS-a.emaRMS)

	// Decide target gain. Below MinRMS we leave the signal alone —
	// otherwise the AGC will amplify silence into hiss.
	target := a.gain
	if a.emaRMS > a.MinRMS {
		target = a.TargetRMS / a.emaRMS
		if target > a.MaxGain {
			target = a.MaxGain
		}
		if target < 0.1 {
			target = 0.1
		}
	}
	// Rate-limit gain change. Default 20 % per chunk; overridable
	// via MaxStep so callers tuning for raw-PCM ASR models (T-one)
	// can pick a gentler ramp that does not pump the signal.
	step := a.MaxStep
	if step <= 0 {
		step = 0.20
	}
	up := 1 + step
	down := 1 - step
	if target > a.gain*up {
		target = a.gain * up
	} else if target < a.gain*down {
		target = a.gain * down
	}
	a.gain = target

	g := a.gain
	for i, x := range samples {
		v := x * g
		// Soft saturate to avoid hard clipping after gain.
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		samples[i] = v
	}
}
