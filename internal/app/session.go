// Package app wires the realtime-speech-translator pipeline together.
//
// Hot-path:
//
//	mic ──▶ capture ──▶ stt.Engine.Push
//	                        │
//	                        ▼
//	                   stt goroutine
//	                        │
//	          ┌─────────────┴────────────────┐
//	          ▼                              ▼
//	     Partial                          Final
//	          │                              │
//	          ▼                              ▼
//	  ui Events chan          mtQueue (drop on overflow)
//	                                         │
//	                                  mt goroutine
//	                                         │
//	                                         ▼
//	                                ui Translation event
//	                                         │
//	                                  ttsQueue (drop on overflow)
//	                                         │
//	                                  tts goroutine
//	                                         │
//	                                         ▼
//	                                playback.Ring.Write
//	                                         │
//	                                         ▼
//	                                   speaker callback
//
// Every stage between mic and speaker is independent — backpressure
// is handled by dropping at the queue layer rather than blocking the
// audio thread.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/capture"
	"github.com/alex60217101990/realtime-speech-translator/internal/audio/playback"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts"
)

// Config bundles everything Session needs to wire the pipeline. STT
// is now a pre-built Backend (cmd layer chose between streaming
// Engine and VadNemoEngine); MT comes pre-built (Disabled by
// default), already wrapped in mt.Serial and any caching the cmd
// chooses to apply.
type Config struct {
	// Source / Target are ISO-639-1 codes; "auto" allowed for source.
	Source string
	Target string

	// STTBackend is the constructed stt.Backend. New(Config) takes
	// it as-is — the cmd layer is responsible for the right backend
	// flavour per model.
	STTBackend stt.Backend
	// STTSampleRate is the rate the backend was configured at; used
	// to open the capture device at the matching rate.
	STTSampleRate int

	TTS tts.Config

	// MT is the translation backend. Nil falls back to mt.Disabled
	// (the source text is forwarded as-is).
	MT mt.Engine

	// TTSEnabled toggles synthesis + playback. When false the
	// pipeline stops after MT and emits only text events.
	TTSEnabled bool

	// Queue depths between stages. Small on purpose: better to drop
	// stale text than to fall behind the live conversation.
	MTQueue  int // default 4
	TTSQueue int // default 2

	// PlaybackBufSamples sizes the playback ring (rounds up to pow2).
	// Zero falls back to playback.DefaultConfig().
	PlaybackBufSamples int

	Logger *slog.Logger
}

// Event is the union surfaced to the UI / CLI consumer.
type Event interface{ isAppEvent() }

// Partial is the live STT transcript; it can change every frame.
type Partial struct{ Text string }

// Final is the closed STT utterance for a finished segment.
type Final struct{ Text string }

// Translation pairs a finalised source sentence with its target.
type Translation struct {
	Source, Target string
}

// Speaking is emitted around TTS playback bursts so the UI can show
// an indicator.
type Speaking struct{ Active bool }

// ErrorEv carries a non-fatal stage error to the UI.
type ErrorEv struct{ Err error }

func (Partial) isAppEvent()     {}
func (Final) isAppEvent()       {}
func (Translation) isAppEvent() {}
func (Speaking) isAppEvent()    {}
func (ErrorEv) isAppEvent()     {}

// Session owns the engines + audio devices. Construct with New,
// start with Start (non-blocking — workers run in background), stop
// with Stop, release with Close.
type Session struct {
	cfg Config
	log *slog.Logger

	cap *capture.Capture
	stt stt.Backend
	tts *tts.Engine
	pb  *playback.Playback
	mt  mt.Engine

	events chan Event

	mtQ  chan string
	ttsQ chan string

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mtDrops  atomic.Uint64
	ttsDrops atomic.Uint64
	running  atomic.Bool

	// Rolling last-N timings, stored as ns. Loaded by the UI poll.
	mtLastNs  atomic.Uint64
	ttsLastNs atomic.Uint64

	// Half-duplex gate: while the TTS engine is emitting audio we
	// must not feed mic capture into the recognizer, otherwise the
	// loudspeaker → mic loop spirals into a feedback recursion where
	// the recognizer transcribes its own previous output. Set to
	// the wall-clock end of the current Speak (plus a small tail)
	// every time runTTS emits.
	muteUntilNs atomic.Int64
	micDropped  atomic.Uint64

	// Sentence stitching state. Only touched from routeSTT.
	stitch stitchState
}

// Sentence-stitching tunables. Same values as perf/simd-mt-tuning.
const (
	stitchWindow         = 700 * time.Millisecond // wait this long for the next fragment
	stitchMaxFragments   = 4                      // never stitch more than this many finals
	stitchMaxFragmentSec = 4                      // a Final longer than this is flushed on its own
)

// Half-duplex tail (mute hold-down after the playback ring drains).
// 1.5 s covers room reverberation + Silero VAD onset latency + ASR
// segment release; anything shorter lets the speaker tail bleed
// into a new utterance the recognizer transcribes back as garbage.
const halfDuplexTailMs = 1500 * time.Millisecond

// stitchState buffers consecutive Finals that arrive within
// stitchWindow of each other and lack terminal punctuation, so the
// MT engine receives one coherent sentence instead of two clauses.
type stitchState struct {
	buf      []string
	firstAt  time.Time
	timer    *time.Timer
	flushCh  chan string // capacity 1; routeSTT consumes when timer fires
}

// New constructs the Session, opens the audio devices and loads the
// models. It does not yet start any goroutine — call Start.
func New(cfg Config) (*Session, error) {
	if cfg.Source == "" {
		return nil, errors.New("app: empty source language")
	}
	if cfg.Target == "" {
		return nil, errors.New("app: empty target language")
	}
	if cfg.MTQueue <= 0 {
		cfg.MTQueue = 4
	}
	if cfg.TTSQueue <= 0 {
		cfg.TTSQueue = 2
	}
	if cfg.MT == nil {
		cfg.MT = mt.Disabled{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.STTBackend == nil {
		return nil, errors.New("app: STTBackend required")
	}
	if cfg.STTSampleRate == 0 {
		cfg.STTSampleRate = 16000
	}

	s := &Session{
		cfg:    cfg,
		log:    cfg.Logger,
		stt:    cfg.STTBackend,
		mt:     cfg.MT,
		events: make(chan Event, 32),
		mtQ:    make(chan string, cfg.MTQueue),
		ttsQ:   make(chan string, cfg.TTSQueue),
	}

	// Wrap stt.Push with a half-duplex gate so audio captured while
	// TTS is playing never reaches the recognizer.
	push := func(samples []float32) {
		if time.Now().UnixNano() < s.muteUntilNs.Load() {
			s.micDropped.Add(1)
			return
		}
		s.stt.Push(samples)
	}
	cap, err := capture.New(
		capture.Config{SampleRate: cfg.STTSampleRate, Channels: 1},
		push,
	)
	if err != nil {
		_ = cfg.STTBackend.Close()
		return nil, fmt.Errorf("app: capture: %w", err)
	}
	s.cap = cap

	if cfg.TTSEnabled {
		ttsEngine, err := tts.New(cfg.TTS)
		if err != nil {
			_ = cap.Close()
			_ = cfg.STTBackend.Close()
			return nil, fmt.Errorf("app: tts: %w", err)
		}
		s.tts = ttsEngine

		pbCfg := playback.Config{
			SampleRate:    ttsEngine.SampleRate(),
			Channels:      1,
			BufferSamples: cfg.PlaybackBufSamples,
		}
		if pbCfg.BufferSamples == 0 {
			pbCfg = playback.DefaultConfig()
			pbCfg.SampleRate = ttsEngine.SampleRate()
		}
		pb, err := playback.New(pbCfg)
		if err != nil {
			_ = ttsEngine.Close()
			_ = cap.Close()
			_ = cfg.STTBackend.Close()
			return nil, fmt.Errorf("app: playback: %w", err)
		}
		s.pb = pb
	}

	return s, nil
}

// Events returns the read end of the event stream. Closes when Stop
// completes draining or Close releases the session.
func (s *Session) Events() <-chan Event { return s.events }

// Start launches the engines and audio devices. Returns once
// background goroutines are running; subsequent Stop/Close handle
// shutdown. ctx cancellation triggers a clean Stop.
func (s *Session) Start(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("app: already started")
	}
	s.log.Info("session starting",
		"source", s.cfg.Source,
		"target", s.cfg.Target,
		"tts_enabled", s.cfg.TTSEnabled,
		"mt_engine", fmt.Sprintf("%T", s.mt),
		"mt_queue", s.cfg.MTQueue,
		"tts_queue", s.cfg.TTSQueue,
	)

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// STT hot loop.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.stt.Run(runCtx)
	}()

	// STT → (UI partials, MT queue) router.
	s.wg.Add(1)
	go s.routeSTT(runCtx)

	// MT worker.
	s.wg.Add(1)
	go s.runMT(runCtx)

	// TTS worker — only spun up when TTS is enabled.
	if s.cfg.TTSEnabled {
		s.wg.Add(1)
		go s.runTTS(runCtx)
	}

	// Heartbeat — periodic info log so the console always shows a
	// pulse, even on a silent room.
	s.wg.Add(1)
	go s.heartbeat(runCtx)

	if s.cfg.TTSEnabled {
		if err := s.pb.Start(); err != nil {
			s.cancel()
			return fmt.Errorf("app: playback start: %w", err)
		}
	}

	if err := s.cap.Start(); err != nil {
		if s.cfg.TTSEnabled {
			_ = s.pb.Stop()
		}
		s.cancel()
		return fmt.Errorf("app: capture start: %w", err)
	}

	// Watchdog: if ctx is cancelled outside Stop, propagate.
	go func() {
		<-runCtx.Done()
		_ = s.Stop()
	}()

	return nil
}

// routeSTT forwards Partials to the UI events channel and pushes
// Finals into the MT queue with two pieces of perf-branch logic
// in front: a hallucination filter that drops known model garbage,
// and a sentence-stitching guard that batches mid-clause Finals
// into one MT input so the translator gets a coherent sentence.
func (s *Session) routeSTT(ctx context.Context) {
	defer s.wg.Done()

	s.stitch.flushCh = make(chan string, 1)

	for {
		select {
		case <-ctx.Done():
			s.stitchStop()
			return

		case batch := <-s.stitch.flushCh:
			// Timer fired — window expired without terminal
			// punctuation; flush whatever we have.
			s.routeMT(ctx, batch)

		case ev, ok := <-s.stt.Events():
			if !ok {
				s.stitchStop()
				return
			}
			switch e := ev.(type) {
			case stt.Partial:
				s.log.Debug("stt partial", "text", trunc(e.Text, 80))
				s.emit(ctx, Partial{Text: e.Text})

			case stt.Final:
				text := strings.TrimSpace(e.Text)
				if text == "" {
					continue
				}
				if isHallucination(text) {
					s.log.Info("stt final dropped: hallucination", "text", trunc(text, 80))
					continue
				}
				s.log.Info("stt final", "text", trunc(text, 120))
				s.emit(ctx, Final{Text: text})
				s.stitchAdd(ctx, text)
			}
		}
	}
}

// stitchAdd folds a Final into the stitch buffer. If the fragment
// completes a sentence (terminal punctuation, fragment cap, max
// duration), it flushes immediately. Otherwise the routine arms a
// timer that wakes routeSTT after stitchWindow.
func (s *Session) stitchAdd(ctx context.Context, text string) {
	s.stitch.buf = append(s.stitch.buf, text)
	if s.stitch.firstAt.IsZero() {
		s.stitch.firstAt = time.Now()
	}

	tooLong := time.Since(s.stitch.firstAt) >= time.Duration(stitchMaxFragmentSec)*time.Second
	enough := len(s.stitch.buf) >= stitchMaxFragments
	terminal := endsTerminal(text)

	if terminal || enough || tooLong {
		s.stitchFlush(ctx)
		return
	}

	// Arm / re-arm timer for stitchWindow.
	if s.stitch.timer != nil {
		s.stitch.timer.Stop()
	}
	s.stitch.timer = time.AfterFunc(stitchWindow, func() {
		// Compose stitched text and send via the flush channel —
		// routeSTT consumes it from its own goroutine so we keep
		// all stitch state single-threaded.
		select {
		case s.stitch.flushCh <- strings.Join(s.stitch.buf, " "):
		default:
			// previous flush still pending; routeSTT will pick
			// it up. Either way we will reset state on its side.
		}
	})
}

// stitchFlush sends the buffered fragments straight to MT and clears
// the stitch state.
func (s *Session) stitchFlush(ctx context.Context) {
	if len(s.stitch.buf) == 0 {
		return
	}
	full := strings.Join(s.stitch.buf, " ")
	s.stitch.buf = s.stitch.buf[:0]
	s.stitch.firstAt = time.Time{}
	if s.stitch.timer != nil {
		s.stitch.timer.Stop()
		s.stitch.timer = nil
	}
	s.routeMT(ctx, full)
}

// stitchStop drops any pending stitch state on shutdown.
func (s *Session) stitchStop() {
	if s.stitch.timer != nil {
		s.stitch.timer.Stop()
		s.stitch.timer = nil
	}
	s.stitch.buf = nil
}

// routeMT pushes one composed sentence onto the MT queue, counting
// drops when the worker can't keep up.
func (s *Session) routeMT(ctx context.Context, text string) {
	select {
	case s.mtQ <- text:
	case <-ctx.Done():
	default:
		s.mtDrops.Add(1)
		s.log.Warn("app: MT queue full, dropping final", "text", trunc(text, 60))
	}
}

// endsTerminal reports whether the trimmed text ends in a sentence-
// closing punctuation mark.
func endsTerminal(text string) bool {
	t := strings.TrimRight(text, " \t\n\r")
	if t == "" {
		return false
	}
	last := t[len(t)-1]
	switch last {
	case '.', '?', '!':
		return true
	}
	// Multi-byte "…" check — three bytes 0xE2 0x80 0xA6.
	if len(t) >= 3 && t[len(t)-3] == 0xE2 && t[len(t)-2] == 0x80 && t[len(t)-1] == 0xA6 {
		return true
	}
	return false
}

// hallucinationFingerprints is a small, conservative deny-list of
// canned phrases the upstream models are known to emit on noise or
// silence. Lowercased, trimmed, with []/() content stripped before
// comparison.
var hallucinationFingerprints = map[string]struct{}{
	// Russian YouTube credit lines that whisper / NeMo sometimes
	// hallucinate over white noise:
	"субтитры от dimatorzok":                {},
	"продолжение в комментариях":            {},
	"подпишись на канал":                    {},
	"спасибо за просмотр":                   {},
	"корректор":                             {},
	"редактор субтитров":                    {},
	// English YouTube credit / outro lines:
	"thanks for watching":     {},
	"like and subscribe":      {},
	"please subscribe":        {},
	"see you next time":       {},
	"see you in the next one": {},
}

func isHallucination(text string) bool {
	norm := strings.ToLower(strings.TrimSpace(text))
	// strip "[ ... ]" and "( ... )" content cheaply.
	norm = stripBracketed(norm, '[', ']')
	norm = stripBracketed(norm, '(', ')')
	norm = strings.TrimSpace(norm)
	if norm == "" {
		return true
	}
	_, hit := hallucinationFingerprints[norm]
	return hit
}

func stripBracketed(s string, open, close byte) string {
	for {
		i := strings.IndexByte(s, open)
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], close)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+1:]
	}
}

// runMT translates one finalised utterance at a time. The Engine is
// expected to be a mt.Serial wrapper already; we do not introduce
// concurrent decode here.
func (s *Session) runMT(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case src, ok := <-s.mtQ:
			if !ok {
				return
			}
			src = strings.TrimSpace(src)
			if src == "" {
				continue
			}
			start := time.Now()
			tgt, err := s.mt.Translate(src, s.cfg.Source, s.cfg.Target)
			elapsed := time.Since(start)
			s.mtLastNs.Store(uint64(elapsed.Nanoseconds()))
			if err != nil {
				s.log.Warn("mt translate failed", "src", trunc(src, 60), "err", err)
				s.emit(ctx, ErrorEv{Err: fmt.Errorf("mt: %w", err)})
				continue
			}
			s.log.Info("mt translation",
				"src_lang", s.cfg.Source,
				"tgt_lang", s.cfg.Target,
				"src", trunc(src, 80),
				"tgt", trunc(tgt, 80),
				"ms", elapsed.Milliseconds(),
			)
			s.emit(ctx, Translation{Source: src, Target: tgt})

			if !s.cfg.TTSEnabled || tgt == "" {
				continue
			}
			select {
			case s.ttsQ <- tgt:
			default:
				s.ttsDrops.Add(1)
				s.log.Warn("app: TTS queue full, dropping translation", "text", trunc(tgt, 60))
			}
		}
	}
}

// runTTS synthesises one translation at a time, streaming PCM
// chunks into the playback ring as they arrive from the engine.
// Concurrent Speak is forbidden by the underlying OfflineTts so the
// sequential loop is exactly what we want.
func (s *Session) runTTS(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case text, ok := <-s.ttsQ:
			if !ok {
				return
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			s.log.Debug("tts speak", "text", trunc(text, 60))
			s.emit(ctx, Speaking{Active: true})
			start := time.Now()
			chunks := s.tts.Speak(ctx, text)
			var totalSamples int

			// Refresh the half-duplex mute on every chunk based on
			// the REAL number of samples sitting in the playback
			// ring + a 700 ms loudspeaker→mic transit grace. This
			// is the only honest way to keep the gate active for
			// the entire audible playback — `time.Since(start)`
			// undercounts because the ring lags synthesis.
			refreshMute := func() {
				if s.pb == nil {
					return
				}
				sr := s.pb.SampleRate()
				if sr <= 0 {
					return
				}
				ringSamples := s.pb.Ring().Len()
				drainMs := int64(ringSamples) * 1000 / int64(sr)
				deadline := time.Now().Add(time.Duration(drainMs)*time.Millisecond + halfDuplexTailMs)
				cur := s.muteUntilNs.Load()
				if deadline.UnixNano() > cur {
					s.muteUntilNs.Store(deadline.UnixNano())
				}
			}

			for chunk := range chunks {
				totalSamples += len(chunk.Samples)
				s.pumpToPlayback(ctx, chunk.Samples)
				refreshMute()
			}
			elapsed := time.Since(start)
			s.ttsLastNs.Store(uint64(elapsed.Nanoseconds()))

			// Final hold-down: keep the gate active until the ring
			// is provably empty + halfDuplexTailMs (room
			// reverberation + Silero VAD onset latency + ASR
			// segment release headroom). Poll every 50 ms so we
			// release the mic the moment everything has settled.
			s.waitPlaybackDrain(ctx, halfDuplexTailMs)

			s.log.Info("tts done",
				"text", trunc(text, 60),
				"samples", totalSamples,
				"ms", elapsed.Milliseconds(),
			)
			s.emit(ctx, Speaking{Active: false})
		}
	}
}

// waitPlaybackDrain blocks until the playback ring is empty (i.e.
// the device callback consumed every sample we wrote) plus a tail
// hold-down. Refreshes the half-duplex mute deadline on every poll
// so the capture gate stays closed for the entire audible playback,
// not just the synthesis wall-clock.
func (s *Session) waitPlaybackDrain(ctx context.Context, tail time.Duration) {
	if s.pb == nil {
		return
	}
	sr := s.pb.SampleRate()
	if sr <= 0 {
		return
	}
	const poll = 50 * time.Millisecond
	for {
		ringSamples := s.pb.Ring().Len()
		drainMs := int64(ringSamples) * 1000 / int64(sr)
		deadline := time.Now().Add(time.Duration(drainMs)*time.Millisecond + tail)
		// Keep the mute deadline aligned with the worst-case
		// drain estimate; the consumer side only consults the
		// stored value.
		if deadline.UnixNano() > s.muteUntilNs.Load() {
			s.muteUntilNs.Store(deadline.UnixNano())
		}
		if ringSamples == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// pumpToPlayback pushes a chunk into the ring with simple
// backpressure: when the ring is full we yield briefly and retry the
// remainder; if the context is cancelled we drop the rest.
func (s *Session) pumpToPlayback(ctx context.Context, samples []float32) {
	remaining := samples
	for len(remaining) > 0 {
		if ctx.Err() != nil {
			return
		}
		n := s.pb.Write(remaining)
		if n == len(remaining) {
			return
		}
		remaining = remaining[n:]
		// Tiny breath: 1 ms is shorter than any audio period.
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// emit sends an event without blocking the producer when the UI
// consumer cannot keep up — losing one Partial is preferable to
// stalling the STT loop.
func (s *Session) emit(ctx context.Context, ev Event) {
	select {
	case s.events <- ev:
	case <-ctx.Done():
	default:
		// UI is behind; drop the event silently.
	}
}

// Stop halts the audio devices and stage goroutines, then drains
// the events channel. Idempotent.
func (s *Session) Stop() error {
	if !s.running.CompareAndSwap(true, false) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.cap != nil {
		_ = s.cap.Stop()
	}
	if s.pb != nil {
		_ = s.pb.Stop()
	}
	s.wg.Wait()
	close(s.events)
	return nil
}

// Close releases native resources. Implicitly stops if still running.
// Safe to call multiple times.
func (s *Session) Close() error {
	_ = s.Stop()
	if s.cap != nil {
		_ = s.cap.Close()
		s.cap = nil
	}
	if s.pb != nil {
		_ = s.pb.Close()
		s.pb = nil
	}
	if s.tts != nil {
		_ = s.tts.Close()
		s.tts = nil
	}
	if s.stt != nil {
		_ = s.stt.Close()
		s.stt = nil
	}
	if s.mt != nil {
		_ = s.mt.Close()
	}
	return nil
}

// heartbeat logs a one-line summary every 30 s so a quiet session
// still produces visible output in the console.
func (s *Session) heartbeat(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			st := s.Stats()
			s.log.Info("heartbeat",
				"captured_s", int(st.Captured.Seconds()),
				"mic_pct", st.MicPct,
				"vad_active_pct", st.VADActivePct,
				"utts", st.Utterances,
				"drops_mt", st.MTDropped,
				"drops_tts", st.TTSDropped,
				"underruns", st.PlaybackUnderrn,
			)
		}
	}
}

// Stats is a snapshot of operational counters surfaced to the UI.
type Stats struct {
	// Lifecycle.
	Running bool

	// Audio path.
	Captured        time.Duration
	MicRMS          float32 // 0..1
	MicPct          int     // 100*MicRMS, rounded
	VADActivePct    int     // 100*VADActiveRatio()
	CaptureDropped  uint64
	PlaybackUnderrn uint64

	// STT.
	Utterances uint64
	STTDropped uint64

	// Per-stage last-utterance latency.
	MTLast  time.Duration
	TTSLast time.Duration

	// Queue pressure.
	MTDropped  uint64
	TTSDropped uint64

	// MicMutedDropped is the count of mic-callback chunks that were
	// dropped on the floor because the half-duplex gate was active
	// (TTS playing). Useful for diagnosing "the app does not hear
	// me" complaints — if this counter is growing while you speak,
	// playback is bleeding into the gate.
	MicMutedDropped uint64

	// Translation memory stats — surfaced via the optional TMStats()
	// accessor on the wrapped engine. cmd layer fills them in
	// because it owns the *mt.Cached pointer; session does not see
	// the cache type.
	TMSize    int
	TMPinned  uint64
	TMHits    uint64
	TMMisses  uint64
	TMHitRate float64

	// MTEnabled is true when the session got a real translation
	// engine (not mt.Disabled). UI uses it to flip the header
	// MT label to "off" when the binary was built without -tags mt.
	MTEnabled bool
}

// Stats returns a point-in-time snapshot for the UI status line.
func (s *Session) Stats() Stats {
	var st Stats
	st.Running = s.running.Load()
	if s.cap != nil {
		st.Captured = s.cap.Captured()
		rms := s.cap.MicRMS()
		st.MicRMS = rms
		st.MicPct = int(math.Round(float64(rms) * 100))
		st.CaptureDropped = s.cap.Dropped()
	}
	if s.stt != nil {
		st.STTDropped = s.stt.Dropped()
		st.Utterances = s.stt.Utterances()
		st.VADActivePct = int(math.Round(float64(s.stt.VADActiveRatio()) * 100))
	}
	if s.pb != nil {
		st.PlaybackUnderrn = s.pb.Underruns()
	}
	st.MTDropped = s.mtDrops.Load()
	st.TTSDropped = s.ttsDrops.Load()
	st.MTLast = time.Duration(s.mtLastNs.Load())
	st.TTSLast = time.Duration(s.ttsLastNs.Load())
	st.MicMutedDropped = s.micDropped.Load()
	_, isDisabled := s.mt.(mt.Disabled)
	st.MTEnabled = s.mt != nil && !isDisabled
	return st
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
