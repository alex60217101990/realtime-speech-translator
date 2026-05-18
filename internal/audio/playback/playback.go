// Package playback opens the default audio output device in float32
// mono mode and pulls samples from an internal lock-free ring buffer
// inside the malgo callback. Producers (typically the TTS engine)
// push synthesized PCM into the ring via Write; if the ring is full
// the producer is told how many samples actually fit, and it can
// retry or drop the excess.
//
// Underrun policy: when the ring is empty the callback emits
// silence (zero-filled output). That keeps the device clock steady
// rather than feeding the host garbage data.
package playback

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/ringbuf"
)

// Config configures the playback device.
type Config struct {
	SampleRate     int // 22050 for Piper voices, set per TTS model
	Channels       int // 1
	BufferSamples  int // ring buffer capacity (rounds up to pow2)
	scratchSamples int // internal: per-callback transfer slice
}

// DefaultConfig returns parameters suitable for Piper TTS playback
// (22 050 Hz mono, ~3 seconds of cushion in the ring).
func DefaultConfig() Config {
	return Config{
		SampleRate:    22050,
		Channels:      1,
		BufferSamples: 1 << 16, // 65536 samples ≈ 3 s @ 22.05 kHz
	}
}

// Playback owns the device + ring. Construct with New, drive with
// Start/Stop, push audio via Write, release with Close.
type Playback struct {
	cfg    Config
	mctx   *malgo.AllocatedContext
	device *malgo.Device

	ring *ringbuf.Ring

	scratch []float32 // reused inside onFrames; sized to device period

	underruns atomic.Uint64
	started   atomic.Bool
}

// New constructs the device and ring buffer.
func New(cfg Config) (*Playback, error) {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 22050
	}
	if cfg.Channels <= 0 {
		cfg.Channels = 1
	}
	if cfg.Channels != 1 {
		return nil, errors.New("playback: only mono (Channels=1) supported")
	}
	if cfg.BufferSamples <= 0 {
		cfg.BufferSamples = 1 << 16
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("playback: malgo InitContext: %w", err)
	}

	p := &Playback{
		cfg:     cfg,
		mctx:    mctx,
		ring:    ringbuf.New(cfg.BufferSamples),
		scratch: make([]float32, 0, 8192),
	}

	dCfg := malgo.DefaultDeviceConfig(malgo.Playback)
	dCfg.Playback.Format = malgo.FormatF32
	dCfg.Playback.Channels = uint32(cfg.Channels)
	dCfg.SampleRate = uint32(cfg.SampleRate)
	dCfg.Alsa.NoMMap = 1

	device, err := malgo.InitDevice(mctx.Context, dCfg, malgo.DeviceCallbacks{
		Data: p.onFrames,
	})
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return nil, fmt.Errorf("playback: malgo InitDevice: %w", err)
	}
	p.device = device
	return p, nil
}

// onFrames fills the host playback buffer from the ring. Out is
// len = n * 4 bytes (mono F32). When the ring underflows the rest is
// zero-filled.
func (p *Playback) onFrames(out, _ []byte, n uint32) {
	nSamples := int(n)
	wantBytes := nSamples * 4
	if wantBytes > len(out) {
		// Defensive: malgo should never hand a shorter buffer.
		wantBytes = len(out) &^ 3
		nSamples = wantBytes / 4
	}

	// Reinterpret the host byte buffer as []float32 in place. The
	// device was configured as FormatF32 mono, so its memory layout
	// is identical to a Go []float32 on every host we ship to
	// (little-endian, 4-byte aligned). This skips the per-sample
	// encode loop and lets ring.Read copy straight into the device
	// buffer with one memmove.
	dst := unsafe.Slice((*float32)(unsafe.Pointer(&out[0])), nSamples)

	got := p.ring.Read(dst)
	if got < nSamples {
		tail := dst[got:]
		for i := range tail {
			tail[i] = 0
		}
		if got == 0 {
			p.underruns.Add(1)
		}
	}
}

// Ring returns the underlying ring. Producers normally call Write
// instead, but Ring is exposed for tests and metrics.
func (p *Playback) Ring() *ringbuf.Ring { return p.ring }

// Write copies samples into the ring and returns the count actually
// stored. Short writes mean the consumer (host audio) is too slow —
// producers should typically pace themselves rather than retry tight
// loops.
func (p *Playback) Write(samples []float32) int {
	return p.ring.Write(samples)
}

// SampleRate is the device sample rate. Producers must hand audio at
// this rate; no resampling is performed here.
func (p *Playback) SampleRate() int { return p.cfg.SampleRate }

// Start begins playback.
func (p *Playback) Start() error {
	if !p.started.CompareAndSwap(false, true) {
		return errors.New("playback: already started")
	}
	if err := p.device.Start(); err != nil {
		p.started.Store(false)
		return fmt.Errorf("playback: device.Start: %w", err)
	}
	return nil
}

// Stop halts the device (idempotent).
func (p *Playback) Stop() error {
	if !p.started.CompareAndSwap(true, false) {
		return nil
	}
	if err := p.device.Stop(); err != nil {
		return fmt.Errorf("playback: device.Stop: %w", err)
	}
	return nil
}

// Close releases the device + context. Implicitly stops.
func (p *Playback) Close() error {
	_ = p.Stop()
	if p.device != nil {
		p.device.Uninit()
		p.device = nil
	}
	if p.mctx != nil {
		_ = p.mctx.Uninit()
		p.mctx.Free()
		p.mctx = nil
	}
	return nil
}

// Underruns returns the cumulative count of callback invocations
// that found the ring entirely empty — a backpressure signal that
// synthesis is slower than playback.
func (p *Playback) Underruns() uint64 { return p.underruns.Load() }
