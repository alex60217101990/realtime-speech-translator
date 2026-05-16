// Package piper wraps the Piper TTS engine as a subprocess.
//
// We deliberately do not link against piper as a C/C++ library: Piper
// depends on espeak-ng for phonemisation and onnxruntime for inference,
// both heavy dependencies that would inflate the binary and the build
// matrix. The subprocess approach trades a one-time exec cost (~30–50
// ms) for build simplicity. See docs/obsidian/decisions/ADR-008.
//
// The wrapper invokes the piper binary once per utterance with
// `--output_raw`, reads 22050 Hz mono int16 PCM from stdout, and
// returns it to the caller as []int16. A future revision may switch to
// the `--json-input` long-lived mode for sub-millisecond per-utterance
// startup; the public API of this package is shaped so that change is
// internal.
package piper

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// SampleRate is the sample rate Piper voices produce. All medium /
// high-quality voices in rhasspy/piper-voices use 22050 Hz; some "low"
// ones use 16000. Callers should consult the voice's `.onnx.json`
// sidecar to be exact; for our manifest-pinned voices we hard-code
// 22050 and document the assumption.
const SampleRate = 22050

// Voice identifies a Piper voice on disk: the .onnx model file plus
// its sibling .onnx.json config. The JSON sidecar must live next to
// ONNXPath; Piper resolves it automatically.
type Voice struct {
	// Lang is an ISO-639-1 code. The application chooses the Voice by
	// matching Lang to the MT target language.
	Lang string
	// ONNXPath is the absolute path to the voice's .onnx file.
	ONNXPath string
}

// Config bundles the engine-level parameters.
type Config struct {
	// BinaryPath points at the piper executable. Empty means look up
	// "piper" on PATH.
	BinaryPath string
	// SpawnTimeout caps how long Engine.Synthesize will wait for a
	// single piper invocation to complete. 0 = 10 s default.
	SpawnTimeout time.Duration
}

// DefaultConfig returns a Config that uses the PATH-resolved binary.
func DefaultConfig() Config {
	return Config{SpawnTimeout: 10 * time.Second}
}

// Engine synthesises text into 22050 Hz mono int16 PCM using Piper.
type Engine struct {
	cfg Config

	mu     sync.RWMutex
	voices map[string]Voice // keyed by ISO-639-1 language code
	bin    string           // resolved binary path
}

// New constructs an Engine and verifies the Piper binary is available.
// Voices are registered separately via SetVoice / AddVoice.
func New(cfg Config) (*Engine, error) {
	if cfg.SpawnTimeout == 0 {
		cfg.SpawnTimeout = 10 * time.Second
	}
	bin := cfg.BinaryPath
	if bin == "" {
		bin = "piper"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("piper: binary %q not found on PATH: %w", bin, err)
	}
	return &Engine{
		cfg:    cfg,
		bin:    resolved,
		voices: map[string]Voice{},
	}, nil
}

// AddVoice registers a Voice for a given language.
func (e *Engine) AddVoice(v Voice) error {
	if v.Lang == "" || v.ONNXPath == "" {
		return errors.New("piper: voice requires Lang and ONNXPath")
	}
	abs, err := filepath.Abs(v.ONNXPath)
	if err != nil {
		return err
	}
	v.ONNXPath = abs
	e.mu.Lock()
	e.voices[strings.ToLower(v.Lang)] = v
	e.mu.Unlock()
	return nil
}

// HasVoice reports whether the engine has a voice registered for lang.
func (e *Engine) HasVoice(lang string) bool {
	e.mu.RLock()
	_, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	return ok
}

// Voice returns the voice for the given language code, or zero value if
// none is registered.
func (e *Engine) Voice(lang string) (Voice, bool) {
	e.mu.RLock()
	v, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	return v, ok
}

// Synthesize runs piper on the given UTF-8 text using the voice
// registered for lang. Returns 22050 Hz mono int16 PCM.
func (e *Engine) Synthesize(ctx context.Context, text, lang string) ([]int16, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("piper: empty text")
	}
	voice, ok := e.Voice(lang)
	if !ok {
		return nil, fmt.Errorf("piper: no voice registered for %q", lang)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, e.cfg.SpawnTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, e.bin,
		"--model", voice.ONNXPath,
		"--output_raw",
	)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("piper: %w (stderr=%q)", err, stderr.String())
	}
	return bytesToInt16(stdout.Bytes()), nil
}

// Close releases internal state. Currently a no-op because each
// Synthesize spawns its own short-lived process; reserved for future
// long-lived process modes.
func (e *Engine) Close() error { return nil }

// bytesToInt16 reinterprets a raw little-endian PCM byte buffer as a
// []int16 slice without copying. The returned slice aliases the input
// buffer; callers must not retain it beyond the lifetime of the source
// bytes.
//
// All target platforms (macOS, Linux, Windows on x86_64 and arm64) are
// little-endian, so no byte-swapping is needed. We assert this at
// build time via the binary package below.
func bytesToInt16(b []byte) []int16 {
	if len(b) == 0 {
		return nil
	}
	if len(b)%2 != 0 {
		// Truncate a stray odd byte rather than panicking — piper has
		// been observed to emit a single zero byte at the very end of
		// some short utterances on macOS.
		b = b[:len(b)-1]
	}
	n := len(b) / 2
	// Validate endianness once: the binary.NativeEndian sentinel was
	// added in Go 1.21. If the host is big-endian we fall back to a
	// copying decoder.
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		out := make([]int16, n)
		for i := range n {
			out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
		}
		return out
	}
	// Little-endian host: reinterpret in place. The returned slice's
	// underlying array is `b`; the data header is rebuilt to use
	// half-size element count.
	return unsafe.Slice((*int16)(unsafe.Pointer(&b[0])), n)
}

// Silence the io unused-import lint when binary aliasing is enabled
// without copying paths above.
var _ io.Reader = (*bytes.Reader)(nil)
