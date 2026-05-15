// Package app provides the high-level composition root for the
// realtime-speech-translator daemon.
//
// M1 implemented the audio loopback. M2 adds VAD + Whisper: the capture
// stream is forwarded into the STT pipeline, which emits Transcript
// events on a channel consumed by the UI. The playback device is not
// opened in M2 — TTS-driven playback returns in M3.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/capture"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
)

// State enumerates the user-visible session states. Stored atomically so
// UI goroutines can poll without locking.
type State int32

const (
	StateIdle State = iota
	StateStarting
	StateRunning
	StateStopping
	StateError
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateError:
		return "error"
	}
	return "unknown"
}

// Config bundles runtime parameters for a Session.
type Config struct {
	SampleRate   uint32
	ModelPath    string
	SourceLang   string // ISO-639-1 or "auto"
	TargetLang   string // ISO-639-1, empty disables MT
	MTBackend    mt.Engine
	PipelineConf stt.Config
}

// DefaultConfig returns a Config tuned for 16 kHz Whisper input.
func DefaultConfig() Config {
	return Config{
		SampleRate:   16000,
		SourceLang:   "auto",
		PipelineConf: stt.DefaultConfig(),
	}
}

// Session owns the audio capture device and the STT pipeline.
type Session struct {
	cfg   Config
	state atomic.Int32
	drops atomic.Uint64

	mctx     *malgo.AllocatedContext
	cap      *capture.Source
	pipeline *stt.Pipeline
	cancel   context.CancelFunc
}

// New constructs a Session with the audio device and STT pipeline
// initialised but not yet running. ModelPath must point at a Whisper
// ggml-format model file.
func New(cfg Config) (*Session, error) {
	if cfg.SampleRate == 0 {
		return nil, errors.New("app: SampleRate must be set")
	}
	if cfg.ModelPath == "" {
		return nil, errors.New("app: ModelPath must be set")
	}

	pcfg := cfg.PipelineConf
	if pcfg.Engine.Language == "" {
		pcfg.Engine.Language = cfg.SourceLang
	}
	if cfg.MTBackend != nil && cfg.TargetLang != "" {
		pcfg.MT = mt.Serial(cfg.MTBackend)
		pcfg.TargetLang = cfg.TargetLang
	}
	pipeline, err := stt.New(cfg.ModelPath, pcfg)
	if err != nil {
		return nil, fmt.Errorf("app: stt pipeline: %w", err)
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		_ = pipeline.Close()
		return nil, fmt.Errorf("app: init miniaudio context: %w", err)
	}

	s := &Session{cfg: cfg, mctx: mctx, pipeline: pipeline}

	cap, err := capture.New(mctx, capture.Config{SampleRate: cfg.SampleRate}, &sttSink{
		pipeline: pipeline,
		drops:    &s.drops,
	})
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		_ = pipeline.Close()
		return nil, err
	}
	s.cap = cap
	return s, nil
}

// Events returns the channel that emits recognised utterances. It is
// closed by Close.
func (s *Session) Events() <-chan stt.Event { return s.pipeline.Output() }

// State returns the current session state.
func (s *Session) State() State { return State(s.state.Load()) }

// Start activates the capture device and the STT pipeline.
func (s *Session) Start() error {
	if !s.state.CompareAndSwap(int32(StateIdle), int32(StateStarting)) {
		return fmt.Errorf("app: cannot start from state %s", State(s.state.Load()))
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	if err := s.pipeline.Start(ctx); err != nil {
		cancel()
		s.state.Store(int32(StateError))
		return err
	}
	if err := s.cap.Start(); err != nil {
		cancel()
		s.state.Store(int32(StateError))
		return err
	}
	s.state.Store(int32(StateRunning))
	return nil
}

// Stop halts the audio device and the pipeline. Idempotent.
func (s *Session) Stop() error {
	if !s.state.CompareAndSwap(int32(StateRunning), int32(StateStopping)) {
		return nil
	}
	var firstErr error
	if err := s.cap.Stop(); err != nil && firstErr == nil {
		firstErr = err
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.state.Store(int32(StateIdle))
	return firstErr
}

// Close releases all resources. Must not be used afterward.
func (s *Session) Close() error {
	_ = s.Stop()
	var firstErr error
	if s.cap != nil {
		if err := s.cap.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.cap = nil
	}
	if s.pipeline != nil {
		if err := s.pipeline.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.pipeline = nil
	}
	if s.mctx != nil {
		_ = s.mctx.Uninit()
		s.mctx.Free()
		s.mctx = nil
	}
	return firstErr
}

// DroppedSamples reports samples captured but not forwarded — should
// remain zero in steady state.
func (s *Session) DroppedSamples() uint64 { return s.drops.Load() }
