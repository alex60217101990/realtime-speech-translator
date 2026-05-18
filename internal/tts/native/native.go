// Package native is the last-resort TTS backend: it shells out to the
// host's built-in speech synthesizer (macOS `say`, Linux `espeak-ng`
// / `espeak`, Windows PowerShell `System.Speech`) and plays through
// the system's default audio output directly — bypassing the
// translator's own playback ring.
//
// Voice quality is far worse than sherpa-onnx Piper, but it lets the
// app stay useful when the user has not yet downloaded a Piper voice
// or when in-process synthesis fails to load. The cmd layer picks
// this backend automatically as a fallback.
package native

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

// Engine wraps a host TTS binary. It is safe to call Speak from any
// goroutine; each call spawns its own subprocess so concurrent
// invocations do not interfere.
type Engine struct {
	cmd     string
	mkArgs  func(text string) []string
	stdin   func(text string) string // non-empty: command reads text on stdin
	backend string                   // "say" | "espeak-ng" | "espeak" | "powershell"
}

// New probes the host for a usable TTS binary and returns the first
// one it finds. Returns nil and an error if no supported backend is
// installed.
func New() (*Engine, error) {
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("say"); err == nil {
			return &Engine{
				cmd:     p,
				mkArgs:  func(t string) []string { return []string{t} },
				backend: "say",
			}, nil
		}
	case "linux":
		for _, name := range []string{"espeak-ng", "espeak"} {
			if p, err := exec.LookPath(name); err == nil {
				return &Engine{
					cmd:     p,
					mkArgs:  func(t string) []string { return []string{t} },
					backend: name,
				}, nil
			}
		}
	case "windows":
		if p, err := exec.LookPath("powershell"); err == nil {
			return &Engine{
				cmd:    p,
				mkArgs: func(string) []string { return []string{"-NoProfile", "-Command", "-"} },
				stdin: func(text string) string {
					// PowerShell single-quoted string with doubled
					// embedded single quotes — the standard escape
					// for literal text in PS.
					escaped := ""
					for _, r := range text {
						if r == '\'' {
							escaped += "''"
						} else {
							escaped += string(r)
						}
					}
					return "Add-Type -AssemblyName System.Speech; " +
						"$s = New-Object System.Speech.Synthesis.SpeechSynthesizer; " +
						"$s.Speak('" + escaped + "')\n"
				},
				backend: "powershell",
			}, nil
		}
	}
	return nil, errors.New("native: no supported TTS backend found")
}

// Backend returns the short name of the resolved binary, useful for
// status displays.
func (e *Engine) Backend() string { return e.backend }

// Speak synthesizes text and plays it through the system's default
// output. Blocks until playback finishes or ctx is cancelled.
func (e *Engine) Speak(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, e.cmd, e.mkArgs(text)...)
	if e.stdin != nil {
		cmd.Stdin = newStringReader(e.stdin(text))
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("native %s: %w", e.backend, err)
	}
	return nil
}

// Close is a no-op (no resources held). Provided to satisfy the
// Engine-like contract the cmd layer expects.
func (e *Engine) Close() error { return nil }

// newStringReader avoids the strings package just to keep the import
// list minimal in this tiny file.
func newStringReader(s string) *stringReader { return &stringReader{s: s} }

type stringReader struct {
	s string
	i int
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, errEOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

var errEOF = errors.New("EOF")
