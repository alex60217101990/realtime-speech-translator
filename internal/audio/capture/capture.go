// Package capture opens the default audio input device, normalises
// the incoming PCM stream to 16 kHz mono float32, and hands every
// chunk to a caller-supplied push function from inside the malgo
// audio callback.
//
// Hot-path:
//
//	host driver ──S16LE──▶ malgo callback ──▶ sample.Int16ToFloat32 (SSE2)
//	                                                │
//	                                                ▼
//	                                    PushFunc(samples []float32)
//	                                    (must not retain `samples`)
//
// The float32 buffer handed to PushFunc is borrowed from an internal
// sync.Pool and recycled on return — keeping the audio callback
// allocation-free at steady state.
package capture

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/sample"
)

// Config configures the capture device.
type Config struct {
	SampleRate int // 16000 for the stt engine
	Channels   int // 1
}

// DefaultConfig returns the parameters the rest of the application
// expects: 16 kHz mono.
func DefaultConfig() Config {
	return Config{SampleRate: 16000, Channels: 1}
}

// PushFunc receives one chunk of float32 PCM samples. The slice is
// valid only for the duration of the call: it lives in a sync.Pool
// and is recycled as soon as PushFunc returns. The consumer (STT
// engine.Push) must copy if it needs to retain.
type PushFunc func(samples []float32)

// Capture wraps a malgo capture device. Construct via New, drive via
// Start / Stop, release via Close.
type Capture struct {
	cfg    Config
	mctx   *malgo.AllocatedContext
	device *malgo.Device

	push PushFunc
	pool sync.Pool

	dropped atomic.Uint64
	started atomic.Bool
}

// New constructs a capture pipeline bound to the default input
// device. The PushFunc is invoked from the audio callback for every
// incoming frame; do not call any host-blocking API from inside it.
func New(cfg Config, push PushFunc) (*Capture, error) {
	if push == nil {
		return nil, errors.New("capture: nil PushFunc")
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 16000
	}
	if cfg.Channels <= 0 {
		cfg.Channels = 1
	}
	if cfg.Channels != 1 {
		return nil, errors.New("capture: only mono (Channels=1) supported")
	}

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("capture: malgo InitContext: %w", err)
	}

	c := &Capture{
		cfg:  cfg,
		mctx: mctx,
		push: push,
	}
	c.pool.New = func() any {
		buf := make([]float32, 0, 4096)
		return &buf
	}

	dCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	dCfg.Capture.Format = malgo.FormatS16
	dCfg.Capture.Channels = uint32(cfg.Channels)
	dCfg.SampleRate = uint32(cfg.SampleRate)
	dCfg.Alsa.NoMMap = 1

	device, err := malgo.InitDevice(mctx.Context, dCfg, malgo.DeviceCallbacks{
		Data: c.onFrames,
	})
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return nil, fmt.Errorf("capture: malgo InitDevice: %w", err)
	}
	c.device = device
	return c, nil
}

// onFrames is the malgo capture callback. n is the number of frames
// (samples per channel); for mono the byte length is n * 2.
func (c *Capture) onFrames(_, in []byte, n uint32) {
	if n == 0 {
		return
	}
	nSamples := int(n)
	required := nSamples * 2
	if required > len(in) {
		c.dropped.Add(1)
		return
	}

	bufPtr := c.pool.Get().(*[]float32)
	buf := *bufPtr
	if cap(buf) < nSamples {
		buf = make([]float32, nSamples)
	} else {
		buf = buf[:nSamples]
	}

	// SIMD int16 → float32.
	src := unsafeBytesToInt16(in[:required])
	sample.Int16ToFloat32(src, buf)

	c.push(buf)

	*bufPtr = buf[:0]
	c.pool.Put(bufPtr)
}

// Start begins capturing. Returns an error if the device cannot be
// started or has already been started.
func (c *Capture) Start() error {
	if !c.started.CompareAndSwap(false, true) {
		return errors.New("capture: already started")
	}
	if err := c.device.Start(); err != nil {
		c.started.Store(false)
		return fmt.Errorf("capture: device.Start: %w", err)
	}
	return nil
}

// Stop halts the device (idempotent).
func (c *Capture) Stop() error {
	if !c.started.CompareAndSwap(true, false) {
		return nil
	}
	if err := c.device.Stop(); err != nil {
		return fmt.Errorf("capture: device.Stop: %w", err)
	}
	return nil
}

// Close releases the device and malgo context. Implicitly stops if
// still running. Safe to call multiple times.
func (c *Capture) Close() error {
	_ = c.Stop()
	if c.device != nil {
		c.device.Uninit()
		c.device = nil
	}
	if c.mctx != nil {
		_ = c.mctx.Uninit()
		c.mctx.Free()
		c.mctx = nil
	}
	return nil
}

// Dropped returns the cumulative count of malgo frames discarded
// because the input byte buffer was shorter than the declared sample
// count — a host driver bug rather than a backpressure signal.
func (c *Capture) Dropped() uint64 { return c.dropped.Load() }
