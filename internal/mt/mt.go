// Package mt defines the machine-translation engine abstraction used
// by the rest of the application and provides two concrete backends:
//
//   - SMaLL-100 (Apache-2.0, distilled M2M-100, 100 languages, ~330 MB
//     int8, single model covering every pair) — see small100.go.
//   - OPUS-MT (Apache-2.0, per-pair Helsinki-NLP) — see opusmt.go.
//
// Both backends share the CTranslate2 inference runtime (internal/mt/ct2)
// and a SentencePiece tokenizer (internal/mt/sp). They differ in how
// source and target language tags are prepended/encoded.
//
// The concrete backends and the cgo runtime they depend on are gated
// behind the `mt` build tag, because CTranslate2 and SentencePiece
// must be compiled from third_party first. Without -tags mt only the
// Disabled engine is available — useful for builds that ship without
// translation (mic → STT → TTS only) and for editor tooling that
// cannot run scripts/build-deps.sh.
package mt

import (
	"errors"
	"sync"
)

// Engine is the minimal interface used by the STT→MT→TTS pipeline. Each
// backend constructs once at session start and serves serialised
// Translate calls until Close.
type Engine interface {
	// Translate runs one source sentence through the model. src is
	// UTF-8 plain text in the source language (no manual tokenization).
	// srcLang / dstLang are ISO-639-1 codes ("en", "ru", "es", ...).
	Translate(src, srcLang, dstLang string) (string, error)
	Close() error
}

// Result carries metadata alongside the translated text. Backends may
// choose to return Result directly for richer reporting.
type Result struct {
	Text string
}

// ErrUnsupportedLang is returned when a backend cannot map the
// requested source or destination language to its own tag scheme.
var ErrUnsupportedLang = errors.New("mt: unsupported language")

// serial wraps an Engine with a Mutex to enforce sequential decoding.
// CTranslate2 itself is internally threaded but the higher-level
// Translator object is *not* documented as thread-safe across
// translate_batch calls when the model is configured with one replica.
type serial struct {
	mu sync.Mutex
	e  Engine
}

// Serial returns an Engine that serialises calls to the wrapped engine.
// Used by the pipeline so the audio thread cannot overlap MT calls.
func Serial(e Engine) Engine { return &serial{e: e} }

func (s *serial) Translate(src, srcLang, dstLang string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e.Translate(src, srcLang, dstLang)
}

func (s *serial) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.e.Close()
}

// Disabled is a no-op Engine that simply returns its input as the
// translation. The pipeline uses it when MT is switched off in
// settings or when the binary was built without -tags mt and the
// CTranslate2 / SentencePiece dependencies are unavailable.
type Disabled struct{}

// Translate passes the source text through untouched, ignoring the
// requested language pair.
func (Disabled) Translate(src, _, _ string) (string, error) { return src, nil }

// Close is a no-op.
func (Disabled) Close() error { return nil }
