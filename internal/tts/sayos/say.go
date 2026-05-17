// Package sayos wraps the macOS built-in `say` command as a TTS
// backend. It exists because the upstream Piper ecosystem is brittle
// on macOS (rhasspy GitHub tarballs ship without their bundled
// .dylib files; the pip wheel of piper-tts pulls onnxruntime which
// has no Python 3.9 wheel on legacy macOS targets), and the native
// `say` command is already on every Mac since OS X 10.0.
//
// Quality is below Piper but acceptable for local monitoring; Apple
// ships dedicated voices for Russian (Milena, Yuri), English (Samantha,
// Alex, …), Spanish, German, French and many others. Latency on
// Apple Silicon is ~50 ms per utterance versus ~200-400 ms for a
// piper subprocess invocation.
//
// Build tag is darwin-only: there is no such binary on Linux/Windows.
package sayos

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// SampleRate matches what we request from `say` via --data-format.
// 22050 mirrors Piper so the downstream playback ringbuf can stay on
// one fixed rate regardless of which backend produced the audio.
const SampleRate = 22050

// Voice maps a language code to a macOS voice name. Built-in
// suggestions are baked into New() but callers can override via
// SetVoice — useful when the user picks "Alex" instead of "Samantha".
type Voice struct {
	// Lang is an ISO-639-1 code.
	Lang string
	// Name is the macOS voice name (`say -v "?"` lists them).
	Name string
}

// Engine renders text into 22050 Hz mono int16 PCM via the `say`
// command. Safe for concurrent use; each Synthesize spawns its own
// short-lived subprocess.
type Engine struct {
	mu     sync.RWMutex
	voices map[string]Voice
	bin    string
	// timeout caps a single say invocation. say can rarely hang on
	// pathological input; 15 s is comfortable for any sane sentence
	// at the slowest voice rate.
	timeout time.Duration
}

// New constructs an Engine. Returns an error if the `say` binary is
// not on PATH (e.g. when the package is somehow loaded on a non-Mac).
// The default voice table covers the languages we ship MT support
// for; callers may override per-language via SetVoice.
func New() (*Engine, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("sayos: only supported on macOS")
	}
	bin, err := exec.LookPath("say")
	if err != nil {
		return nil, fmt.Errorf("sayos: 'say' command not found: %w", err)
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

// defaultVoices picks one stock macOS voice per language we expect to
// translate into. These names exist on every modern macOS install;
// premium / enhanced voices the user may have downloaded separately
// can be selected via SetVoice.
var defaultVoices = []Voice{
	{Lang: "en", Name: "Samantha"},
	{Lang: "ru", Name: "Milena"},
	{Lang: "uk", Name: "Lesya"},
	{Lang: "es", Name: "Mónica"},
	{Lang: "de", Name: "Anna"},
	{Lang: "fr", Name: "Amélie"},
	{Lang: "it", Name: "Alice"},
	{Lang: "pt", Name: "Joana"},
	{Lang: "pl", Name: "Zosia"},
	{Lang: "nl", Name: "Xander"},
	{Lang: "tr", Name: "Yelda"},
	{Lang: "ja", Name: "Kyoko"},
	{Lang: "zh", Name: "Tingting"},
}

// SetVoice overrides the voice used for a language.
func (e *Engine) SetVoice(v Voice) error {
	if v.Lang == "" || v.Name == "" {
		return errors.New("sayos: voice requires Lang and Name")
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

// Synthesize runs `say` on the text and returns 22050 Hz mono int16 PCM.
// The signature matches stt.TTSEngine so this type drops in next to
// piper.Engine.
func (e *Engine) Synthesize(ctx context.Context, text, lang string) ([]int16, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("sayos: empty text")
	}
	e.mu.RLock()
	voice, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sayos: no voice registered for %q", lang)
	}

	tmp, err := os.CreateTemp("", "rst-say-*.wav")
	if err != nil {
		return nil, fmt.Errorf("sayos: tempfile: %w", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.bin,
		"-v", voice.Name,
		"--data-format=LEI16@22050",
		"--file-format=WAVE",
		"-o", path,
	)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sayos: say %w (stderr=%q)", err, stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sayos: read output: %w", err)
	}
	return parseWavPCM16(raw)
}

// Close is a no-op — each Synthesize spawns its own short-lived
// process, mirroring piper.Engine's lifecycle for consistency.
func (e *Engine) Close() error { return nil }

// parseWavPCM16 walks a minimal WAVE/RIFF header and returns the
// "data" chunk reinterpreted as little-endian int16 samples. say's
// WAVE output uses a standard fmt + data layout; we don't validate
// the format chunk because --data-format=LEI16@22050 fixes it.
func parseWavPCM16(b []byte) ([]int16, error) {
	if len(b) < 44 {
		return nil, errors.New("sayos: WAVE too small")
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("sayos: not a RIFF/WAVE file")
	}
	// Walk chunks after the 12-byte RIFF header to find "data".
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
			// Reinterpret without copying — same trick piper.bytesToInt16
			// uses. All target hosts are little-endian.
			return unsafe.Slice((*int16)(unsafe.Pointer(&payload[0])), n), nil
		}
		off += size
		if size%2 == 1 {
			off++ // chunks are word-aligned
		}
	}
	return nil, errors.New("sayos: no data chunk in WAVE output")
}
