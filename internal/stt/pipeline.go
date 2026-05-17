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
	// ID is a monotonic counter assigned by the Pipeline. The async
	// MT worker emits a matching TranslationUpdate carrying the same
	// ID once translation completes; the UI uses it to attach the
	// translation to the right transcript line.
	ID              uint64
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
		// Partials run a fresh full-buffer Whisper Transcribe each tick,
		// which on CPU competes with the final Transcribe and the
		// parallel MT pass. 1500 ms cadence cuts the partial workload
		// in half versus the previous 700 ms; long enough that there is
		// new audio worth decoding, short enough that the live lane
		// still feels responsive.
		PartialPeriod:      1500 * time.Millisecond,
		PartialMinDuration: 700 * time.Millisecond,
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

// TranslationUpdate carries the result of an async MT pass back to the
// UI. It is paired with a prior Event by EventID; the UI typically
// appends one Translation line per update, in arrival order (the MT
// worker is serial so updates arrive FIFO).
type TranslationUpdate struct {
	EventID     uint64
	SourceText  string
	Translation string
	SourceLang  string
	TargetLang  string
	MTLatency   time.Duration
	// TotalLatency is end-of-speech → translation visible (includes
	// the upstream STT + MT queueing). Distinct from Event.DisplayLatency
	// which is end-of-speech → transcript visible.
	TotalLatency time.Duration
	SpeechEnd    time.Time
}

// Pipeline runs VAD + Whisper + (optional) MT + (optional) TTS as a
// single coordinated unit.
type Pipeline struct {
	cfg    Config
	seg    *vad.Segmenter
	engine *whisper.Engine

	ctx context.Context // set in Start; used by TTS Synthesize

	out          chan Event
	translations chan TranslationUpdate
	partials     chan Partial
	done         chan struct{}
	wg           sync.WaitGroup

	// mtQ feeds the async MT worker. Decoupling MT from the STT
	// consumer goroutine lets utterance N+1 start its Whisper pass
	// while utterance N is still being translated, dropping perceived
	// queue lag to ~0 on sustained speech.
	mtQ      chan mtJob
	mtDrops  atomic.Uint64
	lastMTLag atomic.Int64 // nanoseconds; surfaced in following Events

	// ttsQ feeds the async TTS worker. Synthesize+playback are off the
	// critical path so the UI sees the translation as soon as MT
	// completes; TTS audio arrives a beat later.
	ttsQ       chan ttsJob
	lastTTSLag atomic.Int64 // nanoseconds; surfaced into the next Event
	ttsDrops   atomic.Uint64

	eventSeq atomic.Uint64

	mu      sync.Mutex
	prompt  string // rolling InitialPrompt assembled from previous finals
	vocab   map[string]int // word frequency table, used to bias Whisper
	running bool
}

// mtJob carries the data the async MT worker needs to translate one
// utterance and emit a TranslationUpdate paired with the originating
// Event.
type mtJob struct {
	eventID   uint64
	text      string
	srcLang   string
	speechEnd time.Time
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
		cfg:          cfg,
		seg:          seg,
		engine:       eng,
		out:          make(chan Event, cfg.OutputBuffer),
		translations: make(chan TranslationUpdate, cfg.OutputBuffer),
		partials:     make(chan Partial, 4),
		done:         make(chan struct{}),
		vocab:        make(map[string]int, 256),
		// Cap=4 lets two utterances queue for MT without blocking the
		// STT goroutine. Anything beyond that is dropped because m2m100
		// at ~1-2 s/utt cannot keep up with a faster speaker; older
		// jobs are stale by the time MT would reach them anyway.
		mtQ: make(chan mtJob, 4),
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

// TranslationOutput returns the channel that emits async MT results.
// Each TranslationUpdate corresponds to a previously-emitted Event with
// the same EventID. Closed when the Pipeline is stopped via Close.
func (p *Pipeline) TranslationOutput() <-chan TranslationUpdate { return p.translations }

// VADStats exposes the segmenter counters so the UI can render a live
// "VAD is firing" indicator without poking the internal segmenter.
func (p *Pipeline) VADStats() vad.Stats { return p.seg.Stats() }

// SetMT swaps the MT engine and target language at runtime. Safe to call
// from any goroutine; the next handleUtterance picks up the new engine.
// Pass nil to disable MT (transcripts are emitted without translation).
func (p *Pipeline) SetMT(eng mt.Engine, targetLang string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.MT = eng
	p.cfg.TargetLang = targetLang
}

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
	// MT worker is always started — handleUtterance funnels jobs into
	// mtQ unconditionally and the worker no-ops when cfg.MT is nil.
	// Keeping it on simplifies SetMT (hot-reload from the Models tab)
	// because we don't have to manage worker lifecycle on engine swaps.
	p.wg.Add(1)
	go p.runMT(ctx)
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
	// PCM is consumed by Transcribe (it copies into its float32
	// buffer) — return the int16 slice to the segmenter's pool now so
	// the next utterance can reuse the backing array instead of
	// allocating fresh. Safe to call on any path including error.
	vad.ReleasePCM(ut.PCM)
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
	if isHallucination(text) {
		// Whisper trained on YouTube emits subtitle-credit phrases on
		// silence / low-energy buffers ("Субтитры создавал DimaTorzok",
		// "Редактор субтитров А.Синецкая", "Thanks for watching", …).
		// Drop them instead of feeding MT/TTS with phantom content.
		slog.Debug("dropped hallucinated transcript", "text", text, "lang", tr.Language)
		if p.cfg.PartialPeriod > 0 {
			select {
			case p.partials <- Partial{Final: true, Text: "", Language: tr.Language, Duration: ut.Duration}:
			default:
			}
		}
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

	// Snapshot MT engine + target lang under the lock so a concurrent
	// SetMT (called from the Models tab after a successful download)
	// can swap the engine without racing this handler.
	p.mu.Lock()
	mtEng := p.cfg.MT
	mtTarget := p.cfg.TargetLang
	p.mu.Unlock()
	willTranslate := mtEng != nil && mtTarget != "" && tr.Language != mtTarget

	// DisplayLatency is end-of-speech → transcript visible. MT now
	// runs asynchronously, so this no longer includes MT time —
	// transcripts ship as soon as Whisper returns, and the translation
	// is patched in later via TranslationUpdate. That cut wall-time
	// to the first user-visible artifact from ~stt+mt to ~stt alone.
	ev.ID = p.eventSeq.Add(1)
	ev.DisplayLatency = time.Since(speechEnd)
	// TTSLatency on this event reflects the *previous* utterance's
	// synthesise wall time — there's no way to know the current one
	// before we ship it. Good enough for a trend indicator in the UI.
	ev.TTSLatency = time.Duration(p.lastTTSLag.Load())
	// MTLatency on the Event reflects the previous utterance's MT
	// wall time (because the current one hasn't been translated yet).
	// The accurate per-event value arrives on the TranslationUpdate.
	ev.MTLatency = time.Duration(p.lastMTLag.Load())
	ev.TotalLatency = ev.DisplayLatency + ev.TTSLatency

	slog.Info("utterance ready",
		"event_id", ev.ID,
		"text", ev.Text,
		"lang", ev.Language,
		"target", mtTarget,
		"will_translate", willTranslate,
		"duration", ev.Duration,
		"hangover", ev.HangoverLatency,
		"queue", ev.QueueLatency,
		"stt", ev.STTLatency,
		"display", ev.DisplayLatency,
		"prev_mt", ev.MTLatency,
		"prev_tts", ev.TTSLatency,
		"slowest_display_stage", slowestDisplayStage(ev),
	)

	select {
	case p.out <- ev:
	default:
		// UI is behind; drop this event rather than stalling the
		// segmenter consumer loop.
	}

	if willTranslate {
		job := mtJob{
			eventID:   ev.ID,
			text:      ev.Text,
			srcLang:   ev.Language,
			speechEnd: speechEnd,
		}
		select {
		case p.mtQ <- job:
		default:
			// MT worker is behind — drop rather than block STT.
			// Without this, a speaker who outpaces the translator
			// would stall the segmenter consumer and cause queue
			// latency to compound.
			p.mtDrops.Add(1)
			slog.Warn("mt queue full, dropping utterance",
				"event_id", ev.ID, "drops", p.mtDrops.Load())
		}
	} else if p.cfg.TTS != nil {
		// MT off (e.g. same source / target language). Hand the
		// raw transcript to TTS directly so the user still hears
		// playback when configured.
		p.enqueueTTS(ev.Text, ev.Language, speechEnd)
	}
}

// enqueueTTS pushes a job onto the TTS worker. Best-effort: full queue
// drops rather than blocking the caller. Centralised so both the
// MT-disabled path (handleUtterance) and the MT-success path (runMT)
// share one bookkeeping site.
func (p *Pipeline) enqueueTTS(text, lang string, speechEnd time.Time) {
	if p.cfg.TTS == nil || text == "" || lang == "" {
		return
	}
	job := ttsJob{text: text, lang: lang, speechEnd: speechEnd}
	select {
	case p.ttsQ <- job:
	default:
		p.ttsDrops.Add(1)
		slog.Warn("tts queue full, dropping utterance", "lang", lang, "drops", p.ttsDrops.Load())
	}
}

// runMT drains the MT queue serially. Each job runs Translate against
// the engine snapshotted at job-pickup time, emits a TranslationUpdate,
// and (if TTS is configured) forwards the translated text to the TTS
// worker. Decoupling from handleUtterance is the main latency win:
// utterance N+1's Whisper pass starts the instant utterance N is
// emitted, instead of waiting through utterance N's MT.
func (p *Pipeline) runMT(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case job, ok := <-p.mtQ:
			if !ok {
				return
			}
			p.mu.Lock()
			mtEng := p.cfg.MT
			mtTarget := p.cfg.TargetLang
			p.mu.Unlock()
			if mtEng == nil || mtTarget == "" {
				// Engine was swapped to nil between enqueue and pickup.
				// Skip translation but still fire TTS on raw transcript
				// so playback works in MT-off mode.
				p.enqueueTTS(job.text, job.srcLang, job.speechEnd)
				continue
			}
			t0 := time.Now()
			translation, err := mtEng.Translate(job.text, job.srcLang, mtTarget)
			lat := time.Since(t0)
			p.lastMTLag.Store(int64(lat))
			if err != nil {
				slog.Warn("mt translate failed",
					"err", err, "event_id", job.eventID,
					"src_lang", job.srcLang, "dst_lang", mtTarget)
				// Best-effort: still let TTS read the source.
				p.enqueueTTS(job.text, job.srcLang, job.speechEnd)
				continue
			}
			upd := TranslationUpdate{
				EventID:      job.eventID,
				SourceText:   job.text,
				Translation:  translation,
				SourceLang:   job.srcLang,
				TargetLang:   mtTarget,
				MTLatency:    lat,
				TotalLatency: time.Since(job.speechEnd),
				SpeechEnd:    job.speechEnd,
			}
			select {
			case p.translations <- upd:
			default:
				slog.Warn("translation output channel full, dropping update",
					"event_id", job.eventID)
			}
			slog.Info("mt translated",
				"event_id", job.eventID,
				"src_lang", job.srcLang, "dst_lang", mtTarget,
				"mt", lat, "total_to_translation", upd.TotalLatency,
			)
			if translation != "" {
				p.enqueueTTS(translation, mtTarget, job.speechEnd)
			} else {
				p.enqueueTTS(job.text, job.srcLang, job.speechEnd)
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
				vad.ReleasePCM(pcm)
				continue
			}
			if dur == lastDur {
				// No new audio since the previous tick; skip the
				// (expensive) decode and try again next period.
				vad.ReleasePCM(pcm)
				continue
			}
			lastDur = dur

			// Greedy decode, single beam, no prompt history — partials
			// are throwaway so we skip the context-bias setup that the
			// final path uses.
			cfg := p.cfg.Engine
			tr, err := p.engine.Transcribe(pcm, &cfg)
			vad.ReleasePCM(pcm)
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
	safeCloseTranslations(p.translations)
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

func safeCloseTranslations(ch chan TranslationUpdate) {
	defer func() { _ = recover() }()
	close(ch)
}

// hallucinationFingerprints are canonical Whisper "phantom subtitle"
// outputs we have actually observed in slog. Match rule: the whole
// transcript, lower-cased and stripped of trailing punctuation, must
// equal one of these strings. Substring matching was tried and
// rejected because tokens like "корректор" / "субтитры" legitimately
// appear in user speech and were causing real utterances to be
// silently dropped.
var hallucinationFingerprints = map[string]struct{}{
	"субтитры сделал dimatorzok":                                 {},
	"субтитры создавал dimatorzok":                               {},
	"субтитры подогнал «коронован»":                              {},
	"редактор субтитров а.синецкая корректор а.егорова":          {},
	"редактор субтитров а.синецкая\nкорректор а.егорова":         {},
	"продолжение следует...":                                     {},
	"продолжение следует":                                        {},
	"спасибо за просмотр":                                        {},
	"спасибо за внимание":                                        {},
	"подписывайтесь на канал":                                    {},
	"thanks for watching":                                        {},
	"thank you for watching":                                     {},
	"please subscribe":                                           {},
	"like and subscribe":                                         {},
	"see you in the next video":                                  {},
}

// isHallucination reports whether text exactly matches a known
// Whisper phantom subtitle fingerprint. Conservative by design: real
// speech that merely mentions one of these phrases as a fragment is
// kept; only outputs that consist entirely of the fingerprint are
// dropped.
func isHallucination(text string) bool {
	if text == "" {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(text))
	low = strings.TrimRight(low, ".!?…")
	low = strings.TrimSpace(low)
	_, hit := hallucinationFingerprints[low]
	return hit
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
