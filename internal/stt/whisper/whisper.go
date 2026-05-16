// Package whisper provides a higher-level Go API for the upstream
// whisper.cpp Go bindings (bindings/go/pkg/whisper).
//
// The package adds:
//
//   - lifecycle management (model load + context reuse, idle unload);
//   - language tagging and translate-mode mapping;
//   - int16 -> float32 normalisation via the SIMD-aware sample package;
//   - a Transcript value type decoupled from the upstream Segment type
//     so that callers do not have to import the binding directly.
//
// It is not a streaming wrapper. Streaming logic (windowing, partial
// emission, deduplication) lives in internal/stt/stream.
package whisper

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	upstream "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/sample"
)

// Transcript is the result of processing one utterance of PCM.
type Transcript struct {
	Text     string
	Language string        // detected or configured source language code
	Latency  time.Duration // wall time spent in Engine.Transcribe
	Segments []Segment
}

// Segment is a single timestamped chunk of recognised speech. The
// timestamps are relative to the start of the supplied PCM, not absolute
// wall time.
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Config controls per-call recognition parameters.
type Config struct {
	// Language is an ISO-639-1 code ("ru", "en", ...) or "auto" to let
	// Whisper detect.
	Language string

	// Translate routes Whisper into translate-to-English mode. We
	// generally keep this off and rely on a separate MT step; included
	// for completeness.
	Translate bool

	// Threads caps the number of inference threads. Zero means
	// runtime.NumCPU()-1 (clamped to ≥1) — leaves one core for the
	// audio thread + UI but otherwise gives Whisper everything we have,
	// because Transcribe is the single biggest contributor to perceived
	// latency.
	Threads int

	// BeamSize chooses greedy (1) vs beam search (>1). 1 keeps latency
	// minimal and is fine for short utterances.
	BeamSize int

	// InitialPrompt biases the decoder with prior context (e.g. the
	// previous utterance). Bounded to ~200 chars by the binding.
	InitialPrompt string
}

// DefaultConfig returns a Config tuned for low-latency single-utterance
// recognition.
func DefaultConfig() Config {
	return Config{
		Language: "auto",
		Threads:  0,
		BeamSize: 1,
	}
}

// Engine owns a loaded Whisper model and a serial decoding context. It
// is safe to call Transcribe from any goroutine; calls are serialised
// internally because the upstream binding's whisper_context is not
// thread-safe.
type Engine struct {
	mu       sync.Mutex
	model    upstream.Model
	defaults Config

	// pcmBuf is reused across calls to avoid per-utterance float32
	// allocations.
	pcmBuf []float32
}

// New loads a Whisper model from disk. The returned Engine holds the
// model in RAM until Close is called.
func New(modelPath string, defaults Config) (*Engine, error) {
	if modelPath == "" {
		return nil, errors.New("whisper: empty model path")
	}
	m, err := upstream.New(modelPath)
	if err != nil {
		return nil, fmt.Errorf("whisper: load model: %w", err)
	}
	return &Engine{model: m, defaults: defaults}, nil
}

// Close releases the model. Calling Transcribe afterwards returns an
// error.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model == nil {
		return nil
	}
	err := e.model.Close()
	e.model = nil
	e.pcmBuf = nil
	return err
}

// IsMultilingual reports whether the loaded model supports languages
// other than English.
func (e *Engine) IsMultilingual() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model == nil {
		return false
	}
	return e.model.IsMultilingual()
}

// Transcribe runs Whisper on the given 16 kHz mono int16 PCM and returns
// a Transcript. The pcm slice is not retained.
func (e *Engine) Transcribe(pcm []int16, override *Config) (*Transcript, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.model == nil {
		return nil, errors.New("whisper: engine closed")
	}
	if len(pcm) == 0 {
		return nil, errors.New("whisper: empty pcm")
	}

	cfg := e.defaults
	if override != nil {
		cfg = mergeConfig(cfg, *override)
	}
	if cfg.Threads == 0 {
		t := max(runtime.NumCPU()-1, 1)
		cfg.Threads = t
	}

	// Normalise int16 -> float32 into a reusable buffer.
	if cap(e.pcmBuf) < len(pcm) {
		e.pcmBuf = make([]float32, len(pcm))
	} else {
		e.pcmBuf = e.pcmBuf[:len(pcm)]
	}
	sample.Int16ToFloat32(e.pcmBuf, pcm)

	ctx, err := e.model.NewContext()
	if err != nil {
		return nil, fmt.Errorf("whisper: new context: %w", err)
	}
	ctx.SetTranslate(cfg.Translate)
	ctx.SetThreads(uint(cfg.Threads))
	if cfg.BeamSize > 0 {
		ctx.SetBeamSize(cfg.BeamSize)
	}
	if cfg.InitialPrompt != "" {
		ctx.SetInitialPrompt(cfg.InitialPrompt)
	}
	if cfg.Language != "" {
		if err := ctx.SetLanguage(cfg.Language); err != nil {
			return nil, fmt.Errorf("whisper: set lang %q: %w", cfg.Language, err)
		}
	}

	t0 := time.Now()
	if err := ctx.Process(e.pcmBuf, nil, nil, nil); err != nil {
		return nil, fmt.Errorf("whisper: process: %w", err)
	}

	tr := &Transcript{
		Language: ctx.DetectedLanguage(),
		Latency:  time.Since(t0),
	}
	for {
		seg, err := ctx.NextSegment()
		if err != nil {
			break // io.EOF — done
		}
		tr.Segments = append(tr.Segments, Segment{
			Start: seg.Start,
			End:   seg.End,
			Text:  seg.Text,
		})
		tr.Text += seg.Text
	}
	return tr, nil
}

// mergeConfig overlays b on top of a. Zero values in b inherit from a.
func mergeConfig(a, b Config) Config {
	out := a
	if b.Language != "" {
		out.Language = b.Language
	}
	if b.Translate != a.Translate {
		out.Translate = b.Translate
	}
	if b.Threads > 0 {
		out.Threads = b.Threads
	}
	if b.BeamSize > 0 {
		out.BeamSize = b.BeamSize
	}
	if b.InitialPrompt != "" {
		out.InitialPrompt = b.InitialPrompt
	}
	return out
}
