package dsp

import (
	"math"
	"testing"
)

// rms returns sqrt(mean(x^2)).
func rms(x []float32) float64 {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}

// sine generates len samples of a sine at the given frequency.
func sine(n, sr int, freq float64, amp float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = amp * float32(math.Sin(2*math.Pi*freq*float64(i)/float64(sr)))
	}
	return out
}

func TestHighPass_AttenuatesBelowCutoff(t *testing.T) {
	const sr = 16000
	hp := HighPass(sr, 300)
	low := sine(sr, sr, 100, 0.5)  // 100 Hz, should be killed
	high := sine(sr, sr, 1000, 0.5) // 1 kHz, should pass

	hp1 := hp
	hp1.ProcessInPlace(low)
	rmsLow := rms(low)

	hp2 := hp
	hp2.ProcessInPlace(high)
	rmsHigh := rms(high)

	if rmsLow > 0.1 {
		t.Errorf("HP at 300 Hz failed to attenuate 100 Hz: RMS=%v", rmsLow)
	}
	if rmsHigh < 0.20 {
		t.Errorf("HP at 300 Hz over-attenuated 1 kHz: RMS=%v", rmsHigh)
	}
}

func TestLowPass_AttenuatesAboveCutoff(t *testing.T) {
	const sr = 16000
	lp := LowPass(sr, 3400)
	lowMid := sine(sr, sr, 1000, 0.5) // should pass
	high := sine(sr, sr, 6000, 0.5)   // should be killed

	lp1 := lp
	lp1.ProcessInPlace(lowMid)
	rmsMid := rms(lowMid)

	lp2 := lp
	lp2.ProcessInPlace(high)
	rmsHigh := rms(high)

	if rmsMid < 0.20 {
		t.Errorf("LP at 3400 Hz over-attenuated 1 kHz: RMS=%v", rmsMid)
	}
	if rmsHigh > 0.20 {
		t.Errorf("LP at 3400 Hz failed to attenuate 6 kHz: RMS=%v", rmsHigh)
	}
}

func TestBandpass_PassesTelephonyBand(t *testing.T) {
	const sr = 16000
	bp := NewTelephonyBandpass(sr)
	passing := sine(sr, sr, 1000, 0.5)
	bp.ProcessInPlace(passing)
	if r := rms(passing); r < 0.18 {
		t.Errorf("Telephony BP killed 1 kHz tone: RMS=%v", r)
	}
}

func TestPreEmphasis_BoostsHighFrequencies(t *testing.T) {
	const sr = 16000
	low := sine(sr, sr, 200, 0.5)
	high := sine(sr, sr, 6000, 0.5)

	pe := PreEmphasis{Alpha: 0.97}
	preLow := rms(low)
	pe.ProcessInPlace(low)
	postLow := rms(low)

	pe2 := PreEmphasis{Alpha: 0.97}
	preHigh := rms(high)
	pe2.ProcessInPlace(high)
	postHigh := rms(high)

	// Low frequencies attenuated, highs preserved or boosted.
	if postLow > preLow*0.5 {
		t.Errorf("pre-emphasis did not attenuate 200 Hz enough: %.3f → %.3f", preLow, postLow)
	}
	if postHigh < preHigh*0.8 {
		t.Errorf("pre-emphasis attenuated 6 kHz too much: %.3f → %.3f", preHigh, postHigh)
	}
}

func TestAGC_NudgesTowardTarget(t *testing.T) {
	const sr = 16000
	// Quiet signal (RMS ~0.01).
	quiet := sine(sr, sr, 1000, 0.014)
	agc := AGC{TargetRMS: 0.10, MaxGain: 8, MinRMS: 0.005, Tau: 0.5}
	// Run several chunks to let EWMA settle.
	chunk := 1600
	for off := 0; off+chunk <= len(quiet); off += chunk {
		agc.ProcessInPlace(quiet[off : off+chunk])
	}
	// After settling, last chunk RMS should be in the target neighbourhood.
	last := quiet[len(quiet)-chunk:]
	r := rms(last)
	if r < 0.04 || r > 0.20 {
		t.Errorf("AGC failed to nudge into [0.04, 0.20]: got RMS=%v", r)
	}
}

func TestAGC_DoesNotPumpSilence(t *testing.T) {
	const sr = 16000
	silence := make([]float32, sr) // all zeros
	agc := AGC{TargetRMS: 0.10, MaxGain: 8, MinRMS: 0.005, Tau: 0.5}
	for off := 0; off+1600 <= len(silence); off += 1600 {
		agc.ProcessInPlace(silence[off : off+1600])
	}
	if r := rms(silence); r > 0.001 {
		t.Errorf("AGC pumped silence into noise: RMS=%v", r)
	}
}
