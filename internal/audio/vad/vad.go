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

// pcmPool recycles the per-utterance []int16 copy emitted on the
// Output channel. Each utterance currently allocates fresh on close;
// with the pool the GC sees zero churn on the audio thread once the
// pool is warmed (one slice per concurrent utterance, which is at most
// 4 — the Output channel cap). Buckets are size-typical (6 s @ 16 kHz =
// 96000 int16 = 192 KB), so most reuses skip allocation entirely.
var pcmPool = sync.Pool{
	New: func() any {
		// Pre-size to a typical max-length utterance so first use
		// avoids a grow. Cap-only; len reset by the caller.
		b := make([]int16, 0, 6*SampleRate)
		return &b
	},
}

// acquirePCM returns a []int16 of exactly n elements, reusing pool
// memory when capacity allows. Tiny on purpose so the mid-stack
// inliner brings it into processFrame and SnapshotCurrent. The slow
// path (cap too small) is split out so the alloc does not bloat the
// inlined size budget.
func acquirePCM(n int) []int16 {
	bp := pcmPool.Get().(*[]int16)
	if cap(*bp) < n {
		return acquirePCMSlow(n)
	}
	return (*bp)[:n]
}

func acquirePCMSlow(n int) []int16 {
	// Pool entry too small for this utterance — allocate fresh and
	// let the old slice fall out of scope. ReleasePCM will deposit
	// the new larger backing the next time around.
	return make([]int16, n)
}

// ReleasePCM returns an utterance PCM slice to the pool. Callers must
// drop their reference to b after this call. Safe to call with cap(b)==0.
func ReleasePCM(b []int16) {
	if cap(b) == 0 {
		return
	}
	b = b[:0]
	pcmPool.Put(&b)
}

// int16PeakAbs returns the maximum absolute sample value in s. -32768
// is clamped to 32767 because abs(int16(-32768)) would overflow; the
// gate compares against a float64 noise floor so the clamp is harmless
// (one count of imprecision on the most-saturated sample).
func int16PeakAbs(s []int16) int32 {
	var peak int32
	for _, v := range s {
		a := int32(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak < 0 {
		peak = 32767
	}
	return peak
}

// SampleRate is the only rate supported by this segmenter. Whisper input
// is 16 kHz mono, so we match it.
const SampleRate = 16000

// SamplesPerFrame is the number of int16 samples per VAD frame.
const SamplesPerFrame = SampleRate * FrameMs / 1000 // 480

// Utterance is a contiguous speech segment with hangover-padded silence
// trimmed off. PCM is owned by the receiver after read; the segmenter
// does not retain it.
type Utterance struct {
	PCM []int16
	// StartedAt is the wall-clock moment the first speech frame of this
	// utterance was observed.
	StartedAt time.Time
	// Duration is the active speech length (hangover excluded).
	Duration time.Duration
	// EmittedAt is the wall-clock moment the segmenter decided the
	// utterance was complete and pushed it onto the output channel.
	// (StartedAt + Duration) ≈ end-of-speech; EmittedAt - that gap
	// equals the hangover wait the user perceives before transcription
	// can start.
	EmittedAt time.Time
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
//
// HangoverMs/MaxSpeechMs are tuned for low perceived latency: a 200 ms
// hangover still keeps natural mid-sentence pauses joined while
// shaving 100 ms off the user-visible lag, and a 6 s MaxSpeechMs caps
// the worst-case Whisper input length so end-to-end latency cannot
// blow past ~real-time × utterance even on a long monologue.
func DefaultConfig() Config {
	return Config{
		// Aggressiveness 2 strips most background noise. Mode 1 lets
		// quiet breath/room tone into the PCM, which Whisper then
		// transcribes as plausible-sounding garbage ("ревудишь",
		// "вакуи фетилейный"). Stay strict.
		Aggressiveness: 2,
		// 300 ms floor blocks obvious noise burst durations (clicks,
		// keypresses) while letting terse "да" / "ок" replies through.
		// The energy gate (see processFrame) is the primary defence
		// against hallucination-triggering low-RMS utterances.
		MinSpeechMs: 300,
		// 12 s cap keeps a long monologue in one utterance.
		MaxSpeechMs: 12000,
		// 200 ms hangover forces any real pause to close the utterance.
		// Whisper never sees a multi-hundred-ms silence gap *inside*
		// the audio (the source of "inserted words after a pause"
		// hallucinations). The MT-layer stitching guard in
		// internal/stt/pipeline.go glues the resulting fragments back
		// into one translated thought when the first lacks terminal
		// punctuation.
		HangoverMs: 200,
		PrePadMs:   150,
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

	// bgPeak is an EWMA of the per-silence-frame peak absolute amplitude.
	// Used as the noise floor for the energy gate that drops "speech"
	// classified by WebRTC VAD but whose amplitude is indistinguishable
	// from room tone — the most common source of Whisper hallucinations
	// like "Тельно раздевать", "Хоты!", "на".
	bgPeak float64

	out chan Utterance
	mu  sync.Mutex // serialises WriteFrame calls

	// instrumentation surfaced to the UI / logs so it is obvious
	// whether VAD is firing at all.
	activeFrames atomic.Uint64
	totalFrames  atomic.Uint64
	utterances   atomic.Uint64
	drops        atomic.Uint64
	energyDrops  atomic.Uint64
}

const (
	// energyEWMAAlpha is the smoothing factor for the background-noise
	// estimate. 0.97 over 30 ms frames means ~1 s time constant — fast
	// enough to track room changes (fan on/off), slow enough that a
	// single loud sample does not poison the floor.
	energyEWMAAlpha = 0.97
	// energyGateRatio is how many times the noise floor an utterance's
	// peak amplitude must exceed to be considered real speech. Picked
	// empirically: WebRTC VAD already filters obvious noise; the gate
	// only needs to catch the borderline cases the decoder hallucinates
	// over.
	energyGateRatio = 5.0
	// energyAbsFloor is the absolute minimum peak amplitude (int16)
	// below which the utterance is rejected regardless of noise floor.
	// Prevents a clean-room session with bgPeak ≈ 0 from accepting
	// trivially quiet frames. 800 ≈ 2.5 % of full scale.
	energyAbsFloor = 800
)

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
		// Track the per-frame peak as the running noise floor while
		// nothing is speaking. EWMA so a single loud transient (door,
		// keypress) does not raise the floor for long.
		if !active {
			framePeak := float64(int16PeakAbs(frame))
			if s.bgPeak == 0 {
				s.bgPeak = framePeak
			} else {
				s.bgPeak = energyEWMAAlpha*s.bgPeak + (1.0-energyEWMAAlpha)*framePeak
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
		// Trim trailing silence the hangover accumulated before the
		// close: Whisper hallucinates plausible-sounding nonsense when
		// fed audio that ends with >100 ms of room tone, so we keep a
		// short pad and drop the rest. Only the hangover-close path
		// has this padding to remove; max-speech force-close emits
		// raw speech and skips the trim.
		emitLen := len(s.curr)
		if closeReason == "hangover" {
			const trailingPadMs = 100
			cutMs := s.silenceMs - trailingPadMs
			if cutMs > 0 {
				cutSamples := cutMs * SampleRate / 1000
				if cutSamples < emitLen {
					emitLen -= cutSamples
				}
			}
		}
		// Energy gate. WebRTC VAD classified these frames as speech
		// but the utterance peak amplitude is too close to the running
		// noise floor — borderline-energy utterances are precisely
		// where Whisper invents text. Drop instead of emitting.
		uttPeak := float64(int16PeakAbs(s.curr[:emitLen]))
		threshold := s.bgPeak * energyGateRatio
		if threshold < energyAbsFloor {
			threshold = energyAbsFloor
		}
		if uttPeak < threshold {
			s.energyDrops.Add(1)
			s.speaking = false
			s.silenceMs = 0
			s.speechMs = 0
			s.curr = s.curr[:0]
			s.prePadN = 0
			return
		}
		// pcm is pool-allocated; downstream consumer (stt.Pipeline)
		// must call vad.ReleasePCM once Transcribe returns.
		pcm := acquirePCM(emitLen)
		copy(pcm, s.curr[:emitLen])
		ut := Utterance{
			PCM:       pcm,
			StartedAt: s.startedAt,
			Duration:  time.Duration(s.speechMs) * time.Millisecond,
			EmittedAt: time.Now(),
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

// IsSpeaking reports whether the segmenter is currently inside an
// utterance (between speech onset and hangover close). Read under the
// frame mutex so callers see a value consistent with the most recent
// processFrame; the lock is short and contention-free in practice
// since WriteFrame is the only writer.
func (s *Segmenter) IsSpeaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speaking
}

// SnapshotCurrent returns a copy of the accumulating utterance buffer
// alongside its current speech duration. Used by the partial-transcript
// path: while an utterance is still open, the pipeline runs Whisper
// against the snapshot to produce a "what's been said so far" preview.
//
// Returns (nil, 0) when the segmenter is not currently inside an
// utterance (no speech in progress). Caller owns the returned slice.
func (s *Segmenter) SnapshotCurrent() ([]int16, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.speaking || len(s.curr) == 0 {
		return nil, 0
	}
	// Pool-allocated; partial pass in stt.Pipeline.runPartials must
	// call vad.ReleasePCM once Transcribe returns.
	pcm := acquirePCM(len(s.curr))
	copy(pcm, s.curr)
	return pcm, time.Duration(s.speechMs) * time.Millisecond
}

// Close releases the WebRTC VAD context and closes the output channel.
// Must not be called concurrently with WriteFrame.
func (s *Segmenter) Close() error {
	close(s.out)
	// Drop the VAD pointer so the finalizer can free it on GC.
	s.vad = nil
	return nil
}
