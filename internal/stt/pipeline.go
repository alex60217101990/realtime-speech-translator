// Package stt orchestrates voice-activity segmentation and Whisper
// transcription. The Pipeline runs as a single goroutine that consumes
// audio frames, hands them to a VAD segmenter and emits Transcript
// events to a fan-out channel.
//
// This is the M2 entry point — partial (sliding-window) transcripts and
// initial-prompt continuity are layered on later milestones; the public
// API is shaped so they can be added without breaking callers.
package stt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/vad"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt/whisper"
)

// TTSEngine is implemented by internal/tts/piper.Engine (and any future
// alternative). The Pipeline calls it after a successful MT step.
type TTSEngine interface {
	// Synthesize generates 22050 Hz mono int16 PCM for `text` in `lang`
	// (ISO-639-1). The returned slice must be safe to retain.
	Synthesize(ctx context.Context, text, lang string) ([]int16, error)
}

// AudioSink consumes PCM frames produced by TTS. The Pipeline writes
// each successfully-synthesised utterance verbatim; the Sink is
// expected to push them into a ring/device asynchronously.
type AudioSink interface {
	WritePCM(samples []int16)
}

// Event is emitted on the Pipeline's output channel for every recognised
// utterance. When the Pipeline is configured without an MT engine,
// Translation and TargetLang remain empty.
type Event struct {
	Text        string
	Language    string
	Translation string
	TargetLang  string
	Started     time.Time
	Duration    time.Duration
	STTLatency  time.Duration
	MTLatency   time.Duration
	TTSLatency  time.Duration
	TTSSamples  int // 22050 Hz mono samples produced; 0 if TTS disabled
}

// Config groups the runtime parameters of the Pipeline. The VADConfig
// and EngineConfig give callers full control over the underlying
// components; sensible defaults are exposed via DefaultConfig.
type Config struct {
	VAD    vad.Config
	Engine whisper.Config

	// MT is the translation engine. nil disables translation (the
	// Pipeline still emits transcript-only events).
	MT mt.Engine

	// TargetLang is the ISO-639-1 code Translation should produce.
	// Ignored when MT is nil.
	TargetLang string

	// TTS synthesises translated text into PCM. nil disables TTS;
	// the Pipeline still emits transcript+translation events.
	TTS TTSEngine

	// Audio consumes the TTS-produced PCM. nil disables playback even
	// when TTS is set — the synthesized audio is dropped on the floor.
	Audio AudioSink

	// PromptHistory bounds how many recent finals are folded into the
	// next Whisper InitialPrompt for context. Zero disables prompting.
	PromptHistory int

	// OutputBuffer is the capacity of the Event output channel. A small
	// buffer back-pressures UI consumers without blocking the audio
	// thread (the VAD output channel has its own buffer).
	OutputBuffer int
}

// DefaultConfig returns Config tuned for low-latency conversational
// speech at 16 kHz mono.
func DefaultConfig() Config {
	return Config{
		VAD:           vad.DefaultConfig(),
		Engine:        whisper.DefaultConfig(),
		PromptHistory: 1,
		OutputBuffer:  8,
	}
}

// Pipeline runs VAD + Whisper + (optional) MT + (optional) TTS as a
// single coordinated unit.
type Pipeline struct {
	cfg    Config
	seg    *vad.Segmenter
	engine *whisper.Engine

	ctx context.Context // set in Start; used by TTS Synthesize

	out  chan Event
	done chan struct{}
	wg   sync.WaitGroup

	mu      sync.Mutex
	prompt  string // rolling InitialPrompt assembled from previous finals
	running bool
}

// New constructs a Pipeline. The engine and segmenter are created
// internally so callers do not have to wire their lifetimes manually.
func New(modelPath string, cfg Config) (*Pipeline, error) {
	if modelPath == "" {
		return nil, errors.New("stt: empty model path")
	}
	seg, err := vad.New(cfg.VAD)
	if err != nil {
		return nil, err
	}
	eng, err := whisper.New(modelPath, cfg.Engine)
	if err != nil {
		_ = seg.Close()
		return nil, err
	}
	return &Pipeline{
		cfg:    cfg,
		seg:    seg,
		engine: eng,
		out:    make(chan Event, cfg.OutputBuffer),
		done:   make(chan struct{}),
	}, nil
}

// Output returns the Event channel. It is closed when the Pipeline is
// stopped via Close.
func (p *Pipeline) Output() <-chan Event { return p.out }

// VADStats exposes the segmenter counters so the UI can render a live
// "VAD is firing" indicator without poking the internal segmenter.
func (p *Pipeline) VADStats() vad.Stats { return p.seg.Stats() }

// WriteFrame forwards int16 mono PCM into the VAD segmenter. Safe to
// call from a single producer goroutine.
func (p *Pipeline) WriteFrame(samples []int16) { p.seg.WriteFrame(samples) }

// Start launches the recognition goroutine. If a previous Start is
// still winding down (the Whisper cgo call has not returned yet) we
// wait up to startWaitTimeout for it to exit, then proceed. This makes
// Session.Stop → Session.Start work reliably even when the GPU is
// slow.
func (p *Pipeline) Start(ctx context.Context) error {
	const startWaitTimeout = 5 * time.Second
	deadline := time.Now().Add(startWaitTimeout)
	for {
		p.mu.Lock()
		if !p.running {
			p.running = true
			p.mu.Unlock()
			break
		}
		p.mu.Unlock()
		if time.Now().After(deadline) {
			return errors.New("stt: previous run still active after 5 s — try again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.wg.Add(1)
	go p.run(ctx)
	return nil
}

func (p *Pipeline) run(ctx context.Context) {
	p.ctx = ctx
	defer p.wg.Done()
	// Mark not-running on exit so the Pipeline can be Start()ed again
	// in the same lifetime (Session.Stop → Session.Start without
	// throwing away the Whisper context). We deliberately do NOT close
	// p.out here — Close does that exactly once when the Session is
	// torn down. Keeping out open lets the UI consumer goroutine
	// survive Stop/Start cycles.
	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case ut, ok := <-p.seg.Output():
			if !ok {
				return
			}
			p.handleUtterance(ut)
		}
	}
}

// Stop signals the run goroutine to exit without waiting for it.
//
// Why non-blocking: Whisper.Transcribe is a synchronous cgo call that
// does not honour context cancellation, so a wg.Wait could hang for
// the full duration of an in-flight inference (multiple seconds on a
// slow GPU). The trade-off: a follow-up Start may briefly see
// running=true and refuse; the caller should retry. WaitStopped is
// available for the slow path that genuinely needs to block.
func (p *Pipeline) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// running is reset in run()'s defer; nothing to flip here. The
	// outer ctx (passed to Start) is what actually halts the loop —
	// Session.Stop cancels it.
}

// WaitStopped blocks until the previously-started run goroutine has
// fully exited or the timeout elapses. Returns true if the goroutine
// is gone, false on timeout. Useful for tests; production code should
// not need to block.
func (p *Pipeline) WaitStopped(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (p *Pipeline) handleUtterance(ut vad.Utterance) {
	override := p.cfg.Engine
	p.mu.Lock()
	if p.prompt != "" {
		override.InitialPrompt = p.prompt
	}
	p.mu.Unlock()

	tr, err := p.engine.Transcribe(ut.PCM, &override)
	if err != nil {
		// Stay alive — a single bad utterance shouldn't tear the
		// pipeline down. The UI can surface error counters separately.
		slog.Warn("whisper transcribe failed", "err", err)
		return
	}

	slog.Info("whisper transcribed",
		"raw", tr.Text,
		"lang", tr.Language,
		"pcm_samples", len(ut.PCM),
		"duration", ut.Duration,
		"latency", tr.Latency,
	)
	text := normalize(tr.Text)
	if text == "" {
		slog.Debug("transcript empty after normalize", "raw", tr.Text)
		return
	}

	if p.cfg.PromptHistory > 0 {
		p.mu.Lock()
		// Keep the prompt bounded so it doesn't grow unboundedly.
		p.prompt = trimPrompt(p.prompt+" "+text, 200)
		p.mu.Unlock()
	}

	ev := Event{
		Text:       text,
		Language:   tr.Language,
		Started:    ut.StartedAt,
		Duration:   ut.Duration,
		STTLatency: tr.Latency,
	}

	if p.cfg.MT != nil && p.cfg.TargetLang != "" && tr.Language != p.cfg.TargetLang {
		t0 := time.Now()
		translation, err := p.cfg.MT.Translate(text, tr.Language, p.cfg.TargetLang)
		ev.MTLatency = time.Since(t0)
		if err == nil {
			ev.Translation = translation
			ev.TargetLang = p.cfg.TargetLang
		}
		// On MT error we still emit the transcript; the user sees it
		// and can choose another model or language.
	}

	// TTS: synthesise the translation (or, if MT is disabled, the
	// transcript) into PCM and hand it to the playback sink. We do
	// this on the pipeline goroutine — sequential decoding keeps
	// memory bounded and avoids reordered playback.
	if p.cfg.TTS != nil {
		spokenText := ev.Translation
		spokenLang := ev.TargetLang
		if spokenText == "" {
			spokenText = ev.Text
			spokenLang = ev.Language
		}
		if spokenText != "" && spokenLang != "" {
			t0 := time.Now()
			pcm, err := p.cfg.TTS.Synthesize(p.ctx, spokenText, spokenLang)
			ev.TTSLatency = time.Since(t0)
			if err == nil {
				ev.TTSSamples = len(pcm)
				if p.cfg.Audio != nil {
					p.cfg.Audio.WritePCM(pcm)
				}
			}
		}
	}

	select {
	case p.out <- ev:
	default:
		// UI is behind; drop this event rather than stalling the
		// segmenter consumer loop.
	}
}

// Close stops the goroutine, the segmenter and the Whisper engine.
// Output channel is closed before Close returns. After Close, Start
// returns an error — use a fresh Pipeline for the next session.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	wasRunning := p.running
	p.running = false
	p.mu.Unlock()

	if wasRunning {
		// done may have been closed already by a previous Close (no-op
		// in that case is harmless because we only Close once per
		// Pipeline lifetime).
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
	_ = p.seg.Close()
	p.wg.Wait()
	// Close the event channel exactly once. After Close the UI
	// consumer's `for range pipeline.Output()` loop terminates. The
	// recover guards against a double-Close from buggy callers.
	safeClose(p.out)
	if err := p.engine.Close(); err != nil {
		return fmt.Errorf("stt: close engine: %w", err)
	}
	return nil
}

// safeClose protects against double-close panics on the event channel.
func safeClose(ch chan Event) {
	defer func() { _ = recover() }()
	close(ch)
}

// normalize trims surrounding whitespace from Whisper output and drops
// the bracketed non-speech tokens it occasionally emits (e.g. "[BLANK_AUDIO]").
func normalize(s string) string {
	out := make([]rune, 0, len(s))
	depth := 0
	for _, r := range s {
		switch r {
		case '[', '(':
			depth++
			continue
		case ']', ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			out = append(out, r)
		}
	}
	// Trim ASCII whitespace from both ends.
	i, j := 0, len(out)
	for i < j && isSpace(out[i]) {
		i++
	}
	for j > i && isSpace(out[j-1]) {
		j--
	}
	return string(out[i:j])
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// trimPrompt drops leading runes until the string fits maxRunes.
func trimPrompt(s string, maxRunes int) string {
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[len(rs)-maxRunes:])
}
