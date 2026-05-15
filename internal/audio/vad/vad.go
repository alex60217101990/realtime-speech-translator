// Package vad implements an utterance segmenter on top of the WebRTC
// voice-activity detector. It consumes 16 kHz mono int16 PCM and emits
// speech segments (utterances) bounded by hangover silence.
//
// The segmenter is single-goroutine; callers feed frames in via WriteFrame
// and receive completed utterances on the Output channel.
package vad

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	webrtcvad "github.com/maxhawkins/go-webrtcvad"
)

// FrameMs is the fixed analysis frame size handed to WebRTC VAD.
// WebRTC supports 10/20/30 ms; 30 ms gives the best stability for speech.
const FrameMs = 30

// SampleRate is the only rate supported by this segmenter. Whisper input
// is 16 kHz mono, so we match it.
const SampleRate = 16000

// SamplesPerFrame is the number of int16 samples per VAD frame.
const SamplesPerFrame = SampleRate * FrameMs / 1000 // 480

// Utterance is a contiguous speech segment with hangover-padded silence
// trimmed off. PCM is owned by the receiver after read; the segmenter
// does not retain it.
type Utterance struct {
	PCM       []int16
	StartedAt time.Time
	Duration  time.Duration
}

// Config controls segmentation behaviour.
type Config struct {
	// Aggressiveness selects the WebRTC VAD mode in [0, 3]. Higher
	// values reject more non-speech but may also clip short syllables.
	Aggressiveness int

	// MinSpeechMs is the shortest accepted utterance. Shorter speech
	// bursts (clicks, doors, breathing) are discarded.
	MinSpeechMs int

	// MaxSpeechMs forces an utterance close after this much continuous
	// speech, so a single long monologue does not stall Whisper.
	MaxSpeechMs int

	// HangoverMs is the trailing silence required to close an utterance.
	// 300 ms is a common compromise between fast turn-taking and
	// fragmenting sentences mid-pause.
	HangoverMs int

	// PrePadMs are kept before the first detected speech frame so that
	// onset consonants are not clipped. Limited by the internal ring
	// of past silence frames.
	PrePadMs int
}

// DefaultConfig returns a Config tuned for conversational speech at
// 16 kHz mono.
func DefaultConfig() Config {
	return Config{
		Aggressiveness: 2,
		MinSpeechMs:    200,
		MaxSpeechMs:    15000,
		HangoverMs:     300,
		PrePadMs:       150,
	}
}

// Segmenter consumes 16 kHz mono int16 PCM in arbitrary block sizes,
// chunks it to 30 ms frames internally, and emits whole utterances on
// the Output channel.
type Segmenter struct {
	cfg       Config
	vad       *webrtcvad.VAD
	frameBuf  []int16
	prePad    []int16
	prePadN   int
	curr      []int16
	startedAt time.Time
	silenceMs int
	speechMs  int // active-only duration; excludes hangover and pre-pad
	speaking  bool

	out chan Utterance
	mu  sync.Mutex // serialises WriteFrame calls

	// instrumentation surfaced to the UI / logs so it is obvious
	// whether VAD is firing at all.
	activeFrames atomic.Uint64
	totalFrames  atomic.Uint64
	utterances   atomic.Uint64
	drops        atomic.Uint64
}

// New constructs a Segmenter. The Output channel is buffered (cap=4) so
// a slow consumer back-pressures by dropping subsequent utterances when
// the channel is full.
func New(cfg Config) (*Segmenter, error) {
	if cfg.Aggressiveness < 0 || cfg.Aggressiveness > 3 {
		return nil, errors.New("vad: Aggressiveness must be in [0,3]")
	}
	v, err := webrtcvad.New()
	if err != nil {
		return nil, fmt.Errorf("vad: new: %w", err)
	}
	if err := v.SetMode(cfg.Aggressiveness); err != nil {
		return nil, fmt.Errorf("vad: set mode: %w", err)
	}
	prePadFrames := cfg.PrePadMs / FrameMs
	return &Segmenter{
		cfg:      cfg,
		vad:      v,
		frameBuf: make([]int16, 0, SamplesPerFrame),
		prePad:   make([]int16, prePadFrames*SamplesPerFrame),
		curr:     make([]int16, 0, cfg.MaxSpeechMs*SampleRate/1000),
		out:      make(chan Utterance, 4),
	}, nil
}

// Output returns the channel that emits utterances. Closed when Close is
// called.
func (s *Segmenter) Output() <-chan Utterance { return s.out }

// WriteFrame appends samples to the segmenter. It is safe to call from
// any single goroutine; concurrent callers are serialised internally.
// The slice is not retained.
func (s *Segmenter) WriteFrame(samples []int16) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for off := 0; off < len(samples); {
		need := SamplesPerFrame - len(s.frameBuf)
		take := min(len(samples) - off, need)
		s.frameBuf = append(s.frameBuf, samples[off:off+take]...)
		off += take
		if len(s.frameBuf) == SamplesPerFrame {
			s.processFrame(s.frameBuf)
			s.frameBuf = s.frameBuf[:0]
		}
	}
}

// processFrame is called for every complete 30 ms VAD frame.
func (s *Segmenter) processFrame(frame []int16) {
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(&frame[0])), len(frame)*2)
	active, err := s.vad.Process(SampleRate, bytes)
	if err != nil {
		// A malformed frame is non-fatal for the stream; skip it.
		return
	}
	s.totalFrames.Add(1)
	if active {
		s.activeFrames.Add(1)
	}

	if !s.speaking {
		// Roll the pre-pad ring with this silence frame so that, on
		// onset, we can prepend a short tail of pre-speech audio.
		if len(s.prePad) > 0 {
			copy(s.prePad, s.prePad[SamplesPerFrame:])
			copy(s.prePad[len(s.prePad)-SamplesPerFrame:], frame)
			if s.prePadN < len(s.prePad) {
				s.prePadN += SamplesPerFrame
			}
		}
		if active {
			s.speaking = true
			s.startedAt = time.Now()
			s.silenceMs = 0
			s.speechMs = FrameMs
			s.curr = s.curr[:0]
			if s.prePadN > 0 {
				s.curr = append(s.curr, s.prePad[len(s.prePad)-s.prePadN:]...)
			}
			s.curr = append(s.curr, frame...)
		}
		return
	}

	// We are currently inside an utterance.
	s.curr = append(s.curr, frame...)
	if active {
		s.silenceMs = 0
		s.speechMs += FrameMs
	} else {
		s.silenceMs += FrameMs
	}

	closeReason := ""
	switch {
	case s.silenceMs >= s.cfg.HangoverMs:
		closeReason = "hangover"
	case s.speechMs >= s.cfg.MaxSpeechMs:
		closeReason = "max-speech"
	}
	if closeReason == "" {
		return
	}

	if s.speechMs >= s.cfg.MinSpeechMs {
		// Copy out the utterance — the internal buffer is reused.
		pcm := make([]int16, len(s.curr))
		copy(pcm, s.curr)
		ut := Utterance{
			PCM:       pcm,
			StartedAt: s.startedAt,
			Duration:  time.Duration(s.speechMs) * time.Millisecond,
		}
		select {
		case s.out <- ut:
			s.utterances.Add(1)
		default:
			// Consumer is behind; drop this utterance rather than
			// blocking the audio thread.
			s.drops.Add(1)
		}
	}
	s.speaking = false
	s.silenceMs = 0
	s.speechMs = 0
	s.curr = s.curr[:0]
	s.prePadN = 0
}

// Stats returns lightweight counters useful for diagnostics:
// total frames analysed, frames that VAD classified as active speech,
// finished utterances emitted on Output, and utterances dropped
// because the consumer was full.
type Stats struct {
	TotalFrames  uint64
	ActiveFrames uint64
	Utterances   uint64
	Drops        uint64
}

// Stats snapshot for the UI status line.
func (s *Segmenter) Stats() Stats {
	return Stats{
		TotalFrames:  s.totalFrames.Load(),
		ActiveFrames: s.activeFrames.Load(),
		Utterances:   s.utterances.Load(),
		Drops:        s.drops.Load(),
	}
}

// Close releases the WebRTC VAD context and closes the output channel.
// Must not be called concurrently with WriteFrame.
func (s *Segmenter) Close() error {
	close(s.out)
	// Drop the VAD pointer so the finalizer can free it on GC.
	s.vad = nil
	return nil
}
