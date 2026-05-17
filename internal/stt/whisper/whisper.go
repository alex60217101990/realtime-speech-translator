// Package whisper provides a higher-level Go API for the upstream
// whisper.cpp Go bindings (bindings/go/pkg/whisper).
//
// The package adds:
//
//   - lifecycle management (model load + context reuse, idle unload);
//   - language tagging and translate-mode mapping;
//   - int16 -> float32 normalisation via the SIMD-aware sample package;
//   - a Transcript value type decoupled from the upstream Segment type
//     so that callers do not have to import the binding directly.
//
// It is not a streaming wrapper. Streaming logic (windowing, partial
// emission, deduplication) lives in internal/stt/stream.
package whisper

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	upstream "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/sample"
)

// Transcript is the result of processing one utterance of PCM.
type Transcript struct {
	Text     string
	Language string        // detected or configured source language code
	Latency  time.Duration // wall time spent in Engine.Transcribe
	Segments []Segment
}

// Segment is a single timestamped chunk of recognised speech. The
// timestamps are relative to the start of the supplied PCM, not absolute
// wall time.
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Config controls per-call recognition parameters.
type Config struct {
	// Language is an ISO-639-1 code ("ru", "en", ...) or "auto" to let
	// Whisper detect.
	Language string

	// Translate routes Whisper into translate-to-English mode. We
	// generally keep this off and rely on a separate MT step; included
	// for completeness.
	Translate bool

	// Threads caps the number of inference threads. Zero means
	// runtime.NumCPU()-1 (clamped to ≥1) — leaves one core for the
	// audio thread + UI but otherwise gives Whisper everything we have,
	// because Transcribe is the single biggest contributor to perceived
	// latency.
	Threads int

	// BeamSize chooses greedy (1) vs beam search (>1). 1 keeps latency
	// minimal and is fine for short utterances.
	BeamSize int

	// InitialPrompt biases the decoder with prior context (e.g. the
	// previous utterance). Bounded to ~200 chars by the binding.
	InitialPrompt string
}

// DefaultConfig returns a Config tuned for low-latency single-utterance
// recognition.
func DefaultConfig() Config {
	return Config{
		Language: "auto",
		Threads:  0,
		BeamSize: 1,
	}
}

// Engine owns a loaded Whisper model and a serial decoding context. It
// is safe to call Transcribe from any goroutine; calls are serialised
// internally because the upstream binding's whisper_context is not
// thread-safe.
type Engine struct {
	mu       sync.Mutex
	model    upstream.Model
	defaults Config

	// pcmBuf is reused across calls to avoid per-utterance float32
	// allocations.
	pcmBuf []float32
}

// New loads a Whisper model from disk. The returned Engine holds the
// model in RAM until Close is called.
func New(modelPath string, defaults Config) (*Engine, error) {
	if modelPath == "" {
		return nil, errors.New("whisper: empty model path")
	}
	m, err := upstream.New(modelPath)
	if err != nil {
		return nil, fmt.Errorf("whisper: load model: %w", err)
	}
	return &Engine{model: m, defaults: defaults}, nil
}

// Close releases the model. Calling Transcribe afterwards returns an
// error.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model == nil {
		return nil
	}
	err := e.model.Close()
	e.model = nil
	e.pcmBuf = nil
	return err
}

// IsMultilingual reports whether the loaded model supports languages
// other than English.
func (e *Engine) IsMultilingual() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model == nil {
		return false
	}
	return e.model.IsMultilingual()
}

// Transcribe runs Whisper on the given 16 kHz mono int16 PCM and returns
// a Transcript. The pcm slice is not retained.
func (e *Engine) Transcribe(pcm []int16, override *Config) (*Transcript, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.model == nil {
		return nil, errors.New("whisper: engine closed")
	}
	if len(pcm) == 0 {
		return nil, errors.New("whisper: empty pcm")
	}

	cfg := e.defaults
	if override != nil {
		cfg = mergeConfig(cfg, *override)
	}
	if cfg.Threads == 0 {
		// Whisper and CTranslate2 run concurrently in the async
		// pipeline. Letting Whisper grab NumCPU-1 starves m2m100 on
		// the next utterance and triggers context-switching thrashing
		// (15 threads on 8 cores). Half of NumCPU keeps both engines
		// responsive without oversubscription. Floor at 2 so even a
		// 2-core box runs.
		t := max(runtime.NumCPU()/2, 2)
		cfg.Threads = t
	}

	// Normalise int16 -> float32 into a reusable buffer.
	if cap(e.pcmBuf) < len(pcm) {
		e.pcmBuf = make([]float32, len(pcm))
	} else {
		e.pcmBuf = e.pcmBuf[:len(pcm)]
	}
	sample.Int16ToFloat32(e.pcmBuf, pcm)
	// 100 Hz single-pole high-pass IIR. Strips low-frequency rumble
	// (AC hum, mic-stand vibration, distant traffic) that contributes
	// nothing to phoneme identification and skews the log-mel
	// spectrogram. Coefficient α = exp(-2π·fc/fs) = exp(-2π·100/16000)
	// ≈ 0.9610. Stateless per-call: the small phase distortion at
	// utterance boundaries is well below the audible threshold and
	// far below what Whisper's mel filterbank cares about.
	highPass100Hz(e.pcmBuf)
	// Peak-normalise the float32 buffer so Whisper sees audio at the
	// amplitude it was trained on regardless of mic gain or how close
	// the user is to the microphone. Quiet input (peak well below
	// full scale) is the dominant cause of Whisper producing
	// plausible-sounding garbage on real speech — the decoder
	// hallucinates when the log-mel spectrogram has weak energy.
	//
	// Target peak 0.7 leaves headroom for harmonics; gain capped at
	// 12× so we do not amplify a near-silent buffer (room tone with
	// occasional clicks) into something the decoder treats as speech.
	// Skip normalisation when the signal is already loud (peak ≥ 0.5)
	// or so quiet that any gain is amplifying noise (peak < 0.005,
	// roughly -46 dBFS).
	const (
		targetPeak = float32(0.7)
		noFloor    = float32(0.005)
		ceiling    = float32(0.5)
		maxGain    = float32(12.0)
	)
	var peak float32
	for _, v := range e.pcmBuf {
		a := v
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak > noFloor && peak < ceiling {
		gain := targetPeak / peak
		if gain > maxGain {
			gain = maxGain
		}
		for i, v := range e.pcmBuf {
			e.pcmBuf[i] = v * gain
		}
	}

	ctx, err := e.model.NewContext()
	if err != nil {
		return nil, fmt.Errorf("whisper: new context: %w", err)
	}
	ctx.SetTranslate(cfg.Translate)
	ctx.SetThreads(uint(cfg.Threads))
	if cfg.BeamSize > 0 {
		ctx.SetBeamSize(cfg.BeamSize)
	}
	// Use the caller-supplied InitialPrompt when present (e.g. rolling
	// history when PromptHistory>0 upstream). Otherwise fall back to a
	// fixed language seed: a single line of canonical text in the
	// configured source language. This nudges the decoder toward the
	// right phoneme inventory and orthography without the runaway
	// hallucination risk of rolling-history prompts.
	prompt := cfg.InitialPrompt
	if prompt == "" && cfg.Language != "" && cfg.Language != "auto" {
		prompt = languagePromptSeed(cfg.Language)
	}
	if prompt != "" {
		ctx.SetInitialPrompt(prompt)
	}
	if cfg.Language != "" {
		if err := ctx.SetLanguage(cfg.Language); err != nil {
			return nil, fmt.Errorf("whisper: set lang %q: %w", cfg.Language, err)
		}
	}

	t0 := time.Now()
	if err := ctx.Process(e.pcmBuf, nil, nil, nil); err != nil {
		return nil, fmt.Errorf("whisper: process: %w", err)
	}

	tr := &Transcript{
		Language: ctx.DetectedLanguage(),
		Latency:  time.Since(t0),
	}
	for {
		seg, err := ctx.NextSegment()
		if err != nil {
			break // io.EOF — done
		}
		tr.Segments = append(tr.Segments, Segment{
			Start: seg.Start,
			End:   seg.End,
			Text:  seg.Text,
		})
		tr.Text += seg.Text
	}
	return tr, nil
}

// highPass100Hz applies a stateless single-pole IIR high-pass at
// approximately 100 Hz to a 16 kHz float32 buffer. α = exp(-2π·fc/fs).
// Used as cheap preprocessing before Whisper: rolls off AC hum,
// mic-stand vibration and distant rumble, all of which add nothing
// useful to the log-mel spectrogram but eat energy budget.
func highPass100Hz(samples []float32) {
	if len(samples) == 0 {
		return
	}
	const alpha = float32(0.9610) // exp(-2π·100/16000) ≈ 0.9610
	var prevX, prevY float32
	for i, x := range samples {
		y := alpha * (prevY + x - prevX)
		samples[i] = y
		prevX = x
		prevY = y
	}
}

// languagePromptSeed returns a short fixed seed phrase that primes
// Whisper's decoder for the named ISO-639-1 language. The seed is NOT
// rolling history (no compounding hallucination over prior utterances);
// it just gives the language model a one-line nudge toward the right
// phoneme inventory and orthography for the current speech. Empty
// string when no seed is registered for the language (Whisper falls
// back to its language-detect default).
func languagePromptSeed(lang string) string {
	switch lang {
	case "ru":
		return "Это разговор на русском языке."
	case "uk":
		return "Це розмова українською мовою."
	case "en":
		return "This is a conversation in English."
	case "es":
		return "Esta es una conversación en español."
	case "de":
		return "Das ist ein Gespräch auf Deutsch."
	case "fr":
		return "C'est une conversation en français."
	case "it":
		return "Questa è una conversazione in italiano."
	case "pt":
		return "Esta é uma conversa em português."
	case "pl":
		return "To jest rozmowa po polsku."
	case "nl":
		return "Dit is een gesprek in het Nederlands."
	case "tr":
		return "Bu bir Türkçe konuşmadır."
	case "ja":
		return "これは日本語の会話です。"
	case "zh":
		return "这是一段中文对话。"
	}
	return ""
}

// mergeConfig overlays b on top of a. Zero values in b inherit from a.
func mergeConfig(a, b Config) Config {
	out := a
	if b.Language != "" {
		out.Language = b.Language
	}
	if b.Translate != a.Translate {
		out.Translate = b.Translate
	}
	if b.Threads > 0 {
		out.Threads = b.Threads
	}
	if b.BeamSize > 0 {
		out.BeamSize = b.BeamSize
	}
	if b.InitialPrompt != "" {
		out.InitialPrompt = b.InitialPrompt
	}
	return out
}
