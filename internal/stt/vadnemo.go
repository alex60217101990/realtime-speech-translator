package stt

// VadNemoEngine implements stt.Backend on top of sherpa-onnx's
// OfflineRecognizer in NeMo-CTC mode, gated by Silero VAD on the
// input side. It is the right backend for high-quality non-streaming
// Russian ASR (GigaAM v3 from Sber AI): we let the VAD cut natural
// speech segments, preprocess each segment with the same whisper.cpp-
// era pipeline (HPF 100 Hz → pre-emphasis 0.97 → peak-normalise),
// hand the whole segment to the NeMo CTC head in one AcceptWaveform
// call, then emit a Final.
//
// No partials — NeMo CTC decoders in offline mode have no streaming
// readout. Latency budget is `utterance length + ~0.3·utterance for
// decode` on a modern CPU, typically ~1.5 s end-to-end for normal
// conversational speech. For an MT-driven UX (we wait for a complete
// Russian phrase to translate anyway) that is the right trade for
// the huge quality jump over T-one on natural mic input.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/dsp"
)

// VadNemoConfig is the configuration of the VAD + offline NeMo CTC
// backend. Paths are mandatory; numeric knobs default to whisper.cpp-
// era values via DefaultVadNemoConfig().
type VadNemoConfig struct {
	// NeMo CTC model file (e.g. model.int8.onnx).
	NemoCTCModel string
	// tokens.txt path.
	Tokens string
	// Silero VAD model path.
	VADModel string

	// Audio + runtime knobs.
	SampleRate int    // 16000
	NumThreads int    // 0 → max(NumCPU/2, 2)
	Provider   string // "cpu"

	// VAD knobs (Silero ONNX).
	VADThreshold     float32 // 0.5
	VADMinSilenceSec float32 // 0.5
	VADMinSpeechSec  float32 // 0.25
	VADMaxSpeechSec  float32 // 12
	VADWindowSize    int     // 512 (Silero requirement at 16 kHz)
	VADBufferSeconds float32 // 60

	// Preprocessing on each released segment before AcceptWaveform.
	// All ported from perf/simd-mt-tuning/internal/stt/whisper.go.
	EnableHPF100Hz   bool    // strips < 100 Hz rumble; true by default
	PreEmphasisAlpha float32 // 0.97 typical; 0 disables
	PeakTarget       float32 // 0.7
	PeakCeiling      float32 // 0.5 (skip norm when already this loud)
	PeakFloor        float32 // 0.005 (skip norm below this — silence)
	PeakMaxGain      float32 // 12

	// AudioBufferFrames bounds the goroutine inbox.
	AudioBufferFrames int
}

// DefaultVadNemoConfig returns the perf/simd-mt-tuning whisper.cpp
// preprocessing defaults: HPF 100 Hz on, pre-emphasis 0.97, peak-
// normalise to 0.7 with max gain 12×.
func DefaultVadNemoConfig() VadNemoConfig {
	return VadNemoConfig{
		SampleRate:        16000,
		NumThreads:        0,
		Provider:          "cpu",
		VADThreshold:      0.5,
		VADMinSilenceSec:  0.5,
		VADMinSpeechSec:   0.25,
		VADMaxSpeechSec:   12,
		VADWindowSize:     512,
		VADBufferSeconds:  60,
		EnableHPF100Hz:    true,
		PreEmphasisAlpha:  0.97,
		PeakTarget:        0.7,
		PeakCeiling:       0.5,
		PeakFloor:         0.005,
		PeakMaxGain:       12,
		AudioBufferFrames: 64,
	}
}

// VadNemoEngine is the concrete Backend.
type VadNemoEngine struct {
	cfg VadNemoConfig
	rec *sherpa.OfflineRecognizer
	vad *sherpa.VoiceActivityDetector

	audioCh chan []float32
	eventCh chan Event

	// Stateful HPF carries x[n-1] / y[n-1] across segments, so back-
	// to-back utterances do not get an onset transient from the filter.
	hpPrevX float32
	hpPrevY float32

	dropped     atomic.Uint64
	utterances  atomic.Uint64
	vadActiveNs atomic.Uint64
	totalNs     atomic.Uint64

	closeOnce sync.Once
}

// Compile-time assertion that VadNemoEngine satisfies Backend.
var _ Backend = (*VadNemoEngine)(nil)

// NewVadNemo constructs the engine, loads the NeMo CTC model and the
// Silero VAD model. Call Close to release native resources.
func NewVadNemo(cfg VadNemoConfig) (*VadNemoEngine, error) {
	if cfg.NemoCTCModel == "" {
		return nil, errors.New("stt/vadnemo: NemoCTCModel path required")
	}
	if cfg.Tokens == "" {
		return nil, errors.New("stt/vadnemo: tokens path required")
	}
	if cfg.VADModel == "" {
		return nil, errors.New("stt/vadnemo: vad model path required")
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 16000
	}
	if cfg.NumThreads <= 0 {
		// Mirror perf-branch policy: leave half the cores for MT
		// running concurrently with the next utterance's STT.
		cfg.NumThreads = runtime.NumCPU() / 2
		if cfg.NumThreads < 2 {
			cfg.NumThreads = 2
		}
	}
	if cfg.Provider == "" {
		cfg.Provider = "cpu"
	}
	if cfg.AudioBufferFrames <= 0 {
		cfg.AudioBufferFrames = 64
	}
	if cfg.VADWindowSize == 0 {
		cfg.VADWindowSize = 512
	}
	if cfg.VADBufferSeconds == 0 {
		cfg.VADBufferSeconds = 60
	}

	recCfg := sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: cfg.SampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OfflineModelConfig{
			NemoCTC: sherpa.OfflineNemoEncDecCtcModelConfig{
				Model: cfg.NemoCTCModel,
			},
			Tokens:     cfg.Tokens,
			NumThreads: cfg.NumThreads,
			Provider:   cfg.Provider,
		},
		DecodingMethod: "greedy_search",
	}
	rec := sherpa.NewOfflineRecognizer(&recCfg)
	if rec == nil {
		return nil, fmt.Errorf("stt/vadnemo: NewOfflineRecognizer returned nil (check model files)")
	}

	vadCfg := sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              cfg.VADModel,
			Threshold:          cfg.VADThreshold,
			MinSilenceDuration: cfg.VADMinSilenceSec,
			MinSpeechDuration:  cfg.VADMinSpeechSec,
			WindowSize:         cfg.VADWindowSize,
			MaxSpeechDuration:  cfg.VADMaxSpeechSec,
		},
		SampleRate: cfg.SampleRate,
		NumThreads: 1,
		Provider:   cfg.Provider,
	}
	vad := sherpa.NewVoiceActivityDetector(&vadCfg, cfg.VADBufferSeconds)
	if vad == nil {
		sherpa.DeleteOfflineRecognizer(rec)
		return nil, fmt.Errorf("stt/vadnemo: NewVoiceActivityDetector returned nil")
	}

	return &VadNemoEngine{
		cfg:     cfg,
		rec:     rec,
		vad:     vad,
		audioCh: make(chan []float32, cfg.AudioBufferFrames),
		eventCh: make(chan Event, 32),
	}, nil
}

// Push is non-blocking; drops on inbox overflow.
func (e *VadNemoEngine) Push(samples []float32) {
	if len(samples) == 0 {
		return
	}
	buf := make([]float32, len(samples))
	copy(buf, samples)
	select {
	case e.audioCh <- buf:
	default:
		e.dropped.Add(1)
	}
}

// Events is the read end of the event stream. Closes when Run returns.
func (e *VadNemoEngine) Events() <-chan Event { return e.eventCh }

// Run blocks while the engine processes audio. Returns when ctx
// cancels or Close is called.
func (e *VadNemoEngine) Run(ctx context.Context) {
	defer close(e.eventCh)

	emit := func(ev Event) {
		select {
		case e.eventCh <- ev:
		case <-ctx.Done():
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case samples, ok := <-e.audioCh:
			if !ok {
				return
			}
			// VAD state always updated, regardless of segment release.
			e.vad.AcceptWaveform(samples)

			chunkNs := uint64(int64(len(samples)) * int64(time.Second) / int64(e.cfg.SampleRate))
			e.totalNs.Add(chunkNs)
			if e.vad.IsSpeech() {
				e.vadActiveNs.Add(chunkNs)
			}

			// Drain any segments the VAD released this cycle.
			for !e.vad.IsEmpty() {
				seg := e.vad.Front()
				e.vad.Pop()
				if seg == nil || len(seg.Samples) == 0 {
					continue
				}

				text := e.decodeSegment(seg.Samples)
				if text == "" {
					continue
				}
				emit(Final{Text: text})
				e.utterances.Add(1)
			}
		}
	}
}

// decodeSegment runs the per-segment preprocessing chain and the
// NeMo CTC decoder, returning the trimmed transcript.
func (e *VadNemoEngine) decodeSegment(samples []float32) string {
	// Copy so we do not mutate the VAD's buffer in place; sherpa
	// may keep it referenced briefly.
	buf := make([]float32, len(samples))
	copy(buf, samples)

	if e.cfg.EnableHPF100Hz {
		e.applyHPF100Hz(buf)
	}
	if e.cfg.PreEmphasisAlpha > 0 {
		applyPreEmphasis(buf, e.cfg.PreEmphasisAlpha)
	}
	applyPeakNormalise(buf,
		e.cfg.PeakTarget, e.cfg.PeakCeiling, e.cfg.PeakFloor, e.cfg.PeakMaxGain)

	stream := sherpa.NewOfflineStream(e.rec)
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(e.cfg.SampleRate, buf)
	e.rec.Decode(stream)
	res := stream.GetResult()
	if res == nil {
		return ""
	}
	return strings.TrimSpace(res.Text)
}

// applyHPF100Hz is the stateful single-pole IIR carried across
// segments. α = exp(-2π·100/16000) ≈ 0.9610.
func (e *VadNemoEngine) applyHPF100Hz(samples []float32) {
	const alpha = float32(0.9610)
	prevX, prevY := e.hpPrevX, e.hpPrevY
	for i, x := range samples {
		y := alpha * (prevY + x - prevX)
		samples[i] = y
		prevX = x
		prevY = y
	}
	e.hpPrevX, e.hpPrevY = prevX, prevY
}

// applyPreEmphasis is the stateless 1-tap FIR y[n] = x[n] − α·x[n−1]
// over the whole segment. Stateless on purpose: each segment starts
// from a known boundary so the first-sample edge is benign.
func applyPreEmphasis(samples []float32, alpha float32) {
	if len(samples) < 2 {
		return
	}
	var prev float32
	for i, x := range samples {
		samples[i] = x - alpha*prev
		prev = x
	}
}

// applyPeakNormalise is the same routine perf/simd-mt-tuning used in
// front of whisper.cpp. We only amplify when the segment is
// quiet-but-not-silent; loud segments and pure noise pass through.
func applyPeakNormalise(samples []float32, target, ceiling, floor, maxGain float32) {
	if len(samples) == 0 {
		return
	}
	var peak float32
	for _, v := range samples {
		a := v
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak < floor || peak >= ceiling {
		return
	}
	gain := target / peak
	if gain > maxGain {
		gain = maxGain
	}
	for i, v := range samples {
		w := v * gain
		if w > 1 {
			w = 1
		} else if w < -1 {
			w = -1
		}
		samples[i] = w
	}
}

// Close releases the native objects. Safe to call multiple times.
func (e *VadNemoEngine) Close() error {
	e.closeOnce.Do(func() {
		if e.rec != nil {
			sherpa.DeleteOfflineRecognizer(e.rec)
			e.rec = nil
		}
		if e.vad != nil {
			sherpa.DeleteVoiceActivityDetector(e.vad)
			e.vad = nil
		}
	})
	return nil
}

// Dropped returns the cumulative number of input frames dropped due
// to inbox overflow.
func (e *VadNemoEngine) Dropped() uint64 { return e.dropped.Load() }

// Utterances returns the count of segments transcribed since Run.
func (e *VadNemoEngine) Utterances() uint64 { return e.utterances.Load() }

// VADActiveRatio reports the fraction of processed audio time during
// which Silero reported speech.
func (e *VadNemoEngine) VADActiveRatio() float32 {
	total := e.totalNs.Load()
	if total == 0 {
		return 0
	}
	return float32(float64(e.vadActiveNs.Load()) / float64(total))
}

// Compile-time check that dsp is referenced; the package is imported
// because future per-segment DSP tuning (band-pass for non-telephony
// codecs) will live here. Keep the symbol live so the import sticks
// while we iterate.
var _ = dsp.PreEmphasis{}
