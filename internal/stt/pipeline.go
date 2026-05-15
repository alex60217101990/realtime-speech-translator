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
	"sync"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/vad"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt/whisper"
)

// Event is emitted on the Pipeline's output channel for every recognised
// utterance.
type Event struct {
	Text     string
	Language string
	Started  time.Time
	Duration time.Duration
	Latency  time.Duration // STT processing time
}

// Config groups the runtime parameters of the Pipeline. The VADConfig
// and EngineConfig give callers full control over the underlying
// components; sensible defaults are exposed via DefaultConfig.
type Config struct {
	VAD     vad.Config
	Engine  whisper.Config

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

// Pipeline runs VAD + Whisper as a single coordinated unit.
type Pipeline struct {
	cfg     Config
	seg     *vad.Segmenter
	engine  *whisper.Engine

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

// WriteFrame forwards int16 mono PCM into the VAD segmenter. Safe to
// call from a single producer goroutine.
func (p *Pipeline) WriteFrame(samples []int16) { p.seg.WriteFrame(samples) }

// Start launches the recognition goroutine. Calling Start twice without
// an intervening Close returns an error.
func (p *Pipeline) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return errors.New("stt: already running")
	}
	p.running = true
	p.mu.Unlock()

	p.wg.Add(1)
	go p.run(ctx)
	return nil
}

func (p *Pipeline) run(ctx context.Context) {
	defer p.wg.Done()
	defer close(p.out)
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
		return
	}

	text := normalize(tr.Text)
	if text == "" {
		return
	}

	if p.cfg.PromptHistory > 0 {
		p.mu.Lock()
		// Keep the prompt bounded so it doesn't grow unboundedly.
		p.prompt = trimPrompt(p.prompt+" "+text, 200)
		p.mu.Unlock()
	}

	ev := Event{
		Text:     text,
		Language: tr.Language,
		Started:  ut.StartedAt,
		Duration: ut.Duration,
		Latency:  tr.Latency,
	}
	select {
	case p.out <- ev:
	default:
		// UI is behind; drop this event rather than stalling the
		// segmenter consumer loop.
	}
}

// Close stops the goroutine, the segmenter and the Whisper engine.
// Output channel is closed before Close returns.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = false
	p.mu.Unlock()

	close(p.done)
	_ = p.seg.Close()
	p.wg.Wait()
	if err := p.engine.Close(); err != nil {
		return fmt.Errorf("stt: close engine: %w", err)
	}
	return nil
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
