package app

import (
	"sync/atomic"

	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
)

// sttSink adapts a stt.Pipeline to the capture.Sink interface. It is
// invoked from the miniaudio capture thread.
//
// WriteSamples does not allocate: the VAD segmenter buffers frames into
// pre-allocated slices internally. The cgo call into WebRTC VAD has
// fixed cost per 30 ms frame and is acceptable on the audio thread.
//
// drops is incremented when the segmenter's output channel is full and
// the consumer (UI / Whisper goroutine) cannot keep up — see
// vad.Segmenter.processFrame.
type sttSink struct {
	pipeline *stt.Pipeline
	drops    *atomic.Uint64
}

// WriteSamples forwards captured samples to the STT pipeline.
func (s *sttSink) WriteSamples(frames []int16) {
	s.pipeline.WriteFrame(frames)
}
