// Package tts is the in-process Piper text-to-speech engine for the
// translator. It wraps sherpa-onnx's OfflineTts using the VITS/Piper
// model family and surfaces synthesized audio as a stream of PCM
// chunks so the playback layer can start outputting before the full
// sentence has been rendered.
//
// Hot-path:
//
//	text ──▶ Engine.Speak ──▶ <-chan AudioChunk
//	                  │
//	                  ▼
//	          OfflineTts.GenerateWithProgressCallback
//	                  │
//	                  ▼
//	        sherpa C-side calls Go callback
//	         (samples []float32 in [-1, 1])
//	                  │
//	                  ▼
//	                 chan
//
// First chunk typically arrives ~100–300 ms after Speak — well below
// the time to synthesize a full sentence — which is why this design
// replaces the previous subprocess-Piper approach.
package tts

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Config configures the Piper VITS engine. Model / Tokens / DataDir
// are mandatory. Numeric knobs default to Piper-standard values when
// zero; see DefaultConfig.
type Config struct {
	// Piper VITS model files.
	Model   string // path to <voice>.onnx
	Tokens  string // path to tokens.txt
	DataDir string // path to espeak-ng-data directory
	Lexicon string // optional, language-specific

	// Sampler knobs. Defaults are the Piper-published recommendations.
	NoiseScale  float32
	NoiseScaleW float32
	LengthScale float32 // <1 faster, >1 slower

	// Runtime knobs.
	NumThreads int    // 0 → runtime.NumCPU()
	Provider   string // "cpu" by default

	// Defaults for Speak when caller doesn't override.
	Sid   int     // speaker id (0 for single-speaker voices)
	Speed float32 // 1.0 nominal

	// ChunkBuffer bounds the AudioChunk channel. Larger values
	// smooth playback bursts; smaller values pin memory tighter and
	// surface cancellation faster.
	ChunkBuffer int
}

// DefaultConfig returns Piper-style sane defaults. Caller fills the
// model paths.
func DefaultConfig() Config {
	return Config{
		NoiseScale:  0.667,
		NoiseScaleW: 0.8,
		LengthScale: 1.0,
		Provider:    "cpu",
		Speed:       1.0,
		ChunkBuffer: 8,
	}
}

// AudioChunk is one PCM segment emitted during synthesis.
type AudioChunk struct {
	// Samples are mono float32 in [-1, 1]. Length is decided by
	// sherpa-onnx; consumers should not assume a fixed size.
	Samples []float32
	// Progress is a 0..1 hint of synthesis completion at chunk time.
	Progress float32
}

// Engine wraps a Piper VITS OfflineTts instance. Safe for concurrent
// Close; Speak is serialized internally because the underlying native
// object is not thread-safe.
type Engine struct {
	cfg        Config
	impl       *sherpa.OfflineTts
	sampleRate int

	mu        sync.Mutex
	closeOnce sync.Once
}

// New constructs an Engine and loads the model. The caller must call
// Close to release the native handle.
func New(cfg Config) (*Engine, error) {
	if cfg.Model == "" || cfg.Tokens == "" || cfg.DataDir == "" {
		return nil, errors.New("tts: model / tokens / data dir paths required")
	}

	if cfg.NumThreads <= 0 {
		cfg.NumThreads = runtime.NumCPU()
	}
	if cfg.Provider == "" {
		cfg.Provider = "cpu"
	}
	if cfg.Speed <= 0 {
		cfg.Speed = 1.0
	}
	if cfg.NoiseScale == 0 {
		cfg.NoiseScale = 0.667
	}
	if cfg.NoiseScaleW == 0 {
		cfg.NoiseScaleW = 0.8
	}
	if cfg.LengthScale == 0 {
		cfg.LengthScale = 1.0
	}
	if cfg.ChunkBuffer <= 0 {
		cfg.ChunkBuffer = 8
	}

	ttsCfg := sherpa.OfflineTtsConfig{
		Model: sherpa.OfflineTtsModelConfig{
			Vits: sherpa.OfflineTtsVitsModelConfig{
				Model:       cfg.Model,
				Tokens:      cfg.Tokens,
				DataDir:     cfg.DataDir,
				Lexicon:     cfg.Lexicon,
				NoiseScale:  cfg.NoiseScale,
				NoiseScaleW: cfg.NoiseScaleW,
				LengthScale: cfg.LengthScale,
			},
			NumThreads: cfg.NumThreads,
			Provider:   cfg.Provider,
		},
	}

	impl := sherpa.NewOfflineTts(&ttsCfg)
	if impl == nil {
		return nil, fmt.Errorf("tts: failed to construct OfflineTts (check Piper model files)")
	}

	return &Engine{
		cfg:        cfg,
		impl:       impl,
		sampleRate: impl.SampleRate(),
	}, nil
}

// SampleRate returns the model's native output rate (e.g. 22050 for
// most Piper voices). Pass this to the playback layer.
func (e *Engine) SampleRate() int { return e.sampleRate }

// Speak synthesizes text and streams PCM chunks until done or ctx is
// cancelled. The returned channel closes when synthesis completes
// (successfully or otherwise). At most one Speak runs at a time per
// Engine; concurrent callers serialize on an internal mutex.
//
// Cancelling ctx returns false from the next progress callback,
// which instructs sherpa to abort synthesis as quickly as it can.
func (e *Engine) Speak(ctx context.Context, text string) <-chan AudioChunk {
	out := make(chan AudioChunk, e.cfg.ChunkBuffer)

	go func() {
		defer close(out)

		e.mu.Lock()
		defer e.mu.Unlock()

		if e.impl == nil {
			return
		}

		cb := func(samples []float32, progress float32) bool {
			if ctx.Err() != nil {
				return false
			}
			select {
			case out <- AudioChunk{Samples: samples, Progress: progress}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		_ = e.impl.GenerateWithProgressCallback(text, e.cfg.Sid, e.cfg.Speed, cb)
	}()

	return out
}

// Close releases the native model. Safe to call multiple times.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.impl != nil {
			sherpa.DeleteOfflineTts(e.impl)
			e.impl = nil
		}
	})
	return nil
}
