// Package app provides the high-level composition root for the
// realtime-speech-translator daemon. M1 implements only the audio
// loopback path (mic -> ringbuf -> playback); later milestones extend it
// with VAD, STT, MT and TTS stages.
package app

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/capture"
	"github.com/alex60217101990/realtime-speech-translator/internal/audio/playback"
	"github.com/alex60217101990/realtime-speech-translator/internal/audio/ringbuf"
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

// Config bundles the runtime parameters for a Session.
type Config struct {
	SampleRate     uint32
	RingbufSamples int
}

// DefaultConfig returns a Config tuned for 16 kHz mono speech (Whisper
// input rate) with a ~256 ms ring buffer.
func DefaultConfig() Config {
	return Config{
		SampleRate:     16000,
		RingbufSamples: 4096,
	}
}

// Session owns the audio devices and the buffer that connects them. M1
// performs a direct loopback; later milestones replace the consumer side
// of the ring with a VAD segmenter.
type Session struct {
	cfg   Config
	state atomic.Int32
	drops atomic.Uint64

	ctx *malgo.AllocatedContext
	cap *capture.Source
	pb  *playback.Sink
	rb  *ringbuf.Ring
}

// New constructs a Session with audio devices initialised but not yet
// started. The miniaudio context is owned by the Session and freed by
// Close.
func New(cfg Config) (*Session, error) {
	if cfg.SampleRate == 0 {
		return nil, errors.New("app: SampleRate must be set")
	}
	if cfg.RingbufSamples <= 0 {
		return nil, errors.New("app: RingbufSamples must be positive")
	}

	rb, err := ringbuf.New(cfg.RingbufSamples)
	if err != nil {
		return nil, fmt.Errorf("app: ringbuf: %w", err)
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("app: init miniaudio context: %w", err)
	}

	s := &Session{cfg: cfg, ctx: mctx, rb: rb}

	cap, err := capture.New(mctx, capture.Config{SampleRate: cfg.SampleRate}, &ringSink{rb: rb, drops: &s.drops})
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return nil, err
	}
	pb, err := playback.New(mctx, playback.Config{SampleRate: cfg.SampleRate}, &ringSource{rb: rb})
	if err != nil {
		_ = cap.Close()
		_ = mctx.Uninit()
		mctx.Free()
		return nil, err
	}

	s.cap = cap
	s.pb = pb
	return s, nil
}

// State returns the current session state.
func (s *Session) State() State { return State(s.state.Load()) }

// Start activates the audio devices.
func (s *Session) Start() error {
	if !s.state.CompareAndSwap(int32(StateIdle), int32(StateStarting)) {
		return fmt.Errorf("app: cannot start from state %s", State(s.state.Load()))
	}
	if err := s.cap.Start(); err != nil {
		s.state.Store(int32(StateError))
		return err
	}
	if err := s.pb.Start(); err != nil {
		_ = s.cap.Stop()
		s.state.Store(int32(StateError))
		return err
	}
	s.state.Store(int32(StateRunning))
	return nil
}

// Stop halts the audio devices and clears the ring buffer.
func (s *Session) Stop() error {
	if !s.state.CompareAndSwap(int32(StateRunning), int32(StateStopping)) {
		return nil
	}
	var firstErr error
	if err := s.pb.Stop(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.cap.Stop(); err != nil && firstErr == nil {
		firstErr = err
	}
	s.rb.Reset()
	s.state.Store(int32(StateIdle))
	return firstErr
}

// Close releases all resources. The Session must not be used afterward.
func (s *Session) Close() error {
	_ = s.Stop()
	var firstErr error
	if s.pb != nil {
		if err := s.pb.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.pb = nil
	}
	if s.cap != nil {
		if err := s.cap.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.cap = nil
	}
	if s.ctx != nil {
		_ = s.ctx.Uninit()
		s.ctx.Free()
		s.ctx = nil
	}
	return firstErr
}

// DroppedSamples reports the cumulative count of captured samples
// dropped because the ring buffer was full when the capture callback
// fired.
func (s *Session) DroppedSamples() uint64 { return s.drops.Load() }

// Underruns reports the cumulative count of samples the playback device
// had to fill with silence because the ring buffer was empty.
func (s *Session) Underruns() uint64 { return s.pb.Underruns() }
