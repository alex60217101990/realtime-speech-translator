// Package stt is the realtime speech-to-text engine for the
// translator. It wraps sherpa-onnx's streaming Zipformer recognizer
// with a Silero VAD pre-gate so the recognizer never spends CPU on
// pure silence or background noise.
//
// Hot-path:
//
//	mic 16k mono []float32 ──▶ Engine.Push
//	                              │
//	                       audioCh (drop on overflow)
//	                              │
//	                      Engine.Run goroutine
//	                              │
//	                ┌─────────────┴────────────────┐
//	                ▼                              ▼
//	          Silero VAD                    OnlineStream
//	          (gate only)                   (only fed while
//	                                         inSegment)
//	                                              │
//	                                ┌─────────────┴───────────────┐
//	                                ▼                             ▼
//	                          GetResult → Partial          IsEndpoint → Final
//	                                                       (Rule1/2/3) → Reset
//
// Endpoint detection is delegated to the recognizer itself
// (Rule1MinTrailingSilence ≈ 2.4 s closes a regular utterance, Rule3
// caps long monologues). The VAD is *not* used to cut segments — only
// to suppress feeding noise into the ASR, which costs CPU and causes
// hallucinations.
package stt

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
)

// ModelKind selects which streaming online recognizer configuration
// the engine fills in. New backends should add a new value here so
// the rest of the package stays one switch wide.
type ModelKind int

const (
	// ModelTransducer is the streaming Zipformer transducer family
	// (encoder + decoder + joiner). The English published model
	// `sherpa-onnx-streaming-zipformer-en-2023-06-26` uses this.
	ModelTransducer ModelKind = iota
	// ModelToneCtc is the T-one CTC family (one .onnx file). The
	// Russian published model
	// `sherpa-onnx-streaming-t-one-russian-2025-09-08` uses this.
	// Conformer + CTC, 71.6 M params, 300 ms chunks, ~1.2 s total
	// latency; trained on telephony but works on 16 kHz mic input.
	ModelToneCtc
)

// Config is the engine configuration. Paths are mandatory; numeric
// knobs default to sensible values when zero via DefaultConfig().
type Config struct {
	// Kind picks the recognizer family. Zero value =
	// ModelTransducer (Zipformer).
	Kind ModelKind

	// Transducer (Zipformer) paths — used when Kind == ModelTransducer.
	Encoder string
	Decoder string
	Joiner  string

	// ToneCtcModel — single .onnx, used when Kind == ModelToneCtc.
	ToneCtcModel string

	// Tokens is shared by every recognizer family.
	Tokens string

	// Silero VAD model path.
	VADModel string

	// SampleRate is the audio rate of samples passed to Push.
	// Streaming Zipformer models published by k2-fsa expect 16000.
	SampleRate int
	// NumThreads is the per-engine thread cap. 0 picks runtime.NumCPU().
	NumThreads int
	// Provider is the ONNX runtime provider. "cpu" for portability.
	Provider string

	// Endpoint detection knobs. See
	// https://k2-fsa.github.io/sherpa/ncnn/endpoint.html
	Rule1MinTrailingSilenceSec float32
	Rule2MinTrailingSilenceSec float32
	Rule3MinUtteranceLengthSec float32

	// VAD knobs.
	VADThreshold       float32
	VADMinSilenceSec   float32
	VADMinSpeechSec    float32
	VADMaxSpeechSec    float32
	VADWindowSize      int     // Silero requires 512 for 16 kHz audio.
	VADBufferSeconds   float32 // Internal VAD ring buffer span.

	// AudioBufferFrames bounds the goroutine inbox; older frames are
	// dropped when the recognizer can't keep up. Each frame is one
	// Push call from the audio source — typically 10–100 ms.
	AudioBufferFrames int

	// DecodingMethod selects the sherpa decoder. "greedy_search"
	// is fastest but lower quality; "modified_beam_search" is the
	// recommended quality setting and only adds a few ms per chunk.
	DecodingMethod string

	// MaxActivePaths is the beam width when DecodingMethod is
	// modified_beam_search. 4 is the upstream default.
	MaxActivePaths int

	// EnablePreEmphasis turns on a 100 Hz single-pole IIR high-pass
	// before AcceptWaveform. Strips AC hum / mic-stand rumble that
	// adds nothing to the log-mel spectrogram and visibly improves
	// CTC accuracy on cheap built-in laptop microphones.
	EnablePreEmphasis bool
}

// DefaultConfig returns recommended defaults for realtime mic capture.
// Caller fills the model paths.
func DefaultConfig() Config {
	return Config{
		SampleRate:                 16000,
		NumThreads:                 0,
		Provider:                   "cpu",
		Rule1MinTrailingSilenceSec: 2.4,
		Rule2MinTrailingSilenceSec: 1.2,
		Rule3MinUtteranceLengthSec: 20,
		VADThreshold:               0.5,
		VADMinSilenceSec:           0.5,
		VADMinSpeechSec:            0.25,
		VADMaxSpeechSec:            12,
		VADWindowSize:              512,
		VADBufferSeconds:           60,
		AudioBufferFrames:          64,
		DecodingMethod:             "modified_beam_search",
		MaxActivePaths:             4,
		EnablePreEmphasis:          true,
	}
}

// Event is anything an STT backend emits to its consumer. Use a type
// switch on the receiving end.
type Event interface{ isSTTEvent() }

// Partial is the current best transcript for an in-flight utterance.
// It can change every frame; the consumer is expected to overwrite.
// Streaming backends emit Partials; offline-per-segment backends
// emit only Finals.
type Partial struct{ Text string }

// Final is the closed transcript of a finished utterance. The
// recognizer's internal state has already been Reset by the time the
// consumer sees this.
type Final struct{ Text string }

func (Partial) isSTTEvent() {}
func (Final) isSTTEvent()   {}

// Backend is the contract every concrete STT engine satisfies. The
// streaming Zipformer (this package's Engine) and the VAD + offline
// Whisper variant (internal/stt/vadwhisper) both implement it. The
// rstapp.Session field is typed as Backend so the cmd layer picks
// the implementation per configuration.
type Backend interface {
	// Run blocks while the backend processes audio. Returns when
	// ctx cancels or Close is called. Only one Run per Backend.
	Run(ctx context.Context)
	// Push hands a chunk of 16 kHz mono float32 audio to the
	// backend. Must be non-blocking.
	Push(samples []float32)
	// Events is the read end of the event stream. Closes when Run
	// returns.
	Events() <-chan Event
	// Close releases the underlying native resources. Safe to call
	// multiple times.
	Close() error

	// Stats surface for the UI status line. All three are safe to
	// call from any goroutine.
	Dropped() uint64
	Utterances() uint64
	VADActiveRatio() float32
}

// Compile-time assertion that the streaming Engine satisfies the
// Backend contract.
var _ Backend = (*Engine)(nil)

// Engine is the live speech-to-text pipeline. Use New + Run + Push.
type Engine struct {
	cfg Config
	rec *sherpa.OnlineRecognizer
	vad *sherpa.VoiceActivityDetector

	audioCh chan []float32
	eventCh chan Event

	dropped     atomic.Uint64
	utterances  atomic.Uint64
	vadActiveNs atomic.Uint64 // ns inside VAD speech, monotonic
	totalNs     atomic.Uint64 // ns of audio processed, monotonic

	// pre-emphasis state (single-pole high-pass IIR, stateful
	// across chunks). Only touched from the Run goroutine.
	hpPrevX float32
	hpPrevY float32

	closeOnce sync.Once
}

// highPass100Hz applies a 100 Hz single-pole IIR HPF in place,
// carrying state across chunks. α = exp(-2π · fc / fs); for the
// default 16 kHz capture this is ≈ 0.9610.
func (e *Engine) highPass100Hz(samples []float32) {
	if len(samples) == 0 {
		return
	}
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

// Utterances returns the count of finalised utterances since Run.
func (e *Engine) Utterances() uint64 { return e.utterances.Load() }

// VADActiveRatio returns the fraction of processed audio time during
// which Silero reported speech ([0,1]).
func (e *Engine) VADActiveRatio() float32 {
	total := e.totalNs.Load()
	if total == 0 {
		return 0
	}
	return float32(float64(e.vadActiveNs.Load()) / float64(total))
}

// New constructs an Engine and loads the underlying ONNX models. The
// caller must invoke Close to release native resources.
func New(cfg Config) (*Engine, error) {
	switch cfg.Kind {
	case ModelTransducer:
		if cfg.Encoder == "" || cfg.Decoder == "" || cfg.Joiner == "" {
			return nil, errors.New("stt: encoder/decoder/joiner path required for transducer")
		}
	case ModelToneCtc:
		if cfg.ToneCtcModel == "" {
			return nil, errors.New("stt: tone-ctc model path required")
		}
	default:
		return nil, fmt.Errorf("stt: unknown ModelKind %d", cfg.Kind)
	}
	if cfg.Tokens == "" {
		return nil, errors.New("stt: tokens path required")
	}
	if cfg.VADModel == "" {
		return nil, errors.New("stt: vad model path required")
	}

	if cfg.NumThreads <= 0 {
		cfg.NumThreads = runtime.NumCPU()
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 16000
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

	modelCfg := sherpa.OnlineModelConfig{
		Tokens:     cfg.Tokens,
		NumThreads: cfg.NumThreads,
		Provider:   cfg.Provider,
	}
	switch cfg.Kind {
	case ModelTransducer:
		modelCfg.Transducer = sherpa.OnlineTransducerModelConfig{
			Encoder: cfg.Encoder,
			Decoder: cfg.Decoder,
			Joiner:  cfg.Joiner,
		}
	case ModelToneCtc:
		modelCfg.ToneCtc = sherpa.OnlineToneCtcModelConfig{
			Model: cfg.ToneCtcModel,
		}
	}

	decoding := cfg.DecodingMethod
	if decoding == "" {
		decoding = "greedy_search"
	}
	maxPaths := cfg.MaxActivePaths
	if maxPaths <= 0 {
		maxPaths = 4
	}
	recCfg := sherpa.OnlineRecognizerConfig{
		FeatConfig:              sherpa.FeatureConfig{SampleRate: cfg.SampleRate, FeatureDim: 80},
		ModelConfig:             modelCfg,
		DecodingMethod:          decoding,
		MaxActivePaths:          maxPaths,
		EnableEndpoint:          1,
		Rule1MinTrailingSilence: cfg.Rule1MinTrailingSilenceSec,
		Rule2MinTrailingSilence: cfg.Rule2MinTrailingSilenceSec,
		Rule3MinUtteranceLength: cfg.Rule3MinUtteranceLengthSec,
	}

	rec := sherpa.NewOnlineRecognizer(&recCfg)
	if rec == nil {
		return nil, fmt.Errorf("stt: failed to construct OnlineRecognizer (check model files)")
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
		sherpa.DeleteOnlineRecognizer(rec)
		return nil, fmt.Errorf("stt: failed to construct VoiceActivityDetector (check vad model)")
	}

	return &Engine{
		cfg:     cfg,
		rec:     rec,
		vad:     vad,
		audioCh: make(chan []float32, cfg.AudioBufferFrames),
		eventCh: make(chan Event, 32),
	}, nil
}

// Push hands a chunk of 16 kHz mono float32 audio to the engine. It
// is non-blocking: when the engine cannot keep up, samples are dropped
// silently (count surfaced via Dropped) so the audio source is never
// stalled. Callers may pass any chunk size; the engine does not assume
// a specific frame length.
func (e *Engine) Push(samples []float32) {
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

// Events is the read end of the event stream. It closes when Run
// returns. Consumers must not close it.
func (e *Engine) Events() <-chan Event { return e.eventCh }

// Dropped returns the cumulative count of frames dropped due to inbox
// overflow. Useful as a UI/lag signal.
func (e *Engine) Dropped() uint64 { return e.dropped.Load() }

// Run blocks while the engine processes audio. It returns when ctx
// is cancelled or Close is called. Only one Run per Engine.
func (e *Engine) Run(ctx context.Context) {
	defer close(e.eventCh)

	stream := sherpa.NewOnlineStream(e.rec)
	defer sherpa.DeleteOnlineStream(stream)

	var (
		inSegment   bool
		lastPartial string
	)

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

			// Always feed VAD so its internal state tracks the
			// real audio timeline.
			e.vad.AcceptWaveform(samples)
			speech := e.vad.IsSpeech()

			// Track total audio + VAD-speech time for status UI.
			chunkNs := uint64(int64(len(samples)) * int64(time.Second) / int64(e.cfg.SampleRate))
			e.totalNs.Add(chunkNs)
			if speech {
				e.vadActiveNs.Add(chunkNs)
			}

			if !speech && !inSegment {
				// Pure silence or background noise — skip
				// ASR entirely to save CPU and avoid
				// hallucinations.
				continue
			}

			if e.cfg.EnablePreEmphasis {
				e.highPass100Hz(samples)
			}
			stream.AcceptWaveform(e.cfg.SampleRate, samples)
			inSegment = true

			for e.rec.IsReady(stream) {
				e.rec.Decode(stream)
			}

			text := strings.TrimSpace(e.rec.GetResult(stream).Text)
			if text != "" && text != lastPartial {
				emit(Partial{Text: text})
				lastPartial = text
			}

			if e.rec.IsEndpoint(stream) {
				if lastPartial != "" {
					emit(Final{Text: lastPartial})
					e.utterances.Add(1)
				}
				e.rec.Reset(stream)
				lastPartial = ""
				inSegment = false
			}
		}
	}
}

// Close releases the underlying native resources. It is safe to call
// Close multiple times.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		if e.rec != nil {
			sherpa.DeleteOnlineRecognizer(e.rec)
			e.rec = nil
		}
		if e.vad != nil {
			sherpa.DeleteVoiceActivityDetector(e.vad)
			e.vad = nil
		}
	})
	return nil
}
