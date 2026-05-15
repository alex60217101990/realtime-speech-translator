package app

import (
	"sync/atomic"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/ringbuf"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
)

// sttSink adapts a stt.Pipeline to the capture.Sink interface. It is
// invoked from the miniaudio capture thread.
type sttSink struct {
	pipeline *stt.Pipeline
	drops    *atomic.Uint64
}

// WriteSamples forwards captured samples to the STT pipeline. The VAD
// segmenter inside the pipeline buffers into pre-allocated frames.
func (s *sttSink) WriteSamples(frames []int16) {
	s.pipeline.WriteFrame(frames)
}

// playbackSink implements stt.AudioSink. Whenever the pipeline finishes
// a TTS synthesis it hands us the PCM, which we push into the playback
// ring buffer for the malgo callback to drain.
//
// Writes are chunked because the ring buffer's Write returns short when
// it is nearly full, and Piper emits whole utterances (~30–60 kB) that
// would otherwise drop on a single boundary.
type playbackSink struct {
	rb *ringbuf.Ring
}

// WritePCM pushes samples into the ring buffer. The call returns once
// either every sample has been queued or the buffer has been full for
// long enough that we give up and drop the tail. Because the pipeline
// goroutine is single-threaded and TTS already throttles to real time,
// in practice the ring is rarely full.
func (s *playbackSink) WritePCM(samples []int16) {
	off := 0
	// We allow up to two short-write rounds. If the audio device is
	// stalled longer than that, dropping the tail is the right
	// behaviour — better a clipped utterance than ever-growing
	// latency.
	for round := 0; off < len(samples) && round < 2; round++ {
		n := s.rb.Write(samples[off:])
		off += n
		if n == 0 {
			break
		}
	}
}

// playbackSource is the read side: malgo pulls samples through it.
type playbackSource struct {
	rb *ringbuf.Ring
}

// ReadSamples copies up to len(out) samples out of the ring. Short
// returns are filled with silence by the playback package.
func (s *playbackSource) ReadSamples(out []int16) int {
	return s.rb.Read(out)
}
