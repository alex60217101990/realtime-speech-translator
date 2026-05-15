package app

import (
	"sync/atomic"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/ringbuf"
)

// ringSink adapts a *ringbuf.Ring to the capture.Sink interface. It is
// invoked from the audio capture thread; the implementation is
// allocation-free.
type ringSink struct {
	rb    *ringbuf.Ring
	drops *atomic.Uint64
}

// WriteSamples copies frames into the ring. When the ring is full the
// overflow is dropped and the drop counter is incremented; the audio
// thread must never block.
func (s *ringSink) WriteSamples(frames []int16) {
	n := s.rb.Write(frames)
	if n < len(frames) {
		s.drops.Add(uint64(len(frames) - n))
	}
}

// ringSource adapts a *ringbuf.Ring to the playback.Source interface.
type ringSource struct {
	rb *ringbuf.Ring
}

// ReadSamples pulls frames from the ring. Returns the number actually
// read; underrun handling (silence fill) is performed by the playback
// device.
func (s *ringSource) ReadSamples(out []int16) int {
	return s.rb.Read(out)
}
