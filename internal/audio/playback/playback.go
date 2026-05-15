// Package playback wraps a miniaudio playback device and pulls int16 mono
// PCM frames from a Source interface. The Source is invoked from the
// real-time audio thread and must return promptly.
package playback

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// Source produces int16 mono PCM frames into a caller-supplied slice and
// returns the number of samples written. Underrun is signalled by writing
// fewer samples than len(out); the playback driver will fill the gap with
// silence.
type Source interface {
	ReadSamples(out []int16) int
}

// Config selects the output device and stream format.
type Config struct {
	SampleRate uint32
	DeviceID   *malgo.DeviceID
}

// Sink is a running or stopped playback stream.
type Sink struct {
	cfg       Config
	device    *malgo.Device
	src       Source
	running   atomic.Bool
	underruns atomic.Uint64
}

// New constructs but does not start a playback Sink.
func New(ctx *malgo.AllocatedContext, cfg Config, src Source) (*Sink, error) {
	if ctx == nil {
		return nil, errors.New("playback: nil context")
	}
	if src == nil {
		return nil, errors.New("playback: nil source")
	}
	if cfg.SampleRate == 0 {
		return nil, errors.New("playback: SampleRate must be set")
	}

	s := &Sink{cfg: cfg, src: src}

	dc := malgo.DefaultDeviceConfig(malgo.Playback)
	dc.Playback.Format = malgo.FormatS16
	dc.Playback.Channels = 1
	dc.SampleRate = cfg.SampleRate
	dc.Alsa.NoMMap = 1
	if cfg.DeviceID != nil {
		dc.Playback.DeviceID = cfg.DeviceID.Pointer()
	}

	dev, err := malgo.InitDevice(ctx.Context, dc, malgo.DeviceCallbacks{
		Data: s.onFrames,
	})
	if err != nil {
		return nil, fmt.Errorf("playback: init device: %w", err)
	}
	s.device = dev
	return s, nil
}

// onFrames is called by miniaudio's audio thread. pOutput points at the
// buffer to fill with the next framecount samples; pInput is unused.
func (s *Sink) onFrames(pOutput, _ []byte, framecount uint32) {
	if framecount == 0 || len(pOutput) == 0 {
		return
	}
	n := int(framecount)
	hdr := unsafe.SliceData(pOutput)
	out := unsafe.Slice((*int16)(unsafe.Pointer(hdr)), n)
	got := s.src.ReadSamples(out)
	if got < n {
		s.underruns.Add(uint64(n - got))
		// Zero the unfilled tail to play silence rather than stale memory.
		tail := out[got:]
		for i := range tail {
			tail[i] = 0
		}
	}
}

// Start begins playback. Idempotent.
func (s *Sink) Start() error {
	if s.running.Swap(true) {
		return nil
	}
	if err := s.device.Start(); err != nil {
		s.running.Store(false)
		return fmt.Errorf("playback: start device: %w", err)
	}
	return nil
}

// Stop halts playback.
func (s *Sink) Stop() error {
	if !s.running.Swap(false) {
		return nil
	}
	if err := s.device.Stop(); err != nil {
		return fmt.Errorf("playback: stop device: %w", err)
	}
	return nil
}

// Close releases the device.
func (s *Sink) Close() error {
	if s.device != nil {
		s.device.Uninit()
		s.device = nil
	}
	return nil
}

// Underruns reports the cumulative number of samples filled with silence
// because the Source returned short.
func (s *Sink) Underruns() uint64 { return s.underruns.Load() }
