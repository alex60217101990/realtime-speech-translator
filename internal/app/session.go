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
// and TTS sub-configs are constructed by the cmd layer (it knows
// model paths from the manifest); MT comes pre-built (Disabled by
// default), already wrapped in mt.Serial and any caching the cmd
// chooses to apply.
type Config struct {
	// Source / Target are ISO-639-1 codes; "auto" allowed for source.
	Source string
	Target string

	STT stt.Config
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
	stt *stt.Engine
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

	sttEngine, err := stt.New(cfg.STT)
	if err != nil {
		return nil, fmt.Errorf("app: stt: %w", err)
	}

	s := &Session{
		cfg:    cfg,
		log:    cfg.Logger,
		stt:    sttEngine,
		mt:     cfg.MT,
		events: make(chan Event, 32),
		mtQ:    make(chan string, cfg.MTQueue),
		ttsQ:   make(chan string, cfg.TTSQueue),
	}

	cap, err := capture.New(
		capture.Config{SampleRate: cfg.STT.SampleRate, Channels: 1},
		s.stt.Push,
	)
	if err != nil {
		_ = sttEngine.Close()
		return nil, fmt.Errorf("app: capture: %w", err)
	}
	s.cap = cap

	if cfg.TTSEnabled {
		ttsEngine, err := tts.New(cfg.TTS)
		if err != nil {
			_ = cap.Close()
			_ = sttEngine.Close()
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
			_ = sttEngine.Close()
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
// Finals into the MT queue. Drops on full queue rather than blocking
// the STT hot loop.
func (s *Session) routeSTT(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.stt.Events():
			if !ok {
				return
			}
			switch e := ev.(type) {
			case stt.Partial:
				s.log.Debug("stt partial", "text", trunc(e.Text, 80))
				s.emit(ctx, Partial{Text: e.Text})
			case stt.Final:
				s.log.Info("stt final", "text", trunc(e.Text, 120))
				s.emit(ctx, Final{Text: e.Text})
				select {
				case s.mtQ <- e.Text:
				default:
					s.mtDrops.Add(1)
					s.log.Warn("app: MT queue full, dropping final", "text", trunc(e.Text, 60))
				}
			}
		}
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
			for chunk := range chunks {
				totalSamples += len(chunk.Samples)
				s.pumpToPlayback(ctx, chunk.Samples)
			}
			elapsed := time.Since(start)
			s.ttsLastNs.Store(uint64(elapsed.Nanoseconds()))
			s.log.Info("tts done",
				"text", trunc(text, 60),
				"samples", totalSamples,
				"ms", elapsed.Milliseconds(),
			)
			s.emit(ctx, Speaking{Active: false})
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
	return st
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
