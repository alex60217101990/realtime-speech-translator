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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
//
// Latency fields decompose end-to-end lag so the UI / logs can show
// which stage is the bottleneck:
//
//	HangoverLatency : end-of-speech -> VAD emit (≈ vad.Config.HangoverMs)
//	QueueLatency    : VAD emit -> Pipeline pickup (channel wait)
//	STTLatency      : Whisper.Transcribe wall time
//	MTLatency       : MT.Translate wall time (0 when MT disabled / same lang)
//	DisplayLatency  : end-of-speech -> transcript+translation ready (no TTS)
//	TTSLatency      : Piper.Synthesize wall time (filled asynchronously
//	                  by the TTS worker; reflects the *previous* utterance
//	                  if the user is reading the current one before
//	                  playback finishes)
//	TotalLatency    : end-of-speech -> all stages complete (incl. TTS)
//
// DisplayLatency is the number to optimise for: it's the lag the user
// perceives between speaking and seeing the translation. TTS playback
// runs off the critical path on a separate goroutine.
type Event struct {
	Text            string
	Language        string
	Translation     string
	TargetLang      string
	Started         time.Time
	Duration        time.Duration
	HangoverLatency time.Duration
	QueueLatency    time.Duration
	STTLatency      time.Duration
	MTLatency       time.Duration
	DisplayLatency  time.Duration
	TTSLatency      time.Duration
	TotalLatency    time.Duration
	TTSSamples      int // 22050 Hz mono samples produced; 0 if TTS disabled
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

	// PartialPeriod is the polling cadence for partial transcripts.
	// Zero disables partials and the Pipeline only emits finals.
	// A reasonable value is 600-800 ms — short enough to feel live,
	// long enough that Whisper has fresh audio to chew on between
	// passes. Each partial runs a fresh Whisper Transcribe on the
	// accumulating PCM, which serialises against the final
	// Transcribe of the same utterance; expect a small (<200 ms)
	// added delay on the final when partials are enabled.
	PartialPeriod time.Duration

	// PartialMinDuration is the minimum accumulated speech length
	// before the first partial fires. Whisper produces noise on very
	// short audio (<400 ms), so we wait until there's enough signal.
	PartialMinDuration time.Duration
}

// DefaultConfig returns Config tuned for low-latency conversational
// speech at 16 kHz mono.
func DefaultConfig() Config {
	return Config{
		VAD:                vad.DefaultConfig(),
		Engine:             whisper.DefaultConfig(),
		PromptHistory:      1,
		OutputBuffer:       8,
		PartialPeriod:      700 * time.Millisecond,
		PartialMinDuration: 500 * time.Millisecond,
	}
}

// Partial is a "what's been said so far" preview emitted on the
// PartialOutput channel while an utterance is still in progress.
// Duration tells the consumer how much speech was decoded into Text
// (Final partials report the closed utterance's duration). The UI
// displays partials in a separate visual lane and replaces them with
// the Final event when the utterance completes.
type Partial struct {
	Text     string
	Language string
	Duration time.Duration
	Final    bool
}

// Pipeline runs VAD + Whisper + (optional) MT + (optional) TTS as a
// single coordinated unit.
type Pipeline struct {
	cfg    Config
	seg    *vad.Segmenter
	engine *whisper.Engine

	ctx context.Context // set in Start; used by TTS Synthesize

	out      chan Event
	partials chan Partial
	done     chan struct{}
	wg       sync.WaitGroup

	// ttsQ feeds the async TTS worker. Synthesize+playback are off the
	// critical path so the UI sees the translation as soon as MT
	// completes; TTS audio arrives a beat later.
	ttsQ       chan ttsJob
	lastTTSLag atomic.Int64 // nanoseconds; surfaced into the next Event
	ttsDrops   atomic.Uint64

	mu      sync.Mutex
	prompt  string // rolling InitialPrompt assembled from previous finals
	vocab   map[string]int // word frequency table, used to bias Whisper
	running bool
}

// ttsJob is what handleUtterance hands to the TTS worker. text/lang are
// the synthesise inputs; speechEnd is carried through so the worker can
// log a true end-of-speech -> playback-ready figure.
type ttsJob struct {
	text      string
	lang      string
	speechEnd time.Time
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
		cfg:      cfg,
		seg:      seg,
		engine:   eng,
		out:      make(chan Event, cfg.OutputBuffer),
		partials: make(chan Partial, 4),
		done:     make(chan struct{}),
		vocab:    make(map[string]int, 256),
		// Cap=2 keeps TTS at most one utterance behind the displayed
		// translation. Overflow is preferable to unbounded growth: if
		// the user speaks faster than Piper can render, dropping older
		// audio is less confusing than queueing minutes of stale TTS.
		ttsQ: make(chan ttsJob, 2),
	}, nil
}

// Output returns the Event channel. It is closed when the Pipeline is
// stopped via Close.
func (p *Pipeline) Output() <-chan Event { return p.out }

// PartialOutput returns the channel that carries partial (in-progress)
// transcripts. Empty when PartialPeriod is zero. Consumers should
// display partials in a separate UI lane and reset the lane on
// Partial{Final: true} or when the corresponding final Event arrives.
func (p *Pipeline) PartialOutput() <-chan Partial { return p.partials }

// VADStats exposes the segmenter counters so the UI can render a live
// "VAD is firing" indicator without poking the internal segmenter.
func (p *Pipeline) VADStats() vad.Stats { return p.seg.Stats() }

// IsSpeaking forwards the segmenter's live "inside an utterance" flag.
// Used by the voice-viz widget so it can switch to its "speaking"
// palette the instant the VAD opens an utterance, instead of waiting
// for the utterance to be emitted.
func (p *Pipeline) IsSpeaking() bool { return p.seg.IsSpeaking() }

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
	if p.cfg.TTS != nil {
		p.wg.Add(1)
		go p.runTTS(ctx)
	}
	if p.cfg.PartialPeriod > 0 {
		p.wg.Add(1)
		go p.runPartials(ctx)
	}
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
	pickedUp := time.Now()
	speechEnd := ut.StartedAt.Add(ut.Duration)
	// EmittedAt is set by the segmenter the instant it pushed the
	// utterance onto its output channel. May be zero in tests that
	// hand-craft an Utterance, in which case fall back to pickup time.
	emitted := ut.EmittedAt
	if emitted.IsZero() {
		emitted = pickedUp
	}
	hangover := max(emitted.Sub(speechEnd), 0)
	queue := max(pickedUp.Sub(emitted), 0)

	override := p.cfg.Engine
	p.mu.Lock()
	// Build the InitialPrompt from two pieces: top-N most frequent
	// vocabulary words seen so far (biases Whisper toward names /
	// jargon the user actually says) + the rolling tail of recent
	// transcripts (provides natural-language context). Capped at
	// 200 runes — Whisper truncates beyond that anyway.
	override.InitialPrompt = buildInitialPrompt(topVocab(p.vocab, 20), p.prompt, 200)
	p.mu.Unlock()

	tr, err := p.engine.Transcribe(ut.PCM, &override)
	if err != nil {
		// Stay alive — a single bad utterance shouldn't tear the
		// pipeline down. The UI can surface error counters separately.
		slog.Warn("whisper transcribe failed", "err", err)
		return
	}

	rtf := float64(0)
	if ut.Duration > 0 {
		rtf = float64(tr.Latency) / float64(ut.Duration)
	}
	slog.Info("whisper transcribed",
		"raw", tr.Text,
		"lang", tr.Language,
		"pcm_samples", len(ut.PCM),
		"duration", ut.Duration,
		"stt_latency", tr.Latency,
		"rtf", roundFloat(rtf, 2),
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
		// Accumulate word frequencies for the next utterance's
		// InitialPrompt — the application "learns" which words the
		// user says often and biases Whisper toward them.
		accumulateVocab(p.vocab, text)
		p.mu.Unlock()
	}

	ev := Event{
		Text:            text,
		Language:        tr.Language,
		Started:         ut.StartedAt,
		Duration:        ut.Duration,
		HangoverLatency: hangover,
		QueueLatency:    queue,
		STTLatency:      tr.Latency,
	}

	// Signal partial consumers that the in-progress transcript has
	// closed. UI clears its "live" lane and prepares to render the
	// final Event. Best-effort: drop if the partial channel is full.
	if p.cfg.PartialPeriod > 0 {
		select {
		case p.partials <- Partial{Final: true, Text: text, Language: tr.Language, Duration: ut.Duration}:
		default:
		}
	}

	if p.cfg.MT != nil && p.cfg.TargetLang != "" && tr.Language != p.cfg.TargetLang {
		t0 := time.Now()
		translation, err := p.cfg.MT.Translate(text, tr.Language, p.cfg.TargetLang)
		ev.MTLatency = time.Since(t0)
		if err == nil {
			ev.Translation = translation
			ev.TargetLang = p.cfg.TargetLang
		} else {
			slog.Warn("mt translate failed", "err", err, "src_lang", tr.Language, "dst_lang", p.cfg.TargetLang)
		}
		// On MT error we still emit the transcript; the user sees it
		// and can choose another model or language.
	}

	// DisplayLatency is what the user actually feels: end-of-speech ->
	// translation visible. TTS playback is downstream of this point
	// and runs on its own goroutine so we don't gate the UI on it.
	ev.DisplayLatency = time.Since(speechEnd)
	// TTSLatency on this event reflects the *previous* utterance's
	// synthesise wall time — there's no way to know the current one
	// before we ship it. Good enough for a trend indicator in the UI.
	ev.TTSLatency = time.Duration(p.lastTTSLag.Load())
	ev.TotalLatency = ev.DisplayLatency + ev.TTSLatency

	slog.Info("utterance ready",
		"text", ev.Text,
		"translation", ev.Translation,
		"lang", ev.Language,
		"target", ev.TargetLang,
		"duration", ev.Duration,
		"hangover", ev.HangoverLatency,
		"queue", ev.QueueLatency,
		"stt", ev.STTLatency,
		"mt", ev.MTLatency,
		"display", ev.DisplayLatency,
		"prev_tts", ev.TTSLatency,
		"slowest_display_stage", slowestDisplayStage(ev),
	)

	select {
	case p.out <- ev:
	default:
		// UI is behind; drop this event rather than stalling the
		// segmenter consumer loop.
	}

	// Hand the spoken text to the async TTS worker. We pick the
	// translation when available, otherwise the original transcript
	// so the user still hears something when MT is disabled.
	if p.cfg.TTS != nil {
		spokenText := ev.Translation
		spokenLang := ev.TargetLang
		if spokenText == "" {
			spokenText = ev.Text
			spokenLang = ev.Language
		}
		if spokenText != "" && spokenLang != "" {
			job := ttsJob{text: spokenText, lang: spokenLang, speechEnd: speechEnd}
			select {
			case p.ttsQ <- job:
			default:
				// TTS worker is behind — drop rather than block STT.
				p.ttsDrops.Add(1)
				slog.Warn("tts queue full, dropping utterance", "lang", spokenLang, "drops", p.ttsDrops.Load())
			}
		}
	}
}

// runPartials polls the segmenter's accumulating buffer on a timer
// and emits "what's been said so far" previews while an utterance is
// open. Whisper has no incremental API so each partial is a fresh
// full-buffer Transcribe; that competes with the final Transcribe on
// the engine mutex, but the trade-off is worth it for the typing-on-
// the-fly feel the UI gets back.
//
// Skip rules: not speaking, buffer too short, or duration unchanged
// since the previous partial — all three short-circuit without
// touching the engine, so the heavy path runs only when there's new
// audio worth decoding.
func (p *Pipeline) runPartials(ctx context.Context) {
	defer p.wg.Done()
	period := p.cfg.PartialPeriod
	minDur := p.cfg.PartialMinDuration
	if minDur <= 0 {
		minDur = 500 * time.Millisecond
	}
	t := time.NewTicker(period)
	defer t.Stop()
	var lastDur time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-t.C:
			if !p.seg.IsSpeaking() {
				lastDur = 0
				continue
			}
			pcm, dur := p.seg.SnapshotCurrent()
			if dur < minDur || len(pcm) == 0 {
				continue
			}
			if dur == lastDur {
				// No new audio since the previous tick; skip the
				// (expensive) decode and try again next period.
				continue
			}
			lastDur = dur

			// Greedy decode, single beam, no prompt history — partials
			// are throwaway so we skip the context-bias setup that the
			// final path uses.
			cfg := p.cfg.Engine
			tr, err := p.engine.Transcribe(pcm, &cfg)
			if err != nil {
				slog.Debug("partial transcribe failed", "err", err)
				continue
			}
			text := normalize(tr.Text)
			if text == "" {
				continue
			}
			select {
			case p.partials <- Partial{
				Text:     text,
				Language: tr.Language,
				Duration: dur,
			}:
			default:
				// UI is behind — drop the preview rather than blocking.
			}
		}
	}
}

// runTTS drains the TTS queue serially. Keeping it serial preserves
// playback order; making it asynchronous keeps it off the STT->display
// critical path. Updates p.lastTTSLag so the next Event can report it.
func (p *Pipeline) runTTS(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case job, ok := <-p.ttsQ:
			if !ok {
				return
			}
			t0 := time.Now()
			pcm, err := p.cfg.TTS.Synthesize(ctx, job.text, job.lang)
			lat := time.Since(t0)
			p.lastTTSLag.Store(int64(lat))
			if err != nil {
				slog.Warn("tts synthesize failed", "err", err, "lang", job.lang)
				continue
			}
			if p.cfg.Audio != nil {
				p.cfg.Audio.WritePCM(pcm)
			}
			slog.Info("tts complete",
				"lang", job.lang,
				"tts", lat,
				"samples", len(pcm),
				"end_to_play", time.Since(job.speechEnd),
			)
		}
	}
}

// slowestDisplayStage returns the human-readable name of the dominant
// latency contributor on the *display* critical path (i.e. up to the
// moment the user can read the translation; TTS is excluded because it
// runs asynchronously and doesn't gate the UI).
func slowestDisplayStage(ev Event) string {
	stages := [...]struct {
		name string
		dur  time.Duration
	}{
		{"hangover", ev.HangoverLatency},
		{"queue", ev.QueueLatency},
		{"stt", ev.STTLatency},
		{"mt", ev.MTLatency},
	}
	worst := stages[0]
	for _, s := range stages[1:] {
		if s.dur > worst.dur {
			worst = s
		}
	}
	return worst.name
}

// roundFloat truncates f to `places` decimals; slog renders the result
// as a compact number rather than a noisy 0.34829374… literal.
func roundFloat(f float64, places int) float64 {
	mul := 1.0
	for range places {
		mul *= 10
	}
	return float64(int64(f*mul+0.5)) / mul
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
	safeClosePartial(p.partials)
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

func safeClosePartial(ch chan Partial) {
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

// accumulateVocab updates the running word-frequency table from a
// final transcript. Words shorter than 3 runes (or pure ASCII
// punctuation) are ignored — Whisper benefits from a bias toward
// content words, not stop-syllables.
func accumulateVocab(v map[string]int, text string) {
	if v == nil {
		return
	}
	start := -1
	rs := []rune(text)
	for i, r := range rs {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			w := strings.ToLower(string(rs[start:i]))
			if len(w) >= 3 {
				v[w]++
			}
			start = -1
		}
	}
	if start >= 0 {
		w := strings.ToLower(string(rs[start:]))
		if len(w) >= 3 {
			v[w]++
		}
	}
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '\'' || r == '-' ||
		// Cyrillic / Latin extended blocks — covers ru/uk/be/sr and
		// the common European accents we care about.
		(r >= 0x0080 && r != ' ' && r != '\t' && r != '\n' && r != '\r' &&
			r != '.' && r != ',' && r != ';' && r != ':' &&
			r != '!' && r != '?' && r != '"' && r != '(' && r != ')' &&
			r != '[' && r != ']' && r != '{' && r != '}')
}

// topVocab returns the top-n most frequent words. Ties are broken by
// alphabetical order so the output is stable across calls (helps
// Whisper's cache-friendliness and our debugging).
func topVocab(v map[string]int, n int) []string {
	if len(v) == 0 || n <= 0 {
		return nil
	}
	type wf struct {
		w string
		f int
	}
	arr := make([]wf, 0, len(v))
	for w, f := range v {
		arr = append(arr, wf{w, f})
	}
	// Insertion sort; n is small (≤30) and len(v) typically <1000.
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].f != arr[j].f {
			return arr[i].f > arr[j].f
		}
		return arr[i].w < arr[j].w
	})
	if len(arr) > n {
		arr = arr[:n]
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		out[i] = e.w
	}
	return out
}

// buildInitialPrompt joins the vocab bias and the rolling tail into
// one bounded string. Vocab goes first because Whisper attends more
// strongly to the early prompt tokens; rolling tail provides the
// natural-language follow-on.
func buildInitialPrompt(vocab []string, tail string, maxRunes int) string {
	if len(vocab) == 0 && tail == "" {
		return ""
	}
	var b strings.Builder
	if len(vocab) > 0 {
		b.WriteString(strings.Join(vocab, " "))
	}
	if tail != "" {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(tail)
	}
	return trimPrompt(b.String(), maxRunes)
}
