// Package capture wraps a miniaudio capture device and exposes captured
// int16 mono PCM frames through a Sink interface. It is intended for use
// behind a VAD / STT pipeline; resampling and channel conversion are not
// performed here and must be configured to match the requested format.
package capture

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// Sink consumes captured int16 mono PCM frames. Implementations must be
// safe to call from a real-time audio thread: no allocations, no blocking
// system calls. Returning quickly is mandatory; a slow Sink will cause
// audio glitches (DMA buffer overruns) on the input device.
type Sink interface {
	WriteSamples(frames []int16)
}

// Config selects the input device and stream format. SampleRate is in Hz;
// Channels is fixed to 1 (mono) by the package. DeviceID may be nil to use
// the system default.
type Config struct {
	SampleRate uint32
	DeviceID   *malgo.DeviceID
}

// Source is a running or stopped microphone capture stream.
type Source struct {
	cfg     Config
	device  *malgo.Device
	sink    Sink
	running atomic.Bool
	dropped atomic.Uint64
	frames  atomic.Uint64 // cumulative sample count actually received
	peakAbs atomic.Int32  // max |int16| seen since last reset
}

// New constructs but does not start a capture Source. The given context
// must outlive the Source (the device pins it). The Source owns no other
// resources until Start is called.
func New(ctx *malgo.AllocatedContext, cfg Config, sink Sink) (*Source, error) {
	if ctx == nil {
		return nil, errors.New("capture: nil context")
	}
	if sink == nil {
		return nil, errors.New("capture: nil sink")
	}
	if cfg.SampleRate == 0 {
		return nil, errors.New("capture: SampleRate must be set")
	}

	s := &Source{cfg: cfg, sink: sink}

	dc := malgo.DefaultDeviceConfig(malgo.Capture)
	dc.Capture.Format = malgo.FormatS16
	dc.Capture.Channels = 1
	dc.SampleRate = cfg.SampleRate
	dc.Alsa.NoMMap = 1
	if cfg.DeviceID != nil {
		dc.Capture.DeviceID = cfg.DeviceID.Pointer()
	}

	dev, err := malgo.InitDevice(ctx.Context, dc, malgo.DeviceCallbacks{
		Data: s.onFrames,
	})
	if err != nil {
		return nil, fmt.Errorf("capture: init device: %w", err)
	}
	s.device = dev
	return s, nil
}

// onFrames is invoked from miniaudio's audio thread.
// pSample contains the just-captured PCM data; pSample2 is unused for
// capture-only devices. The slice is owned by miniaudio and reused across
// callbacks — implementations must not retain it.
func (s *Source) onFrames(_, pSample []byte, framecount uint32) {
	if framecount == 0 || len(pSample) == 0 {
		return
	}
	// View the byte slice as []int16 without copying. Safe: miniaudio
	// guarantees the buffer is at least framecount*Channels*sizeof(int16)
	// bytes for FormatS16 mono.
	n := int(framecount)
	hdr := unsafe.SliceData(pSample)
	samples := unsafe.Slice((*int16)(unsafe.Pointer(hdr)), n)
	// Cheap audio-thread instrumentation: cumulative samples + peak
	// abs amplitude. Peak is computed inline; min/sum would cost more
	// arithmetic than we want here.
	s.frames.Add(uint64(n))
	var peak int16
	for _, v := range samples {
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if int32(peak) > s.peakAbs.Load() {
		s.peakAbs.Store(int32(peak))
	}
	s.sink.WriteSamples(samples)
}

// Start begins capture. Idempotent: calling Start on a running Source
// returns nil without effect.
func (s *Source) Start() error {
	if s.running.Swap(true) {
		return nil
	}
	if err := s.device.Start(); err != nil {
		s.running.Store(false)
		return fmt.Errorf("capture: start device: %w", err)
	}
	return nil
}

// Stop halts capture without releasing the device. Start may be called
// again to resume.
func (s *Source) Stop() error {
	if !s.running.Swap(false) {
		return nil
	}
	if err := s.device.Stop(); err != nil {
		return fmt.Errorf("capture: stop device: %w", err)
	}
	return nil
}

// Close releases the underlying device. The Source must not be used
// afterward.
func (s *Source) Close() error {
	if s.device != nil {
		s.device.Uninit()
		s.device = nil
	}
	return nil
}

// DroppedFrames reports the number of audio frames the Sink failed to
// consume (incremented by ringbuf-backed sinks when Write returns short).
// Reads-only counter; Sink implementations are responsible for updating it
// via WithDropCounter wrappers if they want the metric surfaced.
func (s *Source) DroppedFrames() uint64 { return s.dropped.Load() }

// CapturedSamples returns the cumulative number of samples received
// from the device. Stays 0 if the audio callback never fires.
func (s *Source) CapturedSamples() uint64 { return s.frames.Load() }

// PeakAbs returns the maximum |int16| seen since the last PeakAbsReset.
// Use it as a "is anyone speaking" indicator; 0 = silence / dead mic.
func (s *Source) PeakAbs() int16 { return int16(s.peakAbs.Load()) }

// PeakAbsReset zeroes the peak counter so the next sample window starts
// fresh.
func (s *Source) PeakAbsReset() { s.peakAbs.Store(0) }

// AddDropped is exposed for Sink wrappers that wish to report overflow
// back to the Source for metrics aggregation.
func (s *Source) AddDropped(n uint64) { s.dropped.Add(n) }

// HealthReport summarises the actual device parameters miniaudio
// negotiated, contrasted with what we requested.
type HealthReport struct {
	RequestedRate     uint32
	InternalRate      uint32
	InternalChannels  uint32
	InternalFormatBad bool
	// HFPSuspect is true when the negotiated rate looks like Bluetooth
	// HFP (8000 or 16000 Hz mono with degraded codec) — surfaces a UI
	// warning so the user understands why audio quality dropped after
	// the headset connected.
	HFPSuspect bool
}

// Health reports the device's negotiated parameters. May be called any
// time after New(); returns zero values if the device has been closed.
func (s *Source) Health() HealthReport {
	if s.device == nil {
		return HealthReport{}
	}
	r := HealthReport{
		RequestedRate:    s.cfg.SampleRate,
		InternalRate:     s.device.CaptureInternalSampleRate(),
		InternalChannels: s.device.CaptureInternalChannels(),
	}
	// HFP heuristic: BT headsets in HFP mode commonly negotiate
	// 8 kHz / 16 kHz mono SCO. If the user's headset *should* be on
	// A2DP but the mic is open, OS forces SCO and we end up here.
	if r.InternalChannels == 1 && (r.InternalRate == 8000 || r.InternalRate == 16000) &&
		r.InternalRate < r.RequestedRate {
		r.HFPSuspect = true
	}
	if s.device.CaptureInternalFormat() != malgo.FormatS16 {
		r.InternalFormatBad = true
	}
	return r
}
