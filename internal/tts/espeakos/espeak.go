// Package espeakos wraps the `espeak-ng` CLI as a TTS backend. It
// targets Linux (where espeak-ng is one apt/dnf away and pre-installed
// on many distros) and is also usable as a secondary fallback on
// macOS when Piper is unavailable and the user has installed
// espeak-ng manually.
//
// Quality is robotic (formant synthesis, not neural) but coverage is
// excellent — 100+ languages including Russian, and latency is
// sub-100 ms per utterance on any commodity CPU.
package espeakos

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// SampleRate matches piper / sayos so the playback ringbuf stays on
// a single fixed rate. espeak-ng renders at 22050 Hz by default when
// asked for WAV; we don't need to remap.
const SampleRate = 22050

// Voice maps an ISO-639-1 language to the espeak-ng voice/variant
// name passed via `-v`. Defaults are baked into New() for the
// languages our MT layer supports.
type Voice struct {
	Lang string
	Name string // e.g. "en-us", "ru", "de"
}

// Engine renders text into 22050 Hz mono int16 PCM via espeak-ng.
type Engine struct {
	mu      sync.RWMutex
	voices  map[string]Voice
	bin     string
	timeout time.Duration
}

// New constructs an Engine. Returns an error if `espeak-ng` is not
// found on PATH.
func New() (*Engine, error) {
	bin, err := exec.LookPath("espeak-ng")
	if err != nil {
		// Fall back to plain `espeak` for older distros that ship the
		// 1.x line under that name.
		bin, err = exec.LookPath("espeak")
		if err != nil {
			return nil, fmt.Errorf("espeakos: neither espeak-ng nor espeak found on PATH")
		}
	}
	e := &Engine{
		bin:     bin,
		voices:  make(map[string]Voice, 16),
		timeout: 15 * time.Second,
	}
	for _, v := range defaultVoices {
		e.voices[v.Lang] = v
	}
	return e, nil
}

// defaultVoices map ISO-639-1 codes to espeak-ng voice identifiers.
// Coverage matches what the MT layer is plausible to emit.
var defaultVoices = []Voice{
	{Lang: "en", Name: "en-us"},
	{Lang: "ru", Name: "ru"},
	{Lang: "uk", Name: "uk"},
	{Lang: "es", Name: "es"},
	{Lang: "de", Name: "de"},
	{Lang: "fr", Name: "fr"},
	{Lang: "it", Name: "it"},
	{Lang: "pt", Name: "pt"},
	{Lang: "pl", Name: "pl"},
	{Lang: "nl", Name: "nl"},
	{Lang: "tr", Name: "tr"},
	{Lang: "ja", Name: "ja"},
	{Lang: "zh", Name: "cmn"},
}

// SetVoice overrides the voice used for a language.
func (e *Engine) SetVoice(v Voice) error {
	if v.Lang == "" || v.Name == "" {
		return errors.New("espeakos: voice requires Lang and Name")
	}
	e.mu.Lock()
	e.voices[strings.ToLower(v.Lang)] = v
	e.mu.Unlock()
	return nil
}

// HasVoice reports whether a voice is registered for the language.
func (e *Engine) HasVoice(lang string) bool {
	e.mu.RLock()
	_, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	return ok
}

// Synthesize runs espeak-ng on the text and returns 22050 Hz mono
// int16 PCM. Matches stt.TTSEngine.Synthesize.
func (e *Engine) Synthesize(ctx context.Context, text, lang string) ([]int16, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("espeakos: empty text")
	}
	e.mu.RLock()
	voice, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("espeakos: no voice registered for %q", lang)
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// --stdout emits a complete RIFF/WAVE stream to stdout. Default
	// sample rate is 22050 Hz mono — exactly what the playback ring
	// expects, so no resampling needed. Text comes in via stdin.
	cmd := exec.CommandContext(ctx, e.bin,
		"-v", voice.Name,
		"--stdout",
	)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("espeakos: %w (stderr=%q)", err, stderr.String())
	}
	return parseWavPCM16(stdout.Bytes())
}

// Close is a no-op — each Synthesize spawns its own short-lived
// subprocess.
func (e *Engine) Close() error { return nil }

// parseWavPCM16 reads a minimal RIFF/WAVE header and reinterprets the
// "data" chunk as little-endian int16 samples. Mirrors the helper in
// internal/tts/sayos so the two backends share an identical decode
// contract.
func parseWavPCM16(b []byte) ([]int16, error) {
	if len(b) < 44 {
		return nil, errors.New("espeakos: WAVE too small")
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("espeakos: not a RIFF/WAVE file")
	}
	off := 12
	for off+8 <= len(b) {
		tag := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		off += 8
		if tag == "data" {
			end := off + size
			if end > len(b) {
				end = len(b)
			}
			payload := b[off:end]
			if len(payload)%2 != 0 {
				payload = payload[:len(payload)-1]
			}
			n := len(payload) / 2
			if n == 0 {
				return nil, nil
			}
			return unsafe.Slice((*int16)(unsafe.Pointer(&payload[0])), n), nil
		}
		off += size
		if size%2 == 1 {
			off++
		}
	}
	return nil, errors.New("espeakos: no data chunk in WAVE output")
}
