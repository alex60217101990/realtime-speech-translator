// Package app provides the high-level composition root for the
// realtime-speech-translator daemon.
//
// M3b assembles the full audio loop: capture → VAD → Whisper → MT →
// Piper TTS → playback. The TTS branch is optional; without it the
// Session emits transcript+translation events only and never opens a
// playback device.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/capture"
	"github.com/alex60217101990/realtime-speech-translator/internal/audio/playback"
	"github.com/alex60217101990/realtime-speech-translator/internal/audio/ringbuf"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
)

// State enumerates the user-visible session states.
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
	SampleRate   uint32 // capture rate; Whisper requires 16 kHz
	ModelPath    string
	SourceLang   string // ISO-639-1 or "auto"
	TargetLang   string // ISO-639-1, empty disables MT
	MTBackend    mt.Engine
	TTSBackend   stt.TTSEngine
	// TTSRate is the playback rate for TTS audio; Piper emits 22050 Hz.
	TTSRate uint32
	// PlaybackBufferSamples sizes the ringbuf between TTS and the
	// audio device. Must be a power of two. Zero defaults to 65536
	// (~3 s at 22050 Hz) which absorbs Piper's bursty output.
	PlaybackBufferSamples int
	// PlaybackDeviceID, when non-nil, selects a specific output device
	// (typically the virtual mic discovered via internal/vmic). nil
	// means the OS default output.
	PlaybackDeviceID *malgo.DeviceID
	PipelineConf     stt.Config
}

// DefaultConfig returns a Config tuned for 16 kHz Whisper input and
// 22050 Hz Piper output.
func DefaultConfig() Config {
	return Config{
		SampleRate:            16000,
		SourceLang:            "auto",
		TTSRate:               22050,
		PlaybackBufferSamples: 65536,
		PipelineConf:          stt.DefaultConfig(),
	}
}

// Session owns the audio devices and the STT/MT/TTS pipeline.
type Session struct {
	cfg   Config
	state atomic.Int32
	drops atomic.Uint64

	mctx      *malgo.AllocatedContext
	cap       *capture.Source
	pipeline  *stt.Pipeline
	playbackD *playback.Sink
	playbackR *ringbuf.Ring
	cancel    context.CancelFunc
}

// New constructs a Session with the audio devices and STT pipeline
// initialised but not yet running.
func New(cfg Config) (*Session, error) {
	if cfg.SampleRate == 0 {
		return nil, errors.New("app: SampleRate must be set")
	}
	if cfg.ModelPath == "" {
		return nil, errors.New("app: ModelPath must be set")
	}
	if cfg.TTSBackend != nil {
		if cfg.TTSRate == 0 {
			return nil, errors.New("app: TTSRate must be set when TTSBackend is configured")
		}
		if cfg.PlaybackBufferSamples <= 0 {
			cfg.PlaybackBufferSamples = 65536
		}
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("app: init miniaudio context: %w", err)
	}
	s := &Session{cfg: cfg, mctx: mctx}

	pcfg := cfg.PipelineConf
	if pcfg.Engine.Language == "" {
		pcfg.Engine.Language = cfg.SourceLang
	}
	if cfg.MTBackend != nil && cfg.TargetLang != "" {
		pcfg.MT = mt.Serial(cfg.MTBackend)
		pcfg.TargetLang = cfg.TargetLang
	}

	// Wire optional TTS + playback. The pipeline writes synthesised
	// PCM through pcfg.Audio (an AudioSink that pushes into our
	// playback ringbuf). The playback device drains that same ring.
	if cfg.TTSBackend != nil {
		rb, err := ringbuf.New(cfg.PlaybackBufferSamples)
		if err != nil {
			_ = mctx.Uninit()
			mctx.Free()
			return nil, fmt.Errorf("app: playback ringbuf: %w", err)
		}
		s.playbackR = rb
		pcfg.TTS = cfg.TTSBackend
		pcfg.Audio = &playbackSink{rb: rb}
	}

	pipeline, err := stt.New(cfg.ModelPath, pcfg)
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return nil, fmt.Errorf("app: stt pipeline: %w", err)
	}
	s.pipeline = pipeline

	cap, err := capture.New(mctx, capture.Config{SampleRate: cfg.SampleRate}, &sttSink{
		pipeline: pipeline,
		drops:    &s.drops,
	})
	if err != nil {
		_ = pipeline.Close()
		_ = mctx.Uninit()
		mctx.Free()
		return nil, err
	}
	s.cap = cap

	if cfg.TTSBackend != nil {
		pb, err := playback.New(mctx, playback.Config{
			SampleRate: cfg.TTSRate,
			DeviceID:   cfg.PlaybackDeviceID,
		}, &playbackSource{rb: s.playbackR})
		if err != nil {
			_ = cap.Close()
			_ = pipeline.Close()
			_ = mctx.Uninit()
			mctx.Free()
			return nil, fmt.Errorf("app: playback device: %w", err)
		}
		s.playbackD = pb
	}

	return s, nil
}

// Events returns the channel that emits recognised utterances.
func (s *Session) Events() <-chan stt.Event { return s.pipeline.Output() }

// State returns the current session state.
func (s *Session) State() State { return State(s.state.Load()) }

// Start activates the capture device, the STT pipeline and (if
// configured) the playback device for TTS output.
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
	if s.playbackD != nil {
		if err := s.playbackD.Start(); err != nil {
			cancel()
			s.state.Store(int32(StateError))
			return err
		}
	}
	if err := s.cap.Start(); err != nil {
		_ = s.stopPlayback()
		cancel()
		s.state.Store(int32(StateError))
		return err
	}
	s.state.Store(int32(StateRunning))
	return nil
}

// Stop halts the audio devices and the pipeline. Idempotent.
func (s *Session) Stop() error {
	if !s.state.CompareAndSwap(int32(StateRunning), int32(StateStopping)) {
		return nil
	}
	var firstErr error
	if err := s.cap.Stop(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.stopPlayback(); err != nil && firstErr == nil {
		firstErr = err
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.playbackR != nil {
		s.playbackR.Reset()
	}
	s.state.Store(int32(StateIdle))
	return firstErr
}

func (s *Session) stopPlayback() error {
	if s.playbackD == nil {
		return nil
	}
	return s.playbackD.Stop()
}

// Close releases all resources.
func (s *Session) Close() error {
	_ = s.Stop()
	var firstErr error
	if s.cap != nil {
		if err := s.cap.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.cap = nil
	}
	if s.playbackD != nil {
		if err := s.playbackD.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.playbackD = nil
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

// DroppedSamples reports samples captured but not forwarded.
func (s *Session) DroppedSamples() uint64 { return s.drops.Load() }

// PlaybackUnderruns reports samples the TTS playback device had to fill
// with silence because the ringbuf was empty.
func (s *Session) PlaybackUnderruns() uint64 {
	if s.playbackD == nil {
		return 0
	}
	return s.playbackD.Underruns()
}
