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

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Config is the engine configuration. Paths are mandatory; numeric
// knobs default to sensible values when zero via DefaultConfig().
type Config struct {
	// Streaming Zipformer transducer paths.
	Encoder string
	Decoder string
	Joiner  string
	Tokens  string

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
	}
}

// Event is anything the engine emits to its consumer. Use a type
// switch on the receiving end.
type Event interface{ isSTTEvent() }

// Partial is the current best transcript for an in-flight utterance.
// It can change every frame; the consumer is expected to overwrite.
type Partial struct{ Text string }

// Final is the closed transcript of a finished utterance. The
// recognizer's internal state has already been Reset by the time the
// consumer sees this.
type Final struct{ Text string }

func (Partial) isSTTEvent() {}
func (Final) isSTTEvent()   {}

// Engine is the live speech-to-text pipeline. Use New + Run + Push.
type Engine struct {
	cfg Config
	rec *sherpa.OnlineRecognizer
	vad *sherpa.VoiceActivityDetector

	audioCh chan []float32
	eventCh chan Event

	dropped atomic.Uint64

	closeOnce sync.Once
}

// New constructs an Engine and loads the underlying ONNX models. The
// caller must invoke Close to release native resources.
func New(cfg Config) (*Engine, error) {
	if cfg.Encoder == "" || cfg.Decoder == "" || cfg.Joiner == "" || cfg.Tokens == "" {
		return nil, errors.New("stt: encoder/decoder/joiner/tokens path required")
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

	recCfg := sherpa.OnlineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: cfg.SampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: cfg.Encoder,
				Decoder: cfg.Decoder,
				Joiner:  cfg.Joiner,
			},
			Tokens:     cfg.Tokens,
			NumThreads: cfg.NumThreads,
			Provider:   cfg.Provider,
		},
		DecodingMethod:          "greedy_search",
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

			if !speech && !inSegment {
				// Pure silence or background noise — skip
				// ASR entirely to save CPU and avoid
				// hallucinations.
				continue
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
