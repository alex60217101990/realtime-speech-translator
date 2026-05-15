// Package mt defines the machine-translation engine abstraction used by
// the rest of the application and implements two concrete backends —
// MADLAD-400 (Apache-2.0, 419 languages, default) and OPUS-MT (per-pair
// Helsinki-NLP, also Apache-2.0, lighter).
//
// Both backends share the CTranslate2 inference runtime (internal/mt/ct2)
// and a SentencePiece tokenizer (internal/mt/sp). They differ in how
// source and target language tags are prepended/encoded — handled per
// backend.
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
